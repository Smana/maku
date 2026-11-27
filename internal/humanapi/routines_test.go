// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/runrequest"
)

type fakeRoutinesGateway struct {
	tokens  []string
	repos   []string
	targets []string
	idem    []string
	bodies  []string

	listRaw   json.RawMessage
	getRaw    json.RawMessage
	runStatus int
	runRaw    json.RawMessage
	pauseRaw  json.RawMessage
	resumeRaw json.RawMessage
	err       error
}

func (f *fakeRoutinesGateway) ListRoutines(_ context.Context, token, repo string) (json.RawMessage, error) {
	f.tokens = append(f.tokens, token)
	f.repos = append(f.repos, repo)
	return f.listRaw, f.err
}

func (f *fakeRoutinesGateway) GetRoutine(_ context.Context, token, owner, name, routine string) (json.RawMessage, error) {
	f.tokens = append(f.tokens, token)
	f.targets = append(f.targets, owner+"/"+name+"/"+routine)
	return f.getRaw, f.err
}

func (f *fakeRoutinesGateway) RunRoutine(_ context.Context, token, owner, name, routine, idempotencyKey string, body json.RawMessage) (int, json.RawMessage, error) {
	f.tokens = append(f.tokens, token)
	f.targets = append(f.targets, owner+"/"+name+"/"+routine)
	f.idem = append(f.idem, idempotencyKey)
	f.bodies = append(f.bodies, string(body))
	return f.runStatus, f.runRaw, f.err
}

func (f *fakeRoutinesGateway) PauseRoutine(_ context.Context, token, owner, name, routine string, body json.RawMessage) (json.RawMessage, error) {
	f.tokens = append(f.tokens, token)
	f.targets = append(f.targets, owner+"/"+name+"/"+routine)
	f.bodies = append(f.bodies, string(body))
	return f.pauseRaw, f.err
}

func (f *fakeRoutinesGateway) ResumeRoutine(_ context.Context, token, owner, name, routine string, body json.RawMessage) (json.RawMessage, error) {
	f.tokens = append(f.tokens, token)
	f.targets = append(f.targets, owner+"/"+name+"/"+routine)
	f.bodies = append(f.bodies, string(body))
	return f.resumeRaw, f.err
}

func routinesOn(gw *fakeRoutinesGateway) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) {
		w := w0()
		w.gate(s)
		s.Humans, s.PublicURL, s.UI = tokenAuth{}, publicURL, policyIndex
		s.Routines = gw
	}
}

var routineRoutesList = []struct{ method, path string }{
	{http.MethodGet, "/routines"},
	{http.MethodGet, "/routines/Smana/maku/dead-links"},
	{http.MethodGet, "/api/routines"},
	{http.MethodGet, "/api/routines/Smana/maku/dead-links"},
	{http.MethodPost, "/api/routines/Smana/maku/dead-links/runs"},
	{http.MethodPost, "/api/routines/Smana/maku/dead-links/pause"},
	{http.MethodPost, "/api/routines/Smana/maku/dead-links/resume"},
}

func TestRoutinesOffIsByteIdentical(t *testing.T) {
	e := setup(t, func(s *Server, _ *fanout.Hub, _ *hubView) { s.Humans, s.UI = tokenAuth{}, policyIndex })
	wantCode, want := sendJSON(t, e, http.MethodGet, "/never-a-route", header("maint"), "")
	for _, rt := range routineRoutesList {
		if code, body := sendJSON(t, e, rt.method, rt.path, header("maint"), "{}"); code != wantCode || body != want {
			t.Errorf("%s %s: %d %q, want %d %q", rt.method, rt.path, code, body, wantCode, want)
		}
	}
}

func TestRoutinePagesServeTheApp(t *testing.T) {
	gw := &fakeRoutinesGateway{}
	e := setup(t, routinesOn(gw))
	for _, p := range []string{"/routines", "/routines/Smana/maku/dead-links"} {
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.ts.URL+p, nil)
		req.Header = header("anyone")
		resp, err := e.ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(b), "maku</html>") || resp.Header.Get("Content-Security-Policy") == "" {
			t.Fatalf("%s: status=%d csp=%q body=%q", p, resp.StatusCode, resp.Header.Get("Content-Security-Policy"), string(b))
		}
	}
}

func TestRoutinesAuthOriginValidationAndForwarding(t *testing.T) {
	gw := &fakeRoutinesGateway{
		listRaw:   json.RawMessage(`{"routines":[{"routine":"dead-links"}]}`),
		getRaw:    json.RawMessage(`{"routine":"dead-links"}`),
		runStatus: http.StatusCreated,
		runRaw:    json.RawMessage(`{"task":"t1"}`),
		pauseRaw:  json.RawMessage(`{"paused":true}`),
		resumeRaw: json.RawMessage(`{"paused":false}`),
	}
	e := setup(t, routinesOn(gw))

	// Unauthenticated -> 401.
	if code, _ := sendJSON(t, e, http.MethodGet, "/api/routines", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("unauth GET = %d", code)
	}
	// System / sponsored / empty access token -> 403 human_only.
	if code, body := sendJSON(t, e, http.MethodGet, "/api/routines", header("anyone", "X-Test-Kind", "system"), ""); code != http.StatusForbidden || !strings.Contains(body, `"human_only"`) {
		t.Fatalf("system GET = %d %s", code, body)
	}
	if code, body := sendJSON(t, e, http.MethodGet, "/api/routines", header("anyone", "X-Test-Kind", "sponsored"), ""); code != http.StatusForbidden || !strings.Contains(body, `"human_only"`) {
		t.Fatalf("sponsored GET = %d %s", code, body)
	}

	// Web POST without Origin or with foreign Origin -> 403 cross_origin.
	noOrigin := header("maint", "X-Test-Client", "web", "Content-Type", "application/json")
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/dead-links/runs", noOrigin, `{}`); code != http.StatusForbidden || !strings.Contains(body, `"cross_origin"`) {
		t.Fatalf("web without Origin = %d %s", code, body)
	}
	foreignOrigin := header("maint", "X-Test-Client", "web", "Origin", "https://evil.example", "Content-Type", "application/json")
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/dead-links/pause", foreignOrigin, `{}`); code != http.StatusForbidden || !strings.Contains(body, `"cross_origin"`) {
		t.Fatalf("web foreign Origin = %d %s", code, body)
	}
	// Bearer CLI POST with foreign Origin is also refused.
	cliForeign := header("maint", "X-Test-Client", "maku", "Origin", "https://evil.example", "Content-Type", "application/json")
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/dead-links/resume", cliForeign, `{}`); code != http.StatusForbidden || !strings.Contains(body, `"cross_origin"`) {
		t.Fatalf("cli foreign Origin = %d %s", code, body)
	}

	// Missing / wrong Content-Type -> 415 unsupported_media_type.
	wrongCT := header("maint", "X-Test-Client", "maku", "Content-Type", "text/plain")
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/dead-links/runs", wrongCT, `{}`); code != http.StatusUnsupportedMediaType || !strings.Contains(body, `"unsupported_media_type"`) {
		t.Fatalf("wrong CT = %d %s", code, body)
	}

	// Path validation & body validation fail before hitting gateway.
	if code, body := sendJSON(t, e, http.MethodGet, "/api/routines?repo=../evil", header("maint"), ""); code != http.StatusBadRequest || !strings.Contains(body, `"bad_repository"`) {
		t.Fatalf("bad repo query = %d %s", code, body)
	}
	if code, body := sendJSON(t, e, http.MethodGet, "/api/routines/Smana/maku/BAD_ROUTINE", header("maint"), ""); code != http.StatusBadRequest || !strings.Contains(body, `"bad_routine"`) {
		t.Fatalf("bad routine path = %d %s", code, body)
	}
	cliHdr := header("maint", "X-Test-Client", "maku", "Content-Type", "application/json")
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/dead-links/runs", cliHdr, `[]`); code != http.StatusBadRequest || !strings.Contains(body, `"bad_json"`) {
		t.Fatalf("array body = %d %s", code, body)
	}
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/dead-links/runs", cliHdr, `{}{}`); code != http.StatusBadRequest || !strings.Contains(body, `"bad_json"`) {
		t.Fatalf("trailing json body = %d %s", code, body)
	}
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/dead-links/runs", cliHdr, `{"p":"`+strings.Repeat("x", 9<<10)+`"}`); code != http.StatusBadRequest || !strings.Contains(body, `"bad_json"`) {
		t.Fatalf("oversized body = %d %s", code, body)
	}
	if len(gw.tokens) != 0 {
		t.Fatalf("invalid requests reached gateway: %+v", gw.tokens)
	}

	// CLI bearer POST (no Origin) succeeds with 201 and forwards token + Idempotency-Key.
	cliRun := header("maint", "X-Test-Client", "maku", "Content-Type", "application/json; charset=utf-8", "Idempotency-Key", "cli-key-1")
	code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/dead-links/runs", cliRun, `{"mode":"pr"}`)
	if code != http.StatusCreated || !strings.Contains(body, `"t1"`) {
		t.Fatalf("cli run = %d %s", code, body)
	}
	if gw.tokens[0] != "tok-maint" || gw.idem[0] != "cli-key-1" || gw.bodies[0] != `{"mode":"pr"}` {
		t.Fatalf("gw got tokens=%v idem=%v bodies=%v", gw.tokens, gw.idem, gw.bodies)
	}

	// Web POST with matching Origin succeeds for pause & resume.
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/dead-links/pause", web("maint"), `{"reason":"hold"}`); code != http.StatusOK || !strings.Contains(body, `"paused":true`) {
		t.Fatalf("web pause = %d %s", code, body)
	}
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/dead-links/resume", web("maint"), `{}`); code != http.StatusOK || !strings.Contains(body, `"paused":false`) {
		t.Fatalf("web resume = %d %s", code, body)
	}

	// Upstream ErrRefused passes through status and reason (including mode_requires_phase2).
	gw.err = runrequest.ErrRefused{Status: http.StatusBadRequest, Reason: "mode_requires_phase2"}
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/dead-links/runs", web("maint"), `{"mode":"propose"}`); code != http.StatusBadRequest || !strings.Contains(body, `"mode_requires_phase2"`) {
		t.Fatalf("upstream refusal = %d %s", code, body)
	}

	// Upstream unexpected error -> 503 routines_unavailable.
	gw.err = errors.New("dial tcp timeout")
	if code, body := sendJSON(t, e, http.MethodGet, "/api/routines", header("maint"), ""); code != http.StatusServiceUnavailable || !strings.Contains(body, `"routines_unavailable"`) {
		t.Fatalf("upstream error = %d %s", code, body)
	}
}
