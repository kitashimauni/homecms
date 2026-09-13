package handlers

import (
	"bytes"
	"encoding/json"
	"hugo-cms/pkg/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func restoreSiteScopeConfig(t *testing.T) {
	t.Helper()

	originalRepoPath := config.RepoPath
	originalContentDir := config.ContentDir
	originalStaticDir := config.StaticDir
	originalPublicDir := config.PublicDir
	originalPublicPath := config.PublicPath
	originalPreviewURL := config.PreviewURL
	originalSiteGenerator := config.SiteGenerator
	originalDefaultSiteID := config.DefaultSiteID
	originalSnippetPaths := append([]string(nil), config.SnippetPaths...)
	originalSites := append([]config.SiteConfig(nil), config.Sites...)

	t.Cleanup(func() {
		config.RepoPath = originalRepoPath
		config.ContentDir = originalContentDir
		config.StaticDir = originalStaticDir
		config.PublicDir = originalPublicDir
		config.PublicPath = originalPublicPath
		config.PreviewURL = originalPreviewURL
		config.SiteGenerator = originalSiteGenerator
		config.DefaultSiteID = originalDefaultSiteID
		config.SnippetPaths = originalSnippetPaths
		config.Sites = originalSites
	})
}

// configureSiteScope installs the legacy registry only for handler contract
// tests that exercise query-based site selection. Production code should keep
// using an explicit SiteRuntime; this helper makes the remaining global
// boundary visible and restores it through restoreSiteScopeConfig.
func configureSiteScope(t *testing.T, sites []config.SiteConfig) {
	t.Helper()
	if len(sites) == 0 {
		t.Fatal("configureSiteScope requires at least one site")
	}
	config.DefaultSiteID = sites[0].ID
	config.Sites = append([]config.SiteConfig(nil), sites...)
	config.ApplySiteRuntime(sites[0])
}

func TestRequestedRuntimeResolvesSelectedSiteWithoutMutatingGlobals(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreSiteScopeConfig(t)

	defaultRepo := t.TempDir()
	selectedRepo := t.TempDir()
	configureSiteScope(t, []config.SiteConfig{
		{
			ID:             "default",
			Name:           "Default",
			RepoPath:       defaultRepo,
			Generator:      "hugo",
			ContentDir:     "content",
			StaticDir:      "static",
			PublicDir:      "public",
			PreviewURL:     "/",
			HugoServerPort: "1314",
			HugoServerBind: "127.0.0.1",
			SnippetPaths:   []string{filepath.Join(defaultRepo, ".vscode", "md.code-snippets")},
		},
		{
			ID:             "docs",
			Name:           "Docs",
			RepoPath:       selectedRepo,
			Generator:      "eleventy",
			ContentDir:     "src",
			StaticDir:      "public-assets",
			PublicDir:      "_site",
			PreviewURL:     "/docs/",
			HugoServerPort: "1315",
			HugoServerBind: "127.0.0.1",
			SnippetPaths:   []string{filepath.Join(selectedRepo, ".vscode", "md.code-snippets")},
		},
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/api/config?site=docs", nil)

	runtime, err := requestedRuntime(c)
	if err != nil {
		t.Fatalf("requestedRuntime() error = %v", err)
	}
	if config.RepoPath != defaultRepo {
		t.Fatalf("RepoPath after runtime resolution = %q, want unchanged default %q", config.RepoPath, defaultRepo)
	}
	if config.ContentDir != "content" {
		t.Fatalf("ContentDir after runtime resolution = %q, want content", config.ContentDir)
	}
	if currentSiteID(c) != "docs" ||
		runtime.RepoPath != selectedRepo ||
		runtime.ContentDir != "src" ||
		runtime.Generator != "eleventy" {
		t.Fatalf("runtime = %#v, want selected site runtime fields", runtime)
	}
}

func TestGetSnippetsUsesSelectedSitePaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreSiteScopeConfig(t)

	defaultRepo := t.TempDir()
	selectedRepo := t.TempDir()
	writeSnippetFile(t, filepath.Join(defaultRepo, ".vscode", "md.code-snippets"), `{
  "Default Snippet": {
    "prefix": "default",
    "body": "default body",
    "description": "default site"
  }
}`)
	writeSnippetFile(t, filepath.Join(selectedRepo, ".vscode", "md.code-snippets"), `{
  "Selected Snippet": {
    "prefix": "selected",
    "body": "selected body",
    "description": "selected site"
  }
}`)

	configureSiteScope(t, []config.SiteConfig{
		{
			ID:             "default",
			RepoPath:       defaultRepo,
			Generator:      "hugo",
			ContentDir:     "content",
			StaticDir:      "static",
			PublicDir:      "public",
			PreviewURL:     "/",
			HugoServerPort: "1314",
			HugoServerBind: "127.0.0.1",
			SnippetPaths:   []string{filepath.Join(defaultRepo, ".vscode", "md.code-snippets")},
		},
		{
			ID:             "docs",
			RepoPath:       selectedRepo,
			Generator:      "eleventy",
			ContentDir:     "src",
			StaticDir:      "static",
			PublicDir:      "public",
			PreviewURL:     "/docs/",
			HugoServerPort: "1315",
			HugoServerBind: "127.0.0.1",
			SnippetPaths:   []string{filepath.Join(selectedRepo, ".vscode", "md.code-snippets")},
		},
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/api/snippets?site=docs", nil)

	GetSnippets(c)

	if w.Code != http.StatusOK {
		t.Fatalf("GetSnippets() status = %d, body = %s", w.Code, w.Body.String())
	}
	var snippets map[string]SnippetDef
	if err := json.Unmarshal(w.Body.Bytes(), &snippets); err != nil {
		t.Fatalf("unmarshal snippets: %v", err)
	}
	if _, ok := snippets["Selected Snippet"]; !ok {
		t.Fatalf("snippets = %#v, want selected site snippet", snippets)
	}
	if _, ok := snippets["Default Snippet"]; ok {
		t.Fatalf("snippets = %#v, should not include default site snippet", snippets)
	}
}

func TestGetConfigIncludesValidationWarnings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreSiteScopeConfig(t)

	repoPath := t.TempDir()
	writeSnippetFile(t, filepath.Join(repoPath, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: posts
      folder: content/posts
      path: "{{slug}}"
      fields:
        - { name: title, widget: string }
preview:
  url_field: permalink
`)
	configureSiteScope(t, []config.SiteConfig{{
		ID:             "default",
		RepoPath:       repoPath,
		Generator:      "hugo",
		ContentDir:     "content",
		StaticDir:      "static",
		PublicDir:      "public",
		PreviewURL:     "/",
		HugoServerPort: "1314",
		HugoServerBind: "127.0.0.1",
	}})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/api/config", nil)

	GetConfig(c)

	if w.Code != http.StatusOK {
		t.Fatalf("GetConfig() status = %d, body = %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	meta, ok := body["_cms"].(map[string]interface{})
	if !ok {
		t.Fatalf("_cms = %#v, want metadata map", body["_cms"])
	}
	warnings, ok := meta["warnings"].([]interface{})
	if !ok || len(warnings) == 0 {
		t.Fatalf("warnings = %#v, want validation warnings", meta["warnings"])
	}
}

func TestGetConfigReturnsEmptyWarningsArrayForCleanConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreSiteScopeConfig(t)

	repoPath := t.TempDir()
	writeSnippetFile(t, filepath.Join(repoPath, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: posts
      folder: content/posts
      path: "{{slug}}"
      fields:
        - { name: slug, widget: string }
        - { name: title, widget: string }
`)
	configureSiteScope(t, []config.SiteConfig{{
		ID:             "default",
		RepoPath:       repoPath,
		Generator:      "hugo",
		ContentDir:     "content",
		StaticDir:      "static",
		PublicDir:      "public",
		PreviewURL:     "/",
		HugoServerPort: "1314",
		HugoServerBind: "127.0.0.1",
	}})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/api/config", nil)

	GetConfig(c)

	if w.Code != http.StatusOK {
		t.Fatalf("GetConfig() status = %d, body = %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"warnings":[]`) {
		t.Fatalf("GetConfig() body = %s, want warnings empty array", w.Body.String())
	}
}

func TestSelectedSiteArticleAPIsUseSelectedRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreSiteScopeConfig(t)

	defaultRepo := t.TempDir()
	selectedRepo := t.TempDir()
	writeSnippetFile(t, filepath.Join(defaultRepo, "content", "posts", "default.md"), `---
title: Default Post
---
default body
`)
	writeSnippetFile(t, filepath.Join(selectedRepo, "src", "docs", "selected.md"), `---
title: Selected Post
---
selected body
`)

	configureSiteScope(t, []config.SiteConfig{
		{
			ID:             "default",
			RepoPath:       defaultRepo,
			Generator:      "hugo",
			ContentDir:     "content",
			StaticDir:      "static",
			PublicDir:      "public",
			PreviewURL:     "/",
			HugoServerPort: "1314",
			HugoServerBind: "127.0.0.1",
		},
		{
			ID:             "docs",
			RepoPath:       selectedRepo,
			Generator:      "eleventy",
			ContentDir:     "src",
			StaticDir:      "public-assets",
			PublicDir:      "_site",
			PreviewURL:     "/docs/",
			HugoServerPort: "1315",
			HugoServerBind: "127.0.0.1",
		},
	})

	listW := httptest.NewRecorder()
	listC, _ := gin.CreateTestContext(listW)
	listC.Request = httptest.NewRequest(http.MethodGet, "/admin/api/articles?site=docs", nil)

	ListArticles(listC)

	if listW.Code != http.StatusOK {
		t.Fatalf("ListArticles() status = %d, body = %s", listW.Code, listW.Body.String())
	}
	var articles []map[string]interface{}
	if err := json.Unmarshal(listW.Body.Bytes(), &articles); err != nil {
		t.Fatalf("unmarshal articles: %v", err)
	}
	if len(articles) != 1 || articles[0]["path"] != "docs/selected.md" || articles[0]["title"] != "Selected Post" {
		t.Fatalf("articles = %#v, want only selected site article", articles)
	}

	getW := httptest.NewRecorder()
	getC, _ := gin.CreateTestContext(getW)
	getC.Request = httptest.NewRequest(http.MethodGet, "/admin/api/article?site=docs&path=docs/selected.md", nil)

	GetArticle(getC)

	if getW.Code != http.StatusOK {
		t.Fatalf("GetArticle() status = %d, body = %s", getW.Code, getW.Body.String())
	}
	var article map[string]interface{}
	if err := json.Unmarshal(getW.Body.Bytes(), &article); err != nil {
		t.Fatalf("unmarshal article: %v", err)
	}
	frontmatter, ok := article["frontmatter"].(map[string]interface{})
	if !ok || frontmatter["title"] != "Selected Post" || article["body"] != "selected body" {
		t.Fatalf("article = %#v, want selected site article content", article)
	}
	if config.RepoPath != defaultRepo || config.ContentDir != "content" {
		t.Fatalf("global runtime leaked: repo=%q content=%q", config.RepoPath, config.ContentDir)
	}
}

func TestSelectedSiteCreateArticleUsesSelectedConfigAndContentDir(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreSiteScopeConfig(t)

	defaultRepo := t.TempDir()
	selectedRepo := t.TempDir()
	writeSnippetFile(t, filepath.Join(defaultRepo, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: posts
      folder: content/posts
      path: "{{slug}}"
      fields:
        - { name: slug, widget: string }
        - { name: title, widget: string }
        - { name: body, widget: markdown }
`)
	writeSnippetFile(t, filepath.Join(selectedRepo, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: docs
      folder: src/docs
      path: "{{slug}}"
      fields:
        - { name: slug, widget: string }
        - { name: title, widget: string }
        - { name: body, widget: markdown }
`)

	configureSiteScope(t, []config.SiteConfig{
		{
			ID:             "default",
			RepoPath:       defaultRepo,
			Generator:      "hugo",
			ContentDir:     "content",
			StaticDir:      "static",
			PublicDir:      "public",
			PreviewURL:     "/",
			HugoServerPort: "1314",
			HugoServerBind: "127.0.0.1",
		},
		{
			ID:             "docs",
			RepoPath:       selectedRepo,
			Generator:      "eleventy",
			ContentDir:     "src",
			StaticDir:      "public-assets",
			PublicDir:      "_site",
			PreviewURL:     "/docs/",
			HugoServerPort: "1315",
			HugoServerBind: "127.0.0.1",
		},
	})

	body := []byte(`{"collection":"docs","fields":{"slug":"launch","title":"Launch Notes","body":"Ready."}}`)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/api/create?site=docs", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	CreateArticle(c)

	if w.Code != http.StatusOK {
		t.Fatalf("CreateArticle() status = %d, body = %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(selectedRepo, "src", "docs", "launch.md")); err != nil {
		t.Fatalf("created article should be under selected site content dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(defaultRepo, "content", "docs", "launch.md")); !os.IsNotExist(err) {
		t.Fatalf("article should not be created under default site, stat err = %v", err)
	}
	if config.RepoPath != defaultRepo || config.ContentDir != "content" {
		t.Fatalf("global runtime leaked: repo=%q content=%q", config.RepoPath, config.ContentDir)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["path"] != "docs/launch.md" {
		t.Fatalf("response path = %q, want docs/launch.md", resp["path"])
	}
}

func TestSelectedSiteGetConfigIncludesSelectedMetadata(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreSiteScopeConfig(t)

	defaultRepo := t.TempDir()
	selectedRepo := t.TempDir()
	writeSnippetFile(t, filepath.Join(selectedRepo, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: docs
      folder: src/docs
      path: "{{slug}}"
      fields:
        - { name: slug, widget: string }
        - { name: title, widget: string }
`)

	configureSiteScope(t, []config.SiteConfig{
		{
			ID:             "default",
			RepoPath:       defaultRepo,
			Generator:      "hugo",
			ContentDir:     "content",
			StaticDir:      "static",
			PublicDir:      "public",
			PreviewURL:     "/",
			HugoServerPort: "1314",
			HugoServerBind: "127.0.0.1",
		},
		{
			ID:             "docs",
			RepoPath:       selectedRepo,
			Generator:      "eleventy",
			ContentDir:     "src",
			StaticDir:      "public-assets",
			PublicDir:      "_site",
			PreviewURL:     "/docs/",
			HugoServerPort: "1315",
			HugoServerBind: "127.0.0.1",
		},
	})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/api/config?site=docs", nil)

	GetConfig(c)

	if w.Code != http.StatusOK {
		t.Fatalf("GetConfig() status = %d, body = %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}
	meta, ok := body["_cms"].(map[string]interface{})
	if !ok {
		t.Fatalf("_cms = %#v, want metadata map", body["_cms"])
	}
	if meta["site_id"] != "docs" ||
		meta["content_dir"] != "src" ||
		meta["static_dir"] != "public-assets" ||
		meta["public_dir"] != "_site" ||
		meta["site_generator"] != "eleventy" ||
		meta["config_source"] != ".homecms.yml" {
		t.Fatalf("_cms = %#v, want selected site metadata", meta)
	}
	if warnings, ok := meta["warnings"].([]interface{}); !ok || warnings == nil {
		t.Fatalf("warnings = %#v, want array", meta["warnings"])
	}
	if config.RepoPath != defaultRepo || config.ContentDir != "content" {
		t.Fatalf("global runtime leaked: repo=%q content=%q", config.RepoPath, config.ContentDir)
	}
}

func writeSnippetFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("create snippet dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write snippet file: %v", err)
	}
}

func TestSelectedSiteHandlersRejectUnknownSite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreSiteScopeConfig(t)

	configureSiteScope(t, []config.SiteConfig{{ID: "default", RepoPath: t.TempDir(), ContentDir: "content", StaticDir: "static", PublicDir: "public"}})

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/admin/api/articles?site=missing", nil)

	GetSnippets(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("GetSnippets() status = %d, want %d", w.Code, http.StatusBadRequest)
	}
}
