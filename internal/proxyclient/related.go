package proxyclient

import (
	"fmt"
	"net/url"
)

func canonicalRelatedRoots(source string, roots map[string]string) (map[string]string, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	from, _ := url.Parse(source)
	out := make(map[string]string, len(roots))
	for profile, endpoint := range roots {
		root, err := canonicalRoot(endpoint)
		if err != nil || profile == "" {
			return nil, fmt.Errorf("invalid delegated companion binding")
		}
		to, _ := url.Parse(root)
		if from.Scheme != to.Scheme || from.Host != to.Host {
			return nil, fmt.Errorf("delegated companion must share the proxy origin")
		}
		out[profile] = root
	}
	return out, nil
}

// Related returns the binding for a companion explicitly associated with the
// source root, of the same proxy, credential kind and principal. It never
// infers ownership from a route's mount path.
func Related(endpoint, profile string) (Binding, bool) {
	source, ok := Lookup(endpoint)
	if !ok {
		return Binding{}, false
	}
	root := source.RelatedRoots[profile]
	if root == "" {
		return Binding{}, false
	}
	b, ok := Lookup(root)
	return b, ok && b.Root == root && b.SamePrincipal(source)
}

// KindActive lets unscoped metadata-only paths avoid reading a local vendor
// credential while a delegated integration is active. It does not select auth
// for a network request; those must use Lookup with an explicit endpoint.
func KindActive(kind string) bool {
	registry.RLock()
	defer registry.RUnlock()
	for _, b := range registry.bindings {
		if b.Kind == kind {
			return true
		}
	}
	return false
}
