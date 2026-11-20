package proxyclient

import (
	"runtime"
	"strings"
	"testing"
)

func TestRegisterCallerTagChanges(t *testing.T) {
	const root = "https://proxyclient-tag.example.test/api"
	secret := "sk-tlp-" + strings.Repeat("a", 64)
	binding := Binding{Root: root, Token: secret, Kind: "chatgpt"}
	tagged := binding
	tagged.Token += ".first"
	if err := Register([]Binding{binding, tagged}); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{tagged.Token, secret + ".second", secret} {
		binding.Token = token
		if err := Register([]Binding{binding}); err != nil {
			t.Fatal(err)
		}
		if got, ok := Lookup(root); !ok || got.Token != token {
			t.Fatal("binding did not use the current token")
		}
	}
	other := binding
	other.Token = "sk-tlp-" + strings.Repeat("b", 64) + ".first"
	if err := Register([]Binding{other}); err == nil || !strings.Contains(err.Error(), "restart") {
		t.Fatal("changing the secret must require a restart")
	}
	if err := Register([]Binding{binding, other}); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatal("conflicting secrets must fail in the same registration")
	}
}

func TestNativeStateDirIgnoresCallerTag(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("HOME", cache)
	t.Setenv("LOCALAPPDATA", cache)
	root := "https://proxyclient-tag.example.test/api"
	secret := "sk-tlp-" + strings.Repeat("a", 64)
	for name, stateDir := range map[string]func(string, string) (string, error){
		"Claude": ClaudeNativeStateDir,
		"agy":    AgyNativeStateDir,
	} {
		t.Run(name, func(t *testing.T) {
			if name == "agy" && runtime.GOOS != "linux" {
				t.Skip("agy native state is Linux-only")
			}
			plain, err := stateDir(root, secret)
			if err != nil {
				t.Fatal(err)
			}
			tagged, err := stateDir(root, secret+".abcd")
			if err != nil || tagged != plain {
				t.Fatal("tag changed the native state directory", err)
			}
			other, err := stateDir(root, "sk-tlp-"+strings.Repeat("b", 64)+".abcd")
			if err != nil || other == plain {
				t.Fatal("different callers did not get distinct native state directories", err)
			}
		})
	}
}
