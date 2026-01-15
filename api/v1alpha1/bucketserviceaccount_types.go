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
	BucketSAConditionCreating        = "Creating"
	BucketSAConditionReady           = "Ready"
	BucketSAConditionError           = "Error"
	BucketSAConditionDeleting        = "Deleting"
	BucketSAConditionDeletionPending = "DeletionPending"

	BucketSAConditionReasonErrCreateUser    = "ErrCreateUser"
	BucketSAConditionReasonErrCreateAccount = "ErrCreateAccount"
	BucketSAConditionReasonErrCreatePolicy  = "ErrCreatePolicy"

	BucketSASecretType = "s3.linka.cloud/service-account"

	ServiceAccountAnnotation = "s3.linka.cloud/service-account"
)

type BucketServiceAccountSpec struct {
	Provider string `json:"provider,omitempty"`
}

// BucketServiceAccountStatus defines the observed state of BucketServiceAccount
type BucketServiceAccountStatus struct {
	SecretName *string `json:"secretName,omitempty"`
	// Conditions represent the latest available observations of a BucketServiceAccount's current state.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
	Phase      string             `json:"phase,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Secret",type=string,JSONPath=`.status.secretName`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// BucketServiceAccount is the Schema for the bucketserviceaccounts API
type BucketServiceAccount struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BucketServiceAccountSpec   `json:"spec,omitempty"`
	Status BucketServiceAccountStatus `json:"status,omitempty"`
}

func (b *BucketServiceAccount) GetObjectMeta() metav1.Object {
	return &b.ObjectMeta
}

// +kubebuilder:object:root=true

// BucketServiceAccountList contains a list of BucketServiceAccount
type BucketServiceAccountList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BucketServiceAccount `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BucketServiceAccount{}, &BucketServiceAccountList{})
}
