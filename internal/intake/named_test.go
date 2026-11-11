// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"slices"
	"strings"
	"testing"
)

func TestCheckNamed(t *testing.T) {
	known := []string{"Smana/ai-native-ref", "Smana/runlore-kb", "Smana/runlore", "Smana/cloud-native-ref"}
	served := []string{"smana/ai-native-ref"}
	both := []string{"smana/ai-native-ref", "smana/cloud-native-ref"}

	tests := []struct {
		name       string
		text       string
		candidates []string
		want       []string // the narrowed candidates when nothing is refused
		refused    string   // a substring of the sentence when refused
	}{
		{"the live case", "let's add mistral support to runlore", served, nil, "runlore isn't served here (served: Smana/ai-native-ref)"},
		{"the re-plan reply", "the docs are wrong\nReply 1: you're wrong I specifically asked runlore", served, nil, "runlore isn't served here"},
		{"owner/name", "add mistral to Smana/runlore please", served, nil, "Smana/runlore isn't served here"},
		{"an unknown repository by owner/name", "fix Smana/something-else", served, nil, "Smana/something-else isn't served here"},
		{"a github url", "see https://github.com/someone/else/issues/3 for the bug", served, nil, "someone/else isn't served here"},
		{"a url to a known repository", "see https://github.com/Smana/runlore.git", served, nil, "Smana/runlore isn't served here"},
		{"a bare name", "runlore needs a mistral provider", served, nil, "runlore isn't served here"},
		{"a bare name with punctuation", "Please fix (runlore), now.", served, nil, "runlore isn't served here"},
		{"case-insensitive", "add mistral to RunLore", served, nil, "Smana/runlore isn't served here"},
		{"a candidate by bare name", "the ai-native-ref docs link is dead", both, []string{"smana/ai-native-ref"}, ""},
		{"a candidate by owner/name narrows", "SMANA/CLOUD-NATIVE-REF has a dead link", both, []string{"smana/cloud-native-ref"}, ""},
		{"a candidate by url", "see https://github.com/Smana/ai-native-ref/blob/main/README.md", both, []string{"smana/ai-native-ref"}, ""},
		{"two candidates named", "both ai-native-ref and cloud-native-ref link to it", both, both, ""},
		{"no repository named", "the install link in the README is dead", both, both, ""},
		{"a word that is not a repository", "add ai support, native docs and a ref", both, both, ""},
		{"a path is not a repository", "fix internal/slackapp/proposal.go and docs/slack.md", both, both, ""},
		{"a longer name is not the shorter one", "the runlore-kbx page", served, served, ""},
		{"a hyphenated known name is its own", "runlore-kb is stale", served, nil, "runlore-kb isn't served here"},
		{"a candidate and an outsider", "port what ai-native-ref does to runlore", served, nil, "runlore isn't served here"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ref := CheckNamed(tc.text, known, tc.candidates)
			if tc.refused != "" {
				if ref == nil || ref.Reason != ReasonRepositoryNotServedHere || !strings.Contains(ref.Sentence, tc.refused) {
					t.Fatalf("got %+v, want %s containing %q", ref, ReasonRepositoryNotServedHere, tc.refused)
				}
				return
			}
			if ref != nil {
				t.Fatalf("refused: %+v", ref)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
