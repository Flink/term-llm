package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

type browserCallerInput struct {
	ID              string          `json:"id,omitempty"`
	Name            string          `json:"name,omitempty"`
	Grants          json.RawMessage `json:"grants"`
	Revision        *int64          `json:"revision,omitempty"`
	Providers       json.RawMessage `json:"providers"`
	ProviderChanges json.RawMessage `json:"provider_changes"`
}

type providerApproval struct {
	ID       string `json:"id"`
	Approved *bool  `json:"approved"`
}

// Parse the alternatives before locking. Raw fields distinguish absence from
// null, which must not accidentally turn a provider edit into a route reset.
func (in browserCallerInput) approvals(method string) ([]providerApproval, error) {
	if in.Providers == nil && in.ProviderChanges == nil {
		return nil, nil // Legacy grants (including omitted or null).
	}
	if in.Grants != nil || method == http.MethodPost && in.ProviderChanges != nil || method == http.MethodPut && in.Providers != nil {
		return nil, inputError("Use providers on creation or provider_changes on edit, not both and not alongside grants")
	}
	if in.Revision == nil {
		return nil, inputError("A configuration revision is required for provider approvals")
	}
	if method == http.MethodPost {
		var ids []string
		if err := json.Unmarshal(in.Providers, &ids); err != nil || ids == nil {
			return nil, inputError("providers must be an array of provider IDs")
		}
		approvals := make([]providerApproval, 0, len(ids))
		for _, id := range ids {
			approvals = append(approvals, providerApproval{ID: id, Approved: new(true)})
		}
		return approvals, nil
	}
	var approvals []providerApproval
	decoder := json.NewDecoder(bytes.NewReader(in.ProviderChanges))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&approvals); err != nil || approvals == nil {
		return nil, inputError("provider_changes must be an array of provider approval changes")
	}
	return approvals, nil
}

func (s *Service) changeCaller(w http.ResponseWriter, r *http.Request) {
	var in browserCallerInput
	if !readJSON(w, r, &in) {
		return
	}
	approvals, err := in.approvals(r.Method)
	if err != nil {
		callerFailure(w, err)
		return
	}
	var grants []string
	if in.Grants != nil && json.Unmarshal(in.Grants, &grants) != nil {
		fail(w, 400, "Invalid JSON fields or body size")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(grants) > 128 || r.Method == http.MethodPost && (len(in.Name) < 1 || len(in.Name) > 80) {
		fail(w, 400, "Enter a client name (1–80 characters) and at most 128 explicit route grants")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if in.Revision != nil && *in.Revision != s.snap.revision {
		fail(w, 409, "Route configuration changed; refresh before granting access")
		return
	}
	if approvals != nil {
		s.changeCallerProviders(w, r, in, approvals)
		return
	}
	if r.Method == http.MethodPut {
		if err := s.store.updateGrants(in.ID, grants, s.snap); err != nil {
			callerFailure(w, err)
			return
		}
		s.cancelCallerSockets(in.ID)
		jsonReply(w, 200, map[string]bool{"ok": true})
		return
	}
	s.issueBrowserCaller(w, in.Name, grants)
}

func (s *Service) issueBrowserCaller(w http.ResponseWriter, name string, grants []string) {
	caller, token, err := s.store.createCaller(name, grants, s.snap)
	if err != nil {
		callerFailure(w, err)
		return
	}
	jsonReply(w, 201, map[string]any{"caller": caller, "token": token})
}

// Called under s.mu, so the groups and the route authorities are from the same
// revision checked by the browser. Provider IDs are selectors, never grants.
func (s *Service) changeCallerProviders(w http.ResponseWriter, r *http.Request, in browserCallerInput, approvals []providerApproval) {
	accounts, err := s.store.accountInfos()
	if err != nil {
		callerFailure(w, err)
		return
	}
	routes, err := expandProviderApprovals(providerGroups(s.snap.config.Routes, accounts), approvals)
	if err != nil {
		callerFailure(w, err)
		return
	}
	if err := checkProviderWorkers(s.snap.routes, routes, accountsByName(accounts)); err != nil {
		callerFailure(w, err)
		return
	}
	if r.Method == http.MethodPost {
		names := make([]string, 0, len(routes))
		for _, route := range s.snap.routes {
			if routes[route.Name] {
				names = append(names, route.Name)
			}
		}
		s.issueBrowserCaller(w, in.Name, names)
		return
	}
	changes := make(map[string]string, len(routes))
	for _, route := range s.snap.routes {
		if approved, touched := routes[route.Name]; touched {
			changes[route.Name] = "" // Revoke only this route.
			if approved {
				changes[route.Name] = route.authority
			}
		}
	}
	if err := s.store.changeProviderGrants(r.Context(), in.ID, changes); err != nil {
		callerFailure(w, err)
		return
	}
	s.cancelChangedGrantSockets(in.ID, changes)
	jsonReply(w, 200, map[string]bool{"ok": true})
}

func checkProviderWorkers(routes []activeRoute, approved map[string]bool, accounts map[string]AccountInfo) error {
	for _, route := range routes {
		if approved[route.Name] {
			if err := sharedWorkerRouteReady(route.Route, accounts[route.Account]); err != nil {
				return err
			}
		}
	}
	return nil
}

func expandProviderApprovals(groups []providerGroup, approvals []providerApproval) (map[string]bool, error) {
	byID := make(map[string][]string, len(groups))
	for _, group := range groups {
		byID[group.ID] = group.Routes
	}
	routes := make(map[string]bool)
	for _, approval := range approvals {
		members, found := byID[approval.ID]
		if !found {
			return nil, inputError("Unknown or duplicate provider ID; refresh before saving")
		}
		if approval.Approved == nil {
			return nil, inputError("Each provider change requires an explicit approved boolean")
		}
		delete(byID, approval.ID)
		for _, name := range members {
			routes[name] = *approval.Approved
		}
	}
	return routes, nil
}
