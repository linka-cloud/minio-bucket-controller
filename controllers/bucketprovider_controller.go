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
	"context"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	s3v1alpha1 "go.linka.cloud/minio-bucket-controller/api/v1alpha1"
	mc2 "go.linka.cloud/minio-bucket-controller/pkg/mc"
	"go.linka.cloud/minio-bucket-controller/pkg/recorder"
)

// BucketProviderReconciler reconciles a BucketProvider object
type BucketProviderReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Rec    recorder.Recorder
}

// +kubebuilder:rbac:groups=s3.linka.cloud,resources=bucketproviders,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=s3.linka.cloud,resources=bucketproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=s3.linka.cloud,resources=bucketproviders/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *BucketProviderReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	var p s3v1alpha1.BucketProvider
	if err := r.Get(ctx, req.NamespacedName, &p); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !p.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	if meta.FindStatusCondition(p.Status.Conditions, s3v1alpha1.ProviderConditionError) == nil &&
		meta.FindStatusCondition(p.Status.Conditions, s3v1alpha1.ProviderConditionReady) == nil &&
		!meta.IsStatusConditionTrue(p.Status.Conditions, s3v1alpha1.ProviderConditionCreating) {
		meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
			Type:               s3v1alpha1.ProviderConditionCreating,
			Status:             metav1.ConditionTrue,
			Reason:             "ProviderCreating",
			Message:            "Creating bucket provider",
			ObservedGeneration: p.Generation,
		})
		p.Status.Phase = s3v1alpha1.ProviderConditionCreating
		if err := r.Status().Update(ctx, &p); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	client, err := newClient(ctx, r.Client, p)
	if err != nil {
		log.Error(err, "failed to create minio client")
		meta.RemoveStatusCondition(&p.Status.Conditions, s3v1alpha1.ProviderConditionReady)
		meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
			Type:               s3v1alpha1.ProviderConditionError,
			Status:             metav1.ConditionTrue,
			Reason:             s3v1alpha1.ErrProviderInvalid,
			Message:            err.Error(),
			ObservedGeneration: p.Generation,
		})
		p.Status.Phase = s3v1alpha1.ProviderConditionError
		if err := r.Status().Update(ctx, &p); err != nil {
			return ctrl.Result{}, err
		}
		r.Rec.Warn(&p, s3v1alpha1.ErrProviderInvalid, err.Error())
		return ctrl.Result{}, nil
	}
	if _, err := client.ListBuckets(ctx); err != nil {
		log.Error(err, "failed to list buckets")
		meta.RemoveStatusCondition(&p.Status.Conditions, s3v1alpha1.ProviderConditionReady)
		meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
			Type:               s3v1alpha1.ProviderConditionError,
			Status:             metav1.ConditionTrue,
			Reason:             s3v1alpha1.ErrProviderUnavailable,
			Message:            err.Error(),
			ObservedGeneration: p.Generation,
		})
		p.Status.Phase = s3v1alpha1.ErrProviderUnavailable
		if err := r.Status().Update(ctx, &p); err != nil {
			return ctrl.Result{}, err
		}
		r.Rec.Warn(&p, s3v1alpha1.ErrProviderUnavailable, err.Error())
		return ctrl.Result{}, nil
	}
	if !meta.IsStatusConditionTrue(p.Status.Conditions, s3v1alpha1.ProviderConditionReady) {
		meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
			Type:               s3v1alpha1.ProviderConditionReady,
			Status:             metav1.ConditionTrue,
			Reason:             "ProviderAvailable",
			Message:            "Bucket provider is available",
			ObservedGeneration: p.Generation,
		})
		p.Status.Phase = s3v1alpha1.ProviderConditionReady
		if err := r.Status().Update(ctx, &p); err != nil {
			return ctrl.Result{}, err
		}
		r.Rec.Event(&p, "ProviderAvailable", "Bucket provider available")
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BucketProviderReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&s3v1alpha1.BucketProvider{}).
		Complete(r)
}

func newClient(ctx context.Context, c client.Client, provider s3v1alpha1.BucketProvider) (*mc2.Client, error) {
	accessKey, err := fetchSecretRef(ctx, c, &provider.Spec.AccessKey)
	if err != nil {
		return nil, err
	}
	secretKey, err := fetchSecretRef(ctx, c, &provider.Spec.SecretKey)
	if err != nil {
		return nil, err
	}
	return mc2.New(provider.Spec.Endpoint, accessKey, secretKey, !provider.Spec.Insecure)
}

func defaultProvider(ctx context.Context, c client.Client) (*s3v1alpha1.BucketProvider, bool, error) {
	var list s3v1alpha1.BucketProviderList
	if err := c.List(ctx, &list); err != nil {
		return nil, false, err
	}
	for _, v := range list.Items {
		if _, ok := v.Annotations[s3v1alpha1.DefaultProviderAnnotation]; ok {
			return &v, true, nil
		}
	}
	return nil, false, nil
}
