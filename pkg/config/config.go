package config

import (
	"fmt"
	"log/slog"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"gopkg.in/yaml.v3"
)

var (
	RepoPath         = "./repo"
	PublicPath       = "./repo/public"
	PreviewURL       = "/"
	ContentDir       = "content"
	StaticDir        = "static"
	PublicDir        = "public"
	SiteGenerator    = "hugo"
	GeneratorRuntime = "direct"
	DefaultSiteID    = "default"
	SitesConfigPath  = ""
	Sites            = []SiteConfig{}

	// Hugo Server settings (legacy path-prefix preview settings; retained for compatibility)
	HugoServerPort = "1314"
	HugoServerBind = "127.0.0.1"

	ServerPort = "8080"

	// Cache settings
	CacheConcurrency  = 20
	FileReadHeadLimit = int64(4096)

	// Media settings
	ArticleMediaDir = ""
	StaticMediaDir  = ""
	MaxUploadSize   = int64(10 * 1024 * 1024) // 10MB default

	// Git settings
	GitUserEmail = "bot@homecms.local"
	GitUserName  = "HomeCMS Bot"
	GitBranch    = "main"
	GitRemote    = "origin"

	// Preview settings
	MarkdownPreviewEnabled           = true
	LocalLivePreviewEnabled          = false
	PreviewDomain                    = ""
	PreviewScheme                    = "https"
	PreviewDeploymentProvider        = ""
	CloudflarePagesAccountID         = ""
	CloudflarePagesProjectName       = ""
	CloudflarePagesAPITokenEnv       = "CLOUDFLARE_API_TOKEN"
	PreviewDeploymentAccessProtected = false

	// Git timeout settings (generous for large repos/slow networks)
	GitCommandTimeout = 60 * time.Second // Local git commands (status, diff, etc.)
	GitNetworkTimeout = 5 * time.Minute  // Network operations (push, pull)

	// Security settings
	AllowedGitHubUsers  = []string{}
	AllowAllGitHubUsers = false
	GitHubOAuthScopes   = []string{"public_repo"} // Default to public_repo only

	// Snippet settings
	SnippetPaths = []string{}
)

var OauthConf *oauth2.Config

type SiteConfig struct {
	ID              string            `yaml:"id" json:"id"`
	Name            string            `yaml:"name" json:"name"`
	RepoPath        string            `yaml:"repo_path" json:"repo_path"`
	Generator       string            `yaml:"generator" json:"generator"`
	Runtime         string            `yaml:"runtime" json:"runtime"`
	ContentDir      string            `yaml:"content_dir" json:"content_dir"`
	StaticDir       string            `yaml:"static_dir" json:"static_dir"`
	PublicDir       string            `yaml:"public_dir" json:"public_dir"`
	PreviewURL      string            `yaml:"preview_url" json:"preview_url"`
	HugoServerPort  string            `yaml:"hugo_server_port" json:"hugo_server_port"`
	HugoServerBind  string            `yaml:"hugo_server_bind" json:"hugo_server_bind"`
	ArticleMediaDir string            `yaml:"article_media_dir" json:"article_media_dir"`
	StaticMediaDir  string            `yaml:"static_media_dir" json:"static_media_dir"`
	SnippetPaths    []string          `yaml:"snippet_paths" json:"snippet_paths"`
	Preview         SitePreviewConfig `yaml:"preview" json:"preview"`
}

type SitePreviewConfig struct {
	Markdown     MarkdownPreviewConfig   `yaml:"markdown" json:"markdown"`
	LocalPreview LocalPreviewConfig      `yaml:"local_preview" json:"local_preview"`
	Deployment   DeploymentPreviewConfig `yaml:"deployment" json:"deployment"`
}

type MarkdownPreviewConfig struct {
	// A pointer preserves the distinction between an omitted value (enabled by
	// default) and an explicit false value in the site registry.
	Enabled *bool `yaml:"enabled" json:"enabled,omitempty"`
}

type LocalPreviewConfig struct {
	// Enabled inherits LOCAL_LIVE_PREVIEW_ENABLED when omitted from a registry
	// entry. URL is derived from site ID, PREVIEW_SCHEME and PREVIEW_DOMAIN and
	// is never persisted in sites.yml.
	Enabled  *bool                     `yaml:"enabled" json:"enabled,omitempty"`
	URL      string                    `yaml:"-" json:"url,omitempty"`
	AlwaysOn bool                      `yaml:"always_on" json:"always_on,omitempty"`
	Refresh  LocalPreviewRefreshConfig `yaml:"refresh" json:"refresh,omitempty"`
}

// LocalPreviewRefreshConfig describes daily generator refreshes. An omitted
// timezone is normalized to UTC so the effective schedule is explicit in the
// status API and does not depend on the host timezone.
type LocalPreviewRefreshConfig struct {
	Times    []string `yaml:"times" json:"times,omitempty"`
	Timezone string   `yaml:"timezone" json:"timezone,omitempty"`
}

type DeploymentPreviewConfig struct {
	Provider        string                `yaml:"provider" json:"provider"`
	CloudflarePages CloudflarePagesConfig `yaml:"cloudflare_pages" json:"cloudflare_pages"`
	AccessProtected bool                  `yaml:"access_protected" json:"access_protected"`
}

type CloudflarePagesConfig struct {
	AccountID   string `yaml:"account_id" json:"account_id"`
	ProjectName string `yaml:"project_name" json:"project_name"`
	// APITokenEnv is the name of the environment variable containing the API
	// token. The token itself is never accepted in YAML or exposed as JSON.
	APITokenEnv string `yaml:"token_env" json:"-"`
}

type SiteRegistryConfig struct {
	DefaultSite string       `yaml:"default_site"`
	Sites       []SiteConfig `yaml:"sites"`
}

func Init() error {
	if err := godotenv.Load(); err != nil {
		slog.Info("No .env file found or error loading", "error", err)
	}
	for _, names := range [][2]string{
		{"HOMECMS_UID", "HUGO_CMS_UID"},
		{"HOMECMS_GID", "HUGO_CMS_GID"},
		{"HOMECMS_HOST_PORT", "HUGO_CMS_HOST_PORT"},
		{"HOMECMS_REPOS", "HUGO_CMS_REPOS"},
	} {
		if os.Getenv(names[0]) == "" && os.Getenv(names[1]) != "" {
			slog.Warn("Deprecated environment variable is in use", "legacy", names[1], "replacement", names[0])
		}
	}

	// Helper to get env with default
	getEnv := func(key, fallback string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return fallback
	}
	appURL := getEnv("APP_URL", "http://localhost:8080")
	redirectURL := getEnv("GITHUB_REDIRECT_URL", appURL+"/admin/auth/callback")

	// Load Configs
	RepoPath = getEnv("REPO_PATH", "./repo")
	ContentDir = cleanRelativeDir(getEnv("CONTENT_DIR", "content"), "content")
	StaticDir = cleanRelativeDir(getEnv("STATIC_DIR", "static"), "static")
	PublicDir = cleanRelativeDir(getEnv("PUBLIC_DIR", "public"), "public")
	PublicPath = getEnv("PUBLIC_PATH", filepath.Join(RepoPath, PublicDir))
	PreviewURL = getEnv("PREVIEW_URL", "/")
	SiteGenerator = strings.ToLower(getEnv("SITE_GENERATOR", "hugo"))
	GeneratorRuntime = cleanGeneratorRuntime(getEnv("GENERATOR_RUNTIME", "direct"))
	DefaultSiteID = getEnv("DEFAULT_SITE_ID", "default")
	SitesConfigPath = getEnv("SITES_CONFIG_PATH", "")

	HugoServerPort = getEnv("HUGO_SERVER_PORT", "1314")
	HugoServerBind = getEnv("HUGO_SERVER_BIND", "127.0.0.1")

	ServerPort = getEnv("PORT", "8080")

	ArticleMediaDir = cleanOptionalRelativeDir(getEnv("ARTICLE_MEDIA_DIR", ""))
	StaticMediaDir = cleanOptionalRelativeDir(getEnv("STATIC_MEDIA_DIR", ""))

	// Max upload size (default 10MB)
	if maxSize := os.Getenv("MAX_UPLOAD_SIZE_MB"); maxSize != "" {
		if val, err := strconv.ParseInt(maxSize, 10, 64); err == nil && val > 0 && val <= 1024 {
			MaxUploadSize = val * 1024 * 1024
		} else {
			slog.Warn("Invalid MAX_UPLOAD_SIZE_MB value; using existing default", "value", maxSize)
		}
	}

	GitUserEmail = getEnv("GIT_USER_EMAIL", "bot@homecms.local")
	GitUserName = getEnv("GIT_USER_NAME", "HomeCMS Bot")
	GitBranch = getEnv("GIT_BRANCH", "main")
	GitRemote = getEnv("GIT_REMOTE", "origin")

	MarkdownPreviewEnabled = true
	if markdownEnabled := os.Getenv("MARKDOWN_PREVIEW_ENABLED"); markdownEnabled != "" {
		value, err := strconv.ParseBool(markdownEnabled)
		if err != nil {
			slog.Warn("Invalid MARKDOWN_PREVIEW_ENABLED value; defaulting to true", "value", markdownEnabled)
		} else {
			MarkdownPreviewEnabled = value
		}
	}

	LocalLivePreviewEnabled = false
	if localEnabled := os.Getenv("LOCAL_LIVE_PREVIEW_ENABLED"); localEnabled != "" {
		value, err := strconv.ParseBool(localEnabled)
		if err != nil {
			slog.Warn("Invalid LOCAL_LIVE_PREVIEW_ENABLED value; defaulting to false", "value", localEnabled)
		} else {
			LocalLivePreviewEnabled = value
		}
	}
	PreviewDomain = normalizePreviewDomain(getEnv("PREVIEW_DOMAIN", ""))
	PreviewScheme = normalizePreviewScheme(getEnv("PREVIEW_SCHEME", "https"))
	if err := validateLocalPreviewBaseSettings(LocalLivePreviewEnabled); err != nil {
		return err
	}

	PreviewDeploymentProvider = strings.ToLower(strings.TrimSpace(getEnv("PREVIEW_DEPLOYMENT_PROVIDER", "")))
	CloudflarePagesAccountID = strings.TrimSpace(getEnv("CLOUDFLARE_PAGES_ACCOUNT_ID", ""))
	CloudflarePagesProjectName = strings.TrimSpace(getEnv("CLOUDFLARE_PAGES_PROJECT_NAME", ""))
	CloudflarePagesAPITokenEnv = strings.TrimSpace(getEnv("CLOUDFLARE_PAGES_API_TOKEN_ENV", "CLOUDFLARE_API_TOKEN"))
	PreviewDeploymentAccessProtected = false
	if protected := os.Getenv("PREVIEW_DEPLOYMENT_ACCESS_PROTECTED"); protected != "" {
		value, err := strconv.ParseBool(protected)
		if err != nil {
			slog.Warn("Invalid PREVIEW_DEPLOYMENT_ACCESS_PROTECTED value; defaulting to false", "value", protected)
		} else {
			PreviewDeploymentAccessProtected = value
		}
	}

	// Security settings
	AllowedGitHubUsers = nil
	AllowAllGitHubUsers = false
	if users := os.Getenv("ALLOWED_GITHUB_USERS"); users != "" {
		AllowedGitHubUsers = splitAndTrim(users, ",")
	}
	if allowAll := os.Getenv("ALLOW_ALL_GITHUB_USERS"); allowAll != "" {
		val, err := strconv.ParseBool(allowAll)
		if err != nil {
			slog.Warn("Invalid ALLOW_ALL_GITHUB_USERS value; defaulting to false", "value", allowAll)
		} else {
			AllowAllGitHubUsers = val
		}
	}
	if cc := os.Getenv("CACHE_CONCURRENCY"); cc != "" {
		if val, err := strconv.Atoi(cc); err == nil && val >= 1 && val <= 256 {
			CacheConcurrency = val
		} else {
			slog.Warn("Invalid CACHE_CONCURRENCY value; using existing default", "value", cc)
		}
	}

	if snippets := os.Getenv("SNIPPET_PATHS"); snippets != "" {
		SnippetPaths = splitAndTrim(snippets, ",")
	} else {
		SnippetPaths = defaultSnippetPaths(RepoPath)
	}
	if err := loadSiteRegistry(); err != nil {
		return err
	}

	// OAuth scopes configuration
	// Use GITHUB_OAUTH_SCOPES env var to override (comma-separated)
	// Options: public_repo (public only), repo (public + private)
	if scopes := os.Getenv("GITHUB_OAUTH_SCOPES"); scopes != "" {
		GitHubOAuthScopes = splitAndTrim(scopes, ",")
	}

	OauthConf = &oauth2.Config{
		ClientID:     os.Getenv("GITHUB_CLIENT_ID"),
		ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
		Scopes:       GitHubOAuthScopes,
		Endpoint:     github.Endpoint,
		RedirectURL:  redirectURL,
	}
	return nil
}

func loadSiteRegistry() error {
	defaultSite := defaultSiteFromGlobals()
	if err := validateSitePreviewConfig(defaultSite); err != nil {
		return err
	}
	Sites = []SiteConfig{defaultSite}
	if SitesConfigPath == "" {
		return nil
	}

	content, err := os.ReadFile(SitesConfigPath)
	if err != nil {
		return fmt.Errorf("read site registry config %q: %w", SitesConfigPath, err)
	}

	var registry SiteRegistryConfig
	decoder := yaml.NewDecoder(strings.NewReader(string(content)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&registry); err != nil {
		return fmt.Errorf("parse site registry config %q: %w", SitesConfigPath, err)
	}

	normalized := make([]SiteConfig, 0, len(registry.Sites))
	seenSiteIDs := make(map[string]int, len(registry.Sites))
	for index, site := range registry.Sites {
		if err := validateSiteRegistryEntry(site, index); err != nil {
			return fmt.Errorf("site registry config %q: %w", SitesConfigPath, err)
		}
		site = normalizeSiteConfig(site)
		if site.ID == "" {
			return fmt.Errorf("site registry config %q: sites[%d].id is required", SitesConfigPath, index)
		}
		if previousIndex, exists := seenSiteIDs[site.ID]; exists {
			return fmt.Errorf("site registry config %q: duplicate site id %q at sites[%d] and sites[%d]", SitesConfigPath, site.ID, previousIndex, index)
		}
		seenSiteIDs[site.ID] = index
		if err := validateSitePreviewConfig(site); err != nil {
			return fmt.Errorf("site %q: %w", site.ID, err)
		}
		normalized = append(normalized, site)
	}
	if len(normalized) == 0 {
		return fmt.Errorf("site registry config %q contains no usable sites", SitesConfigPath)
	}
	Sites = normalized
	if strings.TrimSpace(registry.DefaultSite) != "" {
		DefaultSiteID = strings.TrimSpace(registry.DefaultSite)
	}

	defaultSite, ok := GetSite(DefaultSiteID)
	if !ok {
		return fmt.Errorf("default site %q is not defined in site registry %q", DefaultSiteID, SitesConfigPath)
	}
	applyDefaultSite(defaultSite)
	return nil
}

func validateSiteRegistryEntry(site SiteConfig, index int) error {
	path := fmt.Sprintf("sites[%d]", index)
	if strings.TrimSpace(site.ID) == "" {
		return fmt.Errorf("%s.id is required", path)
	}
	if strings.TrimSpace(site.RepoPath) == "" {
		return fmt.Errorf("%s.repo_path is required for site %q", path, strings.TrimSpace(site.ID))
	}

	switch generator := strings.ToLower(strings.TrimSpace(site.Generator)); generator {
	case "", "hugo", "eleventy", "11ty":
	default:
		return fmt.Errorf("%s.generator has unsupported value %q for site %q", path, site.Generator, strings.TrimSpace(site.ID))
	}
	switch runtime := strings.ToLower(strings.TrimSpace(site.Runtime)); runtime {
	case "", "direct", "mise":
	default:
		return fmt.Errorf("%s.runtime has unsupported value %q for site %q", path, site.Runtime, strings.TrimSpace(site.ID))
	}

	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "content_dir", value: site.ContentDir},
		{name: "static_dir", value: site.StaticDir},
		{name: "public_dir", value: site.PublicDir},
		{name: "article_media_dir", value: site.ArticleMediaDir},
		{name: "static_media_dir", value: site.StaticMediaDir},
	} {
		if err := validateRegistryRelativeDir(path+"."+field.name, field.value); err != nil {
			return err
		}
	}
	if port := strings.TrimSpace(site.HugoServerPort); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return fmt.Errorf("%s.hugo_server_port has invalid value %q; expected 1-65535", path, site.HugoServerPort)
		}
	}
	return nil
}

func validateRegistryRelativeDir(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	isWindowsAbsolute := len(value) >= 3 && ((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) && value[1] == ':' && (value[2] == '/' || value[2] == '\\')
	if strings.ContainsRune(value, '\x00') || filepath.IsAbs(value) || isWindowsAbsolute || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return fmt.Errorf("%s must be a repository-relative path, got %q", field, value)
	}
	cleaned := pathpkg.Clean(strings.ReplaceAll(value, `\`, "/"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("%s must stay within the repository, got %q", field, value)
	}
	return nil
}

func defaultSiteFromGlobals() SiteConfig {
	return normalizeSiteConfig(SiteConfig{
		ID:              DefaultSiteID,
		Name:            "Default",
		RepoPath:        RepoPath,
		Generator:       SiteGenerator,
		Runtime:         GeneratorRuntime,
		ContentDir:      ContentDir,
		StaticDir:       StaticDir,
		PublicDir:       PublicDir,
		PreviewURL:      PreviewURL,
		HugoServerPort:  HugoServerPort,
		HugoServerBind:  HugoServerBind,
		ArticleMediaDir: ArticleMediaDir,
		StaticMediaDir:  StaticMediaDir,
		SnippetPaths:    SnippetPaths,
		Preview: SitePreviewConfig{
			Markdown:     MarkdownPreviewConfig{Enabled: boolPointer(MarkdownPreviewEnabled)},
			LocalPreview: LocalPreviewConfig{Enabled: boolPointer(LocalLivePreviewEnabled)},
			Deployment: DeploymentPreviewConfig{
				Provider: PreviewDeploymentProvider,
				CloudflarePages: CloudflarePagesConfig{
					AccountID:   CloudflarePagesAccountID,
					ProjectName: CloudflarePagesProjectName,
					APITokenEnv: CloudflarePagesAPITokenEnv,
				},
				AccessProtected: PreviewDeploymentAccessProtected,
			},
		},
	})
}

func normalizeSiteConfig(site SiteConfig) SiteConfig {
	site.ID = strings.TrimSpace(site.ID)
	site.Name = strings.TrimSpace(site.Name)
	site.RepoPath = strings.TrimSpace(site.RepoPath)
	site.Generator = strings.ToLower(strings.TrimSpace(site.Generator))
	site.Runtime = cleanGeneratorRuntime(site.Runtime)
	site.ContentDir = cleanRelativeDir(site.ContentDir, "content")
	site.StaticDir = cleanRelativeDir(site.StaticDir, "static")
	site.PublicDir = cleanRelativeDir(site.PublicDir, "public")
	site.PreviewURL = strings.TrimSpace(site.PreviewURL)
	site.HugoServerPort = strings.TrimSpace(site.HugoServerPort)
	site.HugoServerBind = strings.TrimSpace(site.HugoServerBind)
	site.ArticleMediaDir = cleanOptionalRelativeDir(site.ArticleMediaDir)
	site.StaticMediaDir = cleanOptionalRelativeDir(site.StaticMediaDir)
	site.Preview.Deployment.Provider = strings.ToLower(strings.TrimSpace(site.Preview.Deployment.Provider))
	site.Preview.Deployment.CloudflarePages.AccountID = strings.TrimSpace(site.Preview.Deployment.CloudflarePages.AccountID)
	site.Preview.Deployment.CloudflarePages.ProjectName = strings.TrimSpace(site.Preview.Deployment.CloudflarePages.ProjectName)
	site.Preview.Deployment.CloudflarePages.APITokenEnv = strings.TrimSpace(site.Preview.Deployment.CloudflarePages.APITokenEnv)
	site.Preview.LocalPreview = normalizeLocalPreviewConfig(site.Preview.LocalPreview)

	if site.Name == "" {
		site.Name = site.ID
	}
	if site.RepoPath == "" {
		site.RepoPath = "./repo"
	}
	if site.Generator == "" {
		site.Generator = "hugo"
	}
	if site.Runtime == "" {
		site.Runtime = GeneratorRuntime
	}
	if site.PreviewURL == "" {
		site.PreviewURL = "/"
	}
	if site.HugoServerPort == "" {
		site.HugoServerPort = "1314"
	}
	if site.HugoServerBind == "" {
		site.HugoServerBind = "127.0.0.1"
	}
	if site.Preview.Markdown.Enabled == nil {
		site.Preview.Markdown.Enabled = boolPointer(true)
	}
	if site.Preview.LocalPreview.Enabled == nil {
		site.Preview.LocalPreview.Enabled = boolPointer(LocalLivePreviewEnabled)
	}
	site.Preview.LocalPreview.URL = ""
	if site.Preview.LocalPreview.Enabled != nil && *site.Preview.LocalPreview.Enabled {
		if previewURL, err := LocalPreviewURL(site.ID); err == nil {
			site.Preview.LocalPreview.URL = previewURL
		}
	}
	if site.Preview.Deployment.Provider == "cloudflare_pages" && site.Preview.Deployment.CloudflarePages.APITokenEnv == "" {
		site.Preview.Deployment.CloudflarePages.APITokenEnv = "CLOUDFLARE_API_TOKEN"
	}
	site.SnippetPaths = normalizeSnippetPaths(site.SnippetPaths, site.RepoPath)
	return site
}

func validateSitePreviewConfig(site SiteConfig) error {
	if err := validateLocalPreviewRefreshConfig(site.Preview.LocalPreview.Refresh); err != nil {
		return err
	}
	if site.Preview.LocalPreview.Enabled != nil && *site.Preview.LocalPreview.Enabled {
		if err := validateLocalPreviewSite(site); err != nil {
			return err
		}
	}

	deployment := site.Preview.Deployment
	switch deployment.Provider {
	case "":
		return nil
	case "cloudflare_pages":
		if deployment.CloudflarePages.AccountID == "" {
			return fmt.Errorf("preview deployment cloudflare_pages.account_id is required")
		}
		if deployment.CloudflarePages.ProjectName == "" {
			return fmt.Errorf("preview deployment cloudflare_pages.project_name is required")
		}
		if deployment.CloudflarePages.APITokenEnv == "" {
			return fmt.Errorf("preview deployment cloudflare_pages.token_env is required")
		}
		return nil
	default:
		return fmt.Errorf("unsupported preview deployment provider %q", deployment.Provider)
	}
}

func normalizeLocalPreviewConfig(preview LocalPreviewConfig) LocalPreviewConfig {
	times := make([]string, 0, len(preview.Refresh.Times))
	for _, value := range preview.Refresh.Times {
		value = strings.TrimSpace(value)
		if value != "" {
			times = append(times, value)
		}
	}
	sort.Strings(times)
	preview.Refresh.Times = times
	preview.Refresh.Timezone = strings.TrimSpace(preview.Refresh.Timezone)
	if len(times) > 0 && preview.Refresh.Timezone == "" {
		preview.Refresh.Timezone = "UTC"
	}
	return preview
}

func cloneLocalPreviewConfig(preview LocalPreviewConfig) LocalPreviewConfig {
	preview.Refresh.Times = append([]string(nil), preview.Refresh.Times...)
	return preview
}

func validateLocalPreviewRefreshConfig(refresh LocalPreviewRefreshConfig) error {
	if refresh.Timezone != "" {
		if _, err := time.LoadLocation(refresh.Timezone); err != nil {
			return fmt.Errorf("preview.local_preview.refresh.timezone %q is not a valid IANA timezone", refresh.Timezone)
		}
	}
	seen := make(map[string]struct{}, len(refresh.Times))
	for _, value := range refresh.Times {
		if len(value) != len("15:04") || value[2] != ':' {
			return fmt.Errorf("preview.local_preview.refresh.times value %q must use HH:MM format", value)
		}
		if _, err := time.Parse("15:04", value); err != nil {
			return fmt.Errorf("preview.local_preview.refresh.times value %q must use HH:MM format", value)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("preview.local_preview.refresh.times contains duplicate value %q", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func boolPointer(value bool) *bool {
	return &value
}

func defaultSnippetPaths(repoPath string) []string {
	return []string{filepath.Clean(filepath.Join(repoPath, ".vscode", "md.code-snippets"))}
}

func normalizeSnippetPaths(paths []string, repoPath string) []string {
	if len(paths) == 0 {
		return defaultSnippetPaths(repoPath)
	}

	normalized := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if filepath.IsAbs(path) {
			normalized = append(normalized, path)
			continue
		}
		if relativePathWithinRepo(path, repoPath) {
			normalized = append(normalized, path)
			continue
		}
		normalized = append(normalized, filepath.Join(repoPath, path))
	}
	if len(normalized) == 0 {
		return defaultSnippetPaths(repoPath)
	}
	return normalized
}

func relativePathWithinRepo(path, repoPath string) bool {
	if filepath.IsAbs(path) || filepath.IsAbs(repoPath) {
		return false
	}
	cleanPath := filepath.Clean(path)
	cleanRepo := filepath.Clean(repoPath)
	return cleanPath == cleanRepo || strings.HasPrefix(cleanPath, cleanRepo+string(os.PathSeparator))
}

func cleanRelativeDir(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return fallback
	}
	cleaned := filepath.ToSlash(filepath.Clean(value))
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return fallback
	}
	return cleaned
}

func cleanOptionalRelativeDir(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) || strings.HasPrefix(value, "/") || strings.HasPrefix(value, `\`) {
		return ""
	}
	cleaned := filepath.ToSlash(filepath.Clean(value))
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || cleaned == ".." {
		return ""
	}
	return cleaned
}

func cleanGeneratorRuntime(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "direct", "mise":
		return value
	default:
		slog.Warn("Invalid generator runtime; using direct execution", "value", value)
		return "direct"
	}
}

func GetSite(id string) (SiteConfig, bool) {
	id = strings.TrimSpace(id)
	for _, site := range Sites {
		if site.ID == id {
			return site, true
		}
	}
	return SiteConfig{}, false
}

func applyDefaultSite(site SiteConfig) {
	ApplySiteRuntime(site)
}

func RuntimeSiteConfig() SiteConfig {
	return CurrentSiteRuntime().SiteConfig()
}

func ApplySiteRuntime(site SiteConfig) {
	ApplyRuntime(NewSiteRuntime(site))
}

func ApplyRuntime(runtime SiteRuntime) {
	RepoPath = runtime.RepoPath
	SiteGenerator = runtime.Generator
	GeneratorRuntime = runtime.Runtime
	ContentDir = runtime.ContentDir
	StaticDir = runtime.StaticDir
	PublicDir = runtime.PublicDir
	PublicPath = runtime.PublicPath
	PreviewURL = runtime.PreviewURL
	HugoServerPort = runtime.HugoServerPort
	HugoServerBind = runtime.HugoServerBind
	ArticleMediaDir = runtime.ArticleMediaDir
	StaticMediaDir = runtime.StaticMediaDir
	SnippetPaths = append([]string(nil), runtime.SnippetPaths...)
	MarkdownPreviewEnabled = runtime.MarkdownPreviewEnabled
	LocalLivePreviewEnabled = runtime.LocalPreview.Enabled != nil && *runtime.LocalPreview.Enabled
	PreviewDeploymentProvider = runtime.PreviewDeployment.Provider
	CloudflarePagesAccountID = runtime.PreviewDeployment.CloudflarePages.AccountID
	CloudflarePagesProjectName = runtime.PreviewDeployment.CloudflarePages.ProjectName
	CloudflarePagesAPITokenEnv = runtime.PreviewDeployment.CloudflarePages.APITokenEnv
	PreviewDeploymentAccessProtected = runtime.PreviewDeployment.AccessProtected
}

// ValidateSecurityConfig rejects insecure authorization defaults.
func ValidateSecurityConfig() error {
	if AllowAllGitHubUsers && strings.EqualFold(strings.TrimSpace(os.Getenv("GIN_MODE")), "release") {
		return fmt.Errorf("ALLOW_ALL_GITHUB_USERS cannot be enabled when GIN_MODE=release")
	}
	if len(AllowedGitHubUsers) == 0 && !AllowAllGitHubUsers {
		return fmt.Errorf("ALLOWED_GITHUB_USERS must contain at least one user; set ALLOW_ALL_GITHUB_USERS=true only for explicit development use")
	}
	return nil
}

func GetAppURL() string {
	appURL := os.Getenv("APP_URL")
	if appURL == "" {
		appURL = "http://localhost:8080"
	}
	return appURL
}

// splitAndTrim splits a string by separator and trims whitespace from each element
func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

// IsUserAllowed checks if a GitHub username is in the allowed list
func IsUserAllowed(username string) bool {
	if len(AllowedGitHubUsers) == 0 {
		return AllowAllGitHubUsers
	}
	for _, u := range AllowedGitHubUsers {
		if strings.EqualFold(u, username) {
			return true
		}
	}
	return false
}
