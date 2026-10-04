package app

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type BackupState struct {
	State       string     `json:"state"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	SnapshotID  string     `json:"snapshotId,omitempty"`
	Error       string     `json:"error,omitempty"`
}
type BackupConfig struct {
	Dir      string
	Remote   string
	Path     string
	Interval time.Duration
}
type BackupManager struct {
	api           *API
	config        BackupConfig
	mu            sync.Mutex
	states        map[string]BackupState
	remoteStates  map[string]BackupState
	remoteChecked map[string]time.Time
	slots         chan struct{}
}

var validRemote = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func backupConfig() (BackupConfig, error) {
	dir := os.Getenv("MCUI_BACKUP_DIR")
	if dir == "" {
		dir = "./backup"
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return BackupConfig{}, err
	}
	remote := os.Getenv("MCUI_BACKUP_REMOTE")
	if remote == "" {
		remote = "drive"
	}
	if !validRemote.MatchString(remote) {
		return BackupConfig{}, errors.New("MCUI_BACKUP_REMOTE must be a simple rclone remote name")
	}
	path := os.Getenv("MCUI_BACKUP_PATH")
	if path == "" {
		path = "mcui"
	}
	if path == "." || path == ".." || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") || strings.Contains(path, "\\") || strings.Contains(path, ":") || strings.Contains(path, "//") {
		return BackupConfig{}, errors.New("MCUI_BACKUP_PATH must be a relative Drive folder")
	}
	for _, part := range strings.Split(path, "/") {
		if part == "." || part == ".." || !validName.MatchString(part) {
			return BackupConfig{}, errors.New("MCUI_BACKUP_PATH may contain only lowercase folder names, numbers, and hyphens")
		}
	}
	interval := 24 * time.Hour
	if value := os.Getenv("MCUI_BACKUP_INTERVAL"); value == "0" {
		interval = 0
	} else if value != "" {
		interval, err = time.ParseDuration(value)
		if err != nil || interval < time.Minute {
			return BackupConfig{}, errors.New("MCUI_BACKUP_INTERVAL must be 0 or a duration of at least 1m")
		}
	}
	return BackupConfig{Dir: abs, Remote: remote, Path: path, Interval: interval}, nil
}
func newBackupManager(a *API) (*BackupManager, error) {
	config, err := backupConfig()
	if err != nil {
		return nil, err
	}
	return &BackupManager{api: a, config: config, states: map[string]BackupState{}, remoteStates: map[string]BackupState{}, remoteChecked: map[string]time.Time{}, slots: make(chan struct{}, 1)}, nil
}
func (b *BackupManager) ready() bool {
	if _, err := exec.LookPath("rclone"); err != nil {
		return false
	}
	configPath := filepath.Join(b.config.Dir, "rclone.conf")
	conf, err := os.Stat(configPath)
	if err != nil || !conf.Mode().IsRegular() {
		return false
	}
	content, err := os.ReadFile(configPath)
	if err != nil || !hasDriveRemote(string(content), b.config.Remote) {
		return false
	}
	return true
}
func hasDriveRemote(config, remote string) bool {
	inSection := false
	for _, line := range strings.Split(config, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSection = strings.TrimSpace(line[1:len(line)-1]) == remote
			continue
		}
		if inSection {
			key, value, ok := strings.Cut(line, "=")
			if ok && strings.TrimSpace(key) == "type" && strings.TrimSpace(value) == "drive" {
				return true
			}
		}
	}
	return false
}
func (b *BackupManager) repo(name string) string {
	return b.config.Remote + ":" + b.config.Path + "/" + name
}
func removeLegacyBackupMarkers(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return err
	}
	a := API{Root: root}
	for _, entry := range entries {
		if !entry.IsDir() || !safeFolderName(entry.Name()) {
			continue
		}
		if _, err := a.composeFile(entry.Name()); err != nil {
			continue
		}
		path := filepath.Join(root, entry.Name(), "last-backup.json")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
func (b *BackupManager) state(name string) BackupState {
	b.mu.Lock()
	s, ok := b.states[name]
	remote, cached := b.remoteStates[name]
	checked := b.remoteChecked[name]
	b.mu.Unlock()
	if ok {
		return s
	}
	if cached && time.Since(checked) < time.Minute {
		return remote
	}
	if !b.ready() {
		return BackupState{State: "idle"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "rclone", "lsf", "--config", filepath.Join(b.config.Dir, "rclone.conf"), "--files-only", b.repo(name)).Output()
	if err == nil {
		remote = latestBackupFromListing(name, string(out))
	} else {
		remote = BackupState{State: "idle"}
	}
	b.mu.Lock()
	b.remoteStates[name] = remote
	b.remoteChecked[name] = time.Now()
	b.mu.Unlock()
	return remote
}
func latestBackupFromListing(name, listing string) BackupState {
	latest := BackupState{State: "idle"}
	var newest time.Time
	for _, filename := range strings.Split(listing, "\n") {
		stamp, ok := strings.CutPrefix(strings.TrimSpace(filename), name+"-")
		if !ok || !strings.HasSuffix(stamp, ".tar.gz") {
			continue
		}
		stamp = strings.TrimSuffix(stamp, ".tar.gz")
		when, err := time.Parse("20060102T150405.000000000Z", stamp)
		if err != nil || !when.After(newest) {
			continue
		}
		newest = when
		latest = BackupState{State: "complete", CompletedAt: &when, SnapshotID: strings.TrimSpace(filename)}
	}
	return latest
}
func (b *BackupManager) capturing(name string) bool {
	s := b.state(name)
	return s.State == "capturing"
}
func (b *BackupManager) active(name string) bool {
	s := b.state(name)
	return s.State == "capturing" || s.State == "uploading" || s.State == "queued"
}
func (b *BackupManager) setState(name string, s BackupState) {
	b.mu.Lock()
	if s.State == "failed" {
		if last := b.remoteStates[name]; last.State == "complete" {
			s.SnapshotID = last.SnapshotID
		}
	}
	b.states[name] = s
	if s.State == "complete" {
		b.remoteStates[name] = s
		b.remoteChecked[name] = time.Now()
	}
	b.mu.Unlock()
}
func (b *BackupManager) start(name string) error {
	b.api.mu.Lock()
	defer b.api.mu.Unlock()
	if b.api.dimensionBusy(name) {
		return errors.New("A custom dimension import is in progress")
	}
	if _, err := b.api.readServer(name); err != nil {
		return errors.New("Server not found")
	}
	if !b.ready() {
		return errors.New("Backups are not configured: add rclone.conf to the backup directory")
	}
	if b.active(name) {
		return errors.New("Backup already in progress")
	}
	now := time.Now().UTC()
	b.setState(name, BackupState{State: "queued", StartedAt: &now})
	go b.run(name, now)
	return nil
}
func (b *BackupManager) run(name string, started time.Time) {
	b.slots <- struct{}{}
	defer func() { <-b.slots }()
	b.api.mu.Lock()
	state := BackupState{State: "capturing", StartedAt: &started}
	b.setState(name, state)
	b.api.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()
	stage, err := b.capture(ctx, name)
	if err == nil {
		state.State = "uploading"
		b.setState(name, state)
		state.SnapshotID, err = b.upload(ctx, name, stage)
		cleanupErr := os.RemoveAll(stage)
		if err == nil && cleanupErr != nil {
			err = fmt.Errorf("remove backup staging data: %w", cleanupErr)
		}
	}
	finished := time.Now().UTC()
	state.CompletedAt = &finished
	if err != nil {
		state.State = "failed"
		state.Error = err.Error()
	} else {
		state.State = "complete"
	}
	b.setState(name, state)
}
func (b *BackupManager) capture(ctx context.Context, name string) (string, error) {
	dataSource, err := b.api.serverDataSource(name)
	if err != nil {
		return "", fmt.Errorf("resolve server data volume: %w", err)
	}
	status := b.api.status(ctx, name)
	if status != "running" && status != "stopped" {
		return "", fmt.Errorf("Cannot back up server with status %s", status)
	}
	stageBase := filepath.Join(b.api.Root, ".backup-stage")
	if err := os.MkdirAll(stageBase, 0700); err != nil {
		return "", err
	}
	stage := filepath.Join(stageBase, name)
	if err := os.RemoveAll(stage); err != nil {
		return "", err
	}
	if err := os.Mkdir(stage, 0700); err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(stage)
		}
	}()
	wasRunning := status == "running"
	if wasRunning {
		if err := b.compose(ctx, name, "stop", "-t", "120"); err != nil {
			restartErr := b.compose(ctx, name, "up", "-d")
			if restartErr != nil {
				return "", fmt.Errorf("stop server before backup: %v; restart attempt failed: %w", err, restartErr)
			}
			return "", fmt.Errorf("stop server before backup: %w", err)
		}
	}
	server, _, editionErr := b.api.minecraftService(name)
	if editionErr != nil {
		err = editionErr
	} else if dataSource == "" {
		raw := filepath.Join(stage, ".raw")
		err = b.copyContainerData(ctx, name, raw)
		if err == nil {
			err = copyBackupTree(raw, filepath.Join(stage, "data"), server.Edition)
		}
		if removeErr := os.RemoveAll(raw); err == nil {
			err = removeErr
		}
	} else {
		err = copyBackupTree(dataSource, filepath.Join(stage, "data"), server.Edition)
	}
	if wasRunning {
		restartErr := b.compose(ctx, name, "up", "-d")
		if restartErr != nil {
			if err != nil {
				return "", fmt.Errorf("capture failed: %v; restart failed: %w", err, restartErr)
			}
			return "", fmt.Errorf("restart after backup capture: %w", restartErr)
		}
	}
	if err != nil {
		return "", fmt.Errorf("capture server files: %w", err)
	}
	keep = true
	return stage, nil
}
func (b *BackupManager) copyContainerData(ctx context.Context, name, destination string) error {
	_, service, err := b.api.minecraftService(name)
	if err != nil {
		return err
	}
	args, err := b.api.composeArgs(name)
	if err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "docker", append(args, "ps", "-a", "-q", service)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("find container for named data volume: %s: %w", strings.TrimSpace(string(out)), err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return errors.New("named data volume has no server container to copy from")
	}
	if err := os.Mkdir(destination, 0700); err != nil {
		return err
	}
	out, err = exec.CommandContext(ctx, "docker", "cp", id+":/data/.", destination).CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy named data volume: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	closeErr := out.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func (b *BackupManager) compose(parent context.Context, name string, args ...string) error {
	_, service, err := b.api.minecraftService(name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	command, err := b.api.composeArgs(name)
	if err != nil {
		return err
	}
	command = append(command, append(args, service)...)
	out, err := exec.CommandContext(ctx, "docker", command...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
func (b *BackupManager) upload(parent context.Context, name, stage string) (string, error) {
	filename := name + "-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".tar.gz"
	archive := filepath.Join(filepath.Dir(stage), filename)
	defer os.Remove(archive)
	if err := writeBackupArchive(stage, archive); err != nil {
		return "", fmt.Errorf("compress backup: %w", err)
	}
	cmd := exec.CommandContext(parent, "rclone", "copyto", "--config", filepath.Join(b.config.Dir, "rclone.conf"), archive, b.repo(name)+"/"+filename)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("upload backup: %s: %w", cleanError(out), err)
	}
	return filename, nil
}
func writeBackupArchive(source, destination string) (err error) {
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
	}()
	gz := gzip.NewWriter(out)
	tarWriter := tar.NewWriter(gz)
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if header.Linkname, err = os.Readlink(path); err != nil {
				return err
			}
		}
		header.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			header.Name += "/"
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if closeErr := tarWriter.Close(); err == nil {
		err = closeErr
	}
	if closeErr := gz.Close(); err == nil {
		err = closeErr
	}
	return err
}
func cleanError(out []byte) string {
	value := strings.TrimSpace(string(out))
	if len(value) > 2000 {
		value = value[len(value)-2000:]
	}
	if value == "" {
		return "command failed"
	}
	return value
}
func (a *API) backupConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		bad(w, 405, "Method not allowed")
		return
	}
	respond(w, 200, map[string]any{"configured": a.backup.ready(), "interval": a.backup.config.Interval.String(), "destination": "Google Drive"})
}
func (a *API) backupAction(w http.ResponseWriter, r *http.Request, name string) {
	if !safeFolderName(name) {
		bad(w, 404, "Not found")
		return
	}
	if _, err := a.readServer(name); err != nil {
		bad(w, 404, "Server not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		respond(w, 200, a.backup.state(name))
	case http.MethodPost:
		if err := a.backup.start(name); err != nil {
			if strings.Contains(err.Error(), "already in progress") {
				bad(w, 409, err.Error())
			} else {
				bad(w, 400, err.Error())
			}
			return
		}
		respond(w, 202, a.backup.state(name))
	default:
		w.Header().Set("Allow", "GET, POST")
		bad(w, 405, "Method not allowed")
	}
}
func (b *BackupManager) schedule(ctx context.Context) {
	if b.config.Interval == 0 {
		return
	}
	ticker := time.NewTicker(b.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !b.ready() {
				continue
			}
			entries, err := os.ReadDir(b.api.Root)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if entry.IsDir() && safeFolderName(entry.Name()) {
					if _, err := b.api.readServer(entry.Name()); err == nil {
						_ = b.start(entry.Name())
					}
				}
			}
		}
	}
}
