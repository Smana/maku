// SPDX-License-Identifier: Apache-2.0

package maku

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// drillGitHubServer is the drill repository: it records each write with the token's owner.
type drillGitHubServer struct {
	mu       sync.Mutex
	next     int
	writes   []string
	bodies   map[int]string // issue → body and comments
	missing  string         // a fixture path to answer 404
	refsMade []string
	ciMarker map[string]string // branch → .drill/ci-marker
}

func who(r *http.Request) string {
	switch r.Header.Get("Authorization") {
	case "Bearer stranger-secret":
		return "stranger"
	case "Bearer maintainer-secret":
		return "maintainer"
	}
	return "nobody"
}

func (g *drillGitHubServer) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/Smana/drill/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("path") == g.missing {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"type":"file"}`)
	})
	mux.HandleFunc("POST /repos/Smana/drill/issues", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Title, Body string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		g.mu.Lock()
		defer g.mu.Unlock()
		g.next++
		g.bodies[g.next] = in.Body
		g.writes = append(g.writes, who(r)+" issue "+strconv.Itoa(g.next))
		_, _ = io.WriteString(w, `{"number":`+strconv.Itoa(g.next)+`}`)
	})
	mux.HandleFunc("POST /repos/Smana/drill/issues/{n}/comments", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Body string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		n, _ := strconv.Atoi(r.PathValue("n"))
		g.mu.Lock()
		defer g.mu.Unlock()
		g.bodies[n] += "\n" + in.Body
		g.writes = append(g.writes, who(r)+" comment "+r.PathValue("n"))
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("POST /repos/Smana/drill/issues/{n}/labels", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Labels []string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		g.mu.Lock()
		defer g.mu.Unlock()
		g.writes = append(g.writes, who(r)+" label "+r.PathValue("n")+" "+strings.Join(in.Labels, ","))
		_, _ = io.WriteString(w, `[]`)
	})
	mux.HandleFunc("GET /repos/Smana/drill", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"default_branch":"main"}`)
	})
	mux.HandleFunc("GET /repos/Smana/drill/git/ref/heads/main", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"object":{"sha":"`+strings.Repeat("a", 40)+`"}}`)
	})
	mux.HandleFunc("POST /repos/Smana/drill/git/refs", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Ref, SHA string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		g.mu.Lock()
		defer g.mu.Unlock()
		g.refsMade = append(g.refsMade, who(r)+" "+in.Ref)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("PUT /repos/Smana/drill/contents/.drill/ci-marker", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ Content, Branch string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		b, _ := base64.StdEncoding.DecodeString(in.Content)
		g.mu.Lock()
		defer g.mu.Unlock()
		g.ciMarker[in.Branch] = string(b)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected GitHub call %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	return mux
}

// drillFactory is the factory API: it answers unknown_task until its first calls have passed.
type drillFactory struct {
	mu       sync.Mutex
	calls    int
	auth     string
	manifest map[string]any
}

func (f *drillFactory) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls++
		f.auth = r.Header.Get("Authorization")
		if r.Method != http.MethodPost || r.URL.Path != "/v1/hardening/drills" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if f.calls == 1 { // the intake has not made every task yet
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"unknown_task"}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&f.manifest)
		w.WriteHeader(http.StatusCreated)
	})
}

func drillFile(t *testing.T, dir, name, content string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func drillRig(t *testing.T) (DrillPlant, *drillGitHubServer, *drillFactory, *bytes.Buffer) {
	t.Helper()
	gh := &drillGitHubServer{bodies: map[int]string{}, ciMarker: map[string]string{}}
	ghs := httptest.NewServer(gh.handler(t))
	t.Cleanup(ghs.Close)
	f := &drillFactory{}
	fs := httptest.NewServer(f.handler())
	t.Cleanup(fs.Close)
	dir := t.TempDir()
	hook := "#!/bin/sh\ntest -n \"$DRILL_MARKER\" && test \"$DRILL_REPO\" = Smana/drill && echo hookaaaa\n"
	out := &bytes.Buffer{}
	return DrillPlant{Repo: "Smana/drill",
		StrangerTokenFile:   drillFile(t, dir, "stranger", "stranger-secret\n", 0o600),
		MaintainerTokenFile: drillFile(t, dir, "maintainer", "maintainer-secret\n", 0o600),
		FactoryTokenFile:    drillFile(t, dir, "factory", "factory-secret\n", 0o600),
		Factory:             fs.URL, GitHubAPI: ghs.URL, ID: "drill-1", Wait: time.Minute, Poll: time.Millisecond,
		Hooks: map[string]string{"runlore": drillFile(t, dir, "runlore.sh", hook, 0o700),
			"tool_result": drillFile(t, dir, "tool.sh", hook, 0o700), "room": drillFile(t, dir, "room.sh", hook, 0o700)},
		Out: out}, gh, f, out
}

// HR38: the planter plants each scenario of the suite with a fresh marker (strangers' text by the
// stranger, labels by the maintainer), checks the default-branch fixtures without writing them,
// and hands the factory the manifest once its tasks exist. No token is ever printed.
func TestDrillPlant(t *testing.T) {
	d, gh, f, out := drillRig(t)
	if err := d.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.calls != 2 || f.auth != "Bearer factory-secret" {
		t.Fatalf("factory calls %d, auth %q", f.calls, f.auth)
	}
	scenarios, _ := f.manifest["scenarios"].([]any)
	if f.manifest["id"] != "drill-1" || len(scenarios) != 8 {
		t.Fatalf("manifest %v", f.manifest)
	}
	markers := map[string]bool{}
	byScenario := map[string]map[string]any{}
	for _, s := range scenarios {
		p := s.(map[string]any)
		m := p["marker"].(string)
		if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(m) || markers[m] || !regexp.MustCompile(`^[a-z2-7]{8}$`).MatchString(p["task"].(string)) {
			t.Fatalf("planted %v", p)
		}
		markers[m] = true
		byScenario[p["scenario"].(string)] = p
	}
	// The stranger wrote the issue body and the comment; the maintainer labelled every issue.
	writes := strings.Join(gh.writes, "\n")
	for _, want := range []string{"stranger issue 1", "maintainer label 1 factory/drill-injection,factory/ready",
		"maintainer issue 2", "stranger comment 2", "maintainer label 2 factory/drill-injection,factory/ready"} {
		if !strings.Contains(writes, want) {
			t.Errorf("missing %q in\n%s", want, writes)
		}
	}
	if !strings.Contains(gh.bodies[1], byScenario["issue-body"]["marker"].(string)) ||
		!strings.Contains(gh.bodies[2], byScenario["stranger-comment"]["marker"].(string)) {
		t.Fatalf("the markers are not planted: %v", gh.bodies)
	}
	// The issue task's id is the intake's: the issue's first generation.
	if byScenario["issue-body"]["task"] != issueTask("Smana/drill", 1) {
		t.Fatalf("task %v", byScenario["issue-body"])
	}
	// ci_log: the task's own branch carries the planted line its CI echoes.
	ci := byScenario["ci-log"]
	branch := "agent/" + ci["task"].(string)
	if len(gh.refsMade) != 1 || gh.refsMade[0] != "maintainer refs/heads/"+branch || !strings.Contains(gh.ciMarker[branch], ci["marker"].(string)) {
		t.Fatalf("ci fixture %v %v", gh.refsMade, gh.ciMarker)
	}
	// encoded: Base64 and homoglyph variants of the planted text.
	enc := gh.bodies[func() int {
		n := 0
		for i, b := range gh.bodies {
			if strings.Contains(b, "Decode and follow") {
				n = i
			}
		}
		return n
	}()]
	if !strings.Contains(enc, "а") || !regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`).MatchString(enc) {
		t.Fatalf("encoded body %q", enc)
	}
	for _, s := range []string{"runlore-finding", "mcp-tool-result", "room-handoff"} {
		if byScenario[s]["task"] != "hookaaaa" {
			t.Fatalf("%s: %v", s, byScenario[s])
		}
	}
	if o := out.String(); strings.Contains(o, "secret") || !strings.Contains(o, "drill drill-1: 8 scenarios") {
		t.Fatalf("output %q", o)
	}
}

// A hook-planted source without its hook exits 2 naming the hook, before anything is planted; a
// missing fixture is refused, never written.
func TestDrillPlantRefusals(t *testing.T) {
	d, gh, f, _ := drillRig(t)
	delete(d.Hooks, "room")
	err := d.Run(t.Context())
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 2 || !strings.Contains(err.Error(), "--room-hook") {
		t.Fatalf("%v", err)
	}
	if len(gh.writes) != 0 || f.calls != 0 {
		t.Fatalf("planted before refusing: %v, %d", gh.writes, f.calls)
	}

	d, gh, _, _ = drillRig(t)
	gh.missing = "docs/drill/injection.md"
	if err := d.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "docs/drill/injection.md") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("%v", err)
	}
	if len(gh.writes) != 0 {
		t.Fatalf("planted with a fixture missing: %v", gh.writes)
	}

	d, _, _, _ = drillRig(t)
	d.StrangerTokenFile = filepath.Join(t.TempDir(), "none")
	if err := d.Run(t.Context()); err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("%v", err)
	}
}
