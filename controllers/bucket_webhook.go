package controllers

import (
	"bytes"
	"context"
	"fmt"
	"text/template"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	s3v1alpha1 "go.linka.cloud/minio-bucket-controller/api/v1alpha1"
)

var (
	_ webhook.CustomDefaulter = (*BucketReconciler)(nil)
	_ webhook.CustomValidator = (*BucketReconciler)(nil)
)

func (r *BucketReconciler) Default(ctx context.Context, obj runtime.Object) error {
	bucket, ok := obj.(*s3v1alpha1.Bucket)
	if !ok {
		return fmt.Errorf("expected a Bucket but got a %T", obj)
	}
	if bucket.Spec.ReclaimPolicy == "" {
		bucket.Spec.ReclaimPolicy = s3v1alpha1.BucketReclaimRetain
	}
	if bucket.Spec.SecretName == nil || *bucket.Spec.SecretName == "" {
		n := fmt.Sprintf("%s-bucket-credentials", bucket.Name)
		bucket.Spec.SecretName = &n
	}
	if bucket.Spec.ServiceAccount == "" {
		bucket.Spec.ServiceAccount = bucket.Name
	}
	return nil
}

func (r *BucketReconciler) ValidateCreate(_ context.Context, obj runtime.Object) error {
	bucket, ok := obj.(*s3v1alpha1.Bucket)
	if !ok {
		return fmt.Errorf("expected a Bucket but got a %T", obj)
	}
	if err := validateTemplate(bucket.Spec.SecretTemplate); err != nil {
		return field.Invalid(field.NewPath("spec", "secretTemplate"), bucket.Spec.SecretTemplate, err.Error())
	}
	return nil
}

func (r *BucketReconciler) ValidateUpdate(ctx context.Context, o, n runtime.Object) error {
	bucket, ok := n.(*s3v1alpha1.Bucket)
	if !ok {
		return fmt.Errorf("expected a Bucket but got a %T", n)
	}
	if err := validateTemplate(bucket.Spec.SecretTemplate); err != nil {
		return field.Invalid(field.NewPath("spec", "secretTemplate"), bucket.Spec.SecretTemplate, err.Error())
	}
	return nil
}

func (r *BucketReconciler) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	return nil
}

// +kubebuilder:webhook:path=/mutate-s3-linka-cloud-v1alpha1-bucket,mutating=true,failurePolicy=fail,sideEffects=None,groups=s3.linka.cloud,resources=buckets,verbs=create;update,versions=v1alpha1,name=mbucket.kb.io,admissionReviewVersions=v1
// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// +kubebuilder:webhook:path=/validate-s3-linka-cloud-v1alpha1-bucket,mutating=false,failurePolicy=fail,sideEffects=None,groups=s3.linka.cloud,resources=buckets,verbs=create;update,versions=v1alpha1,name=vbucket.kb.io,admissionReviewVersions=v1

func (r *BucketReconciler) SetupWebhookWithManager(_ context.Context, mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&s3v1alpha1.Bucket{}).
		WithDefaulter(r).
		WithValidator(r).
		Complete()
}

func validateTemplate(m map[string]string) error {
	for _, v := range []string{s3v1alpha1.MinioAccessKey, s3v1alpha1.MinioSecretKey, s3v1alpha1.MinioEndpoint, s3v1alpha1.MinioBucket, s3v1alpha1.MinioSecure} {
		if _, ok := m[v]; ok {
			return fmt.Errorf("cannot contain %s", v)
		}
	}
	for k, v := range m {
		tmp, err := template.New("secret").Parse(v)
		if err != nil {
			return fmt.Errorf("%s: unable to parse secret template: %w", k, err)
		}
		var buf bytes.Buffer
		if err := tmp.Execute(&buf, s3v1alpha1.BucketAccess{}); err != nil {
			return fmt.Errorf("%s: unable to execute secret template: %w", k, err)
		}
	}
	return nil
}
