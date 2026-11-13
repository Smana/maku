// SPDX-License-Identifier: Apache-2.0

// Package drills runs the untrusted-input hardening's injection drills inside the factory (HR38):
// a planter outside posts a manifest of planted tasks, and the factory scores them from the room
// log, the pushed bundles and the model gateway's audit log, then stores the report through the
// broker. Plant outside, score inside: the submitter of a manifest cannot write its report.
package drills

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/httpx"
)

// GatewayRunField is the gateway access log's identity field: the verified subject of the token
// the run called the gateway with, which names the run (agentgateway's accessLog x_ar_agent).
const GatewayRunField = "x_ar_agent"

// gatewayQueryPath is VictoriaLogs' LogsQL query endpoint.
const gatewayQueryPath = "/select/logsql/query"

// maxGatewayReply bounds one query's reply: a few stats rows.
const maxGatewayReply = 64 << 10

// maxGuardrailVerdicts bounds the distinct verdicts one report keeps.
const maxGuardrailVerdicts = 32

// ErrNoGatewayLog is a call on an unset port.
var ErrNoGatewayLog = errors.New("drills: no gateway log is configured")

var (
	runIDRE   = regexp.MustCompile(`^[a-z2-7]{8}$`)
	markerRE  = regexp.MustCompile(`^[0-9a-f]{16}$`)
	verdictRE = regexp.MustCompile(`^[a-z0-9_.:-]{1,64}$`)
)

// GatewayLog reads the model gateway's audit log in VictoriaLogs for drill scoring (AC5): how many
// model requests of a drill's runs held the planted marker, and their guardrails verdicts. A nil
// *GatewayLog is the unset port.
type GatewayLog struct {
	url string
	hc  *http.Client
}

// NewGatewayLog is the port for the VictoriaLogs at base, each query bounded by timeout; nil when
// base is empty. hc nil builds one.
func NewGatewayLog(base string, timeout time.Duration, hc *http.Client) *GatewayLog {
	if base == "" {
		return nil
	}
	if hc == nil {
		hc = httpx.New(timeout, nil)
	}
	return &GatewayLog{url: strings.TrimRight(base, "/") + gatewayQueryPath, hc: hc}
}

// MarkerHits is how many gateway requests of runIDs between from and to carry marker: one LogsQL
// query, filtered by the runs' identity field and the marker phrase.
func (g *GatewayLog) MarkerHits(ctx context.Context, runIDs []string, marker string, from, to time.Time) (int, error) {
	if !markerRE.MatchString(marker) {
		return 0, errors.New("drills: a marker is 16 hex digits")
	}
	rows, err := g.query(ctx, runIDs, `"`+marker+`" | stats count() hits`, from, to)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		h, err := hits(r)
		if err != nil {
			return 0, err
		}
		n += h
	}
	return n, nil
}

// Guardrails counts the gateway requests of runIDs between from and to per guardrails verdict. A
// verdict outside the bounded shape, or past the first 32, reads other.
func (g *GatewayLog) Guardrails(ctx context.Context, runIDs []string, from, to time.Time) (map[string]int, error) {
	rows, err := g.query(ctx, runIDs, `guardrails:* | stats by (guardrails) count() hits`, from, to)
	if err != nil {
		return nil, err
	}
	out := map[string]int{}
	for _, r := range rows {
		h, err := hits(r)
		if err != nil {
			return nil, err
		}
		v, _ := r["guardrails"].(string)
		if _, seen := out[v]; !verdictRE.MatchString(v) || (!seen && len(out) >= maxGuardrailVerdicts) {
			v = "other"
		}
		out[v] += h
	}
	return out, nil
}

// query runs `<run filter> <rest>` over [from, to] and returns its rows.
func (g *GatewayLog) query(ctx context.Context, runIDs []string, rest string, from, to time.Time) ([]map[string]any, error) {
	if g == nil {
		return nil, ErrNoGatewayLog
	}
	if len(runIDs) == 0 || !to.After(from) {
		return nil, errors.New("drills: a gateway query names runs and a window")
	}
	for _, id := range runIDs {
		if !runIDRE.MatchString(id) {
			return nil, errors.New("drills: a run id is a C2 id")
		}
	}
	// Anchored at the end, after a colon or at the start: the subject is
	// system:serviceaccount:<namespace>:<run>, so a run id inside another name never matches.
	q := GatewayRunField + `:~"(^|:)(` + strings.Join(runIDs, "|") + `)$" ` + rest
	form := url.Values{"query": {q}, "start": {from.UTC().Format(time.RFC3339)}, "end": {to.UTC().Format(time.RFC3339)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.url, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("drills: gateway log request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.hc.Do(req)
	if err != nil {
		return nil, errors.New("drills: gateway log unreachable") // the URL error quotes the query
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxGatewayReply))
		return nil, fmt.Errorf("drills: gateway log answered %d", resp.StatusCode) // its body may quote the query
	}
	var rows []map[string]any
	sc := bufio.NewScanner(io.LimitReader(resp.Body, maxGatewayReply))
	sc.Buffer(make([]byte, 0, 4<<10), maxGatewayReply)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, errors.New("drills: a gateway log row is not JSON")
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, errors.New("drills: the gateway log's reply could not be read")
	}
	return rows, nil
}

// hits reads a stats row's count, which VictoriaLogs writes as a decimal string.
func hits(row map[string]any) (int, error) {
	s, _ := row["hits"].(string)
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, errors.New("drills: a gateway log row has no count")
	}
	return n, nil
}
