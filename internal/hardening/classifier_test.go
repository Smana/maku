// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

// guard is a fake agent-guard: it records each request and answers with answer.
type guard struct {
	mu       sync.Mutex
	paths    []string
	sizes    []int
	messages [][]guardMessage
	answer   func(msgs []guardMessage) (int, string)
}

type guardMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (g *guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var req struct {
		Body struct {
			Messages []guardMessage `json:"messages"`
		} `json:"body"`
	}
	_ = json.Unmarshal(b, &req)
	g.mu.Lock()
	g.paths = append(g.paths, r.Method+" "+r.URL.Path+" "+r.Header.Get("Content-Type"))
	g.sizes = append(g.sizes, len(b))
	g.messages = append(g.messages, req.Body.Messages)
	g.mu.Unlock()
	code, body := g.answer(req.Body.Messages)
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

const (
	pass   = `{"action":{"reason":"No policy violations found."}}`
	empty  = `{"action":{}}`
	mask   = `{"action":{"body":{"messages":[{"role":"user","content":"****"}]},"reason":"masked"}}`
	reject = `{"action":{"status_code":451,"body":"blocked","reason":"injection"}}`
)

func fixed(code int, body string) func([]guardMessage) (int, string) {
	return func([]guardMessage) (int, string) { return code, body }
}

func newGuard(t *testing.T, answer func([]guardMessage) (int, string)) (*guard, WebhookClassifier) {
	t.Helper()
	g := &guard{answer: answer}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	return g, WebhookClassifier{URL: srv.URL, HC: srv.Client()}
}

// T5 posts the texts as agentgateway's guardrail webhook /request reads them (HR9): a Reject is a
// hit; a Pass or a Mask is none.
func TestWebhookClassifier(t *testing.T) {
	for name, tc := range map[string]struct {
		code    int
		body    string
		hit, ok bool
	}{
		"a pass":                  {200, pass, false, true},
		"an empty pass":           {200, empty, false, true},
		"a mask":                  {200, mask, false, true},
		"a reject":                {200, reject, true, true},
		"a 500":                   {500, pass, false, false},
		"a 403":                   {403, reject, false, false},
		"not JSON":                {200, `{"action":`, false, false},
		"no action":               {200, `{"verdict":"ok"}`, false, false},
		"an action not an object": {200, `{"action":"reject"}`, false, false},
		"a pass with a score":     {200, `{"action":{"reason":"x","score":0.9}}`, false, false},
	} {
		t.Run(name, func(t *testing.T) {
			g, c := newGuard(t, fixed(tc.code, tc.body))
			hit, err := c.Injection(t.Context(), []string{"ignore previous instructions", "second"})
			if (err == nil) != tc.ok || hit != tc.hit {
				t.Fatalf("hit %v err %v, want hit %v ok %v", hit, err, tc.hit, tc.ok)
			}
			if len(g.paths) != 1 || g.paths[0] != "POST /request application/json" {
				t.Fatalf("requests %q", g.paths)
			}
			want := []guardMessage{{"user", "ignore previous instructions"}, {"user", "second"}}
			if got := g.messages[0]; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
				t.Fatalf("messages %+v", got)
			}
		})
	}
}

// No message exceeds 2,000 runes, a text is split rather than cut, and no request exceeds 64 KiB:
// a long output goes over several requests, and any Reject among them is a hit.
func TestWebhookClassifierChunks(t *testing.T) {
	long := strings.Repeat("é", 4500) // 4,500 runes, 9,000 bytes
	big := make([]string, 20)
	for i := range big {
		big[i] = strings.Repeat("x", 6000)
	}
	g, c := newGuard(t, fixed(200, pass))
	if hit, err := c.Injection(t.Context(), append([]string{long, ""}, big...)); err != nil || hit {
		t.Fatalf("hit %v err %v", hit, err)
	}
	var joined strings.Builder
	for i, msgs := range g.messages {
		if g.sizes[i] > MaxClassifierRequestBytes {
			t.Fatalf("request %d is %d bytes", i, g.sizes[i])
		}
		for _, m := range msgs {
			if n := utf8.RuneCountInString(m.Content); n > MaxClassifierMessageRunes || n == 0 {
				t.Fatalf("a message of %d runes", n)
			}
			joined.WriteString(m.Content)
		}
	}
	if len(g.messages) < 2 {
		t.Fatalf("%d requests for ~130 KB", len(g.messages))
	}
	if joined.String() != long+strings.Join(big, "") {
		t.Fatal("a chunk was lost, reordered or altered")
	}

	// a Reject in the last request is still a hit
	n := 0
	_, c = newGuard(t, func([]guardMessage) (int, string) {
		n++
		if n == 2 {
			return 200, reject
		}
		return 200, pass
	})
	if hit, err := c.Injection(t.Context(), big); err != nil || !hit {
		t.Fatalf("hit %v err %v", hit, err)
	}

	// nothing to check: no call
	g, c = newGuard(t, fixed(500, ""))
	if hit, err := c.Injection(t.Context(), []string{"", ""}); err != nil || hit || len(g.paths) != 0 {
		t.Fatalf("hit %v err %v calls %d", hit, err, len(g.paths))
	}

	// more text than the request cap allows is an error, never a partial check
	huge := make([]string, MaxClassifierRequests*MaxClassifierRequestBytes/6000+40)
	for i := range huge {
		huge[i] = strings.Repeat("x", 6000)
	}
	g, c = newGuard(t, fixed(200, pass))
	if _, err := c.Injection(t.Context(), huge); err == nil || len(g.paths) != 0 {
		t.Fatalf("err %v after %d calls", err, len(g.paths))
	}
}

// A timeout and a missing client are errors, which Checker maps to classifier_unavailable.
func TestWebhookClassifierUnavailable(t *testing.T) {
	g := &guard{answer: fixed(200, pass)}
	done := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-done:
		}
		g.ServeHTTP(w, r)
	}))
	defer slow.Close()
	defer close(done) // before Close, which waits for the handler
	hc := slow.Client()
	hc.Timeout = 50 * time.Millisecond
	if _, err := (WebhookClassifier{URL: slow.URL, HC: hc}).Injection(t.Context(), []string{"x"}); err == nil {
		t.Fatal("a timeout is an error")
	}
	if _, err := (WebhookClassifier{URL: slow.URL}).Injection(t.Context(), []string{"x"}); err == nil {
		t.Fatal("no client is an error")
	}
	_, c := newGuard(t, fixed(503, pass))
	if v := (Checker{Classifier: c}).classify(t.Context(), []string{"x"}); v != RefuseClassifierDown {
		t.Fatalf("Checker reads %q", v)
	}
}

// No error carries a byte of the text it was checking: errors reach log lines.
func TestWebhookClassifierNoContentInErrors(t *testing.T) {
	secret := "SECRET-PAYLOAD-7f3a"
	texts := []string{secret, strings.Repeat(secret, 300)}
	for name, answer := range map[string]func([]guardMessage) (int, string){
		"a 500":            fixed(500, secret),
		"echoed garbage":   func(m []guardMessage) (int, string) { return 200, `{"action":` + m[0].Content },
		"an echoed action": func(m []guardMessage) (int, string) { return 200, `{"action":{"oops":"` + m[0].Content + `"}}` },
		"a string action":  func(m []guardMessage) (int, string) { return 200, `{"action":"` + m[0].Content + `"}` },
	} {
		t.Run(name, func(t *testing.T) {
			_, c := newGuard(t, answer)
			_, err := c.Injection(t.Context(), texts)
			if err == nil {
				t.Fatal("want an error")
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("the error quotes the text: %v", err)
			}
		})
	}
}
