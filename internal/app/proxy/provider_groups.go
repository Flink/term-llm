package proxy

import (
	"strconv"
	"strings"

	"github.com/term-llm/term-llm/internal/proxyprofile"
)

// providerGroup groups routes for the admin UI and approval expansion. IDs and
// membership are never persisted as grants or used for forwarding.
type providerGroup struct {
	ID         string                             `json:"id"`
	Label      string                             `json:"label"`
	Routes     []string                           `json:"routes"`
	Config     *proxyprofile.ClientProviderConfig `json:"config,omitempty"`
	ConfigYAML string                             `json:"config_yaml,omitempty"`
}

type providerGroupKey struct {
	explicit   string // Separate namespace from inferred families and singleton routes.
	primary    *routePreset
	credential string // account or secret reference name, never its value
}

type providerGroupBucket struct {
	roles     map[string]bool
	ambiguous bool
	index     int
}

func providerGroups(routes []Route, accounts []AccountInfo) []providerGroup {
	kinds := make(map[string]string, len(accounts))
	for _, account := range accounts {
		kinds[account.Name] = account.Kind
	}
	families := providerPresetFamilies()
	rows := make([]providerGroup, len(routes))
	membership := make([]providerGroupKey, len(routes))
	buckets := make(map[providerGroupKey]*providerGroupBucket)
	chatGPTAccounts := make(map[string]string)
	for i, route := range routes {
		rows[i] = providerGroup{ID: route.Name, Label: route.Name, Routes: []string{route.Name}}
		preset, credential := matchProviderPreset(route, kinds[route.Account], families)
		if preset != nil {
			rows[i].Label = providerPresetLabel(preset)
			if preset.AccountKind == "chatgpt" {
				chatGPTAccounts[route.Name] = route.Account
			}
		}
		key := providerGroupKey{explicit: route.Provider}
		if key.explicit == "" {
			if preset == nil || credential == "" {
				continue
			}
			primary := families[preset.ID]
			if primary == nil || primary.AccountKind != preset.AccountKind {
				continue
			}
			key = providerGroupKey{primary: primary, credential: credential}
		}
		membership[i] = key
		bucket := buckets[key]
		if bucket == nil {
			bucket = &providerGroupBucket{roles: make(map[string]bool), index: -1}
			buckets[key] = bucket
		}
		if key.explicit == "" {
			bucket.ambiguous = bucket.ambiguous || bucket.roles[preset.ID]
			bucket.roles[preset.ID] = true
		}
	}
	groups := mergeProviderGroups(rows, membership, buckets)
	distinguishChatGPTGroups(groups, chatGPTAccounts)
	return groups
}

func mergeProviderGroups(rows []providerGroup, membership []providerGroupKey, buckets map[providerGroupKey]*providerGroupBucket) []providerGroup {
	// Emit in route order, not catalog or map order. If a role has duplicate
	// candidates, leave that entire bucket alone rather than guessing siblings.
	groups := rows[:0]
	for i, row := range rows {
		key := membership[i]
		if bucket := buckets[key]; bucket != nil && (key.explicit != "" || !bucket.ambiguous && len(bucket.roles) > 1) {
			if bucket.index >= 0 {
				group := &groups[bucket.index]
				group.Routes = append(group.Routes, row.ID)
				if key.explicit == row.ID {
					group.Label = row.Label // Prefer the explicit group's anchor when it exists.
				}
				continue
			}
			bucket.index = len(groups)
			if key.explicit != "" {
				row.ID = key.explicit
			} else {
				row.Label = providerPresetLabel(key.primary)
			}
		}
		if key.explicit == "" && buckets[providerGroupKey{explicit: row.ID}] != nil {
			row.ID = "route:" + row.ID // Disjoint from every valid explicit slug.
		}
		groups = append(groups, row)
	}
	return groups
}

func providerPresetLabel(preset *routePreset) string {
	if preset.ID == "chatgpt" {
		return "ChatGPT"
	}
	return preset.Label
}

// Keep the ordinary card label short. When labels collide, use the public
// account name, and the route name if that account also has a duplicate role.
func distinguishChatGPTGroups(groups []providerGroup, accounts map[string]string) {
	type labelKey struct{ label, account string }
	labels := make(map[string]int)
	aliases := make(map[labelKey]int)
	keys := make([]labelKey, len(groups))
	for i, group := range groups {
		if !strings.HasPrefix(group.Label, "ChatGPT") {
			continue
		}
		for _, route := range group.Routes {
			if account := accounts[route]; account != "" && (keys[i].account == "" || route == group.ID) {
				keys[i] = labelKey{group.Label, account}
			}
		}
		if keys[i].account != "" {
			labels[group.Label]++
			aliases[keys[i]]++
		}
	}
	for i, key := range keys {
		if key.account == "" || labels[key.label] < 2 {
			continue
		}
		suffix := key.account
		if aliases[key] > 1 {
			suffix += " / " + groups[i].Routes[0]
		}
		groups[i].Label += " (" + suffix + ")"
	}
}

// Guided setup normally uses the new route's name. An existing explicit group
// can still own that slug after its anchor was removed; pick a fresh grouping
// slug rather than attaching to that group or changing route-slot semantics.
func unusedProviderSlug(routes []Route, name string) string {
	taken := make(map[string]bool, len(routes)*2)
	for _, route := range routes {
		taken[route.Name] = true
		taken[route.Provider] = true
	}
	id := name
	for n := 2; taken[id]; n++ {
		suffix := "-" + strconv.Itoa(n)
		id = name[:min(len(name), 64-len(suffix))] + suffix
	}
	return id
}

func providerPresetFamilies() map[string]*routePreset {
	families := make(map[string]*routePreset)
	for i := range routePresets {
		primary := &routePresets[i]
		if len(primary.Companions) == 0 {
			continue
		}
		families[primary.ID] = primary
		for _, id := range primary.Companions {
			families[id] = primary
		}
	}
	return families
}

func matchProviderPreset(route Route, kind string, families map[string]*routePreset) (*routePreset, string) {
	if route.Account != "" && kind == "" {
		return nil, "" // An unknown account is not a static API key.
	}
	var match *routePreset
	var credential string
	for i := range routePresets {
		preset := &routePresets[i]
		if kind != preset.AccountKind {
			continue
		}
		if kind != "" && nativeProviderForKind(kind) != nil {
			return preset, "" // Legacy native routes can be labeled but never grouped.
		}
		ref, ok := providerPresetCredential(route, preset)
		if !ok {
			continue
		}
		if match == nil {
			match, credential = preset, ref
			continue
		}
		// Grok and its CLI companion have identical destinations and custody.
		// Use the fixed companion name only after both shapes have matched.
		primary := families[match.ID]
		if primary == nil || primary != families[preset.ID] || ref != credential {
			return nil, ""
		}
		if preset.ID == route.Name || match.ID != route.Name && preset == primary {
			match = preset
		}
	}
	return match, credential
}

func providerPresetCredential(route Route, preset *routePreset) (string, bool) {
	if len(route.Rules) != len(preset.Rules) {
		return "", false
	}
	if len(route.Rules) == 0 {
		if !samePresetEndpoint(route.Upstream, preset.Upstream) {
			return "", false
		}
		return presetCredentialRef(route.Account, route.SetHeaders, preset.Auth)
	}
	var credential string
	for _, candidate := range preset.Rules {
		var ref string
		var ok bool
		for _, rule := range route.Rules {
			if rule.PathPrefix == candidate.PathPrefix && samePresetEndpoint(rule.Upstream, candidate.Upstream) {
				ref, ok = presetCredentialRef(route.Account, rule.SetHeaders, candidate.Auth)
				break
			}
		}
		if !ok || credential != "" && ref != credential {
			return "", false
		}
		credential = ref
	}
	return credential, true
}

func samePresetEndpoint(saved, preset string) bool {
	return preset != "" && strings.TrimSuffix(saved, "/") == strings.TrimSuffix(preset, "/")
}

func presetCredentialRef(account string, headers map[string]HeaderRule, auth Auth) (string, bool) {
	if account != "" || auth.Header == "" {
		return account, len(headers) == 0
	}
	if len(headers) != 1 {
		return "", false
	}
	for header, rule := range headers {
		if strings.EqualFold(header, auth.Header) && rule.Prefix == auth.Prefix && rule.Value == nil && rule.Secret != "" {
			return rule.Secret, true
		}
	}
	return "", false
}
