package proxy

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/term-llm/term-llm/internal/sqliteutil"
)

func TestNativeVaultSchema4Migration(t *testing.T) {
	if err := sqliteutil.ValidateMigrations(migrations, 2, schemaVersion, true); err != nil {
		t.Fatal(err)
	}
	s := testService(t)
	route := Route{Name: "existing", LocalPrefix: "/existing/", Upstream: "https://example.test", Enabled: true}
	configure(t, s, Config{Routes: []Route{route}}, nil)
	token := caller(t, s, route.Name)
	// Faithful version-4 schema: native vault did not exist; pairing receipts
	// and managed route records did. Ordinary clients and grants are unchanged.
	if _, err := s.store.db.Exec(removeProxyV6 + "DROP TABLE claude_vault; " + legacyConnectSchemaV4 + " PRAGMA user_version=4;"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec("INSERT INTO connect_requests VALUES('old','hash','peer','pending',1,2,0,'{}')"); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("synthetic failure after vault migration")
	err := sqliteutil.WithImmediateMigrationTx(context.Background(), s.store.db, func(tx sqliteutil.Executor) error {
		if err := migrations[3].Up(tx); err != nil {
			return err
		}
		if _, err := tx.Exec("PRAGMA user_version=5"); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	var version, receipts int
	if err := s.store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.store.db.QueryRow("SELECT count(*) FROM connect_requests").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if version != 4 || receipts != 1 {
		t.Fatal("migration must roll back its drops and marker", version, receipts)
	}
	if err := s.store.initialize(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.authorize(token, s.snap.routes[0]); err != nil {
		t.Fatal("ordinary caller grant lost during replacement", err)
	}
	fresh, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err := fresh.Exec(schema); err != nil {
		t.Fatal(err)
	}
	got, err := sqliteutil.SchemaSignature(context.Background(), s.store.db)
	if err != nil {
		t.Fatal(err)
	}
	want, err := sqliteutil.SchemaSignature(context.Background(), fresh)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("fresh/migrated native vault schema mismatch", err)
	}
}
