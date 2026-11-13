// SPDX-License-Identifier: Apache-2.0

package config

// PrivateCodeSentence is v4 D3's sentence: what privateCode.acknowledged states. The Repository's
// PrivateCode condition, the docs and the banner say it.
const PrivateCodeSentence = "A Repository serving private code needs spec.privateCode.acknowledged: true. " +
	"That field states the code is sent to the configured models, which are usually SaaS."

// PrivateCode acknowledges that a private repository's code is sent to the configured models (v4
// D3). Acknowledged is true or absent: an explicit false says nothing and is refused.
// +kubebuilder:object:generate=true
// +kubebuilder:validation:XValidation:rule="self.acknowledged",message="privateCode.acknowledged is true or absent: false says nothing (D3)"
type PrivateCode struct {
	// Acknowledged states that this repository's code is sent to the configured models, which are
	// usually SaaS.
	// +kubebuilder:validation:Required
	Acknowledged bool `json:"acknowledged"`
}

// PrivateCodeAcknowledged reports whether the repository acknowledges that its code is sent to
// the configured models (v4 D3). A nil repository, one the config does not serve, acknowledges
// nothing.
func (r *Repository) PrivateCodeAcknowledged() bool {
	return r != nil && r.PrivateCode != nil && r.PrivateCode.Acknowledged
}
