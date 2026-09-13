package handlers

import (
	"context"
	"errors"
	"hugo-cms/pkg/services"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

type localPreviewContentRequest struct {
	Revision    uint64                 `json:"revision"`
	Path        string                 `json:"path"`
	Content     string                 `json:"content,omitempty"`
	FrontMatter map[string]interface{} `json:"frontmatter,omitempty"`
	Body        string                 `json:"body,omitempty"`
	Format      string                 `json:"format,omitempty"`
}

func UpdateLocalPreviewContent(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	if runtime.LocalPreview.Enabled == nil || !*runtime.LocalPreview.Enabled {
		ErrorConflict(c, "Local Live Preview is disabled for this site")
		return
	}
	var req localPreviewContentRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ErrorBadRequest(c, "Invalid JSON")
		return
	}
	if strings.TrimSpace(req.Path) == "" || req.Revision == 0 {
		ErrorBadRequest(c, "path and positive revision are required")
		return
	}

	var finalContent []byte
	if req.FrontMatter != nil {
		finalContent, err = services.ConstructFileContent(req.FrontMatter, req.Body, req.Format)
		if err != nil {
			ErrorBadRequest(c, "Failed to construct preview content: "+err.Error())
			return
		}
	} else {
		finalContent = []byte(req.Content)
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), services.DefaultLocalPreviewStopTimeout)
	if err := services.RecoverLocalPreviewForRuntime(ctx, runtime); err != nil {
		cancel()
		slog.Warn("Failed to rebuild Local Live Preview workspace before content update", "site", runtime.ID, "error", err)
		c.AbortWithStatus(http.StatusServiceUnavailable)
		return
	}
	cancel()

	workspaceManager, err := services.DefaultLocalPreviewWorkspaceManager()
	if err != nil {
		ErrorInternal(c, "Local preview workspace is unavailable")
		return
	}
	workspace, created, applied, err := workspaceManager.UpdateWithBeforeWrite(runtime, req.Path, req.Revision, finalContent, func() error {
		return services.DefaultLocalPreviewManager().InvalidateArticleURL(runtime, req.Path)
	})
	if err != nil {
		if errors.Is(err, services.ErrLocalPreviewCleanupTransition) {
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		if errors.Is(err, services.ErrLocalPreviewMetadataInvalidation) {
			ErrorInternal(c, "Failed to synchronize Local Live Preview metadata")
			return
		}
		ErrorBadRequest(c, err.Error())
		return
	}

	if created {
		ctx, cancel := context.WithTimeout(c.Request.Context(), services.DefaultLocalPreviewStopTimeout)
		stopErr := services.DefaultLocalPreviewManager().Stop(ctx, runtime.ID)
		cancel()
		if stopErr != nil {
			slog.Error("Failed to switch Local Live Preview to shadow content", "site", runtime.ID, "error", stopErr)
			ErrorInternal(c, "Failed to switch Local Live Preview to shadow content")
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":      "updated",
		"applied":     applied,
		"revision":    workspace.Revision,
		"preview_url": runtime.LocalPreview.URL,
	})
}
