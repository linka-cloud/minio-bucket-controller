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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BucketReclaimPolicy describes a policy for end-of-life maintenance of buckets.
// +kubebuilder:validation:Enum:=Delete;Retain
type BucketReclaimPolicy string

const (
	// BucketReclaimDelete means the bucket will be deleted from Kubernetes on Bucket resource deletion.
	BucketReclaimDelete BucketReclaimPolicy = "Delete"
	// BucketReclaimRetain means the bucket will be left in its current phase (Released) for manual reclamation by the administrator.
	// The default policy is Retain.
	BucketReclaimRetain BucketReclaimPolicy = "Retain"
)

const (
	MinioAccessKey = "MINIO_ACCESS_KEY"
	MinioSecretKey = "MINIO_SECRET_KEY"
	MinioEndpoint  = "MINIO_ENDPOINT"
	MinioBucket    = "MINIO_BUCKET"
	MinioSecure    = "MINIO_SECURE"
)

const (
	BucketConditionCreating = "Creating"
	BucketConditionReady    = "Ready"
	BucketConditionError    = "Error"
	BucketConditionDeleting = "Deleting"

	BucketConditionReasonErrCreateBucket         = "ErrCreateBucket"
	BucketConditionReasonErrCreateServiceAccount = "ErrCreateServiceAccount"
	BucketConditionReasonErrCreatePolicy         = "ErrCreatePolicy"
	BucketConditionReasonErrCreateSecret         = "ErrCreateSecret"

	BucketAccessSecretType = "s3.linka.cloud/bucket-access"
)

type BucketAccess struct {
	AccessKey string
	SecretKey string
	Endpoint  string
	Bucket    string
	Secure    bool
}

// BucketSpec defines the desired state of Bucket
type BucketSpec struct {
	// ServiceAccount is the name of the service account that should be used for bucket access.
	// If not specified, a service account with the same name as the bucket will be created.
	// +optional
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=^[a-z0-9]+[a-z0-9.-]*[a-z0-9]+$
	ServiceAccount string `json:"serviceAccount,omitempty"`
	// ReclaimPolicy is the name of the BucketReclaimPolicy to use for this bucket.
	// +kubebuilder:default:=Retain
	ReclaimPolicy BucketReclaimPolicy `json:"reclaimPolicy,omitempty"`
	// SecretName is the name of the secret containing the credentials to access the bucket that should be created.
	// +optional
	// +kubebuilder:validation:MinLength=3
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern=^[a-z0-9]+[a-z0-9.-]*[a-z0-9]+$
	SecretName *string `json:"secretName,omitempty"`
	// SecretTemplate is the template for the secret containing the credentials to access the bucket that should be created.
	// The templates takes a BucketAccess struct as input.
	// +optional
	SecretTemplate map[string]string `json:"secretTemplate,omitempty"`
}

// BucketStatus defines the observed state of Bucket
type BucketStatus struct {
	// +optional
	Endpoint *string `json:"endpoint,omitempty"`
	// +optional
	SecretName *string            `json:"secretName,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Reclaim",type=string,JSONPath=`.spec.reclaimPolicy`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.conditions[-1:].type"
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.status.endpoint`,priority=1
// +kubebuilder:printcolumn:name="Service Account",type=string,JSONPath=`.spec.serviceAccount`,priority=1
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.status.secretName`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Bucket is the Schema for the buckets API
// The controller will try to create a bucket with the same name as the Bucket resource,
// it will also create a user and the policy giving read/write access to the bucket.
// It will then create a secret with the credentials the user's service account credentials:
// MINIO_ACCESS_KEY: the account's access key
// MINIO_SECRET_KEY: the account's secret key
// MINIO_ENDPOINT: the endpoint of the minio server
// MINIO_BUCKET: the name of the bucket
// MINIO_SECURE: whether the connection to the minio server should be secure
type Bucket struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketSpec   `json:"spec,omitempty"`
	Status BucketStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BucketList contains a list of Bucket
type BucketList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Bucket `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Bucket{}, &BucketList{})
}
