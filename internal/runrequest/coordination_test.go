// SPDX-License-Identifier: Apache-2.0

package runrequest

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/coordination"
)

func TestCoordinationDraftAndConfirmForwardTheCallersToken(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(body))
		switch {
		case strings.Contains(string(body), `"planHash":"stale"`):
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"stale_plan"}`))
		case strings.Contains(string(body), `"planHash":"evil"`):
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"see https://evil.example now"}`))
		default:
			_, _ = w.Write([]byte(`{"plan":{"id":"pppppppp"},"planHash":"h","planning":{"attemptID":"a","effectiveRequester":"x","reservedMicroUSD":0,"settledMicroUSD":0}}`))
		}
	}))
	defer srv.Close()
	f := Factory{URL: srv.URL, HC: srv.Client()}
	in := coordination.CoordinationConfirmation{Plan: v1alpha1.CoordinationPlan{ID: "pppppppp"}, PlanHash: "h"}

	if _, err := f.CoordinationDraft(t.Context(), "", "pppppppp", in, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("no token: %v", err)
	}
	out, err := f.CoordinationDraft(t.Context(), "tok", "pppppppp", in, "U123")
	if err != nil || out.PlanHash != "h" {
		t.Fatalf("%+v %v", out, err)
	}
	if !strings.HasPrefix(seen[0], "PUT /v1/coordinations/pppppppp/draft Bearer tok ") || !strings.Contains(seen[0], `"onBehalfOf":"U123"`) ||
		strings.Contains(seen[0], `"parent"`) || strings.Contains(seen[0], `"child"`) {
		t.Fatalf("draft sent %s", seen[0])
	}
	in.Parent = &coordination.ParentConfirmation{PlanHash: "h"}
	if _, err := f.CoordinationConfirm(t.Context(), "tok", "pppppppp", in); err != nil {
		t.Fatal(err)
	}
	var sent map[string]any
	_ = json.Unmarshal([]byte(strings.SplitN(seen[1], " ", 5)[4]), &sent)
	if !strings.HasPrefix(seen[1], "POST /v1/coordinations/pppppppp/confirmations Bearer tok ") || sent["parent"] == nil {
		t.Fatalf("confirm sent %s", seen[1])
	}
	in.PlanHash = "stale"
	var ref ErrRefused
	if _, err := f.CoordinationConfirm(t.Context(), "tok", "pppppppp", in); !errors.As(err, &ref) || ref.Reason != "stale_plan" || ref.Status != http.StatusConflict {
		t.Fatalf("stale: %v", err)
	}
	in.PlanHash = "evil"
	if _, err := f.CoordinationConfirm(t.Context(), "tok", "pppppppp", in); !errors.As(err, &ref) || ref.Reason != "refused" {
		t.Fatalf("free text passed through: %v", err)
	}
	if _, err := f.CoordinationConfirm(t.Context(), "tok", "../x", in); err == nil {
		t.Fatal("a hostile parent reached a URL")
	}
}

func TestCoordinationRecoverForwardsOneAction(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(body))
		if strings.Contains(string(body), `"planVersion":1`) {
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"error":"stale_plan"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	f := Factory{URL: srv.URL, HC: srv.Client()}
	if err := f.CoordinationRecover(t.Context(), "tok", "pppppppp", "retry_child", 2); err != nil {
		t.Fatal(err)
	}
	if seen[0] != `POST /v1/coordinations/pppppppp/recovery Bearer tok {"code":"retry_child","planVersion":2}` {
		t.Fatalf("%s", seen[0])
	}
	var ref ErrRefused
	if err := f.CoordinationRecover(t.Context(), "tok", "pppppppp", "retry_child", 1); !errors.As(err, &ref) || ref.Reason != "stale_plan" {
		t.Fatalf("%v", err)
	}
	if err := f.CoordinationRecover(t.Context(), "", "pppppppp", "retry_child", 2); !errors.Is(err, ErrForbidden) {
		t.Fatalf("no token: %v", err)
	}
	if err := f.CoordinationRecover(t.Context(), "tok", "pppppppp", "<b>", 2); err == nil || len(seen) != 2 {
		t.Fatalf("a hostile code reached the factory: %v %v", err, seen)
	}
}
