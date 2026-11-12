// SPDX-License-Identifier: Apache-2.0

package runs

import (
	"testing"

	"github.com/Smana/maku/internal/runwatch"
)

// The run's canary (hardening T3, HR7) rides its claim at CREATE only, so the shadow pass and the
// applier check a run's outputs against that run's canary, whatever a replay would plant now.
func TestCanaryAnnotation(t *testing.T) {
	s := spec()
	if _, ok := Build(runwatch.DefaultKind, s).GetAnnotations()[AnnCanary]; ok {
		t.Fatal("a run without a canary carries the annotation")
	}
	s.Canary = "mkc-0123456789abcdef0123"
	u := Build(runwatch.DefaultKind, s)
	if u.GetAnnotations()[AnnCanary] != s.Canary {
		t.Fatalf("annotations %v", u.GetAnnotations())
	}
	if r, ok := FromUnstructured(runwatch.DefaultKind, u); !ok || r.Canary != s.Canary {
		t.Fatalf("read back %q", r.Canary)
	}
	for _, bad := range []string{"mkc-xyz", "0123456789abcdef0123", "mkc-0123456789abcdef0123\n"} {
		s.Canary = bad
		if s.Validate() == nil {
			t.Errorf("canary %q validates", bad)
		}
	}
	c := newClient()
	s.Canary = "mkc-0123456789abcdef0123"
	if err := c.Create(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	if err := c.Annotate(t.Context(), s.RunID, map[string]string{AnnCanary: "mkc-ffffffffffffffffffff"}); err == nil {
		t.Fatal("the canary was patched after CREATE")
	}
}
