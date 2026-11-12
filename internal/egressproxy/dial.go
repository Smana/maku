// SPDX-License-Identifier: Apache-2.0

package egressproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// ErrRefused is a destination the proxy never dials: a name that resolves to a non-public or denied
// address.
var ErrRefused = errors.New("refused")

// Resolver is net.Resolver's lookup.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Dialer resolves a name itself and dials the address it vetted, so a second lookup cannot
// substitute another (DNS rebinding).
type Dialer struct {
	Resolver Resolver
	Deny     []*net.IPNet // beyond the non-public ranges: the chart's privateCIDRs
	Dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	Timeout  time.Duration
}

// alwaysDenied are ranges net.IP's predicates miss: CGNAT, "this network", benchmarking and reserved
// IPv4, and the IPv6 forms that carry an IPv4 address a gateway may translate to anything: NAT64
// (64:ff9b::/96 and the local-use 64:ff9b:1::/48), 6to4 (2002::/16) and IPv4-compatible (::/96).
// IPv4-mapped addresses (::ffff:a.b.c.d) are folded to IPv4 by To4 below.
var alwaysDenied = MustCIDRs("100.64.0.0/10", "0.0.0.0/8", "192.0.0.0/24", "198.18.0.0/15", "240.0.0.0/4",
	"64:ff9b::/96", "64:ff9b:1::/48", "2002::/16", "::/96")

// MustCIDRs parses CIDRs, panicking on a bad one (for constants and validated config).
func MustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic(err)
		}
		out = append(out, n)
	}
	return out
}

// denied is that ip is not a public unicast address, or is in deny.
func denied(ip net.IP, deny []*net.IPNet) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, n := range append(alwaysDenied, deny...) {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// DialContext dials hostport after resolving its host. Any non-public or denied address among the
// answers refuses the whole name: a name that mixes them is not trusted for either.
func (d *Dialer) DialContext(ctx context.Context, hostport string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return nil, err
	}
	addrs, err := d.Resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("%s: no address", host)
	}
	for _, a := range addrs {
		if denied(a.IP, d.Deny) {
			return nil, fmt.Errorf("%s resolves to %s, a non-public address: %w", host, a.IP, ErrRefused)
		}
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	var errs []error
	for _, a := range addrs {
		dctx, cancel := context.WithTimeout(ctx, timeout)
		c, err := d.Dial(dctx, "tcp", net.JoinHostPort(a.IP.String(), port))
		cancel()
		if err == nil {
			return c, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}
