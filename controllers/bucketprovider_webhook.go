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
	"errors"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"go.linka.cloud/minio-bucket-controller/api/v1alpha1"
)

var _ webhook.CustomValidator = &BucketProviderReconciler{}

// +kubebuilder:webhook:path=/validate-s3-linka-cloud-v1alpha1-bucketprovider,mutating=false,failurePolicy=fail,sideEffects=None,groups=s3.linka.cloud,resources=bucketproviders,verbs=create;update,versions=v1alpha1,name=vbucketprovider.kb.io,admissionReviewVersions=v1

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type
func (r *BucketProviderReconciler) ValidateCreate(ctx context.Context, o runtime.Object) (admission.Warnings, error) {
	p := o.(*v1alpha1.BucketProvider)
	log := ctrl.LoggerFrom(ctx)
	log.Info("validate create", "name", p.Name)
	return r.validate(ctx, p)
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type
func (r *BucketProviderReconciler) ValidateUpdate(ctx context.Context, o runtime.Object, _ runtime.Object) (admission.Warnings, error) {
	p := o.(*v1alpha1.BucketProvider)
	log := ctrl.LoggerFrom(ctx)
	log.Info("validate update", "name", p.Name)

	return r.validate(ctx, p)
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type
func (r *BucketProviderReconciler) ValidateDelete(ctx context.Context, o runtime.Object) (admission.Warnings, error) {
	p := o.(*v1alpha1.BucketProvider)
	log := ctrl.LoggerFrom(ctx)
	log.Info("validate delete", "name", p.Name)
	var l v1alpha1.BucketList
	if err := r.List(ctx, &l, client.MatchingFields{"metadata.name": p.Name}); err != nil {
		return nil, err
	}
	if len(l.Items) > 0 {
		return nil, field.Invalid(field.NewPath("metadata", "name"), p.Name, "cannot delete bucket provider while buckets are still referencing it")
	}
	return nil, nil
}

func (r *BucketProviderReconciler) validate(ctx context.Context, p *v1alpha1.BucketProvider) (admission.Warnings, error) {
	d, ok, err := defaultProvider(ctx, r.Client)
	if err != nil {
		return nil, err
	}
	if ok && d.Name != p.Name {
		return nil, field.Invalid(field.NewPath("metadata", "annotations", v1alpha1.DefaultProviderAnnotation), p.Name, "only one default bucket provider is allowed")
	}
	if p.Spec.Endpoint == "" {
		return nil, field.Invalid(field.NewPath("spec", "endpoint"), p.Spec.Endpoint, "endpoint cannot be empty")
	}
	if strings.HasPrefix(p.Spec.Endpoint, "http://") || strings.HasPrefix(p.Spec.Endpoint, "https://") {
		return nil, field.Invalid(field.NewPath("spec", "endpoint"), p.Spec.Endpoint, "endpoint must not contain http:// or https:// prefix")
	}
	accessKey, err := fetchSecretRef(ctx, r.Client, &p.Spec.AccessKey)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, field.Invalid(field.NewPath("spec", "accessKey", "name"), p.Spec.AccessKey.Name, "secret not found")
		}
		if errors.Is(err, ErrKeyNotFound) {
			return nil, field.Invalid(field.NewPath("spec", "accessKey", "key"), p.Spec.AccessKey.Key, "key not found in secret")
		}
		return nil, errors.New("unable to fetch access key from secret reference")
	}
	if accessKey == "" {
		return nil, field.Invalid(field.NewPath("spec", "accessKey"), p.Spec.AccessKey, "access key cannot be empty")
	}
	secretKey, err := fetchSecretRef(ctx, r.Client, &p.Spec.SecretKey)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, field.Invalid(field.NewPath("spec", "secretKey", "name"), p.Spec.SecretKey.Name, "secret not found")
		}
		if errors.Is(err, ErrKeyNotFound) {
			return nil, field.Invalid(field.NewPath("spec", "secretKey", "key"), p.Spec.SecretKey.Key, "key not found in secret")
		}
		return nil, errors.New("unable to fetch secret key from secret reference")
	}
	if secretKey == "" {
		return nil, field.Invalid(field.NewPath("spec", "secretKey"), p.Spec.SecretKey, "secret key cannot be empty")
	}
	return nil, nil
}

func (r *BucketProviderReconciler) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1alpha1.BucketProvider{}).
		WithValidator(r).
		Complete()
}
