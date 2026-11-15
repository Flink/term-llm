package skills

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAncillaryConfiguredRoots(t *testing.T) {
	const query = "x & café?#"
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		switch r.URL.Path {
		case "/proxy/api/repos/owner/repo/contents/skills":
			if r.URL.Query().Get("ref") != "branch&special" {
				t.Errorf("ref=%q", r.URL.RawQuery)
			}
			if r.Header.Get("Authorization") != "Bearer fake-token" {
				t.Error("missing GitHub credential")
			}
			fmt.Fprint(w, `[]`)
		case "/proxy/raw/owner/repo/main/skills/example/SKILL.md":
			if r.Header.Get("Authorization") != "" {
				t.Error("GitHub token sent to raw content origin")
			}
			fmt.Fprint(w, "# Example")
		case "/proxy/registry/skills/search":
			if r.URL.Query().Get("q") != query {
				t.Errorf("q=%q", r.URL.RawQuery)
			}
			if r.Header.Get("Authorization") != "Bearer registry-key" {
				t.Error("missing registry credential")
			}
			fmt.Fprint(w, `{"success":true,"data":{"skills":[]}}`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL+"/proxy/api/")
	t.Setenv("TERM_LLM_GITHUB_RAW_BASE_URL", server.URL+"/proxy/raw/")
	t.Setenv("SKILLSMP_BASE_URL", server.URL+"/proxy/registry/")
	t.Setenv("GITHUB_TOKEN", "fake-token")
	client := NewGitHubClient()
	if _, err := client.fetchContents(context.Background(), server.URL+"/proxy/api/repos/owner/repo/contents/skills?ref=branch%26special"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.countFiles(context.Background(), GitHubRepoRef{Owner: "owner", Repo: "repo"}, "skills", "branch&special"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchSkillMD(context.Background(), DiscoveredSkill{Path: "skills/example", RepoRef: GitHubRepoRef{Owner: "owner", Repo: "repo", Branch: "main"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRemoteRegistryClientWithKey("registry-key").Search(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls=%v", calls)
	}
}

func TestDiscoverSkillsEscapesBranchOnce(t *testing.T) {
	const branch = "feature/search&more"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if got := r.URL.Query().Get("ref"); got != branch {
			t.Errorf("ref=%q, want %q", got, branch)
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/proxy/repos/owner/repo/contents/skills":
			fmt.Fprint(w, `[{"name":"example","path":"skills/example","type":"dir"}]`)
		case "/proxy/repos/owner/repo/contents/skills/example/SKILL.md":
			if r.Method != http.MethodHead {
				t.Errorf("method=%s, want HEAD", r.Method)
			}
		case "/proxy/repos/owner/repo/contents/skills/example":
			fmt.Fprint(w, `[{"type":"file"},{"type":"dir","path":"skills/example/nested"}]`)
		case "/proxy/repos/owner/repo/contents/skills/example/nested":
			fmt.Fprint(w, `[{"type":"file"}]`)
		default:
			t.Errorf("unexpected request %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("GITHUB_API_URL", server.URL+"/proxy/")
	skills, err := NewGitHubClient().DiscoverSkills(context.Background(), GitHubRepoRef{
		Owner: "owner", Repo: "repo", Path: "skills", Branch: branch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 || skills[0].FileCount != 2 || skills[0].RepoRef.Branch != branch {
		t.Fatalf("skills=%+v", skills)
	}
	if calls != 4 {
		t.Fatalf("requests=%d, want 4", calls)
	}
}
