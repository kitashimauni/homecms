package services

import (
	"hugo-cms/pkg/config"
	"hugo-cms/pkg/models"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestArticleCacheRebuildIsSingleFlightPerKey(t *testing.T) {
	restoreServiceSiteScopeConfig(t)
	originalRebuild := rebuildArticlesCacheFunc
	t.Cleanup(func() { rebuildArticlesCacheFunc = originalRebuild })

	runtime := config.SiteRuntime{RepoPath: filepath.Join(t.TempDir(), "repo"), ContentDir: "content"}
	entered := make(chan struct{})
	release := make(chan struct{})
	var callsMu sync.Mutex
	calls := 0
	rebuildArticlesCacheFunc = func(config.SiteRuntime) ([]models.Article, error) {
		callsMu.Lock()
		calls++
		callsMu.Unlock()
		close(entered)
		<-release
		return []models.Article{{Path: "article.md", Title: "Article"}}, nil
	}

	firstDone := make(chan struct{})
	go func() {
		if _, err := GetArticlesCacheForRuntime(runtime); err != nil {
			t.Errorf("first cache rebuild error: %v", err)
		}
		close(firstDone)
	}()
	<-entered

	secondDone := make(chan struct{})
	go func() {
		if _, err := GetArticlesCacheForRuntime(runtime); err != nil {
			t.Errorf("second cache rebuild error: %v", err)
		}
		close(secondDone)
	}()
	select {
	case <-secondDone:
		t.Fatal("same cache key rebuilt concurrently instead of waiting for the first rebuild")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first cache rebuild did not complete")
	}
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("second cache read did not complete after the rebuild")
	}

	callsMu.Lock()
	defer callsMu.Unlock()
	if calls != 1 {
		t.Fatalf("cache rebuild calls for one key = %d, want 1", calls)
	}
}

func TestArticleCacheRebuildDoesNotBlockAnotherKey(t *testing.T) {
	restoreServiceSiteScopeConfig(t)
	originalRebuild := rebuildArticlesCacheFunc
	t.Cleanup(func() { rebuildArticlesCacheFunc = originalRebuild })

	firstRuntime := config.SiteRuntime{RepoPath: filepath.Join(t.TempDir(), "first"), ContentDir: "content"}
	secondRuntime := config.SiteRuntime{RepoPath: filepath.Join(t.TempDir(), "second"), ContentDir: "content"}
	firstKey := articleCacheKeyForRuntime(firstRuntime)
	enteredFirst := make(chan struct{})
	releaseFirst := make(chan struct{})
	rebuildArticlesCacheFunc = func(runtime config.SiteRuntime) ([]models.Article, error) {
		if articleCacheKeyForRuntime(runtime) == firstKey {
			close(enteredFirst)
			<-releaseFirst
		}
		return []models.Article{{Path: runtime.RepoPath}}, nil
	}

	firstDone := make(chan struct{})
	go func() {
		if _, err := GetArticlesCacheForRuntime(firstRuntime); err != nil {
			t.Errorf("first cache rebuild error: %v", err)
		}
		close(firstDone)
	}()
	<-enteredFirst

	secondDone := make(chan struct{})
	go func() {
		if _, err := GetArticlesCacheForRuntime(secondRuntime); err != nil {
			t.Errorf("second cache rebuild error: %v", err)
		}
		close(secondDone)
	}()
	select {
	case <-secondDone:
	case <-time.After(time.Second):
		t.Fatal("cache rebuild for another key was blocked by the first key")
	}

	close(releaseFirst)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first cache rebuild did not complete")
	}
}
