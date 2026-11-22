package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/term-llm/term-llm/internal/app/proxy/legacyconfig"
	"github.com/term-llm/term-llm/internal/sqliteutil"
)

const schemaVersion = 11

// SQLite secondary indexes already carry the rowid (seq) as a tie-breaker.
const requestFilterIndexesV10 = `
CREATE INDEX requests_method ON requests(method);
CREATE INDEX requests_outcome ON requests(outcome);
CREATE INDEX requests_paths ON requests(
 (CASE WHEN substr(path,1,1)='/' THEN path ELSE '/'||path END),
 (COALESCE(CASE WHEN json_valid(data) THEN json_extract(data,'$.local_prefix') END,'')||ltrim(path,'/'))
);
`

const emptyProviderConfig = `{"routes":[],"providers":{}}`

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
}}, {Version: 6, Description: "admin API keys and caller provisioning metadata", Up: migrateAdminKeys}, {Version: 7, Description: "shared Claude login enrollment", Up: func(tx sqliteutil.Executor) error {
	_, err := tx.Exec(claudeSharedSchema)
	return err
}}, {Version: 8, Description: "account-owned native subscription credentials", Up: migrateSharedNativeAccounts},
	{Version: 9, Description: "stored provider families and unbound secrets", Up: func(tx sqliteutil.Executor) error {
		return migrateProviderFamilies(tx, nil)
	}},
	{Version: 10, Description: "index request method, outcome and path filters", Up: func(tx sqliteutil.Executor) error {
		_, err := tx.Exec(requestFilterIndexesV10)
		return err
	}},
	{Version: 11, Description: "shorten built-in provider display labels", Up: migrateProviderLabelsV11},
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

func (s *store) initialize(fileConfig ...*Config) error {
	var file *Config
	if len(fileConfig) > 0 {
		file = fileConfig[0]
	}
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
			up := migration.Up
			if file != nil && migration.Version == 9 {
				up = func(tx sqliteutil.Executor) error { return migrateProviderFamilies(tx, file) }
			}
			if err := up(tx); err != nil {
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

// Never promote one client's credentials into a login for other clients.
// Keep every route and grant; an operator must reconnect each converted slot.
func migrateSharedNativeAccounts(tx sqliteutil.Executor) error {
	if _, err := tx.Exec(`UPDATE accounts SET state='needs_login', generation=generation+1, expires=0,
		data=json_set(json_remove(data,'$.claude_shared_cipher','$.refresh'),
			'$.credentials',json_object('mode','shared_vault'), '$.claude_shared_id','ns_'||lower(hex(randomblob(32))),
			'$.identity_version',coalesce(json_extract(data,'$.identity_version'),0)+1)
		WHERE kind IN ('claude-code','agy-bin') AND
			(coalesce(json_extract(data,'$.credentials.mode'),'')!='shared_vault' OR coalesce(json_extract(data,'$.claude_shared_id'),'')='')`); err != nil {
		return err
	}
	_, err := tx.Exec("DROP TABLE claude_vault")
	return err
}

// decodeCurrentConfig rejects legacy YAML before the strict decoder reports
// unknown fields. The size bound also applies to the lenient pre-parse.
func decodeCurrentConfig(data []byte) (Config, error) {
	if len(data) <= maxConfig {
		if err := legacyconfig.CheckLegacyFile(data); err != nil {
			return Config{}, err
		}
	}
	return decodeConfig(data)
}

func migrateProviderFamilies(tx sqliteutil.Executor, file *Config) error {
	var data string
	var rename bool
	var err error
	if file == nil {
		data, rename, err = migrateStoredProxyConfig(tx)
	} else {
		data = emptyProviderConfig
		rename, err = checkFileProxyUpgrade(tx, *file)
	}
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE secrets_v9 (name TEXT PRIMARY KEY, value TEXT NOT NULL);
		INSERT INTO secrets_v9 (name,value) SELECT name,value FROM secrets;
		DROP TABLE secrets;
		ALTER TABLE secrets_v9 RENAME TO secrets;`); err != nil {
		return fmt.Errorf("rebuild secrets: %w", err)
	}
	if _, err := tx.Exec("UPDATE configuration SET data=? WHERE id=1", data); err != nil {
		return fmt.Errorf("write stored provider configuration: %w", err)
	}
	if _, err := tx.Exec("UPDATE accounts SET kind='claude-bin' WHERE kind='claude-code'"); err != nil {
		return fmt.Errorf("rename Claude account kind: %w", err)
	}
	if rename {
		return renameLiveClaudeRoutes(tx)
	}
	return nil
}

func migrateStoredProxyConfig(tx sqliteutil.Executor) (string, bool, error) {
	var old string
	if err := tx.QueryRow("SELECT data FROM configuration WHERE id=1").Scan(&old); err != nil {
		return "", false, fmt.Errorf("read v8 configuration: %w", err)
	}
	kinds, err := migrationAccountKinds(tx)
	if err != nil {
		return "", false, err
	}
	data, renames, err := legacyconfig.Migrate([]byte(old), kinds)
	if err != nil {
		return "", false, err
	}
	var c Config
	if json.Unmarshal(data, &c) != nil {
		return "", false, fmt.Errorf("could not decode migrated configuration; check the stored route and provider fields")
	}
	if err := validateConfigStructure(c); err != nil {
		return "", false, fmt.Errorf("invalid migrated configuration; fix the configuration with the previous build: %w", err)
	}
	return string(data), renames.ClaudeRoute, nil
}

func migrationAccountKinds(tx sqliteutil.Executor) (map[string]string, error) {
	rows, err := tx.Query("SELECT name,kind FROM accounts")
	if err != nil {
		return nil, fmt.Errorf("read v8 account kinds: %w", err)
	}
	defer rows.Close()
	kinds := map[string]string{}
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			return nil, fmt.Errorf("read v8 account kind: %w", err)
		}
		kinds[name] = kind
	}
	return kinds, rows.Err()
}

func checkFileProxyUpgrade(tx sqliteutil.Executor, file Config) (bool, error) {
	var old, canonical bool
	for _, r := range file.Routes {
		old = old || r.Name == "claude-code"
		canonical = canonical || r.Name == "claude-bin" && r.Profile == "claude-bin"
		if err := legacyconfig.CheckLegacyAllow(r.Name, r.Allow); err != nil {
			return false, err
		}
		for _, rule := range r.Rules {
			if err := legacyconfig.CheckLegacyAllow(r.Name, rule.Allow); err != nil {
				return false, err
			}
		}
	}
	if old || !canonical {
		return false, nil
	}
	var refs bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM grants WHERE route='claude-code')
		OR EXISTS(SELECT 1 FROM claude_shared_enrollment WHERE route='claude-code')`).Scan(&refs)
	return refs, err
}

func renameLiveClaudeRoutes(tx sqliteutil.Executor) error {
	if _, err := tx.Exec("UPDATE grants SET route='claude-bin' WHERE route='claude-code'"); err != nil {
		return fmt.Errorf("could not move grants from claude-code to claude-bin; remove any conflicting grants before upgrading: %w", err)
	}
	if _, err := tx.Exec("UPDATE claude_shared_enrollment SET route='claude-bin' WHERE route='claude-code'"); err != nil {
		return fmt.Errorf("move Claude enrolments to claude-bin: %w", err)
	}
	var old string
	if err := tx.QueryRow("SELECT data FROM money_settings WHERE id=1").Scan(&old); err != nil {
		return fmt.Errorf("read money settings: %w", err)
	}
	data, err := legacyconfig.RenameClaudeMoneyRates([]byte(old))
	if err != nil {
		return err
	}
	if string(data) == old {
		return nil
	}
	if _, err := tx.Exec("UPDATE money_settings SET data=? WHERE id=1", string(data)); err != nil {
		return fmt.Errorf("rename Claude money rates: %w", err)
	}
	return nil
}

func migrateProviderLabelsV11(tx sqliteutil.Executor) error {
	var data string
	if err := tx.QueryRow("SELECT data FROM configuration WHERE id=1").Scan(&data); err != nil {
		return fmt.Errorf("read stored provider labels: %w", err)
	}
	patch, err := legacyconfig.ProviderLabelPatchV11([]byte(data))
	if err != nil || patch == nil {
		return err
	}
	if _, err := tx.Exec("UPDATE configuration SET data=json_patch(data, ?) WHERE id=1", string(patch)); err != nil {
		return fmt.Errorf("write provider label updates: %w", err)
	}
	return nil
}
