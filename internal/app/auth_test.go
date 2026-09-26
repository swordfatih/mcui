package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionAuthentication(t *testing.T) {
	t.Setenv("MCUI_HTTP_PASSWORD", "secret")
	handler := newSessionAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := func(method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if cookie != nil {
			req.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}

	if got := request("GET", "/api/servers", "", nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("unauthorized API request: %d", got)
	}
	if got := request("POST", "/api/auth/login", `{"password":"wrong"}`, nil).Code; got != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", got)
	}
	login := request("POST", "/api/auth/login", `{"password":"secret"}`, nil)
	if login.Code != http.StatusOK || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login: %d, cookies: %v", login.Code, login.Result().Cookies())
	}
	cookie := login.Result().Cookies()[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("unsafe cookie: %+v", cookie)
	}
	if got := request("GET", "/api/servers", "", cookie).Code; got != http.StatusNoContent {
		t.Fatalf("authenticated API request: %d", got)
	}
	if got := request("POST", "/api/auth/logout", "", cookie).Code; got != http.StatusNoContent {
		t.Fatalf("logout: %d", got)
	}
	if got := request("GET", "/api/servers", "", cookie).Code; got != http.StatusUnauthorized {
		t.Fatalf("old session still accepted: %d", got)
	}
}
