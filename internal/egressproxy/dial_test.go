// SPDX-License-Identifier: Apache-2.0

package egressproxy

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

type fakeResolver map[string][]string

func (f fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	ips, ok := f[host]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	out := make([]net.IPAddr, 0, len(ips))
	for _, s := range ips {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out, nil
}

func TestDialerRefusesNonPublicAddresses(t *testing.T) {
	res := fakeResolver{
		"loop.example": {"127.0.0.1"}, "ten.example": {"10.0.0.1"}, "meta.example": {"169.254.169.254"},
		"v6loop.example": {"::1"}, "cgnat.example": {"100.64.1.1"}, "corp.example": {"203.0.113.9"},
		"mixed.example": {"140.82.112.3", "10.1.2.3"}, "public.example": {"140.82.112.3"},
		"ula.example": {"fd00::1"}, "zero.example": {"0.0.0.0"},
	}
	var dialled []string
	d := &Dialer{Resolver: res, Deny: MustCIDRs("203.0.113.0/24"),
		Dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			dialled = append(dialled, addr)
			c, s := net.Pipe()
			_ = s.Close()
			return c, nil
		}}
	for _, h := range []string{"loop.example", "ten.example", "meta.example", "v6loop.example", "cgnat.example", "corp.example", "mixed.example", "ula.example", "zero.example"} {
		if _, err := d.DialContext(t.Context(), h+":443"); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: want ErrRefused, got %v", h, err)
		}
	}
	if len(dialled) != 0 {
		t.Fatalf("nothing refused may be dialled: %v", dialled)
	}
	c, err := d.DialContext(t.Context(), "public.example:443")
	if err != nil {
		t.Fatalf("public: %v", err)
	}
	_ = c.Close()
	if len(dialled) != 1 || dialled[0] != "140.82.112.3:443" {
		t.Fatalf("the resolved address is dialled, not the name again: %v", dialled)
	}
	if _, err := d.DialContext(t.Context(), "nowhere.example:443"); err == nil || errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "no such host") {
		t.Fatalf("an unresolvable name is an error, not a refusal: %v", err)
	}
}
