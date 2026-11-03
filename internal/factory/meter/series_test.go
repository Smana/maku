// SPDX-License-Identifier: Apache-2.0

package meter

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/httpx"
)

// replicaReply is an api/v1/query reply in the shape a query grouped by (ar_agent, pod) returns:
// one series per gateway replica of run aaaaaaaa, untyped, with its raw counter. A replica left
// out is one whose scrape is stale this tick.
func replicaReply(pods map[string]int64) string {
	var rs []string
	for pod, n := range pods {
		rs = append(rs, fmt.Sprintf(`{"metric":{"ar_agent":"system:serviceaccount:agents:xplane-run-aaaaaaaa","pod":%q},"value":[1727430000,"%d"]}`, pod, n))
	}
	return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(rs, ",") + `]}}`
}

// vmReplies is a VM whose i-th query answers replies[i]; past the last, the last again.
func vmReplies(t *testing.T, replies ...string) *VM {
	t.Helper()
	var mu sync.Mutex
	i := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		body := replies[min(i, len(replies)-1)]
		i++
		mu.Unlock()
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	v, err := NewVM(srv.URL, "Q", httpx.New(5*time.Second, nil))
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// H-4, D10: the meter keeps one baseline per series, so a gateway replica's counter is counted
// once, whatever its scrape does; each case's usage is the run's total after each tick.
func TestEachReplicaIsCountedOnce(t *testing.T) {
	for name, c := range map[string]struct {
		ticks []map[string]int64
		usage []int64
	}{
		// B is stale for one tick: A's growth counts, B's total is kept, and B's return adds only
		// what it grew (a summed baseline re-baselined on the drop and added B's 450 again).
		"a replica drops out and returns": {
			ticks: []map[string]int64{{"a": 600, "b": 400}, {"a": 700}, {"a": 700, "b": 450}},
			usage: []int64{1000, 1100, 1150},
		},
		// A restarts: its counter falls to 50, all of it new; B's growth counts beside it.
		"a replica's counter restarts": {
			ticks: []map[string]int64{{"a": 600, "b": 400}, {"a": 50, "b": 420}, {"a": 80, "b": 430}},
			usage: []int64{1000, 1070, 1110},
		},
		// One series: the per-run behaviour, unchanged while the counter only grows.
		"one series": {
			ticks: []map[string]int64{{"a": 600}, {"a": 650}, {"a": 900}},
			usage: []int64{600, 650, 900},
		},
	} {
		t.Run(name, func(t *testing.T) {
			var replies []string
			for _, pods := range c.ticks {
				replies = append(replies, replicaReply(pods))
			}
			st := &store{patches: map[string]map[string]string{}, runs: []runs.Run{{ID: "aaaaaaaa", Phase: "Running"}}}
			m := &Meter{Runs: st, Source: vmReplies(t, replies...)}
			for i, want := range c.usage {
				if err := m.Tick(t.Context()); err != nil {
					t.Fatal(err)
				}
				if u := st.patches["aaaaaaaa"][runs.AnnUsage]; u != "" { // the composition projects it (SP1)
					st.runs[0].Tokens, _ = strconv.ParseInt(u, 10, 64)
				}
				if st.runs[0].Tokens != want {
					t.Fatalf("tick %d: usage %d, want %d", i+1, st.runs[0].Tokens, want)
				}
			}
		})
	}
}
