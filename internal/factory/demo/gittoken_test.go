// SPDX-License-Identifier: Apache-2.0

package demo

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Smana/maku/internal/factory/forge"
)

type clk struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clk) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

type rig struct {
	g    *GitToken
	c    client.Client
	clk  *clk
	mu   sync.Mutex
	n    int
	fail bool
	ttl  time.Duration
}

func newRig() *rig {
	r := &rig{c: fake.NewClientBuilder().Build(), clk: &clk{t: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)}, ttl: time.Hour}
	r.g = &GitToken{Client: r.c, Namespace: "runs", Name: "agent-git", Clock: r.clk.now,
		Mint: func(context.Context) (forge.Token, error) {
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.fail {
				return forge.Token{}, errors.New("boom")
			}
			r.n++
			return forge.Token{Value: "tok" + string(rune('0'+r.n)), ExpiresAt: r.clk.now().Add(r.ttl)}, nil
		}}
	return r
}

func (r *rig) secret(t *testing.T) *corev1.Secret {
	t.Helper()
	var s corev1.Secret
	if err := r.c.Get(t.Context(), client.ObjectKey{Namespace: "runs", Name: "agent-git"}, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func TestFirstTickCreatesTheSecret(t *testing.T) {
	r := newRig()
	next := r.g.tick(t.Context())
	s := r.secret(t)
	if string(s.Data["token"]) != "tok1" || s.Labels["app.kubernetes.io/managed-by"] != "maku-factory" {
		t.Fatalf("%+v", s)
	}
	if s.Annotations[ExpiresAnnotation] != r.clk.now().Add(time.Hour).Format(time.RFC3339) {
		t.Fatalf("annotation %q", s.Annotations[ExpiresAnnotation])
	}
	if next != 30*time.Minute {
		t.Fatalf("next %v", next)
	}
}

func TestLaterTickUpdatesAndAMintErrorKeepsTheOldToken(t *testing.T) {
	r := newRig()
	r.g.tick(t.Context())
	r.clk.t = r.clk.t.Add(30 * time.Minute)
	r.g.tick(t.Context())
	if string(r.secret(t).Data["token"]) != "tok2" {
		t.Fatal("not updated")
	}
	r.mu.Lock()
	r.fail = true
	r.mu.Unlock()
	if next := r.g.tick(t.Context()); next != time.Minute {
		t.Fatalf("retry after %v", next)
	}
	if string(r.secret(t).Data["token"]) != "tok2" {
		t.Fatal("a failed mint replaced the token")
	}
}

func TestTheAnnotationDrivesAnEarlyRefresh(t *testing.T) {
	r := newRig()
	r.g.tick(t.Context()) // expires in 1h
	r.clk.t = r.clk.t.Add(51 * time.Minute)
	if next := r.g.tick(t.Context()); next != 30*time.Minute || string(r.secret(t).Data["token"]) != "tok2" {
		t.Fatalf("within 10 minutes of expiry the token is rewritten at once (next %v)", next)
	}
}

func TestSleepIsShortenedByExpiry(t *testing.T) {
	r := newRig()
	r.ttl = 20 * time.Minute // GitHub could hand back less than an hour
	if next := r.g.tick(t.Context()); next != 10*time.Minute {
		t.Fatalf("next %v, want the time to 10 minutes before expiry", next)
	}
}

func TestStartMintsAtOnceAndStopsWithTheContext(t *testing.T) {
	r := newRig()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- r.g.Start(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var s corev1.Secret
		if r.c.Get(ctx, client.ObjectKey{Namespace: "runs", Name: "agent-git"}, &s) == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no Secret")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !r.g.NeedLeaderElection() {
		t.Fatal("only the leader writes")
	}
}
