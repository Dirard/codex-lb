package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"codex-lb/internal/adapters/credentials"
)

func importLegacy(ctx context.Context, cfg config, output io.Writer) error {
	vault, err := credentials.Open(cfg.sourceKey, false)
	if err != nil {
		return errors.New("cannot validate the private source encryption key")
	}
	destination := cfg.dataDir
	if cfg.dryRun {
		destination, err = os.MkdirTemp("", "codex-lb-import-*")
		if err != nil {
			return errors.New("cannot create private dry-run directory")
		}
		defer os.RemoveAll(destination)
	} else {
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return errors.New("cannot create destination parent directory")
		}
		// No merging or overwriting another installation, even an empty directory.
		if err := os.Mkdir(destination, 0700); err != nil {
			return errors.New("import destination must be a new directory; existing files are never overwritten")
		}
	}
	if err := copyImportKey(cfg.sourceKey, filepath.Join(destination, "encryption.key"), vault.Fingerprint()); err != nil {
		return err
	}
	data, err := openData(ctx, destination)
	if err != nil {
		return err
	}
	report, importErr := data.store.ImportLegacySnapshot(ctx, cfg.source, data.vault)
	if importErr == nil {
		importErr = data.store.ValidateRuntime(ctx, data.vault)
	}
	closeErr := data.close()
	if importErr != nil {
		return errors.New("legacy import failed validation; source unchanged, destination retained for inspection (temporary dry-run data removed)")
	}
	if closeErr != nil {
		return errors.New("could not close imported database cleanly; do not start it before inspection")
	}
	return json.NewEncoder(output).Encode(struct {
		DryRun                bool  `json:"dryRun"`
		Accounts              int64 `json:"accounts"`
		ModelSources          int64 `json:"modelSources"`
		Groups                int64 `json:"groups"`
		Keys                  int64 `json:"keys"`
		RequestLogs           int64 `json:"requestLogs"`
		UnsettledReservations int64 `json:"unsettledReservations"`
		ActiveProxyBindings   int64 `json:"activeProxyBindings"`
	}{cfg.dryRun, report.Accounts, report.ModelSources, report.Groups, report.Keys, report.RequestLogs, report.UnsettledReservations, report.ActiveProxyBindings})
}

func copyImportKey(source, target, fingerprint string) error {
	input, err := os.Open(source)
	if err != nil {
		return errors.New("cannot reopen source encryption key")
	}
	raw, err := io.ReadAll(io.LimitReader(input, 4097))
	input.Close()
	if err != nil || len(raw) > 4096 || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != fingerprint {
		return errors.New("source encryption key changed during import")
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return errors.New("cannot create destination encryption key")
	}
	_, writeErr := output.Write(raw)
	err = errors.Join(writeErr, output.Sync(), output.Close())
	if err != nil {
		return errors.New("cannot persist destination encryption key")
	}
	return nil
}
