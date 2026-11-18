package proxy

import (
	"context"
	"fmt"
	"log"

	"github.com/term-llm/term-llm/internal/sqliteutil"
)

const schemaVersion = 6

const adminKeySchemaV6 = `
CREATE UNIQUE INDEX callers_external_id ON callers(external_id) WHERE external_id != '' AND revoked = 0;
CREATE TABLE admin_keys (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, hash TEXT UNIQUE NOT NULL,
 created INTEGER NOT NULL, last_used INTEGER NOT NULL DEFAULT 0
);`

// This frozen schema is retained only for the version-4 migration. Native
// login replaced browser pairing in version 5, which drops these tables.
const legacyConnectSchemaV4 = `
CREATE TABLE connect_requests (
 id TEXT PRIMARY KEY, secret_hash TEXT NOT NULL, peer TEXT NOT NULL,
 status TEXT NOT NULL, expires INTEGER NOT NULL, retained_until INTEGER NOT NULL,
 result_expires INTEGER NOT NULL DEFAULT 0, data TEXT NOT NULL
);
CREATE INDEX connect_requests_peer ON connect_requests(peer,status,expires);
CREATE TABLE connect_managed (
 account TEXT PRIMARY KEY, route TEXT UNIQUE NOT NULL, prefix TEXT UNIQUE NOT NULL,
 authority TEXT NOT NULL, spec TEXT NOT NULL, request_id TEXT NOT NULL
);`

// Version 1 was the initial acceptance build, not a released schema. Its grants
// knew only an origin, so their original delegated base/key scope cannot be
// reconstructed safely. Keep callers/tokens/history, but require explicit regrant.
var migrations = []sqliteutil.Migration{{
	Version: 2, Description: "bind caller grants to exact route authority",
	Up: func(tx sqliteutil.Executor) error {
		if _, err := tx.Exec("ALTER TABLE grants RENAME COLUMN origin TO authority"); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM grants")
		return err
	},
}, {Version: 3, Description: "durable renewable account custody", Up: func(tx sqliteutil.Executor) error {
	if _, err := tx.Exec(accountSchema); err != nil {
		return err
	}
	return initializeMoneySchema(tx)
}}, {Version: 4, Description: "bounded fresh-approved Claude connect receipts", Up: func(tx sqliteutil.Executor) error { _, err := tx.Exec(legacyConnectSchemaV4); return err }}, {Version: 5, Description: "native Claude token vault replaces browser pairing", Up: func(tx sqliteutil.Executor) error {
	if _, err := tx.Exec("DROP TABLE connect_requests; DROP TABLE connect_managed;"); err != nil {
		return err
	}
	_, err := tx.Exec(claudeVaultSchema)
	return err
}}, {Version: 6, Description: "admin API keys and caller provisioning metadata", Up: migrateAdminKeys},
}

func migrateAdminKeys(tx sqliteutil.Executor) error {
	for _, column := range []struct{ name, alter string }{
		{"created_by", "ALTER TABLE callers ADD COLUMN created_by TEXT NOT NULL DEFAULT ''"},
		{"external_id", "ALTER TABLE callers ADD COLUMN external_id TEXT NOT NULL DEFAULT ''"},
	} {
		present, err := sqliteutil.ColumnExists(tx, "callers", column.name)
		if err != nil {
			return err
		}
		if !present {
			if _, err := tx.Exec(column.alter); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec(adminKeySchemaV6)
	return err
}

func (s *store) initialize() error {
	var invalidated int
	err := sqliteutil.WithImmediateMigrationTx(context.Background(), s.db, func(tx sqliteutil.Executor) error {
		var v int
		if err := tx.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
			return err
		}
		if v == schemaVersion {
			return nil
		}
		if v < 0 || v > schemaVersion {
			return fmt.Errorf("proxy schema version %d is not supported (maximum %d)", v, schemaVersion)
		}
		if v == 0 {
			return createSchema(tx)
		}
		if v < 2 {
			if err := tx.QueryRow("SELECT count(*) FROM grants").Scan(&invalidated); err != nil {
				return err
			}
		}
		for _, migration := range migrations {
			if migration.Version <= v {
				continue
			}
			if err := migration.Up(tx); err != nil {
				return fmt.Errorf("proxy migration %d (%s): %w", migration.Version, migration.Description, err)
			}
			if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", migration.Version)); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && invalidated > 0 {
		log.Printf("proxy: schema upgrade removed %d origin-only grants; client tokens remain valid after explicit regrant in Clients", invalidated)
	}
	return err
}
func createSchema(tx sqliteutil.Executor) error {
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Scan(&n); err != nil {
		return err
	}
	if n != 0 {
		return fmt.Errorf("unknown unversioned proxy schema")
	}
	if _, err := tx.Exec(schema); err != nil {
		return err
	}
	return nil
}
