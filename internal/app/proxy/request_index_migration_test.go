package proxy

import (
	"context"
	"database/sql"
	"reflect"
	"testing"

	"github.com/term-llm/term-llm/internal/sqliteutil"
)

const removeProxyV10 = `DROP INDEX requests_method; DROP INDEX requests_outcome; DROP INDEX requests_paths; `

// Released v9 used the v8 schema except for the provider configuration seed
// and the removal of the origin field from stored secrets.
func createProxyV9Fixture(t *testing.T, st *store) {
	t.Helper()
	execProxySQL(t, st, proxySchemaV8)
	execProxySQL(t, st, `ALTER TABLE secrets DROP COLUMN origin;
UPDATE configuration SET data=?; PRAGMA user_version=9`, emptyProviderConfig)
}

func TestProxyV10RequestIndexMigration(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	st := &store{db: db}
	createProxyV9Fixture(t, st)
	execProxySQL(t, st, `INSERT INTO requests VALUES(12,'req',10,'client','provider','GET','/models','failed','{"id":"req"}');
INSERT INTO requests VALUES(13,'corrupt',11,'client','provider','GET','/invalid','failed','not JSON');
INSERT INTO money_history VALUES('req',10,'client','provider','{}')`)
	snapshot := func() string {
		return claudeSetupDBState(t, &Service{store: st}, "requests", "money_history", "configuration", "secrets")
	}
	before := snapshot()
	// Force the second CREATE INDEX to fail. The first index and new marker
	// must not be published; removing the obstruction permits a normal retry.
	execProxySQL(t, st, `CREATE INDEX requests_outcome ON requests(path)`)
	if err := st.initialize(); err == nil {
		t.Fatal("expected a migration conflict")
	}
	if proxyCount(t, st, "PRAGMA user_version") != 9 || proxyCount(t, st, "SELECT count(*) FROM sqlite_schema WHERE name='requests_method'") != 0 || snapshot() != before {
		t.Fatal("failed migration changed the v9 store")
	}
	execProxySQL(t, st, `DROP INDEX requests_outcome`)
	if err := st.initialize(); err != nil {
		t.Fatal(err)
	}
	if proxyCount(t, st, "PRAGMA user_version") != schemaVersion || snapshot() != before {
		t.Fatal("upgrade did not publish the current marker or changed data")
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
