// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"net/http"
	"strings"
	"testing"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/api"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/maku"
)

func makeQuestionIssue(author string) forge.Issue {
	return forge.Issue{
		Number: 7,
		Title:  "Fix the link",
		Body:   "## What is wrong\nx\n\n## What it should be\ny\n\n## Acceptance check\ntask check",
		State:  "OPEN",
		Author: author,
	}
}

func TestAnswerMaintainerWritesAndRestarts(t *testing.T) {
	tk := makeActTask("3buqdlot", factoryv1.PhaseEscalated, "needs_author")
	tk.Spec.IssueAuthor = "contributor"
	env := newActEnv(t, tk)
	initial := makeQuestionIssue("contributor")
	env.forge.SetIssue(initial)

	rec := postAct(t, env.srv, "slack", "/v1/tasks/3buqdlot/answers",
		`{"onBehalfOf":"slack:T01ABC/UMAINT","text":"use task check"}`)
	if got := actResult(t, rec); got != api.AnswerRestarted {
		t.Fatalf("result = %q, want %q", got, api.AnswerRestarted)
	}

	iss, err := env.forge.Issue(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	wantBody, err := maku.AppendDecision(initial.Body, "use task check (@Smana)")
	if err != nil {
		t.Fatal(err)
	}
	if iss.Body != wantBody {
		t.Fatalf("issue body = %q, want %q", iss.Body, wantBody)
	}

	old := reconcileTask(t, env.rec, "3buqdlot", 2)
	if old.Status.Phase != factoryv1.PhaseStopped || old.Status.Reason != "superseded" {
		t.Fatalf("old task = %s (%s), want Stopped (superseded)", old.Status.Phase, old.Status.Reason)
	}

	var list factoryv1.TaskList
	if err := env.client.List(t.Context(), &list); err != nil {
		t.Fatal(err)
	}
	var restarted *factoryv1.Task
	for i := range list.Items {
		if list.Items[i].Name != "3buqdlot" {
			restarted = &list.Items[i]
			break
		}
	}
	if restarted == nil {
		t.Fatal("expected a new Task to be created for the answered issue")
	}
	if !strings.Contains(restarted.Spec.Text, "D1: use task check (@Smana)") {
		t.Fatalf("new task snapshot = %q, want decision record D1: use task check (@Smana)", restarted.Spec.Text)
	}
	restarted = reconcileTask(t, env.rec, restarted.Name, 3)
	if restarted.Status.Phase != factoryv1.PhaseImplementing || len(env.runs.specs) != 1 {
		t.Fatalf("restarted task = %s, runs = %d, want Implementing with 1 scoper run", restarted.Status.Phase, len(env.runs.specs))
	}
	for _, s := range env.runs.specs {
		if s.Role != "triager" {
			t.Fatalf("run role = %q, want triager (scoper)", s.Role)
		}
	}
}

func TestAnswerAuthorWritesOnly(t *testing.T) {
	tk := makeActTask("4buqdlot", factoryv1.PhaseEscalated, "needs_author")
	tk.Spec.IssueAuthor = "contributor"
	env := newActEnv(t, tk)
	initial := makeQuestionIssue("contributor")
	env.forge.SetIssue(initial)

	rec := postAct(t, env.srv, "slack", "/v1/tasks/4buqdlot/answers",
		`{"onBehalfOf":"slack:T01ABC/UCONTRIB","text":"use task check"}`)
	if got := actResult(t, rec); got != api.AnswerWritten {
		t.Fatalf("result = %q, want %q", got, api.AnswerWritten)
	}

	iss, err := env.forge.Issue(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	wantBody, err := maku.AppendDecision(initial.Body, "use task check (@contributor)")
	if err != nil {
		t.Fatal(err)
	}
	if iss.Body != wantBody {
		t.Fatalf("issue body = %q, want %q", iss.Body, wantBody)
	}

	var list factoryv1.TaskList
	if err := env.client.List(t.Context(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("tasks count = %d, want 1 (author answer must not create a new task)", len(list.Items))
	}
}

func TestAnswerOthersRefused(t *testing.T) {
	tk := makeActTask("5buqdlot", factoryv1.PhaseEscalated, "needs_author")
	tk.Spec.IssueAuthor = "someone-else"
	env := newActEnv(t, tk)
	initial := makeQuestionIssue("someone-else")
	env.forge.SetIssue(initial)

	rec := postAct(t, env.srv, "slack", "/v1/tasks/5buqdlot/answers",
		`{"onBehalfOf":"slack:T01ABC/UNLINKED","text":"use task check"}`)
	if got := actResult(t, rec); got != api.AnswerNotLinked {
		t.Fatalf("unlinked result = %q, want %q", got, api.AnswerNotLinked)
	}

	rec = postAct(t, env.srv, "slack", "/v1/tasks/5buqdlot/answers",
		`{"onBehalfOf":"slack:T01ABC/UCONTRIB","text":"use task check"}`)
	if got := actResult(t, rec); got != api.AnswerNotAllowed {
		t.Fatalf("third-party result = %q, want %q", got, api.AnswerNotAllowed)
	}

	iss, err := env.forge.Issue(t.Context(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if iss.Body != initial.Body {
		t.Fatalf("issue body changed to %q, want unchanged %q", iss.Body, initial.Body)
	}
}

func TestAnswerNoOpenQuestion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		phase  string
		reason string
	}{
		{name: "running", phase: factoryv1.PhaseImplementing, reason: ""},
		{name: "escalated_other_reason", phase: factoryv1.PhaseEscalated, reason: "implementer-failed"},
		{name: "stopped", phase: factoryv1.PhaseStopped, reason: "manual"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tk := makeActTask("6buqdlot", tc.phase, tc.reason)
			env := newActEnv(t, tk)
			initial := makeQuestionIssue("Smana")
			env.forge.SetIssue(initial)

			rec := postAct(t, env.srv, "slack", "/v1/tasks/6buqdlot/answers",
				`{"onBehalfOf":"slack:T01ABC/UMAINT","text":"use task check"}`)
			if got := actResult(t, rec); got != api.AnswerNoQuestion {
				t.Fatalf("result = %q, want %q", got, api.AnswerNoQuestion)
			}
			iss, _ := env.forge.Issue(t.Context(), 7)
			if iss.Body != initial.Body {
				t.Fatalf("issue body changed to %q", iss.Body)
			}
		})
	}
}

func TestAnswerLoginNeverFromBody(t *testing.T) {
	tk := makeActTask("7buqdlot", factoryv1.PhaseEscalated, "needs_author")
	env := newActEnv(t, tk)
	env.forge.SetIssue(makeQuestionIssue("Smana"))

	rec := postAct(t, env.srv, "slack", "/v1/tasks/7buqdlot/answers",
		`{"onBehalfOf":"slack:T01ABC/UMAINT","text":"use task check","login":"Smana"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d (%s), want 400", rec.Code, rec.Body.String())
	}
}
