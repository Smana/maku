// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/metrics"
)

// reviewReply is a valid reply in the review class, suggesting review-frontier.
var reviewReply = func() []byte {
	draft := `{"repository":"smana/ai-native-ref","issues":[{"title":"docs: the install guide is out of date",` +
		`"body":"## What is wrong\nThe install guide names a flag that is gone.\n## What it should be\nIt names the current flag.\n## Acceptance check\nRunning the guide's command works.",` +
		`"class":"review","suggestion":{"profile":"review-frontier","why":"the guide spans several pages"}}]}`
	content, _ := json.Marshal(draft)
	return []byte(`{"choices":[{"message":{"content":` + string(content) + `}}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
}()

// P2, G4: intake_plan plans from each candidate's catalogue: the model is told its classes and
// profiles, the plan is held to the plan repository's, and the suggestion is the broker's reading.
func TestIntakePlanReadsTheCatalogue(t *testing.T) {
	src := &fakeFactoryCatalogue{}
	e := setup(t, mcpOn, withIntake(t, "session"), withCatalogue(src))
	e.intake.replyWith(reviewReply)
	got := call(t, e, viaRoomctl("dev"), "intake_plan", planArgs())
	if got.isError {
		t.Fatal(got.text)
	}
	if sent := e.intake.sent(); !strings.Contains(sent, "review-frontier") || !strings.Contains(sent, "links-solo (default)") {
		t.Fatalf("the model was not told the catalogue's profiles: %s", sent)
	}
	var out struct {
		Plan intake.Plan `json:"plan"`
	}
	if err := json.Unmarshal(got.structured, &out); err != nil {
		t.Fatal(err)
	}
	is := out.Plan.Issues[0]
	if is.Class != "review" || is.Profile != "" || len(is.Roles) != 0 || out.Plan.Budget.CeilingUSD != 8 {
		t.Fatalf("%+v %+v", is, out.Plan.Budget)
	}
	if s := is.Suggestion; s == nil || s.Profile != "review-frontier" || s.Merge.Outcome != "approval" || !s.Merge.ApproveToMerge {
		t.Fatalf("suggestion %+v", s)
	}
	if len(src.asked) == 0 || !strings.EqualFold(strings.Split(src.asked[0], "|")[0], "smana/ai-native-ref") {
		t.Fatalf("asked %v", src.asked)
	}
	// plan_check holds the shown plan to the same catalogue: review is not in the config's table,
	// so only the catalogue lets it past the class rule (its floor gaps are what remain).
	if got := call(t, e, viaRoomctl("dev"), "plan_check", map[string]any{"plan": out.Plan, "hash": out.Plan.Hash}); !got.isError ||
		!strings.Contains(got.text, "unresolved_marker") {
		t.Fatal(got.text)
	}
}

// Until stage 5 a catalogue that does not answer leaves the config's classes and no suggestion, and
// the cache counts the error.
func TestIntakePlanCatalogueDown(t *testing.T) {
	src := &fakeFactoryCatalogue{byRepo: map[string]error{"smana/ai-native-ref": errors.New("down")}}
	reads := countReads{}
	e := setup(t, mcpOn, withIntake(t, "session"), func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.Catalogue = NewCatalogueCache(src, reads, nil)
	})
	got := call(t, e, viaRoomctl("dev"), "intake_plan", planArgs())
	if got.isError {
		t.Fatal(got.text)
	}
	var out struct {
		Plan intake.Plan `json:"plan"`
	}
	if err := json.Unmarshal(got.structured, &out); err != nil {
		t.Fatal(err)
	}
	if is := out.Plan.Issues[0]; is.Class != "docs-links" || is.Suggestion != nil || out.Plan.Budget.Source != "class:docs-links.taskUSD" {
		t.Fatalf("%+v %+v", is, out.Plan.Budget)
	}
	if reads[metrics.CatalogueError] == 0 {
		t.Fatal("the failed read was not counted")
	}
}
