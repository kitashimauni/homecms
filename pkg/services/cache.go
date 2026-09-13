package services

import (
	"context"
	"hugo-cms/pkg/config"
	"hugo-cms/pkg/models"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	articleCaches            = map[string][]models.Article{}
	cacheMutex               sync.RWMutex
	cacheLocksMu             sync.Mutex
	cacheLocks               = map[string]*sync.Mutex{}
	rebuildArticlesCacheFunc = rebuildArticlesCache
)

func GetArticlesCacheForRuntime(runtime config.SiteRuntime) ([]models.Article, error) {
	start := time.Now()
	cacheKey := articleCacheKeyForRuntime(runtime)
	cacheLock := articleCacheLockForKey(cacheKey)
	cacheLock.Lock()
	defer cacheLock.Unlock()

	cacheMutex.RLock()
	if articleCache, ok := articleCaches[cacheKey]; ok {
		cached := cloneArticles(articleCache)
		cacheMutex.RUnlock()
		return cached, nil
	}
	cacheMutex.RUnlock()

	articles, err := rebuildArticlesCacheFunc(runtime)
	if err != nil {
		return nil, err
	}

	cacheMutex.Lock()
	articleCaches[cacheKey] = articles
	cacheMutex.Unlock()

	slog.Info("Cache rebuild completed", "site_cache_key", cacheKey, "duration", time.Since(start), "count", len(articles))
	return cloneArticles(articles), nil
}

func rebuildArticlesCache(runtime config.SiteRuntime) ([]models.Article, error) {
	contentDir := filepath.Join(runtime.RepoPath, runtime.ContentDir)
	dirtyFiles, _ := getGitDirtyFiles(runtime)

	var paths []string
	if err := filepath.WalkDir(contentDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".md") {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		return nil, err
	}

	articles := make([]models.Article, len(paths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, config.CacheConcurrency)

	for i, path := range paths {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			relPath, _ := filepath.Rel(contentDir, path)
			relPath = filepath.ToSlash(relPath)

			repoRelPath, _ := filepath.Rel(runtime.RepoPath, path)
			repoRelPath = filepath.ToSlash(repoRelPath)
			isDirty := dirtyFiles[repoRelPath]

			content, err := readHead(path, config.FileReadHeadLimit)
			title := relPath
			if err == nil {
				fm, _, _, err := ParseFrontMatter(content)
				if err == nil {
					if t, ok := fm["title"].(string); ok {
						title = t
					}
				}
			}

			articles[i] = models.Article{
				Path:    relPath,
				Title:   title,
				IsDirty: isDirty,
			}
		}(i, path)
	}

	wg.Wait()
	return articles, nil
}

func readHead(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	buf := make([]byte, limit)
	n, err := f.Read(buf)
	// io.EOF is acceptable if file is smaller than limit
	if err != nil && err != io.EOF {
		return nil, err
	}
	return buf[:n], nil
}

func getGitDirtyFiles(runtime config.SiteRuntime) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), config.GitCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain=v1", "-z", "--", runtime.ContentDir)
	cmd.Dir = runtime.RepoPath
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	dirty := make(map[string]bool)
	for _, entry := range parseGitStatusPorcelainZ(out) {
		if len(entry) < 4 {
			continue
		}
		path := strings.TrimSpace(entry[3:])
		path = filepath.ToSlash(path)

		diff, diffErr := CheckSemanticDiffForRuntime(runtime, path)
		if diffErr != nil || diff {
			dirty[path] = true
		}
	}

	cmdUntracked := exec.CommandContext(ctx, "git", "ls-files", "--others", "--exclude-standard", "-z", "--", runtime.ContentDir)
	cmdUntracked.Dir = runtime.RepoPath
	if outUntracked, errUntracked := cmdUntracked.Output(); errUntracked == nil {
		for _, raw := range strings.Split(string(outUntracked), "\x00") {
			path := strings.TrimSpace(raw)
			if path == "" {
				continue
			}
			path = filepath.ToSlash(path)
			if _, exists := dirty[path]; exists {
				continue
			}
			diff, diffErr := CheckSemanticDiffForRuntime(runtime, path)
			if diffErr != nil || diff {
				dirty[path] = true
			}
		}
	}
	return dirty, nil
}

func InvalidateCacheForRuntime(runtime config.SiteRuntime) {
	cacheLock := articleCacheLockForKey(articleCacheKeyForRuntime(runtime))
	cacheLock.Lock()
	defer cacheLock.Unlock()

	cacheMutex.Lock()
	defer cacheMutex.Unlock()
	delete(articleCaches, articleCacheKeyForRuntime(runtime))
}

func UpdateCacheForRuntime(runtime config.SiteRuntime, relPath string) {
	start := time.Now()
	defer func() {
		slog.Debug("Cache update single", "path", relPath, "duration", time.Since(start))
	}()

	cacheKey := articleCacheKeyForRuntime(runtime)
	cacheLock := articleCacheLockForKey(cacheKey)
	cacheLock.Lock()
	defer cacheLock.Unlock()

	cacheMutex.RLock()
	_, cacheLoaded := articleCaches[cacheKey]
	cacheMutex.RUnlock()
	if !cacheLoaded {
		return // Next Get will rebuild
	}

	fullPath := filepath.Join(runtime.RepoPath, runtime.ContentDir, relPath)

	// Check if file exists
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		// Remove from cache
		cacheMutex.Lock()
		articleCache, cacheLoaded := articleCaches[cacheKey]
		if cacheLoaded {
			for i, art := range articleCache {
				if art.Path == relPath {
					articleCache = append(articleCache[:i], articleCache[i+1:]...)
					articleCaches[cacheKey] = articleCache
					break
				}
			}
		}
		cacheMutex.Unlock()
		return
	}

	// For now assuming update/create means it exists or we handle error
	content, err := readHead(fullPath, config.FileReadHeadLimit)
	if err != nil {
		return // Ignore error, maybe remove from cache if not found?
	}

	title := relPath
	fm, _, _, err := ParseFrontMatter(content)
	if err == nil {
		if t, ok := fm["title"].(string); ok {
			title = t
		}
	}

	isDirty, _ := getGitFileStatus(runtime, relPath)

	newArt := models.Article{
		Path:    relPath,
		Title:   title,
		IsDirty: isDirty,
	}

	cacheMutex.Lock()
	defer cacheMutex.Unlock()
	articleCache, cacheLoaded := articleCaches[cacheKey]
	if !cacheLoaded {
		return
	}
	articleCache = cloneArticles(articleCache)
	found := false
	for i, art := range articleCache {
		if art.Path == relPath {
			articleCache[i] = newArt
			found = true
			break
		}
	}
	if !found {
		articleCache = append(articleCache, newArt)
	}
	articleCaches[cacheKey] = articleCache
}

func articleCacheLockForKey(cacheKey string) *sync.Mutex {
	cacheLocksMu.Lock()
	defer cacheLocksMu.Unlock()
	if lock, ok := cacheLocks[cacheKey]; ok {
		return lock
	}
	lock := &sync.Mutex{}
	cacheLocks[cacheKey] = lock
	return lock
}

func cloneArticles(articles []models.Article) []models.Article {
	return append([]models.Article(nil), articles...)
}

func articleCacheKeyForRuntime(runtime config.SiteRuntime) string {
	return filepath.ToSlash(filepath.Clean(runtime.RepoPath)) + "\x00" + filepath.ToSlash(filepath.Clean(runtime.ContentDir))
}

func getGitFileStatus(runtime config.SiteRuntime, relPath string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), config.GitCommandTimeout)
	defer cancel()

	// git status --porcelain content/posts/xxx.md
	// Note: relPath is relative to content/, but git needs relative to RepoPath
	target := filepath.Join(runtime.ContentDir, relPath)
	targetGit := filepath.ToSlash(target)
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain", targetGit)
	cmd.Dir = runtime.RepoPath
	out, err := cmd.Output()
	if err != nil {
		return false, err
	}
	if len(strings.TrimSpace(string(out))) > 0 {
		// Verify semantically
		return CheckSemanticDiffForRuntime(runtime, targetGit)
	}
	return false, nil
}

func parseGitStatusPorcelainZ(out []byte) []string {
	rawEntries := strings.Split(string(out), "\x00")
	entries := make([]string, 0, len(rawEntries))
	for i := 0; i < len(rawEntries); i++ {
		entry := rawEntries[i]
		if entry == "" {
			continue
		}
		entries = append(entries, entry)
		if len(entry) > 0 && (entry[0] == 'R' || entry[0] == 'C') {
			i++ // porcelain -z stores the source path as the next NUL field.
		}
	}
	return entries
}
