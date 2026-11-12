// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// listen serves accept-and-close on 127.0.0.1, returning its address.
func listen(t *testing.T) string {
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
			_ = c.Close()
		}
	}()
	return l.Addr().String()
}

// closed is an address nothing listens on: a connection is refused.
func closed(t *testing.T) string {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := l.Addr().String()
	_ = l.Close()
	return a
}

// connectProxy answers every CONNECT with status.
func connectProxy(t *testing.T, status int) string {
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
			go func() {
				defer func() { _ = c.Close() }()
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				_, _ = io.WriteString(c, "HTTP/1.1 "+strconv.Itoa(status)+" "+http.StatusText(status)+"\r\n\r\n")
			}()
		}
	}()
	return "http://" + l.Addr().String()
}

func runProbe(t *testing.T, args ...string) (int, netprobeResult) {
	t.Helper()
	term := filepath.Join(t.TempDir(), "termination-log")
	fast := []string{"--termination-log", term, "--allow-wait", "1s", "--deny-timeout", "300ms", "--deny-gap", "50ms"}
	code := Netprobe(t.Context(), append(fast, args...), io.Discard)
	raw, err := os.ReadFile(term)
	if err != nil {
		t.Fatal(err)
	}
	var res netprobeResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("termination log %q: %v", raw, err)
	}
	return code, res
}

func TestNetprobePassesWhenTheDenyIsDenied(t *testing.T) {
	code, res := runProbe(t, "--allow", listen(t), "--deny", closed(t), "--connect", "github.com:443", "--proxy", connectProxy(t, http.StatusOK))
	if code != 0 || !res.OK {
		t.Fatalf("exit %d %+v", code, res)
	}
}

func TestNetprobeFailsWhenTheDenyConnects(t *testing.T) {
	deny := listen(t)
	code, res := runProbe(t, "--allow", listen(t), "--deny", deny, "--connect", "github.com:443", "--proxy", connectProxy(t, http.StatusOK))
	want := "deny " + deny + " connected: the CNI does not enforce NetworkPolicy"
	if code != 1 || res.OK || res.Message != want || res.Kind != netprobeUnenforced {
		t.Fatalf("exit %d %+v, want %q", code, res, want)
	}
}

// The deny targets are checked side by side: three of them take one target's time (two attempts, a
// gap apart), not three, so the probe stays short however many negative controls it has.
func TestNetprobeChecksTheDeniesInParallel(t *testing.T) {
	term := filepath.Join(t.TempDir(), "termination-log")
	args := []string{"--termination-log", term, "--allow-wait", "1s", "--deny-timeout", "300ms", "--deny-gap", "400ms",
		"--deny", closed(t), "--deny", closed(t), "--deny", closed(t)}
	start := time.Now()
	if code := Netprobe(t.Context(), args, io.Discard); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Fatalf("three deny targets took %s: sequential (3 x 400 ms gaps), not parallel", d)
	}
}

// With several denies, the one reported is the first in argument order that connected.
func TestNetprobeReportsTheFirstDenyThatConnected(t *testing.T) {
	open := listen(t)
	code, res := runProbe(t, "--deny", closed(t), "--deny", open, "--deny", listen(t))
	if code != 1 || res.Message != "deny "+open+" connected: the CNI does not enforce NetworkPolicy" {
		t.Fatalf("exit %d %+v", code, res)
	}
}

func TestNetprobeFailsWhenTheProxyRefuses(t *testing.T) {
	code, res := runProbe(t, "--allow", listen(t), "--deny", closed(t), "--connect", "github.com:443", "--proxy", connectProxy(t, http.StatusForbidden))
	if code != 1 || res.OK || !strings.Contains(res.Message, "the egress proxy refused github.com:443") || res.Kind != netprobeUnreachable {
		t.Fatalf("exit %d %+v", code, res)
	}
}

func TestNetprobeFailsWhenTheAllowIsUnreachable(t *testing.T) {
	allow := closed(t)
	start := time.Now()
	code, res := runProbe(t, "--allow", allow, "--deny", closed(t), "--connect", "github.com:443", "--proxy", connectProxy(t, http.StatusOK))
	if code != 1 || res.OK || !strings.Contains(res.Message, "allow "+allow) || res.Kind != netprobeUnreachable {
		t.Fatalf("exit %d %+v", code, res)
	}
	if time.Since(start) < time.Second {
		t.Fatal("an allow target is retried for --allow-wait: a new pod's policy may not be programmed yet")
	}
}

func TestNetprobeRefusesBadArguments(t *testing.T) {
	if code := Netprobe(t.Context(), []string{"--termination-log", filepath.Join(t.TempDir(), "t"), "--allow", "nope"}, io.Discard); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
