// SPDX-License-Identifier: Apache-2.0

package egressproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

const releaseHost = "release-assets.githubusercontent.com:443"

var t0 = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

// installPod is a run pod whose install set holds the release host until t0 plus 15 minutes.
func installPod(name, ip, uid string, mut ...func(*corev1.Pod)) *corev1.Pod {
	return runPod(name, ip, append([]func(*corev1.Pod){func(p *corev1.Pod) {
		p.UID = types.UID(uid)
		p.Annotations[InstallAnnotation] = releaseHost + ",objects.githubusercontent.com:443"
		p.Annotations[InstallUntilAnnotation] = t0.Add(15 * time.Minute).Format(time.RFC3339)
	}}, mut...)...)
}

// fakeCloser records the closes the proxy asked for, or fails them.
type fakeCloser struct {
	mu     sync.Mutex
	closed []string // namespace/name/uid
	err    error
}

func (f *fakeCloser) CloseInstall(_ context.Context, namespace, name, uid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.closed = append(f.closed, namespace+"/"+name+"/"+uid)
	return nil
}

func (f *fakeCloser) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.closed...)
}

// clock is a settable now.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time   { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) set(t time.Time)  { c.mu.Lock(); defer c.mu.Unlock(); c.t = t }
func newClock(t time.Time) *clock { return &clock{t: t} }

type reasons struct {
	mu sync.Mutex
	n  map[string]int
}

func (r *reasons) add(reason string) { r.mu.Lock(); defer r.mu.Unlock(); r.n[reason]++ }
func (r *reasons) get(reason string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n[reason]
}

func installProxy(t *testing.T, x *Index, c *clock, cl *fakeCloser, rs *reasons) *fixture {
	t.Helper()
	return newProxy(t, x, time.Minute, func(p *Proxy) {
		p.Now, p.Closer = c.now, cl
		if rs != nil {
			p.Refused = rs.add
		}
	})
}

// closeInstall posts body to the proxy's close path at addr and returns the status code.
func closeInstall(t *testing.T, addr, body string) int {
	t.Helper()
	return onClosePath(t, addr, http.MethodPost, body)
}

// onClosePath sends method to the close path with body.
func onClosePath(t *testing.T, addr, method, body string) int {
	t.Helper()
	c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	req := method + " " + ClosePath + " HTTP/1.1\r\nHost: " + addr + "\r\nContent-Type: application/json\r\nContent-Length: " +
		strconv.Itoa(len(body)) + "\r\nConnection: close\r\n\r\n" + body
	if _, err := io.WriteString(c, req); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: method})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func uidBody(uid string) string { return `{"podUID":"` + uid + `"}` }

func TestInstallSetOpenUntilClosed(t *testing.T) {
	x := NewIndex()
	x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1"))
	c, cl, rs := newClock(t0), &fakeCloser{}, &reasons{n: map[string]int{}}
	f := installProxy(t, x, c, cl, rs)

	if r, _, _ := connect(t, f.url, http.MethodConnect, releaseHost); r.StatusCode != http.StatusOK {
		t.Fatalf("an open install set allows the release host: %d", r.StatusCode)
	}
	if !strings.Contains(f.log.String(), "allow run=abcdefgh "+releaseHost) || !strings.Contains(f.log.String(), "phase=install") {
		t.Fatalf("an install-phase allow is logged with its phase: %s", f.log.String())
	}
	if code := closeInstall(t, f.url, uidBody("uid-1")); code != http.StatusOK {
		t.Fatalf("close: %d", code)
	}
	if got := cl.calls(); len(got) != 1 || got[0] != "agents/run-abcdefgh/uid-1" {
		t.Fatalf("the close is written to the pod, keyed by its UID: %v", got)
	}
	r, _, _ := connect(t, f.url, http.MethodConnect, releaseHost)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("after the close the release host is refused: %d", r.StatusCode)
	}
	if !strings.Contains(f.log.String(), "refuse run=abcdefgh "+releaseHost) || !strings.Contains(f.log.String(), "reason=install_closed") ||
		!strings.Contains(f.log.String(), "phase=agent") {
		t.Fatalf("the refusal carries reason install_closed and phase agent: %s", f.log.String())
	}
	if rs.get(ReasonInstallClosed) != 1 {
		t.Fatalf("an install_closed refusal is counted: %v", rs.n)
	}
	if r, _, _ := connect(t, f.url, http.MethodConnect, "github.com:443"); r.StatusCode != http.StatusOK {
		t.Fatalf("the run set stays open after the close: %d", r.StatusCode)
	}
	// Idempotent: a second close succeeds without another write.
	if code := closeInstall(t, f.url, uidBody("uid-1")); code != http.StatusOK || len(cl.calls()) != 1 {
		t.Fatalf("a second close: %d, writes %v", code, cl.calls())
	}
}

func TestInstallSetClosesAtTheDeadline(t *testing.T) {
	x := NewIndex()
	x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1"))
	c := newClock(t0.Add(15*time.Minute - time.Second))
	f := installProxy(t, x, c, &fakeCloser{}, nil)
	if r, _, _ := connect(t, f.url, http.MethodConnect, releaseHost); r.StatusCode != http.StatusOK {
		t.Fatalf("before the deadline the release host is allowed: %d", r.StatusCode)
	}
	c.set(t0.Add(15 * time.Minute))
	if r, _, _ := connect(t, f.url, http.MethodConnect, releaseHost); r.StatusCode != http.StatusForbidden {
		t.Fatalf("after the deadline the release host is refused: %d", r.StatusCode)
	}
	if !strings.Contains(f.log.String(), "reason=install_closed") {
		t.Fatalf("log: %s", f.log.String())
	}
}

func TestInstallSetFailsClosedOnABadDeadline(t *testing.T) {
	for name, v := range map[string]string{"missing": "", "garbage": "tomorrow"} {
		x := NewIndex()
		x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1", func(p *corev1.Pod) {
			if v == "" {
				delete(p.Annotations, InstallUntilAnnotation)
			} else {
				p.Annotations[InstallUntilAnnotation] = v
			}
		}))
		f := installProxy(t, x, newClock(t0), &fakeCloser{}, nil)
		if r, _, _ := connect(t, f.url, http.MethodConnect, releaseHost); r.StatusCode != http.StatusForbidden {
			t.Errorf("%s deadline: the install set is closed: %d", name, r.StatusCode)
		}
	}
}

func TestInstallSetClosedByTheAnnotation(t *testing.T) {
	x := NewIndex()
	x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1", func(p *corev1.Pod) { p.Annotations[InstallClosedAnnotation] = "true" }))
	f := installProxy(t, x, newClock(t0), &fakeCloser{}, nil)
	if r, _, _ := connect(t, f.url, http.MethodConnect, releaseHost); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a pod another replica closed is closed here: %d", r.StatusCode)
	}
}

func TestInstallSetNoReopen(t *testing.T) {
	x := NewIndex()
	x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1"))
	c, cl := newClock(t0), &fakeCloser{}
	f := installProxy(t, x, c, cl, nil)
	if code := closeInstall(t, f.url, uidBody("uid-1")); code != http.StatusOK {
		t.Fatalf("close: %d", code)
	}
	// The watch delivers the pod again, before the patch has reached it: still closed.
	x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1"))
	if r, _, _ := connect(t, f.url, http.MethodConnect, releaseHost); r.StatusCode != http.StatusForbidden {
		t.Fatalf("a stale pod from the watch does not reopen the set: %d", r.StatusCode)
	}
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodGet} {
		if code := onClosePath(t, f.url, m, uidBody("uid-1")); code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d: there is no other verb on the close path", m, code)
		}
	}
}

func TestInstallCloseRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		x    func() *Index
		body string
		code int
	}{
		"another pod's UID": {func() *Index {
			x := NewIndex()
			x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1"))
			x.Upsert(installPod("run-bbbbbbbb", "10.0.0.9", "uid-2"))
			return x
		}, uidBody("uid-2"), http.StatusForbidden},
		"an IP not in the index": {NewIndex, uidBody("uid-1"), http.StatusForbidden},
		"no UID":                 {func() *Index { x := NewIndex(); x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1")); return x }, `{}`, http.StatusBadRequest},
		"not JSON":               {func() *Index { x := NewIndex(); x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1")); return x }, `uid-1`, http.StatusBadRequest},
	} {
		cl := &fakeCloser{}
		f := installProxy(t, tc.x(), newClock(t0), cl, nil)
		if code := closeInstall(t, f.url, tc.body); code != tc.code {
			t.Errorf("%s: %d, want %d", name, code, tc.code)
		}
		if len(cl.calls()) != 0 {
			t.Errorf("%s: nothing is written: %v", name, cl.calls())
		}
	}
	// Another pod could not close uid-2's set: it stays open for its own pod.
	x := NewIndex()
	x.Upsert(installPod("run-bbbbbbbb", "127.0.0.1", "uid-2", func(p *corev1.Pod) { p.Status.PodIP = "10.0.0.9" }))
	if e, ok := x.Lookup("10.0.0.9"); !ok || !e.InstallOpen(t0) {
		t.Fatalf("uid-2's set is still open: %+v %v", e, ok)
	}
}

func TestInstallCloseWriteFails(t *testing.T) {
	x := NewIndex()
	x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1"))
	f := installProxy(t, x, newClock(t0), &fakeCloser{err: errors.New("conflict")}, nil)
	if code := closeInstall(t, f.url, uidBody("uid-1")); code != http.StatusInternalServerError {
		t.Fatalf("a failed write is a 500, so the harness fails closed: %d", code)
	}
	if r, _, _ := connect(t, f.url, http.MethodConnect, releaseHost); r.StatusCode != http.StatusOK {
		t.Fatalf("an unwritten close does not count: %d", r.StatusCode)
	}
}

func TestInstallIPReuseWithANewUIDStartsOpen(t *testing.T) {
	x := NewIndex()
	old := installPod("run-abcdefgh", "127.0.0.1", "uid-1")
	x.Upsert(old)
	f := installProxy(t, x, newClock(t0), &fakeCloser{}, nil)
	if code := closeInstall(t, f.url, uidBody("uid-1")); code != http.StatusOK {
		t.Fatalf("close: %d", code)
	}
	x.Delete(old.Namespace, old.Name)
	x.Upsert(installPod("run-cccccccc", "127.0.0.1", "uid-3"))
	if r, _, _ := connect(t, f.url, http.MethodConnect, releaseHost); r.StatusCode != http.StatusOK {
		t.Fatalf("a new pod on the same IP starts with its own open set: %d", r.StatusCode)
	}
	// The same name recreated with a new UID starts open as well.
	x.Upsert(installPod("run-cccccccc", "127.0.0.1", "uid-4"))
	if e, _ := x.Lookup("127.0.0.1"); !e.InstallOpen(t0) {
		t.Fatalf("a recreated pod's set is open: %+v", e)
	}
}

func TestRefusalReasons(t *testing.T) {
	rs := &reasons{n: map[string]int{}}
	f := installProxy(t, mapped(), newClock(t0), &fakeCloser{}, rs)
	for _, target := range []string{"example.com:443", "raw.githubusercontent.com:443"} {
		if r, _, _ := connect(t, f.url, http.MethodConnect, target); r.StatusCode != http.StatusForbidden {
			t.Fatalf("%s: %d", target, r.StatusCode)
		}
	}
	if rs.get(ReasonNotAllowed) != 2 || !strings.Contains(f.log.String(), "reason=not_allowed") {
		t.Fatalf("a host on neither set is not_allowed: %v %s", rs.n, f.log.String())
	}
	_, _, _ = connect(t, f.url, http.MethodConnect, "140.82.112.3:443")
	if rs.get(ReasonBadRequest) != 1 {
		t.Fatalf("an IP literal is bad_request: %v", rs.n)
	}
}

// A tunnel to an install host opened while the set was open does not outlive it: the close, or the
// deadline, ends it within the tunnel's tick, on whichever replica holds it.
func TestInstallTunnelEndsWithTheSet(t *testing.T) {
	for name, end := range map[string]func(c *clock, x *Index, f *fixture){
		"closed": func(_ *clock, _ *Index, f *fixture) {
			if code := closeInstall(t, f.url, uidBody("uid-1")); code != http.StatusOK {
				t.Fatalf("close: %d", code)
			}
		},
		"closed on another replica": func(_ *clock, x *Index, _ *fixture) {
			x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1", func(p *corev1.Pod) { p.Annotations[InstallClosedAnnotation] = "true" }))
		},
		"deadline": func(c *clock, _ *Index, _ *fixture) { c.set(t0.Add(time.Hour)) },
	} {
		t.Run(name, func(t *testing.T) {
			x := NewIndex()
			x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1"))
			c := newClock(t0)
			f := installProxy(t, x, c, &fakeCloser{}, nil)
			r, conn, br := connect(t, f.url, http.MethodConnect, releaseHost)
			if r.StatusCode != http.StatusOK {
				t.Fatalf("open set: %d", r.StatusCode)
			}
			keep, kc, _ := connect(t, f.url, http.MethodConnect, "github.com:443")
			if keep.StatusCode != http.StatusOK {
				t.Fatalf("run set: %d", keep.StatusCode)
			}
			end(c, x, f)
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			var ne net.Error
			if _, err := br.ReadByte(); err == nil || (errors.As(err, &ne) && ne.Timeout()) {
				t.Fatalf("the install tunnel ends with the set, not at a timeout: %v", err)
			}
			if _, err := io.WriteString(kc, "ping"); err != nil {
				t.Fatalf("a run-set tunnel stays open: %v", err)
			}
		})
	}
}

// The harness's self-check asks the proxy about its own set through a host no one serves: 409 while
// the set is open, 403 once closed, and never as install_closed, so the alert stays for real downloads.
func TestInstallCheckHost(t *testing.T) {
	x := NewIndex()
	x.Upsert(installPod("run-abcdefgh", "127.0.0.1", "uid-1"))
	rs := &reasons{n: map[string]int{}}
	f := installProxy(t, x, newClock(t0), &fakeCloser{}, rs)
	if r, _, _ := connect(t, f.url, http.MethodConnect, InstallCheckHost); r.StatusCode != http.StatusConflict {
		t.Fatalf("open set: %d, want 409", r.StatusCode)
	}
	if code := closeInstall(t, f.url, uidBody("uid-1")); code != http.StatusOK {
		t.Fatalf("close: %d", code)
	}
	if r, _, _ := connect(t, f.url, http.MethodConnect, InstallCheckHost); r.StatusCode != http.StatusForbidden {
		t.Fatalf("closed set: %d, want 403", r.StatusCode)
	}
	if rs.get(ReasonInstallClosed) != 0 || rs.get(ReasonInstallCheck) != 1 || !strings.Contains(f.log.String(), "reason=install_check") {
		t.Fatalf("the check is no install_closed refusal: %v %s", rs.n, f.log.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.dial) != 0 {
		t.Fatalf("the check host is never dialled: %v", f.dial)
	}
}
