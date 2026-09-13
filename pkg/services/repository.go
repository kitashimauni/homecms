package services

import (
	"hugo-cms/pkg/config"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type repositoryOperationLock struct {
	mu   sync.Mutex
	refs int
}

var (
	repositoryOperationLocksMu sync.Mutex
	repositoryOperationLocks   = map[string]*repositoryOperationLock{}
)

// LockRepositoryOperation serializes working-tree writes with sync and publish
// for one normalized repository path. Different repositories can proceed in
// parallel, while sites sharing a repository continue to use the same lock.
// The returned function must be deferred by the caller.
func LockRepositoryOperation(siteRuntime config.SiteRuntime) func() {
	key := normalizedRepositoryPath(siteRuntime.RepoPath)

	repositoryOperationLocksMu.Lock()
	lock := repositoryOperationLocks[key]
	if lock == nil {
		lock = &repositoryOperationLock{}
		repositoryOperationLocks[key] = lock
	}
	lock.refs++
	repositoryOperationLocksMu.Unlock()

	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()

		repositoryOperationLocksMu.Lock()
		lock.refs--
		if lock.refs == 0 && repositoryOperationLocks[key] == lock {
			delete(repositoryOperationLocks, key)
		}
		repositoryOperationLocksMu.Unlock()
	}
}

func normalizedRepositoryPath(repoPath string) string {
	path := strings.TrimSpace(repoPath)
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}
