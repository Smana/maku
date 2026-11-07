// SPDX-License-Identifier: Apache-2.0

package runctl

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	v1alpha1 "github.com/Smana/maku/api/v1alpha1"
)

// The integration manifest's claim annotations (v4 MR10), as the factory writes them at CREATE
// (runs.AnnCoordinationManifest and its hash), and the harness variables that carry them. The
// harness verifies the hash again, writes the bytes to a read-only file and exports its path as
// MAKU_COORDINATION_MANIFEST: the variables here are its input, never what a check reads.
const (
	annCoordinationManifest      = "agents.ogenki.io/coordination-manifest"
	annCoordinationManifestHash  = "agents.ogenki.io/coordination-manifest-hash"
	EnvCoordinationManifest      = "COORDINATION_MANIFEST"
	EnvCoordinationManifestHash  = "COORDINATION_MANIFEST_SHA256"
	maxCoordinationManifestBytes = 8192 // runs.maxCoordinationJSON
)

// invalidManifest refuses a manifest the claim cannot vouch for, as runs.validManifest refuses to
// write one: only a triager with no branch carries one, as 1..8192 bytes of JSON under the exact
// SHA-256 of its bytes. "" when the run carries none or it holds.
func invalidManifest(r *v1alpha1.AgentRun) string {
	raw, hasRaw := r.Annotations[annCoordinationManifest]
	hash, hasHash := r.Annotations[annCoordinationManifestHash]
	switch {
	case !hasRaw && !hasHash:
		return ""
	case !hasRaw:
		return "a coordination manifest hash rides with no manifest"
	case r.Spec.Role != "triager" || r.Spec.Branch != "":
		return fmt.Sprintf("a coordination manifest rides a triager run with no branch, not %q on %q", r.Spec.Role, r.Spec.Branch)
	case len(raw) == 0 || len(raw) > maxCoordinationManifestBytes || !json.Valid([]byte(raw)):
		return fmt.Sprintf("the coordination manifest is not 1..%d bytes of JSON", maxCoordinationManifestBytes)
	case hash != fmt.Sprintf("%x", sha256.Sum256([]byte(raw))):
		return "the coordination manifest hash is not the SHA-256 of its bytes"
	}
	return ""
}

// manifestEnv is the harness's manifest and hash, nothing for a run that carries none. Invalid has
// refused any other before the pod exists.
func manifestEnv(r *v1alpha1.AgentRun) []corev1.EnvVar {
	raw, ok := r.Annotations[annCoordinationManifest]
	if !ok {
		return nil
	}
	return []corev1.EnvVar{{Name: EnvCoordinationManifest, Value: raw},
		{Name: EnvCoordinationManifestHash, Value: r.Annotations[annCoordinationManifestHash]}}
}
