package proxy

import (
	"context"
	"maps"
	"net/http"
	"slices"
	"strings"
)

type providerCreateInput struct {
	Revision int64                `json:"revision"`
	Name     string               `json:"name"`
	Label    string               `json:"label"`
	Preset   string               `json:"preset"`
	Routes   []providerRouteInput `json:"routes"`
	Value    string               `json:"value,omitempty"`
	Secret   string               `json:"secret,omitempty"`
}

type providerRouteInput struct {
	Name        string `json:"name"`
	Profile     string `json:"profile"`
	LocalPrefix string `json:"local_prefix,omitempty"`
	Upstream    string `json:"upstream,omitempty"`
	Account     string `json:"account,omitempty"`
	AllowHTTP   bool   `json:"allow_insecure_http,omitempty"`
	Enabled     *bool  `json:"enabled,omitempty"`
}

func (s *Service) createProvider(w http.ResponseWriter, r *http.Request) {
	var in providerCreateInput
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.checkConfigEdit(w, in.Revision) {
		return
	}
	c := s.snap.config
	if !slug.MatchString(in.Name) {
		fail(w, 400, "Provider name must be a slug")
		return
	}
	if _, exists := c.Providers[in.Name]; exists {
		fail(w, 409, "Provider name already exists")
		return
	}
	secrets := maps.Clone(s.snap.secrets)
	var addSecret string
	if in.Value != "" {
		if _, exists := secrets[in.Name]; exists || in.Secret != "" || len(secrets) >= 256 {
			fail(w, 409, "Cannot create a new secret; choose an existing secret or another provider name")
			return
		}
		if !validSecretValue(in.Value) {
			fail(w, 400, "Enter a nonempty single-line secret (maximum 16 KiB)")
			return
		}
		addSecret, in.Secret = in.Name, in.Name
		secrets[in.Name] = material{Value: in.Value}
	} else if _, exists := secrets[in.Secret]; in.Secret != "" && !exists {
		fail(w, 400, "Secret does not exist")
		return
	}
	if err := addPresetProvider(&c, in); err != nil {
		fail(w, 400, err.Error())
		return
	}
	s.commitProviderEdit(w, r, c, secrets, addSecret, nil, http.StatusCreated)
}

func addPresetProvider(c *Config, in providerCreateInput) error {
	preset, ok := routePresetByID(in.Preset)
	if !ok {
		return inputError("Unknown provider preset")
	}
	offered := providerPresetCompanions(preset)
	if len(in.Routes) == 0 {
		for _, profile := range offered {
			name := profile
			if profile == in.Preset {
				name = in.Name
			} else if in.Name != in.Preset {
				name = in.Name + "-" + strings.TrimPrefix(profile, in.Preset+"-")
			}
			in.Routes = append(in.Routes, providerRouteInput{Name: name, Profile: profile})
		}
	}
	c.Routes = slices.Clone(c.Routes)
	var account string
	for _, choice := range in.Routes {
		if choice.Profile == "" {
			choice.Profile = in.Preset
		}
		if !slices.Contains(offered, choice.Profile) {
			return inputError("Route profile is not part of this preset")
		}
		p, _ := routePresetByID(choice.Profile)
		usesKey := p.AccountKind == "" && (p.Auth.Header != "" || len(p.Rules) != 0)
		if (p.AccountKind != "") != (choice.Account != "") || in.Secret != "" && !usesKey {
			return inputError("The selected preset does not support that credential source")
		}
		if account != "" && choice.Account != account {
			return inputError("All provider routes must use the same account")
		}
		account = choice.Account
		if usesKey && in.Secret == "" && p.ID != "vllm" && p.ID != "openai-compatible" {
			return inputError("This preset requires a secret")
		}
		r := p.newRoute(in.Name, choice.Name, in.Secret)
		r.Account, r.AllowHTTP = choice.Account, choice.AllowHTTP || p.Billing == "local"
		if choice.LocalPrefix != "" {
			r.LocalPrefix = choice.LocalPrefix
		}
		if choice.Upstream != "" && len(r.Rules) == 0 {
			r.Upstream = choice.Upstream
		}
		if choice.Enabled != nil {
			r.Enabled = *choice.Enabled
		}
		c.Routes = append(c.Routes, r)
	}
	c.Providers = maps.Clone(c.Providers)
	if c.Providers == nil {
		c.Providers = make(map[string]Provider)
	}
	label := strings.TrimSpace(in.Label)
	if label == "" {
		label = preset.Label
	}
	c.Providers[in.Name] = Provider{Label: label}
	return nil
}

func (s *Service) changeProvider(w http.ResponseWriter, r *http.Request, name string) {
	var in struct {
		Revision int64  `json:"revision"`
		Label    string `json:"label"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.checkConfigEdit(w, in.Revision) {
		return
	}
	c := s.snap.config
	p, ok := c.Providers[name]
	if !ok {
		fail(w, 404, "Unknown provider")
		return
	}
	c.Providers = maps.Clone(c.Providers)
	secrets := s.snap.secrets
	var remove []string
	if r.Method == http.MethodPut {
		p.Label = strings.TrimSpace(in.Label)
		c.Providers[name] = p
	} else {
		c.Routes = slices.DeleteFunc(slices.Clone(c.Routes), func(r Route) bool { return r.Provider == name })
		c.Defaults = maps.Clone(c.Defaults)
		c.Secrets = maps.Clone(c.Secrets)
		pruneDeletedRoutes(&c, s.snap.config.Routes)
		secrets = maps.Clone(secrets)
		remaining := routeSecretReferences(c.Routes)
		for key := range routeSecretReferences(providerRoutes(s.snap.config)[name]) {
			if len(remaining[key]) == 0 {
				delete(secrets, key)
				delete(c.Secrets, key)
				remove = append(remove, key)
			}
		}
	}
	s.commitProviderEdit(w, r, c, secrets, "", remove, http.StatusOK)
}

func (s *Service) updateDefaults(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Revision int64             `json:"revision"`
		Defaults map[string]string `json:"defaults"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.checkConfigEdit(w, in.Revision) {
		c := s.snap.config
		c.Defaults = in.Defaults
		s.commitProviderEdit(w, r, c, s.snap.secrets, "", nil, http.StatusOK)
	}
}

func (s *Service) commitProviderEdit(w http.ResponseWriter, r *http.Request, c Config, secrets map[string]material, add string, remove []string, status int) {
	var err error
	c.Money, err = s.currentMoneySettingsLocked(r.Context())
	if err != nil {
		fail(w, 503, "Money settings unavailable; configuration unchanged")
		return
	}
	next, err := s.snap.recompile(c, secrets)
	if err == nil {
		err = s.validateAccounts(next)
	}
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err := s.store.saveProviderEdit(r.Context(), next, add, remove); err != nil {
		fail(w, 503, "Could not commit provider change; active configuration unchanged")
		return
	}
	s.reconcileSockets(next)
	s.snap = next
	jsonReply(w, status, map[string]int64{"revision": next.revision})
}

func (s *store) saveProviderEdit(ctx context.Context, next *snapshot, add string, remove []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if add != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO secrets (name,value) VALUES (?,?)", add, next.secrets[add].Value); err != nil {
			return err
		}
	}
	for _, name := range remove {
		if _, err = tx.ExecContext(ctx, "DELETE FROM secrets WHERE name=?", name); err != nil {
			return err
		}
	}
	if err = saveSetupConfig(ctx, tx, next); err != nil {
		return err
	}
	return tx.Commit()
}

func routeSecretReferences(routes []Route) map[string][]string {
	out := make(map[string][]string)
	for _, r := range routes {
		keys := make(map[string]bool)
		add := func(headers map[string]HeaderRule) {
			for _, h := range headers {
				if h.Secret != "" {
					keys[h.Secret] = true
				}
			}
		}
		add(r.SetHeaders)
		for _, child := range r.Rules {
			add(child.SetHeaders)
		}
		for name := range keys {
			out[name] = append(out[name], r.Name)
		}
	}
	return out
}
