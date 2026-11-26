// SPDX-License-Identifier: Apache-2.0

package meaning_test

import (
	"regexp"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/meaning"
	"github.com/Smana/maku/internal/factory/narrate"
)

var (
	oldWords   = regexp.MustCompile(`(?i)\b(clean|healthy)\b`)
	neverWords = regexp.MustCompile(`(?i)\b(right|correct|safe|successful)\b`)
)

// The fixture policies every surface test reads: one per kind of signal.
func fixtures() []meaning.Input {
	md := []string{`^.+\.md$`}
	return []meaning.Input{
		{Name: "docs-links", Mode: "live"},
		{Name: "docs", Mode: "shadow", Class: config.Class{AllowedFiles: md}},
		{Name: "alert-rules", Mode: "shadow", Class: config.Class{AllowedFiles: []string{`^o/.+\.yaml$`}}},
		{Name: "alert-rules", Mode: "live", Class: config.Class{AllowedFiles: []string{`^o/.+\.yaml$`}, Requires: []string{"plan", "tests"}}},
		{Name: "deps-patch", Mode: "shadow", Class: config.Class{DiffShape: "semver-patch", Author: adopt}},
		{Name: "dead-links", Mode: "shadow", Class: config.Class{DiffShape: "links-only", Oracle: &config.Oracle{Probe: "dead-links"}}},
	}
}

// Correctness signals W1, W10: a decision message prints the line of the sentence meaning.Build
// made, never one of its own.
func TestDecisionMessagesPrintTheSentencesLine(t *testing.T) {
	tk := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "t1"}}
	for _, in := range fixtures() {
		s := meaning.Build(in)
		for name, body := range map[string]string{
			"armed":    narrate.Armed(tk, s, in.Name).Body,
			"wouldArm": narrate.WouldArm(tk, s, in.Name, "abc", false, false, 3, 20).Body,
			"adopted":  narrate.WouldArmAdopted(tk, in.Class, s, in.Name, "abc", false, false).Body,
		} {
			if !strings.Contains(body, s.SignalLine()+" "+narrate.DocsLink) {
				t.Errorf("%s %s lacks %q:\n%s", in.Name, name, s.SignalLine(), body)
			}
		}
	}
}

// W5, Go side: the sentence and the decision messages never say clean or healthy, and never claim
// a change was right, correct, safe or successful.
func TestTheWordsSayAgreedAndQuiet(t *testing.T) {
	tk := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: "t1"}, Spec: v1alpha1.TaskSpec{PredictedClass: "docs"}}
	var texts []string
	for _, in := range fixtures() {
		for _, r := range []meaning.Rules{{}, {UncheckedStaysShadow: true, AuditEvery: 5, WrongLabel: true}} {
			in.Rules = r
			s := meaning.Build(in)
			texts = append(texts, s.Text, s.SignalLine(), meaning.Progress(in.Mode, s, meaning.Counts{Agreed: 3, Minimum: 20, Merges: 4, Quiet: 4}),
				narrate.Armed(tk, s, in.Name).Body, narrate.WouldArm(tk, s, in.Name, "abc", false, false, 3, 20).Body)
		}
	}
	for _, v := range []string{"healthy", "regression", "inconclusive"} {
		texts = append(texts, narrate.MonitorVerdict(tk, 1, v, []string{"main CI: green"}, false, true).Body)
	}
	for _, code := range []string{"below_minimum", "bad_outcome", "no_drill", "Unchecked", "candidates_open"} {
		says, next := meaning.Blocker(code, meaning.Counts{Agreed: 1, Minimum: 5, WrongCalls: 1}, meaning.Rules{UncheckedStaysShadow: true, WrongLabel: true})
		texts = append(texts, says, next)
	}
	for _, text := range texts {
		if oldWords.MatchString(text) || neverWords.MatchString(text) {
			t.Errorf("an old or forbidden word: %q", text)
		}
	}
}
