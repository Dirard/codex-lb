// codex-lb is a single-process proxy with an embedded administration UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // Schedules must not require a separate runtime timezone package.
)

var version = "go-dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		// Underlying provider/database errors can contain secrets. Runtime errors
		// carry safe context; do not print arbitrary wrapped error strings.
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	command := "serve"
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		command, args = args[0], args[1:]
	}
	if command == "version" && len(args) == 0 {
		_, err := fmt.Fprintln(stdout, version)
		return err
	}
	if command != "serve" && command != "import-legacy" && command != "reconcile-usage" {
		return errors.New("expected serve, import-legacy, reconcile-usage, or version")
	}
	cfg, err := parseConfig(command, args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if command == "import-legacy" {
		return importLegacy(ctx, cfg, stdout)
	}
	if command == "reconcile-usage" {
		return reconcileUsage(ctx, cfg, stdout)
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	runtime, err := openRuntime(ctx, cfg, logger)
	if err != nil {
		return err
	}
	return runtime.serve(ctx)
}
