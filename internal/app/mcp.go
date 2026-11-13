// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/mcp"
	"github.com/Smana/maku/internal/metrics"
)

const (
	mcpAddr = ":8090"
	// mcpCallTimeout bounds one tool call, the store read or append and the
	// redaction included; its context ends with it.
	mcpCallTimeout = 15 * time.Second
	mcpDrain       = 10 * time.Second
)

// mcpKey is the key the MCPRoute injects (ruling P13). Unset, :8090 refuses
// every call rather than admit one that sends no key.
func mcpKey(getenv func(string) string) string {
	return strings.TrimSpace(getenv("ROOMS_MCP_KEY"))
}

// subPatterns are every run issuer's subject pattern: the router verified the
// run's token, and any issuer's run may call its tools (review M2).
func subPatterns(issuers []config.IssuerConfig) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(issuers))
	for _, is := range issuers {
		out = append(out, regexp.MustCompile(is.SubPattern)) // config.Validate compiled it
	}
	return out
}

// roomMCP is the :8090 room tools server over the broker's parts (§3): the run
// comes from the router's verified subject, matched by a run issuer's pattern.
func roomMCP(key string, patterns []*regexp.Regexp, roomLog mcp.Log, red mcp.Redactor, runs mcp.Runs,
	m *metrics.Set, log *slog.Logger,
) *mcp.Server {
	return &mcp.Server{Key: func() string { return key }, Runs: runs, SubPatterns: patterns,
		Tools: roomTools(roomLog, red, nil),
		OnReject: func(ctx context.Context, reason string) {
			m.Rejected.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
		},
		Logger: log, Now: time.Now}
}

// roomTools are :8090's tools: the room tools, then the safe-output tools, gated per run (HR14),
// so a run whose claim names no enforced type lists exactly the room tools. The store keeps push
// bundles and reads output answers (HR22, HR27); output_push is served only to a repository
// roomTools.pushRepositories names (stage H3), and only to a run whose claim names push.
func roomTools(roomLog mcp.Log, red mcp.Redactor, push []string) []mcp.Tool {
	out := mcp.OutputConfig{PushRepositories: push}
	if b, ok := roomLog.(mcp.BundleStore); ok {
		out.Bundles = b
	}
	if a, ok := roomLog.(mcp.OutputAnswers); ok {
		out.Answers = a
	}
	return append(mcp.RoomTools(roomLog, red, time.Now), mcp.OutputTools(roomLog, red, time.Now, out)...)
}

// roomToolsServer is :8090's handler under roomTools.auth. key (the default) is the router's injected
// ROOMS_MCP_KEY and verified subject over every run issuer's pattern, as every deployment has it.
// tokenreview never reads the key: the run's own token, reviewed by the apiserver for the room tools
// audience, names the subject, matched by the tokenreview run issuers' patterns alone.
func roomToolsServer(cfg config.Config, a auth, getenv func(string) string, roomLog mcp.Log, red mcp.Redactor, runs mcp.Runs,
	m *metrics.Set, log *slog.Logger,
) *mcp.Server {
	var s *mcp.Server
	if cfg.RoomToolsAuth() == config.RoomToolsAuthTokenReview {
		log.Info(":8090 verifies each run's own token (roomTools.auth tokenreview): ROOMS_MCP_KEY is not read")
		s = roomMCP("", a.roomPatterns, roomLog, red, runs, m, log)
		s.Tokens = a.roomTokens
	} else {
		key := mcpKey(getenv)
		if key == "" {
			log.Warn("ROOMS_MCP_KEY is not set: :8090 refuses every room tool call")
		}
		s = roomMCP(key, subPatterns(cfg.RunIssuers), roomLog, red, runs, m, log)
	}
	if push := cfg.PushRepositories(); len(push) > 0 { // the broker's own gate on output_push
		s.Tools = roomTools(roomLog, red, push)
	}
	return s
}

// mcpServer bounds :8090. Nothing on it streams, so WriteTimeout is set, and
// each call's context ends at callTimeout (mcpCallTimeout in the broker).
func mcpServer(h http.Handler, callTimeout time.Duration, log *slog.Logger) *http.Server {
	timed := http.TimeoutHandler(h, callTimeout, `{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"timed out"}}`)
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// TimeoutHandler writes its 503 on w as is: this is its Content-Type (review M3).
		w.Header().Set("Content-Type", "application/json")
		timed.ServeHTTP(w, r)
	}))
	return &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: callTimeout + 5*time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10,
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn)}
}
