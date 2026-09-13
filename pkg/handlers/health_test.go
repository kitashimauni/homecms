package handlers

import (
	"encoding/json"
	"hugo-cms/pkg/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestReadinessCheckReportsAllSites(t *testing.T) {
	originalSites := config.Sites
	t.Cleanup(func() { config.Sites = originalSites })

	healthyRepo := readinessTestRepo(t, true, true)
	unhealthyRepo := readinessTestRepo(t, false, true)
	config.Sites = []config.SiteConfig{
		{ID: "healthy", RepoPath: healthyRepo, ContentDir: "content"},
		{ID: "missing-content", RepoPath: unhealthyRepo, ContentDir: "content"},
	}

	recorder := performReadinessRequest()
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("ReadinessCheck status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}

	var response struct {
		Status string                            `json:"status"`
		Sites  map[string]map[string]interface{} `json:"sites"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode readiness response: %v", err)
	}
	if response.Status != "degraded" {
		t.Fatalf("readiness status = %q, want degraded", response.Status)
	}
	if len(response.Sites) != 2 {
		t.Fatalf("readiness sites = %#v, want two sites", response.Sites)
	}
	if response.Sites["healthy"]["healthy"] != true {
		t.Fatalf("healthy site result = %#v, want healthy", response.Sites["healthy"])
	}
	if response.Sites["missing-content"]["healthy"] != false {
		t.Fatalf("unhealthy site result = %#v, want unhealthy", response.Sites["missing-content"])
	}
	checks := response.Sites["missing-content"]["checks"].(map[string]interface{})
	if checks["content_dir"].(map[string]interface{})["healthy"] != false {
		t.Fatalf("missing content check = %#v, want false", checks["content_dir"])
	}
	if checks["git_repo"].(map[string]interface{})["healthy"] != true {
		t.Fatalf("git check = %#v, want true", checks["git_repo"])
	}
}

func TestReadinessCheckPreservesSingleSiteChecks(t *testing.T) {
	originalSites := config.Sites
	t.Cleanup(func() { config.Sites = originalSites })

	repo := readinessTestRepo(t, true, true)
	config.Sites = []config.SiteConfig{{ID: "default", RepoPath: repo, ContentDir: "content"}}

	recorder := performReadinessRequest()
	if recorder.Code != http.StatusOK {
		t.Fatalf("ReadinessCheck status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode readiness response: %v", err)
	}
	if _, ok := response["checks"]; !ok {
		t.Fatal("single-site readiness response lost the checks field")
	}
	if _, ok := response["sites"]; !ok {
		t.Fatal("single-site readiness response did not include site diagnostics")
	}
}

func readinessTestRepo(t *testing.T, content, git bool) string {
	t.Helper()
	repo := t.TempDir()
	if content {
		if err := os.Mkdir(filepath.Join(repo, "content"), 0755); err != nil {
			t.Fatalf("create content directory: %v", err)
		}
	}
	if git {
		if err := os.Mkdir(filepath.Join(repo, ".git"), 0755); err != nil {
			t.Fatalf("create git directory: %v", err)
		}
	}
	return repo
}

func performReadinessRequest() *httptest.ResponseRecorder {
	router := gin.New()
	router.GET("/ready", ReadinessCheck)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	return recorder
}
