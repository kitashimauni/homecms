package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"hugo-cms/pkg/config"
	"hugo-cms/pkg/services"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func setupPreviewDegradationRuntime(t *testing.T, generator string) config.SiteRuntime {
	t.Helper()
	restoreSiteScopeConfig(t)

	repoPath := t.TempDir()
	site := config.SiteConfig{
		ID:              "default",
		RepoPath:        repoPath,
		Generator:       generator,
		ContentDir:      "content",
		StaticDir:       "static",
		PublicDir:       "public",
		StaticMediaDir:  "images",
		ArticleMediaDir: "images",
		PreviewURL:      "/",
		HugoServerBind:  "127.0.0.1",
		HugoServerPort:  "1314",
	}
	config.DefaultSiteID = site.ID
	config.Sites = []config.SiteConfig{site}

	return config.NewSiteRuntime(site)
}

func stubPreviewMutationFailures(t *testing.T, syncCalls *int) {
	t.Helper()
	originalInvalidate := invalidateLocalPreviewArticleURL
	originalSync := syncLocalPreviewContentResourceForMutation
	t.Cleanup(func() {
		invalidateLocalPreviewArticleURL = originalInvalidate
		syncLocalPreviewContentResourceForMutation = originalSync
	})

	invalidateLocalPreviewArticleURL = func(config.SiteRuntime, ...string) error {
		return errors.New("local preview returned 503")
	}
	syncLocalPreviewContentResourceForMutation = func(config.SiteRuntime, string, bool, bool) error {
		*syncCalls = *syncCalls + 1
		return errors.New("local preview workspace unavailable")
	}
}

func issue82DeleteMediaRequest(t *testing.T, repoPath string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"repo_path": repoPath})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/admin/api/media", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	DeleteMedia(ctx)
	return recorder
}

func TestIssue82PreviewFailureDoesNotAbortArticleDeletion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, generator := range []string{"hugo", "eleventy"} {
		t.Run(generator, func(t *testing.T) {
			runtime := setupPreviewDegradationRuntime(t, generator)
			articlePath := filepath.Join(runtime.RepoPath, runtime.ContentDir, "posts", "article.md")
			if err := os.MkdirAll(filepath.Dir(articlePath), 0755); err != nil {
				t.Fatalf("create article directory: %v", err)
			}
			articleBody := []byte("---\ntitle: Article\n---\nbody\n")
			if err := os.WriteFile(articlePath, articleBody, 0644); err != nil {
				t.Fatalf("write article: %v", err)
			}

			var syncCalls int
			stubPreviewMutationFailures(t, &syncCalls)
			body, err := json.Marshal(map[string]string{
				"path":          "posts/article.md",
				"base_revision": services.ArticleRevision(articleBody),
			})
			if err != nil {
				t.Fatalf("encode delete request: %v", err)
			}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/admin/api/delete", bytes.NewReader(body))
			ctx.Request.Header.Set("Content-Type", "application/json")

			DeleteArticle(ctx)

			if recorder.Code != http.StatusOK {
				t.Fatalf("DeleteArticle() status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if _, err := os.Stat(articlePath); !os.IsNotExist(err) {
				t.Fatalf("article still exists after successful production delete, stat error = %v", err)
			}
			var response map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response["local_preview_sync"] != false {
				t.Fatalf("local_preview_sync = %#v, want false", response["local_preview_sync"])
			}
			if syncCalls != 1 {
				t.Fatalf("preview sync calls = %d, want 1 after the single production mutation", syncCalls)
			}
		})
	}
}

func TestIssue82PreviewFailureDoesNotAbortMediaMutations(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, generator := range []string{"hugo", "eleventy"} {
		t.Run(generator, func(t *testing.T) {
			runtime := setupPreviewDegradationRuntime(t, generator)
			var syncCalls int
			stubPreviewMutationFailures(t, &syncCalls)

			var uploadBody bytes.Buffer
			writer := multipart.NewWriter(&uploadBody)
			part, err := writer.CreateFormFile("file", "preview.png")
			if err != nil {
				t.Fatalf("create upload part: %v", err)
			}
			if _, err := part.Write([]byte{
				0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
			}); err != nil {
				t.Fatalf("write upload: %v", err)
			}
			if err := writer.WriteField("mode", "static"); err != nil {
				t.Fatalf("write mode: %v", err)
			}
			if err := writer.Close(); err != nil {
				t.Fatalf("close upload: %v", err)
			}

			uploadRecorder := httptest.NewRecorder()
			uploadCtx, _ := gin.CreateTestContext(uploadRecorder)
			uploadCtx.Request = httptest.NewRequest(http.MethodPost, "/admin/api/media/upload", &uploadBody)
			uploadCtx.Request.Header.Set("Content-Type", writer.FormDataContentType())
			UploadMedia(uploadCtx)

			if uploadRecorder.Code != http.StatusOK {
				t.Fatalf("UploadMedia() status = %d, body = %s", uploadRecorder.Code, uploadRecorder.Body.String())
			}
			var uploadResponse struct {
				RepoPath         string `json:"repo_path"`
				LocalPreviewSync bool   `json:"local_preview_sync"`
			}
			if err := json.Unmarshal(uploadRecorder.Body.Bytes(), &uploadResponse); err != nil {
				t.Fatalf("decode upload response: %v", err)
			}
			if uploadResponse.LocalPreviewSync {
				t.Fatal("UploadMedia() reported a successful Local Preview sync after a forced failure")
			}
			uploadedPath := filepath.Join(runtime.RepoPath, filepath.FromSlash(uploadResponse.RepoPath))
			if _, err := os.Stat(uploadedPath); err != nil {
				t.Fatalf("uploaded production media is missing: %v", err)
			}

			deleteRecorder := issue82DeleteMediaRequest(t, uploadResponse.RepoPath)
			if deleteRecorder.Code != http.StatusOK {
				t.Fatalf("DeleteMedia() status = %d, body = %s", deleteRecorder.Code, deleteRecorder.Body.String())
			}
			var deleteResponse map[string]any
			if err := json.Unmarshal(deleteRecorder.Body.Bytes(), &deleteResponse); err != nil {
				t.Fatalf("decode delete response: %v", err)
			}
			if deleteResponse["local_preview_sync"] != false {
				t.Fatalf("DeleteMedia() local_preview_sync = %#v, want false", deleteResponse["local_preview_sync"])
			}
			if _, err := os.Stat(uploadedPath); !os.IsNotExist(err) {
				t.Fatalf("production media still exists after successful delete, stat error = %v", err)
			}
			if syncCalls != 2 {
				t.Fatalf("preview sync calls = %d, want one per production mutation", syncCalls)
			}
		})
	}
}
