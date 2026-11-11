// SPDX-License-Identifier: Apache-2.0

package roomview

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestDurationTable runs the case table the web helper's test reads too: one rule, two languages.
func TestDurationTable(t *testing.T) {
	raw, err := os.ReadFile("testdata/durations.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		MS   int64  `json:"ms"`
		Span string `json:"span"`
		Ago  string `json:"ago"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		d := time.Duration(r.MS) * time.Millisecond
		t.Run(strconv.FormatInt(r.MS, 10), func(t *testing.T) {
			if got := spanText(d); got != r.Span {
				t.Errorf("span = %q, want %q", got, r.Span)
			}
			if got := agoText(d); got != r.Ago {
				t.Errorf("ago = %q, want %q", got, r.Ago)
			}
		})
	}
	if spanText(-time.Second) != "0 s" || agoText(-time.Second) != "just now" {
		t.Error("a negative duration must read as 0")
	}
}
