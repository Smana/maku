// SPDX-License-Identifier: Apache-2.0

// Package demo is what the factory runs only in demo mode, on a laptop cluster with one GitHub App.
package demo

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
	// SecretName is the runs' shared git Secret: the run-controller's default gitSecret.
	SecretName = "agent-git"
	// SecretKey is the key the run-controller reads the token from.
	SecretKey = "token"
	// ExpiresAnnotation carries the token's expiry (RFC 3339).
	ExpiresAnnotation = "maku.ogenki.io/expires-at"

	defaultEvery = 30 * time.Minute
	// refreshWithin rewrites the token once it has less than this to live.
	refreshWithin = 10 * time.Minute
	retryAfter    = time.Minute
)

// Permissions is the demo token's scope, in one place: the runs push branches and open pull
// requests as the factory's own App.
func Permissions() map[string]string {
	return map[string]string{"contents": "write", "pull_requests": "write", "issues": "write",
		"metadata": "read", "statuses": "write"}
}

// GitToken keeps the runs' git Secret filled with a fresh installation token minted from the
// factory's App. A mint failure keeps the old Secret.
type GitToken struct {
	Client    client.Client
	Mint      func(context.Context) (forge.Token, error)
	Namespace string
	Name      string        // SecretName when empty
	Every     time.Duration // defaultEvery when zero
	Clock     func() time.Time
	Log       *slog.Logger
}

// NeedLeaderElection is true: only the leader writes the Secret.
func (g *GitToken) NeedLeaderElection() bool { return true }

func (g *GitToken) name() string {
	if g.Name == "" {
		return SecretName
	}
	return g.Name
}

func (g *GitToken) every() time.Duration {
	if g.Every <= 0 {
		return defaultEvery
	}
	return g.Every
}

func (g *GitToken) now() time.Time {
	if g.Clock != nil {
		return g.Clock()
	}
	return time.Now()
}

func (g *GitToken) log() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.New(slog.DiscardHandler)
}

// Start writes the Secret, then rewrites it every Every and before its token is within ten
// minutes of expiry, until ctx ends. A Secret left by a previous leader that is still fresh is
// kept until it needs renewing.
func (g *GitToken) Start(ctx context.Context) error {
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
func (g *GitToken) existingWait(ctx context.Context) time.Duration {
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
func (g *GitToken) tick(ctx context.Context) time.Duration {
	tok, err := g.Mint(ctx)
	if err != nil {
		g.log().Error("demo git token: mint failed, keeping the current Secret", "error", err.Error())
		return retryAfter
	}
	if err := g.write(ctx, tok); err != nil {
		g.log().Error("demo git token: write failed", "error", err.Error())
		return retryAfter
	}
	return min(g.every(), max(retryAfter, tok.ExpiresAt.Add(-refreshWithin).Sub(g.now())))
}

func (g *GitToken) write(ctx context.Context, tok forge.Token) error {
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
