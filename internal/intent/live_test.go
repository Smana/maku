// SPDX-License-Identifier: Apache-2.0

package intent_test

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/intent"
)

// TestSyntheticSetLive runs the committed set against a live model, on demand (`task intent:eval`):
// it is skipped unless INTENT_EVAL_URL (the gateway's base route) and INTENT_EVAL_KEY_FILE are set, so
// CI and `task check` never reach a model. INTENT_EVAL_MODEL names the alias (default tier-light). It
// prints the confusion matrix and the write-intent precision, and fails under 95 %: of every reply the
// classifier labelled steer, amend, stop or retry, the share the set labels the same.
func TestSyntheticSetLive(t *testing.T) {
	url, keyFile := os.Getenv("INTENT_EVAL_URL"), os.Getenv("INTENT_EVAL_KEY_FILE")
	if url == "" || keyFile == "" {
		t.Skip("set INTENT_EVAL_URL and INTENT_EVAL_KEY_FILE to run the synthetic set against a live model")
	}
	alias := os.Getenv("INTENT_EVAL_MODEL")
	if alias == "" {
		alias = "tier-light"
	}
	m := intent.Model{URL: url, Alias: alias, KeyFile: keyFile, Deadline: 20 * time.Second, HC: httpx.New(30*time.Second, nil)}

	f, err := os.Open("testdata/replies.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	matrix := map[intent.Intent]map[intent.Intent]int{}
	var writePredicted, writeRight int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var s struct{ Reply, Label string }
		if err := json.Unmarshal([]byte(sc.Text()), &s); err != nil {
			t.Fatal(err)
		}
		raw, _, err := m.Classify(t.Context(), s.Reply, "running")
		got := intent.Unknown
		if err != nil {
			t.Logf("%q: %v", s.Reply, err)
		} else {
			res, _ := intent.Parse(raw, s.Reply, 0.7)
			got = res.Intent
		}
		want := intent.Intent(s.Label)
		if matrix[want] == nil {
			matrix[want] = map[intent.Intent]int{}
		}
		matrix[want][got]++
		if got.Write() {
			writePredicted++
			if got == want {
				writeRight++
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%-8s", "want\\got")
	for _, g := range intent.All {
		fmt.Fprintf(&b, " %7s", g)
	}
	b.WriteString("\n")
	for _, w := range intent.All {
		fmt.Fprintf(&b, "%-8s", w)
		for _, g := range intent.All {
			fmt.Fprintf(&b, " %7d", matrix[w][g])
		}
		b.WriteString("\n")
	}
	t.Log("\n" + b.String())
	if writePredicted == 0 {
		t.Fatal("the model labelled no reply with a write intent: precision is undefined")
	}
	precision := float64(writeRight) / float64(writePredicted)
	t.Logf("write-intent precision: %d of %d = %.1f %%", writeRight, writePredicted, precision*100)
	if precision < 0.95 {
		t.Fatalf("write-intent precision %.1f %% is under 95 %%", precision*100)
	}
}
