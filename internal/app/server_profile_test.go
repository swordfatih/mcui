package app

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func profileImage(t *testing.T, size int, format string) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	img.Set(0, 0, color.NRGBA{R: 200, A: 255})
	var buf bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&buf, img, nil)
	} else {
		err = png.Encode(&buf, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func profileRequest(a *API, name, display string, icon []byte, remove, header bool) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	_ = form.WriteField("displayName", display)
	if remove {
		_ = form.WriteField("removeIcon", "true")
	}
	if icon != nil {
		file, _ := form.CreateFormFile("icon", "uploaded.png")
		_, _ = file.Write(icon)
	}
	_ = form.Close()
	r := httptest.NewRequest(http.MethodPut, "/api/server-profile/"+name, &buf)
	r.Header.Set("Content-Type", form.FormDataContentType())
	if header {
		r.Header.Set("X-MCUI-Profile", "1")
	}
	w := httptest.NewRecorder()
	a.serverProfileHandler(w, r)
	return w
}
func TestServerProfilePersistenceAndNotificationIdentity(t *testing.T) {
	a, p := testPushManager(t)
	for _, format := range []string{"png", "jpeg"} {
		w := profileRequest(a, "one", "Family Minecraft 🎮", profileImage(t, 128, format), false, true)
		if w.Code != 200 {
			t.Fatalf("save: %d %s", w.Code, w.Body)
		}
		root, err := os.Open(filepath.Join(a.Root, "one", ".mcui", "icon.png"))
		if err != nil {
			t.Fatal(err)
		}
		config, format, err := image.DecodeConfig(root)
		root.Close()
		if err != nil || format != "png" || config.Width != 256 || config.Height != 256 {
			t.Fatalf("bad normalized image: %+v %s %v", config, format, err)
		}
	}
	restored := &API{Root: a.Root}
	profile := restored.profile("one")
	if profile.DisplayName != "Family Minecraft 🎮" || profile.IconHash == "" {
		t.Fatal(profile)
	}
	fakeDockerConfig(t, `{"services":{"mc":{"image":"itzg/minecraft-server:latest"}}}`)
	server, err := restored.readServer("one")
	if err != nil || server.Name != "one" || server.DisplayName != profile.DisplayName || server.IconURL != profile.iconURL("one") {
		t.Fatalf("list identity: %+v %v", server, err)
	}
	var payload map[string]string
	if err := json.Unmarshal(p.eventPayload(playerEvent{Server: "one", Player: "Alex", Action: "joined"}), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["title"] != "Family Minecraft 🎮" || payload["icon"] != server.IconURL || payload["url"] != "/servers/one" {
		t.Fatal(payload)
	}
	// Background icon fetch works after session expiry, without exposing JSON.
	t.Setenv("MCUI_HTTP_PASSWORD", "secret")
	mux := http.NewServeMux()
	mux.HandleFunc("/server-icons/", a.serverIcon)
	w := httptest.NewRecorder()
	newSessionAuth(mux).ServeHTTP(w, httptest.NewRequest("GET", server.IconURL, nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("icon: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	a.serverIcon(w, httptest.NewRequest("GET", "/server-icons/one/server.json", nil))
	if w.Code != 404 {
		t.Fatal("exposed profile JSON through icon route")
	}
	oldURL := server.IconURL
	if w := profileRequest(a, "one", "New name", nil, false, true); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if a.profile("one").IconHash != profile.IconHash {
		t.Fatal("name edit removed picture")
	}
	if w := profileRequest(a, "one", "New name", nil, true, true); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if a.profile("one").IconHash != "" {
		t.Fatal("remove icon ignored")
	}
	w = httptest.NewRecorder()
	a.serverIcon(w, httptest.NewRequest("GET", oldURL, nil))
	if w.Code != 404 {
		t.Fatal("removed icon still accessible")
	}
	if _, err := os.Stat(filepath.Join(a.Root, "one", "compose.yaml")); err != nil {
		t.Fatal("renamed server directory")
	}
}
func TestServerIconValidation(t *testing.T) {
	for _, size := range []int{64, 1024} {
		if _, err := normalizeServerIcon(profileImage(t, size, "png")); err != nil {
			t.Fatal(err)
		}
	}
	for _, size := range []int{1, 63, 1025} {
		if _, err := normalizeServerIcon(profileImage(t, size, "png")); err == nil {
			t.Fatalf("accepted %dx%d", size, size)
		}
	}
	var rectangular bytes.Buffer
	_ = png.Encode(&rectangular, image.NewNRGBA(image.Rect(0, 0, 100, 200)))
	for _, bad := range [][]byte{rectangular.Bytes(), []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), []byte("GIF89a"), bytes.Repeat([]byte{0}, maxServerIconBytes+1), profileImage(t, 64, "png")[:50]} {
		if _, err := normalizeServerIcon(bad); err == nil {
			t.Fatal("accepted invalid image")
		}
	}
}
func TestServerProfileInvalidInputDoesNotModifyProfile(t *testing.T) {
	a, _ := testPushManager(t)
	if w := profileRequest(a, "one", "Original", profileImage(t, 64, "png"), false, true); w.Code != 200 {
		t.Fatal(w.Body)
	}
	before := a.profile("one")
	for _, display := range []string{"", strings.Repeat("x", 81), "bad\nname"} {
		if w := profileRequest(a, "one", display, nil, false, true); w.Code != 400 {
			t.Fatalf("accepted name %q: %d", display, w.Code)
		}
	}
	if w := profileRequest(a, "one", "changed", []byte("not a PNG"), false, true); w.Code != 400 {
		t.Fatal(w.Body)
	}
	if w := profileRequest(a, "one", "changed", nil, false, false); w.Code != 403 {
		t.Fatal("missing CSRF guard")
	}
	if w := profileRequest(a, "missing", "changed", nil, false, true); w.Code != 404 {
		t.Fatal("accepted missing server")
	}
	if w := profileRequest(a, "one", "changed", profileImage(t, 64, "png"), true, true); w.Code != 400 {
		t.Fatal("accepted conflicting image actions")
	}
	if a.profile("one") != before {
		t.Fatal("invalid edit changed saved profile")
	}
}
func TestServerProfileDoesNotFollowEscapingSymlink(t *testing.T) {
	a, _ := testPushManager(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(a.Root, "one", ".mcui")); err != nil {
		t.Fatal(err)
	}
	w := profileRequest(a, "one", "Escape", nil, false, true)
	if w.Code != 500 {
		t.Fatalf("accepted metadata symlink: %d", w.Code)
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("wrote outside server directory")
	}
}

type rejectingPushClient struct{}

func (rejectingPushClient) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader(`{"reason":"BadJwtToken"}`))}, nil
}
func TestDevicePushTestRequiresSavedSubscription(t *testing.T) {
	a, p := testPushManager(t)
	sub := testPushSubscription(t, "https://fcm.googleapis.com/device")
	client := &fakePushClient{statuses: []int{201}}
	p.client = client
	req := map[string]any{"action": "test", "server": "one", "endpoint": sub.Endpoint}
	if w := pushRequest(a, req, true); w.Code != 409 {
		t.Fatalf("test without subscription: %d", w.Code)
	}
	p.state.Devices[sub.Endpoint] = pushDevice{Subscription: sub, Servers: map[string]bool{"one": true}}
	if w := pushRequest(a, req, false); w.Code != 403 {
		t.Fatal("test missing CSRF guard")
	}
	if w := pushRequest(a, req, true); w.Code != 200 || !strings.Contains(w.Body.String(), "accepted") {
		t.Fatalf("test: %d %s", w.Code, w.Body)
	}
	if len(client.endpoints) != 1 {
		t.Fatal("test did not send to selected device")
	}
	p.client = rejectingPushClient{}
	if w := pushRequest(a, req, true); w.Code != 502 || !strings.Contains(w.Body.String(), "BadJwtToken") || strings.Contains(w.Body.String(), sub.Endpoint) {
		t.Fatalf("unsafe or missing diagnostic: %d %s", w.Code, w.Body)
	}
	_, err := p.send(context.Background(), []byte("test"), sub)
	if err == nil {
		t.Fatal("rejection treated as success")
	}

}
