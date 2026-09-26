package app

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
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
	Name     string `json:"name"`
	Edition  string `json:"edition"`
	Port     int    `json:"port,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Host     string `json:"host,omitempty"`
	Status   string `json:"status"`
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
var validProjectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func safeFolderName(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && !strings.Contains(name, "\\")
}

func projectName(name string) string {
	if validProjectName.MatchString(name) {
		return "mcui-" + name
	}
	hash := sha256.Sum256([]byte(name))
	return fmt.Sprintf("mcui-%x", hash[:8])
}

type composeService struct {
	Image         string          `json:"image"`
	ContainerName string          `json:"container_name"`
	Volumes       []composeVolume `json:"volumes"`
	Ports         []composePort   `json:"ports"`
}

type composePort struct {
	Published string `json:"published"`
	Target    int    `json:"target"`
	Protocol  string `json:"protocol"`
}

type composeVolume struct {
	Type   string `json:"type"`
	Source string `json:"source"`
	Target string `json:"target"`
}

type composeConfig struct {
	Services map[string]composeService `json:"services"`
}

func (a *API) composeFile(name string) (string, error) {
	if !safeFolderName(name) {
		return "", os.ErrInvalid
	}
	for _, filename := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		path := filepath.Join(a.Root, name, filename)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			return path, nil
		}
	}
	return "", os.ErrNotExist
}

func (a *API) composeArgs(name string) ([]string, error) {
	file, err := a.composeFile(name)
	if err != nil {
		return nil, err
	}
	return []string{"compose", "-p", projectName(name), "-f", file}, nil
}

func (a *API) minecraftService(name string) (Server, string, error) {
	config, err := a.readComposeConfig(name)
	if err != nil {
		return Server{}, "", err
	}
	return minecraftServiceFromConfig(name, config)
}

func (a *API) readComposeConfig(name string) (composeConfig, error) {
	args, err := a.composeArgs(name)
	if err != nil {
		return composeConfig{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", append(args, "config", "--format", "json")...).Output()
	if err != nil {
		return composeConfig{}, err
	}
	var config composeConfig
	if err := json.Unmarshal(out, &config); err != nil {
		return composeConfig{}, err
	}
	return config, nil
}

func minecraftServiceFromConfig(name string, config composeConfig) (Server, string, error) {
	var server Server
	var serviceName string
	for service, settings := range config.Services {
		edition := ""
		image := strings.SplitN(settings.Image, ":", 2)[0]
		switch image {
		case "itzg/minecraft-server":
			edition = "java"
		case "itzg/minecraft-bedrock-server":
			edition = "bedrock"
		}
		if edition == "" {
			continue
		}
		if serviceName != "" {
			return Server{}, "", errors.New("multiple Minecraft services")
		}
		serviceName = service
		server = Server{Name: name, Edition: edition, Host: os.Getenv("MCUI_PUBLIC_HOST")}
		for _, port := range settings.Ports {
			if (edition == "bedrock" && port.Target == 19132 && port.Protocol == "udp") || (edition == "java" && port.Target == 25565 && (port.Protocol == "tcp" || port.Protocol == "")) {
				server.Port, _ = strconv.Atoi(port.Published)
				server.Protocol = port.Protocol
				break
			}
		}
	}
	if serviceName == "" {
		return Server{}, "", errors.New("no Minecraft service")
	}
	return server, serviceName, nil
}

func (a *API) serverDataSource(name string) (string, error) {
	config, err := a.readComposeConfig(name)
	if err != nil {
		return "", err
	}
	_, service, err := minecraftServiceFromConfig(name, config)
	if err != nil {
		return "", err
	}
	for _, volume := range config.Services[service].Volumes {
		if filepath.Clean(volume.Target) != "/data" {
			continue
		}
		if volume.Type == "bind" && volume.Source != "" {
			return volume.Source, nil
		}
		if volume.Type == "volume" && volume.Source != "" {
			return "", nil
		}
		return "", fmt.Errorf("Minecraft /data volume has unsupported type %q", volume.Type)
	}
	return "", errors.New("Minecraft service has no /data volume")
}

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
	mux.HandleFunc("/api/server-details/", a.serverDetails)
	mux.HandleFunc("/servers/", serverPageHandler("web/dist/index.html"))
	mux.Handle("/", http.FileServer(http.Dir("web/dist")))
	log.Printf("mcui listening on %s; servers in %s", addr, root)
	return http.ListenAndServe(addr, newSessionAuth(mux))
}
func serverPageHandler(index string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/servers/")
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || !safeFolderName(name) {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, index)
	}
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
			if !e.IsDir() || !safeFolderName(e.Name()) {
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
	if len(parts) != 2 || !safeFolderName(parts[0]) || (parts[1] != "start" && parts[1] != "stop" && parts[1] != "backup") {
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
	_, service, err := a.minecraftService(parts[0])
	if err != nil {
		bad(w, 404, "Server not found")
		return
	}
	args, err := a.composeArgs(parts[0])
	if err != nil {
		bad(w, 404, "Server not found")
		return
	}
	if parts[1] == "start" {
		args = append(args, "up", "-d", service)
	} else {
		args = append(args, "stop", service)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		bad(w, 502, fmt.Sprintf("Docker Compose failed: %s", strings.TrimSpace(string(out))))
		return
	}
	s, _ := a.readServer(parts[0])
	s.Status = a.status(r.Context(), parts[0])
	respond(w, 200, s)
}
func (a *API) status(parent context.Context, name string) string {
	_, service, err := a.minecraftService(name)
	if err != nil {
		return "unknown"
	}
	args, err := a.composeArgs(name)
	if err != nil {
		return "unknown"
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append(args, "ps", "-q", service)...)
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
	server, _, err := a.minecraftService(name)
	return server, err
}
func (a *API) create(req CreateRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !safeFolderName(req.Name) {
		return errors.New("Name must be a single folder name")
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
		compose += "      LEVEL_NAME: \"world\"\n    stdin_open: true\n    tty: true\n"
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
