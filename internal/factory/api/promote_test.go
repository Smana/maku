// SPDX-License-Identifier: Apache-2.0

package api

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/promote"
)

func TestPromotionAndDrillRoutes(t *testing.T) {
	s := policiesServer(true)
	post := func(token, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	if w := post("human", "/v1/policy/promote", `{"repository":"`+coordPrimary+`","class":"docs"}`); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"pullRequest":42`) {
		t.Fatalf("promote: %d %s", w.Code, w.Body)
	}
	if w := post("onlyreader", "/v1/policy/promote", `{"repository":"`+coordPrimary+`","class":"docs"}`); w.Code != http.StatusForbidden {
		t.Fatalf("reader promotes: %d %s", w.Code, w.Body)
	}
	if w := post("human", "/v1/policy/drill", `{"repository":"`+coordPrimary+`","pullRequest":41}`); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), `"pullRequest":41`) {
		t.Fatalf("drill: %d %s", w.Code, w.Body)
	}
	w := getAs(s.Handler(), "human", "/v1/policy/promotion?repository="+coordPrimary+"&class=docs&pr=42")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"pullRequestState":"CLOSED"`) || !strings.Contains(w.Body.String(), `"servedMode":"shadow"`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
}

// A drill that opens nothing says why, and the factory logs the cause: it once answered a bare
// drill_unavailable with nothing logged.
func TestDrillRefusalNamesAndLogsItsCause(t *testing.T) {
	for _, tc := range []struct {
		err    error
		code   int
		reason string
	}{
		{promote.ErrNotMerged, http.StatusConflict, "drill_not_merged"},
		{promote.ErrNoMerger, http.StatusServiceUnavailable, "drill_no_merger"},
		{promote.ErrNoConnection, http.StatusServiceUnavailable, "drill_no_connection"},
		{errors.New("open the revert of #505: Resource not accessible by integration"), http.StatusBadGateway, "drill_revert_failed"},
	} {
		s := policiesServer(true)
		s.Editor.(*fakeEditor).drillErr = tc.err
		var logs bytes.Buffer
		s.Log = slog.New(slog.NewTextHandler(&logs, nil))
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/policy/drill",
			strings.NewReader(`{"repository":"`+coordPrimary+`","pullRequest":505}`))
		req.Header.Set("Authorization", "Bearer human")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), `"`+tc.reason+`"`) {
			t.Errorf("%v: %d %s", tc.err, w.Code, w.Body)
		}
		if !strings.Contains(logs.String(), tc.reason) || !strings.Contains(logs.String(), tc.err.Error()) {
			t.Errorf("%v: not logged: %s", tc.err, logs.String())
		}
	}
}
