// SPDX-License-Identifier: Apache-2.0

// Parse is the authority on what a config requires: the CRDs built on these structs require
// none of their fields, so a field Parse defaults may be omitted from a Factory, Repository or
// MergePolicy as it may from the file (factory resources, "Validation").
// +kubebuilder:validation:Optional

package config
