package services

import (
	"errors"
	"hugo-cms/pkg/config"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncRepoReconcilesLocalChangesThatAlreadyExistOnRemote(t *testing.T) {
	runtime, remoteWork := setupSyncRepository(t)
	writeSyncTestFile(t, filepath.Join(runtime.RepoPath, "content", "selected.md"), "merged remotely and locally\n")
	writeSyncTestFile(t, filepath.Join(remoteWork, "content", "selected.md"), "merged remotely and locally\n")
	commitSyncTestChanges(t, remoteWork, "merge published article")
	runSyncGitCommand(t, remoteWork, "push", "origin", "main")

	if _, err := syncRepoForRuntime(runtime, "token", localSyncGit(t)); err != nil {
		t.Fatalf("syncRepoForRuntime() error = %v", err)
	}
	if status := strings.TrimSpace(runSyncGitOutput(t, runtime.RepoPath, "status", "--porcelain")); status != "" {
		t.Fatalf("status after sync = %q, want clean", status)
	}
	if got := runSyncGitOutput(t, runtime.RepoPath, "show", "HEAD:content/selected.md"); got != "merged remotely and locally\n" {
		t.Fatalf("synced article = %q", got)
	}
}

func TestSyncRepoPreservesUnpublishedTrackedAndUntrackedChanges(t *testing.T) {
	runtime, remoteWork := setupSyncRepository(t)
	writeSyncTestFile(t, filepath.Join(runtime.RepoPath, "content", "selected.md"), "local unpublished change\n")
	writeSyncTestFile(t, filepath.Join(runtime.RepoPath, "content", "unpublished.md"), "untracked article\n")
	writeSyncTestFile(t, filepath.Join(remoteWork, "content", "remote.md"), "remote article\n")
	commitSyncTestChanges(t, remoteWork, "add remote article")
	runSyncGitCommand(t, remoteWork, "push", "origin", "main")

	if _, err := syncRepoForRuntime(runtime, "token", localSyncGit(t)); err != nil {
		t.Fatalf("syncRepoForRuntime() error = %v", err)
	}
	if got := readSyncTestFile(t, filepath.Join(runtime.RepoPath, "content", "selected.md")); got != "local unpublished change\n" {
		t.Fatalf("local tracked change = %q", got)
	}
	if got := readSyncTestFile(t, filepath.Join(runtime.RepoPath, "content", "unpublished.md")); got != "untracked article\n" {
		t.Fatalf("local untracked article = %q", got)
	}
	if got := readSyncTestFile(t, filepath.Join(runtime.RepoPath, "content", "remote.md")); got != "remote article\n" {
		t.Fatalf("remote article = %q", got)
	}
}

func TestSyncRepoReportsConflictWithoutDiscardingLocalContent(t *testing.T) {
	runtime, remoteWork := setupSyncRepository(t)
	writeSyncTestFile(t, filepath.Join(runtime.RepoPath, "content", "selected.md"), "local conflicting change\n")
	writeSyncTestFile(t, filepath.Join(remoteWork, "content", "selected.md"), "remote conflicting change\n")
	commitSyncTestChanges(t, remoteWork, "update article remotely")
	runSyncGitCommand(t, remoteWork, "push", "origin", "main")

	_, err := syncRepoForRuntime(runtime, "token", localSyncGit(t))
	if !errors.Is(err, ErrGitSyncConflict) {
		t.Fatalf("syncRepoForRuntime() error = %v, want conflict", err)
	}
	content := readSyncTestFile(t, filepath.Join(runtime.RepoPath, "content", "selected.md"))
	if content != "local conflicting change\n" {
		t.Fatalf("local content after conflict = %q", content)
	}
	localHead := runSyncGitOutput(t, runtime.RepoPath, "rev-parse", "HEAD")
	remoteHead := runSyncGitOutput(t, remoteWork, "rev-parse", "HEAD")
	if strings.TrimSpace(localHead) == strings.TrimSpace(remoteHead) {
		t.Fatal("local HEAD advanced despite a conflict")
	}
}

func setupSyncRepository(t *testing.T) (config.SiteRuntime, string) {
	t.Helper()
	repoPath := t.TempDir()
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	remoteWork := filepath.Join(t.TempDir(), "remote-work")
	if err := os.MkdirAll(remotePath, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(remoteWork, 0755); err != nil {
		t.Fatal(err)
	}
	runSyncGitCommand(t, remotePath, "init", "--bare")
	runSyncGitCommand(t, repoPath, "init", "-b", "main")
	writeSyncTestFile(t, filepath.Join(repoPath, "content", "selected.md"), "initial\n")
	commitSyncTestChanges(t, repoPath, "initial")
	runSyncGitCommand(t, repoPath, "remote", "add", "origin", remotePath)
	runSyncGitCommand(t, repoPath, "push", "-u", "origin", "main")
	runSyncGitCommand(t, remoteWork, "clone", remotePath, ".")
	runSyncGitCommand(t, remoteWork, "config", "user.name", "Sync Test")
	runSyncGitCommand(t, remoteWork, "config", "user.email", "sync@example.com")

	runtime := config.NewSiteRuntime(config.SiteConfig{ID: "sync", RepoPath: repoPath, ContentDir: "content", StaticDir: "static", PublicDir: "public"})
	runtime.GitBranch = "main"
	runtime.GitRemote = "origin"
	return runtime, remoteWork
}

func localSyncGit(t *testing.T) syncGitFunc {
	t.Helper()
	return func(runtime config.SiteRuntime, _ string, args ...string) (string, error) {
		command := exec.Command("git", args...)
		command.Dir = runtime.RepoPath
		output, err := command.CombinedOutput()
		return string(output), err
	}
}

func commitSyncTestChanges(t *testing.T, repoPath, message string) {
	t.Helper()
	runSyncGitCommand(t, repoPath, "add", ".")
	runSyncGitCommand(t, repoPath, "-c", "user.name=Sync Test", "-c", "user.email=sync@example.com", "commit", "-m", message)
}

func runSyncGitCommand(t *testing.T, repoPath string, args ...string) {
	t.Helper()
	if args[0] == "clone" {
		command := exec.Command("git", args...)
		command.Dir = repoPath
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return
	}
	command := exec.Command("git", args...)
	command.Dir = repoPath
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func runSyncGitOutput(t *testing.T, repoPath string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = repoPath
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(output)
}

func writeSyncTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func readSyncTestFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
