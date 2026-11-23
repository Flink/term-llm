package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/term-llm/term-llm/internal/providerhttp"
)

// GoogleSearcher implements Searcher using Google Custom Search API.
type GoogleSearcher struct {
	baseURL string
	client  *http.Client
	apiKey  string
	cx      string // Custom Search Engine ID
}

func NewGoogleSearcher(apiKey, cx string, client *http.Client) *GoogleSearcher {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &GoogleSearcher{
		baseURL: "https://www.googleapis.com/customsearch/v1",
		client:  client,
		apiKey:  apiKey,
		cx:      cx,
	}
}

type googleResponse struct {
	Items []googleItem `json:"items"`
	Error *googleError `json:"error,omitempty"`
}

type googleItem struct {
	Title   string `json:"title"`
	Link    string `json:"link"`
	Snippet string `json:"snippet"`
}

type googleError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (g *GoogleSearcher) Search(ctx context.Context, query string, maxResults int) ([]Result, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("empty query")
	}
	if maxResults <= 0 {
		maxResults = 10
	}
	if maxResults > 10 {
		maxResults = 10 // Google Custom Search API max is 10 per request
	}

	params := url.Values{}
	params.Set("key", g.apiKey)
	params.Set("cx", g.cx)
	params.Set("q", query)
	params.Set("num", strconv.Itoa(maxResults))

	reqURL := g.baseURL + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := searchClientWithoutRedirects(g.client).Do(req)
	if err != nil {
		// net/http includes the request URL (and therefore the key) in its error.
		if urlErr, ok := err.(*url.Error); ok {
			err = urlErr.Err
		}
		return nil, fmt.Errorf("execute google search: %w", err)
	}
	defer resp.Body.Close()

	body, err := readSearchResponse(resp)
	if err != nil {
		return nil, err
	}

	var googleResp googleResponse
	parseErr := json.Unmarshal(body, &googleResp)
	if resp.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(body))
		if parseErr == nil && googleResp.Error != nil && googleResp.Error.Message != "" {
			message = googleResp.Error.Message
		}
		return nil, providerhttp.NewStatusErrorMessagef(resp, body, "google http %d: %s", resp.StatusCode, message)
	}
	if parseErr != nil {
		return nil, fmt.Errorf("parse response: %w", parseErr)
	}
	if googleResp.Error != nil {
		return nil, fmt.Errorf("google search error %d: %s", googleResp.Error.Code, googleResp.Error.Message)
	}

	results := make([]Result, 0, len(googleResp.Items))
	for _, item := range googleResp.Items {
		results = append(results, Result{
			Title:   item.Title,
			URL:     item.Link,
			Snippet: item.Snippet,
		})
	}

	return results, nil
}
