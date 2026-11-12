// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/webhook"
)

// HR28: a digest-bound approval's card adds the short digest and the protected paths, at most ten
// then "and N more", each escaped; the button value is unchanged.
func TestCardDigestFields(t *testing.T) {
	d := strings.Repeat("ab", 32)
	e := webhook.Event{Room: "aaaaaaaa", ApprovalID: "01M4A1B2C3D4E5F6G7H8J9K0MN", Class: "forge.push", Branch: "agent/26zfnuxm",
		ExpiresAt: "2026-11-14T12:00:00Z", Digest: d, ProtectedMore: 3}
	for i := range 10 {
		e.Protected = append(e.Protected, fmt.Sprintf("dir/f%d.go", i))
	}
	e.Protected[1] = "<!here> & `x`"
	_, raw := approvalCard(e, "https://rooms.example", time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC))
	s := string(raw)
	for _, want := range []string{
		"*Digest*\\n" + d[:12],
		"*Protected paths*\\n`dir/f0.go`, `&lt;!here&gt; &amp; 'x'`, `dir/f2.go`",
		"`dir/f9.go`, and 3 more",
		`"value":"` + cardValue(e.ApprovalID, e.Branch) + `"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the card lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, d[:13]) || strings.Contains(s, "<!here>") {
		t.Fatalf("the full digest or an unescaped path:\n%s", s)
	}
	// Paths that grow when escaped still fit Slack's 2,000-character field: what does not fit is
	// counted with the rest.
	e.Protected, e.ProtectedMore = nil, 2
	for range 10 {
		e.Protected = append(e.Protected, strings.Repeat("&", 256))
	}
	field := "*Protected paths*\n" + protectedLine(e.Protected, e.ProtectedMore, 3)
	if n := len([]rune(field)); n > maxCardField {
		t.Fatalf("the field is %d characters", n)
	}
	if !strings.Contains(field, " more; 3 not shown (hidden characters or too long)") || strings.Contains(field, ", and 2 more;") {
		t.Fatalf("the paths left out are not counted: %s", field[len(field)-80:])
	}
	// Paths the broker refused to show are flagged, even when none is shown.
	if got := protectedLine(nil, 0, 1); got != "none named; 1 not shown (hidden characters or too long)" {
		t.Fatalf("%q", got)
	}
	_, flagged := approvalCard(webhook.Event{Room: "aaaaaaaa", ApprovalID: "01M4A1B2C3D4E5F6G7H8J9K0MN", Class: "forge.push",
		Branch: "agent/26zfnuxm", Digest: d, Protected: []string{"go.mod"}, ProtectedHidden: 1}, "https://rooms.example", time.Now())
	if !strings.Contains(string(flagged), "`go.mod`; 1 not shown (hidden characters or too long)") {
		t.Fatalf("the card does not flag the hidden path:\n%s", flagged)
	}
	// A branch-bound approval's card is unchanged.
	e.Digest, e.Protected, e.ProtectedMore = "", nil, 0
	_, plain := approvalCard(e, "https://rooms.example", time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC))
	if strings.Contains(string(plain), "Digest") || strings.Contains(string(plain), "Protected") {
		t.Fatalf("a branch-bound card:\n%s", plain)
	}
}
