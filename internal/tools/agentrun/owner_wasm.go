//go:build js && wasm

package agentrun

// WebAssembly has no process table, so ownership cannot be proven by PID.
func ownerPIDTerminated(int) bool { return false }
