package app

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const maxServerIconBytes = 2 << 20

type serverProfile struct {
	DisplayName string `json:"displayName"`
	IconHash    string `json:"iconHash,omitempty"`
}

func (p serverProfile) iconURL(name string) string {
	if p.IconHash == "" {
		return ""
	}
	return "/server-icons/" + url.PathEscape(name) + "/" + p.IconHash + ".png"
}
func (a *API) profileRoot(name string, create bool) (*os.Root, error) {
	if !safeFolderName(name) {
		return nil, os.ErrInvalid
	}
	root, err := os.OpenRoot(a.Root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	server, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	defer server.Close()
	if create {
		if err := server.Mkdir(".mcui", 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	return server.OpenRoot(".mcui")
}
func readProfile(root *os.Root, name string) (serverProfile, error) {
	result := serverProfile{DisplayName: name}
	f, err := root.Open("server.json")
	if os.IsNotExist(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	defer f.Close()
	err = json.NewDecoder(io.LimitReader(f, 16384)).Decode(&result)
	if result.DisplayName == "" {
		result.DisplayName = name
	}
	return result, err
}
func (a *API) profile(name string) serverProfile {
	a.profileMu.RLock()
	defer a.profileMu.RUnlock()
	fallback := serverProfile{DisplayName: name}
	root, err := a.profileRoot(name, false)
	if err != nil {
		return fallback
	}
	defer root.Close()
	p, err := readProfile(root, name)
	if err != nil {
		return fallback
	}
	return p
}
func normalizeServerIcon(data []byte) ([]byte, error) {
	if len(data) > maxServerIconBytes {
		return nil, errors.New("Image must be 2 MiB or smaller")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return nil, errors.New("Image must be a valid PNG or JPEG")
	}
	if config.Width != config.Height || config.Width < 64 || config.Width > 1024 {
		return nil, errors.New("Image must be square, between 64×64 and 1024×1024 pixels")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("Could not decode image")
	}
	// A small, metadata-free PNG works consistently as a list and push icon.
	out := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			out.Set(x, y, img.At(img.Bounds().Min.X+x*config.Width/256, img.Bounds().Min.Y+y*config.Height/256))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
func atomicProfileFile(root *os.Root, name string, data []byte) error {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temp := ".profile-" + hex.EncodeToString(random)
	f, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Rename(temp, name)
}
func (a *API) serverProfileHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/server-profile/")
	if _, err := a.composeFile(name); err != nil {
		bad(w, 404, "Server not found")
		return
	}
	if r.Method == http.MethodGet {
		respond(w, 200, a.profile(name))
		return
	}
	if r.Method != http.MethodPut {
		bad(w, 405, "Method not allowed")
		return
	}
	if r.Header.Get("X-MCUI-Profile") != "1" {
		bad(w, 403, "Same-origin profile request required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxServerIconBytes+16384)
	if err := r.ParseMultipartForm(maxServerIconBytes + 16384); err != nil {
		bad(w, 400, "Upload a PNG/JPEG no larger than 2 MiB")
		return
	}
	defer r.MultipartForm.RemoveAll()
	display := strings.TrimSpace(r.FormValue("displayName"))
	if !utf8.ValidString(display) || utf8.RuneCountInString(display) < 1 || utf8.RuneCountInString(display) > 80 || strings.IndexFunc(display, unicode.IsControl) >= 0 {
		bad(w, 400, "Name must contain 1–80 characters without control characters")
		return
	}
	var icon []byte
	files := r.MultipartForm.File["icon"]
	if len(files) > 1 || (len(files) > 0 && r.FormValue("removeIcon") == "true") {
		bad(w, 400, "Choose one image or remove the current image")
		return
	}
	if len(files) == 1 {
		f, err := files[0].Open()
		if err != nil {
			bad(w, 400, "Could not read image")
			return
		}
		data, err := io.ReadAll(io.LimitReader(f, maxServerIconBytes+1))
		f.Close()
		if err != nil {
			bad(w, 400, "Could not read image")
			return
		}
		icon, err = normalizeServerIcon(data)
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
	}
	a.profileMu.Lock()
	defer a.profileMu.Unlock()
	root, err := a.profileRoot(name, true)
	if err != nil {
		bad(w, 500, "Could not open server profile")
		return
	}
	defer root.Close()
	profile, err := readProfile(root, name)
	if err != nil {
		bad(w, 500, "Could not read server profile")
		return
	}
	profile.DisplayName = display
	var oldIcon []byte
	if icon != nil {
		oldIcon, err = root.ReadFile("icon.png")
		if err != nil && !os.IsNotExist(err) {
			bad(w, 500, "Could not read current image")
			return
		}
		if err = atomicProfileFile(root, "icon.png", icon); err != nil {
			bad(w, 500, "Could not save image")
			return
		}
		profile.IconHash = revision(icon)
	} else if r.FormValue("removeIcon") == "true" {
		profile.IconHash = ""
	}
	data, _ := json.MarshalIndent(profile, "", "  ")
	if err := atomicProfileFile(root, "server.json", data); err != nil {
		if icon != nil {
			if oldIcon != nil {
				_ = atomicProfileFile(root, "icon.png", oldIcon)
			} else {
				_ = root.Remove("icon.png")
			}
		}
		bad(w, 500, "Could not save server profile")
		return
	}
	if profile.IconHash == "" {
		_ = root.Remove("icon.png")
	}
	respond(w, 200, profile)
}

// Icon URLs are readable without a session so background notifications after
// sign-out can load them. Only the normalized PNG at its content hash is served.
func (a *API) serverIcon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		bad(w, 405, "Method not allowed")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/server-icons/"), "/")
	if len(parts) != 2 || !safeFolderName(parts[0]) || len(parts[1]) != 68 || !strings.HasSuffix(parts[1], ".png") {
		http.NotFound(w, r)
		return
	}
	a.profileMu.RLock()
	defer a.profileMu.RUnlock()
	root, err := a.profileRoot(parts[0], false)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	p, err := readProfile(root, parts[0])
	if err != nil || p.IconHash+".png" != parts[1] {
		http.NotFound(w, r)
		return
	}
	f, err := root.Open("icon.png")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxServerIconBytes+1))
	if err != nil || revision(data) != p.IconHash {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	http.ServeContent(w, r, "icon.png", time.Time{}, bytes.NewReader(data))
}
