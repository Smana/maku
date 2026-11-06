// SPDX-License-Identifier: Apache-2.0

package slackapp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
)

func slackServer(t *testing.T, handle func(method string, r *http.Request, body []byte) string) *SlackClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Errorf("authorization %q", r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, handle(r.URL.Path, r, body))
	}))
	t.Cleanup(srv.Close)
	return &SlackClient{BaseURL: srv.URL + "/api/", Token: func() string { return "xoxb-test" }, HC: srv.Client()}
}

// R22/R23: Post sends chat.postMessage with the metadata and no unfurling, and returns ts.
func TestSlackPost(t *testing.T) {
	var got map[string]any
	c := slackServer(t, func(path string, _ *http.Request, body []byte) string {
		if path != "/api/chat.postMessage" {
			t.Errorf("path %s", path)
		}
		_ = json.Unmarshal(body, &got)
		return `{"ok":true,"ts":"1729.0002"}`
	})
	meta := metaOf(t, draftEventType, DraftPayload{Repo: "a/b", Title: "t", Body: "b", SHA256: "h"})
	ts, err := c.Post(context.Background(), "C03", "1729.0001", "hello", meta)
	if err != nil || ts != "1729.0002" {
		t.Fatalf("%q %v", ts, err)
	}
	if got["channel"] != "C03" || got["thread_ts"] != "1729.0001" || got["text"] != "hello" ||
		got["unfurl_links"] != false || got["unfurl_media"] != false {
		t.Fatalf("%+v", got)
	}
	m, _ := got["metadata"].(map[string]any)
	p, _ := m["event_payload"].(map[string]any)
	if m["event_type"] != "factory_draft" || p["repo"] != "a/b" || p["sha256"] != "h" {
		t.Fatalf("metadata %+v", got["metadata"])
	}
}

// M9: Message reads conversations.replies with ts=item.ts and keeps the message whose ts is the
// one asked; the thread's parent (also in a reply answer) is never taken for it.
func TestSlackMessage(t *testing.T) {
	var q url.Values
	c := slackServer(t, func(path string, r *http.Request, _ []byte) string {
		if path != "/api/conversations.replies" {
			t.Errorf("path %s", path)
		}
		q = r.URL.Query()
		return `{"ok":true,"messages":[
		 {"ts":"1729.0001","user":"U02DEF","text":"parent"},
		 {"ts":"1729.0002","thread_ts":"1729.0001","user":"U0BOT","text":"draft",
		  "metadata":{"event_type":"factory_draft","event_payload":{"repo":"a/b","title":"t","body":"b","sha256":"h"}}}]}`
	})
	m, err := c.Message(context.Background(), "C03", "1729.0002")
	if err != nil {
		t.Fatal(err)
	}
	if q.Get("channel") != "C03" || q.Get("ts") != "1729.0002" || q.Get("limit") != "1" ||
		q.Get("inclusive") != "true" || q.Get("include_all_metadata") != "true" {
		t.Fatalf("query %v", q)
	}
	if m.TS != "1729.0002" || m.ThreadTS != "1729.0001" || m.User != "U0BOT" || m.Metadata == nil {
		t.Fatalf("%+v", m)
	}
	if p, ok := m.Metadata.draftPayload(); !ok || m.Metadata.EventType != "factory_draft" || p.SHA256 != "h" {
		t.Fatalf("%+v", m)
	}
	if _, err := c.Message(context.Background(), "C03", "9999.0000"); err == nil {
		t.Fatal("a message that is not in the answer was invented")
	}
}

func TestSlackNotOKIsError(t *testing.T) {
	c := slackServer(t, func(string, *http.Request, []byte) string { return `{"ok":false,"error":"channel_not_found"}` })
	if _, err := c.Post(context.Background(), "C03", "", "x", nil); err == nil {
		t.Fatal("Post: ok:false accepted")
	}
	if _, err := c.Message(context.Background(), "C03", "1.0"); err == nil {
		t.Fatal("Message: ok:false accepted")
	}
}

func TestSlackThreadFollowsTheCursor(t *testing.T) {
	var cursors []string
	c := slackServer(t, func(_ string, r *http.Request, _ []byte) string {
		cur := r.URL.Query().Get("cursor")
		cursors = append(cursors, cur)
		if cur == "" {
			return `{"ok":true,"messages":[{"ts":"1729.0001","user":"U02DEF","text":"parent"}],"response_metadata":{"next_cursor":"c2"}}`
		}
		return `{"ok":true,"messages":[{"ts":"1729.0002","thread_ts":"1729.0001","user":"U0BOT","text":"d",
		 "metadata":{"event_type":"factory_draft","event_payload":{"repo":"a/b","author":"U02DEF","rev":2}}}]}`
	})
	msgs, err := c.Thread(context.Background(), "C03", "1729.0001")
	if err != nil || len(msgs) != 2 || len(cursors) != 2 || cursors[1] != "c2" || msgs[1].Metadata == nil {
		t.Fatalf("%v %v %+v", err, cursors, msgs)
	}
	if p, ok := msgs[1].Metadata.draftPayload(); !ok || p.Author != "U02DEF" || p.Rev != 2 {
		t.Fatalf("%+v", msgs[1].Metadata)
	}
}

func TestSlackThreadPastTheLastPageFailsClosed(t *testing.T) {
	c := slackServer(t, func(string, *http.Request, []byte) string {
		return `{"ok":true,"messages":[{"ts":"1729.0001","user":"U02DEF","text":"p"}],"response_metadata":{"next_cursor":"more"}}`
	})
	if _, err := c.Thread(context.Background(), "C03", "1729.0001"); !errors.Is(err, ErrThreadTooLong) {
		t.Fatalf("%v", err)
	}
}

// The link's two Slack calls: chat.postEphemeral to the one user, and users.info's display name,
// else its real name; missing_scope is an error the App shrugs off.
func TestSlackEphemeralAndUserName(t *testing.T) {
	var got map[string]any
	answer := `{"ok":true,"user":{"name":"dana.h","profile":{"display_name":"","real_name":"Dana H"}}}`
	c := slackServer(t, func(path string, r *http.Request, body []byte) string {
		switch path {
		case "/api/chat.postEphemeral":
			_ = json.Unmarshal(body, &got)
			return `{"ok":true,"message_ts":"1729.0003"}`
		case "/api/users.info":
			if r.URL.Query().Get("user") != "U02DEF" {
				t.Errorf("users.info %s", r.URL.RawQuery)
			}
			return answer
		}
		t.Errorf("path %s", path)
		return `{"ok":false}`
	})
	if err := c.PostEphemeral(context.Background(), "C03", "U02DEF", "1729.0001", "hi"); err != nil {
		t.Fatal(err)
	}
	if got["channel"] != "C03" || got["user"] != "U02DEF" || got["thread_ts"] != "1729.0001" || got["text"] != "hi" {
		t.Fatalf("%+v", got)
	}
	if h, n, err := c.UserName(context.Background(), "U02DEF"); err != nil || h != "dana.h" || n != "Dana H" {
		t.Fatalf("%q %q %v", h, n, err)
	}
	answer = `{"ok":false,"error":"missing_scope"}`
	if _, n, err := c.UserName(context.Background(), "U02DEF"); err == nil || n != "" {
		t.Fatalf("missing_scope: %q %v", n, err)
	}
}

// §6: chat.update rewrites the App's own message in place; chat.postMessage with blocks posts the
// card. chat:write already covers both, and no metadata is touched by an update. slackServer fails
// the test itself when a call does not carry "Authorization: Bearer <Token()>".
func TestUpdateAndCardWire(t *testing.T) {
	// The block set PR4 will draw, minus the elements this test does not read.
	const card = `[{"type":"section","text":{"type":"mrkdwn","text":"If you approve: the push goes out."}},` +
		`{"type":"actions","elements":[{"type":"button","action_id":"factory_approval_approve","text":{"type":"plain_text","text":"Approve"},"value":"v1|forge.push|01J0|agent/x"}]}]`

	// body records one call: its path and the body's top-level fields, raw so a subtest compares
	// the exact bytes Slack receives.
	body := func(t *testing.T, path *string, got *map[string]json.RawMessage) func(string, *http.Request, []byte) string {
		t.Helper()
		return func(p string, _ *http.Request, raw []byte) string {
			*path = p
			if err := json.Unmarshal(raw, got); err != nil {
				t.Errorf("%s: body %q: %v", p, raw, err)
			}
			return `{"ok":true,"ts":"1729.0002"}`
		}
	}
	// keys is the body's top-level fields, sorted: a map marshals them in that order, so the sorted
	// list is also the wire's key order.
	keys := func(got map[string]json.RawMessage) string {
		out := make([]string, 0, len(got))
		for k := range got {
			out = append(out, k)
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	fields := func(t *testing.T, got map[string]json.RawMessage, want map[string]string) {
		t.Helper()
		for k, w := range want {
			if string(got[k]) != w {
				t.Errorf("%s = %s, want %s", k, got[k], w)
			}
		}
	}

	t.Run("chat.update, in place and with no metadata", func(t *testing.T) {
		var path string
		var got map[string]json.RawMessage
		c := slackServer(t, body(t, &path, &got))
		if err := c.Update(context.Background(), "C03", "1729.0001", "Approval needed", json.RawMessage(card)); err != nil {
			t.Fatal(err)
		}
		if path != "/api/chat.update" {
			t.Fatalf("path %s", path)
		}
		if have, want := keys(got), "blocks,channel,text,ts,unfurl_links,unfurl_media"; have != want {
			t.Fatalf("fields %s, want %s", have, want)
		}
		fields(t, got, map[string]string{
			"channel": `"C03"`, "ts": `"1729.0001"`, "text": `"Approval needed"`,
			"blocks": card, "unfurl_links": "false", "unfurl_media": "false",
		})
	})

	t.Run("PostCard posts the card with its metadata", func(t *testing.T) {
		var path string
		var got map[string]json.RawMessage
		c := slackServer(t, body(t, &path, &got))
		meta := metaOf(t, approvalEventType, ApprovalPayload{ApprovalID: "01J0", Class: "forge.push", Branch: "agent/x"})
		ts, err := c.PostCard(context.Background(), "C03", "1729.0001", "Approval needed", json.RawMessage(card), meta)
		if err != nil || ts != "1729.0002" {
			t.Fatalf("%q %v", ts, err)
		}
		if path != "/api/chat.postMessage" {
			t.Fatalf("path %s", path)
		}
		if have, want := keys(got), "blocks,channel,metadata,text,thread_ts,unfurl_links,unfurl_media"; have != want {
			t.Fatalf("fields %s, want %s", have, want)
		}
		fields(t, got, map[string]string{
			"channel": `"C03"`, "thread_ts": `"1729.0001"`, "text": `"Approval needed"`,
			"blocks": card, "unfurl_links": "false", "unfurl_media": "false",
			"metadata": `{"event_type":"factory_approval","event_payload":{"approvalId":"01J0","class":"forge.push","branch":"agent/x"}}`,
		})
	})

	// A card's prose is mrkdwn carrying <@user> mentions and & in room names. encoding/json escapes
	// both, and Slack reads them back as the same characters: the bytes below are the wire's.
	t.Run("HTML-significant text reaches the wire escaped", func(t *testing.T) {
		text := `<@U02DEF> & co`
		blocks := json.RawMessage(`[{"type":"section","text":{"type":"mrkdwn","text":"<@U02DEF> & the room"}}]`)
		const (
			wantText   = `"\u003c@U02DEF\u003e \u0026 co"`
			wantBlocks = `[{"type":"section","text":{"type":"mrkdwn","text":"\u003c@U02DEF\u003e \u0026 the room"}}]`
		)
		var path string
		var got map[string]json.RawMessage
		c := slackServer(t, body(t, &path, &got))
		if _, err := c.PostCard(context.Background(), "C03", "", text, blocks, nil); err != nil {
			t.Fatal(err)
		}
		fields(t, got, map[string]string{"text": wantText, "blocks": wantBlocks})
		if _, ok := got["thread_ts"]; ok {
			t.Fatal("chat.postMessage carried a thread_ts for a card with no thread")
		}
		got = nil
		if err := c.Update(context.Background(), "C03", "1729.0001", text, blocks); err != nil {
			t.Fatal(err)
		}
		fields(t, got, map[string]string{"text": wantText, "blocks": wantBlocks})
	})

	// A non-ok answer is Slack's own code: never the token, never a body echoed back.
	t.Run("a refused update is Slack's code, never the token", func(t *testing.T) {
		c := slackServer(t, func(string, *http.Request, []byte) string {
			return `{"ok":false,"error":"cant_update_message"}`
		})
		err := c.Update(context.Background(), "C03", "1729.0001", "Approval needed", json.RawMessage(card))
		if err == nil || !strings.Contains(err.Error(), "cant_update_message") || strings.Contains(err.Error(), "xoxb-test") {
			t.Fatalf("%v", err)
		}
	})
}

// A payload that cannot marshal is an error, not the empty payload every decoder would refuse
// quietly: the caller that can say so must (Task 10's carry-forward).
func TestMetaPayloadRefusesWhatItCannotMarshal(t *testing.T) {
	if _, err := metaPayload(math.Inf(1)); err == nil {
		t.Fatal("an unmarshalable payload was accepted")
	}
	if raw, err := metaPayload(DraftPayload{Repo: "a/b"}); err != nil || string(raw) != `{"repo":"a/b","title":"","body":"","sha256":""}` {
		t.Fatalf("%s %v", raw, err)
	}
}

// CD20: chat.update carries the metadata, so an edit never reposts and a restart never re-reads the
// text. The blocks go only when there are some, and a refused update is Slack's code — never the
// token.
func TestUpdateProposalWire(t *testing.T) {
	const blocks = `{"blocks":[{"type":"section","text":{"type":"mrkdwn","text":"Proposal v2"}}]}`
	meta := &Metadata{EventType: proposalEventType,
		EventPayload: json.RawMessage(`{"plan":{},"hash":"sha256:1111111111111111111111111111111111111111111111111111111111111111","author":"U07JY4EQNDN","rev":2}`)}
	const wantMeta = `{"event_type":"factory_proposal","event_payload":{"plan":{},` +
		`"hash":"sha256:1111111111111111111111111111111111111111111111111111111111111111",` +
		`"author":"U07JY4EQNDN","rev":2}}`

	t.Run("chat.update with the metadata", func(t *testing.T) {
		var path string
		var got map[string]json.RawMessage
		// slackServer fails the test itself when a call does not carry "Authorization: Bearer
		// <Token()>", so the answer's ts is unused here.
		c := slackServer(t, func(p string, _ *http.Request, raw []byte) string {
			path = p
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Errorf("%s: body %q: %v", p, raw, err)
			}
			return `{"ok":true,"ts":"1729.0001"}`
		})
		if err := c.UpdateProposal(context.Background(), "C03", "1729.0001", "text", json.RawMessage(blocks), meta); err != nil {
			t.Fatal(err)
		}
		if path != "/api/chat.update" {
			t.Fatalf("path %s", path)
		}
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if have, want := strings.Join(keys, ","), "blocks,channel,metadata,text,ts,unfurl_links,unfurl_media"; have != want {
			t.Fatalf("fields %s, want %s", have, want)
		}
		for k, want := range map[string]string{
			"channel": `"C03"`, "ts": `"1729.0001"`, "text": `"text"`, "blocks": blocks,
			"metadata": wantMeta, "unfurl_links": "false", "unfurl_media": "false",
		} {
			if string(got[k]) != want {
				t.Errorf("%s = %s, want %s", k, got[k], want)
			}
		}
	})

	t.Run("no blocks, no blocks key", func(t *testing.T) {
		var got map[string]json.RawMessage
		c := slackServer(t, func(_ string, _ *http.Request, raw []byte) string {
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Errorf("body %q: %v", raw, err)
			}
			return `{"ok":true}`
		})
		if err := c.UpdateProposal(context.Background(), "C03", "1729.0001", "text", nil, meta); err != nil {
			t.Fatal(err)
		}
		if _, ok := got["blocks"]; ok {
			t.Fatalf("an update with no blocks carried the key: %+v", got)
		}
	})

	t.Run("a refused update is Slack's code, never the token", func(t *testing.T) {
		c := slackServer(t, func(string, *http.Request, []byte) string {
			return `{"ok":false,"error":"invalid_metadata"}`
		})
		err := c.UpdateProposal(context.Background(), "C03", "1729.0001", "text", json.RawMessage(blocks), meta)
		if err == nil || !strings.Contains(err.Error(), "invalid_metadata") || strings.Contains(err.Error(), "xoxb-test") {
			t.Fatalf("%v", err)
		}
	})
}
