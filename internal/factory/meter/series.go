// SPDX-License-Identifier: Apache-2.0

package meter

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// Series is one series of the meter's query that is a run's: its run, its labels as one key, its
// token type and served model when labelled, and its raw value.
type Series struct {
	Run, Key    string
	Typed       bool   // the series carries gen_ai_token_type
	Type, Model string // gen_ai_token_type, gen_ai_request_model ("" when unlabelled)
	Value       int64
}

// SeriesSource is a Source that also reads the query series by series. With Meter.PerReplica, the
// meter keeps one baseline per series (D10) rather than one per run's sum; without it, the series
// are summed per run and read as RunUsage would. VM implements it.
type SeriesSource interface {
	RunSeries(ctx context.Context) ([]Series, error)
}

// seriesKey is a series' labels as one string, sorted by name, __name__ left out: the identity
// D10 keeps a baseline by. A query that keeps the gateway replica (pod) apart gives each
// replica's counter its own key.
func seriesKey(metric map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(metric)) {
		if k == "__name__" {
			continue
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(strconv.Quote(metric[k]))
		b.WriteByte(',')
	}
	return b.String()
}

// seriesTotal is one series' last reading and the monotonic total the meter gave it.
type seriesTotal struct {
	last  Series
	total int64
}

// fold keeps each run's series monotonic one by one (D10) and returns them with their totals as
// values. A series' total grows by its value's increase, or by its whole value when the value
// fell, since its counter restarted from zero; a series first seen counts its whole value, since a
// new replica's counter starts at zero too. A series missing from this reply keeps its total and
// adds nothing, so a replica whose scrape drops out for a tick is not counted again when it
// returns. Only runs with a series in this reply are returned: a run with none is unread, as
// before.
func (m *Meter) fold(series []Series) []Series {
	if m.series == nil {
		m.series = map[string]map[string]seriesTotal{}
	}
	read := map[string]bool{}
	for _, s := range series {
		run := m.series[s.Run]
		if run == nil {
			run = map[string]seriesTotal{}
			m.series[s.Run] = run
		}
		p, known := run[s.Key]
		switch {
		case !known:
			p.total = s.Value
		case s.Value >= p.last.Value:
			p.total += s.Value - p.last.Value
		default: // a restarted counter: all it counts is new
			p.total += s.Value
		}
		p.last = s
		run[s.Key] = p
		read[s.Run] = true
	}
	var out []Series
	for _, id := range slices.Sorted(maps.Keys(read)) {
		run := m.series[id]
		for _, key := range slices.Sorted(maps.Keys(run)) {
			s := run[key].last
			s.Value = run[key].total
			out = append(out, s)
		}
	}
	return out
}

// usage is this tick's reading of each run. With PerReplica, a SeriesSource is read series by
// series and each kept monotonic on its own first (fold). Otherwise, a summed query's or any other
// Source's per-run reading is taken as it comes: folding a sum would charge every other replica
// again whenever one drops out or restarts.
func (m *Meter) usage(ctx context.Context) (map[string]Usage, error) {
	src, ok := m.Source.(SeriesSource)
	if !ok || !m.PerReplica {
		return m.Source.RunUsage(ctx)
	}
	series, err := src.RunSeries(ctx)
	if err != nil {
		return nil, err
	}
	return sumUsage(m.fold(series)), nil
}

// sumUsage sums series into each run's counters: a typed series counts as its type, and also
// under its served model; an untyped one is the token count itself.
func sumUsage(series []Series) map[string]Usage {
	out := map[string]Usage{}
	for _, s := range series {
		u, n := out[s.Run], s.Value
		if !s.Typed {
			u.Tokens += n
			out[s.Run] = u
			continue
		}
		mu := u.Models[s.Model]
		switch s.Type {
		case "input":
			u.Input, mu.Input = u.Input+n, mu.Input+n
			u.Tokens += n
			u.Typed = true
		case "output":
			u.Output, mu.Output = u.Output+n, mu.Output+n
			u.Tokens += n
			u.Typed = true
		case "cached_input":
			u.CacheRead, mu.CacheRead = u.CacheRead+n, mu.CacheRead+n
		case "cache_creation_input":
			u.CacheWrite, mu.CacheWrite = u.CacheWrite+n, mu.CacheWrite+n
		default:
			continue
		}
		if u.Models == nil {
			u.Models = map[string]ModelUsage{}
		}
		u.Models[s.Model] = mu
		out[s.Run] = u
	}
	return out
}

// countedType is a gen_ai_token_type the meter counts.
func countedType(typ string) bool {
	switch typ {
	case "input", "output", "cached_input", "cache_creation_input":
		return true
	}
	return false
}
