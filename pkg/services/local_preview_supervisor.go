package services

import (
	"context"
	"errors"
	"fmt"
	"hugo-cms/pkg/config"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	LocalPreviewSupervisorStateDisabled = "disabled"
	LocalPreviewSupervisorStateStarting = "starting"
	LocalPreviewSupervisorStateRunning  = "running"
	LocalPreviewSupervisorStateRetrying = "retrying"
	LocalPreviewSupervisorStateStopped  = "stopped"
	persistentPreviewPollInterval       = 500 * time.Millisecond
	persistentPreviewInitialBackoff     = 500 * time.Millisecond
	persistentPreviewMaxBackoff         = 30 * time.Second
)

// LocalPreviewSupervisorStatus is the process-local state exposed by the
// status API for an always-on site.
type LocalPreviewSupervisorStatus struct {
	State           string
	NextRefreshAt   time.Time
	RefreshTimezone string
}

// LocalPreviewPersistentStatus combines site policy with the current
// supervisor state. Zero NextRefreshAt means that no daily refresh is set.
type LocalPreviewPersistentStatus struct {
	AlwaysOn        bool
	NextRefreshAt   time.Time
	RefreshTimezone string
	SupervisorState string
}

// EffectiveLocalPreviewRefreshTimezone returns the configured IANA timezone,
// defaulting to UTC when a daily schedule exists without an explicit timezone.
func EffectiveLocalPreviewRefreshTimezone(refresh config.LocalPreviewRefreshConfig) string {
	timezone := strings.TrimSpace(refresh.Timezone)
	if timezone == "" && len(refresh.Times) > 0 {
		return "UTC"
	}
	return timezone
}

// NextLocalPreviewRefresh returns the next occurrence strictly after now.
// Daily schedules are intentionally limited to HH:MM values and never use a
// cron parser.
func NextLocalPreviewRefresh(now time.Time, refresh config.LocalPreviewRefreshConfig) (time.Time, error) {
	if len(refresh.Times) == 0 {
		return time.Time{}, nil
	}
	timezone := EffectiveLocalPreviewRefreshTimezone(refresh)
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("load local preview refresh timezone %q: %w", timezone, err)
	}
	if now.IsZero() {
		now = time.Now()
	}
	localNow := now.In(location)
	times := append([]string(nil), refresh.Times...)
	sort.Strings(times)
	for dayOffset := 0; dayOffset <= 1; dayOffset++ {
		date := localNow.AddDate(0, 0, dayOffset)
		for _, value := range times {
			if len(value) != len("15:04") || value[2] != ':' {
				return time.Time{}, fmt.Errorf("local preview refresh time %q must use HH:MM format", value)
			}
			parsed, err := time.ParseInLocation("15:04", value, location)
			if err != nil {
				return time.Time{}, fmt.Errorf("parse local preview refresh time %q: %w", value, err)
			}
			candidate := time.Date(date.Year(), date.Month(), date.Day(), parsed.Hour(), parsed.Minute(), 0, 0, location)
			if candidate.After(localNow) {
				return candidate, nil
			}
		}
	}
	return time.Time{}, errors.New("could not calculate next local preview refresh")
}

// StartPersistentPreviewSupervisor starts site supervisors asynchronously.
// Startup prewarm therefore never blocks HTTP server startup.
func (m *LocalPreviewManager) StartPersistentPreviewSupervisor(sites []config.SiteConfig, workspaceManager *LocalPreviewWorkspaceManager) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.shuttingDown || m.supervisorCancel != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m.supervisorCancel = cancel
	m.supervisorDone = done
	for _, site := range sites {
		if localPreviewAlwaysOn(site) {
			next, err := NextLocalPreviewRefresh(time.Now(), site.Preview.LocalPreview.Refresh)
			if err != nil {
				slog.Error("Invalid Local Live Preview refresh schedule", "site", site.ID, "error", err)
			}
			m.supervisorState[site.ID] = LocalPreviewSupervisorStatus{
				State:           LocalPreviewSupervisorStateStarting,
				NextRefreshAt:   next,
				RefreshTimezone: EffectiveLocalPreviewRefreshTimezone(site.Preview.LocalPreview.Refresh),
			}
		}
	}
	m.mu.Unlock()

	go func() {
		var waitGroup sync.WaitGroup
		for _, site := range sites {
			if !localPreviewAlwaysOn(site) {
				continue
			}
			site := site
			waitGroup.Add(1)
			go func() {
				defer waitGroup.Done()
				m.runPersistentPreviewSite(ctx, site, workspaceManager)
			}()
		}
		waitGroup.Wait()
		close(done)
	}()
}

func localPreviewAlwaysOn(site config.SiteConfig) bool {
	return site.Preview.LocalPreview.Enabled != nil &&
		*site.Preview.LocalPreview.Enabled &&
		site.Preview.LocalPreview.AlwaysOn
}

func (m *LocalPreviewManager) setSupervisorStatus(siteID string, status LocalPreviewSupervisorStatus) {
	m.mu.Lock()
	m.supervisorState[siteID] = status
	m.mu.Unlock()
}

// PersistentStatus is safe to call before the supervisor starts. It still
// returns the site policy and effective timezone so the UI can explain why a
// runtime is not being supervised.
func (m *LocalPreviewManager) PersistentStatus(siteID string, policy config.LocalPreviewConfig) LocalPreviewPersistentStatus {
	status := LocalPreviewPersistentStatus{
		AlwaysOn:        policy.AlwaysOn,
		RefreshTimezone: EffectiveLocalPreviewRefreshTimezone(policy.Refresh),
		SupervisorState: LocalPreviewSupervisorStateDisabled,
	}
	if policy.Enabled == nil || !*policy.Enabled || !policy.AlwaysOn {
		status.AlwaysOn = false
		return status
	}
	m.mu.Lock()
	if supervisor, ok := m.supervisorState[siteID]; ok {
		status.SupervisorState = supervisor.State
		status.NextRefreshAt = supervisor.NextRefreshAt
		if supervisor.RefreshTimezone != "" {
			status.RefreshTimezone = supervisor.RefreshTimezone
		}
	} else {
		status.SupervisorState = LocalPreviewSupervisorStateStopped
	}
	m.mu.Unlock()
	return status
}

func (m *LocalPreviewManager) runPersistentPreviewSite(ctx context.Context, site config.SiteConfig, workspaceManager *LocalPreviewWorkspaceManager) {
	refresh := site.Preview.LocalPreview.Refresh
	nextRefresh, err := NextLocalPreviewRefresh(time.Now(), refresh)
	if err != nil {
		m.setSupervisorStatus(site.ID, LocalPreviewSupervisorStatus{
			State:           LocalPreviewSupervisorStateRetrying,
			RefreshTimezone: EffectiveLocalPreviewRefreshTimezone(refresh),
		})
		slog.Error("Local Live Preview supervisor has invalid refresh schedule", "site", site.ID, "error", err)
	}
	backoff := persistentPreviewInitialBackoff
	nextAttempt := time.Now()

	for {
		if ctx.Err() != nil {
			m.setSupervisorStatus(site.ID, LocalPreviewSupervisorStatus{
				State:           LocalPreviewSupervisorStateStopped,
				RefreshTimezone: EffectiveLocalPreviewRefreshTimezone(refresh),
			})
			return
		}

		now := time.Now()
		if !m.isManuallyStopped(site.ID) && !nextRefresh.IsZero() && !now.Before(nextRefresh) {
			refreshSucceeded := true
			m.setSupervisorStatus(site.ID, LocalPreviewSupervisorStatus{
				State:           LocalPreviewSupervisorStateStarting,
				NextRefreshAt:   nextRefresh,
				RefreshTimezone: EffectiveLocalPreviewRefreshTimezone(refresh),
			})
			if err := m.restartPersistentPreview(ctx, site, workspaceManager); err != nil {
				if ctx.Err() != nil {
					return
				}
				m.setSupervisorStatus(site.ID, LocalPreviewSupervisorStatus{
					State:           LocalPreviewSupervisorStateRetrying,
					NextRefreshAt:   nextRefresh,
					RefreshTimezone: EffectiveLocalPreviewRefreshTimezone(refresh),
				})
				slog.Warn("Scheduled Local Live Preview refresh failed", "site", site.ID, "error", err)
				refreshSucceeded = false
				nextAttempt = now.Add(backoff)
				backoff = nextPreviewBackoff(backoff)
			} else {
				backoff = persistentPreviewInitialBackoff
				nextAttempt = now
			}
			nextRefresh, err = NextLocalPreviewRefresh(now.Add(time.Second), refresh)
			if err != nil {
				nextRefresh = time.Time{}
			}
			if refreshSucceeded {
				m.setSupervisorStatus(site.ID, LocalPreviewSupervisorStatus{
					State:           LocalPreviewSupervisorStateRunning,
					NextRefreshAt:   nextRefresh,
					RefreshTimezone: EffectiveLocalPreviewRefreshTimezone(refresh),
				})
			}
		}

		if !m.isManuallyStopped(site.ID) && !now.Before(nextAttempt) {
			slot, ready := m.Status(site.ID)
			process := m.process(site.ID)
			needsStart := !ready || slot.State == LocalPreviewFailed || slot.State == LocalPreviewStopping || process == nil || process.exited()
			if needsStart {
				m.setSupervisorStatus(site.ID, LocalPreviewSupervisorStatus{
					State:           LocalPreviewSupervisorStateStarting,
					NextRefreshAt:   nextRefresh,
					RefreshTimezone: EffectiveLocalPreviewRefreshTimezone(refresh),
				})
				var err error
				if slot.State == LocalPreviewStopping {
					// A failed process-tree stop intentionally leaves the runtime in
					// Stopping with its mapping intact. Retry that stop before
					// allocating a new port or starting a replacement process.
					err = m.restartPersistentPreview(ctx, site, workspaceManager)
				} else {
					err = m.ensurePersistentPreview(ctx, site, workspaceManager)
				}
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					m.setSupervisorStatus(site.ID, LocalPreviewSupervisorStatus{
						State:           LocalPreviewSupervisorStateRetrying,
						NextRefreshAt:   nextRefresh,
						RefreshTimezone: EffectiveLocalPreviewRefreshTimezone(refresh),
					})
					slog.Warn("Local Live Preview supervisor restart failed", "site", site.ID, "error", err)
					nextAttempt = now.Add(backoff)
					backoff = nextPreviewBackoff(backoff)
				} else {
					backoff = persistentPreviewInitialBackoff
					nextAttempt = now
					m.setSupervisorStatus(site.ID, LocalPreviewSupervisorStatus{
						State:           LocalPreviewSupervisorStateRunning,
						NextRefreshAt:   nextRefresh,
						RefreshTimezone: EffectiveLocalPreviewRefreshTimezone(refresh),
					})
				}
			}
		}

		select {
		case <-ctx.Done():
			continue
		case <-time.After(persistentPreviewPollInterval):
		}
	}
}

func nextPreviewBackoff(current time.Duration) time.Duration {
	if current >= persistentPreviewMaxBackoff {
		return persistentPreviewMaxBackoff
	}
	next := current * 2
	if next > persistentPreviewMaxBackoff {
		return persistentPreviewMaxBackoff
	}
	return next
}

func (m *LocalPreviewManager) persistentRuntime(site config.SiteConfig, workspaceManager *LocalPreviewWorkspaceManager) (config.SiteRuntime, *LocalPreviewIngressLease, error) {
	runtime := config.NewSiteRuntime(site)
	if workspaceManager == nil {
		return runtime, nil, nil
	}
	lease, workspace, active, transitioning := workspaceManager.AcquireIngress(site.ID)
	if transitioning {
		lease.Release()
		return config.SiteRuntime{}, nil, ErrLocalPreviewCleanupTransition
	}
	if active {
		runtime.ContentDir = workspace.ContentDir
		if workspace.ProjectDir != "" {
			runtime.LocalPreviewSourceRepoPath = runtime.RepoPath
			runtime.RepoPath = workspace.ProjectDir
			runtime.LocalPreviewProjectDir = workspace.ProjectDir
		}
	}
	return runtime, &lease, nil
}

func (m *LocalPreviewManager) ensurePersistentPreview(ctx context.Context, site config.SiteConfig, workspaceManager *LocalPreviewWorkspaceManager) error {
	runtime := config.NewSiteRuntime(site)
	if err := m.recoverRequiredWorkspace(ctx, runtime, workspaceManager); err != nil {
		return err
	}
	runtime, lease, err := m.persistentRuntime(site, workspaceManager)
	if err != nil {
		return err
	}
	if lease != nil {
		defer lease.Release()
	}
	_, err = m.ensureReadyRuntimeContext(ctx, runtime)
	return err
}

func (m *LocalPreviewManager) restartPersistentPreview(ctx context.Context, site config.SiteConfig, workspaceManager *LocalPreviewWorkspaceManager) error {
	runtime := config.NewSiteRuntime(site)
	if workspaceManager != nil && workspaceManager.RebuildRequired(site.ID) {
		if err := m.recoverRequiredWorkspace(ctx, runtime, workspaceManager); err != nil {
			return err
		}
		m.setManualStop(runtime.ID, false)
		if _, err := m.ensureReadyRuntimeContext(ctx, runtime); err != nil {
			return fmt.Errorf("restart local preview process for rebuilt site %q: %w", runtime.ID, err)
		}
		return nil
	}
	runtime, lease, err := m.persistentRuntime(site, workspaceManager)
	if err != nil {
		return err
	}
	if lease != nil {
		defer lease.Release()
	}
	return m.RestartRuntime(ctx, runtime)
}
