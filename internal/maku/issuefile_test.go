// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/mcpwire"
)

// withFile files through f.
func withFile(f IssueFiler) func(*Relay) { return func(r *Relay) { r.File = f } }

// filing records what the relay asked gh to file.
type filing struct {
	mu    sync.Mutex
	calls []string
	url   string // what gh prints; empty is an issue of Smana/a
}

func (f *filing) file(_ context.Context, repo, title, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, repo+"|"+title+"|"+body)
	if f.url != "" {
		return f.url, nil
	}
	return "https://github.com/Smana/a/issues/7", nil
}

// F3: issue_file files only what the developer accepted in the client's prompt, as the broker
// checked it, with no label; a client that cannot ask files nothing.
func TestIssueFile(t *testing.T) {
	args := map[string]any{"name": "issue_file", "arguments": map[string]any{"repo": "Smana/a", "title": "docs: a dead link", "body": "x"}}
	t.Run("a client that cannot ask", func(t *testing.T) {
		f := &filing{}
		s := relayTo(t, newMCPBroker(t), tok, withFile(f.file))
		s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{}})
		res := s.request(t, 2, "tools/call", args)
		if r := res["result"].(map[string]any); r["isError"] != true || !strings.Contains(fmt.Sprint(r["content"]), "needs_confirmation") || len(f.calls) != 0 {
			t.Fatalf("%v %v", res, f.calls)
		}
	})
	for action, filed := range map[string]bool{"accept": true, "decline": false, "cancel": false} {
		t.Run(action, func(t *testing.T) {
			f := &filing{}
			b := newMCPBroker(t)
			s := relayTo(t, b, tok, withFile(f.file))
			s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}})
			s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": args})
			ask := s.next(t)
			if ask["method"] != "elicitation/create" || !strings.Contains(fmt.Sprint(ask["params"]), "## What is wrong\nstripped") {
				t.Fatalf("the developer sees the broker's draft: %v", ask)
			}
			s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{"action": action, "content": map[string]any{"file": true}}})
			res := s.next(t)
			if res["id"] != 2.0 || strings.Contains(fmt.Sprint(res["result"]), `"filed":true`) != filed || len(f.calls) != map[bool]int{true: 1, false: 0}[filed] {
				t.Fatalf("%v %v", res, f.calls)
			}
			if filed && f.calls[0] != "Smana/a|docs: a dead link|## What is wrong\nstripped" {
				t.Fatalf("filed %q", f.calls[0])
			}
		})
	}
	t.Run("a draft the broker refuses", func(t *testing.T) {
		f := &filing{}
		b := newMCPBroker(t)
		b.draftErr = "invalid_draft: the title"
		s := relayTo(t, b, tok, withFile(f.file))
		s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}})
		res := s.request(t, 2, "tools/call", args)
		if !strings.Contains(fmt.Sprint(res["result"]), "invalid_draft: the title") || len(f.calls) != 0 {
			t.Fatalf("%v", res)
		}
	})
}

// GH files with the developer's gh: each value joined to its flag, the body on stdin, no label.
func TestGHFilesWithoutALabel(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(dir, "args") + "\ncat > " + filepath.Join(dir, "stdin") +
		"\necho https://github.com/Smana/a/issues/7\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	url, err := GH(t.Context(), "Smana/a", "docs: a dead link", "## What is wrong\nx")
	if err != nil || url != "https://github.com/Smana/a/issues/7" {
		t.Fatalf("%q %v", url, err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	stdin, _ := os.ReadFile(filepath.Join(dir, "stdin"))
	if string(args) != "issue\ncreate\n--repo=Smana/a\n--title=docs: a dead link\n--body-file=-\n" || string(stdin) != "## What is wrong\nx" {
		t.Fatalf("args %q stdin %q", args, stdin)
	}
}

// labeling records what the relay asked gh to label.
type labeling struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (l *labeling) label(_ context.Context, repo string, issue int, label string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, fmt.Sprintf("%s#%d+%s", repo, issue, label))
	return l.err
}

func boolp(b bool) *bool { return &b }

// startSession opens a relay whose client can ask, with the fake gh runners and a short room wait.
func startSession(t *testing.T, b *mcpBroker, f *filing, l *labeling, wait time.Duration) session {
	t.Helper()
	s := relayTo(t, b, tok, withFile(f.file), func(r *Relay) {
		r.Label, r.RoomWait = l.label, wait
		r.After = func(time.Duration) <-chan time.Time { return time.After(5 * time.Millisecond) }
	})
	s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}})
	return s
}

var issueFileArgs = map[string]any{"name": "issue_file", "arguments": map[string]any{"repo": "Smana/a", "title": "docs: a dead link", "body": "x"}}

func TestIssueFileSchemaHasNoStart(t *testing.T) {
	var sch struct {
		AdditionalProperties bool                       `json:"additionalProperties"`
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
	}
	if err := json.Unmarshal(issueFileEntry["inputSchema"].(json.RawMessage), &sch); err != nil {
		t.Fatal(err)
	}
	var names []string
	for k := range sch.Properties {
		names = append(names, k)
	}
	sort.Strings(names)
	if sch.AdditionalProperties || strings.Join(names, ",") != "body,hash,plan,repo,title" {
		t.Fatalf("start is a form field, never a tool argument: %v %v", names, sch.AdditionalProperties)
	}
	// The required list is what an older client and an older broker still send: the plan and its
	// hash are the caller's to add, never a field a call without one has to supply.
	if strings.Join(sch.Required, ",") != "repo,title,body" {
		t.Fatalf("a plan is optional: required %v", sch.Required)
	}
}

func TestIssueFileOffersStartOnlyWhenCanStart(t *testing.T) {
	for name, tc := range map[string]struct {
		canStart *bool
		offers   bool
	}{"can": {boolp(true), true}, "cannot": {boolp(false), false}, "older broker": {nil, false}} {
		t.Run(name, func(t *testing.T) {
			b := newMCPBroker(t)
			b.canStart, b.canStartWhy = tc.canStart, "your repository is not in sessionStart"
			s := startSession(t, b, &filing{}, &labeling{}, time.Second)
			s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": issueFileArgs})
			ask := s.next(t)
			p := ask["params"].(map[string]any)
			props := p["requestedSchema"].(map[string]any)["properties"].(map[string]any)
			_, has := props["start"]
			if has != tc.offers {
				t.Fatalf("start offered=%v: %v", has, props)
			}
			msg := p["message"].(string)
			if tc.offers {
				if d := props["start"].(map[string]any); d["type"] != "boolean" || d["default"] != false {
					t.Fatalf("%v", d)
				}
			} else if (tc.canStart != nil && !strings.Contains(msg, "your repository is not in sessionStart")) || !strings.Contains(msg, "factory/ready") {
				t.Fatalf("the message says why and how to start: %q", msg)
			}
			s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{"action": "cancel"}})
			s.next(t)
		})
	}
}

func TestIssueFileStarts(t *testing.T) {
	room := []map[string]any{{"id": "r1", "repository": "Smana/a", "issue": 7}, {"id": "other", "repository": "Smana/b", "issue": 7}}
	for name, tc := range map[string]struct {
		file, start bool
		canStart    bool
		wantFiled   bool
		wantLabel   bool
	}{
		"start":                    {true, true, true, true, true},
		"file only":                {true, false, true, true, false},
		"start the form never had": {true, true, false, true, false},
		"not filed":                {false, true, true, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			b := newMCPBroker(t)
			b.canStart, b.roomText, b.rooms = boolp(tc.canStart), "# card of r1", room
			f, l := &filing{}, &labeling{}
			s := startSession(t, b, f, l, time.Second)
			s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": issueFileArgs})
			ask := s.next(t)
			s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{"action": "accept", "content": map[string]any{"file": tc.file, "start": tc.start}}})
			res := s.next(t)["result"].(map[string]any)
			sc := res["structuredContent"].(map[string]any)
			if sc["filed"] != tc.wantFiled || (tc.wantFiled && sc["started"] != tc.wantLabel) {
				t.Fatalf("%v", sc)
			}
			want := []string(nil)
			if tc.wantLabel {
				want = []string{"Smana/a#7+factory/ready"}
			}
			if fmt.Sprint(l.calls) != fmt.Sprint(want) {
				t.Fatalf("gh label saw %v, want %v", l.calls, want)
			}
			if tc.wantLabel {
				text := res["content"].([]any)[0].(map[string]any)["text"].(string)
				if sc["room"] != "r1" || !strings.Contains(text, "# card of r1") {
					t.Fatalf("the card: %v %q", sc, text)
				}
			}
		})
	}
}

func TestIssueFileNoRoomWithin60s(t *testing.T) {
	b := newMCPBroker(t)
	b.canStart, b.rooms = boolp(true), []map[string]any{}
	l := &labeling{}
	s := startSession(t, b, &filing{}, l, 60*time.Millisecond)
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": issueFileArgs})
	ask := s.next(t)
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{"action": "accept", "content": map[string]any{"file": true, "start": true}}})
	sc := s.next(t)["result"].(map[string]any)["structuredContent"].(map[string]any)
	if sc["started"] != true || sc["url"] != "https://github.com/Smana/a/issues/7" || sc["why"] != "not started yet: the factory's comment on the issue says why" {
		t.Fatalf("%v", sc)
	}
}

// A gh failure files the issue without the label and says so; it does not retry.
func TestIssueFileLabelFailure(t *testing.T) {
	b := newMCPBroker(t)
	b.canStart = boolp(true)
	l := &labeling{err: errors.New("gh: boom")}
	s := startSession(t, b, &filing{}, l, time.Second)
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": issueFileArgs})
	ask := s.next(t)
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{"action": "accept", "content": map[string]any{"file": true, "start": true}}})
	sc := s.next(t)["result"].(map[string]any)["structuredContent"].(map[string]any)
	if sc["filed"] != true || sc["started"] != false || !strings.Contains(fmt.Sprint(sc["why"]), "without the label") || len(l.calls) != 1 {
		t.Fatalf("%v %v", sc, l.calls)
	}
}

func TestGHLabelArgs(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + filepath.Join(dir, "args") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := GHLabel(t.Context(), "Smana/a", 7, "factory/ready"); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args"))
	if string(args) != "issue\nedit\n7\n--repo=Smana/a\n--add-label=factory/ready\n" {
		t.Fatalf("%q", args)
	}
}

func TestIssueNumber(t *testing.T) {
	for url, want := range map[string]int{
		"https://github.com/Smana/a/issues/7":   7,
		"https://github.com/Smana/b/issues/7":   0,
		"https://github.com/Smana/a/pull/7":     0,
		"https://github.com/Smana/a/issues/7/x": 0,
		"https://github.com/Smana/a/issues/0":   0,
		"http://github.com/Smana/a/issues/7":    0,
		"":                                      0,
		"https://github.com/smana/A/issues/7":   7, // gh prints the canonical case
	} {
		n, err := issueNumber(url, "Smana/a")
		if (err == nil) != (want != 0) || n != want {
			t.Errorf("%q: %d %v", url, n, err)
		}
	}
}

// answer files the issue with the given form answer, and returns the result's structuredContent.
func fileWith(t *testing.T, s session, args map[string]any, answer map[string]any) map[string]any {
	t.Helper()
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": args})
	ask := s.next(t)
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": answer})
	sc, _ := s.next(t)["result"].(map[string]any)["structuredContent"].(map[string]any)
	return sc
}

func TestIssueFileStartRefusals(t *testing.T) {
	accept := func(content map[string]any) map[string]any {
		return map[string]any{"action": "accept", "content": content}
	}
	t.Run("no labeler: nothing starts", func(t *testing.T) {
		b := newMCPBroker(t)
		b.canStart = boolp(true)
		f := &filing{}
		s := relayTo(t, b, tok, withFile(f.file))
		s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}})
		s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": issueFileArgs})
		ask := s.next(t)
		if _, has := ask["params"].(map[string]any)["requestedSchema"].(map[string]any)["properties"].(map[string]any)["start"]; has {
			t.Fatal("start offered with no labeler")
		}
		s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": accept(map[string]any{"file": true, "start": true})})
		sc := s.next(t)["result"].(map[string]any)["structuredContent"].(map[string]any)
		if sc["filed"] != true || sc["started"] != false {
			t.Fatalf("%v", sc)
		}
	})
	t.Run("a URL that is not an issue of the repository", func(t *testing.T) {
		for _, url := range []string{"https://github.com/Smana/b/issues/7", "https://github.com/Smana/a/pull/7", "not a url"} {
			b := newMCPBroker(t)
			b.canStart = boolp(true)
			l := &labeling{}
			sc := fileWith(t, startSession(t, b, &filing{url: url}, l, time.Second), issueFileArgs, accept(map[string]any{"file": true, "start": true}))
			if sc["filed"] != true || sc["started"] != false || !strings.Contains(fmt.Sprint(sc["why"]), "without the label") || len(l.calls) != 0 {
				t.Fatalf("%q: %v %v", url, sc, l.calls)
			}
		}
	})
	t.Run("a declined or cancelled form files and labels nothing", func(t *testing.T) {
		for _, action := range []string{"decline", "cancel"} {
			b := newMCPBroker(t)
			b.canStart = boolp(true)
			f, l := &filing{}, &labeling{}
			sc := fileWith(t, startSession(t, b, f, l, time.Second), issueFileArgs, map[string]any{"action": action, "content": map[string]any{"file": true, "start": true}})
			if sc["filed"] != false || len(f.calls) != 0 || len(l.calls) != 0 {
				t.Fatalf("%s: %v %v %v", action, sc, f.calls, l.calls)
			}
		}
	})
	t.Run("a start the agent sends as an argument is not the developer's yes", func(t *testing.T) {
		b := newMCPBroker(t)
		b.canStart = boolp(true)
		l := &labeling{}
		args := map[string]any{"name": "issue_file", "arguments": map[string]any{"repo": "Smana/a", "title": "docs: a dead link", "body": "x", "start": true}}
		sc := fileWith(t, startSession(t, b, &filing{}, l, time.Second), args, accept(map[string]any{"file": true}))
		if sc["started"] != false || len(l.calls) != 0 {
			t.Fatalf("%v %v", sc, l.calls)
		}
	})
}

func TestGHCallsTimeOut(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := ghTimeout
	ghTimeout = 100 * time.Millisecond
	t.Cleanup(func() { ghTimeout = old })
	start := time.Now()
	if err := GHLabel(t.Context(), "Smana/a", 7, "factory/ready"); err == nil {
		t.Fatal("label")
	}
	if _, err := GH(t.Context(), "Smana/a", "t", "b"); err == nil {
		t.Fatal("file")
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("gh was not cut off")
	}
}

// The room's repository is compared as GitHub does, case-insensitively.
func TestIssueFileFindsTheRoomInAnotherCase(t *testing.T) {
	b := newMCPBroker(t)
	b.canStart, b.roomText = boolp(true), "# card"
	b.rooms = []map[string]any{{"id": "r1", "repository": "smana/A", "issue": 7}}
	sc := fileWith(t, startSession(t, b, &filing{}, &labeling{}, time.Second), issueFileArgs, map[string]any{"action": "accept", "content": map[string]any{"file": true, "start": true}})
	if sc["room"] != "r1" {
		t.Fatalf("%v", sc)
	}
}

// O4: Claude Code showed the required `file` toggle unset with "This field is required" until
// toggled, so the form was cancelled; the field now has a default (false: filing still needs an
// explicit yes).
func TestIssueFileFormDefaultsFileToFalse(t *testing.T) {
	b := newMCPBroker(t)
	b.canStart = boolp(true)
	s := startSession(t, b, &filing{}, &labeling{}, time.Second)
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": issueFileArgs})
	ask := s.next(t)
	props := ask["params"].(map[string]any)["requestedSchema"].(map[string]any)["properties"].(map[string]any)
	if d, ok := props["file"].(map[string]any); !ok || d["default"] != false || d["type"] != "boolean" {
		t.Fatalf("file: %v", props["file"])
	}
	s.send(t, map[string]any{"jsonrpc": "2.0", "id": ask["id"], "result": map[string]any{"action": "cancel"}})
	s.next(t)
}

// O4: a client may return a partial content object, or booleans as strings. A missing start is
// false, never a guess, and the result says start was not chosen, so the agent asks the developer.
func TestIssueFileLogsAMissingStart(t *testing.T) {
	b := newMCPBroker(t)
	b.canStart = boolp(true)
	var logs strings.Builder
	f := &filing{}
	s := relayTo(t, b, tok, withFile(f.file), func(r *Relay) {
		r.Label, r.Log = (&labeling{}).label, slog.New(slog.NewTextHandler(&logs, nil))
	})
	s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{"elicitation": map[string]any{}}})
	fileWith(t, s, issueFileArgs, map[string]any{"action": "accept", "content": map[string]any{"file": true}})
	if !strings.Contains(logs.String(), "no usable start") {
		t.Fatalf("logged %q", logs.String())
	}
}

func TestIssueFileStartNotChosen(t *testing.T) {
	for name, tc := range map[string]struct {
		content     map[string]any
		wantStarted bool
		wantNext    string
	}{
		"start missing":        {map[string]any{"file": true}, false, "start was not chosen"},
		"start null":           {map[string]any{"file": true, "start": nil}, false, "start was not chosen"},
		"start as a string":    {map[string]any{"file": true, "start": "true"}, true, ""},
		"file as a string":     {map[string]any{"file": "true", "start": false}, false, ""},
		"start explicit no":    {map[string]any{"file": true, "start": false}, false, ""},
		"start unintelligible": {map[string]any{"file": true, "start": "maybe"}, false, "start was not chosen"},
	} {
		t.Run(name, func(t *testing.T) {
			b := newMCPBroker(t)
			b.canStart, b.roomText, b.rooms = boolp(true), "# card", []map[string]any{{"id": "r1", "repository": "Smana/a", "issue": 7}}
			l := &labeling{}
			sc := fileWith(t, startSession(t, b, &filing{}, l, time.Second), issueFileArgs, map[string]any{"action": "accept", "content": tc.content})
			if sc["filed"] != true || sc["started"] != tc.wantStarted {
				t.Fatalf("%v", sc)
			}
			next, _ := sc["next"].(string)
			if tc.wantNext != "" && !strings.Contains(next, tc.wantNext) || tc.wantNext == "" && strings.Contains(next, "not chosen") {
				t.Fatalf("next %q", next)
			}
			if tc.wantStarted != (len(l.calls) == 1) {
				t.Fatalf("labels %v", l.calls)
			}
		})
	}
}

// O10: OpenCode cut issue_file's form short ("context deadline exceeded"). When the form cannot be
// shown or is not answered, the result holds ONE command that files the issue with gh and never labels
// it: starting the factory stays the developer's own act, whatever the draft could do.
func TestIssueFileFallbackCommand(t *testing.T) {
	check := func(t *testing.T, res map[string]any, _ bool) {
		t.Helper()
		text := fmt.Sprint(res["result"])
		for _, want := range []string{"next:", "gh issue create", "--repo=Smana/a", "docs: a dead link", "--body-file=-"} {
			if !strings.Contains(text, want) {
				t.Fatalf("no %q in %s", want, text)
			}
		}
		if strings.Contains(text, "--add-label") || strings.Contains(text, "gh issue edit") {
			t.Fatalf("the fallback starts the factory: %s", text)
		}
		if !strings.Contains(text, "starts the factory") || !strings.Contains(text, "factory/ready") {
			t.Fatalf("it does not say how the developer starts it: %s", text)
		}
	}
	t.Run("a client that cannot ask", func(t *testing.T) {
		s := relayTo(t, newMCPBroker(t), tok, withFile((&filing{}).file))
		s.request(t, 1, "initialize", map[string]any{"capabilities": map[string]any{}})
		check(t, s.request(t, 2, "tools/call", issueFileArgs), false)
	})
	t.Run("a form that is not answered in time", func(t *testing.T) {
		old := elicitWait
		elicitWait = 50 * time.Millisecond
		t.Cleanup(func() { elicitWait = old })
		b := newMCPBroker(t)
		b.canStart = boolp(true)
		s := startSession(t, b, &filing{}, &labeling{}, time.Second)
		s.send(t, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": issueFileArgs})
		var res map[string]any
		for { // the form, its cancellation, then the result
			m := s.next(t)
			if m["id"] == 2.0 {
				res = m
				break
			}
		}
		check(t, res, true)
	})
}

// The command quotes what the draft holds: a title with a quote, a body that holds the heredoc's end.
func TestFallbackCommandQuoting(t *testing.T) {
	cmd := fallbackCommand(draftShape{Repo: "Smana/a", Title: "fix: it's $(bad)", Body: "x\nBODY_EOF\ny"})
	if !strings.Contains(cmd, `--title='fix: it'\''s $(bad)'`) || strings.Count(cmd, "\nBODY_EOF\n") != 1 && !strings.Contains(cmd, "BODY_EOF_1") {
		t.Fatalf("%s", cmd)
	}
	if strings.Contains(cmd, "--add-label") {
		t.Fatalf("filed only: %s", cmd)
	}
}

// fakeBrokerServer is the broker's /mcp as issue_file calls it: the plan_check answers are the
// results queued here, in order (the last one repeats), and every other tool answers as an older
// broker would — with an issue_draft that is not the plan's, which a governed call must never show.
type fakeBrokerServer struct {
	srv         *httptest.Server
	mu          sync.Mutex
	calls       []string // the tools called, in order
	checks      int      // the plan_check calls
	planResults []string
}

func fakeBroker(t *testing.T, planCheckResults ...string) *fakeBrokerServer {
	t.Helper()
	b := &fakeBrokerServer{planResults: planCheckResults}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m message
		if json.NewDecoder(r.Body).Decode(&m) != nil || len(m.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var c struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(m.Params, &c)
		b.mu.Lock()
		b.calls = append(b.calls, c.Name)
		var res any
		switch c.Name {
		case mcpwire.PlanCheck:
			b.checks++
			if n := min(b.checks, len(b.planResults)); n > 0 {
				res = json.RawMessage(b.planResults[n-1])
			}
		case "issue_draft":
			res = map[string]any{"content": []any{}, "structuredContent": map[string]any{
				"repo": "Smana/other", "title": "not the plan's", "body": "x", "canStart": false}}
		case "rooms_list":
			res = map[string]any{"content": []any{}, "structuredContent": map[string]any{"rooms": []any{}}}
		}
		b.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": res})
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *fakeBrokerServer) planChecks() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.checks
}

// recording is the relay issue_file runs on when the test calls it directly, and the developer on
// the other side of its form: it counts the forms shown and the gh filings, and keeps the last of
// each. The relay's own requests are answered inline, so the call stays on one goroutine.
type recording struct {
	*Relay
	t      *testing.T
	b      *fakeBrokerServer
	filed  int            // the gh filings
	what   string         // the last draft handed to gh, as gh's three arguments
	asked  int            // the forms shown
	form   map[string]any // the last form's params
	answer map[string]any // the developer's answer; accept and file, by default
}

// relay is a relay whose broker is b and whose developer answers every form: issue_file is called
// directly, so a test can read the form and count the filings.
func relay(t *testing.T, b *fakeBrokerServer) *recording {
	t.Helper()
	rec := &recording{t: t, b: b, answer: map[string]any{"action": "accept", "content": map[string]any{"file": true}}}
	rec.Relay = &Relay{URL: b.srv.URL, HC: b.srv.Client(), Token: tok, RoomWait: 100 * time.Millisecond,
		After: func(time.Duration) <-chan time.Time { c := make(chan time.Time, 1); c <- time.Now(); return c }}
	rec.File = func(_ context.Context, repo, title, body string) (string, error) {
		rec.filed++
		rec.what = repo + "|" + title + "|" + body
		return "https://github.com/" + repo + "/issues/7", nil
	}
	rec.Label = func(context.Context, string, int, string) error { return nil }
	rec.forms, rec.waiting = true, map[string]chan json.RawMessage{}
	rec.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	rec.out = writerFunc(rec.client)
	return rec
}

// lastForm is the form the relay showed, as the client received it.
func (r *recording) lastForm() map[string]any { return r.form }

// writerFunc is the relay's client half: it reads what the relay writes to its client.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// client is the other end of the relay's stdout: it records a form and answers it as the developer
// would, and drops the relay's other lines (a cancellation, if a form went unanswered).
func (r *recording) client(line []byte) (int, error) {
	var m message
	if json.Unmarshal(line, &m) == nil && m.Method == "elicitation/create" {
		var p map[string]any
		_ = json.Unmarshal(m.Params, &p)
		r.asked, r.form = r.asked+1, p
		raw, err := json.Marshal(r.answer)
		if err != nil {
			r.t.Fatal(err)
		}
		r.answered(message{ID: m.ID, Result: raw})
	}
	return len(line), nil
}

// planFixture is a golden plan/v1, decoded through encoding/json from the planner package's own
// testdata: the plan these tests hand issue_file is the plan plan_check's own tests use, so a
// change to plan/v1 breaks one place.
func planFixture(t *testing.T, name string) intake.Plan {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "intake", "testdata", "plan", name+".json"))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var p intake.Plan
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return p
}

// checked is plan_check's structuredContent for the fixture: the plan it validated, the hash it
// recomputed over it, and whether the developer may start the factory on it.
func checked(t *testing.T, name string) string {
	t.Helper()
	return checkedStart(t, name, true, "")
}

// checkedStart is that same structuredContent with the canStart the broker answered: false carries
// the broker's own why, which the form repeats to the developer.
func checkedStart(t *testing.T, name string, canStart bool, why string) string {
	t.Helper()
	p := planFixture(t, name)
	d := map[string]any{"plan": p, "hash": intake.Hash(p), "canStart": canStart}
	if why != "" {
		d["canStartWhy"] = why
	}
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// planCheckAccepted is the broker accepting a plan_check: the whole tools/call result, isError false.
func planCheckAccepted(t *testing.T) string {
	return planCheckResult(checked(t, "single"))
}

// planCheckCannotStart is the same acceptance where the developer may not start the factory, for the
// reason the broker gives.
func planCheckCannotStart(t *testing.T, why string) string {
	return planCheckResult(checkedStart(t, "single", false, why))
}

// planCheckResult is a plan_check tools/call result around structuredContent, as the broker sends it.
func planCheckResult(structured string) string {
	return `{"isError":false,"content":[{"type":"text","text":"plan"}],"structuredContent":` + structured + `}`
}

// planCheckRefused is the broker refusing one: the whole tools/call result, isError true, whose text
// is the refusal the model reads.
func planCheckRefused(text string) string {
	b, _ := json.Marshal(map[string]any{"isError": true, "content": []map[string]string{{"type": "text", "text": text}}})
	return string(b)
}

// planCall is issue_file's arguments for p, carrying hash as the hash the caller holds: the plan's
// own repository and first issue, so the call is one a plan can govern, plus anything in extra.
func planCall(t *testing.T, p intake.Plan, hash string, extra map[string]any) json.RawMessage {
	t.Helper()
	a := map[string]any{"repo": p.Repository, "title": p.Issues[0].Title, "body": p.Issues[0].Body, "plan": p, "hash": hash}
	for k, v := range extra {
		a[k] = v
	}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// planArgs is a call governed by the plan intake_plan answered, with the hash it answered with.
func planArgs(t *testing.T) json.RawMessage {
	p := planFixture(t, "single")
	return planCall(t, p, intake.Hash(p), nil)
}

// planArgsChanged is that plan edited after it was shown, with the hash of the plan that was shown:
// the shape that must be refused plan_changed.
func planArgsChanged(t *testing.T) json.RawMessage {
	p := planFixture(t, "single")
	hash := intake.Hash(p)
	p.Issues[0].Body += "\n- one line more, added after the developer was shown the plan\n"
	return planCall(t, p, hash, nil)
}

// Offline proof 4, second half: issue_file re-validates through the broker. A plan whose hash no
// longer holds files nothing and shows no form.
func TestIssueFileRefusesAChangedPlan(t *testing.T) {
	r := relay(t, fakeBroker(t, planCheckRefused("plan_changed: the plan is not the one that was shown")))
	got := r.issueFile(t.Context(), planArgsChanged(t))
	if !strings.Contains(fmt.Sprint(got), "plan_changed") || r.filed != 0 || r.asked != 0 {
		t.Fatalf("%v filed=%d asked=%d", got, r.filed, r.asked)
	}
	if r.b.planChecks() != 1 {
		t.Fatalf("plan_check was called %d times", r.b.planChecks())
	}
}

// A plan the broker accepts is filed as it was validated, and the form shows the repository, the
// issue and the ceiling — and the start field stays what FA4 made it.
func TestIssueFileShowsThePlansCeilingAndKeepsTheStartField(t *testing.T) {
	r := relay(t, fakeBroker(t, planCheckAccepted(t)))
	got := r.issueFile(t.Context(), planArgs(t))
	msg := fmt.Sprint(r.lastForm())
	for _, want := range []string{"Smana/ai-native-ref", "$2.00 ceiling", "Start the factory now", "File this issue"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the form lacks %q: %s", want, msg)
		}
	}
	if r.filed != 1 || !strings.Contains(fmt.Sprint(got), "filed") {
		t.Fatalf("%v filed=%d", got, r.filed)
	}
}

// The plan wins over the arguments: what is filed is the issue plan_check validated, never a second
// copy of the text the caller sent beside it.
func TestIssueFileFilesThePlansIssue(t *testing.T) {
	r := relay(t, fakeBroker(t, planCheckAccepted(t)))
	args := planCall(t, planFixture(t, "single"), intake.Hash(planFixture(t, "single")), map[string]any{
		"repo": "Smana/elsewhere", "title": "docs: not the plan's issue", "body": "x"})
	r.issueFile(t.Context(), args)
	p := planFixture(t, "single")
	if want := p.Repository + "|" + p.Issues[0].Title + "|" + p.Issues[0].Body; r.what != want {
		t.Fatalf("filed %q, want %q", r.what, want)
	}
	if !strings.Contains(fmt.Sprint(r.lastForm()), p.Issues[0].Title) {
		t.Fatalf("the form does not show the plan's issue: %v", r.lastForm())
	}
}

// Without a plan the tool is exactly today's tool: no plan_check call, no ceiling line.
func TestIssueFileWithoutAPlanIsUnchanged(t *testing.T) {
	r := relay(t, fakeBroker(t))
	got := r.issueFile(t.Context(), mustArgs(t, map[string]any{"repo": "Smana/a", "title": "docs: a dead link", "body": "x"}))
	msg := fmt.Sprint(r.lastForm())
	if r.b.planChecks() != 0 || r.asked != 1 || r.filed != 1 {
		t.Fatalf("planChecks=%d asked=%d filed=%d", r.b.planChecks(), r.asked, r.filed)
	}
	if strings.Contains(msg, "ceiling") {
		t.Fatalf("a call with no plan says nothing about money: %s", msg)
	}
	// The draft is still the broker's issue_draft, which is what an older broker answers with.
	if !strings.Contains(msg, "not the plan's") || r.what != "Smana/other|not the plan's|x" {
		t.Fatalf("the form %s, filed %q", msg, r.what)
	}
	if !strings.Contains(fmt.Sprint(got), "filed") {
		t.Fatalf("%v", got)
	}
}

// mustArgs marshals a tool call's arguments.
func mustArgs(t *testing.T, a map[string]any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// An older broker serves no plan_check: a call that names a plan is refused saying so — the tool's
// own name, from the broker's JSON-RPC error — and nothing is shown or filed.
func TestIssueFileRefusesAPlanOnABrokerWithoutThePlanner(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m message
		_ = json.NewDecoder(r.Body).Decode(&m)
		if len(m.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID,
			"error": map[string]any{"code": mcpwire.InvalidParams, "message": "unknown tool: plan_check"}})
	}))
	t.Cleanup(srv.Close)
	r := relay(t, &fakeBrokerServer{srv: srv})
	got := r.issueFile(t.Context(), planArgs(t))
	if !strings.Contains(fmt.Sprint(got), "unknown tool: plan_check") || r.filed != 0 || r.asked != 0 {
		t.Fatalf("%v filed=%d asked=%d", got, r.filed, r.asked)
	}
}

// FA4: a plan this developer may not start still files, the form offers no start field at all, and
// plan_check's own why survives into the message the developer reads.
func TestIssueFileOffersNoStartOnAPlanItCannotStart(t *testing.T) {
	r := relay(t, fakeBroker(t, planCheckCannotStart(t, "your repository is not in sessionStart")))
	got := r.issueFile(t.Context(), planArgs(t))
	form := r.lastForm()
	props := form["requestedSchema"].(map[string]any)["properties"].(map[string]any)
	if _, has := props["start"]; has {
		t.Fatalf("start offered on a plan canStart says no: %v", props)
	}
	msg := fmt.Sprint(form)
	if !strings.Contains(msg, "your repository is not in sessionStart") || !strings.Contains(msg, "factory/ready") {
		t.Fatalf("the form says neither why nor how the developer starts it: %s", msg)
	}
	if r.filed != 1 || !strings.Contains(fmt.Sprint(got), "filed") || strings.Contains(fmt.Sprint(got), `"started":true`) {
		t.Fatalf("%v filed=%d", got, r.filed)
	}
}
