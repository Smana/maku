// SPDX-License-Identifier: Apache-2.0

package gitbundle

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Push pushes in.Head to in.Ref with --force-with-lease=<ref>:<in.OldHead> (an empty OldHead: the
// ref must be absent). A remote already at in.Head is success with nothing pushed only when the
// inspection read it there (HR26); one that moved there since, by another hand, is ErrLeaseLost, as
// is any other move. It holds the mirror's lock. The head's objects are the mirror's,
// from the Inspect that returned in.
func (m *Mirror) Push(ctx context.Context, in Inspection) error {
	branch, ok := strings.CutPrefix(in.Ref, "refs/heads/")
	if !ok || !validBranch(branch) || !isSHA(in.Head) || (in.OldHead != "" && !isSHA(in.OldHead)) {
		return fmt.Errorf("gitbundle: push: invalid inspection")
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	tok, err := m.cache.token(ctx, m.repo)
	if err != nil {
		return err
	}
	return m.withMirror(ctx, func() error { return m.push(ctx, tok, in) })
}

func (m *Mirror) push(ctx context.Context, tok string, in Inspection) error {
	current, err := m.lsRemote(ctx, tok, in.Ref)
	if err != nil {
		return err
	}
	if current == in.Head && in.OldHead == in.Head {
		m.dropOut(ctx, in.outRef)
		return nil // HR26: the inspection read the head there already
	}
	if current != in.OldHead {
		return ErrLeaseLost
	}
	if _, err := m.run(ctx, "", []string{"GIT_NO_LAZY_FETCH=1"}, "cat-file", "-e", in.Head+"^{commit}"); err != nil {
		return fmt.Errorf("gitbundle: push: head not in the mirror, inspect again: %w", err)
	}
	out, err := m.run(ctx, tok, nil, "push", "--porcelain", "--no-verify", "--no-recurse-submodules",
		"--force-with-lease="+in.Ref+":"+in.OldHead, "origin", in.Head+":"+in.Ref)
	if err == nil {
		m.dropOut(ctx, in.outRef)
		return nil
	}
	var ge *gitError
	if errors.As(err, &ge) {
		s := strings.ToLower(string(out) + "\n" + ge.all)
		if strings.Contains(s, "(stale info)") {
			return ErrLeaseLost
		}
		if strings.Contains(s, "[remote rejected]") || strings.Contains(s, "returned error: 403") ||
			strings.Contains(s, "permission denied") || (strings.Contains(s, "permission to") && strings.Contains(s, "denied")) {
			return fmt.Errorf("%w: %s", ErrPushRejected, ge.line)
		}
	}
	return err
}

// ErrPushRejected is a push the remote refused for good, not for the lease: a "[remote rejected]"
// ref (a pre-receive hook, a missing workflows permission) or a 403 / permission denied.
var ErrPushRejected = errors.New("gitbundle: the remote refused the push")
