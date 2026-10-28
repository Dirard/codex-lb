package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"codex-lb/internal/adapters/backups"
	"codex-lb/internal/adapters/chatgpt"
	"codex-lb/internal/adapters/egress"
	"codex-lb/internal/adapters/hostinfo"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/metrics"
	"codex-lb/internal/adapters/provider"
	"codex-lb/internal/adapters/sqlite"
	"codex-lb/internal/adapters/streambuffer"
	"codex-lb/internal/adapters/webui"
	"codex-lb/internal/application"
	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

type runtime struct {
	data           *persistentData
	accounts       *application.AccountsService
	tokens         *application.TokenService
	provider       *provider.Adapter
	proxy          *application.Proxy
	transport      *http.Transport
	server         *http.Server
	requests       requestLifecycle
	cancel         context.CancelFunc
	config         config
	logger         *slog.Logger
	usage          *application.AccountUsageService
	catalog        *application.ModelCatalogService
	automations    *application.AutomationsService
	backups        *application.BackupService
	backgroundCtx  context.Context
	stopBackground context.CancelFunc
	background     sync.WaitGroup
	onServing      func()
	metrics        *metrics.Registry
	reporter       *backgroundReporter
	metricsServer  *http.Server
	metricsAddress string
	metricsMu      sync.Mutex
	capacity       domain.CapacityPlan
	started        time.Time
	nextRetention  time.Time // owned by the maintenance loop
	nextOptimize   time.Time
	retention      *application.RetentionService
	backupFiles    *backups.Directory
	// Background work starts once: on activation, or when the launcher moves
	// it here from a retiring worker.
	backgroundOnce sync.Once
	backgroundMu   sync.Mutex // orders startBackground against haltBackground
	retireOnce     sync.Once
	retireStart    chan struct{}
	retired        chan struct{}
	// peerWorkers is the number of other live workers the launcher reported;
	// peerLoadAt is when it last did (Unix nanoseconds).
	peerWorkers atomic.Int32
	peerLoadAt  atomic.Int64
	// openConns counts accepted connections that are not hijacked; retirement
	// waits for it instead of http.Server.Shutdown, which would drop an
	// accepted connection whose request it had not read yet.
	openConns atomic.Int64
}

func openRuntime(ctx context.Context, cfg config, logger *slog.Logger) (*runtime, error) {
	openedAt := time.Now()
	capacity := planCapacity(cfg, detectHost())
	applyCapacity(capacity, logger)
	options := sqlite.Options{ReadConnections: capacity.SQLiteReaders}
	var inheritedLock *os.File
	var livePeers []string
	if cfg.handoff != nil {
		options.InstanceID, inheritedLock, livePeers = cfg.handoff.instance, cfg.handoff.lock, cfg.handoff.peers
	}
	data, err := openDataWithOptions(ctx, cfg.dataDir, options, inheritedLock)
	if err != nil {
		return nil, err
	}
	firewall, err := application.NewFirewall(ctx, data.store)
	if err != nil {
		data.close()
		return nil, errors.New("cannot load firewall policy")
	}
	if err := data.store.EnsureLocalProxyKey(ctx); err != nil {
		data.close()
		return nil, errors.New("cannot initialize local proxy accounting principal")
	}
	// Reservations of workers that still serve beside this one are not interrupted.
	interrupted, err := data.store.MarkInterruptedReservations(ctx, livePeers)
	if err != nil {
		data.close()
		return nil, errors.New("cannot preserve interrupted usage reservations")
	}
	if interrupted != 0 {
		logger.Warn("interrupted usage retained; use offline reconcile-usage before releasing unknown charges", "count", interrupted)
	}
	egressGuard := egress.New(cfg.allowPrivateModelSources, logger)
	transport := &http.Transport{
		Proxy:             egressGuard.Proxy(http.ProxyFromEnvironment),
		DialContext:       egressGuard.DialContext((&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext),
		ForceAttemptHTTP2: true, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second, ExpectContinueTimeout: time.Second,
		MaxIdleConns: 128, MaxIdleConnsPerHost: 64, MaxConnsPerHost: capacity.UpstreamConnsPerHost,
		IdleConnTimeout: 90 * time.Second,
		// Resume TLS for WebSocket dials and new HTTP/1.1 connections.
		TLSClientConfig: &tls.Config{ClientSessionCache: tls.NewLRUClientSessionCache(512)},
		// SSE has no application ping: detect a dead multiplexed HTTP/2
		// connection by transport liveness, not by model progress.
		HTTP2: &http.HTTP2Config{SendPingTimeout: 30 * time.Second, PingTimeout: 10 * time.Second,
			WriteByteTimeout: 30 * time.Second, MaxReceiveBufferPerStream: 1 << 20},
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
		// OAuth bodies and provider tokens must not follow a redirect to a new host.
		return http.ErrUseLastResponse
	}}
	oauthClient := *client
	oauthClient.Timeout = 30 * time.Second
	oauth := chatgpt.NewOAuthClient(chatgpt.OAuthConfig{HTTPClient: &oauthClient})
	tokens := application.NewTokenService(data.store, data.vault, oauth)
	accounts := application.NewAccountsService(data.store, data.store, data.vault, oauth, nil)
	_ = accounts.ConfigureCallbacks(func(callback application.OAuthCallback) (io.Closer, error) {
		return chatgpt.ListenCallback("127.0.0.1:1455", callback)
	})
	catalogClient := chatgpt.NewModelCatalogClient("", "", &oauthClient)
	catalogClient.ResolveClientVersion = data.store.LoadCodexClientVersion
	catalog := application.NewModelCatalogService(data.store, catalogClient, tokens, application.ModelCatalogConfig{})
	adapter := provider.New(data.store, tokens, data.vault, provider.Config{HTTPClient: client, UpstreamWebSocketSessions: capacity.UpstreamWebSockets,
		ResolveClientVersion: data.store.LoadCodexClientVersion, ChatGPTReasoningFallback: catalog.SubscriptionReasoningFallback})
	if err := catalog.Load(ctx); err != nil {
		_ = accounts.Close()
		_ = tokens.Close()
		_ = adapter.Close()
		data.close()
		return nil, errors.New("cannot load model catalog snapshot")
	}
	proxy := application.NewProxy(data.store, adapter, data.vault, application.ProxyConfig{MaxStreams: capacity.MaxGenerations, MaxQueued: capacity.MaxQueued})
	proxy.OpenResponsePrelude = func() (application.ResponsePrelude, error) { return streambuffer.New(cfg.dataDir, data.vault) }
	proxy.Catalog = catalog
	archives := application.NewErrorArchives(data.store, data.vault)
	proxy.Diagnostics = func(ctx context.Context, diagnostic application.ErrorDiagnostic) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		if err := archives.RecordFailure(cleanup, diagnostic); err != nil {
			logger.Warn("could not persist error diagnostic", "error_type", fmt.Sprintf("%T", err))
		}
	}
	sources := application.NewModelSourceService(data.store, data.vault)
	modelPricing := application.NewModelPricingService(data.store, nil)
	proxy.ResolvePrice = func(ctx context.Context, account domain.Account, model string) (pricing.Price, error) {
		if account.Kind == domain.AccountExternal {
			return sources.Price(ctx, account, model)
		}
		return modelPricing.ResolveCodex(ctx, model)
	}
	api := httpapi.New(data.store, data.vault, cfg.apiConfig(version), logger)
	if err := api.Prepare(ctx); err != nil {
		_ = accounts.Close()
		_ = tokens.Close()
		_ = adapter.Close()
		data.close()
		return nil, errors.New("cannot prepare dashboard authentication")
	}
	api.ConfigureRuntimeUpdater(cfg.updater)
	api.ConfigureAccounts(accounts)
	api.ConfigureFirewall(firewall)
	api.ConfigureErrorArchives(data.store)
	api.ConfigureModelCatalog(catalog)
	api.ConfigureModelPricing(data.store)
	usageClient := chatgpt.NewUsageClient(chatgpt.UsageConfig{HTTPClient: &oauthClient})
	registry := metrics.NewRegistry()
	reporter := newBackgroundReporter(logger, registry)
	usage := application.NewAccountUsageService(data.store, tokens, usageClient, data.store, time.Now)
	usage.ConfigureBackground(application.BackgroundSchedule{InitialDelay: startupJitter(10*time.Second, 20*time.Second), Report: reporter.report})
	usage.ConfigureQuotaRecovery(data.store)
	api.ConfigureUsage(usage)
	warmups := application.NewWarmupService(data.store, adapter, data.store, proxy, usage, time.Now)
	warmups.ResolvePrice = proxy.ResolvePrice
	warmups.Diagnostics = proxy.Diagnostics
	warmups.ConfigureAccountAdmission(proxy)
	api.ConfigureWarmups(warmups)
	limitWarmup := application.NewLimitWarmupService(data.store, data.store, warmups, data.store, time.Now)
	limitWarmup.ConfigureCatalog(catalog)
	usage.ConfigureLimitWarmups(limitWarmup)
	automations := application.NewAutomationsService(data.store, data.store, warmups, time.Now)
	automations.ConfigureBackground(application.BackgroundSchedule{InitialDelay: startupJitter(5*time.Second, 10*time.Second), Report: reporter.report})
	catalog.ConfigureBackground(application.BackgroundSchedule{InitialDelay: startupJitter(30*time.Second, 30*time.Second), Report: reporter.report})
	api.ConfigureAutomations(automations)
	backupFiles, err := backups.OpenDirectory(filepath.Join(cfg.dataDir, "backups"))
	if err != nil {
		_ = accounts.Close()
		_ = tokens.Close()
		_ = adapter.Close()
		data.close()
		return nil, err
	}
	backupService := application.NewBackupService(sqlite.NewSnapshotter(data.store, data.vault), data.store, backupFiles, time.Now)
	api.ConfigureBackups(backupService)
	baseCtx, cancel := context.WithCancel(context.Background())
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	r := &runtime{data: data, accounts: accounts, tokens: tokens, provider: adapter,
		proxy: proxy, transport: transport, cancel: cancel, config: cfg, logger: logger,
		usage: usage, catalog: catalog, automations: automations, backups: backupService,
		backgroundCtx: backgroundCtx, stopBackground: stopBackground,
		metrics: registry, capacity: capacity, started: openedAt, backupFiles: backupFiles,
		retireStart: make(chan struct{}), retired: make(chan struct{})}
	r.reporter = reporter
	proxyMux := http.NewServeMux()
	keyUsage := httpapi.NewKeyUsageHandler(data.store)
	keyUsage.RegisterPublicRoutes(proxyMux)
	operations := application.NewCodexOperations(data.store, data.store, adapter, 0)
	operations.Catalog = catalog
	operations.ConfigureAdmission(proxy)
	operations.ConfigureAccountSelection(proxy)
	proxy.ConfigureCompaction(operations)
	operations.ConfigureDiagnostics(proxy.Diagnostics)
	operations.ConfigurePrice(func(ctx context.Context, account domain.Account, model string) (pricing.Price, error) {
		price, err := proxy.ResolvePrice(ctx, account, model)
		if errors.Is(err, pricing.ErrUnpriced) && account.Kind == domain.AccountChatGPT && model == "gpt-4o-transcribe" {
			return pricing.Price{}, nil
		}
		return price, err
	})
	// One request-body budget is shared by every proxy route that reads a body.
	bodies := httpapi.NewBodyBudget(capacity.BodyBudgetBytes, capacity.BodyBudgetKeyBytes)
	operationRoutes := httpapi.NewCodexOperationsHandler(data.store, operations, cfg.trusted)
	operationRoutes.ConfigureBodyBudget(bodies)
	operationRoutes.RegisterRoutes(proxyMux)
	native := application.NewNativeAPIService(data.store, adapter, adapter, proxy)
	native.ConfigureAdmission(proxy)
	native.ConfigureDiagnostics(proxy.Diagnostics)
	native.ConfigurePrice(proxy.ResolvePrice)
	nativeRoutes := httpapi.NewNativeAPIHandler(data.store, native, cfg.trusted)
	nativeRoutes.ConfigureBodyBudget(bodies)
	nativeRoutes.RegisterRoutes(proxyMux)
	httpapi.NewModelCatalogHandler(data.store, catalog).RegisterPublicRoutes(proxyMux)
	responses := httpapi.NewProxyHandlerWithLimits(data.store, proxy, cfg.trusted, httpapi.ProxyHandlerLimits{Bodies: bodies, ClientWebSockets: capacity.ClientWebSockets})
	api.ConfigureRuntimeCapacity(httpapi.RuntimeCapacitySource{Plan: capacity, Usage: func() httpapi.CapacityUsage {
		active, queued := proxy.AdmissionUsage()
		sockets, _ := responses.ClientWebSockets()
		body := bodies.Usage()
		return httpapi.CapacityUsage{ActiveGenerations: active, QueuedGenerations: queued, ClientWebSockets: sockets,
			BodyBytes: body.UsedBytes, BodyRejections: body.Rejected, ResidentBytes: hostinfo.ResidentBytes()}
	}})
	responses.QuotaHeaders = keyUsage.QuotaHeaders
	responses.RegisterRoutes(proxyMux)
	r.registerGauges(responses, bodies)
	handler := api.Handler(proxyMux, webui.Handler())
	mux := http.NewServeMux()
	for _, path := range []string{"/health", "/health/live", "/health/startup", "/health/ready"} {
		mux.HandleFunc("GET "+path, r.health)
	}
	mux.Handle("/", handler)
	r.server = &http.Server{Addr: cfg.listen, Handler: accessLog(r.requests.track(mux), logger, registry, cfg.trusted),
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: httpapi.RequestBodyTimeout, IdleTimeout: 90 * time.Second,
		MaxHeaderBytes: 64 << 10,
		// No whole-response WriteTimeout: SSE/WS may legitimately run for hours.
	}
	return r, nil
}

func (r *runtime) serve(ctx context.Context) error {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", r.config.listen)
	if err != nil {
		r.close()
		return listenError("HTTP listen address", r.config.listen, err)
	}
	return r.serveListener(ctx, listener)
}

// startBackground starts the loops, the metrics listener and cleanup that
// exactly one worker of an installation runs.
func (r *runtime) startBackground() {
	r.backgroundMu.Lock()
	defer r.backgroundMu.Unlock()
	r.backgroundOnce.Do(func() {
		select {
		case <-r.backgroundCtx.Done():
			return
		default:
		}
		r.background.Add(7)
		go func() {
			defer r.background.Done()
			// A retiring worker may still write a requested backup's temp file.
			if r.waitForSoleWorker(r.backgroundCtx) {
				r.backupFiles.RemoveStaleTemps()
			}
		}()
		go func() {
			defer r.background.Done()
			application.RunPeriodic(r.backgroundCtx, "maintenance", application.BackgroundSchedule{Report: r.reporter.report}, time.Minute, nil, r.maintainReports)
		}()
		go func() { defer r.background.Done(); r.maintainBackups(r.backgroundCtx) }()
		go func() { defer r.background.Done(); r.usage.RunUsagePoller(r.backgroundCtx, time.Minute) }()
		go func() { defer r.background.Done(); _ = r.catalog.Run(r.backgroundCtx) }()
		go func() { defer r.background.Done(); r.automations.RunAutomationsPoller(r.backgroundCtx) }()
		go func() {
			defer r.background.Done()
			// Repricing writes in long chunk series; let a retiring worker
			// settle its requests first.
			if r.waitForSoleWorker(r.backgroundCtx) {
				_ = r.data.store.ResumeModelReprices(r.backgroundCtx)
			}
		}()
		if err := r.startMetrics(); err != nil {
			r.logger.Warn("metrics listener disabled", "address", r.config.metricsListen, "reason", err.Error())
		}
	})
}

// waitForSoleWorker returns true once no other worker of the installation
// serves, or false when ctx ends first.
func (r *runtime) waitForSoleWorker(ctx context.Context) bool {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for r.peerWorkers.Load() > 0 {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
	return ctx.Err() == nil
}

// haltBackground stops background loops and metrics for good; a later
// startBackground does nothing.
func (r *runtime) haltBackground() {
	r.backgroundMu.Lock()
	r.stopBackground()
	r.backgroundMu.Unlock()
	r.stopMetrics()
	r.background.Wait()
}

// retire asks a serving worker to hand its traffic over and returns once its
// background work has stopped. Accepted requests keep running.
func (r *runtime) retire(ctx context.Context) error {
	r.retireOnce.Do(func() { close(r.retireStart) })
	select {
	case <-r.retired:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retirementDeadline bounds how long a retiring worker finishes accepted work
// before the ordinary drain and its shutdown grace apply.
const retirementDeadline = 30 * time.Minute

func (r *runtime) serveListener(ctx context.Context, listener net.Listener) error {
	// A worker started beside a serving one leaves background work to it until
	// the launcher moves it here.
	if r.config.handoff == nil || !r.config.handoff.overlap {
		r.startBackground()
	}
	r.server.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			r.openConns.Add(1)
		case http.StateHijacked, http.StateClosed:
			r.openConns.Add(-1)
		}
	}
	served := make(chan error, 1)
	go func() {
		if r.onServing != nil {
			r.onServing()
		}
		served <- r.server.Serve(listener)
	}()
	r.logger.Info("codex-lb ready", "address", listener.Addr().String(), "version", version,
		"dashboard_auth_mode", r.config.authMode, "trusted_proxies", len(r.config.trusted), "metrics_listen", r.config.metricsListen,
		"max_generations", r.capacity.MaxGenerations, "startup_ms", time.Since(r.started).Milliseconds())
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-served:
	case <-r.retireStart:
		r.retireServing(ctx, listener)
	}
	drainStarted := time.Now()
	activeAtDrain, queuedAtDrain := r.proxy.AdmissionUsage()
	r.logger.Info("draining active requests", "active_generations", activeAtDrain, "queued_generations", queuedAtDrain)
	r.requests.beginDrain()
	r.proxy.BeginDrain()
	r.backgroundMu.Lock()
	r.stopBackground()
	r.backgroundMu.Unlock()
	joined := make(chan struct{})
	go func() { r.requests.active.Wait(); close(joined) }()
	grace, stopGrace := context.WithTimeout(context.Background(), r.config.shutdownGrace)
	shutdown := make(chan struct{})
	go func() { _ = r.server.Shutdown(grace); close(shutdown) }()
	graceExpired := false
	select {
	case <-joined:
	case <-grace.Done():
		graceExpired = true
	}
	stopGrace()
	r.stopMetrics()
	r.cancel()
	_ = r.server.Close() // Also interrupts incomplete request-body reads.
	<-shutdown
	// http.Server.Shutdown does not join hijacked WebSockets. Our handler
	// lifetime includes them and their accounting finalizers.
	cleanup := time.NewTimer(45 * time.Second)
	defer cleanup.Stop()
	select {
	case <-joined:
	case <-cleanup.C:
		// Do not close SQLite beneath a still-running finalizer. The command exits
		// nonzero; durable reservations remain for explicit reconciliation.
		return errors.New("request cleanup did not finish; unresolved accounting retained for reconciliation")
	}
	if graceExpired {
		r.logger.Warn("drain complete; requests still running at the grace deadline were cancelled", "duration_ms", time.Since(drainStarted).Milliseconds(), "shutdown_grace", r.config.shutdownGrace.String())
	} else {
		r.logger.Info("drain complete", "duration_ms", time.Since(drainStarted).Milliseconds())
	}
	if err := r.close(); err != nil {
		return errors.New("runtime storage did not close cleanly")
	}
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return errors.New("HTTP server stopped unexpectedly")
	}
	return nil
}

// retireServing stops accepting and background work, then lets every accepted
// request — queued admissions and current WebSocket turns included — finish
// before the ordinary drain, bounded by retirementDeadline.
func (r *runtime) retireServing(ctx context.Context, listener net.Listener) {
	retiredAt := time.Now()
	active, queued := r.proxy.AdmissionUsage()
	r.logger.Info("retiring: new connections go to the replacement worker", "active_generations", active, "queued_generations", queued)
	deadline, cancel := context.WithTimeout(context.Background(), retirementDeadline)
	defer cancel()
	// Stop accepting first, without http.Server.Shutdown: it would drop an
	// accepted connection whose request is still unread. Closing this worker's
	// copy of the shared listener leaves the socket to the replacement; idle
	// keep-alive connections close and later responses carry Connection: close.
	r.proxy.BeginRetire()
	r.server.SetKeepAlivesEnabled(false)
	_ = listener.Close()
	r.haltBackground()
	close(r.retired)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for r.openConns.Load() > 0 {
		select {
		case <-ticker.C:
		case <-deadline.Done():
			r.logger.Warn("retirement deadline reached; draining remaining requests", "deadline", retirementDeadline.String())
			return
		case <-ctx.Done():
			return
		}
	}
	// Only hijacked WebSockets remain, and no connection can start a new
	// request, so waiting for the request lifecycle cannot race an Add.
	joined := make(chan struct{})
	go func() { r.requests.active.Wait(); close(joined) }()
	select {
	case <-joined:
		r.logger.Info("retired worker finished its requests", "duration_ms", time.Since(retiredAt).Milliseconds())
	case <-deadline.Done():
		r.logger.Warn("retirement deadline reached; draining remaining requests", "deadline", retirementDeadline.String())
	case <-ctx.Done():
	}
}

func (r *runtime) close() error {
	r.cancel()
	r.stopBackground()
	r.background.Wait()
	// All callers of the database have finished before closing it.
	err := errors.Join(r.accounts.Close(), r.tokens.Close(), r.provider.Close())
	r.transport.CloseIdleConnections()
	return errors.Join(err, r.data.close())
}
