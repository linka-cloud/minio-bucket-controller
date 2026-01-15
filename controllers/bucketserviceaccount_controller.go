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
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/minio/madmin-go"
	"go.uber.org/multierr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	s3v1alpha1 "go.linka.cloud/minio-bucket-controller/api/v1alpha1"
	mc2 "go.linka.cloud/minio-bucket-controller/pkg/mc"
	"go.linka.cloud/minio-bucket-controller/pkg/recorder"
)

const (
	saSecretSuffix = "-sa"
	ownerKey       = ".metadata.controller"
)

// BucketServiceAccountReconciler reconciles a BucketServiceAccount object
type BucketServiceAccountReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Rec            recorder.Recorder
	ServiceAccount string
}

// +kubebuilder:rbac:groups=s3.linka.cloud,resources=bucketserviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=s3.linka.cloud,resources=bucketserviceaccounts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=s3.linka.cloud,resources=bucketserviceaccounts/finalizers,verbs=update

func (r *BucketServiceAccountReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var a s3v1alpha1.BucketServiceAccount
	if err := r.Get(ctx, req.NamespacedName, &a); err != nil {
		if client.IgnoreNotFound(err) != nil {
			log.Error(err, "unable to fetch BucketServiceAccount")
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if !a.DeletionTimestamp.IsZero() {
		if re, ok, err := r.reconcileDeletion(ctx, &a); !ok {
			return re, err
		}
		controllerutil.RemoveFinalizer(&a, finalizer)
		if err := r.Update(ctx, &a); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		return ctrl.Result{}, nil
	}

	if controllerutil.AddFinalizer(&a, finalizer) {
		if err := r.Update(ctx, &a); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if len(a.Status.Conditions) == 0 {
		meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
			Type:               s3v1alpha1.BucketSAConditionCreating,
			Status:             metav1.ConditionTrue,
			Reason:             "Creating",
			Message:            "BucketServiceAccount is being created",
			ObservedGeneration: a.Generation,
		})
		if err := r.Status().Update(ctx, &a); err != nil {
			return ctrl.Result{}, err
		}
	}

	var p s3v1alpha1.BucketProvider
	if err := r.Get(ctx, types.NamespacedName{Name: a.Spec.Provider}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.err(&a, fmt.Errorf("referenced bucket provider %s not found", a.Spec.Provider), s3v1alpha1.ErrProviderInvalid)
		}
		return ctrl.Result{}, err
	}
	mc, err := newClient(ctx, r.Client, p)
	if err != nil {
		return ctrl.Result{}, r.err(&a, fmt.Errorf("unable to create minio client: %w", err), s3v1alpha1.ErrProviderInvalid)
	}

	if re, ok, err := r.reconcileUser(ctx, mc, &a); !ok {
		return re, r.err(&a, err, s3v1alpha1.BucketSAConditionReasonErrCreateUser)
	}

	if re, ok, err := r.reconcileServiceAccount(ctx, mc, &a); !ok {
		return re, r.err(&a, err, s3v1alpha1.BucketSAConditionReasonErrCreateAccount)
	}

	if re, ok, err := r.reconcilePolicies(ctx, mc, &a); !ok {
		return re, r.err(&a, err, s3v1alpha1.BucketSAConditionReasonErrCreatePolicy)
	}

	if !meta.IsStatusConditionTrue(a.Status.Conditions, s3v1alpha1.BucketSAConditionReady) &&
		!meta.IsStatusConditionTrue(a.Status.Conditions, s3v1alpha1.BucketSAConditionDeletionPending) {
		meta.RemoveStatusCondition(&a.Status.Conditions, s3v1alpha1.BucketSAConditionCreating)
		meta.RemoveStatusCondition(&a.Status.Conditions, s3v1alpha1.BucketSAConditionError)
		meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
			Type:               s3v1alpha1.BucketSAConditionReady,
			Status:             metav1.ConditionTrue,
			Reason:             s3v1alpha1.BucketSAConditionReady,
			Message:            "BucketServiceAccount is ready",
			ObservedGeneration: a.Generation,
		})
		a.Status.Phase = s3v1alpha1.BucketSAConditionReady
		if err := r.Status().Update(ctx, &a); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func (r *BucketServiceAccountReconciler) reconcileUser(ctx context.Context, mc *mc2.Client, a *s3v1alpha1.BucketServiceAccount) (ctrl.Result, bool, error) {
	name := name(a.Name)
	log := log.FromContext(ctx).WithValues("user", name)
	log.Info("reconciling user")
	us, err := mc.ListUsers(ctx)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to list users: %w", err)
	}
	if _, ok := us[name]; ok {
		return ctrl.Result{}, true, nil
	}
	log.Info("creating user")
	if err := mc.AddUser(ctx, name, mc2.GeneratePassword()); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to create user: %w", err)
	}
	r.Rec.Eventf(a, "UserCreated", "User %s created", name)
	return ctrl.Result{}, true, nil
}

func (r *BucketServiceAccountReconciler) reconcileServiceAccount(ctx context.Context, mc *mc2.Client, a *s3v1alpha1.BucketServiceAccount) (ctrl.Result, bool, error) {
	log := log.FromContext(ctx)
	log.Info("reconciling service account")
	name := name(a.Name)
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      a.Name + saSecretSuffix,
			Namespace: a.Namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by":      "minio-bucket-controller",
				s3v1alpha1.ServiceAccountAnnotation: a.Name,
			},
		},
		Type: s3v1alpha1.BucketSASecretType,
	}
	if err := r.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, false, err
		}
	} else {
		return ctrl.Result{}, true, nil
	}
	res, err := mc.ListServiceAccounts(ctx, name)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to list service accounts: %w", err)
	}
	if len(res.Accounts) != 0 {
		log.Info("service account already exists")
		for _, v := range res.Accounts {
			log.Info("deleting service account %s", v)
			if err := mc.DeleteServiceAccount(ctx, v); err != nil {
				return ctrl.Result{}, false, fmt.Errorf("unable to remove service account: %w", err)
			}
		}
	}
	log.Info("creating service account")
	creds, err := mc.AddServiceAccount(ctx, madmin.AddServiceAccountReq{TargetUser: name})
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to create service account: %w", err)
	}
	s.Data = map[string][]byte{
		s3v1alpha1.MinioAccessKey: []byte(creds.AccessKey),
		s3v1alpha1.MinioSecretKey: []byte(creds.SecretKey),
	}
	if err := ctrl.SetControllerReference(a, s, r.Scheme); err != nil {
		return ctrl.Result{}, false, err
	}
	log.Info("creating secret")
	if err := r.Create(ctx, s); err != nil {
		return ctrl.Result{}, false, multierr.Combine(fmt.Errorf("unable to create secret: %w", err), mc.DeleteServiceAccount(ctx, name))
	}
	s.GetObjectMeta()
	a.Status.SecretName = &s.Name
	log.Info("updating bucket status")
	if err := r.Status().Update(ctx, a); err != nil {
		return ctrl.Result{}, false, multierr.Combine(fmt.Errorf("unable to update bucket status: %w", err), r.Delete(ctx, s), mc.DeleteServiceAccount(ctx, name))
	}
	r.Rec.Eventf(a, "SecretCreated", "Secret %s created", s.Name)
	return ctrl.Result{}, false, nil
}

func (r *BucketServiceAccountReconciler) reconcilePolicies(ctx context.Context, mc *mc2.Client, a *s3v1alpha1.BucketServiceAccount) (ctrl.Result, bool, error) {
	log := log.FromContext(ctx)
	var buckets s3v1alpha1.BucketList
	if err := r.List(ctx, &buckets, client.MatchingFields{ownerKey: a.Name}); err != nil {
		return ctrl.Result{}, false, err
	}
	if len(buckets.Items) == 0 {
		if !meta.IsStatusConditionTrue(a.Status.Conditions, s3v1alpha1.BucketSAConditionDeletionPending) {
			meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
				Type:               s3v1alpha1.BucketSAConditionDeletionPending,
				Status:             metav1.ConditionTrue,
				Reason:             "DanglingServiceAccount",
				Message:            "Service account is not used by any bucket and will be deleted in 1 minute",
				ObservedGeneration: a.Generation,
			})
			a.Status.Phase = s3v1alpha1.BucketSAConditionDeletionPending
			if err := r.Status().Update(ctx, a); err != nil {
				return ctrl.Result{}, false, err
			}
			return ctrl.Result{}, false, nil
		}
	} else {
		if meta.IsStatusConditionTrue(a.Status.Conditions, s3v1alpha1.BucketSAConditionDeletionPending) {
			meta.RemoveStatusCondition(&a.Status.Conditions, s3v1alpha1.BucketSAConditionDeletionPending)
			if err := r.Status().Update(ctx, a); err != nil {
				return ctrl.Result{}, false, err
			}
		}
	}
	var policies []string
	for _, b := range buckets.Items {
		if re, ok, err := r.reconcilePolicy(ctx, mc, &b); !ok {
			return re, false, err
		}
		policies = append(policies, name(b.Name))
	}
	want := strings.Join(policies, ",")
	// u, err := mc.GetUserInfo(ctx, name(a.Name))
	// if err != nil {
	// 	return ctrl.Result{}, false, fmt.Errorf("unable to get user info: %w", err)
	// }
	// if u.PolicyName == want {
	// 	return ctrl.Result{}, true, nil
	// }
	log.Info("assigning policy")
	if err := mc.SetPolicy(ctx, want, name(a.Name), false); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to set policy: %w", err)
	}
	r.Rec.Eventf(a, "PolicyConfigured", "Policy %s configured", name(a.Name))
	return ctrl.Result{}, true, nil
}

func (r *BucketServiceAccountReconciler) reconcilePolicy(ctx context.Context, mc *mc2.Client, bucket *s3v1alpha1.Bucket) (ctrl.Result, bool, error) {
	name := name(bucket.Name)
	log := ctrl.LoggerFrom(ctx).WithValues("policy", name)
	log.Info("reconciling policy")
	want := mc2.Policy(bucket.Name)
	ps, err := mc.ListCannedPolicies(ctx)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to list policies: %w", err)
	}
	if js, ok := ps[name]; ok {
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
	if err := mc.AddCannedPolicy(ctx, name, want); err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to add policy: %w", err)
	}
	return ctrl.Result{}, true, nil
}

func (r *BucketServiceAccountReconciler) reconcileDeletion(ctx context.Context, a *s3v1alpha1.BucketServiceAccount) (ctrl.Result, bool, error) {
	log := log.FromContext(ctx)

	log.Info("reconcile delete")
	if !meta.IsStatusConditionTrue(a.Status.Conditions, s3v1alpha1.BucketSAConditionDeleting) {
		meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
			Type:               s3v1alpha1.BucketSAConditionDeleting,
			Status:             metav1.ConditionTrue,
			Reason:             "BucketServiceAccountDeletion",
			Message:            "BucketServiceAccount is being deleted",
			ObservedGeneration: a.Generation,
		})
		a.Status.Phase = s3v1alpha1.BucketConditionDeleting
		if err := r.Status().Update(ctx, a); err != nil {
			return ctrl.Result{}, false, err
		}
		return ctrl.Result{}, false, nil
	}

	var p s3v1alpha1.BucketProvider
	if err := r.Get(ctx, types.NamespacedName{Name: a.Spec.Provider}, &p); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("bucket provider not found, skipping account deletion")
			return ctrl.Result{}, true, nil
		}
		return ctrl.Result{}, false, fmt.Errorf("unable to get bucket provider: %w", err)
	}
	mc, err := newClient(ctx, r.Client, p)
	if err != nil {
		return ctrl.Result{}, false, fmt.Errorf("unable to create minio client: %w", err)
	}

	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      a.Name + saSecretSuffix,
			Namespace: a.Namespace,
		},
	}
	if err := r.Delete(ctx, s); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return ctrl.Result{}, false, err
		}
	}

	if err := mc.RemoveUser(ctx, name(a.Name)); err != nil {
		if madmin.ToErrorResponse(err).Code == "XMinioAdminNoSuchUser" {
			return ctrl.Result{}, true, nil
		}
		return ctrl.Result{}, false, err
	}
	r.Rec.Eventf(a, "UserDeleted", "User %s deleted", a.Name)
	return ctrl.Result{}, true, nil
}

func (r *BucketServiceAccountReconciler) err(a *s3v1alpha1.BucketServiceAccount, err error, reason string) error {
	if err == nil {
		return nil
	}
	meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
		Type:               s3v1alpha1.BucketSAConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            "Service account is not ready",
		ObservedGeneration: a.Generation,
	})
	meta.SetStatusCondition(&a.Status.Conditions, metav1.Condition{
		Type:               s3v1alpha1.BucketSAConditionError,
		Status:             metav1.ConditionTrue,
		Reason:             reason,
		Message:            err.Error(),
		ObservedGeneration: a.Generation,
		LastTransitionTime: metav1.Now(),
	})
	a.Status.Phase = s3v1alpha1.BucketConditionError
	if err2 := r.Status().Update(context.Background(), a); err2 != nil {
		return multierr.Combine(err, err2)
	}
	r.Rec.Warn(a, "BucketError", err.Error())
	return err
}

func (r *BucketServiceAccountReconciler) gc(ctx context.Context, freq, ttl time.Duration) {
	log := log.FromContext(ctx)
	fn := func() {
		log.V(5).Info("running BucketServiceAccounts garbage collection")
		var list s3v1alpha1.BucketServiceAccountList
		if err := r.List(ctx, &list); err != nil {
			log.Error(err, "unable to list BucketServiceAccounts")
			return
		}
	iter:
		for _, v := range list.Items {
			log := log.WithValues("namespace", v.Namespace, "name", v.Name)
			var found bool
			for _, v := range v.Status.Conditions {
				found = v.Type == s3v1alpha1.BucketSAConditionDeletionPending || v.Type == s3v1alpha1.BucketSAConditionDeleting
				if found && v.LastTransitionTime.Add(ttl).After(time.Now()) {
					continue iter
				}
			}
			if !found {
				continue
			}
			log.Info("garbage collection: deleting BucketServiceAccount")
			if !meta.IsStatusConditionTrue(v.Status.Conditions, s3v1alpha1.BucketSAConditionDeleting) {
				meta.SetStatusCondition(&v.Status.Conditions, metav1.Condition{
					Type:               s3v1alpha1.BucketSAConditionDeleting,
					Status:             metav1.ConditionTrue,
					Reason:             "BucketServiceAccountDeletion",
					Message:            "BucketServiceAccount is being deleted",
					ObservedGeneration: v.Generation,
				})
				v.Status.Phase = s3v1alpha1.BucketConditionDeleting
				if err := r.Status().Update(ctx, &v); err != nil {
					log.Error(err, "unable to update BucketServiceAccount")
					continue
				}
			}
			if err := r.Delete(ctx, &v); err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "unable to delete BucketServiceAccount")
			}
		}
	}
	tk := time.NewTicker(freq)
	defer tk.Stop()
	for {
		select {
		case <-tk.C:
			fn()
		case <-ctx.Done():
			return
		}
	}
}

var gvk = s3v1alpha1.GroupVersion.String()

// SetupWithManager sets up the controller with the Manager.
func (r *BucketServiceAccountReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(ctx, &s3v1alpha1.Bucket{}, ownerKey, func(rawObj client.Object) []string {
		job := rawObj.(*s3v1alpha1.Bucket)
		owner := metav1.GetControllerOf(job)
		if owner == nil {
			return nil
		}
		if owner.APIVersion != gvk || owner.Kind != "BucketServiceAccount" {
			return nil
		}
		return []string{owner.Name}
	}); err != nil {
		return err
	}
	go r.gc(ctx, 5*time.Second, time.Minute)
	return ctrl.NewControllerManagedBy(mgr).
		For(&s3v1alpha1.BucketServiceAccount{}).
		Owns(&s3v1alpha1.Bucket{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}
