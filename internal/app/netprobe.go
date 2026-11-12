// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The kinds of a failed netprobe: the preflight refuses runs on unenforced, and holds them on
// unreachable, which a component still starting (right after install) explains.
const (
	netprobeUnenforced  = "unenforced"
	netprobeUnreachable = "unreachable"
)

// netprobeResult is what netprobe writes to its termination log, for the preflight to read.
type netprobeResult struct {
	OK      bool   `json:"ok"`
	Kind    string `json:"kind,omitempty"`
	Message string `json:"message,omitempty"`
}

type addrList []string

func (a *addrList) String() string { return strings.Join(*a, ",") }
func (a *addrList) Set(v string) error {
	if _, _, err := net.SplitHostPort(v); err != nil {
		return fmt.Errorf("%q is not host:port", v)
	}
	*a = append(*a, v)
	return nil
}

// Netprobe is the preflight probe pod's command (v4 D6, plan PR7): each --allow must accept a TCP
// connection (retried for --allow-wait, as a new pod's policy may not be programmed yet); each --deny
// must time out or be refused twice, --deny-gap apart; and a CONNECT to --connect through --proxy must
// be answered 200. It writes a netprobeResult to --termination-log and returns the exit code.
func Netprobe(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("netprobe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var allow, deny addrList
	fs.Var(&allow, "allow", "host:port that must accept (repeatable)")
	fs.Var(&deny, "deny", "host:port that must not accept (repeatable)")
	connect := fs.String("connect", "", "host:port to CONNECT to through --proxy")
	proxy := fs.String("proxy", "", "the egress proxy's http URL")
	term := fs.String("termination-log", "/dev/termination-log", "where the JSON result goes")
	allowWait := fs.Duration("allow-wait", 30*time.Second, "how long an allow target is retried")
	denyTimeout := fs.Duration("deny-timeout", 3*time.Second, "how long a deny attempt waits")
	denyGap := fs.Duration("deny-gap", 3*time.Second, "the pause between the two deny attempts")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if (*connect == "") != (*proxy == "") {
		_, _ = fmt.Fprintln(stderr, "netprobe: --connect and --proxy go together")
		return 2
	}
	res := netprobe(ctx, allow, deny, *connect, *proxy, *allowWait, *denyTimeout, *denyGap)
	raw, _ := json.Marshal(res) // a struct of strings and a bool
	if err := os.WriteFile(filepath.Clean(*term), raw, 0o600); err != nil {
		_, _ = fmt.Fprintln(stderr, "netprobe:", err)
	}
	_, _ = fmt.Fprintln(stderr, string(raw))
	if !res.OK {
		return 1
	}
	return 0
}

func netprobe(ctx context.Context, allow, deny []string, connect, proxy string, allowWait, denyTimeout, denyGap time.Duration) netprobeResult {
	d := &net.Dialer{}
	tryDial := func(addr string, timeout time.Duration) error {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		c, err := d.DialContext(cctx, "tcp", addr)
		if err == nil {
			_ = c.Close()
		}
		return err
	}
	// Positive controls first: they also give a new pod's policy time to be programmed before the
	// negative control runs.
	for _, a := range allow {
		var err error
		for deadline := time.Now().Add(allowWait); ; {
			if err = tryDial(a, 3*time.Second); err == nil || time.Now().After(deadline) || ctx.Err() != nil {
				break
			}
			sleep(ctx, 500*time.Millisecond)
		}
		if err != nil {
			return netprobeResult{Kind: netprobeUnreachable, Message: fmt.Sprintf("allow %s unreachable: %v", a, err)}
		}
	}
	if connect != "" {
		var err error
		for deadline := time.Now().Add(allowWait); ; {
			if err = connectThrough(ctx, proxy, connect); err == nil || time.Now().After(deadline) || ctx.Err() != nil {
				break
			}
			sleep(ctx, time.Second)
		}
		if err != nil {
			return netprobeResult{Kind: netprobeUnreachable, Message: err.Error()}
		}
	}
	// Every deny target side by side, each tried twice a gap apart: the probe takes one target's time
	// however many there are. The first target in argument order that connected is reported.
	connected := make([]bool, len(deny))
	var wg sync.WaitGroup
	for i, a := range deny {
		wg.Go(func() {
			for n := range 2 {
				if n > 0 {
					sleep(ctx, denyGap)
				}
				if err := tryDial(a, denyTimeout); err == nil {
					connected[i] = true
					return
				}
			}
		})
	}
	wg.Wait()
	for i, a := range deny {
		if connected[i] {
			return netprobeResult{Kind: netprobeUnenforced, Message: "deny " + a + " connected: the CNI does not enforce NetworkPolicy"}
		}
	}
	return netprobeResult{OK: true}
}

// connectThrough asks proxy for a tunnel to target and closes it once established.
func connectThrough(ctx context.Context, proxy, target string) error {
	u, err := url.Parse(proxy)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return fmt.Errorf("--proxy %q is not an http URL", proxy)
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(cctx, "tcp", u.Host)
	if err != nil {
		return fmt.Errorf("the egress proxy %s is unreachable: %w", u.Host, err)
	}
	defer func() { _ = c.Close() }()
	if dl, ok := cctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	if _, err := io.WriteString(c, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n"); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return fmt.Errorf("the egress proxy's answer to CONNECT %s: %w", target, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the egress proxy refused %s: %s", target, resp.Status)
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
