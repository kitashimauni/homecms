package services

import (
	"hugo-cms/pkg/config"
	"path/filepath"
	"testing"
)

func TestValidateConfigForRuntimeWarnsAboutHomeCMSCreateAndPreviewIssues(t *testing.T) {
	repoPath := t.TempDir()
	writeTestFile(t, filepath.Join(repoPath, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: posts
      folder: content/posts
      path: "{{slug}}"
      frontmatter: yaml
      fields:
        - { name: title, widget: string }
media:
  folder: static/images
preview:
  url_field: permalink
`)

	warnings := ValidateConfigForRuntime(testRuntime(repoPath), ".homecms.yml")
	assertWarningCode(t, warnings, "missing_slug_field")
	assertWarningCode(t, warnings, "unknown_preview_url_field")
}

func TestValidateConfigForRuntimeWarnsAboutLegacyConfig(t *testing.T) {
	repoPath := t.TempDir()
	writeTestFile(t, filepath.Join(repoPath, "static", "admin", "config.yml"), `
collections:
  - name: posts
    folder: content/posts
    path: "{{title}}"
    media_folder: "/{{year}}{{month}}{{day}}-{{url_title}}/src"
    fields:
      - name: title
        widget: string
`)

	warnings := ValidateConfigForRuntime(testRuntime(repoPath), "config.yml")
	assertWarningCode(t, warnings, "legacy_config")
	assertNoWarningCode(t, warnings, "unknown_path_variable")
	assertNoWarningCode(t, warnings, "invalid_media_folder")
}

func TestValidateConfigForRuntimeReportsLegacyConfigPathWithStaticDir(t *testing.T) {
	repoPath := t.TempDir()
	writeTestFile(t, filepath.Join(repoPath, "public-assets", "admin", "config.yml"), `
collections:
  - name: posts
    folder: content/posts
    fields:
      - name: slug
        widget: string
`)

	runtime := config.NewSiteRuntime(config.SiteConfig{
		ID:         "test",
		RepoPath:   repoPath,
		ContentDir: "content",
		StaticDir:  "public-assets",
	})
	warnings := ValidateConfigForRuntime(runtime, "config.yml")
	warning := findWarningCode(t, warnings, "legacy_config")
	if warning.Path != "public-assets/admin/config.yml" {
		t.Fatalf("legacy warning path = %q, want public-assets/admin/config.yml", warning.Path)
	}
}

func TestValidateConfigForRuntimeWarnsAboutInvalidPaths(t *testing.T) {
	repoPath := t.TempDir()
	writeTestFile(t, filepath.Join(repoPath, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: posts
      folder: ../posts
      path: "{{slug}}"
      fields:
        - { name: slug, widget: mystery }
media:
  folder: /absolute
`)

	warnings := ValidateConfigForRuntime(config.NewSiteRuntime(config.SiteConfig{
		ID:         "test",
		RepoPath:   repoPath,
		ContentDir: "content",
		StaticDir:  "static",
	}), ".homecms.yml")
	assertWarningCode(t, warnings, "invalid_collection_folder")
	assertWarningCode(t, warnings, "invalid_media_folder")
	assertWarningCode(t, warnings, "unsupported_widget")
}

func TestValidateConfigForRuntimeWarnsAboutCollectionOutsideContentDir(t *testing.T) {
	repoPath := t.TempDir()
	writeTestFile(t, filepath.Join(repoPath, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: posts
      folder: assets/posts
      path: "{{slug}}"
      fields:
        - { name: slug, widget: string }
`)

	warnings := ValidateConfigForRuntime(config.NewSiteRuntime(config.SiteConfig{
		ID:         "test",
		RepoPath:   repoPath,
		ContentDir: "content",
		StaticDir:  "static",
	}), ".homecms.yml")
	assertWarningCode(t, warnings, "invalid_collection_folder")
}

func TestValidateConfigForRuntimeWarnsAboutArticleMediaFolderEscape(t *testing.T) {
	repoPath := t.TempDir()
	writeTestFile(t, filepath.Join(repoPath, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: posts
      folder: content/posts
      media_folder: "{{dirname}}/../../shared"
      fields:
        - { name: slug, widget: string }
`)

	warnings := ValidateConfigForRuntime(config.NewSiteRuntime(config.SiteConfig{
		ID:         "test",
		RepoPath:   repoPath,
		ContentDir: "content",
		StaticDir:  "static",
	}), ".homecms.yml")
	assertWarningCode(t, warnings, "invalid_media_folder")
}

func TestValidateConfigForRuntimeReturnsEmptySliceForCleanHomeCMSConfig(t *testing.T) {
	repoPath := t.TempDir()
	writeTestFile(t, filepath.Join(repoPath, ".homecms.yml"), `
version: 1
content:
  collections:
    - name: posts
      folder: content/posts
      path: "{{slug}}"
      frontmatter: yaml
      fields:
        - { name: slug, widget: string }
        - { name: title, widget: string }
        - { name: permalink, widget: string }
preview:
  url_field: permalink
`)

	warnings := ValidateConfigForRuntime(testRuntime(repoPath), ".homecms.yml")
	if warnings == nil {
		t.Fatal("ValidateConfigForRuntime() returned nil warnings, want empty slice")
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want empty slice", warnings)
	}
}

func assertWarningCode(t *testing.T, warnings []ConfigWarning, code string) {
	t.Helper()
	_ = findWarningCode(t, warnings, code)
}

func findWarningCode(t *testing.T, warnings []ConfigWarning, code string) ConfigWarning {
	t.Helper()
	for _, warning := range warnings {
		if warning.Code == code {
			return warning
		}
	}
	t.Fatalf("warning code %q not found in %#v", code, warnings)
	return ConfigWarning{}
}

func assertNoWarningCode(t *testing.T, warnings []ConfigWarning, code string) {
	t.Helper()
	for _, warning := range warnings {
		if warning.Code == code {
			t.Fatalf("warning code %q unexpectedly found in %#v", code, warnings)
		}
	}
}
