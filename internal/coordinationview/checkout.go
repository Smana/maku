// SPDX-License-Identifier: Apache-2.0

package coordinationview

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/coordination"
)

// The parent checkout document's own apiVersion and kind (MR14): a local file format, not a
// Kubernetes Kind.
const (
	CheckoutAPIVersion = "coordination-checkout/v1"
	CheckoutKind       = "CoordinationCheckout"
)

// The two refusals of Checkout: a viewer missing any child gets no document at all, and a family
// whose children are not all filed at one exact, valid commit gets none either.
var (
	ErrCheckoutRestricted = errors.New("checkout_restricted")
	ErrCheckoutNotReady   = errors.New("checkout_not_ready")
)

// CheckoutDocument is what a parent checkout writes beside its worktrees: the core manifest,
// unchanged, and the family's integration attempts with their exact subjects.
type CheckoutDocument struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	coordination.CheckoutManifest
	IntegrationAttempts []coordination.IntegrationAttemptView `json:"integrationAttempts"`
}

var (
	childIDRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	roomRE    = regexp.MustCompile(`^[a-z0-9]{1,32}$`)
	commitRE  = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
)

// Checkout is v's checkout document, all or nothing: every child of a fully readable family, in
// merge order, at exactly one commit (its open PR head, or its landed merge), with its links,
// dependencies and the artifacts it produces or consumes. Worktrees are the child ids, relative,
// until the client knows its root.
func Checkout(v View) (CheckoutDocument, error) {
	if v.RestrictedChildren != 0 {
		return CheckoutDocument{}, ErrCheckoutRestricted
	}
	if len(v.Plan.Children) == 0 {
		return CheckoutDocument{}, notReady("the plan has no child")
	}
	order := slices.Clone(v.MergeOrder)
	for _, c := range v.Plan.Children {
		if !slices.Contains(order, c.ID) {
			order = append(order, c.ID)
		}
	}
	d := CheckoutDocument{APIVersion: CheckoutAPIVersion, Kind: CheckoutKind,
		CheckoutManifest: coordination.CheckoutManifest{ParentTask: v.ParentTask, PlanVersion: v.Plan.Version, PlanHash: v.PlanHash,
			Children: []coordination.CheckoutChild{}},
		IntegrationAttempts: append([]coordination.IntegrationAttemptView{}, v.IntegrationAttempts...)}
	for _, id := range order {
		c, err := checkoutChild(v, id)
		if err != nil {
			return CheckoutDocument{}, err
		}
		d.Children = append(d.Children, c)
	}
	return d, nil
}

func checkoutChild(v View, id string) (coordination.CheckoutChild, error) {
	i := slices.IndexFunc(v.Children, func(c coordination.CoordinationChildView) bool { return c.ID == id })
	if i < 0 {
		return coordination.CheckoutChild{}, notReady("child %s is not filed", id)
	}
	live := v.Children[i]
	repo := live.Repository
	switch {
	case !childIDRE.MatchString(id):
		return coordination.CheckoutChild{}, notReady("a child id is malformed")
	case !roomRE.MatchString(live.Room):
		return coordination.CheckoutChild{}, notReady("child %s has no valid room", id)
	case !githubURL(live.IssueURL, repo, "issues") || !githubURL(live.PRURL, repo, "pull"):
		return coordination.CheckoutChild{}, notReady("child %s has no valid issue and pull request of %s", id, repo)
	case (live.HeadSHA == "") == (live.MergeCommitSHA == ""):
		return coordination.CheckoutChild{}, notReady("child %s is not at exactly one commit", id)
	case !commitRE.MatchString(live.HeadSHA + live.MergeCommitSHA):
		return coordination.CheckoutChild{}, notReady("child %s has a malformed commit", id)
	}
	arts := []v1alpha1.LandedArtifact{}
	for _, a := range v.Artifacts {
		if a.ChildID == id || slices.Contains(live.Consumes, a.ChildID+"/"+a.Name) {
			arts = append(arts, a)
		}
	}
	return coordination.CheckoutChild{ID: id, Repository: repo, Worktree: id, Room: live.Room, Issue: live.IssueURL, PR: live.PRURL,
		DependsOn: append([]string{}, live.DependsOn...), Artifacts: arts, HeadSHA: live.HeadSHA, MergeCommitSHA: live.MergeCommitSHA}, nil
}

// githubURL is whether u is an issue or pull request (kind) of repo on GitHub.
func githubURL(u, repo, kind string) bool {
	rest, ok := strings.CutPrefix(u, "https://github.com/"+repo+"/"+kind+"/")
	if !ok || rest == "" || len(rest) > 10 {
		return false
	}
	return strings.Trim(rest, "0123456789") == "" && rest[0] != '0'
}

func notReady(format string, a ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrCheckoutNotReady}, a...)...)
}

// ManifestHash is d's sha256 over its JSON, lowercase hex: what a later checkout compares to tell
// the document it wrote from a changed plan or head. Go encodes a struct's fields in their declared
// order, so the encoding is canonical for the type.
func ManifestHash(d CheckoutDocument) string {
	b, _ := json.Marshal(d) // only strings, numbers and slices of them: it cannot fail
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
