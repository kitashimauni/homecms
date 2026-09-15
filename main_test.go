package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestSetupRouterRejectsShortSessionSecret(t *testing.T) {
	originalMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(originalMode) })
	t.Setenv("GIN_MODE", "")
	t.Setenv("SESSION_SECRET", "too-short")

	_, err := SetupRouter()
	if err == nil || !strings.Contains(err.Error(), "at least 32") {
		t.Fatalf("SetupRouter() error = %v, want minimum-length error", err)
	}
}

func TestSetupRouterRequiresSessionSecretInReleaseMode(t *testing.T) {
	originalMode := gin.Mode()
	gin.SetMode(gin.ReleaseMode)
	t.Cleanup(func() { gin.SetMode(originalMode) })
	t.Setenv("GIN_MODE", "")
	t.Setenv("SESSION_SECRET", "")

	_, err := SetupRouter()
	if err == nil || !strings.Contains(err.Error(), "required in release mode") {
		t.Fatalf("SetupRouter() error = %v, want required-secret error", err)
	}
}

func TestSetupRouterAcceptsStrongSessionSecret(t *testing.T) {
	originalMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(originalMode) })
	t.Setenv("GIN_MODE", "")
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 32))

	router, err := SetupRouter()
	if err != nil {
		t.Fatalf("SetupRouter() unexpected error: %v", err)
	}
	if router == nil {
		t.Fatal("SetupRouter() returned a nil router")
	}
}

func TestAdminSecurityHeaders(t *testing.T) {
	originalMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(originalMode) })
	t.Setenv("GIN_MODE", "")
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 32))

	router, err := SetupRouter()
	if err != nil {
		t.Fatalf("SetupRouter() unexpected error: %v", err)
	}

	loginRecorder := httptest.NewRecorder()
	router.ServeHTTP(loginRecorder, httptest.NewRequest(http.MethodGet, "/admin/login", nil))
	if loginRecorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("admin response is missing X-Content-Type-Options")
	}
	if loginRecorder.Header().Get("X-Frame-Options") != "SAMEORIGIN" {
		t.Error("admin response is missing X-Frame-Options")
	}

}

func TestMarkdownPreviewDoesNotUseIframe(t *testing.T) {
	content, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read admin template: %v", err)
	}
	template := string(content)
	if !strings.Contains(template, `id="markdown-preview"`) {
		t.Fatal("template is missing the Markdown preview container")
	}
	previewStart := strings.Index(template, `<div id="preview-view">`)
	if previewStart < 0 {
		t.Fatal("template is missing the Markdown preview view")
	}
	previewEnd := strings.Index(template[previewStart:], `<div id="modal-overlay">`)
	if previewEnd < 0 {
		t.Fatal("template is missing the Markdown preview view")
	}
	markdownPreview := template[previewStart : previewStart+previewEnd]
	if strings.Contains(markdownPreview, "<iframe") {
		t.Fatal("Markdown preview must not use an iframe")
	}
}

func TestLocalPreviewProvidesEmbeddedSurfaceAndFallback(t *testing.T) {
	content, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read admin template: %v", err)
	}
	template := string(content)
	for _, marker := range []string{
		`id="local-preview-view"`,
		`id="local-preview-frame"`,
		`id="local-preview-frame-loading"`,
		`id="local-preview-frame-error"`,
		`onclick="openLocalLivePreview()"`,
	} {
		if !strings.Contains(template, marker) {
			t.Fatalf("Local Preview template is missing %s", marker)
		}
	}
}

func TestMobileEditorUsesDynamicViewportAndSafeArea(t *testing.T) {
	templateContent, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatalf("read admin template: %v", err)
	}
	template := string(templateContent)
	if !strings.Contains(template, `viewport-fit=cover`) || !strings.Contains(template, `interactive-widget=resizes-content`) {
		t.Fatal("mobile editor viewport metadata is missing")
	}

	styleContent, err := os.ReadFile("static/css/style.css")
	if err != nil {
		t.Fatalf("read admin stylesheet: %v", err)
	}
	style := string(styleContent)
	for _, marker := range []string{
		"height: 100dvh",
		"padding-bottom: env(safe-area-inset-bottom, 0px)",
		"#editor {",
		"scroll-padding-bottom: calc(20px + env(safe-area-inset-bottom, 0px))",
	} {
		if !strings.Contains(style, marker) {
			t.Fatalf("mobile editor stylesheet is missing %s", marker)
		}
	}
}

func TestHTTPServerDoesNotCapRequestBodyDuration(t *testing.T) {
	server := newHTTPServer(http.NotFoundHandler())

	if server.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v, want %v", server.ReadHeaderTimeout, 10*time.Second)
	}
	if server.ReadTimeout != 0 {
		t.Fatalf("ReadTimeout = %v, want no global request body deadline", server.ReadTimeout)
	}
	if server.WriteTimeout != 0 {
		t.Fatalf("WriteTimeout = %v, want no global deadline that expires during uploads", server.WriteTimeout)
	}
}

func TestSetupRouterHonorsReleaseModeFromEnvironment(t *testing.T) {
	originalMode := gin.Mode()
	gin.SetMode(gin.DebugMode)
	t.Cleanup(func() { gin.SetMode(originalMode) })
	t.Setenv("GIN_MODE", gin.ReleaseMode)
	t.Setenv("SESSION_SECRET", "")

	_, err := SetupRouter()
	if err == nil || !strings.Contains(err.Error(), "required in release mode") {
		t.Fatalf("SetupRouter() error = %v, want required-secret error", err)
	}
	if gin.Mode() != gin.ReleaseMode {
		t.Fatalf("gin.Mode() = %q, want %q", gin.Mode(), gin.ReleaseMode)
	}
}

func TestSetupRouterRejectsInvalidGinMode(t *testing.T) {
	originalMode := gin.Mode()
	t.Cleanup(func() { gin.SetMode(originalMode) })
	t.Setenv("GIN_MODE", "production")
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 32))

	_, err := SetupRouter()
	if err == nil || !strings.Contains(err.Error(), "invalid GIN_MODE") {
		t.Fatalf("SetupRouter() error = %v, want invalid-mode error", err)
	}
}
