package proxy

import (
	"cmp"
	"fmt"
	"maps"
	"net/http"
	"strings"
	"time"
)

// Native setup descriptors are selected by the admin path, never by request JSON.
type nativeSetupProvider struct {
	proxy             *nativeProxyProvider
	callerName        string
	requiredEndpoints []string
}

var claudeSetup = nativeSetupProvider{
	proxy: claudeProxyProvider, callerName: "Claude Code",
	requiredEndpoints: []string{"GET /api/oauth/profile", "GET /api/oauth/usage", "POST /v1/messages", "POST /v1/messages/count_tokens", "GET /v1/models"},
}

var agySetup = nativeSetupProvider{
	proxy: agyProxyProvider, callerName: "Antigravity",
	requiredEndpoints: []string{"POST /v1internal:loadCodeAssist", "POST /v1internal:fetchUserInfo", "POST /v1internal:retrieveUserQuotaSummary", "POST /v1internal:fetchAvailableModels", "POST /v1internal:listExperiments", "POST /v1internal:generateContent", "POST /v1internal:streamGenerateContent"},
}

type nativeSetupInput struct {
	Revision     *int64 `json:"revision"`
	Route        string `json:"route,omitempty"`
	SharedAction string `json:"shared_action,omitempty"`
	// Shared sign-in arms an existing client or creates one; never a launcher.
	Caller string `json:"caller,omitempty"`
	Name   string `json:"name,omitempty"`
}

type nativeSetupReply struct {
	URL      string `json:"url"`
	Token    string `json:"token,omitempty"` // Empty when an existing client was armed.
	CallerID string `json:"caller_id"`
	Revision int64  `json:"revision"`
}

// nativeSetupAPI is deliberately ordinary authenticated administration. It does
// not enroll a worker, contact an issuer, or read/write a client's login files.
func (s *Service) nativeSetupAPI(w http.ResponseWriter, r *http.Request, provider nativeSetupProvider) {
	var in nativeSetupInput
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
	if r.Method == http.MethodDelete {
		if in.SharedAction != "" {
			fail(w, 400, "Unexpected shared sign-in action")
			return
		}
		s.clearNativeSetup(w, r, provider)
		return
	}
	origin, allowed := s.browserOrigin(r)
	if !allowed {
		fail(w, 403, "Admin Host is not allowed")
		return
	}
	if in.SharedAction == "" && in.Route == "" {
		in.SharedAction = "create"
	}
	target, err := in.setupCaller(r.Method, provider)
	if err != nil {
		provider.setupFailure(w, err)
		return
	}
	next, chosen, account, err := s.prepareNativeSetup(r, provider, in.Route, in.SharedAction)
	if err != nil {
		provider.setupFailure(w, err)
		return
	}
	// Shared sign-in enrolls one route and is not a device grant.
	operator := in.SharedAction != ""
	grants, err := s.nativeSetupGrants(next, chosen, provider.proxy, operator)
	if err != nil {
		provider.setupFailure(w, err)
		return
	}
	if err = s.store.createNativeSetup(r.Context(), next, account, chosen, target, operator, grants); err != nil {
		provider.setupFailure(w, err)
		return
	}
	s.reconcileSockets(next)
	s.publishSnapshotLocked(next)
	jsonReply(w, 201, nativeSetupReply{URL: origin + chosen.LocalPrefix, Token: target.token, CallerID: target.id, Revision: next.revision})
}

// setupCaller validates the request and selects the client that receives the
// setup grant. Only shared sign-in may arm an existing client or name a new one.
func (in nativeSetupInput) setupCaller(method string, provider nativeSetupProvider) (nativeSetupCaller, error) {
	caller, name := strings.TrimSpace(in.Caller), strings.TrimSpace(in.Name)
	switch {
	case in.SharedAction != "" && method != http.MethodPost:
		return nativeSetupCaller{}, inputError("Shared sign-in requires a setup POST")
	case in.SharedAction != "" && (in.SharedAction != "create" || in.Route != "") && (in.SharedAction != "connect" || in.Route == ""):
		return nativeSetupCaller{}, inputError("Invalid shared native sign-in action")
	case in.SharedAction == "" && (caller != "" || name != ""):
		return nativeSetupCaller{}, inputError("Add device always issues a new worker client")
	case caller != "" && name != "", len(name) > 80:
		return nativeSetupCaller{}, inputError("Choose an existing client or name a new one (1–80 characters)")
	case caller != "":
		return nativeSetupCaller{id: caller}, nil
	}
	return nativeSetupCaller{id: randomToken("c_")[:26], token: randomToken("sk-tlp-"), name: cmp.Or(name, provider.callerName)}, nil
}

func (p nativeSetupProvider) setupFailure(w http.ResponseWriter, err error) {
	if safe, ok := err.(inputError); ok {
		fail(w, 400, safe.Error())
		return
	}
	fail(w, 503, "Could not commit "+p.proxy.label+" setup; existing configuration and access are unchanged")
}

// Copy the mutable config containers: compile normalizes routes in place. Money
// is canonical in its singleton, not necessarily the last published snapshot.
func (s *Service) nativeSetupConfig(r *http.Request) (Config, error) {
	c := s.snap.config
	c.Routes = append([]Route{}, c.Routes...)
	c.Providers = maps.Clone(c.Providers)
	c.Defaults = maps.Clone(c.Defaults)
	c.Accounts = make(map[string]AccountRef, len(s.snap.config.Accounts))
	for name, ref := range s.snap.config.Accounts {
		c.Accounts[name] = ref
	}
	var err error
	c.Money, err = s.currentMoneySettingsLocked(r.Context())
	return c, err
}

func (s *Service) prepareNativeSetup(r *http.Request, p nativeSetupProvider, requestedRoute, sharedAction string) (*snapshot, Route, *accountRecord, error) {
	c, err := s.nativeSetupConfig(r)
	if err != nil {
		return nil, Route{}, nil, err
	}
	slots, used, err := s.nativeSetupInventory(c, p.proxy.kind)
	if err != nil {
		return nil, Route{}, nil, err
	}
	preset, err := p.preset()
	if err != nil {
		return nil, Route{}, nil, err
	}
	c, chosen, created, err := s.chooseNativeSetupRoute(c, p, preset, slots, used, requestedRoute)
	if err != nil {
		return nil, Route{}, nil, err
	}
	next, err := s.snap.recompile(c, s.snap.secrets)
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

func (s *Service) chooseNativeSetupRoute(c Config, p nativeSetupProvider, preset routePreset, slots map[string]accountRecord, used map[string]bool, requestedRoute string) (Config, Route, *accountRecord, error) {
	if requestedRoute != "" {
		for _, route := range c.Routes {
			if route.Name == requestedRoute && route.Enabled && p.matches(route, slots, preset) {
				return c, route, nil, nil
			}
		}
		return c, Route{}, nil, inputError("Enable a launcher-compatible shared " + p.proxy.label + " route first")
	}
	account := newNativeAccount(nativeSetupFreeName(preset.ID, used), preset.AccountKind)
	var route Route
	for i := 1; ; i++ {
		name := nativeSetupName(preset.ID, i)
		route = preset.newRoute(name, name, "")
		if !used[name] && providerSlugAvailable(c, name, preset.ID) && validatePrefix(route, s.snap.routes) == nil {
			break
		}
	}
	route.Account = account.Name
	if c.Providers == nil {
		c.Providers = make(map[string]Provider)
	}
	c.Providers[route.Provider] = Provider{Label: preset.Label}
	c.Routes = append(c.Routes, route)
	return c, route, &account, nil
}

func newNativeAccount(name, kind string) accountRecord {
	return accountRecord{
		AccountInfo: AccountInfo{Name: name, Kind: kind, State: "needs_login", AuthMode: claudeSharedMode, Generation: 1, Updated: time.Now().UnixMilli()},
		credentials: accountCredentials{Mode: claudeSharedMode}, claudeSharedID: randomToken("ns_"),
	}
}

func (s *Service) nativeSetupInventory(c Config, kind string) (map[string]accountRecord, map[string]bool, error) {
	infos, err := s.store.accountInfos()
	if err != nil {
		return nil, nil, err
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
		if info.Kind != kind {
			continue
		}
		a, err := s.store.account(info.Name)
		if err != nil {
			return nil, nil, err
		}
		if a.claudeShared() {
			slots[a.Name] = a
		}
	}
	return slots, used, nil
}

func (p nativeSetupProvider) preset() (routePreset, error) {
	for _, preset := range routePresets {
		if preset.ID == p.proxy.kind && preset.AccountKind == p.proxy.kind {
			return preset, nil
		}
	}
	return routePreset{}, fmt.Errorf("native %s preset unavailable", p.proxy.label)
}

func (p nativeSetupProvider) matches(route Route, slots map[string]accountRecord, preset routePreset) bool {
	a, ok := slots[route.Account]
	if !ok || (route.Upstream != preset.Upstream && route.Upstream != preset.Upstream+"/") {
		return false
	}
	origin, err := Origin(route.Upstream)
	return err == nil && accountOriginAllowed(a, origin) && p.compatible(route)
}

// The native helper bootstraps with a bearer token and needs the full core
// endpoint surface. Do not rewrite a customized auth scheme or broaden policy
// merely to reuse its route; a separate canonical route can share its slot.
func (p nativeSetupProvider) compatible(r Route) bool {
	if !strings.EqualFold(r.CallerAuth.Header, "Authorization") || r.CallerAuth.Prefix != "Bearer " {
		return false
	}
	for _, endpoint := range p.requiredEndpoints {
		method, path, _ := strings.Cut(endpoint, " ")
		if !policyAllows(r, method, path) {
			return false
		}
	}
	return true
}

func nativeSetupName(base string, n int) string {
	if n == 1 {
		return base
	}
	return fmt.Sprintf("%s-%d", base, n)
}

func nativeSetupFreeName(base string, used map[string]bool) string {
	for i := 1; ; i++ {
		if name := nativeSetupName(base, i); !used[name] {
			return name
		}
	}
}

func (s *Service) clearNativeSetup(w http.ResponseWriter, r *http.Request, provider nativeSetupProvider) {
	c, err := s.nativeSetupConfig(r)
	if err != nil {
		provider.setupFailure(w, err)
		return
	}
	infos, err := s.store.accountInfos()
	if err != nil {
		provider.setupFailure(w, err)
		return
	}
	accounts := map[string]bool{}
	otherAccounts := map[string]bool{}
	for _, info := range infos {
		if info.Kind == provider.proxy.kind {
			accounts[info.Name] = true
		} else {
			otherAccounts[info.Name] = true
		}
	}
	for name, ref := range c.Accounts {
		if ref.Kind == provider.proxy.kind || accounts[name] {
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
	pruneDeletedRoutes(&c, s.snap.config.Routes)
	next, err := s.snap.recompile(c, s.snap.secrets)
	if err == nil {
		err = s.validateAccounts(next)
	}
	var revoked []string
	if err == nil {
		revoked, err = s.store.clearNativeSetup(r.Context(), next, removed, provider.proxy)
	}
	if err != nil {
		provider.setupFailure(w, err)
		return
	}
	// Publication and live cancellation are strictly after the durable commit.
	s.publishSnapshotLocked(next)
	s.reconcileSockets(next)
	for name := range accounts {
		s.cancelAccountSockets(name)
	}
	for _, id := range revoked {
		s.cancelCallerSockets(id)
	}
	jsonReply(w, 200, map[string]int64{"revision": next.revision})
}
