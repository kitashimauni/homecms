package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hugo-cms/pkg/config"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

const githubAPIBaseURL = "https://api.github.com"

type githubPullRequestClient struct {
	baseURL    string
	httpClient *http.Client
}

type githubPullRequest struct {
	HTMLURL string `json:"html_url"`
	Head    struct {
		SHA string `json:"sha"`
	} `json:"head"`
}

func CreateDraftPreviewPullRequest(ctx context.Context, runtime config.SiteRuntime, token string, state DraftPreviewState) (string, error) {
	return createDraftPreviewPullRequest(ctx, runtime, token, state, githubPullRequestClient{
		baseURL:    githubAPIBaseURL,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	})
}

// CreateArticlePullRequest creates a draft PR for the current article without
// requiring a Deployment Preview provider or URL.
func CreateArticlePullRequest(ctx context.Context, runtime config.SiteRuntime, token string, state DraftPreviewState) (string, error) {
	return createArticlePullRequest(ctx, runtime, token, state, githubPullRequestClient{
		baseURL:    githubAPIBaseURL,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	})
}

func createDraftPreviewPullRequest(ctx context.Context, runtime config.SiteRuntime, token string, state DraftPreviewState, client githubPullRequestClient) (string, error) {
	if state.Status != PreviewDeploymentReady || state.URL == "" {
		return "", fmt.Errorf("draft preview must be ready before creating a pull request")
	}
	return createArticlePullRequest(ctx, runtime, token, state, client)
}

func createArticlePullRequest(ctx context.Context, runtime config.SiteRuntime, token string, state DraftPreviewState, client githubPullRequestClient) (string, error) {
	if strings.TrimSpace(token) == "" {
		return "", fmt.Errorf("GitHub token is required")
	}
	if state.SiteID != runtime.ID || state.Branch != previewBranchPrefix+state.DraftID {
		return "", fmt.Errorf("draft preview does not belong to the selected site")
	}
	if err := validateCommitSHA(state.CommitSHA); err != nil {
		return "", err
	}
	remoteURL, err := readRawRemoteURL(ctx, runtime.RepoPath, runtime.GitRemote)
	if err != nil {
		return "", err
	}
	owner, repository, err := githubRepository(remoteURL)
	if err != nil {
		return "", err
	}
	if client.httpClient == nil {
		return "", fmt.Errorf("GitHub HTTP client is required")
	}

	baseURL := strings.TrimRight(client.baseURL, "/")
	requestURL := baseURL + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repository) + "/pulls"
	headQuery := owner + ":" + state.Branch
	query := url.Values{"state": {"open"}, "head": {headQuery}, "base": {runtime.GitBranch}, "per_page": {"1"}}
	var existing []githubPullRequest
	if err := githubJSONRequest(ctx, client.httpClient, token, http.MethodGet, requestURL+"?"+query.Encode(), nil, &existing); err != nil {
		return "", err
	}
	if len(existing) > 0 {
		return validateGitHubPullRequest(existing[0], state.CommitSHA)
	}

	titlePath := strings.TrimSpace(state.ArticlePath)
	if titlePath == "" {
		titlePath = state.DraftID
	}
	title := "HomeCMS Publish: " + titlePath
	body := fmt.Sprintf("HomeCMSから現在の編集内容をPublishした変更です。\n\n- Commit: `%s`", state.CommitSHA)
	if state.URL != "" {
		title = "HomeCMS preview: " + titlePath
		body = fmt.Sprintf("HomeCMSのデプロイプレビューで確認した変更です。\n\n- Commit: `%s`\n- Preview: %s\n\nRefs #30", state.CommitSHA, state.URL)
	}
	payload := map[string]interface{}{
		"title":                 title,
		"head":                  state.Branch,
		"base":                  runtime.GitBranch,
		"maintainer_can_modify": true,
		"draft":                 true,
		"body":                  body,
	}
	var created githubPullRequest
	if err := githubJSONRequest(ctx, client.httpClient, token, http.MethodPost, requestURL, payload, &created); err != nil {
		return "", err
	}
	return validateGitHubPullRequest(created, state.CommitSHA)
}

func githubRepository(remoteURL string) (string, string, error) {
	parsed, err := url.Parse(strings.TrimSpace(remoteURL))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil {
		return "", "", fmt.Errorf("Git remote must be an HTTPS github.com repository")
	}
	cleaned := strings.Trim(path.Clean(parsed.Path), "/")
	parts := strings.Split(cleaned, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("GitHub repository path must contain owner and repository")
	}
	repository := strings.TrimSuffix(parts[1], ".git")
	if parts[0] == "" || repository == "" {
		return "", "", fmt.Errorf("GitHub repository path is incomplete")
	}
	return parts[0], repository, nil
}

func githubJSONRequest(ctx context.Context, client *http.Client, token, method, requestURL string, payload interface{}, result interface{}) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("encode GitHub request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return fmt.Errorf("create GitHub request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("GitHub API request failed")
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, 2<<20)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, limited)
		return fmt.Errorf("GitHub API returned HTTP %d", response.StatusCode)
	}
	if result != nil {
		if err := json.NewDecoder(limited).Decode(result); err != nil {
			return fmt.Errorf("decode GitHub API response: %w", err)
		}
	}
	return nil
}

func validateGitHubPullRequestURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.User != nil {
		return "", fmt.Errorf("GitHub returned an invalid pull request URL")
	}
	return parsed.String(), nil
}

func validateGitHubPullRequest(pullRequest githubPullRequest, expectedCommit string) (string, error) {
	if !strings.EqualFold(pullRequest.Head.SHA, expectedCommit) {
		return "", ErrDraftPreviewBranchMoved
	}
	return validateGitHubPullRequestURL(pullRequest.HTMLURL)
}
