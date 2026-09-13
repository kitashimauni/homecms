package services

// Shared fixtures for Local Preview lifecycle tests. The fixture deliberately
// stops at the process/workspace boundary; generator-specific behavior remains
// covered by the owning manager and resolver tests.

import (
	"context"
	"fmt"
	"hugo-cms/pkg/config"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestLocalPreviewManager(t *testing.T) (*LocalPreviewManager, config.SiteConfig) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	lifecycle, err := NewLocalPreviewLifecycle(port, port)
	if err != nil {
		t.Fatalf("NewLocalPreviewLifecycle() error = %v", err)
	}
	manager := NewLocalPreviewManager(lifecycle)
	manager.commandFactory = testLocalPreviewCommand
	manager.startAttempts = 1
	manager.startupTimeout = 5 * time.Second
	manager.probeInterval = 10 * time.Millisecond

	enabled := true
	site := config.SiteConfig{
		ID:         "tech",
		Generator:  "hugo",
		RepoPath:   ".",
		ContentDir: "content",
		Preview: config.SitePreviewConfig{
			LocalPreview: config.LocalPreviewConfig{
				Enabled: &enabled,
				URL:     "http://tech.preview.example.com/",
			},
		},
	}
	return manager, site
}

func shutdownTestLocalPreviewManager(t *testing.T, manager *LocalPreviewManager) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func testLocalPreviewCommand(ctx context.Context, runtime config.SiteRuntime, port int, _ string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLocalPreviewHelperProcess$")
	cmd.Env = append(os.Environ(),
		"HOMECMS_LOCAL_PREVIEW_HELPER=1",
		"HOMECMS_LOCAL_PREVIEW_PORT="+strconv.Itoa(port),
		"HOMECMS_LOCAL_PREVIEW_GENERATOR="+runtime.Generator,
	)
	return cmd, nil
}

func TestLocalPreviewHelperProcess(t *testing.T) {
	if os.Getenv("HOMECMS_LOCAL_PREVIEW_HELPER") != "1" {
		return
	}
	port, err := strconv.Atoi(os.Getenv("HOMECMS_LOCAL_PREVIEW_PORT"))
	if err != nil {
		os.Exit(2)
	}
	generator := strings.ToLower(strings.TrimSpace(os.Getenv("HOMECMS_LOCAL_PREVIEW_GENERATOR")))
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		os.Exit(3)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if generator == "eleventy" && r.URL.Path == eleventyLocalPreviewInvalidatePath {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if generator == "eleventy" && r.URL.Path == eleventyLocalPreviewReadyPath {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ready","building":false}`)
			return
		}
		if generator == "eleventy" && r.URL.Path == eleventyLocalPreviewMetadataPath {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"resolved","url":"/posts/one/"}`)
			return
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				os.Exit(4)
			}
			conn, rw, err := hijacker.Hijack()
			if err != nil {
				os.Exit(5)
			}
			_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
			_ = rw.Flush()
			_, _ = io.Copy(conn, conn)
			_ = conn.Close()
			return
		}
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "http://127.0.0.1:"+strconv.Itoa(port)+"/target")
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = fmt.Fprintf(w, "path=%s host=%s proto=%s forwarded-host=%s", r.URL.Path, r.Host, r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Forwarded-Host"))
	})

	server := &http.Server{Handler: handler}
	if value := os.Getenv("HUGO_CMS_LOCAL_PREVIEW_EXIT_AFTER"); value != "" {
		if delay, err := time.ParseDuration(value); err == nil && delay > 0 {
			time.AfterFunc(delay, func() { os.Exit(7) })
		}
	}
	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		os.Exit(6)
	}
	os.Exit(0)
}

type localPreviewWorkspaceFixture struct {
	Repo    string
	Runtime config.SiteRuntime
	Manager *LocalPreviewWorkspaceManager
}

func newLocalPreviewWorkspaceFixture(t *testing.T) localPreviewWorkspaceFixture {
	t.Helper()
	repo := makeLocalPreviewWorkspaceRepo(t)
	manager, err := NewLocalPreviewWorkspaceManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalPreviewWorkspaceManager() error = %v", err)
	}
	t.Cleanup(func() {
		if err := manager.Shutdown(); err != nil {
			t.Errorf("LocalPreviewWorkspaceManager.Shutdown() error = %v", err)
		}
	})
	return localPreviewWorkspaceFixture{
		Repo:    repo,
		Runtime: config.SiteRuntime{ID: "tech", RepoPath: repo, ContentDir: "content"},
		Manager: manager,
	}
}

func makeLocalPreviewWorkspaceRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "content"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "content", "one.md"), []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "content", "two.md"), []byte("two original"), 0644); err != nil {
		t.Fatal(err)
	}
	return repo
}

func installLocalPreviewSiteRegistry(t *testing.T, sites ...config.SiteConfig) {
	t.Helper()
	previousSites := append([]config.SiteConfig(nil), config.Sites...)
	config.Sites = append([]config.SiteConfig(nil), sites...)
	t.Cleanup(func() { config.Sites = previousSites })
}
