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

const (
	ProviderConditionCreating = "Creating"
	ProviderConditionReady    = "Ready"
	ProviderConditionError    = "Error"
	ProviderConditionDeleting = "Deleting"

	ErrProviderInvalid     = "InvalidProvider"
	ErrProviderUnavailable = "UnavailableProvider"

	DefaultProviderAnnotation = "s3.linka.cloud/is-default-provider"
)

type SecretRef struct {
	// Name is the name of the secret
	// +kubebuilder:validation:Required
	Name string `json:"name,omitempty"`
	// Namespace is the namespace of the secret
	// +kubebuilder:validation:Required
	Namespace string `json:"namespace,omitempty"`
	// Key is the key of the secret
	// +kubebuilder:validation:Required
	Key string `json:"key,omitempty"`
}

// BucketProviderSpec defines the desired state of BucketProvider
type BucketProviderSpec struct {
	// Endpoint is the S3 endpoint of the bucket provider
	// +kubebuilder:validation:Required
	Endpoint string `json:"endpoint,omitempty"`
	// AccessKey is a reference to a secret containing the access key
	// +kubebuilder:validation:Required
	AccessKey SecretRef `json:"accessKey,omitempty"`
	// SecretKey is a reference to a secret containing the secret key
	// +kubebuilder:validation:Required
	SecretKey SecretRef `json:"secretKey,omitempty"`
	// Insecure indicates whether to use insecure connection
	// +optional
	Insecure bool `json:"insecure,omitempty"`
}

// BucketProviderStatus defines the observed state of BucketProvider
type BucketProviderStatus struct {
	// Conditions represent the latest available observations of a BucketProvider's current state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	// Phase represents the current phase of the BucketProvider
	Phase string `json:"phase,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// BucketProvider is the Schema for the bucketproviders API
type BucketProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketProviderSpec   `json:"spec,omitempty"`
	Status BucketProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BucketProviderList contains a list of BucketProvider
type BucketProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BucketProvider `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BucketProvider{}, &BucketProviderList{})
}
