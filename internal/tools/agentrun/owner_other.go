//go:build linux || windows

package agentrun

// Linux uses /proc for PID and start-time proof; Windows remains conservative.
func ownerPIDTerminated(int) bool { return false }
