package handlers

import (
	"bytes"
	"encoding/json"
	"hugo-cms/pkg/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestDeleteMedia_PathValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		body           map[string]string
		expectedStatus int
		expectedCode   string
	}{
		{
			name:           "Empty repo_path",
			body:           map[string]string{"repo_path": ""},
			expectedStatus: http.StatusBadRequest,
			expectedCode:   ErrCodeBadRequest,
		},
		{
			name:           "Path traversal attempt",
			body:           map[string]string{"repo_path": "../../../etc/passwd"},
			expectedStatus: http.StatusBadRequest,
			expectedCode:   ErrCodeBadRequest,
		},
		{
			name:           "Invalid directory - not static or content",
			body:           map[string]string{"repo_path": "themes/file.jpg"},
			expectedStatus: http.StatusBadRequest,
			expectedCode:   ErrCodeBadRequest,
		},
		{
			name:           "Valid static path (file may not exist)",
			body:           map[string]string{"repo_path": "static/images/test.jpg"},
			expectedStatus: http.StatusInternalServerError, // File doesn't exist
			expectedCode:   ErrCodeInternalError,
		},
		{
			name:           "Valid content path (file may not exist)",
			body:           map[string]string{"repo_path": "content/posts/image.jpg"},
			expectedStatus: http.StatusInternalServerError, // File doesn't exist
			expectedCode:   ErrCodeInternalError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			body, _ := json.Marshal(tt.body)
			c.Request = httptest.NewRequest("DELETE", "/api/media", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")

			DeleteMedia(c)

			if w.Code != tt.expectedStatus {
				t.Errorf("DeleteMedia() status = %d, want %d", w.Code, tt.expectedStatus)
			}

			var resp APIError
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil {
				if resp.Code != tt.expectedCode {
					t.Errorf("DeleteMedia() code = %q, want %q", resp.Code, tt.expectedCode)
				}
			}
		})
	}
}

func TestDeleteMedia_InvalidJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Request = httptest.NewRequest("DELETE", "/api/media", bytes.NewReader([]byte("invalid json")))
	c.Request.Header.Set("Content-Type", "application/json")

	DeleteMedia(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("DeleteMedia() with invalid JSON status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}

func TestDeleteMedia_ArticleMediaUsesArticleContextAndSyncsPreview(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreSiteScopeConfig(t)
	repoPath := t.TempDir()
	writeHandlerTestFile(t, filepath.Join(repoPath, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: posts
      folder: content/posts
      media_folder: "{{dirname}}/images"
`)
	articlePath := "posts/20260608/takao.md"
	repoMediaPath := "content/posts/20260608/images/photo.jpg"
	writeHandlerTestFile(t, filepath.Join(repoPath, "content", articlePath), "---\ntitle: Takao\n---\n")
	writeHandlerTestFile(t, filepath.Join(repoPath, filepath.FromSlash(repoMediaPath)), "image")
	config.DefaultSiteID = "default"
	config.Sites = []config.SiteConfig{{
		ID:         "default",
		RepoPath:   repoPath,
		Generator:  "eleventy",
		ContentDir: "content",
		StaticDir:  "static",
		PublicDir:  "_site",
	}}

	originalInvalidate := invalidateLocalPreviewArticleURL
	originalSync := syncLocalPreviewContentResourceForMutation
	var invalidated, synced bool
	var syncedPath string
	invalidateLocalPreviewArticleURL = func(config.SiteRuntime, ...string) error {
		invalidated = true
		return nil
	}
	syncLocalPreviewContentResourceForMutation = func(_ config.SiteRuntime, path string, deleted, invalidateMetadata bool) error {
		synced = deleted && invalidateMetadata
		syncedPath = path
		return nil
	}
	t.Cleanup(func() {
		invalidateLocalPreviewArticleURL = originalInvalidate
		syncLocalPreviewContentResourceForMutation = originalSync
	})

	body, err := json.Marshal(map[string]string{
		"repo_path":    repoMediaPath,
		"article_path": articlePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/media/delete?site=default", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	DeleteMedia(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("DeleteMedia() status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(filepath.Join(repoPath, filepath.FromSlash(repoMediaPath))); !os.IsNotExist(err) {
		t.Fatalf("Article Media file still exists, stat error = %v", err)
	}
	if !invalidated || !synced || syncedPath != repoMediaPath {
		t.Fatalf("preview hooks = invalidated:%v synced:%v path:%q", invalidated, synced, syncedPath)
	}
}

func TestListMedia_Modes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		mode string
		path string
	}{
		{
			name: "Static mode",
			mode: "static",
			path: "",
		},
		{
			name: "Content mode without path",
			mode: "content",
			path: "",
		},
		{
			name: "Content mode with path",
			mode: "content",
			path: "posts/test/index.md",
		},
		{
			name: "Empty mode",
			mode: "",
			path: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			url := "/api/media?mode=" + tt.mode
			if tt.path != "" {
				url += "&path=" + tt.path
			}
			c.Request = httptest.NewRequest("GET", url, nil)

			// Set query params manually since CreateTestContext doesn't parse URL
			c.Params = gin.Params{}
			c.Request.URL.RawQuery = "mode=" + tt.mode + "&path=" + tt.path

			ListMedia(c)

			// Should return 200 or 500 depending on config
			// Main test is that it doesn't panic
			t.Logf("ListMedia(%s, %s) returned status %d", tt.mode, tt.path, w.Code)
		})
	}
}

func TestServeMediaRaw_PathValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		path           string
		expectedStatus int
	}{
		{
			name:           "Empty path",
			path:           "",
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "Path traversal attempt",
			path:           "../../../etc/passwd",
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "Repository metadata",
			path:           ".git/config",
			expectedStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			c.Request = httptest.NewRequest("GET", "/api/media/raw?path="+tt.path, nil)
			c.Request.URL.RawQuery = "path=" + tt.path

			ServeMediaRaw(c)

			if w.Code != tt.expectedStatus {
				t.Errorf("ServeMediaRaw() status = %d, want %d", w.Code, tt.expectedStatus)
			}
		})
	}
}

func TestServeMediaRaw_AllowedMedia(t *testing.T) {
	originalRepoPath := config.RepoPath
	t.Cleanup(func() { config.RepoPath = originalRepoPath })

	config.RepoPath = t.TempDir()
	mediaPath := filepath.Join(config.RepoPath, "static", "images", "photo.png")
	if err := os.MkdirAll(filepath.Dir(mediaPath), 0755); err != nil {
		t.Fatalf("create media directory: %v", err)
	}
	if err := os.WriteFile(mediaPath, []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	}, 0644); err != nil {
		t.Fatalf("write media file: %v", err)
	}

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/api/media/raw?path=static/images/photo.png", nil)

	ServeMediaRaw(c)

	if recorder.Code != http.StatusOK {
		t.Fatalf("ServeMediaRaw() status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestUploadMedia_NoFile(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	c.Request = httptest.NewRequest("POST", "/api/media/upload", nil)
	c.Request.Header.Set("Content-Type", "multipart/form-data")

	UploadMedia(c)

	if w.Code != http.StatusBadRequest {
		t.Errorf("UploadMedia() without file status = %d, want %d", w.Code, http.StatusBadRequest)
	}

	var resp APIError
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err == nil {
		if resp.Code != ErrCodeBadRequest {
			t.Errorf("UploadMedia() code = %q, want %q", resp.Code, ErrCodeBadRequest)
		}
	}
}

func writeHandlerTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
