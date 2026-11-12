// SPDX-License-Identifier: Apache-2.0

package intent

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestIntentSpanIsSubstring(t *testing.T) {
	reply := "Hey  team, please   Use the staging DB for the next run, thanks!"
	tests := []struct {
		name, text, want string
	}{
		{"exact span", "use the staging db for the next run", "Use the staging DB for the next run"},
		{"case and whitespace folded", "PLEASE use   the staging", "please   Use the staging"},
		{"span at the very end", "thanks!", "thanks!"},
		{"paraphrase uses the whole reply", "switch to the staging database", reply},
		{"empty uses the whole reply", "", reply},
		{"blank uses the whole reply", "   ", reply},
		{"longer than the reply uses the whole reply", reply + " and more", reply},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Span(reply, tc.text); got != tc.want {
				t.Fatalf("Span = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseRefusesOutsideTheNine(t *testing.T) {
	tests := []struct{ name, raw string }{
		{"intent outside the nine", `{"intent":"merge","text":"","confidence":0.9}`},
		{"missing intent", `{"text":"","confidence":0.9}`},
		{"missing text", `{"intent":"stop","confidence":0.9}`},
		{"missing confidence", `{"intent":"stop","text":""}`},
		{"extra field", `{"intent":"stop","text":"","confidence":0.9,"why":"x"}`},
		{"not JSON", `stop the task`},
		{"two objects", `{"intent":"stop","text":"","confidence":0.9}{"intent":"retry","text":"","confidence":0.9}`},
		{"confidence above one", `{"intent":"stop","text":"","confidence":1.5}`},
		{"confidence below zero", `{"intent":"stop","text":"","confidence":-0.1}`},
		{"intent of the wrong type", `{"intent":7,"text":"","confidence":0.9}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, out := Parse([]byte(tc.raw), "stop the task", 0.7)
			if res.Intent != Unknown || out != OutcomeInvalidOutput {
				t.Fatalf("Parse = %+v, %s; want unknown, invalid_output", res, out)
			}
		})
	}
}

func TestLowConfidenceIsUnknown(t *testing.T) {
	raw := func(c string) []byte { return []byte(`{"intent":"stop","text":"","confidence":` + c + `}`) }
	if res, out := Parse(raw("0.69"), "stop", 0.7); res.Intent != Unknown || out != OutcomeLowConfidence {
		t.Fatalf("0.69 = %+v, %s; want unknown, low_confidence", res, out)
	}
	if res, out := Parse(raw("0.7"), "stop", 0.7); res.Intent != Stop || out != OutcomeOK {
		t.Fatalf("0.7 = %+v, %s; want stop, ok", res, out)
	}
}

func TestReadIntentsCarryNoText(t *testing.T) {
	for _, i := range []Intent{Status, Needs, Changes, PR, Stop, Retry} {
		raw := `{"intent":"` + string(i) + `","text":"whatever it quoted","confidence":0.9}`
		res, out := Parse([]byte(raw), "whatever it quoted", 0.7)
		if out != OutcomeOK || res.Intent != i || res.Text != "" {
			t.Errorf("%s = %+v, %s; want the intent with no text", i, res, out)
		}
	}
	res, _ := Parse([]byte(`{"intent":"steer","text":"use staging","confidence":0.9}`), "Please Use  staging", 0.7)
	if res.Intent != Steer || res.Text != "Use  staging" {
		t.Errorf("steer = %+v, want the reply's own spelling of the span", res)
	}
}

func TestWriteIntents(t *testing.T) {
	want := map[Intent]bool{Steer: true, Amend: true, Stop: true, Retry: true}
	for _, i := range All {
		if i.Write() != want[i] {
			t.Errorf("%s.Write() = %v", i, i.Write())
		}
	}
}

func TestTextHash(t *testing.T) {
	if !strings.HasPrefix(TextHash("a"), "sha256:") || len(TextHash("a")) != len("sha256:")+64 {
		t.Fatalf("TextHash = %q", TextHash("a"))
	}
	if TextHash("a") == TextHash("b") {
		t.Fatal("different spans, same hash")
	}
}

type sample struct {
	Reply string `json:"reply"`
	Label string `json:"label"`
	Text  string `json:"text"`
}

func TestSyntheticSet(t *testing.T) {
	f, err := os.Open("testdata/replies.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	per := map[Intent]int{}
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) == "" {
			continue
		}
		var s sample
		dec := json.NewDecoder(strings.NewReader(sc.Text()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			t.Fatalf("line %d: %v", n+1, err)
		}
		n++
		i := Intent(s.Label)
		if !i.Valid() {
			t.Fatalf("line %d: label %q is not one of the nine", n, s.Label)
		}
		per[i]++
		if s.Reply == "" {
			t.Fatalf("line %d: empty reply", n)
		}
		if i.carriesText() {
			if s.Text == "" {
				t.Errorf("line %d: a %s carries its text", n, i)
			}
			if fr, _ := fold(s.Reply); !strings.Contains(fr, mustFold(s.Text)) {
				t.Errorf("line %d: text %q is not a folded span of the reply", n, s.Text)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n < 60 {
		t.Errorf("%d lines, want at least 60", n)
	}
	for _, i := range All {
		if per[i] < 5 {
			t.Errorf("%s has %d lines, want at least 5", i, per[i])
		}
	}
}

func mustFold(s string) string { f, _ := fold(s); return f }
