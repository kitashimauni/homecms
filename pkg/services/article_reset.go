package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hugo-cms/pkg/config"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var ErrInvalidArticleResetPath = errors.New("invalid article reset path")

type ArticleResetResult struct {
	HeadExists                 bool
	Deleted                    bool
	RequiresDeleteConfirmation bool
	Revision                   string
}

// ArticleHEADExistsForRuntime checks whether the requested article is a file
// in the local Git HEAD without changing the working tree.
func ArticleHEADExistsForRuntime(runtime config.SiteRuntime, articlePath string) (bool, error) {
	_, fullPath, gitPath, err := articleResetPaths(runtime, articlePath)
	if err != nil {
		return false, err
	}
	if hasSymlinkComponent(runtime.RepoPath, fullPath) {
		return false, fmt.Errorf("%w: symlink path is not allowed", ErrInvalidArticleResetPath)
	}
	_, exists, err := readArticleFromHEAD(runtime, gitPath)
	return exists, err
}

// ResetArticleToHEADForRuntime restores only the requested article file from
// the repository's local HEAD. The caller owns the repository operation lock
// when this is combined with other production/preview mutations.
func ResetArticleToHEADForRuntime(runtime config.SiteRuntime, articlePath string, confirmDelete bool) (ArticleResetResult, error) {
	normalizedPath, fullPath, gitPath, err := articleResetPaths(runtime, articlePath)
	if err != nil {
		return ArticleResetResult{}, err
	}
	if hasSymlinkComponent(runtime.RepoPath, fullPath) {
		return ArticleResetResult{}, fmt.Errorf("%w: symlink path is not allowed", ErrInvalidArticleResetPath)
	}

	headContent, headExists, err := readArticleFromHEAD(runtime, gitPath)
	if err != nil {
		return ArticleResetResult{}, err
	}

	if !headExists {
		if !confirmDelete {
			return ArticleResetResult{RequiresDeleteConfirmation: true}, nil
		}
		if info, statErr := os.Lstat(fullPath); statErr == nil {
			if info.IsDir() {
				return ArticleResetResult{}, fmt.Errorf("%w: article path is a directory", ErrInvalidArticleResetPath)
			}
			if removeErr := os.Remove(fullPath); removeErr != nil {
				return ArticleResetResult{}, fmt.Errorf("delete article not present in HEAD: %w", removeErr)
			}
		} else if !os.IsNotExist(statErr) {
			return ArticleResetResult{}, fmt.Errorf("inspect article before reset: %w", statErr)
		}
		InvalidateCacheForRuntime(runtime)
		return ArticleResetResult{Deleted: true}, nil
	}

	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return ArticleResetResult{}, fmt.Errorf("create article directory: %w", err)
	}
	if err := os.WriteFile(fullPath, headContent, 0644); err != nil {
		return ArticleResetResult{}, fmt.Errorf("restore article %q: %w", normalizedPath, err)
	}

	InvalidateCacheForRuntime(runtime)
	return ArticleResetResult{
		HeadExists: true,
		Revision:   ArticleRevision(headContent),
	}, nil
}

func articleResetPaths(runtime config.SiteRuntime, articlePath string) (string, string, string, error) {
	normalizedPath := filepath.ToSlash(filepath.Clean(strings.TrimSpace(articlePath)))
	if normalizedPath == "" || normalizedPath == "." || filepath.IsAbs(articlePath) || strings.HasPrefix(normalizedPath, "../") || normalizedPath == ".." || strings.Contains(normalizedPath, ":") {
		return "", "", "", fmt.Errorf("%w: article path must stay under %s", ErrInvalidArticleResetPath, runtime.ContentDir)
	}

	fullPath := SafeJoin(runtime.RepoPath, runtime.ContentDir, normalizedPath)
	if fullPath == "" {
		return "", "", "", fmt.Errorf("%w: article path must stay under %s", ErrInvalidArticleResetPath, runtime.ContentDir)
	}
	gitPath := filepath.ToSlash(filepath.Join(runtime.ContentDir, normalizedPath))
	return normalizedPath, fullPath, gitPath, nil
}

func readArticleFromHEAD(runtime config.SiteRuntime, gitPath string) ([]byte, bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), config.GitCommandTimeout)
	defer cancel()

	verify := exec.CommandContext(ctx, "git", "rev-parse", "--verify", "HEAD^{commit}")
	verify.Dir = runtime.RepoPath
	if _, err := verify.Output(); err != nil {
		return nil, false, fmt.Errorf("resolve Git HEAD: %w", err)
	}

	listTree := exec.CommandContext(ctx, "git", "ls-tree", "-z", "HEAD", "--", gitPath)
	listTree.Dir = runtime.RepoPath
	treeOutput, err := listTree.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, fmt.Errorf("inspect Git HEAD article: %w", ctx.Err())
		}
		return nil, false, fmt.Errorf("inspect Git HEAD article: %w", err)
	}
	if len(bytes.Trim(treeOutput, "\x00\n\r")) == 0 {
		return nil, false, nil
	}
	fields := strings.Fields(string(bytes.TrimSuffix(treeOutput, []byte{0})))
	if len(fields) < 3 || fields[1] != "blob" {
		return nil, false, fmt.Errorf("Git HEAD article is not a file")
	}

	objectName := "HEAD:" + gitPath
	show := exec.CommandContext(ctx, "git", "show", objectName)
	show.Dir = runtime.RepoPath
	content, err := show.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, fmt.Errorf("read article from Git HEAD: %w", ctx.Err())
		}
		return nil, false, fmt.Errorf("read article from Git HEAD: %w", err)
	}
	return content, true, nil
}

func hasSymlinkComponent(root, target string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return true
	}
	targetAbs, err := filepath.Abs(target)
	if err != nil {
		return true
	}
	rel, err := filepath.Rel(rootAbs, targetAbs)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return true
	}

	current := rootAbs
	for _, component := range strings.Split(rel, string(os.PathSeparator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return false
		}
		if err != nil {
			return true
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}
