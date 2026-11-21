// Package agentrun classifies child run statuses and their process owners.
package agentrun

func Active(status string) bool {
	switch status {
	case "queued", "running", "running_elsewhere", "awaiting_approval":
		return true
	}
	return false
}

func Terminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "interrupted", "turn_limit":
		return true
	}
	return false
}
