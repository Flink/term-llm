package proxy

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/sqliteutil"
)

func createProxyV10Fixture(t *testing.T) *store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	st := &store{db: db}
	createProxyV9Fixture(t, st)
	// Released v10 adds only the request filter indexes to the v9 schema.
	execProxySQL(t, st, requestFilterIndexesV10+"PRAGMA user_version=10")
	return st
}

const v10ProviderLabels = `{
  "providers": {
    "personal": {"label":"Antigravity personal subscription"},
    "claude-bin": {"label":"Claude Code plan"},
    "grok": {"label":"Grok plan","client":{"type":"grok","model":"fixture"}},
    "grok-work": {"label":"Work Grok"},
    "cdck-frontier": {"label":"Grok plan"},
    "chatgpt": {"label":"ChatGPT (personal)"},
    "chatgpt-backend": {"label":"ChatGPT plan usage and dictation"},
    "jina": {"label":"Jina embeddings"},
    "opencode-go": {"label":"OpenCode Go plan"}
  },
  "routes": [
    {"name":"personal","provider":"personal","profile":"agy-bin","local_prefix":"/personal/","account":"personal"},
    {"name":"claude-bin","provider":"claude-bin","profile":"claude-bin","local_prefix":"/claude-bin/","account":"plan"},
    {"name":"grok","provider":"grok","profile":"grok","local_prefix":"/grok/","account":"grok","enabled":true},
    {"name":"grok-work","provider":"grok-work","profile":"grok","account":"work"},
    {"name":"cdck-frontier","provider":"cdck-frontier","upstream":"https://cli-chat-proxy.grok.com/v1"},
    {"name":"chatgpt","provider":"chatgpt","profile":"chatgpt","account":"personal"},
    {"name":"chatgpt-backend","provider":"chatgpt-backend","profile":"chatgpt-backend","account":"work"},
    {"name":"jina","provider":"jina","profile":"jina"},
    {"name":"opencode-go","provider":"opencode-go","profile":"opencode-go"}
  ],
  "secrets":{"fixture":{"stored":"fixture"}}
}`

func TestProxyV11ProviderLabelsMigration(t *testing.T) {
	st := createProxyV10Fixture(t)
	execProxySQL(t, st, `UPDATE configuration SET data=?,revision=17;
INSERT INTO secrets VALUES('fixture','synthetic-secret');
INSERT INTO accounts VALUES('plan','claude-bin','needs_login',1,0,1,'{}');
INSERT INTO callers(id,name,hash,created) VALUES('caller','Test','test-hash',1);
INSERT INTO grants VALUES('caller','grok','fixture-authority');
INSERT INTO requests VALUES(12,'req',10,'caller','grok','GET','/models','ok','{}');`, v10ProviderLabels)
	state := func() string {
		return claudeSetupDBState(t, &Service{store: st}, "secrets", "accounts", "callers", "grants", "requests")
	}
	before := state()
	// A failed write must leave the v10 database intact and permit a retry.
	execProxySQL(t, st, `CREATE TRIGGER reject_label_update BEFORE UPDATE ON configuration BEGIN SELECT RAISE(ABORT, 'fixture'); END;`)
	if err := st.initialize(); err == nil || !strings.Contains(err.Error(), "write provider label updates") {
		t.Fatalf("expected label migration to fail: %v", err)
	}
	if proxyCount(t, st, "PRAGMA user_version") != 10 || proxyV11ConfigData(t, st) != v10ProviderLabels || state() != before {
		t.Fatal("failed upgrade did not leave v10 state intact")
	}
	execProxySQL(t, st, "DROP TRIGGER reject_label_update")
	if err := st.initialize(); err != nil {
		t.Fatal(err)
	}
	if proxyCount(t, st, "PRAGMA user_version") != schemaVersion || proxyCount(t, st, "SELECT revision FROM configuration") != 17 || state() != before {
		t.Fatal("upgrade changed grants, accounts, credentials, history, or revision")
	}
	assertV11ConfigLabels(t, proxyV11ConfigData(t, st))
	ctx := context.Background()
	fresh := testService(t)
	want, err := sqliteutil.SchemaSignature(ctx, fresh.store.db)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sqliteutil.SchemaSignature(ctx, st.db)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("fresh and migrated schemas differ: %v\ngot %v\nwant %v", err, got, want)
	}
}

func proxyV11ConfigData(t *testing.T, st *store) string {
	t.Helper()
	var data string
	if err := st.db.QueryRow("SELECT data FROM configuration WHERE id=1").Scan(&data); err != nil {
		t.Fatal(err)
	}
	return data
}

func assertV11ConfigLabels(t *testing.T, data string) {
	t.Helper()
	var want, got Config
	if err := json.Unmarshal([]byte(v10ProviderLabels), &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(data), &got); err != nil {
		t.Fatal(err)
	}
	for id, label := range map[string]string{
		"personal": "Antigravity", "claude-bin": "Claude Code", "grok": "Grok",
		"chatgpt-backend": "ChatGPT Usage & Dictation", "jina": "Jina",
	} {
		p := want.Providers[id]
		p.Label = label
		want.Providers[id] = p
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected migration changes: got providers %+v, want %+v", got.Providers, want.Providers)
	}
}
