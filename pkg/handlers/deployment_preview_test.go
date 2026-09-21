package handlers

import (
	"encoding/json"
	"errors"
	"hugo-cms/pkg/config"
	"hugo-cms/pkg/services"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestDeploymentDraftPathsIncludesOnlyReferencedMedia(t *testing.T) {
	repo := t.TempDir()
	runtime := config.SiteRuntime{RepoPath: repo, ContentDir: "content", StaticDir: "static"}
	writeDeploymentTestFile(t, filepath.Join(repo, "content", "posts", "hello.md"), "---\ntitle: Hello\n---\n![hero](/images/hero.png)\n![local](local.webp)\n")
	writeDeploymentTestFile(t, filepath.Join(repo, "static", "images", "hero.png"), "image")
	writeDeploymentTestFile(t, filepath.Join(repo, "static", "images", "unrelated.png"), "image")
	writeDeploymentTestFile(t, filepath.Join(repo, "content", "posts", "local.webp"), "image")

	got, err := deploymentDraftPaths(runtime, "posts/hello.md")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"content/posts/hello.md", "static/images/hero.png", "content/posts/local.webp"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("deploymentDraftPaths() = %#v, want %#v", got, want)
	}
}

func TestDeploymentStateResponseOnlyLinksFailedCloudflareDeploymentLogs(t *testing.T) {
	now := time.Now().UTC()
	runtime := config.SiteRuntime{PreviewDeployment: config.DeploymentPreviewConfig{
		Provider:        "cloudflare_pages",
		CloudflarePages: config.CloudflarePagesConfig{AccountID: "account", ProjectName: "project"},
	}}
	state := services.DraftPreviewState{DeploymentID: "deployment", Status: services.PreviewDeploymentFailed, CreatedAt: now, UpdatedAt: now}
	response := deploymentStateResponse(runtime, state)
	if response["log_url"] != "https://dash.cloudflare.com/account/pages/view/project/deployment" {
		t.Fatalf("log_url = %q", response["log_url"])
	}
	state.Status = services.PreviewDeploymentBuilding
	response = deploymentStateResponse(runtime, state)
	if _, ok := response["log_url"]; ok {
		t.Fatal("building deployment unexpectedly exposed a log link")
	}
}

func TestHandleDeploymentErrorReturnsConflictForPublishInvariantFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, err := range []error{
		services.ErrDraftPreviewArticleMismatch,
		services.ErrDraftPreviewNotReady,
		services.ErrDraftPreviewStale,
		services.ErrDraftPreviewBranchMoved,
	} {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		handleDeploymentError(context, "publish", err)
		if recorder.Code != http.StatusConflict {
			t.Fatalf("error %v status = %d, want 409", err, recorder.Code)
		}
	}
}

func TestHandlePublishErrorClassifiesSafeFailureReasons(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		err        error
		statusCode int
		code       string
		message    string
	}{
		{name: "stale preview", err: services.ErrDraftPreviewStale, statusCode: http.StatusConflict, code: publishPreviewStaleCode, message: "stale"},
		{name: "branch moved", err: services.ErrDraftPreviewBranchMoved, statusCode: http.StatusConflict, code: publishBranchMovedCode, message: "branch"},
		{name: "git push", err: errors.New("push draft branch: remote rejected secret details"), statusCode: http.StatusBadGateway, code: publishGitPushCode, message: "branch push"},
		{name: "pull request", err: errors.New("GitHub API returned HTTP 500 with token details"), statusCode: http.StatusBadGateway, code: publishPullRequestCode, message: "pull request"},
		{name: "token", err: errors.New("GitHub token is required"), statusCode: http.StatusUnauthorized, code: ErrCodeUnauthorized, message: "session token"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			context, _ := gin.CreateTestContext(recorder)
			handleDeploymentError(context, "publish", test.err)

			if recorder.Code != test.statusCode {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, test.statusCode, recorder.Body.String())
			}
			var response APIError
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Code != test.code || !strings.Contains(strings.ToLower(response.Message), test.message) {
				t.Fatalf("response = %#v, want code %q and message containing %q", response, test.code, test.message)
			}
			if strings.Contains(strings.ToLower(response.Message), "secret") || strings.Contains(strings.ToLower(response.Message), "token details") {
				t.Fatalf("response leaked internal details: %#v", response)
			}
		})
	}
}

func writeDeploymentTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
