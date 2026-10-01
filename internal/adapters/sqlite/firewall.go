package sqlite

import (
	"context"
	"database/sql"
	"net/netip"

	"codex-lb/internal/domain"
)

const schemaV12 = `CREATE TABLE firewall_allowlist (ip_address TEXT PRIMARY KEY,created_at INTEGER NOT NULL);`

func (s *Store) ListFirewallEntries(ctx context.Context) ([]domain.FirewallEntry, error) {
	rows, err := s.readDB.QueryContext(ctx, "SELECT ip_address,created_at FROM firewall_allowlist ORDER BY created_at,ip_address")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []domain.FirewallEntry{}
	for rows.Next() {
		var e domain.FirewallEntry
		var at int64
		if err := rows.Scan(&e.IPAddress, &at); err != nil {
			return nil, err
		}
		e.CreatedAt = fromMillis(at)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (s *Store) AddFirewallEntry(ctx context.Context, e domain.FirewallEntry) error {
	ip, err := netip.ParseAddr(e.IPAddress)
	if err != nil || ip.Zone() != "" || ip.Unmap().String() != e.IPAddress || e.CreatedAt.IsZero() {
		return ErrInvalid
	}
	res, err := s.db.ExecContext(ctx, "INSERT INTO firewall_allowlist(ip_address,created_at) VALUES(?,?) ON CONFLICT DO NOTHING", e.IPAddress, millis(e.CreatedAt))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrConflict
	}
	return err
}

func (s *Store) DeleteFirewallEntry(ctx context.Context, ip string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM firewall_allowlist WHERE ip_address=?", ip)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func importLegacyFirewall(ctx context.Context, src, dst *sql.Tx, _ LegacyVault) error {
	var exists int
	if err := src.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name='api_firewall_allowlist' AND type='table'").Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return nil
	}
	rows, err := src.QueryContext(ctx, "SELECT ip_address,created_at FROM api_firewall_allowlist")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var created any
		if err := rows.Scan(&raw, &created); err != nil {
			return err
		}
		ip, err := netip.ParseAddr(raw)
		if err != nil || ip.Zone() != "" {
			return ErrInvalid
		}
		at, err := parseLegacyTime(created)
		if err != nil {
			return err
		}
		if _, err := dst.ExecContext(ctx, "INSERT INTO firewall_allowlist(ip_address,created_at) VALUES(?,?) ON CONFLICT DO NOTHING", ip.Unmap().String(), millis(at)); err != nil {
			return err
		}
	}
	return rows.Err()
}
