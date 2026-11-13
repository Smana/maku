// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"golang.org/x/sync/errgroup"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	factoryv1alpha1 "github.com/Smana/maku/api/factory/v1alpha1"
	"github.com/Smana/maku/api/v1alpha1"
	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/coordinationview"
	fconfig "github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/humanapi"
	"github.com/Smana/maku/internal/humanapi/ui"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/intent"
	"github.com/Smana/maku/internal/metrics"
	"github.com/Smana/maku/internal/policy"
	"github.com/Smana/maku/internal/redact"
	"github.com/Smana/maku/internal/roomctrl"
	"github.com/Smana/maku/internal/runrequest"
	"github.com/Smana/maku/internal/runwatch"
	"github.com/Smana/maku/internal/store"
	"github.com/Smana/maku/internal/suspend"
	"github.com/Smana/maku/internal/tlsreload"
	"github.com/Smana/maku/internal/version"
	"github.com/Smana/maku/internal/webhook"
)

// The broker's listeners and bounds (§9 ports; GP-18 for :8443).
const (
	bridgeAddr = ":8443"
	humanAddr  = ":8080"
	opsAddr    = ":9090"
	// observeTimeout bounds one run's lifecycle append from the informer's
	// handler: a stalled database must not freeze the liveness mirror, which
	// every replica's bridge admission reads.
	observeTimeout = 10 * time.Second
	// sweepEvery and sweepTimeout pace the leader's "joined, never left" sweep.
	sweepEvery   = 5 * time.Minute
	sweepTimeout = time.Minute
	// The drains fit the pod's 30 s grace: the manager, :8443, :8080 and :8090 in
	// parallel, then :9090 and the metrics provider. :8080's covers a WebSocket
	// write blocked for WriteWait (10 s) plus the 5 s close handshake (review M11).
	bridgeDrain  = 20 * time.Second
	managerDrain = 20 * time.Second
	humanDrain   = 20 * time.Second
	opsDrain     = 2 * time.Second
	metricsDrain = time.Second
)

// RunBroker runs room-broker's subcommand args[0]: serve (the default),
// retention, migrate or migrate-group. getenv reads ROOMS_CONFIG, ROOMS_DATABASE_URL, POD_NAMESPACE,
// ROOMS_MCP_KEY, ROOMS_GITHUB_APP_DIR and, for migrate alone, ROOMS_MIGRATION_DATABASE_URL.
func RunBroker(ctx context.Context, log *slog.Logger, args []string, getenv func(string) string) error {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "serve":
		return serveBroker(ctx, log, getenv)
	case "retention":
		return runRetention(ctx, log, getenv)
	case "migrate":
		// The schema's owner, never the broker's role: every object the files create belongs to
		// rooms_owner, and ROOMS_DATABASE_URL carries the broker's own credential.
		dsn := getenv("ROOMS_MIGRATION_DATABASE_URL")
		if dsn == "" {
			return errors.New("room-broker migrate: ROOMS_MIGRATION_DATABASE_URL is not set")
		}
		n, err := store.Migrate(ctx, dsn, log)
		if err != nil {
			return fmt.Errorf("room-broker migrate: %w", err)
		}
		log.Info("schema up to date", "applied", n)
		return nil
	case "intent-samples":
		return runIntentSamples(ctx, os.Stdout, args[1:], getenv)
	case "migrate-group":
		return runMigrateGroup(ctx, log, args[1:], getenv, kubeClient(log))
	case "github-unlink":
		return runGitHubUnlink(ctx, log, args[1:], getenv)
	}
	return fmt.Errorf("room-broker: unknown subcommand %q (serve, retention, migrate, migrate-group, intent-samples, github-unlink)", cmd)
}

// runRetention is the daily DELETE-only job (§4, OD-17), connected as
// rooms_retention.
func runRetention(ctx context.Context, log *slog.Logger, getenv func(string) string) error {
	dsn := getenv("ROOMS_DATABASE_URL")
	if dsn == "" {
		return errors.New("room-broker retention: ROOMS_DATABASE_URL is not set")
	}
	st, err := openRetrying(ctx, log, dsn, store.RoleRetention, retentionConnectWait)
	if err != nil {
		return fmt.Errorf("room-broker retention: %w", err)
	}
	defer st.Close()
	warnPlainDB(log, st.Server, nil)
	rooms, events, err := st.PurgeExpired(ctx)
	log.Info("purged expired rooms", "rooms", rooms, "events", events)
	if err != nil {
		return fmt.Errorf("room-broker retention: %w", err)
	}
	// Opted-in intent samples carry their own expiry (SI19): deleted here, with no configuration.
	samples, err := st.PurgeIntentSamples(ctx)
	log.Info("purged expired intent samples", "samples", samples)
	if err != nil {
		return fmt.Errorf("room-broker retention: %w", err)
	}
	// Push bundles are kept 14 days, longer while a pending approval binds one (HR22).
	bundles, err := st.SweepBundles(ctx, time.Now())
	log.Info("swept old push bundles", "bundles", bundles)
	if err != nil {
		return fmt.Errorf("room-broker retention: %w", err)
	}
	return nil
}

// retentionConnectWait bounds how long the retention job retries a database it cannot reach. A
// CNI that enforces NetworkPolicy (kube-router) admits a new pod's address a few seconds after it
// starts, so a one-shot job's first connections can be refused.
var retentionConnectWait = 2 * time.Minute

// openRetrying is store.Open, retried every 5 s while the database is unavailable, up to wait. A
// refusal (the version, the role, row-level security) is returned at once.
func openRetrying(ctx context.Context, log *slog.Logger, dsn string, role store.Role, wait time.Duration) (*store.Store, error) {
	deadline := time.Now().Add(wait)
	for {
		st, err := store.Open(ctx, dsn, role)
		if err == nil || !errors.Is(err, store.ErrUnavailable) || time.Now().After(deadline) {
			return st, err
		}
		log.Info("database unavailable, retrying", "role", string(role))
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(5 * time.Second):
		}
	}
}

// runIntentSamples is `maku-broker intent-samples [--days N]`: the last N days (default 7) of an
// opted-in channel's replies and their labels, one JSON object per line on w, for the shadow stage's
// labelling. It logs nothing on success, so w holds the samples and nothing else; it connects as the
// broker's role, which can read them.
func runIntentSamples(ctx context.Context, w io.Writer, args []string, getenv func(string) string) error {
	fs := flag.NewFlagSet("intent-samples", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	days := fs.Int("days", 7, "how many days back to export")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("room-broker intent-samples: %w", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("room-broker intent-samples: unexpected argument %q", fs.Arg(0))
	}
	if *days < 1 {
		return errors.New("room-broker intent-samples: --days is at least 1")
	}
	dsn := getenv("ROOMS_DATABASE_URL")
	if dsn == "" {
		return errors.New("room-broker intent-samples: ROOMS_DATABASE_URL is not set")
	}
	st, err := store.Open(ctx, dsn, store.RoleBroker)
	if err != nil {
		return fmt.Errorf("room-broker intent-samples: %w", err)
	}
	defer st.Close()
	samples, err := st.IntentSamplesSince(ctx, *days)
	if err != nil {
		return fmt.Errorf("room-broker intent-samples: %w", err)
	}
	enc := json.NewEncoder(w)
	for _, x := range samples {
		if err := enc.Encode(x); err != nil {
			return fmt.Errorf("room-broker intent-samples: %w", err)
		}
	}
	return nil
}

// schemaCurrent refuses to serve a schema older or newer than this binary's. A ledger the broker's
// role cannot read is a bridge: served, with a warning and maku_rooms_schema_unverified at 1. It
// exists for one install, cnref's broker, whose schema the Atlas operator applies into a ledger only
// its owner reads, until ANR follow-up 4 gives the broker broker.database.ownerSecret: its first
// migrate then adopts that ledger into public.room_broker_revisions, which the broker reads.
// gauge reports which of the two the broker started with.
//
// TODO(v4 D9): once ANR sets ownerSecret, a minor release turns an unreadable ledger into a
// refusal, as docs/database.md announces. It is a release decision, never a date in the binary.
func schemaCurrent(ctx context.Context, st interface{ SchemaCurrent(context.Context) error }, log *slog.Logger,
	gauge func(unverified bool)) error {
	err := st.SchemaCurrent(ctx)
	switch {
	case errors.Is(err, store.ErrSchemaUnverified):
		gauge(true)
		log.Warn("schema version unverified: the broker's role cannot read the migration ledger; " +
			"set broker.database.ownerSecret, a future release refuses this (docs/database.md)")
		return nil
	case err != nil:
		return err
	}
	gauge(false)
	return nil
}

// warnPlainDB says once that the database connection is not encrypted, and sets rooms_db_tls for
// the role. It is not refused: the demo's in-cluster hop is plain (docs/database.md).
func warnPlainDB(log *slog.Logger, s store.Server, gauge func(role string, tls bool)) {
	if gauge != nil {
		gauge(s.User, s.TLS)
	}
	if !s.TLS {
		log.Warn("the database connection is not encrypted: set sslmode=verify-full and broker.database.caSecret (docs/database.md)", "role", s.User)
	}
}

func brokerEnv(getenv func(string) string) (cfgPath, dsn, ns string, err error) {
	var missing []error
	for _, kv := range []struct {
		name string
		dst  *string
	}{{"ROOMS_CONFIG", &cfgPath}, {"ROOMS_DATABASE_URL", &dsn}, {"POD_NAMESPACE", &ns}} {
		if *kv.dst = getenv(kv.name); *kv.dst == "" {
			missing = append(missing, fmt.Errorf("%s is not set", kv.name))
		}
	}
	return cfgPath, dsn, ns, errors.Join(missing...)
}

// serveBroker runs the Room controller, the AgentRun watch, the :8443 API, the
// :8080 human API and its fan-out hub, the :8090 room tools when roomTools is on, the JWKS refresh
// and the :9090 metrics and probes until ctx ends or one of them fails. :9090 stops last, so the
// drain's final counts are scraped and /readyz answers 503 rather than refusing connections
// meanwhile (review M4).
func serveBroker(ctx context.Context, log *slog.Logger, getenv func(string) string) error {
	cfgPath, dsn, ns, err := brokerEnv(getenv)
	if err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	if err := checkBrokerSecrets(cfg, readMounted); err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	tlsCfg, err := tlsreload.Config(cfg.TLS.CertFile, cfg.TLS.KeyFile, log)
	if err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	exp, err := metrics.NewExporter(metrics.BrokerBuildInfo, version.Version)
	if err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	defer shutdownMetrics(ctx, log, exp)
	m, err := metrics.New(exp.Meter())
	if err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	st, err := store.Open(ctx, dsn, store.RoleBroker)
	if err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	defer st.Close()
	if err := schemaCurrent(ctx, st, log, m.SchemaUnverified); err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	warnPlainDB(log, st.Server, m.DBTLS)
	logStore := &meteredLog{Store: st, appends: st, drivers: st, m: m, now: time.Now}
	red, err := redact.New()
	if err != nil {
		return fmt.Errorf("room-broker: redaction rules: %w", err)
	}
	a, err := authenticators(ctx, cfg, m, jwksVerifier(log), clusterTokenReviews)
	if err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}

	mgr, err := newManager(log, ns, cfg.Runs)
	if err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	events := &runwatch.Events{Store: logStore, Redactor: red}
	rw, err := wireRuns(ctx, cfg.Runs, mgr.GetCache(), mgr.Add, log, st, events, nil)
	if err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	m.TrackRuns(runInfoWatch(rw.watch))
	rc := &roomctrl.Reconciler{Kind: cfg.Runs, Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Store: logStore,
		Runs: rw.watch, Ends: events, Log: log, Forget: m.ForgetRoom, Observe: roomObserver(m, rw.watch)}
	withSuspend(rc, cfg.Suspend, mgr.GetClient(), logStore, log)
	if err := rc.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}

	api, err := bridgeAPI(logStore, red, a.runs, a.systems, rw.watch, mgr.GetClient(), ns, mgr.Add, m, log)
	if err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	api.Queue = logStore // the system queue routes (SP3 R9)
	// The outputs route (hardening HR2): the factory's answers and shadows, from the factory alone.
	api.OutputClient = fconfig.PrincipalFactory
	api.Bundles = st         // a push request's bundle, read by the factory alone (HR22)
	api.OutputApprovals = st // digest-bound approvals, requested by the factory alone (HR28)
	api.Drills = st          // drill reports, written and read by the factory alone (HR38)
	rw.watch.OnGone(api.Drop)
	countLiveRuns(rw.watch, m)
	hub, err := fanoutHub(st, st, m, log)
	if err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	api.Hub, api.LastAck = hub, st.LastAck // the stream's deliveries (phase 4)
	humans, err := humanSide(cfg, a.humans, mgr.GetClient(), ns, logStore, red, hub, rw.watch, accessApp(getenv("ROOMS_GITHUB_APP_DIR")),
		visibilityApp(getenv("ROOMS_GITHUB_APP_DIR")), mgr.Add, m, log)
	if err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	if a.sponsored != nil { // only a non-nil value: the interface is never a typed nil
		humans.Sponsored = a.sponsored
	}
	if l := cfg.Human.SlackLink; l != nil { // the self-serve link: the App mints, a human confirms, the factory reads
		humans.SlackLink = &humanapi.SlackLinker{Store: st, Principal: "system:" + l.Principal, TeamID: l.TeamID, Repository: l.Repository}
		api.SlackLinks, api.SlackLinkReader = st, fconfig.PrincipalFactory
	}
	if l := cfg.Human.GitHubLink; l != nil { // the GitHub account link (v4 D8): a human links, the factory reads
		if humans.GitHubLink, err = githubLinker(l, st, humans.Identity); err != nil {
			return fmt.Errorf("room-broker: %w", err)
		}
		api.GitHubLinks, api.LinkReader, api.GitHubActiveWindow = st, fconfig.PrincipalFactory, l.ActiveWindow.Duration
	}
	if cfg.Human.ApprovalMemory { // merge policy stage 5: the grants' human door, and the factory's routes
		humans.Grants = st
		api.Grants, api.GrantClient = st, fconfig.PrincipalFactory
	}
	if l := cfg.Human.SlackDecide; l != nil { // the App's one deciding route (v3.x Slack push approval D14)
		humans.SlackDecide = &humanapi.SlackDecider{Store: st, Principal: "system:" + l.Principal, TeamID: l.TeamID,
			Rooms: mgr.GetClient(), Namespace: ns, Identity: humans.Identity, Access: humans.Access, Runs: humans.Runs,
			Metrics: m, Logger: log,
			OnDecided: func(d time.Duration) { m.DecisionSeconds.Record(context.Background(), d.Seconds()) }}
		// Groups stays zero on purpose (D15): standing is the Room CR's alone, never a ZITADEL grant the
		// broker would have to hold a token for.
	}
	if sc, on := cfg.SlackIntents(); on { // intent_classify: the App's read-only classification (natural-language intents)
		humans.Intents = intentClassifier(sc, st, red)
	}
	if v := coordinationViews(cfg, mgr.GetAPIReader(), ns); v != nil {
		humans.CoordinationViews = v
		if g := coordinationWrites(cfg); g != nil {
			humans.CoordinationWrites = g
		}
	}
	humans.Policy = policyWorkflow(cfg, st)
	if humans.Intake != nil {
		// A sponsored plan resolves the Slack user to the account it is linked to, so the link tables
		// are the planner's too (internal/humanapi/intake.go's requester).
		humans.Intake.Links = st
	}
	if len(cfg.Human.MCP.Sponsored) > 0 { // the factory's URL is the config's, never a request's
		humans.Tasks = runrequest.Factory{URL: cfg.FactoryURL, CAFile: cfg.FactoryCAFile}
	}
	// :8090 is the agents' listener and the config names it (I8). Absent it starts, as every existing
	// deployment expects, and ROOMS_MCP_KEY alone decides whether calls are accepted (under
	// roomTools.auth tokenreview, the run's own token does); an explicit roomTools.enabled: false opens
	// no port, starts no server and counts no refusal, so the key is not even read.
	roomToolsEnabled := cfg.RoomToolsEnabled()
	var tools http.Handler
	if roomToolsEnabled {
		tools = roomToolsServer(cfg, a, getenv, logStore, red, rw.watch, m, log)
	} else {
		log.Info(":8090 not started: roomTools.enabled is false")
	}
	if err := addVerdictPoster(getenv("ROOMS_GITHUB_APP_DIR"), mgr.Add, logStore, mgr.GetClient(), ns, cfg.PublicURL, m, log); err != nil {
		return fmt.Errorf("room-broker: verdict poster: %w", err)
	}
	if len(cfg.Webhooks) > 0 { // R30: no subscriber, no emitter
		if err := mgr.Add(&webhook.Emitter{Subs: webhookSubscribers(cfg.Webhooks), Events: st,
			Rooms: roomRepository(mgr.GetClient(), ns), State: st, Metrics: m, Logger: log, Now: time.Now, FactoryPrincipal: fconfig.PrincipalFactory}); err != nil {
			return fmt.Errorf("room-broker: webhook emitter: %w", err)
		}
	}
	down := newDrainFlag(ctx)
	ops := opsHandler(st.Ping, st.SchemaReady, boundedSync(rw.synced), down.Draining, exp.Handler())

	var lc net.ListenConfig
	// The config decides :8090 (I8), so a broker that serves no room tool never binds the port.
	humanLn, mcpLn, opsLn, err := openBrokerListeners(ctx, cfg, roomToolsEnabled, lc.Listen)
	if err != nil {
		return err
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
	refreshers := make([]refresher, 0, len(a.verifiers))
	for _, v := range a.verifiers {
		refreshers = append(refreshers, v)
	}
	g, gctx := errgroup.WithContext(runCtx)
	// A listener or the manager dying cancels gctx, not ctx: /readyz must say 503 for that drain too.
	stopWatch := context.AfterFunc(gctx, down.Begin)
	defer stopWatch()
	g.Go(func() error { return api.ListenAndServeTLS(gctx, bridgeAddr, tlsCfg, bridgeDrain) })
	g.Go(func() error { return hub.Run(gctx) })
	g.Go(func() error { return humans.Serve(gctx, humanLn, humanDrain) })
	if roomToolsEnabled {
		g.Go(func() error { return serveHTTP(gctx, mcpServer(tools, mcpCallTimeout, log), mcpLn, mcpDrain) })
	}
	g.Go(func() error { return refreshJWKS(gctx, refreshers, nil) })
	g.Go(func() error {
		if err := mgr.Start(gctx); err != nil {
			return fmt.Errorf("manager: %w", err)
		}
		return nil
	})
	log.Info("room-broker starting", "version", version.Version, "namespace", ns)
	err = g.Wait()
	stopOps()
	if err = errors.Join(err, <-opsDone); err != nil {
		return fmt.Errorf("room-broker: %w", err)
	}
	return nil
}

// fanoutHub is the replica's fan-out hub over the log and its LISTEN session,
// with the listener's gauge (FORWARD 2.6). Every append notifies inside its own
// transaction (Ruling AT), so no writer calls the hub.
func fanoutHub(r fanout.Reader, l fanout.Listener, m *metrics.Set, log *slog.Logger) (*fanout.Hub, error) {
	hub := fanout.New(r, l, log)
	if err := m.WatchFanout(hub.Healthy); err != nil {
		return nil, err
	}
	return hub, nil
}

// humanListenAddr is :8080, on loopback only in local mode: kubectl port-forward reaches it
// inside the pod's network namespace, and nothing else can.
func humanListenAddr(cfg config.Config) string {
	if cfg.Human.EffectiveMode() == config.HumanModeLocal {
		return "127.0.0.1" + humanAddr
	}
	return humanAddr
}

// humanServer is the :8080 API over the broker's parts, its acts served by
// actor. The client ids are read at use (Ruling AS-a); the group names are literals. vis is the
// factory App the planner's public-only read goes through; nil is a broker whose planner cannot
// verify anything, which intakeServer refuses rather than serving.
func humanServer(cfg config.Config, humans humanapi.Authenticator, rooms client.Reader, ns string, roomLog humanapi.Log,
	hub humanapi.Hub, runs humanapi.Runs, actor *humanapi.Actor, vis gitHubVisibilities, m *metrics.Set, log *slog.Logger,
) (*humanapi.Server, error) {
	h := cfg.Human
	start := make(map[string]bool, len(h.MCP.SessionStart))
	for _, r := range h.MCP.SessionStart {
		start[strings.ToLower(r)] = true
	}
	mode := h.EffectiveMode()
	webClient, roomctlClient := idFile(h.ClientIDFile), idFile(h.RoomctlClientIDFile)
	// maku reads the project id under zitadel, the audience under generic (v4 D8).
	projectID, audience := humanAudience(h), func() string { return "" }
	if h.EffectivePreset() == config.PresetGeneric {
		projectID, audience = audience, humanAudience(h)
	}
	if mode == config.HumanModeLocal {
		// No IdP, so no ids to read; Serve refuses nil ones. Access stays nil too (humanSide sets
		// it only from a human.access block, which local mode refuses): the local user is an
		// admin, and the D7 repository check gates members only.
		local := func() string { return authn.LocalClientID }
		webClient, roomctlClient, projectID = local, local, local
	}
	groups := h.EffectiveGroups()
	// The loopback listener has no token, so its Origin is the only CSRF gate and it applies to every
	// route (humanapi's sameOrigin); OIDC leaves it empty and keeps authn.Humans' own check.
	var origins []string
	if mode == config.HumanModeLocal {
		origins = authn.LoopbackOrigins(cfg.PublicURL)
	}
	dashboards := make([]humanapi.Dashboard, 0, len(h.Dashboards))
	for _, d := range h.Dashboards {
		dashboards = append(dashboards, humanapi.Dashboard{Label: d.Label, URL: d.URL})
	}
	in, err := intakeServer(cfg, vis, actor)
	if err != nil {
		return nil, err
	}
	// coordination_plan rides the planner's route and allowance, so it exists only beside it.
	var coord *humanapi.CoordinationPlanner
	if in != nil {
		coord = &humanapi.CoordinationPlanner{Mode: cfg.Coordination.Mode, Resolve: humanapi.ClassPolicy(in.Classes)}
	}
	// A sponsored principal's reach into a family exists only while coordination is not off.
	var sponsoredRepos map[string]map[string]bool
	if cfg.Coordination.Mode != "" && cfg.Coordination.Mode != fconfig.CoordinationOff {
		sponsoredRepos = h.MCP.SponsoredRepositories()
	}
	return &humanapi.Server{Humans: humans, Groups: policy.Groups{Admin: groups.Admin, Member: groups.Member},
		Intake: in, Coordination: coord, Catalogue: catalogueCache(cfg, m), SponsoredRepositories: sponsoredRepos,
		HumanMode: mode, Origins: origins,
		WebClient: webClient, PublicURL: cfg.PublicURL, Issuer: h.Issuer, RoomctlClient: roomctlClient,
		ProjectID: projectID, Audience: audience, Preset: h.EffectivePreset(), TokenUse: h.EffectiveTokenUse(), Scopes: h.Scopes,
		FactoryPrincipal: fconfig.PrincipalFactory,
		Rooms:            rooms, Namespace: ns, Log: roomLog, Hub: hub, Runs: runs, Actor: actor, Metrics: m, UI: ui.FS, Logger: log,
		MCP: h.MCP.Enabled, Needs: h.NeedsEnabled(), SessionStart: start, Dashboards: dashboards, Unsafe: cfg.Install.Unsafe,
		FactoryInstall: factoryInstall(cfg)}, nil
}

// factoryInstall reads the factory's banner codes (v4 D3) with the caller's own token: nil while no
// factory is configured.
func factoryInstall(cfg config.Config) func(ctx context.Context, accessToken string) ([]string, error) {
	if cfg.FactoryURL == "" {
		return nil
	}
	return runrequest.Factory{URL: cfg.FactoryURL, CAFile: cfg.FactoryCAFile}.Install
}

// catalogueCache is the broker's reader of the factory's catalogue (profile choice at intake,
// stage 1): nil while no factory is configured, which lists no classes_list. The URL is the
// config's, never a request's, and the caller's own token travels with each read.
func catalogueCache(cfg config.Config, m *metrics.Set) *humanapi.CatalogueCache {
	if cfg.FactoryURL == "" {
		return nil
	}
	var reads humanapi.CatalogueReads // a nil *metrics.Set must not become a non-nil interface
	if m != nil {
		reads = m
	}
	return humanapi.NewCatalogueCache(runrequest.Factory{URL: cfg.FactoryURL, CAFile: cfg.FactoryCAFile}, reads, nil)
}

// intentClassifier is human.slackIntents as the human API serves it: the one bounded call through the
// audited egress client to the gateway's `classifier` client, the link tables the author is resolved
// through, and the opt-in sample store behind the redactor (SI3, SI12, SI19).
func intentClassifier(sc config.SlackIntentsConfig, st *store.Store, red humanapi.Redactor) *humanapi.IntentClassifier {
	return &humanapi.IntentClassifier{
		Principal: "system:" + sc.Principal, TeamID: sc.TeamID, Alias: sc.Model,
		Model: intent.Model{URL: sc.URL, Alias: sc.Model, KeyFile: sc.APIKeyFile, Deadline: sc.Deadline.Duration,
			HC: httpx.New(sc.Deadline.Duration, nil)},
		Links: st, Samples: st, Redactor: red,
		MinConfidence: sc.MinConfidence, ConfirmTTL: sc.ConfirmTTL.Duration, MaxInputChars: sc.MaxInputChars,
		DailyCalls: sc.DailyCalls, PerUserPerHour: sc.PerUserPerHour, RetentionDays: sc.RetentionDays, Now: time.Now}
}

// intakeServer is human.intake.plan as the human API serves it (v3.x conversational intake): the
// class table and the allowlist from config, the one bounded model call through the audited egress
// client, GitHub's visibility for the public-only guard, the per-requester daily cap and the
// degraded composer's switch. Nil when the switch is off, so neither tool is on any list a
// switched-off deployment serves.
//
// Its two preconditions are refused here rather than per call, because a planner missing either is a
// switch that reads as on and answers every request with a refusal: the visibility read (no App, no
// way to tell a public repository from a private one, so the guard refuses everything with
// repository_unverified) and Actor.Redactor (plan_check scans a plan's text as issue_draft does, and
// checkDraft fails closed without it).
func intakeServer(cfg config.Config, vis gitHubVisibilities, actor *humanapi.Actor) (*humanapi.Intake, error) {
	plan := cfg.IntakePlan()
	if !plan.Enabled {
		return nil, nil
	}
	if vis == nil {
		return nil, errors.New("human.intake.plan needs the factory App: ROOMS_GITHUB_APP_DIR is not set, so no repository's visibility can be read")
	}
	if actor == nil || actor.Redactor == nil {
		return nil, errors.New("human.intake.plan needs Actor.Redactor: a plan's text is scanned as issue_draft's is")
	}
	allow := make(map[string]bool, len(plan.Repositories))
	for _, repository := range plan.Repositories {
		allow[strings.ToLower(repository)] = true
	}
	classes := make(map[string]intake.Class, len(plan.Classes))
	for name, class := range plan.Classes {
		classes[name] = intake.Class{Roles: class.Roles, CeilingUSD: class.CeilingUSD, Source: class.Source}
	}
	return &humanapi.Intake{Classes: classes, Allow: allow,
		Model: &intake.Model{URL: plan.URL, APIKeyFile: plan.APIKeyFile, Alias: plan.Model,
			HC: httpx.New(plan.Deadline.Duration, nil), Deadline: plan.Deadline.Duration, ReasoningEffort: plan.ReasoningEffort},
		Public:    intakePublicity(vis, accessTTL(cfg.Human)),
		Daily:     plan.DailyPlans,
		MaxRounds: plan.MaxRounds,
		Fallback:  plan.Fallback,
		Now:       time.Now}, nil
}

// roomRuns is the one watch method roomObserver reads; *runwatch.Watcher has it.
type roomRuns interface {
	InRoom(room string) []runwatch.Run
}

// roomObserver feeds the room gauges from a reconciled status and the watch: a
// room with a Running run keeps its activity series in any phase (S1 review I-3).
func roomObserver(m *metrics.Set, runs roomRuns) func(string, v1alpha1.RoomStatus, time.Time) {
	return func(room string, s v1alpha1.RoomStatus, last time.Time) {
		running := false
		for _, r := range runs.InRoom(room) {
			running = running || r.Phase == "Running"
		}
		m.ObserveRoom(room, s.Phase, running, int(s.PendingApprovals), last)
	}
}

// managerOptions watches Rooms in the broker's namespace and kind's AgentRuns in theirs
// only: the RBAC (Task 1.18) grants exactly that, so a cluster-wide informer
// would be refused. Unstructured reads stay uncached, so the finalizer's run
// list and deletes see the API server, not the watch.
func managerOptions(ns string, kind runwatch.Kind) ctrl.Options {
	runObj := &unstructured.Unstructured{}
	runObj.SetGroupVersionKind(kind.GVK())
	drain := managerDrain
	return ctrl.Options{
		LeaderElection: true, LeaderElectionID: "room-broker", LeaderElectionNamespace: ns,
		LeaderElectionReleaseOnCancel: true, GracefulShutdownTimeout: &drain,
		// :9090 serves the rooms_* set and the probes itself.
		Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
		Cache: cache.Options{ByObject: map[client.Object]cache.ByObject{
			&v1alpha1.Room{}: {Namespaces: map[string]cache.Config{ns: {}}},
			runObj:           {Namespaces: map[string]cache.Config{kind.Namespace: {}}},
		}},
		Client: client.Options{Cache: &client.CacheOptions{Unstructured: false}},
	}
}

// newManager builds the manager on managerOptions, logging through log.
func newManager(log *slog.Logger, ns string, kind runwatch.Kind) (ctrl.Manager, error) {
	lr := logr.FromSlogHandler(log.Handler())
	// controller-runtime and client-go's leader election log through these globals.
	ctrl.SetLogger(lr)
	klog.SetLogger(lr)
	restCfg, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	// The factory's Task, read (never watched) by the coordination reader.
	if err := factoryv1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	opts := managerOptions(ns, kind)
	opts.Scheme, opts.Logger = scheme, lr
	mgr, err := ctrl.NewManager(restCfg, opts)
	if err != nil {
		return nil, fmt.Errorf("manager: %w", err)
	}
	return mgr, nil
}

func shutdownMetrics(ctx context.Context, log *slog.Logger, exp *metrics.Exporter) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), metricsDrain)
	defer cancel()
	if err := exp.Shutdown(dctx); err != nil {
		log.Warn("metrics shutdown", "err", err)
	}
}

// serveHTTP serves srv on ln until ctx ends, then drains for at most drain.
func serveHTTP(ctx context.Context, srv *http.Server, ln net.Listener, drain time.Duration) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	select {
	case err := <-served:
		return fmt.Errorf("serve %s: %w", ln.Addr(), err)
	case <-ctx.Done():
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), drain)
	defer cancel()
	shut := srv.Shutdown(dctx)
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		shut = errors.Join(shut, err)
	}
	if shut != nil {
		return fmt.Errorf("shut down %s: %w", ln.Addr(), shut)
	}
	return nil
}

// countLiveRuns feeds rooms_participants{kind="run"} from the watch. Call it
// before the informer starts: a Remove of a run the count never saw would drive
// the value negative.
func countLiveRuns(w *runwatch.Watcher, m *metrics.Set) {
	w.OnLive(func(ctx context.Context, d int64) {
		m.Participants.Add(ctx, d, metric.WithAttributes(attribute.String("kind", "run")))
	})
}

// withSuspend turns on the room controller's suspend trigger when the config does (v2
// suspend). Off, the field stays a nil interface, never a typed nil. The trigger patches claims of
// the room controller's kind.
func withSuspend(rc *roomctrl.Reconciler, cfg config.SuspendConfig, c client.Client, a *meteredLog, log *slog.Logger) {
	if !cfg.Enabled {
		return
	}
	rc.Suspend = &suspend.Suspender{Kind: rc.Kind, Client: c, Approvals: a, After: cfg.After.Duration, Log: log}
	log.Info("the suspend trigger is on", "after", cfg.After.Duration)
}

// webhookSubscribers maps the config's subscribers to the emitter's.
func webhookSubscribers(cfg []config.WebhookConfig) []webhook.Subscriber {
	out := make([]webhook.Subscriber, 0, len(cfg))
	for _, w := range cfg {
		out = append(out, webhook.Subscriber{Name: w.Name, Network: w.Network, URL: w.URL, SecretFile: w.SecretFile,
			Events: w.Events, Repositories: w.Repositories, Generation: w.Generation})
	}
	return out
}

// roomRepository reads a Room's repository from the manager's cache (M5): the database has no
// repository column. A Room not found yet is "", which holds the room's events back.
func roomRepository(rooms client.Reader, ns string) webhook.RoomRepo {
	return func(ctx context.Context, room string) (string, error) {
		var r v1alpha1.Room
		err := rooms.Get(ctx, client.ObjectKey{Namespace: ns, Name: room}, &r)
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return r.Spec.Repository, nil
	}
}

// coordinationViews is the human API's coordination reader: the uncached API reader, since a stale
// informer must never decide what a viewer sees of a family (v4 MR13), in the namespace the
// factory's Tasks and day ledgers share with the broker; nil while coordination is off, which
// serves no coordination route.
func coordinationViews(cfg config.Config, r client.Reader, ns string) humanapi.CoordinationViews {
	if cfg.Coordination.Mode == "" || cfg.Coordination.Mode == fconfig.CoordinationOff {
		return nil
	}
	return coordinationview.Source{Reader: r, Namespace: ns}
}

// policyWorkflow is the promotion workflow (merge policy stage 6): nil while
// policyManagement.promotion is off, which serves no settings page and no policy route. The
// evidence is the factory's, read with the caller's own token at the config's URL, never a request's.
func policyWorkflow(cfg config.Config, st *store.Store) *humanapi.Policy {
	if !cfg.PolicyManagement.Promotion || cfg.FactoryURL == "" {
		return nil
	}
	return &humanapi.Policy{Factory: runrequest.Factory{URL: cfg.FactoryURL, CAFile: cfg.FactoryCAFile},
		Records: st, Operator: cfg.PolicyManagement.IsOperator, NewID: runrequest.NewID}
}

// coordinationWrites is the factory's coordination routes, forwarded with the caller's own token:
// nil while coordination is off or no factory is configured. The URL is the config's, never a
// request's.
func coordinationWrites(cfg config.Config) humanapi.CoordinationGateway {
	if cfg.Coordination.Mode == "" || cfg.Coordination.Mode == fconfig.CoordinationOff || cfg.FactoryURL == "" {
		return nil
	}
	return runrequest.Factory{URL: cfg.FactoryURL, CAFile: cfg.FactoryCAFile}
}
