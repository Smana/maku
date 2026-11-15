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

// SetPrivateRefused sets the runtime visibility check for unacknowledged private repositories (v4 D3).
func (c *Config) SetPrivateRefused(refused func(repository string) bool) {
	if c != nil {
		c.privateRefused = refused
	}
}

// PrivateRefused reports whether repository is private code without its acknowledgement (v4 D3, WF12).
func (c *Config) PrivateRefused(repository string) bool {
	if c == nil {
		return false
	}
	r := c.RepositoryFor(repository)
	if r == nil || r.PrivateCodeAcknowledged() {
		return false
	}
	if r.PrivateCode != nil && !r.PrivateCode.Acknowledged {
		return true
	}
	return c.privateRefused != nil && c.privateRefused(repository)
}
