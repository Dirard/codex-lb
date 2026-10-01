package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"codex-lb/internal/adapters/chatgpt"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/adapters/provider"
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
}

func openRuntime(ctx context.Context, cfg config, logger *slog.Logger) (*runtime, error) {
	data, err := openData(ctx, cfg.dataDir)
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
		MaxIdleConns: 128, MaxIdleConnsPerHost: 64, MaxConnsPerHost: 256,
		IdleConnTimeout: 90 * time.Second,
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
	adapter := provider.New(data.store, tokens, data.vault, provider.Config{HTTPClient: client,
		ResolveClientVersion: data.store.LoadCodexClientVersion, ChatGPTReasoningFallback: catalog.SubscriptionReasoningFallback})
	if err := catalog.Load(ctx); err != nil {
		_ = accounts.Close()
		_ = tokens.Close()
		_ = adapter.Close()
		data.close()
		return nil, errors.New("cannot load model catalog snapshot")
	}
	proxy := application.NewProxy(data.store, adapter, data.vault, application.ProxyConfig{})
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
	api.ConfigureAccounts(accounts)
	api.ConfigureFirewall(firewall)
	api.ConfigureErrorArchives(data.store)
	api.ConfigureModelCatalog(catalog)
	api.ConfigureModelPricing(data.store)
	usageClient := chatgpt.NewUsageClient(chatgpt.UsageConfig{HTTPClient: &oauthClient})
	usage := application.NewAccountUsageService(data.store, tokens, usageClient, data.store, time.Now)
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
	api.ConfigureAutomations(automations)
	baseCtx, cancel := context.WithCancel(context.Background())
	backgroundCtx, stopBackground := context.WithCancel(context.Background())
	r := &runtime{data: data, accounts: accounts, tokens: tokens, provider: adapter,
		proxy: proxy, transport: transport, cancel: cancel, config: cfg, logger: logger,
		usage: usage, catalog: catalog, automations: automations, backgroundCtx: backgroundCtx, stopBackground: stopBackground}
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
	httpapi.RegisterCodexOperationRoutes(proxyMux, data.store, operations)
	native := application.NewNativeAPIService(data.store, adapter, adapter, proxy)
	native.ConfigureAdmission(proxy)
	native.ConfigureDiagnostics(proxy.Diagnostics)
	native.ConfigurePrice(proxy.ResolvePrice)
	httpapi.RegisterNativeAPIRoutes(proxyMux, data.store, native, cfg.trusted)
	httpapi.NewModelCatalogHandler(data.store, catalog).RegisterPublicRoutes(proxyMux)
	responses := httpapi.NewProxyHandler(data.store, proxy, cfg.trusted)
	responses.QuotaHeaders = keyUsage.QuotaHeaders
	responses.RegisterRoutes(proxyMux)
	handler := api.Handler(proxyMux, webui.Handler())
	mux := http.NewServeMux()
	for _, path := range []string{"/health", "/health/live", "/health/startup", "/health/ready"} {
		mux.HandleFunc("GET "+path, r.health)
	}
	mux.Handle("/", handler)
	r.server = &http.Server{Addr: cfg.listen, Handler: r.requests.track(mux),
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, IdleTimeout: 90 * time.Second,
		MaxHeaderBytes: 64 << 10,
		// No whole-response WriteTimeout: SSE/WS may legitimately run for hours.
	}
	return r, nil
}

func (r *runtime) serve(ctx context.Context) error {
	listener, err := net.Listen("tcp", r.config.listen)
	if err != nil {
		r.close()
		return errors.New("cannot bind HTTP listen address")
	}
	return r.serveListener(ctx, listener)
}

func (r *runtime) serveListener(ctx context.Context, listener net.Listener) error {
	r.background.Add(4)
	go func() { defer r.background.Done(); r.maintainReports(r.backgroundCtx) }()
	go func() { defer r.background.Done(); r.usage.RunUsagePoller(r.backgroundCtx, time.Minute) }()
	go func() { defer r.background.Done(); _ = r.catalog.Run(r.backgroundCtx) }()
	go func() { defer r.background.Done(); r.automations.RunAutomationsPoller(r.backgroundCtx) }()
	served := make(chan error, 1)
	go func() { served <- r.server.Serve(listener) }()
	r.logger.Info("codex-lb ready", "address", listener.Addr().String(), "version", version)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-served:
	}
	r.logger.Info("draining active requests")
	r.requests.beginDrain()
	r.proxy.BeginDrain()
	r.stopBackground()
	joined := make(chan struct{})
	go func() { r.requests.active.Wait(); close(joined) }()
	grace, stopGrace := context.WithTimeout(context.Background(), r.config.shutdownGrace)
	shutdown := make(chan struct{})
	go func() { _ = r.server.Shutdown(grace); close(shutdown) }()
	select {
	case <-joined:
	case <-grace.Done():
	}
	stopGrace()
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
