// SPDX-License-Identifier: Apache-2.0

package api_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
)

// cancelEnv is newActEnv with three humans: a maintainer, the task's requester (no maintainer)
// and a writer who is neither, and no sponsored principal declared.
func cancelEnv(t *testing.T, tasks ...*factoryv1.Task) *actEnv {
	t.Helper()
	env := newActEnv(t, tasks[0])
	for _, tk := range tasks[1:] {
		if err := env.client.Create(t.Context(), tk); err != nil {
			t.Fatal(err)
		}
	}
	cfg := env.srv.Cfg.(*config.Config)
	cfg.API.Sponsored = nil // #644: the route is served with no sponsored principal
	env.srv.Auth = actTokens{
		"maint":     {Kind: envelope.ActorHuman, ID: "human:1", Sub: "zsub-smana"},
		"requester": {Kind: envelope.ActorHuman, ID: "human:2", Sub: "zsub-req"},
		"writer":    {Kind: envelope.ActorHuman, ID: "human:3", Sub: "zsub-contrib"},
		"slack":     {Kind: envelope.ActorSystem, ID: "system:slack-app", Sponsored: true, AccessToken: "slack"},
	}
	env.srv.Logins = actLogins{"zsub-smana": "Smana", "zsub-req": "requester", "zsub-contrib": "contributor"}
	env.writers["requester"] = true
	return env
}

func cancelTask(name string) *factoryv1.Task {
	tk := makeActTask(name, factoryv1.PhaseImplementing, "")
	tk.Spec.IssueAuthor = "requester"
	return tk
}

func TestCancelRouteServedWithoutSponsored(t *testing.T) {
	env := cancelEnv(t, cancelTask("3buqdlot"))
	if rec := postAct(t, env.srv, "maint", "/v1/tasks/3buqdlot/stops", `{"onBehalfOf":"slack:T01ABC/UMAINT"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("/stops without api.sponsored: got %d, want 404", rec.Code)
	}
	if got := actResult(t, postAct(t, env.srv, "maint", "/v1/tasks/3buqdlot/cancellations", `{}`)); got != "cancelled" {
		t.Fatalf("got %q, want cancelled", got)
	}
	if v := getActTask(t, env.client, "3buqdlot").Annotations[factoryv1.AnnotationStop]; v != "cancel" {
		t.Fatalf("stop annotation %q", v)
	}
}

func TestCancelNeedsOwnerOrMaintainer(t *testing.T) {
	env := cancelEnv(t, cancelTask("3buqdlot"), cancelTask("4buqdlot"))
	if got := actResult(t, postAct(t, env.srv, "writer", "/v1/tasks/3buqdlot/cancellations", `{}`)); got != "not_maintainer" {
		t.Fatalf("a writer who is neither: got %q", got)
	}
	if v := getActTask(t, env.client, "3buqdlot").Annotations[factoryv1.AnnotationStop]; v != "" {
		t.Fatalf("refused cancel annotated %q", v)
	}
	if got := actResult(t, postAct(t, env.srv, "requester", "/v1/tasks/4buqdlot/cancellations", `{"discard":true}`)); got != "discarded" {
		t.Fatalf("the requester: got %q", got)
	}
	rec := postAct(t, env.srv, "slack", "/v1/tasks/3buqdlot/cancellations", `{}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "human_only") {
		t.Fatalf("a sponsored principal: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postAct(t, env.srv, "", "/v1/tasks/3buqdlot/cancellations", `{}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", rec.Code)
	}
	if got := actResult(t, postAct(t, env.srv, "maint", "/v1/tasks/9zzzzzzz/cancellations", `{}`)); got != "unknown_task" {
		t.Fatalf("unknown: %q", got)
	}
}

// A cancel stops the task's runs and ends it Stopped, cancelled, with one comment on the issue.
func TestCancelStopsRunsAndEnds(t *testing.T) {
	env := cancelEnv(t, cancelTask("3buqdlot"))
	env.forge.SetIssue(forge.Issue{Number: 7, State: "OPEN"})
	if got := actResult(t, postAct(t, env.srv, "maint", "/v1/tasks/3buqdlot/cancellations", `{}`)); got != "cancelled" {
		t.Fatalf("got %q", got)
	}
	tk := reconcileTask(t, env.rec, "3buqdlot", 2)
	if tk.Status.Phase != factoryv1.PhaseStopped || tk.Status.Reason != "cancelled" {
		t.Fatalf("phase %s reason %s", tk.Status.Phase, tk.Status.Reason)
	}
	if calls := env.forge.Calls(); slices.ContainsFunc(calls, func(c string) bool {
		return strings.HasPrefix(c, "delete-branch") || strings.HasPrefix(c, "close-issue")
	}) {
		t.Fatalf("a cancel cleans nothing up: %v", calls)
	}
}

// A discard closes the pull request with a comment, deletes agent/<task> and closes the issue as
// not planned.
func TestDiscardCleansUp(t *testing.T) {
	tk := cancelTask("3buqdlot")
	tk.Status.PullRequest = &factoryv1.PullRequestRef{Number: 12, URL: "https://github.com/" + actRepo + "/pull/12"}
	env := cancelEnv(t, tk)
	env.forge.SetIssue(forge.Issue{Number: 7, State: "OPEN"})
	env.forge.SetPR(forge.PR{Number: 12, State: "OPEN"})
	if got := actResult(t, postAct(t, env.srv, "maint", "/v1/tasks/3buqdlot/cancellations", `{"discard":true}`)); got != "discarded" {
		t.Fatalf("got %q", got)
	}
	got := reconcileTask(t, env.rec, "3buqdlot", 2)
	if got.Status.Phase != factoryv1.PhaseStopped || got.Status.Reason != "discarded" {
		t.Fatalf("phase %s reason %s", got.Status.Phase, got.Status.Reason)
	}
	if !env.forge.Closed(12) {
		t.Fatal("the pull request is open")
	}
	if c := env.forge.Comments(12); len(c) == 0 || !strings.Contains(c[0], "discarded") {
		t.Fatalf("pull request comments %q", c)
	}
	calls := env.forge.Calls()
	for _, want := range []string{"delete-branch agent/3buqdlot", "close-issue-not-planned 7"} {
		if !slices.Contains(calls, want) {
			t.Fatalf("calls %v lack %q", calls, want)
		}
	}
	if c := env.forge.Comments(7); !slices.ContainsFunc(c, func(b string) bool { return strings.Contains(b, "cannot delete an issue") }) {
		t.Fatalf("issue comments %q", c)
	}
}

// The issue deleted on GitHub: a cancel still ends the task, and comments nowhere.
func TestCancelWithDeletedIssue(t *testing.T) {
	env := cancelEnv(t, cancelTask("3buqdlot"))
	env.forge.DeleteIssue(7)
	if got := actResult(t, postAct(t, env.srv, "maint", "/v1/tasks/3buqdlot/cancellations", `{"discard":true}`)); got != "discarded" {
		t.Fatalf("got %q", got)
	}
	tk := reconcileTask(t, env.rec, "3buqdlot", 2)
	if tk.Status.Phase != factoryv1.PhaseStopped || tk.Status.Reason != "discarded" {
		t.Fatalf("phase %s reason %s", tk.Status.Phase, tk.Status.Reason)
	}
	if c := env.forge.Comments(7); len(c) != 0 {
		t.Fatalf("comments on a deleted issue: %q", c)
	}
	if !slices.Contains(env.forge.Calls(), "delete-branch agent/3buqdlot") {
		t.Fatalf("calls %v", env.forge.Calls())
	}
}

// The reconcile finds the task's issue deleted and ends the task, issue_deleted, with no cancel.
func TestDeletedIssueEndsTask(t *testing.T) {
	env := cancelEnv(t, cancelTask("3buqdlot"))
	env.forge.DeleteIssue(7)
	tk := reconcileTask(t, env.rec, "3buqdlot", 2)
	if tk.Status.Phase != factoryv1.PhaseStopped || tk.Status.Reason != "issue_deleted" {
		t.Fatalf("phase %s reason %s", tk.Status.Phase, tk.Status.Reason)
	}
}
