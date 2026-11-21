package proxyclient

import (
	"context"
	"runtime"
)

const agyDarwinSandbox = `(version 1)
(allow default)
; The rewrite proxy and MCP bridge run on loopback. Never contact Google directly.
(deny network-outbound)
(allow network-outbound (remote ip "localhost:*"))
; The SSH marker selects file storage; enforce the Keychain boundary even if agy ignores it.
(deny process-exec (literal "/usr/bin/security"))
(deny file-read* file-write* (regex #"(^|/)Keychains(/|$)"))
(deny mach-lookup (global-name "com.apple.securityd") (global-name "com.apple.securityd.xpc") (global-name "com.apple.SecurityServer") (global-name "com.apple.security.agent"))`

// AgyNativeWorkerInvocation confines the macOS agy-bin subprocess to its
// loopback rewrite proxy and prevents use of the login Keychain. The sandbox
// is mandatory: an unavailable or unusable sandbox must fail the launch.
func AgyNativeWorkerInvocation(binary string, args []string) (string, []string) {
	if runtime.GOOS == "darwin" {
		return "/usr/bin/sandbox-exec", append([]string{"-p", agyDarwinSandbox, binary}, args...)
	}
	return binary, args
}

// CheckAgyNativeWorkerVersion checks agy inside the same sandbox used by the
// model listing and generation commands, before installing the worker aliases.
func CheckAgyNativeWorkerVersion(ctx context.Context, binary, dir string, env []string) error {
	command, args := AgyNativeWorkerInvocation(binary, []string{"--version"})
	if runtime.GOOS == "darwin" {
		return checkAgyNativeVersion(ctx, command, args, dir, env, 13, "1.5.13 on macOS")
	}
	return checkAgyNativeVersion(ctx, command, args, dir, env, 12, AgyNativeMinimumVersion)
}
