// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"
	"testing"
)

func TestRuntimeIsValidated(t *testing.T) {
	digest := "@sha256:" + strings.Repeat("a", 64)
	for rt, want := range map[Runtime]string{
		{}: "",
		{Image: "ghcr.io/acme/maku-harness-jvm" + digest, Size: "medium"}: "",
		{Image: "ghcr.io/acme/maku-harness-jvm:latest"}:                   "not pinned by digest",
		{Size: "huge"}: "is not one of",
	} {
		var errs []string
		rt.validate(func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) })
		if got := strings.Join(errs, "; "); (want == "") != (got == "") || !strings.Contains(got, want) {
			t.Errorf("%+v: %q, want %q", rt, got, want)
		}
	}
}
