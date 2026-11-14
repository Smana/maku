// SPDX-License-Identifier: Apache-2.0

package runwatch

import (
	"errors"
	"fmt"
	"regexp"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/Smana/maku/api/v1alpha1"
)

// Kind is where AgentRuns live and how they are named: the run-controller's CRD (maku.ogenki.io,
// v4 D5), in a namespace and under a name prefix each install chooses. It is a value each binary
// builds from its config and hands to every reader of a claim (D9): there is no process-wide kind,
// so two kinds can share a process, as the tests do.
type Kind struct {
	Group      string `json:"group,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	NamePrefix string `json:"namePrefix,omitempty"`
}

// DefaultKind is the run-controller's, as the chart renders it: run-<C2 id> in agents (C2).
var DefaultKind = Kind{Group: v1alpha1.GroupVersion.Group, Namespace: "agents", NamePrefix: "run-"}

// removedXRGroup is cloud-native-ref's Crossplane AgentRun, removed in v4 step 8 (D5).
const removedXRGroup = "cloud.ogenki.io"

var (
	nsRE     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	prefixRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}-$`) // + 8 id characters stays a DNS label
)

// WithDefaults fills each empty field from DefaultKind.
func (k Kind) WithDefaults() Kind {
	if k.Group == "" {
		k.Group = DefaultKind.Group
	}
	if k.Namespace == "" {
		k.Namespace = DefaultKind.Namespace
	}
	if k.NamePrefix == "" {
		k.NamePrefix = DefaultKind.NamePrefix
	}
	return k
}

// Validate reports every malformed field at once.
func (k Kind) Validate() error {
	var errs []error
	switch k.Group {
	case DefaultKind.Group:
	case removedXRGroup:
		errs = append(errs, fmt.Errorf("group %s, cloud-native-ref's Crossplane AgentRun, was removed in v4 step 8 (D5): "+
			"runs are the run-controller's, group %s (docs/upgrading.md#the-xr-run-kind-and-octo-sts)", removedXRGroup, DefaultKind.Group))
	default:
		errs = append(errs, fmt.Errorf("group %q: runs are the run-controller's AgentRuns, group %s (D5)", k.Group, DefaultKind.Group))
	}
	if !nsRE.MatchString(k.Namespace) {
		errs = append(errs, fmt.Errorf("namespace %q is not a DNS label", k.Namespace))
	}
	if err := ValidatePrefix(k.NamePrefix); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// ValidatePrefix refuses a run-name prefix that, with 8 id characters, is not a DNS label.
func ValidatePrefix(p string) error {
	if !prefixRE.MatchString(p) {
		return fmt.Errorf("namePrefix %q must match %s", p, prefixRE)
	}
	return nil
}

// GVK is k's AgentRun kind (C3).
func (k Kind) GVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{Group: k.Group, Version: "v1alpha1", Kind: "AgentRun"}
}

// ClaimName is the name of run id's claim under k: <prefix><C2 id>.
func (k Kind) ClaimName(id string) string { return k.NamePrefix + id }

// RunSubject matches a run's ServiceAccount token subject under k, capturing the run id: the
// ServiceAccount is named exactly like the run, on both runtimes. It compiles on each call, so a
// caller that matches many subjects keeps the result.
func (k Kind) RunSubject() *regexp.Regexp {
	return regexp.MustCompile("^system:serviceaccount:" + regexp.QuoteMeta(k.Namespace) + ":" +
		regexp.QuoteMeta(k.NamePrefix) + "([a-z2-7]{8})$")
}
