package proxyclient

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// AgyNativeMinimumVersion is the first release verified with private Linux
// credential storage and the native OAuth CONNECT proxy.
const AgyNativeMinimumVersion = "1.5.12"

var agySemverPattern = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(-([0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*))?(\+[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?$`)

// AgyVersionAtLeast reports whether `agy --version` output names a release at
// or above the requested version. Unparseable or ambiguous output fails closed.
func AgyVersionAtLeast(version string, wantMajor, wantMinor, wantPatch int) bool {
	var matches [][]string
	for _, field := range strings.Fields(version) {
		candidate := strings.Trim(strings.TrimSpace(field), "()[]{}<>,;")
		if match := agySemverPattern.FindStringSubmatch(candidate); match != nil {
			matches = append(matches, match)
		}
	}
	// Fail closed if the output is unparseable or contains another version-like
	// token (for example, a runtime version before the actual agy version).
	if len(matches) != 1 {
		return false
	}
	match := matches[0]
	got := make([]int, 3)
	for i := range got {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return false
		}
		got[i] = value
	}
	want := []int{wantMajor, wantMinor, wantPatch}
	for i := range got {
		if got[i] != want[i] {
			return got[i] > want[i]
		}
	}
	// A prerelease of the minimum version sorts below the release itself.
	return match[4] == ""
}

// CheckAgyNativeVersion checks the binary under the same isolated routing and
// credential namespace as the main launch. Never include its output in an error.
func CheckAgyNativeVersion(ctx context.Context, binary, dir string, env []string) error {
	return checkAgyNativeVersion(ctx, binary, []string{"--version"}, dir, env, 12, AgyNativeMinimumVersion)
}

func checkAgyNativeVersion(ctx context.Context, binary string, args []string, dir string, env []string, minPatch int, minimum string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Env, cmd.Dir, cmd.WaitDelay = env, dir, time.Second
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("check agy proxy version: %w", err)
	}
	if !AgyVersionAtLeast(string(out), 1, 5, minPatch) {
		return errors.New("Antigravity native proxy requires agy " + minimum + " (or newer; no direct fallback)")
	}
	return nil
}
