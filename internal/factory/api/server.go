// SPDX-License-Identifier: Apache-2.0

package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/policy"
	"github.com/Smana/maku/internal/tlsreload"
)

// Authenticating maps a request to its caller. Task 5.1's Authenticator satisfies it; the
// principal is always the token's, never the body's (SC-13).
type Authenticating interface {
	Authenticate(*http.Request) (authn.Principal, error)
}

// RunStore creates the claim and lists the live runs the room and branch checks read. Since
// R50 the daily budget does not come from List: it is admitted through the day's ledger.
type RunStore interface {
	Create(ctx context.Context, s runs.Spec) error
	List(ctx context.Context) ([]runs.Run, error)
}

// RunRequest is the POST /v1/runs body (§4). Unknown fields — principal and branch included —
// are ignored: the principal is the token's (SC-13) and the branch the factory's (C3).
type RunRequest struct {
	Role       string `json:"role"`
	Repository string `json:"repository"`
	BaseRef    string `json:"baseRef"`
	Task       struct {
		Text string `json:"text"`
		URL  string `json:"url"`
	} `json:"task"`
	DataClass      string   `json:"dataClass"`
	RoomRef        string   `json:"roomRef"`
	Model          string   `json:"model"`
	MaxTokens      int64    `json:"maxTokens"`
	EgressProfiles []string `json:"egressProfiles"`
	// R35: a stopped run's branch, to resume on. Never a task's or a live run's.
	ResumeBranch string `json:"resumeBranch"`
}

// Server is the run-request API (§4). It serves on every replica (NeedLeaderElection is false),
// and admission is atomic across replicas through the day's ledger (R50).
type Server struct {
	Auth Authenticating
	// Groups names the two agents groups: policy's StartRun rule is the room UI's rule, so
	// the CLI grants nothing the UI refuses (SP2 §1).
	Groups policy.Groups
	Cfg    config.Source
	Runs   RunStore
	// Rooms is the manager's client: it reads Rooms and Tasks (rights, R35), and R50's
	// ledger reservation writes ConfigMaps, so a client.Reader would not do.
	Rooms     client.Client
	Namespace string
	Stopped   func(context.Context) bool // the stop object (§6.1); true on doubt
	NewRunID  func() string
	Now       func() time.Time
	Metrics   *fmetrics.Set
	// Owned is v2 D7's fence: whether the repository's default branch names this factory. nil owns
	// nothing.
	Owned func(repository string) bool
	// PrivateRefused is v4 D3: whether the repository is private code without its acknowledgement,
	// refused new tasks (403 private_code_unacknowledged). nil refuses none.
	PrivateRefused func(repository string) bool
	// Access is v2 D4: whether a human may write the repository on GitHub. nil refuses every
	// human: GitHub is the one answer to who may change a repository, and there is no bypass.
	Access WriteChecker
	// SlackLinks is the broker's self-serve link records (GET /v1/links/slack) and Logins the
	// ZITADEL resolver that turns a linked sub into its current GitHub login: read only for a
	// sponsored principal with onBehalfOf.slackLinks. Either nil makes every such link not_linked.
	SlackLinks SlackLinkReader
	Logins     LoginResolver
	// People is D1's identity link, read only while budgets.perPerson is on: the person a human
	// token is charged as. nil charges every human as their own principal.
	People PersonResolver
	// Forges files an api task's issue as the factory App on its repository (R10): nil files none;
	// a nil filer for a repository refuses a titled request.
	Forges func(repository string) IssueFiler
	// Log records the causes the intake never answers; nil records none.
	Log *slog.Logger
	// Redact is the broker's secret redactor (*redact.Redactor), applied to an api task's issue
	// before it is filed; nil refuses a titled request.
	Redact interface {
		String(s string) (string, []string)
	}
	// Coordination is the multi-repository draft and confirmation flow (v4 MR4, MR5), served only
	// while coordination.mode is plan or execute; nil or off serves no coordination route.
	Coordination CoordinationService
	// Evidence is each repository's promotion evidence (merge policy stage 6), served only while
	// policyManagement.promotion is on; nil or off serves no policy route.
	Evidence EvidenceSource
	// Editor is the settings editor's check and the decision timeline, served beside Evidence.
	Editor PolicyEditor
	// Policies are the class-shape MergePolicies (B16), served beside the evidence; nil lists none.
	Policies PolicySource
	// PolicySnapshot is a repository's file as last read (mergepolicy.Watch.Snapshot): a cut-over
	// repository's file bindings make it a consumer for the policy pages. nil reads none.
	PolicySnapshot func(repository string) (mergepolicy.Snapshot, bool)
	// Readers reads a repository's issues, pull requests and checks with the factory App for the
	// broker's forge_read (HR35); nil, or nil for a repository, answers forge_unavailable.
	// Readiness is each repository's hardening readiness (H5, HR41), served to operators and on the
	// Evidence page; nil serves neither.
	Readiness hardening.Readiness
	// readinessCache serves the page's and the route's readiness for readinessTTL.
	readinessCache *readinessCache
	Readers        func(repository string) ForgeThreads
	// Trust is each repository's fence-file hardening block, its trust floor; nil reads the floor
	// and the central minAuthorTrust.
	Trust *hardening.RepositoryPolicies
	// Writers is v2 D5's write check on a login, read for a factory/vouch label whose labeller is
	// not a maintainer; nil vouches for maintainers' labels only.
	Writers func(ctx context.Context, repository, login string) (bool, error)
	// InstallUnsafe is the factory's banner codes (GET /v1/install, v4 D3); nil lists none.
	InstallUnsafe func() []string
	// forgeReads is forge_read's per-(run, target) answer cache (forgeReadTTL).
	forgeReads *forgeReadCache
}

// SlackLinkReader is the broker's self-serve link records: the ZITADEL sub a Slack user linked;
// *rooms.Client implements it.
type SlackLinkReader interface {
	SlackLink(ctx context.Context, team, user string) (sub string, found bool, err error)
}

// LoginResolver is a ZITADEL sub's current GitHub login, "" when it has none;
// ghidentity.ErrInactive when the user is not active. *ghidentity.Resolver implements it.
type LoginResolver interface {
	Login(ctx context.Context, sub, repository string) (string, error)
}

// PersonResolver names the person a human principal is charged as (v2 per-person caps D1):
// github:<login> through their ZITADEL link, "" with no link. An error is no answer.
type PersonResolver interface {
	Person(ctx context.Context, p authn.Principal, repository string) (string, error)
}

// WriteChecker answers v2 D4 for a human principal: write or higher on repository, from GitHub.
// An error is no answer, and the caller refuses.
type WriteChecker interface {
	CanWrite(ctx context.Context, p authn.Principal, repository string) (bool, error)
}

var (
	roles = []string{"implementer", "reviewer", "tester", "triager"}
	// C5's names and the provider aliases (v3 F15), which validate refuses an internal run.
	models = []string{"agent-default", "tier-light", "tier-standard", "tier-frontier", "kimi-frontier", "mistral-standard", "mistral-light"}

	prURL = regexp.MustCompile(`^https://github\.com/[^/]+/[^/]+/pull/[0-9]+$`)
)

// NeedLeaderElection is false: every replica serves intake.
func (s *Server) NeedLeaderElection() bool { return false }

// Handler is the API's mux: POST /v1/runs, admission in §4's order, POST /v1/tasks while a
// sponsored principal is declared, and the coordination routes while the switch is on.
func (s *Server) Handler() http.Handler {
	cfg := s.Cfg.Current()
	if s.forgeReads == nil {
		s.forgeReads = &forgeReadCache{}
	}
	if s.readinessCache == nil {
		s.readinessCache = &readinessCache{}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/runs", s.createRun)
	// HR35: the broker's forge_read; it admits system:broker alone and refuses a repository that is
	// not in hardening mode enforce, so it reads nothing while H4 is off.
	mux.HandleFunc(ForgeReadRoute, s.forgeRead)
	if s.Readiness != nil {
		mux.HandleFunc(ReadinessRoute, s.hardeningReadiness)
	}
	if cfg.DrillsOn() { // HR38: served while the drill runner is on
		mux.HandleFunc(DrillsRoute, s.postDrill)
	}
	if len(cfg.API.Sponsored)+len(cfg.API.InternalPrincipals) > 0 { // F4, D4: off until a principal is declared
		mux.HandleFunc("POST /v1/tasks", s.createTask)
	}
	if s.anyVerifies(cfg) { // RA11, D4: a 404 until an internal principal may send verdicts
		mux.HandleFunc("POST /v1/tasks/{task}/verification", s.recordVerification)
	}
	if s.coordinationOn(cfg) { // MR16: off lists no coordination route, so each is a 404
		mux.HandleFunc("PUT /v1/coordinations/{task}/draft", s.draftCoordination)
		mux.HandleFunc("POST /v1/coordinations/{task}/confirmations", s.confirmCoordination)
		mux.HandleFunc("POST /v1/coordinations/{task}/recovery", s.recoverCoordination)
	}
	// P17: the catalogue is mounted whenever the API is, not behind policyManagement.promotion.
	mux.HandleFunc(catalogueRoute, s.catalogue)
	// v4 D3: the banner's factory codes, for any signed-in human.
	mux.HandleFunc(InstallRoute, s.install)
	if s.promotionOn(cfg) { // stage 6: off lists no policy route, so each is a 404
		mux.HandleFunc("GET /v1/policy/evidence", s.policyEvidence)
		mux.HandleFunc("GET /v1/policy/repositories", s.policyRepositories)
		if s.Policies != nil {
			mux.HandleFunc("GET /v1/policy/policies", s.policyList)
			mux.HandleFunc("GET /v1/policy/policies/{name}", s.policyGet)
		}
		if s.Editor != nil {
			mux.HandleFunc("POST /v1/policy/preview", s.policyPreview)
			mux.HandleFunc("GET /v1/policy/decisions", s.policyDecisions)
		}
	}
	return mux
}

// apiDrain bounds Start's shutdown: under the manager's 20 s GracefulShutdownTimeout
// (internal/app's managerDrain) and the pod's 30 s grace, and long enough for a handler past its
// Task's create to file the issue.
const apiDrain = 15 * time.Second

// Start serves on api.listen until ctx ends, then drains for at most apiDrain, and returns only
// once the drain has (F-7): the manager waits on Start, so a handler between its Task's create
// and its issue finishes before the process exits.
func (s *Server) Start(ctx context.Context) error {
	ln, err := s.listen(ctx)
	if err != nil {
		return err
	}
	return serve(ctx, s.httpServer(), ln, apiDrain)
}

// listen binds api.listen. With api.tls it serves TLS 1.3 from that pair, re-read when
// cert-manager renews it (S-1, D6): the broker forwards each human's bearer token here, so it
// never crosses the cluster network in the clear. A pair that does not load fails the start.
func (s *Server) listen(ctx context.Context) (net.Listener, error) {
	cfg := s.Cfg.Current()
	var tlsCfg *tls.Config
	if t := cfg.API.TLS; t != nil {
		c, err := tlsreload.Config(t.CertFile, t.KeyFile, s.Log)
		if err != nil {
			return nil, fmt.Errorf("api: api.tls: %w", err)
		}
		tlsCfg = c
	}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.API.Listen)
	if err != nil {
		return nil, fmt.Errorf("api: listen %s: %w", cfg.API.Listen, err)
	}
	if tlsCfg != nil {
		return tls.NewListener(ln, tlsCfg), nil
	}
	return ln, nil
}

// httpServer is the API's listener with its five bounds.
func (s *Server) httpServer() *http.Server {
	return &http.Server{Handler: s.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 120 * time.Second, MaxHeaderBytes: 16 << 10}
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func refuse(w http.ResponseWriter, code int, reason string) {
	reply(w, code, map[string]string{"error": reason})
}

// refuseRepository refuses and counts a refusal by repository (v2 observability): the counter
// keeps only the repository codes, so its label stays bounded.
func (s *Server) refuseRepository(ctx context.Context, w http.ResponseWriter, code int, reason string) {
	if s.Metrics != nil {
		s.Metrics.AdmissionRefused(ctx, reason)
	}
	refuse(w, code, reason)
}

// createRun admits a run in §4's order — stop object, shape, repository, room, R37's owner
// defaults, R35's resume branch, the day's ledger — then creates it with the factory's own
// id and branch. Refusals name only codes: no claim or principal data echoes back (authn rule).
func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
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
	if p.Sponsored { // F4: a machine starts work only as a Task, never as a bare run
		refuse(w, http.StatusForbidden, "sponsored_only_tasks")
		return
	}
	if s.Stopped(r.Context()) { // §6.1: the stop object pauses intake, this API included
		refuse(w, http.StatusServiceUnavailable, "kill_switch")
		return
	}
	var in RunRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		refuse(w, http.StatusBadRequest, "bad_json")
		return
	}
	if reason := validate(in); reason != "" {
		refuse(w, http.StatusBadRequest, reason)
		return
	}
	// GitHub's names are case-insensitive, but spec.repository names the run's minted token:
	// a request is matched case-insensitively and rewritten to the config's spelling, the only one
	// ever written there.
	in.Repository = canonicalRepository(cfg.API.Repositories, in.Repository)
	if !slices.Contains(cfg.API.Repositories, in.Repository) {
		refuse(w, http.StatusForbidden, "repository_not_allowed") // OD-6
		return
	}
	// Spend per identity SI4: a refusal names the requester as authenticated. Resolving their
	// GitHub link here would cost the call mayWrite saves for a request every local rule admits.
	ctx := fmetrics.WithPrincipal(fmetrics.WithRepository(r.Context(), in.Repository), p.ID)
	if s.Owned == nil || !s.Owned(in.Repository) { // v2 D7: another factory's, or nobody's
		s.refuseRepository(ctx, w, http.StatusConflict, "repository_not_owned")
		return
	}
	if s.privateRefused(in.Repository) { // v4 D3
		s.refuseRepository(ctx, w, http.StatusForbidden, "private_code_unacknowledged")
		return
	}
	// The defaults come first: the request's fingerprint and the ledger's reservation both
	// carry what the run will get, not what the caller left out.
	if in.MaxTokens == 0 {
		in.MaxTokens = 2_000_000
	}
	if in.Model == "" {
		in.Model = "agent-default"
	}
	if in.BaseRef == "" {
		in.BaseRef = "main"
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) > maxKeyLen {
		refuse(w, http.StatusBadRequest, "bad_idempotency_key")
		return
	}
	all, err := s.Runs.List(r.Context())
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "runs_unavailable")
		return
	}
	hash := requestHash(in)
	id := ""
	if key != "" {
		var first *runs.Run
		var collision bool
		id, first, collision = s.keyedRun(p, key, all)
		if collision {
			refuse(w, http.StatusConflict, "idempotency_key_collision")
			return
		}
		if first != nil {
			// The room, branch and ledger checks below would meet this run's own claim and
			// reservation as busy: a retry is answered before them, with the first answer.
			if first.RequestHash != hash {
				refuse(w, http.StatusUnprocessableEntity, "idempotency_key_reused")
				return
			}
			w.Header().Set("Idempotent-Replayed", "true")
			reply(w, http.StatusCreated, map[string]string{"runId": first.ID, "branch": first.Branch})
			return
		}
	} else {
		id = s.NewRunID()
	}
	branch := "agent/" + id
	suspendable := false
	if in.RoomRef != "" {
		code, reason, attended := s.admitRoom(r.Context(), p, in, all)
		if code != 0 {
			s.refuseRepository(ctx, w, code, reason)
			return
		}
		branch = "agent/" + in.RoomRef // a room's runs share one branch (C3)
		// v2 suspend: only an attended room has a human whose approval a run may wait on.
		suspendable = attended && cfg.Sandbox.Suspend.Enabled
	}
	// R37 (owner default): internal data and triagers are the admins'. After the room checks
	// so a member naming the wrong class for a room they cannot enter learns that first, and
	// before the ledger so a refused request reserves nothing. System callers are allowlisted
	// one by one (R23) and are not humans in groups.
	if (in.DataClass == "internal" || in.Role == "triager") && p.Kind == envelope.ActorHuman &&
		!slices.Contains(p.Groups, s.Groups.Admin) {
		refuse(w, http.StatusForbidden, "admin_only")
		return
	}
	if in.ResumeBranch != "" {
		code, reason := s.admitResume(r.Context(), p, in.ResumeBranch, in.Repository, all)
		if code != 0 {
			s.refuseRepository(ctx, w, code, reason)
			return
		}
		branch = in.ResumeBranch
	}
	// v2 D4/D6, last before the ledger: the one check that asks GitHub, made only for a request
	// every local rule admitted, so a refused one reserves nothing and costs no call.
	if !s.mayWrite(r.Context(), cfg, p, in.Repository) {
		s.refuseRepository(ctx, w, http.StatusForbidden, "repository_permission")
		return
	}
	person, err := s.person(r.Context(), cfg, p, in.Repository)
	if err != nil {
		refuse(w, http.StatusServiceUnavailable, "identity_unavailable")
		return
	}
	if code, reason := s.reserve(ctx, cfg, p, person, id, in); code != 0 {
		if reason == reasonPersonOver {
			s.refuseRepository(fmetrics.WithPrincipal(ctx, person), w, code, reason) // the key over, as the shadow names it
			return
		}
		refuse(w, code, reason)
		return
	}
	spec := runs.Spec{RunID: id, Role: in.Role, Repository: in.Repository, BaseRef: in.BaseRef, Branch: branch,
		TaskText: in.Task.Text, TaskURL: in.Task.URL, Principal: p.ID, ChargedTo: person, DataClass: in.DataClass, Model: in.Model,
		RoomRef: in.RoomRef, MaxTokens: in.MaxTokens, MaxMinutes: 120,
		EgressProfiles: in.EgressProfiles, RequestHash: hash, Suspendable: suspendable}
	if err := s.Runs.Create(r.Context(), spec); err != nil {
		// Without this the reservation would eat the principal's day (and room) forever:
		// the meter drops entries only of runs that reach a terminal phase, and this one
		// never existed. Best effort — a leaked entry stays bounded by the day's ledger.
		s.dropReservation(r.Context(), id)
		refuse(w, http.StatusServiceUnavailable, "create_failed")
		return
	}
	reply(w, http.StatusCreated, map[string]string{"runId": id, "branch": branch})
}

// maxKeyLen bounds an Idempotency-Key; the broker's is <room>:<client>:<seq>, well under it.
const maxKeyLen = 255

// KeyLifetime is how long an Idempotency-Key answers with its first run: the ledger's day.
const KeyLifetime = 24 * time.Hour

// keyedRun derives the run id of (principal, key) and finds the run already made under it. The
// id is a hash, so a retry on any replica names the same claim, and the ledger's equal-entry
// pass (R48) holds the one reservation. The UTC day is in the hash, so a key frees itself when
// the day turns; the previous day's id is looked up too, so a retry across midnight still finds
// a run younger than KeyLifetime. first is nil when the key has no live run, and id is then the
// one to create.
//
// The id is 40 bits and run ids are public (agent/<id> branches), so a claim under a candidate id
// that another principal owns is never a replay: collision is true and the caller refuses with 409
// (creating would collide on the claim name anyway). Known gap: concurrent retries of one key
// either side of 00:00 UTC both find nothing and compute different ids, so they make two runs.
func (s *Server) keyedRun(p authn.Principal, key string, all []runs.Run) (id string, first *runs.Run, collision bool) {
	now := s.Now()
	id = keyedID(now, p.ID, key)
	for _, cand := range []string{id, keyedID(now.Add(-KeyLifetime), p.ID, key)} {
		for i := range all {
			switch {
			case all[i].ID != cand:
			case all[i].Principal != p.ID:
				return id, nil, true
			case now.Sub(all[i].Created) < KeyLifetime:
				return cand, &all[i], false
			}
		}
	}
	return id, nil, false
}

func keyedID(at time.Time, principal, key string) string {
	sum := sha256.Sum256([]byte(at.UTC().Format(ledger.DayLayout) + "\x00" + principal + "\x00" + key))
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:5]))
}

// requestHash fingerprints the admitted request, defaults applied: a replay with the same key
// and another fingerprint is a different request, not a retry. Egress profiles are a set (sorted,
// nil equals empty). It hashes every RunRequest field, so adding one changes the hash of a run
// made before the deploy: a retry across that rollout may get 422 once.
func requestHash(in RunRequest) string {
	in.EgressProfiles = slices.Sorted(slices.Values(in.EgressProfiles))
	if len(in.EgressProfiles) == 0 {
		in.EgressProfiles = nil
	}
	b, _ := json.Marshal(in) // plain strings, numbers and slices: Marshal cannot fail
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func validate(in RunRequest) string {
	switch {
	case !slices.Contains(roles, in.Role):
		return "bad_role"
	case (in.Task.Text == "") == (in.Task.URL == ""):
		return "exactly_one_of_task_text_or_url"
	case len(in.Task.Text) > 16384:
		return "task_text_too_long"
	case in.Role == "tester":
		return "tester_is_factory_only" // the factory starts the test writer, with its contract stage behind it (TO1)
	case in.Role == "reviewer" && !prURL.MatchString(in.Task.URL):
		return "reviewer_needs_a_pull_request_url"
	case in.DataClass != "public" && in.DataClass != "internal":
		return "bad_data_class"
	case in.RoomRef != "" && !envelope.ValidID(in.RoomRef):
		return "bad_room"
	case in.ResumeBranch != "" && !resumeBranchValid(in.ResumeBranch):
		return "bad_resume_branch"
	case in.ResumeBranch != "" && in.RoomRef != "":
		return "a_room_already_names_its_branch"
	case in.Model != "" && !slices.Contains(models, in.Model):
		return "bad_model"
	case in.DataClass == "internal" && config.PublicOnly(in.Model): // Moonshot processes in China, Mistral in the EU (F4)
		return "provider_alias_is_public_only"
	case in.MaxTokens < 0 || in.MaxTokens > config.RunTokenCeiling:
		return "max_tokens_out_of_range"
	case len(in.EgressProfiles) > 4:
		return "too_many_egress_profiles"
	// T4/T5 (identity design §2, §7): no direct GitHub or profile egress for internal data. The
	// AgentRun XRD refuses the claim too; this names the reason before anything is reserved.
	case in.DataClass == "internal" && len(in.EgressProfiles) > 0:
		return "internal_runs_take_no_egress_profiles"
	}
	for _, e := range in.EgressProfiles {
		if !slices.Contains(config.EgressProfiles, e) {
			return "bad_egress_profile"
		}
	}
	return ""
}

// resumeBranchValid is R35's shape: agent/ followed by the C2 id of the stopped run.
func resumeBranchValid(b string) bool {
	id, ok := strings.CutPrefix(b, "agent/")
	return ok && envelope.ValidID(id)
}

// admitRoom: the room exists, its data class matches, the caller may start a run in it by SP2's
// own rule, and it has no live run (SP2 §1: one Running run per room). Cross-replica exclusion
// is the ledger's (R50). attended reports an admitted room's approval profile.
func (s *Server) admitRoom(ctx context.Context, p authn.Principal, in RunRequest, all []runs.Run) (code int, reason string, attended bool) {
	// v4 MR2: a coordination parent's Room is for planning and progress, and an integration Task's
	// runs are the factory's: neither takes a run from this API, so no branch, token or PR follows.
	var task factoryv1.Task
	switch err := s.Rooms.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: in.RoomRef}, &task); {
	case err == nil && task.Spec.Kind != "":
		return http.StatusForbidden, "coordination_read_only", false
	case err != nil && !apierrors.IsNotFound(err):
		return http.StatusServiceUnavailable, "tasks_unavailable", false
	}
	var room v1alpha1.Room
	err := s.Rooms.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: in.RoomRef}, &room)
	if apierrors.IsNotFound(err) {
		return http.StatusBadRequest, "no_room", false
	}
	if err != nil {
		return http.StatusServiceUnavailable, "rooms_unavailable", false
	}
	if room.Spec.DataClass != in.DataClass {
		return http.StatusBadRequest, "data_class_differs_from_the_room", false
	}
	// v2 D3: the room's repository wins, never overwritten silently (the fingerprint would hash a
	// body the caller never sent). A room without one binds to none.
	if !sameRepository(room.Spec.Repository, in.Repository) {
		return http.StatusBadRequest, "repository_differs_from_the_room", false
	}
	// The broker's rule, not a copy of it: the driver or the owner (agents-admin owns every room).
	// StartRun is not UI-only (SP2 P18), so webUI does not change the answer.
	if !policy.Allowed(s.Groups.Resolve(&room, p, room.Status.Driver, false), policy.StartRun) {
		return http.StatusForbidden, "not_permitted", false
	}
	for _, x := range all {
		if x.RoomRef == in.RoomRef && !runs.Terminal(x.Phase) {
			return http.StatusConflict, "room_busy", false
		}
	}
	return 0, "", room.Spec.Approvals.Profile != "unattended"
}

// admitResume is R35's one caller-named branch: a stopped run's agent/<id>. A task's branch
// resumes with /factory retry, a room's needs the caller's right to start runs there, and a
// branch a live run holds is busy. The branch widens nothing: the agents' App may push any
// agent/** branch already; SC-14's run-reported head and SHA-bound verdicts (R52) keep foreign
// commits away from the merge.
//
// v2 D3: the branch keeps its repository, the one its newest run or its room names. A branch
// neither names is bound by D4 alone: the caller must write the repository they asked for.
func (s *Server) admitResume(ctx context.Context, p authn.Principal, branch, repository string, all []runs.Run) (int, string) {
	id := strings.TrimPrefix(branch, "agent/")
	var task factoryv1.Task
	switch err := s.Rooms.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: id}, &task); {
	case err == nil:
		return http.StatusForbidden, "task_branch"
	case !apierrors.IsNotFound(err):
		return http.StatusServiceUnavailable, "tasks_unavailable"
	}
	var room v1alpha1.Room
	switch err := s.Rooms.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: id}, &room); {
	case err == nil:
		if !policy.Allowed(s.Groups.Resolve(&room, p, room.Status.Driver, false), policy.StartRun) {
			return http.StatusForbidden, "not_permitted"
		}
		if !sameRepository(room.Spec.Repository, repository) {
			return http.StatusBadRequest, "repository_differs_from_the_branch"
		}
	case !apierrors.IsNotFound(err):
		return http.StatusServiceUnavailable, "rooms_unavailable"
	}
	var last *runs.Run
	for i, x := range all {
		if x.Branch != branch {
			continue
		}
		if !runs.Terminal(x.Phase) {
			return http.StatusConflict, "branch_busy"
		}
		if last == nil || x.Created.After(last.Created) {
			last = &all[i]
		}
	}
	if last != nil && !sameRepository(last.Repository, repository) {
		return http.StatusBadRequest, "repository_differs_from_the_branch"
	}
	return 0, ""
}

// sameRepository compares owner/name as GitHub does, case-insensitively. An empty bound names no
// repository, so it matches none.
func sameRepository(bound, asked string) bool {
	return bound != "" && strings.EqualFold(bound, asked)
}

// mayWrite is v2 D4 for a human — write or higher on GitHub, admins included — and D6 for a system
// caller: the repositories api.systemRepositories lists for it. Any doubt refuses.
func (s *Server) mayWrite(ctx context.Context, cfg *config.Config, p authn.Principal, repository string) bool {
	if p.Kind != envelope.ActorHuman {
		return slices.ContainsFunc(cfg.API.SystemRepositories[p.ID], func(r string) bool { return strings.EqualFold(r, repository) })
	}
	if s.Access == nil {
		return false
	}
	ok, err := s.Access.CanWrite(ctx, p, repository)
	return err == nil && ok
}

// reasonPersonOver is the API's refusal of a person past their day (D4), counted by repository.
const reasonPersonOver = "person_budget_exhausted"

// person is who a human's run is charged to while per-person caps are on (D1): their GitHub
// login, else their own principal; "" when the caps are off or the caller is a system.
func (s *Server) person(ctx context.Context, cfg *config.Config, p authn.Principal, repository string) (string, error) {
	if !cfg.Budgets.PerPerson.Enabled || p.Kind != envelope.ActorHuman {
		return "", nil
	}
	if s.People == nil {
		return p.ID, nil
	}
	who, err := s.People.Person(ctx, p, repository)
	if err != nil {
		return "", err
	}
	if who == "" {
		return p.ID, nil // no link: the sub is the person, and cannot label anyway (D1)
	}
	return who, nil
}

// reserve is R50's admission on the day's ledger (ledger.Reserve), its refusals said in HTTP. A run
// charged to a person reserves the fleet and the person in one entry (D2, D3). A run request caps
// in tokens, so it reserves its maxTokens at the reference rate until F4 brings max_cost (TC7).
func (s *Server) reserve(ctx context.Context, cfg *config.Config, p authn.Principal, person, id string, in RunRequest) (int, string) {
	b := cfg.Budgets
	mine := ledger.Reservation{Principal: p.ID, Room: in.RoomRef, MaxMicroUSD: config.RefMicroUSD(in.MaxTokens),
		Repository: in.Repository, Role: in.Role}
	if person != "" {
		mine.Principal, mine.Person = runs.PrincipalFactory, person
	}
	over, err := ledger.Reserve(ctx, s.Rooms, s.Namespace, s.Now(), id, mine, b.DailyMicroUSD(mine.Principal), b.DailyMicroUSD(person), b.EnforcePrincipal)
	switch {
	case errors.Is(err, ledger.ErrPersonOverBudget):
		return http.StatusTooManyRequests, reasonPersonOver
	case errors.Is(err, ledger.ErrOverBudget):
		return http.StatusTooManyRequests, "over_budget"
	case errors.Is(err, ledger.ErrRunBusy):
		return http.StatusConflict, "run_busy"
	case errors.Is(err, ledger.ErrRoomBusy):
		return http.StatusConflict, "room_busy"
	case errors.Is(err, ledger.ErrBusy):
		return http.StatusServiceUnavailable, "ledger_busy"
	case errors.Is(err, ledger.ErrMeterBlind):
		return http.StatusServiceUnavailable, "meter_blind"
	case err != nil:
		return http.StatusServiceUnavailable, "ledger_unavailable"
	}
	if over != "" {
		s.Metrics.PrincipalShadow(ctx, over) // R3: counted, not enforced
	}
	return 0, ""
}

// dropReservation undoes reserve when the run creation itself failed. Best effort: a leaked
// entry costs one day of budget, while a delete that fights a concurrent replica could drop
// another admission's reservation instead.
func (s *Server) dropReservation(ctx context.Context, id string) {
	key := types.NamespacedName{Namespace: s.Namespace, Name: ledger.Prefix + s.Now().UTC().Format(ledger.DayLayout)}
	cm := &corev1.ConfigMap{}
	if err := s.Rooms.Get(ctx, key, cm); err != nil {
		return
	}
	if _, ok := cm.Data[ledger.Reserved+id]; !ok {
		return
	}
	delete(cm.Data, ledger.Reserved+id)
	_ = s.Rooms.Update(ctx, cm)
}

// canonicalRepository is the spelling listed holds for asked, whatever its case, or asked itself when
// the list does not name it (the caller's exact check then refuses it).
func canonicalRepository(listed []string, asked string) string {
	for _, r := range listed {
		if strings.EqualFold(r, asked) {
			return r
		}
	}
	return asked
}

// privateRefused is PrivateRefused, nil refusing none.
func (s *Server) privateRefused(repository string) bool {
	return s.PrivateRefused != nil && s.PrivateRefused(repository)
}
