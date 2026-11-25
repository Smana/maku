// SPDX-License-Identifier: Apache-2.0

package reasons

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEveryReasonHasShortLongAndNextWords(t *testing.T) {
	if len(Codes()) < 100 {
		t.Fatalf("only %d reasons migrated", len(Codes()))
	}
	for _, code := range Codes() {
		w := For(code)
		if w.Short == "" || w.Long == "" || w.Next == "" {
			t.Errorf("%s: %+v", code, w)
		}
		if !utf8.ValidString(w.Short) || utf8.RuneCountInString(w.Short) > 40 {
			t.Errorf("%s: short label %q is invalid or too long", code, w.Short)
		}
		if !strings.Contains(w.Long, "policy-bot") {
			continue
		}
		t.Errorf("%s mentions policy-bot with policy-bot off: %q", code, w.Long)
	}
}

func TestPolicyBotWordsRequireTheSwitch(t *testing.T) {
	if got := For("policy_absent").Long; strings.Contains(got, "policy-bot") {
		t.Fatalf("default wording names policy-bot: %q", got)
	}
	if got := For("policy_absent", true).Long; !strings.Contains(got, "policy-bot") {
		t.Fatalf("legacy policy-bot wording lost its name: %q", got)
	}
}

func TestMarkdownComesFromTheTable(t *testing.T) {
	got := Markdown("no_policy_matched")
	for _, want := range []string{"| Reason | Means | Next step |", "`no_policy_matched`", For("no_policy_matched").Next} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}
