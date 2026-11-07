// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/coordinationview"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/mcpwire"
)

// CoordinationManifestFile is the checkout document a parent checkout writes in its root.
const CoordinationManifestFile = "maku-coordination.json"

// maxManifestBytes bounds an existing manifest read back: two children and their artifacts.
const maxManifestBytes = 1 << 20

// CoordinationCheckout is one parent checkout (v4 MR14): every child of a family in a detached
// worktree at the exact commit the document names, under one root beside the primary clone.
// Workspace is the primary clone; Workspaces names the clone of every other repository.
type CoordinationCheckout struct {
	Git        Git
	Workspace  string
	Workspaces map[string]string
	Document   coordinationview.CheckoutDocument
	// Confirm asks once about every child's flagged files, each prefixed by its child id; nil means
	// the client cannot ask.
	Confirm func(ctx context.Context, files []string, more int) (bool, error)
}

// CoordinationCheckedOut is the root a checkout made or found, and its worktrees.
type CoordinationCheckedOut struct {
	Root         string                 `json:"root"`
	ManifestPath string                 `json:"manifestPath"`
	Children     []CoordinationWorktree `json:"children"`
	Reused       bool                   `json:"reused"`
}

// CoordinationWorktree is one child's worktree.
type CoordinationWorktree struct {
	ChildID    string `json:"childID"`
	Repository string `json:"repository"`
	Path       string `json:"path"`
	SHA        string `json:"sha"`
	Reused     bool   `json:"reused"`
}

var (
	coordIDRE   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	coordRoomRE = regexp.MustCompile(`^[a-z0-9]{1,32}$`)
)

// coordChild is one child as resolved: its clone, its worktree path, and the commit verified.
type coordChild struct {
	c       coordination.CheckoutChild
	top     string
	path    string
	sha     string
	tracked string // the remote-tracking ref that was fetched and verified
}

// Run checks the family out, all or nothing, in the order of MR14: it validates the document,
// resolves every child's clone, fetches and verifies every commit, asks once, verifies again,
// then creates each worktree with git worktree add --detach and writes the manifest last. It never
// clones, creates or moves a branch, touches a clone's own checkout, or runs a hook (RunGit). A
// root it finds is reused only when its manifest is this document's; otherwise it is refused, and a
// root left incomplete by a failure is reported, never repaired or deleted.
func (c CoordinationCheckout) Run(ctx context.Context) (CoordinationCheckedOut, error) {
	var out CoordinationCheckedOut
	d := c.Document
	if err := validCheckoutDocument(d); err != nil {
		return out, err
	}
	git := c.Git
	if git == nil {
		git = RunGit
	}
	children, primary, err := c.clones(ctx, git)
	if err != nil {
		return out, err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(primary))
	if err != nil {
		return out, fmt.Errorf("coordination checkout: %w", err)
	}
	out.Root = filepath.Join(parent, filepath.Base(primary)+"."+d.ParentTask)
	out.ManifestPath = filepath.Join(out.Root, CoordinationManifestFile)
	for i := range children {
		children[i].path = filepath.Join(out.Root, children[i].c.ID)
	}
	unlock, err := lockRoom(ctx, git, primary, "coordination-"+d.ParentTask)
	if err != nil {
		return out, err
	}
	defer unlock()
	want := absolute(d, children)

	if _, err := os.Lstat(out.Root); err == nil {
		return c.reuse(ctx, git, out, children, want)
	} else if !errors.Is(err, os.ErrNotExist) {
		return out, fmt.Errorf("coordination checkout: %w", err)
	}

	var shown []string
	more := 0
	for i := range children {
		if err := verify(ctx, git, &children[i], true); err != nil {
			return out, err
		}
		files, n, err := Checkout{}.flaggedFiles(ctx, git, children[i].top, children[i].sha)
		if err != nil {
			return out, err
		}
		for _, f := range files {
			shown = append(shown, children[i].c.ID+"/"+f)
		}
		more += n
	}
	if len(shown) > 0 {
		listed := strings.Join(CodeSpans(shown, more), ", ")
		if c.Confirm == nil {
			return out, refuse("the family changes files that run code on your machine, and this client cannot ask you: "+listed,
				"read them in the pull requests, then check the children out yourself")
		}
		ok, err := c.Confirm(ctx, shown, more)
		if err != nil {
			return out, refuse("the developer was not asked: "+OneLine(err.Error()), "")
		}
		if !ok {
			return out, refuse("not created: the developer did not confirm the files that run code ("+listed+")", "")
		}
	}
	// The form can take minutes: the root and every commit are checked again before anything is made.
	if err := freePath(out.Root, "coordination "+d.ParentTask); err != nil {
		return out, err
	}
	for i := range children {
		if err := verify(ctx, git, &children[i], false); err != nil {
			return out, err
		}
	}
	if err := os.Mkdir(out.Root, 0o755); err != nil { //nolint:gosec // a checkout root, read by the developer's tools
		return out, fmt.Errorf("coordination checkout: %w", err)
	}
	for _, ch := range children {
		if _, err := git(ctx, ch.top, "worktree", "add", "--detach", ch.path, ch.sha); err != nil {
			return out, incomplete(out.Root, out.Children, err)
		}
		if head, err := revParse(ctx, git, ch.path, "HEAD"); err != nil || head != ch.sha {
			return out, incomplete(out.Root, out.Children, fmt.Errorf("%s is not at %s", ch.c.ID, ch.sha[:12]))
		}
		out.Children = append(out.Children, CoordinationWorktree{ChildID: ch.c.ID, Repository: ch.c.Repository, Path: ch.path, SHA: ch.sha})
	}
	if err := writeManifest(out.ManifestPath, want); err != nil {
		return out, incomplete(out.Root, out.Children, err)
	}
	return out, nil
}

// clones resolves each child's clone: its own Workspaces entry, else the primary Workspace, whose
// origin must be the child's repository. The primary's top is the root's anchor, and it must be a
// clone of one of the family's repositories.
func (c CoordinationCheckout) clones(ctx context.Context, git Git) ([]coordChild, string, error) {
	primary := ""
	var out []coordChild
	for _, ch := range c.Document.Children {
		dir, own := c.Workspaces[ch.Repository]
		if !own {
			dir = c.Workspace
		}
		top, err := cloneOf(ctx, git, dir, ch.Repository)
		if err != nil {
			return nil, "", err
		}
		if !own {
			primary = top
		}
		out = append(out, coordChild{c: ch, top: top})
	}
	if primary == "" {
		return nil, "", refuse(OneLine(c.Workspace)+" is not a clone of any of the family's repositories", "run it from the primary repository's clone")
	}
	return out, primary, nil
}

// cloneOf is dir's top when it is a clone of repo with exactly one origin URL (L18).
func cloneOf(ctx context.Context, git Git, dir, repo string) (string, error) {
	clone := refuse(OneLine(dir)+" is not a checkout of "+repo, "gh repo clone "+repo)
	top, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", clone
	}
	top = strings.TrimRight(top, "\n")
	origin, err := git(ctx, top, "config", "--get-all", "remote.origin.url")
	urls := strings.Fields(origin)
	if err != nil || len(urls) != 1 || !originRE(repo).MatchString(urls[0]) {
		return "", clone
	}
	return top, nil
}

// verify fetches (when fetch) and checks one child's commit: an open child's agent branch on origin
// must be exactly its head, and a landed child's merge must be on origin's default branch.
func verify(ctx context.Context, git Git, ch *coordChild, fetch bool) error {
	stale := func(why string) error {
		return refuse("checkout_stale: "+why, "read the parent room again, then check out its current plan")
	}
	if ch.c.HeadSHA != "" {
		ch.tracked = "refs/remotes/origin/agent/" + ch.c.Room
		if fetch {
			if _, err := git(ctx, ch.top, "fetch", "--no-tags", "origin", "+refs/heads/agent/"+ch.c.Room+":"+ch.tracked); err != nil {
				return stale(ch.c.ID + "'s branch agent/" + ch.c.Room + " is gone from origin")
			}
		}
		sha, err := revParse(ctx, git, ch.top, ch.tracked)
		if err != nil || sha != ch.c.HeadSHA {
			return stale(ch.c.ID + "'s pull request head moved since the document was read")
		}
		ch.sha = sha
		return nil
	}
	if fetch {
		def, err := defaultBranch(ctx, git, ch.top)
		if err != nil {
			return err
		}
		ch.tracked = "refs/remotes/origin/" + def
		if _, err := git(ctx, ch.top, "fetch", "--no-tags", "origin", "+refs/heads/"+def+":"+ch.tracked); err != nil {
			return fmt.Errorf("coordination checkout: fetching %s: %w", OneLine(def), err)
		}
	}
	sha, err := revParse(ctx, git, ch.top, ch.c.MergeCommitSHA)
	if err != nil || sha != ch.c.MergeCommitSHA {
		return stale(ch.c.ID + "'s merge commit is not on origin")
	}
	if _, err := git(ctx, ch.top, "merge-base", "--is-ancestor", sha, ch.tracked); err != nil {
		return stale(ch.c.ID + "'s merge commit is no longer on the default branch")
	}
	ch.sha = sha
	return nil
}

var branchRE = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)

// defaultBranch is origin's HEAD branch, as origin says it now.
func defaultBranch(ctx context.Context, git Git, top string) (string, error) {
	out, err := git(ctx, top, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", fmt.Errorf("coordination checkout: asking origin for its default branch: %w", err)
	}
	for _, l := range strings.Split(out, "\n") {
		if ref, ok := strings.CutPrefix(l, "ref: refs/heads/"); ok {
			name, _, _ := strings.Cut(ref, "\t")
			if branchRE.MatchString(name) && !strings.Contains(name, "..") {
				return name, nil
			}
		}
	}
	return "", errors.New("coordination checkout: origin names no default branch")
}

// reuse answers a root that exists: reused when its manifest is exactly this document and every
// worktree is still at its commit; refused otherwise, and never changed.
func (c CoordinationCheckout) reuse(ctx context.Context, git Git, out CoordinationCheckedOut, children []coordChild,
	want coordinationview.CheckoutDocument) (CoordinationCheckedOut, error) {
	have, err := readManifest(out.ManifestPath)
	if err != nil {
		return out, refuse(OneLine(out.Root)+" exists without a complete checkout manifest: an earlier checkout did not finish",
			"check what it holds, then move it away")
	}
	if coordinationview.ManifestHash(have) != coordinationview.ManifestHash(want) {
		return out, refuse(fmt.Sprintf("checkout_stale: %s holds plan v%d, and the plan or a head has changed since", OneLine(out.Root), have.PlanVersion),
			"move "+OneLine(out.Root)+" away to check the current plan out")
	}
	for _, ch := range children {
		ch.sha = ch.c.HeadSHA + ch.c.MergeCommitSHA
		if head, err := revParse(ctx, git, ch.path, "HEAD"); err != nil || head != ch.sha {
			return out, refuse(OneLine(ch.path)+" is no longer at "+ch.sha[:12], "move "+OneLine(out.Root)+" away to check the plan out again")
		}
		out.Children = append(out.Children, CoordinationWorktree{ChildID: ch.c.ID, Repository: ch.c.Repository, Path: ch.path, SHA: ch.sha, Reused: true})
	}
	out.Reused = true
	return out, nil
}

// absolute is d with each child's worktree its absolute path: the manifest as written.
func absolute(d coordinationview.CheckoutDocument, children []coordChild) coordinationview.CheckoutDocument {
	d.Children = slices.Clone(d.Children)
	for i := range d.Children {
		d.Children[i].Worktree = children[i].path
	}
	return d
}

func readManifest(path string) (coordinationview.CheckoutDocument, error) {
	var d coordinationview.CheckoutDocument
	f, err := os.Open(path) //nolint:gosec // the root's own manifest, at a path this call built
	if err != nil {
		return d, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxManifestBytes+1))
	if err != nil || len(raw) > maxManifestBytes {
		return d, errors.New("manifest unreadable")
	}
	return d, json.Unmarshal(raw, &d)
}

// writeManifest writes d beside the worktrees through a new temporary file and a rename, so a
// reader never sees half a manifest.
func writeManifest(path string, d coordinationview.CheckoutDocument) error {
	raw, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // a manifest, read by the developer's tools
	if err != nil {
		return err
	}
	if _, err := f.Write(append(raw, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// incomplete is a failure after the root was made: it names the root and what is in it, and leaves
// both for the developer.
func incomplete(root string, made []CoordinationWorktree, cause error) error {
	names := make([]string, 0, len(made))
	for _, w := range made {
		names = append(names, w.ChildID)
	}
	return fmt.Errorf("coordination checkout: %s is incomplete (made: %s; no manifest): %w; check it, then move it away",
		OneLine(root), strings.Join(names, ", "), cause)
}

// validCheckoutDocument refuses, before any git call, a document whose ids, rooms or commits could
// name a path or a ref this call does not build.
func validCheckoutDocument(d coordinationview.CheckoutDocument) error {
	bad := func(why string) error { return refuse("the checkout document is malformed: "+why, "") }
	switch {
	case d.APIVersion != coordinationview.CheckoutAPIVersion || d.Kind != coordinationview.CheckoutKind:
		return bad("unknown version")
	case !coordRoomRE.MatchString(d.ParentTask):
		return bad("parent task")
	case len(d.Children) == 0:
		return bad("no child")
	}
	seen := map[string]bool{}
	for _, ch := range d.Children {
		switch {
		case !coordIDRE.MatchString(ch.ID) || seen[ch.ID]:
			return bad("child id")
		case !coordRoomRE.MatchString(ch.Room):
			return bad("room of " + ch.ID)
		case !repoRE.MatchString(ch.Repository):
			return bad("repository of " + ch.ID)
		case (ch.HeadSHA == "") == (ch.MergeCommitSHA == "") || !shaRE.MatchString(ch.HeadSHA+ch.MergeCommitSHA):
			return bad("commit of " + ch.ID)
		}
		seen[ch.ID] = true
	}
	return nil
}

// ValidRepository is whether s is an owner/name, as a checkout's --workspace names one.
func ValidRepository(s string) bool { return repoRE.MatchString(s) }

// coordination_checkout's tools/list entry: closed input, and nothing in it confirms, files, merges
// or recovers.
var coordinationCheckoutEntry = map[string]any{
	"name": toolCoordinationCheckout,
	"description": "Check every child of a coordination parent out, each in a detached git worktree at the exact commit the " +
		"parent names (an open pull request's head, or a landed merge), under one folder beside the developer's repository, " +
		"with maku-coordination.json describing the family. The workspace is the primary repository's clone; workspaces " +
		"maps each other repository (owner/name) to its clone. All or nothing: a child the developer cannot read, a moved " +
		"head or a changed plan refuses the whole family. It never touches a clone's checkout or branches, and asks the " +
		"developer in one form before files that run code. It confirms, files, merges and recovers nothing.",
	"inputSchema": json.RawMessage(`{"type":"object","additionalProperties":false,"required":["parent"],"properties":{` +
		`"parent":{"type":"string","description":"the coordination parent's room id"},` +
		`"workspaces":{"type":"object","maxProperties":1,"additionalProperties":{"type":"string"},` +
		`"description":"owner/name to the local clone of each non-primary repository"}}}`),
}

// coordinationCheckout is coordination_checkout. The family comes from the broker's checkout
// document, never from the agent's text; the developer's yes comes from a form.
func (r *Relay) coordinationCheckout(ctx context.Context, c localCall) any {
	var a struct {
		Parent     string            `json:"parent"`
		Workspaces map[string]string `json:"workspaces"`
	}
	if mcpwire.DecodeArgs(c.Args, &a) != nil || !coordRoomRE.MatchString(a.Parent) || len(a.Workspaces) > 1 {
		return mcpwire.ToolError("invalid_arguments: parent is a room id, workspaces at most one owner/name to a path")
	}
	for repo, path := range a.Workspaces {
		if !repoRE.MatchString(repo) || path == "" {
			return mcpwire.ToolError("invalid_arguments: workspaces maps owner/name to a path")
		}
	}
	doc, err := Client{URL: r.URL, HC: r.HC, Token: r.Token}.CoordinationCheckout(ctx, a.Parent)
	if err != nil {
		return mcpwire.ToolError("not_checked_out: " + OneLine(explainRelay(err)))
	}
	dir, err := r.workspace(ctx)
	if err != nil {
		return mcpwire.ToolError("not_checked_out: " + OneLine(err.Error()))
	}
	r.mu.Lock()
	forms := r.forms
	r.mu.Unlock()
	co := CoordinationCheckout{Git: r.Git, Workspace: dir, Workspaces: a.Workspaces, Document: doc}
	if forms {
		co.Confirm = func(ctx context.Context, files []string, more int) (bool, error) {
			return r.confirmForm(ctx, fmt.Sprintf("Check out every child of coordination parent %s into new worktrees? Its branches change "+
				"files that run code on your machine when a tool opens those folders or you commit there. Nothing runs now.\n\n%s",
				a.Parent, "- "+strings.Join(CodeSpans(files, more), "\n- ")), "Create the worktrees")
		}
	}
	got, err := co.Run(ctx)
	if err != nil {
		if ref, ok := errors.AsType[*CheckoutRefusal](err); ok {
			return mcpwire.ToolError("refused: " + OneLine(ref.Error()))
		}
		return mcpwire.ToolError("not_checked_out: " + OneLine(err.Error()))
	}
	out, _ := mcpwire.ToolResult(got)
	return out
}
