package modelsdev

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/term-llm/term-llm/internal/proxyclient"
)

const fixture = `{
 "anthropic":{"id":"anthropic","name":"Anthropic","api":"https://api.anthropic.com/v1","models":{
  "claude-haiku-5-5":{"id":"claude-haiku-5-5","name":"Claude Haiku 5.5","cost":{
   "input":0.1,"output":0.5,"cache_read":0.01,"cache_write":0.125,
   "tiers":[{"input":0.5,"output":2.5,"cache_write":0,"tier":{"type":"context","size":100000}}]}},
  "unknown":{"name":"Unknown"}, "partial":{"cost":{"input":0}},
  "free":{"cost":{"input":0,"output":0,"cache_read":0,"cache_write":0}}
 }},
 "reseller":{"api":"https://reseller.example/v1","models":{
  "claude-haiku-5-5":{"name":"Resold Haiku","cost":{"input":3,"output":15}}
 }}
}`

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func fixtureClient(body string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func TestDecodePreservesMetadataAndOptionalRates(t *testing.T) {
	catalog, err := Decode(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	provider := catalog["anthropic"]
	if provider.ID != "anthropic" || provider.Name != "Anthropic" || provider.API != "https://api.anthropic.com/v1" {
		t.Fatalf("provider metadata = %+v", provider)
	}
	model := provider.Models["claude-haiku-5-5"]
	if model.ID != "claude-haiku-5-5" || model.Name != "Claude Haiku 5.5" || *model.Cost.CacheWrite != 0.125 {
		t.Fatalf("model metadata or published cache-write baseline lost: %+v", model)
	}
	if provider.Models["free"].Cost.CacheRead == nil || *provider.Models["free"].Cost.CacheRead != 0 {
		t.Fatal("explicit zero cache-read price lost")
	}
	tier := model.Cost.Tiers[0]
	if tier.Tier.Type != "context" || tier.Tier.Size != 100000 || tier.CacheRead != nil || tier.CacheWrite == nil || *tier.CacheWrite != 0 {
		t.Fatalf("tier missing/zero semantics lost: %+v", tier)
	}
	if provider.Models["unknown"].Cost != nil || provider.Models["partial"].Cost.Output != nil {
		t.Fatal("missing rate fabricated")
	}
	if catalog["reseller"].Models["claude-haiku-5-5"].Name != "Resold Haiku" {
		t.Fatal("provider namespaces flattened")
	}
}

func TestDecodeRejectsInvalidCatalogs(t *testing.T) {
	for _, body := range []string{
		`null`, `{}`, `[]`, `{"p":null}`, `{"p":{"models":null}}`,
		`{"p":{"models":{}}} {}`, `{"p":{"models":{"m":{"cost":{"input":-1}}}}}`,
		`{"p":{"models":{"m":{"cost":{"tiers":[{"input":-1}]}}}}}`,
		`{"p":{"models":{"m":{"cost":{"input":"free"}}}}}`,
	} {
		t.Run(body, func(t *testing.T) {
			if _, err := Decode(strings.NewReader(body)); err == nil {
				t.Fatalf("accepted invalid catalog %s", body)
			}
		})
	}
}

type repeatedReader struct{ remaining int64 }

func (r *repeatedReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), r.remaining)
	for i := range p[:n] {
		p[i] = ' '
	}
	r.remaining -= n
	return int(n), nil
}

func TestDecodeBoundsBody(t *testing.T) {
	r := &repeatedReader{remaining: MaxBodyBytes + 100}
	if _, err := Decode(r); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized catalog error = %v", err)
	}
	if r.remaining != 99 {
		t.Fatalf("read beyond bound: %d bytes remain", r.remaining)
	}
}

func TestFetchConfiguredRootAndInjectedTransport(t *testing.T) {
	t.Setenv("MODELS_DEV_BASE_URL", "https://catalog.example/base/")
	client := fixtureClient(fixture)
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://catalog.example/base/api.json" {
			t.Fatalf("request URL = %s", req.URL)
		}
		if req.UserAgent() != "term-llm" {
			t.Fatalf("catalog user agent = %q", req.UserAgent())
		}
		if req.Header.Get("Authorization") != "" {
			t.Fatal("unexpected credentials on public catalog request")
		}
		return fixtureClient(fixture).Transport.RoundTrip(req)
	})
	if _, err := Fetch(context.Background(), client, ""); err != nil {
		t.Fatal(err)
	}
}

func TestFetchDelegatedRootUsesProxyClient(t *testing.T) {
	if err := proxyclient.Register([]proxyclient.Binding{{Root: "https://delegated-catalog.example/", Token: "test-scoped-token"}}); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "Bearer test-scoped-token" {
			t.Fatal("delegated catalog did not use scoped proxy authentication")
		}
		return fixtureClient(fixture).Transport.RoundTrip(req)
	})}
	if _, err := Fetch(context.Background(), client, "https://delegated-catalog.example/api.json"); err != nil {
		t.Fatal(err)
	}
}

func TestFetchErrorsAndCancellation(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("private upstream detail"))}, nil
		})}
		_, err := Fetch(context.Background(), client, "https://catalog.example/api.json")
		if err == nil || !strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "private") {
			t.Fatalf("status error = %v", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, req.Context().Err()
		})}
		_, err := Fetch(ctx, client, "https://catalog.example/api.json")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
	})
}
