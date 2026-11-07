// SPDX-License-Identifier: Apache-2.0

package coordination

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/taskid"
	"github.com/Smana/maku/internal/factory/triage"
)

// The integration purposes and result states (MR10), as the CRD enumerates them.
const (
	PurposeBeforeDependentMerge = "beforeDependentMerge"
	PurposeFinal                = "final"
	ResultPending               = "pending"
	ResultPassed                = "passed"
	ResultFailed                = "failed"
)

var (
	// ErrIntegrationManifest is a manifest that does not name its inputs exactly: a subject with
	// both or neither SHA, a wrong repository, a missing dependency merge or artifact, or another
	// plan's acceptance.
	ErrIntegrationManifest = errors.New("integration_manifest_invalid")
	// ErrIntegrationStale is an integration Task whose manifest its parent no longer holds: the
	// plan was replaced, or a recorded artifact is gone. It never runs.
	ErrIntegrationStale = errors.New("integration_stale")
)

var sha40 = regexp.MustCompile(`^[0-9a-f]{40}$`)

// IntegrationManifest is one integration attempt's immutable inputs (MR10): the exact child heads
// before a dependent merge, or every child's merge commit after the last, the confirmed plan's
// hash and acceptance, and the landed artifacts at the subjects' merge commits. Its canonical JSON
// is what the run's claim carries; nothing else is an input.
type IntegrationManifest struct {
	ParentTask  string                        `json:"parentTask"`
	PlanVersion int32                         `json:"planVersion"`
	PlanHash    string                        `json:"planHash"`
	Purpose     string                        `json:"purpose"`
	Subjects    []v1alpha1.IntegrationSubject `json:"subjects"`
	Acceptance  []v1alpha1.TaskAcceptance     `json:"acceptance"`
	Artifacts   []v1alpha1.LandedArtifact     `json:"artifacts,omitempty"`
}

// CanonicalManifest is m's canonical JSON: subjects sorted by child, artifacts by child and name,
// acceptance in its confirmed order, object keys sorted, as Canonical does for a plan.
func CanonicalManifest(m IntegrationManifest) ([]byte, error) {
	return canonicalJSON(normalizeManifest(m))
}

// ManifestHash is the lowercase hex SHA-256 of CanonicalManifest(m).
func ManifestHash(m IntegrationManifest) string {
	b, err := CanonicalManifest(m)
	if err != nil {
		panic(fmt.Sprintf("coordination: hash a manifest: %v", err)) // strings and integers only: never fails
	}
	return digest(b)
}

func normalizeManifest(m IntegrationManifest) IntegrationManifest {
	m.Subjects = slices.Clone(m.Subjects)
	m.Acceptance = slices.Clone(m.Acceptance)
	m.Artifacts = slices.Clone(m.Artifacts)
	slices.SortStableFunc(m.Subjects, func(a, b v1alpha1.IntegrationSubject) int { return cmp.Compare(a.ChildID, b.ChildID) })
	slices.SortStableFunc(m.Artifacts, func(a, b v1alpha1.LandedArtifact) int {
		return cmp.Or(cmp.Compare(a.ChildID, b.ChildID), cmp.Compare(a.Name, b.Name))
	})
	return m
}

// BeforeDependentManifest is dependent's pre-merge manifest (MR9): every dependency at the merge
// commit merges records for it, and dependent at its exact pull request head.
func BeforeDependentManifest(parent string, cs *v1alpha1.CoordinationStatus, dependent, head string, merges map[string]string) (IntegrationManifest, error) {
	c, ok := PlanChild(cs, dependent)
	if !ok || len(c.DependsOn) == 0 {
		return IntegrationManifest{}, fmt.Errorf("%w: %s is not a dependent child", ErrIntegrationManifest, dependent)
	}
	subjects := []v1alpha1.IntegrationSubject{{ChildID: c.ID, Repository: c.Repository, HeadSHA: head}}
	for _, from := range c.DependsOn {
		p, _ := PlanChild(cs, from)
		subjects = append(subjects, v1alpha1.IntegrationSubject{ChildID: p.ID, Repository: p.Repository, MergeCommitSHA: merges[from]})
	}
	m := manifestOf(parent, cs, PurposeBeforeDependentMerge, subjects)
	return m, ValidateManifest(m, cs)
}

// FinalManifest is the family's final manifest (MR10): every child at its landed merge commit.
func FinalManifest(parent string, cs *v1alpha1.CoordinationStatus, merges map[string]string) (IntegrationManifest, error) {
	if cs == nil || cs.Plan == nil {
		return IntegrationManifest{}, fmt.Errorf("%w: no plan", ErrIntegrationManifest)
	}
	var subjects []v1alpha1.IntegrationSubject
	for _, c := range cs.Plan.Children {
		subjects = append(subjects, v1alpha1.IntegrationSubject{ChildID: c.ID, Repository: c.Repository, MergeCommitSHA: merges[c.ID]})
	}
	m := manifestOf(parent, cs, PurposeFinal, subjects)
	return m, ValidateManifest(m, cs)
}

// manifestOf is the manifest of subjects under cs's current plan: its acceptance, and the landed
// artifacts recorded at the subjects' merge commits.
func manifestOf(parent string, cs *v1alpha1.CoordinationStatus, purpose string, subjects []v1alpha1.IntegrationSubject) IntegrationManifest {
	m := normalizeManifest(IntegrationManifest{ParentTask: parent, PlanVersion: cs.Plan.Version, PlanHash: cs.PlanHash, Purpose: purpose,
		Subjects: subjects, Acceptance: cs.Plan.Integration.Acceptance})
	for _, a := range cs.Artifacts {
		if slices.ContainsFunc(m.Subjects, func(s v1alpha1.IntegrationSubject) bool {
			return s.ChildID == a.ChildID && s.MergeCommitSHA != "" && s.MergeCommitSHA == a.MergeCommitSHA
		}) {
			m.Artifacts = append(m.Artifacts, a)
		}
	}
	return normalizeManifest(m)
}

// ValidateManifest proves m names its inputs exactly under cs's current plan: its plan hash and
// version, each subject a plan child at its own repository with exactly one 40-hex SHA, the
// purpose's subject set (a pre-dependent manifest: the dependent's head and each dependency's
// merge; a final one: every child's merge), the plan's acceptance and the recorded artifacts at
// the subjects' merges.
func ValidateManifest(m IntegrationManifest, cs *v1alpha1.CoordinationStatus) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrIntegrationManifest, fmt.Sprintf(format, args...))
	}
	if cs == nil || cs.Plan == nil || m.PlanHash != cs.PlanHash || m.PlanVersion != cs.Plan.Version {
		return bad("not the current plan")
	}
	heads, seen := 0, map[string]bool{}
	for _, s := range m.Subjects {
		c, ok := PlanChild(cs, s.ChildID)
		switch {
		case !ok || seen[s.ChildID]:
			return bad("subject %q is not one plan child", s.ChildID)
		case c.Repository != s.Repository:
			return bad("subject %s is not at its repository", s.ChildID)
		case (s.HeadSHA == "") == (s.MergeCommitSHA == ""):
			return bad("subject %s names both or neither SHA", s.ChildID)
		case !sha40.MatchString(s.HeadSHA + s.MergeCommitSHA):
			return bad("subject %s SHA is not 40-hex", s.ChildID)
		}
		seen[s.ChildID] = true
		if s.HeadSHA != "" {
			heads++
		}
	}
	switch m.Purpose {
	case PurposeFinal:
		if heads != 0 || len(m.Subjects) != len(cs.Plan.Children) {
			return bad("a final manifest is every child's merge commit")
		}
	case PurposeBeforeDependentMerge:
		dependent := ""
		for _, s := range m.Subjects {
			if s.HeadSHA != "" {
				dependent = s.ChildID
			}
		}
		c, _ := PlanChild(cs, dependent)
		if heads != 1 || len(c.DependsOn) == 0 || len(m.Subjects) != len(c.DependsOn)+1 {
			return bad("a pre-dependent manifest is the dependent's head and each dependency's merge")
		}
		for _, id := range c.DependsOn {
			if !seen[id] {
				return bad("dependency %s is missing", id)
			}
		}
	default:
		return bad("purpose %q", m.Purpose)
	}
	if !slices.Equal(m.Acceptance, cs.Plan.Integration.Acceptance) {
		return bad("not the confirmed acceptance")
	}
	want := manifestOf(m.ParentTask, cs, m.Purpose, m.Subjects)
	if !slices.Equal(normalizeManifest(m).Artifacts, want.Artifacts) {
		return bad("not the artifacts landed at its subjects")
	}
	return nil
}

// ManifestOf is integration Task t's manifest as its parent's status cs holds it now: ok only while
// cs is the plan t was created for and still records every artifact its subjects landed.
func ManifestOf(t *v1alpha1.Task, cs *v1alpha1.CoordinationStatus) (IntegrationManifest, error) {
	in, cp := t.Spec.Integration, t.Spec.CoordinationParent
	if in == nil || cp == nil {
		return IntegrationManifest{}, fmt.Errorf("%w: not an integration Task", ErrIntegrationStale)
	}
	if cs == nil || cs.Plan == nil || cs.PlanHash != in.PlanHash || cs.Plan.Version != cp.PlanVersion {
		return IntegrationManifest{}, fmt.Errorf("%w: the plan was replaced", ErrIntegrationStale)
	}
	m := manifestOf(cp.Task, cs, in.Purpose, in.Subjects)
	if err := ValidateManifest(m, cs); err != nil {
		return IntegrationManifest{}, fmt.Errorf("%w: %w", ErrIntegrationStale, err)
	}
	if taskid.Name(taskid.CoordinationIntegrationKey(cp.Task, in.Purpose, ManifestHash(m), attemptOf(t))) != t.Name {
		return IntegrationManifest{}, fmt.Errorf("%w: the manifest is not the one the Task was created for", ErrIntegrationStale)
	}
	return m, nil
}

// attemptOf is the attempt number t's key carries; 0 when it carries none.
func attemptOf(t *v1alpha1.Task) int {
	key := t.Spec.Source.Key
	i := strings.LastIndex(key, ":a")
	if i < 0 {
		return 0
	}
	n, _ := strconv.Atoi(key[i+2:])
	return n
}

// IntegrationTask is attempt n of manifest m under parent (MR2, MR10, MR17): a read-only Task in
// the primary repository, requested by the parent's confirmer and hashed to the confirmed plan,
// with one attempt's fixed cap, the primary child's tier, no issue, template, scope or branch,
// and its own Room under its own name, never the parent's. Its name is its key's.
func IntegrationTask(ns string, cfg *config.Config, parent *v1alpha1.Task, m IntegrationManifest, attempt int) *v1alpha1.Task {
	cs := parent.Status.Coordination
	p := *cs.Plan
	key := taskid.CoordinationIntegrationKey(parent.Name, m.Purpose, ManifestHash(m), attempt)
	primary, _ := find(p, primaryChildID(p))
	b := triage.BudgetFor(cfg, primary.Tier)
	ib := IntegrationBudget(p)
	b.RunMicroUSD, b.TaskMicroUSD = ib.RunMicroUSD, ib.TaskMicroUSD
	// An unpriced run holds a task to TaskTokens: bound them by the attempt's cap at the reference
	// rate, as a child's are, so the fallback cannot spend past it.
	if lt := int64(float64(ib.TaskMicroUSD) / config.RefUSDPerMToken); b.TaskTokens <= 0 || lt < b.TaskTokens {
		b.TaskTokens = lt
	}
	requester := ""
	if pc := parentConfirmation(cs); pc != nil {
		requester = v1alpha1.Person(pc.LinkedGitHubLogin)
	}
	return &v1alpha1.Task{
		ObjectMeta: metav1.ObjectMeta{Name: taskid.Name(key), Namespace: ns, Labels: map[string]string{v1alpha1.LabelCoordinationParent: parent.Name}},
		Spec: v1alpha1.TaskSpec{
			Kind: v1alpha1.TaskKindIntegration,
			Source: v1alpha1.Source{Kind: v1alpha1.SourceKindCoordination, Ref: "coordination:" + parent.Name + "/integration", Key: key,
				RequestedBy: requester, Trust: "untrusted", ContentSHA256: cs.PlanHash},
			Repository: p.Primary.Repository, Text: fmt.Sprintf("Integration verification (%s) of coordination %s, plan version %d.", m.Purpose, parent.Name, p.Version),
			DataClass: "public", Budget: b,
			CoordinationParent: &v1alpha1.CoordinationParent{Task: parent.Name, PlanVersion: p.Version},
			Integration:        &v1alpha1.IntegrationSpec{Purpose: m.Purpose, PlanHash: cs.PlanHash, Subjects: slices.Clone(m.Subjects)},
		},
	}
}

// PrimaryChild is the plan's child in its primary repository: the one whose execute authority an
// integration attempt re-reads.
func PrimaryChild(cs *v1alpha1.CoordinationStatus) string {
	if cs == nil || cs.Plan == nil {
		return ""
	}
	return primaryChildID(*cs.Plan)
}
