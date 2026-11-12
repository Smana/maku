// SPDX-License-Identifier: Apache-2.0

// Package runtoken keeps the run-controller's two git Secrets filled with installation tokens: a
// write token (agent-git) for implementer and tester runs, a read token (agent-git-read) for
// reviewer and triager runs. The demo mints them from the factory's App; runTokens.enabled mints
// them from the App it names. Each token covers every served repository of one owner.
//
// Interim: replaced by the per-run minter, v4 D10 (rollout step 3).
package runtoken

import (
	"context"
	"log/slog"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/Smana/maku/internal/factory/forge"
)

const (
	// WriteSecret is the run-controller's default gitSecret, ReadSecret its default gitReadSecret.
	WriteSecret = "agent-git"
	ReadSecret  = "agent-git-read"
	// SecretKey is the key the run-controller reads the token from.
	SecretKey = "token"
	// ExpiresAnnotation carries the token's expiry (RFC 3339).
	ExpiresAnnotation = "maku.ogenki.io/expires-at"

	defaultEvery = 30 * time.Minute
	// refreshWithin rewrites the token once it has less than this to live.
	refreshWithin = 10 * time.Minute
	retryAfter    = time.Minute
)

// Write is the write token's scope, in one place: the runs push branches and open pull requests.
// It has no statuses: write: in the demo one App backs the factory, the merger and every run, and a
// run that could post a status could forge the gate's maku/merge-gate.
func Write() map[string]string {
	return map[string]string{"contents": "write", "pull_requests": "write", "metadata": "read"}
}

// Read is the read token's scope: what a reviewer or a triager reads (the code, the pull request,
// the issue, its checks and workflow runs), and nothing it could write.
func Read() map[string]string {
	return map[string]string{"contents": "read", "pull_requests": "read", "issues": "read", "checks": "read", "actions": "read", "metadata": "read"}
}

// Writers are the two Secrets' writers in namespace: mint is called with each one's scope.
func Writers(c client.Client, namespace string, mint func(context.Context, map[string]string) (forge.Token, error), log *slog.Logger) []*Writer {
	w := func(name string, perms func() map[string]string) *Writer {
		return &Writer{Client: c, Namespace: namespace, Name: name, Log: log,
			Mint: func(ctx context.Context) (forge.Token, error) { return mint(ctx, perms()) }}
	}
	return []*Writer{w(WriteSecret, Write), w(ReadSecret, Read)}
}

// Writer keeps one git Secret filled with a fresh installation token. A mint failure keeps the old
// Secret.
type Writer struct {
	Client    client.Client
	Mint      func(context.Context) (forge.Token, error)
	Namespace string
	Name      string        // WriteSecret when empty
	Every     time.Duration // defaultEvery when zero
	Clock     func() time.Time
	Log       *slog.Logger
}

// NeedLeaderElection is true: only the leader writes the Secret.
func (g *Writer) NeedLeaderElection() bool { return true }

func (g *Writer) name() string {
	if g.Name == "" {
		return WriteSecret
	}
	return g.Name
}

func (g *Writer) every() time.Duration {
	if g.Every <= 0 {
		return defaultEvery
	}
	return g.Every
}

func (g *Writer) now() time.Time {
	if g.Clock != nil {
		return g.Clock()
	}
	return time.Now()
}

func (g *Writer) log() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Start writes the Secret, then rewrites it every Every and before its token is within ten
// minutes of expiry, until ctx ends. A Secret left by a previous leader that is still fresh is
// kept until it needs renewing.
func (g *Writer) Start(ctx context.Context) error {
	wait := g.existingWait(ctx)
	for {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
		}
		wait = g.tick(ctx)
		if ctx.Err() != nil {
			return nil
		}
	}
}

// existingWait is how long the current Secret can stay as it is; zero when absent, unreadable or due.
func (g *Writer) existingWait(ctx context.Context) time.Duration {
	var s corev1.Secret
	if err := g.Client.Get(ctx, client.ObjectKey{Namespace: g.Namespace, Name: g.name()}, &s); err != nil {
		return 0
	}
	exp, err := time.Parse(time.RFC3339, s.Annotations[ExpiresAnnotation])
	if err != nil || len(s.Data[SecretKey]) == 0 {
		return 0
	}
	return min(g.every(), max(0, exp.Add(-refreshWithin).Sub(g.now())))
}

// tick mints and writes one token and returns how long to wait for the next: Every, shortened to
// ten minutes before this token expires; a minute after a failure.
func (g *Writer) tick(ctx context.Context) time.Duration {
	tok, err := g.Mint(ctx)
	if err != nil {
		g.log().Error("run token: mint failed, keeping the current Secret", "secret", g.name(), "error", err.Error())
		return retryAfter
	}
	if err := g.write(ctx, tok); err != nil {
		g.log().Error("run token: write failed", "secret", g.name(), "error", err.Error())
		return retryAfter
	}
	return min(g.every(), max(retryAfter, tok.ExpiresAt.Add(-refreshWithin).Sub(g.now())))
}

func (g *Writer) write(ctx context.Context, tok forge.Token) error {
	key := client.ObjectKey{Namespace: g.Namespace, Name: g.name()}
	var s corev1.Secret
	err := g.Client.Get(ctx, key, &s)
	missing := apierrors.IsNotFound(err)
	if err != nil && !missing {
		return err
	}
	if missing {
		s = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}}
	}
	if s.Labels == nil {
		s.Labels = map[string]string{}
	}
	if s.Annotations == nil {
		s.Annotations = map[string]string{}
	}
	s.Labels["app.kubernetes.io/managed-by"] = "maku-factory"
	s.Annotations[ExpiresAnnotation] = tok.ExpiresAt.UTC().Format(time.RFC3339)
	s.Data = map[string][]byte{SecretKey: []byte(tok.Value)}
	if missing {
		return g.Client.Create(ctx, &s)
	}
	return g.Client.Update(ctx, &s)
}
