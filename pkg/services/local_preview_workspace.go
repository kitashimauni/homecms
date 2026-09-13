package services

import (
	"bytes"
	"errors"
	"fmt"
	"hugo-cms/pkg/config"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const DefaultLocalPreviewIdleTimeout = 30 * time.Minute

// ErrLocalPreviewCleanupTransition indicates that a request waited behind a
// cleanup generation and must not recreate the workspace after cleanup.
var ErrLocalPreviewCleanupTransition = errors.New("local preview cleanup transition in progress")

// LocalPreviewWorkspace is the site-scoped unsaved-content workspace. It has
// no browser owner: every editor tab may update the same site workspace and
// the newest request wins.
type LocalPreviewWorkspace struct {
	SiteID         string
	ArticlePath    string
	ContentDir     string
	ProjectDir     string
	Revision       uint64
	LastActivityAt time.Time
}

// LocalPreviewIngressLease protects preview preparation from a concurrent
// cleanup transition. Callers release it before serving a long-lived stream.
type LocalPreviewIngressLease struct {
	gate *sync.RWMutex
	once sync.Once
}

func (lease *LocalPreviewIngressLease) Release() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		if lease.gate != nil {
			lease.gate.RUnlock()
		}
	})
}

// LocalPreviewCleanupLease holds the site's write gate while the generator is
// stopped and the shadow workspace is detached. It is a lifecycle lock, not a
// browser/session ownership claim.
type LocalPreviewCleanupLease struct {
	manager    *LocalPreviewWorkspaceManager
	siteID     string
	gate       *sync.RWMutex
	generation uint64
	once       sync.Once
}

func (lease *LocalPreviewCleanupLease) Release() {
	if lease == nil {
		return
	}
	lease.once.Do(func() {
		if lease.gate != nil {
			lease.gate.Unlock()
		}
		if lease.manager != nil {
			lease.manager.mu.Lock()
			if lease.manager.generations[lease.siteID] == lease.generation {
				delete(lease.manager.cleanupActive, lease.siteID)
			}
			lease.manager.mu.Unlock()
		}
	})
}

// LocalPreviewWorkspaceManager owns ephemeral shadow content directories and,
// for Eleventy, a temporary project-root overlay. Activity is tracked per
// site runtime, independently of browser tabs and editor revision counters.
type LocalPreviewWorkspaceManager struct {
	root            string
	mu              sync.Mutex
	workspaces      map[string]LocalPreviewWorkspace
	activities      map[string]time.Time
	rebuildRequired map[string]bool
	siteGates       map[string]*sync.RWMutex
	generations     map[string]uint64
	cleanupActive   map[string]bool
	closed          bool
	now             func() time.Time
	removeWorkspace func(string) error
}

func NewLocalPreviewWorkspaceManager(root string) (*LocalPreviewWorkspaceManager, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("local preview workspace root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve local preview workspace root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0700); err != nil {
		return nil, fmt.Errorf("create local preview workspace root: %w", err)
	}
	return &LocalPreviewWorkspaceManager{
		root:            absRoot,
		workspaces:      make(map[string]LocalPreviewWorkspace),
		activities:      make(map[string]time.Time),
		rebuildRequired: make(map[string]bool),
		siteGates:       make(map[string]*sync.RWMutex),
		generations:     make(map[string]uint64),
		cleanupActive:   make(map[string]bool),
		now:             time.Now,
		removeWorkspace: os.RemoveAll,
	}, nil
}

var (
	defaultLocalPreviewWorkspaceOnce sync.Once
	defaultLocalPreviewWorkspace     *LocalPreviewWorkspaceManager
	defaultLocalPreviewWorkspaceErr  error
)

func DefaultLocalPreviewWorkspaceManager() (*LocalPreviewWorkspaceManager, error) {
	defaultLocalPreviewWorkspaceOnce.Do(func() {
		root, err := os.MkdirTemp("", "homecms-local-preview-*")
		if err != nil {
			defaultLocalPreviewWorkspaceErr = fmt.Errorf("create local preview temporary root: %w", err)
			return
		}
		defaultLocalPreviewWorkspace, defaultLocalPreviewWorkspaceErr = NewLocalPreviewWorkspaceManager(root)
		if defaultLocalPreviewWorkspaceErr != nil {
			_ = os.RemoveAll(root)
		}
	})
	return defaultLocalPreviewWorkspace, defaultLocalPreviewWorkspaceErr
}

func (m *LocalPreviewWorkspaceManager) currentTimeLocked() time.Time {
	if m.now == nil {
		return time.Now().UTC()
	}
	return m.now().UTC()
}

func (m *LocalPreviewWorkspaceManager) siteGate(siteID string) *sync.RWMutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	gate := m.siteGates[siteID]
	if gate == nil {
		gate = &sync.RWMutex{}
		m.siteGates[siteID] = gate
	}
	return gate
}

func (m *LocalPreviewWorkspaceManager) acquireReadGate(siteID string) (*sync.RWMutex, uint64, bool) {
	gate := m.siteGate(siteID)
	m.mu.Lock()
	generation := m.generations[siteID]
	transitioning := m.cleanupActive[siteID]
	m.mu.Unlock()
	gate.RLock()
	return gate, generation, transitioning
}

// AcquireIngress returns one consistent workspace snapshot while holding the
// site's read gate. Cleanup takes the corresponding write gate, so an ingress
// request cannot capture a workspace while it is being detached.
func (m *LocalPreviewWorkspaceManager) AcquireIngress(siteID string) (LocalPreviewIngressLease, LocalPreviewWorkspace, bool, bool) {
	gate, generation, transitioning := m.acquireReadGate(siteID)

	m.mu.Lock()
	if m.closed || transitioning || m.generations[siteID] != generation {
		m.mu.Unlock()
		gate.RUnlock()
		return LocalPreviewIngressLease{}, LocalPreviewWorkspace{}, false, true
	}
	now := m.currentTimeLocked()
	m.activities[siteID] = now
	workspace, active := m.workspaces[siteID]
	if active {
		workspace.LastActivityAt = now
		m.workspaces[siteID] = workspace
	}
	m.mu.Unlock()
	return LocalPreviewIngressLease{gate: gate}, workspace, active, false
}

// AcquireNavigation protects the active workspace and generator URL
// resolution from a concurrent cleanup transition. The lease must be held
// until URL resolution has completed.
func (m *LocalPreviewWorkspaceManager) AcquireNavigation(siteID string) (LocalPreviewIngressLease, LocalPreviewWorkspace, bool, bool) {
	return m.AcquireIngress(siteID)
}

// Touch records activity for a site runtime even when it has not created a
// shadow workspace yet, such as preview access using saved content.
func (m *LocalPreviewWorkspaceManager) Touch(siteID string) {
	if strings.TrimSpace(siteID) == "" {
		return
	}
	gate, generation, transitioning := m.acquireReadGate(siteID)
	defer gate.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed && !transitioning && m.generations[siteID] == generation {
		m.activities[siteID] = m.currentTimeLocked()
	}
}

// Update applies a site-scoped shadow update. The client revision is accepted
// only as request metadata; ordering is assigned by this server-side critical
// section so independent browser tabs cannot permanently reject each other's
// revisions. Every accepted request receives the next server revision.
func (m *LocalPreviewWorkspaceManager) Update(runtime config.SiteRuntime, articlePath string, clientRevision uint64, content []byte) (LocalPreviewWorkspace, bool, bool, error) {
	return m.update(runtime, articlePath, clientRevision, content, nil)
}

// UpdateWithBeforeWrite applies an update after running beforeWrite once the
// site workspace and article path have been validated.
func (m *LocalPreviewWorkspaceManager) UpdateWithBeforeWrite(runtime config.SiteRuntime, articlePath string, clientRevision uint64, content []byte, beforeWrite func() error) (LocalPreviewWorkspace, bool, bool, error) {
	return m.update(runtime, articlePath, clientRevision, content, beforeWrite)
}

func (m *LocalPreviewWorkspaceManager) update(runtime config.SiteRuntime, articlePath string, clientRevision uint64, content []byte, beforeWrite func() error) (LocalPreviewWorkspace, bool, bool, error) {
	if clientRevision == 0 {
		return LocalPreviewWorkspace{}, false, false, fmt.Errorf("local preview revision must be greater than zero")
	}
	articlePath = filepath.Clean(strings.TrimSpace(articlePath))
	if articlePath == "." || filepath.IsAbs(articlePath) {
		return LocalPreviewWorkspace{}, false, false, fmt.Errorf("invalid local preview article path")
	}
	if runtime.ID == "" {
		return LocalPreviewWorkspace{}, false, false, fmt.Errorf("local preview site ID is required")
	}

	sourceContentDir := SafeJoin(runtime.RepoPath, "", runtime.ContentDir)
	if sourceContentDir == "" {
		return LocalPreviewWorkspace{}, false, false, fmt.Errorf("invalid local preview content directory")
	}
	if SafeJoin(sourceContentDir, "", articlePath) == "" {
		return LocalPreviewWorkspace{}, false, false, fmt.Errorf("invalid local preview article path")
	}

	gate, generation, transitioning := m.acquireReadGate(runtime.ID)
	defer gate.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return LocalPreviewWorkspace{}, false, false, fmt.Errorf("local preview workspace manager is closed")
	}
	if transitioning || m.generations[runtime.ID] != generation {
		return LocalPreviewWorkspace{}, false, false, ErrLocalPreviewCleanupTransition
	}
	now := m.currentTimeLocked()
	workspace, exists := m.workspaces[runtime.ID]
	created := false
	if !exists {
		workspaceRoot := filepath.Join(m.root, runtime.ID)
		if err := os.RemoveAll(workspaceRoot); err != nil {
			return LocalPreviewWorkspace{}, false, false, fmt.Errorf("reset local preview workspace: %w", err)
		}

		contentDir := filepath.Join(workspaceRoot, "content")
		projectDir := ""
		if isEleventyLocalPreviewGenerator(runtime.Generator) {
			inputDir, err := eleventyLocalPreviewInputDir(runtime)
			if err != nil {
				return LocalPreviewWorkspace{}, false, false, err
			}
			publicDir, err := eleventyLocalPreviewPublicDir(runtime)
			if err != nil {
				return LocalPreviewWorkspace{}, false, false, err
			}
			contentDir = filepath.Join(workspaceRoot, inputDir)
			if err := createEleventyLocalPreviewProjectOverlay(runtime.RepoPath, workspaceRoot, inputDir, publicDir, sourceContentDir); err != nil {
				_ = os.RemoveAll(workspaceRoot)
				return LocalPreviewWorkspace{}, false, false, err
			}
			projectDir = workspaceRoot
		} else if err := copyLocalPreviewContentTree(sourceContentDir, contentDir); err != nil {
			_ = os.RemoveAll(workspaceRoot)
			return LocalPreviewWorkspace{}, false, false, err
		}
		workspace = LocalPreviewWorkspace{
			SiteID:      runtime.ID,
			ArticlePath: filepath.ToSlash(articlePath),
			ContentDir:  contentDir,
			ProjectDir:  projectDir,
		}
		created = true
	} else {
		workspace.ArticlePath = filepath.ToSlash(articlePath)
	}

	target := SafeJoin(workspace.ContentDir, "", articlePath)
	if target == "" {
		if created {
			_ = os.RemoveAll(filepath.Dir(workspace.ContentDir))
		}
		return LocalPreviewWorkspace{}, false, false, fmt.Errorf("invalid local preview workspace path")
	}
	if existing, err := os.ReadFile(target); err == nil && bytes.Equal(existing, content) {
		workspace.LastActivityAt = now
		m.workspaces[runtime.ID] = workspace
		m.activities[runtime.ID] = now
		return workspace, created, false, nil
	} else if err != nil && !os.IsNotExist(err) {
		if created {
			_ = os.RemoveAll(filepath.Dir(workspace.ContentDir))
		}
		return LocalPreviewWorkspace{}, false, false, fmt.Errorf("read local preview target: %w", err)
	}
	if beforeWrite != nil {
		if err := beforeWrite(); err != nil {
			if created {
				_ = os.RemoveAll(filepath.Dir(workspace.ContentDir))
			}
			return LocalPreviewWorkspace{}, false, false, err
		}
	}
	if err := writeLocalPreviewFileAtomic(target, content); err != nil {
		if created {
			_ = os.RemoveAll(filepath.Dir(workspace.ContentDir))
		}
		return LocalPreviewWorkspace{}, false, false, err
	}
	workspace.Revision++
	workspace.LastActivityAt = now
	m.workspaces[runtime.ID] = workspace
	m.activities[runtime.ID] = now
	return workspace, created, true, nil
}

// Status reports the current site workspace. There is no stale or ownership
// state; runtime idle cleanup is based solely on activity timestamps.
func (m *LocalPreviewWorkspaceManager) Status(siteID string) (LocalPreviewWorkspace, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	workspace, ok := m.workspaces[siteID]
	return workspace, ok
}

func (m *LocalPreviewWorkspaceManager) Active(siteID string) (LocalPreviewWorkspace, bool) {
	return m.Status(siteID)
}

// MarkRebuildRequired records that the shadow workspace may no longer
// represent production after an auxiliary resource sync failed. The next
// preview recovery must detach it and recreate it from the production tree.
func (m *LocalPreviewWorkspaceManager) MarkRebuildRequired(siteID string) {
	siteID = strings.TrimSpace(siteID)
	if m == nil || siteID == "" {
		return
	}
	m.mu.Lock()
	if !m.closed {
		m.rebuildRequired[siteID] = true
	}
	m.mu.Unlock()
}

// RebuildRequired reports whether the site's shadow workspace requires a full
// production-based rebuild before it is used again.
func (m *LocalPreviewWorkspaceManager) RebuildRequired(siteID string) bool {
	siteID = strings.TrimSpace(siteID)
	if m == nil || siteID == "" {
		return false
	}
	m.mu.Lock()
	required := m.rebuildRequired[siteID]
	m.mu.Unlock()
	return required
}

// ClearRebuildRequired is called only after a full reset has detached the
// possibly stale workspace successfully.
func (m *LocalPreviewWorkspaceManager) ClearRebuildRequired(siteID string) {
	siteID = strings.TrimSpace(siteID)
	if m == nil || siteID == "" {
		return
	}
	m.mu.Lock()
	delete(m.rebuildRequired, siteID)
	m.mu.Unlock()
}

// IdleSites returns site runtimes with no recent preview activity. It includes
// runtimes that only served saved content and therefore have no workspace yet.
func (m *LocalPreviewWorkspaceManager) IdleSites(timeout time.Duration) []string {
	if timeout <= 0 {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	now := m.currentTimeLocked()
	idle := make([]string, 0)
	for siteID, lastActivity := range m.activities {
		if !lastActivity.IsZero() && now.Sub(lastActivity) >= timeout {
			idle = append(idle, siteID)
		}
	}
	return idle
}

// BeginCleanup obtains exclusive access to a site's workspace and preview
// ingress. The caller must stop the generator before FinishCleanup, or call
// CancelCleanup when stopping fails.
func (m *LocalPreviewWorkspaceManager) BeginCleanup(siteID string) (LocalPreviewCleanupLease, bool, error) {
	return m.beginCleanup(siteID, 0)
}

// BeginIdleCleanup repeats the idle check while holding the site's write gate,
// so activity that races with IdleSites cannot be stopped immediately after it
// was recorded.
func (m *LocalPreviewWorkspaceManager) BeginIdleCleanup(siteID string, timeout time.Duration) (LocalPreviewCleanupLease, bool, error) {
	if timeout <= 0 {
		return LocalPreviewCleanupLease{}, false, nil
	}
	return m.beginCleanup(siteID, timeout)
}

func (m *LocalPreviewWorkspaceManager) beginCleanup(siteID string, idleTimeout time.Duration) (LocalPreviewCleanupLease, bool, error) {
	gate := m.siteGate(siteID)
	gate.Lock()
	m.mu.Lock()
	closed := m.closed
	if !closed && idleTimeout > 0 {
		lastActivity, active := m.activities[siteID]
		if !active || lastActivity.IsZero() || m.currentTimeLocked().Sub(lastActivity) < idleTimeout {
			m.mu.Unlock()
			gate.Unlock()
			return LocalPreviewCleanupLease{}, false, nil
		}
	}
	if closed {
		m.mu.Unlock()
		gate.Unlock()
		return LocalPreviewCleanupLease{}, false, fmt.Errorf("local preview workspace manager is closed")
	}
	m.generations[siteID]++
	m.cleanupActive[siteID] = true
	generation := m.generations[siteID]
	m.mu.Unlock()
	return LocalPreviewCleanupLease{manager: m, siteID: siteID, gate: gate, generation: generation}, true, nil
}

func (m *LocalPreviewWorkspaceManager) FinishCleanup(lease *LocalPreviewCleanupLease) (bool, error) {
	if lease == nil || lease.manager != m || lease.gate == nil || lease.siteID == "" {
		return false, fmt.Errorf("invalid local preview cleanup lease")
	}
	m.mu.Lock()
	workspace, ok := m.workspaces[lease.siteID]
	if !ok {
		delete(m.activities, lease.siteID)
		m.mu.Unlock()
		lease.Release()
		return false, nil
	}
	cleanupPath, err := m.detachWorkspaceLocked(lease.siteID, workspace)
	if err != nil {
		m.mu.Unlock()
		lease.Release()
		return false, err
	}
	delete(m.activities, lease.siteID)
	m.mu.Unlock()
	lease.Release()
	m.cleanupWorkspaceAsync(lease.siteID, cleanupPath)
	return true, nil
}

func (m *LocalPreviewWorkspaceManager) CancelCleanup(lease *LocalPreviewCleanupLease) {
	if lease != nil && lease.manager == m {
		lease.Release()
	}
}

// SyncContentResource mirrors a content-directory resource change made through
// the normal CMS media API into an active shadow workspace.
func (m *LocalPreviewWorkspaceManager) SyncContentResource(runtime config.SiteRuntime, repoPath string, deleted bool) (bool, error) {
	if runtime.ID == "" {
		return false, fmt.Errorf("local preview site ID is required")
	}
	sourceContentDir := SafeJoin(runtime.RepoPath, "", runtime.ContentDir)
	if sourceContentDir == "" {
		return false, fmt.Errorf("invalid local preview content directory")
	}
	sourcePath := SafeJoin(runtime.RepoPath, "", filepath.Clean(strings.TrimSpace(repoPath)))
	if sourcePath == "" {
		return false, fmt.Errorf("invalid local preview resource path")
	}
	relative, err := filepath.Rel(sourceContentDir, sourcePath)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return false, nil
	}

	gate, generation, transitioning := m.acquireReadGate(runtime.ID)
	defer gate.RUnlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false, fmt.Errorf("local preview workspace manager is closed")
	}
	if transitioning || m.generations[runtime.ID] != generation {
		return false, ErrLocalPreviewCleanupTransition
	}
	workspace, active := m.workspaces[runtime.ID]
	if !active {
		return false, nil
	}
	now := m.currentTimeLocked()
	m.activities[runtime.ID] = now
	workspace.LastActivityAt = now
	m.workspaces[runtime.ID] = workspace
	target := SafeJoin(workspace.ContentDir, "", relative)
	if target == "" {
		return false, fmt.Errorf("invalid local preview resource target")
	}
	if deleted {
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return false, fmt.Errorf("remove local preview content resource: %w", err)
		}
		return true, nil
	}

	info, err := os.Lstat(sourcePath)
	if err != nil {
		return false, fmt.Errorf("stat local preview content resource: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("local preview content resource must be a regular file")
	}
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return false, fmt.Errorf("read local preview content resource: %w", err)
	}
	if err := writeLocalPreviewFileAtomic(target, content); err != nil {
		return false, err
	}
	return true, nil
}

func (m *LocalPreviewWorkspaceManager) detachWorkspaceLocked(siteID string, workspace LocalPreviewWorkspace) (string, error) {
	workspaceRoot := filepath.Dir(workspace.ContentDir)
	cleanupParent := filepath.Join(m.root, ".cleanup")
	if err := os.MkdirAll(cleanupParent, 0700); err != nil {
		return "", fmt.Errorf("create local preview cleanup directory: %w", err)
	}
	cleanupPath, err := os.MkdirTemp(cleanupParent, "workspace-")
	if err != nil {
		return "", fmt.Errorf("allocate local preview cleanup path: %w", err)
	}
	if err := os.Remove(cleanupPath); err != nil {
		return "", fmt.Errorf("prepare local preview cleanup path: %w", err)
	}
	if err := os.Rename(workspaceRoot, cleanupPath); err != nil {
		if os.IsNotExist(err) {
			delete(m.workspaces, siteID)
			return "", nil
		}
		return "", fmt.Errorf("detach local preview workspace: %w", err)
	}
	delete(m.workspaces, siteID)
	return cleanupPath, nil
}

func (m *LocalPreviewWorkspaceManager) cleanupWorkspaceAsync(siteID, cleanupPath string) {
	if cleanupPath == "" {
		return
	}
	removeWorkspace := m.removeWorkspace
	if removeWorkspace == nil {
		removeWorkspace = os.RemoveAll
	}
	go func() {
		if err := removeWorkspace(cleanupPath); err != nil {
			slog.Error("Local preview workspace cleanup failed", "site", siteID, "path", cleanupPath, "error", err)
		}
	}()
}

func (m *LocalPreviewWorkspaceManager) Shutdown() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	m.workspaces = make(map[string]LocalPreviewWorkspace)
	m.activities = make(map[string]time.Time)
	m.rebuildRequired = make(map[string]bool)
	root := m.root
	m.mu.Unlock()
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove local preview workspace root: %w", err)
	}
	return nil
}

func copyLocalPreviewContentTree(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("stat local preview content directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("local preview content directory must be a real directory")
	}

	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("local preview content symlink is not supported: %s", rel)
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("unsupported local preview content file type: %s", rel)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		return copyLocalPreviewFile(path, target, entryInfo.Mode().Perm())
	})
}

func copyLocalPreviewFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}

func writeLocalPreviewFileAtomic(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create local preview article directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".local-preview-*")
	if err != nil {
		return fmt.Errorf("create local preview temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write local preview content: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		if _, statErr := os.Stat(path); statErr != nil {
			return fmt.Errorf("replace local preview content: %w", err)
		}
		if removeErr := os.Remove(path); removeErr != nil {
			return fmt.Errorf("remove previous local preview content: %w", removeErr)
		}
		if err := os.Rename(temporaryPath, path); err != nil {
			return fmt.Errorf("replace local preview content: %w", err)
		}
	}
	return nil
}
