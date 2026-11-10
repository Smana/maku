// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	factoryv1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/factory/api"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/demo"
	"github.com/Smana/maku/internal/factory/evidence"
	"github.com/Smana/maku/internal/factory/fence"
	"github.com/Smana/maku/internal/factory/fluxwatch"
	"github.com/Smana/maku/internal/factory/fmetrics"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/factory/intake"
	"github.com/Smana/maku/internal/factory/killswitch"
	"github.com/Smana/maku/internal/factory/ledger"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/meter"
	"github.com/Smana/maku/internal/factory/policyview"
	"github.com/Smana/maku/internal/factory/reconciler"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/factory/taskid"
	"github.com/Smana/maku/internal/factory/tracing"
	"github.com/Smana/maku/internal/factory/triage"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/metrics"
	"github.com/Smana/maku/internal/policy"
	"github.com/Smana/maku/internal/redact"
	"github.com/Smana/maku/internal/runwatch"
	"github.com/Smana/maku/internal/version"
)

// The factory's outbound bounds and GitHub's API root.
const (
	githubAPI     = "https://api.github.com/"
	githubTimeout = 15 * time.Second
	brokerTimeout = 15 * time.Second
	vmTimeout     = 10 * time.Second
	// C7's own deadline is 2 s.
	classifierTimeout = 3 * time.Second
	// pingEvery keeps the installation token warm and forge.Healthy true on every replica.
	pingEvery = time.Minute
	// traceDrain bounds the span exporter's flush at exit, inside the pod's grace.
	traceDrain = 2 * time.Second
	// fenceEvery is how often each repository's fence file is read (v2 D7); fenceStale how long a
	// failed read keeps the last answer before the repository counts as not owned.
	fenceEvery = 5 * time.Minute
	fenceStale = 15 * time.Minute
)

// RunFactory runs agent-factory (SP3): the Task reconciler, the issue poller, the run meter
// and the stop's sweep on the leader; the run-request API on :8082 (TLS when api.tls is set), :9090 metrics and probes
// and the GitHub ping on every replica. getenv reads FACTORY_CONFIG and POD_NAMESPACE. A
// config that does not load fails the start, so a bad config fails the rollout rather than
// the running factory (§4).
func RunFactory(ctx context.Context, log *slog.Logger, getenv func(string) string) error {
	if err := runFactory(ctx, log, getenv); err != nil {
		return fmt.Errorf("agent-factory: %w", err)
	}
	return nil
}

func factoryEnv(getenv func(string) string) (cfgPath, ns string, err error) {
	var missing []error
	for _, kv := range []struct {
		name string
		dst  *string
	}{{"FACTORY_CONFIG", &cfgPath}, {"POD_NAMESPACE", &ns}} {
		if *kv.dst = getenv(kv.name); *kv.dst == "" {
			missing = append(missing, fmt.Errorf("%s is not set", kv.name))
		}
	}
	return cfgPath, ns, errors.Join(missing...)
}

// warnIgnored names each key the config sets that this release accepts and ignores: a file written
// for the previous release loads, and its owner learns which key to move to (#155).
func warnIgnored(log *slog.Logger, cfg *config.Config) {
	for _, k := range cfg.Ignored {
		log.Warn("config key ignored: the daily budgets are in USD", "key", k.Key, "use", k.Use)
	}
}

// warnConfig logs each sentence the config loaded with (a later release refuses it) and sets the
// gauge of a live class that declares no holdoutChecks (TO5).
func warnConfig(log *slog.Logger, cfg *config.Config, m *fmetrics.Set) {
	for _, w := range cfg.Warnings {
		log.Warn("config warning", "warning", w)
	}
	if m == nil {
		return
	}
	for _, r := range cfg.Repos() {
		for _, name := range slices.Sorted(maps.Keys(r.Classes)) {
			if cl := r.Classes[name]; cl.Live && len(cl.HoldoutChecks) == 0 {
				m.ClassWithoutHoldout(context.Background(), r.Name, name)
			}
		}
	}
}

// brokerClient is the HTTP client for the broker: its CA when broker.caFile is set, the system
// roots otherwise (demo mode, where the chart's CA is not mounted).
func brokerClient(cfg *config.Config) (*http.Client, error) {
	var roots *x509.CertPool
	if cfg.Broker.CAFile != "" {
		var err error
		if roots, err = rooms.LoadCA(cfg.Broker.CAFile); err != nil {
			return nil, err
		}
	}
	return httpx.New(brokerTimeout, roots), nil
}

// usageSource reads run usage from meter.url, or is meter.Off when none is set (demo mode).
func usageSource(cfg *config.Config) (meter.Source, error) {
	if cfg.Meter.URL == "" {
		return meter.Off{}, nil
	}
	return meter.NewVM(cfg.Meter.URL, cfg.Meter.Query, cfg.Runs, httpx.New(vmTimeout, nil))
}

// throttle maps gateway 429s to runs; nil without meter.logsUrl.
func throttle(cfg *config.Config) meter.Throttled {
	if cfg.Meter.LogsURL == "" {
		return nil
	}
	return meter.VL{Kind: cfg.Runs, URL: cfg.Meter.LogsURL, Query: cfg.Meter.ThrottleQuery, HC: httpx.New(vmTimeout, nil)}
}

// humanVerifier fetches the humans' JWKS. In demo mode there is none: nil, and the Authenticator
// refuses every human token with demo_no_human_tokens.
func humanVerifier(ctx context.Context, cfg *config.Config) (*authn.Verifier, error) {
	if cfg.Demo {
		return nil, nil
	}
	return authn.NewVerifier(ctx, cfg.API.HumanIssuer, cfg.API.HumanJWKS)
}

// demoGitToken keeps the runs' shared git Secret filled with a token from the factory App, on
// the served repositories (demo mode: one App does both the factory's and the runs' work).
func demoGitToken(cfg *config.Config, c client.Client, log *slog.Logger) (*demo.GitToken, error) {
	var owner string
	var names []string
	for _, r := range cfg.Repos() {
		o, n, _ := strings.Cut(r.Name, "/")
		if owner != "" && !strings.EqualFold(o, owner) {
			return nil, fmt.Errorf("demo: repositories %q and %q have different owners: one App installation serves one account", owner+"/"+names[0], r.Name)
		}
		owner = o
		names = append(names, n)
	}
	hc := httpx.New(githubTimeout, nil)
	opts := forge.MintOptions{AppIDFile: cfg.GitHub.AppIDFile, KeyFile: cfg.GitHub.PrivateKeyFile,
		Owner: owner, Repositories: names, Permissions: demo.Permissions()}
	return &demo.GitToken{Client: c, Namespace: cfg.Runs.Namespace, Every: 30 * time.Minute, Log: log,
		Mint: func(ctx context.Context) (forge.Token, error) { return forge.Mint(ctx, hc, githubAPI, opts) }}, nil
}

// requirePublic stops a demo start when a served repository is private or its visibility cannot
// be read: the demo's agents can read the model key and the GitHub token, so nothing private is
// served. public answers for one repository.
func requirePublic(ctx context.Context, names []string, public func(ctx context.Context, name string) (bool, error)) error {
	for _, name := range names {
		ok, err := public(ctx, name)
		if err != nil {
			return fmt.Errorf("demo: reading the visibility of %s: %w", name, err)
		}
		if !ok {
			return fmt.Errorf("demo: %s is private; the demo serves public repositories only", name)
		}
	}
	return nil
}

func runFactory(ctx context.Context, log *slog.Logger, getenv func(string) string) error {
	cfgPath, ns, err := factoryEnv(getenv)
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	warnIgnored(log, cfg)
	exp, err := metrics.NewExporter(metrics.FactoryBuildInfo, version.Version)
	if err != nil {
		return err
	}
	defer shutdownMetrics(ctx, log, exp)
	sink, tp, shutdownTrace, err := taskSink(ctx, cfg.Tracing.OTLPEndpoint)
	if err != nil {
		return err
	}
	defer shutdownTrace(context.WithoutCancel(ctx))

	bc, err := brokerClient(cfg)
	if err != nil {
		return err
	}
	broker, err := rooms.New(cfg.Broker.URL, cfg.Broker.TokenFile, bc, tp)
	if err != nil {
		return err
	}
	vm, err := usageSource(cfg)
	if err != nil {
		return err
	}
	conns, err := connectRepositories(ctx, log, cfg)
	if err != nil {
		return err
	}
	if cfg.Demo {
		err := requirePublic(ctx, conns.names(), func(ctx context.Context, name string) (bool, error) {
			c, _ := conns.get(name)
			return c.gh.Public(ctx)
		})
		if err != nil {
			return err
		}
	}
	mgr, err := newFactoryManager(log, ns, cfg.Runs)
	if err != nil {
		return err
	}
	m, err := fmetrics.New(exp.Meter(), mgr.GetClient(), ns, elected(mgr))
	if err != nil {
		return err
	}
	m.ListRepositories(conns.names()...)
	warnConfig(log, cfg, m)
	m.ChargePeople(cfg.Budgets.PerPerson.Enabled)
	sponsors := map[string]string{}
	for _, sp := range cfg.API.Sponsored {
		sponsors[sp.Principal()] = factoryv1.Person(sp.Sponsor)
	}
	for _, ip := range cfg.API.InternalPrincipals {
		sponsors[ip.Principal()] = factoryv1.Person(ip.Sponsor)
	}
	m.Sponsors(sponsors)
	// v2 D7: a repository is acted on only while its default branch names this factory.
	fenced := &fence.Fence{Name: cfg.Factory, Repositories: conns.names(), Read: conns.fenceFile,
		Every: fenceEvery, Stale: fenceStale, Now: time.Now, OnState: m.RepositoryOwned, Log: log}
	// Stages 1, 3 and 5 (merge policy): observed and compared, each merge decision decided again
	// under it, and its approval subset's grants consulted there. Stage 4: a cut-over repository
	// is decided on it, and every reader reads its classes from it, aged out at the fence's window.
	var snapshots func(string) (mergepolicy.Snapshot, bool)
	var authority func(string) (mergepolicy.Snapshot, error)
	if cfg.PolicyManagement.Read {
		w := &mergepolicy.Watch{Cfg: cfg, Metrics: m, Log: log, Now: time.Now, Stale: fenceStale}
		fenced.OnFile = w.Observe
		if cfg.PolicyManagement.DualEvaluation || cfg.PolicyManagement.ApprovalMemory {
			snapshots = w.Snapshot
		}
		if cfg.PolicyManagement.RepositoryAuthority {
			cfg.SetClassSource(w.Classes)
			authority = w.Authority
		}
	}
	costs := &meter.Costs{} // the meter writes each run's cost, the reconciler sums a task's
	runUSD := map[string]float64{}
	for name, t := range cfg.Tiers {
		if t.RunUSD > 0 {
			runUSD[name] = t.RunUSD
			m.RunBudgetUSD(ctx, name, t.RunUSD)
		}
	}
	// The day ledgers are invisible to the manager's cache: its ConfigMap informer carries a
	// metadata.name selector for the stop object (factoryManagerOptions). R50's reservation, the
	// meter's settle and Queued's daily cap (which reserves its runs there, #78) read and write
	// date-named ledgers, so through a live client.
	live, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		return err
	}
	rc := runs.Client{C: mgr.GetClient(), Kind: cfg.Runs}
	perms := conns.permissions(cfg.API.Access)
	var c7 triage.Classifier // nil while triage.classifierURL is unset: no HTTP client, no call
	if cfg.Triage.ClassifierURL != "" {
		c7 = triage.HTTPClassifier{URL: cfg.Triage.ClassifierURL, HC: httpx.New(classifierTimeout, nil)}
	}
	rec := &reconciler.Reconciler{Kind: cfg.Runs, Client: mgr.GetClient(), Ledger: live, Namespace: ns, Cfg: cfg, Clients: conns.task, Owned: fenced.Owned,
		Policy: snapshots, Authority: authority, CanWrite: perms.CanWrite, Runs: rc, Rooms: broker,
		Triage: triage.Classify{Cfg: cfg, C: c7,
			Clients: conns.class},
		Metrics: m, Now: time.Now, Nonce: taskid.Random, Log: log,
		Trace: sink, Costs: costs}
	if cfg.PolicyManagement.ApprovalMemory { // stage 5: grants read from the broker, never cached
		rec.Grants = broker
	}
	if cfg.AnyFlux() {
		// Uncached: the reader serves a few reads per monitor pass, and a cache would start an
		// informer per Flux kind (and need watch, which the ClusterRole does not grant).
		rec.Flux = fluxwatch.Reader{C: mgr.GetAPIReader()}
	}
	if err := rec.SetupWithManager(mgr); err != nil {
		return err
	}
	// stopped is §6.1's pause: the stop object stops intake, and from phase 5 the API is intake
	// too — it refuses 503 kill_switch exactly when the poller stands down.
	stopped := func(ctx context.Context) bool {
		on, err := killswitch.Engaged(ctx, mgr.GetClient(), ns)
		return on || err != nil // intake pauses on doubt
	}
	humansV, err := humanVerifier(ctx, cfg)
	if err != nil {
		return err
	}
	var systemsV *authn.Verifier
	if cfg.API.SystemIssuer != "" {
		if systemsV, err = authn.NewVerifier(ctx, cfg.API.SystemIssuer, cfg.API.SystemJWKS); err != nil {
			return err
		}
	}
	clientIDs := func() []string { // re-read: the ExternalSecret may rotate a rebuilt client
		out := make([]string, 0, len(cfg.API.ClientIDFiles))
		for _, f := range cfg.API.ClientIDFiles {
			b, _ := os.ReadFile(filepath.Clean(f))
			out = append(out, strings.TrimSpace(string(b)))
		}
		return out
	}
	// The two groups R23 admits: api.groups, which defaults to the values the broker's config carries.
	groups := policy.Groups{Admin: cfg.API.Groups.Admin, Member: cfg.API.Groups.Member}
	access := conns.access(log, cfg, perms)
	people, _ := access.(api.PersonResolver) // D1: the login the write check reads is the person charged
	auth := &api.Authenticator{Humans: humansV, Groups: groups, ClientIDs: clientIDs,
		Systems: systemsV, SystemAllow: cfg.API.SystemPrincipals}
	if humansV != nil && len(cfg.API.Sponsored)+len(cfg.API.InternalPrincipals) > 0 { // F4: the third branch, only while a principal is declared
		clients := make([]authn.SponsoredClient, 0, len(cfg.API.Sponsored)+len(cfg.API.InternalPrincipals))
		client := func(sp config.Sponsored, internal bool) authn.SponsoredClient {
			return authn.SponsoredClient{Name: sp.Name, Internal: internal, ClientID: func() string {
				b, _ := os.ReadFile(filepath.Clean(sp.ClientIDFile)) // re-read, as clientIDs: a rebuilt client rotates
				return strings.TrimSpace(string(b))
			}}
		}
		for _, sp := range cfg.API.Sponsored {
			clients = append(clients, client(sp, false))
		}
		for _, ip := range cfg.API.InternalPrincipals {
			clients = append(clients, client(ip.Sponsored, true))
		}
		auth.Sponsored = &authn.Sponsored{V: humansV, Clients: clients, Exclude: clientIDs,
			Audience: func() string {
				b, _ := os.ReadFile(filepath.Clean(cfg.API.ProjectIDFile))
				return strings.TrimSpace(string(b))
			}}
	}
	// The self-serve link: the broker's records, and the same ZITADEL resolver api.access reads.
	var logins api.LoginResolver
	if ga, ok := access.(api.GitHubAccess); ok {
		logins = ga.Identity
	}
	apiSrv := &api.Server{Auth: auth, Log: log,
		Cfg: cfg, Groups: groups, Runs: rc, Rooms: live, Namespace: ns, Stopped: stopped, SlackLinks: broker, Logins: logins,
		NewRunID: taskid.Random, Now: time.Now, Metrics: m, Access: access, People: people, Owned: fenced.Owned,
		Forges: func(repo string) api.IssueFiler {
			c, ok := conns.get(repo)
			if !ok || c.gh == nil {
				return nil // a nil interface, never a typed-nil *forge.GitHub (C24)
			}
			return c.gh
		}}
	// Coordination (v4 MR16): Prerequisites stays nil because neither capability it must prove,
	// repository-declared class authority (v4 item 3) nor hardened safe outputs (v4 item 4), has
	// shipped, so execute refuses coordination_prerequisites and files nothing; plan drafts and
	// confirms only. coordination.Prerequisites composes the two once class authority has a port;
	// hardening.Evaluator, its second half, answers false through stage H4 (hardening design).
	// Each repository's checks and filing go through that repository's own App connection.
	coord := &coordination.Service{Client: live, Namespace: ns, Cfg: cfg, Prerequisites: nil, Now: time.Now, Metrics: m,
		Checks: coordination.GitHubChecks{Cfg: cfg, Forges: func(repo string) coordination.CheckForge {
			if c, ok := conns.get(repo); ok && c.gh != nil {
				return c.gh
			}
			return nil // a nil interface, never a typed-nil *forge.GitHub
		}},
		Forges: func(repo string) coordination.IssueForge {
			if c, ok := conns.get(repo); ok && c.gh != nil {
				return c.gh
			}
			return nil
		}}
	apiSrv.Coordination, rec.Coordination = coord, coord // the manager has not started: rec reads it from its first pass
	// Stage 6 (merge policy): each repository's promotion evidence, through its own App connection.
	var evidenceRefresh *evidence.Refresher
	if cfg.PolicyManagement.Promotion {
		ev := &evidence.Service{Cfg: cfg, Now: time.Now, Log: log, Sources: func(repo string) (evidence.Source, bool) {
			c, ok := conns.get(repo)
			if !ok || c.gh == nil {
				return nil, false
			}
			return evidenceSource{c.gh}, true
		}}
		apiSrv.Evidence = ev
		evidenceRefresh = &evidence.Refresher{Service: ev, Owned: fenced.Owned, OnAge: m.PolicyEvidenceAge}
		apiSrv.Editor = policyEditor{&policyview.Service{Cfg: cfg, Reports: ev, Comments: func(repo string) (policyview.CommentReader, bool) {
			c, ok := conns.get(repo)
			if !ok || c.gh == nil {
				return nil, false
			}
			return commentReader(c.gh), true
		}}}
	}
	// The api intake and factory/publish scan what they publish, and both fail closed without it.
	redactor, err := redact.New()
	if err != nil {
		return err
	}
	apiSrv.Redact = redactor
	proposal := func(ctx context.Context, t *factoryv1.Task) (string, error) {
		text, err := rec.Proposal(ctx, t)
		if errors.Is(err, reconciler.ErrNoProposal) {
			return "", intake.ErrNoProposal
		}
		return text, err
	}
	var runnables []manager.Runnable
	if evidenceRefresh != nil {
		runnables = append(runnables, evidenceRefresh)
	}
	if cfg.Demo {
		g, err := demoGitToken(cfg, live, log)
		if err != nil {
			return err
		}
		runnables = append(runnables, g)
	}
	for _, rp := range cfg.Repos() {
		c, _ := conns.get(rp.Name)
		name := rp.Name
		poller := &intake.IssuePoller{Forge: c.gh, Client: mgr.GetClient(), Namespace: ns, Cfg: cfg,
			Repository: name, Demo: cfg.Demo, Errors: m, Log: log.With("repository", name), Stopped: stopped, Owned: fenced.Owned, Ledger: live,
			CanWrite: func(ctx context.Context, login string) (bool, error) { return perms.CanWrite(ctx, name, login) },
			Proposal: proposal}
		poller.Redact = redactor
		runnables = append(runnables, poller)
	}
	if cfg.RunLore.Listen != "" { // R14: no runlore block, no intake; its port serves nothing
		runlore, ok := conns.get(cfg.RunLoreRepository())
		if !ok {
			return fmt.Errorf("runlore.repository %s has no GitHub connection", cfg.RunLoreRepository())
		}
		// FA-8: RunLore pushes findings over the tailnet; the token file is re-read per request,
		// so the ExternalSecret's rotations need no restart (AGENTS.md).
		runnables = append(runnables, &intake.RunLore{Forge: runlore.gh, Owned: fenced.Owned, Client: mgr.GetClient(), Namespace: ns, Cfg: cfg,
			Token: func() string {
				b, _ := os.ReadFile(filepath.Clean(cfg.RunLore.TokenFile))
				return strings.TrimSpace(string(b))
			},
			Stopped: stopped, Now: time.Now, Errors: m, Log: log})
	}
	for _, r := range append(runnables,
		&intake.Scheduler{Clients: conns.schedule, Owned: fenced.Owned, Client: mgr.GetClient(), Namespace: ns, Cfg: cfg,
			Stopped: stopped, Now: time.Now, Errors: m, Log: log},
		// MC10: off unless a class adopts; leader-only, through the Scheduler's connections.
		&intake.Adopter{Clients: conns.schedule, Owned: fenced.Owned, Client: mgr.GetClient(), Namespace: ns, Cfg: cfg,
			Stopped: stopped, Now: time.Now, Errors: m, Log: log},
		&meter.Meter{Runs: rc, Source: vm, PerReplica: cfg.Meter.ByReplica(), Every: cfg.Poll.Meter.Duration, OnRevoke: m.RunRevoked, Log: log,
			OnCharge: m.Charged, OnCharges: m.RunCharges,
			Prices: cfg.Meter.Prices, RunUSD: runUSD, OnCost: runCost(m), OnLateCost: m.LateCost, OnSpend: m.RunSpend, OnSettled: m.RunSettled, Costs: costs,
			OnFallback: func(ctx context.Context, why string) { m.RunBudgetFallback(ctx, "run", why) },
			OnUnpriced: m.ModelUnpriced, OnInputExcludesCache: m.InputExcludesCache,
			Throttle: throttle(cfg),
			Budgets:  cfg.Budgets, Profiles: cfg.HarnessProfiles, B1Ceiling: config.RunTokenCeiling, Now: time.Now,
			Day:          func(p string, spent, c int64) { m.BudgetDay(ctx, p, spent, c) },
			OnUsageError: m.MeterUsageReadError, OnSucceeded: m.MeterSucceeded, OnOvershoot: m.RunOvershoot,
			Ledger: live, Tasks: mgr.GetClient(), Namespace: ns},
		&killswitch.Sweeper{Reader: mgr.GetClient(), Namespace: ns, Runs: rc, Every: 15 * time.Second,
			Release: func(ctx context.Context, r runs.Run) error {
				return ledger.Release(ctx, live, ns, time.Now(), r.ID) // admission reads today's ledger only
			},
			OnSweep: func(revoked []runs.Run) {
				for _, r := range revoked {
					m.Revoked(fmetrics.WithPrincipal(ctx, r.Payer()), "manual")
				}
			}, Log: log},
		apiSrv, fenced,
		&pinger{ping: conns.ping, every: pingEvery, up: func() bool { return conns.healthy(time.Now()) }, record: m.GitHubUp},
	) {
		if err := mgr.Add(r); err != nil {
			return fmt.Errorf("manager: %w", err)
		}
	}
	down := newDrainFlag(ctx)
	ops := factoryOps(mgr.GetCache().WaitForCacheSync, down.Draining, exp.Handler())
	return serveFactory(ctx, log, mgr, ops, down, ns)
}

// serveFactory runs the manager until ctx ends or it fails; :9090 stops last, so the drain's
// final counts are scraped and /readyz answers 503 meanwhile, as the broker's does.
func serveFactory(ctx context.Context, log *slog.Logger, mgr manager.Manager, ops http.Handler, down *drainFlag, ns string) error {
	var lc net.ListenConfig
	opsLn, err := lc.Listen(ctx, "tcp", opsAddr)
	if err != nil {
		return fmt.Errorf("ops listener: %w", err)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	opsCtx, stopOps := context.WithCancel(context.WithoutCancel(ctx))
	defer stopOps()
	opsDone := make(chan error, 1)
	go func() {
		err := serveHTTP(opsCtx, opsServer(ops, log), opsLn, opsDrain)
		if err != nil {
			cancelRun() // :9090 failing takes the rest down: the pod is unprobeable
		}
		opsDone <- err
	}()
	g, gctx := errgroup.WithContext(runCtx)
	stopWatch := context.AfterFunc(gctx, down.Begin) // the manager dying is a shutdown too
	defer stopWatch()
	g.Go(func() error {
		if err := mgr.Start(gctx); err != nil {
			return fmt.Errorf("manager: %w", err)
		}
		return nil
	})
	log.Info("agent-factory starting", "version", version.Version, "namespace", ns)
	err = g.Wait()
	stopOps()
	return errors.Join(err, <-opsDone)
}

// connectMerger connects the merger App, or answers the fail-closed forge.Absent when its key
// is not mounted and no class needs it (#57: the chart mounts the Secret optional). A class
// live or in shadow reads checks through the merger, so for it a missing key stops the start.
func connectMerger(ctx context.Context, log *slog.Logger, cfg *config.Config, repository string) (forge.Merger, error) {
	idFile := cfg.GitHub.MergerAppIDFile
	if idFile != "" {
		_, err := os.Stat(filepath.Clean(idFile))
		switch {
		case err == nil:
		case errors.Is(err, os.ErrNotExist) && !cfg.NeedsMerger():
			idFile = ""
		case errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("merger App key %s is missing, and a live or shadow class needs it: create the agent-factory-merger Secret or turn the class off", idFile)
		}
	}
	if idFile == "" {
		log.Warn("no merger App key: checks, merges and reverts fail closed until it is mounted and the pod restarts")
		return forge.Absent{}, nil
	}
	return forge.Connect(ctx, forge.Options{Repository: repository, AppIDFile: cfg.GitHub.MergerAppIDFile,
		PrivateKeyFile: cfg.GitHub.MergerKeyFile, APIURL: githubAPI, UserAgent: "agent-factory-merger/" + version.Version,
		HTTP: httpx.New(githubTimeout, nil), Now: time.Now, Permissions: forge.MergerPermissions(cfg.AnyCheckReruns(), cfg.AnyLive() || cfg.AnyMonitored(), cfg.AnyGateStatus())})
}

// taskSink exports task spans (R46) when tracing.otlpEndpoint is set, and is a nil interface
// otherwise, never a typed nil the reconciler would call. The exporter dials lazily: an
// unreachable collector loses spans, never the factory's start.
func taskSink(ctx context.Context, endpoint string) (tracing.Sink, trace.TracerProvider, func(context.Context), error) {
	if endpoint == "" {
		return nil, nil, func(context.Context) {}, nil
	}
	exp, err := tracing.NewOTLP(ctx, endpoint)
	if err != nil {
		return nil, nil, nil, err
	}
	return exp, exp.Provider(), func(ctx context.Context) {
		dctx, cancel := context.WithTimeout(ctx, traceDrain)
		defer cancel()
		_ = exp.Shutdown(dctx)
	}, nil
}

// factoryOps is :9090 for the factory: /readyz once the caches synced; /startupz at once, since
// the server starts only after the config parsed. Readiness does not look at GitHub (#58): an
// outage would unready every replica at once, and an unready replica serves nothing less than a
// ready one — the API and the intake need no GitHub call to answer, and the loops that do
// retry on their own. The outage shows in the loops' error logs and the intake error counter.
func factoryOps(synced func(context.Context) bool, draining func() bool, metricsH http.Handler) http.Handler {
	noDependency := func(context.Context) error { return nil }
	started := func(context.Context) (bool, error) { return true, nil }
	return opsHandler(noDependency, started, boundedSync(synced), draining, metricsH)
}

// elected reports whether this replica leads; the leader-only gauges read it.
func elected(mgr manager.Manager) func() bool {
	return func() bool {
		select {
		case <-mgr.Elected():
			return true
		default:
			return false
		}
	}
}

// factoryManagerOptions caches Tasks, Rooms and the stop object in the factory's namespace and
// kind's AgentRuns in theirs only: the chart's Roles grant exactly that. Unstructured reads stay
// uncached, so a stop's run list sees the API server (ruling SN), not the watch.
func factoryManagerOptions(ns string, kind runwatch.Kind) ctrl.Options {
	runObj := &unstructured.Unstructured{}
	runObj.SetGroupVersionKind(kind.GVK())
	drain := managerDrain
	here := map[string]cache.Config{ns: {}}
	return ctrl.Options{
		LeaderElection: true, LeaderElectionID: "agent-factory.agents.ogenki.io", LeaderElectionNamespace: ns,
		LeaderElectionReleaseOnCancel: true, GracefulShutdownTimeout: &drain,
		// :9090 serves the agent_factory_* set and the probes itself.
		Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&factoryv1.Task{}: {Namespaces: here},
			&v1alpha1.Room{}:  {Namespaces: here},
			&corev1.ConfigMap{}: {Namespaces: here,
				Field: fields.OneTermEqualSelector("metadata.name", killswitch.ConfigMap)},
			runObj: {Namespaces: map[string]cache.Config{kind.Namespace: {}}},
		}},
		Client: client.Options{Cache: &client.CacheOptions{Unstructured: false}},
	}
}

// newFactoryManager builds the manager on factoryManagerOptions, logging through log.
func newFactoryManager(log *slog.Logger, ns string, kind runwatch.Kind) (ctrl.Manager, error) {
	lr := logr.FromSlogHandler(log.Handler())
	// controller-runtime and client-go's leader election log through these globals.
	ctrl.SetLogger(lr)
	klog.SetLogger(lr)
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	scheme, err := factoryScheme(kind)
	if err != nil {
		return nil, err
	}
	opts := factoryManagerOptions(ns, kind)
	opts.Scheme, opts.Logger = scheme, lr
	mgr, err := ctrl.NewManager(restCfg, opts)
	if err != nil {
		return nil, fmt.Errorf("manager: %w", err)
	}
	return mgr, nil
}

// factoryScheme is the manager's scheme: client-go's, the factory's own types, Maku's (for Room),
// and the runs kind.
func factoryScheme(kind runwatch.Kind) (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{clientgoscheme.AddToScheme, factoryv1.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			return nil, err
		}
	}
	runs.Scheme(scheme, kind)
	return scheme, nil
}

// pinger probes GitHub on every replica, leader or not: a follower makes no other call to it. A
// failed ping is not an error to act on. The pinger reports whether GitHub answered recently as
// agent_factory_github_up, the outage signal now that /readyz ignores GitHub (#58).
type pinger struct {
	ping  func(context.Context) error
	every time.Duration
	// up says whether a call succeeded recently; record publishes it. A nil record reports nothing.
	up     func() bool
	record func(ctx context.Context, up bool)
	// ticker paces the pings; nil means a time.Ticker.
	ticker func(d time.Duration) (c <-chan time.Time, stop func())
}

// NeedLeaderElection is false: every replica reports its own view of GitHub.
func (*pinger) NeedLeaderElection() bool { return false }

// Start pings at once, then every period, until ctx ends.
func (p *pinger) Start(ctx context.Context) error {
	tick, stop := p.tick()
	defer stop()
	for {
		_ = p.ping(ctx)
		if p.record != nil {
			p.record(ctx, p.up())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick:
		}
	}
}

func (p *pinger) tick() (<-chan time.Time, func()) {
	if p.ticker != nil {
		return p.ticker(p.every)
	}
	t := time.NewTicker(p.every)
	return t.C, t.Stop
}

// evidenceSource is a repository's connection as the promotion evidence reads it: the default
// branch is RepositoryInfo's.
type evidenceSource struct{ *forge.GitHub }

func (e evidenceSource) DefaultBranch(ctx context.Context) (string, error) {
	info, err := e.RepositoryInfo(ctx)
	return info.DefaultBranch, err
}

// commentReader is a pull request's 50 newest comments as the decision timeline reads them.
func commentReader(gh *forge.GitHub) policyview.CommentReader {
	return func(ctx context.Context, pr int) ([]policyview.Comment, error) {
		cs, err := gh.RecentComments(ctx, pr)
		out := make([]policyview.Comment, 0, len(cs))
		for _, c := range cs {
			out = append(out, policyview.Comment{ID: c.ID, Author: c.Author, Body: c.Body, At: c.At})
		}
		return out, err
	}
}

// policyEditor is the policy view as the API serves it, documents as any (api.PolicyEditor).
type policyEditor struct{ *policyview.Service }

func (e policyEditor) Preview(ctx context.Context, repository, file string) (any, error) {
	return e.Service.Preview(ctx, repository, file)
}

func (e policyEditor) Timeline(ctx context.Context, repository string, pr int) (any, error) {
	return e.Service.Timeline(ctx, repository, pr)
}

// runCost hands the meter's cost report to the metric, key by key.
func runCost(m *fmetrics.Set) func(context.Context, meter.CostKey, float64, map[string]float64) {
	return func(ctx context.Context, k meter.CostKey, usd float64, by map[string]float64) {
		m.RunCost(ctx, k.Tier, k.Role, k.Cohort, k.Profile, usd, by)
	}
}
