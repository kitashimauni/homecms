package handlers

import (
	"context"
	"hugo-cms/pkg/services"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func GetLocalPreviewStatus(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	enabled := runtime.LocalPreview.Enabled != nil && *runtime.LocalPreview.Enabled
	if !enabled {
		c.JSON(http.StatusOK, gin.H{
			"enabled":   false,
			"status":    "disabled",
			"generator": runtime.Generator,
		})
		return
	}

	workspaceManager, err := services.DefaultLocalPreviewWorkspaceManager()
	if err != nil {
		ErrorInternal(c, "Local preview workspace is unavailable")
		return
	}
	workspace, active := workspaceManager.Status(runtime.ID)

	processState := services.LocalPreviewStopped
	processError := ""
	if slot, ok := services.DefaultLocalPreviewManager().Status(runtime.ID); ok {
		processState = slot.State
		processError = slot.Error
	}

	response := gin.H{
		"enabled":                    true,
		"generator":                  runtime.Generator,
		"status":                     string(processState),
		"process_state":              processState,
		"process_error":              processError,
		"preview_url":                runtime.LocalPreview.URL,
		"workspace_active":           active,
		"workspace_rebuild_required": workspaceManager.RebuildRequired(runtime.ID),
	}
	persistent := services.DefaultLocalPreviewManager().PersistentStatus(runtime.ID, runtime.LocalPreview)
	response["always_on"] = persistent.AlwaysOn
	response["supervisor_state"] = persistent.SupervisorState
	if persistent.RefreshTimezone != "" {
		response["refresh_timezone"] = persistent.RefreshTimezone
	}
	if !persistent.NextRefreshAt.IsZero() {
		response["next_refresh_at"] = persistent.NextRefreshAt.Format(time.RFC3339)
	}
	if active && !workspace.LastActivityAt.IsZero() {
		age := time.Since(workspace.LastActivityAt)
		if age < 0 {
			age = 0
		}
		response["last_activity_age_seconds"] = int(age.Seconds())
	}
	c.JSON(http.StatusOK, response)
}

func StopLocalPreview(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	workspaceManager, err := services.DefaultLocalPreviewWorkspaceManager()
	if err != nil {
		ErrorInternal(c, "Local preview workspace is unavailable")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), services.DefaultLocalPreviewStopTimeout)
	resetErr := services.DefaultLocalPreviewManager().ResetRuntime(ctx, runtime.ID, workspaceManager)
	cancel()
	if resetErr != nil {
		slog.Error("Failed to reset Local Live Preview", "site", runtime.ID, "error", resetErr)
		ErrorInternal(c, "Failed to reset Local Live Preview")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "stopped"})
}
