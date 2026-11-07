// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
)

// The coordination claim annotations (v4 MR7, MR10), each a bounded JSON value written at CREATE
// only beside the SHA-256 of its exact bytes. Artifacts are a dependent child's landed references:
// its sandbox fetches each public file anonymously at the recorded repository/commit/path and
// verifies contentSHA256 before use, so neither the bytes nor a producer token ride the claim.
// The manifest is an integration run's immutable subjects (read by PR 6's integration path).
const (
	AnnCoordinationArtifacts     = "agents.ogenki.io/coordination-artifacts"
	AnnCoordinationArtifactsHash = "agents.ogenki.io/coordination-artifacts-hash"
	AnnCoordinationManifest      = "agents.ogenki.io/coordination-manifest"
	AnnCoordinationManifestHash  = "agents.ogenki.io/coordination-manifest-hash"
)

// The bounds of a claim's coordination annotations: a consumer's references are at most its
// plan's consumes (8), each at most 64 KiB of file, and the JSON under maxCoordinationJSON.
const (
	maxCoordinationArtifacts = 8
	maxArtifactBytes         = 65536
	maxCoordinationJSON      = 8192
)

var (
	hex40      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	childID    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	artifactID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	repository = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
)

// coordinationRoles are the implementation roles a dependent child's runs take: the existing set,
// never a triager or scoper, which a coordination child does not run.
var coordinationRoles = []string{"implementer", "reviewer", "tester"}

// validArtifacts refuses a reference set the claim cannot vouch for.
func (s Spec) validArtifacts() error {
	as := s.CoordinationArtifacts
	if len(as) == 0 {
		return nil
	}
	var errs []error
	if !slices.Contains(coordinationRoles, s.Role) {
		errs = append(errs, fmt.Errorf("role %q takes no coordination artifacts", s.Role))
	}
	if len(as) > maxCoordinationArtifacts {
		errs = append(errs, fmt.Errorf("%d coordination artifacts, at most %d", len(as), maxCoordinationArtifacts))
	}
	seen := map[string]bool{}
	for i, a := range as {
		key := a.ChildID + "/" + a.Name
		switch {
		case seen[key]:
			errs = append(errs, fmt.Errorf("artifact %d: %s twice", i, key))
		case !childID.MatchString(a.ChildID) || !artifactID.MatchString(a.Name) || !repository.MatchString(a.Repository) || len(a.Repository) > 140:
			errs = append(errs, fmt.Errorf("artifact %d: not a child, name and repository", i))
		case a.Path == "" || len(a.Path) > 512 || strings.ContainsFunc(a.Path, unicode.IsControl):
			errs = append(errs, fmt.Errorf("artifact %d: path is not 1..512 printable bytes", i))
		case !hex40.MatchString(a.MergeCommitSHA) || !hex40.MatchString(a.BlobSHA) || !hex64.MatchString(a.ContentSHA256):
			errs = append(errs, fmt.Errorf("artifact %d: merge and blob ids are 40-hex, the content digest 64-hex", i))
		case a.SizeBytes < 0 || a.SizeBytes > maxArtifactBytes:
			errs = append(errs, fmt.Errorf("artifact %d: size %d is not within 0..%d", i, a.SizeBytes, maxArtifactBytes))
		}
		seen[key] = true
	}
	if b, _ := artifactsJSON(as); len(b) > maxCoordinationJSON {
		errs = append(errs, fmt.Errorf("coordination artifacts are %d bytes, at most %d", len(b), maxCoordinationJSON))
	}
	return errors.Join(errs...)
}

// artifactsJSON is the annotation value and its SHA-256; "" for none.
func artifactsJSON(as []v1alpha1.LandedArtifact) ([]byte, string) {
	if len(as) == 0 {
		return nil, ""
	}
	b, _ := json.Marshal(as) // plain strings and ints: never fails
	return b, fmt.Sprintf("%x", sha256.Sum256(b))
}
