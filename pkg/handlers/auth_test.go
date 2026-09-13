package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hugo-cms/pkg/config"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

type authRoundTripFunc func(*http.Request) (*http.Response, error)

func (f authRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func githubValidationResponse(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d", status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{}`)),
	}
}

func useAuthHTTPClient(t *testing.T, roundTrip authRoundTripFunc) {
	t.Helper()
	originalClient := httpClient
	httpClient = &http.Client{Transport: roundTrip}
	t.Cleanup(func() { httpClient = originalClient })
}

func tokenValidationRouter() *gin.Engine {
	router := newSessionTestRouter()
	router.GET("/admin/api/protected", TokenValidation, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET("/session-state", func(c *gin.Context) {
		session := sessions.Default(c)
		c.JSON(http.StatusOK, gin.H{
			"access_token":       session.Get("access_token"),
			"token_validated_at": session.Get("token_validated_at"),
		})
	})
	return router
}

func requestWithCookie(t *testing.T, router *gin.Engine, method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	router.ServeHTTP(recorder, request)
	return recorder
}

func firstResponseCookie(t *testing.T, recorder *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookies := recorder.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("response did not contain a session cookie")
	}
	return cookies[0]
}

func newSessionTestRouter() *gin.Engine {
	router := gin.New()
	store := cookie.NewStore(
		[]byte(strings.Repeat("a", 64)),
		[]byte(strings.Repeat("b", 32)),
	)
	router.Use(sessions.Sessions("test-session", store))
	return router
}

func sessionCookie(t *testing.T, router *gin.Engine, values map[string]interface{}) *http.Cookie {
	t.Helper()

	router.GET("/seed-session", func(c *gin.Context) {
		session := sessions.Default(c)
		for key, value := range values {
			session.Set(key, value)
		}
		if err := session.Save(); err != nil {
			t.Fatalf("save test session: %v", err)
		}
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/seed-session", nil))
	result := recorder.Result()
	defer result.Body.Close()
	cookies := result.Cookies()
	if len(cookies) == 0 {
		t.Fatal("session seed did not return a cookie")
	}
	return cookies[0]
}

func TestAuthRequiredFailsClosedWithoutAllowlist(t *testing.T) {
	originalUsers := config.AllowedGitHubUsers
	originalAllowAll := config.AllowAllGitHubUsers
	t.Cleanup(func() {
		config.AllowedGitHubUsers = originalUsers
		config.AllowAllGitHubUsers = originalAllowAll
	})
	config.AllowedGitHubUsers = nil
	config.AllowAllGitHubUsers = false

	router := newSessionTestRouter()
	cookie := sessionCookie(t, router, map[string]interface{}{
		"access_token": "token",
		"github_user":  "octocat",
	})
	router.GET("/protected", AuthRequired, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.AddCookie(cookie)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusFound {
		t.Fatalf("AuthRequired status = %d, want %d", recorder.Code, http.StatusFound)
	}
}

func TestAuthRequiredAllowsConfiguredUser(t *testing.T) {
	originalUsers := config.AllowedGitHubUsers
	originalAllowAll := config.AllowAllGitHubUsers
	t.Cleanup(func() {
		config.AllowedGitHubUsers = originalUsers
		config.AllowAllGitHubUsers = originalAllowAll
	})
	config.AllowedGitHubUsers = []string{"octocat"}
	config.AllowAllGitHubUsers = false

	router := newSessionTestRouter()
	cookie := sessionCookie(t, router, map[string]interface{}{
		"access_token": "token",
		"github_user":  "OctoCat",
	})
	router.GET("/protected", AuthRequired, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.AddCookie(cookie)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("AuthRequired status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestCSRFProtectionRejectsInvalidSessionTokenType(t *testing.T) {
	router := newSessionTestRouter()
	cookie := sessionCookie(t, router, map[string]interface{}{
		"csrf_token": 42,
	})
	router.POST("/protected", CSRFProtection, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/protected", nil)
	request.Header.Set("X-CSRF-Token", "token")
	request.AddCookie(cookie)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("CSRFProtection status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}

func TestGetCSRFTokenReplacesInvalidSessionToken(t *testing.T) {
	router := newSessionTestRouter()
	cookie := sessionCookie(t, router, map[string]interface{}{
		"csrf_token": 42,
	})
	router.GET("/csrf", GetCSRFToken)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/csrf", nil)
	request.AddCookie(cookie)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("GetCSRFToken status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if strings.Contains(recorder.Body.String(), `"csrf_token":42`) {
		t.Fatal("GetCSRFToken returned an invalid token from the session")
	}
}

func TestValidateGitHubTokenClassifiesInvalidAndTransientResponses(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		transport   error
		expectation TokenValidationResult
	}{
		{name: "valid", status: http.StatusOK, expectation: TokenValid},
		{name: "invalid token", status: http.StatusUnauthorized, expectation: TokenInvalid},
		{name: "forbidden or rate limited", status: http.StatusForbidden, expectation: TokenValidationUnavailable},
		{name: "rate limited", status: http.StatusTooManyRequests, expectation: TokenValidationUnavailable},
		{name: "github unavailable", status: http.StatusServiceUnavailable, expectation: TokenValidationUnavailable},
		{name: "transport error", transport: context.DeadlineExceeded, expectation: TokenValidationUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useAuthHTTPClient(t, func(*http.Request) (*http.Response, error) {
				if tt.transport != nil {
					return nil, tt.transport
				}
				return githubValidationResponse(tt.status), nil
			})

			if got := validateGitHubToken(context.Background(), "token"); got != tt.expectation {
				t.Fatalf("validateGitHubToken() = %d, want %d", got, tt.expectation)
			}
		})
	}
}

func TestTokenValidationKeepsSessionOnTransientFailure(t *testing.T) {
	oldValidation := time.Now().Unix() - 301
	router := tokenValidationRouter()
	cookie := sessionCookie(t, router, map[string]interface{}{
		"access_token":       "token",
		"github_user":        "octocat",
		"token_validated_at": oldValidation,
	})
	useAuthHTTPClient(t, func(*http.Request) (*http.Response, error) {
		return githubValidationResponse(http.StatusServiceUnavailable), nil
	})

	recorder := requestWithCookie(t, router, http.MethodGet, "/admin/api/protected", cookie)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("TokenValidation transient status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	stateRecorder := requestWithCookie(t, router, http.MethodGet, "/session-state", cookie)
	var state map[string]interface{}
	if err := json.Unmarshal(stateRecorder.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode session state: %v", err)
	}
	if state["access_token"] != "token" {
		t.Fatalf("access_token after transient failure = %#v, want token", state["access_token"])
	}
	if got := int64(state["token_validated_at"].(float64)); got != oldValidation {
		t.Fatalf("token_validated_at after transient failure = %d, want unchanged %d", got, oldValidation)
	}
}

func TestTokenValidationUpdatesTimestampAfterTransientRecovery(t *testing.T) {
	oldValidation := time.Now().Unix() - 301
	router := tokenValidationRouter()
	cookie := sessionCookie(t, router, map[string]interface{}{
		"access_token":       "token",
		"github_user":        "octocat",
		"token_validated_at": oldValidation,
	})
	responses := []*http.Response{
		githubValidationResponse(http.StatusTooManyRequests),
		githubValidationResponse(http.StatusOK),
	}
	useAuthHTTPClient(t, func(*http.Request) (*http.Response, error) {
		response := responses[0]
		responses = responses[1:]
		return response, nil
	})

	first := requestWithCookie(t, router, http.MethodGet, "/admin/api/protected", cookie)
	if first.Code != http.StatusNoContent {
		t.Fatalf("first TokenValidation status = %d, want %d", first.Code, http.StatusNoContent)
	}
	second := requestWithCookie(t, router, http.MethodGet, "/admin/api/protected", cookie)
	if second.Code != http.StatusNoContent {
		t.Fatalf("recovered TokenValidation status = %d, want %d", second.Code, http.StatusNoContent)
	}
	updatedCookie := firstResponseCookie(t, second)
	stateRecorder := requestWithCookie(t, router, http.MethodGet, "/session-state", updatedCookie)
	var state map[string]interface{}
	if err := json.Unmarshal(stateRecorder.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode recovered session state: %v", err)
	}
	if got := int64(state["token_validated_at"].(float64)); got <= oldValidation {
		t.Fatalf("token_validated_at after recovery = %d, want greater than %d", got, oldValidation)
	}
}

func TestTokenValidationClearsSessionOnlyForInvalidToken(t *testing.T) {
	router := tokenValidationRouter()
	cookie := sessionCookie(t, router, map[string]interface{}{
		"access_token":       "token",
		"github_user":        "octocat",
		"token_validated_at": time.Now().Unix() - 301,
	})
	useAuthHTTPClient(t, func(*http.Request) (*http.Response, error) {
		return githubValidationResponse(http.StatusUnauthorized), nil
	})

	recorder := requestWithCookie(t, router, http.MethodGet, "/admin/api/protected", cookie)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("invalid TokenValidation status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	clearedCookie := firstResponseCookie(t, recorder)
	stateRecorder := requestWithCookie(t, router, http.MethodGet, "/session-state", clearedCookie)
	var state map[string]interface{}
	if err := json.Unmarshal(stateRecorder.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode cleared session state: %v", err)
	}
	if state["access_token"] != nil {
		t.Fatalf("access_token after invalid token = %#v, want nil", state["access_token"])
	}
}

func TestRequestBodyLimit(t *testing.T) {
	router := gin.New()
	router.POST("/limited", RequestBodyLimit(4), func(c *gin.Context) {
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/limited", strings.NewReader("12345"))
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("RequestBodyLimit status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
}
