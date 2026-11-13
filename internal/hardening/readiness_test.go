// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

const (
	readOnly  = "issuer: https://token.actions.githubusercontent.com\nsubject: x\npermissions:\n  contents: read\n  pull_requests: read\n"
	writeable = "issuer: x\nsubject: y\npermissions:\n  contents: write\n  pull_requests: read\n"
	publisher = "issuer: x\nsubject: system:serviceaccount:agent-system:maku-factory\npermissions:\n  contents: write\n  pull_requests: write\n  issues: write\n"
)

type fakeFiles map[string]string

func (f fakeFiles) DefaultBranchFile(_ context.Context, _, path string) ([]byte, bool, error) {
	if s, ok := f[path]; ok {
		if s == "ERR" {
			return nil, false, errors.New("github: 502")
		}
		return []byte(s), true, nil
	}
	return nil, false, nil
}

type fakeDrills struct {
	r   DrillRecord
	ok  bool
	err error
	key DrillKey
}

func (f *fakeDrills) LastPass(_ context.Context, k DrillKey) (DrillRecord, bool, error) {
	f.key = k
	return f.r, f.ok, f.err
}

func hardened() (Settings, fakeFiles, *fakeDrills) {
	s := Settings{Mode: ModeEnforce, Layers: LayersFor(ModeEnforce, ModeAudit), ReadFilter: true,
		DrillMaxAge: 168 * time.Hour, ConfigHash: "cafe", Version: "v1.2.3"}
	files := fakeFiles{PublisherPolicyPath: publisher}
	for _, r := range SandboxRoles() {
		files[RolePolicyPath(r)] = readOnly
	}
	return s, files, &fakeDrills{r: DrillRecord{ID: "drill-42", At: now.Add(-24 * time.Hour)}, ok: true}
}

func eval(s Settings, f PolicyFiles, d Drills) Evaluator {
	return Evaluator{Settings: func(string) Settings { return s }, Policies: f, Drills: d, Now: func() time.Time { return now }}
}

// R1 to R7 all hold: hardened, citing the drill it relied on, keyed by the version and config hash.
func TestReadinessHardened(t *testing.T) {
	s, f, d := hardened()
	r, err := eval(s, f, d).Hardened(t.Context(), "o/r")
	if err != nil || !r.Hardened || len(r.Missing) != 0 || r.Drill != "drill-42" {
		t.Fatalf("%+v %v", r, err)
	}
	if d.key != (DrillKey{Version: "v1.2.3", ConfigHash: "cafe"}) {
		t.Fatalf("drill key %+v", d.key)
	}
}

// AC9 offline: each fact made false alone refuses, naming only its own Missing.
func TestReadinessEachFact(t *testing.T) {
	for name, tc := range map[string]struct {
		breakIt func(*Settings, fakeFiles, *fakeDrills)
		want    Missing
	}{
		"R1 audit":         {func(s *Settings, _ fakeFiles, _ *fakeDrills) { s.Mode = ModeAudit }, MissingEnforce},
		"R2 a role writes": {func(_ *Settings, f fakeFiles, _ *fakeDrills) { f[RolePolicyPath("tester")] = writeable }, MissingWriteToken},
		"R2 admin": {func(_ *Settings, f fakeFiles, _ *fakeDrills) {
			f[RolePolicyPath("reviewer")] = "permissions: {administration: admin}\n"
		}, MissingWriteToken},
		"R2 a policy that won't read": {func(_ *Settings, f fakeFiles, _ *fakeDrills) { f[RolePolicyPath("triager")] = "permissions: [x" }, MissingWriteToken},
		"R3 no publisher":             {func(_ *Settings, f fakeFiles, _ *fakeDrills) { delete(f, PublisherPolicyPath) }, MissingPublisher},
		"R3 a read-only publisher":    {func(_ *Settings, f fakeFiles, _ *fakeDrills) { f[PublisherPolicyPath] = readOnly }, MissingPublisher},
		"R4 a layer in audit":         {func(s *Settings, _ fakeFiles, _ *fakeDrills) { s.Layers.Paths = ModeAudit }, MissingLayer},
		"R5 no read filter":           {func(s *Settings, _ fakeFiles, _ *fakeDrills) { s.ReadFilter = false }, MissingReadFilter},
		"R6 no drill":                 {func(_ *Settings, _ fakeFiles, d *fakeDrills) { d.ok = false }, MissingDrill},
		"R6 a stale drill":            {func(_ *Settings, _ fakeFiles, d *fakeDrills) { d.r.At = now.Add(-169 * time.Hour) }, MissingDrill},
		"R6 a drill from the future":  {func(_ *Settings, _ fakeFiles, d *fakeDrills) { d.r.At = now.Add(time.Hour) }, MissingDrill},
		"R7 the demo":                 {func(s *Settings, _ fakeFiles, _ *fakeDrills) { s.Demo = true }, MissingDemoProfile},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, d := hardened()
			tc.breakIt(&s, f, d)
			r, err := eval(s, f, d).Hardened(t.Context(), "o/r")
			if err != nil || r.Hardened || !slices.Equal(r.Missing, []Missing{tc.want}) || r.Drill != "" {
				t.Fatalf("%+v %v, want only %s", r, err, tc.want)
			}
		})
	}
}

// A fact that cannot be read is an error, and an error is a refusal.
func TestReadinessErrors(t *testing.T) {
	s, f, d := hardened()
	f[RolePolicyPath("implementer")] = "ERR"
	if _, err := eval(s, f, d).Hardened(t.Context(), "o/r"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("%v", err)
	}
	s, f, d = hardened()
	d.err = errors.New("store down")
	if _, err := eval(s, f, d).Hardened(t.Context(), "o/r"); err == nil {
		t.Fatal("a drill store error passed")
	}
}

// Stage H0's answer: with no policy reader, no drill store and the mode off, every fact is missing
// and nothing is read twice or cached.
func TestReadinessH0(t *testing.T) {
	e := Evaluator{Settings: func(string) Settings { return Settings{Mode: ModeOff, Layers: LayersFor(ModeOff, ModeAudit)} }}
	r, err := e.Hardened(t.Context(), "o/r")
	want := []Missing{MissingEnforce, MissingWriteToken, MissingPublisher, MissingLayer, MissingReadFilter, MissingDrill}
	if err != nil || r.Hardened || !slices.Equal(r.Missing, want) {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := (Evaluator{}).Hardened(t.Context(), "o/r"); err == nil {
		t.Fatal("no settings is no answer")
	}
}

// countingFiles counts every policy read.
type countingFiles struct {
	fakeFiles
	reads int
}

func (c *countingFiles) DefaultBranchFile(ctx context.Context, repo, path string) ([]byte, bool, error) {
	c.reads++
	return c.fakeFiles.DefaultBranchFile(ctx, repo, path)
}

// Plan PR8 (HR42, option A): R2 and R3 read the token source runs actually get. The XR reads the
// octo-sts policies; the minter reads its role table through Grants and no policy file; a shared
// token makes R2 missing whatever the policies say.
func TestReadinessTokenSource(t *testing.T) {
	readRoles := map[string]map[string]string{}
	for _, r := range SandboxRoles() {
		readRoles[r] = map[string]string{"contents": "read", "pull_requests": "read", "metadata": "read"}
	}
	writeRoles := map[string]map[string]string{"implementer": {"contents": "write", "pull_requests": "read"}}
	pub := map[string]string{"contents": "read", "pull_requests": "write", "issues": "write"}
	grants := func(roles map[string]map[string]string, p map[string]string) func(string) (map[string]map[string]string, map[string]string) {
		return func(string) (map[string]map[string]string, map[string]string) { return roles, p }
	}
	for name, tc := range map[string]struct {
		src       TokenSource
		pubMinter bool
		grants    func(string) (map[string]map[string]string, map[string]string)
		want      []Missing
		reads     int
	}{
		"the XR, as today":                  {TokenSourceXR, false, nil, nil, 5},
		"no source reads as the XR":         {"", false, nil, nil, 5},
		"the minter, read-only roles":       {TokenSourceMinter, true, grants(readRoles, pub), nil, 0},
		"the minter, a writing role":        {TokenSourceMinter, true, grants(writeRoles, pub), []Missing{MissingWriteToken}, 0},
		"the minter, a read-only publisher": {TokenSourceMinter, true, grants(readRoles, map[string]string{"contents": "read"}), []Missing{MissingPublisher}, 0},
		"the minter with no grants":         {TokenSourceMinter, true, nil, []Missing{MissingWriteToken, MissingPublisher}, 0},
		"the minter, octo-sts publishes":    {TokenSourceMinter, false, grants(readRoles, nil), nil, 1},
		"shared":                            {TokenSourceShared, true, grants(readRoles, pub), []Missing{MissingWriteToken}, 0},
	} {
		t.Run(name, func(t *testing.T) {
			s, f, d := hardened()
			s.TokenSource, s.PublisherMinter = tc.src, tc.pubMinter
			files := &countingFiles{fakeFiles: f}
			e := eval(s, files, d)
			e.Grants = tc.grants
			r, err := e.Hardened(t.Context(), "o/r")
			if err != nil || !slices.Equal(r.Missing, tc.want) || r.Hardened != (len(tc.want) == 0) {
				t.Fatalf("%+v %v, want missing %v", r, err, tc.want)
			}
			if files.reads != tc.reads {
				t.Fatalf("%d policy reads, want %d", files.reads, tc.reads)
			}
		})
	}
}

func TestPolicyGrantsWrite(t *testing.T) {
	for in, want := range map[string]bool{readOnly: false, writeable: true, publisher: true, "issuer: x\n": false,
		"permissions: {issues: Write}\n": true, "permissions: {checks: none}\n": false} {
		got, err := PolicyGrantsWrite([]byte(in))
		if err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := PolicyGrantsWrite([]byte("permissions: 3\n")); err == nil {
		t.Fatal("a malformed policy read")
	}
}

// The gauge's source (Task 20): a policy's permissions, keys and access lower-cased.
func TestPolicyPermissions(t *testing.T) {
	got, err := PolicyPermissions([]byte("issuer: x\nsubject: y\npermissions:\n  Contents: Read\n  pull_requests: WRITE\n"))
	if err != nil || len(got) != 2 || got["contents"] != "read" || got["pull_requests"] != "write" {
		t.Fatalf("%v %v", got, err)
	}
	if got, err := PolicyPermissions([]byte("issuer: x\n")); err != nil || len(got) != 0 {
		t.Fatalf("no permissions: %v %v", got, err)
	}
	for _, bad := range []string{"permissions: 3\n", "permissions: [a]\n", ":\n- ["} {
		if _, err := PolicyPermissions([]byte(bad)); err == nil {
			t.Errorf("%q read", bad)
		}
	}
}
