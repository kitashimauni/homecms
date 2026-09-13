package services

import (
	"hugo-cms/pkg/config"
	"path/filepath"
	"testing"
	"time"
)

func TestLockRepositoryOperationAllowsDifferentRepositoriesInParallel(t *testing.T) {
	root := t.TempDir()
	firstRuntime := config.SiteRuntime{RepoPath: filepath.Join(root, "first")}
	secondRuntime := config.SiteRuntime{RepoPath: filepath.Join(root, "second")}

	unlockFirst := LockRepositoryOperation(firstRuntime)
	defer unlockFirst()

	acquired := make(chan struct{})
	done := make(chan struct{})
	go func() {
		unlockSecond := LockRepositoryOperation(secondRuntime)
		close(acquired)
		unlockSecond()
		close(done)
	}()

	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("different repository lock was blocked by the first repository")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("different repository lock did not complete")
	}
}

func TestLockRepositoryOperationSerializesSharedRepository(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "shared")
	firstRuntime := config.SiteRuntime{RepoPath: repoPath}
	secondRuntime := config.SiteRuntime{RepoPath: filepath.Join(repoPath, ".")}

	unlockFirst := LockRepositoryOperation(firstRuntime)

	acquired := make(chan struct{})
	done := make(chan struct{})
	go func() {
		unlockSecond := LockRepositoryOperation(secondRuntime)
		close(acquired)
		unlockSecond()
		close(done)
	}()

	select {
	case <-acquired:
		t.Fatal("shared repository lock was acquired before the first lock was released")
	case <-time.After(50 * time.Millisecond):
	}

	unlockFirst()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shared repository lock did not proceed after release")
	}
}
