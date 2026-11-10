// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
	"github.com/Smana/maku/internal/factory/narrate"
)

// filingRig is an execute-mode service whose writes to the parent fail at the failAt-th write
// (counting status updates and spec patches), and one fake forge per repository.
type filingRig struct {
	s      *Service
	forges map[string]*forgetest.Fake
	writes int
	failAt int
}

var errCrash = errors.New("the controller crashed here")

func newFilingRig(t *testing.T) *filingRig {
	t.Helper()
	r := &filingRig{forges: map[string]*forgetest.Fake{repoA: forgetest.NewFake(), repoB: forgetest.NewFake()}}
	count := func() error {
		r.writes++
		if r.failAt > 0 && r.writes == r.failAt {
			return errCrash
		}
		return nil
	}
	c := fake.NewClientBuilder().WithScheme(testScheme()).WithStatusSubresource(&v1alpha1.Task{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
				if err := count(); err != nil {
					return err
				}
				return c.SubResource(sub).Update(ctx, obj, opts...)
			},
			Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, p client.Patch, opts ...client.PatchOption) error {
				if err := count(); err != nil {
					return err
				}
				return c.Patch(ctx, obj, p, opts...)
			},
		}).Build()
	cfg := testConfig(config.CoordinationExecute)
	cfg.FactoryLogin = "ogenki-agent-factory[bot]"
	at := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	r.s = &Service{Client: c, Namespace: ns, Cfg: cfg, Checks: newFakeChecks(), Prerequisites: fakePrereq{true, true},
		Now: func() time.Time { return at }, Forges: func(repo string) IssueForge {
			if f, ok := r.forges[repo]; ok {
				return f
			}
			return nil
		}}
	return r
}

// confirmAll drafts p and records the parent's and both children's confirmations.
func (r *filingRig) confirmAll(t *testing.T, p v1alpha1.CoordinationPlan) {
	t.Helper()
	mustDraft(t, r.s, p)
	ctx := context.Background()
	for _, c := range []struct {
		a  Actor
		in CoordinationConfirmation
	}{{smana, parentConfirm(p)}, {smana, childConfirm(p, "contract")}, {other, childConfirm(p, "consumer")}} {
		if _, err := r.s.Confirm(ctx, c.a, c.in); err != nil {
			t.Fatalf("confirm: %v", err)
		}
	}
	r.writes = 0
}

func (r *filingRig) issues(repo, label string) []forge.Issue {
	is, _ := r.forges[repo].IssuesLabelled(context.Background(), label)
	return is
}

// The parent is filed in the primary with only its output label, each child in its own repository
// with its own class label and never factory/child-of, every marker on the first line, and the
// plan reposted once as an inert block with public child links.
func TestCoordinationIssuesAreFiledWithTheirMarkers(t *testing.T) {
	r := newFilingRig(t)
	p := draftPlan(t)
	r.confirmAll(t, p)
	if err := r.s.File(context.Background(), r.s.Cfg.Current(), p.ID); err != nil {
		t.Fatal(err)
	}
	parents, contract, consumer := r.issues(repoA, narrate.LabelCoordination), r.issues(repoA, narrate.LabelCoordinationChild), r.issues(repoB, narrate.LabelCoordinationChild)
	if len(parents) != 1 || len(contract) != 1 || len(consumer) != 1 {
		t.Fatalf("issues %d/%d/%d", len(parents), len(contract), len(consumer))
	}
	hash := PlanHash(p)
	if !strings.HasPrefix(parents[0].Body, narrate.CoordinationParentMarker(p.ID, 1, hash)) || !slices.Equal(parents[0].Labels, []string{narrate.LabelCoordination}) {
		t.Fatalf("parent %q %v", parents[0].Body[:80], parents[0].Labels)
	}
	if !strings.HasPrefix(consumer[0].Body, narrate.CoordinationChildMarker(p.ID, "consumer", 1)) ||
		!slices.Equal(consumer[0].Labels, []string{narrate.LabelCoordinationChild, "class:scripts"}) {
		t.Fatalf("child %v", consumer[0].Labels)
	}
	for _, i := range append(append(parents, contract...), consumer...) {
		for _, l := range i.Labels {
			if strings.HasPrefix(l, "factory/child-of") || l == "factory/ready" || l == "factory/split-proposed" {
				t.Fatalf("issue %d carries %s", i.Number, l)
			}
		}
	}
	comments := r.forges[repoA].Comments(parents[0].Number)
	if len(comments) != 1 || !strings.HasPrefix(comments[0], narrate.CoordinationRepostMarker(p.ID, 1, hash)) ||
		!strings.Contains(comments[0], "https://github.com/"+repoB+"/issues/") {
		t.Fatalf("reposts %q", comments)
	}
	task := parentOf(t, r.s, p.ID)
	if task.Status.Reason != ReasonFiled || task.Spec.Issue != parents[0].Number || len(task.Status.Coordination.Children) != 2 {
		t.Fatalf("parent %s issue %d children %+v", task.Status.Reason, task.Spec.Issue, task.Status.Coordination.Children)
	}
	if err := r.s.File(context.Background(), r.s.Cfg.Current(), p.ID); err != nil || len(r.forges[repoA].Comments(parents[0].Number)) != 1 {
		t.Fatalf("a second File: %v", err)
	}
}

// Every repository check finishes before the first issue write: B's lost write files nothing in A.
func TestCoordinationFilingChecksEveryRepositoryFirst(t *testing.T) {
	r := newFilingRig(t)
	p := draftPlan(t)
	r.confirmAll(t, p)
	r.s.Checks.(*fakeChecks).perms[repoB]["other-maintainer"] = "read"
	if err := r.s.File(context.Background(), r.s.Cfg.Current(), p.ID); !errors.Is(err, ErrRepositoryPermission) {
		t.Fatalf("err = %v", err)
	}
	if n := len(r.forges[repoA].Created()) + len(r.forges[repoB].Created()); n != 0 {
		t.Fatalf("%d issues filed before every check passed", n)
	}
	r.s.Prerequisites = nil
	r.s.Checks.(*fakeChecks).perms[repoB]["other-maintainer"] = "write"
	if err := r.s.File(context.Background(), r.s.Cfg.Current(), p.ID); !errors.Is(err, ErrPrerequisites) || len(r.forges[repoA].Created()) != 0 {
		t.Fatalf("without prerequisites: %v", err)
	}
}

// Review Focus 1: a crash after any issue write but before its status write leaves the parent
// filing_incomplete with no child Task; each replay adopts by author and marker, never duplicates.
func TestFilingCrashReplayAdoptsWithoutStarting(t *testing.T) {
	// The parent's writes during File: 1 filing_incomplete, 2 the parent issue's link, 3 child A,
	// 4 child B, 5 filed.
	for _, tc := range []struct {
		name   string
		failAt int
	}{{"after the parent issue", 2}, {"after child A's issue", 3}, {"after child B's issue", 4}, {"before the final parent status", 5}} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFilingRig(t)
			p := draftPlan(t)
			r.confirmAll(t, p)
			r.failAt = tc.failAt
			if err := r.s.File(context.Background(), r.s.Cfg.Current(), p.ID); !errors.Is(err, errCrash) {
				t.Fatalf("err = %v, want the crash", err)
			}
			if got := parentOf(t, r.s, p.ID).Status.Reason; got != ReasonFilingIncomplete {
				t.Fatalf("reason %q after the crash", got)
			}
			r.failAt, r.writes = 0, 0
			if err := r.s.File(context.Background(), r.s.Cfg.Current(), p.ID); err != nil {
				t.Fatalf("replay: %v", err)
			}
			if a, b := len(r.forges[repoA].Created()), len(r.forges[repoB].Created()); a != 2 || b != 1 {
				t.Fatalf("issues %d in A, %d in B: want 2 and 1, none duplicated", a, b)
			}
			task := parentOf(t, r.s, p.ID)
			if task.Status.Reason != ReasonFiled || len(task.Status.Coordination.Children) != 2 {
				t.Fatalf("after replay %s %+v", task.Status.Reason, task.Status.Coordination.Children)
			}
			var l v1alpha1.TaskList
			if err := r.s.Client.List(context.Background(), &l); err != nil || len(l.Items) != 1 {
				t.Fatalf("%d tasks: filing started a child", len(l.Items))
			}
		})
	}
}

// A forged marker in an issue the factory did not author adopts nothing.
func TestCoordinationFilingAdoptsOnlyFactoryIssues(t *testing.T) {
	r := newFilingRig(t)
	p := draftPlan(t)
	r.confirmAll(t, p)
	r.forges[repoA].SetIssue(forge.Issue{Number: 3, Author: "mallory", Labels: []string{narrate.LabelCoordination},
		Body: narrate.CoordinationParentMarker(p.ID, 1, PlanHash(p))})
	if err := r.s.File(context.Background(), r.s.Cfg.Current(), p.ID); err != nil {
		t.Fatal(err)
	}
	if n := parentOf(t, r.s, p.ID).Spec.Issue; n == 3 {
		t.Fatal("the forged issue was adopted")
	}
}
