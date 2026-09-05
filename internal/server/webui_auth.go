package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"bqagent/internal/globalconfig"
)

const (
	webUIAuthCookie      = "bqagent_webui_session"
	webUIAuthSessionLife = 24 * time.Hour
	maxWebUILoginBytes   = 4 << 10
)

type webUIAuth struct {
	required     bool
	passwordHash [sha256.Size]byte
	now          func() time.Time
	mu           sync.Mutex
	sessions     map[string]time.Time
}

type webUIAuthResponse struct {
	Required      bool `json:"required"`
	Authenticated bool `json:"authenticated"`
}

func loadWebUIAuth(store *globalconfig.Store) *webUIAuth {
	password := ""
	if store != nil {
		config, err := store.LoadWebUI()
		if err != nil {
			log.Printf("load WebUI authentication config: %v", err)
		} else if config != nil {
			password = config.Password
		}
	}
	return newWebUIAuth(password)
}

func newWebUIAuth(password string) *webUIAuth {
	password = strings.TrimSpace(password)
	auth := &webUIAuth{required: password != "", now: time.Now, sessions: make(map[string]time.Time)}
	if auth.required {
		auth.passwordHash = sha256.Sum256([]byte(password))
	}
	return auth
}

func (auth *webUIAuth) protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if auth == nil || !auth.required || !webUIProtectedPath(request.URL.Path) {
			next.ServeHTTP(writer, request)
			return
		}
		if !auth.authenticated(request) {
			writeJSON(writer, http.StatusUnauthorized, chatResponse{Error: "authentication required"})
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead && !sameOriginRequest(request) {
			writeJSON(writer, http.StatusForbidden, chatResponse{Error: "cross-origin request is not allowed"})
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func webUIProtectedPath(path string) bool {
	if path == "/api/v1/webui/auth" {
		return false
	}
	return strings.HasPrefix(path, "/api/v1/webui/") ||
		path == "/api/v1/status" ||
		path == "/api/v1/chat" ||
		path == "/api/v1/chat/stop" ||
		strings.HasPrefix(path, "/api/v1/runs/")
}

func (auth *webUIAuth) handle(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Vary", "Cookie")
	switch request.Method {
	case http.MethodGet:
		writeJSON(writer, http.StatusOK, webUIAuthResponse{Required: auth.required, Authenticated: !auth.required || auth.authenticated(request)})
	case http.MethodPost:
		auth.login(writer, request)
	case http.MethodDelete:
		auth.logout(writer, request)
	default:
		writer.Header().Set("Allow", "GET, POST, DELETE")
		writeError(writer, http.StatusMethodNotAllowed, chatResponse{Error: "method not allowed"})
	}
}

func (auth *webUIAuth) login(writer http.ResponseWriter, request *http.Request) {
	if !sameOriginRequest(request) {
		writeError(writer, http.StatusForbidden, chatResponse{Error: "cross-origin login is not allowed"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxWebUILoginBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var payload struct {
		Password string `json:"password"`
	}
	if err := decoder.Decode(&payload); err != nil {
		writeError(writer, http.StatusBadRequest, chatResponse{Error: "invalid login request"})
		return
	}
	// Password validation and session creation must be atomic with password changes.
	auth.mu.Lock()
	defer auth.mu.Unlock()
	if auth.required {
		provided := sha256.Sum256([]byte(payload.Password))
		if subtle.ConstantTimeCompare(provided[:], auth.passwordHash[:]) != 1 {
			writeError(writer, http.StatusUnauthorized, chatResponse{Error: "密码错误"})
			return
		}
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		writeError(writer, http.StatusInternalServerError, chatResponse{Error: "failed to create login session"})
		return
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	expires := auth.now().Add(webUIAuthSessionLife)
	auth.pruneLocked()
	auth.sessions[token] = expires
	http.SetCookie(writer, &http.Cookie{
		Name: webUIAuthCookie, Value: token, Path: "/", Expires: expires,
		MaxAge: int(webUIAuthSessionLife.Seconds()), HttpOnly: true,
		Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(writer, http.StatusOK, webUIAuthResponse{Required: auth.required, Authenticated: true})
}

func (auth *webUIAuth) logout(writer http.ResponseWriter, request *http.Request) {
	if !sameOriginRequest(request) {
		writeError(writer, http.StatusForbidden, chatResponse{Error: "cross-origin logout is not allowed"})
		return
	}
	if cookie, err := request.Cookie(webUIAuthCookie); err == nil {
		auth.mu.Lock()
		delete(auth.sessions, cookie.Value)
		auth.mu.Unlock()
	}
	http.SetCookie(writer, &http.Cookie{
		Name: webUIAuthCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
	writeJSON(writer, http.StatusOK, webUIAuthResponse{Required: auth.required, Authenticated: !auth.required})
}

func (auth *webUIAuth) authenticated(request *http.Request) bool {
	if auth == nil {
		return true
	}
	auth.mu.Lock()
	defer auth.mu.Unlock()
	return auth.authenticatedLocked(request)
}

func (auth *webUIAuth) authenticatedLocked(request *http.Request) bool {
	if auth == nil || !auth.required {
		return true
	}
	cookie, err := request.Cookie(webUIAuthCookie)
	if err != nil || cookie.Value == "" {
		return false
	}
	expires, ok := auth.sessions[cookie.Value]
	if !ok || !expires.After(auth.now()) {
		delete(auth.sessions, cookie.Value)
		return false
	}
	return true
}

func (auth *webUIAuth) pruneLocked() {
	now := auth.now()
	for token, expires := range auth.sessions {
		if !expires.After(now) {
			delete(auth.sessions, token)
		}
	}
}

func sameOriginRequest(request *http.Request) bool {
	origin := strings.TrimSpace(request.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, request.Host)
}
