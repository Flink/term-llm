package skills

import (
	"net/url"
	"strings"

	"github.com/term-llm/term-llm/internal/providerhttp"
)

// RawGitHubURL constructs a skill's raw-content URL from a GitHub repository
// URL using the explicit content root. Unsupported hosts return an empty URL.
// Already supplied resource URLs must be fetched directly, not passed through
// this constructor.
func RawGitHubURL(repository string) (string, error) {
	if strings.HasPrefix(repository, "github.com/") {
		repository = "https://" + repository
	}
	u, err := url.Parse(repository)
	if err != nil || u.Host != "github.com" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return "", nil
	}
	canonical := constructRawGitHubURL(strings.TrimRight(repository, "/"))
	if !strings.HasPrefix(canonical, githubRawBase+"/") {
		return "", nil
	}
	base, err := providerhttp.EnvBaseURL("TERM_LLM_GITHUB_RAW_BASE_URL", githubRawBase)
	if err != nil {
		return "", err
	}
	return base + strings.TrimPrefix(canonical, githubRawBase), nil
}
