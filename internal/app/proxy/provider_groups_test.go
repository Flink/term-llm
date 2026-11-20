package proxy

import "testing"

func providerTestRoute(t *testing.T, id, ref string) Route {
	t.Helper()
	p, ok := routePresetByID(id)
	if !ok {
		t.Fatalf("missing preset %s", id)
	}
	var r Route
	if p.AccountKind != "" {
		r = p.newRoute(id, id, "")
		r.Account = ref
	} else {
		r = p.newRoute(id, id, ref)
	}
	if p.Family != "inference" || id == "chatgpt-backend" || id == "chatgpt-openai" {
		r.Provider = canonicalTestProfile(id)
	}
	return r
}
