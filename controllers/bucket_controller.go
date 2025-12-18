// Copyright 2023 Linka Cloud
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controllers

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"text/template"
	"time"

	"github.com/minio/minio-go/v7"
	"go.uber.org/multierr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	s3v1alpha1 "go.linka.cloud/minio-bucket-controller/api/v1alpha1"
	"go.linka.cloud/minio-bucket-controller/pkg/mc"
	"go.linka.cloud/minio-bucket-controller/pkg/recorder"
)

const (
	finalizer = "bucket.s3.linka.cloud/finalizer"
	prefix    = "bucket.s3.linka.cloud/"
)

// BucketReconciler reconciles a Bucket object
type BucketReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	MC     *mc.Client
	Rec    recorder.Recorder
}

// +kubebuilder:rbac:groups=s3.linka.cloud,resources=buckets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=s3.linka.cloud,resources=buckets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=s3.linka.cloud,resources=buckets/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *BucketReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var bucket s3v1alpha1.Bucket
	if err := r.Get(ctx, req.NamespacedName, &bucket); err != nil {
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "unable to fetch Bucket")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if !bucket.DeletionTimestamp.IsZero() {
		if re, ok, err := r.reconcileDeletion(ctx, &bucket); !ok {
			return re, err
		}
		controllerutil.RemoveFinalizer(&bucket, finalizer)
		if err := r.Update(ctx, &bucket); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	if controllerutil.AddFinalizer(&bucket, finalizer) {
		if err := r.Update(ctx, &bucket); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if bucket.Spec.SecretName == nil {
		n := fmt.Sprintf("%s-bucket-credentials", bucket.Name)
		bucket.Spec.SecretName = &n
		if err := r.Update(ctx, &bucket); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if bucket.Spec.ReclaimPolicy == "" {
		bucket.Spec.ReclaimPolicy = s3v1alpha1.BucketReclaimRetain
		if err := r.Update(ctx, &bucket); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if len(bucket.Status.Conditions) == 0 {
		meta.SetStatusCondition(&bucket.Status.Conditions, metav1.Condition{
			Type:               s3v1alpha1.BucketConditionCreating,
			Status:             metav1.ConditionTrue,
			Reason:             "Creating",
			Message:            "Bucket is being created",
			ObservedGeneration: bucket.Generation,
		})
		bucket.Status.Phase = s3v1alpha1.BucketConditionCreating
		if err := r.Status().Update(ctx, &bucket); err != nil {
			return ctrl.Result{}, err
		}
		r.Rec.Eventf(&bucket, "BucketCreating", "Bucket %s is being created", bucket.Name)
		return ctrl.Result{}, nil
	}
	if re, ok, err := r.reconcileServiceAccount(ctx, &bucket); !ok {
		return re, r.err(&bucket, err, s3v1alpha1.BucketConditionReasonErrCreateServiceAccount)
	}
	if re, ok, err := r.reconcileBucket(ctx, &bucket); !ok {
		return re, r.err(&bucket, err, s3v1alpha1.BucketConditionReasonErrCreateBucket)
	}
	if re, ok, err := r.reconcileSecret(ctx, &bucket); !ok {
		return re, r.err(&bucket, err, s3v1alpha1.BucketConditionReasonErrCreateSecret)
	}
	if !meta.IsStatusConditionTrue(bucket.Status.Conditions, s3v1alpha1.BucketConditionReady) {
		meta.RemoveStatusCondition(&bucket.Status.Conditions, s3v1alpha1.BucketConditionCreating)
		meta.RemoveStatusCondition(&bucket.Status.Conditions, s3v1alpha1.BucketConditionError)
		meta.SetStatusCondition(&bucket.Status.Conditions, metav1.Condition{
			Type:               s3v1alpha1.BucketConditionReady,
			Status:             metav1.ConditionTrue,
			Reason:             "Ready",
			Message:            "Bucket is ready",
			ObservedGeneration: bucket.Generation,
		})
		bucket.Status.Phase = s3v1alpha1.BucketConditionReady
		if err := r.Status().Update(ctx, &bucket); err != nil {
			return ctrl.Result{}, err
		}
		r.Rec.Eventf(&bucket, "BucketReady", "Bucket %s is ready", bucket.Name)
	}
	return ctrl.Result{}, nil
}

func (r *BucketReconciler) reconcileBucket(ctx context.Context, bucket *s3v1alpha1.Bucket) (ctrl.Result, bool, error) {
	log := log.FromContext(ctx)
	log.Info("reconciling bucket")
	ok, err := r.MC.BucketExists(ctx, bucket.Name)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to check if bucket exists: %w", err)
	}
	if ok {
		if bucket.Status.Endpoint == nil {
			r.Rec.Warnf(bucket, "BucketExists", "bucket %s already exists", bucket.Name)
			return ctrl.Result{}, false, fmt.Errorf("conflict: bucket already exists")
		}
		return ctrl.Result{}, true, nil
	}
	log.Info("creating bucket")
	if err := r.MC.MakeBucket(ctx, bucket.Name, minio.MakeBucketOptions{}); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to create bucket: %w", err)
	}
	e := r.MC.Endpoint()
	bucket.Status.Endpoint = &e
	log.Info("updating bucket endpoint status")
	if err := r.Status().Update(ctx, bucket); err != nil {
		return ctrl.Result{}, false, multierr.Combine(fmt.Errorf("unable to update bucket status: %w", err), r.MC.RemoveBucket(ctx, bucket.Name))
	}
	r.Rec.Eventf(bucket, "BucketCreated", "Bucket %s created", bucket.Name)
	return ctrl.Result{}, false, nil
}

func (r *BucketReconciler) reconcileServiceAccount(ctx context.Context, bucket *s3v1alpha1.Bucket) (ctrl.Result, bool, error) {
	log := log.FromContext(ctx)
	log.Info("reconciling service account")
	var sa s3v1alpha1.BucketServiceAccount
	if err := r.Get(ctx, types.NamespacedName{Name: bucket.Spec.ServiceAccount, Namespace: bucket.Namespace}, &sa); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, false, fmt.Errorf("unable to get service account: %w", err)
		}
		sa = s3v1alpha1.BucketServiceAccount{
			ObjectMeta: metav1.ObjectMeta{
				Name:      bucket.Spec.ServiceAccount,
				Namespace: bucket.Namespace,
			},
		}
		if err := r.Create(ctx, &sa); err != nil {
			return ctrl.Result{}, false, fmt.Errorf("unable to create service account: %w", err)
		}
	}
	b := bucket.DeepCopy()
	if err := ForceControllerReference(&sa, b, r.Scheme); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to set controller reference: %v", err)
	}
	if equality.Semantic.DeepEqual(b, bucket) {
		return ctrl.Result{}, true, nil
	}
	if err := r.Update(ctx, b); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to update bucket: %w", err)
	}
	return ctrl.Result{}, false, nil
}

func (r *BucketReconciler) reconcileSecret(ctx context.Context, bucket *s3v1alpha1.Bucket) (ctrl.Result, bool, error) {
	log := log.FromContext(ctx)
	log.Info("reconciling secret")
	name := name(bucket.Name)
	if bucket.Status.SecretName != nil && *bucket.Spec.SecretName != *bucket.Status.SecretName {
		log.Info("deleting old secret")
		if err := r.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: *bucket.Status.SecretName, Namespace: bucket.Namespace}}); err != nil {
			if client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, false, fmt.Errorf("unable to delete old secret: %w", err)
			}
		} else {
			r.Rec.Eventf(bucket, "SecretDeleted", "Secret %s deleted", *bucket.Status.SecretName)
		}
	}
	var (
		secret corev1.Secret
		update bool
	)
	if err := r.Get(ctx, types.NamespacedName{Name: *bucket.Spec.SecretName, Namespace: bucket.Namespace}, &secret); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, false, fmt.Errorf("unable to get secret: %w", err)
		}
		secret = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      *bucket.Spec.SecretName,
				Namespace: bucket.Namespace,
			},
		}
	} else {
		update = true
	}
	s2 := secret.DeepCopy()
	s2.Type = s3v1alpha1.BucketAccessSecretType
	s2.Labels = bucket.Labels
	var as corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: bucket.Spec.ServiceAccount + saSecretSuffix, Namespace: bucket.Namespace}, &as); err != nil {
		log.Error(err, "failed to get service account secret")
		return ctrl.Result{RequeueAfter: time.Second}, false, client.IgnoreNotFound(err)
	}
	a := s3v1alpha1.BucketAccess{
		Bucket:    bucket.Name,
		AccessKey: string(as.Data[s3v1alpha1.MinioAccessKey]),
		SecretKey: string(as.Data[s3v1alpha1.MinioSecretKey]),
		Endpoint:  r.MC.Endpoint(),
		Secure:    r.MC.Secure(),
	}
	s2.Data = map[string][]byte{
		s3v1alpha1.MinioBucket:    []byte(bucket.Name),
		s3v1alpha1.MinioAccessKey: as.Data[s3v1alpha1.MinioAccessKey],
		s3v1alpha1.MinioSecretKey: as.Data[s3v1alpha1.MinioSecretKey],
		s3v1alpha1.MinioEndpoint:  []byte(r.MC.Endpoint()),
		s3v1alpha1.MinioSecure:    []byte(strconv.FormatBool(r.MC.Secure())),
	}
	for k, v := range bucket.Spec.SecretTemplate {
		tmp, err := template.New("secret").Parse(v)
		if err != nil {
			return ctrl.Result{}, false, fmt.Errorf("%s: unable to parse secret template: %w", k, err)
		}
		var buf bytes.Buffer
		if err := tmp.Execute(&buf, a); err != nil {
			return ctrl.Result{}, false, fmt.Errorf("%s: unable to execute secret template: %w", k, err)
		}
		s2.Data[k] = buf.Bytes()
	}
	if err := ctrl.SetControllerReference(bucket, s2, r.Scheme); err != nil {
		return ctrl.Result{}, false, multierr.Combine(fmt.Errorf("unable to set controller reference: %w", err), r.MC.DeleteServiceAccount(ctx, name))
	}
	if equality.Semantic.DeepEqual(secret, s2) {
		return ctrl.Result{}, true, nil
	}
	if update {
		log.Info("updating secret")
		if err := r.Update(ctx, s2); err != nil {
			return ctrl.Result{}, false, fmt.Errorf("unable to update secret: %w", err)
		}
		r.Rec.Eventf(bucket, "SecretUpdated", "Secret %s updated", secret.Name)
		return ctrl.Result{}, true, nil
	}
	log.Info("creating secret")
	if err := r.Create(ctx, s2); err != nil {
		return ctrl.Result{}, false, multierr.Combine(fmt.Errorf("unable to create secret: %w", err), r.MC.DeleteServiceAccount(ctx, name))
	}
	bucket.Status.SecretName = &s2.Name
	log.Info("updating bucket status")
	if err := r.Status().Update(ctx, bucket); err != nil {
		return ctrl.Result{}, false, multierr.Combine(fmt.Errorf("unable to update bucket status: %w", err), r.Delete(ctx, &secret), r.MC.DeleteServiceAccount(ctx, name))
	}
	r.Rec.Eventf(bucket, "SecretCreated", "Secret %s created", secret.Name)
	return ctrl.Result{}, true, nil
}

func (r *BucketReconciler) reconcileDeletion(ctx context.Context, bucket *s3v1alpha1.Bucket) (ctrl.Result, bool, error) {
	log := log.FromContext(ctx)
	log.Info("reconciling deletion")
	if !meta.IsStatusConditionTrue(bucket.Status.Conditions, s3v1alpha1.BucketConditionDeleting) {
		meta.SetStatusCondition(&bucket.Status.Conditions, metav1.Condition{
			Type:               s3v1alpha1.BucketConditionDeleting,
			Status:             metav1.ConditionTrue,
			Reason:             "Deleting",
			Message:            "Bucket is being deleted",
			ObservedGeneration: bucket.Generation,
		})
		bucket.Status.Phase = s3v1alpha1.BucketConditionDeleting
		if err := r.Status().Update(ctx, bucket); err != nil {
			return ctrl.Result{}, false, fmt.Errorf("unable to update bucket status: %w", err)
		}
		r.Rec.Eventf(bucket, "BucketDeleting", "Bucket %s is being deleted", bucket.Name)
		return ctrl.Result{}, false, nil
	}
	if bucket.Spec.ReclaimPolicy != s3v1alpha1.BucketReclaimDelete {
		return ctrl.Result{}, true, nil
	}
	log.Info("deleting bucket")
	if err := r.MC.RemoveBucketWithOptions(ctx, bucket.Name, minio.RemoveBucketOptions{ForceDelete: true}); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to remove bucket: %w", err)
	}
	r.Rec.Eventf(bucket, "BucketDeleted", "Bucket %s deleted", bucket.Name)
	return ctrl.Result{}, true, nil
}

func (r *BucketReconciler) err(bucket *s3v1alpha1.Bucket, err error, reason string) error {
	if err == nil {
		return nil
	}
	meta.SetStatusCondition(&bucket.Status.Conditions, metav1.Condition{
		Type:               s3v1alpha1.BucketConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             "Ready",
		Message:            "Bucket is not ready",
		ObservedGeneration: bucket.Generation,
	})
	meta.SetStatusCondition(&bucket.Status.Conditions, metav1.Condition{
		Type:               s3v1alpha1.BucketConditionError,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            err.Error(),
		ObservedGeneration: bucket.Generation,
		LastTransitionTime: metav1.Now(),
	})
	bucket.Status.Phase = s3v1alpha1.BucketConditionError
	if err2 := r.Status().Update(context.Background(), bucket); err2 != nil {
		return multierr.Combine(err, err2)
	}
	r.Rec.Warn(bucket, "BucketError", err.Error())
	return err
}

// SetupWithManager sets up the controller with the Manager.
func (r *BucketReconciler) SetupWithManager(_ context.Context, mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&s3v1alpha1.Bucket{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

func name(n string) string {
	return prefix + n
}
