package handlers

import (
	"bytes"
	"encoding/json"
	"hugo-cms/pkg/config"
	"hugo-cms/pkg/services"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func runArticleResetGit(t *testing.T, repoPath string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
}

func TestResetArticleRestoresHEADAndSynchronizesSelectedSite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreSiteScopeConfig(t)

	repoPath := t.TempDir()
	articlePath := filepath.Join(repoPath, "content", "posts", "article.md")
	mediaPath := filepath.Join(repoPath, "content", "posts", "image.jpg")
	if err := os.MkdirAll(filepath.Dir(articlePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(articlePath, []byte("HEAD article\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runArticleResetGit(t, repoPath, "init")
	runArticleResetGit(t, repoPath, "config", "user.email", "test@example.com")
	runArticleResetGit(t, repoPath, "config", "user.name", "HomeCMS Test")
	runArticleResetGit(t, repoPath, "add", "content/posts/article.md")
	runArticleResetGit(t, repoPath, "commit", "-m", "initial article")

	if err := os.WriteFile(articlePath, []byte("local unpublished article\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mediaPath, []byte("article media\n"), 0644); err != nil {
		t.Fatal(err)
	}

	site := config.SiteConfig{
		ID:         "docs",
		RepoPath:   repoPath,
		Generator:  "hugo",
		ContentDir: "content",
		StaticDir:  "static",
		PublicDir:  "public",
	}
	config.DefaultSiteID = site.ID
	config.Sites = []config.SiteConfig{site}

	var invalidated bool
	var syncedPath string
	var syncedDeleted, syncedMetadata bool
	originalInvalidate := invalidateLocalPreviewArticleURL
	originalSync := syncLocalPreviewContentResourceForMutation
	invalidateLocalPreviewArticleURL = func(runtime config.SiteRuntime, paths ...string) error {
		invalidated = runtime.ID == "docs" && len(paths) == 1 && paths[0] == "posts/article.md"
		return nil
	}
	syncLocalPreviewContentResourceForMutation = func(runtime config.SiteRuntime, path string, deleted, invalidateMetadata bool) error {
		if runtime.ID == "docs" {
			syncedPath = path
			syncedDeleted = deleted
			syncedMetadata = invalidateMetadata
		}
		return nil
	}
	t.Cleanup(func() {
		invalidateLocalPreviewArticleURL = originalInvalidate
		syncLocalPreviewContentResourceForMutation = originalSync
	})

	body, err := json.Marshal(map[string]string{"path": "posts/article.md"})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/admin/api/article/reset?site=docs", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")

	ResetArticle(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("ResetArticle() status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	content, err := os.ReadFile(articlePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "HEAD article\n" {
		t.Fatalf("article after reset = %q, want HEAD content", content)
	}
	media, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(media) != "article media\n" {
		t.Fatalf("media after reset = %q, want unchanged media", media)
	}
	if !invalidated || syncedPath != "content/posts/article.md" || syncedDeleted || !syncedMetadata {
		t.Fatalf("preview hooks = invalidated:%v path:%q deleted:%v metadata:%v", invalidated, syncedPath, syncedDeleted, syncedMetadata)
	}

	var response struct {
		Status           string `json:"status"`
		Deleted          bool   `json:"deleted"`
		Revision         string `json:"revision"`
		LocalPreviewSync bool   `json:"local_preview_sync"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "restored" || response.Deleted || !response.LocalPreviewSync {
		t.Fatalf("reset response = %#v, want restored response", response)
	}
	if want := services.ArticleRevision([]byte("HEAD article\n")); response.Revision != want {
		t.Fatalf("reset revision = %q, want %q", response.Revision, want)
	}
}
