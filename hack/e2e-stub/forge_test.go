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
	_, ts := newStubServer(t)
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
	if issue.Number != number || issue.Body != "body" || issue.Author != "dev" || issue.Association != "OWNER" {
		t.Fatalf("forge issue = %+v", issue)
	}
}
