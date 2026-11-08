// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCoordinationOpenDraftsOnly(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = append(got, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(body))
		if strings.Contains(string(body), `"planHash":"stale"`) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"plan_version"}`))
			return
		}
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	r := &Relay{URL: srv.URL, HC: srv.Client(), Token: func(context.Context) (string, error) { return "tok", nil }}
	call := func(args string) map[string]any {
		raw, _ := json.Marshal(r.coordinationOpen(t.Context(), localCall{Name: toolCoordinationOpen, Args: json.RawMessage(args)}))
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		return m
	}
	conf := `{"plan":{"id":"pppppppp","version":1},"planHash":"h","planning":{"attemptID":"a","effectiveRequester":"Smana","reservedMicroUSD":0,"settledMicroUSD":0}}`
	text, sc, isErr := resultOf(map[string]any{"result": call(`{"confirmation":` + conf + `}`)})
	if isErr || sc["parentTask"] != "pppppppp" || sc["roomURL"] != srv.URL+"/r/pppppppp" {
		t.Fatalf("%q %v", text, sc)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "PUT /api/coordinations/pppppppp/draft Bearer tok ") {
		t.Fatalf("%v", got)
	}
	for name, args := range map[string]string{
		"a confirm field":  `{"confirmation":` + conf + `,"confirm":true}`,
		"a parent record":  `{"confirmation":{"plan":{"id":"pppppppp"},"planHash":"h","planning":{},"parent":{"planHash":"h"}}}`,
		"a hostile parent": `{"confirmation":{"plan":{"id":"../x"},"planHash":"h","planning":{}}}`,
	} {
		if _, _, isErr := resultOf(map[string]any{"result": call(args)}); !isErr {
			t.Errorf("%s was accepted", name)
		}
	}
	if len(got) != 1 {
		t.Fatalf("a refused call reached the broker: %v", got)
	}
	text, _, isErr = resultOf(map[string]any{"result": call(`{"confirmation":{"plan":{"id":"pppppppp"},"planHash":"stale","planning":{}}}`)})
	if !isErr || !strings.Contains(text, "plan_version") {
		t.Fatalf("%q", text)
	}
}
