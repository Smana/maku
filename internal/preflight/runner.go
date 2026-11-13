// SPDX-License-Identifier: Apache-2.0

package preflight

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// componentTimeout bounds one component's /preflight: its own checks run within 30 s (IN3).
const componentTimeout = 35 * time.Second

// Duration is a JSON duration string ("30s").
type Duration struct{ time.Duration }

// UnmarshalJSON reads "30s".
func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// ComponentRef is a component whose /preflight the runner collects: its name and its ops URL.
type ComponentRef struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// RunConfig is the preflight pod's config, rendered by the chart (preflight-config.yaml).
type RunConfig struct {
	Cluster    ClusterConfig  `json:"cluster"`
	Components []ComponentRef `json:"components"`
	Timeout    Duration       `json:"timeout"`
}

// LoadRunConfig reads the config strictly: a key the binary does not know fails the run.
func LoadRunConfig(path string) (RunConfig, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return RunConfig{}, fmt.Errorf("preflight config: %w", err)
	}
	var cfg RunConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return RunConfig{}, fmt.Errorf("preflight config %s: %w", path, err)
	}
	return cfg, nil
}

// Deps are the runner's clients: the API server through client and its version endpoint, and HTTP
// to the components' ops ports.
type Deps struct {
	Client  client.Reader
	Version Versioner
	HTTP    *http.Client
}

// representative is the catalog check an unreachable component is reported under.
var representative = map[string]string{"broker": "database", "factory": "apps", "run-controller": "run-path"}

// Run runs the cluster checks, collects every component's /preflight, writes the JSON lines to out
// and the summary to term (the pod's termination message), and returns the exit status.
func Run(ctx context.Context, d Deps, cfg RunConfig, out io.Writer, term string, strict bool) int {
	if cfg.Timeout.Duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout.Duration)
		defer cancel()
	}
	var r Report
	add := func(res Result) {
		if err := r.Add(res); err != nil {
			_ = r.Add(Result{Check: res.Check, Component: res.Component, Outcome: Fail, Code: Undecided, Message: "a result was withheld: " + err.Error()})
		}
	}
	for _, res := range ClusterChecks(ctx, d.Client, d.Version, cfg.Cluster) {
		add(res)
	}
	for _, comp := range cfg.Components {
		for _, res := range collect(ctx, d.HTTP, comp) {
			add(res)
		}
	}
	if err := r.WriteJSONLines(out); err != nil {
		return 1
	}
	if term != "" {
		_ = os.WriteFile(term, []byte(r.Summary()), 0o600) //nolint:gosec // the pod's termination file
	}
	return r.Exit(strict)
}

// collect reads one component's /preflight. An image without it answers 404: component_too_old.
func collect(ctx context.Context, hc *http.Client, comp ComponentRef) []Result {
	check := representative[comp.Name]
	if check == "" {
		check = "run-path"
	}
	failed := func(code, msg string) []Result {
		return []Result{{Check: check, Component: comp.Name, Outcome: Fail, Code: code, Message: msg}}
	}
	ctx, cancel := context.WithTimeout(ctx, componentTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(comp.URL, "/")+"/preflight", nil)
	if err != nil {
		return failed(Undecided, "the component's URL is malformed")
	}
	start := time.Now()
	resp, err := hc.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return failed(Undecided, comp.Name+" did not answer /preflight in time")
		}
		return failed(Undecided, comp.Name+" is unreachable on its ops port")
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return failed(ComponentTooOld, comp.Name+" serves no /preflight: upgrade its image to this chart's release")
	case resp.StatusCode != http.StatusOK:
		return failed(Undecided, fmt.Sprintf("%s answered /preflight with HTTP %d", comp.Name, resp.StatusCode))
	}
	doc, err := DecodeComponent(resp.Body)
	if err != nil {
		return failed(Undecided, comp.Name+": "+err.Error())
	}
	took := time.Since(start).Seconds()
	for i := range doc.Checks {
		doc.Checks[i].Component = comp.Name
		if doc.Checks[i].Seconds == 0 {
			doc.Checks[i].Seconds = took
		}
	}
	return doc.Checks
}
