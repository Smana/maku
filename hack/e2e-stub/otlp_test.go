// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func exportRequest(t *testing.T, traceIDs ...string) []byte {
	t.Helper()
	var spans []*tracepb.Span
	for _, id := range traceIDs {
		raw, err := hex.DecodeString(id)
		if err != nil {
			t.Fatal(err)
		}
		spans = append(spans, &tracepb.Span{TraceId: raw, SpanId: []byte{1, 2, 3, 4, 5, 6, 7, 8}, Name: "agent-run"})
	}
	body, err := proto.Marshal(&collectortrace.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{{ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func postTraces(t *testing.T, ts *httptest.Server, contentType, encoding string, body []byte) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/v1/traces", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func TestOTLPTraces(t *testing.T) {
	_, ts := newStubServer(t)
	const a, b = "0af7651916cd43dd8448eb211c80319c", "4bf92f3577b34da6a3ce929d0e0e4736"
	if got := postTraces(t, ts, "application/x-protobuf", "", exportRequest(t, a, a)); got != http.StatusOK {
		t.Fatalf("POST /v1/traces = %d, want 200", got)
	}
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	if _, err := w.Write(exportRequest(t, b)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if got := postTraces(t, ts, "application/x-protobuf", "gzip", gz.Bytes()); got != http.StatusOK {
		t.Fatalf("gzip POST /v1/traces = %d, want 200", got)
	}
	for _, tc := range []struct {
		name, contentType, encoding string
		body                        []byte
		want                        int
	}{
		{"json", "application/json", "", []byte(`{}`), http.StatusUnsupportedMediaType},
		{"not protobuf", "application/x-protobuf", "", []byte{0xff, 0xff, 0xff}, http.StatusBadRequest},
		{"bad gzip", "application/x-protobuf", "gzip", []byte("nope"), http.StatusBadRequest},
		{"other encoding", "application/x-protobuf", "br", exportRequest(t, b), http.StatusUnsupportedMediaType},
	} {
		if got := postTraces(t, ts, tc.contentType, tc.encoding, tc.body); got != tc.want {
			t.Errorf("%s: POST /v1/traces = %d, want %d", tc.name, got, tc.want)
		}
	}
	status, raw := request(t, ts, http.MethodGet, "/__traces", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /__traces = %d", status)
	}
	got := decodeResponse[[]string](t, raw)
	if len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("trace ids %v, want [%s %s]: each once, oldest first", got, a, b)
	}
}

func TestOTLPTracesAreBounded(t *testing.T) {
	stub := New()
	for i := range maxReceived + 5 {
		id := make([]byte, 16)
		id[15] = byte(i + 1)
		stub.recordTrace(hex.EncodeToString(id))
	}
	if got := stub.Traces(); len(got) != maxReceived {
		t.Fatalf("%d trace ids kept, want %d", len(got), maxReceived)
	}
}
