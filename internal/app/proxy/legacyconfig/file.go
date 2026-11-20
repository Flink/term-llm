package legacyconfig

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// CheckLegacyFile looks for v8 fields before strict YAML decoding. It only
// reads identifiers and field names; parser diagnostics and values are never
// surfaced. A profiled route needs a provider entry, but a correctly declared
// hand-made route may omit its profile.
func CheckLegacyFile(raw []byte) error {
	var file struct {
		Routes []struct {
			Name, Provider, Profile string
		}
		Providers map[string]map[string]yaml.Node
		Secrets   map[string]map[string]yaml.Node
		Accounts  map[string]struct{ Kind string }
	}
	if yaml.Unmarshal(raw, &file) != nil {
		return fmt.Errorf("invalid YAML configuration (check fields and types)")
	}
	var problems []string
	for _, r := range file.Routes {
		var missing []string
		switch {
		case r.Provider == "":
			missing = append(missing, "provider and its providers entry")
		case file.Providers[r.Provider] == nil:
			missing = append(missing, fmt.Sprintf("providers entry %q", publicName(r.Provider)))
		case oldHintFields(file.Providers[r.Provider]):
			missing = append(missing, fmt.Sprintf("stored providers entry %q (move hints under client)", publicName(r.Provider)))
		}
		if len(missing) > 0 {
			if r.Profile == "" {
				missing = append(missing, "profile (for preset routes)")
			}
			problems = append(problems, fmt.Sprintf("route %q missing %s", publicName(r.Name), strings.Join(missing, ", ")))
		}
		if r.Name == oldClaude || r.Profile == oldClaude || r.Provider == oldClaude {
			problems = append(problems, fmt.Sprintf("route %q: rename the legacy Claude route, profile or provider from claude-code to claude-bin", publicName(r.Name)))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(file.Secrets)) {
		if _, exists := file.Secrets[name]["origin"]; exists {
			problems = append(problems, fmt.Sprintf("secret %q: secret origin binding was removed; delete this line", publicName(name)))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(file.Accounts)) {
		if file.Accounts[name].Kind == oldClaude {
			problems = append(problems, fmt.Sprintf("account %q: change kind from claude-code to claude-bin", publicName(name)))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("legacy proxy configuration; update before use: %s", strings.Join(problems, "; "))
	}
	return nil
}

func oldHintFields(entry map[string]yaml.Node) bool {
	for key := range entry {
		switch key {
		case "label", "family", "client":
		default:
			return true
		}
	}
	return false
}
