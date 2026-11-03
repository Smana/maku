// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Smana/maku/internal/envelope"
)

func TestEventsEndpoint(t *testing.T) {
	seqs := func(t *testing.T, body string) []int64 {
		t.Helper()
		var got struct {
			Events []envelope.Event `json:"events"`
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("%v: %s", err, body)
		}
		out := make([]int64, 0, len(got.Events))
		for _, ev := range got.Events {
			out = append(out, ev.Seq)
		}
		return out
	}
	for _, c := range []struct {
		name, query string
		n           int
		code        int
		first, last int64
		count       int
	}{
		{"a page after N", "?after=10&limit=5", 30, http.StatusOK, 11, 15, 5},
		{"the default limit", "", 700, http.StatusOK, 1, 500, 500},
		{"limit at the cap", "?limit=500", 700, http.StatusOK, 1, 500, 500},
		{"limit capped", "?limit=501", 30, http.StatusBadRequest, 0, 0, 0},
		{"after not a number", "?after=x", 30, http.StatusBadRequest, 0, 0, 0},
		{"after negative", "?after=-1", 30, http.StatusBadRequest, 0, 0, 0},
		{"limit zero", "?limit=0", 30, http.StatusBadRequest, 0, 0, 0},
		{"limit not a number", "?limit=x", 30, http.StatusBadRequest, 0, 0, 0},
		{"past the end", "?after=30", 30, http.StatusOK, 0, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t, abc(t))
			e.log.evs = nil
			e.log.add(c.n)
			code, body := get(t, e, "/api/rooms/"+roomID+"/events"+c.query, admin("boss"))
			if code != c.code {
				t.Fatalf("%d %s", code, body)
			}
			if code != http.StatusOK {
				return
			}
			got := seqs(t, body)
			if len(got) != c.count || (c.count > 0 && (got[0] != c.first || got[len(got)-1] != c.last)) {
				t.Fatalf("seqs %v, want %d from %d to %d", got, c.count, c.first, c.last)
			}
			if c.count == 0 && body != "{\"events\":[]}\n" {
				t.Fatalf("an empty page is %q", body)
			}
		})
	}

	t.Run("unreadable room", func(t *testing.T) {
		w := world(map[string][]string{"Smana/a": {"dev1"}}, "nolink")
		e := setup(t, abc(t), w.option())
		_, missing := get(t, e, "/api/rooms/6kq7x2md/events", header("dev1"))
		for _, room := range []string{roomB, roomC} {
			if code, body := get(t, e, "/api/rooms/"+room+"/events", header("dev1")); code != http.StatusNotFound || body != missing {
				t.Errorf("%s: %d %q, want %q", room, code, body, missing)
			}
		}
		if code, _ := get(t, e, "/api/rooms/"+roomID+"/events", header("dev1")); code != http.StatusOK {
			t.Errorf("a reader: %d", code)
		}
	})
	t.Run("log unreadable", func(t *testing.T) {
		e := setup(t, abc(t))
		e.srv.Log = failingLog{e.log}
		code, body := get(t, e, "/api/rooms/"+roomID+"/events", admin("boss"))
		if code != http.StatusServiceUnavailable || body != "room log unreadable\n" {
			t.Fatalf("%d %q", code, body)
		}
	})
	t.Run("no principal", func(t *testing.T) {
		e := setup(t, abc(t))
		if code, _ := get(t, e, "/api/rooms/"+roomID+"/events", nil); code != http.StatusUnauthorized {
			t.Fatalf("anonymous: %d", code)
		}
	})
}
