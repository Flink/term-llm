package proxy

import (
	"strings"

	"github.com/term-llm/term-llm/internal/providerspec"
)

// devicePresetRoute is the standard route a device sign-in creates
// for one preset: the account replaces any static key substitution.
func devicePresetRoute(preset routePreset, name, account string) Route {
	r := preset.newRoute(account, name, "")
	r.Account = account
	return r
}

func deviceCompanionRoutes(preset routePreset, account string, c Config) []Route {
	var out []Route
	for _, id := range preset.Companions {
		companion, ok := routePresetByID(id)
		if !ok || companion.AccountKind != preset.AccountKind || len(c.Routes)+len(out) >= 128 {
			continue
		}
		name := id
		if account != preset.ID {
			name = account + strings.TrimPrefix(id, preset.ID)
			if !slug.MatchString(name) {
				continue
			}
		}
		r := devicePresetRoute(companion, name, account)
		if !providerSlugAvailable(c, name, id) || routeSlotTaken(r, c.Routes) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func providerSlugAvailable(c Config, name, own string) bool {
	_, exists := c.Providers[name]
	_, builtin := providerspec.Lookup(name)
	return !exists && (!builtin || name == own)
}

func unusedProviderSlug(c Config, base, own string) string {
	for i := 1; ; i++ {
		name := nativeSetupName(base, i)
		if providerSlugAvailable(c, name, own) && !routeSlotTaken(Route{Name: name, LocalPrefix: "/" + name + "/"}, c.Routes) {
			return name
		}
	}
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
