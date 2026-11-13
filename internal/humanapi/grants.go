// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/grant"
	"github.com/Smana/maku/internal/repoaccess"
	"github.com/Smana/maku/internal/store"
)

// Approval memory's human door (merge policy stage 5, MP17): readers of a repository list, search
// and export its grants; its maintainers (GitHub write) create, renew and reconfirm them; any of
// its maintainers, or an admin, revokes one. Every check is the server's: the page hides nothing
// that matters. Writes come from the web UI's session only, from this broker's own origin.

// GrantStore is the store's grants as the human API reads and moves them; *store.Store has it.
type GrantStore interface {
	CreateGrant(ctx context.Context, n store.NewGrant) (store.Grant, error)
	Grant(ctx context.Context, id string) (store.Grant, error)
	Grants(ctx context.Context, f store.GrantFilter) ([]store.Grant, error)
	GrantEvents(ctx context.Context, id string) ([]store.GrantEvent, error)
	MoveGrant(ctx context.Context, id string, m store.GrantMove) (store.Grant, error)
}

const (
	// maxGrantWrite bounds a grant write's body: 32 patterns of 256 bytes and their envelope.
	maxGrantWrite = 16 << 10
	// The lifetime a maintainer may ask, in days: the factory's mergeGuardrails.maxGrantLifetime
	// (90 days by default) shortens it at every lookup.
	defaultGrantDays = 90
	maxGrantDays     = 365
	grantsPage       = "/settings/approval-memory"
)

// grantOutside is what a grant never bypasses (MP9): said with every grant created, so the
// maintainer sees the conditions that remain outside it.
var grantOutside = []string{
	"the fixed base: required CI, the leak scan, the ownership fence, policy-bot, provenance, author checks and head pinning",
	"the class's own conditions and its deny rules",
	"the breaker and the daily caps",
	"the class being live: in shadow or off, a grant is inactive",
	"the class definition it was made under (the policy and this repository's binding): a change to it suspends it until a maintainer reconfirms it",
	"its expiry, and the operator's maximum grant lifetime",
}

// grantView is a grant as the page and the export show it.
type grantView struct {
	ID         string     `json:"id"`
	Version    int        `json:"version"`
	Repository string     `json:"repository"`
	Class      string     `json:"class"`
	Files      []string   `json:"files"`
	DiffShape  string     `json:"diffShape,omitempty"`
	PolicyHash string     `json:"policyHash"`
	OriginPR   int        `json:"originPR"`
	CreatedBy  string     `json:"createdBy"`
	CreatedAt  time.Time  `json:"createdAt"`
	RenewedAt  time.Time  `json:"renewedAt"`
	ExpiresAt  time.Time  `json:"expiresAt"`
	Status     string     `json:"status"`
	StatusAt   time.Time  `json:"statusAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	LastUsedPR int        `json:"lastUsedPR,omitempty"`
}

func viewOf(g store.Grant) grantView {
	v := grantView{ID: g.ID, Version: g.Version, Repository: g.Repository, Class: g.Class, Files: g.Files, DiffShape: g.DiffShape,
		PolicyHash: g.PolicyHash, OriginPR: g.OriginPR, CreatedBy: g.CreatedBy, CreatedAt: g.CreatedAt, RenewedAt: g.RenewedAt,
		ExpiresAt: g.ExpiresAt, Status: g.Status, StatusAt: g.StatusAt, LastUsedPR: g.LastUsedPR}
	if !g.LastUsedAt.IsZero() {
		used := g.LastUsedAt
		v.LastUsedAt = &used
	}
	return v
}

type grantEventView struct {
	Event      string    `json:"event"`
	Version    int       `json:"version"`
	Actor      string    `json:"actor"`
	Reason     string    `json:"reason,omitempty"`
	PolicyHash string    `json:"policyHash,omitempty"`
	PR         int       `json:"pr,omitempty"`
	Head       string    `json:"head,omitempty"`
	At         time.Time `json:"at"`
}

// grantRoutes serves the page's direct load and the grant API (human.approvalMemory).
func (s *Server) grantRoutes(mux *http.ServeMux, index http.Handler) {
	mux.Handle("GET "+grantsPage, withCSP(index))
	mux.HandleFunc("GET /api/approval-memory/grants", s.listGrants)
	mux.HandleFunc("GET /api/approval-memory/grants/{id}", s.grantDetail)
	mux.HandleFunc("GET /api/approval-memory/export", s.exportGrants)
	mux.HandleFunc("POST /api/approval-memory/preview", s.previewGrant)
	mux.HandleFunc("POST /api/approval-memory/grants", s.createGrant)
	mux.HandleFunc("POST /api/approval-memory/grants/{id}/{action}", s.moveGrant)
}

// repoStanding is what p may do on repository: read it, write it (a maintainer), or neither. An
// admin reads every repository; writing it takes GitHub write, as everywhere else. An error is a
// check that could not be made: the caller fails closed.
func (s *Server) repoStanding(ctx context.Context, p authn.Principal, repository string) (read, write bool, login string, err error) {
	admin := s.Groups.IsAdmin(p)
	if p.Sponsored || s.Access == nil || s.Identity == nil {
		return admin, false, "", nil
	}
	ctx, cancel := context.WithTimeout(ctx, accessWait)
	defer cancel()
	login, err = s.Identity.Login(ctx, p.Sub, repository)
	switch {
	case errors.Is(err, repoaccess.ErrNoRepository):
		return admin, false, "", nil
	case err != nil:
		return admin, false, "", err
	case login == "":
		return admin, false, "", nil
	}
	if write, err = s.Access.CanWrite(ctx, repository, login); err != nil {
		return admin, false, login, err
	}
	if write || admin {
		return true, write, login, nil
	}
	read, err = s.Access.CanRead(ctx, repository, login)
	return read, false, login, err
}

// listGrants: GET /api/approval-memory/grants?repository=&class=&status=&q=, the grants of the
// repositories the caller may read, newest first. q searches the id, repository, class, patterns
// and creator. A repository whose access cannot be checked lists nothing and says so.
func (s *Server) listGrants(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.GrantFilter{Repository: strings.ToLower(q.Get("repository")), Class: q.Get("class"), Status: q.Get("status")}
	if f.Status != "" && !slices.Contains([]string{grant.StatusActive, grant.StatusSuspended, grant.StatusRevoked,
		grant.StatusExpired, grant.StatusDisabled}, f.Status) {
		refuseJSON(w, http.StatusBadRequest, "bad_status")
		return
	}
	gs, err := s.Grants.Grants(r.Context(), f)
	if err != nil {
		s.log().Warn("approval grants unreadable", "err", err)
		refuseJSON(w, http.StatusServiceUnavailable, "grants_unavailable")
		return
	}
	search := strings.ToLower(strings.TrimSpace(q.Get("q")))
	readable := map[string]bool{}
	unverified := false
	out := struct {
		Grants     []grantView `json:"grants"`
		Unverified bool        `json:"unverified,omitempty"`
	}{Grants: []grantView{}}
	for _, g := range gs {
		read, seen := readable[g.Repository]
		if !seen {
			read, _, _, err = s.repoStanding(r.Context(), p, g.Repository)
			unverified = unverified || err != nil
			readable[g.Repository] = read && err == nil
			read = readable[g.Repository]
		}
		if read && matches(g, search) {
			out.Grants = append(out.Grants, viewOf(g))
		}
	}
	out.Unverified = unverified
	writeJSON(w, out)
}

func matches(g store.Grant, q string) bool {
	if q == "" {
		return true
	}
	hay := strings.ToLower(strings.Join(append([]string{g.ID, g.Repository, g.Class, g.CreatedBy, g.Status}, g.Files...), "\n"))
	return strings.Contains(hay, q)
}

// readableGrant is the grant {id}, when the caller may read its repository; ok is false once the
// refusal is written. An unreadable grant is a 404, as one that does not exist.
func (s *Server) readableGrant(w http.ResponseWriter, r *http.Request) (authn.Principal, store.Grant, bool) {
	p, ok := s.principal(w, r)
	if !ok {
		return p, store.Grant{}, false
	}
	id := r.PathValue("id")
	if !grant.IDRE.MatchString(id) {
		refuseJSON(w, http.StatusNotFound, "no_grant")
		return p, store.Grant{}, false
	}
	g, err := s.Grants.Grant(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNoGrant):
		refuseJSON(w, http.StatusNotFound, "no_grant")
		return p, g, false
	case err != nil:
		s.log().Warn("approval grant unreadable", "err", err)
		refuseJSON(w, http.StatusServiceUnavailable, "grants_unavailable")
		return p, g, false
	}
	read, _, _, err := s.repoStanding(r.Context(), p, g.Repository)
	switch {
	case err != nil:
		refuseJSON(w, http.StatusServiceUnavailable, reasonAccessUnverified)
		return p, g, false
	case !read:
		refuseJSON(w, http.StatusNotFound, "no_grant")
		return p, g, false
	}
	return p, g, true
}

// grantDetail: GET /api/approval-memory/grants/{id}, the grant with its usage and outcome history.
func (s *Server) grantDetail(w http.ResponseWriter, r *http.Request) {
	_, g, ok := s.readableGrant(w, r)
	if !ok {
		return
	}
	evs, err := s.Grants.GrantEvents(r.Context(), g.ID)
	if err != nil {
		s.log().Warn("approval grant history unreadable", "err", err)
		refuseJSON(w, http.StatusServiceUnavailable, "grants_unavailable")
		return
	}
	out := struct {
		Grant   grantView        `json:"grant"`
		Events  []grantEventView `json:"events"`
		Outside []string         `json:"outside"`
	}{Grant: viewOf(g), Events: []grantEventView{}, Outside: grantOutside}
	for _, e := range evs {
		out.Events = append(out.Events, grantEventView{Event: e.Event, Version: e.Version, Actor: e.Actor, Reason: e.Reason,
			PolicyHash: e.PolicyHash, PR: e.PR, Head: e.Head, At: e.At})
	}
	writeJSON(w, out)
}

// exportGrants: GET /api/approval-memory/export?repository=, the caller's readable grants as YAML,
// for review beside the repository's policy.
func (s *Server) exportGrants(w http.ResponseWriter, r *http.Request) {
	p, ok := s.principal(w, r)
	if !ok {
		return
	}
	repo := strings.ToLower(r.URL.Query().Get("repository"))
	gs, err := s.Grants.Grants(r.Context(), store.GrantFilter{Repository: repo})
	if err != nil {
		refuseJSON(w, http.StatusServiceUnavailable, "grants_unavailable")
		return
	}
	doc := struct {
		APIVersion string      `json:"apiVersion"`
		Kind       string      `json:"kind"`
		Grants     []grantView `json:"grants"`
	}{APIVersion: "maku.ogenki.io/v1alpha1", Kind: "ApprovalGrantExport", Grants: []grantView{}}
	readable := map[string]bool{}
	for _, g := range gs {
		read, seen := readable[g.Repository]
		if !seen {
			read, _, _, err = s.repoStanding(r.Context(), p, g.Repository)
			read = read && err == nil
			readable[g.Repository] = read
		}
		if read {
			doc.Grants = append(doc.Grants, viewOf(g))
		}
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		refuseJSON(w, http.StatusInternalServerError, "export_failed")
		return
	}
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="approval-grants.yaml"`)
	_, _ = w.Write(out)
}

// grantWriter is the web UI's signed-in human, posting from this broker's origin (P18: a maku or
// MCP bearer can set any Origin, so only the UI's session writes a grant).
func (s *Server) grantWriter(w http.ResponseWriter, r *http.Request) (authn.Principal, bool) {
	p, ok := s.principal(w, r)
	if !ok {
		return p, false
	}
	if p.Kind != envelope.ActorHuman || p.Sponsored || s.WebClient == nil || p.ClientID == "" || p.ClientID != s.WebClient() {
		refuseJSON(w, http.StatusForbidden, "web_only")
		return p, false
	}
	if want, o := origin(s.PublicURL), r.Header.Get("Origin"); want == "" || o == "" || origin(o) != want {
		refuseJSON(w, http.StatusForbidden, "cross_origin")
		return p, false
	}
	return p, true
}

func decodeGrant(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxGrantWrite))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil || dec.More() {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return false
	}
	return true
}

func lifetime(days int) (time.Duration, bool) {
	if days == 0 {
		days = defaultGrantDays
	}
	return time.Duration(days) * 24 * time.Hour, days >= 1 && days <= maxGrantDays
}

// ruleRequest is "merge and create rule" as the page posts it, for a preview or a create. A
// maintainer names the files the approval subset caught on the pull request (caughtFiles), which
// become exact patterns, or writes the patterns (files) themselves; not both.
type ruleRequest struct {
	Repository   string   `json:"repository"`
	Class        string   `json:"class"`
	Files        []string `json:"files,omitempty"`
	CaughtFiles  []string `json:"caughtFiles,omitempty"`
	DiffShape    string   `json:"diffShape,omitempty"`
	PolicyHash   string   `json:"policyHash"`
	PullRequest  int      `json:"pullRequest"`
	LifetimeDays int      `json:"lifetimeDays"`
}

// ruleProposal is what a maintainer sees before saving a rule, and again once it is saved: the
// exact predicate, the future pull requests it would match, the conditions that stay outside it,
// its expiry, how to revoke it, and what "merge once" does instead.
type ruleProposal struct {
	Grant     *grantView      `json:"grant,omitempty"`
	Predicate grant.Predicate `json:"predicate"`
	Matches   string          `json:"matches"`
	Outside   []string        `json:"outside"`
	ExpiresAt time.Time       `json:"expiresAt"`
	Revoke    string          `json:"revoke"`
	MergeOnce string          `json:"mergeOnce"`
}

// mergeOnce is the other choice: the existing approved-merge path, for this head only (AM1).
const mergeOnce = "approve the pull request's head on GitHub: the approved-merge path merges that head only, and saves nothing"

// matchesText says which future pull requests the predicate would let arm, in words a maintainer
// can check against the class: the subset's files only, every other condition still decided.
func matchesText(pr grant.Predicate) string {
	shape := ""
	if pr.DiffShape != "" {
		shape = ", and whose diff is " + pr.DiffShape
	}
	return "a future pull request of " + pr.Repository + " decided in the live class " + pr.Class + " under class definition " +
		pr.PolicyHash + ", whose every file in the class's requiresApproval subset matches one of " +
		strings.Join(pr.Files, " | ") + shape + ". Files outside the subset are judged by the class as before."
}

// proposal validates a rule request for p: the predicate, its lifetime, and GitHub write on the
// repository (MP17). It answers the proposal and the creator's login, or writes the refusal.
func (s *Server) proposal(w http.ResponseWriter, r *http.Request, p authn.Principal) (ruleRequest, grant.Predicate, time.Duration, string, bool) {
	var in ruleRequest
	if !decodeGrant(w, r, &in) {
		return in, grant.Predicate{}, 0, "", false
	}
	pred := grant.Predicate{Repository: strings.ToLower(in.Repository), Class: in.Class, Files: in.Files,
		DiffShape: in.DiffShape, PolicyHash: in.PolicyHash}
	if len(in.CaughtFiles) > 0 {
		pred.Files = nil
		for _, f := range in.CaughtFiles {
			pred.Files = append(pred.Files, grant.Exact(f))
		}
	}
	life, okLife := lifetime(in.LifetimeDays)
	if err := pred.Validate(); err != nil || in.PullRequest < 1 || !okLife || (len(in.Files) > 0 && len(in.CaughtFiles) > 0) {
		refuseJSON(w, http.StatusBadRequest, "bad_predicate")
		return in, pred, 0, "", false
	}
	_, write, login, err := s.repoStanding(r.Context(), p, pred.Repository)
	switch {
	case err != nil:
		refuseJSON(w, http.StatusServiceUnavailable, reasonAccessUnverified)
		return in, pred, 0, "", false
	case !write:
		refuseJSON(w, http.StatusForbidden, "repository_permission")
		return in, pred, 0, "", false
	}
	return in, pred, life, login, true
}

// previewGrant: POST /api/approval-memory/preview, "merge and create rule" before anything is
// saved: the proposal a create would answer, for a maintainer of the repository.
func (s *Server) previewGrant(w http.ResponseWriter, r *http.Request) {
	p, ok := s.grantWriter(w, r)
	if !ok {
		return
	}
	_, pred, life, _, ok := s.proposal(w, r, p)
	if !ok {
		return
	}
	writeJSON(w, ruleProposal{Predicate: pred, Matches: matchesText(pred), Outside: grantOutside,
		ExpiresAt: s.now().Add(life).UTC(), Revoke: "POST /api/approval-memory/grants/<id>/revoke, once saved", MergeOnce: mergeOnce})
}

// createGrant: POST /api/approval-memory/grants, "merge and create rule" (MP9, MP10): a
// maintainer of the repository saves the exact predicate. The answer is the proposal with the
// grant, its expiry and how to revoke it.
func (s *Server) createGrant(w http.ResponseWriter, r *http.Request) {
	p, ok := s.grantWriter(w, r)
	if !ok {
		return
	}
	in, pred, life, login, ok := s.proposal(w, r, p)
	if !ok {
		return
	}
	id := strings.ToLower(linkCode.EncodeToString(randomBytes(16)))
	g, err := s.Grants.CreateGrant(r.Context(), store.NewGrant{ID: id, Repository: pred.Repository, Class: pred.Class,
		Files: pred.Files, DiffShape: pred.DiffShape, PolicyHash: pred.PolicyHash, OriginPR: in.PullRequest, CreatedBy: login, Lifetime: life})
	if err != nil {
		s.log().Warn("approval grant not created", "err", err)
		refuseJSON(w, http.StatusServiceUnavailable, "grants_unavailable")
		return
	}
	v := viewOf(g)
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, ruleProposal{Grant: &v, Predicate: pred, Matches: matchesText(pred), Outside: grantOutside, ExpiresAt: g.ExpiresAt,
		Revoke: "POST /api/approval-memory/grants/" + g.ID + "/revoke", MergeOnce: mergeOnce})
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b) // crypto/rand never fails on a supported platform
	return b
}

// moveGrant: POST /api/approval-memory/grants/{id}/{revoke|renew|reconfirm}. Revoking takes any
// maintainer of the repository, or an admin (an operator stopping a defect); renewing and
// reconfirming take a maintainer. A reconfirmation rebinds the grant to the policy its latest
// suspension was found under, never to a hash the caller names.
func (s *Server) moveGrant(w http.ResponseWriter, r *http.Request) {
	p, ok := s.grantWriter(w, r)
	if !ok {
		return
	}
	action := r.PathValue("action")
	event, known := map[string]string{"revoke": grant.EventRevoked, "renew": grant.EventRenewed, "reconfirm": grant.EventReconfirmed}[action]
	if !known {
		refuseJSON(w, http.StatusNotFound, "no_action")
		return
	}
	var in struct {
		Version      int    `json:"version"`
		Reason       string `json:"reason"`
		LifetimeDays int    `json:"lifetimeDays"`
	}
	if !decodeGrant(w, r, &in) {
		return
	}
	life, okLife := lifetime(in.LifetimeDays)
	if len(in.Reason) > grant.MaxReasonBytes || in.Version < 0 || !okLife {
		refuseJSON(w, http.StatusBadRequest, "bad_json")
		return
	}
	_, g, ok := s.readableGrant(w, r)
	if !ok {
		return
	}
	_, write, login, err := s.repoStanding(r.Context(), p, g.Repository)
	switch {
	case err != nil:
		refuseJSON(w, http.StatusServiceUnavailable, reasonAccessUnverified)
		return
	case !write && (event != grant.EventRevoked || !s.Groups.IsAdmin(p)):
		refuseJSON(w, http.StatusForbidden, "repository_permission")
		return
	}
	actor := login
	if actor == "" {
		actor = p.ID
	}
	m := store.GrantMove{Event: event, Actor: actor, Reason: in.Reason, Version: in.Version, Lifetime: life}
	if event == grant.EventReconfirmed {
		hash, found := s.suspendedUnder(r.Context(), g.ID)
		if !found {
			refuseJSON(w, http.StatusConflict, "grant_state")
			return
		}
		m.PolicyHash = hash
	}
	moved, err := s.Grants.MoveGrant(r.Context(), g.ID, m)
	switch {
	case errors.Is(err, store.ErrGrantState), errors.Is(err, store.ErrGrantVersion):
		refuseJSON(w, http.StatusConflict, "grant_state")
	case err != nil:
		s.log().Warn("approval grant not moved", "event", event, "err", err)
		refuseJSON(w, http.StatusServiceUnavailable, "grants_unavailable")
	default:
		writeJSON(w, viewOf(moved))
	}
}

// suspendedUnder is the policy hash the grant's latest suspension was found under: the one a
// reconfirmation binds it to.
func (s *Server) suspendedUnder(ctx context.Context, id string) (string, bool) {
	evs, err := s.Grants.GrantEvents(ctx, id)
	if err != nil {
		return "", false
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Event == grant.EventSuspended && evs[i].PolicyHash != "" {
			return evs[i].PolicyHash, true
		}
	}
	return "", false
}
