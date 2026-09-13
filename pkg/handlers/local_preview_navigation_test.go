package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"hugo-cms/pkg/config"
	"hugo-cms/pkg/services"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type fakeLocalPreviewNavigationWorkspaceManager struct {
	workspace     services.LocalPreviewWorkspace
	active        bool
	transitioning bool
}

func (m *fakeLocalPreviewNavigationWorkspaceManager) AcquireNavigation(string) (services.LocalPreviewIngressLease, services.LocalPreviewWorkspace, bool, bool) {
	return services.LocalPreviewIngressLease{}, m.workspace, m.active, m.transitioning
}

func TestNavigateLocalPreviewUsesShadowContentAndPreservesProductionContent(t *testing.T) {
	site, runtime, workspaceManager := newLocalPreviewNavigationFixture(t)
	productionArticle := filepath.Join(runtime.RepoPath, runtime.ContentDir, "one.md")
	if err := os.WriteFile(productionArticle, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	workspace, _, _, err := workspaceManager.Update(runtime, "one.md", 7, []byte("draft"))
	if err != nil {
		t.Fatal(err)
	}

	var resolvedRuntime config.SiteRuntime
	response := executeLocalPreviewNavigationRequest(t, site.ID, localPreviewNavigationDependencies{
		workspaceManager: workspaceManager,
		resolveArticleURL: func(_ context.Context, runtime config.SiteRuntime, gotWorkspace services.LocalPreviewWorkspace, path string) (string, error) {
			resolvedRuntime = runtime
			if gotWorkspace.ContentDir != workspace.ContentDir || path != "one.md" {
				t.Fatalf("resolver arguments = runtime=%#v workspace=%#v path=%q", runtime, gotWorkspace, path)
			}
			return "https://tech.preview.example.com/posts/one/", nil
		},
	}, "one.md")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["status"] != "resolved" || payload["article_url"] != "https://tech.preview.example.com/posts/one/" || int(payload["revision"].(float64)) != 1 {
		t.Fatalf("response = %#v", payload)
	}
	if resolvedRuntime.ContentDir != workspace.ContentDir {
		t.Fatalf("resolver content directory = %q, want shadow directory %q", resolvedRuntime.ContentDir, workspace.ContentDir)
	}
	if got, err := os.ReadFile(productionArticle); err != nil || string(got) != "original" {
		t.Fatalf("production content = %q err=%v, want original", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(workspace.ContentDir, "one.md")); err != nil || string(got) != "draft" {
		t.Fatalf("shadow content = %q err=%v, want draft", got, err)
	}
}

func TestNavigateLocalPreviewAcceptsAnyTabAndArticle(t *testing.T) {
	site, runtime, workspaceManager := newLocalPreviewNavigationFixture(t)
	if _, _, _, err := workspaceManager.Update(runtime, "one.md", 1, []byte("draft")); err != nil {
		t.Fatal(err)
	}
	resolveCalls := 0
	response := executeLocalPreviewNavigationRequest(t, site.ID, localPreviewNavigationDependencies{
		workspaceManager: workspaceManager,
		resolveArticleURL: func(context.Context, config.SiteRuntime, services.LocalPreviewWorkspace, string) (string, error) {
			resolveCalls++
			return "https://tech.preview.example.com/other/", nil
		},
	}, "other.md")
	if response.Code != http.StatusOK || resolveCalls != 1 {
		t.Fatalf("status=%d resolver calls=%d", response.Code, resolveCalls)
	}
}

func TestNavigateLocalPreviewReturnsMetadataFreshness(t *testing.T) {
	site, _ := localPreviewNavigationTestSite(t)
	configureLocalPreviewNavigationTestSite(t, site)
	workspaceManager := &fakeLocalPreviewNavigationWorkspaceManager{
		workspace: services.LocalPreviewWorkspace{SiteID: site.ID, ArticlePath: "one.md", ContentDir: filepath.Join(site.RepoPath, "shadow", "content"), Revision: 4},
		active:    true,
	}
	response := executeLocalPreviewNavigationRequest(t, site.ID, localPreviewNavigationDependencies{
		workspaceManager: workspaceManager,
		resolveArticleURLWithMetadata: func(context.Context, config.SiteRuntime, services.LocalPreviewWorkspace, string) (services.PreviewArticleURLResolution, error) {
			return services.PreviewArticleURLResolution{
				URL:                    "https://tech.preview.example.com/old/",
				Fresh:                  false,
				Status:                 "stale",
				InvalidationGeneration: 12,
				ActiveBuildGeneration:  11,
			}, nil
		},
	}, "one.md")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload map[string]interface{}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["fresh"] != false || payload["metadata_status"] != "stale" || payload["article_url"] != "https://tech.preview.example.com/old/" {
		t.Fatalf("response = %#v", payload)
	}
	if int(payload["invalidation_generation"].(float64)) != 12 || int(payload["active_build_generation"].(float64)) != 11 {
		t.Fatalf("response generations = %#v", payload)
	}
}

func TestNavigateLocalPreviewRequiresActiveWorkspace(t *testing.T) {
	site, _ := localPreviewNavigationTestSite(t)
	configureLocalPreviewNavigationTestSite(t, site)
	workspaceManager := &fakeLocalPreviewNavigationWorkspaceManager{}
	response := executeLocalPreviewNavigationRequest(t, site.ID, localPreviewNavigationDependencies{
		workspaceManager: workspaceManager,
		resolveArticleURL: func(context.Context, config.SiteRuntime, services.LocalPreviewWorkspace, string) (string, error) {
			return "", nil
		},
	}, "one.md")
	if response.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", response.Code)
	}
}

func TestNavigateLocalPreviewReturnsErrorWhenResolverFails(t *testing.T) {
	site, _ := localPreviewNavigationTestSite(t)
	configureLocalPreviewNavigationTestSite(t, site)
	workspaceManager := &fakeLocalPreviewNavigationWorkspaceManager{
		workspace: services.LocalPreviewWorkspace{SiteID: site.ID, ArticlePath: "one.md", ContentDir: filepath.Join(site.RepoPath, "shadow", "content")},
		active:    true,
	}
	response := executeLocalPreviewNavigationRequest(t, site.ID, localPreviewNavigationDependencies{
		workspaceManager: workspaceManager,
		resolveArticleURL: func(context.Context, config.SiteRuntime, services.LocalPreviewWorkspace, string) (string, error) {
			return "", errors.New("generator failed")
		},
	}, "one.md")
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", response.Code)
	}
}

func TestNavigateLocalPreviewHoldsReadGateDuringURLResolution(t *testing.T) {
	site, runtime, manager := newLocalPreviewNavigationFixture(t)
	if _, _, _, err := manager.Update(runtime, "one.md", 1, []byte("draft")); err != nil {
		t.Fatal(err)
	}
	resolverStarted := make(chan struct{})
	allowResolve := make(chan struct{})
	requestDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		requestDone <- executeLocalPreviewNavigationRequest(t, site.ID, localPreviewNavigationDependencies{
			workspaceManager: manager,
			resolveArticleURL: func(context.Context, config.SiteRuntime, services.LocalPreviewWorkspace, string) (string, error) {
				close(resolverStarted)
				<-allowResolve
				return "https://tech.preview.example.com/one/", nil
			},
		}, "one.md")
	}()
	select {
	case <-resolverStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("URL resolver did not start")
	}
	cleanupDone := make(chan *struct {
		lease   *services.LocalPreviewCleanupLease
		claimed bool
		err     error
	}, 1)
	go func() {
		lease, claimed, err := manager.BeginCleanup(site.ID)
		cleanupDone <- &struct {
			lease   *services.LocalPreviewCleanupLease
			claimed bool
			err     error
		}{lease: &lease, claimed: claimed, err: err}
	}()
	select {
	case <-cleanupDone:
		t.Fatal("cleanup crossed the navigation read lease")
	case <-time.After(50 * time.Millisecond):
	}
	close(allowResolve)
	select {
	case response := <-requestDone:
		if response.Code != http.StatusOK {
			t.Fatalf("navigation status = %d, body = %s", response.Code, response.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("navigation did not finish")
	}
	select {
	case result := <-cleanupDone:
		if result.err != nil || !result.claimed {
			t.Fatalf("cleanup claimed=%v err=%v", result.claimed, result.err)
		}
		if _, err := manager.FinishCleanup(result.lease); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup did not acquire the gate after navigation")
	}
}

func executeLocalPreviewNavigationRequest(t *testing.T, siteID string, dependencies localPreviewNavigationDependencies, articlePath string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/admin/api/preview/local/navigate", func(c *gin.Context) {
		navigateLocalPreview(c, dependencies)
	})
	body, err := json.Marshal(map[string]string{"path": articlePath})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/admin/api/preview/local/navigate?site="+siteID, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	return response
}

func localPreviewNavigationTestSite(t *testing.T) (config.SiteConfig, config.SiteRuntime) {
	t.Helper()
	enabled := true
	site := config.SiteConfig{
		ID: "tech", Generator: "hugo", RepoPath: t.TempDir(), ContentDir: "content",
		Preview: config.SitePreviewConfig{LocalPreview: config.LocalPreviewConfig{Enabled: &enabled, URL: "http://tech.preview.example.com/"}},
	}
	if err := os.MkdirAll(filepath.Join(site.RepoPath, site.ContentDir), 0755); err != nil {
		t.Fatal(err)
	}
	return site, config.NewSiteRuntime(site)
}

func configureLocalPreviewNavigationTestSite(t *testing.T, site config.SiteConfig) {
	t.Helper()
	previousSites := config.Sites
	previousDefaultSiteID := config.DefaultSiteID
	config.Sites = []config.SiteConfig{site}
	config.DefaultSiteID = site.ID
	t.Cleanup(func() {
		config.Sites = previousSites
		config.DefaultSiteID = previousDefaultSiteID
	})
}

func newLocalPreviewNavigationFixture(t *testing.T) (config.SiteConfig, config.SiteRuntime, *services.LocalPreviewWorkspaceManager) {
	t.Helper()
	site, runtime := localPreviewNavigationTestSite(t)
	configureLocalPreviewNavigationTestSite(t, site)
	manager, err := services.NewLocalPreviewWorkspaceManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalPreviewWorkspaceManager() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown() })
	return site, runtime, manager
}
