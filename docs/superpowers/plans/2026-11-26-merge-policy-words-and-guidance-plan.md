# Merge policy words and guidance (plan 1 of the correctness-signals design) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every human surface of a merge policy says what checked the meaning of a change, in one vocabulary (holds, signal-checked, unchecked, agreed, quiet, regression, wrong call). It also teaches a newcomer what the policy does and what to do next. No served decision changes.

**Architecture:** A new pure Go package, `internal/factory/meaning`, derives a policy's correctness signal from fields it already has (CS1) and builds the one sentence of CS7. The decision record gains `signal=`. The factory API returns the sentence beside each policy, binding, class report, preview and timeline. The web pages, `maku policy`, the GitHub decision comments, the promotion pull request and Slack print what the API or the package returns and never rebuild it. The rules of plan 2 (`Unchecked`, the audit, `factory/wrong`, sensitive kinds) are **not** enforced here. The sentence takes a `meaning.Rules` value that is off in this plan, so no surface promises a rule the gate does not yet apply.

**Tech Stack:** Go 1.27.4 (factory `internal/factory`, broker `internal/humanapi`, CLI `internal/app`, Slack `internal/slackapp`), TypeScript (`web/`, vitest, jsdom, esbuild), Node 24.21.0, bash (`hack/`). Versions are the `mise.toml` pins.

**Spec:** [`docs/superpowers/specs/2026-11-26-merge-policy-correctness-signals-design.md`](../specs/2026-11-26-merge-policy-correctness-signals-design.md), sections Vocabulary, CS1, CS7, [Guidance surfaces](../specs/2026-11-26-merge-policy-correctness-signals-design.md#guidance-surfaces) 1 to 4 and 6, and acceptance criteria W1 to W11. Builds on [the 2026-11-24 trust and usability design](../specs/2026-11-24-merge-policies-trust-and-usability-design.md).

## Global Constraints

- Executed in the Maku repository, in a worktree branched from `origin/main`. Every path below is Maku's.
- **No behaviour change.** No gate row, no binding resolution, no evidence count and no promotion rule changes. The only change to what the gate writes is the added `signal=` field of the decision record (W11).
- Wire names stay as they are: `clean`, `bad`, `healthy`, `mergedUnchanged`, `decision=arm`, `shadow_would_arm`, `verdict=healthy`, metric labels, JSON keys and reason codes. Only the words a person reads change.
- **The vocabulary is closed** (spec, Vocabulary): holds, correctness signal, signal-checked, unchecked, agreed, quiet, regression, wrong call. "Verified" is never shown alone. "Right", "correct", "safe" and "successful" appear on no surface. A number is always followed by its judge.
- **Never promise a plan 2 rule.** Plan 2 owns "Never merges without you", "Once live Maku asks you about one in five", "Label it `factory/wrong`", the sensitive-kind note, and the `Unchecked`, `candidates_open` and audit blockers. They exist here only behind `meaning.Rules`, which is off in this plan. Each one has a test with the rules on, so plan 2 only turns them on.
- One Go function builds the sentence: `meaning.Build`. The web reads it from the API. When an older factory returns no sentence, the page says that the sentence is unavailable. It does not rebuild one.
- Every page change ships with its `web/preview` fixture and a vitest. Screenshots at 1280 px and 390 px, light and dark, go on the UI pull request. `web/DESIGN.md` governs: no new dependency, existing tokens and components.
- `task check` exits 0 before every push. The UI bundle is committed: run `task ui:build` and then `task ui:check` after any `web/` change.
- Commits are conventional, in English, with no trailers. Each PR is squash-merged.
- Never set `HOME` or use `env -i` when running tools (mise shims).

## Starting point: what the 2026-11-24 usability plan already built

Decision 10 puts this plan right after the 2026-11-24 usability plan. That plan is built on `origin/main`:

| Usability plan item | Maku PR | On `origin/main` |
|---|---|---|
| E, readable decisions: one reason table (`internal/factory/reasons`), the rendered record | #687 | Yes, `30f96b7f` |
| F, `maku policy list/explain/validate`, pause and resume | #689, #709 | Yes |
| G and H, CRD help, guide, observability | #695 | Yes |
| I, settings pages built around decisions, presets, backtest | #691, #710 | Yes |
| J, promotion: the factory opens the pull request | #692, #717 | Yes |

All of these ship from `v0.54.0`. `origin/main` describes as `v0.54.9`. The live-validation checklists in ai-native-ref's copies of the 2026-11-24 plans are still unticked, and those files still say "no release contains them", which is out of date. The proof checks and tamper checks (MT2) are **not** built: `internal/factory/config/classes.go` has no `proof`. So this plan derives no "equivalence proof" signal.

## What the code says that the spec does not

| # | Finding | Where | How this plan handles it |
|---|---|---|---|
| F1 | The live card's "Live · N clean decisions recorded" counts shadow decisions merged unchanged (`Evidence.Clean` = `Shadow.MergedUnchanged`), not live merges. "34 merged alone" has to come from `Outcomes.Merges` | `web/src/class-card.ts:201`, `internal/factory/evidence/evidence.go` (`Evaluate`) | Task 13 reads `outcomes.merges` and computes quiet as `outcomes.conclusive − outcomes.bad` |
| F2 | A live `semver-minor` class is already refused by class validation (`semver-minor is shadow only (M2)`), so for `deps-minor` the spec's "a live binding would resolve `Unchecked`" is already a refusal today | `internal/factory/config/classes.go` (`validateClass`) | Plan 2 needs no new rule for it. The words say "shadow only" |
| F3 | Maku ships three catalog policies (`docs-links`, `deps-patch`, `docs`). `dead-links`, `deps-minor`, `kb-entries` and `alert-rules` are ai-native-ref objects, so the guided flow cannot rely on "a shipped policy" for six of its nine rows | `charts/agent-factory/templates/merge-policies-catalog.yaml` | Task 16 uses the served policy of that name when the catalog lists it, and otherwise a built-in template |
| F4 | Several plan 1 surfaces in the spec describe plan 2 behaviour. Examples are the `alert-rules` "Never merges without you" sentence, "Once live Maku asks you about every fifth one", the monitor comment's `factory/wrong` line, the sensitive-kind card note, and the `Unchecked` and `candidates_open` blockers. Shipped as written, they would be false until plan 2 lands | spec CS7, Guidance surfaces 1 and 6 | `meaning.Rules`, off here. See Global Constraints |
| F5 | A card that is not eligible shows no Promote button today. It shows "Ask an operator for an exception" instead, so the spec's "Promote: unchanged" has no disabled button to describe | `web/src/class-card.ts` (`classCard`) | Task 14 adds a disabled Promote button with its blockers and keeps the exception button |
| F6 | The policy-page test fixture models `requires` as binding parameters (`["allowedFiles", "holdoutChecks", "oracleWorkflow"]`). The `config.PolicySpec` doc comment says "without … requires", although `MergeClass.Requires` (stages, #694) is served | `web/test/policy-page.test.ts`, `internal/factory/config/binding.go:93` | Task 12 fixes the fixture. Task 1 fixes the comment |
| F7 | `hack/docs.sh` skips `docs/superpowers/`, so neither this plan nor the spec is link-checked | `hack/docs.sh` | Noted only |
| F8 | The decision record is written for refusals too. The spec lists only `:ok` and `none` | `internal/factory/reconciler/merge.go` (`withRecord`) | Task 4 prints `signal=` only on a decision that held (`arm`, `shadow_would_arm`), so every refusal's record stays byte-identical |
| F9 | `MergePolicy.status.signal` and the `CHECKED BY` printer column belong to CS2 | spec CS2 | They move to plan 2. Plan 1 serves the signal through the API only |

## File structure

| File | Responsibility | Task |
|---|---|---|
| `internal/factory/meaning/signal.go` (new) | `Signal`, `Of`, `Prose`, `Record` | 1 |
| `internal/factory/meaning/files.go` (new) | `Files`: file patterns in words (a port of `web/src/policy-words.ts` `patternGloss`) | 2 |
| `internal/factory/meaning/sentence.go` (new) | `Input`, `Rules`, `InForce`, `Sentence`, `Build`, `Stages` | 3 |
| `internal/factory/decision/decision.go` | `Rows.Signal`, `signal=` in `Marker` | 4 |
| `internal/factory/reconciler/merge.go` | Sets `Rows.Signal` on decisions that held | 4 |
| `docs/merge-policies.md`, `docs/factory.md`, `hack/vocabulary.sh` | "Holds, agreed, signal-checked", worked examples, quiet | 5 |
| `internal/factory/evidence/service.go`, `internal/factory/api/evidence.go`, `internal/factory/policyview/{recent,timeline,preview}.go` | `meaning` on API responses, `signal` read back from records | 6 |
| `internal/app/maku_policy_online.go`, `internal/app/maku.go`, `internal/maku/policy.go` | `maku policy explain` (both forms) and `list` | 7 |
| `internal/factory/narrate/narrate.go`, `internal/factory/reasons/reasons.go` | Decision comments and words | 8 |
| `internal/factory/promote/promote.go` | Promotion pull request body | 9 |
| `internal/envelope/policy.go`, `internal/factory/reconciler/policyfact.go`, `internal/slackapp/endline.go` | Slack signal line | 10 |
| `internal/app/policy_sentence_surfaces_test.go` (new), `internal/factory/meaning/vocabulary_test.go` (new) | W1 cross-surface test, Go vocabulary test | 11 |
| `web/src/policy-help.ts` (new) | Help lines and read-only words per field | 12 |
| `web/src/policy-page.ts`, `web/src/sentence.ts` (new) | Header sentence, meaning line, read-only fields, bindings | 12 |
| `web/src/class-card.ts`, `web/src/settings-api.ts`, `web/src/decisions.ts` | Progress words | 13 |
| `web/src/promote-blockers.ts` (new), `web/src/class-card.ts` | Disabled Promote button and its blockers | 14 |
| `hack/gen-policy-explainer.go` (new), `web/src/generated/how-policies-decide.ts` (generated), `web/src/policy-explainer.ts` (new) | The "How merge policies decide" explainer | 15 |
| `web/src/policy-guide.ts` (new) | The guided new-policy flow | 16 |
| `docs/reference/web-ui-settings.md` | Help lines and the guided flow | 12, 16 |

## Pull requests

| PR | Title | Tasks |
|---|---|---|
| A | `feat(policy): derive the correctness signal and build one sentence` | 1–4 |
| B | `docs(merge-policies): holds, agreed, signal-checked` | 5 |
| C | `feat(policy): the API, the CLI and the comments say what checked the meaning` | 6–11 |
| D | `feat(ui): the policy pages explain and guide` | 12–16 |
| — | Acceptance sweep, done before D merges | 17 |

PR B comes before C because the comments, the CLI help and the explainer link to its anchor. `hack/docs.sh` checks that link once it exists.

---

## PR A: `feat(policy): derive the correctness signal and build one sentence`

### Task 1: the signal, derived (CS1)

**Files:**
- Create: `internal/factory/meaning/signal.go`
- Test: `internal/factory/meaning/signal_test.go`
- Modify: `internal/factory/config/binding.go:93` (the doc comment only, F6)

**Interfaces:**
- Produces: `type Signal string` with the constants `LinksOnly`, `SemverPatch`, `SemverMinor`, `Oracle`, `Contract`, `None`; `func Of(name string, cl config.Class) Signal`; `func Prose(cl config.Class) bool`; `func Record(s Signal, contractHeld bool) string`.

- [ ] **Step 1: Write the failing test**

```go
// SPDX-License-Identifier: Apache-2.0

package meaning_test

import (
	"testing"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/meaning"
)

func TestOf(t *testing.T) {
	adopt := &config.Author{Adopt: &config.Adopt{Logins: []string{"renovate[bot]"}, Branches: []string{"^renovate/"}}}
	for _, c := range []struct {
		name  string
		cl    config.Class
		want  meaning.Signal
		prose bool
	}{
		{"docs-links", config.Class{ModifyOnly: true, MaxModifiedLines: 20}, meaning.LinksOnly, false}, // built-in shape
		{"dead-links", config.Class{DiffShape: "links-only", Oracle: &config.Oracle{Probe: "dead-links", MaxFindings: 8}}, meaning.Oracle, false},
		{"deps-patch", config.Class{DiffShape: "semver-patch", Author: adopt}, meaning.SemverPatch, false},
		{"deps-minor", config.Class{DiffShape: "semver-minor", Author: adopt}, meaning.SemverMinor, false},
		{"docs", config.Class{AllowedFiles: []string{`^.+\.mdx?$`}}, meaning.None, true},
		{"kb-entries", config.Class{DiffShape: "added-entry", AllowedFiles: []string{`^kb/.+\.md$`}}, meaning.None, true},
		{"alert-rules", config.Class{AllowedFiles: []string{`^observability/.+\.ya?ml$`}, Requires: []string{"plan"}}, meaning.None, false},
		{"alert-rules", config.Class{AllowedFiles: []string{`^observability/.+\.ya?ml$`}, Requires: []string{"plan", "tests"}}, meaning.Contract, false},
		{"tests-only", config.Class{Requires: []string{"tests"}}, meaning.None, false}, // decision 4: no plan, no signal
		{"narrowed", config.Class{AllowedFiles: []string{`^docs/.+$`}, NarrowedFiles: []string{`^docs/.+\.rst$`}}, meaning.None, true},
	} {
		if got := meaning.Of(c.name, c.cl); got != c.want {
			t.Errorf("Of(%s) = %q, want %q", c.name, got, c.want)
		}
		if got := meaning.Prose(c.cl); got != c.prose {
			t.Errorf("Prose(%s) = %v, want %v", c.name, got, c.prose)
		}
	}
}

func TestRecord(t *testing.T) {
	for _, c := range []struct {
		s    meaning.Signal
		held bool
		want string
	}{
		{meaning.LinksOnly, false, "links-only:ok"},
		{meaning.Oracle, false, "oracle:ok"},
		{meaning.Contract, true, "contract:ok"},
		{meaning.Contract, false, "none"}, // no contract on the task: nothing judged the meaning
		{meaning.None, true, "none"},
	} {
		if got := meaning.Record(c.s, c.held); got != c.want {
			t.Errorf("Record(%q, %v) = %q, want %q", c.s, c.held, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/factory/meaning/ -run 'TestOf|TestRecord' -v`
Expected: FAIL, because `package meaning` does not exist.

- [ ] **Step 3: Write the minimal implementation**

```go
// SPDX-License-Identifier: Apache-2.0

// Package meaning is what checks a merge policy's changes for meaning (the correctness signal of
// the 2026-11-26 design, CS1) and the one sentence every surface prints about a policy (CS7).
// Pure: config in, words out. The web pages, `maku policy explain`, the decision comments, the
// promotion pull request and Slack print what it returns and never rebuild it.
package meaning

import (
	"cmp"
	"slices"
	"strings"

	"github.com/Smana/maku/internal/factory/config"
)

// Signal is a policy's correctness signal, from a closed list; None when nothing independent of
// the author checks what a change means.
type Signal string

const (
	LinksOnly   Signal = "links-only"
	SemverPatch Signal = "semver-patch"
	SemverMinor Signal = "semver-minor"
	Oracle      Signal = "oracle"
	Contract    Signal = "contract"
	None        Signal = "none"
)

// Of derives the signal from the fields the resolved class already has. Bounds (files, caps,
// modifyOnly), added-entry, holdouts, the reviewer and the monitor are never a signal.
func Of(name string, cl config.Class) Signal {
	if cl.Oracle != nil {
		return Oracle
	}
	switch cmp.Or(cl.DiffShape, config.BuiltInShape(name)) {
	case "links-only":
		return LinksOnly
	case "semver-patch":
		return SemverPatch
	case "semver-minor":
		return SemverMinor
	}
	if slices.Contains(cl.Requires, "plan") && slices.Contains(cl.Requires, "tests") {
		return Contract
	}
	return None
}

// proseSuffixes is the closed list of CS2: a pattern ending in one of them admits prose only.
var proseSuffixes = []string{`\.md$`, `\.mdx$`, `\.mdx?$`, `\.rst$`, `\.txt$`, `\.adoc$`}

func allProse(patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	for _, p := range patterns {
		if !slices.ContainsFunc(proseSuffixes, func(s string) bool { return strings.HasSuffix(p, s) }) {
			return false
		}
	}
	return true
}

// Prose reports whether every file the class admits is prose: a changed path must match both
// AllowedFiles and a binding's NarrowedFiles, so either list being all prose is enough.
func Prose(cl config.Class) bool { return allProse(cl.AllowedFiles) || allProse(cl.NarrowedFiles) }

// Record is the decision record's signal= value for a decision that held: "<signal>:ok", or "none"
// when nothing judged the meaning. A contract policy's decision is contract:ok only when the task
// ran under a contract whose gate rows (2b, 2c) held.
func Record(s Signal, contractHeld bool) string {
	if s == None || (s == Contract && !contractHeld) {
		return string(None)
	}
	return string(s) + ":ok"
}
```

In `internal/factory/config/binding.go:93`, change the doc comment to `// PolicySpec is a MergePolicy's class definition: MergeClass, without mode or workflow fields. Requires (workflow stages, WR6) is served.`

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/factory/meaning/ ./internal/factory/config/ -v -run 'TestOf|TestRecord'`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/factory/meaning internal/factory/config/binding.go
git commit -m "feat(policy): derive a policy's correctness signal from its fields"
```

### Task 2: file patterns in words, in Go

`web/src/policy-words.ts` `patternGloss` is the only gloss today. The sentence is built in Go (W1), so the gloss moves to Go. Its tests carry over unchanged. Directories are wrapped in code spans, as CS7 writes `` `observability/` ``.

**Files:**
- Create: `internal/factory/meaning/files.go`
- Test: `internal/factory/meaning/files_test.go`

**Interfaces:**
- Produces: `func Files(patterns []string) string` (Markdown, "" for no patterns); `func natural(xs []string, word string) string` (package-private, reused by Task 3).

- [ ] **Step 1: Write the failing test**

```go
func TestFiles(t *testing.T) {
	for in, want := range map[string]string{
		`^website/content/.+\.mdx?$`:        "Markdown under `website/content/`",
		`^(docs|website/content)/.+\.md$`:   "Markdown under `docs/` or `website/content/`",
		`^README\.md$`:                      "`README.md`",
		`^.+\.mdx?$`:                        "Markdown files",
		`^observability/.+\.ya?ml$`:         "YAML under `observability/`",
		`^docs/`:                            "files under `docs/`",
	} {
		if got := meaning.Files([]string{in}); got != want {
			t.Errorf("Files(%q) = %q, want %q", in, got, want)
		}
	}
	if got := meaning.Files([]string{`^a/.+\.md$`, `^b/.+\.md$`}); got != "Markdown under `a/` or Markdown under `b/`" {
		t.Errorf("two patterns: %q", got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/factory/meaning/ -run TestFiles -v`
Expected: FAIL with `undefined: meaning.Files`

- [ ] **Step 3: Write the implementation** (a line-for-line port of `patternGloss` in `web/src/policy-words.ts`)

```go
var (
	alternativesRE = regexp.MustCompile(`^\(([^)]+)\)(/.*)$`)
	extensionRE    = regexp.MustCompile(`\\\.(mdx?|ya?ml|json|go|ts)(?:\?|$)`)
	metaRE         = regexp.MustCompile(`[.\[\]()+*?|]`)
)

func gloss(pattern string) string {
	p := strings.TrimSuffix(strings.TrimPrefix(pattern, "^"), "$")
	unescape := strings.NewReplacer(`\.`, ".", `\/`, "/")
	if !strings.HasSuffix(p, "/") && !strings.Contains(p, ".+") && !strings.Contains(p, ".*") && !strings.ContainsAny(p, "[(") {
		return "`" + unescape.Replace(p) + "`"
	}
	rest, prefixes := p, []string{}
	if m := alternativesRE.FindStringSubmatch(p); m != nil {
		rest = m[2]
		for _, x := range strings.Split(m[1], "|") {
			prefixes = append(prefixes, x+"/")
		}
	} else if i := metaRE.FindStringIndex(p); i == nil {
		prefixes = append(prefixes, p)
	} else {
		prefixes = append(prefixes, p[:i[0]])
	}
	kind := "files"
	if m := extensionRE.FindStringSubmatch(rest); m != nil {
		switch {
		case strings.HasPrefix(m[1], "md"):
			kind = "Markdown"
		case strings.HasPrefix(m[1], "ya"):
			kind = "YAML"
		default:
			kind = strings.ToUpper(m[1])
		}
	}
	var dirs []string
	for _, x := range prefixes {
		if x = unescape.Replace(x); x != "" {
			dirs = append(dirs, "`"+x+"`")
		}
	}
	if len(dirs) == 0 {
		return kind + " files"
	}
	return kind + " under " + natural(dirs, "or")
}

// Files is patterns in words, joined with "or"; "" for none.
func Files(patterns []string) string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		out = append(out, gloss(p))
	}
	return natural(out, "or")
}

// natural joins "a, b and c".
func natural(xs []string, word string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " " + word + " " + xs[len(xs)-1]
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/factory/meaning/ -run TestFiles -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/factory/meaning/files.go internal/factory/meaning/files_test.go
git commit -m "feat(policy): file patterns in words, in Go"
```

### Task 3: the one sentence (CS7, W1, W8)

**Files:**
- Create: `internal/factory/meaning/sentence.go`
- Test: `internal/factory/meaning/sentence_test.go`

**Interfaces:**
- Consumes: `Of`, `Prose`, `Files`, `natural` (Tasks 1 and 2).
- Produces:

```go
type Rules struct {
	UncheckedStaysShadow bool // CS2: an unchecked policy that is not prose serves in shadow
	AuditEvery           int  // CS4: >0, live prose waits for a maintainer every AuditEvery decisions
	WrongLabel           bool // CS6: factory/wrong is read
}
var InForce = Rules{} // plan 1 enforces none; plan 2 sets them in the commit that enforces each
type Input struct {
	Name                    string
	Class                   config.Class // a binding's resolved class, or the policy's own (PolicySpec.Class())
	Mode                    string       // config.ModeLive, ModeShadow, ModeDisabled; "" for a policy read without a repository
	VerifyFor, RevertWindow time.Duration // the repository's merge.verifyFor and revertWindow; 0 leaves After and Undo out
	AutoRevert              bool
	Rules                   Rules
}
type Sentence struct {
	Signal     Signal `json:"signal"`
	Prose      bool   `json:"prose"`
	Lead       string `json:"lead"`
	When       string `json:"when"`
	CheckedBy  string `json:"checkedBy"`
	NotChecked string `json:"notChecked,omitempty"`
	OnceLive   string `json:"onceLive,omitempty"`
	After      string `json:"after,omitempty"`
	Undo       string `json:"undo,omitempty"`
	WayOut     string `json:"wayOut,omitempty"`
	Text       string `json:"text"` // the whole sentence, Markdown: **bold** labels and `code`
}
func Build(in Input) Sentence
func (s Sentence) SignalLine() string // the one line of Guidance surface 6, e.g. "Meaning checked by the `links-only` shape."
func Stages(requires []string) string // "a plan you approve, tests written before the change"
```

- [ ] **Step 1: Write the failing test.** These are golden strings from CS7. With `Rules{}` (plan 1) the text claims no plan 2 rule. With the rules on, it matches the spec word for word.

```go
var (
	adopt     = &config.Author{Adopt: &config.Adopt{Logins: []string{"renovate[bot]"}, Branches: []string{"^renovate/"}}}
	merge     = func(in meaning.Input) meaning.Input { in.VerifyFor, in.RevertWindow, in.AutoRevert = 30*time.Minute, 7*24*time.Hour, true; return in }
	dur       = func(d time.Duration) config.Duration { return config.Duration{Duration: d} }
	alertYAML = []string{`^observability/.+\.ya?ml$`}
)

func TestBuild(t *testing.T) {
	plan2 := meaning.Rules{UncheckedStaysShadow: true, AuditEvery: 5, WrongLabel: true}
	for _, c := range []struct {
		name string
		in   meaning.Input
		want string
	}{
		{"docs-links live", merge(meaning.Input{Name: "docs-links", Mode: "live", Class: config.Class{ModifyOnly: true, MaxModifiedLines: 20,
			HoldoutChecks: []string{"Check the documentation links 🔗"}}}),
			"**Merges without you when** an agent changes only link targets in Markdown, at most 20 lines, and `Check the documentation links 🔗` passes. " +
				"**Meaning checked by** the `links-only` shape: every changed line is a link target on the same host. **Not checked:** what the new page says. " +
				"**After the merge** Maku watches main for 30 minutes and reverts on red. **Undo:** `factory/revert` within 7 days."},
		{"docs shadow, plan 1", meaning.Input{Name: "docs", Mode: "shadow", Class: config.Class{AllowedFiles: []string{`^website/content/.+\.mdx?$`},
			MaxPatchFiles: 5, MaxModifiedLines: 60, MaxPerDay: 2}},
			"**Would merge without you when** an agent changes Markdown under `website/content/`, at most 5 files and 60 lines, 2 a day, and a reviewer approves. " +
				"**Meaning checked by** nothing: this is prose."},
		{"docs shadow, plan 2", meaning.Input{Name: "docs", Mode: "shadow", Rules: plan2, Class: config.Class{AllowedFiles: []string{`^website/content/.+\.mdx?$`},
			MaxPatchFiles: 5, MaxModifiedLines: 60, MaxPerDay: 2}},
			"**Would merge without you when** an agent changes Markdown under `website/content/`, at most 5 files and 60 lines, 2 a day, and a reviewer approves. " +
				"**Meaning checked by** nothing: this is prose. **Once live** Maku asks you about every fifth one."},
		{"alert-rules today, plan 1", meaning.Input{Name: "alert-rules", Mode: "shadow", Class: config.Class{AllowedFiles: alertYAML, Requires: []string{"plan"}}},
			"**Would merge without you when** an agent changes YAML under `observability/` and a reviewer approves. " +
				"**Meaning checked by** nothing. **Not checked:** what a change means. **To have its meaning checked,** require a plan and tests."},
		{"alert-rules today, plan 2", meaning.Input{Name: "alert-rules", Mode: "shadow", Rules: plan2, Class: config.Class{AllowedFiles: alertYAML, Requires: []string{"plan"}}},
			"**Never merges without you:** nothing checks what a change to YAML under `observability/` means. Maku records when it would have merged. " +
				"**To let it merge alone,** require a plan and tests."},
		{"alert-rules with a contract", merge(meaning.Input{Name: "alert-rules", Mode: "live", Class: config.Class{AllowedFiles: alertYAML, Requires: []string{"plan", "tests"},
			Monitor: &config.Monitor{Verification: &config.VerificationWatch{Within: dur(2 * time.Hour)}, FollowUpWindow: dur(2 * time.Hour)}}}),
			"**Merges after you approve the plan, without a review of the diff, when** an agent changes YAML under `observability/` and a reviewer approves. " +
				"**Meaning checked by** the test contract: red before the change, green after. **Not checked:** behaviour the plan did not name. " +
				"**After the merge** Maku watches main for 30 minutes and reverts on red; waits 2 hours for RunLore's verdict; reads follow-up fixes for 2 hours. " +
				"**Undo:** `factory/revert` within 7 days."},
		{"deps-patch live", merge(meaning.Input{Name: "deps-patch", Mode: "live", Class: config.Class{Author: adopt, DiffShape: "semver-patch", ModifyOnly: true,
			MaxPatchFiles: 3, MaxModifiedLines: 10, MaxPerDay: 2, ReleaseAgeCheck: "renovate/stability-days",
			Monitor: &config.Monitor{Flux: &config.FluxWatch{Within: dur(time.Hour)}, FollowUpWindow: dur(72 * time.Hour)}}}),
			"**Merges without you when** `renovate[bot]` moves only a patch version, at most 3 files and 10 lines, 2 a day, and `renovate/stability-days` passes. " +
				"**Meaning checked by** the `semver-patch` shape: only a patch version moves. **Not checked:** the release itself; Maku waits for its release age. " +
				"**After the merge** Maku watches main for 30 minutes and reverts on red; watches Flux for 1 hour; reads follow-up fixes for 72 hours. " +
				"**Undo:** `factory/revert` within 7 days."},
		{"deps-minor shadow", meaning.Input{Name: "deps-minor", Mode: "shadow", Class: config.Class{Author: adopt, DiffShape: "semver-minor", ModifyOnly: true}},
			"**Would merge without you when** `renovate[bot]` moves only a minor or patch version. " +
				"**Meaning checked by** the `semver-minor` shape: only a minor or patch version moves. **Not checked:** the release itself, and what the new version renders. This policy is shadow only."},
		{"dead-links", meaning.Input{Name: "dead-links", Mode: "shadow", Class: config.Class{DiffShape: "links-only", ModifyOnly: true,
			Oracle: &config.Oracle{Probe: "dead-links", MaxFindings: 8}, HoldoutChecks: []string{"links"}}},
			"**Would merge without you when** an agent fixes only the links a detector reported, in Markdown, and `links` passes. " +
				"**Meaning checked by** a detector: the `dead-links` probe named the broken links, and the diff changes exactly those. **Not checked:** the detector's own errors."},
		{"off", meaning.Input{Name: "docs", Mode: "disabled", Class: config.Class{AllowedFiles: []string{`^.+\.md$`}}},
			"**Merges nothing here:** the policy is off in this repository."},
	} {
		if got := meaning.Build(c.in).Text; got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestSignalLine(t *testing.T) {
	for _, c := range []struct{ in meaning.Input; want string }{
		{meaning.Input{Name: "docs-links", Mode: "live"}, "Meaning checked by the `links-only` shape."},
		{meaning.Input{Name: "x", Mode: "shadow", Class: config.Class{AllowedFiles: alertYAML, Requires: []string{"plan", "tests"}}}, "Meaning checked by the test contract."},
		{meaning.Input{Name: "docs", Mode: "live", Class: config.Class{AllowedFiles: []string{`^.+\.md$`}}}, "Meaning not checked: this is prose."},
		{meaning.Input{Name: "x", Mode: "shadow", Class: config.Class{AllowedFiles: alertYAML}}, "Meaning not checked."},
	} {
		if got := meaning.Build(c.in).SignalLine(); got != c.want {
			t.Errorf("%s: %q, want %q", c.in.Name, got, c.want)
		}
	}
}

func TestStages(t *testing.T) {
	if got := meaning.Stages([]string{"plan", "tests", "judge", "review"}); got != "a plan you approve, tests written before the change, a completion judge and a review" {
		t.Error(got)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/factory/meaning/ -run 'TestBuild|TestSignalLine|TestStages' -v`
Expected: FAIL with `undefined: meaning.Build`

- [ ] **Step 3: Write the implementation**

```go
// Rules are the plan 2 rules the words may promise. InForce is what this build enforces.
type Rules struct {
	UncheckedStaysShadow bool
	AuditEvery           int
	WrongLabel           bool
}

// InForce is off in plan 1: no surface promises a rule the gate does not apply. Plan 2 turns each
// field on in the commit that enforces it.
var InForce = Rules{}

var checkedBy = map[Signal]string{
	LinksOnly:   "the `links-only` shape: every changed line is a link target on the same host",
	SemverPatch: "the `semver-patch` shape: only a patch version moves",
	SemverMinor: "the `semver-minor` shape: only a minor or patch version moves",
	Oracle:      "a detector: the `%s` probe named the broken links, and the diff changes exactly those",
	Contract:    "the test contract: red before the change, green after",
}

var notChecked = map[Signal]string{
	LinksOnly:   "what the new page says",
	SemverPatch: "the release itself; Maku waits for its release age",
	SemverMinor: "the release itself, and what the new version renders. This policy is shadow only",
	Oracle:      "the detector's own errors",
	Contract:    "behaviour the plan did not name",
	None:        "what a change means",
}

var verbs = map[Signal]string{
	LinksOnly: "changes only link targets", SemverPatch: "moves only a patch version",
	SemverMinor: "moves only a minor or patch version", Oracle: "fixes only the links a detector reported",
}

// Build is the sentence of CS7 for one policy, or one repository's binding of it.
func Build(in Input) Sentence {
	cl, s := in.Class, Of(in.Name, in.Class)
	out := Sentence{Signal: s, Prose: Prose(cl)}
	if in.Mode == config.ModeDisabled {
		out.Lead, out.Text = "Merges nothing here:", "**Merges nothing here:** the policy is off in this repository."
		return out
	}
	files := Files(patternsOf(cl))
	if s == None && !out.Prose && in.Rules.UncheckedStaysShadow {
		out.Lead = "Never merges without you:"
		out.When = "nothing checks what a change to " + files + " means. Maku records when it would have merged."
		out.CheckedBy, out.WayOut = "nothing", "To let it merge alone, require a plan and tests."
		out.Text = "**" + out.Lead + "** " + out.When + " **To let it merge alone,** require a plan and tests."
		return out
	}
	verb := "Would merge"
	if in.Mode == config.ModeLive {
		verb = "Merges"
	}
	out.Lead = verb + " without you when"
	if s == Contract {
		out.Lead = verb + " after you approve the plan, without a review of the diff, when"
	}
	out.When = when(in.Name, cl, s, files)
	switch {
	case s == Oracle:
		out.CheckedBy = fmt.Sprintf(checkedBy[Oracle], cl.Oracle.Probe)
	case s != None:
		out.CheckedBy = checkedBy[s]
	case out.Prose:
		out.CheckedBy = "nothing: this is prose"
	default:
		out.CheckedBy = "nothing"
	}
	if !out.Prose || s != None {
		out.NotChecked = notChecked[s]
	}
	if s == None && out.Prose && in.Rules.AuditEvery > 0 {
		out.OnceLive = "Maku asks you about every " + ordinal(in.Rules.AuditEvery) + " one."
	}
	if s == None && !out.Prose {
		out.WayOut = "To have its meaning checked, require a plan and tests."
	}
	if in.VerifyFor > 0 {
		out.After = after(in, cl)
	}
	if in.RevertWindow > 0 {
		out.Undo = "`factory/revert` within " + human(in.RevertWindow) + "."
	}
	out.Text = text(out)
	return out
}
```

Write the helpers in the same file. Each one is fixed by a row of `TestBuild`:

- `patternsOf(cl)` returns `cl.NarrowedFiles` when it is set, otherwise `cl.AllowedFiles`.
- `when(name, cl, s, files)` puts the parts together in this order:
  1. The actor: `an agent`, or the adopted logins in code spans.
  2. The verb: `verbs[s]`, or `adds one entry` for `added-entry`, or `changes`.
  3. The files: `files`, or `in Markdown` for `LinksOnly` and `Oracle` when no pattern is set; nothing for semver shapes. The files take ` in ` before them for the link shapes, so the clause reads "changes only link targets in Markdown".
  4. The caps: `at most N files and M lines`.
  5. The day cap: `N a day`.
  6. The conditions, joined with ` and `: each holdout or release-age check as `` `X` passes``, then `a reviewer approves` when `s` is `None` or `Contract` and the class is not adopted.
  
  Caps and the day cap join with `, `. The conditions join the rest with `, and ` when a cap or the day cap is present, and with ` and ` otherwise.
- `after(in, cl)` starts with `Maku watches main for <verifyFor>`, then adds ` and reverts on red` (or ` and opens a revert on red` without `AutoRevert`). It then appends each set monitor field, joined with `; `, in this order: `watches Flux for X`, `waits X for RunLore's verdict`, `reads the detector's next N runs`, `reads follow-up fixes for X`.
- `human(d)` gives `N minutes` below an hour, `1 hour`/`N hours` below 7 days or when not a whole number of days, and `N days` otherwise. This makes `72h` read "72 hours" and `168h` read "7 days".
- `ordinal(5)` is `fifth`. Cover 2 to 10, the range of `mergeGuardrails.auditEvery`.
- `text(out)` builds `**Lead** When.`, then appends each part that is set: ` **Meaning checked by** CheckedBy.`, ` **Not checked:** NotChecked.`, ` **Once live** OnceLive`, ` **After the merge** After.`, ` **Undo:** Undo`, and the way out as ` **To have its meaning checked,** require a plan and tests.`
- `SignalLine()` returns `Meaning checked by <CheckedBy up to the first ":">.` when `Signal != None`. Otherwise it returns `Meaning not checked: this is prose.` or `Meaning not checked.`
- `Stages(requires)` maps `plan` to "a plan you approve", `tests` to "tests written before the change", `judge` to "a completion judge" and `review` to "a review", joined with `natural(…, "and")`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/factory/meaning/ -v`
Expected: PASS. Every golden string matches.

- [ ] **Step 5: Commit**

```bash
git add internal/factory/meaning/sentence.go internal/factory/meaning/sentence_test.go
git commit -m "feat(policy): one sentence says what a policy merges and what checks it"
```

### Task 4: `signal=` in the decision record (W11)

**Files:**
- Modify: `internal/factory/decision/decision.go` (`Rows`, `Marker`)
- Modify: `internal/factory/reconciler/merge.go:440-494` (`withRecord`: the main record and the `d.Shadows` loop)
- Test: `internal/factory/decision/decision_test.go`, `internal/factory/reconciler/ledger_test.go`, `scripts/shadow-merge-tally_test.sh`

**Interfaces:**
- Consumes: `meaning.Of`, `meaning.Record`.
- Produces: `decision.Rows.Signal string`, printed as ` signal=<value>` after `considered=` and before `reviewer=`, and only when it is non-empty.

- [ ] **Step 1: Write the failing tests**

In `decision_test.go`:

```go
func TestMarkerSignal(t *testing.T) {
	r := decision.Rows{Counted: true, Files: 1, MaxFiles: 5, Lines: 4, MaxLines: 20, Reviewer: "none"}
	without := decision.Marker("abc", "docs-links", "arm", "armed", r)
	r.Signal = "links-only:ok"
	with := decision.Marker("abc", "docs-links", "arm", "armed", r)
	if !strings.Contains(with, " signal=links-only:ok reviewer=none -->") {
		t.Fatalf("signal not before reviewer: %s", with)
	}
	if strings.Replace(with, " signal=links-only:ok", "", 1) != without {
		t.Fatalf("W11: only signal= may differ\n%s\n%s", with, without)
	}
}
```

In `ledger_test.go`, add a case that runs a `docs-links` arm and a `docs` shadow hold through the reconciler fake. Assert that the main record carries `signal=links-only:ok` and the shadow line `signal=none`. Assert that a refused decision (`patch_too_large`) carries no `signal=`. For the six existing marker assertions, assert equality after `regexp.MustCompile(` signal=[^ ]+`).ReplaceAllString(got, "")`, so they keep proving byte-identity apart from the new field (W11).

In `scripts/shadow-merge-tally_test.sh`, add one fixture line to `scripts/testdata/tally/prs-decisions.json` that carries ` signal=none` before `reviewer=`. Assert the same tally as for the line without it. The `jq` capture `[^>]*?reviewer=` already skips the field.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/factory/decision/ ./internal/factory/reconciler/ -run 'TestMarkerSignal|Ledger' -v && bash scripts/shadow-merge-tally_test.sh`
Expected: FAIL with `r.Signal undefined`

- [ ] **Step 3: Implement**

In `decision.go`, add this field to `Rows`:

```go
	// Signal is what judged the meaning of a decision that held (design CS1): "<signal>:ok" or
	// "none"; "" on a refusal, and then it does not print, so a refusal's record is unchanged.
	Signal string
```

In `Marker`, after the `Considered` block:

```go
	if r.Signal != "" {
		dual += " signal=" + r.Signal
	}
```

In `merge.go` `withRecord`, before `ev.Body += "\n\n" + decision.Marker(head, class, …)`:

```go
	if held := d.Verdict == "arm" || d.Reason == "shadow_would_arm"; held && class != "" {
		if cl, ok := cfg.RepositoryClass(t.Spec.Repository, class); ok {
			d.Rows.Signal = meaning.Record(meaning.Of(class, cl), t.Status.Contract != nil)
		}
	}
```

Do the same for each `sh` in `d.Shadows`, with `sh.Name` and `sh.Rows`. Use the existing lookup of a repository's resolved class: `(*config.Repository).Class(name)` in `internal/factory/config/config.go:442`, reached through the `rc` that `withRecord`'s caller already holds. If `rc` is not in scope, pass it in. Do not add a new lookup. `t.Status.Contract` is the task's contract (`ContractReady`). Gate rows 2b and 2c have already held on any decision that reaches `arm` or `shadow_would_arm`.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/factory/... && bash scripts/shadow-merge-tally_test.sh`
Expected: PASS

- [ ] **Step 5: Commit and open PR A**

```bash
git add internal/factory/decision internal/factory/reconciler scripts
git commit -m "feat(gate): record what judged the meaning of a decision that held"
task check
```

---

## PR B: `docs(merge-policies): holds, agreed, signal-checked`

### Task 5: the docs section every surface links to (W9)

**Files:**
- Modify: `docs/merge-policies.md` (new first section, "The catalog" at line 64, "Worked examples" at line 468, "Reading a decision" at line 936)
- Modify: `docs/factory.md` ("The post-merge monitor", line 705)
- Modify: `hack/vocabulary.sh`, `hack/vocabulary_test.sh`

- [ ] **Step 1: Write the failing vocabulary check.** In `hack/vocabulary_test.sh`, add two fixtures. One is a page under a temporary `docs/` that says "34 clean decisions". The other says "the merge was healthy". Both must fail. A third says `` `verdict=healthy` `` and must pass. Extend `hack/vocabulary.sh` so that, on `docs/merge-policies.md`, `docs/factory.md` and `docs/reference/web-ui-settings.md`, prose outside backticks matching `(?i)\bclean (decision|merge|shadow merge)s?\b|\bhealthy\b` fails with: `FAIL: say "agreed" or "quiet" (docs/merge-policies.md#holds-agreed-signal-checked)`.

Run: `bash hack/vocabulary_test.sh`
Expected: FAIL. The new fixtures are not refused yet.

- [ ] **Step 2: Make the check pass on its fixtures**, then run `bash hack/vocabulary.sh`.
Expected: FAIL, listing today's hits in `docs/merge-policies.md` (lines 47, 165, 391, 481, 849, 852, 923, 971) and `docs/factory.md` (the signals table's `Healthy` column).

- [ ] **Step 3: Write the docs**

1. `docs/merge-policies.md` opens, under `# Merge policies` and before `## Start here`, with `## Holds, agreed, signal-checked`:
   - Three sentences: what a policy decides (it **holds**: the change is inside the policy), what the monitor hears (**quiet** or a **regression**), and what the counts mean (who judged: a signal, a maintainer, nobody).
   - The spec's vocabulary table copied exactly. The wire-name column stays.
   - The spec's flow diagram (mermaid, from "The flow, with the new parts"). The dashed plan 2 boxes become plain boxes labelled "(coming)". This page states status once at the top (`hack/docs.sh` refuses per-row status words), so the section says: "The audit, `factory/wrong` and sensitive kinds are described in [the design](superpowers/specs/2026-11-26-merge-policy-correctness-signals-design.md) and are not enforced yet."
   - "What checks the meaning": the CS1 signals table and the "not correctness signals" table.
2. "The catalog": one worked example each for `docs-links`, `deps-patch` and `docs`. Each has:
   - a real diff that holds and one that does not, with the reason code;
   - the sentence, pasted from `meaning.Build` (take the golden strings of Task 3);
   - the decision record with `signal=`;
   - what the monitor then reads;
   - how a wrong call is reported today (`factory/revert` inside the window, a follow-up fix).
3. "Worked examples": an `alert-rules` example rewritten around the contract. It covers the plan ("fire above 5 % for 10 minutes"), the rule test, the change and the record `signal=contract:ok`. It shows the `requires: [plan, tests]` policy YAML of CS3.
4. Replace every hit Step 2 listed. "Clean decisions" becomes "agreed decisions" and "healthy" becomes "quiet". Keep `verdict=healthy` and the `Healthy` condition reasons in backticks as wire values. In `docs/factory.md` "The signals" table, rename the column `Healthy` to `Quiet (wire value `healthy`)` and add one sentence: "Quiet means the window ended with no revert, no regression and no follow-up. It does not mean the change was right ([vocabulary](merge-policies.md#holds-agreed-signal-checked))."
5. The `shadow_would_require_approval` row in the reasons table is generated. Task 8 changes its words.

- [ ] **Step 4: Verify**

Run: `bash hack/vocabulary_test.sh && bash hack/vocabulary.sh && bash hack/docs.sh`
Expected: PASS. The new anchor `#holds-agreed-signal-checked` resolves.

- [ ] **Step 5: Commit and open PR B**

```bash
git add docs/merge-policies.md docs/factory.md hack/vocabulary.sh hack/vocabulary_test.sh
git commit -m "docs(merge-policies): holds, agreed, signal-checked, and a worked example per catalog policy"
```

---

## PR C: `feat(policy): the API, the CLI and the comments say what checked the meaning`

### Task 6: the API carries the sentence

**Files:**
- Modify: `internal/factory/evidence/service.go:64-118` (`ClassReport`, `PolicyReport`, and the `ClassReport{…}` builder at line 548)
- Modify: `internal/factory/api/evidence.go:185` (`visible`: per-binding sentences for the consumers the caller reads)
- Modify: `internal/factory/policyview/preview.go` (`PolicyPreview.Meaning`), `timeline.go` (`Decision.Signal`, `Timeline.Meanings`), `recent.go` (`RecentDecision.Signal`), `service.go` (`Timeline` fills `Meanings`)
- Test: `internal/factory/evidence/service_test.go`, `internal/factory/api/evidence_test.go`, `internal/factory/policyview/{policyview,recent}_test.go`, `internal/humanapi/policy_test.go` (pass-through)

**Interfaces:**
- Produces these JSON fields, all additive:
  - `ClassReport.meaning` (`*meaning.Sentence`)
  - `PolicyReport.meaning` (the policy alone: `Mode: ""`, no repository)
  - `PolicyReport.bindings` (`[]BindingMeaning{Repository, Mode string; Meaning meaning.Sentence; HoldoutChecks, ProtectedFiles []string}`, readable consumers only)
  - `PolicyPreview.meaning`
  - `Timeline.meanings` (`map[string]meaning.Sentence`, one per class in the timeline)
  - `Decision.signal` and `RecentDecision.signal` (the record's `signal=`, `""` for an older record)

- [ ] **Step 1: Write the failing tests**
  - `service_test.go`: a report for a repository that binds `docs-links` live returns `classes[0].meaning.text`, equal to `meaning.Build(...)` for its resolved class with the repository's `VerifyFor`, `RevertWindow` and `AutoRevert`, and `meaning.InForce`.
  - `api/evidence_test.go`: `GET /v1/policy/policies/alert-rules`, read by a reader of only one of two consumers, returns `bindings` with that one repository, and `meaning.signal == "none"`.
  - `recent_test.go`: a record with `signal=links-only:ok` reads back `Signal: "links-only:ok"`. One without it reads `""`.
  - `policyview_test.go`: `Timeline` for a pull request decided under `docs` carries `meanings["docs"].checkedBy == "nothing: this is prose"`. `CheckPolicy` with `requires: [plan, tests]` returns `meaning.signal == "contract"`.

Run: `go test ./internal/factory/evidence/ ./internal/factory/api/ ./internal/factory/policyview/ ./internal/humanapi/ -v -run 'Meaning|Signal|Bindings'`
Expected: FAIL, because the fields are undefined.

- [ ] **Step 2: Implement.** One helper builds every `meaning.Input`, so no caller assembles one by hand:

```go
// internal/factory/meaning/input.go
// For is the Input of repository rc's binding of the class name, under the rules in force.
func For(rc *config.Repository, name string) (Input, bool) {
	cl, ok := rc.Class(name)
	if !ok {
		return Input{}, false
	}
	return Input{Name: name, Class: cl, Mode: config.ModeOf(cl), VerifyFor: rc.Merge.VerifyFor.Duration,
		RevertWindow: rc.Merge.RevertWindow.Duration, AutoRevert: rc.Merge.AutoRevert, Rules: InForce}, true
}
```

Use the existing mode reader if there is one: `policyMode(cl)` in `internal/factory/reconciler/policyfact.go` reads `cl.Live`/`cl.Shadow`. Move it into `config` as `ModeOf` and make the reconciler call it. Then fill each new field from `meaning.Build(meaning.For(rc, name))`. `visible` already holds `cfg` and the readable set. Build `bindings` from `s.bindersOf(cfg, p.Name)` filtered by `reads`. Parse `signal` in `recent.go` and `timeline.go` from the `fields` map they already build from `field.FindAllStringSubmatch`.

- [ ] **Step 3: Run the tests to verify they pass.** Same command. Expected: PASS.
- [ ] **Step 4: Commit:** `git commit -m "feat(policy): the API returns what checks each policy's meaning"`

### Task 7: `maku policy explain` and `list` (W8)

**Files:**
- Modify: `internal/app/maku_policy_online.go` (`explain` gains the `owner/name <policy>` form; `printTimeline`; `list` prints the policies first)
- Modify: `internal/maku/policy.go` (new `Client.PolicyRepository`, GET `/api/policy/repositories/{owner}/{name}`)
- Modify: `internal/app/maku.go:136-138` (usage; help links `docs/merge-policies.md#holds-agreed-signal-checked`)
- Modify: `internal/app/maku_completion.go` if the second positional needs completion
- Test: `internal/app/maku_policy_online_test.go`, `internal/app/maku_policy_docs_test.go`

- [ ] **Step 1: Write the failing tests.** These use the fake broker already in `maku_policy_online_test.go`.

```go
func TestPolicyExplainPR(t *testing.T) {
	out := runPolicy(t, fakeBroker(timelineWith("alert-rules", "shadow", "shadow_would_arm", meaningOf("alert-rules"))), "explain", "Smana/cloud-native-ref#2741")
	for _, want := range []string{
		"alert-rules", "holds:", "meaning checked by: nothing",
		"To have its meaning checked: require a plan and tests.", // W8, rules off
		"recorded as: would have merged. Merge it unchanged if you agree",
	} {
		if !strings.Contains(out, want) { t.Errorf("missing %q in\n%s", want, out) }
	}
}

func TestPolicyExplainPolicy(t *testing.T) {
	out := runPolicy(t, fakeBroker(reportWith("docs", "shadow", evidence(7, 20, 1, 0, "none"))), "explain", "Smana/cloud-native-ref", "docs")
	for _, want := range []string{meaningOf("docs").Text, "7 of 20 agreed: you merged 7 unchanged, changed 1. 0 wrong calls.",
		"Rollback drill: not done.", "needs 13 more decisions you agree with", "no rollback drill on this repository yet"} {
		if !strings.Contains(out, want) { t.Errorf("missing %q", want) }
	}
}

func TestPolicyListTable(t *testing.T) {
	out := runPolicy(t, fakeBroker(reportWith4()), "list", "--repository", "Smana/cloud-native-ref")
	if !strings.HasPrefix(out, "POLICY") || !strings.Contains(out, "CHECKED BY") || !strings.Contains(out, "nothing (prose)") || !strings.Contains(out, "WRONG CALLS") {
		t.Fatal(out)
	}
	if !strings.Contains(out, "PR  ") { t.Fatal("the recent decisions still follow the table") }
}
```

`meaningOf(name)` calls `meaning.Build` on the fixture class, so the CLI is compared with the function and not with a copy (W1). Write `reportWith`, `evidence` and `reportWith4` as fixtures beside the existing ones in that test file.

- [ ] **Step 2: Run the tests to verify they fail.** `go test ./internal/app/ -run 'TestPolicyExplain|TestPolicyListTable' -v`. Expected: FAIL.
- [ ] **Step 3: Implement**
  - `explain`: one positional with `#` keeps today's path. Two positionals (`owner/name`, `policy`) call `PolicyRepository` and print the card. Anything else returns `policy explain takes owner/name#PR, or owner/name and a policy name`.
  - `printTimeline` prints, per class, the lines of the spec's sample:
    - `holds: 1 of 3 files, 6 of 40 lines, reviewer approved.` from the rows; `does not hold: <reason long words>` on a refusal;
    - `meaning checked by: <CheckedBy>`;
    - `<WayOut>` when it is set;
    - `recorded as: …`.
  
    Keep today's tabular step listing behind `--steps`.
  - `list` prints `POLICY MODE CHECKED BY PROGRESS WRONG CALLS` from the repository report, then the existing recent-decisions table. The columns are:
    - CHECKED BY: `meaning.signal`, or `nothing (prose)` / `nothing`.
    - PROGRESS: `N of M agreed` in shadow; `N merged alone, Q quiet` when live, with `outcomes.merges` and `outcomes.conclusive - outcomes.bad` (F1).
    - WRONG CALLS: `evidence.bad`.
    
    `--output json` adds `policies` beside `decisions`.
  - The blocker words come from one Go table, `internal/factory/meaning/blockers.go` (`func Blocker(code string, e evidence-like counts) (says, next string)`), with the rows of the spec's "Why Promote is disabled" table. `Unchecked` and `candidates_open` sit behind `Rules`. Task 14's TypeScript copy is generated from it by `hack/gen-reasons.go` (extend the generator).
- [ ] **Step 4: Run the tests to verify they pass.** Same command. Expected: PASS.
- [ ] **Step 5: Commit:** `git commit -m "feat(cli): maku policy explain and list say what checked the meaning"`

### Task 8: GitHub decision comments and the reason words (W10)

**Files:**
- Modify: `internal/factory/narrate/narrate.go:717-760` (`Armed`, `WouldArm`, `WouldArmAdopted`), `:922` (`MonitorVerdict`)
- Modify: `internal/factory/reconciler/merge.go:317,320,387` (pass the sentence), `internal/factory/reconciler/monitor.go:214,218` (unchanged keys)
- Modify: `internal/factory/reasons/reasons.go:180` (`shadow_would_require_approval` next: "a maintainer approves the current head; this does not count as an agreed decision")
- Regenerate: `web/src/reasons.ts` and the reasons table in `docs/merge-policies.md` with `go run ./hack/gen-reasons.go`
- Test: `internal/factory/narrate/narrate_test.go` (one golden per message)

- [ ] **Step 1: Write the failing golden tests**

```go
func TestDecisionCommentsCarryTheSignal(t *testing.T) {
	task := testTask("t1", "docs-links")
	links := meaning.Build(meaning.Input{Name: "docs-links", Mode: "live"})
	prose := meaning.Build(meaning.Input{Name: "docs", Mode: "shadow", Class: config.Class{AllowedFiles: []string{`^.+\.md$`}}})
	alert := meaning.Build(meaning.Input{Name: "alert-rules", Mode: "shadow", Class: config.Class{AllowedFiles: []string{`^o/.+\.yaml$`}}})
	const docs = "[How merge policies decide](https://github.com/Smana/maku/blob/main/docs/merge-policies.md#holds-agreed-signal-checked)"
	for name, c := range map[string]struct{ got, want string }{
		"merged, signal-checked": {narrate.Armed(task, links, "docs-links").Body,
			"Agent factory task `t1`: Merged automatically by policy `docs-links`, at the decided head: GitHub re-checks that head at merge time and refuses a moved one. main's CI is watched after the merge.\n\nMeaning checked by the `links-only` shape. " + docs},
		"shadow, prose": {narrate.WouldArm(task, prose, "docs", "abc", false, false, 7, 20).Body,
			"Agent factory task `t1`: Policy `docs` would have merged this. It is in shadow here. Merge it unchanged if you agree: that makes 7 of 20 agreed decisions toward going live.\n\nMeaning not checked: this is prose. " + docs},
		"shadow, unchecked": {narrate.WouldArm(task, alert, "alert-rules", "abc", false, false).Body,
			"Agent factory task `t1`: Policy `alert-rules` would have merged this. It is in shadow here. Merge it unchanged if you agree.\n\nMeaning not checked. " + docs},
		"monitor quiet": {narrate.MonitorVerdict(task, 12, "healthy", []string{"main CI: green"}, false, false).Body,
			"Agent factory monitor of #12 (`docs-links`): **quiet**. main CI: green"},
	} {
		if c.got != c.want { t.Errorf("%s:\n got %q\nwant %q", name, c.got, c.want) }
	}
	if k := narrate.MonitorVerdict(task, 12, "healthy", nil, false, false).Key; k != "monitor-12 verdict=healthy" {
		t.Errorf("the key is a wire name: %q", k)
	}
}
```

Also add the merged comment for live prose: "Meaning not checked: this is prose." In plan 1 there is no audit sentence after it. The spec's "Maku asks a maintainer about one in five" appears only when `meaning.InForce.AuditEvery > 0`; test that with the rules on. Add `WouldArmAdopted` for `deps-patch`, ending in "Meaning checked by the `semver-patch` shape."

- [ ] **Step 2: Run the test to verify it fails.** `go test ./internal/factory/narrate/ -run TestDecisionCommentsCarryTheSignal -v`. Expected: FAIL (signature mismatch).
- [ ] **Step 3: Implement**
  - `Armed`, `WouldArm` and `WouldArmAdopted` take a `meaning.Sentence` right after the task and append `"\n\n" + s.SignalLine() + " " + docsLink`.
  - `WouldArm` says "agreed decisions" where it now says "clean decisions".
  - `MonitorVerdict` prints `quiet` for the wire value `healthy` in the body only. It keeps `regression` and `inconclusive`. The key and the metric label stay `healthy`.
  - The breaker paragraph says "class" today. Change it to "The policy goes back to a maintainer at its next decision (breaker.maxFollowUps); nothing is reverted."
  - Define `docsLink` once in `narrate` as a `const`.
  - The callers in `merge.go` take the sentence from `meaning.Build(meaning.For(rc, d.Matched))`.
- [ ] **Step 4: Run the tests to verify they pass:** `go run ./hack/gen-reasons.go && go test ./internal/factory/... ./internal/humanapi/... && bash hack/docs.sh`. Expected: PASS. `git status` shows `web/src/reasons.ts` and `docs/merge-policies.md` regenerated.
- [ ] **Step 5: Commit:** `git commit -m "feat(gate): every decision comment names what checked the meaning"`

### Task 9: the promotion pull request (W1)

**Files:**
- Modify: `internal/factory/promote/promote.go:267-282` (`bodyFor`) and its callers at line 104-122 (pass `cfg` and `rc`)
- Test: `internal/factory/promote/promote_test.go`

- [ ] **Step 1: Write the failing test.** Assert that `bodyFor` for `docs` contains:
  - `meaning.Build(...)` in the future tense. Build it with `Mode: "live"`: the pull request turns it live.
  - `Meaning checked by:` and `Not checked:`.
  - `- 20 agreed decisions (minimum 20)` and `- 0 wrong calls`.
  - the list of supporting pull requests under `Agreed decisions:`.
  
  Assert that it does not contain `clean` or `bad outcomes`.
- [ ] **Step 2: Run:** `go test ./internal/factory/promote/ -v`. Expected: FAIL.
- [ ] **Step 3: Implement**

```go
	s := meaning.Build(in) // in = meaning.For(rc, class) with Mode set to config.ModeLive
	fmt.Fprintf(&body, "Once merged: %s\n\n", s.Text)
	fmt.Fprintf(&body, "Evidence read at %s:\n\n", rep.EvidenceAt.UTC().Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(&body, "- %d agreed decisions (minimum %d)\n- %d wrong calls\n- rollback drill %s\n", e.Clean, e.Minimum, e.Bad, drillWords(e.Drill))
	if len(e.Shadow.Judged) > 0 {
		body.WriteString("\nAgreed decisions:\n")
		// one line per number, as today
	}
```

`drillWords` maps `passed` to "done" and `none` to "not done". Keep the other values with underscores replaced by spaces.

- [ ] **Step 4: Run:** same command. Expected: PASS.
- [ ] **Step 5: Commit:** `git commit -m "feat(promotion): the pull request says what checks the meaning and what is left open"`

### Task 10: the Slack line (W10)

**Files:**
- Modify: `internal/envelope/policy.go:33-52` (`PolicyFact.Signal string`, `json:"signal,omitempty"`, closed list)
- Modify: `internal/factory/reconciler/policyfact.go:29` (fill it from `meaning.Of`)
- Modify: `internal/slackapp/endline.go:137-166` (`gatePart`)
- Modify: `docs/reference/event-envelope.md` (the new field)
- Test: `internal/slackapp/endline_test.go`, `internal/envelope/policy_test.go`

- [ ] **Step 1: Write the failing tests.**
  - A summary merged by class `docs-links` with `Signal: "links-only"` gives `merged automatically by policy docs-links (meaning checked by links-only)`.
  - `Signal: "none"` gives `… (meaning not checked)`.
  - An empty signal (an older room log) gives today's line unchanged.
  - A signal outside `meaning`'s closed list is dropped. Slack only prints closed values (`envelope.ValidSignal`).
- [ ] **Step 2: Run:** `go test ./internal/slackapp/ ./internal/envelope/ -run 'Gate|Signal' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `envelope.ValidSignal`, with the list mirrored from `meaning`. `envelope` must not import `factory`, so a test in `internal/factory/meaning` asserts that the two lists are equal. Then fill the fact and extend `gatePart` in the `"class"` case.
- [ ] **Step 4: Run:** same command. Expected: PASS.
- [ ] **Step 5: Commit:** `git commit -m "feat(slack): the merge line names what checked the meaning"`

### Task 11: one sentence on every surface, and no old words (W1, W5 Go side)

**Files:**
- Create: `internal/app/policy_sentence_surfaces_test.go`
- Create: `internal/factory/meaning/vocabulary_test.go`

- [ ] **Step 1: Write the tests**
  - Surfaces (W1): from one config fixture with `docs-links` live and `alert-rules` shadow, compute `want := meaning.Build(meaning.For(rc, name))`. Assert that:
    - the factory API's `ClassReport.meaning.text` equals `want.Text` (through `internal/factory/api`'s test server);
    - `maku policy explain owner/name docs-links` prints `want.Text`;
    - `promote.BodyFor` contains `want.Text`, after exporting `bodyFor` for the test or testing through `Service.Promote` with a fake;
    - `narrate.Armed(...).Body` contains `want.SignalLine()`;
    - Slack's `gatePart` contains the signal from `want.Signal`.
    
    The web side of W1 is Task 12's vitest, which prints the API's `text` verbatim.
  - Vocabulary (Go side of W5): for every code in `reasons.Codes()`, the `Short`, `Long` and `Next` words, and the bodies of `narrate.Armed`, `WouldArm`, `WouldArmAdopted` and `MonitorVerdict` over the fixture policies, contain neither `\bclean\b` nor `\bhealthy\b` (case-insensitive). They also contain none of `\b(right|correct|safe|successful)\b` and no `\bverified\b` unless it is preceded by `main stayed green` or followed by `: resolved`.

    Today `merged_verified` reads "merged, and main stayed green for 30 minutes", which passes. If a word fails, fix the word, not the test.
- [ ] **Step 2: Run:** `go test ./internal/app/ ./internal/factory/meaning/ -v -run 'Surfaces|Vocabulary'`. Expected: PASS after Tasks 6 to 10. A failure names the surface that drifted.
- [ ] **Step 3: Commit and open PR C**

```bash
git add internal/app/policy_sentence_surfaces_test.go internal/factory/meaning/vocabulary_test.go
git commit -m "test(policy): one sentence on every surface, and no clean or healthy in its words"
task check
```

---

## PR D: `feat(ui): the policy pages explain and guide`

Every task in this PR edits `web/` and ends with `task ui:test`, `task ui:build` and `task ui:check`. Fixtures go in `web/preview/settings-fixtures.ts`.

### Task 12: the policy page: sentence, help lines, every field, bindings (W2, W3, W4)

**Files:**
- Create: `web/src/policy-help.ts`, `web/src/sentence.ts`
- Modify: `web/src/policy-page.ts:12-16` (`PolicyReport` gains `meaning?`, `bindings?`), `:66-86` (`policyPage`), `:94-133` (`field`), `:150-200` (`policyEditor`)
- Modify: `web/src/policy-fields.ts` (`BINDING_FIELDS` labels gain help through `policy-help.ts`)
- Modify: `web/test/policy-page.test.ts` (fixture F6: `requires: ["plan"]`), new `web/test/policy-help.test.ts`
- Modify: `docs/reference/web-ui-settings.md` (`## Settings`: the help lines table)

**Interfaces:**
- Produces:
  - `export const FIELD_HELP: Record<string, string>` (key → help line, the spec's 13 rows: `allowedFiles`, `protectedFiles`, `maxPatchFiles`, `maxModifiedLines`, `maxPerDay`, `modifyOnly`, `diffShape`, `holdoutChecks`, `requiresApproval`, `requires`, `evidence`, `monitor`, `oracle`, `author`, plus `description`, `companionFiles`, `requiredLabel`, `releaseAgeCheck`);
  - `export function readOnlyWords(key: string, value: unknown): string`;
  - `export function sentenceNode(text: string): HTMLElement` (renders `**…**` as `<strong>` and `` `…` `` as `<code>`, everything else as text; no HTML is parsed).

- [ ] **Step 1: Write the failing tests**

```ts
// web/test/policy-help.test.ts
import { describe, expect, it } from "vitest";
import { MEANING_KEYS } from "../src/policy-fields";
import { FIELD_HELP, readOnlyWords } from "../src/policy-help";
import { sentenceNode } from "../src/sentence";

describe("help lines (W3)", () => {
  it("has one for every field the policy can hold, naming what happens when it is exceeded or unmet", () => {
    for (const key of MEANING_KEYS) expect(FIELD_HELP[key], key).toMatch(/\S/);
    expect(FIELD_HELP.maxModifiedLines).toContain("`patch_too_large`");
    expect(FIELD_HELP.holdoutChecks).toContain("not that the change is right");
  });
  it("reads the fields the form does not edit", () => {
    expect(readOnlyWords("requires", ["plan"])).toBe("Tasks that touch these files run: a plan you approve");
    expect(readOnlyWords("evidence", "standard")).toBe("20 agreed decisions before promotion");
    expect(readOnlyWords("monitor", { verification: { within: "2h" }, followUpWindow: "2h" }))
      .toBe("After a merge, waits 2 h for RunLore's verdict and reads follow-up fixes for 2 h. Silence here is quiet, not right.");
    expect(readOnlyWords("author", { adopt: { logins: ["renovate[bot]"], branches: ["^renovate/"] } }))
      .toBe("Pull requests of renovate[bot] from branches starting renovate/");
  });
  it("renders the sentence without parsing HTML", () => {
    const n = sentenceNode("**Merges** `<b>x</b>` now");
    expect(n.querySelector("strong")!.textContent).toBe("Merges");
    expect(n.querySelector("code")!.textContent).toBe("<b>x</b>");
    expect(n.querySelector("b")).toBeNull();
  });
});
```

In `web/test/policy-page.test.ts`, add an `alert-rules` fixture: `spec: {description, requires: ["plan"], evidence: "standard", monitor: {verification: {within: "2h"}, followUpWindow: "2h"}}`, with `meaning: {signal: "none", text: "**Would merge without you when** …"}` and `bindings: [{repository: "Smana/cloud-native-ref", mode: "shadow", meaning: {when: "an agent changes YAML under `observability/` …"}}]`. Assert:

- W2: `.policy-sentence` is the rendered `meaning.text`. `.policy-meaning` reads "Meaning checked by: nothing." `.policy-readonly li` lists `requires`, `evidence` and `monitor` with their words. `table.bindings` has one row: `Smana/cloud-native-ref`, `shadow`, the file scope, and a link to `/settings/merge-policies/Smana/cloud-native-ref`. The page does not contain "No extra policy restriction".
- W4: `app.textContent` does not contain "Each binding must supply" and contains "Tasks that touch these files run: a plan you approve".
- Older factory: without `meaning`, `.policy-sentence` reads "This factory does not say what checks this policy's meaning. Update Maku to see it." It is not rebuilt in the browser.
- Each `label.editor-field` has a `.field-help` line equal to `FIELD_HELP[label.dataset.key]` (W3, over the rendered form).

- [ ] **Step 2: Run:** `cd web && npx vitest run test/policy-help.test.ts test/policy-page.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement**
  - `policy-help.ts`: copy the spec's help-line table into `FIELD_HELP` word for word. Wrap reason codes in backticks; the help line renders through `sentenceNode`.
  - `readOnlyWords` uses `meaning`'s stage words. Keep one copy: `hack/gen-reasons.go` also emits `STAGE_WORDS` from `meaning.Stages`' map into `web/src/reasons.ts`.
  - `field()`: append `el("small", undefined, "field-help")` holding `sentenceNode(FIELD_HELP[key])`.
  - `policyPage`:
    1. Replace `el("p", policyWords(p.spec), "policy-sentence")` with `sentenceNode(p.meaning.text)` (class `policy-sentence`).
    2. Add `p.policy-meaning`: "Meaning checked by: " + `checkedBy`, plus `wayOut` when it is set.
    3. Add a `ul.policy-readonly` for each set key among `requires`, `evidence` (when the form does not edit it), `monitor`, `oracle` and `author`.
    4. Delete line 77, "Each binding must supply".
    5. Replace the consumers table with `table.bindings`. Its columns are Repository (link), Mode, What it may change (`meaning.when`), Added checks, Added protected paths, Status. Keep the refused reason in Status.
  - Editor: the live sentence comes from the preview response's `meaning` after **Check the change**. Before the check, the editor shows the saved sentence with the note "Check the change to see the new sentence." Remove `policyWords` from `policy-page.ts` and `class-card.ts`. Keep `patternGloss` and `patternDetails` for "Show raw file patterns".
  - Add the help-lines table to `docs/reference/web-ui-settings.md` under `## Settings`, generated from the same table by the `hack/gen-reasons.go` extension, so the two cannot drift.
- [ ] **Step 4: Run:** `task ui:test && task ui:build && task ui:check && bash hack/docs.sh`. Expected: PASS.
- [ ] **Step 5: Commit:** `git commit -m "feat(ui): a policy's page says what it merges, what checks it, and what every field does"`

### Task 13: the card's progress and the settings words (W5)

**Files:**
- Modify: `web/src/class-card.ts:33-64` (`evidenceBar`, `generationLine`), `:86-96` (`ladder`), `:196-219` (live, off and shadow progress, eligibility line), `:275` and `:331-335` (exception words); use `meaning.text` in place of `policyWords` at line 172
- Modify: `web/src/settings-api.ts:53-96` (`LADDER_LEGEND`, `cleanCount` → `agreedCount`, `trackRecordParts`, `blockerActions`)
- Modify: `web/src/decisions.ts` (`recentDecisions(recent, now, policy?)` filter, so each number links to its list: `#decisions?policy=<name>`)
- Modify: `web/src/settings.ts` (`Evidence` type gains `outcomes`)
- Modify: `web/test/vocabulary.test.ts` (new test), `web/test/class-card.test.ts`, `web/test/settings-api.test.ts`

- [ ] **Step 1: Write the failing tests**

```ts
// web/test/vocabulary.test.ts, beside the workflow test, reusing stripComments, stringLiterals and ALLOWED
const OLD_RE = /\b(clean|healthy)\b/i;
const NEVER_RE = /\b(right|correct|safe|successful)\b/i;
// Wire names a page sends or reads, never shows.
const WIRE = new Set(["clean", "would_merge:clean", "healthy", "mergedUnchanged"]);

test("merge-policy pages say agreed and quiet, never clean or healthy (W5)", () => {
  const dir = join(__dirname, "..", "src");
  const bad: string[] = [];
  for (const name of ["class-card.ts", "settings-api.ts", "policy-page.ts", "policy-help.ts", "policy-guide.ts",
    "policy-explainer.ts", "promote-blockers.ts", "decisions.ts", "settings.ts", "binding-editor.ts", "policy-describe.ts"]) {
    const src = stripComments(readFileSync(join(dir, name), "utf8"));
    for (const lit of stringLiterals(src)) {
      if (WIRE.has(lit)) continue;
      if (OLD_RE.test(lit)) bad.push(`${name}: ${JSON.stringify(lit)}`);
      if (NEVER_RE.test(lit) && !/not right|not that the change is right/.test(lit)) bad.push(`${name}: ${JSON.stringify(lit)}`);
    }
  }
  expect(bad).toEqual([]);
});
```

In `class-card.test.ts`:

- Shadow, `clean 7, minimum 20, shadow.changed 1, bad 0, drill none`: the card reads "7 of 20 agreed: you merged 7 unchanged, changed 1. 0 wrong calls. Rollback drill: not done."
- Live, `outcomes {merges 34, conclusive 34, bad 0}`, with `meaning.signal "links-only"`: "Live. 34 merged alone, all signal-checked by `links-only`. 34 quiet. 0 wrong calls."
- Live prose (`signal "none"`, `prose true`): "Live. 20 merged alone, unchecked: nothing checks the meaning of prose. 0 wrong calls." There is no audit clause while the rules are off.
- The 7, the 34 and the 0 are links to the decisions table filtered by the policy.
- The "How sure is this?" fold ends with "A wrong call nobody reported is not in this number."

- [ ] **Step 2: Run:** `cd web && npx vitest run test/vocabulary.test.ts test/class-card.test.ts test/settings-api.test.ts`. Expected: FAIL, listing today's literals (`class-card.ts:51,52,61,88,201,218,275,331,335`; `settings-api.ts:53,60,61,67,72`).
- [ ] **Step 3: Implement** the words above.
  - `LADDER_LEGEND`: "29 / 46 / 61 quiet merges in a row prove a wrong-call rate under ~10 % / 6.5 % / 5 % at 95 % confidence; shown, never acted on."
  - `trackRecordParts`: `N wrong calls` in place of `N bad`, with the title "Merges of this policy that were reverted, followed up or regressed." Change "worst-case bad rate" to "worst-case wrong-call rate".
  - Exceptions: "Exception: 12 agreed decisions instead of 20".
  - The off state: "Off: no decisions recorded."
- [ ] **Step 4: Run:** `task ui:test && task ui:build && task ui:check`. Expected: PASS.
- [ ] **Step 5: Commit:** `git commit -m "feat(ui): the card counts agreed, quiet and wrong calls, each with its judge"`

### Task 14: a disabled Promote button says why (W6)

**Files:**
- Create: `web/src/promote-blockers.ts`
- Modify: `web/src/class-card.ts:205-245` (the shadow branch: always show **Promote**, disabled when not eligible, above it the future-tense sentence and one line per blocker; keep "Ask an operator for an exception")
- Test: `web/test/promote-blockers.test.ts`

**Interfaces:**
- Consumes: `BLOCKERS` from `web/src/reasons.ts`, generated from `meaning/blockers.go` (Task 7).
- Produces: `export function blockerLines(e: Evidence, meaning: Sentence | undefined, rules: { uncheckedStaysShadow: boolean }): { says: string; next?: { text: string; href?: string } }[]`.

- [ ] **Step 1: Write the failing test.** There is one case per row of the spec's "Why Promote is disabled" table:

| Input | `says` | `next` |
|---|---|---|
| `below_minimum`, clean 7, minimum 20 | "Needs 13 more decisions you agree with." | "Pull requests waiting for a merge" → the decisions table filtered by `Would merge` |
| `bad_outcome`, bad 1 | "1 wrong call in the last 180 days." | the decisions table filtered to wrong calls |
| `no_drill` | "No rollback drill on this repository yet." | "Run the drill", linking `docs/merge-policies.md#rollback` and showing `maku policy drill owner/name#PR` |
| `Unchecked` with rules on, not prose | "Nothing checks what these changes mean, so this policy cannot merge alone." | "Add a signal" → the guided flow at step 2 |
| `Unchecked` with rules off | no line (the factory does not emit it in plan 1) | — |
| prose, rules on | not a blocker; the line above the button is "Once live, Maku asks you about one in five." | — |
| `candidates_open` | "2 later fixes may have corrected a merge of this policy. Answer them first." | the candidates (plan 2 link, rules on only) |
| unknown code `x_y` | "x y" | — |

Also assert that the button is present and `disabled` when not eligible, and enabled and unchanged when eligible.

- [ ] **Step 2: Run:** `cd web && npx vitest run test/promote-blockers.test.ts test/class-card.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement** `blockerLines` and the card change. Delete `blockerActions` from `settings-api.ts`. Its one caller now uses `blockerLines`.
- [ ] **Step 4: Run:** `task ui:test && task ui:build && task ui:check`. Expected: PASS.
- [ ] **Step 5: Commit:** `git commit -m "feat(ui): a disabled Promote button names each blocker and its next step"`

### Task 15: "How merge policies decide", rendered from the docs

The spec says the page renders the docs section and does not keep a second copy. The bundle cannot read the repository at runtime, so a generator copies the section into a TypeScript module. A test fails when the copy and the docs differ, the same way `web/src/reasons.ts` is held to `internal/factory/reasons`.

**Files:**
- Create: `hack/gen-policy-explainer.go` (reads `docs/merge-policies.md`, takes `## Holds, agreed, signal-checked` up to the next `## `, writes `web/src/generated/how-policies-decide.ts` as `export const HOW_POLICIES_DECIDE = <JSON string>`)
- Modify: `hack/docs.sh` (run it beside `go run ./hack/gen-reasons.go`)
- Create: `web/src/policy-explainer.ts` (a `<details class="policy-explainer">` with the summary "How merge policies decide", rendered through `web/src/render.ts`, the same safe markdown-it renderer as the room; the mermaid block shows as code with the line "The diagram is on the docs page" and a link)
- Modify: `web/src/policy-page.ts` and `web/src/settings.ts` (a link in the page header opens it)
- Test: `web/test/policy-explainer.test.ts`, `hack/docs_test.sh`

- [ ] **Step 1: Write the failing tests.**
  - Vitest: `HOW_POLICIES_DECIDE` equals the section read from `../../docs/merge-policies.md` by the same slicing rule. The explainer contains the vocabulary table's eight words. Its link targets `https://github.com/Smana/maku/blob/main/docs/merge-policies.md#holds-agreed-signal-checked`.
  - `hack/docs_test.sh`: a doc whose generated module is stale fails `hack/docs.sh`.
- [ ] **Step 2: Run:** `cd web && npx vitest run test/policy-explainer.test.ts; bash hack/docs_test.sh`. Expected: FAIL.
- [ ] **Step 3: Implement** the generator, the module and the link.
- [ ] **Step 4: Run:** `bash hack/docs.sh && task ui:test && task ui:build && task ui:check`. Expected: PASS. `git status` is clean after `hack/docs.sh`.
- [ ] **Step 5: Commit:** `git commit -m "feat(ui): the policy page explains how merge policies decide, from the docs"`

### Task 16: the guided new-policy flow (W7)

**Files:**
- Create: `web/src/policy-guide.ts`
- Modify: `web/src/policy-page.ts` (`presets` at line 135: a "Guide me" button above "Start from a shipped policy"; the flow ends by calling the editor's `use(spec)`)
- Modify: `docs/reference/web-ui-settings.md` (the three steps and the kinds table)
- Test: `web/test/policy-guide.test.ts`

**Interfaces:**
- Produces:
  - `export const KINDS: { kind: string; signal: string; preset?: string; says: string; template?: Record<string, unknown> }[]`, the nine rows of the spec's table;
  - `export function policyGuide(catalog: PolicyReport[], use: (spec: Record<string, unknown>) => void, start?: 1 | 2): HTMLElement`.

- [ ] **Step 1: Write the failing test.** For each of the nine kinds:
  - step 1 lists it;
  - choosing it shows step 2 with the suggested signal and the `says` line. "Prose" reads "Meaning checked by nothing: this is prose." "Configuration" reads "Needs a test runner for this file type in the repository. Without it, nothing checks the meaning."
  - step 3 shows the files, caps and day-limit fields with their `FIELD_HELP` lines, then the sentence;
  - **Open in the editor** calls `use` with a spec.
  
  For a kind whose preset the catalog serves (`docs-links`, `deps-patch`, `docs` in the fixture), the spec is that policy's spec. For the others (F3), it is `template`. `alert-rules`' template is `{requires: ["plan", "tests"], evidence: "standard", allowedFiles: []}`. "Generated or formatted files" and "Application code" end at step 2 without the button and show "Available when proofs ship." and "No policy covers it. A maintainer merges." The flow posts nothing (no `fetch` call).

  The sentence at step 3 comes from the preview endpoint after the user presses **Check**, like the editor. Without the check, the step shows the kind's `says` line. The browser never builds a sentence (W1).
- [ ] **Step 2: Run:** `cd web && npx vitest run test/policy-guide.test.ts`. Expected: FAIL.
- [ ] **Step 3: Implement.** Step 2 of the flow is also where Task 14's "Add a signal" lands (`start = 2`).
- [ ] **Step 4: Run:** `task ui:test && task ui:build && task ui:check && bash hack/docs.sh`. Expected: PASS.
- [ ] **Step 5: Commit:** `git commit -m "feat(ui): a guided flow from the kind of change to a pre-filled policy"`

## Task 17: acceptance sweep (before PR D merges)

- [ ] Map each criterion to its evidence in the PR D description:

| # | Evidence |
|---|---|
| W1 | `internal/app/policy_sentence_surfaces_test.go` (Task 11) + `web/test/policy-page.test.ts` prints `meaning.text` verbatim (Task 12) |
| W2 | `web/test/policy-page.test.ts`, the `alert-rules` fixture (Task 12) |
| W3 | `web/test/policy-help.test.ts` over `MEANING_KEYS` and the rendered form (Task 12) |
| W4 | `web/test/policy-page.test.ts` (Task 12) |
| W5 | `web/test/vocabulary.test.ts` (Task 13), `internal/factory/meaning/vocabulary_test.go` (Task 11), `hack/vocabulary.sh` (Task 5) |
| W6 | `web/test/promote-blockers.test.ts` (Task 14) |
| W7 | `web/test/policy-guide.test.ts` (Task 16) |
| W8 | `internal/app/maku_policy_online_test.go` (Task 7) |
| W9 | `hack/docs.sh` link check + `web/test/policy-explainer.test.ts` (Tasks 5, 15) |
| W10 | `internal/factory/narrate/narrate_test.go`, `internal/slackapp/endline_test.go` (Tasks 8, 10) |
| W11 | `internal/factory/decision/decision_test.go`, `internal/factory/reconciler/ledger_test.go` (Task 4) |

- [ ] Run `task check` and cite its exit code.
- [ ] Take screenshots at 1280 px and 390 px, light and dark, of the `alert-rules` and `docs-links` policy pages, a shadow card with a disabled Promote button, and guided-flow step 2, all from `npm run preview` with `web/preview/settings-fixtures.ts`. Attach them to PR D. At 390 px, the document width equals the viewport width.
- [ ] Grep that no surface promises a plan 2 rule while `meaning.InForce` is off: `git grep -n -e "Never merges without you" -e "one in five" -e "factory/wrong" -- internal web/src ':!*_test.go' ':!web/test'`. Every hit must be behind `Rules`.

---

## Plan 2 sketch: rules (Maku), before any second policy goes live

Each row is a task with its own TDD cycle. Each one turns on the matching field of `meaning.InForce` in the same commit, so the words of plan 1 start telling the truth at the moment the rule does.

| # | Task | Turns on |
|---|---|---|
| R1 | Binding reason `Unchecked` (CS2) in `config.Resolve`. An unchecked policy that is not prose never serves above shadow. `MergePolicy.status.signal` and the `CHECKED BY` printer column (F9) | `UncheckedStaysShadow` |
| R2 | `added-entry` no longer qualifies for `evidence: low` (decision 2). This is a validation change that refuses `kb-entries` at `low`, so **plan 3's AD5 must merge in ai-native-ref before a Maku release with R2 is pinned** | — |
| R3 | `contract_absent`: a contract policy does not hold without a task contract (CS3) | — |
| R4 | The audit (CS4): `mergeGuardrails.auditEvery` (2 to 10, default 5), `audit_sample`, `audit_pending`, `audit_disagreed` as a wrong call. The audit comment. The "Next audit in N" card line | `AuditEvery` |
| R5 | `factory/wrong` (CS6): label read with the actor check of `factory/hold`, `maku policy wrong owner/name#PR --reason`, the "Report a wrong call" button, the breaker counting, and the narration "Wrong call on `docs`: #398, reported … after the merge". The monitor comment's "Found it wrong later? Label it `factory/wrong`." | `WrongLabel` |
| R6 | Sensitive kinds (CS5): fixed-base row, `sensitive_change` and `sensitive_unreadable`, `mergeGuardrails.sensitiveKinds` additive only, the hand-off naming the kind, and the card note | — |
| R7 | Evidence (CS8): an agreed decision counts only when its `signal=` matches the policy's current signal. The legacy-record rule. The card's four numbers. The wrong-calls list with route and age | — |
| R8 | Reasons table rows for every new code, with next steps (Guidance surface 4, "Reasons table") | — |
| R9 | Second step: `sensitiveFields`, `merge.latentWindow` (30 days) and late follow-up candidates with `candidates_open` | — |

## Plan 3 sketch: adoption (ai-native-ref and cloud-native-ref)

Order: the cloud-native-ref runner first, because a host change is a cloud-native-ref PR before any `HOST_REF` bump. Then ai-native-ref, at each Maku pin.

| # | Repository | Task | Criterion |
|---|---|---|---|
| P1 | cloud-native-ref | `scripts/ci/tests/`: run `promtool test rules` over each `VMRule`'s `spec` (extract the groups, as `scripts/ci/validate-vmrules.sh` does for `check rules`). Name it as the repository's `merge.contractCheck`. Add one rule test as the worked example the site cites, such as "fires above 5 % for 10 minutes", with a test that a raised threshold fails | AD4 |
| P2 | ai-native-ref | Bump the Maku pin to a release containing plan 1. Bump `HOST_REF` to P1's commit | — |
| P3 | ai-native-ref | `alert-rules` declares `requires: [plan, tests]`. `kb-entries` declares `evidence: standard`, **before** any pin of a Maku release with plan 2's R2. Runbook paths go under `requiresApproval` in each binding | AD5 |
| P4 | ai-native-ref | `website/content/docs/use/guide/merge-policies.md` rewritten: the mental model, the four-row threat table, the alert-rule walkthrough on P1's test, "What Maku will never tell you", and screenshot placeholders. Follows `docs/docs-style.md`; links to cloud-native-ref are absolute | AD1 |
| P5 | ai-native-ref | `website/content/docs/operate/auto-merge-safety.md`: the threat model and the sensitive kinds in force | AD3 |
| P6 | ai-native-ref | `cheat-sheet.md` rows (`factory/wrong`, `maku policy wrong`, `maku policy explain`, the audit). `when-things-go-wrong.md` and `merging.md` lines. Rows for rules not yet in the pinned Maku wait for the pin that has them | AD2 |
| P7 | ai-native-ref | Replace the screenshot placeholders at the first live session after the pin | AD6 |

`task check` in ai-native-ref is the evidence for P2 to P6.

## Self-review

- **Spec coverage.**
  - Vocabulary: Tasks 5, 8, 11 and 13.
  - CS1: Task 1.
  - CS7 sentence: Task 3; the CS7 surfaces: Tasks 6 to 10.
  - Guidance surfaces: point 1 is Tasks 12 to 15; point 2 is Task 16; point 3 is Task 7; point 4 is Tasks 5, 12, 15 and 16; point 6 is Tasks 8 and 10.
  - The reasons-table rows of Guidance surface 4 name plan 2 codes, so they go to R8.
  - Point 5, the website, is plan 3.
  - W1 to W11 are mapped in Task 17.
- **Placeholders.** Each helper Task 3 describes in prose is fixed by a golden row of `TestBuild`. Every test names its file and command.
- **Names.** These names are used the same way across tasks: `meaning.Build`, `meaning.For`, `meaning.Of`, `meaning.Record`, `meaning.Rules`, `meaning.InForce`, `Sentence.SignalLine`, `meaning.Stages`, `Rows.Signal`, `PolicyFact.Signal`, `FIELD_HELP`, `readOnlyWords`, `sentenceNode`, `blockerLines`, `KINDS` and `policyGuide`.

## Estimate

Seventeen tasks in four pull requests:

| PR | Tasks | Size |
|---|---|---|
| A | 4 | About 1 day |
| B | 1 | Half a day |
| C | 6 | About 2 days |
| D | 6 | About 2.5 days |

That is about 6 working days with subagent-driven execution and a review per task, plus one day for review rounds and screenshots. No live cluster is needed: every criterion is offline.
