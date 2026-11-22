package proxy

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"maps"
	"time"

	"github.com/term-llm/term-llm/internal/app/proxy/deviceflow"
)

func accountIdentityHash(a accountRecord) string {
	if a.credentials.AccountID != "" {
		return identityHash(a.Kind, a.credentials.AccountID)
	}
	return a.identityHash
}

func identityHash(kind, accountID string) string {
	return hashToken("proxy-" + kind + "-identity:" + accountID)
}

type deviceSetupTarget struct {
	kind            string
	name            string
	reconnect       bool
	identityVersion int64
}

type deviceSetupError struct {
	code    int
	message string
}

func (s *Service) prepareDeviceSetup(in deviceSetupInput, kind string) (deviceSetupTarget, *deviceSetupError) {
	if in.Name != "" && in.Account != "" {
		return deviceSetupTarget{}, &deviceSetupError{400, "Specify a name for a new route or an account to reconnect, not both"}
	}
	target := deviceSetupTarget{kind: kind, name: cmp.Or(in.Account, in.Name), reconnect: in.Account != ""}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if target.name == "" {
		target.name = unusedProviderSlug(s.snap.config, kind, kind)
	}
	if !slug.MatchString(target.name) {
		return deviceSetupTarget{}, &deviceSetupError{400, "Name must be a slug of at most 64 characters"}
	}
	old, problem := s.checkDeviceSetupTarget(target)
	target.identityVersion = old.identityVersion
	return target, problem
}

// Check at start and again under the commit lock. The admin may change routes
// or accounts while the user is entering the device code.
func (s *Service) checkDeviceSetupTarget(target deviceSetupTarget) (accountRecord, *deviceSetupError) {
	if !target.reconnect && s.opts.ConfigFile != "" {
		return accountRecord{}, &deviceSetupError{409, "Routes are operator-managed; edit the mounted file and Reload"}
	}
	old, err := s.store.account(target.name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return accountRecord{}, &deviceSetupError{503, "Could not check available accounts"}
	}
	if target.reconnect {
		switch {
		case err != nil:
			return old, &deviceSetupError{404, deviceflow.Label(target.kind) + " account not found"}
		case old.Kind != target.kind:
			return old, &deviceSetupError{409, "This account is not a " + deviceflow.Label(target.kind) + " account"}
		case old.DeviceReconnect == deviceflow.CustomSettings:
			// Production credentials must never go to custom issuers or origins.
			return old, &deviceSetupError{409, "Accounts with custom OAuth settings cannot use " + deviceflow.Label(target.kind) + " device sign-in"}
		case old.DeviceReconnect != deviceflow.Eligible:
			recovery := "create a new provider and approve clients again"
			if s.opts.ConfigFile != "" {
				recovery = "ask the operator to configure a new provider and approve clients again"
			}
			return old, &deviceSetupError{409, "The original " + deviceflow.Label(target.kind) + " account identity is unavailable; " + recovery}
		}
		return old, nil
	}
	if err == nil {
		return old, &deviceSetupError{409, "An account named " + target.name + " already exists; choose a different provider name. To reuse this name, remove the account under Providers › Unused accounts if no route uses it"}
	}
	return old, s.checkNewDeviceRoute(target.name, target.kind)
}

func (s *Service) checkNewDeviceRoute(name, kind string) *deviceSetupError {
	if len(s.snap.routes) >= 128 {
		return &deviceSetupError{409, "Route limit reached"}
	}
	if !providerSlugAvailable(s.snap.config, name, kind) {
		return &deviceSetupError{409, "A provider or built-in name already exists; choose a different name"}
	}
	for _, route := range s.snap.routes {
		if route.Name == name {
			return &deviceSetupError{409, "A route with this name already exists; choose a different name"}
		}
	}
	if validatePrefix(Route{Name: name, LocalPrefix: "/" + name + "/"}, s.snap.routes) != nil {
		return &deviceSetupError{409, "This route prefix is reserved or overlaps an existing route; choose a different name"}
	}
	return nil
}

func (s *Service) completeDeviceSetup(ctx context.Context, id string, a *deviceSetupAttempt, creds accountCredentials) string {
	if validateAccountCredentials(a.target.kind, creds) != nil || creds.ExpiresAt <= time.Now().Unix() {
		return deviceflow.Label(a.target.kind) + " did not issue usable credentials; start again"
	}
	unlock, err := s.accountLock(ctx, a.target.name)
	if err != nil {
		return deviceflow.Label(a.target.kind) + " sign-in did not complete; start again"
	}
	s.mu.Lock()
	var attemptedCommit bool
	defer func() {
		unlock()
		if attemptedCommit {
			s.reconcileNativeUsageLocked()
		}
		s.mu.Unlock()
	}()
	unlockOwner, valid := s.lockDeviceSetupOwner(a.owner)
	defer unlockOwner()
	if !valid {
		return "Admin session expired or was revoked; sign in and try again"
	}
	d := &s.deviceSetup
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.attempts[id] != a || a.result.Status != "pending" || ctx.Err() != nil || s.closing {
		return deviceflow.Label(a.target.kind) + " sign-in did not complete; start again"
	}
	// Rebuild from durable state even if setup reports a later error. An
	// unchanged binding retains its report when a transaction rolls back.
	attemptedCommit = true
	route, problem := s.commitDeviceSetup(ctx, a.target, creds)
	if problem != nil {
		return problem.message
	}
	a.result = deviceSetupStatus{Status: "connected", Route: route}
	a.expires = time.Now().Add(deviceSetupResultTTL)
	return ""
}

func (s *Service) commitDeviceSetup(ctx context.Context, target deviceSetupTarget, creds accountCredentials) (string, *deviceSetupError) {
	old, problem := s.checkDeviceSetupTarget(target)
	if problem != nil {
		return "", problem
	}
	a := accountRecord{AccountInfo: AccountInfo{Name: target.name, Kind: target.kind, State: "ready", Generation: 1, ExpiresAt: creds.ExpiresAt, Updated: time.Now().UnixMilli()}, credentials: creds}
	if target.reconnect {
		// Routine refresh may change the generation, but sign-out/replacement
		// changes the identity epoch and makes this pending authorization stale.
		if old.identityVersion != target.identityVersion {
			return "", &deviceSetupError{409, "The " + deviceflow.Label(target.kind) + " account changed while signing in; start again"}
		}
		if accountIdentityHash(old) != identityHash(old.Kind, creds.AccountID) {
			if old.Kind == "grok" {
				return "", &deviceSetupError{409, "Sign in to the original Grok account, or create a separate route"}
			}
			return "", &deviceSetupError{409, "Sign in to the original ChatGPT account and workspace, or create a separate route"}
		}
		a.Generation, a.identityVersion, a.operator = old.Generation+1, old.identityVersion+1, old.operator
		if err := s.store.replaceAccount(ctx, a, old.Generation); err != nil {
			return "", &deviceSetupError{503, "Could not update the " + deviceflow.Label(target.kind) + " account; existing access is unchanged"}
		}
		s.rememberAccount(a.Name, a.credentials)
		s.cancelAccountSockets(a.Name)
		return s.deviceRouteForAccount(a.Name), nil
	}
	return s.provisionDeviceRoute(ctx, a)
}

// Reconnect preserves every route, including custom and disabled routes. The
// first attached route is useful to the UI; prefer an enabled route if any.
func (s *Service) deviceRouteForAccount(name string) string {
	var first string
	for _, route := range s.snap.routes {
		if route.Account == name {
			if route.Enabled {
				return route.Name
			}
			first = cmp.Or(first, route.Name)
		}
	}
	return first
}

func (s *Service) provisionDeviceRoute(ctx context.Context, a accountRecord) (string, *deviceSetupError) {
	preset, ok := routePresetByID(a.Kind)
	if !ok || preset.AccountKind != a.Kind {
		return "", &deviceSetupError{503, "Standard route unavailable"}
	}
	c := s.snap.config
	money, err := s.currentMoneySettingsLocked(ctx)
	if err != nil {
		return "", &deviceSetupError{503, "Could not read current proxy configuration"}
	}
	c.Money = money
	c.Providers = maps.Clone(c.Providers)
	if c.Providers == nil {
		c.Providers = make(map[string]Provider)
	}
	c.Providers[a.Name] = Provider{Label: preset.Label}
	route := devicePresetRoute(preset, a.Name, a.Name)
	c.Routes = append(append([]Route(nil), c.Routes...), route)
	companions := deviceCompanionRoutes(preset, a.Name, c)
	c.Routes = append(c.Routes, companions...)
	added := 1 + len(companions)
	next, err := s.snap.recompile(c, s.snap.secrets)
	if err != nil {
		return "", &deviceSetupError{503, "Could not prepare the standard " + deviceflow.Label(a.Kind) + " route"}
	}
	for _, r := range next.routes[len(next.routes)-added:] {
		if !accountOriginAllowed(a, r.origin) {
			return "", &deviceSetupError{503, "Could not prepare the standard " + deviceflow.Label(a.Kind) + " route"}
		}
	}
	err = s.store.accountWrite(ctx, func(tx *sql.Tx) error {
		if err := insertAccount(ctx, tx, a); err != nil {
			return err
		}
		return saveSetupConfig(ctx, tx, next)
	})
	if err != nil {
		return "", &deviceSetupError{503, "Could not save the " + deviceflow.Label(a.Kind) + " route and account; existing configuration is unchanged"}
	}
	s.rememberAccount(a.Name, a.credentials)
	s.reconcileSockets(next)
	s.snap = next // completeDeviceSetup reconciles after releasing the device locks
	return route.Name, nil
}
