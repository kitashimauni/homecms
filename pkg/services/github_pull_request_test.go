package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"hugo-cms/pkg/config"
)

func TestCreateDraftPreviewPullRequestCreatesDraftPR(t *testing.T) {
	repo := t.TempDir()
	runGitForPullRequestTest(t, repo, "init")
	runGitForPullRequestTest(t, repo, "remote", "add", "origin", "https://github.com/example/site.git")

	var posted map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"html_url":"https://github.com/example/site/pull/42","head":{"sha":"` + testCommitSHA + `"}}`))
	}))
	defer server.Close()

	runtime := config.SiteRuntime{ID: "site", RepoPath: repo, GitRemote: "origin", GitBranch: "main"}
	state := DraftPreviewState{
		SiteID: "site", DraftID: "draft-1", Branch: "cms-preview/draft-1",
		ArticlePath: "posts/hello.md", Paths: []string{"content/posts/hello.md"},
		CommitSHA: testCommitSHA, Status: PreviewDeploymentReady,
		URL: "https://abc.pages.dev", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	got, err := createDraftPreviewPullRequest(context.Background(), runtime, "test-token", state, githubPullRequestClient{baseURL: server.URL, httpClient: server.Client()})
	if err != nil {
		t.Fatalf("createDraftPreviewPullRequest() error = %v", err)
	}
	if got != "https://github.com/example/site/pull/42" {
		t.Fatalf("URL = %q", got)
	}
	if posted["head"] != state.Branch || posted["base"] != "main" || posted["draft"] != true {
		t.Fatalf("payload = %#v", posted)
	}
}

func TestCreateDraftPreviewPullRequestRejectsMismatchedExistingOrCreatedHead(t *testing.T) {
	for _, existing := range []bool{true, false} {
		t.Run(map[bool]string{true: "existing", false: "created"}[existing], func(t *testing.T) {
			repo := t.TempDir()
			runGitForPullRequestTest(t, repo, "init")
			runGitForPullRequestTest(t, repo, "remote", "add", "origin", "https://github.com/example/site.git")
			postCalled := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet {
					if existing {
						_, _ = w.Write([]byte(`[{"html_url":"https://github.com/example/site/pull/42","head":{"sha":"ffffffffffffffffffffffffffffffffffffffff"}}]`))
					} else {
						_, _ = w.Write([]byte(`[]`))
					}
					return
				}
				postCalled = true
				_, _ = w.Write([]byte(`{"html_url":"https://github.com/example/site/pull/42","head":{"sha":"ffffffffffffffffffffffffffffffffffffffff"}}`))
			}))
			defer server.Close()

			runtime := config.SiteRuntime{ID: "site", RepoPath: repo, GitRemote: "origin", GitBranch: "main"}
			now := time.Now()
			state := DraftPreviewState{SiteID: "site", DraftID: "draft-1", ArticlePath: "posts/hello.md", Paths: []string{"content/posts/hello.md"}, Branch: "cms-preview/draft-1", CommitSHA: testCommitSHA, Status: PreviewDeploymentReady, URL: "https://abc.pages.dev", CreatedAt: now, UpdatedAt: now}
			_, err := createDraftPreviewPullRequest(context.Background(), runtime, "test-token", state, githubPullRequestClient{baseURL: server.URL, httpClient: server.Client()})
			if !errors.Is(err, ErrDraftPreviewBranchMoved) {
				t.Fatalf("error = %v, want branch moved", err)
			}
			if postCalled == existing {
				t.Fatalf("postCalled = %v, existing = %v", postCalled, existing)
			}
		})
	}
}

func TestCreateArticlePullRequestDoesNotRequirePreviewURL(t *testing.T) {
	repo := t.TempDir()
	runGitForPullRequestTest(t, repo, "init")
	runGitForPullRequestTest(t, repo, "remote", "add", "origin", "https://github.com/example/site.git")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[]`))
			return
		}
		_, _ = w.Write([]byte(`{"html_url":"https://github.com/example/site/pull/108","head":{"sha":"` + testCommitSHA + `"}}`))
	}))
	defer server.Close()

	runtime := config.SiteRuntime{ID: "site", RepoPath: repo, GitRemote: "origin", GitBranch: "main"}
	state := DraftPreviewState{SiteID: "site", DraftID: "draft-direct", Branch: "cms-preview/draft-direct", ArticlePath: "posts/hello.md", Paths: []string{"content/posts/hello.md"}, CommitSHA: testCommitSHA, Status: PreviewDeploymentReady}
	got, err := createArticlePullRequest(context.Background(), runtime, "test-token", state, githubPullRequestClient{baseURL: server.URL, httpClient: server.Client()})
	if err != nil {
		t.Fatalf("createArticlePullRequest() error = %v", err)
	}
	if got != "https://github.com/example/site/pull/108" {
		t.Fatalf("URL = %q", got)
	}
}

func runGitForPullRequestTest(t *testing.T, directory string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = filepath.Clean(directory)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
