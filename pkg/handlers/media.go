package handlers

import (
	"errors"
	"hugo-cms/pkg/config"
	"hugo-cms/pkg/services"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func ListMedia(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	mode := c.Query("mode")
	articlePath := c.Query("path")
	files, err := services.ListMediaFilesForRuntime(runtime, mode, articlePath)
	if err != nil {
		if errors.Is(err, services.ErrInvalidMedia) {
			ErrorBadRequest(c, "Invalid media request")
			return
		}
		ErrorInternal(c, "Failed to list media: "+err.Error())
		return
	}
	for i := range files {
		files[i].URL = addSiteQuery(files[i].URL, runtime.ID)
	}
	c.JSON(http.StatusOK, files)
}

func UploadMedia(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	mode := c.PostForm("mode")
	articlePath := c.PostForm("path")
	file, err := c.FormFile("file")
	if err != nil {
		if c.Request.Body != nil && strings.Contains(err.Error(), "request body too large") {
			RespondError(c, http.StatusRequestEntityTooLarge, ErrCodeBadRequest, "Upload request is too large")
			return
		}
		ErrorBadRequest(c, "No file uploaded")
		return
	}

	// Check file size
	if file.Size > config.MaxUploadSize {
		maxMB := config.MaxUploadSize / 1024 / 1024
		ErrorBadRequest(c, "File too large. Maximum size is "+strconv.FormatInt(maxMB, 10)+"MB")
		return
	}

	// The generated filename is only known after the upload is accepted, so
	// establish a root metadata barrier before changing any production resource.
	// Preview is an auxiliary consumer, so an unavailable preview must not block
	// the authoritative production mutation.
	if err := invalidateLocalPreviewArticleURL(runtime); err != nil {
		slog.Warn("Failed to prepare Local Live Preview metadata before media upload", "site", runtime.ID, "error", err)
	}

	info, err := services.SaveMediaFileForRuntime(runtime, file, mode, articlePath)
	if err != nil {
		if errors.Is(err, services.ErrInvalidMedia) {
			ErrorBadRequest(c, err.Error())
			return
		}
		ErrorInternal(c, "Failed to save file: "+err.Error())
		return
	}
	localPreviewSync := true
	if err := syncLocalPreviewContentResourceForMutation(runtime, info.RepoPath, false, true); err != nil {
		localPreviewSync = false
	}
	info.LocalPreviewSync = &localPreviewSync
	info.URL = addSiteQuery(info.URL, runtime.ID)

	c.JSON(http.StatusOK, info)
}

func DeleteMedia(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	var req struct {
		RepoPath string `json:"repo_path"`
	}
	if err := c.BindJSON(&req); err != nil {
		ErrorBadRequest(c, "Invalid JSON")
		return
	}

	// Validate path is not empty and doesn't contain path traversal
	if req.RepoPath == "" {
		ErrorBadRequest(c, "repo_path is required")
		return
	}

	if !services.ValidateMediaRepoPathForRuntime(runtime, req.RepoPath) {
		ErrorBadRequest(c, "Invalid media path")
		return
	}

	if err := invalidateLocalPreviewArticleURL(runtime, localPreviewResourcePath(runtime, req.RepoPath)); err != nil {
		slog.Warn("Failed to prepare Local Live Preview metadata before media deletion", "site", runtime.ID, "path", req.RepoPath, "error", err)
	}

	if err := services.DeleteMediaFileForRuntime(runtime, req.RepoPath); err != nil {
		ErrorInternal(c, "Failed to delete: "+err.Error())
		return
	}
	localPreviewSync := true
	if err := syncLocalPreviewContentResourceForMutation(runtime, req.RepoPath, true, true); err != nil {
		localPreviewSync = false
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted", "local_preview_sync": localPreviewSync})
}

var invalidateLocalPreviewArticleURL = func(runtime config.SiteRuntime, articlePaths ...string) error {
	return services.DefaultLocalPreviewManager().InvalidateArticleURL(runtime, articlePaths...)
}

var syncLocalPreviewContentResourceForMutation = syncLocalPreviewContentResource

func syncLocalPreviewContentResource(runtime config.SiteRuntime, repoPath string, deleted, invalidateMetadata bool) error {
	workspaceManager, err := services.DefaultLocalPreviewWorkspaceManager()
	if err != nil {
		slog.Warn("Local preview workspace unavailable during content resource sync", "site", runtime.ID, "error", err)
		return err
	}
	synced, err := workspaceManager.SyncContentResource(runtime, repoPath, deleted)
	if err != nil {
		// The media operation already succeeded in the production workspace. Do
		// not turn a preview-only synchronization failure into a misleading media
		// retry; log it and let the next workspace rebuild recover the resource.
		slog.Warn("Failed to synchronize content resource into Local Live Preview", "site", runtime.ID, "path", repoPath, "error", err)
		return err
	}
	if !invalidateMetadata {
		return nil
	}

	// Content resources are part of the Eleventy input tree, while static
	// resources are linked from the production project. Invalidate both paths
	// after the shadow sync; the wrapper uses a directory recovery fallback for
	// deleted resources and a full-input fallback when no relative path exists.
	articlePath := localPreviewResourcePath(runtime, repoPath)
	if !synced && articlePath != "" {
		// There is no active shadow workspace to rebuild, but an active Eleventy
		// process may still observe a production-linked resource.
		articlePath = ""
	}
	if err := invalidateLocalPreviewArticleURL(runtime, articlePath); err != nil {
		slog.Warn("Failed to invalidate Local Live Preview metadata after media sync", "site", runtime.ID, "path", repoPath, "error", err)
		return err
	}
	return nil
}

func localPreviewResourcePath(runtime config.SiteRuntime, repoPath string) string {
	contentRoot := services.SafeJoin(runtime.RepoPath, "", runtime.ContentDir)
	resourcePath := services.SafeJoin(runtime.RepoPath, "", repoPath)
	if contentRoot == "" || resourcePath == "" {
		return ""
	}
	relative, err := filepath.Rel(contentRoot, resourcePath)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(relative)
}

func ServeMediaRaw(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	targetPath := c.Query("path")
	if targetPath == "" {
		ErrorBadRequest(c, "Path parameter required")
		return
	}
	if !services.ValidateMediaRepoPathForRuntime(runtime, targetPath) {
		ErrorNotFound(c, "Invalid media path")
		return
	}

	fullPath := services.SafeJoin(runtime.RepoPath, "", targetPath)
	if fullPath == "" {
		ErrorNotFound(c, "Invalid path")
		return
	}

	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=300")
	c.File(fullPath)
}
