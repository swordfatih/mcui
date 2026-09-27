package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type assetRecord struct {
	Path     string       `json:"path"`
	Layer    string       `json:"layer"`
	Size     int64        `json:"size"`
	Archived bool         `json:"archived"`
	Hash     string       `json:"hash,omitempty"`
	Patches  []assetPatch `json:"patches,omitempty"`
}
type assetState struct {
	UUID   string        `json:"uuid"`
	Assets []assetRecord `json:"assets"`
}
type assetLayer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Selected bool   `json:"selected"`
}
type assetListing struct {
	Layers []assetLayer  `json:"layers"`
	Assets []assetRecord `json:"assets"`
}
type assetChange struct {
	Action  string   `json:"action"`
	Layer   string   `json:"layer"`
	Paths   []string `json:"paths"`
	Preview bool     `json:"preview,omitempty"`
}

func assetRelative(path string) bool {
	if path == "" || strings.Contains(path, "\\") || strings.HasPrefix(path, "/") {
		return false
	}
	clean := filepath.Clean(path)
	return clean == path && clean != "." && clean != ".." && !strings.HasPrefix(clean, ".."+string(filepath.Separator))
}
func managedPackFile(path string) bool {
	switch path {
	case "manifest.json", "pack_icon.png", "blocks.json", "textures/terrain_texture.json", "textures/flipbook_textures.json", "textures/flipbook_texture.json", "textures/textures_list.json":
		return true
	}
	return false
}
func assetPath(root, rel string) (string, error) {
	if !assetRelative(rel) {
		return "", errors.New("Invalid asset path")
	}
	current := root
	for _, part := range strings.Split(rel, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("Symlinked assets are not supported")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return current, nil
}
func safeAssetStorage(serverRoot, target string) error {
	rel, err := filepath.Rel(serverRoot, target)
	if err != nil || !assetRelative(filepath.ToSlash(rel)) {
		return errors.New("Invalid asset storage path")
	}
	current := serverRoot
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Symlinked asset storage is not supported")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
func assetHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func moveAssetFile(from, to string) error {
	if err := os.Rename(from, to); err == nil {
		return nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return err
	}
	input, err := os.Open(from)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(to), ".asset-move-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, input); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), to); err != nil {
		return err
	}
	if err := os.Remove(from); err != nil {
		_ = os.Remove(to)
		return err
	}
	return nil
}
func pruneAssetDirs(path, stop string) {
	for dir := filepath.Dir(path); dir != stop && strings.HasPrefix(dir, stop+string(filepath.Separator)); dir = filepath.Dir(dir) {
		if err := os.Remove(dir); err != nil {
			return
		}
	}
}
func writeAssetJSON(path string, content []byte) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("Reference file is not regular")
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".asset-json-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
func writeAssetState(path string, state assetState) error {
	if len(state.Assets) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(content, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
func readAssetState(path, uuid string) (assetState, error) {
	state := assetState{UUID: uuid, Assets: []assetRecord{}}
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(content, &state); err != nil {
		return state, err
	}
	if !strings.EqualFold(state.UUID, uuid) {
		return state, errors.New("Archived assets belong to another pack UUID")
	}
	return state, nil
}
func assetLayerPath(packDir, layer string) (string, error) {
	if layer == "main" {
		return packDir, nil
	}
	if !safeFolderName(layer) {
		return "", errors.New("Invalid subpack")
	}
	return filepath.Join(packDir, "subpacks", layer), nil
}
func (a *API) packAssets(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/pack-assets/"), "/")
	if len(parts) != 3 || !safeFolderName(parts[0]) || parts[1] != "resource" || !safeFolderName(parts[2]) {
		bad(w, 404, "Pack not found")
		return
	}
	name, folder := parts[0], parts[2]
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		bad(w, 405, "Method not allowed")
		return
	}
	if r.Method == http.MethodPost {
		a.mu.Lock()
		defer a.mu.Unlock()
	}
	data, world, err := a.packPaths(name)
	if err != nil {
		bad(w, 400, err.Error())
		return
	}
	packDir := filepath.Join(data, "resource_packs", folder)
	info, err := os.Lstat(packDir)
	if err != nil || !info.IsDir() || builtInPack("resource", folder) {
		bad(w, 404, "Pack not found")
		return
	}
	manifest, err := readPackManifest(filepath.Join(packDir, "manifest.json"))
	if err != nil || packKind(manifest) != "resource" {
		bad(w, 404, "Pack not found")
		return
	}
	layers := []assetLayer{{ID: "main", Name: "Main pack"}}
	allowed := map[string]bool{"main": true}
	refs, err := readPackRefs(filepath.Join(world, "world_resource_packs.json"))
	if err != nil {
		bad(w, 400, err.Error())
		return
	}
	selected := ""
	packActive := false
	for _, ref := range refs {
		if strings.EqualFold(ref.PackID, manifest.Header.UUID) {
			selected = ref.Subpack
			packActive = true
			break
		}
	}
	layers[0].Selected = packActive && selected == ""
	for _, sub := range manifest.Subpacks {
		if !safeFolderName(sub.FolderName) || allowed[sub.FolderName] {
			continue
		}
		layerPath := filepath.Join(packDir, "subpacks", sub.FolderName)
		if info, err := os.Lstat(layerPath); err != nil || !info.IsDir() {
			continue
		}
		label := sub.Name
		if label == "" {
			label = sub.FolderName
		}
		layers = append(layers, assetLayer{ID: sub.FolderName, Name: label, Selected: packActive && selected == sub.FolderName})
		allowed[sub.FolderName] = true
	}
	root := filepath.Join(a.Root, name, ".mcui")
	statePath := filepath.Join(root, "asset-state", "resource", folder+".json")
	if err := safeAssetStorage(filepath.Join(a.Root, name), statePath); err != nil {
		bad(w, 409, err.Error())
		return
	}
	state, err := readAssetState(statePath, manifest.Header.UUID)
	if err != nil {
		bad(w, 409, err.Error())
		return
	}
	if r.Method == http.MethodGet && r.URL.Query().Get("content") == "1" {
		layer, rel := r.URL.Query().Get("layer"), r.URL.Query().Get("path")
		if !allowed[layer] || !assetRelative(rel) {
			bad(w, 400, "Invalid asset path")
			return
		}
		ext := strings.ToLower(filepath.Ext(rel))
		if ext != ".png" && ext != ".jpg" && ext != ".jpeg" && ext != ".gif" && ext != ".webp" && ext != ".tga" {
			bad(w, 415, "Preview unavailable")
			return
		}
		layerDir, _ := assetLayerPath(packDir, layer)
		for _, item := range state.Assets {
			if item.Layer == layer && item.Path == rel {
				layerDir = filepath.Join(root, "archived-assets", "resource", folder, layer)
				break
			}
		}
		if err := safeAssetStorage(filepath.Join(a.Root, name), filepath.Join(root, "archived-assets", "resource", folder, layer)); err != nil {
			bad(w, 409, err.Error())
			return
		}
		path, err := assetPath(layerDir, rel)
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			bad(w, 404, "Asset not found")
			return
		}
		if ext == ".tga" {
			if info.Size() > 64<<20 {
				bad(w, 413, "Image is too large to preview")
				return
			}
			if err := serveTGAPreview(w, path); err != nil {
				bad(w, 415, err.Error())
			}
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, path)
		return
	}
	if r.Method == http.MethodGet {
		result := assetListing{Layers: layers, Assets: []assetRecord{}}
		for _, layer := range layers {
			layerDir, _ := assetLayerPath(packDir, layer.ID)
			err := filepath.WalkDir(layerDir, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if entry.Type()&os.ModeSymlink != 0 {
					return nil
				}
				if entry.IsDir() {
					if layer.ID == "main" && path == filepath.Join(packDir, "subpacks") {
						return filepath.SkipDir
					}
					return nil
				}
				if !entry.Type().IsRegular() {
					return nil
				}
				rel, err := filepath.Rel(layerDir, path)
				if err != nil {
					return err
				}
				if layer.ID == "main" && (rel == "manifest.json" || rel == "pack_icon.png") {
					return nil
				}
				info, err := entry.Info()
				if err != nil {
					return err
				}
				result.Assets = append(result.Assets, assetRecord{Path: filepath.ToSlash(rel), Layer: layer.ID, Size: info.Size()})
				return nil
			})
			if err != nil {
				bad(w, 500, err.Error())
				return
			}
		}
		for _, archived := range state.Assets {
			archived.Archived = true
			result.Assets = append(result.Assets, archived)
		}
		sort.Slice(result.Assets, func(i, j int) bool {
			if result.Assets[i].Layer != result.Assets[j].Layer {
				return result.Assets[i].Layer < result.Assets[j].Layer
			}
			return result.Assets[i].Path < result.Assets[j].Path
		})
		respond(w, 200, result)
		return
	}
	if a.backup != nil && a.backup.capturing(name) {
		bad(w, 409, "Server is being captured for backup")
		return
	}
	if a.status(r.Context(), name) != "stopped" {
		bad(w, 409, "Stop the server before changing assets")
		return
	}
	var change assetChange
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&change); err != nil {
		bad(w, 400, "Invalid asset request")
		return
	}
	if (change.Action != "archive" && change.Action != "restore") || !allowed[change.Layer] || len(change.Paths) == 0 || len(change.Paths) > 100 {
		bad(w, 400, "Invalid asset request")
		return
	}
	layerDir, _ := assetLayerPath(packDir, change.Layer)
	archiveDir := filepath.Join(root, "archived-assets", "resource", folder, change.Layer)
	if err := safeAssetStorage(filepath.Join(a.Root, name), archiveDir); err != nil {
		bad(w, 409, err.Error())
		return
	}
	seen := map[string]bool{}
	type move struct {
		from, to string
		record   assetRecord
	}
	moves := []move{}
	for _, rel := range change.Paths {
		if !assetRelative(rel) || seen[rel] || managedPackFile(rel) {
			bad(w, 400, "Invalid or duplicate asset path")
			return
		}
		seen[rel] = true
		live, err := assetPath(layerDir, rel)
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
		archived, err := assetPath(archiveDir, rel)
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
		source, dest := live, archived
		if change.Action == "restore" {
			source, dest = archived, live
		}
		sourceInfo, err := os.Lstat(source)
		if err != nil || !sourceInfo.Mode().IsRegular() {
			bad(w, 409, "Asset is missing or not a regular file: "+rel)
			return
		}
		if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
			bad(w, 409, "Destination already exists: "+rel)
			return
		}
		hash, err := assetHash(source)
		if err != nil {
			bad(w, 500, err.Error())
			return
		}
		record := assetRecord{Path: rel, Layer: change.Layer, Size: sourceInfo.Size(), Hash: hash}
		if change.Action == "restore" {
			found := false
			for _, item := range state.Assets {
				if item.Layer == change.Layer && item.Path == rel {
					found = true
					if item.Hash != hash {
						bad(w, 409, "Archived asset changed: "+rel)
						return
					}
					record.Patches = item.Patches
					break
				}
			}
			if !found {
				bad(w, 409, "Asset is not in archive index: "+rel)
				return
			}
		} else {
			for _, item := range state.Assets {
				if item.Layer == change.Layer && item.Path == rel {
					bad(w, 409, "Asset is already archived: "+rel)
					return
				}
			}
		}
		moves = append(moves, move{source, dest, record})
	}
	records := make([]assetRecord, len(moves))
	for i, item := range moves {
		records[i] = item.record
	}
	edits, records, warnings, err := referenceEdits(layerDir, change.Action, records)
	if err != nil {
		bad(w, 409, err.Error())
		return
	}
	changes := []string{}
	for _, record := range records {
		for _, patch := range record.Patches {
			label := patch.File
			if patch.Key != "" {
				label += " · " + patch.Key
			}
			changes = append(changes, label)
		}
	}
	if change.Preview {
		respond(w, 200, map[string]any{"count": len(moves), "edits": len(edits), "changes": changes, "warnings": warnings})
		return
	}
	for i := range moves {
		moves[i].record = records[i]
	}
	completed := []move{}
	for _, item := range moves {
		if err := os.MkdirAll(filepath.Dir(item.to), 0755); err != nil {
			break
		}
		if err := moveAssetFile(item.from, item.to); err != nil {
			break
		}
		completed = append(completed, item)
	}
	if len(completed) != len(moves) {
		for i := len(completed) - 1; i >= 0; i-- {
			_ = moveAssetFile(completed[i].to, completed[i].from)
		}
		bad(w, 500, "Could not move all assets")
		return
	}
	written := []assetFileEdit{}
	for _, edit := range edits {
		if err := writeAssetJSON(edit.Path, edit.After); err != nil {
			for i := len(written) - 1; i >= 0; i-- {
				_ = writeAssetJSON(written[i].Path, written[i].Before)
			}
			for i := len(completed) - 1; i >= 0; i-- {
				_ = moveAssetFile(completed[i].to, completed[i].from)
			}
			bad(w, 500, "Could not update asset references: "+err.Error())
			return
		}
		written = append(written, edit)
	}
	next := assetState{UUID: state.UUID, Assets: []assetRecord{}}
	if change.Action == "archive" {
		next.Assets = append(next.Assets, state.Assets...)
		for _, item := range moves {
			next.Assets = append(next.Assets, item.record)
		}
	} else {
		for _, item := range state.Assets {
			if item.Layer != change.Layer || !seen[item.Path] {
				next.Assets = append(next.Assets, item)
			}
		}
	}
	if err := writeAssetState(statePath, next); err != nil {
		for i := len(written) - 1; i >= 0; i-- {
			_ = writeAssetJSON(written[i].Path, written[i].Before)
		}
		for i := len(completed) - 1; i >= 0; i-- {
			_ = moveAssetFile(completed[i].to, completed[i].from)
		}
		bad(w, 500, fmt.Sprintf("Could not update asset index: %v", err))
		return
	}
	if change.Action == "restore" {
		for _, item := range moves {
			pruneAssetDirs(item.from, filepath.Join(root, "archived-assets"))
		}
		if len(next.Assets) == 0 {
			pruneAssetDirs(statePath, root)
		}
	}
	respond(w, 200, map[string]any{"count": len(moves), "edits": len(edits), "changes": changes, "warnings": warnings})
}
