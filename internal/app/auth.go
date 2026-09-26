package app

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

const sessionLifetime = 12 * time.Hour

type authHandler struct {
	password string
	mu       sync.Mutex
	sessions map[string]time.Time
	next     http.Handler
}

func newSessionAuth(next http.Handler) http.Handler {
	return &authHandler{password: os.Getenv("MCUI_HTTP_PASSWORD"), sessions: make(map[string]time.Time), next: next}
}

func (a *authHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		a.next.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch r.URL.Path {
	case "/api/auth/session":
		if r.Method != http.MethodGet {
			bad(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		respond(w, http.StatusOK, map[string]bool{
			"authenticated": a.password == "" || a.authorized(r),
			"enabled":       a.password != "",
		})
	case "/api/auth/login":
		a.login(w, r)
	case "/api/auth/logout":
		if r.Method != http.MethodPost {
			bad(w, http.StatusMethodNotAllowed, "Method not allowed")
			return
		}
		if cookie, err := r.Cookie("mcui_session"); err == nil {
			a.mu.Lock()
			delete(a.sessions, cookie.Value)
			a.mu.Unlock()
		}
		a.setCookie(w, r, "", -1)
		w.WriteHeader(http.StatusNoContent)
	default:
		if a.password != "" && !a.authorized(r) {
			bad(w, http.StatusUnauthorized, "Sign in required")
			return
		}
		a.next.ServeHTTP(w, r)
	}
}

func (a *authHandler) authorized(r *http.Request) bool {
	cookie, err := r.Cookie("mcui_session")
	if err != nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	expires, ok := a.sessions[cookie.Value]
	if !ok {
		return false
	}
	if time.Now().After(expires) {
		delete(a.sessions, cookie.Value)
		return false
	}
	return true
}

func (a *authHandler) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		bad(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var request struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		bad(w, http.StatusBadRequest, "Invalid request")
		return
	}
	if a.password == "" || subtle.ConstantTimeCompare([]byte(request.Password), []byte(a.password)) != 1 {
		bad(w, http.StatusUnauthorized, "Incorrect password")
		return
	}
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		bad(w, http.StatusInternalServerError, "Could not create session")
		return
	}
	token := hex.EncodeToString(bytes)
	a.mu.Lock()
	a.sessions[token] = time.Now().Add(sessionLifetime)
	a.mu.Unlock()
	a.setCookie(w, r, token, int(sessionLifetime.Seconds()))
	respond(w, http.StatusOK, map[string]bool{"authenticated": true})
}

func (a *authHandler) setCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: "mcui_session", Value: value, Path: "/api", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode,
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
}
