// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/go-github/v92/github"
)

// ErrBranchExists means a policy-change branch is already present and can be updated safely.
var ErrBranchExists = errors.New("forge: the policy-change branch exists")

// BranchFile reads path at ref with the blob SHA needed for an optimistic update.
func (g *GitHub) BranchFile(ctx context.Context, path, ref string) (string, string, bool, error) {
	fc, found, err := g.file(ctx, path, &github.RepositoryContentGetOptions{Ref: ref})
	return string(fc.Bytes), fc.BlobSHA, found, err
}

// CreateBranch creates branch at base's current head.
func (g *GitHub) CreateBranch(ctx context.Context, branch, base string) error {
	b, _, err := g.rest.Repositories.GetBranch(ctx, g.owner, g.name, base, 0)
	if err != nil {
		return fmt.Errorf("forge: read base branch: %w", err)
	}
	_, _, err = g.rest.Git.CreateRef(ctx, g.owner, g.name, github.CreateRef{Ref: "refs/heads/" + branch,
		SHA: b.GetCommit().GetSHA()})
	if err != nil {
		var ge *github.ErrorResponse
		if errors.As(err, &ge) && ge.Response != nil && ge.Response.StatusCode == 422 {
			return ErrBranchExists
		}
		return fmt.Errorf("forge: create policy-change branch: %w", err)
	}
	return nil
}

// UpdateFile writes content to path on branch, requiring the blob read to still match.
func (g *GitHub) UpdateFile(ctx context.Context, path, branch, message string, content []byte, blob string) error {
	_, _, err := g.rest.Repositories.UpdateFile(ctx, g.owner, g.name, path, &github.RepositoryContentFileOptions{
		Message: new(message), Content: content, SHA: new(blob), Branch: new(branch),
	})
	if err != nil {
		return fmt.Errorf("forge: update policy manifest: %w", err)
	}
	return nil
}
