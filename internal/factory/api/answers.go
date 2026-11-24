// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/factory/taskid"
)

// AnswerForge reads and updates an issue's body as the factory App for POST /v1/tasks/{task}/answers.
type AnswerForge interface {
	Issue(ctx context.Context, number int) (forge.Issue, error)
	EditIssueBody(ctx context.Context, number int, body string) error
}

// AnswerRequest is the POST /v1/tasks/{task}/answers body. Unknown fields — login included — are
// refused: the actor is resolved from the link onBehalfOf names, never supplied in the body.
type AnswerRequest struct {
	OnBehalfOf string `json:"onBehalfOf"`
	Text       string `json:"text"`
}

// Closed result codes answered by POST /v1/tasks/{task}/answers.
const (
	AnswerRestarted   = "answered_restarted"
	AnswerWritten     = "answered"
	AnswerNotLinked   = "not_linked"
	AnswerNotAllowed  = "not_allowed"
	AnswerNoQuestion  = "no_question"
	AnswerUnknownTask = "unknown_task"

	maxAnswerText    = 2 << 10
	maxSnapshotBytes = 65536
)

var answerDecisionRE = regexp.MustCompile(`^([DA])([0-9]+)(?: \[(g[0-9]+)\])?: (.+)$`)

func (s *Server) answerForge(repository string) AnswerForge {
	if s.Answers != nil {
		if fg := s.Answers(repository); fg != nil {
			return fg
		}
	}
	if s.Forges != nil {
		if fg, ok := s.Forges(repository).(AnswerForge); ok && fg != nil {
			return fg
		}
	}
	if s.Readers != nil {
		if fg, ok := s.Readers(repository).(AnswerForge); ok && fg != nil {
			return fg
		}
	}
	return nil
}

func (s *Server) answerTask(w http.ResponseWriter, r *http.Request) {
	cfg := s.Cfg.Current()
	p, err := s.Auth.Authenticate(r)
	switch {
	case errors.Is(err, authn.ErrForbidden):
		refuse(w, http.StatusForbidden, "not_permitted")
		return
	case err != nil:
		refuse(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	sp := cfg.SponsoredBy(p.ID)
	if !p.Sponsored || p.Internal || sp == nil {
		refuse(w, http.StatusForbidden, reasonSponsoredOnly)
		return
	}
	ctx := r.Context()
	if s.Stopped != nil && s.Stopped(ctx) {
		refuse(w, http.StatusServiceUnavailable, "kill_switch")
		return
	}
	var in AnswerRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxActBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() || in.OnBehalfOf == "" || len(in.Text) > maxAnswerText {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	text, ok := cleanAnswerText(in.Text)
	if !ok {
		refuse(w, http.StatusBadRequest, "invalid_draft")
		return
	}
	if text == "" {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	if s.Redact == nil {
		s.failAct(ctx, w, p.ID, "intake_failed", errors.New("api: no redactor"))
		return
	}
	if _, rules := s.Redact.String(text); len(rules) > 0 {
		refuse(w, http.StatusBadRequest, "invalid_draft")
		return
	}
	taskName := r.PathValue("task")
	if !envelope.ValidID(taskName) {
		reply(w, http.StatusOK, ActResult{Result: AnswerUnknownTask})
		return
	}

	t, found, err := s.existing(ctx, taskName)
	if err != nil {
		s.failAct(ctx, w, p.ID, "tasks_unavailable", err)
		return
	}
	if !found {
		reply(w, http.StatusOK, ActResult{Result: AnswerUnknownTask})
		return
	}
	repo := canonicalRepository(sp.Repositories, t.Spec.Repository)
	rc := cfg.RepositoryFor(repo)
	if !slices.Contains(sp.Repositories, repo) || rc == nil {
		refuse(w, http.StatusForbidden, "repository_not_allowed")
		return
	}
	if t.Status.Phase != factoryv1.PhaseEscalated || t.Status.Reason != "needs_author" || t.Spec.Issue <= 0 {
		reply(w, http.StatusOK, ActResult{Result: AnswerNoQuestion})
		return
	}
	id, ok := strings.CutPrefix(in.OnBehalfOf, "slack:")
	team, user, slash := strings.Cut(id, "/")
	if !ok || !slash || team == "" || user == "" {
		reply(w, http.StatusOK, ActResult{Result: AnswerNotLinked})
		return
	}
	login, code, reason := s.onBehalfLogin(ctx, sp, in.OnBehalfOf, repo)
	switch reason {
	case "":
	case "not_linked":
		reply(w, http.StatusOK, ActResult{Result: AnswerNotLinked})
		return
	default:
		refuse(w, code, reason)
		return
	}

	fg := s.answerForge(repo)
	if fg == nil {
		s.failAct(ctx, w, p.ID, "forge_unavailable", errNoForge)
		return
	}
	iss, err := fg.Issue(ctx, t.Spec.Issue)
	if err != nil {
		s.failAct(ctx, w, p.ID, "forge_unavailable", err)
		return
	}

	isMaintainer := false
	if rc.IsMaintainer(login) {
		canWrite, err := s.actLoginWrites(ctx, repo, login)
		if err != nil {
			s.failAct(ctx, w, p.ID, "access_unverified", err)
			return
		}
		isMaintainer = canWrite
	}
	isAuthor := (iss.Author != "" && strings.EqualFold(iss.Author, login)) ||
		(t.Spec.IssueAuthor != "" && strings.EqualFold(t.Spec.IssueAuthor, login))
	if !isMaintainer && !isAuthor {
		reply(w, http.StatusOK, ActResult{Result: AnswerNotAllowed})
		return
	}

	newBody, err := appendIssueDecision(iss.Body, fmt.Sprintf("%s (@%s)", text, login))
	if err != nil {
		refuse(w, http.StatusConflict, "invalid_decisions")
		return
	}
	if err := fg.EditIssueBody(ctx, t.Spec.Issue, newBody); err != nil {
		s.failAct(ctx, w, p.ID, "forge_unavailable", err)
		return
	}
	iss.Body = newBody
	if !isMaintainer {
		reply(w, http.StatusOK, ActResult{Result: AnswerWritten})
		return
	}

	if err := s.restartAnsweredIssue(ctx, cfg, repo, t, iss, strings.ToLower(login)); err != nil {
		s.failAct(ctx, w, p.ID, "tasks_unavailable", err)
		return
	}
	reply(w, http.StatusOK, ActResult{Result: AnswerRestarted})
}

func cleanAnswerText(s string) (string, bool) {
	clean, rep := sanitize.Public(s)
	if rep.Withheld {
		return "", false
	}
	return strings.Join(strings.Fields(clean), " "), true
}

// appendIssueDecision appends one single-line D<n>: record to body's ## Decisions section using the
// same layout as internal/maku.AppendDecision, while keeping any trailing factory filing footer below
// the Decisions section so sanitize.DecisionRecords continues to validate the body.
func appendIssueDecision(body, answer string) (string, error) {
	mainBody, footer := splitFiledFooter(body)
	next, err := nextDecisionNumber(mainBody)
	if err != nil {
		return "", err
	}
	record := fmt.Sprintf("D%d: %s", next, answer)
	var updated string
	if hasHeadingLine(mainBody, "## Decisions") {
		sep := "\n"
		if strings.HasSuffix(mainBody, "\n") {
			sep = ""
		}
		updated = mainBody + sep + record
	} else {
		sep := "\n\n"
		switch {
		case mainBody == "" || strings.HasSuffix(mainBody, "\n\n"):
			sep = ""
		case strings.HasSuffix(mainBody, "\n"):
			sep = "\n"
		}
		updated = mainBody + sep + "## Decisions\n\n" + record
	}
	return updated + footer, nil
}

func splitFiledFooter(body string) (string, string) {
	const marker = "\n\n---\nFiled by the agent factory,"
	if idx := strings.LastIndex(body, marker); idx >= 0 {
		return body[:idx], body[idx:]
	}
	return body, ""
}

func hasHeadingLine(body, heading string) bool {
	for line := range strings.SplitSeq(body, "\n") {
		if strings.TrimSpace(line) == heading {
			return true
		}
	}
	return false
}

func nextDecisionNumber(body string) (int, error) {
	if hasHeadingLine(body, "## Acceptance check") {
		records, err := sanitize.DecisionRecords(body)
		if err != nil {
			return 0, err
		}
		if len(records) == 0 {
			return 1, nil
		}
		return records[len(records)-1].Number + 1, nil
	}
	inDecisions := false
	maxNum := 0
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "## Decisions" {
			inDecisions = true
			continue
		}
		if !inDecisions || trimmed == "" {
			continue
		}
		m := answerDecisionRE.FindStringSubmatch(line)
		if m == nil {
			return 0, errors.New("invalid_decisions")
		}
		n, err := strconv.Atoi(m[2])
		if err != nil || n <= maxNum {
			return 0, errors.New("invalid_decisions")
		}
		maxNum = n
	}
	return maxNum + 1, nil
}

// restartAnsweredIssue supersedes the bounced task and creates the next generation's Task for the
// issue, scoped again from the updated issue snapshot just as a re-applied factory/ready does.
func (s *Server) restartAnsweredIssue(ctx context.Context, cfg *config.Config, repo string, prev *factoryv1.Task, iss forge.Issue, login string) error {
	var list factoryv1.TaskList
	if err := s.Rooms.List(ctx, &list, client.InNamespace(s.Namespace)); err != nil {
		return err
	}
	var supersede []*factoryv1.Task
	taken := map[string]bool{}
	for i := range list.Items {
		item := &list.Items[i]
		taken[item.Name] = true
		if item.Spec.Issue != prev.Spec.Issue || !strings.EqualFold(item.Spec.Repository, repo) {
			continue
		}
		if factoryv1.TerminalPhase(item.Status.Phase) {
			continue
		}
		if item.Name == prev.Name || item.Status.Phase == factoryv1.PhaseEscalated {
			supersede = append(supersede, item)
		}
	}
	if !slices.ContainsFunc(supersede, func(t *factoryv1.Task) bool { return t.Name == prev.Name }) {
		supersede = append(supersede, prev)
	}
	for _, old := range supersede {
		if err := s.annotateTaskStop(ctx, old.Name, "superseded"); err != nil {
			return err
		}
	}

	gen := 1
	var key, name string
	for {
		key = taskid.IssueKey(repo, prev.Spec.Issue, gen)
		name = taskid.Name(key)
		if !taken[name] && name != prev.Name {
			break
		}
		gen++
	}

	text, _ := sanitize.Text(forge.SnapshotRaw(iss))
	if len(text) > maxSnapshotBytes {
		text = strings.ToValidUTF8(text[:maxSnapshotBytes], "")
	}
	sum := forge.SnapshotSHA256(iss)
	dataClass := prev.Spec.DataClass
	if dataClass == "" {
		dataClass = cfg.Defaults.DataClass
	}
	author := iss.Author
	if author == "" {
		author = prev.Spec.IssueAuthor
	}
	labels := map[string]string{factoryv1.LabelIssue: strconv.Itoa(prev.Spec.Issue)}
	var parent *factoryv1.Parent
	if prev.Spec.Parent != nil {
		cp := *prev.Spec.Parent
		parent = &cp
		labels[factoryv1.LabelParent] = cp.Task
	}
	next := &factoryv1.Task{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: s.Namespace,
			Labels:    labels,
		},
		Spec: factoryv1.TaskSpec{
			Source: factoryv1.Source{
				Kind:          "issue",
				Ref:           fmt.Sprintf("%s#%d", repo, prev.Spec.Issue),
				Key:           key,
				RequestedBy:   "github:" + login,
				Trust:         "untrusted",
				ContentSHA256: sum,
			},
			Repository:     repo,
			Issue:          prev.Spec.Issue,
			IssueAuthor:    author,
			Text:           text,
			DataClass:      dataClass,
			MaxMicroUSD:    prev.Spec.MaxMicroUSD,
			Parent:         parent,
			Origin:         prev.Spec.Origin,
			PredictedClass: prev.Spec.PredictedClass,
		},
	}
	if err := s.Rooms.Create(ctx, next); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (s *Server) annotateTaskStop(ctx context.Context, name, why string) error {
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{factoryv1.AnnotationStop: why}}})
	if err != nil {
		return err
	}
	obj := &factoryv1.Task{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: s.Namespace}}
	return s.Rooms.Patch(ctx, obj, client.RawPatch(types.MergePatchType, body))
}
