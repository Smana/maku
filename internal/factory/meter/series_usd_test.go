// SPDX-License-Identifier: Apache-2.0

package meter

import (
	"context"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/Smana/maku/internal/factory/api"
	"github.com/Smana/maku/internal/factory/runs"
)

// scripted is a SeriesSource that answers the series it is set to.
type scripted struct{ now []Series }

func (s *scripted) RunSeries(context.Context) ([]Series, error)        { return s.now, nil }
func (s *scripted) RunUsage(context.Context) (map[string]Usage, error) { return sumUsage(s.now), nil }

func typedSeries(pod, typ, model string, n int64) Series {
	return Series{Run: "aaaaaaaa", Key: pod + "/" + typ + "/" + model, Typed: true, Type: typ, Model: model, Value: n}
}

// pv is a pod's input and cached-input counters on a model.
type pv struct {
	pod, model string
	in, c      int64
}

func reply(ps ...pv) []Series {
	var out []Series
	for _, p := range ps {
		out = append(out, typedSeries(p.pod, "input", p.model, p.in), typedSeries(p.pod, "cached_input", p.model, p.c))
	}
	return out
}

// usdEnv is a priced run with a ledger in a fake cluster: the money path production caps on.
func usdEnv(t *testing.T) (func() (int64, int64), func(*scripted, bool) *Meter, func()) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	c := fake.NewClientBuilder().WithScheme(cmScheme()).Build()
	st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{glmRun("aaaaaaaa", now)}}
	read := func() (int64, int64) {
		var cm corev1.ConfigMap
		if err := c.Get(t.Context(), types.NamespacedName{Namespace: "agent-system", Name: api.LedgerPrefix + "20261022"}, &cm); err != nil {
			t.Fatal(err)
		}
		sp, _ := api.LedgerSpentColumn(&cm)
		se, _ := api.SettledColumn(&cm)
		return se["aaaaaaaa"], sp["github:alice"]
	}
	mk := func(s *scripted, per bool) *Meter {
		m := usdLedgerMeter(st, s, c, &now)
		m.PerReplica = per
		return m
	}
	proj := func() {
		if u := st.patches["aaaaaaaa"][runs.AnnUsage]; u != "" {
			st.runs[0].Tokens, _ = strconv.ParseInt(u, 10, 64)
		}
	}
	return read, mk, proj
}

func TestAFoldedRunIsChargedOnceInUSDAcrossDropoutAndRestart(t *testing.T) {
	read, mk, proj := usdEnv(t)
	s := &scripted{}
	m := mk(s, true)
	p := served["glm-5.3"]
	steps := []struct {
		r     []Series
		in, c int64
	}{
		{reply(pv{"a", "glm-5.3", 1000, 800}, pv{"b", "glm-5.3", 500, 400}), 1500, 1200},
		{reply(pv{"a", "glm-5.3", 1100, 880}), 1600, 1280},                               // b stale
		{reply(pv{"a", "glm-5.3", 1100, 880}, pv{"b", "glm-5.3", 550, 440}), 1650, 1320}, // b back
		{reply(pv{"a", "glm-5.3", 50, 40}, pv{"b", "glm-5.3", 550, 440}), 1700, 1360},    // a restarts
		{nil, 1700, 1360}, // both stale
		{reply(pv{"a", "glm-5.3", 60, 48}, pv{"b", "glm-5.3", 560, 448}), 1720, 1376}, // both back
	}
	for i, st := range steps {
		s.now = st.r
		if err := m.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		proj()
		want := cost(p, Usage{Input: st.in, CacheRead: st.c})
		se, sp := read()
		cc, _ := m.Costs.Get("aaaaaaaa")
		if se != want || sp != want || cc.MicroUSD != want {
			t.Fatalf("step %d: settled %d spent %d cost %d, want %d", i, se, sp, cc.MicroUSD, want)
		}
	}
}

func TestSwitchingToPerReplicaChargesNothingOnTheFirstTick(t *testing.T) {
	read, mk, proj := usdEnv(t)
	p := served["glm-5.3"]
	s := &scripted{}
	sum := func(in, c int64) []Series { return reply(pv{"sum", "glm-5.3", in, c}) }
	old := mk(s, false)
	for _, r := range [][2]int64{{1500, 1200}, {1100, 880}, {1650, 1320}} {
		s.now = sum(r[0], r[1])
		if err := old.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		proj()
	}
	before, _ := read() // includes H-4's over-count
	s2 := &scripted{now: reply(pv{"a", "glm-5.3", 1100, 880}, pv{"b", "glm-5.3", 550, 440})}
	nw := mk(s2, true)
	if err := nw.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	proj()
	if se, sp := read(); se != before || sp != before {
		t.Fatalf("first per-replica tick charged: settled %d spent %d, was %d", se, sp, before)
	}
	s2.now = reply(pv{"a", "glm-5.3", 1200, 960}, pv{"b", "glm-5.3", 550, 440})
	if err := nw.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	proj()
	inc := cost(p, Usage{Input: 1750, CacheRead: 1400}) - cost(p, Usage{Input: 1650, CacheRead: 1320})
	if se, _ := read(); se != before+inc {
		t.Fatalf("settled %d, want %d", se, before+inc)
	}
}

func TestSwitchingToPerRunChargesNothingOnTheFirstTick(t *testing.T) {
	read, mk, proj := usdEnv(t)
	p := served["glm-5.3"]
	s := &scripted{now: reply(pv{"a", "glm-5.3", 1000, 800}, pv{"b", "glm-5.3", 500, 400})}
	folded := mk(s, true)
	if err := folded.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	proj()
	s.now = reply(pv{"a", "glm-5.3", 50, 40}, pv{"b", "glm-5.3", 500, 400})
	if err := folded.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	proj()
	before, _ := read()
	if want := cost(p, Usage{Input: 1550, CacheRead: 1240}); before != want {
		t.Fatalf("settled %d, want %d", before, want)
	}
	s2 := &scripted{now: reply(pv{"sum", "glm-5.3", 550, 440})} // raw below the annotation
	nw := mk(s2, false)
	if err := nw.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	proj()
	if se, _ := read(); se != before {
		t.Fatalf("first per-run tick charged: settled %d, was %d", se, before)
	}
	s2.now = reply(pv{"sum", "glm-5.3", 650, 520})
	if err := nw.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	proj()
	inc := cost(p, Usage{Input: 650, CacheRead: 520}) - cost(p, Usage{Input: 550, CacheRead: 440})
	if se, _ := read(); se != before+inc {
		t.Fatalf("settled %d, want %d", se, before+inc)
	}
}
