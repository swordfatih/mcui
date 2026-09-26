package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	api    *API
	config BackupConfig
	mu     sync.Mutex
	states map[string]BackupState
	slots  chan struct{}
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
	return &BackupManager{api: a, config: config, states: map[string]BackupState{}, slots: make(chan struct{}, 1)}, nil
}
func (b *BackupManager) ready() bool {
	if _, err := exec.LookPath("restic"); err != nil {
		return false
	}
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
	pass, err := os.Stat(filepath.Join(b.config.Dir, "restic-password"))
	return err == nil && pass.Mode().IsRegular() && pass.Size() > 0
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
	return "rclone:" + b.config.Remote + ":" + b.config.Path + "/" + name
}
func (b *BackupManager) environment(name string) []string {
	env := os.Environ()
	env = append(env, "RCLONE_CONFIG="+filepath.Join(b.config.Dir, "rclone.conf"), "RESTIC_PASSWORD_FILE="+filepath.Join(b.config.Dir, "restic-password"), "RESTIC_REPOSITORY="+b.repo(name), "RESTIC_CACHE_DIR="+filepath.Join(b.config.Dir, "cache"))
	return env
}
func (b *BackupManager) state(name string) BackupState {
	b.mu.Lock()
	s, ok := b.states[name]
	b.mu.Unlock()
	if ok {
		return s
	}
	data, err := os.ReadFile(filepath.Join(b.api.Root, name, "last-backup.json"))
	if err == nil && json.Unmarshal(data, &s) == nil && s.State == "complete" {
		return s
	}
	return BackupState{State: "idle"}
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
	if s.State == "complete" {
		dir := filepath.Join(b.api.Root, name)
		if data, err := json.Marshal(s); err == nil {
			tmp := filepath.Join(dir, "last-backup.json.tmp")
			if err := os.WriteFile(tmp, data, 0600); err == nil {
				_ = os.Rename(tmp, filepath.Join(dir, "last-backup.json"))
			}
		}
	}
	b.mu.Lock()
	b.states[name] = s
	b.mu.Unlock()
}
func (b *BackupManager) start(name string) error {
	b.api.mu.Lock()
	defer b.api.mu.Unlock()
	if _, err := b.api.readServer(name); err != nil {
		return errors.New("Server not found")
	}
	if !b.ready() {
		return errors.New("Backups are not configured: add rclone.conf and restic-password to the backup directory")
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
	serverDir := filepath.Join(b.api.Root, name)
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
	err := copyBackupTree(filepath.Join(serverDir, "data"), filepath.Join(stage, "data"))
	if err == nil {
		err = copyFile(filepath.Join(serverDir, "compose.yaml"), filepath.Join(stage, "compose.yaml"))
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
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	command := []string{"compose", "-p", "mcui-" + name, "-f", filepath.Join(b.api.Root, name, "compose.yaml")}
	command = append(command, args...)
	out, err := exec.CommandContext(ctx, "docker", command...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker compose: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
func (b *BackupManager) upload(parent context.Context, name, stage string) (string, error) {
	if err := os.MkdirAll(filepath.Join(b.config.Dir, "cache"), 0700); err != nil {
		return "", err
	}
	env := b.environment(name)
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(parent, "restic", args...)
		cmd.Env = env
		return cmd.CombinedOutput()
	}
	if _, err := run("cat", "config"); err != nil {
		if out, initErr := run("init"); initErr != nil {
			return "", fmt.Errorf("restic repository unavailable: %s", cleanError(out))
		}
	}
	out, err := run("backup", "--json", "--tag", "mcui:"+name, stage)
	if err != nil {
		return "", fmt.Errorf("restic backup failed: %s", cleanError(out))
	}
	var summary struct {
		MessageType string `json:"message_type"`
		SnapshotID  string `json:"snapshot_id"`
	}
	for _, line := range bytes.Split(out, []byte("\n")) {
		var item struct {
			MessageType string `json:"message_type"`
			SnapshotID  string `json:"snapshot_id"`
		}
		if json.Unmarshal(line, &item) == nil && item.MessageType == "summary" {
			summary = item
		}
	}
	if summary.SnapshotID == "" {
		return "", errors.New("restic backup completed without a snapshot ID")
	}
	return summary.SnapshotID, nil
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
	if !validName.MatchString(name) {
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
				if entry.IsDir() && validName.MatchString(entry.Name()) {
					if _, err := b.api.readServer(entry.Name()); err == nil {
						_ = b.start(entry.Name())
					}
				}
			}
		}
	}
}
