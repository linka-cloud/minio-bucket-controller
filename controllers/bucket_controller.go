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

	"github.com/minio/madmin-go"
	"github.com/minio/minio-go/v7"
	"go.uber.org/multierr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/json"
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
	if re, ok, err := r.reconcileBucket(ctx, &bucket); !ok {
		return re, err
	}
	if re, ok, err := r.reconcileUser(ctx, &bucket); !ok {
		return re, err
	}
	if re, ok, err := r.reconcilePolicy(ctx, &bucket); !ok {
		return re, err
	}
	if re, ok, err := r.reconcileSecret(ctx, &bucket); !ok {
		return re, err
	}
	if re, ok, err := r.reconcileSecretTemplate(ctx, &bucket); !ok {
		return re, err
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

func (r *BucketReconciler) reconcileUser(ctx context.Context, bucket *s3v1alpha1.Bucket) (ctrl.Result, bool, error) {
	name := r.name(bucket)
	log := log.FromContext(ctx).WithValues("user", name)
	log.Info("reconciling user")
	us, err := r.MC.ListUsers(ctx)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to list users: %w", err)
	}
	if _, ok := us[name]; ok {
		return ctrl.Result{}, true, nil
	}
	log.Info("creating user")
	if err := r.MC.AddUser(ctx, name, mc.GeneratePassword()); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to create user: %w", err)
	}
	r.Rec.Eventf(bucket, "UserCreated", "User %s created", name)
	return ctrl.Result{}, true, nil
}

func (r *BucketReconciler) reconcilePolicy(ctx context.Context, bucket *s3v1alpha1.Bucket) (ctrl.Result, bool, error) {
	name := r.name(bucket)
	log := ctrl.LoggerFrom(ctx).WithValues("policy", name)
	log.Info("reconciling policy")
	want := mc.Policy(bucket.Name)
	ps, err := r.MC.ListCannedPolicies(ctx)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to list policies: %w", err)
	}
	if js, ok := ps[r.name(bucket)]; ok {
		log.Info("policy exists")
		got, err := json.Marshal(js)
		if err != nil {
			return ctrl.Result{}, false, fmt.Errorf("unable to marshal policy: %w", err)
		}
		if bytes.Equal(got, want) {
			return ctrl.Result{}, true, nil
		}
		log.Info("policy differs: updating")
	} else {
		log.Info("creating policy")
	}
	if err := r.MC.AddCannedPolicy(ctx, name, want); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to add policy: %w", err)
	}
	log.Info("assigning policy")
	if err := r.MC.SetPolicy(ctx, name, name, false); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to set policy: %w", err)
	}
	r.Rec.Eventf(bucket, "PolicyConfigured", "Policy %s configured", name)
	return ctrl.Result{}, true, nil
}

func (r *BucketReconciler) reconcileSecret(ctx context.Context, bucket *s3v1alpha1.Bucket) (ctrl.Result, bool, error) {
	log := log.FromContext(ctx)
	log.Info("reconciling secret")
	name := r.name(bucket)
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
	var secret corev1.Secret
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
		return ctrl.Result{}, true, nil
	}
	res, err := r.MC.ListServiceAccounts(ctx, name)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to list service accounts: %w", err)
	}
	if len(res.Accounts) != 0 {
		log.Info("service account already exists for bucket")
		for _, v := range res.Accounts {
			log.Info("deleting service account %s", v)
			if err := r.MC.DeleteServiceAccount(ctx, v); err != nil {
				return ctrl.Result{}, false, fmt.Errorf("unable to remove service account: %w", err)
			}
		}
	}
	log.Info("creating service account")
	creds, err := r.MC.AddServiceAccount(ctx, madmin.AddServiceAccountReq{TargetUser: name})
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to create service account: %w", err)
	}
	secret.Data = map[string][]byte{
		s3v1alpha1.MinioBucket:    []byte(bucket.Name),
		s3v1alpha1.MinioAccessKey: []byte(creds.AccessKey),
		s3v1alpha1.MinioSecretKey: []byte(creds.SecretKey),
		s3v1alpha1.MinioEndpoint:  []byte(r.MC.Endpoint()),
		s3v1alpha1.MinioSecure:    []byte(strconv.FormatBool(r.MC.Secure())),
	}
	if err := ctrl.SetControllerReference(bucket, &secret, r.Scheme); err != nil {
		return ctrl.Result{}, false, multierr.Combine(fmt.Errorf("unable to set controller reference: %w", err), r.MC.DeleteServiceAccount(ctx, name))
	}
	log.Info("creating secret")
	if err := r.Create(ctx, &secret); err != nil {
		return ctrl.Result{}, false, multierr.Combine(fmt.Errorf("unable to create secret: %w", err), r.MC.DeleteServiceAccount(ctx, name))
	}
	bucket.Status.SecretName = &secret.Name
	log.Info("updating bucket status")
	if err := r.Status().Update(ctx, bucket); err != nil {
		return ctrl.Result{}, false, multierr.Combine(fmt.Errorf("unable to update bucket status: %w", err), r.Delete(ctx, &secret), r.MC.DeleteServiceAccount(ctx, name))
	}
	r.Rec.Eventf(bucket, "SecretCreated", "Secret %s created", secret.Name)
	return ctrl.Result{}, false, nil
}

func (r *BucketReconciler) reconcileSecretTemplate(ctx context.Context, bucket *s3v1alpha1.Bucket) (ctrl.Result, bool, error) {
	log := log.FromContext(ctx)
	log.Info("reconciling secret template")

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: *bucket.Spec.SecretName, Namespace: bucket.Namespace}, &secret); err != nil {
		// should not happen
		return ctrl.Result{}, false, err
	}

	a := s3v1alpha1.BucketAccess{
		Endpoint:  r.MC.Endpoint(),
		Secure:    r.MC.Secure(),
		AccessKey: string(secret.Data[s3v1alpha1.MinioAccessKey]),
		SecretKey: string(secret.Data[s3v1alpha1.MinioSecretKey]),
		Bucket:    bucket.Name,
	}

	s2 := secret.DeepCopy()
	s2.Data = map[string][]byte{
		s3v1alpha1.MinioBucket:    []byte(bucket.Name),
		s3v1alpha1.MinioAccessKey: []byte(a.AccessKey),
		s3v1alpha1.MinioSecretKey: []byte(a.SecretKey),
		s3v1alpha1.MinioEndpoint:  []byte(a.Endpoint),
		s3v1alpha1.MinioSecure:    []byte(strconv.FormatBool(a.Secure)),
	}

	for _, v := range []string{s3v1alpha1.MinioAccessKey, s3v1alpha1.MinioSecretKey, s3v1alpha1.MinioEndpoint, s3v1alpha1.MinioBucket, s3v1alpha1.MinioSecure} {
		if _, ok := bucket.Spec.SecretTemplate[v]; ok {
			return ctrl.Result{}, false, fmt.Errorf("secret template must not contain %s", v)
		}
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
	if equality.Semantic.DeepEqual(secret.Data, s2.Data) {
		return ctrl.Result{}, true, nil
	}
	if err := r.Update(ctx, s2); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to update secret: %w", err)
	}
	return ctrl.Result{}, false, nil
}

func (r *BucketReconciler) reconcileDeletion(ctx context.Context, bucket *s3v1alpha1.Bucket) (ctrl.Result, bool, error) {
	log := log.FromContext(ctx)
	log.Info("reconciling deletion")
	name := r.name(bucket)
	us, err := r.MC.ListUsers(ctx)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to list users: %w", err)
	}
	if _, ok := us[name]; ok {
		log.Info("deleting user", "user", name)
		if err := r.MC.RemoveUser(ctx, name); err != nil {
			return ctrl.Result{}, false, fmt.Errorf("unable to remove user: %w", err)
		}
		r.Rec.Eventf(bucket, "UserDeleted", "User %s deleted", name)
	}
	ps, err := r.MC.ListCannedPolicies(ctx)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to list policies: %w", err)
	}
	if _, ok := ps[name]; ok {
		log.Info("deleting policy", "policy", name)
		if err := r.MC.RemoveCannedPolicy(ctx, name); err != nil {
			return ctrl.Result{}, false, fmt.Errorf("unable to remove policy: %w", err)
		}
		r.Rec.Eventf(bucket, "PolicyDeleted", "Policy %s deleted", name)
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

// SetupWithManager sets up the controller with the Manager.
func (r *BucketReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&s3v1alpha1.Bucket{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

func (r *BucketReconciler) name(b *s3v1alpha1.Bucket) string {
	return prefix + b.Name
}
