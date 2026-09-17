package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"hugo-cms/pkg/config"
	"hugo-cms/pkg/models"
	"hugo-cms/pkg/services"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

func HandleSync(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	session := sessions.Default(c)
	token, ok := session.Get("access_token").(string)
	if !ok {
		ErrorUnauthorized(c, "Invalid session token")
		return
	}
	log, err := services.SyncRepoForRuntime(runtime, token)

	if err != nil {
		if errors.Is(err, services.ErrGitSyncConflict) {
			ErrorConflict(c, "Git Sync conflict: local changes conflict with remote updates. Resolve the conflict with Diff or Reset before syncing again.")
			return
		}
		ErrorInternal(c, "Sync failed: "+log)
		return
	}

	response := gin.H{
		"status":              "ok",
		"log":                 log,
		"local_preview_reset": true,
	}
	// Keep the cleanup independent of the browser connection: production sync
	// has succeeded and the old preview must not survive a client disconnect.
	if resetErr := services.ResetLocalPreviewForRuntime(context.Background(), runtime); resetErr != nil {
		// Git sync has already succeeded. Keep the successful HTTP response but
		// expose a separate diagnostic so the frontend can ask the user to
		// reopen/retry Local Preview without misreporting the repository sync.
		slog.Error("Git sync succeeded but Local Live Preview reset failed", "site", runtime.ID, "error", resetErr)
		response["local_preview_reset"] = false
		response["local_preview_reset_error"] = "Local Live Preview reset failed; see server log"
	}
	c.JSON(http.StatusOK, response)
}

func ListArticles(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	articles, err := services.GetArticlesCacheForRuntime(runtime)
	if err != nil {
		ErrorInternal(c, "Failed to fetch articles")
		return
	}
	c.JSON(http.StatusOK, articles)
}

func GetArticle(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	targetPath := c.Query("path")
	if targetPath == "" {
		ErrorBadRequest(c, "Path parameter is required")
		return
	}

	fullPath := services.SafeJoin(runtime.RepoPath, runtime.ContentDir, targetPath)
	if fullPath == "" {
		ErrorBadRequest(c, "Invalid path")
		return
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		ErrorNotFound(c, "File not found")
		return
	}
	revision := services.ArticleRevision(content)

	fm, body, format, err := services.ParseFrontMatter(content)
	if err != nil {
		c.JSON(http.StatusOK, models.Article{
			Path:       targetPath,
			Content:    string(content),
			RawContent: string(content),
			Revision:   revision,
		})
		return
	}

	c.JSON(http.StatusOK, models.Article{
		Path:        targetPath,
		RawContent:  string(content),
		FrontMatter: fm,
		Body:        body,
		Format:      format,
		Revision:    revision,
	})
}

func SaveArticle(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	var art models.Article
	if err := c.BindJSON(&art); err != nil {
		ErrorBadRequest(c, "Invalid JSON")
		return
	}

	if art.Path == "" {
		ErrorBadRequest(c, "Path is required")
		return
	}

	fullPath := services.SafeJoin(runtime.RepoPath, runtime.ContentDir, art.Path)
	if fullPath == "" {
		ErrorBadRequest(c, "Invalid path")
		return
	}

	unlock := services.LockRepositoryOperation(runtime)
	defer unlock()

	var finalContent []byte

	if art.FrontMatter != nil {
		finalContent, err = services.ConstructFileContent(art.FrontMatter, art.Body, art.Format)
		if err != nil {
			ErrorInternal(c, "Failed to construct file content: "+err.Error())
			return
		}
	} else {
		finalContent = []byte(art.Content)
	}

	currentRevision, err := services.ArticleRevisionForPath(fullPath)
	if err != nil {
		ErrorInternal(c, "Failed to check article revision")
		return
	}
	if currentRevision != art.BaseRevision {
		respondArticleRevisionConflict(c, art.Path, currentRevision)
		return
	}

	if err := os.WriteFile(fullPath, finalContent, 0644); err != nil {
		ErrorInternal(c, "Save failed")
		return
	}

	services.UpdateCacheForRuntime(runtime, art.Path)
	c.JSON(http.StatusOK, gin.H{
		"status":   "saved",
		"revision": services.ArticleRevision(finalContent),
	})
}

func CreateArticle(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	var req struct {
		Path       string                 `json:"path"`
		Content    string                 `json:"content"`
		Collection string                 `json:"collection"`
		Fields     map[string]interface{} `json:"fields"`
	}
	if err := c.BindJSON(&req); err != nil {
		ErrorBadRequest(c, "Invalid JSON")
		return
	}

	unlock := services.LockRepositoryOperation(runtime)
	defer unlock()

	// New logic: Collection-based creation
	if req.Collection != "" {
		cmsConfig, err := services.GetCMSConfigForRuntime(runtime)
		if err != nil {
			ErrorInternal(c, "Failed to load CMS config")
			return
		}

		var targetCollection *models.Collection
		for _, col := range cmsConfig.Collections {
			if col.Name == req.Collection {
				targetCollection = &col
				break
			}
		}

		if targetCollection == nil {
			ErrorNotFound(c, "Collection not found: "+req.Collection)
			return
		}

		// Resolve Path
		relPath, err := services.ResolvePath(*targetCollection, req.Fields)
		if err != nil {
			ErrorInternal(c, "Failed to resolve path: "+err.Error())
			return
		}

		collectionFolder, err := services.CollectionFolderWithinContentForRuntime(runtime, *targetCollection)
		if err != nil {
			ErrorBadRequest(c, err.Error())
			return
		}
		fullPath := services.SafeJoin(runtime.RepoPath, collectionFolder, relPath)
		if fullPath == "" {
			ErrorBadRequest(c, "Invalid resolved path")
			return
		}

		// Check if file exists
		if _, err := os.Stat(fullPath); err == nil {
			ErrorConflict(c, "File already exists")
			return
		}

		// Generate Content
		content, err := services.GenerateContentFromCollection(*targetCollection, req.Fields)
		if err != nil {
			ErrorInternal(c, "Failed to generate content: "+err.Error())
			return
		}

		// Write File
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			ErrorInternal(c, "Failed to create directory")
			return
		}

		if err := os.WriteFile(fullPath, content, 0644); err != nil {
			ErrorInternal(c, "Failed to write file")
			return
		}

		contentRelPath, _ := filepath.Rel(filepath.Join(runtime.RepoPath, runtime.ContentDir), fullPath)
		contentRelPath = filepath.ToSlash(contentRelPath)

		services.UpdateCacheForRuntime(runtime, contentRelPath)
		c.JSON(http.StatusOK, gin.H{
			"status":   "created",
			"path":     contentRelPath,
			"revision": services.ArticleRevision(content),
		})
		return
	}

	// Legacy/Direct path logic
	if req.Path == "" || strings.Contains(req.Path, "..") {
		ErrorBadRequest(c, "Invalid path")
		return
	}
	fullPath := services.SafeJoin(runtime.RepoPath, runtime.ContentDir, req.Path)
	if fullPath == "" {
		ErrorBadRequest(c, "Invalid path")
		return
	}

	log, err := services.CreateContentForRuntime(runtime, req.Path)
	if err != nil {
		if os.IsExist(err) {
			ErrorConflict(c, log)
		} else {
			ErrorInternal(c, "Content creation failed: "+log)
		}
		return
	}

	services.UpdateCacheForRuntime(runtime, req.Path)
	content, err := os.ReadFile(fullPath)
	if err != nil {
		ErrorInternal(c, "Content creation succeeded but revision could not be read")
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"status":   "created",
		"log":      log,
		"revision": services.ArticleRevision(content),
	})
}

func GetDiff(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	var art models.Article
	if err := c.BindJSON(&art); err != nil {
		ErrorBadRequest(c, "Invalid JSON")
		return
	}

	if art.Path == "" {
		ErrorBadRequest(c, "Path is required")
		return
	}

	fullPath := services.SafeJoin(runtime.RepoPath, runtime.ContentDir, art.Path)
	if fullPath == "" {
		ErrorBadRequest(c, "Invalid path")
		return
	}

	currentContent, err := os.ReadFile(fullPath)
	if err != nil {
		currentContent = []byte("")
	}

	// Apply defaults for normalization
	collectionPath := filepath.Join(runtime.ContentDir, art.Path)
	collection, _ := services.GetCollectionForPathForRuntime(runtime, collectionPath)
	currentContent = services.NormalizeContent(currentContent, collection)

	var newContent []byte
	if art.FrontMatter != nil {
		newContent, err = services.ConstructFileContent(art.FrontMatter, art.Body, art.Format)
		if err != nil {
			ErrorInternal(c, "Construction failed")
			return
		}
	} else {
		newContent = []byte(art.Content)
	}

	newContent = services.NormalizeContent(newContent, collection)

	tmpDir := os.TempDir()
	f1, err := os.CreateTemp(tmpDir, "diff_old_*")
	if err != nil {
		ErrorInternal(c, "Failed to create temp file")
		return
	}
	defer os.Remove(f1.Name())

	f2, err := os.CreateTemp(tmpDir, "diff_new_*")
	if err != nil {
		ErrorInternal(c, "Failed to create temp file")
		return
	}
	defer os.Remove(f2.Name())

	if _, err := f1.Write(currentContent); err != nil {
		ErrorInternal(c, "Failed to write temp file")
		return
	}
	if _, err := f2.Write(newContent); err != nil {
		ErrorInternal(c, "Failed to write temp file")
		return
	}
	f1.Close()
	f2.Close()

	relPath := filepath.Join(runtime.ContentDir, art.Path)
	diffStr, diffType := services.DiffForRuntime(runtime, f1.Name(), f2.Name(), relPath)

	c.JSON(http.StatusOK, gin.H{"diff": diffStr, "type": diffType})
}

func DeleteArticle(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	var req struct {
		Path         string `json:"path"`
		BaseRevision string `json:"base_revision"`
	}
	if err := c.BindJSON(&req); err != nil {
		ErrorBadRequest(c, "Invalid JSON")
		return
	}

	if req.Path == "" || strings.Contains(req.Path, "..") {
		ErrorBadRequest(c, "Invalid path")
		return
	}
	fullPath := services.SafeJoin(runtime.RepoPath, runtime.ContentDir, req.Path)
	if fullPath == "" {
		ErrorBadRequest(c, "Invalid path")
		return
	}

	unlock := services.LockRepositoryOperation(runtime)
	defer unlock()

	currentRevision, err := services.ArticleRevisionForPath(fullPath)
	if err != nil {
		ErrorInternal(c, "Failed to check article revision")
		return
	}
	if currentRevision == "" {
		ErrorNotFound(c, "File not found")
		return
	}
	if currentRevision != req.BaseRevision {
		respondArticleRevisionConflict(c, req.Path, currentRevision)
		return
	}

	// Invalidate Eleventy metadata before deleting the shadow article. This
	// makes URL resolution return a rebuilding state while the old file is
	// being removed, instead of briefly serving its stale URL. Preview is an
	// auxiliary consumer, so a failed barrier is logged and the production
	// deletion continues; the post-delete sync below is the final convergence
	// point.
	if err := invalidateLocalPreviewArticleURL(runtime, filepath.ToSlash(req.Path)); err != nil {
		slog.Warn("Failed to prepare Local Live Preview metadata before article deletion", "site", runtime.ID, "path", req.Path, "error", err)
	}

	if err := services.DeleteFileForRuntime(runtime, req.Path); err != nil {
		ErrorInternal(c, "Delete failed: "+err.Error())
		return
	}

	// Re-scan or remove from cache
	// Assuming UpdateCache handles re-scan or we'll fix it
	services.UpdateCacheForRuntime(runtime, req.Path)
	// Keep an active site-scoped Local Live Preview workspace in sync without
	// releasing the generator runtime for the next article.
	localPreviewSync := true
	if err := syncLocalPreviewContentResourceForMutation(runtime, filepath.ToSlash(filepath.Join(runtime.ContentDir, req.Path)), true, true); err != nil {
		localPreviewSync = false
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted", "local_preview_sync": localPreviewSync})
}

func respondArticleRevisionConflict(c *gin.Context, path, currentRevision string) {
	c.JSON(http.StatusConflict, gin.H{
		"status":           "error",
		"code":             ErrCodeConflict,
		"message":          "Article was changed externally; reload before saving",
		"path":             path,
		"current_revision": currentRevision,
	})
}

func GetConfig(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	cfg, err := services.GetConfigForRuntime(runtime)
	if err != nil {
		ErrorInternal(c, "Failed to parse config")
		return
	}
	if cfg == nil {
		cfg = map[string]interface{}{}
	}
	configSource, _ := cfg["_config_source"].(string)
	delete(cfg, "_config_source")
	warnings := services.ValidateConfigForRuntime(runtime, configSource)
	cfg["_cms"] = gin.H{
		"content_dir":       runtime.ContentDir,
		"static_dir":        runtime.StaticDir,
		"public_dir":        runtime.PublicDir,
		"article_media_dir": runtime.ArticleMediaDir,
		"site_generator":    runtime.Generator,
		"default_site":      config.DefaultSiteID,
		"site_id":           runtime.ID,
		"config_source":     configSource,
		"warnings":          warnings,
		"markdown_preview": gin.H{
			"enabled": runtime.MarkdownPreviewEnabled,
		},
		"preview_deployment": gin.H{
			"enabled":          runtime.PreviewDeployment.Provider != "",
			"provider":         runtime.PreviewDeployment.Provider,
			"access_protected": runtime.PreviewDeployment.AccessProtected,
		},
	}
	c.JSON(http.StatusOK, cfg)
}

func ListSites(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"default_site":  config.DefaultSiteID,
		"selected_site": currentSiteID(c),
		"sites":         config.Sites,
	})
}

type SnippetDef struct {
	Prefix      interface{} `json:"prefix"`
	Body        interface{} `json:"body"`
	Description string      `json:"description"`
	Scope       string      `json:"scope,omitempty"`
}

func GetSnippets(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	allSnippets := make(map[string]SnippetDef)

	for _, path := range runtime.SnippetPaths {
		// Clean the path
		path = filepath.Clean(path)

		content, err := os.ReadFile(path)
		if err != nil {
			// Skip missing or unreadable files
			continue
		}

		// Simple sanitization for JSONC (VS Code snippets)
		sanitized := sanitizeJSONC(string(content))

		var fileSnippets map[string]SnippetDef
		if err := json.Unmarshal([]byte(sanitized), &fileSnippets); err != nil {
			// Skip malformed files
			continue
		}

		for name, snippet := range fileSnippets {
			// Scope check
			// If scope is defined, it must contain "markdown"
			if snippet.Scope != "" {
				scopes := strings.Split(snippet.Scope, ",")
				isMarkdown := false
				for _, s := range scopes {
					if strings.TrimSpace(s) == "markdown" {
						isMarkdown = true
						break
					}
				}
				if !isMarkdown {
					continue
				}
			}

			// Add to map (last one wins if duplicate names)
			allSnippets[name] = snippet
		}
	}

	c.JSON(http.StatusOK, allSnippets)
}

// sanitizeJSONC is a simple helper to remove comments and trailing commas.
func sanitizeJSONC(src string) string {
	var out []rune
	var inString bool
	var escaped bool

	// Convert to runes for iteration
	runes := []rune(src)
	length := len(runes)

	for i := 0; i < length; i++ {
		c := runes[i]

		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
			} else {
				if c == '\\' {
					escaped = true
				} else if c == '"' {
					inString = false
				}
			}
			continue
		}

		// Check for comments
		if c == '/' && i+1 < length {
			next := runes[i+1]
			if next == '/' {
				// Single line comment: skip until newline
				i += 2
				for i < length && runes[i] != '\n' {
					i++
				}
				// Keep the newline
				if i < length {
					out = append(out, '\n')
				}
				continue
			} else if next == '*' {
				// Block comment: skip until */
				i += 2
				for i+1 < length && !(runes[i] == '*' && runes[i+1] == '/') {
					i++
				}
				i++ // skip '/'
				continue
			}
		}

		out = append(out, c)
	}

	res := string(out)

	// Remove trailing commas using Regex
	// We matched comment-stripped text. Now we remove commas before closing braces.
	// We must avoid matching inside strings.
	// But we already preserved strings in `out`.
	// Since we are doing a quick fix and valid JSON rarely has `,\s*}` inside a string,
	// we will use regex. A robust parser is better but complex.

	// Regex to remove trailing commas: ,(?:\s*)([}\]])
	re := regexp.MustCompile(`,(\s*[}\]])`)
	return re.ReplaceAllString(res, "$1")
}
