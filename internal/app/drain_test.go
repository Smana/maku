// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

// /readyz says "shutting down" from the process context ending (a signal) and from Begin, which
// the serve loops call when a listener or the manager dies: that cancels their own context, not
// the process's.
func TestDrainFlag(t *testing.T) {
	for name, c := range map[string]struct {
		act  func(cancel context.CancelFunc, d *drainFlag)
		want bool
	}{
		"nothing happened":          {func(context.CancelFunc, *drainFlag) {}, false},
		"the process was signalled": {func(cancel context.CancelFunc, _ *drainFlag) { cancel() }, true},
		"a serving loop began it":   {func(_ context.CancelFunc, d *drainFlag) { d.Begin() }, true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			d := newDrainFlag(ctx)
			c.act(cancel, d)
			if got := d.Draining(); got != c.want {
				t.Fatalf("Draining = %v, want %v", got, c.want)
			}
		})
	}
}

// recordListeners opens real listeners and remembers them, so a test can kill one the way a
// failing accept loop would.
type recordListeners struct {
	mu  sync.Mutex
	lns []net.Listener
}

func (r *recordListeners) listen(ctx context.Context, network, address string) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, network, address)
	if err == nil {
		r.mu.Lock()
		r.lns = append(r.lns, ln)
		r.mu.Unlock()
	}
	return ln, err
}

// A serving listener dying with the process context still live starts the shutdown: /readyz must
// answer 503 while the rest drains, as it does on a signal. Wait is not called until the check is
// done, so :9090 is still up.
func TestSlackReadyzIsUnreadyWhenAListenerDies(t *testing.T) {
	cfg := slackFiles(t, slackChannels)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rl := &recordListeners{}
	run, err := startSlackAppOn(ctx, slackLog(), []string{"--config", cfg}, os.Getenv, fakeCluster, rl.listen)
	if err != nil {
		t.Fatal(err)
	}
	if st, body := httpDo(t, http.MethodGet, run.Ops, "/readyz"); st != http.StatusOK {
		t.Fatalf("before: %d %s", st, body)
	}
	rl.mu.Lock()
	_ = rl.lns[0].Close() // the events listener's accept loop fails
	rl.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := httpDo(t, http.MethodGet, run.Ops, "/readyz")
		if st == http.StatusServiceUnavailable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("/readyz = %d after a listener died, want 503", st)
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-t.Context().Done():
		}
	}
	if err := run.Wait(); err == nil {
		t.Fatal("Wait returned nil, want the listener's error")
	}
}
