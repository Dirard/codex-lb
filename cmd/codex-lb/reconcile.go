package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"codex-lb/internal/domain"
)

// reconcileUsage never guesses an interrupted provider's usage. It operates
// offline under the same inode lock as serve and uses ordinary atomic settlement.
func reconcileUsage(ctx context.Context, cfg config, output io.Writer) error {
	if info, err := os.Lstat(filepath.Join(cfg.dataDir, "codex-lb.sqlite3")); err != nil || !info.Mode().IsRegular() {
		return errors.New("reconciliation requires an existing Go database")
	}
	data, err := openData(ctx, cfg.dataDir)
	if err != nil {
		return err
	}
	defer data.close()
	if _, err := data.store.MarkInterruptedReservations(ctx); err != nil {
		return errors.New("cannot mark interrupted reservations for reconciliation")
	}
	if cfg.reservation == "" {
		items, err := data.store.ListReservationsNeedingReconciliation(ctx, cfg.afterReservation, 1000)
		if err != nil {
			return errors.New("cannot list interrupted reservations")
		}
		if items == nil {
			items = []domain.Reservation{}
		}
		next := ""
		if len(items) == 1000 {
			next = items[len(items)-1].ID
		}
		return json.NewEncoder(output).Encode(struct {
			Items []domain.Reservation `json:"items"`
			Next  string               `json:"nextAfter,omitempty"`
		}{items, next})
	}
	reservation, err := data.store.GetReservation(ctx, cfg.reservation)
	if err != nil {
		return errors.New("reservation not found")
	}
	if reservation.Status != "reserved" {
		return json.NewEncoder(output).Encode(struct {
			Status string `json:"alreadySettled"`
		}{reservation.Status})
	}
	settlement := domain.UsageSettlement{Status: "released"}
	if !cfg.releaseReservation {
		settlement, err = readUsageSettlement(cfg.settlement, reservation)
		if err != nil {
			return err
		}
		if settlement.Event.AccountID != "" {
			account, err := data.store.GetAccount(ctx, settlement.Event.AccountID)
			if err != nil {
				return errors.New("settlement account not found")
			}
			settlement.Event.PlanType, settlement.Event.Source = account.PlanType, string(account.Kind)
			if account.Kind == domain.AccountExternal {
				settlement.Event.ModelSourceID = account.ID
			}
		}
	}
	applied, err := data.store.SettleUsage(ctx, reservation.ID, settlement)
	if err != nil {
		return errors.New("usage reconciliation failed; reserved budget is retained")
	}
	return json.NewEncoder(output).Encode(struct {
		Applied bool `json:"applied"`
	}{applied})
}

func readUsageSettlement(path string, reservation domain.Reservation) (domain.UsageSettlement, error) {
	var input struct {
		Status      string              `json:"status"`
		Usage       *domain.UsageAmount `json:"usage"`
		AccountID   string              `json:"accountId"`
		ServiceTier string              `json:"serviceTier"`
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Size() > 65536 {
		return domain.UsageSettlement{}, errors.New("settlement must be a regular JSON file of at most 64 KiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return domain.UsageSettlement{}, errors.New("cannot read settlement file")
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(body) > 65536 {
		return domain.UsageSettlement{}, errors.New("settlement file cannot be read within its size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Usage == nil ||
		(input.Status != "success" && input.Status != "error") || input.Usage.Validate() != nil ||
		len(input.ServiceTier) > 128 || len(input.AccountID) > 256 {
		return domain.UsageSettlement{}, errors.New("settlement requires one JSON object: status success/error and confirmed nonnegative usage")
	}
	if reservation.AccountID != "" {
		if input.AccountID != "" && input.AccountID != reservation.AccountID {
			return domain.UsageSettlement{}, errors.New("settlement cannot change reservation owner")
		}
		input.AccountID = reservation.AccountID
	}
	digest := sha256.Sum256([]byte(reservation.ID))
	event := domain.UsageEvent{RequestID: "reconcile_" + hex.EncodeToString(digest[:]),
		APIKeyID: reservation.APIKeyID, AccountID: input.AccountID, Model: reservation.Model,
		RequestedAt: reservation.CreatedAt, RequestKind: "reconciled", Status: input.Status,
		ServiceTier: input.ServiceTier, Usage: *input.Usage}
	status := "finalized"
	if input.Status == "error" {
		status, event.ErrorCode = "failed", "interrupted_request_reconciled"
	}
	return domain.UsageSettlement{Status: status, Event: event}, nil
}
