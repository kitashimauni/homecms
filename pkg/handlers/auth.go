package handlers

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"hugo-cms/pkg/config"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

// GitHubUser represents the user info from GitHub API
type GitHubUser struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

// TokenValidationResult distinguishes token revocation from an unavailable
// GitHub validation service. Only TokenInvalid is authoritative enough to
// clear an existing CMS session.
type TokenValidationResult int

const (
	TokenValid TokenValidationResult = iota
	TokenInvalid
	TokenValidationUnavailable
)

func AuthRequired(c *gin.Context) {
	session := sessions.Default(c)
	token, tokenOK := session.Get("access_token").(string)
	username, userOK := session.Get("github_user").(string)
	if !tokenOK || token == "" || !userOK || username == "" || !config.IsUserAllowed(username) {
		if tokenOK || userOK {
			session.Clear()
			if err := session.Save(); err != nil {
				slog.Error("Failed to clear unauthorized session", "error", err)
			}
		}
		if strings.HasPrefix(c.Request.URL.Path, "/admin/api/") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		} else {
			c.Redirect(http.StatusFound, "/admin/login")
			c.Abort()
		}
		return
	}
	c.Next()
}

func LoginPage(c *gin.Context) {
	c.HTML(http.StatusOK, "login.html", nil)
}

func generateStateOauth() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate OAuth state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func GithubLogin(c *gin.Context) {
	state, err := generateStateOauth()
	if err != nil {
		ErrorInternal(c, "Failed to start OAuth login")
		return
	}
	session := sessions.Default(c)
	session.Set("oauth_state", state)
	if err := session.Save(); err != nil {
		ErrorInternal(c, "Failed to persist OAuth session")
		return
	}

	url := config.OauthConf.AuthCodeURL(state, oauth2.AccessTypeOffline)
	c.Redirect(http.StatusTemporaryRedirect, url)
}

func AuthCallback(c *gin.Context) {
	session := sessions.Default(c)
	retrievedState, ok := session.Get("oauth_state").(string)
	queryState := c.Query("state")

	if !ok || retrievedState == "" || queryState == "" ||
		subtle.ConstantTimeCompare([]byte(retrievedState), []byte(queryState)) != 1 {
		c.String(http.StatusBadRequest, "Invalid OAuth State")
		return
	}

	// Remove state from session
	session.Delete("oauth_state")
	if err := session.Save(); err != nil {
		ErrorInternal(c, "Failed to update OAuth session")
		return
	}

	// Use request context for proper cancellation propagation
	ctx := c.Request.Context()

	code := c.Query("code")
	if code == "" {
		c.String(http.StatusBadRequest, "Missing OAuth Code")
		return
	}
	token, err := config.OauthConf.Exchange(ctx, code)
	if err != nil {
		c.String(http.StatusInternalServerError, "OAuth Exchange Failed")
		return
	}

	// Fetch GitHub user info to check authorization
	user, err := fetchGitHubUser(ctx, token.AccessToken)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to fetch user info")
		return
	}

	// Check if user is allowed
	if !config.IsUserAllowed(user.Login) {
		c.HTML(http.StatusForbidden, "login.html", gin.H{
			"Error": fmt.Sprintf("User '%s' is not authorized to access this CMS", user.Login),
		})
		return
	}

	session.Set("access_token", token.AccessToken)
	session.Set("github_user", user.Login)
	session.Set("token_validated_at", time.Now().Unix())
	if err := session.Save(); err != nil {
		ErrorInternal(c, "Failed to persist login session")
		return
	}

	c.Redirect(http.StatusFound, "/admin/")
}

// HTTP client with timeout for external API calls
var httpClient = &http.Client{
	Transport: &http.Transport{
		// 接続（TCP）を確立するまでの制限
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
		}).DialContext,
		// TLSの証明書交換にかける制限
		TLSHandshakeTimeout: 10 * time.Second,
		// リクエストを送ってから、最初の1バイト（ヘッダー）が返ってくるまでの制限
		ResponseHeaderTimeout: 10 * time.Second,
	},
}

// fetchGitHubUser fetches the authenticated user's info from GitHub API
func fetchGitHubUser(ctx context.Context, accessToken string) (*GitHubUser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API returned status %d", resp.StatusCode)
	}

	var user GitHubUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	return &user, nil
}

// validateGitHubToken checks if the access token is still valid by calling
// GitHub API. A 401 is the explicit invalid-token response; all other
// non-success responses are treated as temporarily unavailable because 403
// can represent rate limiting or another GitHub-side restriction.
func validateGitHubToken(ctx context.Context, accessToken string) TokenValidationResult {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return TokenValidationUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := httpClient.Do(req)
	if err != nil {
		slog.Warn("GitHub token validation unavailable", "error", err)
		return TokenValidationUnavailable
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		return TokenValid
	case http.StatusUnauthorized:
		return TokenInvalid
	default:
		slog.Warn("GitHub token validation returned a transient status", "status", resp.StatusCode)
		return TokenValidationUnavailable
	}
}

// TokenValidation middleware periodically validates the GitHub access token
// It caches the validation result to avoid excessive API calls
func TokenValidation(c *gin.Context) {
	session := sessions.Default(c)
	token, ok := session.Get("access_token").(string)
	if !ok || token == "" {
		// No token, let AuthRequired handle it
		c.Next()
		return
	}

	// Check last validation time (validate every 5 minutes)
	lastValidation, _ := session.Get("token_validated_at").(int64)
	now := time.Now().Unix()

	// Validate token every 5 minutes
	if now-lastValidation > 300 {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()

		validationResult := validateGitHubToken(ctx, token)
		if validationResult == TokenInvalid {
			// Token is invalid, clear session
			session.Clear()
			if err := session.Save(); err != nil {
				slog.Error("Failed to clear expired session", "error", err)
			}

			if strings.HasPrefix(c.Request.URL.Path, "/admin/api/") {
				ErrorUnauthorized(c, "Session expired. Please login again.")
				c.Abort()
				return
			}
			c.Redirect(http.StatusFound, "/admin/login")
			c.Abort()
			return
		}
		if validationResult == TokenValidationUnavailable {
			// A GitHub/API/network outage is not evidence that the token was
			// revoked. Keep the session, leave the timestamp unchanged, and
			// retry on the next request after the validation interval.
			slog.Warn("Keeping GitHub session after transient token validation failure")
			c.Next()
			return
		}

		// Update validation timestamp
		session.Set("token_validated_at", now)
		if err := session.Save(); err != nil {
			slog.Error("Failed to persist token validation time", "error", err)
		}
	}

	c.Next()
}

func Logout(c *gin.Context) {
	session := sessions.Default(c)
	session.Clear()
	if err := session.Save(); err != nil {
		ErrorInternal(c, "Failed to clear session")
		return
	}
	c.Redirect(http.StatusFound, "/admin/login")
}

// GetCSRFToken returns the CSRF token for the current session
func GetCSRFToken(c *gin.Context) {
	session := sessions.Default(c)
	token, ok := session.Get("csrf_token").(string)
	if !ok || token == "" {
		var err error
		token, err = generateCSRFToken()
		if err != nil {
			ErrorInternal(c, "Failed to generate CSRF token")
			return
		}
		session.Set("csrf_token", token)
		if err := session.Save(); err != nil {
			ErrorInternal(c, "Failed to persist CSRF token")
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"csrf_token": token})
}

func RequestBodyLimit(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}

// CSRFProtection middleware validates CSRF tokens on POST/PUT/DELETE requests
func CSRFProtection(c *gin.Context) {
	// Skip CSRF check for GET and HEAD requests
	if c.Request.Method == "GET" || c.Request.Method == "HEAD" || c.Request.Method == "OPTIONS" {
		c.Next()
		return
	}

	session := sessions.Default(c)
	sessionToken, ok := session.Get("csrf_token").(string)
	if !ok || sessionToken == "" {
		// Generate a new token if none exists (for better UX)
		// Client should retry after getting new token
		ErrorForbidden(c, "CSRF token expired. Please refresh and try again.")
		c.Abort()
		return
	}

	// Require the header so multipart bodies are not parsed before the upload
	// handler has applied its request-size limit.
	requestToken := c.GetHeader("X-CSRF-Token")

	if requestToken == "" {
		ErrorForbidden(c, "CSRF token missing from request")
		c.Abort()
		return
	}

	if subtle.ConstantTimeCompare([]byte(requestToken), []byte(sessionToken)) != 1 {
		ErrorForbidden(c, "CSRF token mismatch. Please refresh and try again.")
		c.Abort()
		return
	}

	c.Next()
}

func generateCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate CSRF token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
