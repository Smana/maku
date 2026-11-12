// SPDX-License-Identifier: Apache-2.0

package egressproxy

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuf is a log sink the handler goroutines write to while the test reads it.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// echo is the upstream every allowed CONNECT is dialled to.
func echo(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer func() { _ = c.Close() }(); _, _ = io.Copy(c, c) }()
		}
	}()
	return l.Addr().String()
}

type fixture struct {
	url  string
	log  *syncBuf
	dial []string
	mu   sync.Mutex
}

func newProxy(t *testing.T, x *Index, idle time.Duration, mut ...func(*Proxy)) *fixture {
	t.Helper()
	up := echo(t)
	f := &fixture{log: &syncBuf{}}
	p := &Proxy{Index: x, IdleTimeout: idle, Log: slog.New(slog.NewTextHandler(f.log, nil)),
		Dial: func(ctx context.Context, hostport string) (net.Conn, error) {
			f.mu.Lock()
			f.dial = append(f.dial, hostport)
			f.mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, "tcp", up)
		}}
	for _, m := range mut {
		m(p)
	}
	s := httptest.NewServer(p)
	t.Cleanup(s.Close)
	f.url = strings.TrimPrefix(s.URL, "http://")
	return f
}

func mapped() *Index {
	x := NewIndex()
	x.Upsert(runPod("run-abcdefgh", "127.0.0.1"))
	return x
}

// reply is the proxy's status line, its body already closed.
type reply struct {
	StatusCode int
	Status     string
}

// connect sends CONNECT target (or method's plain request) and returns the reply and the open
// connection, positioned after the reply's headers.
func connect(t *testing.T, proxy, method, target string) (reply, net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := (&net.Dialer{}).DialContext(t.Context(), "tcp", proxy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if method == http.MethodConnect {
		_, err = io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	} else {
		_, err = io.WriteString(c, method+" http://"+target+"/ HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
	}
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	// A CONNECT request: a 200 reply then has no body, and the tunnel's bytes stay in br.
	req := &http.Request{Method: method}
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.Fatal(err)
	}
	if method == http.MethodConnect && resp.StatusCode == http.StatusOK {
		_ = resp.Body.Close()
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	return reply{resp.StatusCode, resp.Status}, c, br
}

func TestProxyTunnelsAnAllowedHost(t *testing.T) {
	f := newProxy(t, mapped(), time.Minute)
	resp, c, br := connect(t, f.url, http.MethodConnect, "github.com:443")
	if resp.StatusCode != http.StatusOK || resp.Status != "200 Connection established" {
		t.Fatalf("status %q", resp.Status)
	}
	if _, err := io.WriteString(c, "ping"); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("bytes both ways: %q %v", buf, err)
	}
	if !strings.Contains(f.log.String(), "allow run=abcdefgh github.com:443") {
		t.Fatalf("log: %s", f.log.String())
	}
	if len(f.dial) != 1 || f.dial[0] != "github.com:443" {
		t.Fatalf("dialled %v", f.dial)
	}
}

func TestProxyRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		x      *Index
		target string
		log    string
	}{
		"unlisted host":   {mapped(), "example.com:443", "refuse run=abcdefgh example.com:443"},
		"other port":      {mapped(), "github.com:8443", "refuse run=abcdefgh github.com:8443"},
		"case and dot":    {mapped(), "GitHub.com.:443", "refuse run=abcdefgh GitHub.com.:443"},
		"unmapped client": {NewIndex(), "github.com:443", "refuse run=- github.com:443"},
		"ip literal":      {mapped(), "140.82.112.3:443", "refuse run=abcdefgh 140.82.112.3:443"},
		"v6 literal":      {mapped(), "[::1]:443", "refuse run=abcdefgh [::1]:443"},
	} {
		f := newProxy(t, tc.x, time.Minute)
		resp, _, _ := connect(t, f.url, http.MethodConnect, tc.target)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: status %d", name, resp.StatusCode)
		}
		if !strings.Contains(f.log.String(), tc.log) {
			t.Errorf("%s: log %q lacks %q", name, f.log.String(), tc.log)
		}
		if len(f.dial) != 0 {
			t.Errorf("%s: nothing refused is dialled: %v", name, f.dial)
		}
	}
}

func TestProxyServesConnectOnly(t *testing.T) {
	f := newProxy(t, mapped(), time.Minute)
	resp, _, _ := connect(t, f.url, http.MethodGet, "github.com")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d", resp.StatusCode)
	}
}

func TestProxyClosesAnIdleTunnel(t *testing.T) {
	f := newProxy(t, mapped(), 200*time.Millisecond)
	resp, c, br := connect(t, f.url, http.MethodConnect, "github.com:443")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	if _, err := br.ReadByte(); err == nil {
		t.Fatal("an idle tunnel sends nothing and closes")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("closed after %s, not after the idle timeout", d)
	}
}

// capped checks that one more tunnel than the cap is refused 429 while the others stay open, and
// that closing one frees its slot.
func capped(t *testing.T, mut func(*Proxy), reason string) {
	t.Helper()
	f := newProxy(t, mapped(), time.Minute, mut)
	r1, c1, _ := connect(t, f.url, http.MethodConnect, "github.com:443")
	r2, _, _ := connect(t, f.url, http.MethodConnect, "api.github.com:443")
	if r1.StatusCode != http.StatusOK || r2.StatusCode != http.StatusOK {
		t.Fatalf("under the cap: %d %d", r1.StatusCode, r2.StatusCode)
	}
	r3, _, _ := connect(t, f.url, http.MethodConnect, "github.com:443")
	if r3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("over the cap: status %d", r3.StatusCode)
	}
	if !strings.Contains(f.log.String(), "refuse run=abcdefgh github.com:443") || !strings.Contains(f.log.String(), reason) {
		t.Fatalf("log %q lacks the refusal and %q", f.log.String(), reason)
	}
	_ = c1.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		r4, _, _ := connect(t, f.url, http.MethodConnect, "github.com:443")
		if r4.StatusCode == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a closed tunnel frees its slot: still %d", r4.StatusCode)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestProxyCapsTunnelsPerClient(t *testing.T) {
	capped(t, func(p *Proxy) { p.MaxTunnelsPerClient = 2 }, "tunnels from this client")
}

func TestProxyCapsTunnelsGlobally(t *testing.T) {
	capped(t, func(p *Proxy) { p.MaxTunnels = 2 }, "tunnels in all")
}

func TestProxyKeepsABusyTunnel(t *testing.T) {
	f := newProxy(t, mapped(), 300*time.Millisecond)
	resp, c, br := connect(t, f.url, http.MethodConnect, "github.com:443")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	buf := make([]byte, 1)
	for range 6 { // 6 x 100ms: twice the idle timeout, never idle
		time.Sleep(100 * time.Millisecond)
		if _, err := c.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadFull(br, buf); err != nil {
			t.Fatalf("a busy tunnel stays open: %v", err)
		}
	}
}
