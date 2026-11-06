// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
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

// blocksOf decodes a message's blocks, in order.
func blocksOf(t *testing.T, raw json.RawMessage) []slackBlock {
	t.Helper()
	var doc struct {
		Blocks []slackBlock `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode the blocks: %v", err)
	}
	return doc.Blocks
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
	if actions.Type != "actions" || len(actions.Elements) != 2 {
		t.Fatalf("the actions block is %+v", actions)
	}
	value := proposalValue(p.Hash, p.Rev)
	wantStyle := map[string]string{actionProposalConfirm: "primary", actionProposalCancel: "danger"}
	seen := map[string]bool{}
	for _, e := range actions.Elements {
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

	// The context block names the channel: a proposal has no room yet, so it links none.
	if ctx := blocks[len(blocks)-1]; ctx.Type != "context" || len(ctx.Elements) != 1 ||
		ctx.Elements[0].Text == nil || ctx.Elements[0].Text.Text != "<#C03>" {
		t.Errorf("the context block is %+v", blocks[len(blocks)-1])
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
