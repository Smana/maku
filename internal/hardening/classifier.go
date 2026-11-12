// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/Smana/maku/internal/httpx"
)

// Bounds on what WebhookClassifier sends: a message's runes, a request's bytes, and the requests
// one Injection call may make.
const (
	MaxClassifierMessageRunes = 2000
	MaxClassifierRequestBytes = 64 << 10
	MaxClassifierRequests     = 16
)

// maxClassifierReply bounds agent-guard's answer: a Mask echoes the messages back.
const maxClassifierReply = 256 << 10

// WebhookClassifier is T5's client for agent-guard, over agentgateway's guardrail webhook contract
// (HR9): POST <URL>/request with {"body":{"messages":[{"role":"user","content":…}]}}, answered by
// a Pass, Mask or Reject action. HC is the audited egress client (httpx.New) and is required.
type WebhookClassifier struct {
	URL string
	HC  *http.Client
}

type guardrailMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type guardrailRequest struct {
	Body struct {
		Messages []guardrailMessage `json:"messages"`
	} `json:"body"`
}

// Injection reports whether agent-guard rejects any of texts. Each text is split into messages of
// at most MaxClassifierMessageRunes, sent over as few requests of at most MaxClassifierRequestBytes
// as fit; any Reject is a hit, a Pass or a Mask is none. A non-2xx answer, a timeout, an answer
// that is no guardrail action, or more text than MaxClassifierRequests carry is an error, which
// Checker reads as classifier_unavailable. No error quotes a text.
func (w WebhookClassifier) Injection(ctx context.Context, texts []string) (bool, error) {
	if w.HC == nil {
		return false, errors.New("agent-guard: no HTTP client")
	}
	batches, err := batch(texts)
	if err != nil {
		return false, err
	}
	for _, b := range batches {
		hit, err := w.post(ctx, b)
		if err != nil || hit {
			return hit, err
		}
	}
	return false, nil
}

// batch splits texts into messages and packs them into request bodies, in order.
func batch(texts []string) ([][]byte, error) {
	envelope, _ := json.Marshal(guardrailRequest{}) // a struct of a nil slice: Marshal cannot fail
	var (
		out  [][]byte
		cur  []guardrailMessage
		size int
	)
	flush := func() error {
		if len(cur) == 0 {
			return nil
		}
		if len(out) == MaxClassifierRequests {
			return fmt.Errorf("agent-guard: the texts need more than %d requests", MaxClassifierRequests)
		}
		var r guardrailRequest
		r.Body.Messages = cur
		b, _ := json.Marshal(r) // strings only: Marshal cannot fail
		out = append(out, b)
		cur, size = nil, 0
		return nil
	}
	for _, t := range texts {
		for _, chunk := range chunks(t) {
			m := guardrailMessage{Role: "user", Content: chunk}
			mb, _ := json.Marshal(m) // strings only: Marshal cannot fail
			if len(cur) > 0 && len(envelope)+size+len(mb)+1 > MaxClassifierRequestBytes {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			cur = append(cur, m)
			size += len(mb) + 1
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// chunks splits s into pieces of at most MaxClassifierMessageRunes runes; an empty s has none.
func chunks(s string) []string {
	var out []string
	for s != "" {
		end, n := 0, 0
		for end < len(s) && n < MaxClassifierMessageRunes {
			_, size := utf8.DecodeRuneInString(s[end:])
			end += size
			n++
		}
		out = append(out, s[:end])
		s = s[end:]
	}
	return out
}

// post sends one request and reads its action.
func (w WebhookClassifier) post(ctx context.Context, body []byte) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(w.URL, "/")+"/request", bytes.NewReader(body))
	if err != nil {
		return false, errors.New("agent-guard: the URL does not make a request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.HC.Do(req)
	if err != nil {
		return false, fmt.Errorf("agent-guard: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return false, fmt.Errorf("agent-guard: status %d", resp.StatusCode)
	}
	b, err := httpx.ReadBody(resp.Body, maxClassifierReply)
	if err != nil {
		return false, fmt.Errorf("agent-guard: %w", err)
	}
	return readAction(b)
}

// readAction reads a guardrail answer: a Reject carries status_code, a Mask a body, and a Pass
// nothing but a reason. Anything else is an error, as agentgateway itself treats it.
func readAction(b []byte) (bool, error) {
	var r struct {
		Action map[string]json.RawMessage `json:"action"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.Action == nil {
		return false, errors.New("agent-guard: the answer is not a guardrail action")
	}
	if _, ok := r.Action["status_code"]; ok {
		return true, nil
	}
	if _, ok := r.Action["body"]; ok {
		return false, nil
	}
	for k := range r.Action {
		if k != "reason" {
			return false, errors.New("agent-guard: a pass action carries a field other than reason")
		}
	}
	return false, nil
}
