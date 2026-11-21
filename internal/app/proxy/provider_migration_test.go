package proxy

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/sqliteutil"
)

// Released v8 on-disk shape, before adding providers or removing secret origins.
const proxySchemaV8 = `
CREATE TABLE configuration (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL, data TEXT NOT NULL);
INSERT INTO configuration VALUES(1,0,'{"routes":[]}');
CREATE TABLE secrets (name TEXT PRIMARY KEY, origin TEXT NOT NULL, value TEXT NOT NULL);
CREATE TABLE callers (id TEXT PRIMARY KEY, name TEXT NOT NULL, hash TEXT UNIQUE NOT NULL, created INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, created_by TEXT NOT NULL DEFAULT '', external_id TEXT NOT NULL DEFAULT '');
CREATE TABLE grants (caller TEXT NOT NULL, route TEXT NOT NULL, authority TEXT NOT NULL, PRIMARY KEY(caller,route));
CREATE TABLE requests (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT UNIQUE NOT NULL, started INTEGER NOT NULL, caller TEXT NOT NULL, route TEXT NOT NULL, method TEXT NOT NULL, path TEXT NOT NULL, outcome TEXT NOT NULL, data TEXT NOT NULL);
CREATE INDEX requests_time ON requests(started,seq);
CREATE INDEX requests_caller ON requests(caller,seq);
CREATE INDEX requests_route ON requests(route,seq);
` + accountSchema + moneySchema + adminKeySchemaV6 + claudeSharedSchema + `PRAGMA user_version=8;`

const legacyClaudeConfig = `{"routes":[
  {"name":"claude-code","local_prefix":"/claude-code/","upstream":"https://api.anthropic.com","account":"plan","enabled":true},
  {"name":"claude-code-2","local_prefix":"/alternate/","upstream":"https://api.anthropic.com","account":"same-vendor","enabled":true}
]}`

const convertedClaudeFile = `routes:
  - name: claude-bin
    local_prefix: /claude-bin/
    provider: claude-bin
    profile: claude-bin
    upstream: https://api.anthropic.com
    account: plan
providers:
  claude-bin: {label: Claude Code plan}
`

const oldClaudeRate = `{"rates":[{"provider":"claude-code","model":"synthetic","tier":"default","currency":"USD","source":"operator","asof":"2026-11-20","input_micros_per_million":1,"output_micros_per_million":2}]}`
const legacyProxyToken = "sk-tlp-fixture-only"
const migratedHistory = `{"route":"claude-code","provider":"claude-code"}`

func newProxyV8Fixture(t *testing.T, config string) (*store, Options) {
	t.Helper()
	root := t.TempDir()
	opts := Options{StateDir: filepath.Join(root, "state"), Auth: "token", AdminTokenFile: filepath.Join(root, "admin")}
	if err := os.Mkdir(opts.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(opts.AdminTokenFile, []byte(strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opts.StateDir, "proxy.db")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", sqliteutil.FileURI(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	s := &store{db: db}
	t.Cleanup(func() { _ = s.close() })
	execProxySQL(t, s, proxySchemaV8)
	execProxySQL(t, s, "UPDATE configuration SET revision=41,data=? WHERE id=1", config)
	execProxySQL(t, s, `INSERT INTO secrets VALUES('token','https://example.test:443','stored-private-1'),('other','https://alt.test:443','stored-private-2')`)
	return s, opts
}

func seedClaudeV8(t *testing.T, st *store) string {
	t.Helper()
	const dst = "https://api.anthropic.com"
	target, err := url.Parse(dst)
	if err != nil {
		t.Fatal(err)
	}
	authority := routeAuthority(Route{Account: "plan"}, dst+":443", target)
	const account = `{"credentials":{"mode":"shared_vault"},"claude_shared_id":"unchanged"}`
	execProxySQL(t, st, `INSERT INTO accounts VALUES('plan','claude-code','needs_login',1,0,1,?),
		('same-vendor','claude-code','needs_login',1,0,1,?)`, account, account)
	execProxySQL(t, st, "INSERT INTO callers(id,name,hash,created) VALUES('caller','Worker',?,1)", hashToken(legacyProxyToken))
	execProxySQL(t, st, `INSERT INTO grants VALUES('caller','claude-code',?)`, authority)
	execProxySQL(t, st, "INSERT INTO claude_shared_enrollment VALUES('plan','caller','claude-code',?,2000000000,1)", authority)
	execProxySQL(t, st, "UPDATE money_settings SET data=? WHERE id=1", oldClaudeRate)
	execProxySQL(t, st, "INSERT INTO requests(id,started,caller,route,method,path,outcome,data) VALUES('one',1,'caller','claude-code','POST','/v1/messages','ok',?)", migratedHistory)
	execProxySQL(t, st, "INSERT INTO money_history VALUES('one',1,'caller','claude-code',?)", migratedHistory)
	return authority
}

func openMigratedProxy(t *testing.T, opts Options) *Service {
	t.Helper()
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestProviderMigrationV9StoresIdentityAndPreservesClaudeGrant(t *testing.T) {
	st, opts := newProxyV8Fixture(t, legacyClaudeConfig)
	authority := seedClaudeV8(t, st)
	s := openMigratedProxy(t, opts)
	store := s.store
	if v := proxyCount(t, store, "PRAGMA user_version"); v != schemaVersion {
		t.Fatalf("user_version = %d", v)
	}
	if s.snap.revision != 42 || len(s.snap.routes) != 2 {
		t.Fatal("route count or revision changed during migration")
	}
	r := s.snap.routes[1]
	for _, candidate := range s.snap.routes {
		if candidate.Name == "claude-code-2" {
			r = candidate
		}
	}
	if r.Name != "claude-code-2" || r.Profile != "claude-bin" || r.LocalPrefix != "/alternate/" {
		t.Fatal("named Claude route should only gain a profile and provider")
	}
	assertMigratedClaudeGrant(t, s, authority)
	if n := proxyCount(t, store, "SELECT count(*) FROM accounts WHERE kind='claude-bin'"); n != 2 {
		t.Fatalf("expected both Claude accounts to be renamed, got %d", n)
	}
	for _, table := range []string{"requests", "money_history"} {
		var route, raw string
		if err := store.db.QueryRow("SELECT route,data FROM "+table).Scan(&route, &raw); err != nil || route != "claude-code" || raw != migratedHistory {
			t.Fatalf("migration changed %s history: %v", table, err)
		}
	}
	assertMigratedSchemaAndSecrets(t, store)
}

func assertMigratedClaudeGrant(t *testing.T, s *Service, authority string) {
	t.Helper()
	var canonical activeRoute
	for _, r := range s.snap.routes {
		if r.Name == "claude-bin" {
			canonical = r
		}
	}
	if canonical.Profile != "claude-bin" || canonical.Provider != "claude-bin" || canonical.LocalPrefix != "/claude-bin/" || canonical.authority != authority {
		t.Fatal("canonical Claude route changed authority or did not migrate")
	}
	if _, err := s.store.authorize(legacyProxyToken, canonical); err != nil {
		t.Fatal("original grant lost authorization", err)
	}
	old := canonical
	old.Name = "claude-code"
	if _, err := s.store.authorize(legacyProxyToken, old); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("grant was copied instead of moved", err)
	}
	var enrolledRoute, enrolledAuthority string
	var reserved int
	if err := s.store.db.QueryRow("SELECT route,authority,reserved FROM claude_shared_enrollment WHERE account='plan'").Scan(&enrolledRoute, &enrolledAuthority, &reserved); err != nil || enrolledRoute != "claude-bin" || enrolledAuthority != authority || reserved != 1 {
		t.Fatal("Claude enrolment did not keep its authority and reservation", err)
	}
	settings, err := s.store.loadMoneySettings(context.Background())
	if err != nil || len(settings.Rates) != 1 || settings.Rates[0].Provider != "claude-bin" || *settings.Rates[0].Output != 2 {
		t.Fatal("live Claude rate did not move", err)
	}
}

func assertMigratedSchemaAndSecrets(t *testing.T, st *store) {
	t.Helper()
	fresh, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if _, err := fresh.Exec(schema); err != nil {
		t.Fatal(err)
	}
	got, err := sqliteutil.SchemaSignature(context.Background(), st.db)
	if err != nil {
		t.Fatal(err)
	}
	want, err := sqliteutil.SchemaSignature(context.Background(), fresh)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("fresh and migrated schemas differ", err)
	}
	if n := proxyCount(t, st, "SELECT count(*) FROM pragma_table_info('secrets') WHERE name='origin'"); n != 0 {
		t.Fatal("origin column was retained")
	}
	secrets, err := st.materials()
	if err != nil || len(secrets) != 2 || secrets["token"].Value != "stored-private-1" || secrets["other"].Value != "stored-private-2" {
		t.Fatal("stored secrets did not survive", err)
	}
}

func TestProviderMigrationV9FilePrecheckAndStaleDB(t *testing.T) {
	st, opts := newProxyV8Fixture(t, `{"routes":[{"name":"stale","allow":["GET /*"]}],"providers":{"bad":{"model":"obsolete"}}}`)
	authority := seedClaudeV8(t, st)
	opts.ConfigFile = filepath.Join(filepath.Dir(opts.StateDir), "proxy.yml")
	if err := os.WriteFile(opts.ConfigFile, []byte(`routes:
  - name: legacy
    set_headers: {X-Private: {value: literal-private}}
secrets:
  token: {stored: token, origin: origin-private}
`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(opts)
	if s != nil {
		_ = s.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "legacy") || !strings.Contains(err.Error(), "secret origin binding was removed; delete this line") || strings.Contains(err.Error(), "private") {
		t.Fatalf("legacy file was not safely rejected: %v", err)
	}
	if n := proxyCount(t, st, "PRAGMA user_version"); n != 8 {
		t.Fatalf("legacy file reached SQLite upgrade, version=%d", n)
	}
	withStar := strings.Replace(convertedClaudeFile, "    account: plan\n", "    account: plan\n    allow: ['GET /v1/*']\n", 1)
	if err := os.WriteFile(opts.ConfigFile, []byte(withStar), 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Open(opts)
	if s != nil {
		_ = s.Close()
	}
	if err == nil || !strings.Contains(err.Error(), `route "claude-bin" contains a legacy '*' Allow entry`) || proxyCount(t, st, "PRAGMA user_version") != 8 {
		t.Fatalf("file mode did not roll back the Allow scan: %v", err)
	}
	if err := os.WriteFile(opts.ConfigFile, []byte(convertedClaudeFile), 0600); err != nil {
		t.Fatal(err)
	}
	s = openMigratedProxy(t, opts)
	assertMigratedClaudeGrant(t, s, authority)
	var raw string
	if err := s.store.db.QueryRow("SELECT data FROM configuration WHERE id=1").Scan(&raw); err != nil || raw != emptyProviderConfig {
		t.Fatal("file mode did not replace the stale database copy", err)
	}
}

func TestProviderMigrationV9RollsBack(t *testing.T) {
	for _, tc := range []struct{ name, config, want string }{
		{"legacy Allow", `{"routes":[{"name":"restricted","allow":["GET /v1/*"],"set_headers":{"X-Foo":{"value":"literal-private"}}}]}`, "restricted"},
		{"structural validation", `{"routes":[{"name":"restricted","allow":["BOGUS"]}]}`, "restricted"},
		{"live money conflict after writes", legacyClaudeConfig, "money rates exist for both claude-code and claude-bin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, opts := newProxyV8Fixture(t, tc.config)
			seedClaudeV8(t, st)
			if tc.config == legacyClaudeConfig {
				execProxySQL(t, st, `UPDATE money_settings SET data=json_insert(data,'$.rates[#]',json('{"provider":"claude-bin"}'))`)
			}
			fixture := &Service{store: st}
			before := claudeSetupDBState(t, fixture, "configuration", "secrets", "accounts", "grants", "claude_shared_enrollment", "money_settings", "requests", "money_history")
			s, err := Open(opts)
			if s != nil {
				_ = s.Close()
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private") {
				t.Fatalf("migration did not safely abort: %v", err)
			}
			if v := proxyCount(t, st, "PRAGMA user_version"); v != 8 {
				t.Fatalf("aborted migration changed version to %d", v)
			}
			if after := claudeSetupDBState(t, fixture, "configuration", "secrets", "accounts", "grants", "claude_shared_enrollment", "money_settings", "requests", "money_history"); after != before {
				t.Fatal("aborted migration changed v8 data")
			}
			execProxySQL(t, st, "UPDATE configuration SET data=? WHERE id=1", legacyClaudeConfig)
			execProxySQL(t, st, "UPDATE money_settings SET data=? WHERE id=1", oldClaudeRate)
			if v := proxyCount(t, openMigratedProxy(t, opts).store, "PRAGMA user_version"); v != schemaVersion {
				t.Fatalf("retry did not upgrade fixed v8 fixture: version=%d", v)
			}
		})
	}
}

func TestProviderMigrationV9StructuralOnly(t *testing.T) {
	st, _ := newProxyV8Fixture(t, `{"routes":[{"name":"openai","local_prefix":"/openai/","upstream":"srv://unresolvable.invalid","enabled":true,"set_headers":{"Authorization":{"secret":"missing","prefix":"Bearer "}}}]}`)
	if err := st.initialize(); err != nil {
		t.Fatal("schema migration attempted upstream resolution or secret lookup", err)
	}
	if v := proxyCount(t, st, "PRAGMA user_version"); v != schemaVersion {
		t.Fatalf("structural validation did not publish marker: %d", v)
	}
}

func TestProviderMigrationV9RejectsLegacyImport(t *testing.T) {
	s := testService(t)
	cookie, csrf := loginAdmin(t, s)
	r := adminRequest(s, "POST", "import", map[string]any{"revision": s.snap.revision, "yaml": `routes: [{name: ungrouped, set_headers: {X-Custom: {value: literal-private}}}]`}, cookie, csrf)
	if r.Code != 400 || !strings.Contains(r.Body.String(), "ungrouped") || strings.Contains(r.Body.String(), "literal-private") {
		t.Fatalf("legacy import response: %d %s", r.Code, r.Body)
	}
}
