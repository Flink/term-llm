package proxy

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/term-llm/term-llm/internal/sqliteutil"
)

const removeProxyV7 = `DROP TABLE claude_shared_enrollment; `

// Remove version 6 objects before reconstructing older fixtures from live stores.
const removeProxyV6 = `DROP INDEX callers_external_id; DROP TABLE admin_keys;
ALTER TABLE callers DROP COLUMN external_id; ALTER TABLE callers DROP COLUMN created_by; `

// Released v5 shape: authority-bound grants and a native vault, but no API keys
// or provisioning metadata. Seed rows against this schema, not the latest one.
const proxySchemaV5 = `
CREATE TABLE configuration (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL, data TEXT NOT NULL);
INSERT INTO configuration VALUES(1,0,'{"routes":[]}');
CREATE TABLE secrets (name TEXT PRIMARY KEY, origin TEXT NOT NULL, value TEXT NOT NULL);
CREATE TABLE callers (id TEXT PRIMARY KEY, name TEXT NOT NULL, hash TEXT UNIQUE NOT NULL, created INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE grants (caller TEXT NOT NULL, route TEXT NOT NULL, authority TEXT NOT NULL, PRIMARY KEY(caller,route));
CREATE TABLE requests (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT UNIQUE NOT NULL, started INTEGER NOT NULL, caller TEXT NOT NULL, route TEXT NOT NULL, method TEXT NOT NULL, path TEXT NOT NULL, outcome TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX requests_time ON requests(started,seq);
CREATE INDEX requests_caller ON requests(caller,seq);
CREATE INDEX requests_route ON requests(route,seq);
` + accountSchema + moneySchema + claudeVaultSchema + `PRAGMA user_version=5;`

func TestAdminKeySchemaV6Migration(t *testing.T) {
	ctx := context.Background()
	if err := sqliteutil.ValidateMigrations(migrations, 2, schemaVersion, true); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	st := &store{db: db}
	t.Cleanup(func() { _ = st.close() })
	execProxySQL(t, st, proxySchemaV5)
	execProxySQL(t, st, "INSERT INTO callers(id,name,hash,created) VALUES('legacy','Legacy',?,1); INSERT INTO grants VALUES('legacy','route','exact');", hashToken("sk-tlp-synthetic-legacy"))
	before, err := sqliteutil.SchemaSignature(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("synthetic version 6 callback failure")
	err = sqliteutil.WithImmediateMigrationTx(ctx, db, func(tx sqliteutil.Executor) error {
		if err := migrations[4].Up(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version=6"); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("callback did not fail: %v", err)
	}
	after, err := sqliteutil.SchemaSignature(ctx, db)
	if err != nil || !reflect.DeepEqual(after, before) || proxyCount(t, st, "PRAGMA user_version") != 5 {
		t.Fatalf("migration rollback changed v5: %v", err)
	}
	if err := st.initialize(); err != nil {
		t.Fatal(err)
	}
	if proxyCount(t, st, "PRAGMA user_version") != schemaVersion {
		t.Fatal("migration retry did not publish marker")
	}
	if err := st.prepareStatements(); err != nil {
		t.Fatal(err)
	}
	original, err := st.authorize("sk-tlp-synthetic-legacy", activeRoute{Route: Route{Name: "route"}, authority: "exact"})
	if err != nil || original.ID != "legacy" {
		t.Fatalf("legacy token lost: %v", err)
	}
	if _, err := st.authorize("sk-tlp-synthetic-legacy", activeRoute{Route: Route{Name: "route"}, authority: "other"}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("grant lost exact binding: %v", err)
	}
	cs, err := st.callers()
	if err != nil || len(cs) != 1 || cs[0].Name != "Legacy" || cs[0].CreatedBy != "" || cs[0].ExternalID != "" || !reflect.DeepEqual(cs[0].Grants, []string{"route"}) {
		t.Fatalf("legacy callers: %+v, %v", cs, err)
	}
	fresh := testService(t)
	want, err := sqliteutil.SchemaSignature(ctx, fresh.store.db)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sqliteutil.SchemaSignature(ctx, db)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh and migrated schema differ: %v\ngot %v\nwant %v", err, got, want)
	}
}

func execProxySQL(t *testing.T, st *store, query string, args ...any) {
	t.Helper()
	if _, err := st.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func proxyCount(t *testing.T, st *store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
