package proxy

import "strings"

// Native siblings share a stored provider. Every
// device route must support the same account custody and the launcher's Bearer
// credential. Never mint a partial device token for a mixed-custody provider.
func (s *Service) nativeSetupGrants(next *snapshot, chosen Route, p *nativeProxyProvider, operator bool) ([]string, error) {
	grants := []string{chosen.Name}
	if operator {
		return grants, nil
	}
	infos, err := s.store.accountInfos()
	if err != nil {
		return nil, err
	}
	accounts := accountsByName(infos)
	for _, route := range next.routes {
		if route.Name != chosen.Name {
			if route.Provider != chosen.Provider {
				continue
			}
			if !nativeDeviceSibling(chosen, route.Route, p) {
				return nil, inputError("This provider contains an incompatible native route (" + route.Name + "); separate it into another provider before adding a device")
			}
			grants = append(grants, route.Name)
		}
		if err := sharedWorkerRouteReady(route.Route, accounts[route.Account]); err != nil {
			return nil, err
		}
	}
	return grants, nil
}

func nativeDeviceSibling(chosen, sibling Route, p *nativeProxyProvider) bool {
	return sibling.Account == chosen.Account && len(sibling.Rules) == 0 &&
		strings.TrimSuffix(sibling.Upstream, "/") == "https://"+p.apiHost &&
		strings.EqualFold(sibling.CallerAuth.Header, "Authorization") && sibling.CallerAuth.Prefix == "Bearer "
}

func accountsByName(infos []AccountInfo) map[string]AccountInfo {
	accounts := make(map[string]AccountInfo, len(infos))
	for _, account := range infos {
		accounts[account.Name] = account
	}
	return accounts
}

func sharedWorkerRouteReady(route Route, account AccountInfo) error {
	if nativeProviderForKind(account.Kind) != nil && (!route.Enabled || account.State != "ready") {
		return inputError("Enable and connect the shared " + nativeProviderForKind(account.Kind).label + " subscription for route " + route.Name + " before approving access")
	}
	return nil
}
