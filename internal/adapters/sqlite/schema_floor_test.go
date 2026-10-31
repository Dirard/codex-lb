package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

type schemaFingerprint struct {
	version, floor int
	schema         string
}

// readSchemaFingerprint reads the schema state of a closed database file
// without writing to it.
func readSchemaFingerprint(t *testing.T, path string) schemaFingerprint {
	t.Helper()
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state schemaFingerprint
	if err := db.QueryRow("PRAGMA user_version").Scan(&state.version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT coalesce((SELECT reader_floor FROM schema_compatibility WHERE id=1),-1)`).Scan(&state.floor); err != nil {
		state.floor = -2 // no compatibility table
	}
	if err := db.QueryRow(`SELECT group_concat(type||':'||name||':'||coalesce(sql,''),';') FROM
 (SELECT type,name,sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name)`).Scan(&state.schema); err != nil {
		t.Fatal(err)
	}
	return state
}

// newerDatabase returns a closed database migrated by a simulated newer
// release: three more migrations, one new table and the given recorded floor.
func newerDatabase(t *testing.T, floor int) string {
	t.Helper()
	store, path := testStore(t)
	saveTestAccount(t, store, "acct")
	if err := store.SaveAPIKey(context.Background(), testKey("key", nil), fixedTime); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"CREATE TABLE future_feature(id INTEGER PRIMARY KEY)",
		fmt.Sprintf("UPDATE schema_compatibility SET reader_floor=%d WHERE id=1", floor),
		fmt.Sprintf("PRAGMA user_version=%d", SchemaVersion()+3),
	} {
		if _, err := store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSchemaFloorClassifiesMigrations41To46AsAdditive(t *testing.T) {
	if SchemaVersion() != 46 || !additiveMigrations[46] || SchemaFloor() != 40 || !additiveMigrations[41] || !additiveMigrations[42] || !additiveMigrations[43] || !additiveMigrations[44] || !additiveMigrations[45] {
		t.Fatalf("schema %d floor %d", SchemaVersion(), SchemaFloor())
	}
	store, path := testStore(t)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if state := readSchemaFingerprint(t, path); state.version != 46 || state.floor != 40 {
		t.Fatalf("fresh database records %+v", state)
	}
	// Reopening at the current schema applies nothing and keeps the floor.
	before := readSchemaFingerprint(t, path)
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if after := readSchemaFingerprint(t, path); after != before {
		t.Fatalf("reopen changed schema: %+v -> %+v", before, after)
	}
}

// Schema 40 reservations gain no owner and therefore count as not alive.
func TestMigrationFrom40RecordsFloorAndLeavesReservationsUnowned(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v40.sqlite")
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: path, RawQuery: "_synchronous=0"}).String())
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:40] {
		if _, err := db.ExecContext(ctx, migration); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO accounts(id,kind,provider,email,status,created_at) VALUES('acct','chatgpt','openai','a@example.invalid','active',0)`,
		`INSERT INTO api_keys(id,name,key_hash,key_prefix,created_at) VALUES('key','key','hash','sk',0)`,
		`INSERT INTO usage_reservations(id,api_key_id,account_id,model,status,created_at,updated_at) VALUES('v40','key','acct','gpt-test','reserved',0,0)`,
		fmt.Sprintf("PRAGMA application_id=%d", applicationID), "PRAGMA user_version=40",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := store.readDB.QueryRowContext(ctx, `SELECT owner_instance FROM usage_reservations WHERE id='v40'`).Scan(&owner); err != nil || owner != "" {
		t.Fatalf("schema 40 reservation owner %q: %v", owner, err)
	}
	if count, err := store.MarkInterruptedReservations(ctx, []string{store.InstanceID()}); err != nil || count != 1 {
		t.Fatalf("unowned reservation not interrupted: %d %v", count, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if state := readSchemaFingerprint(t, path); state.version != SchemaVersion() || state.floor != 40 {
		t.Fatalf("migrated database records %+v", state)
	}
}

// A newer release's database whose floor admits this schema opens without
// any schema change, keeps WAL, and serves ordinary writes.
func TestNewerDatabaseWithinFloorOpensUnchanged(t *testing.T) {
	ctx := context.Background()
	for _, floor := range []int{SchemaFloor(), SchemaVersion()} {
		t.Run(fmt.Sprint(floor), func(t *testing.T) {
			path := newerDatabase(t, floor)
			before := readSchemaFingerprint(t, path)
			store, err := Open(path)
			if err != nil {
				t.Fatalf("compatible newer database refused: %v", err)
			}
			var journal string
			if err := store.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
				t.Fatalf("journal=%s error=%v", journal, err)
			}
			if _, err := store.ReserveUsage(ctx, domain.ReservationRequest{ID: "newer", APIKeyID: "key", AccountID: "acct",
				Model: "gpt-test", Now: fixedTime.Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if after := readSchemaFingerprint(t, path); after != before {
				t.Fatalf("newer database modified: %+v -> %+v", before, after)
			}
		})
	}
}

func TestNewerDatabaseAboveFloorOrWithoutFloorIsRefused(t *testing.T) {
	for name, prepare := range map[string]func(*sql.DB) error{
		"floor above schema": func(db *sql.DB) error {
			_, err := db.Exec(fmt.Sprintf("UPDATE schema_compatibility SET reader_floor=%d", SchemaVersion()+1))
			return err
		},
		"no floor row": func(db *sql.DB) error {
			_, err := db.Exec("DELETE FROM schema_compatibility")
			return err
		},
		"no floor table": func(db *sql.DB) error {
			_, err := db.Exec("DROP TABLE schema_compatibility")
			return err
		},
		"foreign application": func(db *sql.DB) error {
			_, err := db.Exec("PRAGMA application_id=1")
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := newerDatabase(t, SchemaFloor())
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if err := prepare(db); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := readSchemaFingerprint(t, path)
			if store, err := Open(path); !errors.Is(err, ErrInvalid) {
				if store != nil {
					_ = store.Close()
				}
				t.Fatalf("incompatible newer database opened: %v", err)
			}
			if after := readSchemaFingerprint(t, path); after != before {
				t.Fatalf("refused database modified: %+v -> %+v", before, after)
			}
		})
	}
}

// Online backups of a compatible newer database work from this schema too;
// the launcher or a rolled-back worker may be the older release.
func TestSnapshotOfNewerDatabaseFollowsItsFloor(t *testing.T) {
	ctx := context.Background()
	store, vault, dir := backupTestStore(t)
	if _, err := store.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", SchemaVersion()+2)); err != nil {
		t.Fatal(err)
	}
	if err := WriteSnapshot(ctx, store.filePath, filepath.Join(dir, "compatible.sqlite3"), vault); err != nil {
		t.Fatalf("compatible newer snapshot refused: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, fmt.Sprintf("UPDATE schema_compatibility SET reader_floor=%d", SchemaVersion()+1)); err != nil {
		t.Fatal(err)
	}
	if err := WriteSnapshot(ctx, store.filePath, filepath.Join(dir, "incompatible.sqlite3"), vault); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incompatible newer snapshot: %v", err)
	}
}
