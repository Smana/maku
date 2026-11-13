// SPDX-License-Identifier: Apache-2.0

package gitbundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/Smana/maku/internal/hardening"
)

// Kind is how a push lands (HR25).
type Kind string

// The kinds of push.
const (
	// FastForward: the new head descends from the branch's current head.
	FastForward Kind = "fast_forward"
	// Rebase: the branch is absent, or the new head descends from the base's current head (and not
	// the branch's).
	Rebase Kind = "rebase"
)

// File is one changed entry: Status is git's raw letter (A, M, D, T), Mode is the new mode
// ("100644", "100755", "120000", "160000"), the old mode for a deletion.
type File struct{ Path, Status, Mode string }

// Stats is a change's size: files changed, and lines added plus deleted (binary files count 0
// lines).
type Stats struct{ Files, Lines int }

// Inspection is what a bundle would change on the remote.
type Inspection struct {
	// outRef holds the bundle's objects in the mirror until Push, or a refusal, deletes it.
	outRef   string
	Ref      string // the one ref, refs/heads/<branch>
	Head     string // the bundle's head SHA (40 or 64 hex)
	OldHead  string // the branch's head on the remote when read, "" when absent: the lease
	BaseHead string // the base branch's head on the remote when read
	Kind     Kind
	// Files is OldHead..Head for FastForward, merge-base(BaseHead, Head)..Head for Rebase,
	// deletions included.
	Files []File
	// Added is the added lines of Files' diff (no "+" prefix), then each new commit's message.
	Added []string
	// Cumulative is merge-base(BaseHead, Head)..Head, always.
	Cumulative Stats
}

// Refused is a bundle the inspection refuses. Reason is one of hardening.RefusePushRef,
// RefusePushHistory, RefusePushTree, RefusePushSize. Detail is bounded, never bundle content (a ref
// name or a path may appear, cut at 200 bytes).
type Refused struct {
	Reason hardening.Refusal
	Detail string
}

func (r *Refused) Error() string {
	if r.Detail == "" {
		return "gitbundle: refused: " + string(r.Reason)
	}
	return "gitbundle: refused: " + string(r.Reason) + ": " + r.Detail
}

func refuse(reason hardening.Refusal, detail string) *Refused {
	return &Refused{Reason: reason, Detail: cut(detail)}
}

// Inspect checks bundle as a push of exactly refs/heads/<branch> onto base (e.g. "agent/t-1",
// "main"), and returns what it would change. A *Refused error is the bundle's fault; any other
// error is the factory's (retry later). It holds the mirror's lock for its whole run.
func (m *Mirror) Inspect(ctx context.Context, bundle []byte, branch, base string) (Inspection, error) {
	if !validBranch(branch) || !validBranch(base) {
		return Inspection{}, fmt.Errorf("gitbundle: invalid branch %q or base %q", cut(branch), cut(base))
	}
	if len(bundle) > hardening.MaxBundle {
		return Inspection{}, refuse(hardening.RefusePushSize, "bundle of "+strconv.Itoa(len(bundle))+" bytes")
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	tok, err := m.cache.token(ctx, m.repo)
	if err != nil {
		return Inspection{}, err
	}
	var in Inspection
	err = m.withMirror(ctx, func() error {
		var opErr error
		in, opErr = m.inspect(ctx, tok, bundle, branch, base)
		return opErr
	})
	if err != nil {
		return Inspection{}, err
	}
	return in, nil
}

func (m *Mirror) inspect(ctx context.Context, tok string, bundle []byte, branch, base string) (Inspection, error) {
	ref := "refs/heads/" + branch
	f, err := os.CreateTemp(m.dir, "bundle-*.tmp")
	if err != nil {
		return Inspection{}, fmt.Errorf("gitbundle: bundle file: %w", err)
	}
	file := f.Name()
	defer func() { _ = os.Remove(file) }()
	_, err = f.Write(bundle)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return Inspection{}, fmt.Errorf("gitbundle: bundle file: %w", err)
	}

	// The one ref.
	out, err := m.run(ctx, "", nil, "bundle", "list-heads", file)
	if err != nil {
		if ferr := m.factoryErr(ctx, err, ""); ferr != nil {
			return Inspection{}, ferr
		}
		return Inspection{}, refuse(hardening.RefusePushRef, "unreadable bundle")
	}
	head, err := oneHead(out, ref)
	if err != nil {
		return Inspection{}, err
	}

	oldHead, baseHead, err := m.sync(ctx, tok, branch, base)
	if err != nil {
		return Inspection{}, err
	}

	// The history the bundle needs is the mirror's, with no lazy fetch for a prerequisite.
	noLazy := []string{"GIT_NO_LAZY_FETCH=1"}
	if _, err := m.run(ctx, "", noLazy, "bundle", "verify", "--quiet", file); err != nil {
		if ferr := m.factoryErr(ctx, err, baseHead); ferr != nil {
			return Inspection{}, ferr
		}
		// The header's prerequisites, not git's wording, say which failure it is.
		for _, p := range prerequisites(bundle) {
			if _, err := m.run(ctx, "", noLazy, "cat-file", "-e", p+"^{commit}"); err != nil {
				return Inspection{}, refuse(hardening.RefusePushHistory, "missing prerequisite commits")
			}
		}
		return Inspection{}, refuse(hardening.RefusePushTree, "bundle failed verification")
	}

	// The objects, under fsck, into a ref of their own.
	sum := sha256.Sum256(bundle)
	outRef := "refs/maku/out/" + hex.EncodeToString(sum[:])[:16]
	if _, err := m.fetchBundle(ctx, tok, file, "+"+ref+":"+outRef); err != nil {
		if ferr := m.factoryErr(ctx, err, baseHead); ferr != nil {
			return Inspection{}, ferr
		}
		return Inspection{}, refuse(hardening.RefusePushTree, "objects failed fsck")
	}
	in, err := m.inspectFetched(ctx, tok, ref, outRef, head, oldHead, baseHead, branch, base)
	var refused *Refused
	if errors.As(err, &refused) {
		m.dropOut(ctx, outRef)
	}
	return in, err
}

// dropOut deletes the ref a refused or pushed bundle's objects were fetched into; its objects go
// with the mirror's size limit.
func (m *Mirror) dropOut(ctx context.Context, outRef string) {
	if outRef != "" {
		_, _ = m.run(ctx, "", nil, "update-ref", "-d", outRef)
	}
}

// inspectFetched is the inspection of a bundle fetched into outRef.
func (m *Mirror) inspectFetched(ctx context.Context, tok, ref, outRef, head, oldHead, baseHead, branch, base string) (Inspection, error) {
	got, err := m.revParse(ctx, outRef)
	var ge *gitError
	if errors.As(err, &ge) {
		return Inspection{}, refuse(hardening.RefusePushRef, "bundle head is not a commit")
	}
	if err != nil {
		return Inspection{}, err
	}
	if got != head {
		return Inspection{}, refuse(hardening.RefusePushRef, "bundle head is not a commit")
	}

	in := Inspection{Ref: ref, Head: head, OldHead: oldHead, BaseHead: baseHead, outRef: outRef}
	from := ""
	switch {
	case oldHead != "" && oldHead == head:
		in.Kind, from = FastForward, oldHead
	case oldHead != "":
		ok, err := m.isAncestor(ctx, oldHead, head)
		if err != nil {
			return Inspection{}, err
		}
		if ok {
			in.Kind, from = FastForward, oldHead
		}
	}
	if in.Kind == "" {
		ok, err := m.isAncestor(ctx, baseHead, head)
		if err != nil {
			return Inspection{}, err
		}
		if !ok {
			return Inspection{}, refuse(hardening.RefusePushHistory, "head descends from neither "+branch+" nor "+base)
		}
		in.Kind = Rebase
	}
	mergeBase, err := m.mergeBase(ctx, baseHead, head)
	if err != nil {
		return Inspection{}, err
	}
	if in.Kind == Rebase {
		from = mergeBase
	}

	// Every blob either range changes is sized by its header before any diff reads one (a highly
	// compressible blob never inflates), and no range may change a binary file: T3 and the caps read
	// text only.
	for _, r := range [][2]string{{from, head}, {mergeBase, head}} {
		if err := m.checkBlobs(ctx, tok, r[0], r[1]); err != nil {
			return Inspection{}, err
		}
	}
	if in.Files, err = m.files(ctx, tok, from, head, head); err != nil {
		return Inspection{}, err
	}
	if _, err := m.stats(ctx, tok, from, head); err != nil { // the incremental range's binaries
		return Inspection{}, err
	}
	if in.Added, err = m.added(ctx, tok, from, head); err != nil {
		return Inspection{}, err
	}
	if in.Cumulative, err = m.stats(ctx, tok, mergeBase, head); err != nil {
		return Inspection{}, err
	}
	return in, nil
}

// factoryErr is err when a failed call on the bundle is the factory's own (a timeout, a cancelled
// context, a git that could not start, or a mirror that no longer answers), nil when it is git's
// answer about the bundle. A hostile bundle's errors may read like corruption, so the mirror is
// probed rather than the message trusted: a mirror failing the probe is broken (re-created once).
func (m *Mirror) factoryErr(ctx context.Context, err error, baseHead string) error {
	var ge *gitError
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.Is(err, errOutputCap) || !errors.As(err, &ge) {
		return err
	}
	if _, perr := m.run(ctx, "", nil, "rev-parse", "--git-dir"); perr != nil {
		return fmt.Errorf("%w: %w", errMirrorBroken, err)
	}
	if baseHead != "" {
		if _, perr := m.run(ctx, "", []string{"GIT_NO_LAZY_FETCH=1"}, "cat-file", "-e", baseHead+"^{tree}"); perr != nil {
			return fmt.Errorf("%w: %w", errMirrorBroken, err)
		}
	}
	return nil
}

// oneHead parses `bundle list-heads`: exactly one line, naming ref.
func oneHead(out []byte, ref string) (string, error) {
	var names []string
	head := ""
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		sha, name, _ := strings.Cut(line, " ")
		names = append(names, name)
		if name == ref && isSHA(sha) {
			head = sha
		}
	}
	if len(names) != 1 || head == "" {
		if len(names) == 0 {
			return "", refuse(hardening.RefusePushRef, "no ref")
		}
		return "", refuse(hardening.RefusePushRef, "refs "+strings.Join(names, ", "))
	}
	return head, nil
}

// fetchBundle fetches refspec from the bundle file under fsck. git counts a bundle file as the
// "file" transport, which the allow-list refuses: this one call allows it, for the factory's own
// temporary file (lazy fetches it spawns still go to origin, the mirror's configured remote).
func (m *Mirror) fetchBundle(ctx context.Context, tok, file, refspec string) ([]byte, error) {
	return m.runCfg(ctx, tok, nil, []string{"protocol.file.allow=always"},
		"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules", "--", file, refspec)
}

// prerequisites are the SHAs of the bundle header's "-<sha>" lines (git has already parsed the
// header by then; this reads at most its first 64 KiB, and only well-formed SHAs).
func prerequisites(bundle []byte) []string {
	header := bundle[:min(len(bundle), 64<<10)]
	if end := bytes.Index(header, []byte("\n\n")); end >= 0 {
		header = header[:end]
	}
	var shas []string
	for _, line := range strings.Split(string(header), "\n") {
		rest, ok := strings.CutPrefix(line, "-")
		if !ok {
			continue
		}
		sha, _, _ := strings.Cut(rest, " ")
		if isSHA(sha) {
			shas = append(shas, sha)
		}
	}
	return shas
}

// isAncestor is `merge-base --is-ancestor a b`.
func (m *Mirror) isAncestor(ctx context.Context, a, b string) (bool, error) {
	_, err := m.run(ctx, "", nil, "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	var ge *gitError
	if errors.As(err, &ge) && ge.code == 1 {
		return false, nil
	}
	return false, err
}

// mergeBase is merge-base(a, b); none is push_history.
func (m *Mirror) mergeBase(ctx context.Context, a, b string) (string, error) {
	out, err := m.run(ctx, "", nil, "merge-base", a, b)
	if err != nil {
		var ge *gitError
		if errors.As(err, &ge) && ge.code == 1 {
			return "", refuse(hardening.RefusePushHistory, "no merge base")
		}
		return "", err
	}
	sha := strings.TrimSpace(string(out))
	if !isSHA(sha) {
		return "", fmt.Errorf("gitbundle: merge-base: not a sha")
	}
	return sha, nil
}

// readChange runs a git call that reads the change. Builder's choice: an output over the cap is
// push_size, as a change too large to read is too large to push.
func (m *Mirror) readChange(ctx context.Context, tok string, args ...string) ([]byte, error) {
	out, err := m.run(ctx, tok, nil, args...)
	if errors.Is(err, errOutputCap) {
		return nil, refuse(hardening.RefusePushSize, "change too large to read")
	}
	return out, err
}

// checkBlobs refuses a change from..to with a blob, old or new, over MaxBlob (push_size), sized by
// cat-file --batch-check from its header: never inflated.
func (m *Mirror) checkBlobs(ctx context.Context, tok, from, to string) error {
	out, err := m.readChange(ctx, tok, "diff", "--raw", "-z", "--no-renames", "--no-abbrev", from, to, "--")
	if err != nil {
		return err
	}
	var shas []string
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		parts := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(parts) != 5 {
			return fmt.Errorf("gitbundle: diff --raw: unexpected entry")
		}
		for j, sha := range parts[2:4] {
			mode := parts[j]
			if isSHA(sha) && strings.Trim(sha, "0") != "" && mode != "160000" && mode != "040000" {
				shas = append(shas, sha)
			}
		}
	}
	if len(shas) == 0 {
		return nil
	}
	sizes, err := m.runIn(ctx, tok, nil, nil, []byte(strings.Join(shas, "\n")+"\n"),
		"cat-file", "--batch-check=%(objecttype) %(objectsize)")
	if errors.Is(err, errOutputCap) {
		return refuse(hardening.RefusePushSize, "change too large to read")
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(string(sizes)), "\n") {
		typ, size, _ := strings.Cut(line, " ")
		n, err := strconv.ParseInt(size, 10, 64)
		if typ != "blob" || err != nil {
			return fmt.Errorf("gitbundle: cat-file --batch-check: unexpected entry")
		}
		if n > MaxBlob {
			return refuse(hardening.RefusePushSize, "a blob of "+size+" bytes")
		}
	}
	return nil
}

// files is the raw diff from..to, refusing a submodule, an escaping symlink or a .git path. tree is
// the commit whose tree a symlink's target is resolved in.
func (m *Mirror) files(ctx context.Context, tok, from, to, tree string) ([]File, error) {
	out, err := m.readChange(ctx, tok, "diff", "--raw", "-z", "--no-renames", "--no-abbrev",
		"--no-ext-diff", "--no-textconv", from, to, "--")
	if err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	var files []File
	for i := 0; i+1 < len(fields); i += 2 {
		meta, p := fields[i], fields[i+1]
		parts := strings.Fields(strings.TrimPrefix(meta, ":"))
		if len(parts) != 5 {
			return nil, fmt.Errorf("gitbundle: diff --raw: unexpected entry")
		}
		oldMode, newMode, newSHA, status := parts[0], parts[1], parts[3], parts[4]
		if oldMode == "160000" || newMode == "160000" {
			return nil, refuse(hardening.RefusePushTree, "submodule "+p)
		}
		if hasDotGit(p) {
			return nil, refuse(hardening.RefusePushTree, "path "+p)
		}
		mode := newMode
		if status == "D" {
			mode = oldMode
		} else if newMode == "120000" {
			if err := m.checkSymlink(ctx, tok, tree, p, newSHA); err != nil {
				return nil, err
			}
		}
		files = append(files, File{Path: p, Status: status, Mode: mode})
	}
	return files, nil
}

// checkSymlink refuses a symlink at p whose target is absolute, has a ".." component, resolves
// outside the root or into .git, or names another symlink in tree: no chain can lead out.
func (m *Mirror) checkSymlink(ctx context.Context, tok, tree, p, sha string) error {
	if !isSHA(sha) {
		return fmt.Errorf("gitbundle: diff --raw: not a sha")
	}
	out, err := m.run(ctx, tok, nil, "cat-file", "blob", sha)
	if errors.Is(err, errOutputCap) {
		return refuse(hardening.RefusePushTree, "symlink "+p)
	}
	if err != nil {
		return err
	}
	target := string(out)
	if target == "" || strings.HasPrefix(target, "/") || strings.ContainsRune(target, 0) {
		return refuse(hardening.RefusePushTree, "symlink "+p)
	}
	for _, seg := range strings.Split(target, "/") {
		if seg == ".." {
			return refuse(hardening.RefusePushTree, "symlink "+p)
		}
	}
	resolved := path.Clean(path.Join(path.Dir(p), target))
	if resolved == ".." || strings.HasPrefix(resolved, "../") || hasDotGit(resolved) {
		return refuse(hardening.RefusePushTree, "symlink "+p)
	}
	entry, err := m.run(ctx, "", nil, "ls-tree", "--full-tree", "-z", tree, "--", resolved)
	if err != nil {
		return err
	}
	if strings.HasPrefix(string(entry), "120000 ") {
		return refuse(hardening.RefusePushTree, "symlink "+p+" to a symlink")
	}
	return nil
}

// hasDotGit reports whether p has a ".git" component, in any case.
func hasDotGit(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.EqualFold(seg, ".git") {
			return true
		}
	}
	return false
}

// added is the added lines of the diff from..to, then each new commit's message.
func (m *Mirror) added(ctx context.Context, tok, from, to string) ([]string, error) {
	out, err := m.readChange(ctx, tok, "diff", "-U0", "--no-color", "--no-ext-diff", "--no-textconv", from, to, "--")
	if err != nil {
		return nil, err
	}
	var added []string
	inHunk := false
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case inHunk && strings.HasPrefix(line, "+"):
			added = append(added, line[1:])
		}
	}
	if from == to {
		return added, nil
	}
	logOut, err := m.readChange(ctx, tok, "log", "--no-color", "-z", "--format=%B", from+".."+to, "--")
	if err != nil {
		return nil, err
	}
	for _, msg := range bytes.Split(logOut, []byte{0}) {
		if s := strings.TrimSpace(string(msg)); s != "" {
			added = append(added, s)
		}
	}
	return added, nil
}

// stats is the numstat of from..to.
func (m *Mirror) stats(ctx context.Context, tok, from, to string) (Stats, error) {
	out, err := m.readChange(ctx, tok, "diff", "--numstat", "-z", "--no-renames", "--no-ext-diff", "--no-textconv", from, to, "--")
	if err != nil {
		return Stats{}, err
	}
	var st Stats
	for _, rec := range strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 {
			return Stats{}, fmt.Errorf("gitbundle: diff --numstat: unexpected entry")
		}
		st.Files++
		for _, n := range parts[:2] {
			if n == "-" { // a binary file: T3 and the line caps read text only
				return Stats{}, refuse(hardening.RefusePushTree, "binary file "+parts[2])
			}
			v, err := strconv.Atoi(n)
			if err != nil {
				return Stats{}, fmt.Errorf("gitbundle: diff --numstat: %w", err)
			}
			st.Lines += v
		}
	}
	return st, nil
}
