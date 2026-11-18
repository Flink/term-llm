package proxy

import "strings"

// chatGPTPresetRoute is the standard route a ChatGPT device sign-in creates
// for one preset: the account replaces any static key substitution.
func chatGPTPresetRoute(preset routePreset, name, account string) Route {
	return Route{Name: name, LocalPrefix: "/" + name + "/", Account: account, Enabled: true, Upstream: preset.Upstream, CallerAuth: preset.Auth, Billing: preset.Billing, Usage: preset.Usage, WebSocket: preset.WebSocket}
}

// chatGPTCompanionRoutes adds the fixed-name sibling routes (dictation, usage,
// embeddings, live) beside a new ChatGPT route on the same account. Clients
// bind those features by route name, so a taken name or prefix is skipped and
// left to the operator rather than renamed.
func chatGPTCompanionRoutes(preset routePreset, account string, routes []Route) []Route {
	var out []Route
	for _, id := range preset.Companions {
		companion, ok := routePresetByID(id)
		if !ok || companion.AccountKind != preset.AccountKind || len(routes)+len(out) >= 128 {
			continue
		}
		r := chatGPTPresetRoute(companion, id, account)
		if routeSlotTaken(r, routes) || routeSlotTaken(r, out) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func routePresetByID(id string) (routePreset, bool) {
	for _, p := range routePresets {
		if p.ID == id {
			return p, true
		}
	}
	return routePreset{}, false
}

func routeSlotTaken(r Route, routes []Route) bool {
	if validatePrefix(r, nil) != nil {
		return true
	}
	for _, other := range routes {
		if other.Name == r.Name || strings.HasPrefix(r.LocalPrefix, other.LocalPrefix) || strings.HasPrefix(other.LocalPrefix, r.LocalPrefix) {
			return true
		}
	}
	return false
}
