// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

// MR7: an artifact is landed only as the exact file at the producer's merge commit, read through
// the producer's own client, with its path, kind, size, blob id and content digest proven. Every
// other shape records nothing; a read the forge failed is retried, never unverified.
func TestVerifyArtifactAcceptsOnlyTheExactLandedFile(t *testing.T) {
	const (
		merge = "a1b2c3d4e5f60718293a4b5c6d7e8f9011223344"
		head  = "b1b2c3d4e5f60718293a4b5c6d7e8f9011223344"
		path  = "schemas/consumer-v1.json"
		body  = `{"type":"object"}`
	)
	child := v1alpha1.CoordinationChild{ID: "contract", Repository: "Smana/public-contracts"}
	spec := v1alpha1.ArtifactSpec{Name: "consumer-v1-schema", Kind: "file", Path: path, MediaType: "application/json", MaxBytes: 65536}
	for _, tc := range []struct {
		name   string
		setup  func(f *forgetest.Fake)
		reader ArtifactReader // instead of the fake
		edit   func(s *v1alpha1.ArtifactSpec)
		sha    string
		want   error // nil: landed
		retry  bool  // a forge failure: not unverified
	}{
		{name: "the exact file at the merge commit", setup: func(f *forgetest.Fake) { f.SetFileAt(merge, path, body) }},
		{name: "65536 bytes", setup: func(f *forgetest.Fake) { f.SetFileAt(merge, path, strings.Repeat("x", 65536)) }},
		{name: "65537 bytes", setup: func(f *forgetest.Fake) { f.SetFileAt(merge, path, strings.Repeat("x", 65537)) }, want: ErrArtifactUnverified},
		{name: "over its own maxBytes", setup: func(f *forgetest.Fake) { f.SetFileAt(merge, path, body) },
			edit: func(s *v1alpha1.ArtifactSpec) { s.MaxBytes = 4 }, want: ErrArtifactUnverified},
		{name: "a maxBytes over 64 KiB", setup: func(f *forgetest.Fake) { f.SetFileAt(merge, path, body) },
			edit: func(s *v1alpha1.ArtifactSpec) { s.MaxBytes = 65537 }, want: ErrArtifactUnverified},
		{name: "only at the PR head", setup: func(f *forgetest.Fake) { f.SetFileAt(head, path, body) }, want: ErrArtifactUnverified},
		{name: "only on the newer default branch", setup: func(f *forgetest.Fake) { f.SetFile(path, body) }, want: ErrArtifactUnverified},
		{name: "missing", setup: func(*forgetest.Fake) {}, want: ErrArtifactUnverified},
		{name: "a wrong path", setup: func(f *forgetest.Fake) { f.SetFileAt(merge, path, body) },
			edit: func(s *v1alpha1.ArtifactSpec) { s.Path = "schemas/other.json" }, want: ErrArtifactUnverified},
		{name: "a path the forge answers for another", reader: answer{Path: "elsewhere.json", Bytes: []byte(body),
			BlobSHA: forgetest.BlobSHA([]byte(body)), SizeBytes: int64(len(body))}, want: ErrArtifactUnverified},
		{name: "a merge sha that is not 40-hex", setup: func(f *forgetest.Fake) { f.SetFileAt(merge, path, body) }, sha: "main", want: ErrArtifactUnverified},
		{name: "a wrong blob digest", setup: func(f *forgetest.Fake) {
			f.SetFileContentAt(merge, forge.FileContent{Path: path, Bytes: []byte(body), BlobSHA: strings.Repeat("0", 40), SizeBytes: int64(len(body))})
		}, want: ErrArtifactUnverified},
		{name: "a blob id that is not hex", setup: func(f *forgetest.Fake) {
			f.SetFileContentAt(merge, forge.FileContent{Path: path, Bytes: []byte(body), BlobSHA: "zz", SizeBytes: int64(len(body))})
		}, want: ErrArtifactUnverified},
		{name: "a declared size that is not the bytes'", setup: func(f *forgetest.Fake) {
			f.SetFileContentAt(merge, forge.FileContent{Path: path, Bytes: []byte(body), BlobSHA: forgetest.BlobSHA([]byte(body)), SizeBytes: 3})
		}, want: ErrArtifactUnverified},
		{name: "a directory or symlink", setup: func(f *forgetest.Fake) { f.SetFileAt(merge, path, forgetest.NotAFile) }, want: ErrArtifactUnverified},
		{name: "a kind that is not file", setup: func(f *forgetest.Fake) { f.SetFileAt(merge, path, body) },
			edit: func(s *v1alpha1.ArtifactSpec) { s.Kind = "image" }, want: ErrArtifactUnverified},
		{name: "a failed read", setup: func(f *forgetest.Fake) { f.SetFileAt(merge, path, "!") }, retry: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := forgetest.NewFake()
			var fx ArtifactReader = f
			if tc.setup != nil {
				tc.setup(f)
			}
			if tc.reader != nil {
				fx = tc.reader
			}
			s := spec
			if tc.edit != nil {
				tc.edit(&s)
			}
			sha := merge
			if tc.sha != "" {
				sha = tc.sha
			}
			got, err := VerifyArtifact(t.Context(), fx, child, s, sha)
			switch {
			case tc.retry:
				if err == nil || errors.Is(err, ErrArtifactUnverified) {
					t.Fatalf("a failed read is retried: %v", err)
				}
			case tc.want != nil:
				if !errors.Is(err, tc.want) || got != (v1alpha1.LandedArtifact{}) || Reason(err) != tc.want.Error() {
					t.Fatalf("got %+v %v, want %v and no reference", got, err, tc.want)
				}
			default:
				fc, _, _ := f.FileAtCommit(t.Context(), merge, s.Path)
				want := v1alpha1.LandedArtifact{ChildID: "contract", Name: s.Name, Repository: child.Repository, Path: s.Path,
					MergeCommitSHA: merge, BlobSHA: forgetest.BlobSHA(fc.Bytes),
					ContentSHA256: fmt.Sprintf("%x", sha256.Sum256(fc.Bytes)), SizeBytes: int64(len(fc.Bytes))}
				if err != nil || got != want {
					t.Fatalf("got %+v %v, want %+v", got, err, want)
				}
			}
		})
	}
}

// answer is a reader that serves one file whatever is asked.
type answer forge.FileContent

func (a answer) FileAtCommit(context.Context, string, string) (forge.FileContent, bool, error) {
	return forge.FileContent(a), true, nil
}

// The landed reference set a consumer is created with is exactly its required consumes, each
// found among the parent's recorded artifacts.
func TestConsumedArtifacts(t *testing.T) {
	a := v1alpha1.LandedArtifact{ChildID: "contract", Name: "consumer-v1-schema", Repository: "Smana/public-contracts", Path: "s.json",
		MergeCommitSHA: strings.Repeat("a", 40), BlobSHA: strings.Repeat("b", 40), ContentSHA256: strings.Repeat("c", 64), SizeBytes: 3}
	c := v1alpha1.CoordinationChild{ID: "consumer", Consumes: []v1alpha1.ArtifactConsumer{
		{From: "contract", Artifact: "consumer-v1-schema", Required: true}, {From: "contract", Artifact: "optional", Required: false}}}
	if got, ok := ConsumedArtifacts(c, []v1alpha1.LandedArtifact{a}); !ok || len(got) != 1 || got[0] != a {
		t.Fatalf("got %+v %v", got, ok)
	}
	if _, ok := ConsumedArtifacts(c, nil); ok {
		t.Fatal("a required artifact not landed is not consumable")
	}
}
