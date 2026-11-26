// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestRuntimeIsValidated(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		rt   Runtime
		want string
	}{
		{Runtime{}, ""},
		{Runtime{Image: "ghcr.io/acme/maku-harness-jvm" + digest, Size: "medium"}, ""},
		{Runtime{Image: "ghcr.io/acme/maku-harness-jvm:latest"}, "not pinned by digest"},
		{Runtime{Size: "huge"}, "is not one of"},
	} {
		rt, want := tc.rt, tc.want
		var errs []string
		rt.validate(func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) })
		if got := strings.Join(errs, "; "); (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%+v: %q, want %q", rt, got, want)
		}
	}
}

func TestServicesAreValidated(t *testing.T) {
	for msg, svcs := range map[string][]Service{"": {"postgres", "redis"}, "not one of": {"mysql"}, "listed twice": {"redis", "redis"}} {
		var errs []string
		Runtime{Services: svcs}.validate(func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) })
		got := strings.Join(errs, "; ")
		if (msg == "") != (got == "") || !strings.Contains(got, msg) {
			t.Errorf("%v: %q", svcs, got)
		}
	}
}
