// SPDX-License-Identifier: Apache-2.0

package hardening

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

// table is the minter's role table as Grants answers it: every sandbox role read-only, the
// publisher writing.
type table struct {
	roles map[string]map[string]string
	pub   map[string]string
}

func readOnlyTable() *table {
	t := &table{roles: map[string]map[string]string{}, pub: map[string]string{"contents": "write", "pull_requests": "write", "issues": "write"}}
	for _, r := range SandboxRoles() {
		t.roles[r] = map[string]string{"contents": "read", "pull_requests": "read", "metadata": "read"}
	}
	return t
}

func (t *table) grants(string) (map[string]map[string]string, map[string]string) {
	return maps.Clone(t.roles), maps.Clone(t.pub)
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

func hardened() (Settings, *table, *fakeDrills) {
	s := Settings{Mode: ModeEnforce, Layers: LayersFor(ModeEnforce, ModeAudit), ReadFilter: true,
		DrillMaxAge: 168 * time.Hour, ConfigHash: "cafe", Version: "v1.2.3", TokenSource: TokenSourceMinter, PublisherMinter: true}
	return s, readOnlyTable(), &fakeDrills{r: DrillRecord{ID: "drill-42", At: now.Add(-24 * time.Hour)}, ok: true}
}

func eval(s Settings, tb *table, d Drills) Evaluator {
	return Evaluator{Settings: func(string) Settings { return s }, Grants: tb.grants, Drills: d, Now: func() time.Time { return now }}
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
		breakIt func(*Settings, *table, *fakeDrills)
		want    Missing
	}{
		"R1 audit":         {func(s *Settings, _ *table, _ *fakeDrills) { s.Mode = ModeAudit }, MissingEnforce},
		"R2 a role writes": {func(_ *Settings, f *table, _ *fakeDrills) { f.roles["tester"] = map[string]string{"contents": "write"} }, MissingWriteToken},
		"R2 admin": {func(_ *Settings, f *table, _ *fakeDrills) {
			f.roles["reviewer"] = map[string]string{"administration": "admin"}
		}, MissingWriteToken},
		"R2 a shared token":          {func(s *Settings, _ *table, _ *fakeDrills) { s.TokenSource = TokenSourceShared }, MissingWriteToken},
		"R2 no token source":         {func(s *Settings, _ *table, _ *fakeDrills) { s.TokenSource = "" }, MissingWriteToken},
		"R3 no publisher row":        {func(_ *Settings, f *table, _ *fakeDrills) { f.pub = nil }, MissingPublisher},
		"R3 a read-only publisher":   {func(_ *Settings, f *table, _ *fakeDrills) { f.pub = map[string]string{"contents": "read"} }, MissingPublisher},
		"R3 not the minter":          {func(s *Settings, _ *table, _ *fakeDrills) { s.PublisherMinter = false }, MissingPublisher},
		"R4 a layer in audit":        {func(s *Settings, _ *table, _ *fakeDrills) { s.Layers.Paths = ModeAudit }, MissingLayer},
		"R5 no read filter":          {func(s *Settings, _ *table, _ *fakeDrills) { s.ReadFilter = false }, MissingReadFilter},
		"R6 no drill":                {func(_ *Settings, _ *table, d *fakeDrills) { d.ok = false }, MissingDrill},
		"R6 a stale drill":           {func(_ *Settings, _ *table, d *fakeDrills) { d.r.At = now.Add(-169 * time.Hour) }, MissingDrill},
		"R6 a drill from the future": {func(_ *Settings, _ *table, d *fakeDrills) { d.r.At = now.Add(time.Hour) }, MissingDrill},
		"R7 the demo":                {func(s *Settings, _ *table, _ *fakeDrills) { s.Demo = true }, MissingDemoProfile},
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
	d.err = errors.New("store down")
	if _, err := eval(s, f, d).Hardened(t.Context(), "o/r"); err == nil {
		t.Fatal("a drill store error passed")
	}
}

// Stage H0's answer: with no role table, no drill store and the mode off, every fact is missing
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

// Plan PR8 (HR42, option A): R2 and R3 read the minter's role table through Grants; a shared
// token makes R2 missing whatever the table says, and no table fails both.
func TestReadinessTokenSource(t *testing.T) {
	writeRoles := map[string]map[string]string{"implementer": {"contents": "write", "pull_requests": "read"}}
	for name, tc := range map[string]struct {
		src       TokenSource
		pubMinter bool
		table     func(*table)
		noGrants  bool
		want      []Missing
	}{
		"the minter, read-only roles":       {TokenSourceMinter, true, nil, false, nil},
		"the minter, a writing role":        {TokenSourceMinter, true, func(t *table) { t.roles = writeRoles }, false, []Missing{MissingWriteToken}},
		"the minter, a read-only publisher": {TokenSourceMinter, true, func(t *table) { t.pub = map[string]string{"contents": "read"} }, false, []Missing{MissingPublisher}},
		"the minter with no grants":         {TokenSourceMinter, true, nil, true, []Missing{MissingWriteToken, MissingPublisher}},
		"shared":                            {TokenSourceShared, true, nil, false, []Missing{MissingWriteToken}},
	} {
		t.Run(name, func(t *testing.T) {
			s, tb, d := hardened()
			s.TokenSource, s.PublisherMinter = tc.src, tc.pubMinter
			if tc.table != nil {
				tc.table(tb)
			}
			e := eval(s, tb, d)
			if tc.noGrants {
				e.Grants = nil
			}
			r, err := e.Hardened(t.Context(), "o/r")
			if err != nil || !slices.Equal(r.Missing, tc.want) || r.Hardened != (len(tc.want) == 0) {
				t.Fatalf("%+v %v, want missing %v", r, err, tc.want)
			}
		})
	}
}

// GrantsWrite reads write and admin, in any case, as a write.
func TestGrantsWrite(t *testing.T) {
	for _, tc := range []struct {
		perms map[string]string
		want  bool
	}{
		{map[string]string{"contents": "read"}, false},
		{map[string]string{"contents": "write"}, true},
		{map[string]string{"issues": "Write"}, true},
		{map[string]string{"administration": "ADMIN"}, true},
		{map[string]string{"checks": "none"}, false},
		{nil, false},
	} {
		if got := GrantsWrite(tc.perms); got != tc.want {
			t.Errorf("%v: %v, want %v", tc.perms, got, tc.want)
		}
	}
}
