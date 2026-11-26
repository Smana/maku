// SPDX-License-Identifier: Apache-2.0

package humanapi_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/Smana/maku/internal/app"
	"github.com/Smana/maku/internal/humanapi"
	"github.com/Smana/maku/internal/maku"
)

// maku start against the real broker handler, not a stubbed response: with human.intake.wholePlans
// on, POST /api/handoffs/{id}/check answers wholePlans and every issue, and the terminal files all
// three issues of the plan in order. Before the fix the route dropped both, and the terminal printed
// "this files the first" and filed one issue (live on gcp-0 after #729).
func TestTerminalStartFilesAWholePlanThroughTheBroker(t *testing.T) {
	ts := httptest.NewTLSServer(humanapi.WholePlanBroker(t))
	t.Cleanup(ts.Close)

	dir := t.TempDir()
	out := &strings.Builder{}
	r := app.Maku{Dir: dir, HC: ts.Client(), Stream: ts.Client(), Out: out, Err: &strings.Builder{}}
	if err := r.Run(t.Context(), []string{"configure", "--url", ts.URL, "--issuer", "https://auth.example",
		"--client-id", "cli", "--project-id", "29184"}); err != nil {
		t.Fatal(err)
	}
	if err := maku.SaveJSON(filepath.Join(dir, "token.json"), &oauth2.Token{AccessToken: "tok-dev", TokenType: "Bearer",
		RefreshToken: "rt", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	r.Ask = func(string) (bool, error) { return true, nil }
	var titles []string
	r.FileIssue = func(_ context.Context, repo, title, _ string) (string, error) {
		titles = append(titles, title)
		return fmt.Sprintf("https://github.com/%s/issues/%d", repo, 40+len(titles)), nil
	}
	r.AddLabel = func(context.Context, string, int, string) error { return nil }
	r.LabelIssue = r.AddLabel

	if err := r.Run(t.Context(), []string{"start", "fix the three stale docs pages"}); err != nil {
		t.Fatalf("maku start: %v\n%s", err, out)
	}
	want := []string{"docs: the install link points at a moved page", "docs: the quickstart names a removed flag",
		"docs: the upgrade page omits the migration step"}
	if !slices.Equal(titles, want) {
		t.Fatalf("filed %q, want %q\n%s", titles, want, out)
	}
	if got := out.String(); !strings.Contains(got, "this files all 3, in order") || strings.Contains(got, "this files the first") ||
		!strings.Contains(got, "Started 3 issues") {
		t.Fatalf("output:\n%s", got)
	}
}
