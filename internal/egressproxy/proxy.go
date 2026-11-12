// SPDX-License-Identifier: Apache-2.0

package egressproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// DefaultIdleTimeout closes a tunnel that moved no byte either way for this long.
const DefaultIdleTimeout = 5 * time.Minute

// The decisions of Proxy.Count, the bounded label of maku_egress_proxy_connections_total.
const (
	DecisionAllow  = "allow"
	DecisionRefuse = "refuse"
	DecisionError  = "error"
)

// Lookuper maps a client IP to its run.
type Lookuper interface {
	Lookup(ip string) (Entry, bool)
}

// Proxy serves CONNECT only. A client gets a tunnel to host:port when its source IP maps to a run
// whose allow-list holds exactly that pair, and the host is a name, never an IP literal. Each
// decision logs `allow run=<id> <host:port>` or `refuse run=<id|-> <host:port>`, as the harness's
// test proxy does.
type Proxy struct {
	Index       Lookuper
	Dial        func(ctx context.Context, hostport string) (net.Conn, error)
	IdleTimeout time.Duration
	Log         *slog.Logger
	Count       func(decision string) // optional
	Bytes       func(n int64)         // optional
	// MaxTunnels caps the open tunnels in all, MaxTunnelsPerClient those of one source IP; 0 is no
	// cap. A CONNECT over either is refused 429 before anything is dialled.
	MaxTunnels, MaxTunnelsPerClient int

	mu     sync.Mutex
	open   map[string]int // by client IP
	nTotal int
}

// acquire takes a tunnel slot for ip, or says which cap is full.
func (p *Proxy) acquire(ip string) (full string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case p.MaxTunnels > 0 && p.nTotal >= p.MaxTunnels:
		return fmt.Sprintf("too many tunnels in all (%d)", p.MaxTunnels)
	case p.MaxTunnelsPerClient > 0 && p.open[ip] >= p.MaxTunnelsPerClient:
		return fmt.Sprintf("too many tunnels from this client (%d)", p.MaxTunnelsPerClient)
	}
	if p.open == nil {
		p.open = map[string]int{}
	}
	p.open[ip]++
	p.nTotal++
	return ""
}

func (p *Proxy) release(ip string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nTotal--
	if p.open[ip]--; p.open[ip] <= 0 {
		delete(p.open, ip)
	}
}

func (p *Proxy) count(decision string) {
	if p.Count != nil {
		p.Count(decision)
	}
}

func (p *Proxy) refuse(w http.ResponseWriter, run, target, reason string, code int) {
	p.Log.Warn("refuse run="+run+" "+target, "reason", reason)
	p.count(DecisionRefuse)
	http.Error(w, http.StatusText(code), code)
}

// ServeHTTP decides and, when allowed, splices the client to the destination.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	e, mapped := p.Index.Lookup(ip)
	run := "-"
	if mapped {
		run = e.RunID
	}
	target := r.Host
	if r.Method != http.MethodConnect {
		p.refuse(w, run, target, "only CONNECT is served", http.StatusMethodNotAllowed)
		return
	}
	host, _, err := net.SplitHostPort(target)
	switch {
	case err != nil:
		p.refuse(w, run, target, "not host:port", http.StatusForbidden)
		return
	case net.ParseIP(host) != nil:
		p.refuse(w, run, target, "an IP literal: only names on the run's allow-list", http.StatusForbidden)
		return
	case !mapped:
		p.refuse(w, run, target, "the client "+ip+" is no single running run pod", http.StatusForbidden)
		return
	case !slices.Contains(e.Allow, target):
		p.refuse(w, run, target, "not on the run's allow-list", http.StatusForbidden)
		return
	}
	if full := p.acquire(ip); full != "" {
		p.refuse(w, run, target, full, http.StatusTooManyRequests)
		return
	}
	defer p.release(ip)
	up, err := p.Dial(r.Context(), target)
	if errors.Is(err, ErrRefused) {
		p.refuse(w, run, target, err.Error(), http.StatusForbidden)
		return
	}
	if err != nil {
		p.Log.Warn("error run="+run+" "+target, "err", err)
		p.count(DecisionError)
		http.Error(w, http.StatusText(http.StatusBadGateway), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = up.Close()
		p.count(DecisionError)
		http.Error(w, "no hijack", http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		_ = up.Close()
		p.count(DecisionError)
		return
	}
	// The server's header and idle deadlines must not end the tunnel: its own idle timeout does.
	_ = client.SetDeadline(time.Time{})
	p.Log.Info("allow run=" + run + " " + target)
	p.count(DecisionAllow)
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		_ = client.Close()
		_ = up.Close()
		return
	}
	// Bytes the client sent past its request headers are the tunnel's first.
	var head []byte
	if n := buf.Reader.Buffered(); n > 0 {
		head, _ = buf.Peek(n)
	}
	p.splice(client, up, head)
}

// splice copies both ways until either side closes or no byte moves for IdleTimeout.
func (p *Proxy) splice(client, up net.Conn, head []byte) {
	idle := p.IdleTimeout
	if idle <= 0 {
		idle = DefaultIdleTimeout
	}
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var once sync.Once
	closeBoth := func() { once.Do(func() { _ = client.Close(); _ = up.Close() }) }
	done := make(chan struct{})
	var total atomic.Int64
	cp := func(dst, src net.Conn, first []byte) {
		if len(first) > 0 {
			if _, err := dst.Write(first); err != nil {
				closeBoth()
				return
			}
			total.Add(int64(len(first)))
		}
		b := make([]byte, 32<<10)
		for {
			n, err := src.Read(b)
			if n > 0 {
				last.Store(time.Now().UnixNano())
				if _, werr := dst.Write(b[:n]); werr != nil {
					break
				}
				total.Add(int64(n))
			}
			if err != nil {
				break
			}
		}
		closeBoth()
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); cp(up, client, head) }()
	go func() { defer wg.Done(); cp(client, up, nil) }()
	go func() { wg.Wait(); close(done) }()
	tick := time.NewTicker(min(idle/4, time.Second))
	defer tick.Stop()
	for {
		select {
		case <-done:
			if p.Bytes != nil {
				p.Bytes(total.Load())
			}
			return
		case <-tick.C:
			if time.Since(time.Unix(0, last.Load())) > idle {
				closeBoth()
			}
		}
	}
}
