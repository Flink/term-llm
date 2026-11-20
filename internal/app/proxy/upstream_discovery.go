package proxy

import (
	"github.com/term-llm/term-llm/internal/app/proxy/grantscope"
	"github.com/term-llm/term-llm/internal/config"
)

var resolveUpstreamSRV = config.ResolveProxySRV

// The resolver pins one answer per configured URL to the compiled snapshot,
// never the request. Secret and account edits reuse answers for unchanged URLs
// so a rotation cannot retarget a route. Startup, route saves, and operator
// reload resolve afresh and reconcile grants before publishing the snapshot.
type upstreamDiscovery struct {
	previous map[string]string
	resolved map[string]string
}

func (d *upstreamDiscovery) resolve(raw string) (string, error) {
	if !grantscope.IsSRV(raw) {
		return raw, nil
	}
	if target, ok := d.resolved[raw]; ok {
		return target, nil
	}
	target, ok := d.previous[raw]
	if !ok {
		var err error
		target, err = resolveUpstreamSRV(raw)
		if err != nil {
			return "", err
		}
	}
	if d.resolved == nil {
		d.resolved = make(map[string]string)
	}
	d.resolved[raw] = target
	return target, nil
}

func (s *snapshot) recompile(c Config, secrets map[string]material) (*snapshot, error) {
	return compileWithDiscovery(c, s.revision+1, secrets, s.upstreams)
}
