package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func (a *API) resetSource(ctx context.Context, name string) (string, func(), error) {
	source, err := a.serverDataSource(name)
	if err != nil {
		return "", nil, err
	}
	if source != "" {
		return source, func() {}, nil
	}
	stage, err := os.MkdirTemp("", "mcui-reset-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(stage) }
	data := filepath.Join(stage, "data")
	if a.backup == nil {
		cleanup()
		return "", nil, errors.New("backup manager unavailable")
	}
	if err := a.backup.copyContainerData(ctx, name, data); err != nil {
		cleanup()
		return "", nil, err
	}
	return data, cleanup, nil
}

func (a *API) serverReset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/server-reset/")
	if !safeFolderName(name) {
		bad(w, 404, "Server not found")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		bad(w, 405, "Method not allowed")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	server, _, err := a.minecraftService(name)
	if err != nil {
		bad(w, 404, "Server not found")
		return
	}
	if a.backup != nil && a.backup.active(name) {
		bad(w, 409, "Backup in progress")
		return
	}
	if a.status(r.Context(), name) != "stopped" {
		bad(w, 409, "Stop the server before reviewing or resetting files")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	source, cleanup, err := a.resetSource(ctx, name)
	if err != nil {
		bad(w, 500, err.Error())
		return
	}
	defer cleanup()
	plan, err := planData(source, server.Edition)
	if err != nil {
		bad(w, 409, err.Error())
		return
	}
	if r.Method == http.MethodGet {
		respond(w, 200, plan)
		return
	}
	var request struct {
		Confirm  string `json:"confirm"`
		Revision string `json:"revision"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil {
		bad(w, 400, "Invalid confirmation")
		return
	}
	if request.Confirm != name || request.Revision != plan.Revision {
		bad(w, 409, "Reset review changed; review the files again")
		return
	}
	dataSource, err := a.serverDataSource(name)
	if err != nil {
		bad(w, 500, err.Error())
		return
	}
	if dataSource == "" {
		err = a.deleteNamedVolumeFiles(ctx, name, plan)
	} else {
		err = deleteDataFiles(source, plan)
	}
	if err != nil {
		bad(w, 500, err.Error())
		return
	}
	respond(w, 200, map[string]any{"deleted": len(plan.Delete), "kept": len(plan.Keep)})
}

func deleteDataFiles(root string, plan resetPlan) error {
	data, err := os.OpenRoot(root)
	if err != nil {
		return err
	}
	defer data.Close()
	for _, item := range plan.Delete {
		if !assetRelative(item.Path) {
			return fs.ErrInvalid
		}
		info, err := data.Lstat(item.Path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("file changed during reset: %s", item.Path)
		}
		if err := data.Remove(item.Path); err != nil {
			return err
		}
	}
	for _, rel := range plan.DeleteDirs {
		if !assetRelative(rel) {
			return fs.ErrInvalid
		}
		if err := data.Remove(rel); err != nil {
			return err
		}
	}
	return nil
}

func (a *API) deleteNamedVolumeFiles(ctx context.Context, name string, plan resetPlan) error {
	_, service, err := a.minecraftService(name)
	if err != nil {
		return err
	}
	args, err := a.composeArgs(name)
	if err != nil {
		return err
	}
	out, err := exec.CommandContext(ctx, "docker", append(args, "ps", "-a", "-q", service)...).Output()
	if err != nil {
		return fmt.Errorf("find server container: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return errors.New("named data volume has no server container")
	}
	out, err = exec.CommandContext(ctx, "docker", "inspect", "--format", "{{json .Mounts}}", id).Output()
	if err != nil {
		return fmt.Errorf("inspect server volumes: %w", err)
	}
	var mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Destination string `json:"Destination"`
	}
	if err := json.Unmarshal(out, &mounts); err != nil {
		return err
	}
	var volume string
	for _, mount := range mounts {
		if filepath.Clean(mount.Destination) == "/data" && mount.Type == "volume" {
			volume = mount.Name
		}
	}
	if volume == "" {
		return errors.New("server has no named data volume")
	}
	var input strings.Builder
	for _, item := range plan.Delete {
		if strings.ContainsAny(item.Path, "\r\n") {
			return errors.New("newline in data path cannot be reset")
		}
		input.WriteByte('F')
		input.WriteString(base64.StdEncoding.EncodeToString([]byte(item.Path)))
		input.WriteByte('\n')
	}
	for _, dir := range plan.DeleteDirs {
		if strings.ContainsAny(dir, "\r\n") {
			return errors.New("newline in data path cannot be reset")
		}
		input.WriteByte('D')
		input.WriteString(base64.StdEncoding.EncodeToString([]byte(dir)))
		input.WriteByte('\n')
	}
	if input.Len() == 0 {
		return nil
	}
	script := `set -eu
while IFS= read -r encoded; do
  kind=${encoded%"${encoded#?}"}
  path=$(printf '%s' "${encoded#?}" | base64 -d)
  case "$path" in /*|../*|*/../*|*/..|./*|*/./*|*/.|"") exit 1;; esac
  parent="$path"
  while [ "$parent" != "${parent%/*}" ]; do
    parent="${parent%/*}"
    if [ -L "/data/$parent" ]; then exit 1; fi
  done
  if [ -L "/data/$path" ]; then exit 1; fi
  case "$kind" in
    F) [ -f "/data/$path" ] || exit 1; rm -f -- "/data/$path" ;;
    D) [ -d "/data/$path" ] || exit 1; rmdir -- "/data/$path" ;;
    *) exit 1 ;;
  esac
done`
	config, err := a.readComposeConfig(name)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", "--entrypoint", "sh", "-v", volume+":/data", config.Services[service].Image, "-c", script)
	cmd.Stdin = strings.NewReader(input.String())
	out, err = cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("reset named volume: %s: %w", cleanError(out), err)
	}
	return nil
}
