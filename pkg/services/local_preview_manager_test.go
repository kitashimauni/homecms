package services

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"hugo-cms/pkg/config"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLocalPreviewTimeoutEnvironmentNames(t *testing.T) {
	t.Run("new names are used", func(t *testing.T) {
		t.Setenv(localPreviewStartupTimeoutEnv, "7s")
		t.Setenv(legacyLocalPreviewStartupTimeoutEnv, "9s")
		t.Setenv(localPreviewIdleTimeoutEnv, "11s")
		t.Setenv(legacyLocalPreviewIdleTimeoutEnv, "13s")
		if got := configuredLocalPreviewStartupTimeout(); got != 7*time.Second {
			t.Fatalf("startup timeout = %s, want 7s", got)
		}
		if got := configuredLocalPreviewIdleTimeout(); got != 11*time.Second {
			t.Fatalf("idle timeout = %s, want 11s", got)
		}
	})

	t.Run("legacy names remain supported", func(t *testing.T) {
		t.Setenv(localPreviewStartupTimeoutEnv, "")
		t.Setenv(localPreviewIdleTimeoutEnv, "")
		t.Setenv(legacyLocalPreviewStartupTimeoutEnv, "7s")
		t.Setenv(legacyLocalPreviewIdleTimeoutEnv, "11s")
		if got := configuredLocalPreviewStartupTimeout(); got != 7*time.Second {
			t.Fatalf("legacy startup timeout = %s, want 7s", got)
		}
		if got := configuredLocalPreviewIdleTimeout(); got != 11*time.Second {
			t.Fatalf("legacy idle timeout = %s, want 11s", got)
		}
	})
}

func TestHugoLocalPreviewArgs(t *testing.T) {
	runtime := config.SiteRuntime{ContentDir: "content"}
	got, err := hugoLocalPreviewArgs(runtime, 14123, "https://tech.preview.example.com/")
	if err != nil {
		t.Fatalf("hugoLocalPreviewArgs() error = %v", err)
	}
	want := []string{
		"server",
		"--source", ".",
		"--environment", localPreviewHugoEnvironment,
		"--contentDir", "content",
		"--bind", "127.0.0.1",
		"--port", "14123",
		"--baseURL", "https://tech.preview.example.com/",
		"--appendPort=false",
		"--liveReloadPort", "443",
		"--renderToMemory",
		"--buildDrafts",
		"--buildFuture",
		"--buildExpired",
		"--watch",
		"--noHTTPCache",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("hugoLocalPreviewArgs() = %#v, want %#v", got, want)
	}
}

func TestHugoLocalPreviewArgsUsesHTTPReloadPort(t *testing.T) {
	args, err := hugoLocalPreviewArgs(config.SiteRuntime{ContentDir: "content"}, 14123, "http://tech.preview.example.com/")
	if err != nil {
		t.Fatalf("hugoLocalPreviewArgs() error = %v", err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--liveReloadPort 80") {
		t.Fatalf("args = %q, want live reload port 80", joined)
	}
}

func TestHugoLocalPreviewArgsRejectsURLPort(t *testing.T) {
	if _, err := hugoLocalPreviewArgs(config.SiteRuntime{ContentDir: "content"}, 14123, "https://tech.preview.example.com:8443/"); err == nil {
		t.Fatal("hugoLocalPreviewArgs() should reject an external URL port")
	}
}

func TestEleventyLocalPreviewArgs(t *testing.T) {
	repo := t.TempDir()
	runtime := config.SiteRuntime{
		RepoPath:             repo,
		ContentDir:           filepath.Join(t.TempDir(), "shadow", "content"),
		ProductionContentDir: "content",
	}
	outputDir := filepath.Join(t.TempDir(), "homecms-local-preview", "output")
	got, err := eleventyLocalPreviewArgs(runtime, 14123, outputDir)
	if err != nil {
		t.Fatalf("eleventyLocalPreviewArgs() error = %v", err)
	}
	scriptPath, err := eleventyLocalPreviewScriptPath()
	if err != nil {
		t.Fatalf("eleventyLocalPreviewScriptPath() error = %v", err)
	}
	want := []string{
		"node", scriptPath,
		"--serve",
		"--input", "content",
		"--output", outputDir,
		"--port", "14123",
		"--host", LocalPreviewBindAddress,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("eleventyLocalPreviewArgs() = %#v, want %#v", got, want)
	}
}

func TestEleventyLocalPreviewArgsRejectsRelativeOutput(t *testing.T) {
	if _, err := eleventyLocalPreviewArgs(config.SiteRuntime{ContentDir: "content"}, 14123, "_site"); err == nil {
		t.Fatal("eleventyLocalPreviewArgs() should reject a relative output directory")
	}
}

func TestEleventyNodeCommandArgsUsesDetectedPackageManager(t *testing.T) {
	for _, testCase := range []struct {
		name string
		pm   eleventyPackageManager
		want []string
	}{
		{name: "npm", pm: eleventyPackageManager{Name: "npm"}, want: []string{"exec", "--", "node", "helper.cjs", "--json"}},
		{name: "pnpm", pm: eleventyPackageManager{Name: "pnpm"}, want: []string{"exec", "node", "helper.cjs", "--json"}},
		{name: "yarn", pm: eleventyPackageManager{Name: "yarn"}, want: []string{"exec", "node", "helper.cjs", "--json"}},
		{name: "bun", pm: eleventyPackageManager{Name: "bun"}, want: []string{"run", "node", "helper.cjs", "--json"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := eleventyNodeCommandArgs(testCase.pm, "helper.cjs", "--json"); !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("eleventyNodeCommandArgs() = %#v, want %#v", got, testCase.want)
			}
		})
	}
}

func TestEleventyLocalPreviewCommandUsesDetectedPackageManagerAndLoopbackServer(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "content"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{"devDependencies":{"@11ty/eleventy":"^3.0.0"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "package-lock.json"), []byte(`{"lockfileVersion":3}`), 0644); err != nil {
		t.Fatal(err)
	}
	runtime := config.SiteRuntime{
		ID:                   "daily-blog",
		RepoPath:             repo,
		Generator:            "eleventy",
		ContentDir:           "content",
		ProductionContentDir: "content",
	}
	cmd, err := eleventyLocalPreviewCommand(context.Background(), runtime, 14123)
	if err != nil {
		t.Fatalf("eleventyLocalPreviewCommand() error = %v", err)
	}
	cleanup := localPreviewProcessCleanup(runtime, cmd.Dir)
	t.Cleanup(func() {
		if err := cleanup(); err != nil {
			t.Errorf("cleanup() error = %v", err)
		}
	})
	joined := strings.Join(cmd.Args, " ")
	for _, want := range []string{"npm", "exec", "--", "node", "eleventy-local-preview.cjs", "--serve", "--input", "content", "--output", "--port", "14123", "--host", LocalPreviewBindAddress} {
		if !strings.Contains(joined, want) {
			t.Fatalf("command args = %q, missing %q", joined, want)
		}
	}
	if cmd.Dir == repo || !strings.Contains(cmd.Dir, "homecms-local-preview") {
		t.Fatalf("command dir = %q, want an isolated Eleventy project root", cmd.Dir)
	}
}

func TestLocalPreviewManagerSupportsEleventy(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)
	site.Generator = "eleventy"
	defer shutdownTestLocalPreviewManager(t, manager)

	if _, err := manager.EnsureReady(site); err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
}

func TestLocalPreviewManagerReusesSiteRuntimeForRepeatedPreparation(t *testing.T) {
	for _, generator := range []string{"hugo", "eleventy"} {
		t.Run(generator, func(t *testing.T) {
			manager, site := newTestLocalPreviewManager(t)
			site.Generator = generator
			defer shutdownTestLocalPreviewManager(t, manager)

			first, err := manager.EnsureReady(site)
			if err != nil {
				t.Fatalf("first EnsureReady() error = %v", err)
			}
			firstProcess := manager.process(site.ID)
			second, err := manager.EnsureReady(site)
			if err != nil {
				t.Fatalf("second EnsureReady() error = %v", err)
			}
			if first.Port != second.Port || firstProcess == nil || manager.process(site.ID) != firstProcess {
				t.Fatalf("site runtime was recreated: first=%#v second=%#v process_changed=%v", first, second, manager.process(site.ID) != firstProcess)
			}
		})
	}
}

func TestLocalPreviewManagerResolvesEleventyURLFromRunningProcess(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)
	site.Generator = "eleventy"
	defer shutdownTestLocalPreviewManager(t, manager)

	runtime := config.NewSiteRuntime(site)
	workspace := LocalPreviewWorkspace{
		SiteID:      site.ID,
		ArticlePath: "posts/one.md",
		ContentDir:  filepath.Join(t.TempDir(), "content"),
	}
	got, err := manager.ResolveArticleURL(context.Background(), runtime, workspace, workspace.ArticlePath)
	if err != nil {
		t.Fatalf("ResolveArticleURL() error = %v", err)
	}
	if got != "http://tech.preview.example.com/posts/one/" {
		t.Fatalf("resolved URL = %q", got)
	}
}

func TestLocalPreviewManagerInvalidatesEleventyMetadata(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)
	site.Generator = "eleventy"
	defer shutdownTestLocalPreviewManager(t, manager)

	if _, err := manager.EnsureReady(site); err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
	if err := manager.InvalidateArticleURL(config.NewSiteRuntime(site)); err != nil {
		t.Fatalf("InvalidateArticleURL() error = %v", err)
	}
}

func TestLocalPreviewManagerEnsureReadyAndProxy(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)
	defer shutdownTestLocalPreviewManager(t, manager)

	slot, err := manager.EnsureReady(site)
	if err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
	if slot.State != LocalPreviewReady {
		t.Fatalf("slot.State = %q, want ready", slot.State)
	}

	request := httptest.NewRequest(http.MethodGet, "http://tech.preview.example.com/css/main.css", nil)
	request.Host = "tech.preview.example.com"
	response := httptest.NewRecorder()
	if err := manager.Proxy(response, request, site); err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "path=/css/main.css") {
		t.Fatalf("proxy changed root-relative path: %q", body)
	}
	if !strings.Contains(body, "host=tech.preview.example.com") {
		t.Fatalf("proxy did not preserve external host: %q", body)
	}
	if !strings.Contains(body, "proto=http") {
		t.Fatalf("proxy did not set external scheme: %q", body)
	}
}

func TestLocalPreviewProxyRewritesInternalLocation(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)
	defer shutdownTestLocalPreviewManager(t, manager)

	request := httptest.NewRequest(http.MethodGet, "http://tech.preview.example.com/redirect", nil)
	request.Host = "tech.preview.example.com"
	response := httptest.NewRecorder()
	if err := manager.Proxy(response, request, site); err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if response.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", response.Code)
	}
	if got := response.Header().Get("Location"); got != "http://tech.preview.example.com/target" {
		t.Fatalf("Location = %q", got)
	}
}

func TestLocalPreviewProxyPassesUpgrade(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)
	defer shutdownTestLocalPreviewManager(t, manager)

	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := manager.Proxy(w, r, site); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
		}
	}))
	defer outer.Close()

	address := strings.TrimPrefix(outer.URL, "http://")
	conn, err := net.DialTimeout("tcp", address, 2*time.Second)
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	defer conn.Close()

	if _, err := fmt.Fprintf(conn, "GET /_livereload HTTP/1.1\r\nHost: tech.preview.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
		t.Fatalf("write upgrade request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("ReadResponse() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", response.StatusCode)
	}
}

func TestLocalPreviewManagerStopReleasesSlot(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)

	if _, err := manager.EnsureReady(site); err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Stop(ctx, site.ID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, ok := manager.Status(site.ID); ok {
		t.Fatal("Stop() should release the lifecycle slot")
	}
}

func TestLocalPreviewManagerProcessExitDoesNotReleaseStoppingSlot(t *testing.T) {
	lifecycle, err := NewLocalPreviewLifecycle(14100, 14100)
	if err != nil {
		t.Fatalf("NewLocalPreviewLifecycle() error = %v", err)
	}
	manager := NewLocalPreviewManager(lifecycle)
	const siteID = "tech"
	if _, err := lifecycle.Reserve(siteID, nil); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if _, err := lifecycle.Transition(siteID, LocalPreviewStarting, nil); err != nil {
		t.Fatalf("Transition(starting) error = %v", err)
	}
	if _, err := lifecycle.Transition(siteID, LocalPreviewReady, nil); err != nil {
		t.Fatalf("Transition(ready) error = %v", err)
	}
	if _, err := lifecycle.Transition(siteID, LocalPreviewStopping, nil); err != nil {
		t.Fatalf("Transition(stopping) error = %v", err)
	}

	terminated := make(chan struct{})
	close(terminated)
	process := &managedLocalPreviewProcess{
		cmd:  &exec.Cmd{},
		done: terminated,
	}
	manager.setProcess(siteID, process)

	manager.handleProcessExit(siteID, process)

	slot, ok := manager.Status(siteID)
	if !ok {
		t.Fatal("process exit callback released a stopping lifecycle slot")
	}
	if slot.State != LocalPreviewStopping {
		t.Fatalf("slot.State = %q, want stopping", slot.State)
	}
	if manager.process(siteID) != process {
		t.Fatal("process exit callback removed the process mapping while stopping")
	}
}

func TestLocalPreviewManagerResetRuntimeStopsAndDetachesWorkspace(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)
	defer shutdownTestLocalPreviewManager(t, manager)

	repo := t.TempDir()
	contentDir := filepath.Join(repo, "content")
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		t.Fatal(err)
	}
	productionPath := filepath.Join(contentDir, "one.md")
	if err := os.WriteFile(productionPath, []byte("production before sync"), 0644); err != nil {
		t.Fatal(err)
	}
	site.RepoPath = repo

	workspaceManager, err := NewLocalPreviewWorkspaceManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := config.NewSiteRuntime(site)
	workspace, created, applied, err := workspaceManager.Update(runtime, "one.md", 1, []byte("unsaved draft"))
	if err != nil || !created || !applied {
		t.Fatalf("Update() created=%v applied=%v err=%v", created, applied, err)
	}

	if _, err := manager.EnsureReady(site); err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.ResetRuntime(ctx, site.ID, workspaceManager); err != nil {
		t.Fatalf("ResetRuntime() error = %v", err)
	}
	if _, ok := manager.Status(site.ID); ok {
		t.Fatal("ResetRuntime() should release the generator lifecycle slot")
	}
	if _, active := workspaceManager.Status(site.ID); active {
		t.Fatal("ResetRuntime() should detach the shadow workspace")
	}
	if _, err := os.Stat(workspace.ContentDir); !os.IsNotExist(err) {
		t.Fatalf("detached workspace content still exists: %v", err)
	}
	assertFileContent(t, productionPath, "production before sync")

	if err := os.WriteFile(productionPath, []byte("production after sync"), 0644); err != nil {
		t.Fatal(err)
	}
	latest, created, applied, err := workspaceManager.Update(runtime, "one.md", 2, []byte("production after sync"))
	if err != nil || !created || applied {
		t.Fatalf("post-reset Update() created=%v applied=%v err=%v", created, applied, err)
	}
	assertWorkspaceFileContent(t, filepath.Join(latest.ContentDir, "one.md"), "production after sync")
}

func TestLocalPreviewManagerStopDoesNotWaitForCleanup(t *testing.T) {
	lifecycle, err := NewLocalPreviewLifecycle(14100, 14100)
	if err != nil {
		t.Fatalf("NewLocalPreviewLifecycle() error = %v", err)
	}
	manager := NewLocalPreviewManager(lifecycle)
	const siteID = "tech"
	if _, err := lifecycle.Reserve(siteID, nil); err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if _, err := lifecycle.Transition(siteID, LocalPreviewStarting, nil); err != nil {
		t.Fatalf("Transition(starting) error = %v", err)
	}
	if _, err := lifecycle.Transition(siteID, LocalPreviewReady, nil); err != nil {
		t.Fatalf("Transition(ready) error = %v", err)
	}

	terminated := make(chan struct{})
	cleanupDone := make(chan struct{})
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	var cancelOnce sync.Once
	process := &managedLocalPreviewProcess{
		cmd:         &exec.Cmd{},
		done:        terminated,
		cleanupDone: cleanupDone,
		cleanup: func() error {
			close(cleanupStarted)
			<-releaseCleanup
			return nil
		},
	}
	process.cancel = func() {
		cancelOnce.Do(func() {
			close(terminated)
			go process.finishCleanup(siteID)
		})
	}
	manager.setProcess(siteID, process)

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := manager.Stop(ctx, siteID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, ok := manager.Status(siteID); ok {
		t.Fatal("Stop() should release the lifecycle slot before cleanup completes")
	}
	select {
	case <-cleanupStarted:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not start after process termination")
	}

	close(releaseCleanup)
	select {
	case <-cleanupDone:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not complete")
	}
	if err := process.cleanupError(); err != nil {
		t.Fatalf("cleanup error = %v", err)
	}
}

func TestEleventyLocalPreviewCleanupLeavesWorkspaceOwnedProject(t *testing.T) {
	projectDir := t.TempDir()
	outputDir := filepath.Join(projectDir, "public")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}
	outputFile := filepath.Join(outputDir, "generated.html")
	if err := os.WriteFile(outputFile, []byte("preview"), 0644); err != nil {
		t.Fatal(err)
	}

	runtime := config.SiteRuntime{
		Generator:              "eleventy",
		LocalPreviewProjectDir: projectDir,
	}
	cleanup := localPreviewProcessCleanup(runtime, projectDir)
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	if _, err := os.Stat(outputFile); err != nil {
		t.Fatalf("workspace-owned output was removed by process cleanup: %v", err)
	}
}

func TestEleventyLocalPreviewGenerationsDoNotShareCleanupPath(t *testing.T) {
	repo := t.TempDir()
	contentDir := filepath.Join(repo, "content")
	if err := os.MkdirAll(contentDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contentDir, "one.md"), []byte("one"), 0644); err != nil {
		t.Fatal(err)
	}
	runtime := config.SiteRuntime{
		ID:         "tech",
		RepoPath:   repo,
		Generator:  "eleventy",
		ContentDir: "content",
	}

	firstProject, _, err := prepareEleventyLocalPreviewProject(runtime)
	if err != nil {
		t.Fatalf("prepare first project: %v", err)
	}
	secondProject, _, err := prepareEleventyLocalPreviewProject(runtime)
	if err != nil {
		t.Fatalf("prepare second project: %v", err)
	}
	if firstProject == secondProject {
		t.Fatalf("preview generations share project path %q", firstProject)
	}

	firstCleanup := localPreviewProcessCleanup(runtime, firstProject)
	if err := firstCleanup(); err != nil {
		t.Fatalf("first cleanup: %v", err)
	}
	if _, err := os.Stat(secondProject); err != nil {
		t.Fatalf("old cleanup removed new preview project: %v", err)
	}
	secondCleanup := localPreviewProcessCleanup(runtime, secondProject)
	if err := secondCleanup(); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
}

func TestLocalPreviewManagerBeginShutdownRejectsNewStarts(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)
	manager.BeginShutdown()

	if _, err := manager.EnsureReady(site); !errors.Is(err, errLocalPreviewShuttingDown) {
		t.Fatalf("EnsureReady() error = %v, want shutting down", err)
	}
	if _, ok := manager.Status(site.ID); ok {
		t.Fatal("EnsureReady() should not reserve a slot after BeginShutdown")
	}
}

func TestLocalPreviewManagerShutdownRejectsRestart(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)
	if _, err := manager.EnsureReady(site); err != nil {
		t.Fatalf("EnsureReady() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if _, ok := manager.Status(site.ID); ok {
		t.Fatal("Shutdown() should release the lifecycle slot")
	}
	if _, err := manager.EnsureReady(site); !errors.Is(err, errLocalPreviewShuttingDown) {
		t.Fatalf("EnsureReady() after Shutdown error = %v, want shutting down", err)
	}
}
