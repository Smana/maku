// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"crypto/sha1" //nolint:gosec // git's blob id is SHA-1: recomputed to prove the bytes are the blob's
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
)

// MaxArtifactBytes is the initial file artifact bound (MR7): 64 KiB.
const MaxArtifactBytes = 65536

// ErrArtifactUnverified is a producer's artifact that is not the exact declared file at its
// landed merge commit: missing, not a file, the wrong path, size or digest. No reference is
// recorded and its dependent is not admitted (MR7, Review Focus 3).
var ErrArtifactUnverified = errors.New("artifact_unverified")

// ArtifactReader is the producer repository's own factory App client: the only one an artifact is
// read through, so no consumer credential ever touches it.
type ArtifactReader interface {
	FileAtCommit(ctx context.Context, sha, path string) (forge.FileContent, bool, error)
}

var hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// VerifyArtifact reads spec, produced by child, at child's landed merge commit mergeSHA through
// fx, the producer's own client, and answers its immutable reference (MR7). Only the file at that
// commit counts: a PR head, a newer default branch or a prose handoff never does. The bytes'
// git blob id and size must be the ones GitHub declared, the size within spec.MaxBytes and 64 KiB.
// Anything else is ErrArtifactUnverified with no reference; a failed read is a plain error to
// retry.
func VerifyArtifact(ctx context.Context, fx ArtifactReader, child v1alpha1.CoordinationChild, spec v1alpha1.ArtifactSpec, mergeSHA string) (v1alpha1.LandedArtifact, error) {
	unverified := func(format string, a ...any) (v1alpha1.LandedArtifact, error) {
		return v1alpha1.LandedArtifact{}, fmt.Errorf("%w: %s/%s: %s", ErrArtifactUnverified, child.ID, spec.Name, fmt.Sprintf(format, a...))
	}
	switch {
	case spec.Kind != "file":
		return unverified("kind %q is not file", spec.Kind)
	case spec.MaxBytes < 1 || spec.MaxBytes > MaxArtifactBytes:
		return unverified("maxBytes %d is not within 1..%d", spec.MaxBytes, MaxArtifactBytes)
	case !hex40.MatchString(mergeSHA):
		return unverified("the merge commit is not 40-hex")
	}
	fc, found, err := fx.FileAtCommit(ctx, mergeSHA, spec.Path)
	switch {
	case errors.Is(err, forge.ErrNotAFile):
		return unverified("not a file at the merge commit")
	case err != nil:
		return v1alpha1.LandedArtifact{}, fmt.Errorf("read artifact %s/%s: %w", child.ID, spec.Name, err)
	case !found:
		return unverified("absent at the merge commit")
	}
	size := int64(len(fc.Bytes))
	switch {
	case fc.Path != spec.Path:
		return unverified("the forge answered another path")
	case size != fc.SizeBytes:
		return unverified("%d bytes read, %d declared", size, fc.SizeBytes)
	case size > spec.MaxBytes:
		return unverified("%d bytes over maxBytes %d", size, spec.MaxBytes)
	case !hex40.MatchString(fc.BlobSHA) || fc.BlobSHA != blobSHA(fc.Bytes):
		return unverified("the bytes are not the declared blob")
	}
	return v1alpha1.LandedArtifact{ChildID: child.ID, Name: spec.Name, Repository: child.Repository, Path: spec.Path,
		MergeCommitSHA: mergeSHA, BlobSHA: fc.BlobSHA, ContentSHA256: fmt.Sprintf("%x", sha256.Sum256(fc.Bytes)), SizeBytes: size}, nil
}

// blobSHA is git's object id of b as a blob.
func blobSHA(b []byte) string {
	h := sha1.New() //nolint:gosec // git's object id, not a security digest
	_, _ = fmt.Fprintf(h, "blob %d\x00", len(b))
	_, _ = h.Write(b)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ConsumedArtifacts is c's required consumes, each as recorded in landed; ok is false while any is
// not yet landed. An optional consume is not handed over in this release (MR7: required edges
// alone gate admission).
func ConsumedArtifacts(c v1alpha1.CoordinationChild, landed []v1alpha1.LandedArtifact) ([]v1alpha1.LandedArtifact, bool) {
	var out []v1alpha1.LandedArtifact
	for _, k := range c.Consumes {
		if !k.Required {
			continue
		}
		a, ok := LandedRef(landed, k.From, k.Artifact)
		if !ok {
			return nil, false
		}
		out = append(out, a)
	}
	return out, true
}

// LandedRef is producer from's artifact name among landed.
func LandedRef(landed []v1alpha1.LandedArtifact, from, name string) (v1alpha1.LandedArtifact, bool) {
	for _, a := range landed {
		if a.ChildID == from && a.Name == name {
			return a, true
		}
	}
	return v1alpha1.LandedArtifact{}, false
}

// ProducedSpec is producer p's declared artifact name.
func ProducedSpec(p v1alpha1.CoordinationChild, name string) (v1alpha1.ArtifactSpec, bool) {
	for _, s := range p.Produces {
		if s.Name == name {
			return s, true
		}
	}
	return v1alpha1.ArtifactSpec{}, false
}
