// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Smana/maku/internal/intake"
)

// planFixture decodes this package's own plan/v1 fixture: the one plan the proposal tests read. It
// describes the same plan as the intake plan's testdata/plan/slack-one-issue.json, and carries no
// hash, so every hash in these tests is the one intake.Hash computes.
func planFixture(t *testing.T, name string) intake.Plan {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "plan", name+".json"))
	if err != nil {
		t.Fatalf("read the plan fixture: %v", err)
	}
	var p intake.Plan
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("decode the plan fixture: %v", err)
	}
	return p
}

// proposalFixture is that plan as the proposal at revision 1, carrying the hash of what it says.
func proposalFixture(t *testing.T) ProposalPayload {
	t.Helper()
	p := planFixture(t, "slack-one-issue")
	prop, err := newProposal(p, intake.Hash(p), "U07JY4EQNDN", 1)
	if err != nil {
		t.Fatalf("newProposal: %v", err)
	}
	return prop
}

// clonePlan is p as a value nothing shares: a mutation of the copy cannot reach the original's
// slices.
func clonePlan(t *testing.T, p intake.Plan) intake.Plan {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	var out intake.Plan
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal plan: %v", err)
	}
	return out
}

// testBlock is a block as Slack reads it: its own text, and its elements left raw because a block's
// elements are not one shape — a button's text is a text object, a context element's is a string.
type testBlock struct {
	Type     string          `json:"type"`
	Text     *slackBlockText `json:"text"`
	Elements json.RawMessage `json:"elements"`
}

// buttons is an actions block's elements, decoded as the buttons Slack reads.
func (b testBlock) buttons(t *testing.T) []slackButton {
	t.Helper()
	var out []slackButton
	if err := json.Unmarshal(b.Elements, &out); err != nil {
		t.Fatalf("decode the buttons: %v", err)
	}
	return out
}

// context is a context block's elements, decoded as the text elements Slack reads.
func (b testBlock) context(t *testing.T) []slackContextText {
	t.Helper()
	var out []slackContextText
	if err := json.Unmarshal(b.Elements, &out); err != nil {
		t.Fatalf("decode the context elements: %v", err)
	}
	return out
}

// blocksOf decodes a message's blocks, in order. They are the array chat.postMessage takes as its
// `blocks` field: an object there is Slack's invalid_blocks_format, and no proposal is posted.
func blocksOf(t *testing.T, raw json.RawMessage) []testBlock {
	t.Helper()
	var blocks []testBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("decode the blocks as Slack's array: %v", err)
	}
	return blocks
}

// wireBlocks is each block as Slack's own JSON parser sees it: a map, so a test can assert the shape
// of a value rather than the Go type this package happens to declare for it.
func wireBlocks(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var blocks []map[string]any
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatalf("decode the blocks as Slack's JSON array: %v", err)
	}
	return blocks
}

// CD20: the metadata is the whole proposal. It round-trips unchanged, and a payload whose hash does
// not hold — because a byte of the plan moved — is not a proposal.
func TestProposalMetadataRoundTrip(t *testing.T) {
	plan := planFixture(t, "slack-one-issue")
	hash := intake.Hash(plan)
	p, err := newProposal(plan, hash, "U07JY4EQNDN", 1)
	if err != nil {
		t.Fatalf("newProposal: %v", err)
	}

	// The message's wire metadata: one tag and one payload, under Slack's own cap.
	meta := &Metadata{EventType: proposalEventType, EventPayload: mustJSON(t, p)}
	raw, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal the metadata: %v", err)
	}
	if len(raw) > maxMetadata {
		t.Fatalf("the metadata is %d bytes, over maxMetadata (%d)", len(raw), maxMetadata)
	}

	// An App that remembers nothing but the message reads the same proposal back: nothing about a
	// proposal is stored anywhere else (CD20).
	var wire Metadata
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("unmarshal the metadata: %v", err)
	}
	got, ok := wire.proposalPayload()
	if !ok {
		t.Fatal("a proposal's own metadata did not decode as a proposal")
	}
	if !reflect.DeepEqual(got.Plan, plan) {
		t.Errorf("the plan came back changed:\n got %+v\nwant %+v", got.Plan, plan)
	}
	if got.Hash != hash || got.Author != "U07JY4EQNDN" || got.Rev != 1 {
		t.Errorf("hash %q, author %q, rev %d", got.Hash, got.Author, got.Rev)
	}
	if !proposalHashHolds(got) {
		t.Error("a proposal whose plan and hash agree did not hold")
	}

	// One byte of the plan, and the hash no longer holds: it is not a proposal, it is a message that
	// happens to be tagged.
	moved := got
	moved.Plan = clonePlan(t, got.Plan)
	moved.Plan.Issues[0].Body += "\n"
	if proposalHashHolds(moved) {
		t.Error("a plan that moved by one byte still held its hash")
	}

	// The tag is what makes it a proposal, and the payload has to be the shape the tag claims.
	if _, ok := metaOf(t, draftEventType, p).proposalPayload(); ok {
		t.Error("a draft's tag decoded as a proposal")
	}
	if _, ok := (&Metadata{EventType: proposalEventType, EventPayload: json.RawMessage(`"nope"`)}).proposalPayload(); ok {
		t.Error("a payload that is not an object decoded as a proposal")
	}
	var none *Metadata
	if _, ok := none.proposalPayload(); ok {
		t.Error("no metadata decoded as a proposal")
	}

	// A hash of the wrong shape binds nothing, whatever the plan hashes to.
	for _, bad := range []string{
		"",
		"sha256:" + strings.Repeat("A", 64), // uppercase
		"sha256:" + strings.Repeat("a", 63), // short
		hash + "0",                          // long
		"sha512:" + strings.Repeat("a", 64), // another digest
		"sha256:" + strings.Repeat("a", 64) + "\n", // a trailing byte
	} {
		if proposalHashHolds(ProposalPayload{Plan: plan, Hash: bad}) {
			t.Errorf("%q held as a hash", bad)
		}
	}
}

// Today's Filed lines carry the repository alone, which is why the marker's room is optional: a
// decoder that insisted on a room would read every Filed line in every deployed thread as foreign.
func TestMarkerPayloadDecodesFiledLines(t *testing.T) {
	today := metaOf(t, filedEventType, DraftPayload{Repo: "Smana/ai-native-ref"})
	m, ok := today.markerPayload()
	if !ok {
		t.Fatal("a Filed line's tag did not decode as a marker")
	}
	if m.Repo != "Smana/ai-native-ref" || m.Room != "" {
		t.Errorf("repo %q, room %q", m.Repo, m.Room)
	}

	tagged := metaOf(t, filedEventType, MarkerPayload{Repo: "Smana/ai-native-ref", Room: "26zfnuxm"})
	if m, ok := tagged.markerPayload(); !ok || m.Room != "26zfnuxm" {
		t.Errorf("a marker with a room decoded to %+v (%v)", m, ok)
	}

	if _, ok := metaOf(t, draftEventType, MarkerPayload{Repo: "Smana/ai-native-ref"}).markerPayload(); ok {
		t.Error("a draft's tag decoded as a marker")
	}
	var none *Metadata
	if _, ok := none.markerPayload(); ok {
		t.Error("no metadata decoded as a marker")
	}
}

// §plan's Slack bound: a plan whose canonical JSON exceeds 7 KiB is refused, never truncated, so
// the metadata stays under maxMetadata.
func TestProposalTooLargeIsRefused(t *testing.T) {
	plan := planFixture(t, "slack-one-issue")
	big := clonePlan(t, plan)
	big.Issues[0].Body += strings.Repeat("x", maxProposalPlanBytes)

	_, err := newProposal(big, intake.Hash(big), "U07JY4EQNDN", 1)
	if !errors.Is(err, errProposalTooLarge) {
		t.Fatalf("an oversized plan was not refused: %v", err)
	}
	if want := "the plan is too long for Slack; use your coding agent or the web UI"; err.Error() != want {
		t.Errorf("the refusal reads %q, want %q", err, want)
	}

	// The fixture is inside the bound, and its message's metadata is inside Slack's.
	p, err := newProposal(plan, intake.Hash(plan), "U07JY4EQNDN", 1)
	if err != nil {
		t.Fatalf("a plan inside the bound was refused: %v", err)
	}
	raw, err := json.Marshal(&Metadata{EventType: proposalEventType, EventPayload: mustJSON(t, p)})
	if err != nil {
		t.Fatalf("marshal the metadata: %v", err)
	}
	if len(raw) > maxMetadata {
		t.Errorf("the metadata is %d bytes, over maxMetadata (%d)", len(raw), maxMetadata)
	}
	// The bound is the plan's canonical JSON, so a plan measured at it is refused by nothing else.
	if j, err := plan.JSON(); err != nil || len(j) > maxProposalPlanBytes {
		t.Fatalf("the fixture is %d bytes of canonical JSON (%v)", len(j), err)
	}
}

// CD7: a confirmation binds the version it showed. The button's value carries the hash and the
// revision; a value with anything else, or a hash of the wrong shape, binds nothing.
func TestProposalValueBindsTheHash(t *testing.T) {
	hash := intake.Hash(planFixture(t, "slack-one-issue"))
	for rev := 1; rev <= maxProposalRevisions; rev++ {
		v := proposalValue(hash, rev)
		if want := "v1|proposal|" + hash + "|" + strconv.Itoa(rev); v != want {
			t.Fatalf("revision %d's value is %q, want %q", rev, v, want)
		}
		gotHash, gotRev, ok := parseProposalValue(v)
		if !ok || gotHash != hash || gotRev != rev {
			t.Errorf("%q parsed as (%q, %d, %v)", v, gotHash, gotRev, ok)
		}
	}

	for _, bad := range []string{
		"",                             // nothing
		"v1|proposal|" + hash,          // three parts
		"v1|proposal|" + hash + "|1|2", // five parts
		"v2|proposal|" + hash + "|1",   // another version's value
		"v1|approval|forge.push|1",     // another family's value
		"|proposal|" + hash + "|1",     // no version
		"v1||" + hash + "|1",           // no family
		"v1|proposal|" + strings.TrimPrefix(hash, "sha256:") + "|1",        // the digest alone
		"v1|proposal|sha256:" + strings.Repeat("A", 64) + "|1",             // uppercase
		"v1|proposal|" + hash + "|0",                                       // below the first revision
		"v1|proposal|" + hash + "|" + strconv.Itoa(maxProposalRevisions+1), // past the cap
		"v1|proposal|" + hash + "|-1",                                      // not a revision
		"v1|proposal|" + hash + "| 1",                                      // nor is a padded one
		"v1|proposal|" + hash + "|1\n",                                     // nor one with a trailing byte
		"v1|proposal|" + hash + "|9999999999999999999999",                  // nor one that overflows
	} {
		if h, rev, ok := parseProposalValue(bad); ok {
			t.Errorf("%q parsed as (%q, %d)", bad, h, rev)
		}
	}
}

// CD14/CD27: materiality is exactly the hashed set. Each single hashed field moves its own section
// and no other; evidence, an alternative, the question's wording and the revision move none.
func TestProposalChangedFollowsTheMaterialTable(t *testing.T) {
	prev := proposalFixture(t)

	for _, tc := range []struct {
		name   string
		mutate func(*intake.Plan)
		want   []string
	}{
		{"repository", func(p *intake.Plan) { p.Repository = "Smana/cloud-native-ref" }, []string{"repository"}},
		{"ceiling", func(p *intake.Plan) { p.Budget.CeilingUSD = 2 }, []string{"budget"}},
		{"ceiling source", func(p *intake.Plan) { p.Budget.Source = "class:docs-links.taskUSD" }, []string{"budget"}},
		// The display rounds to cents, so $1.5049 and $1.50 both read "$1.50": the mark follows the
		// material bytes, and those differ, so the section is marked even though the arrow did not move.
		{"an amount the display rounds away", func(p *intake.Plan) { p.Budget.CeilingUSD = 1.5049 }, []string{"budget"}},
		// A difference the hash itself cannot see is not a change: the ceiling is hashed in
		// micro-dollars, and 1.5000001 rounds to the same millionth.
		{"an amount the hash does not see", func(p *intake.Plan) { p.Budget.CeilingUSD = 1.5000001 }, nil},
		{"title", func(p *intake.Plan) { p.Issues[0].Title += "!" }, []string{"issue"}},
		{"body", func(p *intake.Plan) { p.Issues[0].Body += "\n" }, []string{"issue"}},
		{"class", func(p *intake.Plan) { p.Issues[0].Class = "forge.push" }, []string{"issue"}},
		{"roles", func(p *intake.Plan) { p.Issues[0].Roles = []string{"implementer"} }, []string{"roles"}},
		{"dependsOn", func(p *intake.Plan) { p.Issues[0].DependsOn = []int{1} }, []string{"roles"}},
		{"a second issue", func(p *intake.Plan) {
			p.Issues = append(p.Issues, intake.Issue{Index: 2, Title: "docs: another link", Body: p.Issues[0].Body, Class: "docs-links", Roles: []string{"implementer"}})
		}, []string{"issue", "roles"}},
		// Everything below is display: the person reads it, and a Confirm does not bind it.
		{"the revision", func(p *intake.Plan) { p.Rev = 7 }, nil},
		{"the evidence", func(p *intake.Plan) {
			p.RepositoryEvidence = append(p.RepositoryEvidence, intake.Evidence{Kind: "path", Text: "the README"})
		}, nil},
		{"an alternative", func(p *intake.Plan) {
			p.Alternatives = []intake.Alternative{{Repository: "Smana/cloud-native-ref", Why: "it also fits"}}
		}, nil},
		{"the question", func(p *intake.Plan) {
			p.Question = &intake.Question{Text: "which repository?", Recommended: intake.Answer{Answer: "this one"}}
		}, nil},
	} {
		plan := clonePlan(t, prev.Plan)
		tc.mutate(&plan)
		next := prev
		next.Plan = plan
		got := proposalChanged(prev, next)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: marked %v, want %v", tc.name, got, tc.want)
		}
		// Confirm is exactly "something a Confirm would file moved".
		if confirm := len(got) > 0; confirm != (len(tc.want) > 0) {
			t.Errorf("%s: Confirm is %v", tc.name, confirm)
		}
	}

	// Nothing moved: nothing is marked.
	if got := proposalChanged(prev, prev); len(got) != 0 {
		t.Errorf("an unchanged plan marked %v", got)
	}
}

// One message, no parse of rendered text: the sections and their blocks come from the payload, and
// the text is the same sections joined, which is what Slack shows in a notification.
func TestProposalSectionsAndBlocks(t *testing.T) {
	p := proposalFixture(t)
	text, raw, err := proposalSections(p, p.Plan.Question, nil, "C03")
	if err != nil {
		t.Fatalf("proposalSections: %v", err)
	}
	if len(raw) > maxSlackReply {
		t.Errorf("the blocks are %d bytes, over maxSlackReply (%d)", len(raw), maxSlackReply)
	}
	blocks := blocksOf(t, raw)
	if len(blocks) < 3 {
		t.Fatalf("%d blocks: %+v", len(blocks), blocks)
	}

	// The header block is Slack's header type: plain text, the revision alone. What it cannot carry
	// follows in the section right under it.
	if b := blocks[0]; b.Type != "header" || b.Text == nil || b.Text.Type != "plain_text" || b.Text.Text != "Proposal v1" {
		t.Errorf("the header block is %+v", blocks[0])
	}
	if b := blocks[1]; b.Type != "section" || b.Text == nil || b.Text.Type != "mrkdwn" ||
		!strings.Contains(b.Text.Text, p.Hash) || !strings.Contains(b.Text.Text, "<@U07JY4EQNDN>") {
		t.Errorf("the block under the header is %+v", blocks[1])
	}

	// One section block per remaining section: its heading in bold, its body under it, and every
	// body also in the fallback text.
	headings := []string{"Repository", "Issue", "Roles", "Ceiling", "Editing"}
	sections := blocks[2 : len(blocks)-2]
	if len(sections) != len(headings) {
		t.Fatalf("%d section blocks, want %d: %+v", len(sections), len(headings), sections)
	}
	for i, b := range sections {
		if b.Type != "section" || b.Text == nil || b.Text.Type != "mrkdwn" {
			t.Fatalf("section %d is %+v", i, b)
		}
		head := "*" + headings[i] + "*\n"
		if !strings.HasPrefix(b.Text.Text, head) {
			t.Errorf("section %d starts %q, want %q", i, b.Text.Text, head)
		}
		if body := strings.TrimPrefix(b.Text.Text, head); body == "" || !strings.Contains(text, body) {
			t.Errorf("section %d's body is not in the fallback text: %q", i, body)
		}
	}

	// Two buttons, one value: the hash and the revision the message showed.
	actions := blocks[len(blocks)-2]
	if actions.Type != "actions" || len(actions.buttons(t)) != 2 {
		t.Fatalf("the actions block is %+v", actions)
	}
	value := proposalValue(p.Hash, p.Rev)
	wantStyle := map[string]string{actionProposalConfirm: "primary", actionProposalCancel: "danger"}
	seen := map[string]bool{}
	for _, e := range actions.buttons(t) {
		style, ok := wantStyle[e.ActionID]
		if !ok {
			t.Errorf("an unexpected button %q", e.ActionID)
			continue
		}
		seen[e.ActionID] = true
		if e.Type != "button" || e.Style != style || e.Value != value {
			t.Errorf("button %q is %+v", e.ActionID, e)
		}
		if e.Text == nil || e.Text.Type != "plain_text" || e.Text.Text == "" {
			t.Errorf("button %q has no plain text: %+v", e.ActionID, e.Text)
		}
	}
	if !seen[actionProposalConfirm] || !seen[actionProposalCancel] {
		t.Errorf("the buttons are %v", seen)
	}
	// A button's text is a text object — the opposite of the context element's string — so the two
	// shapes are asserted where Slack reads them.
	actionElements, ok := wireBlocks(t, raw)[len(blocks)-2]["elements"].([]any)
	if !ok || len(actionElements) != 2 {
		t.Fatalf("the actions block's elements are %+v", actionElements)
	}
	if button, ok := actionElements[0].(map[string]any); !ok {
		t.Fatalf("the first button is %T", actionElements[0])
	} else if text, ok := button["text"].(map[string]any); !ok || text["type"] != "plain_text" {
		t.Errorf("the first button's text is %T (%v), want a plain_text text object", button["text"], button["text"])
	}

	// The context block names the channel: a proposal has no room yet, so it links none. Its element
	// is Slack's shape — `{"type":"mrkdwn","text":"<#C03>"}`, the text a string — and not a text
	// object nested inside it, which Slack refuses and renders as nothing.
	last := len(blocks) - 1
	if blocks[last].Type != "context" {
		t.Errorf("the last block is %+v", blocks[last])
	}
	if ctx := blocks[last].context(t); len(ctx) != 1 || ctx[0].Type != "mrkdwn" || ctx[0].Text != "<#C03>" {
		t.Errorf("the context elements are %+v", ctx)
	}
	// On the wire the text is a JSON string, not an object: Slack refuses the nested shape, and the
	// bug it caused was invisible to a typed decode that only read the fields it expected.
	wire := wireBlocks(t, raw)[last]
	elements, ok := wire["elements"].([]any)
	if !ok || len(elements) != 1 {
		t.Fatalf("the context block's elements are %+v", wire["elements"])
	}
	element, ok := elements[0].(map[string]any)
	if !ok {
		t.Fatalf("the context element is %T", elements[0])
	}
	if text, ok := element["text"].(string); !ok || text != "<#C03>" {
		t.Errorf("the context element's text is %T (%v), want the JSON string \"<#C03>\"", element["text"], element["text"])
	}
	if element["type"] != "mrkdwn" {
		t.Errorf("the context element's type is %v", element["type"])
	}

	// The fallback text is the sections joined: plain headings, no markup, never parsed back.
	if !strings.HasPrefix(text, "Proposal v1 · "+p.Hash+" · by <@U07JY4EQNDN>") {
		t.Errorf("the text starts %q", text)
	}
	for _, want := range []string{"\nRepository\n", "\nIssue\n", "\nRoles\n", "\nCeiling\n", "\nEditing\n", "$1.50 (channel:C1.maxCostUSD)"} {
		if !strings.Contains(text, want) {
			t.Errorf("the text is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "*") || strings.Contains(text, "action_id") {
		t.Errorf("the fallback text carries markup:\n%s", text)
	}

	// A material change marks its section and says a new confirmation is required; nothing else is
	// marked, and the header is never a part of the plan.
	_, changedRaw, err := proposalSections(p, nil, []string{"budget"}, "C03")
	if err != nil {
		t.Fatalf("proposalSections with a change: %v", err)
	}
	marks := 0
	for _, b := range blocksOf(t, changedRaw) {
		if b.Text == nil {
			continue
		}
		if strings.Contains(b.Text.Text, "‹changed›") {
			marks++
			if !strings.HasPrefix(b.Text.Text, "*Ceiling ‹changed›*") {
				t.Errorf("a marked block reads %q", b.Text.Text)
			}
		}
	}
	if marks != 1 {
		t.Errorf("%d sections are marked, want 1", marks)
	}
	if b := blocksOf(t, changedRaw)[1]; b.Text == nil || !strings.Contains(b.Text.Text, "requires a new confirmation") {
		t.Errorf("a marked message's header does not ask for a new confirmation: %+v", b)
	}
}

// A proposal whose question is set shows it; without one it shows none, and the payload's own plan
// is what decides when the caller names no question.
func TestProposalSectionsCarryTheQuestion(t *testing.T) {
	p := proposalFixture(t)
	q := &intake.Question{Text: "which repository?", Recommended: intake.Answer{Answer: "Smana/ai-native-ref", Why: "it owns the page"}}

	blocks := blocksOf(t, mustBlocks(t, p, q))
	found := false
	for _, b := range blocks {
		if b.Text != nil && strings.HasPrefix(b.Text.Text, "*One question*") {
			found = true
			if !strings.Contains(b.Text.Text, "which repository?") || !strings.Contains(b.Text.Text, "Recommended: Smana/ai-native-ref") {
				t.Errorf("the question block reads %q", b.Text.Text)
			}
		}
	}
	if !found {
		t.Errorf("no question block in %d blocks", len(blocks))
	}

	// The fixture carries no question: the message shows none, and adds no empty block for one.
	for _, b := range blocksOf(t, mustBlocks(t, p, nil)) {
		if b.Text != nil && strings.Contains(b.Text.Text, "One question") {
			t.Errorf("a plan with no question shows one: %q", b.Text.Text)
		}
	}

	// A plan that carries the question is shown it even when the caller passes none.
	carried := proposalFixture(t)
	carried.Plan.Question = q
	shown := false
	for _, b := range blocksOf(t, mustBlocks(t, carried, nil)) {
		if b.Text != nil && strings.HasPrefix(b.Text.Text, "*One question*") {
			shown = true
		}
	}
	if !shown {
		t.Error("the plan's own question was not shown")
	}
}

// A proposal the App could not read back is never built: the hash is the plan's own and the revision
// is inside the cap, or newProposal refuses. A message tagged with a hash that does not bind its plan
// would be posted and then read as a foreign message by the very App that posted it.
func TestNewProposalRefusesWhatItCannotReadBack(t *testing.T) {
	plan := planFixture(t, "slack-one-issue")
	hash := intake.Hash(plan)
	moved := clonePlan(t, plan)
	moved.Issues[0].Title += "!"

	for _, tc := range []struct {
		name string
		hash string
		rev  int
		want error
	}{
		{"another plan's hash", intake.Hash(moved), 1, errProposalHash},
		{"no hash", "", 1, errProposalHash},
		{"an off-shape hash", "sha256:" + strings.Repeat("A", 64), 1, errProposalHash},
		{"revision 0", hash, 0, errProposalRev},
		{"a revision past the cap", hash, maxProposalRevisions + 1, errProposalRev},
		{"a negative revision", hash, -1, errProposalRev},
	} {
		p, err := newProposal(plan, tc.hash, "U07JY4EQNDN", tc.rev)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(p, ProposalPayload{}) {
			t.Errorf("%s: a refused proposal was still built: %+v", tc.name, p)
		}
	}

	// The cap's own edges are proposals: the first revision and the last.
	for _, rev := range []int{1, maxProposalRevisions} {
		if _, err := newProposal(plan, hash, "U07JY4EQNDN", rev); err != nil {
			t.Errorf("revision %d was refused: %v", rev, err)
		}
	}
}

// A chunk of nothing but whitespace is no block: Slack refuses a section whose text is empty, and a
// blank run inside a long section is not a section of its own.
func TestSplitSlackTextDropsWhitespaceOnlyChunks(t *testing.T) {
	blank := strings.Repeat("\n", 2*slackSectionLimit)
	chunks := splitSlackText(blank, slackSectionLimit)
	if len(chunks) != 0 {
		t.Errorf("blank text produced %d chunks: %q", len(chunks), chunks)
	}
	for _, chunk := range splitSlackText("start"+strings.Repeat(" ", 2*slackSectionLimit)+"end", slackSectionLimit) {
		if strings.TrimSpace(chunk) == "" {
			t.Errorf("a chunk is blank: %q", chunk)
		}
		if n := utf8.RuneCountInString(chunk); n > slackSectionLimit {
			t.Errorf("a chunk is %d characters", n)
		}
	}

	// A heading with no body is still a section: the heading is text.
	if got := splitSlackText(slackSectionText("Roles", ""), slackSectionLimit); len(got) != 1 || !strings.HasPrefix(got[0], "*Roles*") {
		t.Errorf("a heading with no body produced %q", got)
	}
}

// The same at the message's level: a plan whose issue text is mostly whitespace yields no empty
// section block, and what the plan did say is still there.
func TestProposalSectionsEmitNoBlankBlock(t *testing.T) {
	plan := planFixture(t, "slack-one-issue")
	plan.Issues[0].Body = "## What is wrong\nfirst" + strings.Repeat(" ", 2*slackSectionLimit) + "last" +
		"\n\n## What it should be\nit resolves\n\n## Acceptance check\nit passes"
	p, err := newProposal(plan, intake.Hash(plan), "U07JY4EQNDN", 1)
	if err != nil {
		t.Fatalf("newProposal: %v", err)
	}

	blocks := blocksOf(t, mustBlocks(t, p, nil))
	var carried strings.Builder
	for i, b := range blocks {
		if b.Text == nil {
			continue
		}
		if strings.TrimSpace(b.Text.Text) == "" {
			t.Errorf("block %d (%s) is blank", i, b.Type)
		}
		if strings.Contains(b.Text.Text, "first") {
			carried.WriteString(b.Text.Text)
		}
	}
	if !strings.Contains(carried.String(), "last") {
		t.Error("the text after the blank run was dropped with it")
	}
}

// Slack's own bounds, refused rather than relied on: a section block's text is at most 3 000
// characters, so a long section is carried whole across several blocks and nothing a Confirm binds
// is cut. A plan at the 7 KiB bound is the largest message a proposal can be, and it fits.
func TestProposalSectionsStayInsideSlacksCaps(t *testing.T) {
	plan := planFixture(t, "slack-one-issue")
	// The template's body, padded to the bound: the issue section is far past 3 000 characters.
	base := clonePlan(t, plan)
	base.Issues[0].Body = proposalBody("")
	raw, err := base.JSON()
	if err != nil {
		t.Fatalf("marshal the base plan: %v", err)
	}
	pad := maxProposalPlanBytes - len(raw) - 1
	if pad <= 0 {
		t.Fatalf("the base plan is %d bytes: no room to pad to the bound", len(raw))
	}
	line := "a line that must survive the split\n"
	// A newline costs two bytes inside JSON, so the padding is trimmed until the plan fits the bound
	// rather than computed from the body's length.
	n := pad / (len(line) + 1)
	for {
		plan.Issues[0].Body = proposalBody(strings.Repeat(line, n))
		j, err := plan.JSON()
		if err != nil {
			t.Fatalf("marshal the padded plan: %v", err)
		}
		if len(j) <= maxProposalPlanBytes {
			break
		}
		if n == 0 {
			t.Fatalf("the padded plan is %d bytes at no padding", len(j))
		}
		n--
	}
	if len(plan.Issues[0].Body) <= slackSectionLimit {
		t.Fatalf("the body is %d bytes: not past the section limit", len(plan.Issues[0].Body))
	}

	p, err := newProposal(plan, intake.Hash(plan), "U07JY4EQNDN", 1)
	if err != nil {
		t.Fatalf("a plan at the bound was refused: %v", err)
	}
	if meta, err := json.Marshal(&Metadata{EventType: proposalEventType, EventPayload: mustJSON(t, p)}); err != nil || len(meta) > maxMetadata {
		t.Fatalf("a plan at the bound puts %d bytes of metadata (%v)", len(meta), err)
	}

	text, blocksRaw, err := proposalSections(p, nil, nil, "C03")
	if err != nil {
		t.Fatalf("proposalSections: %v", err)
	}
	if len(blocksRaw) > maxSlackReply {
		t.Errorf("the blocks are %d bytes, over maxSlackReply", len(blocksRaw))
	}
	if len(text) > maxSlackText {
		t.Errorf("the fallback text is %d bytes, over maxSlackText (%d)", len(text), maxSlackText)
	}
	blocks := blocksOf(t, blocksRaw)
	if len(blocks) > maxSlackBlocks {
		t.Errorf("%d blocks, over Slack's %d", len(blocks), maxSlackBlocks)
	}
	issue := 0
	inIssue := false
	var carried strings.Builder
	for _, b := range blocks {
		if b.Text == nil {
			inIssue = false
			continue
		}
		limit := slackSectionLimit
		if b.Type == "header" {
			limit = slackHeaderLimit
		}
		if n := utf8.RuneCountInString(b.Text.Text); n > limit {
			t.Errorf("a %s block is %d characters, over Slack's %d", b.Type, n, limit)
		}
		switch {
		case b.Type == "section" && strings.HasPrefix(b.Text.Text, "*Issue*"):
			inIssue = true
			issue++
			carried.WriteString(strings.TrimPrefix(b.Text.Text, "*Issue*\n"))
		case inIssue && b.Type == "section" && !strings.HasPrefix(b.Text.Text, "*"):
			issue++ // the same section carried on: no heading, so it reads as the continuation
			carried.WriteString(b.Text.Text)
		default:
			inIssue = false
		}
	}
	if issue < 2 {
		t.Errorf("the issue section was carried in %d blocks, want more than one", issue)
	}
	if !strings.Contains(carried.String(), "a line that must survive the split") {
		t.Error("the padded body is not in the blocks")
	}
	bodyLine := "a line that must survive the split"
	if want := strings.Count(plan.Issues[0].Body, bodyLine); strings.Count(carried.String(), bodyLine) != want {
		t.Errorf("%d of the body's %d lines survived the split", strings.Count(carried.String(), bodyLine), want)
	}
}

// A body with no line breaks is still carried whole: the block cut falls inside the line, by count
// of characters so that no rune is split in two, and the text is nothing but what the plan said.
func TestProposalSectionsSplitAnUnbrokenLine(t *testing.T) {
	plan := planFixture(t, "slack-one-issue")
	body := strings.Repeat("a", 4000) // no template headings: the renderer shows it whole
	plan.Issues[0].Body = body
	p, err := newProposal(plan, intake.Hash(plan), "U07JY4EQNDN", 1)
	if err != nil {
		t.Fatalf("newProposal: %v", err)
	}

	blocks := blocksOf(t, mustBlocks(t, p, nil))
	var carried strings.Builder
	for _, b := range blocks {
		if b.Text == nil {
			continue
		}
		if n := utf8.RuneCountInString(b.Text.Text); n > slackSectionLimit {
			t.Errorf("a %s block is %d characters, over Slack's %d", b.Type, n, slackSectionLimit)
		}
		if strings.Contains(b.Text.Text, "aaaa") {
			carried.WriteString(b.Text.Text)
		}
	}
	// The run is whole across the blocks: concatenated, the body the plan wrote is there unbroken.
	if !strings.Contains(carried.String(), body) {
		t.Errorf("the body's %d characters are not unbroken in the blocks", len(body))
	}
}

// proposalBody is the template's body with padding in its first section, so parseBody still splits
// it: the renderer's three labelled parts, not a raw body in a code block.
func proposalBody(padding string) string {
	return "## What is wrong\n" + padding + "\n\n## What it should be\nit resolves\n\n## Acceptance check\nthe check passes"
}

// mustBlocks is proposalSections' blocks for a payload, failing the test on an error.
func mustBlocks(t *testing.T, p ProposalPayload, q *intake.Question) json.RawMessage {
	t.Helper()
	_, blocks, err := proposalSections(p, q, nil, "C03")
	if err != nil {
		t.Fatalf("proposalSections: %v", err)
	}
	return blocks
}

// ---- the message in the thread: posted once, edited in place, found again after a restart ----

// proposalApp is an App that remembers nothing but Slack: no thread map, no store, the bot's own id
// the fake posts as (CD20).
func proposalApp(fake *fakeSlack) *App { return &App{Slack: fake, BotUserID: "U0BOT"} }

// revision is p at another revision: the same plan, its own hash, the same author.
func revision(t *testing.T, p ProposalPayload, rev int) ProposalPayload {
	t.Helper()
	next, err := newProposal(p.Plan, p.Hash, p.Author, rev)
	if err != nil {
		t.Fatalf("newProposal at rev %d: %v", rev, err)
	}
	next.Degraded = p.Degraded
	return next
}

// CD7: forty revisions are one message. The thread ends with one ts, four chat.update calls, and the
// last message's metadata carrying the last plan — and when Slack refuses an edit, the App reposts
// and makes the old message inert, so the thread holds one live proposal and never two (Review
// Focus 1, D10's rule: made inert once, never retried into a loop).
func TestOneProposalMessageAcrossRevisions(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSlack{msgs: map[string]Message{}}
	app := proposalApp(fake)
	const channel, thread = "C03", "1729.0001"

	first := proposalFixture(t)
	if got := app.postProposal(ctx, channel, thread, first, nil); got != "proposed" {
		t.Fatalf("postProposal: %q", got)
	}
	if n := len(fake.posts); n != 1 {
		t.Fatalf("%d posts, want the one proposal message", n)
	}
	ts := fake.posts[0].ts
	if fake.posts[0].thread != thread || len(fake.posts[0].blocks) == 0 {
		t.Fatalf("the proposal is not a thread reply with blocks: %+v", fake.posts[0])
	}
	if fake.posts[0].meta == nil || fake.posts[0].meta.EventType != proposalEventType {
		t.Fatalf("the proposal carries no metadata: %+v", fake.posts[0].meta)
	}

	prev := first
	for rev := 2; rev <= 5; rev++ {
		next := revision(t, prev, rev)
		if got := app.updateProposal(ctx, channel, ts, prev, next, nil); got != "updated" {
			t.Fatalf("updateProposal at rev %d: %q", rev, got)
		}
		prev = next
	}

	// One message, four edits on it: a revision never makes a second message.
	if n := len(fake.posts); n != 1 {
		t.Fatalf("%d messages after four revisions, want one", n)
	}
	edits := fake.proposalEdits()
	if len(edits) != 4 {
		t.Fatalf("%d proposal updates, want four", len(edits))
	}
	for _, e := range edits {
		if e.ts != ts || e.channel != channel {
			t.Fatalf("an update named %s/%s, want %s/%s", e.channel, e.ts, channel, ts)
		}
		if e.meta == nil || len(e.blocks) == 0 || e.text == "" {
			t.Fatalf("an update carried no metadata, blocks or text: %+v", e)
		}
		// Only the revision moved: nothing material did, so no section is marked (CD14, CD27).
		if strings.Contains(e.text, "‹changed›") {
			t.Fatalf("a revision alone marked a section: %q", e.text)
		}
	}

	// The last revision's plan is the message's metadata: a restart reads the whole plan back.
	m, ok := fake.msgs[ts]
	if !ok {
		t.Fatal("the proposal message is gone")
	}
	last, ok := m.Metadata.proposalPayload()
	if !ok || last.Rev != 5 || last.Hash != prev.Hash || !reflect.DeepEqual(last.Plan, prev.Plan) {
		t.Fatalf("the message's metadata after five revisions: %+v %v", last, ok)
	}
	if latest, closed := app.threadProposal(fake.thread()); latest == nil || latest.TS != ts || closed {
		t.Fatalf("the thread's one live proposal: %+v %v", latest, closed)
	}

	// Slack refuses the sixth edit — an edit window, a metadata shape it will not take, a message
	// deleted. The person is never left with two live proposals and never with buttons binding a
	// hash the metadata no longer holds (Review Focus 1).
	fake.failProposalUpdates = 1
	sixth := revision(t, prev, 6)
	if got := app.updateProposal(ctx, channel, ts, prev, sixth, nil); got != "reposted" {
		t.Fatalf("updateProposal over a refused edit: %q", got)
	}
	if n := len(fake.posts); n != 2 {
		t.Fatalf("%d messages, want the repost: %+v", n, fake.posts)
	}
	// The repost is a reply in the same conversation: without the thread's root ts to hand, the
	// replaced message's own ts is the handle Slack resolves to the thread it is in.
	fresh := fake.posts[1]
	if fresh.ts == ts || len(fresh.blocks) == 0 || fresh.thread != ts {
		t.Fatalf("the reposted proposal: %+v", fresh)
	}
	if p, ok := fresh.meta.proposalPayload(); !ok || p.Rev != 6 || !reflect.DeepEqual(p.Plan, sixth.Plan) {
		t.Fatalf("the reposted metadata: %+v %v", p, ok)
	}

	// The old message is made inert, once, with an emptied block list — not an absent one: chat.update
	// leaves the blocks alone when the key is absent, so nil would keep the old Confirm and Cancel
	// live beside the fresh proposal's (Review Focus 1). The approval design's inert card writes the
	// same emptied list for the same reason.
	inert := fake.updatesMade()
	if len(inert) != 1 || inert[0].ts != ts || inert[0].channel != channel ||
		inert[0].text != "This proposal was replaced below." {
		t.Fatalf("the replaced message: %+v", inert)
	}
	if string(inert[0].blocks) != "[]" {
		t.Fatalf("the replaced message's blocks are %q, want an emptied list", inert[0].blocks)
	}
	// Slack keeps a message's metadata through an update that carries none, so the replaced message
	// is still decodable as the revision it showed. Only threadProposal's newest-wins rule keeps one
	// proposal live: PR 6 must read the newest proposal before filing, never the clicked message's
	// metadata (the comment on repostProposal says so).
	if old, ok := fake.msgs[ts].Metadata.proposalPayload(); !ok || old.Rev != 5 {
		t.Fatalf("the replaced message no longer reads as the revision it showed: %+v %v", old, ok)
	}
	if latest, closed := app.threadProposal(fake.thread()); latest == nil || latest.TS != fresh.ts || closed {
		t.Fatalf("the newest message is the one live proposal: %+v %v", latest, closed)
	}
}

// CD20 and acceptance 5: an App built from nothing — no memory of the thread — recovers the same plan
// and hash from the message's metadata. A message whose metadata is absent, or whose hash does not
// hold, is not a proposal; a Filed marker closes the thread.
func TestProposalRecoveredFromMetadataAlone(t *testing.T) {
	ctx := context.Background()
	fake := &fakeSlack{msgs: map[string]Message{}}
	app := proposalApp(fake)
	p := proposalFixture(t)
	if got := app.postProposal(ctx, "C03", "1729.0001", p, nil); got != "proposed" {
		t.Fatalf("postProposal: %q", got)
	}

	// A second App that has never seen this thread: the message is the whole of what it knows.
	restarted := proposalApp(fake)
	latest, closed := restarted.threadProposal(fake.thread())
	if latest == nil || closed {
		t.Fatalf("a restarted App reads %+v, closed %v", latest, closed)
	}
	if latest.Metadata == nil || latest.Metadata.EventType != proposalEventType {
		t.Fatalf("the recovered message's tag: %+v", latest.Metadata)
	}
	back, ok := latest.Metadata.proposalPayload()
	if !ok {
		t.Fatal("the message's metadata is not a proposal")
	}
	if back.Hash != intake.Hash(back.Plan) {
		t.Fatalf("the recovered hash %q is not the hash of the recovered plan", back.Hash)
	}
	if !reflect.DeepEqual(back.Plan, p.Plan) || back.Rev != p.Rev || back.Author != p.Author {
		t.Fatalf("the recovered plan: %+v", back)
	}

	// A hash of a different plan is not a proposal, whatever the tag says.
	other := clonePlan(t, p.Plan)
	other.Issues[0].Title = "docs: another plan entirely"
	forged := ProposalPayload{Plan: p.Plan, Hash: intake.Hash(other), Author: p.Author, Rev: 1}
	if latest, closed := restarted.threadProposal([]Message{{TS: "1729.2000", ThreadTS: "1729.0001",
		User: "U0BOT", Text: "a proposal", Metadata: metaOf(t, proposalEventType, forged)}}); latest != nil || closed {
		t.Fatalf("a proposal whose hash does not hold: %+v %v", latest, closed)
	}

	// No metadata at all: the rendered text is not a proposal (nothing here parses it).
	if latest, _ := restarted.threadProposal([]Message{{TS: "1729.2001", ThreadTS: "1729.0001",
		User: "U0BOT", Text: "Proposal v1 · sha256:…"}}); latest != nil {
		t.Fatalf("a message with no metadata: %+v", latest)
	}

	// A Filed marker in the thread closes it: the proposal is no longer the thread's live one.
	msgs := append(fake.thread(), Message{TS: "1729.2002", ThreadTS: "1729.0001", User: "U0BOT",
		Text:     "Filed: Smana/ai-native-ref",
		Metadata: metaOf(t, filedEventType, MarkerPayload{Repo: "Smana/ai-native-ref", Room: "26zfnuxm"})})
	if latest, closed := restarted.threadProposal(msgs); latest == nil || !closed {
		t.Fatalf("a filed thread: %+v closed=%v", latest, closed)
	}
}

// CD18: a plan the composer wrote, not the planner's, says so in the message — the label rides the
// payload so the message says it, and a restart that reads the metadata back still says it. A
// degraded plan is never shown as the planner's.
func TestProposalCarriesTheDegradedComposerLabel(t *testing.T) {
	p := proposalFixture(t)
	plain := mustBlocks(t, p, nil)
	for _, b := range blocksOf(t, plain) {
		if b.Text != nil && strings.Contains(b.Text.Text, intake.TemplateLabel) {
			t.Fatalf("a plan the planner wrote says it is a template draft: %q", b.Text.Text)
		}
	}

	degraded := p
	degraded.Degraded = true
	text, raw, err := proposalSections(degraded, nil, nil, "C03")
	if err != nil {
		t.Fatalf("proposalSections: %v", err)
	}
	if !strings.Contains(text, intake.TemplateLabel) {
		t.Errorf("the fallback text does not carry the label: %q", text)
	}
	found := false
	for _, b := range blocksOf(t, raw) {
		if b.Text != nil && strings.Contains(b.Text.Text, intake.TemplateLabel) {
			found = true
		}
	}
	if !found {
		t.Error("no block carries the degraded composer's label")
	}

	// The label rides the payload, so the message read back after a restart says it too.
	meta := &Metadata{EventType: proposalEventType, EventPayload: mustJSON(t, degraded)}
	back, ok := meta.proposalPayload()
	if !ok || !back.Degraded || !proposalHashHolds(back) {
		t.Fatalf("the recovered degraded proposal: %+v %v", back, ok)
	}
	// It is display, not material: the hash still binds the plan, and the label marks no section.
	if back.Hash != intake.Hash(back.Plan) {
		t.Fatalf("the degraded payload's hash does not bind its plan")
	}
	// A payload that never says it is not degraded, and the tag's wire bytes keep their shape.
	if raw, err := json.Marshal(ProposalPayload{Plan: p.Plan, Hash: p.Hash, Author: p.Author, Rev: p.Rev}); err != nil ||
		strings.Contains(string(raw), "degraded") {
		t.Fatalf("a non-degraded payload carries the key: %s (%v)", raw, err)
	}
}

// CD19 and §plan's Slack bound: a plan Slack cannot carry, or one covering more than one issue, is
// refused with its one pointer line — nothing is filed from a message that showed part of a plan.
func TestPostProposalRefusesWhatSlackCannotFile(t *testing.T) {
	ctx := context.Background()

	t.Run("a plan past the 7 KiB bound", func(t *testing.T) {
		fake := &fakeSlack{msgs: map[string]Message{}}
		app := proposalApp(fake)
		plan := planFixture(t, "slack-one-issue")
		padding := strings.Repeat("x", maxProposalPlanBytes)
		plan.Issues[0].Body = proposalBody(padding)
		p := ProposalPayload{Plan: plan, Hash: intake.Hash(plan), Author: "U07JY4EQNDN", Rev: 1}

		if got := app.postProposal(ctx, "C03", "1729.0001", p, nil); got != "too_large" {
			t.Fatalf("postProposal = %q", got)
		}
		if len(fake.posts) != 1 || len(fake.posts[0].blocks) != 0 || fake.posts[0].meta != nil {
			t.Fatalf("the pointer line: %+v", fake.posts)
		}
		if !strings.Contains(fake.posts[0].text, "use your coding agent or the web UI") {
			t.Fatalf("the line is %q", fake.posts[0].text)
		}
	})

	t.Run("a plan covering two issues", func(t *testing.T) {
		fake := &fakeSlack{msgs: map[string]Message{}}
		app := proposalApp(fake)
		plan := planFixture(t, "slack-one-issue")
		second := plan.Issues[0]
		second.Index, second.Title = 2, "docs: a second issue"
		plan.Issues = append(plan.Issues, second)
		p := ProposalPayload{Plan: plan, Hash: intake.Hash(plan), Author: "U07JY4EQNDN", Rev: 1}

		if got := app.postProposal(ctx, "C03", "1729.0001", p, nil); got != "multi_issue" {
			t.Fatalf("postProposal = %q", got)
		}
		if len(fake.posts) != 1 || len(fake.posts[0].blocks) != 0 || fake.posts[0].meta != nil {
			t.Fatalf("the pointer line: %+v", fake.posts)
		}
		if !strings.Contains(fake.posts[0].text, "2 issues") ||
			!strings.Contains(fake.posts[0].text, "Slack files one") {
			t.Fatalf("the line is %q", fake.posts[0].text)
		}
	})
}
