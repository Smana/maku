// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/policyview"
	promotepkg "github.com/Smana/maku/internal/factory/promote"
	"github.com/Smana/maku/internal/runrequest"
	"github.com/Smana/maku/internal/store"
)

// PolicyGateway is the factory's policy routes as the broker forwards to them, with the caller's
// own token (merge policy stage 6). runrequest.Factory satisfies it.
type PolicyGateway interface {
	PolicyEvidence(ctx context.Context, accessToken, repository string) (evidence.Report, error)
	PolicyRepositories(ctx context.Context, accessToken string) (runrequest.PolicyIndex, error)
	PolicyPreview(ctx context.Context, accessToken, repository, file string) (policyview.Preview, error)
	PolicyDecisions(ctx context.Context, accessToken, repository string, pr int) (policyview.Timeline, error)
	PolicyRecentDecisions(ctx context.Context, accessToken, repository, since string) (policyview.Recent, error)
	PolicyBacktest(ctx context.Context, accessToken, repository string, request policyview.BacktestRequest, since string) (policyview.Backtest, error)
	PolicyPromote(ctx context.Context, accessToken, repository, class string, minimum int) (promotepkg.Result, error)
	PolicyPromotionStatus(ctx context.Context, accessToken, repository, class string, number int) (promotepkg.Status, error)
	PolicyDrill(ctx context.Context, accessToken, repository string, number int) (runrequest.PolicyDrillResult, error)
	// PolicyList, Policy and the two previews are the MergePolicies' pages (B16).
	PolicyList(ctx context.Context, accessToken string) ([]evidence.PolicyReport, error)
	Policy(ctx context.Context, accessToken, name string) (evidence.PolicyReport, error)
	PolicyPreviewBindings(ctx context.Context, accessToken, repository string, bindings []config.Binding) (policyview.BindingPreview, error)
	PolicyPreviewPolicy(ctx context.Context, accessToken, name string, spec config.PolicySpec) (policyview.PolicyPreview, error)
}

// PolicyRecords is the promotion workflow's audit; *store.Store implements it.
type PolicyRecords interface {
	RecordPromotion(ctx context.Context, p store.Promotion) (store.Promotion, error)
	OpenPromotion(ctx context.Context, id string, number int, url, branch string) (store.Promotion, error)
	MovePromotion(ctx context.Context, id, state string, pullRequest int, by string) (store.Promotion, error)
	Promotion(ctx context.Context, id string) (store.Promotion, error)
	RequestException(ctx context.Context, e store.Exception) (store.Exception, error)
	DecideException(ctx context.Context, id, state, by, note string, expires time.Time) (store.Exception, error)
	Exception(ctx context.Context, id string) (store.Exception, error)
	PolicyAudit(ctx context.Context, repository string) ([]store.Promotion, []store.Exception, error)
}

// Policy is the promotion workflow, policyManagement.promotion (stage 6): the settings pages, each
// class's evidence read through the factory, and the promotion and exception records. Maku records
// and links; a maintainer's own pull request changes the class (MP1, MP14).
type Policy struct {
	Factory PolicyGateway
	Records PolicyRecords
	// Operator is whether a GitHub login approves exceptions (policyManagement.operators).
	Operator func(login string) bool
	NewID    func() string
}

// ExceptionLifetime is how long an approved exception may carry a promotion.
const ExceptionLifetime = 14 * 24 * time.Hour

// maxPreviewBody bounds a proposed policy file: the file's own bound and its envelope.
const maxPreviewBody = mergepolicy.MaxFileBytes + 4<<10

// maxPolicyBody bounds a promotion or exception request: a justification of 2,000 characters and
// its envelope.
const maxPolicyBody = 8 << 10

// policyRoutes registers the settings pages and the policy API; only with Policy set.
func (s *Server) policyRoutes(mux *http.ServeMux, index http.Handler) {
	for _, p := range []string{"GET /settings", "GET /settings/merge-policies", "GET /settings/merge-policies/{owner}/{name}",
		"GET /settings/merge-policies/{owner}/{name}/decisions/{pr}", "GET /settings/evidence",
		// PR15: more specific than {owner}/{name}, so it wins; an owner named policies is reached
		// from the Evidence page's link only.
		"GET /settings/merge-policies/policies/{name}"} {
		mux.Handle(p, withCSP(index))
	}
	mux.HandleFunc("GET /api/policy/repositories", s.policyRepositories)
	mux.HandleFunc("GET /api/policy/repositories/{owner}/{name}", s.policyRepository)
	mux.HandleFunc("POST /api/policy/repositories/{owner}/{name}/preview", s.policyPreview)
	mux.HandleFunc("POST /api/policy/repositories/{owner}/{name}/backtest", s.policyBacktest)
	mux.HandleFunc("POST /api/policy/repositories/{owner}/{name}/drill", s.policyDrill)
	mux.HandleFunc("GET /api/policy/repositories/{owner}/{name}/decisions", s.policyRecentDecisions)
	mux.HandleFunc("GET /api/policy/repositories/{owner}/{name}/decisions/{pr}", s.policyDecisions)
	mux.HandleFunc("GET /api/policy/policies", s.policyList)
	mux.HandleFunc("GET /api/policy/policies/{name}", s.policyGet)
	mux.HandleFunc("POST /api/policy/policies/{name}/preview", s.policyPolicyPreview)
	mux.HandleFunc("POST /api/policy/promotions", s.requestPromotion)
	mux.HandleFunc("POST /api/policy/promotions/{id}", s.movePromotion)
	mux.HandleFunc("POST /api/policy/exceptions", s.requestException)
	mux.HandleFunc("POST /api/policy/exceptions/{id}", s.decideException)
}

// policyCaller is a human with a token: a machine reads and records nothing here.
func (s *Server) policyCaller(w http.ResponseWriter, r *http.Request) (authn.Principal, bool) {
	p, ok := s.principal(w, r)
	if !ok || !s.readAllowedJSON(w, r, p) {
		return p, false
	}
	if p.Kind != envelope.ActorHuman || p.Sponsored || p.AccessToken == "" {
		refuseJSON(w, http.StatusForbidden, "human_only")
		return p, false
	}
	return p, true
}

// policyWriter is the web UI's signed-in human, posting JSON from this broker's origin (P18: a
// maku or MCP bearer can set any Origin, so only the UI's session mutates promotions or exceptions).
func (s *Server) policyWriter(w http.ResponseWriter, r *http.Request) (authn.Principal, bool) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return p, false
	}
	if s.WebClient == nil || p.ClientID == "" || p.ClientID != s.WebClient() {
		refuseJSON(w, http.StatusForbidden, "web_only")
		return p, false
	}
	if want, o := origin(s.PublicURL), r.Header.Get("Origin"); want == "" || o == "" || origin(o) != want {
		refuseJSON(w, http.StatusForbidden, "cross_origin")
		return p, false
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		refuseJSON(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return p, false
	}
	return p, true
}

// policyPromotionWriter admits the human CLI and the browser. Browser sessions retain the origin
// check; a CLI token has none, but every caller must send JSON and prove maintainer access below.
func (s *Server) policyPromotionWriter(w http.ResponseWriter, r *http.Request) (authn.Principal, bool) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return p, false
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		refuseJSON(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return p, false
	}
	if s.WebClient != nil && p.ClientID != "" && p.ClientID == s.WebClient() {
		if want, o := origin(s.PublicURL), r.Header.Get("Origin"); want == "" || o == "" || origin(o) != want {
			refuseJSON(w, http.StatusForbidden, "cross_origin")
			return p, false
		}
	}
	return p, true
}

// roles are what the caller may do on one repository: their GitHub login, write (a maintainer
// requests), and operator (approves exceptions). Every write route asks again: nothing is taken
// from the page.
type roles struct {
	Login      string `json:"login"`
	Maintainer bool   `json:"maintainer"`
	Operator   bool   `json:"operator"`
}

func (s *Server) rolesOn(ctx context.Context, p authn.Principal, repository string) (roles, error) {
	if s.Identity == nil || s.Access == nil {
		return roles{}, errAccessUnverified
	}
	ctx, cancel := context.WithTimeout(ctx, accessWait)
	defer cancel()
	login, err := s.Identity.Login(ctx, p.Sub, repository)
	if err != nil || login == "" {
		return roles{}, err
	}
	writes, err := s.Access.CanWrite(ctx, repository, login)
	if err != nil {
		return roles{}, err
	}
	return roles{Login: login, Maintainer: writes, Operator: s.Policy.Operator != nil && s.Policy.Operator(login)}, nil
}

// report is the repository's evidence, read by the factory for this caller: a refusal is answered
// here and ok is false.
func (s *Server) report(w http.ResponseWriter, r *http.Request, p authn.Principal, repository string) (evidence.Report, bool) {
	rep, err := s.Policy.Factory.PolicyEvidence(r.Context(), p.AccessToken, repository)
	return rep, s.forwarded(w, err)
}

func classOf(rep evidence.Report, class string) (evidence.ClassReport, bool) {
	i := slices.IndexFunc(rep.Classes, func(c evidence.ClassReport) bool { return c.Class == class })
	if i < 0 {
		return evidence.ClassReport{}, false
	}
	return rep.Classes[i], true
}

// policyRepositories is GET /api/policy/repositories: the configured repositories the caller reads.
func (s *Server) policyRepositories(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return
	}
	rs, err := s.Policy.Factory.PolicyRepositories(r.Context(), p.AccessToken)
	if err != nil {
		refuseJSON(w, http.StatusServiceUnavailable, "evidence_unavailable")
		return
	}
	if rs.Repositories == nil {
		rs.Repositories = []string{}
	}
	writeJSON(w, rs) // maintains is null from an older factory: the gear then shows as before
}

// policyRepository is GET /api/policy/repositories/{owner}/{name}: the evidence the factory reads
// for this caller (a 404 for a repository they cannot read), then its promotion and exception
// records and what the caller may do there.
func (s *Server) policyRepository(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return
	}
	rep, ok := s.report(w, r, p, r.PathValue("owner")+"/"+r.PathValue("name"))
	if !ok {
		return
	}
	ps, es, err := s.Policy.Records.PolicyAudit(r.Context(), rep.Repository)
	if err != nil {
		refuseJSON(w, http.StatusServiceUnavailable, "records_unavailable")
		return
	}
	ps = s.closePromotions(r, p, rep.Repository, ps)
	who, err := s.rolesOn(r.Context(), p, rep.Repository)
	if err != nil {
		who = roles{} // the page still reads; every write asks again
	}
	writeJSON(w, struct {
		Report     evidence.Report      `json:"report"`
		Promotions []store.Promotion    `json:"promotions"`
		Exceptions []store.Exception    `json:"exceptions"`
		You        roles                `json:"you"`
		Catalogue  *catalogue.Catalogue `json:"catalogue"`
	}{rep, ps, es, who, s.catalogueFor(r.Context(), p, rep.Repository)})
}

func (s *Server) closePromotions(r *http.Request, p authn.Principal, repository string, records []store.Promotion) []store.Promotion {
	for i, record := range records {
		if record.PullRequest == 0 || (record.State != store.PromotionRequested && record.State != store.PromotionPROpened) {
			continue
		}
		status, err := s.Policy.Factory.PolicyPromotionStatus(r.Context(), p.AccessToken, repository, record.Class, record.PullRequest)
		if err != nil {
			continue
		}
		state := ""
		switch {
		case status.ServedMode == config.ModeLive:
			state = store.PromotionMerged
		case status.PullRequestState == "CLOSED":
			state = store.PromotionAbandoned
		}
		if state == "" {
			continue
		}
		updated, err := s.Policy.Records.MovePromotion(r.Context(), record.ID, state, 0, "factory")
		if err == nil {
			records[i] = updated
			s.policyChange(r.Context(), repository, record.Class, "promotion_"+state)
		}
	}
	return records
}

// catalogueFor is what the repository offers at intake, for the page's Profiles rows (profile
// choice at intake, stage 1): nil when no reader is wired or the factory does not answer, and the
// page still reads, as it does when the caller's roles cannot be had.
func (s *Server) catalogueFor(ctx context.Context, p authn.Principal, repository string) *catalogue.Catalogue {
	if s.Catalogue == nil {
		return nil
	}
	c, err := s.Catalogue.Get(ctx, p, repository, "")
	if err != nil {
		s.log().Warn("settings: catalogue unread", "repository", repository, "err", err)
		return nil
	}
	return &c
}

func decodePolicy(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPolicyBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return false
	}
	return true
}

// maintainerOf answers whether the caller writes repository, refusing for them when not.
func (s *Server) maintainerOf(w http.ResponseWriter, r *http.Request, p authn.Principal, repository string) (roles, bool) {
	who, err := s.rolesOn(r.Context(), p, repository)
	switch {
	case err != nil:
		refuseJSON(w, http.StatusServiceUnavailable, reasonAccessUnverified)
		return who, false
	case !who.Maintainer:
		refuseJSON(w, http.StatusForbidden, "not_a_maintainer")
		return who, false
	}
	return who, true
}

// promotionSnapshot is what a promotion was granted on: the class's evidence as read at ReadAt,
// the minimum applied, and the line the proposal changes.
type promotionSnapshot struct {
	ReadAt    time.Time         `json:"readAt"`
	Minimum   int               `json:"minimum"`
	Threshold int               `json:"threshold"`
	Mode      string            `json:"mode"`
	Evidence  evidence.Evidence `json:"evidence"`
	Line      int               `json:"line"`
}

// requestPromotion opens the promotion pull request after re-reading eligibility. Without gitops,
// it returns the command and one-line proposal instead. A requested record can be resumed; an open
// pull request blocks a second request.
func (s *Server) requestPromotion(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyPromotionWriter(w, r)
	if !ok {
		return
	}
	var in struct {
		Repository  string `json:"repository"`
		Class       string `json:"class"`
		ExceptionID string `json:"exceptionId"`
	}
	if !decodePolicy(w, r, &in) {
		return
	}
	rep, ok := s.report(w, r, p, in.Repository)
	if !ok {
		return
	}
	who, ok := s.maintainerOf(w, r, p, rep.Repository)
	if !ok {
		return
	}
	c, found := classOf(rep, in.Class)
	switch {
	case !found:
		refuseJSON(w, http.StatusNotFound, "no_such_class")
		return
	case c.Proposal == nil:
		refuseJSON(w, http.StatusConflict, "not_promotable")
		return
	case c.Evidence == nil || rep.EvidenceAt == nil:
		refuseJSON(w, http.StatusConflict, "evidence_pending")
		return
	}
	threshold := classMinimum(rep, c)
	if in.ExceptionID != "" {
		e, err := s.Policy.Records.Exception(r.Context(), in.ExceptionID)
		if err != nil || e.State != store.ExceptionApproved || e.Repository != rep.Repository || e.Class != c.Class ||
			e.ExpiresAt == nil || !e.ExpiresAt.After(s.now()) {
			refuseJSON(w, http.StatusConflict, "exception_unusable")
			return
		}
		threshold = e.Minimum
	}
	ev := *c.Evidence
	if ev.Clean < threshold || ev.Bad > 0 || ev.Drill != evidence.DrillPassed {
		promotionRefusal(w, ev, threshold)
		return
	}
	snap, err := json.Marshal(promotionSnapshot{ReadAt: *rep.EvidenceAt, Minimum: rep.Minimum, Threshold: threshold, Mode: c.Mode,
		Evidence: ev, Line: c.Proposal.Line})
	if err != nil {
		refuseJSON(w, http.StatusInternalServerError, "snapshot")
		return
	}
	out, err := s.Policy.Records.RecordPromotion(r.Context(), store.Promotion{ID: s.Policy.NewID(), Repository: rep.Repository,
		Class: c.Class, Threshold: threshold, Evidence: snap, ExceptionID: in.ExceptionID, RequestedBy: who.Login})
	if errors.Is(err, store.ErrPromotionOpen) {
		out, err = s.promotionToResume(r, rep.Repository, c.Class)
	}
	if !s.recorded(w, err) {
		return
	}
	opened, err := s.Policy.Factory.PolicyPromote(r.Context(), p.AccessToken, rep.Repository, c.Class, threshold)
	if err != nil {
		refuseJSON(w, http.StatusServiceUnavailable, "promotion_unavailable")
		return
	}
	if opened.PullRequest > 0 {
		out, err = s.Policy.Records.OpenPromotion(r.Context(), out.ID, opened.PullRequest, opened.PullRequestURL, opened.Branch)
		if !s.recorded(w, err) {
			return
		}
	}
	s.policyChange(r.Context(), out.Repository, out.Class, "promotion_"+out.State)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(struct {
		Promotion store.Promotion   `json:"promotion"`
		Proposal  evidence.Proposal `json:"proposal"`
		Command   string            `json:"command,omitempty"`
	}{out, opened.Proposal, opened.Command})
}

func (s *Server) promotionToResume(r *http.Request, repository, class string) (store.Promotion, error) {
	records, _, err := s.Policy.Records.PolicyAudit(r.Context(), repository)
	if err != nil {
		return store.Promotion{}, err
	}
	for _, p := range records {
		if p.Class == class && p.State == store.PromotionRequested && p.PullRequest == 0 {
			return p, nil
		}
	}
	return store.Promotion{}, store.ErrPromotionOpen
}

func promotionRefusal(w http.ResponseWriter, ev evidence.Evidence, threshold int) {
	var blockers []string
	if ev.Clean < threshold {
		blockers = append(blockers, "below_minimum")
	}
	if ev.Bad > 0 {
		blockers = append(blockers, "bad_outcome")
	}
	if ev.Drill != evidence.DrillPassed {
		blockers = append(blockers, "no_drill")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	writeJSON(w, map[string]any{"error": "not_eligible", "blockers": blockers,
		"counts": map[string]any{"clean": ev.Clean, "minimum": threshold, "bad": ev.Bad, "drill": ev.Drill}})
}

// movePromotion is POST /api/policy/promotions/{id} {state, pullRequest?}: a maintainer names the
// pull request they opened, or ends the request merged or abandoned.
func (s *Server) movePromotion(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyWriter(w, r)
	if !ok {
		return
	}
	var in struct {
		State       string `json:"state"`
		PullRequest int    `json:"pullRequest"`
	}
	if !decodePolicy(w, r, &in) {
		return
	}
	if !slices.Contains([]string{store.PromotionPROpened, store.PromotionMerged, store.PromotionAbandoned}, in.State) || in.PullRequest < 0 {
		refuseJSON(w, http.StatusBadRequest, "bad_state")
		return
	}
	cur, err := s.Policy.Records.Promotion(r.Context(), r.PathValue("id"))
	if !s.recorded(w, err) {
		return
	}
	who, ok := s.maintainerOf(w, r, p, cur.Repository)
	if !ok {
		return
	}
	out, err := s.Policy.Records.MovePromotion(r.Context(), cur.ID, in.State, in.PullRequest, who.Login)
	if !s.recorded(w, err) {
		return
	}
	s.policyChange(r.Context(), out.Repository, out.Class, "promotion_"+out.State)
	writeJSON(w, out)
}

// policyDrill opens a rollback-drill revert for a maintainer.
func (s *Server) policyDrill(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return
	}
	var in struct {
		PullRequest int `json:"pullRequest"`
	}
	if !decodePolicy(w, r, &in) {
		return
	}
	if in.PullRequest < 1 {
		refuseJSON(w, http.StatusBadRequest, "pr_required")
		return
	}
	repository := r.PathValue("owner") + "/" + r.PathValue("name")
	rep, ok := s.report(w, r, p, repository)
	if !ok {
		return
	}
	if _, ok := s.maintainerOf(w, r, p, rep.Repository); !ok {
		return
	}
	out, err := s.Policy.Factory.PolicyDrill(r.Context(), p.AccessToken, rep.Repository, in.PullRequest)
	if !s.forwarded(w, err) {
		return
	}
	writeJSON(w, out)
}

// requestException is POST /api/policy/exceptions {repository, class, minimum, justification}: a
// maintainer asks for a class to be promoted on fewer clean decisions than the minimum (MP7).
func (s *Server) requestException(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyWriter(w, r)
	if !ok {
		return
	}
	var in struct {
		Repository    string `json:"repository"`
		Class         string `json:"class"`
		Minimum       int    `json:"minimum"`
		Justification string `json:"justification"`
	}
	if !decodePolicy(w, r, &in) {
		return
	}
	rep, ok := s.report(w, r, p, in.Repository)
	if !ok {
		return
	}
	who, ok := s.maintainerOf(w, r, p, rep.Repository)
	if !ok {
		return
	}
	c, found := classOf(rep, in.Class)
	switch {
	case !found:
		refuseJSON(w, http.StatusNotFound, "no_such_class")
		return
	case in.Minimum < 1 || in.Minimum >= classMinimum(rep, c):
		refuseJSON(w, http.StatusBadRequest, "minimum_not_below")
		return
	case len(in.Justification) < 20 || len(in.Justification) > 2000:
		refuseJSON(w, http.StatusBadRequest, "justification_length")
		return
	}
	out, err := s.Policy.Records.RequestException(r.Context(), store.Exception{ID: s.Policy.NewID(), Repository: rep.Repository,
		Class: c.Class, Minimum: in.Minimum, Justification: in.Justification, RequestedBy: who.Login})
	if !s.recorded(w, err) {
		return
	}
	s.policyChange(r.Context(), out.Repository, out.Class, "exception_requested")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(out)
}

// decideException is POST /api/policy/exceptions/{id} {decision, note}: an operator approves (for
// ExceptionLifetime) or refuses a requested exception, once.
func (s *Server) decideException(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyWriter(w, r)
	if !ok {
		return
	}
	var in struct {
		Decision string `json:"decision"`
		Note     string `json:"note"`
	}
	if !decodePolicy(w, r, &in) {
		return
	}
	if (in.Decision != store.ExceptionApproved && in.Decision != store.ExceptionRefused) || len(in.Note) > 2000 {
		refuseJSON(w, http.StatusBadRequest, "bad_decision")
		return
	}
	cur, err := s.Policy.Records.Exception(r.Context(), r.PathValue("id"))
	if !s.recorded(w, err) {
		return
	}
	who, err := s.rolesOn(r.Context(), p, cur.Repository)
	switch {
	case err != nil:
		refuseJSON(w, http.StatusServiceUnavailable, reasonAccessUnverified)
		return
	case !who.Operator:
		refuseJSON(w, http.StatusForbidden, "not_an_operator")
		return
	case in.Decision == store.ExceptionApproved && strings.EqualFold(cur.RequestedBy, who.Login):
		refuseJSON(w, http.StatusForbidden, "self_approval")
		return
	}
	out, err := s.Policy.Records.DecideException(r.Context(), cur.ID, in.Decision, who.Login, in.Note, s.now().Add(ExceptionLifetime))
	if !s.recorded(w, err) {
		return
	}
	s.policyChange(r.Context(), out.Repository, out.Class, "exception_"+out.State)
	writeJSON(w, out)
}

// policyPreview is POST /api/policy/repositories/{owner}/{name}/preview {file}: the editor's check
// of a proposed policy file, for a maintainer (MP17). The factory parses, validates against the
// central guardrails, compares structurally and simulates; nothing is written anywhere.
func (s *Server) policyPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return
	}
	var in struct {
		// Kind is file (the default) or binding (B16): the proposed spec.mergePolicies.
		Kind     string           `json:"kind"`
		File     string           `json:"file"`
		Bindings []config.Binding `json:"bindings"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPreviewBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return
	}
	rep, ok := s.report(w, r, p, r.PathValue("owner")+"/"+r.PathValue("name"))
	if !ok {
		return
	}
	if _, ok := s.maintainerOf(w, r, p, rep.Repository); !ok {
		return
	}
	switch in.Kind {
	case "", "file", "binding":
	default:
		refuseJSON(w, http.StatusBadRequest, "kind_unknown") // as the factory refuses it
		return
	}
	if in.Kind == "binding" {
		pv, err := s.Policy.Factory.PolicyPreviewBindings(r.Context(), p.AccessToken, rep.Repository, in.Bindings)
		if !s.forwarded(w, err) {
			return
		}
		writeJSON(w, pv)
		return
	}
	pv, err := s.Policy.Factory.PolicyPreview(r.Context(), p.AccessToken, rep.Repository, in.File)
	if !s.forwarded(w, err) {
		return
	}
	writeJSON(w, pv)
}

// policyBacktest replays an edited policy over the repository's recent recorded diffs.
func (s *Server) policyBacktest(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return
	}
	var in struct {
		policyview.BacktestRequest
		Since string `json:"since"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPreviewBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return
	}
	if in.Kind != "" && in.Kind != "file" && in.Kind != "binding" && in.Kind != "policy" {
		refuseJSON(w, http.StatusBadRequest, "kind_unknown")
		return
	}
	rep, ok := s.report(w, r, p, r.PathValue("owner")+"/"+r.PathValue("name"))
	if !ok {
		return
	}
	if _, ok := s.maintainerOf(w, r, p, rep.Repository); !ok {
		return
	}
	bt, err := s.Policy.Factory.PolicyBacktest(r.Context(), p.AccessToken, rep.Repository, in.BacktestRequest, in.Since)
	if !s.forwarded(w, err) {
		return
	}
	writeJSON(w, bt)
}

// policyList is GET /api/policy/policies: the MergePolicies the factory shows this caller (B16).
func (s *Server) policyList(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return
	}
	ps, err := s.Policy.Factory.PolicyList(r.Context(), p.AccessToken)
	if !s.forwarded(w, err) {
		return
	}
	writeJSON(w, map[string][]evidence.PolicyReport{"policies": ps})
}

// policyGet is GET /api/policy/policies/{name}: one MergePolicy and the consumers the caller reads.
func (s *Server) policyGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return
	}
	pol, err := s.Policy.Factory.Policy(r.Context(), p.AccessToken, r.PathValue("name"))
	if !s.forwarded(w, err) {
		return
	}
	writeJSON(w, pol)
}

// policyPolicyPreview is POST /api/policy/policies/{name}/preview {spec}: a proposed MergePolicy,
// its manifest and its consumers' changes (B11, B16). It writes nothing.
func (s *Server) policyPolicyPreview(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return
	}
	var in struct {
		Spec config.PolicySpec `json:"spec"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPreviewBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil || dec.More() {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return
	}
	pv, err := s.Policy.Factory.PolicyPreviewPolicy(r.Context(), p.AccessToken, r.PathValue("name"), in.Spec)
	if !s.forwarded(w, err) {
		return
	}
	writeJSON(w, pv)
}

// policyRecentDecisions is GET /api/policy/repositories/{owner}/{name}/decisions?since=<date>:
// the repository's recent per-policy decisions, for a reader of it.
func (s *Server) policyRecentDecisions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return
	}
	recent, err := s.Policy.Factory.PolicyRecentDecisions(r.Context(), p.AccessToken,
		r.PathValue("owner")+"/"+r.PathValue("name"), r.URL.Query().Get("since"))
	if !s.forwarded(w, err) {
		return
	}
	writeJSON(w, recent)
}

// policyDecisions is GET /api/policy/repositories/{owner}/{name}/decisions/{pr}: a pull request's
// decision timeline, which the factory reads for a reader of the repository.
func (s *Server) policyDecisions(w http.ResponseWriter, r *http.Request) {
	p, ok := s.policyCaller(w, r)
	if !ok {
		return
	}
	pr, err := strconv.Atoi(r.PathValue("pr"))
	if err != nil || pr < 1 {
		refuseJSON(w, http.StatusBadRequest, "pr_required")
		return
	}
	tl, err := s.Policy.Factory.PolicyDecisions(r.Context(), p.AccessToken, r.PathValue("owner")+"/"+r.PathValue("name"), pr)
	if !s.forwarded(w, err) {
		return
	}
	writeJSON(w, tl)
}

// forwarded answers the factory's refusal with its bounded reason; true when err is nil.
func (s *Server) forwarded(w http.ResponseWriter, err error) bool {
	var ref runrequest.ErrRefused
	switch {
	case err == nil:
		return true
	case errors.As(err, &ref):
		refuseJSON(w, ref.Status, ref.Reason)
	case errors.Is(err, runrequest.ErrForbidden):
		refuseJSON(w, http.StatusForbidden, "human_only")
	default:
		refuseJSON(w, http.StatusServiceUnavailable, "evidence_unavailable")
	}
	return false
}

// recorded answers a store refusal with its reason; true when err is nil.
func (s *Server) recorded(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrNoPolicyRecord):
		refuseJSON(w, http.StatusNotFound, "not_found")
	case errors.Is(err, store.ErrPromotionOpen):
		refuseJSON(w, http.StatusConflict, "promotion_open")
	case errors.Is(err, store.ErrExceptionUnusable):
		refuseJSON(w, http.StatusConflict, "exception_unusable")
	case errors.Is(err, store.ErrPolicyMoved):
		refuseJSON(w, http.StatusConflict, "already_moved")
	default:
		s.log().Warn("policy record", "err", err)
		refuseJSON(w, http.StatusServiceUnavailable, "records_unavailable")
	}
	return false
}

func (s *Server) policyChange(ctx context.Context, repository, class, change string) {
	if s.Metrics != nil {
		s.Metrics.PolicyChange(ctx, repository, class, change)
	}
}

// classMinimum is the clean decisions c's promotion needs without an exception: its evidence's
// minimum (MR4), else the report's while its evidence is being read.
func classMinimum(rep evidence.Report, c evidence.ClassReport) int {
	if c.Evidence != nil && c.Evidence.Minimum > 0 {
		return c.Evidence.Minimum
	}
	return rep.Minimum
}
