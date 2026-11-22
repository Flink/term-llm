package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/term-llm/term-llm/internal/app/proxy/nativeusage"
	"github.com/term-llm/term-llm/internal/grokprotocol"
	rp "github.com/term-llm/term-llm/internal/llm/providers/responsesproviders"
	"github.com/term-llm/term-llm/internal/secrettext"
)

const nativeUsageResponseLimit = 4 << 20

func (s *Service) fetchNativeUsageBinding(ctx context.Context, snap *snapshot, b nativeUsageBinding) (nativeUsageReport, error) {
	if !s.nativeUsageCurrent(snap, b) {
		return nativeUsageReport{}, errNativeUsageChanged
	}
	access := b.apiKey
	var a accountRecord
	var err error
	if b.identity != "" {
		a, access, err = s.nativeUsageAccess(ctx, snap, b)
		if err != nil {
			return nativeUsageReport{}, err
		}
	}
	bound := func() bool { return s.nativeUsageBindingCurrent(snap, b) }
	// A quota 401 is not a rejection of the token for inference.
	report, err := s.requestNativeUsage(ctx, bound, b.kind, access, a.credentials.AccountID)
	if err != nil {
		return nativeUsageReport{}, err
	}
	if !bound() {
		return nativeUsageReport{}, errNativeUsageChanged
	}
	s.sanitizeNativeUsage(&report, append(accountValues(a.credentials), access)...)
	return report, nil
}

func (s *Service) nativeUsageBindingCurrent(snap *snapshot, b nativeUsageBinding) bool {
	if !s.nativeUsageCurrent(snap, b) {
		return false
	}
	if b.identity == "" {
		return true
	}
	a, err := s.store.account(b.route.Account)
	return err == nil && nativeUsageIdentity(a) == b.identity && usableNativeUsageAccount(a, b.route.origin)
}

func (s *Service) nativeUsageAccess(ctx context.Context, snap *snapshot, b nativeUsageBinding) (accountRecord, string, error) {
	bound := func() bool { return ctx.Err() == nil && s.nativeUsageCurrent(snap, b) }
	check := func(a accountRecord) bool {
		return nativeUsageIdentity(a) == b.identity && usableNativeUsageAccount(a, b.route.origin) && bound()
	}
	if p := nativeProviderForKind(b.kind); p != nil {
		if !bound() {
			return accountRecord{}, "", errNativeUsageChanged
		}
		f, err := s.nativeForwarder(p)
		if err != nil {
			return accountRecord{}, "", errAccountUnavailable
		}
		return s.boundNativeAccess(ctx, f.ca.vaultKey, b.route.Account, 0, check, bound)
	}
	a, err := s.ensureBoundAccount(ctx, b.route.Account, 0, check)
	return a, a.credentials.AccessToken, err
}

func (s *Service) requestNativeUsage(ctx context.Context, bound func() bool, kind, access, accountID string) (nativeUsageReport, error) {
	headers := http.Header{"Authorization": {"Bearer " + access}}
	read := func(method, endpoint, input string) ([]byte, error) {
		if !bound() || access == "" {
			return nil, errNativeUsageChanged
		}
		return s.readNativeUsage(ctx, method, endpoint, input, headers)
	}
	var endpoint string
	var decode func([]byte) (nativeUsageReport, error)
	switch kind {
	case "claude-bin":
		headers.Set("Anthropic-Beta", "oauth-2025-04-20")
		endpoint, decode = "https://"+claudeAPIHost+"/api/oauth/usage", nativeusage.DecodeClaude
	case "agy-bin":
		root := "https://" + agyAPIHost + "/v1internal:"
		hints, err := read(http.MethodPost, root+"loadCodeAssist", `{"metadata":{"ideType":"ANTIGRAVITY"}}`)
		if err != nil {
			return nativeUsageReport{}, err
		}
		var project struct {
			Project string `json:"cloudaicompanionProject"`
		}
		if json.Unmarshal(hints, &project) != nil || strings.TrimSpace(project.Project) == "" || len(project.Project) > 512 {
			return nativeUsageReport{}, errors.New("Antigravity usage project unavailable")
		}
		body, _ := json.Marshal(map[string]string{"project": strings.TrimSpace(project.Project)})
		data, err := read(http.MethodPost, root+"retrieveUserQuotaSummary", string(body))
		return parseNativeUsage(data, err, nativeusage.DecodeAgy)
	case "chatgpt":
		headers.Set("ChatGPT-Account-ID", accountID)
		headers.Set("originator", rp.ChatGPTOriginator)
		headers.Set("User-Agent", rp.ChatGPTUserAgent())
		endpoint, decode = "https://chatgpt.com/backend-api/wham/usage", nativeusage.DecodeChatGPT
	case "grok":
		headers.Set("X-Userid", accountID)
		headers.Set("X-Xai-Token-Auth", "xai-grok-cli")
		headers.Set("X-Grok-Client-Version", rp.GrokProxyCompatibilityVersion)
		headers.Set("X-Grok-Client-Mode", grokprotocol.ClientModeHeadless)
		headers.Set("User-Agent", rp.GrokUserAgent)
		endpoint, decode = "https://cli-chat-proxy.grok.com/v1/billing?format=credits", nativeusage.DecodeGrok
	case "opencode-go":
		rp.ApplyOpenCodeGoAttributionHeaders(headers, "")
		endpoint, decode = rp.OpenCodeGoBaseURL+"/usage", nativeusage.DecodeOpenCodeGo
	case "openrouter":
		endpoint, decode = "https://openrouter.ai/api/v1/key", nativeusage.DecodeOpenRouter
	default:
		return nativeUsageReport{}, errNativeUsageChanged
	}
	data, err := read(http.MethodGet, endpoint, "")
	return parseNativeUsage(data, err, decode)
}

func parseNativeUsage(data []byte, err error, decode func([]byte) (nativeUsageReport, error)) (nativeUsageReport, error) {
	if err != nil {
		return nativeUsageReport{}, err
	}
	report, err := decode(data)
	if err != nil {
		return nativeUsageReport{}, errors.New("upstream did not return valid usage data")
	}
	return report, nil
}

type nativeUsageHTTPStatus int

func (e nativeUsageHTTPStatus) Error() string {
	return fmt.Sprintf("upstream usage returned HTTP %d", e)
}

func (s *Service) readNativeUsage(ctx context.Context, method, endpoint, input string, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, strings.NewReader(input))
	if err != nil {
		return nil, errors.New("could not create upstream usage request")
	}
	req.Header = headers.Clone()
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	if input != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	// RoundTrip is direct (no environment proxy) and cannot follow redirects.
	resp, err := s.transport.RoundTrip(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("upstream usage request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nativeUsageHTTPStatus(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, nativeUsageResponseLimit+1))
	if err != nil || len(data) > nativeUsageResponseLimit {
		return nil, errors.New("could not read bounded upstream usage body")
	}
	return data, nil
}

func (s *Service) sanitizeNativeUsage(r *nativeUsageReport, values ...string) {
	clean := func(text string) string { return secrettext.Redact(s.redactAccountMaterial(text, values...)) }
	r.Plan, r.Detail = clean(r.Plan), clean(r.Detail)
	for i := range r.Groups {
		g := &r.Groups[i]
		g.Name, g.Detail = clean(g.Name), clean(g.Detail)
		for j := range g.Windows {
			w := &g.Windows[j]
			w.Label, w.Detail = clean(w.Label), clean(w.Detail)
		}
	}
}
