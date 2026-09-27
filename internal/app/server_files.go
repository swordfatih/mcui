package app

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type fileEntry struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Directory bool      `json:"directory"`
	Size      int64     `json:"size"`
	Modified  time.Time `json:"modified"`
}
type fileListing struct {
	Path    string      `json:"path"`
	Entries []fileEntry `json:"entries"`
}
type cleanupItem struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Size   int64  `json:"size"`
}
type cleanupListing struct {
	Items []cleanupItem `json:"items"`
}

func serverFilePath(root, rel string) (string, error) {
	if rel == "" {
		return root, nil
	}
	if !assetRelative(rel) {
		return "", errors.New("Invalid file path")
	}
	current := root
	for _, part := range strings.Split(rel, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("Symlinked paths are unavailable")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return current, nil
}
func protectedServerFile(rel string) bool {
	if rel == "" || rel == ".mcui" || strings.HasPrefix(rel, ".mcui/") || rel == "bedrock_data" {
		return true
	}
	if !strings.Contains(rel, "/") && (strings.HasPrefix(rel, "compose.") || strings.HasPrefix(rel, "docker-compose.") || rel == "last-backup.json") {
		return true
	}
	parts := strings.Split(rel, "/")
	if len(parts) >= 2 && parts[0] == "bedrock_data" {
		if len(parts) == 2 && (parts[1] == "definitions" || parts[1] == "treatments") {
			return true
		}
		if len(parts) == 2 && (parts[1] == "worlds" || parts[1] == "resource_packs" || parts[1] == "behavior_packs") {
			return true
		}
		if len(parts) >= 3 && parts[1] == "worlds" {
			return true
		}
		if len(parts) >= 3 && (parts[1] == "resource_packs" || parts[1] == "behavior_packs") && builtInPack(strings.TrimSuffix(parts[1], "_packs"), parts[2]) {
			return true
		}
	}
	return false
}
func fileTreeSize(path string) int64 {
	var size int64
	_ = filepath.WalkDir(path, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.Type().IsRegular() {
			if info, err := entry.Info(); err == nil {
				size += info.Size()
			}
		}
		return nil
	})
	return size
}
func (a *API) fileCleanup(name, root string) cleanupListing {
	result := cleanupListing{Items: []cleanupItem{}}
	data := filepath.Join(root, "bedrock_data")
	if info, err := os.Lstat(data); err != nil || !info.IsDir() {
		return result
	}
	entries, _ := os.ReadDir(data)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.HasPrefix(strings.ToLower(entry.Name()), "backup-pre-") {
			continue
		}
		rel := "bedrock_data/" + entry.Name()
		result.Items = append(result.Items, cleanupItem{Path: rel, Name: entry.Name(), Kind: "Pre-update backup", Reason: "A snapshot beside the live Bedrock data. It is not the active server directory.", Size: fileTreeSize(filepath.Join(data, entry.Name()))})
	}
	for _, kind := range []string{"behavior_packs", "resource_packs"} {
		entries, _ := os.ReadDir(filepath.Join(data, kind))
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !strings.Contains(strings.ToLower(entry.Name()), ".backup-") {
				continue
			}
			rel := "bedrock_data/" + kind + "/" + entry.Name()
			result.Items = append(result.Items, cleanupItem{Path: rel, Name: entry.Name(), Kind: "Pack folder backup", Reason: "The folder name marks this as a saved copy of a pack. Check its contents before deleting.", Size: fileTreeSize(filepath.Join(data, kind, entry.Name()))})
		}
	}
	_, world, err := a.packPaths(name)
	if err == nil {
		for _, kind := range []string{"behavior", "resource"} {
			refs, err := readPackRefs(filepath.Join(world, packFile(kind)))
			if err != nil {
				continue
			}
			selected := map[string]bool{}
			for _, ref := range refs {
				selected[strings.ToLower(ref.PackID)] = true
			}
			folder := packFolder(kind)
			entries, _ := os.ReadDir(filepath.Join(data, folder))
			for _, entry := range entries {
				if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || strings.Contains(strings.ToLower(entry.Name()), ".backup-") || builtInPack(kind, entry.Name()) {
					continue
				}
				manifest, err := readPackManifest(filepath.Join(data, folder, entry.Name(), "manifest.json"))
				if err != nil || packKind(manifest) != kind || selected[strings.ToLower(manifest.Header.UUID)] {
					continue
				}
				rel := "bedrock_data/" + folder + "/" + entry.Name()
				result.Items = append(result.Items, cleanupItem{Path: rel, Name: entry.Name(), Kind: "Unselected " + kind + " pack", Reason: "Its UUID is absent from this world’s pack JSON. It may still be kept for later or for another world.", Size: fileTreeSize(filepath.Join(data, folder, entry.Name()))})
			}
		}
	}
	sort.Slice(result.Items, func(i, j int) bool { return result.Items[i].Path < result.Items[j].Path })
	return result
}
func downloadServerFile(w http.ResponseWriter, r *http.Request, path, name string, directory bool) {
	if !directory {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
		http.ServeFile(w, r, path)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".zip"))
	writer := zip.NewWriter(w)
	_ = filepath.WalkDir(path, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(filepath.Dir(path), current)
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return nil
		}
		header.Name = filepath.ToSlash(rel)
		header.Method = zip.Deflate
		dest, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		source, err := os.Open(current)
		if err != nil {
			return nil
		}
		_, copyErr := io.Copy(dest, source)
		source.Close()
		return copyErr
	})
	_ = writer.Close()
}
func (a *API) serverFiles(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/server-files/")
	if !safeFolderName(name) {
		bad(w, 404, "Server not found")
		return
	}
	if _, err := a.composeFile(name); err != nil {
		bad(w, 404, "Server not found")
		return
	}
	root := filepath.Join(a.Root, name)
	if info, err := os.Lstat(root); err != nil || !info.IsDir() {
		bad(w, 404, "Server not found")
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == ".mcui" || strings.HasPrefix(rel, ".mcui/") {
		bad(w, 403, "MCUI metadata is protected")
		return
	}
	path, err := serverFilePath(root, rel)
	if err != nil {
		bad(w, 400, err.Error())
		return
	}
	if r.Method == http.MethodGet && r.URL.Query().Get("cleanup") == "1" {
		respond(w, 200, a.fileCleanup(name, root))
		return
	}
	if r.Method == http.MethodGet {
		info, err := os.Lstat(path)
		if err != nil {
			bad(w, 404, "File not found")
			return
		}
		if r.URL.Query().Get("download") == "1" {
			downloadServerFile(w, r, path, filepath.Base(path), info.IsDir())
			return
		}
		if !info.IsDir() {
			bad(w, 400, "Choose a folder to browse")
			return
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			bad(w, 500, err.Error())
			return
		}
		result := fileListing{Path: rel, Entries: []fileEntry{}}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 || entry.Name() == ".mcui" {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			child := entry.Name()
			if rel != "" {
				child = rel + "/" + child
			}
			result.Entries = append(result.Entries, fileEntry{Name: entry.Name(), Path: child, Directory: entry.IsDir(), Size: info.Size(), Modified: info.ModTime()})
		}
		sort.Slice(result.Entries, func(i, j int) bool {
			if result.Entries[i].Directory != result.Entries[j].Directory {
				return result.Entries[i].Directory
			}
			return strings.ToLower(result.Entries[i].Name) < strings.ToLower(result.Entries[j].Name)
		})
		respond(w, 200, result)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		bad(w, 405, "Method not allowed")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.backup != nil && a.backup.capturing(name) {
		bad(w, 409, "Server is being captured for backup")
		return
	}
	if a.status(r.Context(), name) != "stopped" {
		bad(w, 409, "Stop the server before changing files")
		return
	}
	if r.Method == http.MethodDelete {
		if protectedServerFile(rel) {
			bad(w, 403, "This server file is protected")
			return
		}
		info, err := os.Lstat(path)
		if err != nil || !(info.Mode().IsRegular() || info.IsDir()) {
			bad(w, 404, "File not found")
			return
		}
		if err := os.RemoveAll(path); err != nil {
			bad(w, 500, err.Error())
			return
		}
		respond(w, 200, map[string]string{"message": "Deleted"})
		return
	}
	if rel != "" && rel != "bedrock_data" && rel != "bedrock_data/resource_packs" && rel != "bedrock_data/behavior_packs" && protectedServerFile(rel) {
		bad(w, 403, "This folder is protected")
		return
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		bad(w, 404, "Folder not found")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		bad(w, 400, "Invalid or oversized upload")
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	source, header, err := r.FormFile("file")
	if err != nil {
		bad(w, 400, "Choose a file")
		return
	}
	defer source.Close()
	if !safeFolderName(header.Filename) || header.Filename == ".mcui" {
		bad(w, 400, "Invalid file name")
		return
	}
	targetRel := strings.TrimPrefix(rel+"/"+header.Filename, "/")
	if protectedServerFile(targetRel) {
		bad(w, 403, "This server file is protected")
		return
	}
	target, err := serverFilePath(root, targetRel)
	if err != nil {
		bad(w, 400, err.Error())
		return
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		bad(w, 409, "A file with this name already exists")
		return
	}
	temp, err := os.CreateTemp(path, ".mcui-upload-*")
	if err != nil {
		bad(w, 500, err.Error())
		return
	}
	defer os.Remove(temp.Name())
	if _, err := io.Copy(temp, source); err != nil {
		temp.Close()
		bad(w, 500, err.Error())
		return
	}
	if err := temp.Close(); err != nil {
		bad(w, 500, err.Error())
		return
	}
	if err := os.Rename(temp.Name(), target); err != nil {
		bad(w, 500, err.Error())
		return
	}
	respond(w, 200, map[string]string{"path": targetRel})
}
