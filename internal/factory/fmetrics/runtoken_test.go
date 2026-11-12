// SPDX-License-Identifier: Apache-2.0

package fmetrics

import (
	"strings"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// The minter's mints and revokes are counted on closed vocabularies (v4 D10, plan PR6).
func TestRunTokenMintsAndRevokes(t *testing.T) {
	ctx := t.Context()
	s, scrape := rig(t, fake.NewClientBuilder().WithScheme(scheme()).Build(), func() bool { return false })
	for _, r := range []string{"ok", "not_installed", "permission_not_granted", "scope_mismatch", "error"} {
		s.RunTokenMint(ctx, "implementer", r)
	}
	s.RunTokenMint(ctx, "publisher", "ok")
	s.RunTokenMint(ctx, "an agent's text", "a token's text")
	s.RunTokenRevoke(ctx, "ok")
	s.RunTokenRevoke(ctx, "error")
	s.RunTokenRevoke(ctx, "a token's text")
	body := scrape()
	for _, want := range []string{
		`maku_factory_run_token_mints_total{result="ok",role="implementer"} 1`,
		`maku_factory_run_token_mints_total{result="not_installed",role="implementer"} 1`,
		`maku_factory_run_token_mints_total{result="permission_not_granted",role="implementer"} 1`,
		`maku_factory_run_token_mints_total{result="scope_mismatch",role="implementer"} 1`,
		`maku_factory_run_token_mints_total{result="error",role="implementer"} 1`,
		`maku_factory_run_token_mints_total{result="ok",role="publisher"} 1`,
		`maku_factory_run_token_mints_total{result="other",role="other"} 1`,
		`maku_factory_run_token_revokes_total{result="ok"} 1`,
		`maku_factory_run_token_revokes_total{result="error"} 1`,
		`maku_factory_run_token_revokes_total{result="other"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(body, "text") {
		t.Errorf("a label carries free text:\n%s", body)
	}
}
