package handlers

import (
	"errors"
	"hugo-cms/pkg/config"
	"hugo-cms/pkg/services"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

type deploymentPreviewRequest struct {
	Path    string `json:"path"`
	DraftID string `json:"draft_id"`
	Mode    string `json:"mode"`
}

func UpdateDeploymentPreview(c *gin.Context) {
	runtime, token, ok := deploymentRuntimeAndToken(c)
	if !ok {
		return
	}
	var req deploymentPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ErrorBadRequest(c, "Invalid JSON")
		return
	}
	paths, err := deploymentDraftPaths(runtime, req.Path)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	store, provider, ok := deploymentDependencies(c, runtime)
	if !ok {
		return
	}
	state, err := services.UpdateDraftPreview(c.Request.Context(), runtime, token, req.DraftID, req.Path, paths, store, provider)
	if err != nil {
		handleDeploymentError(c, "update", err)
		return
	}
	c.JSON(http.StatusOK, deploymentStateResponse(runtime, state))
}

func GetDeploymentPreview(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	store, provider, ok := deploymentDependencies(c, runtime)
	if !ok {
		return
	}
	state, err := services.RefreshDraftPreview(c.Request.Context(), runtime.ID, c.Param("draft_id"), store, provider)
	if errors.Is(err, os.ErrNotExist) {
		ErrorNotFound(c, "Deployment preview does not exist")
		return
	}
	if err != nil {
		handleDeploymentError(c, "status", err)
		return
	}
	c.JSON(http.StatusOK, deploymentStateResponse(runtime, state))
}

func RetryDeploymentPreview(c *gin.Context) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return
	}
	store, provider, ok := deploymentDependencies(c, runtime)
	if !ok {
		return
	}
	state, err := services.RetryDraftPreview(c.Request.Context(), runtime.ID, c.Param("draft_id"), store, provider)
	if errors.Is(err, os.ErrNotExist) {
		ErrorNotFound(c, "Deployment preview does not exist")
		return
	}
	if err != nil {
		handleDeploymentError(c, "retry", err)
		return
	}
	c.JSON(http.StatusOK, deploymentStateResponse(runtime, state))
}

func DiscardDeploymentPreview(c *gin.Context) {
	runtime, token, ok := deploymentRuntimeAndToken(c)
	if !ok {
		return
	}
	store, provider, ok := deploymentDependencies(c, runtime)
	if !ok {
		return
	}
	if err := services.CleanupDraftPreview(c.Request.Context(), runtime, token, c.Param("draft_id"), store, provider); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			ErrorNotFound(c, "Deployment preview does not exist")
			return
		}
		handleDeploymentError(c, "discard", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "discarded"})
}

func PublishDeploymentPreview(c *gin.Context) {
	runtime, token, ok := deploymentRuntimeAndToken(c)
	if !ok {
		return
	}
	var req deploymentPreviewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		ErrorBadRequest(c, "Invalid JSON")
		return
	}
	if strings.ToLower(filepath.Ext(strings.TrimSpace(req.Path))) != ".md" || services.SafeJoin(runtime.RepoPath, runtime.ContentDir, req.Path) == "" {
		ErrorBadRequest(c, "Invalid article path")
		return
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		if strings.TrimSpace(runtime.PreviewDeployment.Provider) == "" {
			mode = "direct"
		} else {
			mode = "preview"
		}
	}
	var prURL string
	var err error
	switch mode {
	case "direct":
		paths, pathsErr := deploymentDraftPaths(runtime, req.Path)
		if pathsErr != nil {
			ErrorBadRequest(c, pathsErr.Error())
			return
		}
		prURL, err = services.PublishArticle(c.Request.Context(), runtime, token, req.DraftID, req.Path, paths)
	case "preview":
		store, provider, ok := deploymentDependencies(c, runtime)
		if !ok {
			return
		}
		prURL, err = services.PublishDraftPreview(c.Request.Context(), runtime, token, req.DraftID, req.Path, store, provider)
	default:
		ErrorBadRequest(c, "Invalid publish mode")
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		ErrorNotFound(c, "Deployment preview does not exist")
		return
	}
	if err != nil {
		handleDeploymentError(c, "publish", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "url": prURL})
}

func deploymentRuntimeAndToken(c *gin.Context) (config.SiteRuntime, string, bool) {
	runtime, err := requestedRuntime(c)
	if err != nil {
		ErrorBadRequest(c, err.Error())
		return config.SiteRuntime{}, "", false
	}
	token, ok := sessions.Default(c).Get("access_token").(string)
	if !ok || strings.TrimSpace(token) == "" {
		ErrorUnauthorized(c, "Invalid session token")
		return config.SiteRuntime{}, "", false
	}
	return runtime, token, true
}

func deploymentDependencies(c *gin.Context, runtime config.SiteRuntime) (*services.DraftPreviewStore, services.PreviewDeploymentProvider, bool) {
	provider, err := services.NewPreviewDeploymentProvider(runtime)
	if err != nil {
		if services.IsPreviewProviderError(err, services.PreviewProviderNotConfigured) {
			ErrorConflict(c, "Deployment preview provider is not configured")
			return nil, nil, false
		}
		slog.Error("Failed to configure deployment preview provider", "site", runtime.ID, "error", err)
		ErrorInternal(c, "Deployment preview provider is unavailable")
		return nil, nil, false
	}
	root := strings.TrimSpace(os.Getenv("PREVIEW_STATE_DIR"))
	if root == "" {
		root = filepath.Join("data", "preview-deployments")
	}
	store, err := services.NewDraftPreviewStore(root)
	if err != nil {
		slog.Error("Failed to open deployment preview state store", "site", runtime.ID, "error", err)
		ErrorInternal(c, "Deployment preview state is unavailable")
		return nil, nil, false
	}
	return store, provider, true
}

func deploymentDraftPaths(runtime config.SiteRuntime, articlePath string) ([]string, error) {
	articlePath = strings.TrimSpace(articlePath)
	if strings.ToLower(filepath.Ext(articlePath)) != ".md" || services.SafeJoin(runtime.RepoPath, runtime.ContentDir, articlePath) == "" {
		return nil, errors.New("invalid article path")
	}
	repoArticle := filepath.ToSlash(filepath.Join(runtime.ContentDir, filepath.FromSlash(articlePath)))
	paths := []string{repoArticle}
	base := strings.ToLower(filepath.Base(articlePath))
	if base == "index.md" || base == "_index.md" {
		paths[0] = filepath.ToSlash(filepath.Dir(filepath.FromSlash(repoArticle)))
		return paths, nil
	}
	content, err := os.ReadFile(services.SafeJoin(runtime.RepoPath, runtime.ContentDir, articlePath))
	if err != nil {
		return nil, errors.New("article must be saved before deployment preview")
	}
	_, body, _, err := services.ParseFrontMatter(content)
	if err != nil {
		body = string(content)
	}
	paths = append(paths, services.MarkdownPreviewMediaPaths(runtime, articlePath, body)...)
	return paths, nil
}

func handleDeploymentError(c *gin.Context, operation string, err error) {
	if operation == "publish" {
		handlePublishError(c, err)
		return
	}
	if errors.Is(err, services.ErrDraftPreviewArticleMismatch) ||
		errors.Is(err, services.ErrDraftPreviewNotReady) ||
		errors.Is(err, services.ErrDraftPreviewStale) ||
		errors.Is(err, services.ErrDraftPreviewBranchMoved) {
		ErrorConflict(c, err.Error())
		return
	}
	if services.IsPreviewProviderError(err, services.PreviewProviderInvalidInput) || strings.Contains(strings.ToLower(err.Error()), "invalid draft") {
		ErrorBadRequest(c, "Invalid deployment preview request")
		return
	}
	if services.IsPreviewProviderError(err, services.PreviewProviderConflict) {
		ErrorConflict(c, "Deployment preview operation conflicts with provider state")
		return
	}
	slog.Error("Deployment preview operation failed", "operation", operation, "error", err)
	ErrorInternal(c, "Deployment preview operation failed")
}

const (
	publishArticleMismatchCode = "PUBLISH_ARTICLE_MISMATCH"
	publishPreviewNotReadyCode = "PUBLISH_PREVIEW_NOT_READY"
	publishPreviewStaleCode    = "PUBLISH_PREVIEW_STALE"
	publishBranchMovedCode     = "PUBLISH_BRANCH_MOVED"
	publishGitPushCode         = "PUBLISH_GIT_PUSH_FAILED"
	publishBranchCheckCode     = "PUBLISH_BRANCH_CHECK_FAILED"
	publishPullRequestCode     = "PUBLISH_PULL_REQUEST_FAILED"
	publishProviderCode        = "PUBLISH_PROVIDER_FAILED"
	publishStateCode           = "PUBLISH_STATE_FAILED"
	publishFailedCode          = "PUBLISH_FAILED"
)

func handlePublishError(c *gin.Context, err error) {
	errorMessage := strings.ToLower(err.Error())

	switch {
	case errors.Is(err, services.ErrDraftPreviewArticleMismatch):
		RespondError(c, http.StatusConflict, publishArticleMismatchCode, "The deployment preview article does not match the selected article")
	case errors.Is(err, services.ErrDraftPreviewNotReady):
		RespondError(c, http.StatusConflict, publishPreviewNotReadyCode, "The deployment preview is not ready for Publish")
	case errors.Is(err, services.ErrDraftPreviewStale):
		RespondError(c, http.StatusConflict, publishPreviewStaleCode, "The deployment preview is stale; update it or use direct Publish")
	case errors.Is(err, services.ErrDraftPreviewBranchMoved):
		RespondError(c, http.StatusConflict, publishBranchMovedCode, "The Publish branch changed while it was being verified")
	case services.IsPreviewProviderError(err, services.PreviewProviderInvalidInput):
		RespondError(c, http.StatusBadRequest, publishProviderCode, "The deployment preview provider rejected the Publish request")
	case services.IsPreviewProviderError(err, services.PreviewProviderUnauthorized),
		services.IsPreviewProviderError(err, services.PreviewProviderForbidden):
		RespondError(c, http.StatusBadGateway, publishProviderCode, "The deployment preview provider authentication failed")
	case services.IsPreviewProviderError(err, services.PreviewProviderConflict),
		services.IsPreviewProviderError(err, services.PreviewProviderRateLimited),
		services.IsPreviewProviderError(err, services.PreviewProviderUnavailable),
		services.IsPreviewProviderError(err, services.PreviewProviderNotFound),
		services.IsPreviewProviderError(err, services.PreviewProviderInvalidReply):
		RespondError(c, http.StatusBadGateway, publishProviderCode, "The deployment preview provider could not complete Publish")
	case strings.Contains(errorMessage, "github token is required"):
		RespondError(c, http.StatusUnauthorized, ErrCodeUnauthorized, "The GitHub session token is missing or expired")
	case strings.Contains(errorMessage, "push draft branch"):
		RespondError(c, http.StatusBadGateway, publishGitPushCode, "GitHub rejected the Publish branch push")
	case strings.Contains(errorMessage, "read remote draft branch"):
		RespondError(c, http.StatusBadGateway, publishBranchCheckCode, "The Publish branch could not be verified on GitHub")
	case strings.Contains(errorMessage, "github") || strings.Contains(errorMessage, "pull request"):
		RespondError(c, http.StatusBadGateway, publishPullRequestCode, "The GitHub pull request could not be created or verified")
	case strings.Contains(errorMessage, "draft") || strings.Contains(errorMessage, "state"):
		RespondError(c, http.StatusConflict, publishStateCode, "The Publish state is inconsistent; refresh the article and try again")
	default:
		slog.Error("Publish operation failed", "error", err)
		RespondError(c, http.StatusInternalServerError, publishFailedCode, "Publish failed due to an internal server error")
	}
}

func deploymentStateResponse(runtime config.SiteRuntime, state services.DraftPreviewState) gin.H {
	response := gin.H{
		"site_id":          state.SiteID,
		"draft_id":         state.DraftID,
		"article_path":     state.ArticlePath,
		"branch":           state.Branch,
		"commit_sha":       state.CommitSHA,
		"deployment_id":    state.DeploymentID,
		"status":           state.Status,
		"url":              state.URL,
		"failure_reason":   state.FailureReason,
		"access_protected": state.AccessProtected,
		"cleanup_pending":  state.CleanupPending,
		"created_at":       state.CreatedAt,
		"updated_at":       state.UpdatedAt,
		"retryable":        state.Status == services.PreviewDeploymentFailed,
	}
	if state.Status == services.PreviewDeploymentFailed && state.DeploymentID != "" && runtime.PreviewDeployment.Provider == "cloudflare_pages" {
		account := url.PathEscape(runtime.PreviewDeployment.CloudflarePages.AccountID)
		project := url.PathEscape(runtime.PreviewDeployment.CloudflarePages.ProjectName)
		deployment := url.PathEscape(state.DeploymentID)
		response["log_url"] = "https://dash.cloudflare.com/" + account + "/pages/view/" + project + "/" + deployment
	}
	return response
}
