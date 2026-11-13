// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"io"
	"mime"
	"net/http"
	"slices"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

// otlpTraces is an OTLP/HTTP trace sink (protobuf, plain or gzip), as a run's harness exports to it
// (plan v4 step 6, Task 2): it keeps each span's trace id, once, and answers an empty success.
func (s *Stub) otlpTraces(w http.ResponseWriter, r *http.Request) {
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/x-protobuf" {
		http.Error(w, "OTLP/HTTP protobuf only", http.StatusUnsupportedMediaType)
		return
	}
	raw, ok := readBody(w, r)
	if !ok {
		return
	}
	switch r.Header.Get("Content-Encoding") {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			http.Error(w, "invalid gzip body", http.StatusBadRequest)
			return
		}
		if raw, err = io.ReadAll(io.LimitReader(zr, maxRequestBody+1)); err != nil || len(raw) > maxRequestBody {
			http.Error(w, "invalid gzip body", http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "gzip or no encoding only", http.StatusUnsupportedMediaType)
		return
	}
	var req collectortrace.ExportTraceServiceRequest
	if err := proto.Unmarshal(raw, &req); err != nil {
		http.Error(w, "invalid OTLP trace request", http.StatusBadRequest)
		return
	}
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			for _, sp := range ss.GetSpans() {
				s.recordTrace(hex.EncodeToString(sp.GetTraceId()))
			}
		}
	}
	body, _ := proto.Marshal(&collectortrace.ExportTraceServiceResponse{}) // an empty message
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(body)
}

// recordTrace keeps id once, the newest maxReceived.
func (s *Stub) recordTrace(id string) {
	s.receivedMu.Lock()
	defer s.receivedMu.Unlock()
	if slices.Contains(s.traces, id) {
		return
	}
	s.traces = append(s.traces, id)
	if len(s.traces) > maxReceived {
		s.traces = s.traces[len(s.traces)-maxReceived:]
	}
}

// Traces returns the trace ids received so far, each once, oldest first.
func (s *Stub) Traces() []string {
	s.receivedMu.Lock()
	defer s.receivedMu.Unlock()
	return append([]string{}, s.traces...)
}
