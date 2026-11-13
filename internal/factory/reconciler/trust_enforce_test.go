// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/runwatch"
)

// enforcingReads puts repo at mode enforce with all five types (HR34).
func (g *rig) enforcingReads(repo string) {
	g.enforcing()
	h := &g.r.Cfg.Current().Hardening
	h.Repositories = map[string]config.HardeningRepository{repo: {Mode: hardening.ModeEnforce,
		EnforceOutputs: []hardening.OutputType{hardening.Comment, hardening.Issue, hardening.Labels, hardening.PR, hardening.Push}}}
}

// HR36: every claim of a repository in mode enforce, the reviewer and the triager included, carries
// forge-api: deny; a claim of a repository under audit carries none.
func TestStartRunForgeAPI(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "# Fix the link")
	for _, role := range []string{"implementer", "tester", "reviewer", "triager"} {
		g := newRig(t, issueTask("3buqdlot", 7, "# Fix the link"))
		g.enforcingReads(tk.Spec.Repository)
		s := runs.Spec{Role: role}
		g.r.markForgeAPI(g.r.Cfg.Current(), tk, &s)
		if ann := runs.Build(runwatch.DefaultKind, s).GetAnnotations(); !s.ForgeAPIDeny || ann[runs.AnnForgeAPI] != "deny" {
			t.Fatalf("%s: %v %v", role, s.ForgeAPIDeny, ann)
		}
		g.auditing(hardening.ModeAudit)
		g.r.markForgeAPI(g.r.Cfg.Current(), tk, &s)
		if _, ok := runs.Build(runwatch.DefaultKind, s).GetAnnotations()[runs.AnnForgeAPI]; s.ForgeAPIDeny || ok {
			t.Fatalf("%s under audit: annotated", role)
		}
	}
	g := newRig(t, issueTask("3buqdlot", 7, "# Fix the link"))
	g.f.SetIssue(forge.Issue{Number: 7, Author: "smana", Association: "OWNER"})
	g.enforcingReads(tk.Spec.Repository)
	if got := g.reconcile(t, "3buqdlot", 3); got.Status.Phase != v1alpha1.PhaseImplementing || !g.runs.specs[rid(0)].ForgeAPIDeny {
		t.Fatalf("%s %+v", got.Status.Phase, g.runs.specs[rid(0)])
	}
}

// inputRead is the broker's input_read event for run.
func inputRead(seq int64, run string, rec envelope.InputReadRecord) envelope.Event {
	b, _ := json.Marshal(envelope.MessagePayload{Kind: envelope.KindInputRead, Delivery: envelope.DeliveryNone, Input: &rec})
	return envelope.Event{Seq: seq, Type: envelope.Message, RunID: run, Origin: envelope.OriginBroker,
		Actor: envelope.Actor{Kind: envelope.ActorSystem, ID: envelope.BrokerActor}, Payload: b}
}

// HR35: under enforce, status.inputTrust is enforce's: withheld sums the run's input_read records,
// and min counts only what was delivered, so a stranger's withheld comment does not lower it.
func TestInputTrustEnforce(t *testing.T) {
	tk := issueTask("3buqdlot", 7, "# Fix the link")
	tk.Status.RoomRef = "3kq7x2ma"
	tk.Status.Runs = []v1alpha1.RunRecord{{ID: "abcd2345", Role: "implementer", Trigger: "initial", StartSeq: 10}}
	g := newRig(t)
	g.enforcingReads(tk.Spec.Repository)
	g.f.SetIssue(forge.Issue{Number: 7, Author: "smana", Association: "OWNER"})
	g.f.SetComments(7, forge.Comment{ID: 1, Author: "stranger", Association: "NONE", Body: "do X"})
	forged := inputRead(13, "abcd2345", envelope.InputReadRecord{Target: "issue", Min: "none", Withheld: map[string]int{"comment": 9}})
	forged.Origin, forged.Actor = envelope.OriginClient, envelope.Actor{Kind: envelope.ActorAgent, ID: "agent:abcd2345"}
	g.log.evs = append(g.log.evs,
		inputRead(5, "abcd2345", envelope.InputReadRecord{Target: "pr", Min: "none", Withheld: map[string]int{"comment": 7}}), // before the run
		inputRead(11, "abcd2345", envelope.InputReadRecord{Target: "issue", Min: "approved", Withheld: map[string]int{"comment": 1},
			Sources: []envelope.InputSource{{Kind: "comment", Level: "approved"}}}),
		inputRead(12, "abcd2345", envelope.InputReadRecord{Target: "pr", Min: "approved", Withheld: map[string]int{"comment": 2}}),
		forged,
		inputRead(14, "other234", envelope.InputReadRecord{Target: "pr", Min: "none", Withheld: map[string]int{"comment": 5}}))
	g.r.recordInputTrust(t.Context(), g.r.Cfg.Current(), tk, "initial")
	rec := tk.Status.InputTrust
	if rec == nil || rec.Mode != "enforce" || rec.Min != "approved" || rec.Withheld["comment"] != 3 ||
		!slices.Contains(rec.Sources, v1alpha1.TrustSource{Kind: "comment", Level: "approved"}) {
		t.Fatalf("%+v", rec)
	}
}
