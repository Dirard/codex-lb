package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/adapters/credentials"
	"codex-lb/internal/adapters/httpapi"
	"codex-lb/internal/application"
)

type config struct {
	dataDir, listen, source, sourceKey        string
	output                                    string
	trusted                                   []netip.Prefix
	unauthenticatedClients                    []netip.Prefix
	bootstrapToken                            string
	shutdownGrace                             time.Duration
	dryRun                                    bool
	authMode, authHeader                      string
	connectAddress                            string
	reservation, settlement, afterReservation string
	releaseReservation                        bool
	selfUpdate                                bool
	memoryLimit                               int64 // 0 derive from host, <0 leave unset
	maxGenerations, maxClientWebSockets       int
	logLevel                                  slog.Level
	metricsListen                             string
	publicOrigin                              string
	allowPrivateModelSources                  bool
	updater                                   application.RuntimeUpdater
	handoff                                   *workerHandoff // lent by the launcher, never a flag
}

func parseConfig(command string, args []string, output io.Writer) (config, error) {
	cfg := config{dataDir: os.Getenv("CODEX_LB_DATA_DIR"), bootstrapToken: os.Getenv("CODEX_LB_DASHBOARD_BOOTSTRAP_TOKEN")}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&cfg.dataDir, "data-dir", cfg.dataDir, "absolute private data directory (CODEX_LB_DATA_DIR)")
	if command == "serve" {
		flags.BoolVar(&cfg.selfUpdate, "self-update", true, "enable the built-in version manager; no external service-manager access")
		cfg.connectAddress = strings.TrimSpace(os.Getenv("CODEX_LB_CONNECT_ADDRESS"))
		flags.StringVar(&cfg.authMode, "dashboard-auth-mode", "standard", "standard, trusted_header, or explicitly disabled authentication")
		flags.StringVar(&cfg.authHeader, "dashboard-auth-header", "X-Auth-Request-Email", "identity header supplied by a configured trusted reverse proxy")
		flags.StringVar(&cfg.listen, "listen", "127.0.0.1:2455", "HTTP listen address; use a reverse proxy for TLS")
		flags.DurationVar(&cfg.shutdownGrace, "shutdown-grace", 30*time.Second, "time allowed for active responses before cancellation")
		flags.Func("memory-limit", "Go soft memory limit: auto (75% of detected memory), off, or a size such as 3GiB; GOMEMLIMIT is honored when this is auto", func(raw string) error {
			limit, err := parseMemoryLimit(raw)
			if err != nil {
				return err
			}
			cfg.memoryLimit = limit
			return nil
		})
		flags.Func("log-level", "debug, info, warn or error; successful requests are logged at debug", func(raw string) error {
			return cfg.logLevel.UnmarshalText([]byte(strings.TrimSpace(raw)))
		})
		flags.StringVar(&cfg.metricsListen, "metrics-listen", "", "optional address for Prometheus /metrics and /debug/pprof, e.g. 127.0.0.1:9464; unauthenticated, keep it on loopback")
		flags.Func("max-generations", "concurrent generation bound; default derived from detected memory (512 on a 1 GB host)", positiveIntFlag("max-generations", &cfg.maxGenerations))
		flags.Func("max-client-websockets", "client WebSocket bound; default derived from detected memory and descriptor limit (4096 on a 1 GB host)", positiveIntFlag("max-client-websockets", &cfg.maxClientWebSockets))
		flags.StringVar(&cfg.publicOrigin, "public-origin", "", "HTTPS origin browsers use for the dashboard, e.g. https://lb.example.com; enables Secure cookies and HSTS")
		flags.BoolVar(&cfg.allowPrivateModelSources, "allow-private-model-sources", false, "allow model sources on loopback, private and link-local addresses (self-hosted sources)")
		flags.Func("unauthenticated-client", "client CIDR (resolved through trusted proxies) permitted without a key only while API-key auth is disabled", func(raw string) error {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return errors.New("unauthenticated-client must be a CIDR")
			}
			cfg.unauthenticatedClients = append(cfg.unauthenticatedClients, prefix.Masked())
			return nil
		})
		flags.Func("trusted-proxy", "trusted reverse-proxy CIDR; repeat for multiple networks", func(raw string) error {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return errors.New("trusted-proxy must be a CIDR")
			}
			cfg.trusted = append(cfg.trusted, prefix.Masked())
			return nil
		})
	} else if command == "backup" {
		flags.StringVar(&cfg.output, "output", "", "absolute new snapshot file, or an existing directory for a timestamped name")
	} else if command == "reconcile-usage" {
		flags.StringVar(&cfg.reservation, "reservation", "", "interrupted reservation to reconcile; omit to list")
		flags.StringVar(&cfg.settlement, "settlement", "", "absolute JSON file with confirmed status, usage, optional accountId and serviceTier")
		flags.BoolVar(&cfg.releaseReservation, "release", false, "explicitly release a reservation confirmed not to have consumed usage")
		flags.StringVar(&cfg.afterReservation, "after", "", "list reservations after this ID")
	} else {
		flags.StringVar(&cfg.source, "source", "", "checkpointed COPY of legacy SQLite; original is not modified")
		flags.StringVar(&cfg.sourceKey, "source-key", "", "private Fernet key file for that snapshot")
		flags.BoolVar(&cfg.dryRun, "dry-run", false, "run a complete import in a temporary directory; leave destination untouched")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, err
		}
		return cfg, errors.New("invalid command options; use --help")
	}
	if cfg.dataDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return cfg, errors.New("set --data-dir to an absolute private data directory")
		}
		cfg.dataDir = filepath.Join(dir, "codex-lb")
	}
	if flags.NArg() != 0 || !filepath.IsAbs(cfg.dataDir) {
		return cfg, errors.New("data-dir must be absolute; unexpected positional arguments are not accepted")
	}
	cfg.dataDir = filepath.Clean(cfg.dataDir)
	if cfg.dataDir == string(filepath.Separator) {
		return cfg, errors.New("data-dir must be a dedicated private directory")
	}
	if command == "serve" {
		if err := httpapi.ValidateConfig(cfg.apiConfig("")); err != nil {
			return cfg, err
		}
		host, port, err := net.SplitHostPort(cfg.listen)
		if err != nil || strings.TrimSpace(host) != host || port == "" || cfg.shutdownGrace < 0 || cfg.shutdownGrace > 30*time.Minute {
			return cfg, errors.New("invalid listen address or shutdown-grace (expected 0s to 30m)")
		}
		if cfg.metricsListen != "" {
			if _, port, err := net.SplitHostPort(cfg.metricsListen); err != nil || port == "" {
				return cfg, errors.New("invalid metrics-listen address")
			}
		}
		if cfg.authMode == "disabled" && !loopbackListenHost(host) {
			return cfg, errors.New("dashboard-auth-mode=disabled requires a loopback --listen address")
		}
	} else if command == "backup" {
		if !filepath.IsAbs(cfg.output) {
			return cfg, errors.New("backup requires an absolute --output file or directory")
		}
		cfg.output = filepath.Clean(cfg.output)
	} else if command == "reconcile-usage" {
		if cfg.reservation == "" && (cfg.settlement != "" || cfg.releaseReservation) || cfg.reservation != "" && (cfg.afterReservation != "" || cfg.releaseReservation == (cfg.settlement != "")) {
			return cfg, errors.New("supply --reservation with exactly one of --release or --settlement; omit both to list")
		}
		if cfg.settlement != "" && !filepath.IsAbs(cfg.settlement) {
			return cfg, errors.New("settlement file must be an absolute path")
		}
	} else if !filepath.IsAbs(cfg.source) || !filepath.IsAbs(cfg.sourceKey) {
		return cfg, errors.New("import-legacy requires absolute --source and --source-key paths")
	}
	return cfg, nil
}

// apiConfig is the HTTP adapter configuration derived from command options.
func (cfg config) apiConfig(version string) httpapi.Config {
	return httpapi.Config{
		TrustedProxies: cfg.trusted, BootstrapToken: cfg.bootstrapToken,
		BootstrapTokenFile: credentials.NewBootstrapTokenFile(filepath.Join(cfg.dataDir, "dashboard-bootstrap-token")),
		Version:            version, ConnectAddress: cfg.connectAddress,
		DashboardAuthMode: cfg.authMode, DashboardAuthHeader: cfg.authHeader,
		UnauthenticatedClientCIDRs: cfg.unauthenticatedClients,
		PublicOrigin:               cfg.publicOrigin, AllowPrivateModelSources: cfg.allowPrivateModelSources,
	}
}

func loopbackListenHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// listenError names the address and the operating-system reason (for example
// "address already in use" or "permission denied"); neither contains secrets.
func listenError(purpose, address string, err error) error {
	reason := "unknown error"
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		reason = opErr.Err.Error()
	}
	return fmt.Errorf("cannot bind %s %s: %s", purpose, address, reason)
}

func positiveIntFlag(name string, target *int) func(string) error {
	return func(raw string) error {
		value, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || value <= 0 || value > 1<<20 {
			return fmt.Errorf("%s must be a positive integer", name)
		}
		*target = value
		return nil
	}
}

// parseMemoryLimit accepts auto, off, or a byte size with the GOMEMLIMIT
// suffixes B, KiB, MiB, GiB and TiB.
func parseMemoryLimit(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	switch strings.ToLower(value) {
	case "auto", "":
		return 0, nil
	case "off":
		return -1, nil
	}
	multiplier := int64(1)
	for _, unit := range []struct {
		suffix string
		bytes  int64
	}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(value, unit.suffix) {
			value, multiplier = strings.TrimSuffix(value, unit.suffix), unit.bytes
			break
		}
	}
	number, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || number <= 0 || number > (1<<62)/multiplier || number*multiplier < 64<<20 {
		return 0, errors.New("memory-limit must be auto, off, or a size of at least 64MiB such as 3GiB")
	}
	return number * multiplier, nil
}
