package handlers

import (
	"context"
	"hugo-cms/pkg/config"
	"hugo-cms/pkg/services"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

type localPreviewNavigateRequest struct {
	Path string `json:"path"`
}

type localPreviewNavigationWorkspaceManager interface {
	AcquireNavigation(siteID string) (services.LocalPreviewIngressLease, services.LocalPreviewWorkspace, bool, bool)
}

type localPreviewNavigationDependencies struct {
	workspaceManager              localPreviewNavigationWorkspaceManager
	resolveArticleURL             func(context.Context, config.SiteRuntime, services.LocalPreviewWorkspace, string) (string, error)
	resolveArticleURLWithMetadata func(context.Context, config.SiteRuntime, services.LocalPreviewWorkspace, string) (services.PreviewArticleURLResolution, error)
}

// NavigateLocalPreview resolves the selected shadow article through the
// configured generator and returns the URL to open in the local preview.
func NavigateLocalPreview(c *gin.Context) {
	navigateLocalPreview(c, localPreviewNavigationDependencies{
		resolveArticleURLWithMetadata: services.DefaultLocalPreviewManager().ResolveArticleURLWithMetadata,
	})
}

func navigateLocalPreview(c *gin.Context, dependencies localPreviewNavigationDependencies) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	if runtime.LocalPreview.Enabled == nil || !*runtime.LocalPreview.Enabled {
		ErrorConflict(c, "Local Live Preview is disabled for this site")
		return
	}
	var req localPreviewNavigateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ErrorBadRequest(c, "Invalid JSON")
		return
	}
	req.Path = filepath.Clean(strings.TrimSpace(req.Path))
	if req.Path == "." || filepath.IsAbs(req.Path) {
		ErrorBadRequest(c, "a relative path is required")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), services.DefaultLocalPreviewStopTimeout)
	if err := services.RecoverLocalPreviewForRuntime(ctx, runtime); err != nil {
		cancel()
		slog.Warn("Failed to rebuild Local Live Preview workspace before navigation", "site", runtime.ID, "error", err)
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	cancel()

	workspaceManager := dependencies.workspaceManager
	if workspaceManager == nil {
		workspaceManager, err = services.DefaultLocalPreviewWorkspaceManager()
		if err != nil {
			ErrorInternal(c, "Local preview workspace is unavailable")
			return
		}
	}
	navigationLease, workspace, active, transitioning := workspaceManager.AcquireNavigation(runtime.ID)
	defer navigationLease.Release()
	if transitioning {
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	if !active {
		ErrorConflict(c, "Local Live Preview workspace is not active")
		return
	}
	if dependencies.resolveArticleURL == nil && dependencies.resolveArticleURLWithMetadata == nil {
		ErrorInternal(c, "Local preview URL resolver is unavailable")
		return
	}
	resolverRuntime := runtime
	resolverRuntime.ContentDir = workspace.ContentDir
	if workspace.ProjectDir != "" {
		resolverRuntime.LocalPreviewSourceRepoPath = resolverRuntime.RepoPath
		resolverRuntime.RepoPath = workspace.ProjectDir
		resolverRuntime.LocalPreviewProjectDir = workspace.ProjectDir
	}
	var resolution services.PreviewArticleURLResolution
	if dependencies.resolveArticleURLWithMetadata != nil {
		resolution, err = dependencies.resolveArticleURLWithMetadata(c.Request.Context(), resolverRuntime, workspace, req.Path)
	} else {
		var articleURL string
		articleURL, err = dependencies.resolveArticleURL(c.Request.Context(), resolverRuntime, workspace, req.Path)
		resolution = services.PreviewArticleURLResolution{URL: articleURL, Fresh: true, Status: "resolved"}
	}
	if err != nil {
		slog.Error("Failed to resolve Local Live Preview article URL", "site", runtime.ID, "article_path", req.Path, "error", err)
		ErrorInternal(c, "Failed to resolve Local Live Preview article URL")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":                  "resolved",
		"article_url":             resolution.URL,
		"fresh":                   resolution.Fresh,
		"metadata_status":         resolution.Status,
		"invalidation_generation": resolution.InvalidationGeneration,
		"active_build_generation": resolution.ActiveBuildGeneration,
		"revision":                workspace.Revision,
		"preview_url":             runtime.LocalPreview.URL,
		"article_path":            workspace.ArticlePath,
	})
}
