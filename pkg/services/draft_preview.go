package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hugo-cms/pkg/config"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	draftIDPattern             = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)
	commitSHAPattern           = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	draftPreviewOperationLocks = struct {
		sync.Mutex
		items map[string]*draftPreviewOperationLock
	}{items: make(map[string]*draftPreviewOperationLock)}
)

type draftPreviewOperationLock struct {
	mutex sync.Mutex
	users int
}

type gitPushFunc func(dir, token string, args ...string) (string, error)

type draftPreviewRemoteHeadFunc func(context.Context, config.SiteRuntime, string, string) (string, error)
type draftPreviewPullRequestFunc func(context.Context, config.SiteRuntime, string, DraftPreviewState) (string, error)

const previewBranchPrefix = "cms-preview/"

var (
	ErrDraftPreviewArticleMismatch = errors.New("draft preview article does not match request")
	ErrDraftPreviewNotReady        = errors.New("draft preview is not ready")
	ErrDraftPreviewStale           = errors.New("draft preview content is stale")
	ErrDraftPreviewBranchMoved     = errors.New("draft preview branch no longer matches the reviewed commit")
)

type DraftPreviewState struct {
	SiteID          string                  `json:"site_id"`
	DraftID         string                  `json:"draft_id"`
	ArticlePath     string                  `json:"article_path"`
	Paths           []string                `json:"paths"`
	Branch          string                  `json:"branch"`
	CommitSHA       string                  `json:"commit_sha"`
	DeploymentID    string                  `json:"deployment_id,omitempty"`
	Status          PreviewDeploymentStatus `json:"status"`
	URL             string                  `json:"url,omitempty"`
	FailureReason   string                  `json:"failure_reason,omitempty"`
	AccessProtected bool                    `json:"access_protected"`
	CleanupPending  bool                    `json:"cleanup_pending,omitempty"`
	CreatedAt       time.Time               `json:"created_at"`
	UpdatedAt       time.Time               `json:"updated_at"`
}

type DraftPreviewStore struct {
	root string
	mu   sync.Mutex
}

func NewDraftPreviewStore(root string) (*DraftPreviewStore, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("draft preview state root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve draft preview state root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0700); err != nil {
		return nil, fmt.Errorf("create draft preview state root: %w", err)
	}
	return &DraftPreviewStore{root: absRoot}, nil
}

func (store *DraftPreviewStore) Save(state DraftPreviewState) error {
	if err := validateDraftPreviewState(state); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	directory, path := store.statePath(state.SiteID, state.DraftID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return fmt.Errorf("create draft preview state directory: %w", err)
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode draft preview state: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".state-*.json")
	if err != nil {
		return fmt.Errorf("create temporary draft preview state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect draft preview state: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return fmt.Errorf("write draft preview state: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync draft preview state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close draft preview state: %w", err)
	}
	// Windows cannot replace an existing destination with os.Rename. The
	// state remains recoverable because the old file is removed only after the
	// fully-written temporary file has been closed and synced.
	if err := os.Rename(temporaryPath, path); err != nil {
		if _, statErr := os.Stat(path); statErr != nil {
			return fmt.Errorf("replace draft preview state: %w", err)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove previous draft preview state: %w", err)
		}
		if err := os.Rename(temporaryPath, path); err != nil {
			return fmt.Errorf("replace draft preview state: %w", err)
		}
	}
	return nil
}

func (store *DraftPreviewStore) Get(siteID, draftID string) (DraftPreviewState, error) {
	if strings.TrimSpace(siteID) == "" {
		return DraftPreviewState{}, fmt.Errorf("site ID is required")
	}
	if err := validateDraftID(draftID); err != nil {
		return DraftPreviewState{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	_, path := store.statePath(siteID, draftID)
	content, err := os.ReadFile(path)
	if err != nil {
		return DraftPreviewState{}, err
	}
	var state DraftPreviewState
	if err := json.Unmarshal(content, &state); err != nil {
		return DraftPreviewState{}, fmt.Errorf("decode draft preview state: %w", err)
	}
	if state.SiteID != siteID || state.DraftID != draftID {
		return DraftPreviewState{}, fmt.Errorf("draft preview state identity mismatch")
	}
	if err := validateDraftPreviewState(state); err != nil {
		return DraftPreviewState{}, fmt.Errorf("invalid stored draft preview state: %w", err)
	}
	return state, nil
}

func (store *DraftPreviewStore) Delete(siteID, draftID string) error {
	if strings.TrimSpace(siteID) == "" {
		return fmt.Errorf("site ID is required")
	}
	if err := validateDraftID(draftID); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	_, path := store.statePath(siteID, draftID)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete draft preview state: %w", err)
	}
	return nil
}

// ListCleanupPending returns persisted cleanup tombstones so a caller can
// retry cleanup after a process restart.
func (store *DraftPreviewStore) ListCleanupPending() ([]DraftPreviewState, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	states := make([]DraftPreviewState, 0)
	err := filepath.WalkDir(store.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || !strings.HasSuffix(entry.Name(), ".json") {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var state DraftPreviewState
		if err := json.Unmarshal(content, &state); err != nil {
			return fmt.Errorf("decode cleanup-pending draft preview state %q: %w", path, err)
		}
		if err := validateDraftPreviewState(state); err != nil {
			return fmt.Errorf("invalid cleanup-pending draft preview state %q: %w", path, err)
		}
		if state.CleanupPending {
			states = append(states, state)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list cleanup-pending draft previews: %w", err)
	}
	return states, nil
}

func (store *DraftPreviewStore) statePath(siteID, draftID string) (string, string) {
	hash := sha256.Sum256([]byte(siteID))
	directory := filepath.Join(store.root, hex.EncodeToString(hash[:]))
	return directory, filepath.Join(directory, draftID+".json")
}

// CommitAndPushDraftPreview builds a commit from the current working tree by
// using a private temporary index. It never changes HEAD or checks out the
// preview branch in the CMS repository.
func CommitAndPushDraftPreview(ctx context.Context, runtime config.SiteRuntime, token, draftID string, paths []string) (string, string, error) {
	return commitAndPushDraftPreview(ctx, runtime, token, draftID, paths, func(_ string, token string, args ...string) (string, error) {
		return ExecuteGitWithTokenForRuntime(runtime, token, args...)
	})
}

// draftPreviewMatchesWorkingTree verifies that the files covered by a draft
// still produce the exact committed tree. This is the server-side counterpart
// of the UI's stale marker and prevents a client from creating a PR after
// editing content that has not been deployed.
func draftPreviewMatchesWorkingTree(ctx context.Context, runtime config.SiteRuntime, state DraftPreviewState) (bool, error) {
	unlock := LockRepositoryOperation(runtime)
	defer unlock()
	return draftPreviewMatchesWorkingTreeLocked(ctx, runtime, state)
}

// draftPreviewMatchesWorkingTreeLocked requires the repository operation lock.
func draftPreviewMatchesWorkingTreeLocked(ctx context.Context, runtime config.SiteRuntime, state DraftPreviewState) (bool, error) {
	if err := validateDraftPreviewState(state); err != nil {
		return false, err
	}
	if state.SiteID != runtime.ID {
		return false, fmt.Errorf("draft preview does not belong to the selected site")
	}
	validatedPaths, err := validateDraftPaths(runtime.RepoPath, state.Paths)
	if err != nil {
		return false, err
	}
	commandContext, cancel := context.WithTimeout(ctx, config.GitCommandTimeout)
	defer cancel()

	index, err := os.CreateTemp("", "homecms-verify-index-*")
	if err != nil {
		return false, fmt.Errorf("create verification Git index: %w", err)
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		os.Remove(indexPath)
		return false, fmt.Errorf("close verification Git index: %w", err)
	}
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("prepare verification Git index: %w", err)
	}
	defer os.Remove(indexPath)

	env := temporaryIndexEnvironment(indexPath)
	if _, err := runGitCommand(commandContext, runtime.RepoPath, env, "read-tree", state.CommitSHA); err != nil {
		return false, fmt.Errorf("initialize verification Git index: %w", err)
	}
	addArgs := append([]string{"add", "-A", "--"}, validatedPaths...)
	if _, err := runGitCommand(commandContext, runtime.RepoPath, env, addArgs...); err != nil {
		return false, fmt.Errorf("stage verification paths: %w", err)
	}
	workingTree, err := runGitCommand(commandContext, runtime.RepoPath, env, "write-tree")
	if err != nil {
		return false, fmt.Errorf("write verification tree: %w", err)
	}
	committedTree, err := runGitCommand(commandContext, runtime.RepoPath, nil, "rev-parse", state.CommitSHA+"^{tree}")
	if err != nil {
		return false, fmt.Errorf("read deployed draft tree: %w", err)
	}
	return strings.TrimSpace(workingTree) == strings.TrimSpace(committedTree), nil
}

func commitAndPushDraftPreview(ctx context.Context, runtime config.SiteRuntime, token, draftID string, paths []string, push gitPushFunc) (string, string, error) {
	commandContext, cancel := context.WithTimeout(ctx, config.GitCommandTimeout)
	defer cancel()
	ctx = commandContext
	if err := validateDraftID(draftID); err != nil {
		return "", "", err
	}
	validatedPaths, err := validateDraftPaths(runtime.RepoPath, paths)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(token) == "" {
		return "", "", fmt.Errorf("GitHub token is required")
	}
	branch := previewBranchPrefix + draftID
	if err := validatePreviewBranch(branch); err != nil {
		return "", "", err
	}

	unlock := LockRepositoryOperation(runtime)
	defer unlock()

	index, err := os.CreateTemp("", "homecms-draft-index-*")
	if err != nil {
		return "", "", fmt.Errorf("create temporary Git index: %w", err)
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		os.Remove(indexPath)
		return "", "", fmt.Errorf("close temporary Git index: %w", err)
	}
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", fmt.Errorf("prepare temporary Git index: %w", err)
	}
	defer os.Remove(indexPath)

	previousCommit, localBranchExists, err := localDraftCommit(ctx, runtime, branch)
	if err != nil {
		return "", "", err
	}
	baseCommit, err := productionBaseCommit(ctx, runtime)
	if err != nil {
		return "", "", err
	}
	env := temporaryIndexEnvironment(indexPath)
	if _, err := runGitCommand(ctx, runtime.RepoPath, env, "read-tree", baseCommit); err != nil {
		return "", "", fmt.Errorf("initialize draft Git index: %w", err)
	}
	addArgs := []string{"add", "-A", "--"}
	addArgs = append(addArgs, validatedPaths...)
	if output, err := runGitCommand(ctx, runtime.RepoPath, env, addArgs...); err != nil {
		return "", "", fmt.Errorf("stage draft paths: %w: %s", err, strings.TrimSpace(output))
	}
	treeSHA, err := runGitCommand(ctx, runtime.RepoPath, env, "write-tree")
	if err != nil {
		return "", "", fmt.Errorf("write draft tree: %w", err)
	}
	treeSHA = strings.TrimSpace(treeSHA)
	baseTree, err := runGitCommand(ctx, runtime.RepoPath, nil, "rev-parse", baseCommit+"^{tree}")
	if err != nil {
		return "", "", fmt.Errorf("read draft base tree: %w", err)
	}

	commitSHA := baseCommit
	if treeSHA != strings.TrimSpace(baseTree) {
		commitEnv := append(temporaryIndexEnvironment(indexPath),
			"GIT_AUTHOR_NAME="+runtime.GitUserName,
			"GIT_AUTHOR_EMAIL="+runtime.GitUserEmail,
			"GIT_COMMITTER_NAME="+runtime.GitUserName,
			"GIT_COMMITTER_EMAIL="+runtime.GitUserEmail,
		)
		commitSHA, err = runGitCommand(ctx, runtime.RepoPath, commitEnv,
			"commit-tree", treeSHA, "-p", baseCommit, "-m", "Update draft "+draftID+" via HomeCMS")
		if err != nil {
			return "", "", fmt.Errorf("create draft commit: %w", err)
		}
		commitSHA = strings.TrimSpace(commitSHA)
	}
	if err := validateCommitSHA(commitSHA); err != nil {
		return "", "", fmt.Errorf("Git returned an invalid draft commit: %w", err)
	}

	ref := "refs/heads/" + branch
	updateArgs := []string{"update-ref", ref, commitSHA}
	if localBranchExists {
		updateArgs = append(updateArgs, previousCommit)
	}
	if _, err := runGitCommand(ctx, runtime.RepoPath, nil, updateArgs...); err != nil {
		return "", "", fmt.Errorf("update local draft branch: %w", err)
	}
	lease := "--force-with-lease=" + ref + ":" + previousCommit
	pushLog, err := push(runtime.RepoPath, token, "push", lease, runtime.GitRemote, ref+":"+ref)
	if err != nil {
		rollbackArgs := []string{"update-ref"}
		if localBranchExists {
			rollbackArgs = append(rollbackArgs, ref, previousCommit, commitSHA)
		} else {
			rollbackArgs = append(rollbackArgs, "-d", ref, commitSHA)
		}
		if _, rollbackErr := runGitCommand(ctx, runtime.RepoPath, nil, rollbackArgs...); rollbackErr != nil {
			return "", "", fmt.Errorf("push draft branch: %w: %s; rollback local draft ref: %v", err, strings.TrimSpace(pushLog), rollbackErr)
		}
		return "", "", fmt.Errorf("push draft branch: %w: %s", err, strings.TrimSpace(pushLog))
	}
	return branch, commitSHA, nil
}

func UpdateDraftPreview(ctx context.Context, runtime config.SiteRuntime, token, draftID, articlePath string, paths []string, store *DraftPreviewStore, provider PreviewDeploymentProvider) (DraftPreviewState, error) {
	return updateDraftPreview(ctx, runtime, token, draftID, articlePath, paths, store, provider, func(_ string, token string, args ...string) (string, error) {
		return ExecuteGitWithTokenForRuntime(runtime, token, args...)
	})
}

func updateDraftPreview(ctx context.Context, runtime config.SiteRuntime, token, draftID, articlePath string, paths []string, store *DraftPreviewStore, provider PreviewDeploymentProvider, push gitPushFunc) (DraftPreviewState, error) {
	if store == nil || provider == nil {
		return DraftPreviewState{}, fmt.Errorf("draft preview store and provider are required")
	}
	if strings.TrimSpace(runtime.ID) == "" {
		return DraftPreviewState{}, fmt.Errorf("site ID is required")
	}
	if err := validateDraftID(draftID); err != nil {
		return DraftPreviewState{}, err
	}
	articlePath, err := normalizeDraftArticlePath(runtime, articlePath)
	if err != nil {
		return DraftPreviewState{}, err
	}
	validatedPaths, err := validateDraftPaths(runtime.RepoPath, paths)
	if err != nil {
		return DraftPreviewState{}, err
	}
	if validatedPaths[0] != draftArticleRepoPath(runtime, articlePath) {
		return DraftPreviewState{}, fmt.Errorf("draft paths do not match article path")
	}
	unlockOperation := lockDraftPreviewOperation(runtime.ID, draftID)
	defer unlockOperation()
	now := time.Now().UTC()
	createdAt := now
	if previous, getErr := store.Get(runtime.ID, draftID); getErr == nil {
		if previous.ArticlePath != articlePath {
			return DraftPreviewState{}, ErrDraftPreviewArticleMismatch
		}
		createdAt = previous.CreatedAt
	} else if !errors.Is(getErr, os.ErrNotExist) {
		return DraftPreviewState{}, getErr
	}
	branch, commitSHA, err := commitAndPushDraftPreview(ctx, runtime, token, draftID, validatedPaths, push)
	if err != nil {
		return DraftPreviewState{}, err
	}
	state := DraftPreviewState{
		SiteID:          runtime.ID,
		DraftID:         draftID,
		ArticlePath:     articlePath,
		Paths:           append([]string(nil), validatedPaths...),
		Branch:          branch,
		CommitSHA:       commitSHA,
		Status:          PreviewDeploymentQueued,
		AccessProtected: runtime.PreviewDeployment.AccessProtected,
		CreatedAt:       createdAt,
		UpdatedAt:       now,
	}
	if err := store.Save(state); err != nil {
		return DraftPreviewState{}, err
	}
	deployment, err := provider.Trigger(ctx, branch, commitSHA)
	if err != nil {
		if IsPreviewProviderError(err, PreviewProviderNotFound) {
			return state, nil
		}
		return state, err
	}
	return applyProviderDeployment(store, state, deployment)
}

func RefreshDraftPreview(ctx context.Context, siteID, draftID string, store *DraftPreviewStore, provider PreviewDeploymentProvider) (DraftPreviewState, error) {
	if store == nil || provider == nil {
		return DraftPreviewState{}, fmt.Errorf("draft preview store and provider are required")
	}
	if strings.TrimSpace(siteID) == "" {
		return DraftPreviewState{}, fmt.Errorf("site ID is required")
	}
	if err := validateDraftID(draftID); err != nil {
		return DraftPreviewState{}, err
	}
	unlockOperation := lockDraftPreviewOperation(siteID, draftID)
	defer unlockOperation()
	return refreshDraftPreview(ctx, siteID, draftID, store, provider)
}

func refreshDraftPreview(ctx context.Context, siteID, draftID string, store *DraftPreviewStore, provider PreviewDeploymentProvider) (DraftPreviewState, error) {
	state, err := store.Get(siteID, draftID)
	if err != nil {
		return DraftPreviewState{}, err
	}
	var deployment PreviewDeployment
	if state.DeploymentID == "" {
		deployment, err = provider.Trigger(ctx, state.Branch, state.CommitSHA)
	} else {
		deployment, err = provider.Status(ctx, state.DeploymentID)
	}
	if err != nil {
		if IsPreviewProviderError(err, PreviewProviderNotFound) && state.DeploymentID == "" {
			return state, nil
		}
		return state, err
	}
	return applyProviderDeployment(store, state, deployment)
}

func RetryDraftPreview(ctx context.Context, siteID, draftID string, store *DraftPreviewStore, provider PreviewDeploymentProvider) (DraftPreviewState, error) {
	if store == nil || provider == nil {
		return DraftPreviewState{}, fmt.Errorf("draft preview store and provider are required")
	}
	if strings.TrimSpace(siteID) == "" {
		return DraftPreviewState{}, fmt.Errorf("site ID is required")
	}
	if err := validateDraftID(draftID); err != nil {
		return DraftPreviewState{}, err
	}
	unlockOperation := lockDraftPreviewOperation(siteID, draftID)
	defer unlockOperation()
	state, err := store.Get(siteID, draftID)
	if err != nil {
		return DraftPreviewState{}, err
	}
	if state.DeploymentID == "" {
		return refreshDraftPreview(ctx, siteID, draftID, store, provider)
	}
	deployment, err := provider.Retry(ctx, state.DeploymentID)
	if err != nil {
		return state, err
	}
	return applyProviderDeployment(store, state, deployment)
}

// PublishDraftPreview verifies and publishes one reviewed draft as an atomic
// draft operation. Update, retry, and cleanup for the same draft cannot move
// its branch until the pull request lookup or creation has completed.
func PublishDraftPreview(ctx context.Context, runtime config.SiteRuntime, token, draftID, requestedArticlePath string, store *DraftPreviewStore, provider PreviewDeploymentProvider) (string, error) {
	return publishDraftPreview(ctx, runtime, token, draftID, requestedArticlePath, store, provider, remoteDraftBranchCommit, CreateDraftPreviewPullRequest)
}

func publishDraftPreview(ctx context.Context, runtime config.SiteRuntime, token, draftID, requestedArticlePath string, store *DraftPreviewStore, provider PreviewDeploymentProvider, remoteHead draftPreviewRemoteHeadFunc, createPullRequest draftPreviewPullRequestFunc) (string, error) {
	if store == nil || provider == nil || remoteHead == nil || createPullRequest == nil {
		return "", fmt.Errorf("draft preview publish dependencies are required")
	}
	if strings.TrimSpace(runtime.ID) == "" {
		return "", fmt.Errorf("site ID is required")
	}
	if err := validateDraftID(draftID); err != nil {
		return "", err
	}
	articlePath, err := normalizeDraftArticlePath(runtime, requestedArticlePath)
	if err != nil {
		return "", err
	}

	unlockOperation := lockDraftPreviewOperation(runtime.ID, draftID)
	defer unlockOperation()

	state, err := refreshDraftPreview(ctx, runtime.ID, draftID, store, provider)
	if err != nil {
		return "", err
	}
	if state.ArticlePath != articlePath {
		return "", ErrDraftPreviewArticleMismatch
	}
	if state.Status != PreviewDeploymentReady || state.URL == "" {
		return "", ErrDraftPreviewNotReady
	}

	unlocksRepository := LockRepositoryOperation(runtime)
	matches, matchErr := draftPreviewMatchesWorkingTreeLocked(ctx, runtime, state)
	unlocksRepository()
	if matchErr != nil {
		return "", matchErr
	}
	if !matches {
		return "", ErrDraftPreviewStale
	}

	remoteCommit, err := remoteHead(ctx, runtime, token, state.Branch)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(remoteCommit, state.CommitSHA) {
		return "", ErrDraftPreviewBranchMoved
	}

	pullRequestURL, err := createPullRequest(ctx, runtime, token, state)
	if err != nil {
		return "", err
	}
	remoteCommit, err = remoteHead(ctx, runtime, token, state.Branch)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(remoteCommit, state.CommitSHA) {
		return "", ErrDraftPreviewBranchMoved
	}
	return pullRequestURL, nil
}

func remoteDraftBranchCommit(_ context.Context, runtime config.SiteRuntime, token, branch string) (string, error) {
	if err := validatePreviewBranch(branch); err != nil {
		return "", err
	}
	ref := "refs/heads/" + branch
	output, err := ExecuteGitWithTokenForRuntime(runtime, token, "ls-remote", "--refs", runtime.GitRemote, ref)
	if err != nil {
		return "", fmt.Errorf("read remote draft branch: %w", err)
	}
	fields := strings.Fields(output)
	if len(fields) != 2 || fields[1] != ref {
		return "", ErrDraftPreviewBranchMoved
	}
	if err := validateCommitSHA(fields[0]); err != nil {
		return "", fmt.Errorf("remote draft branch returned invalid commit: %w", err)
	}
	return fields[0], nil
}

func CleanupDraftPreview(ctx context.Context, runtime config.SiteRuntime, token, draftID string, store *DraftPreviewStore, provider PreviewDeploymentProvider) error {
	return cleanupDraftPreview(ctx, runtime, token, draftID, store, provider, func(_ string, token string, args ...string) (string, error) {
		return ExecuteGitWithTokenForRuntime(runtime, token, args...)
	})
}

func cleanupDraftPreview(ctx context.Context, runtime config.SiteRuntime, token, draftID string, store *DraftPreviewStore, provider PreviewDeploymentProvider, push gitPushFunc) error {
	if store == nil || provider == nil {
		return fmt.Errorf("draft preview store and provider are required")
	}
	if strings.TrimSpace(runtime.ID) == "" {
		return fmt.Errorf("site ID is required")
	}
	if err := validateDraftID(draftID); err != nil {
		return err
	}
	unlockOperation := lockDraftPreviewOperation(runtime.ID, draftID)
	defer unlockOperation()
	state, err := store.Get(runtime.ID, draftID)
	if err != nil {
		return err
	}
	state.CleanupPending = true
	state.UpdatedAt = time.Now().UTC()
	if err := store.Save(state); err != nil {
		return err
	}

	unlock := LockRepositoryOperation(runtime)
	deleteLog, deleteBranchErr := push(runtime.RepoPath, token, "push", runtime.GitRemote, ":refs/heads/"+state.Branch)
	unlock()
	if deleteBranchErr != nil {
		return fmt.Errorf("delete remote draft branch: %w: %s", deleteBranchErr, strings.TrimSpace(deleteLog))
	}
	if state.DeploymentID != "" {
		if err := provider.Delete(ctx, state.DeploymentID); err != nil && !IsPreviewProviderError(err, PreviewProviderNotFound) {
			return err
		}
	}

	unlock = LockRepositoryOperation(runtime)
	_, localDeleteErr := runGitCommand(ctx, runtime.RepoPath, nil, "update-ref", "-d", "refs/heads/"+state.Branch, state.CommitSHA)
	unlock()
	if localDeleteErr != nil {
		return fmt.Errorf("delete local draft branch: %w", localDeleteErr)
	}
	return store.Delete(runtime.ID, draftID)
}

func applyProviderDeployment(store *DraftPreviewStore, state DraftPreviewState, deployment PreviewDeployment) (DraftPreviewState, error) {
	if deployment.Branch != state.Branch || !strings.EqualFold(deployment.CommitSHA, state.CommitSHA) {
		return state, &PreviewProviderError{Kind: PreviewProviderInvalidReply, Operation: "match", Err: fmt.Errorf("provider deployment does not match draft commit")}
	}
	state.DeploymentID = deployment.ID
	state.Status = deployment.Status
	state.URL = ""
	state.FailureReason = ""
	if deployment.Status == PreviewDeploymentReady {
		if err := validatePreviewURL(deployment.URL); err != nil {
			return state, &PreviewProviderError{Kind: PreviewProviderInvalidReply, Operation: "url", Err: err}
		}
		state.URL = deployment.URL
	} else if deployment.Status == PreviewDeploymentFailed {
		state.FailureReason = deployment.FailureReason
	}
	state.UpdatedAt = time.Now().UTC()
	if err := store.Save(state); err != nil {
		return state, err
	}
	return state, nil
}

func localDraftCommit(ctx context.Context, runtime config.SiteRuntime, branch string) (string, bool, error) {
	localRef := "refs/heads/" + branch
	output, err := runGitCommand(ctx, runtime.RepoPath, nil, "rev-parse", "--verify", "--quiet", localRef+"^{commit}")
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("resolve local draft branch %q: %w", branch, err)
	}
	commit := strings.TrimSpace(output)
	if err := validateCommitSHA(commit); err != nil {
		return "", false, err
	}
	return commit, true, nil
}

func productionBaseCommit(ctx context.Context, runtime config.SiteRuntime) (string, error) {
	baseRef := "refs/heads/" + runtime.GitBranch
	output, err := runGitCommand(ctx, runtime.RepoPath, nil, "rev-parse", "--verify", baseRef+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve production branch %q: %w", runtime.GitBranch, err)
	}
	commit := strings.TrimSpace(output)
	if err := validateCommitSHA(commit); err != nil {
		return "", err
	}
	return commit, nil
}

func runGitCommand(ctx context.Context, directory string, extraEnv []string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	if extraEnv != nil {
		command.Env = extraEnv
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

func temporaryIndexEnvironment(indexPath string) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(key, "GIT_INDEX_FILE") {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, "GIT_INDEX_FILE="+indexPath)
}

func validateDraftID(draftID string) error {
	if !draftIDPattern.MatchString(draftID) {
		return fmt.Errorf("invalid draft ID: must be 1-64 lowercase letters, digits, or internal hyphens")
	}
	return nil
}

func validatePreviewBranch(branch string) error {
	if !strings.HasPrefix(branch, previewBranchPrefix) {
		return fmt.Errorf("preview branch must start with %q", previewBranchPrefix)
	}
	return validateDraftID(strings.TrimPrefix(branch, previewBranchPrefix))
}

func validateCommitSHA(commitSHA string) error {
	if !commitSHAPattern.MatchString(commitSHA) {
		return fmt.Errorf("commit SHA must be a full 40-character hexadecimal SHA-1")
	}
	return nil
}

func validateDraftPaths(repoPath string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("at least one draft path is required")
	}
	result := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, value := range paths {
		if strings.ContainsRune(value, '\x00') || filepath.IsAbs(value) {
			return nil, fmt.Errorf("invalid draft path %q", value)
		}
		clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(value)))
		if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean == ".git" || strings.HasPrefix(clean, ".git/") {
			return nil, fmt.Errorf("invalid draft path %q", value)
		}
		if SafeJoin(repoPath, "", clean) == "" {
			return nil, fmt.Errorf("draft path escapes repository: %q", value)
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		result = append(result, clean)
	}
	return result, nil
}

func normalizeDraftArticlePath(runtime config.SiteRuntime, articlePath string) (string, error) {
	clean, err := normalizeStoredDraftArticlePath(articlePath)
	if err != nil {
		return "", err
	}
	if SafeJoin(runtime.RepoPath, runtime.ContentDir, clean) == "" {
		return "", fmt.Errorf("draft article path escapes content directory")
	}
	return clean, nil
}

func draftArticleRepoPath(runtime config.SiteRuntime, articlePath string) string {
	repoArticle := filepath.ToSlash(filepath.Join(runtime.ContentDir, filepath.FromSlash(articlePath)))
	base := strings.ToLower(filepath.Base(articlePath))
	if base == "index.md" || base == "_index.md" {
		return filepath.ToSlash(filepath.Dir(filepath.FromSlash(repoArticle)))
	}
	return repoArticle
}

func normalizeStoredDraftArticlePath(articlePath string) (string, error) {
	articlePath = strings.TrimSpace(articlePath)
	if articlePath == "" || strings.ContainsRune(articlePath, '\x00') || filepath.IsAbs(articlePath) {
		return "", fmt.Errorf("invalid draft article path")
	}
	clean := filepath.ToSlash(filepath.Clean(articlePath))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || !strings.EqualFold(filepath.Ext(clean), ".md") {
		return "", fmt.Errorf("invalid draft article path")
	}
	return clean, nil
}

func validateStoredDraftPaths(paths []string) error {
	if len(paths) == 0 {
		return fmt.Errorf("draft preview paths are required")
	}
	seen := make(map[string]struct{}, len(paths))
	for _, value := range paths {
		if strings.ContainsRune(value, '\x00') || filepath.IsAbs(value) {
			return fmt.Errorf("invalid stored draft path %q", value)
		}
		clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(value)))
		if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean == ".git" || strings.HasPrefix(clean, ".git/") || clean != value {
			return fmt.Errorf("invalid stored draft path %q", value)
		}
		if _, exists := seen[clean]; exists {
			return fmt.Errorf("duplicate stored draft path %q", value)
		}
		seen[clean] = struct{}{}
	}
	return nil
}

func validateDraftPreviewState(state DraftPreviewState) error {
	if strings.TrimSpace(state.SiteID) == "" {
		return fmt.Errorf("site ID is required")
	}
	if err := validateDraftID(state.DraftID); err != nil {
		return err
	}
	articlePath, err := normalizeStoredDraftArticlePath(state.ArticlePath)
	if err != nil || articlePath != state.ArticlePath {
		return fmt.Errorf("invalid draft preview article path")
	}
	if err := validateStoredDraftPaths(state.Paths); err != nil {
		return err
	}
	if state.Branch != previewBranchPrefix+state.DraftID {
		return fmt.Errorf("draft branch does not match draft ID")
	}
	if err := validateCommitSHA(state.CommitSHA); err != nil {
		return err
	}
	if state.DeploymentID != "" {
		if err := validateDeploymentID(state.DeploymentID); err != nil {
			return err
		}
	}
	if state.URL != "" {
		if state.Status != PreviewDeploymentReady {
			return fmt.Errorf("only a ready deployment may have a URL")
		}
		if err := validatePreviewURL(state.URL); err != nil {
			return err
		}
	}
	if state.FailureReason != "" && state.Status != PreviewDeploymentFailed {
		return fmt.Errorf("only a failed deployment may have a failure reason")
	}
	switch state.Status {
	case PreviewDeploymentQueued, PreviewDeploymentBuilding, PreviewDeploymentReady, PreviewDeploymentFailed:
	default:
		return fmt.Errorf("invalid deployment status %q", state.Status)
	}
	if state.CreatedAt.IsZero() || state.UpdatedAt.IsZero() {
		return fmt.Errorf("draft preview timestamps are required")
	}
	return nil
}

func validatePreviewURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("provider returned an invalid preview URL")
	}
	return nil
}

func lockDraftPreviewOperation(siteID, draftID string) func() {
	key := siteID + "\x00" + draftID
	draftPreviewOperationLocks.Lock()
	item := draftPreviewOperationLocks.items[key]
	if item == nil {
		item = &draftPreviewOperationLock{}
		draftPreviewOperationLocks.items[key] = item
	}
	item.users++
	draftPreviewOperationLocks.Unlock()

	item.mutex.Lock()
	return func() {
		item.mutex.Unlock()
		draftPreviewOperationLocks.Lock()
		item.users--
		if item.users == 0 {
			delete(draftPreviewOperationLocks.items, key)
		}
		draftPreviewOperationLocks.Unlock()
	}
}
