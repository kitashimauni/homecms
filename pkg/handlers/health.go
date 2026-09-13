package handlers

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"hugo-cms/pkg/config"

	"github.com/gin-gonic/gin"
)

var startTime = time.Now()

// HealthCheck returns basic health status of the server
// This endpoint is always available, even if dependencies are unhealthy
func HealthCheck(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"uptime":    time.Since(startTime).String(),
		"version":   "1.0.0",
	})
}

// ReadinessCheck performs deeper health checks on dependencies
// Returns 503 if any critical dependency is unhealthy
func ReadinessCheck(c *gin.Context) {
	allHealthy := true
	siteResults := gin.H{}
	runtimes := readinessRuntimes()
	var singleSiteChecks gin.H
	for _, runtime := range runtimes {
		checks, healthy := checkSiteReadiness(runtime)
		siteResult := gin.H{
			"healthy": healthy,
			"checks":  checks,
		}
		siteResults[runtime.ID] = siteResult
		if !healthy {
			allHealthy = false
		}
		if len(runtimes) == 1 {
			singleSiteChecks = checks
		}
	}

	response := gin.H{
		"status":    getOverallStatus(allHealthy),
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"uptime":    time.Since(startTime).String(),
		"sites":     siteResults,
	}
	if singleSiteChecks != nil {
		// Preserve the original single-site response contract while exposing
		// the site-scoped shape for multi-site deployments.
		response["checks"] = singleSiteChecks
	}

	if allHealthy {
		c.JSON(http.StatusOK, response)
	} else {
		c.JSON(http.StatusServiceUnavailable, response)
	}
}

func readinessRuntimes() []config.SiteRuntime {
	if len(config.Sites) == 0 {
		return []config.SiteRuntime{config.CurrentSiteRuntime()}
	}

	runtimes := make([]config.SiteRuntime, 0, len(config.Sites))
	for _, site := range config.Sites {
		runtimes = append(runtimes, config.NewSiteRuntime(site))
	}
	return runtimes
}

func checkSiteReadiness(runtime config.SiteRuntime) (gin.H, bool) {
	contentDir := filepath.Join(runtime.RepoPath, runtime.ContentDir)
	contentAccessible := isDirAccessible(contentDir)
	gitHealthy := isGitRepoHealthy(runtime.RepoPath)
	checks := gin.H{
		"content_dir": gin.H{"healthy": contentAccessible},
		"git_repo":    gin.H{"healthy": gitHealthy},
	}
	return checks, contentAccessible && gitHealthy
}

func getOverallStatus(healthy bool) string {
	if healthy {
		return "ok"
	}
	return "degraded"
}

func isDirAccessible(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func isGitRepoHealthy(repoPath string) bool {
	if !isDirAccessible(repoPath) {
		return false
	}
	gitDir := filepath.Join(repoPath, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return false
	}
	if info.IsDir() {
		return true
	}
	if !info.Mode().IsRegular() {
		return false
	}
	content, err := os.ReadFile(gitDir)
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(content)), "gitdir:")
}
