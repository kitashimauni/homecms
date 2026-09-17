package services

import (
	"bytes"
	"errors"
	"fmt"
	"hugo-cms/pkg/config"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testFileHeader(t *testing.T, filename string, content []byte) *multipart.FileHeader {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile() error: %v", err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatalf("write multipart content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	request := httptest.NewRequest("POST", "/", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if err := request.ParseMultipartForm(int64(body.Len())); err != nil {
		t.Fatalf("ParseMultipartForm() error: %v", err)
	}
	return request.MultipartForm.File["file"][0]
}

func withMediaConfig(t *testing.T, repoPath string) config.SiteRuntime {
	t.Helper()

	originalMaxSize := config.MaxUploadSize
	t.Cleanup(func() {
		config.MaxUploadSize = originalMaxSize
	})

	config.MaxUploadSize = 1024 * 1024
	return config.NewSiteRuntime(config.SiteConfig{
		ID:              "test",
		RepoPath:        repoPath,
		Generator:       "hugo",
		ContentDir:      "content",
		StaticDir:       "static",
		PublicDir:       "public",
		PreviewURL:      "/",
		HugoServerBind:  "127.0.0.1",
		HugoServerPort:  "1314",
		StaticMediaDir:  "images",
		ArticleMediaDir: "images",
	})
}

func TestListMediaFiles(t *testing.T) {
	repoPath := t.TempDir()
	runtime := withMediaConfig(t, repoPath)

	staticDir := filepath.Join(repoPath, "static", "images")
	if err := os.MkdirAll(staticDir, 0755); err != nil {
		t.Fatalf("create static directory: %v", err)
	}

	for _, name := range []string{"photo.jpg", "clip.mp4", "document.pdf", "script.svg"} {
		if err := os.WriteFile(filepath.Join(staticDir, name), []byte("test"), 0644); err != nil {
			t.Fatalf("create test media %q: %v", name, err)
		}
	}

	files, err := ListMediaFilesForRuntime(runtime, "static", "")
	if err != nil {
		t.Fatalf("ListMediaFiles() unexpected error: %v", err)
	}
	if len(files) != 1 || files[0].Name != "photo.jpg" {
		t.Fatalf("ListMediaFiles() = %#v, want only photo.jpg", files)
	}

	t.Run("Content mode requires article path", func(t *testing.T) {
		files, err := ListMediaFilesForRuntime(runtime, "content", "")
		// Should return empty without error since articlePath is empty
		if err != nil {
			t.Errorf("ListMediaFiles() unexpected error: %v", err)
		}
		if len(files) != 0 {
			t.Errorf("ListMediaFiles() expected empty result for content mode without path")
		}
	})
}

func TestMediaFileStruct(t *testing.T) {
	mf := MediaFile{
		Name:     "test.jpg",
		Path:     "/images/test.jpg",
		Size:     1024,
		URL:      "/admin/api/media/raw?path=static/images/test.jpg",
		RepoPath: "static/images/test.jpg",
	}

	if mf.Name != "test.jpg" {
		t.Errorf("MediaFile.Name = %q, want %q", mf.Name, "test.jpg")
	}
	if mf.Size != 1024 {
		t.Errorf("MediaFile.Size = %d, want %d", mf.Size, 1024)
	}
}

func TestSaveMediaFile_InvalidExtension(t *testing.T) {
	// Create a mock file header - this is tricky without a full request
	// Document expected behavior: should reject files with disallowed extensions
	t.Run("Extension validation", func(t *testing.T) {
		// SVG is intentionally rejected because it can contain executable content.
		allowedExts := []string{".jpg", ".jpeg", ".png", ".gif", ".webp", ".mp4", ".webm", ".pdf"}
		disallowedExts := []string{".svg", ".exe", ".sh", ".bat", ".php", ".html", ".js"}

		for _, ext := range allowedExts {
			t.Logf("Allowed extension: %s", ext)
		}
		for _, ext := range disallowedExts {
			t.Logf("Disallowed extension: %s", ext)
		}
	})
}

func TestDeleteMediaFile(t *testing.T) {
	// Create temp directory and file
	tmpDir, err := os.MkdirTemp("", "delete_media_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.jpg")
	if err := os.WriteFile(testFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Fatal("Test file should exist before deletion")
	}

	// Delete the file
	if err := os.Remove(testFile); err != nil {
		t.Errorf("Failed to delete file: %v", err)
	}

	// Verify file no longer exists
	if _, err := os.Stat(testFile); !os.IsNotExist(err) {
		t.Error("Test file should not exist after deletion")
	}
}

func TestDeleteMediaFile_InvalidPath(t *testing.T) {
	runtime := withMediaConfig(t, t.TempDir())
	// DeleteMediaFile should return error for invalid paths
	err := DeleteMediaFileForRuntime(runtime, "")
	if err == nil {
		t.Error("DeleteMediaFile(\"\") should return error for empty path")
	}

	err = DeleteMediaFileForRuntime(runtime, "../../../etc/passwd")
	if err == nil {
		t.Error("DeleteMediaFile should return error for path traversal attempt")
	}
}

func TestSaveMediaFileRejectsArticlePathTraversal(t *testing.T) {
	baseDir := t.TempDir()
	repoPath := filepath.Join(baseDir, "repo")
	if err := os.MkdirAll(filepath.Join(repoPath, "content"), 0755); err != nil {
		t.Fatalf("create content directory: %v", err)
	}
	runtime := withMediaConfig(t, repoPath)

	header := testFileHeader(t, "image.png", []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	})
	if _, err := SaveMediaFileForRuntime(runtime, header, "content", "../../outside/index.md"); err == nil {
		t.Fatal("SaveMediaFile() should reject an article path outside content")
	}

	if _, err := os.Stat(filepath.Join(baseDir, "outside")); !os.IsNotExist(err) {
		t.Fatalf("outside directory should not be created, stat error = %v", err)
	}
}

func TestSaveMediaFileRejectsMismatchedContent(t *testing.T) {
	repoPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoPath, "static", "images"), 0755); err != nil {
		t.Fatalf("create static directory: %v", err)
	}
	runtime := withMediaConfig(t, repoPath)

	header := testFileHeader(t, "not-an-image.jpg", []byte("%PDF-1.7\n"))
	if _, err := SaveMediaFileForRuntime(runtime, header, "static", ""); err == nil {
		t.Fatal("SaveMediaFile() should reject content that does not match its extension")
	}
}

func TestSaveMediaFileWritesValidatedImage(t *testing.T) {
	repoPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoPath, "static", "images"), 0755); err != nil {
		t.Fatalf("create static directory: %v", err)
	}
	runtime := withMediaConfig(t, repoPath)

	header := testFileHeader(t, "image.png", []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	})
	media, err := SaveMediaFileForRuntime(runtime, header, "static", "")
	if err != nil {
		t.Fatalf("SaveMediaFile() unexpected error: %v", err)
	}
	if !ValidateMediaRepoPathForRuntime(runtime, media.RepoPath) {
		t.Fatalf("SaveMediaFile() returned invalid repository path %q", media.RepoPath)
	}
	if _, err := os.Stat(filepath.Join(repoPath, filepath.FromSlash(media.RepoPath))); err != nil {
		t.Fatalf("saved media file not found: %v", err)
	}
}

func articleMediaTestRuntime(t *testing.T, mediaFolder string) config.SiteRuntime {
	t.Helper()
	repoPath := t.TempDir()
	writeTestFile(t, filepath.Join(repoPath, ".homecms.yml"), fmt.Sprintf(`
version: 1
content:
  collections:
    - name: posts
      folder: content/posts
      media_folder: %q
`, mediaFolder))
	runtime := config.NewSiteRuntime(config.SiteConfig{
		ID:             "test",
		RepoPath:       repoPath,
		Generator:      "eleventy",
		ContentDir:     "content",
		StaticDir:      "static",
		PublicDir:      "_site",
		PreviewURL:     "/",
		HugoServerBind: "127.0.0.1",
		HugoServerPort: "1314",
	})
	originalMaxSize := config.MaxUploadSize
	config.MaxUploadSize = 1024 * 1024
	t.Cleanup(func() { config.MaxUploadSize = originalMaxSize })
	return runtime
}

func TestArticleMediaUsesCollectionFolderForNormalArticle(t *testing.T) {
	runtime := articleMediaTestRuntime(t, "{{dirname}}")
	articlePath := "posts/20260608/takao.md"
	articleDir := filepath.Join(runtime.RepoPath, "content", "posts", "20260608")
	writeTestFile(t, filepath.Join(articleDir, "takao.md"), "---\ntitle: Takao\n---\n")
	writeTestFile(t, filepath.Join(articleDir, "existing.jpg"), "existing image")

	files, err := ListMediaFilesForRuntime(runtime, "content", articlePath)
	if err != nil {
		t.Fatalf("ListMediaFilesForRuntime() error = %v", err)
	}
	if len(files) != 1 || files[0].Path != "existing.jpg" || files[0].RepoPath != "content/posts/20260608/existing.jpg" {
		t.Fatalf("ListMediaFilesForRuntime() = %#v, want article-relative existing.jpg", files)
	}

	header := testFileHeader(t, "image.png", []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	})
	media, err := SaveMediaFileForRuntime(runtime, header, "content", articlePath)
	if err != nil {
		t.Fatalf("SaveMediaFileForRuntime() error = %v", err)
	}
	if !strings.HasPrefix(media.RepoPath, "content/posts/20260608/image_") || media.Path != filepath.Base(media.RepoPath) {
		t.Fatalf("saved media = %#v, want article-directory path", media)
	}
	if !ValidateArticleMediaRepoPathForRuntime(runtime, articlePath, media.RepoPath) {
		t.Fatalf("saved media path %q should be valid for article", media.RepoPath)
	}

	if err := DeleteArticleMediaFileForRuntime(runtime, media.RepoPath, articlePath); err != nil {
		t.Fatalf("DeleteArticleMediaFileForRuntime() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(runtime.RepoPath, filepath.FromSlash(media.RepoPath))); !os.IsNotExist(err) {
		t.Fatalf("deleted media still exists, stat error = %v", err)
	}
}

func TestArticleMediaUsesCollectionSubdirectory(t *testing.T) {
	runtime := articleMediaTestRuntime(t, "{{dirname}}/images")
	articlePath := "posts/20260608/takao.md"
	articleDir := filepath.Join(runtime.RepoPath, "content", "posts", "20260608")
	writeTestFile(t, filepath.Join(articleDir, "takao.md"), "---\ntitle: Takao\n---\n")
	writeTestFile(t, filepath.Join(articleDir, "sibling.jpg"), "not in media folder")
	writeTestFile(t, filepath.Join(articleDir, "images", "existing.jpg"), "existing image")

	files, err := ListMediaFilesForRuntime(runtime, "content", articlePath)
	if err != nil {
		t.Fatalf("ListMediaFilesForRuntime() error = %v", err)
	}
	if len(files) != 1 || files[0].Path != "images/existing.jpg" {
		t.Fatalf("ListMediaFilesForRuntime() = %#v, want only images/existing.jpg", files)
	}
	if ValidateArticleMediaRepoPathForRuntime(runtime, articlePath, "content/posts/20260608/sibling.jpg") {
		t.Fatal("sibling media outside the configured target should be rejected")
	}

	header := testFileHeader(t, "image.png", []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	})
	media, err := SaveMediaFileForRuntime(runtime, header, "content", articlePath)
	if err != nil {
		t.Fatalf("SaveMediaFileForRuntime() error = %v", err)
	}
	if !strings.HasPrefix(media.RepoPath, "content/posts/20260608/images/image_") || !strings.HasPrefix(media.Path, "images/") {
		t.Fatalf("saved media = %#v, want images-relative path", media)
	}
}

func TestArticleMediaRejectsCollectionEscape(t *testing.T) {
	runtime := articleMediaTestRuntime(t, "{{dirname}}/../../shared")
	articlePath := "posts/20260608/takao.md"
	articleDir := filepath.Join(runtime.RepoPath, "content", "posts", "20260608")
	writeTestFile(t, filepath.Join(articleDir, "takao.md"), "---\ntitle: Takao\n---\n")

	if _, err := ListMediaFilesForRuntime(runtime, "content", articlePath); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("ListMediaFilesForRuntime() error = %v, want invalid media", err)
	}
}

func TestSaveMediaFileUsesHomeCMSMediaFolder(t *testing.T) {
	repoPath := t.TempDir()
	writeTestFile(t, filepath.Join(repoPath, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: posts
      folder: content/posts
media:
  folder: assets/images
  public_path: /images
`)
	runtime := config.NewSiteRuntime(config.SiteConfig{
		ID:             "test",
		RepoPath:       repoPath,
		Generator:      "eleventy",
		ContentDir:     "content",
		StaticDir:      "static",
		PublicDir:      "_site",
		PreviewURL:     "/",
		HugoServerBind: "127.0.0.1",
		HugoServerPort: "1314",
	})

	header := testFileHeader(t, "image.png", []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	})
	media, err := SaveMediaFileForRuntime(runtime, header, "static", "")
	if err != nil {
		t.Fatalf("SaveMediaFileForRuntime() unexpected error: %v", err)
	}
	if !strings.HasPrefix(media.RepoPath, "assets/images/") {
		t.Fatalf("RepoPath = %q, want assets/images prefix", media.RepoPath)
	}
	if !strings.HasPrefix(media.Path, "/images/") {
		t.Fatalf("Path = %q, want /images prefix", media.Path)
	}
	if !ValidateMediaRepoPathForRuntime(runtime, media.RepoPath) {
		t.Fatalf("media repo path %q should be valid", media.RepoPath)
	}
}

func TestSaveMediaFileUsesRootPublicPathForStaticMediaRoot(t *testing.T) {
	repoPath := t.TempDir()
	writeTestFile(t, filepath.Join(repoPath, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: posts
      folder: content/posts
media:
  folder: static
`)
	runtime := config.NewSiteRuntime(config.SiteConfig{
		ID:             "test",
		RepoPath:       repoPath,
		Generator:      "hugo",
		ContentDir:     "content",
		StaticDir:      "static",
		PublicDir:      "public",
		PreviewURL:     "/",
		HugoServerBind: "127.0.0.1",
		HugoServerPort: "1314",
	})

	header := testFileHeader(t, "image.png", []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	})
	media, err := SaveMediaFileForRuntime(runtime, header, "static", "")
	if err != nil {
		t.Fatalf("SaveMediaFileForRuntime() unexpected error: %v", err)
	}
	if !strings.HasPrefix(media.RepoPath, "static/") {
		t.Fatalf("RepoPath = %q, want static prefix", media.RepoPath)
	}
	if strings.HasPrefix(media.Path, "/static/") {
		t.Fatalf("Path = %q, should not include static directory prefix", media.Path)
	}
	if !strings.HasPrefix(media.Path, "/image_") || !strings.HasSuffix(media.Path, ".png") {
		t.Fatalf("Path = %q, want root-relative generated image path", media.Path)
	}
}

func TestSaveMediaFileNormalizesLegacyAbsoluteMediaFolder(t *testing.T) {
	repoPath := t.TempDir()
	writeTestFile(t, filepath.Join(repoPath, "static", "admin", "config.yml"), `
media_folder: /static/images
public_folder: /images
collections:
  - name: posts
    folder: content/posts
`)
	runtime := config.NewSiteRuntime(config.SiteConfig{
		ID:             "test",
		RepoPath:       repoPath,
		Generator:      "hugo",
		ContentDir:     "content",
		StaticDir:      "static",
		PublicDir:      "public",
		PreviewURL:     "/",
		HugoServerBind: "127.0.0.1",
		HugoServerPort: "1314",
	})

	header := testFileHeader(t, "image.png", []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	})
	media, err := SaveMediaFileForRuntime(runtime, header, "static", "")
	if err != nil {
		t.Fatalf("SaveMediaFileForRuntime() unexpected error: %v", err)
	}
	if !strings.HasPrefix(media.RepoPath, "static/images/") {
		t.Fatalf("RepoPath = %q, want static/images prefix", media.RepoPath)
	}
	if !strings.HasPrefix(media.Path, "/images/") {
		t.Fatalf("Path = %q, want /images prefix", media.Path)
	}
}

func TestValidateMediaRepoPath(t *testing.T) {
	repoPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoPath, "static", "images"), 0755); err != nil {
		t.Fatalf("create static directory: %v", err)
	}
	runtime := withMediaConfig(t, repoPath)

	tests := []struct {
		path string
		want bool
	}{
		{path: "static/images/photo.jpg", want: true},
		{path: "static/images/clip.mp4", want: true},
		{path: "static/images/document.pdf", want: true},
		{path: "content/posts/photo.png", want: true},
		{path: ".git/config", want: false},
		{path: "static/../.git/config.jpg", want: false},
		{path: "static/images/script.svg", want: false},
		{path: "../../outside/photo.jpg", want: false},
	}

	for _, tt := range tests {
		if got := ValidateMediaRepoPathForRuntime(runtime, tt.path); got != tt.want {
			t.Errorf("ValidateMediaRepoPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
