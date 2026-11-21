// Package fakecli helps tests write fake CLI executables as scripts.
package fakecli

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

var python struct {
	once sync.Once
	path string
	err  error
}

// PythonShebang returns a "#!" line naming the real python3 interpreter, or
// skips the test when there is none. Fake CLIs must not use
// "#!/usr/bin/env python3": version-manager shims (mise, pyenv, asdf) resolve
// the interpreter on every launch and, under the isolated empty HOME tests
// run with, that alone can take seconds, enough to trip CLI startup timeouts
// on a loaded machine.
func PythonShebang(t testing.TB) string {
	t.Helper()
	python.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		var out []byte
		out, python.err = exec.CommandContext(ctx, "python3", "-c", "import sys; print(sys.executable)").Output()
		python.path = strings.TrimSpace(string(out))
	})
	if python.err != nil || python.path == "" {
		t.Skipf("python3 required for fake CLI: %v", python.err)
	}
	return "#!" + python.path
}

// PythonScript replaces a leading "#!/usr/bin/env python3" line in script
// with PythonShebang.
func PythonScript(t testing.TB, script string) string {
	t.Helper()
	const envShebang = "#!/usr/bin/env python3"
	if !strings.HasPrefix(script, envShebang) {
		t.Fatalf("fake CLI script does not start with %q", envShebang)
	}
	return PythonShebang(t) + strings.TrimPrefix(script, envShebang)
}
