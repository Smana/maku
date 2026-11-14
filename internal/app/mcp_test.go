// SPDX-License-Identifier: Apache-2.0

package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/mcp"
	"github.com/Smana/maku/internal/metrics"
	"github.com/Smana/maku/internal/redact"
	"github.com/Smana/maku/internal/runwatch"
)

// HR22, HR27: the broker's room log is the bundle store and the answers reader roomMCP finds by
// assertion, so output_push and output_status are served once a claim names push.
var (
	_ mcp.BundleStore   = (*meteredLog)(nil)
	_ mcp.OutputAnswers = (*meteredLog)(nil)
)

type fakeToolLog struct{ drafts []envelope.Draft }

func (l *fakeToolLog) Append(_ context.Context, d envelope.Draft) (envelope.Event, bool, error) {
	l.drafts = append(l.drafts, d)
	return envelope.Event{Seq: int64(len(l.drafts))}, false, nil
}

func (l *fakeToolLog) Range(context.Context, string, int64, int) ([]envelope.Event, error) {
	return nil, nil
}

// The :8090 wiring: the router's key, the run from its subject, the real
// redactor before the append, and refusals counted by reason.
func TestRoomMCP(t *testing.T) {
	red, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	exp, err := metrics.NewExporter(metrics.BrokerBuildInfo, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = exp.Shutdown(t.Context()) }()
	m, err := metrics.New(exp.Meter())
	if err != nil {
		t.Fatal(err)
	}
	runs := runwatch.New(runwatch.DefaultKind)
	runs.Upsert(t.Context(), &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "run-7f3cq2xz", "namespace": "agents"},
		"spec":     map[string]any{"roomRef": "3kq7x2ma", "role": "implementer"},
		"status":   map[string]any{"phase": "Running"},
	}})
	pattern := regexp.MustCompile(`^system:serviceaccount:agents:run-([a-z2-7]{8})$`)
	toolLog := &fakeToolLog{}
	pat := "ghp_" + rand.Text()[:26] + "Ab12CdE34f"
	srv := mcpServer(roomMCP("k", []*regexp.Regexp{pattern}, toolLog, red, runs, m, slog.New(slog.DiscardHandler)), mcpCallTimeout, slog.New(slog.DiscardHandler))
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.WriteTimeout == 0 || srv.IdleTimeout == 0 || srv.MaxHeaderBytes == 0 {
		t.Fatalf(":8090 streams nothing, so every bound is set: %+v", srv)
	}

	do := func(method, path, key string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "room_post", "arguments": map[string]any{"text": "the token is " + pat}}})
		r := httptest.NewRequestWithContext(t.Context(), method, path, bytes.NewReader(body))
		r.Header.Set(mcp.KeyHeader, key)
		r.Header.Set(mcp.IdentityHeader, "system:serviceaccount:agents:run-7f3cq2xz")
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, r)
		return rec
	}
	if rec := do(http.MethodPost, "/mcp", "k"); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "isError") {
		t.Fatalf("room_post = %d %s", rec.Code, rec.Body)
	}
	if len(toolLog.drafts) != 1 {
		t.Fatalf("%d appends", len(toolLog.drafts))
	}
	d := toolLog.drafts[0]
	if strings.Contains(string(d.Payload), pat) || !strings.Contains(string(d.Payload), "[REDACTED:github-pat]") ||
		strings.Join(d.Redactions, ",") != "github-pat" {
		t.Fatalf("stored %s, redactions %v", d.Payload, d.Redactions)
	}
	if rec := do(http.MethodPost, "/mcp", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong key = %d", rec.Code)
	}
	if rec := do(http.MethodPost, "/other", "k"); rec.Code != http.StatusNotFound {
		t.Fatalf("another path = %d", rec.Code)
	}
	scrape := httptest.NewRecorder()
	exp.Handler().ServeHTTP(scrape, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(scrape.Body)
	if !strings.Contains(string(body), `maku_rooms_rejected_actions_total{reason="mcp_key"} 1`+"\n") {
		t.Fatalf("the refusal is not counted:\n%s", body)
	}

	unset := mcpServer(roomMCP(mcpKey(func(string) string { return " \n" }), []*regexp.Regexp{pattern}, toolLog, red, runs, m, slog.New(slog.DiscardHandler)),
		mcpCallTimeout, slog.New(slog.DiscardHandler))
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	r.Header.Set(mcp.IdentityHeader, "system:serviceaccount:agents:run-7f3cq2xz")
	rec := httptest.NewRecorder()
	unset.Handler.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an unset ROOMS_MCP_KEY must refuse a request with no key: %d", rec.Code)
	}
}

func TestMCPKey(t *testing.T) {
	env := map[string]string{"ROOMS_MCP_KEY": " s3cret\n"}
	if got := mcpKey(func(k string) string { return env[k] }); got != "s3cret" {
		t.Fatalf("key = %q", got)
	}
}

// M2: a run of any configured issuer reaches its tools.
func TestSubPatterns(t *testing.T) {
	ps := subPatterns([]config.IssuerConfig{{SubPattern: `^a-([a-z2-7]{8})$`}, {SubPattern: `^b-([a-z2-7]{8})$`}})
	if len(ps) != 2 || !ps[1].MatchString("b-7f3cq2xz") {
		t.Fatalf("%v", ps)
	}
}

// M3 and the review's timeout gap: a call's context ends at the call timeout,
// and the 503 is a JSON-RPC error like every other.
func TestACallEndsAtItsTimeout(t *testing.T) {
	ended := make(chan error, 1)
	slow := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		ended <- r.Context().Err()
	})
	srv := mcpServer(slow, 50*time.Millisecond, slog.New(slog.DiscardHandler))
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(`{}`)))
	if err := <-ended; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("the call's ctx ended with %v", err)
	}
	var out struct {
		JSONRPC string `json:"jsonrpc"`
		Error   struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Content-Type") != "application/json" ||
		json.Unmarshal(rec.Body.Bytes(), &out) != nil || out.JSONRPC != "2.0" || out.Error.Code != -32603 {
		t.Fatalf("%d %q %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if mcpCallTimeout != 15*time.Second {
		t.Fatalf("the call timeout is %s", mcpCallTimeout)
	}
}

// Task 6: roomTools.auth picks :8090's credential. key reads ROOMS_MCP_KEY and the router's subject;
// tokenreview never reads the key, and admits the run's own room tools token alone.
func TestRoomToolsServerAuth(t *testing.T) {
	red, err := redact.New()
	if err != nil {
		t.Fatal(err)
	}
	m, err := metrics.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	runs := runwatch.New(runwatch.Kind{Group: "maku.ogenki.io", Namespace: "agents", NamePrefix: "run-"})
	runs.Upsert(t.Context(), &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "run-7f3cq2xz", "namespace": "agents"},
		"spec":     map[string]any{"roomRef": "3kq7x2ma", "role": "reviewer"},
		"status":   map[string]any{"phase": "Running"},
	}})
	const runSub = "system:serviceaccount:agents:run-7f3cq2xz"
	cfg := config.Config{RunIssuers: []config.IssuerConfig{{Mode: config.IssuerModeTokenReview,
		SubPattern: `^system:serviceaccount:agents:run-([a-z2-7]{8})$`}}, Human: localHuman()}
	api := &reviews{status: authenticated(runSub, authn.AudienceRoomTools)}
	provide := func() (authn.TokenReviewer, error) { return api, nil }
	getenv := func(k string) string { return map[string]string{"ROOMS_MCP_KEY": "k"}[k] }
	list := func(s http.Handler, hdr map[string]string) int {
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, r)
		return rec.Code
	}
	keyed := map[string]string{mcp.KeyHeader: "k", mcp.IdentityHeader: runSub}
	bearer := map[string]string{"Authorization": "Bearer tools-token"}
	log := slog.New(slog.DiscardHandler)

	a, err := authenticators(t.Context(), cfg, m, nil, provide)
	if err != nil {
		t.Fatal(err)
	}
	key := roomToolsServer(cfg, a, getenv, &fakeToolLog{}, red, runs, m, log)
	if got := list(key, keyed); got != http.StatusOK {
		t.Fatalf("key mode, the router's headers: %d", got)
	}
	if got := list(key, bearer); got != http.StatusUnauthorized {
		t.Fatalf("key mode, a bearer token alone: %d", got)
	}

	cfg.RoomTools = &config.RoomToolsConfig{Auth: config.RoomToolsAuthTokenReview}
	if a, err = authenticators(t.Context(), cfg, m, nil, provide); err != nil {
		t.Fatal(err)
	}
	tr := roomToolsServer(cfg, a, getenv, &fakeToolLog{}, red, runs, m, log)
	if got := list(tr, bearer); got != http.StatusOK {
		t.Fatalf("tokenreview mode, the run's token: %d", got)
	}
	if got := list(tr, keyed); got != http.StatusUnauthorized {
		t.Fatalf("tokenreview mode, the key and a subject header: %d", got)
	}
}

// HR35: :8090 serves forge_read only with the broker's token for the factory configured.
func TestForgeReadServedByConfig(t *testing.T) {
	if got := forgeReadTools(config.Config{}, &fakeToolLog{}); len(got) != 0 {
		t.Fatalf("served with no token: %v", got)
	}
	cfg := config.Config{FactoryURL: "https://factory.example", RoomTools: &config.RoomToolsConfig{ForgeReadTokenFile: "/var/run/secrets/maku/factory/token"}}
	if got := forgeReadTools(cfg, &fakeToolLog{}); len(got) != 1 || got[0].Name != "forge_read" {
		t.Fatalf("%v", got)
	}
}

// EX11: :8090 serves kb_search and kb_get only with roomTools.knowledge set, and only those it names.
func TestKnowledgeServedByConfig(t *testing.T) {
	m, err := metrics.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := knowledgeTools(config.Config{}, m); len(got) != 0 {
		t.Fatalf("served with no endpoint: %v", got)
	}
	k := &config.KnowledgeConfig{URL: "http://runlore.runlore.svc:8080/mcp", Tools: []string{"kb_get"}, Roles: []string{"triager"},
		Timeout: config.Duration{Duration: 10 * time.Second}, MaxBytes: 1 << 10}
	got := knowledgeTools(config.Config{RoomTools: &config.RoomToolsConfig{Knowledge: k}}, m)
	if len(got) != 1 || got[0].Name != "kb_get" || strings.Join(got[0].Roles, ",") != "triager" {
		t.Fatalf("%v", got)
	}
}
