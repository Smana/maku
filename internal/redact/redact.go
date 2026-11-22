// SPDX-License-Identifier: Apache-2.0

// Package redact removes secrets before anything reaches the log (SP2 §4, T8).
// It uses gitleaks' default rules; the test pins the four the design names.
package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/rs/zerolog"
	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/logging"
)

// ErrKeyCollision reports two object keys that are one once NULs and secrets
// are removed. Keeping either would drop the other's value, and which one
// depends on map order, so the payload is refused. The error never quotes a key.
var ErrKeyCollision = errors.New("redact: two object keys are the same once redacted")

// Redactor applies gitleaks' default rules to text and JSON payloads.
type Redactor struct{ det *detect.Detector }

// New parses gitleaks' default configuration and builds the detector once. It also silences
// gitleaks' own global logger, which logs each finding's secret at Trace to stderr,
// outside slog: call it at startup, before any scan runs.
func New() (*Redactor, error) {
	logging.Logger = zerolog.Nop()
	d, err := detect.NewDetectorDefaultConfig()
	if err != nil {
		return nil, err
	}
	// Honour no allow marker: gitleaks otherwise drops every finding on a line carrying one,
	// and the text we scan is harness output, which can carry anything.
	d.IgnoreGitleaksAllow = true
	return &Redactor{det: d}, nil
}

// String replaces each finding's secret with [REDACTED:<rule>]. DetectString scans into a
// per-call findings slice without mutating the shared Detector's state (only atomic TotalBytes).
func (r *Redactor) String(s string) (string, []string) {
	fired := map[string]bool{}
	return r.scan(s, fired), keys(fired)
}

func (r *Redactor) scan(s string, fired map[string]bool) string {
	for _, f := range r.det.DetectString(s) {
		secret := f.Secret
		if secret == "" {
			secret = f.Match
		}
		if secret == "" {
			continue
		}
		s = strings.ReplaceAll(s, secret, "[REDACTED:"+f.RuleID+"]")
		fired[f.RuleID] = true
	}
	return s
}

// Payload redacts every string of a JSON document, object keys included, and
// keeps its shape. Each string is scanned alone: a secret split across sibling
// fields is not detected, so callers must not split tokens.
//
// It honours ctx between strings, and an ended ctx fails the whole payload: it
// is never returned partly scanned. One string's scan is not interruptible:
// gitleaks' ctx-aware entry point takes its deprecated Fragment type, and the
// caller's body limit bounds a string anyway.
func (r *Redactor) Payload(ctx context.Context, raw json.RawMessage) (json.RawMessage, []string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, nil, err
	}
	fired := map[string]bool{}
	v, err := r.walk(ctx, v, fired)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("redact: %w", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, nil, err
	}
	return out, keys(fired), nil
}

func (r *Redactor) walk(ctx context.Context, v any, fired map[string]bool) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	switch t := v.(type) {
	case string:
		return r.scan(strings.ReplaceAll(t, "\x00", ""), fired), nil // jsonb refuses NUL (review I6)
	case []any:
		for i := range t {
			e, err := r.walk(ctx, t[i], fired)
			if err != nil {
				return nil, err
			}
			t[i] = e
		}
		return t, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			// A key is text like any other: an env dump puts secrets there.
			key := r.scan(strings.ReplaceAll(k, "\x00", ""), fired)
			if _, merged := out[key]; merged {
				return nil, ErrKeyCollision
			}
			val, err := r.walk(ctx, e, fired)
			if err != nil {
				return nil, err
			}
			out[key] = val
		}
		return out, nil
	default:
		return v, nil
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
