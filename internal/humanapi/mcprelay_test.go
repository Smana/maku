// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/maku"
	"github.com/Smana/maku/internal/store"
)

// bearerAuth is headerAuth for maku's bearer: "Bearer <user>" is <user> through maku's client.
type bearerAuth struct{}

func (bearerAuth) Authenticate(r *http.Request) (authn.Principal, error) {
	who, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	c := r.Clone(r.Context())
	c.Header = header(who, "X-Test-Client", "maku")
	return headerAuth{}.Authenticate(c)
}

// The design's live proof 1, offline: an agent on maku mcp lists the six tools, queues a note via
// answer (only after the developer accepts), tails as a task, and files a handoff via start only
// once the developer accepts it.
func TestMakuMCPAgainstTheBroker(t *testing.T) {
	l := &actLog{st: store.RoomState{ID: roomID, Driver: "system:factory", DriverEpoch: 3}}
	hs := newMemHandoffStore()
	e := setup(t, mcpOn, actorOn(l), withIntake(t, "session"), func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.Humans = bearerAuth{}
		s.Handoffs = hs
	})
	var mu sync.Mutex
	var filed []string
	polled := make(chan struct{}, 8)
	relay := &maku.Relay{URL: e.ts.URL, HC: e.ts.Client(),
		Token: func(context.Context) (string, error) { return "own", nil },
		File: func(_ context.Context, repo, title, body string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			filed = append(filed, repo+"|"+title+"|"+body)
			return "https://github.com/Smana/ai-native-ref/issues/7", nil
		},
		Label: func(_ context.Context, _ string, _ int, _ string) error { return nil },
		After: func(time.Duration) <-chan time.Time {
			polled <- struct{}{}
			c := make(chan time.Time, 1)
			c <- time.Now()
			return c
		}}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- relay.Serve(t.Context(), inR, outW); _ = outW.Close() }()
	out := bufio.NewScanner(outR)
	send := func(v any) {
		b, _ := json.Marshal(v)
		if _, err := inW.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	next := func() map[string]any {
		if !out.Scan() {
			t.Fatal("the relay closed")
		}
		var m map[string]any
		_ = json.Unmarshal(out.Bytes(), &m)
		return m
	}
	ask := func(id int, method string, params any) map[string]any {
		send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		return next()
	}

	if res := ask(1, "initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{"elicitation": map[string]any{}}}); !strings.Contains(fmt.Sprint(res), "tasks") {
		t.Fatalf("initialize: %v", res)
	}
	send(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	var names []string
	for _, tl := range ask(2, "tools/list", nil)["result"].(map[string]any)["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "handoff,start,inbox,task,wait,answer,cancel" {
		t.Fatalf("the default list is the six, then cancel, in order: %v", names)
	}

	send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": map[string]any{"name": "answer", "arguments": map[string]any{"task": roomID, "text": "use v2"}}})
	elNote := next()
	if elNote["method"] != "elicitation/create" {
		t.Fatalf("answer elicitation: %v", elNote)
	}
	send(map[string]any{"jsonrpc": "2.0", "id": elNote["id"], "result": map[string]any{"action": "accept", "content": map[string]any{"send": true}}})
	if res := next(); strings.Contains(fmt.Sprint(res), "isError:true") {
		t.Fatalf("answer: %v", res)
	}
	if len(l.queue) != 1 || !strings.HasPrefix(l.queue[0].Text, "⟦machine-sent via mcp") {
		t.Fatalf("queued %+v", l.queue)
	}

	created := ask(4, "tools/call", map[string]any{"name": "room_tail", "arguments": map[string]any{"room": roomID}, "task": map[string]any{"ttl": 60000}})
	id := created["result"].(map[string]any)["task"].(map[string]any)["taskId"]
	send(map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tasks/result", "params": map[string]any{"taskId": id}})
	<-polled     // the relay saw the task working and waits
	e.log.add(1) // the room moves
	if res := next(); res["id"] != 5.0 || !strings.Contains(fmt.Sprint(res["result"]), "seq:11") {
		t.Fatalf("tasks/result: %v", res)
	}

	ho := ask(6, "tools/call", map[string]any{"name": "handoff", "arguments": map[string]any{"text": "fix the broken install link in README.md"}})
	if ho["result"].(map[string]any)["structuredContent"] == nil {
		t.Fatalf("handoff: %v", ho)
	}
	hoID := ho["result"].(map[string]any)["structuredContent"].(map[string]any)["id"].(string)
	send(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call", "params": map[string]any{"name": "start",
		"arguments": map[string]any{"id": hoID}}})
	el := next()
	if el["method"] != "elicitation/create" {
		t.Fatalf("%v", el)
	}
	send(map[string]any{"jsonrpc": "2.0", "id": el["id"], "result": map[string]any{"action": "accept", "content": map[string]any{"file": true}}})
	if res := next(); !strings.Contains(fmt.Sprint(res["result"]), "issues/7") {
		t.Fatalf("start: %v", res)
	}
	mu.Lock()
	if len(filed) != 1 || !strings.HasPrefix(filed[0], "smana/ai-native-ref|") {
		t.Fatalf("filed %q", filed)
	}
	mu.Unlock()
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
