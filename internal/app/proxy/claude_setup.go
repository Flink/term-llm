package proxy

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

type claudeSetupInput struct {
	Revision *int64 `json:"revision"`
	Route    string `json:"route,omitempty"`
}

type claudeSetupReply struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	CallerID string `json:"caller_id"`
	Revision int64  `json:"revision"`
}

// claudeSetupAPI is deliberately ordinary authenticated administration. It does
// not enroll a worker, contact an issuer, or read/write a client's login files.
func (s *Service) claudeSetupAPI(w http.ResponseWriter, r *http.Request, sess session) {
	var in claudeSetupInput
	if !readJSON(w, r, &in) {
		return
	}
	if in.Revision == nil {
		fail(w, 400, "A configuration revision is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.opts.ConfigFile != "" {
		fail(w, 409, "Routes are operator-managed; edit the mounted file and Reload")
		return
	}
	if *in.Revision != s.snap.revision {
		fail(w, 409, "Configuration changed in another tab; refresh before saving")
		return
	}
	// One security operation, not separate account/config/client actions.
	if s.passkey != nil && !s.consumeRecentAuth(w, sess) {
		return
	}
	if r.Method == http.MethodDelete {
		s.clearClaudeSetup(w, r)
		return
	}
	origin, allowed := s.browserOrigin(r)
	if !allowed {
		fail(w, 403, "Admin Host is not allowed")
		return
	}
	next, chosen, account, err := s.prepareClaudeSetup(r, in.Route)
	if err != nil {
		claudeSetupFailure(w, err)
		return
	}
	id, token := randomToken("c_")[:26], randomToken("sk-tlp-")
	if err = s.store.createClaudeSetup(r.Context(), next, account, chosen.Name, id, token); err != nil {
		claudeSetupFailure(w, err)
		return
	}
	s.reconcileSockets(next)
	s.snap = next
	jsonReply(w, 201, claudeSetupReply{URL: origin + chosen.LocalPrefix, Token: token, CallerID: id, Revision: next.revision})
}

func claudeSetupFailure(w http.ResponseWriter, err error) {
	if safe, ok := err.(inputError); ok {
		fail(w, 400, safe.Error())
		return
	}
	fail(w, 503, "Could not commit Claude setup; existing configuration and access are unchanged")
}

// Copy the mutable config containers: compile normalizes routes in place. Money
// is canonical in its singleton, not necessarily the last published snapshot.
func (s *Service) claudeSetupConfig(r *http.Request) (Config, error) {
	c := s.snap.config
	c.Routes = append([]Route{}, c.Routes...)
	c.Accounts = make(map[string]AccountRef, len(s.snap.config.Accounts))
	for name, ref := range s.snap.config.Accounts {
		c.Accounts[name] = ref
	}
	var err error
	c.Money, err = s.currentMoneySettingsLocked(r.Context())
	return c, err
}

func (s *Service) prepareClaudeSetup(r *http.Request, requestedRoute string) (*snapshot, Route, *accountRecord, error) {
	c, err := s.claudeSetupConfig(r)
	if err != nil {
		return nil, Route{}, nil, err
	}
	infos, slots, used, err := s.claudeSetupInventory(c)
	if err != nil {
		return nil, Route{}, nil, err
	}
	preset, err := claudeSetupPreset()
	if err != nil {
		return nil, Route{}, nil, err
	}
	var chosen Route
	var found bool
	if requestedRoute == "" {
		chosen, found = claudeSetupExistingRoute(c.Routes, slots, preset)
	} else {
		for _, route := range c.Routes {
			if route.Name == requestedRoute && route.Enabled && claudeSetupMatches(route, slots, preset) {
				chosen, found = route, true
				break
			}
		}
		if !found {
			return nil, Route{}, nil, inputError("Enable a launcher-compatible Claude route before adding a device")
		}
	}
	var created *accountRecord
	if found {
		for i := range c.Routes {
			if c.Routes[i].Name == chosen.Name {
				c.Routes[i].Enabled = true
			}
		}
		chosen.Enabled = true
	} else {
		origin, err := Origin(preset.Upstream)
		if err != nil {
			return nil, Route{}, nil, err
		}
		accountName := ""
		// accountInfos is sorted, making reuse stable without map-order choices.
		for _, info := range infos {
			if a, ok := slots[info.Name]; ok && accountOriginAllowed(a, origin) {
				accountName = a.Name
				break
			}
		}
		if accountName == "" {
			accountName = claudeSetupFreeName(preset.ID, used)
			created = &accountRecord{AccountInfo: AccountInfo{Name: accountName, Kind: preset.AccountKind, State: "native_login", AuthMode: "vault", Generation: 1, Updated: time.Now().UnixMilli()}, credentials: accountCredentials{Mode: "vault"}}
		}
		chosen = Route{Account: accountName, Enabled: true, Upstream: preset.Upstream, CallerAuth: preset.Auth, Billing: preset.Billing, Usage: preset.Usage, WebSocket: preset.WebSocket}
		used = map[string]bool{}
		for _, route := range c.Routes {
			used[route.Name] = true
		}
		for i := 1; ; i++ {
			name := claudeSetupName(preset.ID, i)
			chosen.Name, chosen.LocalPrefix = name, "/"+name+"/"
			if !used[name] && validatePrefix(chosen, s.snap.routes) == nil {
				break
			}
		}
		c.Routes = append(c.Routes, chosen)
	}
	next, err := compile(c, s.snap.revision+1, s.snap.secrets)
	if err != nil {
		return nil, Route{}, nil, inputError(err.Error())
	}
	// The new slot is not visible until commit. Its route uses canonical native
	// defaults; all other account bindings use the normal activation validator.
	check := *next
	check.routes = nil
	for _, route := range next.routes {
		if created == nil || route.Account != created.Name {
			check.routes = append(check.routes, route)
		}
	}
	if err = s.validateAccounts(&check); err != nil {
		return nil, Route{}, nil, err
	}
	return next, chosen, created, nil
}

func (s *Service) claudeSetupInventory(c Config) ([]AccountInfo, map[string]accountRecord, map[string]bool, error) {
	infos, err := s.store.accountInfos()
	if err != nil {
		return nil, nil, nil, err
	}
	slots := map[string]accountRecord{}
	used := map[string]bool{}
	for _, route := range c.Routes {
		used[route.Name] = true
	}
	for name := range c.Accounts {
		used[name] = true
	}
	for _, info := range infos {
		used[info.Name] = true
		if info.Kind != "claude-code" {
			continue
		}
		a, err := s.store.account(info.Name)
		if err != nil {
			return nil, nil, nil, err
		}
		if validateAccountCredentials(a.Kind, a.credentials) == nil {
			slots[a.Name] = a
		}
	}
	return infos, slots, used, nil
}

func claudeSetupPreset() (routePreset, error) {
	for _, p := range routePresets {
		if p.ID == "claude-code" && p.AccountKind == "claude-code" {
			return p, nil
		}
	}
	return routePreset{}, fmt.Errorf("native Claude preset unavailable")
}

func claudeSetupExistingRoute(routes []Route, slots map[string]accountRecord, p routePreset) (Route, bool) {
	// Prefer a working enabled route over waking an intentionally disabled one.
	for _, enabled := range []bool{true, false} {
		for _, route := range routes {
			if route.Enabled == enabled && claudeSetupMatches(route, slots, p) {
				return route, true
			}
		}
	}
	return Route{}, false
}

func claudeSetupMatches(route Route, slots map[string]accountRecord, p routePreset) bool {
	a, ok := slots[route.Account]
	if !ok || (route.Upstream != p.Upstream && route.Upstream != p.Upstream+"/") {
		return false
	}
	origin, err := Origin(route.Upstream)
	return err == nil && accountOriginAllowed(a, origin) && claudeSetupNativeCompatible(route)
}

// The native helper bootstraps with a bearer token and needs the full core
// endpoint surface. Do not rewrite a customized auth scheme or broaden policy
// merely to reuse its route; a separate canonical route can share its slot.
func claudeSetupNativeCompatible(r Route) bool {
	if !strings.EqualFold(r.CallerAuth.Header, "Authorization") || r.CallerAuth.Prefix != "Bearer " {
		return false
	}
	for _, endpoint := range []string{"GET /api/oauth/profile", "GET /api/oauth/usage", "POST /v1/messages", "POST /v1/messages/count_tokens", "GET /v1/models"} {
		method, path, _ := strings.Cut(endpoint, " ")
		if !policyAllows(r, method, path) {
			return false
		}
	}
	return true
}

func claudeSetupName(base string, n int) string {
	if n == 1 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, n)
}

func claudeSetupFreeName(base string, used map[string]bool) string {
	for i := 1; ; i++ {
		if name := claudeSetupName(base, i); !used[name] {
			return name
		}
	}
}

func (s *Service) clearClaudeSetup(w http.ResponseWriter, r *http.Request) {
	c, err := s.claudeSetupConfig(r)
	if err != nil {
		claudeSetupFailure(w, err)
		return
	}
	infos, err := s.store.accountInfos()
	if err != nil {
		claudeSetupFailure(w, err)
		return
	}
	accounts := map[string]bool{}
	otherAccounts := map[string]bool{}
	for _, info := range infos {
		if info.Kind == "claude-code" {
			accounts[info.Name] = true
		} else {
			otherAccounts[info.Name] = true
		}
	}
	for name, ref := range c.Accounts {
		if ref.Kind == "claude-code" || accounts[name] {
			if !otherAccounts[name] {
				accounts[name] = true
			}
			delete(c.Accounts, name)
		}
	}
	var removed []string
	routes := c.Routes[:0]
	for _, route := range c.Routes {
		if route.Account != "" && accounts[route.Account] {
			removed = append(removed, route.Name)
		} else {
			routes = append(routes, route)
		}
	}
	c.Routes = routes
	next, err := compile(c, s.snap.revision+1, s.snap.secrets)
	if err == nil {
		err = s.validateAccounts(next)
	}
	var revoked []string
	if err == nil {
		revoked, err = s.store.clearClaudeSetup(r.Context(), next, removed)
	}
	if err != nil {
		claudeSetupFailure(w, err)
		return
	}
	// Publication and live cancellation are strictly after the durable commit.
	s.snap = next
	s.reconcileSockets(next)
	for name := range accounts {
		s.cancelAccountSockets(name)
	}
	for _, id := range revoked {
		s.cancelCallerSockets(id)
	}
	jsonReply(w, 200, map[string]int64{"revision": next.revision})
}
