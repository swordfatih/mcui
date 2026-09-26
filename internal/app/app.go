package app

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Server struct {
	Name    string `json:"name"`
	Edition string `json:"edition"`
	Port    int    `json:"port"`
	Status  string `json:"status"`
}
type CreateRequest struct {
	Name       string `json:"name"`
	Edition    string `json:"edition"`
	Port       int    `json:"port"`
	WorldPath  string `json:"worldPath"`
	AcceptEULA bool   `json:"acceptEula"`
}
type API struct {
	Root   string
	mu     sync.Mutex
	backup *BackupManager
}

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
var portPattern = regexp.MustCompile(`(?m)^\s*- "?([0-9]+):(?:25565|19132)(?:/udp)?"?\s*$`)

func Serve(addr, root string) error {
	var err error
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	a := &API{Root: root}
	backup, err := newBackupManager(a)
	if err != nil {
		return err
	}
	a.backup = backup
	go backup.schedule(context.Background())
	mux := http.NewServeMux()
	mux.HandleFunc("/api/servers", a.servers)
	mux.HandleFunc("/api/backups/config", a.backupConfigHandler)
	mux.HandleFunc("/api/servers/", a.action)
	mux.Handle("/", http.FileServer(http.Dir("web/dist")))
	log.Printf("mcui listening on %s; servers in %s", addr, root)
	return http.ListenAndServe(addr, mux)
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func bad(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}
func (a *API) servers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		entries, err := os.ReadDir(a.Root)
		if err != nil {
			bad(w, 500, err.Error())
			return
		}
		out := []Server{}
		for _, e := range entries {
			if !e.IsDir() || !validName.MatchString(e.Name()) {
				continue
			}
			s, err := a.readServer(e.Name())
			if err == nil {
				s.Status = a.status(r.Context(), e.Name())
				out = append(out, s)
			}
		}
		respond(w, 200, out)
	case http.MethodPost:
		var req CreateRequest
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			bad(w, 400, "Invalid JSON request")
			return
		}
		if err := a.create(req); err != nil {
			bad(w, 400, err.Error())
			return
		}
		s, _ := a.readServer(req.Name)
		s.Status = "stopped"
		respond(w, 201, s)
	default:
		w.Header().Set("Allow", "GET, POST")
		bad(w, 405, "Method not allowed")
	}
}
func (a *API) action(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		bad(w, 405, "Method not allowed")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/servers/"), "/")
	if len(parts) != 2 || !validName.MatchString(parts[0]) || (parts[1] != "start" && parts[1] != "stop" && parts[1] != "backup") {
		bad(w, 404, "Not found")
		return
	}
	if parts[1] == "backup" {
		a.backupAction(w, r, parts[0])
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		bad(w, 405, "Method not allowed")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.backup.capturing(parts[0]) {
		bad(w, 409, "Server is being captured for backup")
		return
	}
	if _, err := a.readServer(parts[0]); err != nil {
		bad(w, 404, "Server not found")
		return
	}
	args := []string{"-p", "mcui-" + parts[0], "-f", filepath.Join(a.Root, parts[0], "compose.yaml")}
	if parts[1] == "start" {
		args = append(args, "up", "-d")
	} else {
		args = append(args, "stop")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", append([]string{"compose"}, args...)...).CombinedOutput()
	if err != nil {
		bad(w, 502, fmt.Sprintf("Docker Compose failed: %s", strings.TrimSpace(string(out))))
		return
	}
	s, _ := a.readServer(parts[0])
	s.Status = a.status(r.Context(), parts[0])
	respond(w, 200, s)
}
func (a *API) status(parent context.Context, name string) string {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", "mcui-"+name, "-f", filepath.Join(a.Root, name, "compose.yaml"), "ps", "-q", "mc")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "stopped"
	}
	ctx2, cancel2 := context.WithTimeout(parent, 5*time.Second)
	defer cancel2()
	out, err = exec.CommandContext(ctx2, "docker", "inspect", "--format", "{{.State.Status}}", id).Output()
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}
func (a *API) readServer(name string) (Server, error) {
	b, err := os.ReadFile(filepath.Join(a.Root, name, "compose.yaml"))
	if err != nil {
		return Server{}, err
	}
	data := string(b)
	edition := ""
	if strings.Contains(data, "image: itzg/minecraft-server:") {
		edition = "java"
	} else if strings.Contains(data, "image: itzg/minecraft-bedrock-server:") {
		edition = "bedrock"
	} else {
		return Server{}, errors.New("unrecognized image")
	}
	match := portPattern.FindStringSubmatch(data)
	if match == nil {
		return Server{}, errors.New("port missing")
	}
	port, _ := strconv.Atoi(match[1])
	return Server{Name: name, Edition: edition, Port: port}, nil
}
func (a *API) create(req CreateRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !validName.MatchString(req.Name) {
		return errors.New("Name must use lowercase letters, numbers, and hyphens (1–40 characters)")
	}
	if req.Edition != "java" && req.Edition != "bedrock" {
		return errors.New("Edition must be java or bedrock")
	}
	if req.Port < 1 || req.Port > 65535 {
		return errors.New("Port must be between 1 and 65535")
	}
	if !req.AcceptEULA {
		return errors.New("Accept the Minecraft EULA to create a server")
	}
	if req.WorldPath != "" && !filepath.IsAbs(req.WorldPath) {
		return errors.New("World path must be absolute")
	}
	entries, err := os.ReadDir(a.Root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		s, err := a.readServer(e.Name())
		if err == nil && s.Port == req.Port && s.Edition == req.Edition {
			return errors.New("Port already used by a server of this edition")
		}
	}
	dir := filepath.Join(a.Root, req.Name)
	if err := os.Mkdir(dir, 0755); err != nil {
		if os.IsExist(err) {
			return errors.New("Server name already exists")
		}
		return err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(dir)
		}
	}()
	data := filepath.Join(dir, "data")
	if err := os.Mkdir(data, 0755); err != nil {
		return err
	}
	if req.WorldPath != "" {
		if err := importWorld(req.WorldPath, data, req.Edition); err != nil {
			return err
		}
	}
	image, target, protocol := "itzg/minecraft-server:latest", 25565, ""
	if req.Edition == "bedrock" {
		image, target, protocol = "itzg/minecraft-bedrock-server:latest", 19132, "/udp"
	}
	compose := fmt.Sprintf("services:\n  mc:\n    image: %s\n    environment:\n      EULA: \"TRUE\"\n", image)
	if req.Edition == "bedrock" {
		compose += "      LEVEL_NAME: \"world\"\n"
	}
	compose += fmt.Sprintf("    ports:\n      - \"%d:%d%s\"\n    volumes:\n      - ./data:/data\n    restart: unless-stopped\n    stop_grace_period: 2m\n", req.Port, target, protocol)
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0644); err != nil {
		return err
	}
	keep = true
	return nil
}
func importWorld(path, data, edition string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("World path: %w", err)
	}
	var source string
	if info.IsDir() {
		source = path
	} else if info.Mode().IsRegular() {
		tmp, err := os.MkdirTemp("", "mcui-world-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		if err = extract(path, tmp); err != nil {
			return err
		}
		source = tmp
	} else {
		return errors.New("World path must be a directory, .zip or .tar.gz file")
	}
	root, err := findWorld(source, edition)
	if err != nil {
		return err
	}
	target := filepath.Join(data, "world")
	if edition == "bedrock" {
		if err = os.MkdirAll(filepath.Join(data, "worlds"), 0755); err != nil {
			return err
		}
		target = filepath.Join(data, "worlds", "world")
	}
	return copyTree(root, target)
}
func findWorld(base, edition string) (string, error) {
	marker := "level.dat"
	if edition == "bedrock" {
		marker = "level.dat"
	}
	var found []string
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("World contains a symbolic link")
		}
		if d.IsDir() {
			rel, _ := filepath.Rel(base, path)
			if strings.Count(rel, string(os.PathSeparator)) > 4 {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, marker)); err == nil {
				found = append(found, path)
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(found) != 1 {
		return "", errors.New("World path must contain exactly one world with level.dat")
	}
	return found[0], nil
}
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("World contains a symbolic link")
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0755)
		}
		if !d.Type().IsRegular() {
			return errors.New("World contains a non-regular file")
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(f, in)
		inputCloseErr := in.Close()
		outputCloseErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if inputCloseErr != nil {
			return inputCloseErr
		}
		return outputCloseErr
	})
}

const maxArchiveBytes int64 = 4 << 30

func safePath(root, name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return "", errors.New("Unsafe archive path")
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", errors.New("Unsafe archive path")
	}
	return filepath.Join(root, clean), nil
}
func extract(path, root string) error {
	var count int64
	write := func(name string, mode fs.FileMode, size int64, reader io.Reader) error {
		out, err := safePath(root, name)
		if err != nil {
			return err
		}
		if mode.IsDir() {
			return os.MkdirAll(out, 0755)
		}
		if !mode.IsRegular() {
			return errors.New("Archive contains a link or special file")
		}
		if size < 0 || size > maxArchiveBytes-count {
			return errors.New("Archive exceeds 4 GiB limit")
		}
		count += size
		if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		n, err := io.Copy(f, io.LimitReader(reader, size+1))
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if n != size {
			return errors.New("Archive entry size mismatch")
		}
		return closeErr
	}
	if strings.HasSuffix(strings.ToLower(path), ".zip") {
		z, err := zip.OpenReader(path)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			r, err := f.Open()
			if err != nil {
				return err
			}
			err = write(f.Name, f.Mode(), int64(f.UncompressedSize64), r)
			_ = r.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	if strings.HasSuffix(strings.ToLower(path), ".tar.gz") || strings.HasSuffix(strings.ToLower(path), ".tgz") {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return err
			}
			mode := fs.FileMode(0644)
			if h.Typeflag == tar.TypeDir {
				mode = os.ModeDir
			} else if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
				return errors.New("Archive contains a link or special file")
			}
			if err := write(h.Name, mode, h.Size, tr); err != nil {
				return err
			}
		}
	}
	return errors.New("World archive must be .zip, .tar.gz, or .tgz")
}
