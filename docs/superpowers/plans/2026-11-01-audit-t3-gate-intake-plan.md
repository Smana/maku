# Audit T3: gate and intake Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the audit's theme T3: the gate's diff read no longer passes a compare truncated at
GitHub's 300-file cap (H-3), a backslash can no longer re-arm a mention or link that `sanitize.Public`
defused (S-6), the config refuses a triage template that is missing or an `investigate` that
writes beside internal data (S-7), a removed maintainer no longer makes a fresh label vanish (F-5),
and one failing stop or revert item no longer stalls a repository's whole intake (F-6).

**Architecture:** five independent fixes in four packages, one task and one commit each, in the
order a reviewer reads the gate then intake: `internal/factory/forge` (H-3), `internal/factory/sanitize`
(S-6), `internal/factory/config` (S-7), `internal/factory/intake` (F-5, then F-6: both edit
`issues.go`). No interface, CRD or metric changes. One pull request for the theme (D0).

**Tech Stack:** Go 1.27.3, stdlib `testing`, `httptest` (forge's TLS rig), goldmark with GFM (the
sanitize tests' renderer), controller-runtime's fake client and `interceptor.Funcs`,
`forgetest.Fake`, golangci-lint v2 through `task lint`.

**Spec:** [`docs/superpowers/specs/2026-11-01-audit-fixes-design.md`](../specs/2026-11-01-audit-fixes-design.md),
theme T3 and D0; findings in [`2026-11-01-project-audit.md`](../specs/2026-11-01-project-audit.md)
(H-3, S-6, S-7, F-5, F-6). Executors read both.

**Provenance:** every code step below was prototyped on `e14b7949` with `go test -overlay`, the repository
untouched: each new test failed against the code before its fix with the output quoted in its
"run to fail" step, then passed under `-race`; the five fixes together built, passed `go vet
./internal/...`, `gofmt -l` and `go test -race ./internal/humanapi/ ./internal/factory/...`.
golangci-lint was not run on the prototype: `task lint` before each commit is the check. Line
numbers are `e14b7949`'s and drift as tasks land: find blocks by their text.

## Spec deviations

| # | Spec says | Plan does | Evidence |
|---|---|---|---|
| V1 | F-5: count every label event, **and** on `AlreadyExists` for a Task in a terminal phase retry with generation + 1, up to 3 | Count every label event only. No retry | `forge.LabelEvents` (`github.go:152-178`) returns every `labeled` event of the label, oldest first, or `ErrEventsTruncated`, on which `labelEvents` (`issues.go:145-158`) makes the label wait. GitHub never deletes an issue event, so counting every event makes a label's generation its fixed 1-based place among them: each label keeps its generation and every later label is strictly higher. A Task created under the old count has a generation at or below its own event's place, so a fresh label never collides with any earlier Task, old or new. The only `AlreadyExists` left is the same label read twice (S2's dedup: `RemoveLabel` failed, or the listing lags), and that Task can already be terminal when triage refused it within one poll: the spec's retry would then start a second Task for one label. The retry fixes nothing and adds a duplicate run |
| V2 | F-5's pinning test: "creates a Task **and comments**" | Asserts the Task (generation 3, requested by the labeller) and the consumed label, and that intake posts nothing | Intake never comments on a created Task: `TestAMaintainersLabelCreatesOneTask` (`issues_test.go:100-131`) pins zero comments. The reconciler narrates the Task once it exists |

Upgrade edge (V1), accepted: a label whose Task was created by the old count and whose
`RemoveLabel` failed across the rollout gets a new key on the first poll after it. If that Task
is still open, the label is refused `task_active` (one comment); if it already ended, a second
Task starts. It needs a failed label removal inside one rollout.

## Global Constraints

- **Branch:** `fix/audit-t3-gate-intake` from `origin/main`. T3 depends on no other theme (spec,
  Themes table) and may run in a worktree beside T4.
- **D0:** one commit per finding, each with the test that pins it, written first. The PR is
  merged with a rebase, not a squash, so each finding keeps its own commit.
- **Commits:** Conventional Commits in English, the repo's `type(scope): sentence (ID)` style, **no
  trailers**: no `Co-Authored-By`, no generated-with line. Same for the PR body.
- **Gates per task:** the task's named test fails first for the reason given, then passes; then
  `go build ./... && go vet ./...`, the whole package with `go test -race -count=1`, and
  `task lint` (never a bare `golangci-lint`), all before the commit. Task 6 runs `task check`
  (exit 0; `internal/store` needs Docker).
- **Tests:** stdlib `testing` only, table-driven `t.Run` where there are cases, hand-written fakes
  (`forgetest.Fake`, a wrapper embedding it), `httptest` for HTTP, `-race` always.
- **SPDX:** every new `.go` file starts with `// SPDX-License-Identifier: Apache-2.0`.
- **Behaviour a user sees** changes only where a finding is a bug (spec scope): a compare of exactly
  100 files is now read (GitHub lists 300); a config missing `solo`, `pair` or `trio`, or with an
  `investigate` other than `[triager]` beside `runlore` or `api.internalPrincipals`, now fails to load.

## File map

| File | Change | Task |
|---|---|---|
| `internal/factory/forge/github.go:1193-1195` | `Diff` refuses at `>= maxChangedFiles` | 1 |
| `internal/factory/forge/forge.go:29-31` | `ErrDiffTooLarge`'s comment and text | 1 |
| `internal/factory/forge/github_test.go` | `c1...wide` fixture, the default compare at 300, one assertion, `TestDiffRefusesAListingAtGitHubsFileCap` | 1 |
| `internal/factory/reconciler/merge.go:146`, `docs/factory.md:307,862` | The 100-file wording | 1 |
| `internal/factory/sanitize/public.go:115-130` | `codeSpanLive` evens a backslash run | 2 |
| `internal/factory/sanitize/public_test.go` | `live`, `TestPublicSpansSurviveABackslash` | 2 |
| `internal/factory/config/config.go` | `validateTriageTemplates`, called from `Validate` | 3 |
| `internal/factory/config/templates_test.go` | New | 3 |
| `docs/factory.md:1333` | The `templates` row | 3 |
| `internal/factory/intake/issues.go:244-259` | `Generation` counts every label | 4 |
| `internal/factory/intake/issues_test.go:223-239` | The generation unit test | 4 |
| `internal/factory/intake/generation_test.go` | New | 4 |
| `internal/factory/intake/issues.go:193-242,491-595` | `Poll` joins every pass's errors; `stops`/`reverts` per item via `stop`/`revert` | 5 |
| `internal/factory/intake/passes_test.go` | New | 5 |

---

### Task 1: H-3, `Diff` refuses a compare at GitHub's 300-file cap

GitHub's compare lists at most 300 files (`maxChangedFiles`, which `ChangedFiles` and `Commit`
already use); `Diff` refused only exactly 100 (`github.go:1193`), so a compare of 300 or more files
was judged on a partial list and a class with `maxPatchFiles: 0` could arm it. `docs/factory.md:307`
promised "100 files or more" while 101 to 299 were read: the doc follows the fix (300).

**Files:**
- Modify: `internal/factory/forge/github.go:1193-1195`
- Modify: `internal/factory/forge/forge.go:29-31`
- Modify: `internal/factory/forge/github_test.go:598` (add a case before it), `:614-617`, `:1138-1140`; append a test
- Modify: `internal/factory/reconciler/merge.go:146` (comment), `docs/factory.md:307`, `docs/factory.md:862`

**Interfaces:**
- Consumes: nothing new.
- Produces: `forge.ErrDiffTooLarge` unchanged as a sentinel (callers use `errors.Is`); its text
  changes. `(*GitHub).Diff` returns it for `len(cmp.Files) >= maxChangedFiles`.

- [ ] **Step 1: Write the failing test**

In `internal/factory/forge/github_test.go`, the fake compare handler gains a 299-file case. Replace:

```go
		case "c1...big":
			var fs []string
			for i := range 300 {
```

with:

```go
		case "c1...wide": // one under GitHub's cap: the whole list, and no merge base, so Diff stops there
			var fs []string
			for i := range 299 {
				fs = append(fs, fmt.Sprintf(`{"filename":"f%03d.py"}`, i))
			}
			_, _ = io.WriteString(w, `{"status":"ahead","files":[`+strings.Join(fs, ",")+`]}`)
		case "c1...big":
			var fs []string
			for i := range 300 {
```

The default case stood for "a full page of 100", which is no truncation: replace

```go
		default: // mid...new: exactly one page, so more changed files are unread.
			var fs []string
			for i := range 100 {
```

with:

```go
		default: // mid...new: GitHub's cap of 300 files, so more changed files are unread.
			var fs []string
			for i := range 300 {
```

In `TestFilesReadsBothSidesOfADiff`, replace:

```go
	if _, err := r.g.Diff(ctx, "mid", "new"); err == nil || !strings.Contains(err.Error(), "page") {
		t.Errorf("a truncated compare must be an error: %v", err)
	}
```

with:

```go
	if _, err := r.g.Diff(ctx, "mid", "new"); !errors.Is(err, ErrDiffTooLarge) {
		t.Errorf("a truncated compare must be ErrDiffTooLarge: %v", err)
	}
```

Append to the file:

```go
// H-3: a compare lists at most maxChangedFiles (300) files, so a listing that long may hide more,
// whatever the class's maxPatchFiles allows: Diff refuses it, as ChangedFiles does. One fewer is
// the whole list, and Diff goes on to read it.
func TestDiffRefusesAListingAtGitHubsFileCap(t *testing.T) {
	r := newRig(t)
	if _, err := r.g.Diff(t.Context(), "c1", "big"); !errors.Is(err, ErrDiffTooLarge) {
		t.Fatalf("300 files: %v, want ErrDiffTooLarge", err)
	}
	// c1...wide carries no merge base: reaching that check proves the count let 299 files through.
	if _, err := r.g.Diff(t.Context(), "c1", "wide"); errors.Is(err, ErrDiffTooLarge) || err == nil || !strings.Contains(err.Error(), "merge base") {
		t.Fatalf("299 files: %v, want the merge-base check, not ErrDiffTooLarge", err)
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test -race -count=1 ./internal/factory/forge/ -run 'TestDiffRefusesAListingAtGitHubsFileCap|TestFilesReadsBothSidesOfADiff'`

Expected: FAIL. Both 300-file compares pass the `== 100` check and stop at the missing merge base:

```
--- FAIL: TestFilesReadsBothSidesOfADiff
    a truncated compare must be ErrDiffTooLarge: forge: comparing mid and new returned no merge base: ...
--- FAIL: TestDiffRefusesAListingAtGitHubsFileCap
    300 files: forge: comparing c1 and big returned no merge base: ..., want ErrDiffTooLarge
```

- [ ] **Step 3: Refuse at the cap**

In `internal/factory/forge/github.go` (`Diff`), replace:

```go
	if len(cmp.Files) == 100 { // one page exactly: unseen files must not let a diff pass as links-only
		return Diff{}, fmt.Errorf("%w: 100 files differ between %s and %s, the links-only test cannot see them all", ErrDiffTooLarge, base, head)
	}
```

with:

```go
	if len(cmp.Files) >= maxChangedFiles { // GitHub's cap: unseen files must not let a diff pass a class's checks
		return Diff{}, fmt.Errorf("%w: %d files differ between %s and %s, as many as a compare lists: the diff cannot be judged whole",
			ErrDiffTooLarge, len(cmp.Files), base, head)
	}
```

In `internal/factory/forge/forge.go`, replace:

```go
// ErrDiffTooLarge is Diff reaching a full page of files: the files past it are unseen, so the
// diff cannot be judged. The merge path hands such a pull request to a human (patch_too_large).
var ErrDiffTooLarge = errors.New("forge: a diff of a full page of files, the rest unread")
```

with:

```go
// ErrDiffTooLarge is a compare listing as many files as GitHub lists (maxChangedFiles, 300), or
// more commits than one page: the files past it are unseen, so the diff cannot be judged. The
// merge path hands such a pull request to a human (patch_too_large).
var ErrDiffTooLarge = errors.New("forge: a diff past what one compare lists, the rest unread")
```

In `internal/factory/reconciler/merge.go`, replace the comment

```go
			// A full page of files is over any class's maxPatchFiles: hand it off, as the gate does.
```

with:

```go
			// A compare at GitHub's 300-file cap may hide files: hand it off, as the gate does.
```

In `docs/factory.md`, replace line 307:

```markdown
A diff of 100 files or more is `patch_too_large` without being read.
```

with:

```markdown
A compare that lists 300 files or more, GitHub's cap, is `patch_too_large` without being read.
```

and line 862:

```markdown
unreplayable. A `patch_too_large` from a diff over GitHub's page of files prints `-` too. The
```

with:

```markdown
unreplayable. A `patch_too_large` from a compare at GitHub's 300-file cap prints `-` too. The
```

- [ ] **Step 4: Run the tests to make sure they pass**

Run: `go test -race -count=1 ./internal/factory/forge/ -run 'TestDiffRefusesAListingAtGitHubsFileCap|TestFilesReadsBothSidesOfADiff|TestChangedFilesListsTheCompare'`
Expected: PASS.

Then: `go build ./... && go vet ./... && go test -race -count=1 ./internal/factory/forge/... ./internal/factory/reconciler/ ./internal/factory/intake/ && task lint`
Expected: PASS, 0 lint issues (the reconciler and the adopter match `ErrDiffTooLarge` with `errors.Is` only).

- [ ] **Step 5: Commit**

```bash
git add internal/factory/forge/github.go internal/factory/forge/forge.go internal/factory/forge/github_test.go \
  internal/factory/reconciler/merge.go docs/factory.md
git commit -m "fix(forge): Diff refuses a compare at GitHub's 300-file cap, as ChangedFiles does (H-3)"
```

---

### Task 2: S-6, a backslash cannot escape the code span `Public` opens

`codeSpanLive` wraps a mention, reference or autolink in backticks, but keeps the character before
it outside: `\@smana` became ``\`@smana` ``, whose backslash escapes the opening backtick, and the
mention is live again. The same holds for `\#12`, `\Smana/x#7` and `\https://…`. An odd run of
backslashes before the span gains one, so the run escapes itself; an even run (`\\@x`) already
does and must gain none, or it would turn odd.

**Files:**
- Modify: `internal/factory/sanitize/public.go:115-130` (`codeSpanLive`)
- Modify: `internal/factory/sanitize/public_test.go` (append)

**Interfaces:**
- Consumes: the test file's existing `links(out string) []string` helper.
- Produces: test helper `live(out string) []string` (text nodes outside code spans). No API change.

- [ ] **Step 1: Write the failing test**

Append to `internal/factory/sanitize/public_test.go` (its imports already hold `strings`, `testing`,
goldmark, `ast`, `extension` and `gmtext`):

```go
// live is the text GitHub reads for mentions, references and bare links: each text node outside a
// code span, as goldmark parses the output.
func live(out string) []string {
	src := []byte(out)
	var texts []string
	doc := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(gmtext.NewReader(src))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.CodeSpan:
			return ast.WalkSkipChildren, nil
		case *ast.Text:
			texts = append(texts, string(n.Segment.Value(src)))
		}
		return ast.WalkContinue, nil
	})
	return texts
}

// S-6: a backslash before the code span Public opens would escape its backtick and leave what it
// wraps live. An odd run of backslashes gains one, so the run escapes itself; an even run already
// does, and gains none.
func TestPublicSpansSurviveABackslash(t *testing.T) {
	for in, inert := range map[string]string{
		`\@smana`:                 "@smana",
		`\#12`:                    "#12",
		`\\@smana`:                "@smana",
		`\\\#12`:                  "#12",
		`see \Smana/x#7`:          "Smana/x#7",
		`\https://evil.example/x`: "https://evil.example/x",
	} {
		t.Run(in, func(t *testing.T) {
			out, _ := Public(in)
			if !strings.Contains(out, "`"+inert+"`") {
				t.Fatalf("%q: %s is not in a code span: %q", in, inert, out)
			}
			for _, text := range live(out) {
				if strings.Contains(text, inert) {
					t.Fatalf("%q: %s is live in %q", in, inert, out)
				}
			}
			if l := links(out); len(l) != 0 {
				t.Fatalf("%q: %v survived in %q", in, l, out)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test -race -count=1 ./internal/factory/sanitize/ -run TestPublicSpansSurviveABackslash`

Expected: FAIL on every odd run; `\\@smana` passes (its even run already escapes itself):

```
--- FAIL: TestPublicSpansSurviveABackslash/\@smana       "\\@smana": @smana is live in "\\`@smana`"
--- FAIL: TestPublicSpansSurviveABackslash/\#12          "\\#12": #12 is live in "\\`#12`"
--- FAIL: TestPublicSpansSurviveABackslash/\\\#12        "\\\\\\#12": #12 is live in "\\\\\\`#12`"
--- FAIL: TestPublicSpansSurviveABackslash/see_\Smana/x#7
--- FAIL: TestPublicSpansSurviveABackslash/\https://evil.example/x
```

- [ ] **Step 3: Even the backslash run before each span**

In `internal/factory/sanitize/public.go`, replace:

```go
// codeSpanLive wraps every autolink, mention and reference of t in a code span.
func codeSpanLive(t string) string {
	var b strings.Builder
	last := 0
	for _, m := range autolink.FindAllStringSubmatchIndex(t, -1) {
		start, end := m[0], m[1]
		if m[2] >= 0 { // a mention or #n: the character before it stays outside
			start = m[2]
		}
		b.WriteString(t[last:start])
		b.WriteString("`" + t[start:end] + "`")
		last = end
	}
```

with:

```go
// codeSpanLive wraps every autolink, mention and reference of t in a code span. A span opened
// right after an odd run of backslashes would have its backtick escaped, leaving the match live
// (S-6: `\@smana`), so such a run gains one backslash and escapes itself instead. An even run
// already does.
func codeSpanLive(t string) string {
	var b strings.Builder
	last := 0
	for _, m := range autolink.FindAllStringSubmatchIndex(t, -1) {
		start, end := m[0], m[1]
		if m[2] >= 0 { // a mention or #n: the character before it stays outside
			start = m[2]
		}
		before := t[last:start]
		b.WriteString(before)
		if run := len(before) - len(strings.TrimRight(before, `\`)); run%2 == 1 {
			b.WriteByte('\\')
		}
		b.WriteString("`" + t[start:end] + "`")
		last = end
	}
```

The run cannot reach back past `last`: the text before it ends a previous match, and no match
ends in a backslash (the URL pattern stops at one).

- [ ] **Step 4: Run the tests to make sure they pass**

Run: `go test -race -count=1 ./internal/factory/sanitize/ -run 'TestPublic'`
Expected: PASS (the new test and the existing `TestPublicIsInert`, `TestPublicSurvivesBacktickTricks`,
`TestPublicDefusesMentionsAndReferences`).

Then: `go build ./... && go vet ./... && go test -race -count=1 ./internal/factory/sanitize/ ./internal/humanapi/ ./internal/factory/intake/ && task lint`
Expected: PASS, 0 lint issues (`humanapi` and the RunLore intake import `sanitize`).

- [ ] **Step 5: Commit**

```bash
git add internal/factory/sanitize/public.go internal/factory/sanitize/public_test.go
git commit -m "fix(sanitize): a backslash before a defused mention or link no longer escapes its code span (S-6)"
```

---

### Task 3: S-7, the config refuses a missing triage template and a writing `investigate`

`triage.Matrix` (`internal/factory/triage/classify.go:25-37`) and the oracle path choose `solo`,
`pair`, `trio` or `investigate`; `Validate` checked only that a declared template is one of those
names. A missing `pair` is a team with no roles, so no reviewer ever joins; and the reconciler
starts the triager only for a `[triager]` template (`reconciler.go:1009-1013`), so
`investigate: {roles: [implementer]}` runs an implementer on a RunLore finding's internal data
(R38). `triage` imports `config`, so `Validate` names the set triage can choose rather than calling
`Matrix`: `solo`, `pair` and `trio` always (chosen at run time by class labels, the classifier and
the control share), and `investigate`, as exactly `[triager]`, whenever its tasks can arrive:
`runlore` set or `api.internalPrincipals` declared (`triage.Investigates`). The deployed config
(ai-native-ref `helm-values-configmap.yaml:294-299`) declares all four, `investigate: {roles: [triager]}`.

**Files:**
- Modify: `internal/factory/config/config.go:1296` (call) and end of file (`validateTriageTemplates`)
- Create: `internal/factory/config/templates_test.go`
- Modify: `docs/factory.md:1333` (the `templates` row)

**Interfaces:**
- Consumes: `good` (`config_test.go:19`), `withSponsored` (`sponsored_test.go:22`), `runloreBlock`
  (`internal_test.go:11`), all in package `config`'s tests.
- Produces: `(*Config).validateTriageTemplates(bad func(string, ...any))`, unexported.

- [ ] **Step 1: Write the failing test**

Create `internal/factory/config/templates_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// S-7: every template triage can choose is defined, and investigate is a lone triager wherever
// its tasks can arrive: a RunLore finding, or an internal principal's api task, reads internal
// data, so nothing of it may write (R38). Each refusal names its key.
func TestTriageTemplatesAreDefined(t *testing.T) {
	const lore = "runlore:\n  listen: \":8080\"\n  tokenFile: /etc/agent-factory-intake/token\n  minConfidence: 0.75\n  dailyCap: 5\n"
	const investigate = "  investigate: {roles: [triager]}  # R38: proposes a public issue text, never writes\n"
	writes := func(raw string) string {
		return strings.Replace(raw, "investigate: {roles: [triager]}", "investigate: {roles: [implementer]}", 1)
	}
	internal := strings.Replace(withSponsored(runloreBlock), lore, "", 1) // an internal principal, no runlore block
	for name, c := range map[string]struct{ raw, want string }{
		"no solo": {strings.Replace(good, "  solo: {roles: [implementer]}\n", "", 1), "templates.solo is missing"},
		"no pair": {strings.Replace(good, "  pair: {roles: [implementer, reviewer], maxReviewRounds: 2}\n", "", 1), "templates.pair is missing"},
		"no trio": {strings.Replace(good, "  trio: {roles: [implementer, tester, reviewer], maxReviewRounds: 2}\n", "", 1), "templates.trio is missing"},
		"no investigate beside runlore": {strings.Replace(good, investigate, "", 1),
			"templates.investigate.roles must be [triager] while runlore or api.internalPrincipals is set"},
		"an implementer investigating for runlore": {writes(good),
			"templates.investigate.roles must be [triager] while runlore or api.internalPrincipals is set"},
		"an implementer investigating for an internal principal": {writes(internal),
			"templates.investigate.roles must be [triager] while runlore or api.internalPrincipals is set"},
	} {
		t.Run(name, func(t *testing.T) {
			if c.raw == good || c.raw == internal {
				t.Fatal("the edit did not apply")
			}
			if _, err := Parse([]byte(c.raw)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err %v, want it to name %q", err, c.want)
			}
		})
	}
	// Neither runlore nor an internal principal: no investigate task can arrive, so its row is free.
	for name, raw := range map[string]string{
		"no investigate":             strings.Replace(strings.Replace(good, lore, "", 1), investigate, "", 1),
		"an implementer investigate": writes(strings.Replace(good, lore, "", 1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(raw)); err != nil {
				t.Fatal(err)
			}
		})
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test -race -count=1 ./internal/factory/config/ -run TestTriageTemplatesAreDefined`

Expected: FAIL on the six refusal cases; the two "neither" cases pass already:

```
--- FAIL: TestTriageTemplatesAreDefined/no_pair                 err <nil>, want it to name "templates.pair is missing"
--- FAIL: TestTriageTemplatesAreDefined/no_trio                 err <nil>, want ...
--- FAIL: TestTriageTemplatesAreDefined/no_solo                 err config: defaults.template "solo" is not a template, want ...
--- FAIL: TestTriageTemplatesAreDefined/no_investigate_beside_runlore                          err <nil>, want ...
--- FAIL: TestTriageTemplatesAreDefined/an_implementer_investigating_for_runlore               err <nil>, want ...
--- FAIL: TestTriageTemplatesAreDefined/an_implementer_investigating_for_an_internal_principal err <nil>, want ...
```

- [ ] **Step 3: Validate the templates triage can choose**

In `internal/factory/config/config.go` (`Validate`), replace:

```go
	// Nothing reads defaults.template or defaults.tier: checked only when set.
	if _, ok := c.Templates[c.Defaults.Template]; c.Defaults.Template != "" && !ok {
```

with:

```go
	c.validateTriageTemplates(bad)
	// Nothing reads defaults.template or defaults.tier: checked only when set.
	if _, ok := c.Templates[c.Defaults.Template]; c.Defaults.Template != "" && !ok {
```

Append to `internal/factory/config/config.go`:

```go
// validateTriageTemplates (S-7): every template triage can choose is defined. solo, pair and trio
// are chosen by a task's class and tier (triage.Matrix, an oracle class's solo); a missing one is
// a team with no roles, which no reviewer ever joins. investigate is chosen for a RunLore finding
// and an internal principal's api task: wherever one can arrive it must be the lone triager,
// since the reconciler starts the triager only for a [triager] template and an implementer
// otherwise, on internal data (R38).
func (c *Config) validateTriageTemplates(bad func(string, ...any)) {
	for _, name := range []string{"solo", "pair", "trio"} {
		if _, ok := c.Templates[name]; !ok {
			bad("templates.%s is missing: triage chooses it by class and tier", name)
		}
	}
	if c.RunLore == (RunLore{}) && len(c.API.InternalPrincipals) == 0 {
		return // no investigate task can arrive
	}
	if roles := c.Templates["investigate"].Roles; !slices.Equal(roles, []string{"triager"}) {
		bad("templates.investigate.roles must be [triager] while runlore or api.internalPrincipals is set: its tasks read internal data and must not write (R38), got %v", roles)
	}
}
```

In `docs/factory.md`, the `templates` row (line 1333) becomes:

```markdown
| `templates` | `<name>: {roles, maxReviewRounds, reviewerModel}`; every template but a lone `triager` has an `implementer`; `reviewerModel` (a gateway logical name) needs a `reviewer` role. `solo`, `pair` and `trio` are required: triage chooses them by class and tier. Once `runlore` or `api.internalPrincipals` is set, `investigate` is required as `{roles: [triager]}` | Required; `reviewerModel` unset: the other tier's model |
```

- [ ] **Step 4: Run the tests to make sure they pass**

Run: `go test -race -count=1 ./internal/factory/config/ -run TestTriageTemplatesAreDefined`
Expected: PASS.

Then: `go build ./... && go vet ./... && go test -race -count=1 ./internal/factory/config/ ./internal/factory/triage/ ./internal/app/ && task lint`
Expected: PASS (every fixture, `testdata/sponsored.yaml` and `internal/app/runs_kind_test.go`'s
included, declares all four templates with `investigate: {roles: [triager]}`), 0 lint issues.

- [ ] **Step 5: Commit**

```bash
git add internal/factory/config/config.go internal/factory/config/templates_test.go docs/factory.md
git commit -m "fix(config): refuse a missing triage template, and an investigate that writes beside internal data (S-7)"
```

---

### Task 4: F-5, a label's generation counts every label event

`Generation` counted the labels of *current* maintainers: removing one from the config lowered the
count, `IssueKey` collided with an earlier label's Task, `Create` answered `AlreadyExists`, which
intake reads as its dedup, and the label was consumed with no Task and no word. Counting every
label event, whoever labelled, gives each label a generation that never changes (see V1). `ok`
still answers whether the latest labeller is a maintainer.

**Files:**
- Modify: `internal/factory/intake/issues.go:244-259` (`Generation`)
- Modify: `internal/factory/intake/issues_test.go:223-239` (`TestGenerationCountsMaintainersInTimeOrder`)
- Create: `internal/factory/intake/generation_test.go`

**Interfaces:**
- Consumes: `poller`, `labelled`, `tasks` and `t0` (`issues_test.go:36-98`), `forgetest.Fake`.
- Produces: `intake.Generation` with its signature unchanged; its `int` is now the number of label
  events. `stops` and `publish.go:70` read only its `last` and `ok`.

- [ ] **Step 1: Write the failing tests**

Create `internal/factory/intake/generation_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/taskid"
)

// F-5: a maintainer removed from the config no longer lowers the count. bob's label made
// generation 1 and Smana's generation 2, both done; with bob gone, Smana's fresh label is
// generation 3, never a second generation 2 that AlreadyExists would swallow as the dedup.
func TestARemovedMaintainerDoesNotRewindTheGeneration(t *testing.T) {
	done := func(gen int) client.Object {
		return &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: taskid.Name(taskid.IssueKey("Smana/cloud-native-ref", 7, gen)),
			Namespace: "agent-system", Labels: map[string]string{v1alpha1.LabelIssue: "7"}},
			Spec:   v1alpha1.TaskSpec{Issue: 7, Repository: "Smana/cloud-native-ref"},
			Status: v1alpha1.TaskStatus{Phase: v1alpha1.PhaseDone}}
	}
	f := forgetest.NewFake()
	labelled(f, 7, "bob", "Smana", "Smana") // the poller's maintainers are [Smana]: bob was removed
	p, c := poller(t, f, done(1), done(2))
	if err := p.Poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	name := taskid.Name(taskid.IssueKey("Smana/cloud-native-ref", 7, 3))
	var got v1alpha1.Task
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: "agent-system", Name: name}, &got); err != nil {
		t.Fatalf("generation 3 was not created: %v (tasks: %d)", err, len(tasks(t, c)))
	}
	if got.Spec.Source.RequestedBy != "github:Smana" || len(tasks(t, c)) != 3 {
		t.Fatalf("%+v, %d tasks", got.Spec.Source, len(tasks(t, c)))
	}
	if r := f.Removed(7); len(r) != 1 || r[0] != "factory/ready" || len(f.Comments(7)) != 0 {
		t.Fatalf("the label is consumed, with nothing to refuse: removed %q comments %q", r, f.Comments(7))
	}
}
```

In `internal/factory/intake/issues_test.go`, the unit test follows the new count. Replace:

```go
func TestGenerationCountsMaintainersInTimeOrder(t *testing.T) {
	is := func(l string) bool { return l == "Smana" }
	evs := []forge.LabelEvent{{Actor: "Smana", At: t0.Add(2 * time.Minute)}, {Actor: "someone", At: t0.Add(time.Minute)},
		{Actor: "Smana", At: t0}}
	gen, last, ok := Generation(evs, is)
	if gen != 2 || !ok || !last.At.Equal(t0.Add(2*time.Minute)) {
```

with:

```go
func TestGenerationCountsEveryLabelInTimeOrder(t *testing.T) {
	is := func(l string) bool { return l == "Smana" }
	evs := []forge.LabelEvent{{Actor: "Smana", At: t0.Add(2 * time.Minute)}, {Actor: "someone", At: t0.Add(time.Minute)},
		{Actor: "Smana", At: t0}}
	gen, last, ok := Generation(evs, is)
	if gen != 3 || !ok || !last.At.Equal(t0.Add(2*time.Minute)) {
```

- [ ] **Step 2: Run them to make sure they fail**

Run: `go test -race -count=1 ./internal/factory/intake/ -run 'TestARemovedMaintainerDoesNotRewindTheGeneration|TestGenerationCountsEveryLabelInTimeOrder'`

Expected: FAIL. The count of Smana's two labels is 2, whose Task exists, so the label is consumed as
a duplicate:

```
--- FAIL: TestARemovedMaintainerDoesNotRewindTheGeneration
    generation 3 was not created: tasks.maku.ogenki.io "u7uat4qk" not found (tasks: 2)
--- FAIL: TestGenerationCountsEveryLabelInTimeOrder
    2 {...} true
```

- [ ] **Step 3: Count every label event**

In `internal/factory/intake/issues.go`, replace:

```go
// Generation counts maintainers' labels (R4). ok is false when the latest label is not a
// maintainer's: that is the label currently on the issue, and it starts nothing (§1).
func Generation(evs []forge.LabelEvent, isMaintainer func(string) bool) (int, forge.LabelEvent, bool) {
	if len(evs) == 0 {
		return 0, forge.LabelEvent{}, false
	}
	evs = slices.Clone(evs)
	slices.SortStableFunc(evs, func(a, b forge.LabelEvent) int { return a.At.Compare(b.At) })
	gen := 0
	for _, e := range evs {
		if isMaintainer(e.Actor) {
			gen++
		}
	}
	last := evs[len(evs)-1]
	return gen, last, isMaintainer(last.Actor)
}
```

with:

```go
// Generation is the latest label's place among every label event of the trigger label, whoever
// labelled (R4, F-5): GitHub never deletes an event, so each label keeps its generation and a
// later one is always higher. Counting maintainers' labels only, a maintainer removed from the
// config lowered the count, and a fresh label collided with an earlier label's task. ok is false
// when the latest label is not a maintainer's: that is the label currently on the issue, and it
// starts nothing (§1).
func Generation(evs []forge.LabelEvent, isMaintainer func(string) bool) (int, forge.LabelEvent, bool) {
	if len(evs) == 0 {
		return 0, forge.LabelEvent{}, false
	}
	evs = slices.Clone(evs)
	slices.SortStableFunc(evs, func(a, b forge.LabelEvent) int { return a.At.Compare(b.At) })
	last := evs[len(evs)-1]
	return len(evs), last, isMaintainer(last.Actor)
}
```

The `AlreadyExists` comment at `issues.go:363` (`// AlreadyExists is the dedup (S2)`) stays true:
only the same label read twice can collide now.

- [ ] **Step 4: Run the tests to make sure they pass**

Run: `go test -race -count=1 ./internal/factory/intake/ -run 'TestARemovedMaintainerDoesNotRewindTheGeneration|TestGenerationCountsEveryLabelInTimeOrder|TestAMaintainersLabelCreatesOneTask'`
Expected: PASS (the last one pins S2's dedup: a second poll of the same label creates nothing).

Then: `go build ./... && go vet ./... && go test -race -count=1 ./internal/factory/intake/ && task lint`
Expected: PASS, 0 lint issues.

- [ ] **Step 5: Commit**

```bash
git add internal/factory/intake/issues.go internal/factory/intake/issues_test.go internal/factory/intake/generation_test.go
git commit -m "fix(intake): a label's generation counts every label, so a removed maintainer no longer swallows one (F-5)"
```

---

### Task 5: F-6, one failing stop or revert item no longer stalls the poll

`stops` and `reverts` returned on the first item's error, and `Poll` returned on theirs: a locked
issue whose stop answer GitHub refuses (403) stalled the repository's revert, ready and orphan
passes every minute. Each item now runs alone (`stop`, `revert`), its error is collected with
`errors.Join` and its label left for the next poll, and `Poll` runs every pass before it reports
the join. An unread control issue still ends the pass: whether intake may start work is unknown.
A stop list that cannot be read no longer keeps intake from starting work; a maintainer's stop
label stays on its issue until a task matches it (`stops`' contract), so a task started meanwhile
is stopped on the next poll.

**Files:**
- Modify: `internal/factory/intake/issues.go:193-242` (`Poll`), `:491-544` (`stops`, new `stop`), `:552-595` (`reverts`, new `revert`)
- Create: `internal/factory/intake/passes_test.go`

**Interfaces:**
- Consumes: `poller`, `labelled`, `tasks`, `t0`; `forgetest.Fake`'s `SetLabeled`, `SetEvents`,
  `SetAgentPulls`, `Removed`, `Added`; `interceptor.NewClient`.
- Produces: unexported `(*IssuePoller).stop(ctx, n int, all []v1alpha1.Task) error` and
  `(*IssuePoller).revert(ctx, n int, all []v1alpha1.Task) error`; test type `lockedIssue`.

- [ ] **Step 1: Write the failing test**

Create `internal/factory/intake/passes_test.go`:

```go
// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

// lockedIssue is the fake forge with issue n locked: GitHub refuses every comment on it, and
// will on every poll.
type lockedIssue struct {
	*forgetest.Fake
	n int
}

func (f lockedIssue) Comment(ctx context.Context, n int, body string) error {
	if n == f.n {
		return errors.New("forge: comment: 403 Unable to create comment because issue is locked")
	}
	return f.Fake.Comment(ctx, n, body)
}

// F-6: one stop or revert item that fails, a locked issue refusing the factory's answer or a
// task that cannot be patched, is reported, and the same poll still serves every other item,
// the ready label and the orphan scan.
func TestOneFailingItemDoesNotStallThePoll(t *testing.T) {
	task := func(name string, phase string, pr *v1alpha1.PullRequestRef) client.Object {
		return &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "agent-system"},
			Status: v1alpha1.TaskStatus{Phase: phase, PullRequest: pr}}
	}
	f := forgetest.NewFake()
	// Stops: #9's is a non-maintainer's, answered on a locked issue; #12's reaches a running task.
	f.SetLabeled(LabelStop, forge.Item{Number: 9}, forge.Item{Number: 12, PullRequest: true})
	f.SetEvents(9, forge.LabelEvent{Actor: "someone", Label: LabelStop, At: t0})
	f.SetEvents(12, forge.LabelEvent{Actor: "Smana", Label: LabelStop, At: t0})
	// Reverts: #16's task cannot be patched; #14's can.
	f.SetLabeled(LabelRevert, forge.Item{Number: 16, PullRequest: true}, forge.Item{Number: 14, PullRequest: true})
	f.SetEvents(16, forge.LabelEvent{Actor: "Smana", Label: LabelRevert, At: t0})
	f.SetEvents(14, forge.LabelEvent{Actor: "Smana", Label: LabelRevert, At: t0})
	labelled(f, 13, "Smana")
	f.SetAgentPulls(forge.AgentPull{Number: 22, HeadRef: "agent/bbbbbbbb", Labels: []string{"factory/class:review"}})
	p, c := poller(t, lockedIssue{Fake: f, n: 9},
		task("3buqdlot", v1alpha1.PhaseImplementing, &v1alpha1.PullRequestRef{Number: 12}),
		task("4buqdlot", v1alpha1.PhaseDone, &v1alpha1.PullRequestRef{Number: 14, AutoMerged: true}),
		task("5buqdlot", v1alpha1.PhaseDone, &v1alpha1.PullRequestRef{Number: 16, AutoMerged: true}))
	p.Client = interceptor.NewClient(c.(client.WithWatch), interceptor.Funcs{
		Patch: func(ctx context.Context, cl client.WithWatch, obj client.Object, patch client.Patch, o ...client.PatchOption) error {
			if obj.GetName() == "5buqdlot" {
				return errors.New("apiserver unavailable")
			}
			return cl.Patch(ctx, obj, patch, o...)
		}})
	err := p.Poll(t.Context())
	if err == nil || !strings.Contains(err.Error(), "locked") || !strings.Contains(err.Error(), "5buqdlot") {
		t.Fatalf("both failures are reported: %v", err)
	}
	annotation := func(name, key string) string {
		var got v1alpha1.Task
		_ = c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: name}, &got)
		return got.Annotations[key]
	}
	if annotation("3buqdlot", v1alpha1.AnnotationStop) != "label" || len(f.Removed(12)) != 1 {
		t.Errorf("the stop after the locked issue's: %q removed %v", annotation("3buqdlot", v1alpha1.AnnotationStop), f.Removed(12))
	}
	if len(f.Removed(9)) != 0 {
		t.Errorf("an unanswered stop label stays for the next poll: %v", f.Removed(9))
	}
	if annotation("4buqdlot", v1alpha1.AnnotationRevert) != "label" || len(f.Removed(14)) != 1 || len(f.Removed(16)) != 0 {
		t.Errorf("the revert after the failed one: %q removed #14 %v #16 %v", annotation("4buqdlot", v1alpha1.AnnotationRevert),
			f.Removed(14), f.Removed(16))
	}
	if n := len(tasks(t, c)); n != 4 || !slices.Equal(f.Removed(13), []string{"factory/ready"}) {
		t.Errorf("the ready label: %d tasks, removed %v", n, f.Removed(13))
	}
	if !slices.Equal(f.Added(22), []string{LabelOrphaned}) {
		t.Errorf("the orphan scan: %v", f.Added(22))
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test -race -count=1 ./internal/factory/intake/ -run TestOneFailingItemDoesNotStallThePoll`

Expected: FAIL. `stops` returns on #9 and `Poll` returns with it, before #12, the reverts, #13 and
the orphan scan:

```
--- FAIL: TestOneFailingItemDoesNotStallThePoll
    both failures are reported: narrate: forge: comment: 403 Unable to create comment because issue is locked
```

- [ ] **Step 3: Run every item and every pass, then join**

In `internal/factory/intake/issues.go`, replace `Poll`'s head, from its doc comment through the line
`var errs []error` and the `publishes` call that follows it:

```go
// Poll is one pass: the control issue's label first (§6.1), then stop labels, honoured even with
// intake paused, then reverts (§6.4's rollback path is a maintainer's, not new work), then each
// issue carrying the trigger label, then the orphan scan (R51), which also runs on leader start —
// this poll is the first thing Start runs. One issue's failure does not keep the others waiting.
func (p *IssuePoller) Poll(ctx context.Context) error {
	var publishErr error
	engaged := false // reflects the stop label this pass saw or set, so the reader's cache may not hold it yet; it errs safe if the publish fails
	if n := p.controlIssue(); n > 0 {
		iss, err := p.Forge.Issue(ctx, n)
		if err != nil {
			return err
		}
		on := slices.Contains(iss.Labels, LabelStop)
		engaged = on
		// A failed publish must not hold back the stop labels and reverts of this pass.
		publishErr = killswitch.PublishIssue(ctx, p.Client, p.Namespace, on)
	}
	if err := p.stops(ctx); err != nil {
		return errors.Join(publishErr, err)
	}
	if p.Owned == nil || !p.Owned(p.repo().Name) {
		return publishErr // another factory's, or nobody's: its labels wait for the owner (v2 D7)
	}
	if err := p.reverts(ctx); err != nil {
		return errors.Join(publishErr, err)
	}
	if engaged || p.Stopped(ctx) {
		return publishErr
	}
	items, err := p.Forge.Labeled(ctx, p.repo().TriggerLabel)
	if err != nil {
		return fmt.Errorf("intake: %w", err)
	}
	var errs []error
	if err := p.publishes(ctx); err != nil { // before factory/ready: the maintainer reads the filed issue, then starts it
```

with:

```go
// Poll is one pass: the control issue's label first (§6.1), then stop labels, honoured even with
// intake paused, then reverts (§6.4's rollback path is a maintainer's, not new work), then each
// issue carrying the trigger label, then the orphan scan (R51), which also runs on leader start —
// this poll is the first thing Start runs. One item's failure, in any pass, does not keep the
// others or the later passes waiting (F-6): the poll reports every failure once all have run. An
// unread control issue alone stops the pass, since whether intake may start work is then unknown.
func (p *IssuePoller) Poll(ctx context.Context) error {
	var errs []error
	engaged := false // reflects the stop label this pass saw or set, so the reader's cache may not hold it yet; it errs safe if the publish fails
	if n := p.controlIssue(); n > 0 {
		iss, err := p.Forge.Issue(ctx, n)
		if err != nil {
			return err
		}
		on := slices.Contains(iss.Labels, LabelStop)
		engaged = on
		// A failed publish must not hold back the stop labels and reverts of this pass.
		if err := killswitch.PublishIssue(ctx, p.Client, p.Namespace, on); err != nil {
			errs = append(errs, err)
		}
	}
	if err := p.stops(ctx); err != nil {
		errs = append(errs, err)
	}
	if p.Owned == nil || !p.Owned(p.repo().Name) {
		return errors.Join(errs...) // another factory's, or nobody's: its labels wait for the owner (v2 D7)
	}
	if err := p.reverts(ctx); err != nil {
		errs = append(errs, err)
	}
	if engaged || p.Stopped(ctx) {
		return errors.Join(errs...)
	}
	items, err := p.Forge.Labeled(ctx, p.repo().TriggerLabel)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("intake: %w", err))...)
	}
	if err := p.publishes(ctx); err != nil { // before factory/ready: the maintainer reads the filed issue, then starts it
```

and `Poll`'s last line, `return errors.Join(append(errs, publishErr)...)`, with:

```go
	return errors.Join(errs...)
```

In `stops`, replace the loop and the final `return nil`, from `for _, it := range items {` to the
end of the function:

```go
	for _, it := range items {
		if it.Number == p.controlIssue() {
			continue // the control issue keeps its factory/stop: removing it is how a human resumes (§6.1)
		}
		evs, ok, err := p.labelEvents(ctx, it.Number, LabelStop)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if _, last, byMaintainer := Generation(evs, p.repo().IsMaintainer); !byMaintainer {
			if err := p.narrator().PostOnce(ctx, it.Number, fmt.Sprintf("issue-%d", it.Number),
				narrate.StopIgnored(it.Number, "unauthorised_stopper", last.At)); err != nil {
				return err
			}
			if err := p.Forge.RemoveLabel(ctx, it.Number, LabelStop); err != nil {
				return err
			}
			continue
		}
		hit := false
		for i := range l.Items {
			t := &l.Items[i]
			if v1alpha1.TerminalPhase(t.Status.Phase) || !p.owns(t) {
				continue
			}
			// SL13: a stop on a parent's issue stops its parts too, found by spec.parent, never by name.
			if t.Spec.Issue == it.Number || (t.Status.PullRequest != nil && t.Status.PullRequest.Number == it.Number) ||
				(t.Spec.Parent != nil && t.Spec.Parent.Issue == it.Number) {
				if err := p.annotateStop(ctx, t, "label"); err != nil {
					return err
				}
				hit = true
			}
		}
		if hit {
			if err := p.Forge.RemoveLabel(ctx, it.Number, LabelStop); err != nil {
				return err
			}
		}
	}
	return nil
}
```

with:

```go
	var errs []error
	for _, it := range items {
		if it.Number == p.controlIssue() {
			continue // the control issue keeps its factory/stop: removing it is how a human resumes (§6.1)
		}
		if err := p.stop(ctx, it.Number, l.Items); err != nil {
			errs = append(errs, fmt.Errorf("stop #%d: %w", it.Number, err)) // its label stays: the next poll retries it
		}
	}
	return errors.Join(errs...)
}

// stop serves the factory/stop on issue or pull request n, given every task of the namespace.
func (p *IssuePoller) stop(ctx context.Context, n int, all []v1alpha1.Task) error {
	evs, ok, err := p.labelEvents(ctx, n, LabelStop)
	if err != nil || !ok {
		return err
	}
	if _, last, byMaintainer := Generation(evs, p.repo().IsMaintainer); !byMaintainer {
		if err := p.narrator().PostOnce(ctx, n, fmt.Sprintf("issue-%d", n),
			narrate.StopIgnored(n, "unauthorised_stopper", last.At)); err != nil {
			return err
		}
		return p.Forge.RemoveLabel(ctx, n, LabelStop)
	}
	hit := false
	for i := range all {
		t := &all[i]
		if v1alpha1.TerminalPhase(t.Status.Phase) || !p.owns(t) {
			continue
		}
		// SL13: a stop on a parent's issue stops its parts too, found by spec.parent, never by name.
		if t.Spec.Issue == n || (t.Status.PullRequest != nil && t.Status.PullRequest.Number == n) ||
			(t.Spec.Parent != nil && t.Spec.Parent.Issue == n) {
			if err := p.annotateStop(ctx, t, "label"); err != nil {
				return err
			}
			hit = true
		}
	}
	if !hit {
		return nil
	}
	return p.Forge.RemoveLabel(ctx, n, LabelStop)
}
```

In `reverts`, replace the loop and the final `return nil`, from `for _, it := range items {` to the
end of the function:

```go
	for _, it := range items {
		evs, ok, err := p.labelEvents(ctx, it.Number, LabelRevert)
		if err != nil || !ok {
			continue // GitHub has not listed it yet, or its newest events are unread: it waits
		}
		actor := evs[len(evs)-1].Actor
		if !p.repo().IsMaintainer(actor) {
			continue // labels are intent, and only a maintainer's counts
		}
		if w, err := p.writer(ctx, actor); err != nil || !w {
			continue // a revert opens a pull request: a write, so D5's check holds it too
		}
		ann := map[string]string{v1alpha1.AnnotationRevert: "label"}
		drill, err := p.drill(ctx, it.Number)
		if err != nil {
			continue // unread, the drill is unknown: the revert waits rather than count as a revert
		}
		if drill {
			ann[v1alpha1.AnnotationDrill] = "true"
		}
		for i := range l.Items {
			t := &l.Items[i]
			if ref := t.Status.PullRequest; ref != nil && ref.Number == it.Number && (ref.AutoMerged || ref.MergedHow == "approved") && p.owns(t) {
				body, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": ann}})
				obj := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: t.Name, Namespace: t.Namespace}}
				if err := p.Client.Patch(ctx, obj, client.RawPatch(types.MergePatchType, body)); err != nil {
					return fmt.Errorf("revert task %s: %w", t.Name, err)
				}
			}
		}
		if err := p.Forge.RemoveLabel(ctx, it.Number, LabelRevert); err != nil {
			return err
		}
	}
	return nil
}
```

with:

```go
	var errs []error
	for _, it := range items {
		if err := p.revert(ctx, it.Number, l.Items); err != nil {
			errs = append(errs, fmt.Errorf("revert #%d: %w", it.Number, err)) // its label stays: the next poll retries it
		}
	}
	return errors.Join(errs...)
}

// revert serves the factory/revert on pull request n, given every task of the namespace.
func (p *IssuePoller) revert(ctx context.Context, n int, all []v1alpha1.Task) error {
	evs, ok, err := p.labelEvents(ctx, n, LabelRevert)
	if err != nil || !ok {
		return nil // GitHub has not listed it yet, or its newest events are unread: it waits
	}
	actor := evs[len(evs)-1].Actor
	if !p.repo().IsMaintainer(actor) {
		return nil // labels are intent, and only a maintainer's counts
	}
	if w, err := p.writer(ctx, actor); err != nil || !w {
		return nil // a revert opens a pull request: a write, so D5's check holds it too
	}
	ann := map[string]string{v1alpha1.AnnotationRevert: "label"}
	drill, err := p.drill(ctx, n)
	if err != nil {
		return nil // unread, the drill is unknown: the revert waits rather than count as a revert
	}
	if drill {
		ann[v1alpha1.AnnotationDrill] = "true"
	}
	for i := range all {
		t := &all[i]
		if ref := t.Status.PullRequest; ref != nil && ref.Number == n && (ref.AutoMerged || ref.MergedHow == "approved") && p.owns(t) {
			body, _ := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": ann}})
			obj := &v1alpha1.Task{ObjectMeta: metav1.ObjectMeta{Name: t.Name, Namespace: t.Namespace}}
			if err := p.Client.Patch(ctx, obj, client.RawPatch(types.MergePatchType, body)); err != nil {
				return fmt.Errorf("revert task %s: %w", t.Name, err)
			}
		}
	}
	return p.Forge.RemoveLabel(ctx, n, LabelRevert)
}
```

`revert` keeps the swallowed waits exactly as they were (the audit's Low "`issues.go:562` swallows
revert errors" stays parked): only what used to abort the pass is now collected.

- [ ] **Step 4: Run the tests to make sure they pass**

Run: `go test -race -count=1 ./internal/factory/intake/ -run 'TestOneFailingItemDoesNotStallThePoll|TestStopLabelReachesTheTask|TestRevertLabel|TestDrillLabelMarksTheRevertADrill|TestAFailedPublishDoesNotSkipReverts|TestControlIssue'`
Expected: PASS.

Then: `go build ./... && go vet ./... && go test -race -count=1 ./internal/factory/intake/ && task lint`
Expected: PASS, 0 lint issues.

- [ ] **Step 5: Commit**

```bash
git add internal/factory/intake/issues.go internal/factory/intake/passes_test.go
git commit -m "fix(intake): one failing stop or revert no longer stalls the poll (F-6)"
```

---

### Task 6: the gate and the pull request

**Files:** none changed.

- [ ] **Step 1: Run the whole gate**

Run: `task check`
Expected: exit 0, Docker running for `internal/store`'s testcontainers. Note the exit code for the PR.

- [ ] **Step 2: One commit per finding**

Run: `git log --oneline origin/main..HEAD`
Expected: five commits, in order, ending `(H-3)`, `(S-6)`, `(S-7)`, `(F-5)`, `(F-6)`, none with a trailer
(`git log origin/main..HEAD --format=%B | grep -ci 'co-authored-by\|generated with'` prints `0`).

- [ ] **Step 3: Push and open the pull request**

```bash
git fetch origin && git rebase origin/main
git push -u origin fix/audit-t3-gate-intake
gh pr create --base main --title "fix(factory): the gate and intake audit fixes (H-3, S-6, S-7, F-5, F-6)" --body-file - <<'EOF'
## What

Theme T3 of the 2026-11-01 audit fixes (docs/superpowers/specs/2026-11-01-audit-fixes-design.md), one commit per finding:

- **H-3** `forge.Diff` refuses a compare at GitHub's 300-file cap (`>= maxChangedFiles`), as `ChangedFiles` does. It refused only exactly 100, so a 300-file listing was judged whole.
- **S-6** `sanitize.Public` adds a backslash to an odd run before each code span it opens: `\@smana`, `\#12`, `\https://…` no longer come out live.
- **S-7** the config requires `solo`, `pair` and `trio`, and `investigate: {roles: [triager]}` once `runlore` or `api.internalPrincipals` is set.
- **F-5** a label's generation counts every label event, so a maintainer removed from the config no longer makes a fresh label collide with an earlier Task and vanish.
- **F-6** the stop and revert passes collect per-item errors and continue; the poll reports them after the ready label and the orphan scan ran.

## Deviations from the spec

- F-5 drops the "retry with generation + 1 on AlreadyExists for a terminal Task": once every label counts, a label's generation never changes and no earlier Task can share it; the retry would only start a second Task for one label whose first ended within a poll. The plan's V1 has the evidence.

## Behaviour change

- A config without `solo`, `pair` or `trio`, or with an `investigate` other than `[triager]` beside `runlore` or `api.internalPrincipals`, no longer loads. The deployed config declares all four as required.
- A compare of exactly 100 files is now read (GitHub lists 300).

## Verification

`task check`: exit 0. Each finding's test failed before its fix (the plan quotes each failure).
EOF
```

Merge with a rebase, not a squash, so each finding keeps its commit (D0).

## Self-review

- **Spec coverage:** H-3 Task 1 (300 refused, 299 read); S-6 Task 2 (`\@x`, `\#12`, `\\@x` through
  goldmark, plus `\\\#12`, a reference and a URL); S-7 Task 3 (each refused shape names
  `templates.<name>`); F-5 Task 4 (V1, V2); F-6 Task 5 (a permanently failing comment, a failing
  patch, the other items, ready intake and the orphan scan). D0: one commit each, Task 6 checks.
- **Placeholders:** none; every code step is the prototyped code.
- **Names:** `ErrDiffTooLarge`, `maxChangedFiles`, `codeSpanLive`, `live`, `links`,
  `validateTriageTemplates`, `Generation`, `stop`, `revert`, `lockedIssue` are used as defined.
