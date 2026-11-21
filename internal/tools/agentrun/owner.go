package agentrun

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var processStartedAt = time.Now().UnixNano()

// CurrentOwner identifies this process for persisted child runs.
func CurrentOwner() string {
	host, _ := os.Hostname()
	start := strconv.FormatInt(processStartedAt, 10)
	if runtime.GOOS == "linux" {
		if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", os.Getpid())); err == nil {
			if i := strings.LastIndexByte(string(stat), ')'); i >= 0 {
				fields := strings.Fields(string(stat)[i+1:])
				if len(fields) > 19 {
					start = fields[19]
				} // field 22 (starttime), after pid and comm.
			}
		}
	}
	return fmt.Sprintf("%s:%d:%s", host, os.Getpid(), start)
}

// OwnerTerminated requires proof: never infer death from a stale timestamp.
func OwnerTerminated(owner string) bool {
	host, _ := os.Hostname()
	parts := strings.Split(owner, ":")
	if len(parts) != 3 || parts[0] != host {
		return false
	}
	pid, err := strconv.Atoi(parts[1])
	if err != nil || pid <= 0 {
		return false
	}
	if runtime.GOOS != "linux" {
		return ownerPIDTerminated(pid)
	} // Non-Linux Unix can prove ESRCH, but not process start identity.
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(string(stat), ')')
	if i < 0 {
		return false
	}
	fields := strings.Fields(string(stat)[i+1:])
	return len(fields) > 19 && fields[19] != parts[2]
}
