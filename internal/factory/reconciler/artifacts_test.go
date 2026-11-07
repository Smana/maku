// SPDX-License-Identifier: Apache-2.0

package reconciler

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/forge/forgetest"
)

const (
	artifactPath = "schemas/consumer-v1.json"
	producerHead = "b1b2c3d4e5f60718293a4b5c6d7e8f9011223344"
	producerMerg = "a1b2c3d4e5f60718293a4b5c6d7e8f9011223344"
	schemaBody   = `{"type":"object"}`
)

// repoFakes binds each coordination repository to its own factory App fake.
func repoFakes(g *rig) (a, b *forgetest.Fake) {
	a, b = forgetest.NewFake(), forgetest.NewFake()
	g.r.Clients = func(repo string) (TaskForge, forge.Merger, bool) {
		switch repo {
		case coordA:
			return a, a, true
		case coordB:
			return b, b, true
		}
		return nil, nil, false
	}
	return a, b
}

// landProducer records the producer child's pull request as GitHub reported it on the Task:
// merged at producerMerg when merged, else only open at producerHead.
func landProducer(t *testing.T, g *rig, name string, merged bool) {
	t.Helper()
	tk, ok := g.coordTask(t, name)
	if !ok {
		t.Fatalf("no producer task %s", name)
	}
	tk.Status.PullRequest = &v1alpha1.PullRequestRef{Number: 5, URL: "https://github.com/" + coordA + "/pull/5", HeadSHA: producerHead}
	if merged {
		at := metav1.NewTime(now)
		tk.Status.PullRequest.MergeCommitSHA, tk.Status.PullRequest.MergedAt = producerMerg, &at
		tk.Status.Phase = v1alpha1.PhaseDone
	}
	if err := g.c.Status().Update(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
}

// Review Focus 3, MR7: a dependent child has no Task until its producer's artifact is read, through
// the producer's own client, as the exact file at the producer PR's landed merge commit. A PR head,
// newer default-branch bytes, the consumer repository's copy, a wrong type, size or digest records
// no reference and creates nothing; only the exact file creates the reference and the Task, whose
// runs carry that reference alone.
func TestOnlyExactLandedArtifactUnlocks(t *testing.T) {
	exact := func(body string) func(a, b *forgetest.Fake) {
		return func(a, _ *forgetest.Fake) { a.SetFileAt(producerMerg, artifactPath, body) }
	}
	for _, tc := range []struct {
		name   string
		merged bool
		setup  func(a, b *forgetest.Fake)
		gate   error
		reason string // the dependent's reason on the parent; "" admits it
	}{
		{name: "the exact file at the merge commit", merged: true, setup: exact(schemaBody)},
		{name: "65536 bytes", merged: true, setup: exact(strings.Repeat("x", 65536))},
		{name: "not merged yet", setup: func(a, _ *forgetest.Fake) { a.SetFileAt(producerHead, artifactPath, schemaBody) }, reason: reasonWaitingDependency},
		{name: "only at the PR head", merged: true, setup: func(a, _ *forgetest.Fake) { a.SetFileAt(producerHead, artifactPath, schemaBody) }, reason: "artifact_unverified"},
		{name: "only on the newer default branch", merged: true, setup: func(a, _ *forgetest.Fake) { a.SetFile(artifactPath, schemaBody) }, reason: "artifact_unverified"},
		{name: "only in the consumer's repository", merged: true, setup: func(_, b *forgetest.Fake) { b.SetFileAt(producerMerg, artifactPath, schemaBody) }, reason: "artifact_unverified"},
		{name: "missing", merged: true, setup: func(*forgetest.Fake, *forgetest.Fake) {}, reason: "artifact_unverified"},
		{name: "a wrong path", merged: true, setup: func(a, _ *forgetest.Fake) { a.SetFileAt(producerMerg, "schemas/consumer-v2.json", schemaBody) }, reason: "artifact_unverified"},
		{name: "a wrong blob digest", merged: true, setup: func(a, _ *forgetest.Fake) {
			a.SetFileContentAt(producerMerg, forge.FileContent{Path: artifactPath, Bytes: []byte(schemaBody), BlobSHA: strings.Repeat("0", 40), SizeBytes: int64(len(schemaBody))})
		}, reason: "artifact_unverified"},
		{name: "a directory or symlink", merged: true, setup: exact(forgetest.NotAFile), reason: "artifact_unverified"},
		{name: "65537 bytes", merged: true, setup: exact(strings.Repeat("x", 65537)), reason: "artifact_unverified"},
		{name: "the execute gate refuses", merged: true, setup: exact(schemaBody), gate: coordination.ErrPrerequisites, reason: coordination.ReasonPrerequisites},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := coordinationPlan(t, false)
			g, fc := coordinationRig(t, config.CoordinationExecute, filedParent(p, coordination.ReasonFiled))
			a, b := repoFakes(g)
			cs := g.reconcile(t, p.ID, 2).Status.Coordination
			if cs.Children[0].Task == "" || cs.Children[1].Task != "" || cs.Children[1].Reason != reasonWaitingDependency {
				t.Fatalf("before the producer lands: %+v", cs.Children)
			}
			tc.setup(a, b)
			landProducer(t, g, cs.Children[0].Task, tc.merged)
			fc.ready["consumer"] = tc.gate
			parent := g.reconcile(t, p.ID, 2)
			cs = parent.Status.Coordination
			if parent.Status.Phase != v1alpha1.PhaseCoordinating || parent.Status.Reason != coordination.ReasonFiled {
				t.Fatalf("parent %s/%s", parent.Status.Phase, parent.Status.Reason)
			}
			if len(b.FileReads()) != 0 {
				t.Fatalf("read through the consumer's client: %v", b.FileReads())
			}
			if reads := a.FileReads(); slices.ContainsFunc(reads, func(r string) bool { return !strings.HasPrefix(r, producerMerg+":") }) {
				t.Fatalf("read at another commit than the merge: %v", reads)
			}
			consumer := cs.Children[1]
			if tc.reason != "" {
				if consumer.Task != "" || consumer.Reason != tc.reason {
					t.Fatalf("consumer %+v, want no task and %s", consumer, tc.reason)
				}
				if tc.reason != coordination.ReasonPrerequisites && len(cs.Artifacts) != 0 {
					t.Fatalf("a reference was recorded: %+v", cs.Artifacts)
				}
				return
			}
			fcont, _, _ := a.FileAtCommit(t.Context(), producerMerg, artifactPath)
			want := v1alpha1.LandedArtifact{ChildID: "contract", Name: "consumer-v1-schema", Repository: coordA, Path: artifactPath,
				MergeCommitSHA: producerMerg, BlobSHA: forgetest.BlobSHA(fcont.Bytes),
				ContentSHA256: fmt.Sprintf("%x", sha256.Sum256(fcont.Bytes)), SizeBytes: int64(len(fcont.Bytes))}
			if len(cs.Artifacts) != 1 || cs.Artifacts[0] != want || consumer.Task == "" || consumer.Reason != "" {
				t.Fatalf("artifacts %+v consumer %+v", cs.Artifacts, consumer)
			}
			child, ok := g.coordTask(t, consumer.Task)
			if !ok || child.Spec.Repository != coordB || !slices.Equal(child.Spec.CoordinationArtifacts, []v1alpha1.LandedArtifact{want}) {
				t.Fatalf("dependent %v %+v", ok, child.Spec)
			}
			ct := g.reconcile(t, child.Name, 3)
			if ct.Status.Phase != v1alpha1.PhaseImplementing || len(g.runs.specs) != 1 {
				t.Fatalf("dependent %s/%s, %d runs", ct.Status.Phase, ct.Status.Reason, len(g.runs.specs))
			}
			for _, s := range g.runs.specs {
				if s.Repository != coordB || !slices.Equal(s.CoordinationArtifacts, []v1alpha1.LandedArtifact{want}) {
					t.Fatalf("run %+v", s)
				}
				if err := s.Validate(); err != nil {
					t.Fatalf("the claim is one runs.Client creates: %v", err)
				}
			}
		})
	}
}

// A dependent child whose references are not its parent's recorded ones is refused before any run.
func TestDependentWithForeignArtifactsIsRefused(t *testing.T) {
	p := coordinationPlan(t, false)
	parent := filedParent(p, coordination.ReasonFiled)
	ref := v1alpha1.LandedArtifact{ChildID: "contract", Name: "consumer-v1-schema", Repository: coordA, Path: artifactPath,
		MergeCommitSHA: producerMerg, BlobSHA: strings.Repeat("b", 40), ContentSHA256: strings.Repeat("c", 64), SizeBytes: 3}
	parent.Status.Coordination.Artifacts = []v1alpha1.LandedArtifact{ref}
	child := coordination.ChildTask("agent-system", cfg(), parent, p.Children[1], 22, 1)
	child.CreationTimestamp = metav1.NewTime(now)
	forged := ref
	forged.ContentSHA256 = strings.Repeat("d", 64)
	child.Spec.CoordinationArtifacts = []v1alpha1.LandedArtifact{forged}
	g, _ := coordinationRig(t, config.CoordinationExecute, parent, child)
	ct := g.reconcile(t, child.Name, 3)
	if ct.Status.Phase != v1alpha1.PhaseRejected || ct.Status.Reason != "artifact_unverified" || len(g.runs.specs) != 0 {
		t.Fatalf("%s/%s, %d runs", ct.Status.Phase, ct.Status.Reason, len(g.runs.specs))
	}
}
