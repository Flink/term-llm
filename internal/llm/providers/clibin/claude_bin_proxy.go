package clibin

import (
	"context"
	"fmt"
	"strings"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

const claudeProxyRootEnv = "TERM_LLM_CLAUDE_PROXY_ROOT"

// prepareClaudeProxyEnvironment replaces the final child environment of every
// claude-bin inference and model-listing launch with a native proxy-bound one.
// The shared client bootstrap fetches the process-local CA and proxy origin;
// this wrapper owns only the provider marker and scoped binding checks. Failures
// happen before exec, so a bound provider can never fall back to a local
// Claude login or vendor credential.
func prepareClaudeProxyEnvironment(ctx context.Context, env []string) ([]string, error) {
	root := envValue(env, claudeProxyRootEnv)
	if root == "" {
		return env, nil
	}
	binding, ok := proxyclient.Lookup(root)
	if !ok || binding.Kind != "claude-code" || !proxyclient.SameRoot(binding.Root, root) ||
		proxyclient.ValidateClaudeNativeToken(binding.Token) != nil {
		return nil, fmt.Errorf("claude-bin proxy root has no scoped credential binding")
	}
	stateDir, err := proxyclient.ClaudeNativeStateDir(binding.Root, binding.Token)
	if err != nil {
		return nil, err
	}
	out, err := proxyclient.PrepareClaudeNativeEnvironment(ctx, binding.Root, binding.Token, stateDir, env)
	if err != nil {
		return nil, err
	}
	return withoutClaudeProxyMarker(out), nil
}

// withoutClaudeProxyMarker removes the internal routing marker so neither the
// native process nor a resumed session can select the proxy environment twice.
func withoutClaudeProxyMarker(env []string) []string {
	marker := claudeProxyRootEnv + "="
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, marker) {
			out = append(out, entry)
		}
	}
	return out
}
