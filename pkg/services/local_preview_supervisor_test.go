package services

import (
	"context"
	"errors"
	"hugo-cms/pkg/config"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestNextLocalPreviewRefreshUsesConfiguredTimezoneAndRollsOverDaily(t *testing.T) {
	now := time.Date(2026, 9, 11, 18, 30, 0, 0, time.UTC)
	got, err := NextLocalPreviewRefresh(now, config.LocalPreviewRefreshConfig{
		Times:    []string{"04:00", "18:30"},
		Timezone: "Asia/Tokyo",
	})
	if err != nil {
		t.Fatalf("NextLocalPreviewRefresh() error = %v", err)
	}
	want := time.Date(2026, 9, 12, 4, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	if !got.Equal(want) {
		t.Fatalf("next refresh = %s, want %s", got, want)
	}

	got, err = NextLocalPreviewRefresh(time.Date(2026, 9, 12, 12, 0, 0, 0, time.FixedZone("JST", 9*60*60)), config.LocalPreviewRefreshConfig{
		Times:    []string{"04:00"},
		Timezone: "Asia/Tokyo",
	})
	if err != nil {
		t.Fatalf("NextLocalPreviewRefresh() rollover error = %v", err)
	}
	if got.Day() != 13 || got.Hour() != 4 {
		t.Fatalf("rollover refresh = %s, want next day at 04:00", got)
	}
}

func TestNextLocalPreviewRefreshDefaultsToUTC(t *testing.T) {
	got, err := NextLocalPreviewRefresh(time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC), config.LocalPreviewRefreshConfig{Times: []string{"04:00"}})
	if err != nil {
		t.Fatalf("NextLocalPreviewRefresh() error = %v", err)
	}
	if got.Location().String() != "UTC" || got.Hour() != 4 {
		t.Fatalf("next refresh = %s, want UTC 04:00", got)
	}
}

func TestNextPreviewBackoffIsBounded(t *testing.T) {
	backoff := persistentPreviewInitialBackoff
	for i := 0; i < 20; i++ {
		backoff = nextPreviewBackoff(backoff)
	}
	if backoff != persistentPreviewMaxBackoff {
		t.Fatalf("backoff = %s, want max %s", backoff, persistentPreviewMaxBackoff)
	}
	if nextPreviewBackoff(persistentPreviewMaxBackoff) != persistentPreviewMaxBackoff {
		t.Fatal("backoff exceeded configured maximum")
	}
}

func TestPersistentStatusExposesPolicyBeforeSupervisorStarts(t *testing.T) {
	manager := NewLocalPreviewManager(nil)
	enabled := true
	status := manager.PersistentStatus("docs", config.LocalPreviewConfig{
		Enabled:  &enabled,
		AlwaysOn: true,
		Refresh:  config.LocalPreviewRefreshConfig{Times: []string{"04:00"}, Timezone: "Asia/Tokyo"},
	})
	if !status.AlwaysOn || status.SupervisorState != LocalPreviewSupervisorStateStopped || status.RefreshTimezone != "Asia/Tokyo" {
		t.Fatalf("persistent status = %#v", status)
	}
}

func TestPersistentSupervisorPrewarmsAndRestartsUnexpectedExit(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)
	defer shutdownTestLocalPreviewManager(t, manager)
	site.Preview.LocalPreview.AlwaysOn = true
	var starts atomic.Int32
	manager.commandFactory = func(ctx context.Context, runtime config.SiteRuntime, port int, previewURL string) (*exec.Cmd, error) {
		cmd, err := testLocalPreviewCommand(ctx, runtime, port, previewURL)
		if err != nil {
			return nil, err
		}
		if starts.Add(1) == 1 {
			cmd.Env = append(cmd.Env, "HUGO_CMS_LOCAL_PREVIEW_EXIT_AFTER=100ms")
		}
		return cmd, nil
	}

	manager.StartPersistentPreviewSupervisor([]config.SiteConfig{site}, nil)
	waitForSupervisor(t, func() bool { return starts.Load() >= 1 && manager.process(site.ID) != nil })
	waitForSupervisor(t, func() bool {
		slot, ok := manager.Status(site.ID)
		return starts.Load() >= 2 && ok && slot.State == LocalPreviewReady
	})
	if slot, ok := manager.Status(site.ID); !ok || slot.State != LocalPreviewReady {
		t.Fatalf("supervisor status = %#v, exists=%v; want ready after restart", slot, ok)
	}
}

func TestPersistentSupervisorRetriesStoppingRuntimeAfterFailedStop(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)
	defer shutdownTestLocalPreviewManager(t, manager)
	site.Preview.LocalPreview.AlwaysOn = true

	var starts atomic.Int32
	manager.commandFactory = func(ctx context.Context, runtime config.SiteRuntime, port int, previewURL string) (*exec.Cmd, error) {
		starts.Add(1)
		return testLocalPreviewCommand(ctx, runtime, port, previewURL)
	}
	if _, err := manager.EnsureReady(site); err != nil {
		t.Fatalf("initial EnsureReady() error = %v", err)
	}
	if _, err := manager.lifecycle.Transition(site.ID, LocalPreviewStopping, nil); err != nil {
		t.Fatalf("transition to stopping: %v", err)
	}

	var stopAttempts atomic.Int32
	manager.processTerminator = func(ctx context.Context, siteID string, process *managedLocalPreviewProcess) error {
		if stopAttempts.Add(1) == 1 {
			return errors.New("injected process-tree termination failure")
		}
		return terminateManagedLocalPreviewProcess(ctx, siteID, process)
	}

	manager.StartPersistentPreviewSupervisor([]config.SiteConfig{site}, nil)
	waitForSupervisor(t, func() bool {
		slot, ok := manager.Status(site.ID)
		return starts.Load() >= 2 && stopAttempts.Load() >= 2 && ok && slot.State == LocalPreviewReady
	})
	if status := manager.PersistentStatus(site.ID, site.Preview.LocalPreview); status.SupervisorState != LocalPreviewSupervisorStateRunning {
		t.Fatalf("supervisor status = %#v, want running after stop retry", status)
	}
}

func TestScheduledRefreshKeepsActiveWorkspaceAttached(t *testing.T) {
	manager, site := newTestLocalPreviewManager(t)
	defer shutdownTestLocalPreviewManager(t, manager)
	fixture := newLocalPreviewWorkspaceFixture(t)
	workspaceManager := fixture.Manager
	site.RepoPath = fixture.Repo
	runtime := config.NewSiteRuntime(site)
	if _, _, _, err := workspaceManager.Update(runtime, "one.md", 1, []byte("draft")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnsureReady(site); err != nil {
		t.Fatal(err)
	}
	if err := manager.restartPersistentPreview(context.Background(), site, workspaceManager); err != nil {
		t.Fatalf("restartPersistentPreview() error = %v", err)
	}
	if _, active := workspaceManager.Status(site.ID); !active {
		t.Fatal("scheduled refresh detached the active shadow workspace")
	}
	if slot, ok := manager.Status(site.ID); !ok || slot.State != LocalPreviewReady {
		t.Fatalf("process status = %#v, exists=%v; want ready", slot, ok)
	}
}

func TestStopIdleSkipsAlwaysOnSites(t *testing.T) {
	manager := NewLocalPreviewManager(nil)
	manager.idleTimeout = time.Nanosecond
	workspaceManager, err := NewLocalPreviewWorkspaceManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceManager.Shutdown() })

	enabled := true
	site := config.SiteConfig{ID: "always-on", RepoPath: t.TempDir(), ContentDir: "content", Preview: config.SitePreviewConfig{LocalPreview: config.LocalPreviewConfig{Enabled: &enabled, AlwaysOn: true}}}
	if err := os.MkdirAll(filepath.Join(site.RepoPath, site.ContentDir), 0755); err != nil {
		t.Fatal(err)
	}
	installLocalPreviewSiteRegistry(t, site)
	runtime := config.NewSiteRuntime(site)
	if _, _, _, err := workspaceManager.Update(runtime, "one.md", 1, []byte("draft")); err != nil {
		t.Fatal(err)
	}
	workspaceManager.now = func() time.Time { return time.Now().UTC().Add(time.Hour) }
	if err := manager.StopIdle(context.Background(), workspaceManager); err != nil {
		t.Fatalf("StopIdle() error = %v", err)
	}
	if _, active := workspaceManager.Status(site.ID); !active {
		t.Fatal("always-on workspace was cleaned up by idle reaper")
	}
}

func waitForSupervisor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("persistent preview supervisor did not reach the expected state")
}
