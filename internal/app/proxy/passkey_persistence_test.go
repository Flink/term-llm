package proxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxyPasskeyDurableSessions(t *testing.T) {
	s, opts := passkeyFixture(t)
	b := newPasskeyBrowser(t, s)
	_, login := bootstrapPasskey(t, b)
	login("reauth", 1)
	original := b.cookies["tlp_admin"]
	csrf := b.csrf
	// Pending ceremonies, unlike browser sessions, must not survive restart.
	b.begin("reauth", struct{}{})
	raw, err := os.ReadFile(filepath.Join(opts.StateDir, "admin-auth", "sessions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), original.Value) || !strings.Contains(string(raw), "token_hash") {
		t.Fatal("session store must persist hashes, not raw browser tokens")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	opts.PasskeyBootstrapTokenFile = ""
	restarted, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	b.s = restarted
	b.send("GET", "state", nil, 200)
	b.status()
	if b.csrf != csrf {
		t.Fatal("restart changed session CSRF token")
	}
	b.send("POST", "auth/sessions/revoke", struct{}{}, 403)
	b.send("POST", "auth/reauth/finish", struct{}{}, 401)
	// Logout must durably revoke the cookie even if a browser replays it.
	b.send("POST", "logout", struct{}{}, 200)
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	afterLogout, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer afterLogout.Close()
	b.s = afterLogout
	b.cookies["tlp_admin"] = original
	b.send("GET", "state", nil, 401)
}

func TestProxyPasskeySessionOpenFailureReleasesLocks(t *testing.T) {
	s, opts := passkeyFixture(t)
	b := newPasskeyBrowser(t, s)
	_, _ = bootstrapPasskey(t, b)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(opts.StateDir, "admin-auth", "sessions.json")
	// A directory is rejected on all platforms, without changing credentials.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if unexpected, err := Open(opts); err == nil {
		unexpected.Close()
		t.Fatal("opened unsafe session store")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(opts)
	if err != nil {
		t.Fatalf("failed startup leaked state lock: %v", err)
	}
	defer restarted.Close()
	if restarted.passkey.store.CredentialCount() != 1 {
		t.Fatal("failed startup changed credentials")
	}
	b.s = restarted
	b.send("GET", "state", nil, 401)
}
