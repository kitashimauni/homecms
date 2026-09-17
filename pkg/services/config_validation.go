package services

import (
	"fmt"
	"hugo-cms/pkg/config"
	"hugo-cms/pkg/models"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

type ConfigWarning struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

func ValidateConfigForRuntime(runtime config.SiteRuntime, source string) []ConfigWarning {
	switch source {
	case homeCMSConfigFile:
		return validateHomeCMSConfigFile(runtime, filepath.Join(runtime.RepoPath, homeCMSConfigFile))
	case legacyCMSConfigFile:
		return validateLegacyCMSConfigFile(runtime)
	default:
		return []ConfigWarning{{
			Severity: "warning",
			Code:     "unknown_config_source",
			Message:  "CMS config source could not be identified.",
		}}
	}
}

func validateHomeCMSConfigFile(runtime config.SiteRuntime, path string) []ConfigWarning {
	content, err := os.ReadFile(path)
	if err != nil {
		return []ConfigWarning{configWarning("error", "config_read_failed", ".homecms.yml", "Failed to read .homecms.yml: "+err.Error())}
	}
	var home models.HomeCMSConfig
	if err := yaml.Unmarshal(content, &home); err != nil {
		return []ConfigWarning{configWarning("error", "config_parse_failed", ".homecms.yml", "Failed to parse .homecms.yml: "+err.Error())}
	}
	return validateHomeCMSConfig(runtime, home)
}

func validateLegacyCMSConfigFile(runtime config.SiteRuntime) []ConfigWarning {
	legacyPath := filepath.ToSlash(filepath.Join(runtime.StaticDir, "admin", legacyCMSConfigFile))
	warnings := []ConfigWarning{configWarning(
		"warning",
		"legacy_config",
		legacyPath,
		fmt.Sprintf("Legacy %s is loaded for compatibility. Prefer .homecms.yml for new configuration and new features.", legacyPath),
	)}

	content, err := os.ReadFile(legacyCMSConfigPath(runtime))
	if err != nil {
		return append(warnings, configWarning("error", "config_read_failed", legacyPath, "Failed to read legacy CMS config: "+err.Error()))
	}
	var cfg models.CMSConfig
	if err := yaml.Unmarshal(content, &cfg); err != nil {
		return append(warnings, configWarning("error", "config_parse_failed", legacyPath, "Failed to parse legacy CMS config: "+err.Error()))
	}
	return append(warnings, validateCMSConfig(runtime, cfg, legacyCMSConfigFile)...)
}

func validateHomeCMSConfig(runtime config.SiteRuntime, home models.HomeCMSConfig) []ConfigWarning {
	cfg := models.CMSConfig{
		MediaFolder:  home.Media.Folder,
		PublicFolder: home.Media.PublicPath,
		Preview: models.CMSPreview{
			URLField: home.Preview.URLField,
		},
		Collections: make([]models.Collection, 0, len(home.Content.Collections)),
	}
	for _, collection := range home.Content.Collections {
		cfg.Collections = append(cfg.Collections, models.Collection{
			Name:         collection.Name,
			Label:        collection.Label,
			Folder:       collection.Folder,
			Path:         collection.Path,
			Extension:    collection.Extension,
			Format:       collection.FrontMatter,
			MediaFolder:  collection.MediaFolder,
			PublicFolder: collection.PublicFolder,
			Fields:       collection.Fields,
		})
	}
	return validateCMSConfig(runtime, cfg, homeCMSConfigFile)
}

func validateCMSConfig(runtime config.SiteRuntime, cfg models.CMSConfig, source string) []ConfigWarning {
	warnings := []ConfigWarning{}
	if len(cfg.Collections) == 0 {
		warnings = append(warnings, configWarning("error", "missing_collections", collectionRootPath(source), "CMS config should define at least one collection."))
	}

	previewField := strings.TrimSpace(cfg.Preview.URLField)
	previewFieldFound := previewField == ""

	for i, collection := range cfg.Collections {
		pathPrefix := collectionPath(source, i)
		fieldNames := map[string]bool{}
		for j, field := range collection.Fields {
			fieldPath := fmt.Sprintf("%s.fields[%d]", pathPrefix, j)
			name := strings.TrimSpace(field.Name)
			if name == "" {
				warnings = append(warnings, configWarning("warning", "missing_field_name", fieldPath+".name", "Field name is empty; the field cannot be saved reliably."))
				continue
			}
			fieldNames[name] = true
			if previewField != "" && name == previewField {
				previewFieldFound = true
			}
			if widget := strings.TrimSpace(field.Widget); widget != "" && !isSupportedWidget(widget) {
				warnings = append(warnings, configWarning("warning", "unsupported_widget", fieldPath+".widget", fmt.Sprintf("Widget %q is not explicitly supported by the editor and will be rendered as a string input.", widget)))
			}
		}

		if strings.TrimSpace(collection.Name) == "" {
			warnings = append(warnings, configWarning("error", "missing_collection_name", pathPrefix+".name", "Collection name is required."))
		}
		folder := strings.TrimSpace(collection.Folder)
		if folder == "" {
			warnings = append(warnings, configWarning("error", "missing_collection_folder", pathPrefix+".folder", "Collection folder is required."))
		} else if _, err := CollectionFolderWithinContentForRuntime(runtime, collection); err != nil {
			warnings = append(warnings, configWarning("error", "invalid_collection_folder", pathPrefix+".folder", fmt.Sprintf("Collection folder must be a safe relative path under %s.", runtime.ContentDir)))
		}

		if format := strings.TrimSpace(collection.Format); format != "" && !isSupportedFrontMatterFormat(format, source) {
			warnings = append(warnings, configWarning("warning", "unsupported_frontmatter", pathPrefix+".frontmatter", fmt.Sprintf("Front matter format %q is not supported; use yaml, toml, or json.", format)))
		}

		// Legacy collection media_folder is a Netlify/Sveltia path template and
		// is intentionally ignored by the Article Media resolver.
		if source != legacyCMSConfigFile {
			if mediaFolder := strings.TrimSpace(collection.MediaFolder); mediaFolder != "" && !isSafeArticleMediaFolder(mediaFolder) {
				warnings = append(warnings, configWarning("error", "invalid_media_folder", pathPrefix+".media_folder", "Collection media_folder must stay within the article directory and collection."))
			}
		}

		warnings = append(warnings, validatePathTemplate(source, pathPrefix+".path", collection.Path, fieldNames)...)
	}

	if previewField != "" && !previewFieldFound {
		warnings = append(warnings, configWarning("warning", "unknown_preview_url_field", previewPath(source)+".url_field", fmt.Sprintf("preview.url_field %q is not defined in any collection fields; previews may fall back to file paths.", previewField)))
	}

	mediaFolder := strings.TrimSpace(cfg.MediaFolder)
	if mediaFolder != "" {
		valid := cleanConfigPath(mediaFolder) != ""
		if source == legacyCMSConfigFile {
			valid = cleanMediaRepoRelDir(mediaFolder) != ""
		}
		if !valid {
			warnings = append(warnings, configWarning("error", "invalid_media_folder", mediaFolderPath(source), "Media folder must be a safe repository-relative path."))
		}
	}

	return warnings
}

func isSafeArticleMediaFolder(folder string) bool {
	folder = strings.TrimSpace(filepath.ToSlash(folder))
	if folder == "" || filepath.IsAbs(folder) || strings.HasPrefix(folder, "/") || strings.HasPrefix(folder, `\`) || strings.Contains(folder, ":") {
		return false
	}
	folder = strings.ReplaceAll(folder, "{{dirname}}", "")
	folder = strings.TrimLeft(strings.TrimSpace(folder), "/")
	if strings.Contains(folder, "{{") || strings.Contains(folder, "}}") {
		return false
	}
	return folder == "" || cleanConfigPath(folder) != ""
}

func validatePathTemplate(source, path, template string, fieldNames map[string]bool) []ConfigWarning {
	if strings.TrimSpace(template) == "" {
		template = "{{slug}}"
	}

	warnings := []ConfigWarning{}
	for _, variable := range pathTemplateVariables(template) {
		if isBuiltInPathVariable(variable) || fieldNames[variable] {
			continue
		}
		if variable == "slug" {
			warnings = append(warnings, configWarning("error", "missing_slug_field", path, "Path template uses {{slug}}, but the collection does not define a slug field. Add a slug field or change the path template."))
			continue
		}
		warnings = append(warnings, configWarning("warning", "unknown_path_variable", path, fmt.Sprintf("Path template uses {{%s}}, but no matching field is defined.", variable)))
	}
	return warnings
}

func pathTemplateVariables(template string) []string {
	re := regexp.MustCompile(`{{([^}]+)}}`)
	matches := re.FindAllStringSubmatch(template, -1)
	var variables []string
	seen := map[string]bool{}
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		variable := strings.TrimSpace(match[1])
		if variable == "" || seen[variable] {
			continue
		}
		seen[variable] = true
		variables = append(variables, variable)
	}
	return variables
}

func isBuiltInPathVariable(variable string) bool {
	switch variable {
	case "year", "month", "day", "hour", "minute", "second":
		return true
	default:
		return false
	}
}

func isSupportedWidget(widget string) bool {
	switch strings.ToLower(strings.TrimSpace(widget)) {
	case "string", "text", "markdown", "boolean", "datetime", "list", "number", "select":
		return true
	default:
		return false
	}
}

func isSupportedFrontMatterFormat(format, source string) bool {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if source == homeCMSConfigFile {
		switch normalized {
		case "yaml", "yml", "toml", "json":
			return true
		default:
			return false
		}
	}
	switch normalized {
	case "yaml", "yml", "toml", "json", "yaml-frontmatter", "toml-frontmatter", "json-frontmatter":
		return true
	default:
		return false
	}
}

func collectionRootPath(source string) string {
	if source == homeCMSConfigFile {
		return "content.collections"
	}
	return "collections"
}

func collectionPath(source string, index int) string {
	if source == homeCMSConfigFile {
		return fmt.Sprintf("content.collections[%d]", index)
	}
	return fmt.Sprintf("collections[%d]", index)
}

func previewPath(source string) string {
	if source == homeCMSConfigFile {
		return "preview"
	}
	return "preview"
}

func mediaFolderPath(source string) string {
	if source == homeCMSConfigFile {
		return "media.folder"
	}
	return "media_folder"
}

func configWarning(severity, code, path, message string) ConfigWarning {
	return ConfigWarning{
		Severity: severity,
		Code:     code,
		Path:     path,
		Message:  message,
	}
}
