/*
Copyright The Kubernetes Authors.

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

// Code generated by applyconfiguration-gen. DO NOT EDIT.

package v1

// StorageOSPersistentVolumeSourceApplyConfiguration represents a declarative configuration of the StorageOSPersistentVolumeSource type for use
// with apply.
type StorageOSPersistentVolumeSourceApplyConfiguration struct {
	VolumeName      *string                            `json:"volumeName,omitempty"`
	VolumeNamespace *string                            `json:"volumeNamespace,omitempty"`
	FSType          *string                            `json:"fsType,omitempty"`
	ReadOnly        *bool                              `json:"readOnly,omitempty"`
	SecretRef       *ObjectReferenceApplyConfiguration `json:"secretRef,omitempty"`
}

// StorageOSPersistentVolumeSourceApplyConfiguration constructs a declarative configuration of the StorageOSPersistentVolumeSource type for use with
// apply.
func StorageOSPersistentVolumeSource() *StorageOSPersistentVolumeSourceApplyConfiguration {
	return &StorageOSPersistentVolumeSourceApplyConfiguration{}
}
func (b StorageOSPersistentVolumeSourceApplyConfiguration) IsApplyConfiguration() {}

// WithVolumeName sets the VolumeName field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the VolumeName field is set to the value of the last call.
func (b *StorageOSPersistentVolumeSourceApplyConfiguration) WithVolumeName(value string) *StorageOSPersistentVolumeSourceApplyConfiguration {
	b.VolumeName = &value
	return b
}

// WithVolumeNamespace sets the VolumeNamespace field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the VolumeNamespace field is set to the value of the last call.
func (b *StorageOSPersistentVolumeSourceApplyConfiguration) WithVolumeNamespace(value string) *StorageOSPersistentVolumeSourceApplyConfiguration {
	b.VolumeNamespace = &value
	return b
}

// WithFSType sets the FSType field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the FSType field is set to the value of the last call.
func (b *StorageOSPersistentVolumeSourceApplyConfiguration) WithFSType(value string) *StorageOSPersistentVolumeSourceApplyConfiguration {
	b.FSType = &value
	return b
}

// WithReadOnly sets the ReadOnly field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the ReadOnly field is set to the value of the last call.
func (b *StorageOSPersistentVolumeSourceApplyConfiguration) WithReadOnly(value bool) *StorageOSPersistentVolumeSourceApplyConfiguration {
	b.ReadOnly = &value
	return b
}

// WithSecretRef sets the SecretRef field in the declarative configuration to the given value
// and returns the receiver, so that objects can be built by chaining "With" function invocations.
// If called multiple times, the SecretRef field is set to the value of the last call.
func (b *StorageOSPersistentVolumeSourceApplyConfiguration) WithSecretRef(value *ObjectReferenceApplyConfiguration) *StorageOSPersistentVolumeSourceApplyConfiguration {
	b.SecretRef = value
	return b
}
