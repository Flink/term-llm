package legacyconfig

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/term-llm/term-llm/internal/proxyprofile"
)

const oldClaude = "claude-code"
const newClaude = "claude-bin"

var slug = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type auth struct{ Header, Prefix string }
type headerRule struct {
	Secret, Prefix string
	Value          *string
}
type destination struct {
	Upstream   string
	SetHeaders map[string]headerRule `json:"set_headers"`
	Allow      []string
}
type routeRule struct {
	destination
	PathPrefix string `json:"path_prefix"`
}
type route struct {
	destination
	Name, Provider, Account string
	LocalPrefix             string `json:"local_prefix"`
	Rules                   []routeRule
}
type routePreset struct {
	ID, Label, Upstream, AccountKind, Family string
	Auth                                     auth
	Companions                               []string
	Rules                                    []routePresetRule
	Inference                                bool
}
type routePresetRule struct {
	PathPrefix, Upstream string
	Auth                 auth
}

type object = map[string]json.RawMessage

type provider struct {
	Label  string                             `json:"label"`
	Client *proxyprofile.ClientProviderConfig `json:"client,omitempty"`
}

type providerIdentity struct {
	providerGroup
	name string
}

// Renames describes changes to route identities with live references outside
// the configuration. Provider-only renames need no database updates.
type Renames struct{ ClaudeRoute bool }

// Migrate builds the v9 JSON without resolving secrets or upstreams. Only
// fields that change shape are rewritten; route destinations and header rules
// remain raw JSON. accountKinds contains the v8 kind keyed by account name.
func Migrate(old []byte, accountKinds map[string]string) ([]byte, Renames, error) {
	var legacy struct {
		Routes    []route
		Providers map[string]proxyprofile.ClientProviderConfig
		Secrets   map[string]object
	}
	var document object
	if json.Unmarshal(old, &document) != nil || document == nil || json.Unmarshal(old, &legacy) != nil {
		return nil, Renames{}, fmt.Errorf("invalid stored v8 configuration; check its fields and types with the previous build")
	}
	routes := legacy.Routes
	identities, profiles, owners, err := identifyProviders(routes, accountKinds)
	if err != nil {
		return nil, Renames{}, err
	}
	providers, err := migrateHints(legacy.Providers, identities, owners)
	if err != nil {
		return nil, Renames{}, err
	}
	renamed, err := renameClaude(routes, profiles, providers, owners)
	if err != nil {
		return nil, Renames{}, err
	}
	if err := scanRoutes(routes); err != nil {
		return nil, Renames{}, err
	}
	for _, secret := range legacy.Secrets {
		delete(secret, "origin")
	}
	if err := setField(document, "secrets", legacy.Secrets); err != nil {
		return nil, Renames{}, err
	}
	if err := rewriteRoutes(document, routes, profiles); err != nil {
		return nil, Renames{}, err
	}
	delete(document, "defaults")
	if err := setField(document, "providers", providers); err != nil {
		return nil, Renames{}, err
	}
	if renamed && document["money"] != nil {
		money, err := RenameClaudeMoneyRates(document["money"])
		if err != nil {
			return nil, Renames{}, err
		}
		document["money"] = money
	}
	data, err := json.Marshal(document)
	return data, Renames{ClaudeRoute: renamed}, err
}

func identifyProviders(routes []route, accountKinds map[string]string) ([]providerIdentity, []string, map[string]string, error) {
	indices := make(map[string]int, len(routes))
	explicit := make(map[string][]string)
	for i, r := range routes {
		if !slug.MatchString(r.Name) {
			return nil, nil, nil, fmt.Errorf("route %d has an invalid name; use a slug of at most 64 characters", i+1)
		}
		if _, found := indices[r.Name]; found {
			return nil, nil, nil, fmt.Errorf("duplicate route %q; rename one of the routes", r.Name)
		}
		indices[r.Name] = i
		if r.Provider != "" {
			if !slug.MatchString(r.Provider) {
				return nil, nil, nil, fmt.Errorf("route %q has an invalid provider group; use a slug of at most 64 characters", r.Name)
			}
			explicit[r.Provider] = append(explicit[r.Provider], r.Name)
		}
	}
	// Reject ambiguity even if the old matcher would merge the ungrouped
	// route into a bucket whose first route didn't carry the colliding name.
	for _, r := range routes {
		if r.Provider == "" && len(explicit[r.Name]) > 0 {
			return nil, nil, nil, fmt.Errorf("ungrouped route %q collides with explicit group %q (routes %s); rename the route or group", r.Name, r.Name, strings.Join(explicit[r.Name], ", "))
		}
	}
	families := providerPresetFamilies()
	groups, matched := providerGroups(routes, accountKinds, families)
	for i, p := range matched {
		if p == nil {
			matched[i] = profileByName(routes[i], accountKinds)
		}
	}
	ids, owners, err := assignProviders(groups, matched, indices, explicit, families)
	if err != nil {
		return nil, nil, nil, err
	}
	profiles := make([]string, len(routes))
	for i, p := range matched {
		routes[i].Provider = owners[routes[i].Name]
		if p != nil {
			profiles[i] = p.ID
		}
	}
	return ids, profiles, owners, nil
}

func profileByName(r route, accountKinds map[string]string) *routePreset {
	kind := accountKinds[r.Account]
	if r.Account != "" && kind == "" {
		return nil
	}
	for _, profiles := range [][]routePreset{routePresets, gatedProfiles} {
		for i := range profiles {
			p := &profiles[i]
			if p.ID == r.Name && p.Inference && p.AccountKind == kind {
				return p
			}
		}
	}
	return nil
}

func assignProviders(groups []providerGroup, matched []*routePreset, indices map[string]int, explicit map[string][]string, families map[string]*routePreset) ([]providerIdentity, map[string]string, error) {
	ids := make([]providerIdentity, 0, len(groups))
	owners := make(map[string]string, len(indices))
	byName := make(map[string][]string, len(groups))
	for _, group := range groups {
		name, err := nameProvider(group, matched, indices, explicit[group.ID] != nil, families)
		if err != nil {
			return nil, nil, err
		}
		if others := byName[name]; len(others) != 0 {
			return nil, nil, fmt.Errorf("routes %s and %s require provider %q; rename or remove the conflicting routes or group", strings.Join(others, ", "), strings.Join(group.Routes, ", "), name)
		}
		byName[name] = group.Routes
		ids = append(ids, providerIdentity{providerGroup: group, name: name})
		for _, route := range group.Routes {
			owners[route] = name
		}
	}
	for _, id := range ids {
		if owner := owners[id.name]; owner != "" && owner != id.name {
			return nil, nil, fmt.Errorf("provider %q (routes %s) conflicts with route %q owned by %q; rename the conflicting route or group", id.name, strings.Join(id.Routes, ", "), id.name, owner)
		}
	}
	return ids, owners, nil
}

func nameProvider(group providerGroup, matched []*routePreset, indices map[string]int, explicit bool, families map[string]*routePreset) (string, error) {
	var family *routePreset
	var primary, builtin string
	for _, name := range group.Routes {
		p := matched[indices[name]]
		if builtin == "" && builtinName(name) {
			builtin = name
		}
		if p == nil {
			continue
		}
		if explicit && legacyNative(p.AccountKind) {
			return "", fmt.Errorf("explicit group %q contains native route %q; remove its provider group before upgrading", group.ID, name)
		}
		parent := families[p.ID]
		if parent == nil {
			parent = p
		}
		if explicit && family != nil && family != parent {
			return "", fmt.Errorf("explicit group %q mixes companion sets (routes %s); split the group before upgrading", group.ID, strings.Join(group.Routes, ", "))
		}
		family = parent
		if p == parent && primary == "" {
			primary = name
		}
	}
	if family != nil && family.Family != "" {
		if explicit && group.ID != family.ID {
			return "", fmt.Errorf("non-inference group %q (routes %s) must use canonical provider name %q; rename the group before upgrading", group.ID, strings.Join(group.Routes, ", "), family.ID)
		}
		return family.ID, nil
	}
	if explicit {
		return group.ID, nil
	}
	if primary != "" {
		return primary, nil
	}
	if builtin != "" {
		return builtin, nil
	}
	return group.Routes[0], nil
}

func migrateHints(hints map[string]proxyprofile.ClientProviderConfig, ids []providerIdentity, owners map[string]string) (map[string]provider, error) {
	providers := make(map[string]provider, len(ids))
	keys := make(map[string]string, len(owners)+len(ids))
	maps.Copy(keys, owners)
	for _, id := range ids {
		keys[id.ID] = id.name
		providers[id.name] = provider{Label: id.Label}
	}
	used := make(map[string]string)
	for _, key := range slices.Sorted(maps.Keys(hints)) {
		hint := hints[key]
		if hint.IsZero() {
			continue
		}
		name := keys[key]
		if name == "" {
			return nil, fmt.Errorf("client hint %q does not match a route or group; remove the unused hint", publicName(key))
		}
		p := providers[name]
		if p.Client != nil && !reflect.DeepEqual(*p.Client, hint) {
			return nil, fmt.Errorf("provider %q has conflicting client hints %q and %q; choose one hint before upgrading", name, used[name], key)
		}
		p.Client, used[name] = &hint, key
		providers[name] = p
	}
	return providers, nil
}

func renameClaude(routes []route, profiles []string, providers map[string]provider, owners map[string]string) (bool, error) {
	renamed := owners[oldClaude] != ""
	if renamed {
		if owners[newClaude] != "" {
			return false, fmt.Errorf("cannot rename route %q: route %q is occupied; rename that route first", oldClaude, newClaude)
		}
		for _, r := range routes {
			if r.LocalPrefix == "/claude-bin/" {
				return false, fmt.Errorf("cannot rename route %q: /claude-bin/ is occupied by route %q; change that prefix first", oldClaude, r.Name)
			}
		}
		if _, found := providers[newClaude]; found {
			return false, fmt.Errorf("cannot rename route %q: provider %q is occupied; rename that group first", oldClaude, newClaude)
		}
	}
	for i := range routes {
		if profiles[i] == oldClaude {
			profiles[i] = newClaude
		}
		if routes[i].Name == oldClaude {
			if lenGroup(routes, routes[i].Provider) != 1 {
				return false, fmt.Errorf("route %q shares provider %q; split it into its own group before upgrading", oldClaude, routes[i].Provider)
			}
			providers[newClaude] = providers[routes[i].Provider]
			delete(providers, routes[i].Provider)
			routes[i].Name, routes[i].Provider = newClaude, newClaude
			if routes[i].LocalPrefix == "/claude-code/" {
				routes[i].LocalPrefix = "/claude-bin/"
			}
		}
	}
	return renamed, nil
}

func lenGroup(routes []route, name string) int {
	n := 0
	for _, r := range routes {
		if r.Provider == name {
			n++
		}
	}
	return n
}

func rewriteRoutes(document object, routes []route, profiles []string) error {
	raw := make([]object, 0)
	if field := document["routes"]; (field != nil && json.Unmarshal(field, &raw) != nil) || len(raw) != len(routes) {
		return fmt.Errorf("invalid stored v8 routes; check each route with the previous build")
	}
	for i, r := range routes {
		for key, value := range map[string]string{"name": r.Name, "provider": r.Provider, "profile": profiles[i], "local_prefix": r.LocalPrefix} {
			if key == "profile" && value == "" {
				delete(raw[i], key)
				continue
			}
			if err := setField(raw[i], key, value); err != nil {
				return err
			}
		}
	}
	return setField(document, "routes", raw)
}

func setField(dst object, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("could not encode migrated configuration field %s", key)
	}
	dst[key] = b
	return nil
}

func publicName(name string) string {
	if slug.MatchString(name) {
		return name
	}
	return "(invalid name)"
}

func scanRoutes(routes []route) error {
	for _, r := range routes {
		if err := CheckLegacyAllow(r.Name, r.Allow); err != nil {
			return err
		}
		for _, rule := range r.Rules {
			if err := CheckLegacyAllow(r.Name, rule.Allow); err != nil {
				return err
			}
		}
	}
	return nil
}

// CheckLegacyAllow rejects entries whose meaning could change on upgrade.
func CheckLegacyAllow(route string, entries []string) error {
	for _, entry := range entries {
		if strings.Contains(entry, "*") {
			return fmt.Errorf("route %q contains a legacy '*' Allow entry; remove or rewrite it before upgrading, then re-approve the route", publicName(route))
		}
	}
	return nil
}
