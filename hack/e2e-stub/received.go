// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// absent marks a field the request did not carry, as the probe's echo provider writes it.
const absent = "<absent>"

// maxReceived bounds the record: the k3d test reads a handful of calls.
const maxReceived = 64

// ModelRequest is what one model request carried, as the provider behind the model gateway sees it
// (plan v4 step 4, Task 12): the fields a gateway preset rewrites, and the Authorization's digest,
// so the test tells the gateway's key from a caller's token without the stub printing either.
type ModelRequest struct {
	Role            string `json:"role"`
	Path            string `json:"path"`
	Model           any    `json:"model"`
	Temperature     any    `json:"temperature"`
	TopP            any    `json:"top_p"`
	ReasoningEffort any    `json:"reasoning_effort"`
	Authorization   string `json:"authorization"`
}

func (s *Stub) record(role string, r *http.Request, raw []byte) {
	var body map[string]any
	_ = json.Unmarshal(raw, &body) // the caller decoded it already
	field := func(name string) any {
		if v, ok := body[name]; ok {
			return v
		}
		return absent
	}
	auth := absent
	if h := r.Header.Get("Authorization"); h != "" {
		sum := sha256.Sum256([]byte(h))
		auth = "sha256:" + hex.EncodeToString(sum[:])
	}
	in := ModelRequest{
		Role: role, Path: r.URL.Path, Model: field("model"), Temperature: field("temperature"),
		TopP: field("top_p"), ReasoningEffort: field("reasoning_effort"), Authorization: auth,
	}
	s.receivedMu.Lock()
	defer s.receivedMu.Unlock()
	s.received = append(s.received, in)
	if len(s.received) > maxReceived {
		s.received = s.received[len(s.received)-maxReceived:]
	}
}

// Received returns the model requests recorded so far, oldest first.
func (s *Stub) Received() []ModelRequest {
	s.receivedMu.Lock()
	defer s.receivedMu.Unlock()
	return append([]ModelRequest{}, s.received...)
}

// readBody reads a bounded request body, answering 413 or 400 itself when it cannot.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "invalid request body", http.StatusBadRequest)
		}
		return nil, false
	}
	return raw, true
}
