package app

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func (a *API) serverPack(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/server-packs/"), "/")
	if (len(parts) != 3 && len(parts) != 4) || !safeFolderName(parts[0]) || (parts[1] != "resource" && parts[1] != "behavior") || !safeFolderName(parts[2]) || (len(parts) == 4 && parts[3] != "icon") {
		bad(w, 404, "Pack not found")
		return
	}
	name, kind, folder := parts[0], parts[1], parts[2]
	if r.Method == http.MethodGet && len(parts) == 4 {
		data, _, err := a.packPaths(name)
		if err != nil {
			bad(w, 404, "Pack not found")
			return
		}
		packDir := filepath.Join(data, packFolder(kind), folder)
		info, err := os.Lstat(packDir)
		if err != nil || !info.IsDir() {
			bad(w, 404, "Pack not found")
			return
		}
		manifest, err := readPackManifest(filepath.Join(packDir, "manifest.json"))
		if err != nil || packKind(manifest) != kind {
			bad(w, 404, "Pack not found")
			return
		}
		icon := filepath.Join(packDir, "pack_icon.png")
		info, err = os.Lstat(icon)
		if err != nil || !info.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		http.ServeFile(w, r, icon)
		return
	}
	if len(parts) != 3 {
		bad(w, 405, "Method not allowed")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodDelete {
		bad(w, 405, "Method not allowed")
		return
	}
	if r.Method == http.MethodDelete {
		a.mu.Lock()
		defer a.mu.Unlock()
	}
	listing, err := a.listPacks(name)
	if err != nil {
		bad(w, 400, err.Error())
		return
	}
	var pack *packInfo
	for i := range listing.Packs {
		if listing.Packs[i].ID == packID(kind, folder) {
			pack = &listing.Packs[i]
			break
		}
	}
	if pack == nil {
		bad(w, 404, "Pack not found")
		return
	}
	if r.Method == http.MethodGet {
		if pack.BuiltIn {
			bad(w, 404, "Pack page not available")
			return
		}
		respond(w, 200, pack)
		return
	}
	if pack.BuiltIn {
		bad(w, 403, "Bedrock-provided packs cannot be deleted here")
		return
	}
	if a.backup != nil && a.backup.capturing(name) {
		bad(w, 409, "Server is being captured for backup")
		return
	}
	if a.status(r.Context(), name) != "stopped" {
		bad(w, 409, "Stop the server before deleting a pack")
		return
	}
	data, world, err := a.packPaths(name)
	if err != nil {
		bad(w, 400, err.Error())
		return
	}
	path := filepath.Join(data, packFolder(kind), folder)
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		bad(w, 404, "Pack not found")
		return
	}
	refsPath := filepath.Join(world, packFile(kind))
	refs, err := readPackRefs(refsPath)
	if err != nil {
		bad(w, 400, err.Error())
		return
	}
	kept := make([]packRef, 0, len(refs))
	for _, ref := range refs {
		if pack.Active && strings.EqualFold(ref.PackID, pack.UUID) && (samePackVersion(ref.Version, pack.Version) || len(refs) == 1) {
			continue
		}
		kept = append(kept, ref)
	}
	trash, err := os.MkdirTemp(data, ".mcui-delete-*")
	if err != nil {
		bad(w, 500, err.Error())
		return
	}
	defer os.RemoveAll(trash)
	staged := filepath.Join(trash, folder)
	if err := os.Rename(path, staged); err != nil {
		bad(w, 500, err.Error())
		return
	}
	if pack.Active {
		if err := writePackRefs(refsPath, kept); err != nil {
			rollback := os.Rename(staged, path)
			if rollback != nil {
				bad(w, 500, "Could not update world JSON; pack restore also failed: "+errors.Join(err, rollback).Error())
				return
			}
			bad(w, 500, err.Error())
			return
		}
	}
	if err := os.RemoveAll(staged); err != nil {
		bad(w, 500, err.Error())
		return
	}
	respond(w, 200, map[string]string{"message": "Pack deleted. Restart the server before playing."})
}
