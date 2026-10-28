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
	"sync"
	"time"

	"codex-lb/internal/adapters/chatgpt"
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
}

func openRuntime(ctx context.Context, cfg config, logger *slog.Logger) (*runtime, error) {
	openedAt := time.Now()
	capacity := planCapacity(cfg, detectHost())
	applyCapacity(capacity, logger)
	data, err := openDataWithOptions(ctx, cfg.dataDir, sqlite.Options{ReadConnections: capacity.SQLiteReaders})
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
	interrupted, err := data.store.MarkInterruptedReservations(ctx)
	if err != nil {
		data.close()
		return nil, errors.New("cannot preserve interrupted usage reservations")
	}
	if interrupted != 0 {
		logger.Warn("interrupted usage retained; use offline reconcile-usage before releasing unknown charges", "count", interrupted)
	}
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
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
	api := httpapi.New(data.store, data.vault, httpapi.Config{TrustedProxies: cfg.trusted, BootstrapToken: cfg.bootstrapToken, Version: version, ConnectAddress: cfg.connectAddress, DashboardAuthMode: cfg.authMode, DashboardAuthHeader: cfg.authHeader, UnauthenticatedClientCIDRs: cfg.unauthenticatedClients}, logger)
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
	baseCtx, cancel := context.WithCancel(context.Background())
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	r := &runtime{data: data, accounts: accounts, tokens: tokens, provider: adapter,
		proxy: proxy, transport: transport, cancel: cancel, config: cfg, logger: logger,
		usage: usage, catalog: catalog, automations: automations, backgroundCtx: backgroundCtx, stopBackground: stopBackground,
		metrics: registry, capacity: capacity, started: openedAt}
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
	httpapi.RegisterCodexOperationRoutes(proxyMux, data.store, operations, cfg.trusted)
	native := application.NewNativeAPIService(data.store, adapter, adapter, proxy)
	native.ConfigureAdmission(proxy)
	native.ConfigureDiagnostics(proxy.Diagnostics)
	native.ConfigurePrice(proxy.ResolvePrice)
	httpapi.RegisterNativeAPIRoutes(proxyMux, data.store, native, cfg.trusted)
	httpapi.NewModelCatalogHandler(data.store, catalog).RegisterPublicRoutes(proxyMux)
	responses := httpapi.NewProxyHandlerWithLimits(data.store, proxy, cfg.trusted, httpapi.ProxyHandlerLimits{BodyReaders: capacity.BodyReaders, ClientWebSockets: capacity.ClientWebSockets})
	api.ConfigureRuntimeCapacity(httpapi.RuntimeCapacitySource{Plan: capacity, Usage: func() httpapi.CapacityUsage {
		active, queued := proxy.AdmissionUsage()
		sockets, _ := responses.ClientWebSockets()
		return httpapi.CapacityUsage{ActiveGenerations: active, QueuedGenerations: queued, ClientWebSockets: sockets, ResidentBytes: hostinfo.ResidentBytes()}
	}})
	responses.QuotaHeaders = keyUsage.QuotaHeaders
	responses.RegisterRoutes(proxyMux)
	r.registerGauges(responses)
	handler := api.Handler(proxyMux, webui.Handler())
	mux := http.NewServeMux()
	for _, path := range []string{"/health", "/health/live", "/health/startup", "/health/ready"} {
		mux.HandleFunc("GET "+path, r.health)
	}
	mux.Handle("/", handler)
	r.server = &http.Server{Addr: cfg.listen, Handler: accessLog(r.requests.track(mux), logger, registry, cfg.trusted),
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, IdleTimeout: 90 * time.Second,
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

func (r *runtime) serveListener(ctx context.Context, listener net.Listener) error {
	r.background.Add(4)
	go func() {
		defer r.background.Done()
		application.RunPeriodic(r.backgroundCtx, "maintenance", application.BackgroundSchedule{Report: r.reporter.report}, time.Minute, nil, r.maintainReports)
	}()
	go func() { defer r.background.Done(); r.usage.RunUsagePoller(r.backgroundCtx, time.Minute) }()
	go func() { defer r.background.Done(); _ = r.catalog.Run(r.backgroundCtx) }()
	go func() { defer r.background.Done(); r.automations.RunAutomationsPoller(r.backgroundCtx) }()
	served := make(chan error, 1)
	go func() {
		if r.onServing != nil {
			r.onServing()
		}
		served <- r.server.Serve(listener)
	}()
	if err := r.startMetrics(); err != nil {
		r.logger.Warn("metrics listener disabled", "address", r.config.metricsListen, "reason", err.Error())
	}
	r.logger.Info("codex-lb ready", "address", listener.Addr().String(), "version", version,
		"dashboard_auth_mode", r.config.authMode, "trusted_proxies", len(r.config.trusted), "metrics_listen", r.config.metricsListen,
		"max_generations", r.capacity.MaxGenerations, "startup_ms", time.Since(r.started).Milliseconds())
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-served:
	}
	drainStarted := time.Now()
	activeAtDrain, queuedAtDrain := r.proxy.AdmissionUsage()
	r.logger.Info("draining active requests", "active_generations", activeAtDrain, "queued_generations", queuedAtDrain)
	r.requests.beginDrain()
	r.proxy.BeginDrain()
	r.stopBackground()
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

func (r *runtime) close() error {
	r.cancel()
	r.stopBackground()
	r.background.Wait()
	// All callers of the database have finished before closing it.
	err := errors.Join(r.accounts.Close(), r.tokens.Close(), r.provider.Close())
	r.transport.CloseIdleConnections()
	return errors.Join(err, r.data.close())
}
