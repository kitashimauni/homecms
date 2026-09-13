package handlers

import (
	"context"
	"hugo-cms/pkg/config"
	"hugo-cms/pkg/services"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// LocalPreviewIngress routes requests in the configured preview hostname
// namespace before the CMS session/auth middleware runs. Viewer authentication
// belongs to the external preview ingress (for example Tailscale or Cloudflare
// Access); the CMS admin session cookie is deliberately not reused here.
func LocalPreviewIngress(manager *services.LocalPreviewManager) gin.HandlerFunc {
	return localPreviewIngress(manager)
}

type localPreviewRuntimeProxy interface {
	ProxyRuntime(http.ResponseWriter, *http.Request, config.SiteRuntime) error
}

type localPreviewRuntimePreparer interface {
	PrepareProxyRuntime(config.SiteRuntime) (http.Handler, error)
}

func localPreviewIngress(manager localPreviewRuntimeProxy) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !config.IsLocalPreviewHostCandidate(c.Request.Host) {
			return
		}

		site, err := config.ResolveLocalPreviewHost(c.Request.Host)
		if err != nil {
			slog.Warn("Rejected local preview host", "host", c.Request.Host, "error", err)
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if services.IsLocalPreviewControlPath(c.Request.URL.Path) {
			// Readiness, metadata, and invalidation are loopback-only contracts
			// between the CMS and the Eleventy wrapper. LiveReload paths remain
			// proxyable, but the control plane must not be exposed on the
			// externally reachable preview hostname.
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if manager == nil {
			slog.Error("Local preview manager is not configured", "site", site.ID)
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		runtime := config.NewSiteRuntime(site)
		ctx, cancel := context.WithTimeout(c.Request.Context(), services.DefaultLocalPreviewStopTimeout)
		if err := services.RecoverLocalPreviewForRuntime(ctx, runtime); err != nil {
			cancel()
			slog.Warn("Failed to rebuild Local Live Preview workspace before ingress", "site", site.ID, "error", err)
			c.AbortWithStatus(http.StatusServiceUnavailable)
			return
		}
		cancel()

		var ingressLease services.LocalPreviewIngressLease
		// Phase 3 keeps unsaved editor content outside the production working
		// tree. Eleventy receives the workspace's project-root overlay so config,
		// collections, passthrough and plugins keep their normal project-relative
		// semantics.
		if workspaceManager, workspaceErr := services.DefaultLocalPreviewWorkspaceManager(); workspaceErr == nil {
			var transitioning bool
			var workspace services.LocalPreviewWorkspace
			var active bool
			ingressLease, workspace, active, transitioning = workspaceManager.AcquireIngress(site.ID)
			if transitioning {
				// Cleanup owns the stop-and-detach window. Do not fall back to saved
				// content or restart the old workspace process.
				ingressLease.Release()
				c.AbortWithStatus(http.StatusServiceUnavailable)
				return
			}
			if active {
				runtime.ContentDir = workspace.ContentDir
				if workspace.ProjectDir != "" {
					runtime.LocalPreviewSourceRepoPath = runtime.RepoPath
					runtime.RepoPath = workspace.ProjectDir
					runtime.LocalPreviewProjectDir = workspace.ProjectDir
				}
			}
		} else {
			slog.Warn("Local preview shadow workspace unavailable; serving saved content", "site", site.ID, "error", workspaceErr)
		}

		if preparer, ok := manager.(localPreviewRuntimePreparer); ok {
			proxy, err := preparer.PrepareProxyRuntime(runtime)
			ingressLease.Release()
			if err != nil {
				slog.Error("Local preview proxy failed", "site", site.ID, "error", err)
				c.AbortWithStatus(http.StatusBadGateway)
				return
			}
			proxy.ServeHTTP(c.Writer, c.Request)
			c.Abort()
			return
		}

		// Test doubles and legacy adapters that only provide ProxyRuntime keep
		// the gate for the duration of that call. The production manager above
		// exposes PrepareProxyRuntime so streaming responses do not hold it.
		defer ingressLease.Release()
		if err := manager.ProxyRuntime(c.Writer, c.Request, runtime); err != nil {
			slog.Error("Local preview proxy failed", "site", site.ID, "error", err)
			if !c.Writer.Written() {
				c.AbortWithStatus(http.StatusBadGateway)
			} else {
				c.Abort()
			}
			return
		}
		c.Abort()
	}
}
