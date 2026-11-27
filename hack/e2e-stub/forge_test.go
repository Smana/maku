// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/x509"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/httpx"
)

type staticToken string

func (s staticToken) Token(context.Context) (string, error) { return string(s), nil }

// The factory's own forge client reads the scenario issue from the stub: the stub serves whatever
// query the forge sends now, not a hand-copied serialization of it that can drift.
func TestForgeReadsTheScenarioIssue(t *testing.T) {
	stub, ts := newStubServer(t)
	number := createIssue(t, ts, "body")
	roots := x509.NewCertPool()
	roots.AddCert(ts.Certificate())
	g, err := forge.ConnectTokens(t.Context(), forge.Options{Repository: "owner/repo", APIURL: ts.URL + "/",
		UserAgent: "agent-factory/test", HTTP: httpx.New(5*time.Second, roots), Now: time.Now}, staticToken("t"))
	if err != nil {
		t.Fatal(err)
	}
	issue, err := g.Issue(t.Context(), number)
	if err != nil {
		t.Fatalf("forge Issue: %v", err)
	}
	if issue.Number != number || issue.Body != "body" || issue.Author != "dev" || issue.Association != "OWNER" ||
		issue.CreatedAt.IsZero() || !issue.ClosedAt.IsZero() || issue.StateReason != "" {
		t.Fatalf("forge issue = %+v", issue)
	}

	if _, ok := stub.issues.CloseIssue(number, "completed"); !ok {
		t.Fatalf("CloseIssue(%d) returned !ok", number)
	}
	closed, err := g.Issue(t.Context(), number)
	if err != nil {
		t.Fatalf("forge closed Issue: %v", err)
	}
	if closed.State != "CLOSED" || closed.StateReason != "completed" || closed.CreatedAt.IsZero() || closed.ClosedAt.IsZero() {
		t.Fatalf("forge closed issue = %+v", closed)
	}
}
