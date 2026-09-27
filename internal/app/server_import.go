package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxBackupUploadBytes int64 = 4<<30 + 1<<20

var driveFileID = regexp.MustCompile(`^[A-Za-z0-9_-]{10,}$`)

func parseCreateMultipart(w http.ResponseWriter, r *http.Request) (CreateRequest, func(), error) {
	cleanup := func() {
		if r.MultipartForm != nil {
			_ = r.MultipartForm.RemoveAll()
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBackupUploadBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		cleanup()
		return CreateRequest{}, nil, fmt.Errorf("read backup upload: %w", err)
	}
	port, err := strconv.Atoi(r.FormValue("port"))
	if err != nil {
		cleanup()
		return CreateRequest{}, nil, errors.New("Invalid port")
	}
	req := CreateRequest{Name: r.FormValue("name"), Edition: r.FormValue("edition"), Port: port, WorldPath: r.FormValue("worldPath"), BackupPath: r.FormValue("backupPath"), BackupURL: r.FormValue("backupUrl"), AcceptEULA: r.FormValue("acceptEula") == "true"}
	files := r.MultipartForm.File["backupFile"]
	if len(files) != 1 {
		cleanup()
		return CreateRequest{}, nil, errors.New("Select one backup file")
	}
	name := strings.ToLower(files[0].Filename)
	if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".tgz") {
		cleanup()
		return CreateRequest{}, nil, errors.New("Backup file must be .tar.gz or .tgz")
	}
	source, err := files[0].Open()
	if err != nil {
		cleanup()
		return CreateRequest{}, nil, err
	}
	defer source.Close()
	archive, err := os.CreateTemp("", "mcui-backup-upload-*.tar.gz")
	if err != nil {
		cleanup()
		return CreateRequest{}, nil, err
	}
	req.backupFile = archive.Name()
	previous := cleanup
	cleanup = func() { previous(); _ = os.Remove(req.backupFile) }
	_, copyErr := io.Copy(archive, source)
	closeErr := archive.Close()
	if copyErr != nil {
		cleanup()
		return CreateRequest{}, nil, copyErr
	}
	if closeErr != nil {
		cleanup()
		return CreateRequest{}, nil, closeErr
	}
	return req, cleanup, nil
}

func parseDriveFileURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() != "drive.google.com" || u.Port() != "" || u.User != nil || u.Fragment != "" {
		return "", errors.New("Use a Google Drive file sharing URL")
	}
	var id string
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 3 && parts[0] == "file" && parts[1] == "d" {
		id = parts[2]
	}
	if u.Path == "/open" {
		id = u.Query().Get("id")
	}
	if !driveFileID.MatchString(id) {
		return "", errors.New("Google Drive link has no valid file ID")
	}
	return id, nil
}

func (a *API) prepareBackupImport(parent context.Context, req CreateRequest) (string, string, func(), error) {
	archive := req.BackupPath
	if req.backupFile != "" {
		archive = req.backupFile
	}
	var id string
	if req.BackupURL != "" {
		var err error
		id, err = parseDriveFileURL(req.BackupURL)
		if err != nil {
			return "", "", nil, err
		}
		if a.backup == nil || !a.backup.ready() {
			return "", "", nil, errors.New("Configure Google Drive backups before importing a Drive link")
		}
	} else {
		if !strings.HasSuffix(strings.ToLower(archive), ".tar.gz") && !strings.HasSuffix(strings.ToLower(archive), ".tgz") {
			return "", "", nil, errors.New("Backup file must be .tar.gz or .tgz")
		}
		info, err := os.Lstat(archive)
		if err != nil {
			return "", "", nil, err
		}
		if !info.Mode().IsRegular() {
			return "", "", nil, errors.New("Backup path must be a regular file")
		}
	}
	stage, err := os.MkdirTemp("", "mcui-backup-import-*")
	if err != nil {
		return "", "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	if id != "" {
		archive = filepath.Join(stage, "backup.tar.gz")
		ctx, cancel := context.WithTimeout(parent, 2*time.Hour)
		defer cancel()
		cmd := exec.CommandContext(ctx, "rclone", "backend", "copyid", a.backup.config.Remote+":", id, archive, "--config", filepath.Join(a.backup.config.Dir, "rclone.conf"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			cleanup()
			return "", "", nil, fmt.Errorf("download Google Drive backup: %s: %w", cleanError(out), err)
		}
	}
	if err := extract(archive, stage); err != nil {
		cleanup()
		return "", "", nil, fmt.Errorf("extract backup: %w", err)
	}
	data := filepath.Join(stage, "data")
	info, err := os.Lstat(data)
	if err != nil || !info.IsDir() {
		cleanup()
		return "", "", nil, errors.New("Backup archive must contain a data/ folder")
	}
	plan, err := planData(data, req.Edition)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	if len(plan.Keep) == 0 && len(plan.Delete) > 0 {
		cleanup()
		return "", "", nil, errors.New("Backup contains no user data for the selected edition")
	}
	worldName, err := backupWorldName(data, req.Edition)
	if err != nil {
		cleanup()
		return "", "", nil, err
	}
	return data, worldName, cleanup, nil
}

func backupWorldName(data, edition string) (string, error) {
	if edition == "java" {
		return "", nil
	}
	if _, err := os.Stat(filepath.Join(data, "world", "level.dat")); err == nil {
		return "", errors.New("This backup appears to contain a Java world; select Java")
	}
	worlds, err := os.ReadDir(filepath.Join(data, "worlds"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var found []string
	for _, world := range worlds {
		if !world.IsDir() {
			continue
		}
		if info, err := os.Lstat(filepath.Join(data, "worlds", world.Name(), "level.dat")); err == nil && info.Mode().IsRegular() {
			found = append(found, world.Name())
		}
	}
	for _, name := range found {
		if name == "world" {
			return name, nil
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) > 1 {
		return "", errors.New("Backup has multiple Bedrock worlds and no world named world")
	}
	return "", nil
}
