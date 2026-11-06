// SPDX-License-Identifier: Apache-2.0

package intake_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/intake"
)

// pub is the visibility double the guard's tests are built over: a repository in the map is public,
// one absent is private, and a nil map is GitHub unable to answer at all. It reads names as GitHub
// does, without regard to case.
type pub map[string]bool

func (p pub) Public(_ context.Context, repository string) (bool, error) {
	if p == nil {
		return false, errors.New("github: no answer")
	}
	for repo, public := range p {
		if strings.EqualFold(repo, repository) {
			return public, nil
		}
	}
	return false, nil
}

// pubOf is the double above from a plain map: a repository in it is public, one absent is private,
// and nil answers an error for every repository.
func pubOf(public map[string]bool) pub { return pub(public) }

// Review Focus 2: a candidate GitHub reports private, or cannot answer for, never reaches the
// model, and a private one is not named back as an alternative.
func TestPrivateOrUnverifiableRepositoriesNeverReachTheModel(t *testing.T) {
	g := intake.Guard{Allow: map[string]bool{"smana/ai-native-ref": true, "smana/private": true},
		MayWrite: func(context.Context, string) (bool, error) { return true, nil },
		Public:   pubOf(map[string]bool{"Smana/ai-native-ref": true, "Smana/private": false})}
	got, r := g.Candidates(t.Context(), []string{"Smana/private", "Smana/ai-native-ref"})
	if r != nil || !slices.Equal(got, []string{"smana/ai-native-ref"}) {
		t.Fatalf("%v %+v", got, r)
	}
	// Named explicitly and private: refused with the reason and the two ways forward.
	if _, r := g.Candidates(t.Context(), []string{"Smana/private"}); r == nil || r.Reason != intake.ReasonPrivateRepository ||
		!strings.Contains(r.Sentence, "issue_draft") {
		t.Fatalf("%+v", r)
	} else if strings.Contains(r.Sentence, "Smana") {
		t.Fatalf("the refusal names the repository: %s", r.Sentence)
	}
	// GitHub cannot answer: refused, never planned on a guess.
	g.Public = pubOf(nil) // every lookup errors
	if _, r := g.Candidates(t.Context(), []string{"Smana/ai-native-ref"}); r == nil || r.Reason != intake.ReasonRepositoryUnverified {
		t.Fatalf("%+v", r)
	}
}

// CD5: the candidate set is the allowlist ∩ what the requester may write; a repository outside
// either is not a candidate and is not named back.
func TestCandidatesAreTheAllowlistIntersectedWithWrite(t *testing.T) {
	g := intake.Guard{Allow: map[string]bool{"a/one": true, "a/two": true},
		MayWrite: func(_ context.Context, repo string) (bool, error) { return repo == "a/two", nil },
		Public:   pubOf(map[string]bool{"a/one": true, "a/two": true})}
	if got, _ := g.Candidates(t.Context(), []string{"a/one", "a/two", "z/outsider"}); !slices.Equal(got, []string{"a/two"}) {
		t.Fatalf("%v", got)
	}
	if _, r := g.Candidates(t.Context(), []string{"a/one"}); r == nil || r.Reason != intake.ReasonNoWrite {
		t.Fatalf("%+v", r)
	}
}

// The candidate set's shape: the allowlist owns the order (a map has none, and two processes must
// agree), a spelling is not a repository, nothing named narrows nothing, and the contract's bound is
// 20 candidates.
func TestCandidatesAreOrderedLowercasedAndBounded(t *testing.T) {
	writeAll := func(context.Context, string) (bool, error) { return true, nil }
	for _, c := range []struct {
		name  string
		allow map[string]bool
		// mayWrite is checked per repository; nil means every repository.
		mayWrite func(ctx context.Context, repository string) (bool, error)
		asked    []string
		want     []string
		// wantReason is the reason an empty want must be refused with.
		wantReason string
		// checks is the number of write checks a request may cost: a candidate set that checks a
		// repository twice is a candidate set that asks GitHub twice.
		checks int
	}{
		{name: "the allowlist owns the order",
			allow: map[string]bool{"b/two": true, "a/one": true, "c/three": true},
			asked: []string{"c/three", "a/one", "b/two"},
			want:  []string{"a/one", "b/two", "c/three"}, checks: 3},
		{name: "one repository in three spellings is one candidate",
			allow: map[string]bool{"A/One": true},
			asked: []string{"A/ONE", "a/one", "A/One"},
			want:  []string{"a/one"}, checks: 1},
		{name: "nothing named narrows nothing, so the allowlist is the ask",
			allow:    map[string]bool{"a/one": true, "b/two": true},
			mayWrite: func(_ context.Context, repo string) (bool, error) { return repo == "a/one", nil },
			asked:    nil,
			want:     []string{"a/one"}, checks: 2},
		{name: "a toggled-off allowlist entry is not a candidate",
			allow: map[string]bool{"a/one": false},
			asked: []string{"a/one"},
			want:  nil, wantReason: intake.ReasonRepositoryNotAllowed},
		{name: "a write check that errored on one of several candidates drops it",
			allow: map[string]bool{"a/one": true, "a/two": true},
			mayWrite: func(_ context.Context, repo string) (bool, error) {
				if repo == "a/one" {
					return false, errors.New("github: 502")
				}
				return true, nil
			},
			asked: []string{"a/one", "a/two"},
			want:  []string{"a/two"}, checks: 2},
		{name: "several candidates that all failed a check are unverified, not not-allowed",
			allow: map[string]bool{"a/one": true},
			mayWrite: func(context.Context, string) (bool, error) {
				return false, errors.New("github: 502")
			},
			asked: []string{"a/one", "z/other"},
			want:  nil, wantReason: intake.ReasonRepositoryUnverified, checks: 1},
		{name: "the contract's bound is 20 candidates",
			allow: func() map[string]bool {
				allow := map[string]bool{}
				for i := range 25 {
					allow[fmt.Sprintf("o/r%02d", i)] = true
				}
				return allow
			}(),
			asked: nil,
			want: func() []string {
				out := make([]string, 0, 20)
				for i := range 20 {
					out = append(out, fmt.Sprintf("o/r%02d", i))
				}
				return out
			}(),
			checks: 20},
	} {
		t.Run(c.name, func(t *testing.T) {
			checks := 0
			mayWrite := c.mayWrite
			if mayWrite == nil {
				mayWrite = writeAll
			}
			g := intake.Guard{Allow: c.allow,
				MayWrite: func(ctx context.Context, repo string) (bool, error) {
					checks++
					return mayWrite(ctx, repo)
				},
				// Every repository the allowlist serves is public: this table is about who is
				// allowed to ask, not about what GitHub answers.
				Public: pub(publicly(c.allow))}
			got, r := g.Candidates(t.Context(), c.asked)
			if !slices.Equal(got, c.want) {
				t.Fatalf("%v, want %v (refusal %+v)", got, c.want, r)
			}
			if c.want == nil && (r == nil || r.Reason != c.wantReason) {
				t.Fatalf("an empty candidate set must say why: %+v", r)
			}
			if c.want != nil && r != nil {
				t.Fatalf("a request that holds was refused: %+v", r)
			}
			if checks != c.checks {
				t.Fatalf("%d write checks, want %d", checks, c.checks)
			}
		})
	}
}

// publicly is the map of every repository an allowlist serves, each of them public.
func publicly(allow map[string]bool) map[string]bool {
	public := make(map[string]bool, len(allow))
	for repo := range allow {
		public[repo] = true
	}
	return public
}

// A repository the requester names and the guard cannot offer is refused without being named: the
// refusal is the person's whole answer, and the name is theirs to keep.
func TestARepositoryOutsideTheAllowlistIsNeverNamedBack(t *testing.T) {
	g := intake.Guard{Allow: map[string]bool{"a/one": true},
		MayWrite: func(context.Context, string) (bool, error) { return true, nil },
		Public:   pubOf(map[string]bool{"a/one": true, "z/secret-repo": true})}
	r := func() *intake.Refusal { _, r := g.Candidates(t.Context(), []string{"z/secret-repo"}); return r }()
	if r == nil || r.Reason != intake.ReasonRepositoryNotAllowed {
		t.Fatalf("%+v", r)
	}
	if strings.Contains(r.Error(), "secret-repo") {
		t.Fatalf("the refusal echoes the repository: %s", r.Error())
	}
}

// A guard that cannot check write access or visibility plans nothing. It fails towards the person
// with a reason, never towards a panic inside a tool and never towards a request nobody verified.
func TestAGuardWithoutACheckPlansNothing(t *testing.T) {
	allow := map[string]bool{"a/one": true}
	writeAll := func(context.Context, string) (bool, error) { return true, nil }
	for name, g := range map[string]intake.Guard{
		"no write check":     {Allow: allow, Public: pubOf(map[string]bool{"a/one": true})},
		"no visibility":      {Allow: allow, MayWrite: writeAll},
		"neither of them":    {Allow: allow},
		"an empty allowlist": {},
	} {
		t.Run(name, func(t *testing.T) {
			for _, asked := range [][]string{{"a/one"}, nil} {
				if _, r := g.Candidates(t.Context(), asked); r == nil || r.Reason != intake.ReasonRepositoryUnverified {
					t.Fatalf("asked %v: %+v", asked, r)
				}
			}
		})
	}
}
