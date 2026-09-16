package services

import (
	"context"
	"encoding/json"
	"errors"
	"hugo-cms/pkg/config"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakePreviewProvider struct {
	deployment PreviewDeployment
	triggerErr error
	statusErr  error
	deleteErr  error
	deletedID  string
}

func (provider *fakePreviewProvider) Trigger(context.Context, string, string) (PreviewDeployment, error) {
	return provider.deployment, provider.triggerErr
}

func (provider *fakePreviewProvider) Status(context.Context, string) (PreviewDeployment, error) {
	return provider.deployment, provider.statusErr
}

func (provider *fakePreviewProvider) URL(context.Context, string) (string, error) {
	return provider.deployment.URL, nil
}

func (provider *fakePreviewProvider) Delete(_ context.Context, deploymentID string) error {
	provider.deletedID = deploymentID
	return provider.deleteErr
}

func (provider *fakePreviewProvider) Retry(context.Context, string) (PreviewDeployment, error) {
	return provider.deployment, nil
}

func TestCommitAndPushDraftPreviewUsesTemporaryIndexWithoutCheckout(t *testing.T) {
	runtime, remotePath := setupDraftPreviewRepository(t)
	writeDraftTestFile(t, filepath.Join(runtime.RepoPath, "content", "selected.md"), "selected changed\n")
	writeDraftTestFile(t, filepath.Join(runtime.RepoPath, "content", "unrelated.md"), "unrelated changed\n")

	beforeHead := strings.TrimSpace(runGitOutputForDraftTest(t, runtime.RepoPath, "rev-parse", "HEAD"))
	branch, commitSHA, err := commitAndPushDraftPreview(context.Background(), runtime, "test-token", "draft-1", []string{"content/selected.md"}, localDraftPush(t, runtime))
	if err != nil {
		t.Fatalf("commitAndPushDraftPreview() error = %v", err)
	}
	if branch != "cms-preview/draft-1" || !commitSHAPattern.MatchString(commitSHA) {
		t.Fatalf("branch/commit = %q/%q", branch, commitSHA)
	}
	afterHead := strings.TrimSpace(runGitOutputForDraftTest(t, runtime.RepoPath, "rev-parse", "HEAD"))
	if afterHead != beforeHead {
		t.Fatalf("working checkout HEAD changed from %s to %s", beforeHead, afterHead)
	}
	if currentBranch := strings.TrimSpace(runGitOutputForDraftTest(t, runtime.RepoPath, "branch", "--show-current")); currentBranch != "main" {
		t.Fatalf("current branch = %q, want main", currentBranch)
	}
	if staged := strings.TrimSpace(runGitOutputForDraftTest(t, runtime.RepoPath, "diff", "--cached", "--name-only")); staged != "" {
		t.Fatalf("real Git index was modified: %q", staged)
	}
	if got := runGitOutputForDraftTest(t, runtime.RepoPath, "show", commitSHA+":content/selected.md"); got != "selected changed\n" {
		t.Fatalf("selected content = %q", got)
	}
	if got := runGitOutputForDraftTest(t, runtime.RepoPath, "show", commitSHA+":content/unrelated.md"); got != "unrelated initial\n" {
		t.Fatalf("unrelated content leaked into draft commit: %q", got)
	}
	remoteCommit := strings.TrimSpace(runGitOutputForDraftTest(t, remotePath, "rev-parse", "refs/heads/cms-preview/draft-1"))
	if remoteCommit != commitSHA {
		t.Fatalf("remote commit = %q, want %q", remoteCommit, commitSHA)
	}
}

func TestCommitAndPushDraftPreviewRebasesOntoLatestProductionBranch(t *testing.T) {
	runtime, remotePath := setupDraftPreviewRepository(t)
	selectedPath := filepath.Join(runtime.RepoPath, "content", "selected.md")
	writeDraftTestFile(t, selectedPath, "draft version one\n")
	_, firstCommit, err := commitAndPushDraftPreview(context.Background(), runtime, "token", "draft-rebase", []string{"content/selected.md"}, localDraftPush(t, runtime))
	if err != nil {
		t.Fatal(err)
	}

	writeDraftTestFile(t, filepath.Join(runtime.RepoPath, "theme.txt"), "new production theme\n")
	runGitForDraftTest(t, runtime.RepoPath, "add", "theme.txt")
	runGitForDraftTest(t, runtime.RepoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "update production theme")
	productionCommit := strings.TrimSpace(runGitOutputForDraftTest(t, runtime.RepoPath, "rev-parse", "refs/heads/main"))
	writeDraftTestFile(t, selectedPath, "draft version two\n")

	_, secondCommit, err := commitAndPushDraftPreview(context.Background(), runtime, "token", "draft-rebase", []string{"content/selected.md"}, localDraftPush(t, runtime))
	if err != nil {
		t.Fatal(err)
	}
	if secondCommit == firstCommit {
		t.Fatal("preview commit did not change")
	}
	if parent := strings.TrimSpace(runGitOutputForDraftTest(t, runtime.RepoPath, "rev-parse", secondCommit+"^")); parent != productionCommit {
		t.Fatalf("preview parent = %s, want latest production %s", parent, productionCommit)
	}
	if got := runGitOutputForDraftTest(t, runtime.RepoPath, "show", secondCommit+":theme.txt"); got != "new production theme\n" {
		t.Fatalf("production theme = %q", got)
	}
	if remoteCommit := strings.TrimSpace(runGitOutputForDraftTest(t, remotePath, "rev-parse", "refs/heads/cms-preview/draft-rebase")); remoteCommit != secondCommit {
		t.Fatalf("remote commit = %s, want %s", remoteCommit, secondCommit)
	}
}

func TestCommitAndPushDraftPreviewLeaseFailurePreservesRemoteAndRollsBackLocalRef(t *testing.T) {
	runtime, remotePath := setupDraftPreviewRepository(t)
	selectedPath := filepath.Join(runtime.RepoPath, "content", "selected.md")
	writeDraftTestFile(t, selectedPath, "draft version one\n")
	_, firstCommit, err := commitAndPushDraftPreview(context.Background(), runtime, "token", "draft-lease", []string{"content/selected.md"}, localDraftPush(t, runtime))
	if err != nil {
		t.Fatal(err)
	}
	productionCommit := strings.TrimSpace(runGitOutputForDraftTest(t, runtime.RepoPath, "rev-parse", "refs/heads/main"))
	runGitForDraftTest(t, remotePath, "update-ref", "refs/heads/cms-preview/draft-lease", productionCommit, firstCommit)
	writeDraftTestFile(t, selectedPath, "draft version two\n")

	if _, _, err := commitAndPushDraftPreview(context.Background(), runtime, "token", "draft-lease", []string{"content/selected.md"}, localDraftPush(t, runtime)); err == nil {
		t.Fatal("force-with-lease unexpectedly overwrote a moved remote branch")
	}
	if remoteCommit := strings.TrimSpace(runGitOutputForDraftTest(t, remotePath, "rev-parse", "refs/heads/cms-preview/draft-lease")); remoteCommit != productionCommit {
		t.Fatalf("remote commit = %s, want externally moved %s", remoteCommit, productionCommit)
	}
	if localCommit := strings.TrimSpace(runGitOutputForDraftTest(t, runtime.RepoPath, "rev-parse", "refs/heads/cms-preview/draft-lease")); localCommit != firstCommit {
		t.Fatalf("local ref = %s, want rollback to %s", localCommit, firstCommit)
	}
}

func TestUpdateDraftPreviewPersistsArticleAndPathsAndRejectsRebinding(t *testing.T) {
	runtime, _ := setupDraftPreviewRepository(t)
	writeDraftTestFile(t, filepath.Join(runtime.RepoPath, "content", "selected.md"), "changed\n")
	store, err := NewDraftPreviewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	provider := &fakePreviewProvider{triggerErr: &PreviewProviderError{Kind: PreviewProviderNotFound, Operation: "trigger"}}
	paths := []string{"content/selected.md"}
	state, err := updateDraftPreview(context.Background(), runtime, "token", "draft-state", "selected.md", paths, store, provider, localDraftPush(t, runtime))
	if err != nil {
		t.Fatal(err)
	}
	paths[0] = "content/unrelated.md"
	stored, err := store.Get(runtime.ID, state.DraftID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ArticlePath != "selected.md" || len(stored.Paths) != 1 || stored.Paths[0] != "content/selected.md" {
		t.Fatalf("stored draft identity = %#v", stored)
	}
	if _, err := updateDraftPreview(context.Background(), runtime, "token", "draft-state", "unrelated.md", []string{"content/unrelated.md"}, store, provider, localDraftPush(t, runtime)); !errors.Is(err, ErrDraftPreviewArticleMismatch) {
		t.Fatalf("rebind error = %v, want article mismatch", err)
	}
}

func TestPublishDraftPreviewRejectsDifferentArticleAndMovedRemoteBranch(t *testing.T) {
	runtime, _ := setupDraftPreviewRepository(t)
	writeDraftTestFile(t, filepath.Join(runtime.RepoPath, "content", "selected.md"), "reviewed\n")
	branch, commitSHA, err := commitAndPushDraftPreview(context.Background(), runtime, "token", "draft-publish", []string{"content/selected.md"}, localDraftPush(t, runtime))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewDraftPreviewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := DraftPreviewState{SiteID: runtime.ID, DraftID: "draft-publish", ArticlePath: "selected.md", Paths: []string{"content/selected.md"}, Branch: branch, CommitSHA: commitSHA, DeploymentID: "deployment-1", Status: PreviewDeploymentReady, URL: "https://immutable.example", CreatedAt: now, UpdatedAt: now}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	provider := &fakePreviewProvider{deployment: PreviewDeployment{ID: state.DeploymentID, Branch: branch, CommitSHA: commitSHA, Status: PreviewDeploymentReady, URL: state.URL}}
	remoteCalled := false
	createCalled := false
	remoteHead := func(context.Context, config.SiteRuntime, string, string) (string, error) {
		remoteCalled = true
		return strings.Repeat("f", 40), nil
	}
	createPR := func(context.Context, config.SiteRuntime, string, DraftPreviewState) (string, error) {
		createCalled = true
		return "https://github.com/example/site/pull/1", nil
	}

	if _, err := publishDraftPreview(context.Background(), runtime, "token", state.DraftID, "unrelated.md", store, provider, remoteHead, createPR); !errors.Is(err, ErrDraftPreviewArticleMismatch) {
		t.Fatalf("article mismatch error = %v", err)
	}
	if remoteCalled || createCalled {
		t.Fatal("article mismatch reached remote or pull request operations")
	}
	if _, err := publishDraftPreview(context.Background(), runtime, "token", state.DraftID, state.ArticlePath, store, provider, remoteHead, createPR); !errors.Is(err, ErrDraftPreviewBranchMoved) {
		t.Fatalf("remote mismatch error = %v", err)
	}
	if !remoteCalled || createCalled {
		t.Fatalf("remote/create calls = %v/%v", remoteCalled, createCalled)
	}

	remoteChecks := 0
	createCalled = false
	remoteHead = func(context.Context, config.SiteRuntime, string, string) (string, error) {
		remoteChecks++
		if remoteChecks == 1 {
			return commitSHA, nil
		}
		return strings.Repeat("e", 40), nil
	}
	if _, err := publishDraftPreview(context.Background(), runtime, "token", state.DraftID, state.ArticlePath, store, provider, remoteHead, createPR); !errors.Is(err, ErrDraftPreviewBranchMoved) {
		t.Fatalf("post-PR remote mismatch error = %v", err)
	}
	if remoteChecks != 2 || !createCalled {
		t.Fatalf("remote checks/create calls = %d/%v", remoteChecks, createCalled)
	}
}

func TestPublishArticleCreatesPRWithoutDeploymentPreview(t *testing.T) {
	runtime, remotePath := setupDraftPreviewRepository(t)
	writeDraftTestFile(t, filepath.Join(runtime.RepoPath, "content", "selected.md"), "published directly\n")

	remoteHead := func(_ context.Context, _ config.SiteRuntime, _ string, branch string) (string, error) {
		return strings.TrimSpace(runGitOutputForDraftTest(t, remotePath, "rev-parse", "refs/heads/"+branch)), nil
	}
	var publishedState DraftPreviewState
	createPR := func(_ context.Context, _ config.SiteRuntime, _ string, state DraftPreviewState) (string, error) {
		publishedState = state
		return "https://github.com/example/site/pull/108", nil
	}

	url, err := publishArticle(context.Background(), runtime, "token", "draft-direct", "selected.md", []string{"content/selected.md"}, localDraftPush(t, runtime), remoteHead, createPR)
	if err != nil {
		t.Fatalf("publishArticle() error = %v", err)
	}
	if url != "https://github.com/example/site/pull/108" {
		t.Fatalf("URL = %q", url)
	}
	if publishedState.Status != PreviewDeploymentReady || publishedState.URL != "" {
		t.Fatalf("direct publish state = %#v", publishedState)
	}
	if publishedState.ArticlePath != "selected.md" || publishedState.Paths[0] != "content/selected.md" {
		t.Fatalf("published paths = %#v", publishedState)
	}
}

func TestPublishDraftPreviewHoldsDraftLockThroughPullRequestCreation(t *testing.T) {
	runtime, _ := setupDraftPreviewRepository(t)
	writeDraftTestFile(t, filepath.Join(runtime.RepoPath, "content", "selected.md"), "reviewed\n")
	branch, commitSHA, err := commitAndPushDraftPreview(context.Background(), runtime, "token", "draft-atomic", []string{"content/selected.md"}, localDraftPush(t, runtime))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewDraftPreviewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := DraftPreviewState{SiteID: runtime.ID, DraftID: "draft-atomic", ArticlePath: "selected.md", Paths: []string{"content/selected.md"}, Branch: branch, CommitSHA: commitSHA, DeploymentID: "deployment-1", Status: PreviewDeploymentReady, URL: "https://immutable.example", CreatedAt: now, UpdatedAt: now}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	provider := &fakePreviewProvider{deployment: PreviewDeployment{ID: state.DeploymentID, Branch: branch, CommitSHA: commitSHA, Status: PreviewDeploymentReady, URL: state.URL}}
	pullRequestStarted := make(chan struct{})
	releasePullRequest := make(chan struct{})
	publishDone := make(chan error, 1)
	go func() {
		_, err := publishDraftPreview(context.Background(), runtime, "token", state.DraftID, state.ArticlePath, store, provider,
			func(context.Context, config.SiteRuntime, string, string) (string, error) { return commitSHA, nil },
			func(context.Context, config.SiteRuntime, string, DraftPreviewState) (string, error) {
				close(pullRequestStarted)
				<-releasePullRequest
				return "https://github.com/example/site/pull/1", nil
			})
		publishDone <- err
	}()
	<-pullRequestStarted

	lockAcquired := make(chan struct{})
	go func() {
		unlock := lockDraftPreviewOperation(runtime.ID, state.DraftID)
		close(lockAcquired)
		unlock()
	}()
	select {
	case <-lockAcquired:
		t.Fatal("same draft lock was released before pull request creation completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(releasePullRequest)
	if err := <-publishDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-lockAcquired:
	case <-time.After(time.Second):
		t.Fatal("same draft lock was not released after publish completed")
	}
}

func TestDraftPreviewMatchesWorkingTreeDetectsStaleContent(t *testing.T) {
	runtime, _ := setupDraftPreviewRepository(t)
	selectedPath := filepath.Join(runtime.RepoPath, "content", "selected.md")
	writeDraftTestFile(t, selectedPath, "selected changed\n")
	branch, commitSHA, err := commitAndPushDraftPreview(context.Background(), runtime, "test-token", "draft-verify", []string{"content/selected.md"}, localDraftPush(t, runtime))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := DraftPreviewState{SiteID: runtime.ID, DraftID: "draft-verify", ArticlePath: "selected.md", Paths: []string{"content/selected.md"}, Branch: branch, CommitSHA: commitSHA, Status: PreviewDeploymentReady, URL: "https://abc.pages.dev", CreatedAt: now, UpdatedAt: now}
	match, err := draftPreviewMatchesWorkingTree(context.Background(), runtime, state)
	if err != nil || !match {
		t.Fatalf("initial match = %v, error = %v", match, err)
	}
	writeDraftTestFile(t, selectedPath, "edited after deployment\n")
	match, err = draftPreviewMatchesWorkingTree(context.Background(), runtime, state)
	if err != nil {
		t.Fatal(err)
	}
	if match {
		t.Fatal("draftPreviewMatchesWorkingTree() accepted stale deployed content")
	}
}

func TestCommitAndPushDraftPreviewRejectsUnsafeIdentifiersAndPaths(t *testing.T) {
	runtime, _ := setupDraftPreviewRepository(t)
	pushCalled := false
	push := func(string, string, ...string) (string, error) {
		pushCalled = true
		return "", nil
	}
	tests := []struct {
		draftID string
		paths   []string
	}{
		{draftID: "../escape", paths: []string{"content/selected.md"}},
		{draftID: "UPPER", paths: []string{"content/selected.md"}},
		{draftID: "draft-1", paths: []string{"../outside.md"}},
		{draftID: "draft-1", paths: []string{".git/config"}},
		{draftID: "draft-1", paths: nil},
	}
	for _, test := range tests {
		if _, _, err := commitAndPushDraftPreview(context.Background(), runtime, "token", test.draftID, test.paths, push); err == nil {
			t.Fatalf("draft=%q paths=%#v should fail", test.draftID, test.paths)
		}
	}
	if pushCalled {
		t.Fatal("unsafe input reached push")
	}
}

func TestDraftPreviewStoreRoundTripAndIdentityProtection(t *testing.T) {
	store, err := NewDraftPreviewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	state := DraftPreviewState{
		SiteID:          "docs site",
		DraftID:         "draft-1",
		ArticlePath:     "selected.md",
		Paths:           []string{"content/selected.md"},
		Branch:          "cms-preview/draft-1",
		CommitSHA:       testCommitSHA,
		DeploymentID:    "deployment-1",
		Status:          PreviewDeploymentReady,
		URL:             "https://immutable.docs.pages.dev",
		AccessProtected: true,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := store.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Get(state.SiteID, state.DraftID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.CommitSHA != state.CommitSHA || got.URL != state.URL || !got.AccessProtected {
		t.Fatalf("Get() = %#v", got)
	}
	_, path := store.statePath(state.SiteID, state.DraftID)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var encoded map[string]interface{}
	if err := json.Unmarshal(content, &encoded); err != nil {
		t.Fatal(err)
	}
	if _, exists := encoded["token"]; exists || strings.Contains(string(content), "CLOUDFLARE_API_TOKEN") {
		t.Fatalf("state exposed token data: %s", content)
	}
	if err := store.Delete(state.SiteID, state.DraftID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(state.SiteID, state.DraftID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Get() after Delete error = %v", err)
	}
}

func TestDraftPreviewStoreListsCleanupTombstones(t *testing.T) {
	store, err := NewDraftPreviewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, state := range []DraftPreviewState{
		{SiteID: "docs", DraftID: "pending", ArticlePath: "selected.md", Paths: []string{"content/selected.md"}, Branch: "cms-preview/pending", CommitSHA: testCommitSHA, Status: PreviewDeploymentFailed, CleanupPending: true, CreatedAt: now, UpdatedAt: now},
		{SiteID: "docs", DraftID: "active", ArticlePath: "selected.md", Paths: []string{"content/selected.md"}, Branch: "cms-preview/active", CommitSHA: testCommitSHA, Status: PreviewDeploymentBuilding, CreatedAt: now, UpdatedAt: now},
	} {
		if err := store.Save(state); err != nil {
			t.Fatal(err)
		}
	}
	states, err := store.ListCleanupPending()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].DraftID != "pending" {
		t.Fatalf("cleanup-pending states = %#v", states)
	}
}

func TestApplyProviderDeploymentNeverExposesWrongOrUnreadyURL(t *testing.T) {
	store, err := NewDraftPreviewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := DraftPreviewState{SiteID: "docs", DraftID: "draft-1", ArticlePath: "selected.md", Paths: []string{"content/selected.md"}, Branch: "cms-preview/draft-1", CommitSHA: testCommitSHA, Status: PreviewDeploymentQueued, CreatedAt: now, UpdatedAt: now}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	building := PreviewDeployment{ID: "deployment-1", Branch: state.Branch, CommitSHA: state.CommitSHA, Status: PreviewDeploymentBuilding, URL: "https://stale.example"}
	got, err := applyProviderDeployment(store, state, building)
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "" {
		t.Fatalf("building deployment exposed URL %q", got.URL)
	}
	wrong := building
	wrong.CommitSHA = "ffffffffffffffffffffffffffffffffffffffff"
	if _, err := applyProviderDeployment(store, state, wrong); !IsPreviewProviderError(err, PreviewProviderInvalidReply) {
		t.Fatalf("wrong commit error = %v", err)
	}
}

func TestCleanupDraftPreviewDeletesRemoteProviderLocalRefAndState(t *testing.T) {
	runtime, remotePath := setupDraftPreviewRepository(t)
	writeDraftTestFile(t, filepath.Join(runtime.RepoPath, "content", "selected.md"), "changed\n")
	branch, commitSHA, err := commitAndPushDraftPreview(context.Background(), runtime, "token", "draft-1", []string{"content/selected.md"}, localDraftPush(t, runtime))
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewDraftPreviewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := DraftPreviewState{SiteID: runtime.ID, DraftID: "draft-1", ArticlePath: "selected.md", Paths: []string{"content/selected.md"}, Branch: branch, CommitSHA: commitSHA, DeploymentID: "deployment-1", Status: PreviewDeploymentReady, URL: "https://immutable.example", CreatedAt: now, UpdatedAt: now}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	provider := &fakePreviewProvider{}
	if err := cleanupDraftPreview(context.Background(), runtime, "token", "draft-1", store, provider, localDraftPush(t, runtime)); err != nil {
		t.Fatalf("cleanupDraftPreview() error = %v", err)
	}
	if provider.deletedID != "deployment-1" {
		t.Fatalf("provider deleted ID = %q", provider.deletedID)
	}
	if command := exec.Command("git", "rev-parse", "--verify", "refs/heads/cms-preview/draft-1"); func() bool { command.Dir = remotePath; return command.Run() == nil }() {
		t.Fatal("remote preview branch still exists")
	}
	if command := exec.Command("git", "rev-parse", "--verify", "refs/heads/cms-preview/draft-1"); func() bool { command.Dir = runtime.RepoPath; return command.Run() == nil }() {
		t.Fatal("local preview branch still exists")
	}
	if _, err := store.Get(runtime.ID, "draft-1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state still exists: %v", err)
	}
}

func setupDraftPreviewRepository(t *testing.T) (config.SiteRuntime, string) {
	t.Helper()
	repoPath := t.TempDir()
	remotePath := filepath.Join(t.TempDir(), "remote.git")
	if err := os.MkdirAll(remotePath, 0755); err != nil {
		t.Fatal(err)
	}
	runGitForDraftTest(t, remotePath, "init", "--bare")
	runGitForDraftTest(t, repoPath, "init", "-b", "main")
	writeDraftTestFile(t, filepath.Join(repoPath, "content", "selected.md"), "selected initial\n")
	writeDraftTestFile(t, filepath.Join(repoPath, "content", "unrelated.md"), "unrelated initial\n")
	runGitForDraftTest(t, repoPath, "add", ".")
	runGitForDraftTest(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "initial")
	runGitForDraftTest(t, repoPath, "remote", "add", "origin", remotePath)
	runGitForDraftTest(t, repoPath, "push", "-u", "origin", "main")
	runtime := config.NewSiteRuntime(config.SiteConfig{ID: "docs", RepoPath: repoPath, ContentDir: "content", StaticDir: "static", PublicDir: "public"})
	runtime.GitBranch = "main"
	runtime.GitRemote = "origin"
	runtime.GitUserName = "Test"
	runtime.GitUserEmail = "test@example.com"
	return runtime, remotePath
}

func localDraftPush(t *testing.T, runtime config.SiteRuntime) gitPushFunc {
	t.Helper()
	return func(directory, _ string, args ...string) (string, error) {
		command := exec.Command("git", args...)
		command.Dir = directory
		output, err := command.CombinedOutput()
		return string(output), err
	}
}

func writeDraftTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func runGitForDraftTest(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

func runGitOutputForDraftTest(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(output)
}
