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
	if _, err := s.store.db.Exec(removeProxyV7 + removeProxyV6 + "" + legacyConnectSchemaV4 + " PRAGMA user_version=4;"); err != nil {
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

func TestNativeVaultSchema8DiscardsCallerLoginsAndPreservesGrants(t *testing.T) {
	s := testService(t)
	ctx := context.Background()
	// Start from the current canonical store but reconstruct the table and
	// account rows that a real v7 proxy would have on disk.
	execProxySQL(t, s.store, claudeVaultSchema+`PRAGMA user_version=7;`)
	// Build and grant the routes with current runtime kinds, then put the
	// historical Claude kind on disk before running the frozen v8 migration.
	kinds := []string{claudeProxyProvider.kind, agyProxyProvider.kind}
	var routes []Route
	for _, kind := range kinds {
		p := nativeProviderForKind(kind)
		name := "legacy-" + kind
		a := newNativeAccount(name, kind)
		a.State, a.ExpiresAt, a.Generation, a.identityVersion = "native_login", 2_000_000_000, 3, 5
		a.credentials.Mode = "vault"
		a.claudeSharedID = ""
		if err := s.store.replaceAccount(ctx, a, 0); err != nil {
			t.Fatal(err)
		}
		route := claudeSetupRoute(name, name)
		route.Upstream = "https://" + p.apiHost
		routes = append(routes, route)
	}
	shared, key := seedReadyClaudeShared(t, s, "already-shared", "existing-shared-access", "existing-shared-refresh")
	routes = append(routes, claudeSetupRoute(shared.Name, shared.Name))
	configure(t, s, Config{Routes: routes}, nil)
	grants := []string{routes[0].Name, routes[1].Name, routes[2].Name}
	client, token, err := s.store.createCaller("Existing client", grants, s.snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, ar := range s.snap.routes[:len(kinds)] {
		execProxySQL(t, s.store, `INSERT INTO claude_vault VALUES(?,?,?,?,?,5,1,?,?,'ready',2000000000,0,x'010203')`, ar.Name, client.ID, ar.Name, ar.authority, ar.Account, "access-"+ar.Name, "refresh-"+ar.Name)
	}
	execProxySQL(t, s.store, `INSERT INTO claude_shared_enrollment(account,caller,route,authority,expires) VALUES(?,?,?,?,2000000000)`, shared.Name, client.ID, routes[2].Name, s.snap.routes[2].authority)
	execProxySQL(t, s.store, `UPDATE accounts SET kind='claude-code' WHERE name IN (?,?)`, routes[0].Account, shared.Name)
	before := claudeSetupDBState(t, s, "configuration", "callers", "grants", "claude_shared_enrollment")

	// Test the frozen v8 boundary; v9 intentionally renames the kind and
	// transforms configuration in a separate migration.
	if err := sqliteutil.WithImmediateMigrationTx(ctx, s.store.db, func(tx sqliteutil.Executor) error {
		if err := migrateSharedNativeAccounts(tx); err != nil {
			return err
		}
		_, err := tx.Exec("PRAGMA user_version=8")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if proxyCount(t, s.store, "PRAGMA user_version") != 8 || proxyCount(t, s.store, "SELECT count(*) FROM sqlite_master WHERE name IN ('claude_vault','claude_vault_binding')") != 0 {
		t.Fatal("migration did not remove the per-caller vault schema")
	}
	if after := claudeSetupDBState(t, s, "configuration", "callers", "grants", "claude_shared_enrollment"); after != before {
		t.Fatal("migration changed configuration, caller grants, or existing enrollment")
	}
	for _, ar := range s.snap.routes {
		if _, err := s.store.authorize(token, ar); err != nil {
			t.Fatalf("preserved token lost route %s: %v", ar.Name, err)
		}
		a, err := s.store.account(ar.Account)
		if err != nil || a.credentials.Mode != claudeSharedMode || a.claudeSharedID == "" {
			t.Fatalf("account %s was not kept as a shared slot: %v", ar.Account, err)
		}
		if ar.Account != routes[1].Account && a.Kind != "claude-code" {
			t.Fatalf("v8 unexpectedly renamed the historical Claude kind to %q", a.Kind)
		}
		if a.Name == shared.Name {
			secret, err := claudeSharedSecret(key, a)
			if err != nil || a.State != "ready" || a.Generation != shared.Generation || a.claudeSharedID != shared.claudeSharedID || secret.Access != "existing-shared-access" || secret.Refresh != "existing-shared-refresh" {
				t.Fatal("migration modified an existing shared login", err)
			}
		} else if a.State != "needs_login" || a.ExpiresAt != 0 || a.Generation != 4 || a.identityVersion != 6 || len(a.claudeSharedCipher) != 0 {
			t.Fatalf("legacy account %s was not reset without importing caller credentials: %+v", ar.Account, a.AccountInfo)
		}
	}
}
