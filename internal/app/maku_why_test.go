// SPDX-License-Identifier: Apache-2.0

package app

import (
	"strings"
	"testing"
)

// Developer review gap 8: maku why says why a task stopped, in words, what to do next, how its last
// run ended and where to look, without kubectl.
func TestWhySaysWhyAndWhatToDo(t *testing.T) {
	r, out := makuRig(t)
	r.broker.summary = `{"apiVersion":"summary/v1","room":"26zfnuxm","url":"https://rooms.example/r/26zfnuxm",` +
		`"status":{"phase":"Escalated","task":"3buqdlot","reason":"CI stayed red after the fix runs","reasonCode":"ci_red",` +
		`"lastJudge":{"verdict":"not_yet","reason":"the store test fails","evidence":["expected 200, got 404","line 2","line 3","line 4","line 5","line 6"],"at":"2026-11-26T10:00:00Z"},` +
		`"pr":{"number":12,"url":"https://github.com/acme/api/pull/12"}},` +
		`"runs":[{"id":"r1","role":"implementer","outcome":"succeeded"},{"id":"r2","role":"implementer","outcome":"failed"}],` +
		`"needsYou":[],"actions":[],"notes":{"untrusted":true,"items":[]},"cursor":"seq:9"}`
	if err := r.Run(t.Context(), []string{"why", "26zfnuxm"}); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Escalated: CI stayed red after the fix runs (ci_red)", "What to do:", "/factory retry",
		"Last run: r2, implementer, failed", "The judge: not_yet, the store test fails", "expected 200, got 404", "https://rooms.example/r/26zfnuxm",
		"https://github.com/acme/api/pull/12"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "line 6") || strings.Contains(got, "kubectl") {
		t.Errorf("at most five evidence lines, and no kubectl:\n%s", got)
	}
}

func TestWhyOfATaskThatWaitsForNothing(t *testing.T) {
	r, out := makuRig(t)
	if err := r.Run(t.Context(), []string{"why", "26zfnuxm"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "Implementing") || !strings.Contains(got, "nothing is stuck") {
		t.Errorf("%s", got)
	}
}
