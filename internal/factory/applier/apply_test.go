// SPDX-License-Identifier: Apache-2.0

package applier

import (
	"strings"
	"testing"

	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/hardening"
)

// A run's follow-up issue is filed by the factory App, factory/proposed alone, never the trigger
// label (HR11): the draft's title and body, a paragraph naming its task, run and output, and the
// marker. A replay finds it and files nothing; the cap is one.
func TestApplyIssue(t *testing.T) {
	r := newRig(t, 7)
	id := r.request(t, runID, hardening.Issue, hardening.IssuePayload{Title: "docs: a follow-up", Body: issueMD})
	r.tick(t)
	a := r.only(t, id)
	created := r.fac.Created()
	if a.kind != envelope.KindOutputApplied || len(created) != 1 || len(r.pub.Calls()) != 0 {
		t.Fatalf("answer %+v, filed %q, publisher calls %q", a, created, r.pub.Calls())
	}
	title, rest, _ := strings.Cut(created[0], "\n")
	body, labels, _ := strings.Cut(rest, "\nlabels: ")
	if title != "docs: a follow-up" || labels != "factory/proposed" {
		t.Errorf("title %q, labels %q", title, labels)
	}
	for _, want := range []string{"## What is wrong\nx\n", "run `" + runID + "`", "task `" + taskID + "`",
		"https://github.com/owner/repo/issues/7", "output `" + id + "`"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body lacks %q:\n%s", want, body)
		}
	}
	if got, ok := hardening.FindMarker(body); !ok || got != id {
		t.Errorf("the body does not end with the marker:\n%s", body)
	}
	if !strings.HasPrefix(a.rec.Object, "https://github.com/owner/repo/issues/") {
		t.Errorf("object %q", a.rec.Object)
	}

	// A restart over the same log, with the answer lost: the marker is found, nothing is filed.
	restarted := newRig(t, 7)
	restarted.fac = r.fac
	restarted.a.Factory = func(string) Filer { return r.fac }
	restarted.request(t, runID, hardening.Issue, hardening.IssuePayload{Title: "docs: a follow-up", Body: issueMD})
	restarted.rooms.mu.Lock()
	restarted.rooms.events[len(restarted.rooms.events)-1].Payload = r.rooms.events[0].Payload // the same request
	restarted.rooms.mu.Unlock()
	restarted.tick(t)
	if len(r.fac.Created()) != 1 {
		t.Errorf("a replay filed again: %q", r.fac.Created())
	}
	if b := restarted.only(t, id); b.rec.Object != a.rec.Object {
		t.Errorf("the replay's object %q, want %q", b.rec.Object, a.rec.Object)
	}

	second := r.request(t, runID, hardening.Issue, hardening.IssuePayload{Title: "docs: another", Body: issueMD})
	r.tick(t)
	if b := r.only(t, second); b.kind != envelope.KindOutputRefused || b.rec.Reason != string(hardening.RefuseCap) {
		t.Fatalf("a second issue: %+v", b)
	}
	if len(r.fac.Created()) != 1 {
		t.Error("a second issue was filed")
	}
}

// Only the factory App's own issue carrying the marker is a replay: a copy anyone else filed is
// not, and the issue is filed.
func TestApplyIssueIgnoresAForeignMarker(t *testing.T) {
	r := newRig(t, 0)
	id := r.request(t, runID, hardening.Issue, hardening.IssuePayload{Title: "docs: a follow-up", Body: issueMD})
	r.fac.SetIssue(forge.Issue{Number: 40, Author: "mallory", Labels: []string{proposedLabel}, Body: "x\n\n" + hardening.Marker(id)})
	r.tick(t)
	if a := r.only(t, id); a.kind != envelope.KindOutputApplied || strings.HasSuffix(a.rec.Object, "/issues/40") {
		t.Fatalf("answer %+v", a)
	}
	if len(r.fac.Created()) != 1 {
		t.Errorf("filed %q", r.fac.Created())
	}
}
