package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestProxyRunClaudeCommandIsIsolatedAndNeverAcceptsVendorCredentials(t *testing.T) {
	root := newProxyCommand()
	leaf, args, err := root.Find([]string{"run", "claude"})
	if err != nil || len(args) != 0 || leaf.Annotations["isolated-startup"] != "true" {
		t.Fatal("Claude proxy run must use isolated startup", err, args)
	}
	for _, name := range []string{"url", "token", "claude-command"} {
		if leaf.Flags().Lookup(name) == nil {
			t.Fatal("missing flag", name)
		}
	}
	for _, name := range []string{"account", "name", "reconnect", "paste-token", "no-browser", "cancel", "admin-token", "access-token", "refresh-token", "proxy-all-providers"} {
		if leaf.Flags().Lookup(name) != nil {
			t.Fatal("retired or credential/global rebind flag is forbidden", name)
		}
	}
	if leaf.Args == nil {
		t.Fatal("native arguments must be forwarded")
	}
	if _, _, err := root.Find([]string{"connect", "claude"}); err == nil {
		t.Fatal("retired connect command is still registered")
	}
	undo, _, err := root.Find([]string{"disconnect", "claude"})
	if err != nil || undo.Annotations["isolated-startup"] != "true" {
		t.Fatal("missing isolated legacy Claude undo")
	}
}

func TestProxyRunClaudeHelpDocumentsNativeArgumentSeparator(t *testing.T) {
	root := newProxyCommand()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"run", "claude", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	help := out.String()
	for _, want := range []string{"--url", "--token", "--claude-command", "proxy run claude -- --resume"} {
		if !strings.Contains(help, want) {
			t.Fatalf("help missing %q:\n%s", want, help)
		}
	}
	if strings.Contains(help, "--paste-token") || strings.Contains(help, "--admin-token") {
		t.Fatalf("retired/unsafe flag in help:\n%s", help)
	}
}

func TestProxyRunClaudeAcceptsNativeFlagsOnlyAfterSeparator(t *testing.T) {
	root := newProxyCommand()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	// A native flag before -- is a term-llm parse error; after -- it is
	// forwarded. No metadata request is made because the explicit binding is
	// rejected before any network call.
	root.SetArgs([]string{"run", "claude", "--url", "https://127.0.0.1:1/claude-code", "--token", "sk-ant-vendor-credential", "--", "--resume"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "scoped proxy client token") {
		t.Fatalf("vendored token or native argument handling changed: %v", err)
	}
}
