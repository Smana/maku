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
	windows []string
	targets []string
	idem    []string
	bodies  []string

	listRaw    json.RawMessage
	catalogRaw json.RawMessage
	getRaw     json.RawMessage
	digestRaw  json.RawMessage
	previewRaw json.RawMessage
	putRaw     json.RawMessage
	runStatus  int
	runRaw     json.RawMessage
	pauseRaw   json.RawMessage
	resumeRaw  json.RawMessage
	err        error
}

func (f *fakeRoutinesGateway) ListRoutines(_ context.Context, token, repo string) (json.RawMessage, error) {
	f.tokens = append(f.tokens, token)
	f.repos = append(f.repos, repo)
	return f.listRaw, f.err
}

func (f *fakeRoutinesGateway) CatalogRoutines(_ context.Context, token string) (json.RawMessage, error) {
	f.tokens = append(f.tokens, token)
	return f.catalogRaw, f.err
}

func (f *fakeRoutinesGateway) GetRoutine(_ context.Context, token, owner, name, routine string) (json.RawMessage, error) {
	f.tokens = append(f.tokens, token)
	f.targets = append(f.targets, owner+"/"+name+"/"+routine)
	return f.getRaw, f.err
}

func (f *fakeRoutinesGateway) PreviewRoutine(_ context.Context, token, owner, name string, body json.RawMessage) (json.RawMessage, error) {
	f.tokens = append(f.tokens, token)
	f.targets = append(f.targets, owner+"/"+name)
	f.bodies = append(f.bodies, string(body))
	return f.previewRaw, f.err
}

func (f *fakeRoutinesGateway) PutRoutine(_ context.Context, token, owner, name, routine string, body json.RawMessage) (json.RawMessage, error) {
	f.tokens = append(f.tokens, token)
	f.targets = append(f.targets, owner+"/"+name+"/"+routine)
	f.bodies = append(f.bodies, string(body))
	return f.putRaw, f.err
}

func (f *fakeRoutinesGateway) RoutineDigest(_ context.Context, token string, repos []string, window string) (json.RawMessage, error) {
	f.tokens = append(f.tokens, token)
	f.repos = append(f.repos, strings.Join(repos, ","))
	f.windows = append(f.windows, window)
	return f.digestRaw, f.err
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
	{http.MethodGet, "/routines/catalog"},
	{http.MethodGet, "/routines/Smana/maku/dead-links"},
	{http.MethodGet, "/routines/Smana/maku/dead-links/edit"},
	{http.MethodGet, "/api/routines"},
	{http.MethodGet, "/api/routines/catalog"},
	{http.MethodGet, "/api/routines/digest"},
	{http.MethodGet, "/api/routines/Smana/maku/dead-links"},
	{http.MethodPost, "/api/routines/Smana/maku/preview"},
	{http.MethodPost, "/api/routines/Smana/maku/dead-links/runs"},
	{http.MethodPost, "/api/routines/Smana/maku/dead-links/pause"},
	{http.MethodPost, "/api/routines/Smana/maku/dead-links/resume"},
	{http.MethodPut, "/api/routines/Smana/maku/dead-links"},
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
	for _, p := range []string{"/routines", "/routines/catalog", "/routines/Smana/maku/dead-links", "/routines/Smana/maku/dead-links/edit"} {
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
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/dead-links/runs", cliHdr, `{"p":"`+strings.Repeat("x", 33<<10)+`"}`); code != http.StatusBadRequest || !strings.Contains(body, `"bad_json"`) {
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

func TestRoutineDigestRoute(t *testing.T) {
	gw := &fakeRoutinesGateway{
		digestRaw: json.RawMessage(`{"window":"7d","repositories":[{"repository":"Smana/maku","routines":[]}]}`),
	}
	internalP := sponsoredP
	internalP.ID = "system:internal-app"
	internalP.Internal = true
	noTokP := sponsoredP
	noTokP.ID = "system:notok"
	noTokP.AccessToken = ""

	e := setup(t, routinesOn(gw), withSponsored(sponsoredP, internalP, noTokP), func(s *Server, _ *fanout.Hub, _ *hubView) {
		s.SponsoredRepositories = map[string]map[string]bool{
			"system:slack-app": {"Smana/maku": true, "acme/api": true},
		}
	})

	// Unauthenticated -> 401.
	if code, _ := sendJSON(t, e, http.MethodGet, "/api/routines/digest", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("unauth = %d", code)
	}
	// Internal or tokenless sponsored -> 403 human_only.
	if code, body := sendJSON(t, e, http.MethodGet, "/api/routines/digest", bearer("internal-app"), ""); code != http.StatusForbidden || !strings.Contains(body, `"human_only"`) {
		t.Fatalf("internal = %d %s", code, body)
	}
	if code, body := sendJSON(t, e, http.MethodGet, "/api/routines/digest", bearer("notok"), ""); code != http.StatusForbidden || !strings.Contains(body, `"human_only"`) {
		t.Fatalf("notok = %d %s", code, body)
	}
	// Bad window / bad repo -> 400 before gateway.
	if code, body := sendJSON(t, e, http.MethodGet, "/api/routines/digest?window=31d", bearer("slack-app"), ""); code != http.StatusBadRequest || !strings.Contains(body, `"bad_window"`) {
		t.Fatalf("bad window = %d %s", code, body)
	}
	if code, body := sendJSON(t, e, http.MethodGet, "/api/routines/digest?repositories=../evil", bearer("slack-app"), ""); code != http.StatusBadRequest || !strings.Contains(body, `"bad_repository"`) {
		t.Fatalf("bad repo = %d %s", code, body)
	}
	// Sponsored requesting out-of-scope repo -> 403 repository_not_allowed before gateway.
	if code, body := sendJSON(t, e, http.MethodGet, "/api/routines/digest?repositories=Smana/maku,other/repo", bearer("slack-app"), ""); code != http.StatusForbidden || !strings.Contains(body, `"repository_not_allowed"`) {
		t.Fatalf("out of scope = %d %s", code, body)
	}
	if len(gw.tokens) != 0 {
		t.Fatalf("rejected calls hit gateway: %+v", gw.tokens)
	}

	// Sponsored without explicit repo filter forwards sorted allowed repos + sponsored token.
	code, body := sendJSON(t, e, http.MethodGet, "/api/routines/digest", bearer("slack-app"), "")
	if code != http.StatusOK || !strings.Contains(body, `"Smana/maku"`) {
		t.Fatalf("sponsored default = %d %s", code, body)
	}
	if gw.tokens[0] != "t" || gw.repos[0] != "Smana/maku,acme/api" || gw.windows[0] != "" {
		t.Fatalf("gw got tokens=%v repos=%v windows=%v", gw.tokens, gw.repos, gw.windows)
	}

	// Sponsored with in-scope case-insensitive repo forwards canonical allowed repo.
	code, _ = sendJSON(t, e, http.MethodGet, "/api/routines/digest?repository=smana/MAKU&window=14d", bearer("slack-app"), "")
	if code != http.StatusOK || gw.repos[1] != "Smana/maku" || gw.windows[1] != "14d" {
		t.Fatalf("sponsored scoped = %d repos=%v windows=%v", code, gw.repos, gw.windows)
	}

	// Human reader forwards human access token.
	code, _ = sendJSON(t, e, http.MethodGet, "/api/routines/digest?window=7d", header("maint"), "")
	if code != http.StatusOK || gw.tokens[2] != "tok-maint" {
		t.Fatalf("human = %d tokens=%v", code, gw.tokens)
	}
}

func TestRoutineCatalogPreviewPutProxy(t *testing.T) {
	gw := &fakeRoutinesGateway{
		catalogRaw: json.RawMessage(`{"routines":[],"repositories":["Smana/maku"],"uiWrite":true}`),
		previewRaw: json.RawMessage(`{"valid":true}`),
		putRaw:     json.RawMessage(`{"routine":"doc-drift"}`),
	}
	e := setup(t, routinesOn(gw))

	if code, _ := sendJSON(t, e, http.MethodGet, "/api/routines/catalog", nil, ""); code != http.StatusUnauthorized {
		t.Fatalf("unauth catalog = %d", code)
	}
	if code, body := sendJSON(t, e, http.MethodGet, "/api/routines/catalog", header("maint"), ""); code != http.StatusOK || !strings.Contains(body, `"uiWrite":true`) {
		t.Fatalf("catalog = %d %s", code, body)
	}

	// CSRF / Origin check applies to preview and PUT.
	noOrigin := header("maint", "X-Test-Client", "web", "Content-Type", "application/json")
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/preview", noOrigin, `{}`); code != http.StatusForbidden || !strings.Contains(body, `"cross_origin"`) {
		t.Fatalf("preview without Origin = %d %s", code, body)
	}
	if code, body := sendJSON(t, e, http.MethodPut, "/api/routines/Smana/maku/doc-drift", noOrigin, `{}`); code != http.StatusForbidden || !strings.Contains(body, `"cross_origin"`) {
		t.Fatalf("put without Origin = %d %s", code, body)
	}

	// 16 KiB preview payload succeeds (under 32 KiB limit).
	largePrompt := `{"binding":{"name":"doc-drift","mode":"propose"},"promptText":"` + strings.Repeat("a", 12<<10) + `"}`
	if code, body := sendJSON(t, e, http.MethodPost, "/api/routines/Smana/maku/preview", web("maint"), largePrompt); code != http.StatusOK || !strings.Contains(body, `"valid":true`) {
		t.Fatalf("preview = %d %s", code, body)
	}
	if code, body := sendJSON(t, e, http.MethodPut, "/api/routines/Smana/maku/doc-drift", web("maint"), `{"mode":"propose"}`); code != http.StatusOK || !strings.Contains(body, `"doc-drift"`) {
		t.Fatalf("put = %d %s", code, body)
	}
}
