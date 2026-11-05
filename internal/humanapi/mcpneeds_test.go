// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/summary"
)

// splitAndMergeGate is PR 3's list fixture as a tool call takes it: the split proposal on 26zfnuxm and
// the pull request waiting for dev's merge on 3kq7x2ma (needsWorld), with the GitHub on which dev writes
// Smana/a — injected as one option, since a tool call names no route of its own.
type splitAndMergeGate struct{ gate option }

// worldWithSplitAndMerge is that fixture, under the plan's name for it: the world, the split and the
// merge in one option.
func worldWithSplitAndMerge(t *testing.T) splitAndMergeGate {
	t.Helper()
	return splitAndMergeGate{gate: needsWorld(t, nil)}
}

// needKeysOf is a needs_list answer's items' identities, in the order the tool gave them (CD21).
func needKeysOf(t *testing.T, got toolCall) []string {
	t.Helper()
	var data struct{ Items []summary.Need }
	if err := json.Unmarshal([]byte(got.structured), &data); err != nil {
		t.Fatalf("%s: %v", got.structured, err)
	}
	return needKeys(data.Items)
}

// CD10: the tool's items are the route's items, in the same order, and its text is the list to show as it is.
func TestNeedsListMatchesTheRoute(t *testing.T) {
	e := setup(t, mcpOn, needsOn, worldWithSplitAndMerge(t).gate)
	got := call(t, e, viaRoomctl("dev"), "needs_list", map[string]any{})
	var data struct{ Items []summary.Need }
	if err := json.Unmarshal([]byte(got.structured), &data); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, n := range data.Items {
		keys = append(keys, n.Key())
	}
	if !slices.Equal(keys, []string{"26zfnuxm/plan_confirmation/2238", "3kq7x2ma/merge/2239"}) {
		t.Fatalf("%v", keys)
	}
	if !strings.Contains(got.text, "needs you") {
		t.Fatalf("%s", got.text)
	}
}

// CD21: the tool is the route's list, item for item: one derivation, one order, and the same envelope
// as structuredContent (the `since` cursor included).
func TestNeedsListIsTheRoutesList(t *testing.T) {
	e := setup(t, mcpOn, needsOn, worldWithSplitAndMerge(t).gate)
	route := mustNeeds(t, e, "", admin("dev"))
	tool := call(t, e, viaRoomctl("dev"), "needs_list", map[string]any{})
	if got, want := needKeysOf(t, tool), needKeys(route.Items); !slices.Equal(got, want) {
		t.Fatalf("%v, want the route's %v", got, want)
	}
	var data needsBody
	if err := json.Unmarshal([]byte(tool.structured), &data); err != nil {
		t.Fatal(err)
	}
	if data.APIVersion != needsVersion || data.Access != route.Access {
		t.Fatalf("apiVersion %q, access %q; want %q and %q", data.APIVersion, data.Access, needsVersion, route.Access)
	}
	if _, err := time.Parse(time.RFC3339, data.Cursor); err != nil {
		t.Errorf("cursor %q is no RFC 3339 instant: %v", data.Cursor, err)
	}
}

// Review Focus 3: a `since` that is not an RFC 3339 instant, or one in the future, is refused by the tool
// as the route refuses it — never read as everything, and never as nothing.
func TestNeedsListRefusesABadSince(t *testing.T) {
	e := setup(t, mcpOn, needsOn, worldWithSplitAndMerge(t).gate)
	future := e.srv.now().Add(time.Hour).UTC().Format(time.RFC3339)
	for _, since := range []string{"yesterday", "2026-11-04 19:10:00", "9999-99-99T00:00:00Z", future} {
		got := call(t, e, viaRoomctl("dev"), "needs_list", map[string]any{"since": since})
		if !got.isError || !strings.Contains(got.text, "invalid_arguments: since is an RFC 3339 instant") {
			t.Errorf("%s: %+v", since, got)
		}
	}
	// A cursor the answer gave back is always served: the refusal never costs the client its own cursor.
	served := call(t, e, viaRoomctl("dev"), "needs_list", map[string]any{})
	var data struct{ Cursor string }
	if err := json.Unmarshal([]byte(served.structured), &data); err != nil {
		t.Fatal(err)
	}
	if got := call(t, e, viaRoomctl("dev"), "needs_list", map[string]any{"since": data.Cursor}); got.isError {
		t.Fatalf("its own cursor %q: %s", data.Cursor, got.text)
	}
}

// CD28: the tool's schema is closed and carries no field that could act: an argument this list does not
// take, a decision among them, is refused rather than ignored.
func TestNeedsListTakesNoDecision(t *testing.T) {
	e := setup(t, mcpOn, needsOn, worldWithSplitAndMerge(t).gate)
	for _, extra := range []string{"decision", "approve", "merge", "action"} {
		got := call(t, e, viaRoomctl("dev"), "needs_list", map[string]any{extra: "x"})
		if !got.isError || !strings.Contains(got.text, "invalid_arguments") {
			t.Errorf("%s: %+v", extra, got)
		}
	}
}

// Review Focus 1: a room whose facts cannot be read contributes no item, and the tool's text says so in
// one line — naming only a room the caller may read, as the route does.
func TestNeedsListSaysWhenARoomsStateIsUnread(t *testing.T) {
	e := setup(t, mcpOn, needsOn, worldWithSplitAndMerge(t).gate, unreadableFactsRooms(roomID))
	got := call(t, e, viaRoomctl("dev"), "needs_list", map[string]any{})
	if keys := needKeysOf(t, got); !slices.Equal(keys, []string{"26zfnuxm/plan_confirmation/2238"}) {
		t.Fatalf("%v, want the readable room's item alone", keys)
	}
	var data struct{ Unread []string }
	if err := json.Unmarshal([]byte(got.structured), &data); err != nil || !slices.Equal(data.Unread, []string{roomID}) {
		t.Fatalf("unread %v (%v), want [%s]", data.Unread, err, roomID)
	}
	if !strings.Contains(got.text, "could not be read") || !strings.Contains(got.text, roomID) {
		t.Fatalf("%s, want the one line naming %s", got.text, roomID)
	}
}

// The list narrows to one room and to one repository, as the route's does, and never reveals a room the
// caller may not read.
func TestNeedsListNarrowsToRoomAndRepo(t *testing.T) {
	e := setup(t, mcpOn, needsOn, worldWithSplitAndMerge(t).gate)
	if keys := needKeysOf(t, call(t, e, viaRoomctl("dev"), "needs_list", map[string]any{"room": roomID})); !slices.Equal(keys, []string{"3kq7x2ma/merge/2239"}) {
		t.Fatalf("room: %v", keys)
	}
	if keys := needKeysOf(t, call(t, e, viaRoomctl("dev"), "needs_list", map[string]any{"repo": "smana/A"})); len(keys) != 2 {
		t.Fatalf("repo, as the room list matches it: %v", keys)
	}
	for _, args := range []map[string]any{{"repo": "Smana/b"}, {"room": roomC}} {
		if keys := needKeysOf(t, call(t, e, viaRoomctl("dev"), "needs_list", args)); len(keys) != 0 {
			t.Errorf("%v: %v", args, keys)
		}
	}
}

// CD28: needs_list is one list with GET /api/needs, so the operator's switch governs both: with
// human.needs.enabled off the tool is on neither list, and a call on either door is an unknown tool.
// The App never reads needs the feature is off for.
func TestNeedsListFollowsItsSwitch(t *testing.T) {
	inits := map[string]http.Header{"a human": viaRoomctl("dev"), "the App": bearer("slack-app")}
	off := setup(t, mcpOn, withSponsored(sponsoredP)) // needsOn left out: human.needs.enabled is off
	for name, h := range inits {
		if names := toolNames(t, off, h); strings.Contains(names, "needs_list") {
			t.Errorf("%s sees needs_list while human.needs.enabled is off: %s", name, names)
		}
		_, out := rpc(t, off, h, "tools/call", map[string]any{"name": "needs_list", "arguments": map[string]any{}})
		if rpcCode(out) != -32602 || !strings.Contains(fmt.Sprint(out["error"]), "unknown tool: needs_list") {
			t.Errorf("%s, switch off: %v", name, out)
		}
	}
	on := setup(t, mcpOn, needsOn, withSponsored(sponsoredP))
	for name, h := range inits {
		if names := toolNames(t, on, h); !strings.Contains(names, "needs_list") {
			t.Errorf("%s does not see needs_list with human.needs.enabled on: %s", name, names)
		}
	}
}

// The list is measured by the door it came through: needs_list_total carries surface "tool" for this one,
// as surface "api" for the route, so a dashboard's single query covers both (PR 3's metrics).
func TestNeedsListIsMeasured(t *testing.T) {
	e := setup(t, mcpOn, needsOn, worldWithSplitAndMerge(t).gate)
	call(t, e, viaRoomctl("dev"), "needs_list", map[string]any{})
	call(t, e, viaRoomctl("dev"), "needs_list", map[string]any{"since": "yesterday"})
	body := scrape(t, e)
	for _, want := range []string{
		`maku_needs_list_total{outcome="ok",principal="human:dev",surface="tool"} 1`,
		`maku_needs_list_total{outcome="refused",principal="human:dev",surface="tool"} 1`,
	} {
		if !strings.Contains(body, want+"\n") {
			t.Errorf("missing %s in\n%s", want, body)
		}
	}
}
