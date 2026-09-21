package services

import (
	"errors"
	"hugo-cms/pkg/config"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResetArticleToHEADRestoresOnlyArticle(t *testing.T) {
	runtime, _ := setupSyncRepository(t)
	articlePath := "selected.md"
	articleFile := filepath.Join(runtime.RepoPath, "content", articlePath)
	mediaFile := filepath.Join(runtime.RepoPath, "static", "images", "keep.png")
	writeSyncTestFile(t, mediaFile, "HEAD media\n")
	runSyncGitOutput(t, runtime.RepoPath, "add", "static/images/keep.png")
	runSyncGitOutput(t, runtime.RepoPath, "commit", "-m", "add media")
	writeSyncTestFile(t, articleFile, "local unpublished change\n")
	writeSyncTestFile(t, mediaFile, "locally changed media\n")

	result, err := ResetArticleToHEADForRuntime(runtime, articlePath)
	if err != nil {
		t.Fatalf("ResetArticleToHEADForRuntime() error = %v", err)
	}
	if !result.HeadExists || result.Deleted {
		t.Fatalf("reset result = %#v, want restored article", result)
	}
	if got := readSyncTestFile(t, articleFile); got != "initial\n" {
		t.Fatalf("article after reset = %q, want HEAD content", got)
	}
	if got := readSyncTestFile(t, mediaFile); got != "locally changed media\n" {
		t.Fatalf("media after reset = %q, want unchanged media", got)
	}
	if status := runSyncGitOutput(t, runtime.RepoPath, "status", "--porcelain", "--", "content/selected.md"); status != "" {
		t.Fatalf("article status after reset = %q, want clean", status)
	}
}

func TestResetArticleToHEADDeletesNewArticleButKeepsMedia(t *testing.T) {
	runtime, _ := setupSyncRepository(t)
	articlePath := "posts/new.md"
	articleFile := filepath.Join(runtime.RepoPath, "content", filepath.FromSlash(articlePath))
	mediaFile := filepath.Join(runtime.RepoPath, "content", "posts", "new-image.jpg")
	writeSyncTestFile(t, articleFile, "new article\n")
	writeSyncTestFile(t, mediaFile, "article media\n")

	result, err := ResetArticleToHEADForRuntime(runtime, articlePath)
	if err != nil {
		t.Fatalf("ResetArticleToHEADForRuntime() error = %v", err)
	}
	if result.HeadExists || !result.Deleted {
		t.Fatalf("reset result = %#v, want deleted new article", result)
	}
	if _, err := os.Stat(articleFile); !os.IsNotExist(err) {
		t.Fatalf("new article still exists, stat error = %v", err)
	}
	if got := readSyncTestFile(t, mediaFile); got != "article media\n" {
		t.Fatalf("article media after reset = %q, want unchanged media", got)
	}
}

func TestResetArticleToHEADRejectsUnsafePath(t *testing.T) {
	runtime, _ := setupSyncRepository(t)
	_, err := ResetArticleToHEADForRuntime(runtime, "../outside.md")
	if !errors.Is(err, ErrInvalidArticleResetPath) {
		t.Fatalf("ResetArticleToHEADForRuntime() error = %v, want invalid path", err)
	}
}

func TestResetArticleToHEADRejectsSymlinkPath(t *testing.T) {
	runtime, _ := setupSyncRepository(t)
	target := filepath.Join(runtime.RepoPath, "content", "selected.md")
	outside := filepath.Join(runtime.RepoPath, "content", "outside.md")
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("outside\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlinks are not available in this environment: %v", err)
	}

	_, err := ResetArticleToHEADForRuntime(runtime, "selected.md")
	if !errors.Is(err, ErrInvalidArticleResetPath) {
		t.Fatalf("ResetArticleToHEADForRuntime() error = %v, want symlink rejection", err)
	}
}

func TestResetArticleToHEADRequiresGitHEAD(t *testing.T) {
	repoPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoPath, "content"), 0755); err != nil {
		t.Fatal(err)
	}
	runtime := config.NewSiteRuntime(config.SiteConfig{RepoPath: repoPath, ContentDir: "content"})
	_, err := ResetArticleToHEADForRuntime(runtime, "article.md")
	if err == nil {
		t.Fatal("ResetArticleToHEADForRuntime() should reject a repository without HEAD")
	}
	if _, ok := err.(*exec.ExitError); ok {
		t.Fatalf("ResetArticleToHEADForRuntime() leaked git command error: %v", err)
	}
}
