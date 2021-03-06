/*


Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// OpenShiftClusterSpec defines the desired state of OpenShiftCluster
type OpenShiftClusterSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Release ReleaseSpec `json:"release"`

	InitialComputeReplicas int `json:"initialComputeReplicas"`

	// PullSecret is a pull secret injected into the container runtime of guest
	// workers. It should have an ".dockerconfigjson" key containing the pull secret JSON.
	PullSecret corev1.LocalObjectReference `json:"pullSecret"`

	SSHKey corev1.LocalObjectReference `json:"sshKey"`

	ProviderCreds corev1.LocalObjectReference `json:"providerCreds"`

	ServiceCIDR string `json:"serviceCIDR"`
	PodCIDR     string `json:"podCIDR"`
}

type ReleaseSpec struct {
	// +kubebuilder:validation:Optional
	Channel string `json:"channel"`
	// Image is the release image pullspec for the control plane
	// +kubebuilder:validation:Required
	Image string `json:"image"`
}

// OpenShiftClusterStatus defines the observed state of OpenShiftCluster
type OpenShiftClusterStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	Ready bool `json:"ready"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=openshiftclusters,shortName=oc;ocs,scope=Namespaced
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// OpenShiftCluster is the Schema for the openshiftclusters API
type OpenShiftCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OpenShiftClusterSpec   `json:"spec,omitempty"`
	Status OpenShiftClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// OpenShiftClusterList contains a list of OpenShiftCluster
type OpenShiftClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []OpenShiftCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&OpenShiftCluster{}, &OpenShiftClusterList{})
}
