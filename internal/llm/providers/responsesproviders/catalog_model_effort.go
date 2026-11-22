package responsesproviders

import "slices"

// SplitCatalogModelEffort resolves exact IDs before interpreting effort
// suffixes. A known base with an unsupported suffix is still a match: the
// literal ID is returned so the caller does not fall back to static efforts.
func SplitCatalogModelEffort(catalog []ModelInfo, model string) (base, effort string, efforts []string, found bool) {
	for _, info := range catalog {
		if info.ID == model {
			return model, "", info.ReasoningEfforts, true
		}
	}
	candidate, ok := trimKnownEffortSuffix(model)
	if ok {
		effort := model[len(candidate)+1:]
		for _, info := range catalog {
			if info.ID == candidate {
				if slices.Contains(info.ReasoningEfforts, effort) {
					return candidate, effort, info.ReasoningEfforts, true
				}
				return model, "", nil, true
			}
		}
	}
	return model, "", nil, false
}
