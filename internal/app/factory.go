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
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
	"github.com/Smana/maku/internal/factory/applier"
	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/configsource"
	"github.com/Smana/maku/internal/factory/coordination"
	"github.com/Smana/maku/internal/factory/decision"
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
	"github.com/Smana/maku/internal/factory/minter"
	"github.com/Smana/maku/internal/factory/policyview"
	"github.com/Smana/maku/internal/factory/reconciler"
	"github.com/Smana/maku/internal/factory/rooms"
	"github.com/Smana/maku/internal/factory/runs"
	"github.com/Smana/maku/internal/factory/runtoken"
	"github.com/Smana/maku/internal/factory/statuswriter"
	"github.com/Smana/maku/internal/factory/taskid"
	"github.com/Smana/maku/internal/factory/tracing"
	"github.com/Smana/maku/internal/factory/triage"
	"github.com/Smana/maku/internal/hardening"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/metrics"
	"github.com/Smana/maku/internal/policy"
	"github.com/Smana/maku/internal/preflight"
	"github.com/Smana/maku/internal/redact"
	"github.com/Smana/maku/internal/runwatch"
	"github.com/Smana/maku/internal/version"
)

// The factory's outbound bounds and GitHub's API root.
const (
	githubAPIDefault = "https://api.github.com/"
	githubTimeout    = 15 * time.Second
	brokerTimeout    = 15 * time.Second
	vmTimeout        = 10 * time.Second
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

// githubAPIURL returns the in-cluster test root when set, and GitHub's public API otherwise.
func githubAPIURL() string {
	if root := os.Getenv("ROOMS_GITHUB_API_URL"); root != "" {
		return root
	}
	return githubAPIDefault
}

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

// holdoutGauge is agent_factory_class_without_holdout (fmetrics.Set).
type holdoutGauge interface {
	ClassWithoutHoldout(ctx context.Context, repository, class string)
}

// warnConfig logs each sentence the config loaded with (a later release refuses it) and sets the
// gauge of a live class that declares no holdoutChecks (TO5).
func warnConfig(log *slog.Logger, cfg *config.Config, m holdoutGauge) {
	for _, w := range cfg.Warnings {
		log.Warn("config warning", "warning", w)
	}
	if w := roleTableWarning(cfg, minter.Roles); w != "" {
		log.Warn("config warning", "warning", w)
	}
	if m == nil {
		return
	}
	for _, c := range withoutHoldout(cfg) {
		m.ClassWithoutHoldout(context.Background(), c.repository, c.class)
	}
}

// startupWire wires the config's repositories at start. A repository that cannot be wired is
// refused alone (#447): the factory serves the others and the retrier connects it later.
func startupWire(ctx context.Context, log *slog.Logger, conns *repoConns, cfg *config.Config) {
	if err := conns.sync(ctx, cfg); err != nil {
		log.Warn("a repository is not wired yet: the factory serves the others and retries every minute", "err", err)
	}
}

// repoClass is one class of one repository.
type repoClass struct{ repository, class string }

// withoutHoldout are the live classes that declare no holdoutChecks (TO5), in order.
func withoutHoldout(cfg *config.Config) []repoClass {
	var out []repoClass
	for _, r := range cfg.Repos() {
		for _, name := range slices.Sorted(maps.Keys(r.Classes)) {
			if cl := r.Classes[name]; cl.Live && len(cl.HoldoutChecks) == 0 {
				out = append(out, repoClass{r.Name, name})
			}
		}
	}
	return out
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
	return meter.NewVM(cfg.Meter.URL, cfg.Meter.Query, cfg.Runs.Kind, httpx.New(vmTimeout, nil))
}

// meteredBy is what the factory stamps on each run it creates (spec.meteredBy, review #518): gateway
// under runs.gatewayIdentity, so a run-controller in model.mode secret refuses the run rather than
// start one the meter would price at $0.
func meteredBy(cfg *config.Config) string {
	if cfg.Runs.GatewayIdentity {
		return runs.MeteredByGateway
	}
	return ""
}

// throttle maps gateway 429s to runs: PromQL on meter.url (meter.throttle.promQuery, GW10), else
// VictoriaLogs (meter.logsURL); nil with neither.
func throttle(cfg *config.Config) (meter.Throttled, error) {
	switch {
	case cfg.Meter.Throttle.PromQuery != "":
		return meter.NewVM(cfg.Meter.URL, cfg.Meter.Throttle.PromQuery, cfg.Runs.Kind, httpx.New(vmTimeout, nil))
	case cfg.Meter.LogsURL != "":
		return meter.VL{Kind: cfg.Runs.Kind, URL: cfg.Meter.LogsURL, Query: cfg.Meter.ThrottleQuery, HC: httpx.New(vmTimeout, nil)}, nil
	}
	return nil, nil
}

// humanVerifier fetches the humans' JWKS. In demo mode there is none: nil, and the Authenticator
// refuses every human token with demo_no_human_tokens.
func humanVerifier(ctx context.Context, cfg *config.Config) (*authn.Verifier, error) {
	if cfg.Demo {
		return nil, nil
	}
	if cfg.API.HumanJWKS == "" {
		// The issuer's discovery document names jwks_uri. Fetched now, as a JWKS URL would be: the
		// factory starts only once its humans' keys are in hand.
		v, err := authn.NewDiscoveringVerifier(cfg.API.HumanIssuer)
		if err != nil {
			return nil, err
		}
		if err := v.Refresh(ctx); err != nil {
			return nil, fmt.Errorf("authn: initial discovery and JWKS fetch for %s: %w", cfg.API.HumanIssuer, err)
		}
		return v, nil
	}
	return authn.NewVerifier(ctx, cfg.API.HumanIssuer, cfg.API.HumanJWKS)
}

// fileReader reads a mounted id at use: an ExternalSecret may rotate it. Unset or unreadable is "".
func fileReader(path string) func() string {
	return func() string {
		if path == "" {
			return ""
		}
		b, _ := os.ReadFile(filepath.Clean(path))
		return strings.TrimSpace(string(b))
	}
}

// apiAudience is the aud value a generic sponsored or access token must hold, and the zitadel
// preset's project id; nil-safe readers both.
func apiAudience(a config.API) func() string {
	switch {
	case a.EffectivePreset() == config.PresetZITADEL:
		return fileReader(a.ProjectIDFile)
	case a.AudienceFile != "":
		return fileReader(a.AudienceFile)
	}
	aud := a.Audience
	return func() string { return aud }
}

// runTokens keeps the run-controller's two git Secrets filled with tokens from the App
// cfg.RunTokenApp names (the factory's own in the demo), on the served repositories: nil when the
// factory writes no run token. served is read at each mint: the set is live (factory resources D12).
// Interim: replaced by the per-run minter, v4 D10 (rollout step 3).
func runTokens(cfg *config.Config, served func() []string, c client.Client, log *slog.Logger) ([]*runtoken.Writer, error) {
	appIDFile, keyFile, ok := cfg.RunTokenApp()
	if !ok {
		return nil, nil
	}
	if _, _, err := oneOwner(served()); err != nil {
		return nil, err
	}
	hc := httpx.New(githubTimeout, nil)
	ws := runtoken.Writers(c, cfg.Runs.Namespace, func(ctx context.Context, perms map[string]string) (forge.Token, error) {
		owner, names, err := oneOwner(served())
		if err != nil {
			return forge.Token{}, err
		}
		return forge.Mint(ctx, hc, githubAPIURL(), forge.MintOptions{AppIDFile: appIDFile, KeyFile: keyFile,
			Owner: owner, Repositories: names, Permissions: perms})
	}, log)
	for _, w := range ws {
		w.Every = 30 * time.Minute
	}
	return ws, nil
}

// agentsMinter mints from the App cfg.RunTokenApp names, the agents App (the factory's own in the
// demo), with its own installation cache: nil when the config names none.
func agentsMinter(cfg *config.Config, met *fmetrics.Set, log *slog.Logger) *minter.Minter {
	appIDFile, keyFile, ok := cfg.RunTokenApp()
	if !ok {
		return nil
	}
	app := &forge.AppClient{API: githubAPIURL(), HTTP: httpx.New(githubTimeout, nil), IDFile: appIDFile, KeyFile: keyFile,
		Installations: &forge.Installations{}}
	mt := &minter.Minter{Client: app, Log: log}
	if met != nil {
		mt.Metrics = met
	}
	return mt
}

// perRunTokens is the per-run minter (v4 D10, runTokens.perRun): one token per run, for its
// repository and role, from the App cfg.RunTokenApp names, written as the run's own Secret. nil
// when runTokens.perRun is off: the shared writer, if any, stays (D25). Installations are looked up
// per repository, so the shared writer's one-owner limit does not apply.
func perRunTokens(cfg *config.Config, current func() *config.Config, c client.Client, runsCache, ledgers client.Reader, ns string, met *fmetrics.Set, log *slog.Logger) *minter.RunTokens {
	if !cfg.RunTokens.PerRun {
		return nil
	}
	mt := agentsMinter(cfg, met, log)
	if mt == nil {
		return nil
	}
	var rm minter.RefusalMetrics
	if met != nil {
		rm = met
	}
	return &minter.RunTokens{Minter: mt, Client: c, Runs: runsCache, Tasks: c, Kind: cfg.Runs.Kind, TaskNamespace: ns,
		Reserved: func(ctx context.Context, id string) (string, string, bool, error) {
			r, ok, err := ledger.Held(ctx, ledgers, ns, time.Now(), id)
			return r.Repository, r.Role, ok, err
		},
		Enforced: func(repo string) []hardening.OutputType { return current().Hardening.EnforcedFor(repo) },
		Metrics:  rm, Log: log, Now: time.Now}
}

// oneOwner splits the repositories into their one owner and their names: one App installation
// serves one account.
func oneOwner(repos []string) (owner string, names []string, err error) {
	for _, r := range repos {
		o, n, _ := strings.Cut(r, "/")
		if owner != "" && !strings.EqualFold(o, owner) {
			return "", nil, fmt.Errorf("run tokens: repositories %q and %q have different owners: one App installation serves one account", owner+"/"+names[0], r)
		}
		owner = o
		names = append(names, n)
	}
	return owner, names, nil
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

func runFactory(ctx context.Context, log *slog.Logger, getenv func(string) string) (err error) {
	cfgPath, ns, err := factoryEnv(getenv)
	if err != nil {
		return err
	}
	exp, err := metrics.NewExporter(metrics.FactoryBuildInfo, version.Version)
	if err != nil {
		return err
	}
	defer shutdownMetrics(ctx, log, exp)
	// :9090 answers before the first Sync, so a factory waiting for its resources is alive and
	// started, and not ready until the manager's caches synced. It stops last, after the manager.
	gate := &startupGate{}
	down := newDrainFlag(ctx)
	// GET /preflight (v4 D24) answers from the start, undecided until the checks are built below.
	var pfSource atomic.Pointer[preflight.Source]
	pfOn := preflightOn(getenv)
	ops, err := startOps(ctx, log, withPreflight(gate.handler(factoryOps(gate.Synced, down.Draining, exp.Handler())), pfOn, func() http.Handler {
		return preflight.Handler("factory", version.Version, func(ctx context.Context) []preflight.Result {
			if src := pfSource.Load(); src != nil {
				return (*src)(ctx)
			}
			return []preflight.Result{{Check: "apps", Outcome: preflight.Fail, Code: preflight.Undecided, Message: "the factory is still starting"}}
		}, nil)
	}))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, ops.stop()) }()
	// D5, D14: the config is the file and the factory's resources, built before anything reads
	// it. The first Sync reads through a plain client, as the manager does not exist yet. While
	// nothing can be served the start waits for the resources; any other error fails it.
	src, kinds, err := firstSync(ops.ctx, log, cfgPath, ns, gate)
	if errors.Is(err, errStoppedWaiting) {
		log.Info("agent-factory stopped while waiting for a servable config")
		return nil
	}
	if err != nil {
		return err
	}
	live := src.Live
	cfg := live.Current() // the startup fields (D12): read once, never re-derived
	if err := checkFactorySecrets(cfg, readMounted); err != nil {
		return err
	}
	warnIgnored(log, cfg)
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
	throttled, err := throttle(cfg)
	if err != nil {
		return err
	}
	// A repository whose installation is missing is left out, never silently: it is logged, its
	// Connected condition says why, and the retry wires it once the App is installed (#447).
	conns := newRepoConns(log)
	startupWire(ctx, log, conns, cfg)
	mgr, err := newFactoryManager(log, ns, cfg.Runs.Kind, kinds)
	if err != nil {
		return err
	}
	// From here the Source reads through the manager's uncached reader. Nothing else syncs before
	// the manager starts it, so swapping the reader after the first Sync races with nothing.
	src.Reader = mgr.GetAPIReader()
	// The leader publishes the resources' status, on its own ticker and after each Sync. The hook
	// runs with no lock held (a live.Subscribe subscriber would run under the Source's lock, and
	// Outcomes would deadlock).
	var policyWatch *mergepolicy.Watch // nil while policyManagement.read is off
	recorder := &decision.Recorder{}
	// v4 D3 (step 7 R1): each fence pass reads every served repository's visibility; private code
	// without spec.privateCode.acknowledged is warned (the condition, the gauge, the banner).
	privateCode := &privateCodeTracker{Live: live.Current, Log: log, Readers: func(repo string) repoInfoReader {
		if c, ok := conns.get(repo); ok && c.gh != nil {
			return c.gh
		}
		return nil // a nil interface, never a typed-nil *forge.GitHub
	}}
	statusWriter := &statuswriter.Writer{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Namespace: ns, Log: log,
		Inputs: factoryStatusInputs(ns, src, mgr.GetClient(), conns, func() *mergepolicy.Watch { return policyWatch }, recorder, privateCode.Visibility, log)}
	src.OnSynced = statusWriter.Kick
	if err := src.Watch(ctx, mgr.GetCache(), kinds); err != nil {
		return err
	}
	if err := mgr.Add(src); err != nil {
		return fmt.Errorf("manager: %w", err)
	}
	if err := mgr.Add(statusWriter); err != nil {
		return fmt.Errorf("manager: %w", err)
	}
	m, err := fmetrics.New(exp.Meter(), mgr.GetClient(), ns, elected(mgr))
	if err != nil {
		return err
	}
	conns.failed = m.ConfigWireError
	m.ListRepositories(conns.names()...)
	warnConfig(log, cfg, m)
	// D12 on App ids: the agents App is never the factory's or the merger's, and a merger that is the
	// factory App merges as factoryLogin (review of #510).
	mergerFolded, err := appIdentities(cfg, readAppID)
	if err != nil {
		return err
	}
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
	fenced := &fence.Fence{Name: cfg.Factory, Repositories: conns.names, Read: conns.fenceFile,
		Every: fenceEvery, Stale: fenceStale, Now: time.Now, Log: log}
	privateCode.Record = m.PrivateCodeUnacknowledged
	// The doors that start work without a label (the schedules, adoption) start nothing on private
	// code without its acknowledgement (v4 D3); a task already started finishes, so the reconciler,
	// the merge and the reverts keep fenced.Owned.
	startable := func(repository string) bool { return fenced.Owned(repository) && !privateCode.Refused(repository) }
	fenced.OnState = func(ctx context.Context, repo string, owned bool) {
		m.RepositoryOwned(ctx, repo, owned)
		privateCode.Observe(ctx, repo, owned)
	}
	fenced.OnForget = func(_ context.Context, repo string) {
		m.ForgetRepository(repo)
		privateCode.Forget(repo)
	}
	// Stages 1, 3 and 5 (merge policy): observed and compared, each merge decision decided again
	// under it, and its approval subset's grants consulted there. Stage 4: a cut-over repository
	// is decided on it, and every reader reads its classes from it, aged out at the fence's window.
	var snapshots func(string) (mergepolicy.Snapshot, bool)
	var authority func(string) (mergepolicy.Snapshot, error)
	if cfg.PolicyManagement.Read {
		w := &mergepolicy.Watch{Cfg: live, Metrics: m, Log: log, Now: time.Now, Stale: fenceStale}
		policyWatch = w
		fenced.OnFile = w.Observe
		if cfg.PolicyManagement.DualEvaluation || cfg.PolicyManagement.ApprovalMemory {
			snapshots = w.Snapshot
		}
		if cfg.PolicyManagement.RepositoryAuthority {
			live.Prepare(func(c *config.Config) { c.SetClassSource(w.Classes) }) // every published config
			authority = w.Authority
		}
	}
	// The api intake and factory/publish scan what they publish, and both fail closed without it;
	// the hardening shadow pass scans with it too (T3).
	redactor, err := redact.New()
	if err != nil {
		return err
	}
	// Hardening H1: under audit, the shadow pass's checker, and each repository's fence-file
	// hardening block, fed beside the merge policy's watch. nil under off.
	hardened := hardeningPorts(cfg, redactor)
	if hardened != nil {
		fenced.OnFile = chainOnFile(fenced.OnFile, hardened.Policies.Observe)
	}
	// D11: on each fence pass over a repository with merge.gateStatus (read live, so a publish that
	// turns it on is heard), whether its default branch requires maku/merge-gate from the App that
	// posts it. Read with the factory App's token; it warns and never refuses.
	gateRead := &gateState{} // the preflight's merge-gate check serves the observer's last read
	fenced.OnFile = chainOnFile(fenced.OnFile, gateGuardObserver(live.Current, func(repo string) rulesReader {
		if c, ok := conns.get(repo); ok && c.gh != nil {
			return c.gh
		}
		return nil // a nil interface, never a typed-nil *forge.GitHub
	}, gateRead.record(m.MergeGateRequired), gateRead.unread(m.MergeGateRulesUnread), log))
	// Hardening H2: on each fence pass over an enforced repository, the sandbox roles' and the
	// publisher's GitHub permissions, read from the token source they get: the octo-sts policies, or
	// the minter's role table (R2 as evidence, plan PR8).
	if cfg.Hardening.Enforcing() {
		fenced.OnFile = chainOnFile(fenced.OnFile, sandboxPermissionObserver(live.Current, func(repo string) policyReader {
			if c, ok := conns.get(repo); ok && c.gh != nil {
				return c.gh
			}
			return nil // a nil interface, never a typed-nil *forge.GitHub
		}, tableGrants(live.Current, minter.Roles), m.SandboxPermissions, log))
	}
	costs := &meter.Costs{} // the meter writes each run's cost, the reconciler sums a task's
	for name, usd := range meter.RunUSD(cfg) {
		m.RunBudgetUSD(ctx, name, usd)
	}
	// The day ledgers are invisible to the manager's cache: its ConfigMap informer carries a
	// metadata.name selector for the stop object (factoryManagerOptions). R50's reservation, the
	// meter's settle and Queued's daily cap (which reserves its runs there, #78) read and write
	// date-named ledgers, so through a live client.
	ledgers, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme()})
	if err != nil {
		return err
	}
	rc := runs.Client{C: mgr.GetClient(), Kind: cfg.Runs.Kind, MeteredBy: meteredBy(cfg)}
	perRun := perRunTokens(cfg, live.Current, mgr.GetClient(), mgr.GetCache(), ledgers, ns, m, log)
	if perRun != nil {
		rc.Created = perRun.Ensure // the first token, right after the claim exists (plan PR5)
	}
	perms := conns.permissions(cfg.API.Access)
	var c7 triage.Classifier // nil while triage.classifierURL is unset: no HTTP client, no call
	if cfg.Triage.ClassifierURL != "" {
		c7 = triage.HTTPClassifier{URL: cfg.Triage.ClassifierURL, HC: httpx.New(classifierTimeout, nil)}
	}
	rec := &reconciler.Reconciler{Kind: cfg.Runs.Kind, Client: mgr.GetClient(), Ledger: ledgers, Namespace: ns, Cfg: live, Clients: conns.task, Owned: fenced.Owned,
		Policy: snapshots, Authority: authority, CanWrite: perms.CanWrite, Runs: rc, Rooms: broker,
		Triage: triage.Classify{C: c7,
			Clients: conns.class},
		Metrics: m, Now: time.Now, Nonce: taskid.Random, Log: log,
		Trace: sink, Costs: costs, Decisions: recorder, Hardening: hardened}
	if cfg.PolicyManagement.ApprovalMemory { // stage 5: grants read from the broker, never cached
		rec.Grants = broker
	}
	// Uncached: the reader serves a few reads per monitor pass, and a cache would start an
	// informer per Flux kind (and need watch, which the ClusterRole does not grant). Always set: a
	// publish may add a class that declares monitor.flux, and a pass with no Flux class reads nothing.
	rec.Flux = fluxwatch.Reader{C: mgr.GetAPIReader()}
	if err := rec.SetupWithManager(mgr); err != nil {
		return err
	}
	// Hardening H2: the safe-output applier, only while a repository has an enforceOutputs list.
	a, err := safeOutputApplier(cfg, applierDeps{Reader: mgr.GetAPIReader(), Namespace: ns, Rooms: broker, Runs: rc,
		Factory: func(repo string) applier.Filer {
			if c, ok := conns.get(repo); ok && c.gh != nil {
				return c.gh
			}
			return nil // a nil interface, never a typed-nil *forge.GitHub
		}, Hardened: hardened, Live: live.Current, Metrics: m, Log: log, Minter: agentsMinter(cfg, m, log),
		PushRooms: broker, Tasks: mgr.GetClient(), Getenv: os.Getenv})
	if err != nil {
		return err
	}
	if a != nil {
		if err := mgr.Add(a); err != nil {
			return fmt.Errorf("manager: %w", err)
		}
		log.Info("hardening: the safe-output applier is on", "interval", a.Interval, "answerWait", a.AnswerWait)
	}
	// Hardening H4 (HR38): the drill runner scores the drills planted against this factory, on the
	// leader, while hardening.drills.gatewayLog is set; it reads applied pushes through the
	// applier's mirror. Its reports go to the broker, which readiness's R6 reads (rooms.Client is
	// hardening.Drills).
	dd := drillDeps{Client: ledgers, Namespace: ns, Rooms: broker, Claims: rc, Live: live.Current, Metrics: m, Log: log}
	if a != nil {
		dd.Pushers = a.Pushers
	}
	dr, err := drillRunner(cfg, dd)
	if err != nil {
		return err
	}
	if dr != nil {
		if err := mgr.Add(dr); err != nil {
			return fmt.Errorf("manager: %w", err)
		}
		log.Info("hardening: the drill runner is on", "timeout", cfg.Hardening.Drills.Timeout)
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
	access := conns.access(log, cfg, perms, broker)
	people, _ := access.(api.PersonResolver) // D1: the login the write check reads is the person charged
	auth := &api.Authenticator{Humans: humansV, Groups: groups, ClientIDs: clientIDs,
		Systems: systemsV, SystemAllow: cfg.API.SystemPrincipals,
		TokenUse: cfg.API.EffectiveTokenUse(), RolesClaim: cfg.API.RolesClaim, RolesRefused: m.HumanRolesClaimRefused}
	if cfg.API.EffectivePreset() == config.PresetGeneric {
		auth.Generic = true
		auth.Audience = apiAudience(cfg.API) // the zitadel preset's human path pins the client alone, as before
	}
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
			Audience: apiAudience(cfg.API), ClientClaim: cfg.API.SponsoredClientClaim, Generic: auth.Generic}
	}
	// The self-serve link: the broker's records, and the same ZITADEL resolver api.access reads.
	var logins api.LoginResolver
	if ga, ok := access.(api.GitHubAccess); ok {
		logins = ga.Identity
	}
	apiSrv := &api.Server{Auth: auth, Log: log,
		Cfg: live, Groups: groups, Runs: rc, Rooms: ledgers, Namespace: ns, Stopped: stopped, SlackLinks: broker, Logins: logins,
		NewRunID: taskid.Random, Now: time.Now, Metrics: m, Access: access, People: people, Owned: fenced.Owned, PrivateRefused: privateCode.Refused,
		InstallUnsafe: privateCode.InstallUnsafe,
		Forges: func(repo string) api.IssueFiler {
			c, ok := conns.get(repo)
			if !ok || c.gh == nil {
				return nil // a nil interface, never a typed-nil *forge.GitHub (C24)
			}
			return c.gh
		},
		// Hardening H4 (HR35): the broker's forge_read reads through the factory App, filtered by
		// the repository's trust floor; the route serves only repositories in mode enforce.
		Readers: func(repo string) api.ForgeThreads {
			c, ok := conns.get(repo)
			if !ok || c.gh == nil {
				return nil // a nil interface, never a typed-nil *forge.GitHub (C24)
			}
			return c.gh
		}}
	if hardened != nil {
		apiSrv.Trust = hardened.Policies
	}
	apiSrv.Writers = perms.CanWrite // a factory/vouch from a writer who is not a maintainer (v2 D5)
	// Hardening H5 (HR41): readiness, computed afresh on every read, from the live settings, each
	// repository's policy files through its own connection, the minter's role table and the broker's
	// drills. The API and the Evidence page show it, the leader reports it every five minutes, and
	// coordination's execute admission asks it with class authority (v4 MR16).
	readiness := hardeningEvaluator(live.Current, func(repo string) policyReader {
		if c, ok := conns.get(repo); ok && c.gh != nil {
			return c.gh
		}
		return nil // a nil interface, never a typed-nil *forge.GitHub
	}, tableGrants(live.Current, minter.Roles), broker, time.Now)
	apiSrv.Readiness = readiness
	if err := mgr.Add(readinessReporter(live.Current, readiness, m, log)); err != nil {
		return fmt.Errorf("manager: %w", err)
	}
	// Each repository's checks and filing go through that repository's own App connection.
	coord := &coordination.Service{Client: ledgers, Namespace: ns, Cfg: live, Prerequisites: coordinationPrerequisites(live, readiness),
		Now: time.Now, Metrics: m,
		Checks: coordination.GitHubChecks{Forges: func(repo string) coordination.CheckForge {
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
		ev := &evidence.Service{Cfg: live, Namespace: ns, Now: time.Now, Log: log,
			// The cached client, on every replica: a miss is no status, never an error page.
			PolicyStatus: func(ctx context.Context, name string) (evidence.PolicyStatus, bool) {
				var mp factoryv1.MergePolicy
				if err := mgr.GetClient().Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &mp); err != nil {
					return evidence.PolicyStatus{}, false
				}
				return evidence.PolicyStatus{Namespace: ns, Generation: mp.Generation, Status: mp.Status}, true
			},
			RepositoryStatus: func(ctx context.Context, name string) (evidence.RepositoryStatusView, bool) {
				var r factoryv1.Repository
				if err := mgr.GetClient().Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, &r); err != nil {
					return evidence.RepositoryStatusView{}, false
				}
				return evidence.RepositoryStatusView{Namespace: ns, Generation: r.Generation, Status: r.Status}, true
			}, Sources: func(repo string) (evidence.Source, bool) {
				c, ok := conns.get(repo)
				if !ok || c.gh == nil {
					return nil, false
				}
				return evidenceSource{c.gh}, true
			}}
		apiSrv.Evidence, apiSrv.Policies = ev, ev
		apiSrv.PolicySnapshot = func(repo string) (mergepolicy.Snapshot, bool) {
			if policyWatch == nil { // policyManagement.read off: no file is read
				return mergepolicy.Snapshot{}, false
			}
			return policyWatch.Snapshot(repo)
		}
		evidenceRefresh = &evidence.Refresher{Service: ev, Owned: fenced.Owned, OnAge: m.PolicyEvidenceAge}
		apiSrv.Editor = policyEditor{&policyview.Service{Cfg: live, Reports: ev, Namespace: ns, Comments: func(repo string) (policyview.CommentReader, bool) {
			c, ok := conns.get(repo)
			if !ok || c.gh == nil {
				return nil, false
			}
			return commentReader(c.gh), true
		}}}
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
	if perRun != nil {
		runnables = append(runnables, perRun)
	} else {
		tokens, err := runTokens(cfg, conns.names, ledgers, log)
		if err != nil {
			return err
		}
		for _, w := range tokens {
			runnables = append(runnables, w)
		}
	}
	// One issue poller per served repository, following the set as publishes change it (D12).
	pollers := &intake.Pollers{Log: log, New: func(name string) (*intake.IssuePoller, bool) {
		c, ok := conns.get(name)
		if !ok {
			return nil, false
		}
		return &intake.IssuePoller{Forge: c.gh, Client: mgr.GetClient(), Namespace: ns, Cfg: live,
			Repository: name, Demo: cfg.Demo, Errors: m, Log: log.With("repository", name), Stopped: stopped, Owned: fenced.Owned, Ledger: ledgers,
			CanWrite: func(ctx context.Context, login string) (bool, error) { return perms.CanWrite(ctx, name, login) },
			Proposal: proposal, Redact: redactor}, true
	}}
	pollers.Sync(conns.names())
	runnables = append(runnables, pollers)
	if perRun != nil { // D12: both Apps' installations against their required sets, once
		runnables = append(runnables, appGrantChecks(cfg, mergerFolded, agentsMinter(cfg, m, log), conns.names, m.GitHubAppPermissionMissing, log))
	}
	// v4 D24: the factory's own checks, now that every part they read exists.
	if pfOn {
		apps := []appGrants{{Name: "factory", Checker: factoryMinter(cfg, preflightAppNeed(cfg, mergerFolded), log)}}
		if perRun != nil && !cfg.Demo {
			if am := agentsMinter(cfg, m, log); am != nil {
				apps = append(apps, appGrants{Name: "agents", Checker: am})
			}
		}
		pfChecks := factoryChecks{Live: live.Current, Repos: conns.names, Apps: apps, Gate: gateRead, Private: privateCode,
			Kube: mgr.GetAPIReader(), Namespace: ns, HC: httpx.New(preflight.CheckTimeout, nil)}.source()
		pfSource.Store(&pfChecks)
	}
	if cfg.RunLore.Listen != "" { // R14: no runlore block, no intake; its port serves nothing
		if _, ok := conns.get(cfg.RunLoreRepository()); !ok {
			return fmt.Errorf("runlore.repository %s has no GitHub connection", cfg.RunLoreRepository())
		}
		// FA-8: RunLore pushes findings over the tailnet; the token file is re-read per request,
		// so the ExternalSecret's rotations need no restart (AGENTS.md). Its connection is looked
		// up per finding: the repository set is live (D12).
		runnables = append(runnables, &intake.RunLore{Forges: func(repo string) (intake.RunLoreForge, bool) {
			c, ok := conns.get(repo)
			if !ok || c.gh == nil {
				return nil, false // a nil interface, never a typed-nil *forge.GitHub
			}
			return c.gh, true
		}, Owned: fenced.Owned, Client: mgr.GetClient(), Namespace: ns, Cfg: live,
			Token: func() string {
				b, _ := os.ReadFile(filepath.Clean(cfg.RunLore.TokenFile))
				return strings.TrimSpace(string(b))
			},
			Stopped: stopped, Now: time.Now, Errors: m, Log: log})
	}
	wired := &wiring{log: log, conns: conns, pollers: pollers, m: m, kick: fenced.Kick}
	for _, r := range append(runnables,
		&intake.Scheduler{Clients: conns.schedule, Owned: startable, Client: mgr.GetClient(), Namespace: ns, Cfg: live,
			Stopped: stopped, Now: time.Now, Errors: m, Log: log},
		// MC10: off unless a class adopts; leader-only, through the Scheduler's connections.
		&intake.Adopter{Clients: conns.schedule, Owned: startable, Client: mgr.GetClient(), Namespace: ns, Cfg: live,
			Stopped: stopped, Now: time.Now, Errors: m, Log: log},
		&meter.Meter{Runs: rc, Source: vm, PerReplica: cfg.Meter.ByReplica(), Every: cfg.Poll.Meter.Duration, OnRevoke: m.RunRevoked, Log: log,
			OnCharge: m.Charged, OnCharges: m.RunCharges,
			Cfg: live, OnCost: runCost(m), OnLateCost: m.LateCost, OnSpend: m.RunSpend, OnSettled: m.RunSettled, Costs: costs,
			OnFallback: func(ctx context.Context, why string) { m.RunBudgetFallback(ctx, "run", why) },
			OnUnpriced: m.ModelUnpriced, OnInputExcludesCache: m.InputExcludesCache,
			Throttle: throttled, GatewayIdentity: cfg.Runs.GatewayIdentity,
			B1Ceiling: config.RunTokenCeiling, Now: time.Now,
			Day:          func(p string, spent, c int64) { m.BudgetDay(ctx, p, spent, c) },
			OnUsageError: m.MeterUsageReadError, OnSucceeded: m.MeterSucceeded, OnOvershoot: m.RunOvershoot,
			Ledger: ledgers, Tasks: mgr.GetClient(), Namespace: ns},
		&killswitch.Sweeper{Reader: mgr.GetClient(), Namespace: ns, Runs: rc, Every: 15 * time.Second,
			Release: func(ctx context.Context, r runs.Run) error {
				return ledger.Release(ctx, ledgers, ns, time.Now(), r.ID) // admission reads today's ledger only
			},
			OnSweep: func(revoked []runs.Run) {
				for _, r := range revoked {
					m.Revoked(fmetrics.WithPrincipal(ctx, r.Payer()), "manual")
				}
			}, Log: log},
		apiSrv, fenced, &retrier{every: pingEvery, retry: func(ctx context.Context) { wired.retry(ctx, live.Current) }},
		&pinger{ping: conns.ping, every: pingEvery, up: func() bool { return conns.healthy(time.Now()) }, record: m.GitHubUp},
	) {
		if err := mgr.Add(r); err != nil {
			return fmt.Errorf("manager: %w", err)
		}
	}
	// D12: each publish re-wires the repositories and re-derives what the startup derived. It is
	// registered once every runnable is built, and a failure in it is logged, never fatal.
	live.Subscribe(onPublish(ctx, wired))
	gate.serve(mgr.GetCache().WaitForCacheSync)
	return serveFactory(ops.ctx, log, mgr, down, ns)
}

// opsRun is :9090 serving from the start of runFactory. ctx is the run's: :9090 failing cancels
// it, as the pod is then unprobeable.
type opsRun struct {
	ctx       context.Context
	cancelRun context.CancelFunc
	stopOps   context.CancelFunc
	done      chan error
}

// startOps serves h on opsAddr until stop.
func startOps(ctx context.Context, log *slog.Logger, h http.Handler) (*opsRun, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", opsAddr)
	if err != nil {
		return nil, fmt.Errorf("ops listener: %w", err)
	}
	o := &opsRun{done: make(chan error, 1)}
	o.ctx, o.cancelRun = context.WithCancel(ctx)
	opsCtx, stopOps := context.WithCancel(context.WithoutCancel(ctx))
	o.stopOps = stopOps
	go func() {
		err := serveHTTP(opsCtx, opsServer(h, log), ln, opsDrain)
		if err != nil {
			o.cancelRun()
		}
		o.done <- err
	}()
	return o, nil
}

// stop drains :9090 and returns its error; call it once, after everything else stopped.
func (o *opsRun) stop() error {
	o.stopOps()
	o.cancelRun()
	return <-o.done
}

// serveFactory runs the manager until ctx ends or it fails; :9090 (opsRun) stops after it, so the
// drain's final counts are scraped and /readyz answers 503 meanwhile, as the broker's does.
func serveFactory(ctx context.Context, log *slog.Logger, mgr manager.Manager, down *drainFlag, ns string) error {
	g, gctx := errgroup.WithContext(ctx)
	stopWatch := context.AfterFunc(gctx, down.Begin) // the manager dying is a shutdown too
	defer stopWatch()
	g.Go(func() error {
		if err := mgr.Start(gctx); err != nil {
			return fmt.Errorf("manager: %w", err)
		}
		return nil
	})
	log.Info("agent-factory starting", "version", version.Version, "namespace", ns)
	return g.Wait()
}

// firstSync builds the config from the file at cfgPath and the factory's resources in ns, through
// a plain client: the manager, whose reader the Source moves to next, needs the config to be
// built. The file is read and its shape checked first (config.CheckShape), so a missing, empty or
// malformed one fails before any cluster call. While nothing can
// be served it waits for the resources (waitServable); any other error, including an unreachable
// API server, fails the start (D14). kinds are the resource kinds the API server serves
// (servedKinds), read once the config serves.
func firstSync(ctx context.Context, log *slog.Logger, cfgPath, ns string, gate *startupGate) (src *configsource.Source, kinds []client.Object, err error) {
	raw, err := os.ReadFile(filepath.Clean(cfgPath))
	if err != nil {
		return nil, nil, fmt.Errorf("config: %w", err)
	}
	// A file no resource can fix fails at once; only a file waiting for its resources waits.
	if err := config.CheckShape(raw); err != nil {
		return nil, nil, err
	}
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("kubeconfig: %w", err)
	}
	// The plain client reads the factory's own kinds only: the runs kind, which factoryScheme
	// needs, is a startup field of the config this builds.
	scheme := runtime.NewScheme()
	if err := factoryv1.AddToScheme(scheme); err != nil {
		return nil, nil, err
	}
	plain, err := client.New(restCfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, nil, fmt.Errorf("config client: %w", err)
	}
	src = &configsource.Source{Reader: plain, Namespace: ns, File: raw, Log: log}
	if err := waitServable(ctx, log, gate, src.Sync, waitStateOf(src), servableFirst, servableMax, servableLogEvery); err != nil {
		return nil, nil, forbiddenHint(err, ns)
	}
	if kinds, err = servedKinds(plain); err != nil {
		return nil, nil, err
	}
	return src, kinds, nil
}

// forbiddenHint names the Role rule a refused list needs; the start still fails.
func forbiddenHint(err error, ns string) error {
	if !apierrors.IsForbidden(err) {
		return err
	}
	return fmt.Errorf("%w: the factory's Role in %s must grant get, list and watch on factories, repositories and mergepolicies (maku.ogenki.io)", err, ns)
}

// servedKinds are the factory's resource kinds the API server serves: the manager caches only
// those, as a ByObject entry for a kind with no CRD fails the manager's construction. A CRD
// installed later is read by the Source's periodic resync, and watched from the next start.
func servedKinds(c client.Client) ([]client.Object, error) {
	var out []client.Object
	for _, obj := range configsource.Kinds() {
		if _, err := c.IsObjectNamespaced(obj); meta.IsNoMatchError(err) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("factory resources: %w", err)
		}
		out = append(out, obj)
	}
	return out, nil
}

// publishMetrics are the series a publish re-derives (fmetrics.Set).
type publishMetrics interface {
	holdoutGauge
	ClassWithHoldout(ctx context.Context, repository, class string)
	ConfigWireError(ctx context.Context, repository string)
	ListRepositories(names ...string)
	ChargePeople(on bool)
	RunBudgetUSD(ctx context.Context, tier string, usd float64)
}

// wiring is what follows the served repository set (D12): the connections, the issue pollers,
// the repository list of the metrics and the fence.
type wiring struct {
	log     *slog.Logger
	conns   *repoConns
	pollers interface{ Sync(repos []string) }
	m       publishMetrics
	kick    func() // a fence refresh, not waiting for it; nil kicks nothing

	// servedMu holds a served() from reading the set to applying it, so a retry that read the set
	// before a publish changed it cannot apply it after the publish's.
	servedMu sync.Mutex
}

// served moves the pollers, the metrics' repository list and the fence onto the served set once
// a sync changed it. A dropped repository's repository_owned keeps its last value: a 0 would
// fire AgentFactoryRepositoryNotOwned for a repository nobody serves.
func (w *wiring) served() []string {
	w.servedMu.Lock()
	defer w.servedMu.Unlock()
	names := w.conns.names()
	w.m.ListRepositories(names...)
	w.pollers.Sync(names)
	if w.kick != nil {
		w.kick()
	}
	return names
}

// guard keeps a panic in the wiring from crashing the process: logged and counted under "*".
func (w *wiring) guard(ctx context.Context) {
	if r := recover(); r != nil {
		w.log.Error("config wiring panicked: the factory keeps serving what it had", "panic", r, "stack", string(debug.Stack()))
		w.m.ConfigWireError(ctx, "*")
	}
}

// onPublish is the config's subscriber (D12): it wires the repositories next lists (a failure is
// logged, counted and retried, never fatal), moves what follows the served set onto it, and
// re-derives the startup's gauges: per-person charging, each tier's run budget (0 for a tier no
// longer capped) and the live classes without holdoutChecks (0 for one no longer so).
func onPublish(ctx context.Context, w *wiring) func(prev, next *config.Config) {
	return func(prev, next *config.Config) {
		defer w.guard(ctx)
		if err := w.conns.sync(ctx, next); err != nil {
			w.log.Error("config published: a repository is not wired yet; retried every minute", "err", err)
		}
		gaps := withoutHoldout(next)
		for _, g := range withoutHoldout(prev) {
			if !slices.Contains(gaps, g) {
				w.m.ClassWithHoldout(ctx, g.repository, g.class) // while the list still names it
			}
		}
		names := w.served()
		w.m.ChargePeople(next.Budgets.PerPerson.Enabled)
		budgets := meter.RunUSD(next)
		for tier := range meter.RunUSD(prev) {
			if _, ok := budgets[tier]; !ok {
				w.m.RunBudgetUSD(ctx, tier, 0)
			}
		}
		for tier, usd := range budgets {
			w.m.RunBudgetUSD(ctx, tier, usd)
		}
		warnConfig(w.log, next, w.m)
		w.log.Info("config published", "hash", next.Hash, "repositories", names)
	}
}

// retry wires cur() again while a repository failed to wire (I1), then follows the served set.
func (w *wiring) retry(ctx context.Context, cur func() *config.Config) {
	defer w.guard(ctx)
	ran, err := w.conns.retry(ctx, cur)
	if !ran {
		return
	}
	if err != nil {
		w.log.Warn("config wiring retried: a repository is still not wired", "err", err)
	} else {
		w.log.Info("config wiring retried: every repository is wired")
	}
	w.served()
}

// retrier retries a failed wiring every period on every replica: each wires its own connections.
type retrier struct {
	every time.Duration
	retry func(context.Context)
}

// NeedLeaderElection is false: the API and the fence serve from every replica's connections.
func (*retrier) NeedLeaderElection() bool { return false }

// Start retries at each period until ctx ends.
func (r *retrier) Start(ctx context.Context) error {
	t := time.NewTicker(r.every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			r.retry(ctx)
		}
	}
}

// errMergerKeyMissing is a merger key a class needs and the pod does not mount: the repository is
// left out, whether it was served or not.
var errMergerKeyMissing = errors.New("merger App key missing")

// connectMerger connects the merger App, or answers the fail-closed forge.Absent when its key
// is not mounted and no class of the repository needs it (#57: the chart mounts the Secret
// optional). A class live or in shadow reads checks through the merger, so for it a missing key
// fails the repository: the start, or the publish that made the class need it.
func connectMerger(ctx context.Context, log *slog.Logger, cfg *config.Config, repository string) (forge.Merger, error) {
	idFile := cfg.GitHub.MergerAppIDFile
	if idFile != "" {
		_, err := os.Stat(filepath.Clean(idFile))
		switch {
		case err == nil:
		case errors.Is(err, os.ErrNotExist) && !cfg.RepositoryFor(repository).NeedsMerger():
			idFile = ""
		case errors.Is(err, os.ErrNotExist):
			return nil, fmt.Errorf("%w: %s is not mounted, and a live or shadow class needs it: create the agent-factory-merger Secret or turn the class off", errMergerKeyMissing, idFile)
		}
	}
	if idFile == "" {
		log.Warn("no merger App key: checks, merges and reverts fail closed until it is mounted and the pod restarts")
		return forge.Absent{}, nil
	}
	return forge.Connect(ctx, forge.Options{Repository: repository, AppIDFile: cfg.GitHub.MergerAppIDFile,
		PrivateKeyFile: cfg.GitHub.MergerKeyFile, APIURL: githubAPIURL(), UserAgent: "agent-factory-merger/" + version.Version,
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

// factoryOps is :9090 for the factory: /readyz once the caches synced; /startupz at once, as the
// server starts before the first Sync and a factory waiting for its resources (waitServable) must
// not be killed by the startup probe. Readiness does not look at GitHub (#58): an
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

// factoryManagerOptions caches Tasks, Rooms, the stop object and the served factory resources in
// the factory's namespace and kind's AgentRuns in theirs only: the chart's Roles grant exactly that. Unstructured reads stay
// uncached, so a stop's run list sees the API server (ruling SN), not the watch.
func factoryManagerOptions(ns string, kind runwatch.Kind, resources []client.Object) ctrl.Options {
	runObj := &unstructured.Unstructured{}
	runObj.SetGroupVersionKind(kind.GVK())
	drain := managerDrain
	here := map[string]cache.Config{ns: {}}
	byObject := map[client.Object]cache.ByObject{
		&factoryv1.Task{}: {Namespaces: here},
		&v1alpha1.Room{}:  {Namespaces: here},
		&corev1.ConfigMap{}: {Namespaces: here,
			Field: fields.OneTermEqualSelector("metadata.name", killswitch.ConfigMap)},
		runObj: {Namespaces: map[string]cache.Config{kind.Namespace: {}}},
	}
	for _, obj := range resources { // the config's own kinds (D5), watched in the factory's namespace
		byObject[obj] = cache.ByObject{Namespaces: here}
	}
	return ctrl.Options{
		LeaderElection: true, LeaderElectionID: "agent-factory.agents.ogenki.io", LeaderElectionNamespace: ns,
		LeaderElectionReleaseOnCancel: true, GracefulShutdownTimeout: &drain,
		// :9090 serves the agent_factory_* set and the probes itself.
		Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
		Cache:  cache.Options{ByObject: byObject},
		Client: client.Options{Cache: &client.CacheOptions{Unstructured: false}},
	}
}

// newFactoryManager builds the manager on factoryManagerOptions, logging through log.
func newFactoryManager(log *slog.Logger, ns string, kind runwatch.Kind, resources []client.Object) (ctrl.Manager, error) {
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
	opts := factoryManagerOptions(ns, kind, resources)
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

func (e policyEditor) PreviewBindings(ctx context.Context, repository string, bindings []config.Binding) (any, error) {
	return e.Service.PreviewBindings(ctx, repository, bindings)
}

func (e policyEditor) PreviewPolicy(ctx context.Context, name string, spec config.PolicySpec, readable func(string) bool) (any, error) {
	return e.Service.PreviewPolicy(ctx, name, spec, readable)
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
