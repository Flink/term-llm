package search

import (
	"io"
	"net/http"

	"github.com/term-llm/term-llm/internal/providerhttp"
	"github.com/term-llm/term-llm/internal/proxyclient"
)

const (
	maxSearchResponseBytes = 2 << 20
	maxSearchErrorBytes    = 8 << 10
)

// Refuse redirects on authenticated requests. Go may forward custom key
// headers to a different host, and a redirect can also replay the search query.
func stopSearchRedirect(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func searchClientWithoutRedirects(client *http.Client) *http.Client {
	copy := *client
	copy.CheckRedirect = stopSearchRedirect
	return &copy
}

func doSearchRequest(client *http.Client, req *http.Request, authenticated bool) (*http.Response, error) {
	if authenticated {
		client = searchClientWithoutRedirects(client)
	}
	return proxyclient.Do(client, req)
}

// Keep HTTP status errors (including Retry-After) even when their diagnostic
// body is oversized or unreadable. Never parse a truncated successful reply.
func readSearchResponse(resp *http.Response) ([]byte, error) {
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxSearchErrorBytes))
		return body, nil
	}
	return providerhttp.ReadBounded(resp.Body, maxSearchResponseBytes)
}
