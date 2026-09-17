package services

import (
	"bytes"
	"errors"
	"fmt"
	"hugo-cms/pkg/config"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type MediaFile struct {
	Name             string `json:"name"`
	Path             string `json:"path"` // Relative path for usage in markdown
	Size             int64  `json:"size"`
	URL              string `json:"url"` // URL for preview
	RepoPath         string `json:"repo_path"`
	LocalPreviewSync *bool  `json:"local_preview_sync,omitempty"`
}

var ErrInvalidMedia = errors.New("invalid media")

var allowedMediaTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".pdf":  "application/pdf",
}

func isAllowedMediaExtension(path string) bool {
	_, ok := allowedMediaTypes[strings.ToLower(filepath.Ext(path))]
	return ok
}

func isAllowedImageExtension(path string) bool {
	mediaType, ok := allowedMediaTypes[strings.ToLower(filepath.Ext(path))]
	return ok && strings.HasPrefix(mediaType, "image/")
}

type staticMediaTarget struct {
	repoRelDir string
	publicBase string
}

type articleMediaTarget struct {
	articleDir string
	targetDir  string
}

func ValidateMediaRepoPathForRuntime(runtime config.SiteRuntime, repoPath string) bool {
	if repoPath == "" || !isAllowedMediaExtension(repoPath) {
		return false
	}
	normalized := filepath.ToSlash(filepath.Clean(repoPath))
	staticPrefix := filepath.ToSlash(filepath.Clean(runtime.StaticDir)) + "/"
	contentPrefix := filepath.ToSlash(filepath.Clean(runtime.ContentDir)) + "/"
	staticMedia := staticMediaTargetForRuntime(runtime)
	staticMediaPrefix := filepath.ToSlash(filepath.Clean(staticMedia.repoRelDir)) + "/"
	if normalized == "." ||
		(!strings.HasPrefix(normalized, staticPrefix) &&
			!strings.HasPrefix(normalized, staticMediaPrefix) &&
			!strings.HasPrefix(normalized, contentPrefix)) {
		return false
	}
	return SafeJoin(runtime.RepoPath, "", normalized) != ""
}

func ValidateArticleMediaRepoPathForRuntime(runtime config.SiteRuntime, articlePath, repoPath string) bool {
	if repoPath == "" || !isAllowedMediaExtension(repoPath) {
		return false
	}
	target, err := articleMediaTargetForRuntime(runtime, articlePath)
	if err != nil {
		return false
	}
	fullMediaPath := SafeJoin(runtime.RepoPath, "", filepath.ToSlash(filepath.Clean(repoPath)))
	return fullMediaPath != "" && isPathWithin(target.targetDir, fullMediaPath) && isResolvedPathWithin(target.targetDir, fullMediaPath)
}

func articleMediaTargetForRuntime(runtime config.SiteRuntime, articlePath string) (articleMediaTarget, error) {
	invalid := func(message string) (articleMediaTarget, error) {
		return articleMediaTarget{}, fmt.Errorf("%w: %s", ErrInvalidMedia, message)
	}

	articlePath = strings.TrimSpace(filepath.ToSlash(articlePath))
	fullArticlePath := SafeJoin(runtime.RepoPath, runtime.ContentDir, articlePath)
	if fullArticlePath == "" {
		return invalid("invalid article path")
	}

	collectionPath := filepath.ToSlash(filepath.Join(runtime.ContentDir, articlePath))
	collection, collectionErr := GetCollectionForPathForRuntime(runtime, collectionPath)
	collectionRoot := SafeJoin(runtime.RepoPath, "", filepath.ToSlash(filepath.Clean(runtime.ContentDir)))
	if collectionErr == nil {
		collectionFolder, err := CollectionFolderWithinContentForRuntime(runtime, *collection)
		if err != nil {
			return invalid("invalid collection folder")
		}
		collectionRoot = SafeJoin(runtime.RepoPath, "", collectionFolder)
	}
	if collectionRoot == "" {
		return invalid("invalid content collection root")
	}

	extension := ".md"
	if collection != nil && strings.TrimSpace(collection.Extension) != "" {
		extension = "." + strings.TrimPrefix(strings.TrimSpace(collection.Extension), ".")
	}
	if !strings.EqualFold(filepath.Ext(fullArticlePath), extension) {
		return invalid("invalid article path")
	}

	mediaFolder := ""
	if collection != nil {
		mediaFolder = strings.TrimSpace(collection.MediaFolder)
	}
	if mediaFolder == "" {
		mediaFolder = strings.TrimSpace(runtime.ArticleMediaDir)
	}
	if mediaFolder == "" {
		baseName := strings.ToLower(filepath.Base(fullArticlePath))
		if baseName == "index.md" || baseName == "_index.md" {
			mediaFolder = "{{dirname}}"
		} else {
			return invalid("article media is not configured for this collection")
		}
	}

	if filepath.IsAbs(mediaFolder) || strings.HasPrefix(mediaFolder, "/") || strings.HasPrefix(mediaFolder, `\`) || strings.Contains(mediaFolder, ":") {
		return invalid("invalid article media folder")
	}
	mediaFolder = strings.ReplaceAll(mediaFolder, "{{dirname}}", "")
	mediaFolder = strings.TrimLeft(strings.TrimSpace(mediaFolder), `/\`)
	if strings.Contains(mediaFolder, "{{") || strings.Contains(mediaFolder, "}}") || strings.Contains(mediaFolder, ":") {
		return invalid("invalid article media folder")
	}
	articleDir := filepath.Dir(fullArticlePath)
	targetDir := SafeJoin(articleDir, "", mediaFolder)
	contentRoot := filepath.Join(runtime.RepoPath, runtime.ContentDir)
	if targetDir == "" ||
		!isPathWithin(collectionRoot, targetDir) ||
		!isResolvedPathWithin(collectionRoot, targetDir) ||
		!isPathWithin(contentRoot, targetDir) ||
		!isResolvedPathWithin(contentRoot, targetDir) {
		return invalid("article media folder is outside the collection")
	}
	return articleMediaTarget{articleDir: articleDir, targetDir: targetDir}, nil
}

func ListMediaFilesForRuntime(runtime config.SiteRuntime, mode, articlePath string) ([]MediaFile, error) {
	var searchDirs []string
	usageRoots := make(map[string]string)
	if mode == "" {
		mode = "static"
	}

	// Determine search roots based on mode
	if mode == "static" {
		staticMedia := staticMediaTargetForRuntime(runtime)
		staticDir := SafeJoin(runtime.RepoPath, "", staticMedia.repoRelDir)
		if staticDir == "" {
			return nil, fmt.Errorf("%w: invalid static media directory", ErrInvalidMedia)
		}
		if _, err := os.Stat(staticDir); err == nil {
			searchDirs = append(searchDirs, staticDir)
		}
	} else if mode == "content" {
		if articlePath == "" {
			return nil, nil // No article context, return empty
		}
		target, err := articleMediaTargetForRuntime(runtime, articlePath)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(target.targetDir); err == nil {
			searchDirs = append(searchDirs, target.targetDir)
			usageRoots[target.targetDir] = target.articleDir
		}
	} else {
		return nil, fmt.Errorf("%w: invalid media mode", ErrInvalidMedia)
	}

	var files []MediaFile
	for _, root := range searchDirs {
		// Walk directory to find images
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			// The current media picker renders thumbnails with <img> and
			// inserts image Markdown, so only expose image files here.
			if isAllowedImageExtension(path) {
				// Found image
				relPath, relErr := filepath.Rel(runtime.RepoPath, path)
				if relErr != nil {
					return relErr
				}
				relPath = filepath.ToSlash(relPath)
				if !ValidateMediaRepoPathForRuntime(runtime, relPath) {
					return nil
				}

				// Determine Usage Path
				usagePath := ""
				if mode == "static" {
					staticMedia := staticMediaTargetForRuntime(runtime)
					staticRel, relErr := filepath.Rel(filepath.Join(runtime.RepoPath, filepath.FromSlash(staticMedia.repoRelDir)), path)
					if relErr != nil {
						return relErr
					}
					usagePath = joinPublicPath(staticMedia.publicBase, filepath.ToSlash(staticRel))
				} else {
					articleRoot := usageRoots[root]
					bundleRel, relErr := filepath.Rel(articleRoot, path)
					if relErr != nil {
						return relErr
					}
					usagePath = filepath.ToSlash(bundleRel)
				}

				info, infoErr := d.Info()
				if infoErr != nil {
					return infoErr
				}
				files = append(files, MediaFile{
					Name:     d.Name(), // Or relative path from root?
					Path:     usagePath,
					Size:     info.Size(),
					URL:      "/admin/api/media/raw?path=" + url.QueryEscape(relPath),
					RepoPath: relPath,
				})
			}
			return nil
		})
		if err != nil {
			slog.Warn("Walk error during media listing", "root", root, "error", err)
		}
	}
	return files, nil
}

func SaveMediaFileForRuntime(runtime config.SiteRuntime, header *multipart.FileHeader, mode, articlePath string) (*MediaFile, error) {
	unlock := LockRepositoryOperation(runtime)
	defer unlock()

	src, err := header.Open()
	if err != nil {
		return nil, err
	}
	defer src.Close()

	if header.Size <= 0 || header.Size > config.MaxUploadSize {
		return nil, fmt.Errorf("%w: invalid file size", ErrInvalidMedia)
	}

	filename := filepath.Base(header.Filename)
	filename = strings.ReplaceAll(filename, " ", "_")

	ext := strings.ToLower(filepath.Ext(filename))
	expectedContentType, ok := allowedMediaTypes[ext]
	if !ok {
		return nil, fmt.Errorf("%w: file type not allowed: %s", ErrInvalidMedia, ext)
	}

	name := strings.TrimSuffix(filename, ext)
	filename = fmt.Sprintf("%s_%d%s", name, time.Now().UnixNano(), ext)

	var targetDir string
	articleUsageRoot := ""
	staticMedia := staticMediaTargetForRuntime(runtime)

	if mode == "static" {
		targetDir = SafeJoin(runtime.RepoPath, "", staticMedia.repoRelDir)
	} else if mode == "content" {
		// Content mode
		if articlePath == "" {
			return nil, fmt.Errorf("%w: article path required for content upload", ErrInvalidMedia)
		}
		target, err := articleMediaTargetForRuntime(runtime, articlePath)
		if err != nil {
			return nil, err
		}
		targetDir = target.targetDir
		articleUsageRoot = target.articleDir
	} else {
		return nil, fmt.Errorf("%w: invalid media mode", ErrInvalidMedia)
	}
	if targetDir == "" {
		return nil, fmt.Errorf("%w: invalid media target directory", ErrInvalidMedia)
	}

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return nil, err
	}

	fullMediaPath := filepath.Join(targetDir, filename)
	dst, err := os.CreateTemp(targetDir, ".upload-*")
	if err != nil {
		return nil, err
	}
	tempPath := dst.Name()
	defer os.Remove(tempPath)

	head := make([]byte, 512)
	n, readErr := io.ReadFull(src, head)
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		dst.Close()
		return nil, fmt.Errorf("read upload: %w", readErr)
	}
	if detected := http.DetectContentType(head[:n]); detected != expectedContentType {
		dst.Close()
		return nil, fmt.Errorf("%w: file content does not match extension: got %s", ErrInvalidMedia, detected)
	}

	reader := io.MultiReader(bytes.NewReader(head[:n]), src)
	written, copyErr := io.Copy(dst, io.LimitReader(reader, config.MaxUploadSize+1))
	if copyErr != nil {
		dst.Close()
		return nil, copyErr
	}
	if written > config.MaxUploadSize {
		dst.Close()
		return nil, fmt.Errorf("%w: file exceeds maximum upload size", ErrInvalidMedia)
	}
	if err := dst.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tempPath, fullMediaPath); err != nil {
		return nil, err
	}
	if err := os.Chmod(fullMediaPath, 0644); err != nil {
		return nil, err
	}

	// Calculate Result
	relPath, err := filepath.Rel(runtime.RepoPath, fullMediaPath)
	if err != nil {
		return nil, err
	}
	relPath = filepath.ToSlash(relPath)

	usagePath := ""
	if mode == "static" {
		staticRel, relErr := filepath.Rel(filepath.Join(runtime.RepoPath, filepath.FromSlash(staticMedia.repoRelDir)), fullMediaPath)
		if relErr != nil {
			return nil, relErr
		}
		usagePath = joinPublicPath(staticMedia.publicBase, filepath.ToSlash(staticRel))
	} else {
		bundleRel, relErr := filepath.Rel(articleUsageRoot, fullMediaPath)
		if relErr != nil {
			return nil, relErr
		}
		usagePath = filepath.ToSlash(bundleRel)
	}

	return &MediaFile{
		Name:     filename,
		Path:     usagePath,
		Size:     header.Size,
		URL:      "/admin/api/media/raw?path=" + url.QueryEscape(relPath),
		RepoPath: relPath,
	}, nil
}

func DeleteMediaFileForRuntime(runtime config.SiteRuntime, repoPath string) error {
	return deleteMediaFileForRuntime(runtime, repoPath, "")
}

func DeleteArticleMediaFileForRuntime(runtime config.SiteRuntime, repoPath, articlePath string) error {
	return deleteMediaFileForRuntime(runtime, repoPath, articlePath)
}

func deleteMediaFileForRuntime(runtime config.SiteRuntime, repoPath, articlePath string) error {
	unlock := LockRepositoryOperation(runtime)
	defer unlock()

	valid := ValidateMediaRepoPathForRuntime(runtime, repoPath)
	if articlePath != "" {
		valid = ValidateArticleMediaRepoPathForRuntime(runtime, articlePath, repoPath)
	}
	if !valid {
		return fmt.Errorf("%w: invalid media path", ErrInvalidMedia)
	}
	fullMediaPath := SafeJoin(runtime.RepoPath, "", repoPath)
	if fullMediaPath == "" {
		return fmt.Errorf("%w: invalid media path", ErrInvalidMedia)
	}
	return os.Remove(fullMediaPath)
}

func staticMediaTargetForRuntime(runtime config.SiteRuntime) staticMediaTarget {
	if runtime.StaticMediaDir != "" {
		return staticMediaTarget{
			repoRelDir: filepath.ToSlash(filepath.Join(runtime.StaticDir, runtime.StaticMediaDir)),
			publicBase: "/" + strings.Trim(filepath.ToSlash(runtime.StaticMediaDir), "/"),
		}
	}

	if cfg, err := GetCMSConfigForRuntime(runtime); err == nil && cfg.MediaFolder != "" {
		repoRelDir := cleanMediaRepoRelDir(cfg.MediaFolder)
		if repoRelDir == "" {
			return staticMediaTarget{
				repoRelDir: filepath.ToSlash(filepath.Clean(runtime.StaticDir)),
				publicBase: "",
			}
		}
		publicBase := cfg.PublicFolder
		if publicBase == "" {
			if repoRelDir == filepath.ToSlash(filepath.Clean(runtime.StaticDir)) {
				publicBase = ""
			} else {
				publicBase = "/" + strings.Trim(filepath.ToSlash(filepath.Base(repoRelDir)), "/")
			}
		}
		return staticMediaTarget{
			repoRelDir: repoRelDir,
			publicBase: cleanPublicPath(publicBase),
		}
	}

	return staticMediaTarget{
		repoRelDir: filepath.ToSlash(filepath.Clean(runtime.StaticDir)),
		publicBase: "",
	}
}

func joinPublicPath(base, rel string) string {
	rel = strings.Trim(filepath.ToSlash(rel), "/")
	base = cleanPublicPath(base)
	if base == "" {
		return "/" + rel
	}
	if rel == "" {
		return base
	}
	return base + "/" + rel
}

func cleanMediaRepoRelDir(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.ToSlash(path)
	path = strings.TrimLeft(path, "/")
	if strings.Contains(path, ":") {
		return ""
	}
	return cleanConfigPath(path)
}
