package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hugo-cms/pkg/config"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var ErrGitSyncConflict = errors.New("git sync conflict")

type syncGitFunc func(config.SiteRuntime, string, ...string) (string, error)

func CheckSemanticDiffForRuntime(runtime config.SiteRuntime, relPath string) (bool, error) {
	gitPath := filepath.ToSlash(relPath)

	ctx, cancel := context.WithTimeout(context.Background(), config.GitCommandTimeout)
	defer cancel()

	cmdHead := exec.CommandContext(ctx, "git", "show", "HEAD:"+gitPath)
	cmdHead.Dir = runtime.RepoPath
	headContent, _ := cmdHead.Output()

	diskPath := filepath.Join(runtime.RepoPath, filepath.FromSlash(gitPath))
	diskContent, _ := os.ReadFile(diskPath)

	collection, _ := GetCollectionForPathForRuntime(runtime, gitPath)

	headFM, headBody, headErr := canonicalizeContentForDiff(headContent, collection)
	diskFM, diskBody, diskErr := canonicalizeContentForDiff(diskContent, collection)

	if headErr != nil || diskErr != nil {
		headTrimmed := strings.TrimSpace(normalizeLineEndings(string(headContent)))
		diskTrimmed := strings.TrimSpace(normalizeLineEndings(string(diskContent)))
		return headTrimmed != diskTrimmed, nil
	}

	if !bytes.Equal(headFM, diskFM) {
		return true, nil
	}

	return headBody != diskBody, nil
}

func ExecuteGitWithToken(dir, token string, args ...string) (string, error) {
	return executeGitWithToken(dir, config.GitRemote, token, args...)
}

func ExecuteGitWithTokenForRuntime(runtime config.SiteRuntime, token string, args ...string) (string, error) {
	return executeGitWithToken(runtime.RepoPath, runtime.GitRemote, token, args...)
}

func executeGitWithToken(dir, remote, token string, args ...string) (string, error) {
	if token == "" {
		return "GitHub token is required", fmt.Errorf("empty GitHub token")
	}
	start := time.Now()
	defer func() {
		slog.Debug("Git command executed", "args", args, "duration", time.Since(start))
	}()

	// Create context with timeout for network operations (pull/push)
	ctx, cancel := context.WithTimeout(context.Background(), config.GitNetworkTimeout)
	defer cancel()

	// 1. Prepare secure remote URL (username only, no password)
	// We want to use the token for auth, but via ASKPASS.
	// We need to ensure the remote URL in the command triggers ASKPASS.
	// Typically, https://username@host/repo... works, asking for password.

	remoteUrl, err := readRawRemoteURL(ctx, dir, remote)
	if err != nil {
		return "Failed to get remote url", err
	}
	authenticatedUrl, err := authenticatedGitHubURL(remoteUrl)
	if err != nil {
		return "Remote URL must use HTTPS on github.com", err
	}

	// 2. Prepare Arguments
	newArgs := make([]string, len(args))
	copy(newArgs, args)
	for i, v := range newArgs {
		if v == remote {
			newArgs[i] = authenticatedUrl
		}
	}

	// 3. Setup ASKPASS
	scriptPath, err := createAskPassScript()
	if err != nil {
		return "Failed to setup auth helper", err
	}
	defer os.Remove(scriptPath)

	cmd := exec.CommandContext(ctx, "git", newArgs...)
	cmd.Dir = dir

	// 4. Set Environment
	env := gitAuthEnvironment(os.Environ(), scriptPath, token, authenticatedUrl)
	cmd.Env = env

	output, err := cmd.CombinedOutput()

	// 5. Sanitize Log
	// The token is not in args, but might be in verbose output if any.
	safeLog := strings.ReplaceAll(string(output), token, "***")
	// Also hide the URL with username just in case user considers it sensitive, though it's generic
	safeLog = strings.ReplaceAll(safeLog, authenticatedUrl, remoteUrl)

	return safeLog, err
}

func readRawRemoteURL(ctx context.Context, dir, remote string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "config", "--local", "--get", "remote."+remote+".url")
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read configured remote URL: %w", err)
	}

	remoteURL := strings.TrimSpace(string(output))
	if remoteURL == "" {
		return "", fmt.Errorf("configured remote URL is empty")
	}
	return remoteURL, nil
}

func gitAuthEnvironment(base []string, scriptPath, token, authenticatedURL string) []string {
	env := make([]string, 0, len(base)+9)
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		upperKey := strings.ToUpper(key)
		if upperKey == "GIT_ASKPASS" ||
			upperKey == "GIT_TOKEN" ||
			upperKey == "GIT_TERMINAL_PROMPT" ||
			upperKey == "GIT_CONFIG_COUNT" ||
			strings.HasPrefix(upperKey, "GIT_CONFIG_KEY_") ||
			strings.HasPrefix(upperKey, "GIT_CONFIG_VALUE_") {
			continue
		}
		env = append(env, entry)
	}

	return append(env,
		"GIT_ASKPASS="+scriptPath,
		"GIT_TOKEN="+token,
		"GIT_TERMINAL_PROMPT=0", // Disable interactive prompt fallback
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=credential.helper",
		"GIT_CONFIG_VALUE_0=",
		// An exact self-rewrite wins over broader global insteadOf rules and
		// keeps the validated GitHub HTTPS URL as the actual network target.
		"GIT_CONFIG_KEY_1=url."+authenticatedURL+".insteadOf",
		"GIT_CONFIG_VALUE_1="+authenticatedURL,
	)
}

func authenticatedGitHubURL(remoteURL string) (string, error) {
	u, err := url.Parse(remoteURL)
	if err != nil {
		return "", fmt.Errorf("parse remote URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") || !strings.EqualFold(u.Hostname(), "github.com") {
		return "", fmt.Errorf("unsupported Git remote %q", remoteURL)
	}
	if u.User != nil {
		return "", fmt.Errorf("Git remote must not contain embedded credentials")
	}
	if u.Path == "" || u.Path == "/" {
		return "", fmt.Errorf("Git remote repository path is missing")
	}

	u.User = url.User("oauth2")
	return u.String(), nil
}

func createAskPassScript() (string, error) {
	var scriptContent string
	var pattern string

	if runtime.GOOS == "windows" {
		scriptContent = "@echo %GIT_TOKEN%"
		pattern = "git-askpass-*.bat"
	} else {
		scriptContent = "#!/bin/sh\necho \"$GIT_TOKEN\""
		pattern = "git-askpass-*.sh"
	}

	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if _, err := f.WriteString(scriptContent); err != nil {
		return "", err
	}

	if runtime.GOOS != "windows" {
		if err := f.Chmod(0700); err != nil {
			return "", err
		}
	}
	return f.Name(), nil
}

func SyncRepoForRuntime(runtime config.SiteRuntime, token string) (string, error) {
	return syncRepoForRuntime(runtime, token, ExecuteGitWithTokenForRuntime)
}

type syncUntrackedBackup struct {
	originalPath string
	backupPath   string
}

func prepareEquivalentUntrackedBackups(runtime config.SiteRuntime, token string, git syncGitFunc, pathsOutput string) (string, []syncUntrackedBackup, error) {
	backupRoot, err := os.MkdirTemp("", "homecms-git-sync-")
	if err != nil {
		return "", nil, fmt.Errorf("create sync backup directory: %w", err)
	}
	backups := make([]syncUntrackedBackup, 0)
	restore := func() error {
		if err := restoreSyncUntrackedBackups(backups); err != nil {
			return err
		}
		return os.RemoveAll(backupRoot)
	}

	for _, rawPath := range strings.Split(pathsOutput, "\x00") {
		if rawPath == "" {
			continue
		}
		gitPath := filepath.ToSlash(filepath.Clean(rawPath))
		localPath := SafeJoin(runtime.RepoPath, "", gitPath)
		if localPath == "" {
			continue
		}
		localContent, readErr := os.ReadFile(localPath)
		if readErr != nil {
			continue
		}
		remoteContent, showErr := git(runtime, token, "show", "FETCH_HEAD:"+gitPath)
		if showErr != nil || !bytes.Equal(localContent, []byte(remoteContent)) {
			continue
		}

		backupPath := filepath.Join(backupRoot, filepath.FromSlash(gitPath))
		if err := os.MkdirAll(filepath.Dir(backupPath), 0755); err != nil {
			restoreErr := restore()
			if restoreErr != nil {
				return "", nil, fmt.Errorf("prepare sync backup for %s: %w; restore failed: %v", gitPath, err, restoreErr)
			}
			return "", nil, fmt.Errorf("prepare sync backup for %s: %w", gitPath, err)
		}
		if err := os.Rename(localPath, backupPath); err != nil {
			restoreErr := restore()
			if restoreErr != nil {
				return "", nil, fmt.Errorf("prepare sync backup for %s: %w; restore failed: %v", gitPath, err, restoreErr)
			}
			return "", nil, fmt.Errorf("prepare sync backup for %s: %w", gitPath, err)
		}
		backups = append(backups, syncUntrackedBackup{originalPath: localPath, backupPath: backupPath})
	}

	if len(backups) == 0 {
		_ = os.RemoveAll(backupRoot)
		return "", nil, nil
	}
	return backupRoot, backups, nil
}

func restoreSyncUntrackedBackups(backups []syncUntrackedBackup) error {
	for i := len(backups) - 1; i >= 0; i-- {
		backup := backups[i]
		if _, err := os.Lstat(backup.backupPath); err != nil {
			continue
		}
		if err := os.Remove(backup.originalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(backup.originalPath), 0755); err != nil {
			return err
		}
		if err := os.Rename(backup.backupPath, backup.originalPath); err != nil {
			return err
		}
	}
	return nil
}

func syncRepoForRuntime(runtime config.SiteRuntime, token string, git syncGitFunc) (string, error) {
	unlock := LockRepositoryOperation(runtime)
	defer unlock()

	var logs []string
	appendLog := func(output string) {
		if trimmed := strings.TrimSpace(output); trimmed != "" {
			logs = append(logs, trimmed)
		}
	}
	run := func(args ...string) (string, error) {
		output, err := git(runtime, token, args...)
		appendLog(output)
		return output, err
	}
	logOutput := func() string { return strings.Join(logs, "\n") }

	originalHead, err := run("rev-parse", "HEAD^{commit}")
	if err != nil {
		return logOutput(), fmt.Errorf("resolve local branch: %w", err)
	}
	originalHead = strings.TrimSpace(originalHead)
	if err := validateCommitSHA(originalHead); err != nil {
		return logOutput(), fmt.Errorf("local branch returned invalid commit: %w", err)
	}
	if _, err := run("fetch", "--no-tags", runtime.GitRemote, runtime.GitBranch); err != nil {
		return logOutput(), fmt.Errorf("fetch remote branch: %w", err)
	}
	remoteHead, err := run("rev-parse", "FETCH_HEAD^{commit}")
	if err != nil {
		return logOutput(), fmt.Errorf("resolve fetched branch: %w", err)
	}
	if err := validateCommitSHA(strings.TrimSpace(remoteHead)); err != nil {
		return logOutput(), fmt.Errorf("fetched branch returned invalid commit: %w", err)
	}
	dirtyOutput, err := git(runtime, token, "status", "--porcelain=v1", "-z")
	if err != nil {
		return logOutput(), fmt.Errorf("inspect local changes: %w", err)
	}
	hasLocalChanges := len(dirtyOutput) > 0
	if !hasLocalChanges {
		if _, err := run("merge", "--ff-only", "FETCH_HEAD"); err != nil {
			return logOutput(), fmt.Errorf("%w: local branch cannot be fast-forwarded to the remote branch", ErrGitSyncConflict)
		}
		InvalidateCacheForRuntime(runtime)
		return logOutput(), nil
	}

	untrackedOutput, err := git(runtime, token, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return logOutput(), fmt.Errorf("inspect untracked changes: %w", err)
	}
	backupRoot, untrackedBackups, err := prepareEquivalentUntrackedBackups(runtime, token, git, untrackedOutput)
	if err != nil {
		return logOutput(), err
	}
	cleanupBackups := func(restore bool) error {
		if restore {
			if err := restoreSyncUntrackedBackups(untrackedBackups); err != nil {
				return err
			}
		}
		if backupRoot != "" {
			return os.RemoveAll(backupRoot)
		}
		return nil
	}

	remainingChanges, err := git(runtime, token, "status", "--porcelain=v1", "-z")
	if err != nil {
		_ = cleanupBackups(true)
		return logOutput(), fmt.Errorf("inspect remaining local changes: %w", err)
	}
	if len(remainingChanges) == 0 {
		if _, err := run("merge", "--ff-only", "FETCH_HEAD"); err != nil {
			if restoreErr := cleanupBackups(true); restoreErr != nil {
				return logOutput(), fmt.Errorf("sync failed and restoring local untracked changes failed: %w", restoreErr)
			}
			return logOutput(), fmt.Errorf("%w: local branch cannot be fast-forwarded to the remote branch", ErrGitSyncConflict)
		}
		if err := cleanupBackups(false); err != nil {
			return logOutput(), fmt.Errorf("clean up sync backup: %w", err)
		}
		InvalidateCacheForRuntime(runtime)
		return logOutput(), nil
	}

	if _, err := run("stash", "push", "--include-untracked", "--message", "HomeCMS Git Sync local changes"); err != nil {
		_ = cleanupBackups(true)
		return logOutput(), fmt.Errorf("stash local changes before sync: %w", err)
	}
	restoreStash := func() error {
		_, restoreErr := run("stash", "pop")
		return restoreErr
	}
	if _, err := run("merge", "--ff-only", "FETCH_HEAD"); err != nil {
		if restoreErr := restoreStash(); restoreErr != nil {
			_ = cleanupBackups(true)
			return logOutput(), fmt.Errorf("sync failed and restoring local changes failed: %w", restoreErr)
		}
		if restoreErr := cleanupBackups(true); restoreErr != nil {
			return logOutput(), fmt.Errorf("sync failed and restoring local untracked changes failed: %w", restoreErr)
		}
		return logOutput(), fmt.Errorf("%w: local branch cannot be fast-forwarded to the remote branch", ErrGitSyncConflict)
	}
	if _, err := run("stash", "pop"); err != nil {
		if _, resetErr := run("reset", "--hard", originalHead); resetErr != nil {
			_ = cleanupBackups(true)
			return logOutput(), fmt.Errorf("%w: conflict occurred and restoring the local branch failed: %v", ErrGitSyncConflict, resetErr)
		}
		if _, restoreErr := run("stash", "pop"); restoreErr != nil {
			_ = cleanupBackups(true)
			return logOutput(), fmt.Errorf("%w: conflict occurred and restoring local changes failed: %v", ErrGitSyncConflict, restoreErr)
		}
		if restoreErr := cleanupBackups(true); restoreErr != nil {
			return logOutput(), fmt.Errorf("%w: conflict occurred and restoring local untracked changes failed: %v", ErrGitSyncConflict, restoreErr)
		}
		return logOutput(), fmt.Errorf("%w: local changes conflict with remote updates", ErrGitSyncConflict)
	}
	if err := cleanupBackups(false); err != nil {
		return logOutput(), fmt.Errorf("clean up sync backup: %w", err)
	}
	InvalidateCacheForRuntime(runtime)
	return logOutput(), nil
}

func DiffForRuntime(runtime config.SiteRuntime, f1Path, f2Path, relPath string) (string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), config.GitCommandTimeout)
	defer cancel()

	// 1. Check Unsaved Diff (Saved/Disk Normalized vs Editor Normalized)
	cmd := exec.CommandContext(ctx, "git", "diff", "--no-index", "--", f1Path, f2Path)
	output, err := cmd.CombinedOutput()

	if err != nil && cmd.ProcessState.ExitCode() == 1 {
		diffStr := string(output)
		// Fix labels
		// git diff --no-index usually shows path
		diffStr = strings.ReplaceAll(diffStr, f1Path, "Saved (Normalized)")
		diffStr = strings.ReplaceAll(diffStr, f2Path, "Editor")
		return diffStr, "unsaved"
	}

	// 2. Check Git Diff (HEAD Normalized vs Editor Normalized)
	// We use f2Path (Editor Normalized) as the "New" content because f1==f2 here.

	// Get HEAD content
	// Use filepath.ToSlash to ensure forward slashes for git
	gitPath := filepath.ToSlash(relPath)
	cmdHead := exec.CommandContext(ctx, "git", "show", "HEAD:"+gitPath)
	cmdHead.Dir = runtime.RepoPath
	outHead, _ := cmdHead.Output()
	// err is expected for new files, we treat it as empty

	// Normalize HEAD content with defaults
	collection, _ := GetCollectionForPathForRuntime(runtime, relPath)
	normalizedHead := NormalizeContent(outHead, collection)

	// Write to temp file
	fHead, err := os.CreateTemp("", "diff_head_*")
	if err != nil {
		slog.Warn("Failed to create temp file for diff", "error", err)
		return "", "none"
	}
	defer os.Remove(fHead.Name())
	if _, err := fHead.Write(normalizedHead); err != nil {
		slog.Warn("Failed to write temp file for diff", "error", err)
		return "", "none"
	}
	fHead.Close()

	cmdGit := exec.CommandContext(ctx, "git", "diff", "--no-index", "--", fHead.Name(), f2Path)
	outGit, err := cmdGit.CombinedOutput()

	if err != nil && cmdGit.ProcessState.ExitCode() == 1 {
		diffStr := string(outGit)
		diffStr = strings.ReplaceAll(diffStr, fHead.Name(), "HEAD (Normalized)")
		diffStr = strings.ReplaceAll(diffStr, f2Path, "Current (Normalized)")
		return diffStr, "git"
	}

	return "", "none"
}
