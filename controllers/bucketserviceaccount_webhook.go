package controllers

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	log2 "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	s3v1alpha1 "go.linka.cloud/minio-bucket-controller/api/v1alpha1"
)

var _ webhook.CustomValidator = (*BucketServiceAccountReconciler)(nil)

func (r *BucketServiceAccountReconciler) ValidateCreate(ctx context.Context, obj runtime.Object) error {
	o, ok := any(obj).(metav1.ObjectMetaAccessor)
	if !ok {
		return apierrors.NewBadRequest(fmt.Sprintf("expected a BucketServiceAccount but got a %T", obj))
	}
	log := log2.FromContext(ctx)
	// TODO(adphi): check if bucket already exists
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}
	if !r.allowed(req.UserInfo.Username) {
		return apierrors.NewForbidden(s3v1alpha1.GroupVersion.WithResource("bucketserviceaccounts").GroupResource(), o.GetObjectMeta().GetName(), fmt.Errorf("user %s is not allowed to create service account", req.UserInfo.Username))
	}
	switch o := o.(type) {
	case *s3v1alpha1.BucketServiceAccount:
		log.Info("validate create", "user", req.UserInfo.Username)
		// TODO(adphi): prevent user to create service account
		us, err := r.MC.ListUsers(ctx)
		if err != nil {
			return apierrors.NewInternalError(fmt.Errorf("unable to list users: %w", err))
		}
		if _, ok := us[prefix+o.GetObjectMeta().GetName()]; ok {
			return apierrors.NewConflict(s3v1alpha1.GroupVersion.WithResource("bucketserviceaccounts").GroupResource(), o.Name, fmt.Errorf("account already exists"))
		}
		return nil
	case *corev1.Secret:
		return nil
	default:
		return apierrors.NewBadRequest(fmt.Sprintf("expected a BucketServiceAccount or a Secret but got a %T", obj))
	}
}

func (r *BucketServiceAccountReconciler) ValidateUpdate(ctx context.Context, o, n runtime.Object) error {
	a, ok := any(n).(metav1.ObjectMetaAccessor)
	if !ok {
		return apierrors.NewBadRequest(fmt.Sprintf("expected a BucketServiceAccount but got a %T", o))
	}
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}
	if !r.allowed(req.UserInfo.Username) {
		return apierrors.NewForbidden(s3v1alpha1.GroupVersion.WithResource("bucketserviceaccounts").GroupResource(), a.GetObjectMeta().GetName(), fmt.Errorf("user %s is not allowed to update bucket service account", req.UserInfo.Username))
	}
	return nil
}

func (r *BucketServiceAccountReconciler) ValidateDelete(ctx context.Context, obj runtime.Object) error {
	a, ok := any(obj).(metav1.ObjectMetaAccessor)
	if !ok {
		return apierrors.NewBadRequest(fmt.Sprintf("expected a BucketServiceAccount but got a %T", obj))
	}
	a.GetObjectMeta().GetName()
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return apierrors.NewInternalError(err)
	}
	if !r.allowed(req.UserInfo.Username) {
		return apierrors.NewForbidden(s3v1alpha1.GroupVersion.WithResource("bucketserviceaccounts").GroupResource(), a.GetObjectMeta().GetName(), fmt.Errorf("user %s is not allowed to deleta bucket service account", req.UserInfo.Username))
	}
	return nil
}

// +kubebuilder:webhook:path=/validate-s3-linka-cloud-v1alpha1-bucketserviceaccount,mutating=false,failurePolicy=fail,sideEffects=None,groups=s3.linka.cloud,resources=bucketserviceaccounts,verbs=create;update;delete,versions=v1alpha1,name=vbucketserviceaccount.kb.io,admissionReviewVersions=v1

func (r *BucketServiceAccountReconciler) SetupWebhookWithManager(_ context.Context, mgr ctrl.Manager) error {
	if err := ctrl.NewWebhookManagedBy(mgr).
		For(&s3v1alpha1.BucketServiceAccount{}).
		WithValidator(r).
		Complete(); err != nil {
		return err
	}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&corev1.Secret{}).
		WithValidator(r).
		Complete()
}

func (r *BucketServiceAccountReconciler) allowed(name string) bool {
	return name == r.ServiceAccount || strings.HasPrefix(name, "system:serviceaccount:kube-system:")
}
