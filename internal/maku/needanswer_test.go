// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAppendDecision(t *testing.T) {
	const plain = "## What is wrong\nx\n\n## What it should be\ny\n\n## Acceptance check\ntask check"
	t.Run("creates Decisions after Acceptance check", func(t *testing.T) {
		got, err := AppendDecision(plain, "  Use task check  ")
		want := plain + "\n\n## Decisions\n\nD1: Use task check"
		if err != nil || got != want {
			t.Fatalf("AppendDecision() = %q, %v; want %q", got, err, want)
		}
	})

	t.Run("uses the maximum interleaved non-contiguous number", func(t *testing.T) {
		body := plain + "\n\n## Decisions\n\nD1 [g1]: first choice\nA2 [g2]: assumed second\nD7: scoped earlier"
		got, err := AppendDecision(body, "Use Linux")
		want := body + "\nD8: Use Linux"
		if err != nil || got != want {
			t.Fatalf("AppendDecision() = %q, %v; want %q", got, err, want)
		}
	})

	t.Run("preserves every prior byte on repeated appends", func(t *testing.T) {
		body := plain + "\n\n## Decisions\n\nD1: first\n"
		first, err := AppendDecision(body, "second")
		if err != nil || first != body+"D2: second" {
			t.Fatalf("first AppendDecision() = %q, %v", first, err)
		}
		second, err := AppendDecision(first, "third")
		if err != nil || second != first+"\nD3: third" {
			t.Fatalf("second AppendDecision() = %q, %v", second, err)
		}
	})

	for _, answer := range []string{"", " \t "} {
		t.Run("rejects blank "+strings.ReplaceAll(answer, "\t", "tab"), func(t *testing.T) {
			if got, err := AppendDecision(plain, answer); err == nil || got != "" {
				t.Fatalf("AppendDecision() = %q, %v; want no body and an error", got, err)
			}
		})
	}
	for _, answer := range []string{"first\nsecond", "first\rsecond", "\nfirst", "first\n"} {
		t.Run("rejects multiline "+strings.NewReplacer("\n", "LF", "\r", "CR").Replace(answer), func(t *testing.T) {
			if got, err := AppendDecision(plain, answer); err == nil || got != "" {
				t.Fatalf("AppendDecision() = %q, %v; want no body and an error", got, err)
			}
		})
	}

	t.Run("rejects an invalid existing Decisions section", func(t *testing.T) {
		body := plain + "\n\n## Decisions\nD2: later\nD1: earlier"
		if got, err := AppendDecision(body, "third"); err == nil || got != "" {
			t.Fatalf("AppendDecision() = %q, %v; want no body and an error", got, err)
		}
	})
	t.Run("does not create Decisions without Acceptance check", func(t *testing.T) {
		if got, err := AppendDecision("## What is wrong\nx", "choice"); err == nil || got != "" {
			t.Fatalf("AppendDecision() = %q, %v; want no body and an error", got, err)
		}
	})
}

func TestGHIssueView(t *testing.T) {
	dir := t.TempDir()
	fixture := `{"body":"## Acceptance check\ntask check","labels":[{"name":"factory/ready"},{"name":"needs/spec"}],"updatedAt":"2026-11-09T10:11:12Z","comments":[{"author":{"login":"alice"},"body":"first","createdAt":"2026-11-09T10:12:13Z"},{"author":{"login":"bob"},"body":"second","createdAt":"2026-11-09T10:13:14Z"}]}`
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(dir, "args") + "\ncat <<'EOF'\n" + fixture + "\nEOF\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := (GHIssues{}).View(t.Context(), "Smana/a", 7)
	if err != nil {
		t.Fatal(err)
	}
	want := IssueSnapshot{
		Body:      "## Acceptance check\ntask check",
		Labels:    []string{"factory/ready", "needs/spec"},
		UpdatedAt: time.Date(2026, 11, 9, 10, 11, 12, 0, time.UTC),
		Comments: []IssueComment{
			{Author: "alice", Body: "first", CreatedAt: time.Date(2026, 11, 9, 10, 12, 13, 0, time.UTC)},
			{Author: "bob", Body: "second", CreatedAt: time.Date(2026, 11, 9, 10, 13, 14, 0, time.UTC)},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("View() = %#v; want %#v", got, want)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "issue\nview\n7\n--repo=Smana/a\n--json\nbody,labels,updatedAt,comments\n" {
		t.Fatalf("gh args = %q", args)
	}
}

func TestGHIssueEditBody(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(dir, "args") + "\ncat > " + filepath.Join(dir, "stdin") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	body := "## Acceptance check\ntask check\n\n## Decisions\n\nD1: use gh\n--add-label=factory/ready\n"
	if err := (GHIssues{}).EditBody(t.Context(), "Smana/a", 7, body); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if string(args) != "issue\nedit\n7\n--repo=Smana/a\n--body-file=-\n" {
		t.Fatalf("gh args = %q", args)
	}
	stdin, err := os.ReadFile(filepath.Join(dir, "stdin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(stdin) != body {
		t.Fatalf("gh stdin = %q; want %q", stdin, body)
	}
}

func TestGHIssueCallsTimeOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := ghTimeout
	ghTimeout = 100 * time.Millisecond
	t.Cleanup(func() { ghTimeout = old })
	start := time.Now()
	if _, err := (GHIssues{}).View(t.Context(), "Smana/a", 7); err == nil {
		t.Fatal("View succeeded while gh was stuck")
	}
	if err := (GHIssues{}).EditBody(t.Context(), "Smana/a", 7, "body"); err == nil {
		t.Fatal("EditBody succeeded while gh was stuck")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("gh was not cut off")
	}
}

const needAnswerBody = "## What is wrong\nx\n\n## What it should be\ny\n\n## Acceptance check\ntask check"

var needAnswerUpdated = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

type issueBodyEdit struct {
	repo  string
	issue int
	body  string
}

type fakeIssueEditor struct {
	snapshots []IssueSnapshot
	views     int
	edits     []issueBodyEdit
	viewErr   error
	editErr   error
}

func (f *fakeIssueEditor) View(context.Context, string, int) (IssueSnapshot, error) {
	f.views++
	if f.viewErr != nil {
		return IssueSnapshot{}, f.viewErr
	}
	if len(f.snapshots) == 0 {
		return IssueSnapshot{}, fmt.Errorf("no issue snapshot")
	}
	return f.snapshots[min(f.views-1, len(f.snapshots)-1)], nil
}

func (f *fakeIssueEditor) EditBody(_ context.Context, repo string, issue int, body string) error {
	f.edits = append(f.edits, issueBodyEdit{repo: repo, issue: issue, body: body})
	return f.editErr
}

type needAnswerBroker struct {
	srv *httptest.Server

	mu    sync.Mutex
	rooms []map[string]any
	brief map[string]any
	calls []toolCall
}

func newNeedAnswerBroker(t *testing.T, rooms []map[string]any, brief map[string]any) *needAnswerBroker {
	t.Helper()
	b := &needAnswerBroker{rooms: rooms, brief: brief}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m message
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			t.Errorf("decode broker call: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var c struct {
			Name string         `json:"name"`
			Args map[string]any `json:"arguments"`
		}
		_ = json.Unmarshal(m.Params, &c)
		b.mu.Lock()
		b.calls = append(b.calls, toolCall{Name: c.Name, Args: c.Args})
		b.mu.Unlock()
		res := map[string]any{"content": []any{map[string]any{"type": "text", "text": "broker"}}, "structuredContent": map[string]any{}}
		switch c.Name {
		case "rooms_list":
			res["structuredContent"] = map[string]any{"rooms": b.rooms}
		case "room_brief":
			if b.brief == nil {
				res["isError"] = true
				res["content"] = []any{map[string]any{"type": "text", "text": "no such room"}}
			} else {
				res["structuredContent"] = b.brief
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": res})
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *needAnswerBroker) toolCalls() []toolCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]toolCall(nil), b.calls...)
}

type needAnswerRig struct {
	relay  *Relay
	issues *fakeIssueEditor
	broker *needAnswerBroker
	form   map[string]any
	forms  int
}

func newNeedAnswerRig(t *testing.T, issues *fakeIssueEditor, rooms []map[string]any, brief map[string]any) *needAnswerRig {
	t.Helper()
	rig := &needAnswerRig{issues: issues}
	rig.broker = newNeedAnswerBroker(t, rooms, brief)
	labels := 0
	rig.relay = &Relay{URL: rig.broker.srv.URL, HC: rig.broker.srv.Client(), Token: tok, Issues: issues,
		Label: func(context.Context, string, int, string) error { labels++; return nil },
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)), forms: true,
		waiting: map[string]chan json.RawMessage{}}
	t.Cleanup(func() {
		if labels != 0 {
			t.Errorf("need_answer called Label %d times", labels)
		}
	})
	rig.relay.out = writerFunc(func(line []byte) (int, error) {
		var m message
		if json.Unmarshal(line, &m) == nil && m.Method == "elicitation/create" {
			rig.forms++
			_ = json.Unmarshal(m.Params, &rig.form)
			answer := map[string]any{"action": "accept", "content": map[string]any{"answer": "Use Linux", "send": true}}
			raw, err := json.Marshal(answer)
			if err != nil {
				t.Fatal(err)
			}
			rig.relay.answered(message{ID: m.ID, Result: raw})
		}
		return len(line), nil
	})
	return rig
}

func needAnswerSnapshot() IssueSnapshot {
	return IssueSnapshot{Body: needAnswerBody, Labels: []string{"factory/needs-author", "needs/spec"}, UpdatedAt: needAnswerUpdated}
}

func briefQuestion(text, recommended string) map[string]any {
	return map[string]any{"apiVersion": "brief/v1", "room": "3buqdlot", "text": "brief", "cursor": "seq:1",
		"question": map[string]any{"text": text, "recommended": recommended}}
}

func publicBounce(question, assumption string, at time.Time) IssueComment {
	return IssueComment{Author: "factory-app[bot]", CreatedAt: at, Body: "<!-- agent-factory scope=3buqdlot event=needs-author-2 -->\n" +
		"Agent factory task `3buqdlot` needs an answer. The scoper's question, and the assumption it would make otherwise:\n\n" +
		"```\n" + question + "\nAssumption: " + assumption + "\n```\n\n" +
		"**To answer**, edit this issue's title or body."}
}

func internalBounce(at time.Time) IssueComment {
	return IssueComment{Author: "factory-app[bot]", CreatedAt: at, Body: "<!-- agent-factory scope=3buqdlot event=needs-author-3 -->\n" +
		"Agent factory task `3buqdlot` needs an answer. The scoper's question stays in [the room](https://rooms.example/r/3buqdlot): the task's data class is not public."}
}

func needAnswerResult(t *testing.T, got any) map[string]any {
	t.Helper()
	m, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("need_answer result = %T", got)
	}
	if m["isError"] == true {
		t.Fatalf("need_answer refused: %s", toolText(got))
	}
	sc, ok := m["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("need_answer structuredContent = %#v", m["structuredContent"])
	}
	return sc
}

func requireNoNeedAnswerWrite(t *testing.T, rig *needAnswerRig) {
	t.Helper()
	if len(rig.issues.edits) != 0 {
		t.Fatalf("EditBody called %d times: %#v", len(rig.issues.edits), rig.issues.edits)
	}
}

func TestNeedAnswer(t *testing.T) {
	t.Run("success through the authorized room brief", func(t *testing.T) {
		initial := needAnswerSnapshot()
		initial.Comments = []IssueComment{publicBounce("Public question must not win?", "Public answer", needAnswerUpdated)}
		issues := &fakeIssueEditor{snapshots: []IssueSnapshot{initial}}
		rig := newNeedAnswerRig(t, issues, nil, briefQuestion("Which dashboard?", "Use agent-run"))
		got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7, "room": "3buqdlot"}))
		sc := needAnswerResult(t, got)
		if sc["answered"] != true || sc["started"] != false || sc["issue"] != "https://github.com/Smana/a/issues/7" ||
			sc["next"] != "a maintainer must re-apply factory/ready" {
			t.Fatalf("need_answer result = %#v", sc)
		}
		if issues.views != 2 || len(issues.edits) != 1 {
			t.Fatalf("View calls = %d, EditBody calls = %d", issues.views, len(issues.edits))
		}
		wantBody := needAnswerBody + "\n\n## Decisions\n\nD1: Use Linux"
		if got := issues.edits[0]; got.repo != "Smana/a" || got.issue != 7 || got.body != wantBody {
			t.Fatalf("EditBody = %#v; want body %q", got, wantBody)
		}
		if rig.forms != 1 {
			t.Fatalf("forms = %d", rig.forms)
		}
		message := fmt.Sprint(rig.form["message"])
		for _, want := range []string{"Which dashboard?", "Use agent-run", "## Decisions", "D1: <answer>", "factory/ready"} {
			if !strings.Contains(message, want) {
				t.Errorf("form does not show %q:\n%s", want, message)
			}
		}
		schema := rig.form["requestedSchema"].(map[string]any)
		required := fmt.Sprint(schema["required"])
		if !strings.Contains(required, "answer") || !strings.Contains(required, "send") {
			t.Errorf("form required fields = %s", required)
		}
		props := schema["properties"].(map[string]any)
		if props["answer"].(map[string]any)["type"] != "string" || props["send"].(map[string]any)["default"] != false {
			t.Errorf("form properties = %#v", props)
		}
		for _, call := range rig.broker.toolCalls() {
			if call.Name == "rooms_list" {
				t.Error("an explicit room was resolved through rooms_list")
			}
		}
	})

	t.Run("newest public bounce comment is the fallback", func(t *testing.T) {
		initial := needAnswerSnapshot()
		initial.Comments = []IssueComment{
			publicBounce("Old question?", "Old answer", needAnswerUpdated.Add(-time.Hour)),
			publicBounce("Newest public question?", "Use the public recommendation", needAnswerUpdated),
		}
		issues := &fakeIssueEditor{snapshots: []IssueSnapshot{initial}}
		rig := newNeedAnswerRig(t, issues, nil, nil)
		got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7, "room": "3buqdlot"}))
		needAnswerResult(t, got)
		if len(issues.edits) != 1 || rig.forms != 1 {
			t.Fatalf("EditBody calls = %d, forms = %d", len(issues.edits), rig.forms)
		}
		message := fmt.Sprint(rig.form["message"])
		if !strings.Contains(message, "Newest public question?") || !strings.Contains(message, "Use the public recommendation") || strings.Contains(message, "Old question?") {
			t.Fatalf("fallback form shows the wrong comment:\n%s", message)
		}
	})

	for _, tc := range []struct {
		name   string
		labels []string
	}{
		{name: "needs-author absent", labels: []string{"needs/spec"}},
		{name: "ready present", labels: []string{"factory/needs-author", "factory/ready"}},
		{name: "neither bounce nor ready", labels: nil},
	} {
		t.Run("wrong labels: "+tc.name, func(t *testing.T) {
			initial := needAnswerSnapshot()
			initial.Labels = tc.labels
			issues := &fakeIssueEditor{snapshots: []IssueSnapshot{initial}}
			rig := newNeedAnswerRig(t, issues, nil, briefQuestion("Question?", "Answer"))
			got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7, "room": "3buqdlot"}))
			if toolText(got) == "" || rig.forms != 0 || issues.views != 1 {
				t.Fatalf("result = %v, forms = %d, views = %d", got, rig.forms, issues.views)
			}
			requireNoNeedAnswerWrite(t, rig)
		})
	}

	t.Run("no elicitation", func(t *testing.T) {
		issues := &fakeIssueEditor{snapshots: []IssueSnapshot{needAnswerSnapshot()}}
		rig := newNeedAnswerRig(t, issues, nil, briefQuestion("Question?", "Answer"))
		rig.relay.forms = false
		got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7, "room": "3buqdlot"}))
		if !strings.Contains(toolText(got), "needs_confirmation") || rig.forms != 0 {
			t.Fatalf("result = %v, forms = %d", got, rig.forms)
		}
		requireNoNeedAnswerWrite(t, rig)
	})

	for _, tc := range []struct {
		name   string
		action string
		send   bool
	}{
		{name: "decline", action: "decline", send: true},
		{name: "cancel", action: "cancel", send: true},
		{name: "send left false", action: "accept", send: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issues := &fakeIssueEditor{snapshots: []IssueSnapshot{needAnswerSnapshot()}}
			rig := newNeedAnswerRig(t, issues, nil, briefQuestion("Question?", "Answer"))
			rig.relay.out = writerFunc(func(line []byte) (int, error) {
				var m message
				if json.Unmarshal(line, &m) == nil && m.Method == "elicitation/create" {
					rig.forms++
					raw, _ := json.Marshal(map[string]any{"action": tc.action, "content": map[string]any{"answer": "Use Linux", "send": tc.send}})
					rig.relay.answered(message{ID: m.ID, Result: raw})
				}
				return len(line), nil
			})
			got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7, "room": "3buqdlot"}))
			sc := needAnswerResult(t, got)
			if sc["answered"] != false || sc["started"] != false || rig.forms != 1 || issues.views != 1 {
				t.Fatalf("result = %#v, forms = %d, views = %d", sc, rig.forms, issues.views)
			}
			requireNoNeedAnswerWrite(t, rig)
		})
	}

	t.Run("nil Issues", func(t *testing.T) {
		r := &Relay{forms: true}
		got := r.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7}))
		if !strings.Contains(toolText(got), "unavailable") {
			t.Fatalf("need_answer without Issues = %v", got)
		}
	})
}

func TestNeedAnswerInternalWithoutRoom(t *testing.T) {
	for _, tc := range []struct {
		name     string
		comments []IssueComment
	}{
		{name: "no bounce question"},
		{name: "internal bounce", comments: []IssueComment{internalBounce(needAnswerUpdated)}},
		{name: "newest internal bounce hides an older public one", comments: []IssueComment{
			publicBounce("Old public question?", "Old answer", needAnswerUpdated.Add(-time.Hour)),
			internalBounce(needAnswerUpdated),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := needAnswerSnapshot()
			initial.Comments = tc.comments
			issues := &fakeIssueEditor{snapshots: []IssueSnapshot{initial}}
			rig := newNeedAnswerRig(t, issues, []map[string]any{}, nil)
			got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7}))
			if !strings.Contains(toolText(got), "needs_room_access") || rig.forms != 0 || issues.views != 1 {
				t.Fatalf("result = %v, forms = %d, views = %d", got, rig.forms, issues.views)
			}
			requireNoNeedAnswerWrite(t, rig)
		})
	}
}

func TestNeedAnswerRoomResolution(t *testing.T) {
	t.Run("none uses only the public fallback", func(t *testing.T) {
		initial := needAnswerSnapshot()
		initial.Comments = []IssueComment{publicBounce("Public question?", "Public answer", needAnswerUpdated)}
		issues := &fakeIssueEditor{snapshots: []IssueSnapshot{initial}}
		rig := newNeedAnswerRig(t, issues, []map[string]any{{"id": "other123", "repository": "Smana/other", "issue": 7}}, nil)
		got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7}))
		needAnswerResult(t, got)
		if len(issues.edits) != 1 {
			t.Fatalf("EditBody calls = %d", len(issues.edits))
		}
		for _, call := range rig.broker.toolCalls() {
			if call.Name == "room_brief" {
				t.Error("room_brief called without a resolved room")
			}
		}
	})

	t.Run("ambiguous refuses even with a public fallback", func(t *testing.T) {
		initial := needAnswerSnapshot()
		initial.Comments = []IssueComment{publicBounce("Public question?", "Public answer", needAnswerUpdated)}
		issues := &fakeIssueEditor{snapshots: []IssueSnapshot{initial}}
		rooms := []map[string]any{
			{"id": "roomone1", "repository": "Smana/a", "issue": 7},
			{"id": "roomtwo2", "repository": "smana/A", "issue": 7},
		}
		rig := newNeedAnswerRig(t, issues, rooms, briefQuestion("Question?", "Answer"))
		got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7}))
		if !strings.Contains(toolText(got), "ambiguous_room") || rig.forms != 0 {
			t.Fatalf("result = %v, forms = %d", got, rig.forms)
		}
		requireNoNeedAnswerWrite(t, rig)
		for _, call := range rig.broker.toolCalls() {
			if call.Name == "room_brief" {
				t.Error("room_brief called for an ambiguous issue")
			}
		}
	})
}

func TestNeedAnswerRejectsConflict(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*IssueSnapshot)
	}{
		{name: "body", change: func(s *IssueSnapshot) { s.Body += "\nConcurrent edit" }},
		{name: "labels", change: func(s *IssueSnapshot) { s.Labels = []string{"needs/spec", "factory/needs-author"} }},
		{name: "updatedAt", change: func(s *IssueSnapshot) { s.UpdatedAt = s.UpdatedAt.Add(time.Second) }},
		{name: "ready already present", change: func(s *IssueSnapshot) { s.Labels = append(slices.Clone(s.Labels), "factory/ready") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := needAnswerSnapshot()
			changed := needAnswerSnapshot()
			tc.change(&changed)
			issues := &fakeIssueEditor{snapshots: []IssueSnapshot{initial, changed}}
			rig := newNeedAnswerRig(t, issues, nil, briefQuestion("Question?", "Answer"))
			got := rig.relay.needAnswer(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "issue": 7, "room": "3buqdlot"}))
			if !strings.Contains(toolText(got), "issue_changed") || rig.forms != 1 || issues.views != 2 {
				t.Fatalf("result = %v, forms = %d, views = %d", got, rig.forms, issues.views)
			}
			requireNoNeedAnswerWrite(t, rig)
		})
	}
}
