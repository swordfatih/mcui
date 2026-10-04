package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type packSubpack struct {
	Folder string `json:"folder"`
	Name   string `json:"name"`
}

type packDetailResponse struct {
	packInfo
	Subpacks        []packSubpack `json:"subpacks"`
	SelectedSubpack string        `json:"selectedSubpack"`
}

func availableSubpacks(packDir string, manifest packManifest) []packSubpack {
	options := []packSubpack{}
	root := filepath.Join(packDir, "subpacks")
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() {
		return options
	}
	seen := map[string]bool{}
	for _, sub := range manifest.Subpacks {
		if !safeFolderName(sub.FolderName) || seen[sub.FolderName] {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, sub.FolderName))
		if err != nil || !info.IsDir() {
			continue
		}
		label := sub.Name
		if label == "" {
			label = sub.FolderName
		}
		options = append(options, packSubpack{Folder: sub.FolderName, Name: label})
		seen[sub.FolderName] = true
	}
	return options
}

func packReferenceIndex(refs []packRef, pack packInfo) int {
	fallback := -1
	count := 0
	for i, ref := range refs {
		if !strings.EqualFold(ref.PackID, pack.UUID) {
			continue
		}
		if samePackVersion(ref.Version, pack.Version) {
			return i
		}
		fallback = i
		count++
	}
	if count != 1 {
		return -1
	}
	return fallback
}

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
	if r.Method != http.MethodGet && r.Method != http.MethodPut && r.Method != http.MethodDelete {
		bad(w, 405, "Method not allowed")
		return
	}
	if r.Method != http.MethodGet {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.rejectDimensionMutation(w, name) {
			return
		}
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
		data, world, err := a.packPaths(name)
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
		manifest, err := readPackManifest(filepath.Join(data, packFolder(kind), folder, "manifest.json"))
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
		refs, err := readPackRefs(filepath.Join(world, packFile(kind)))
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
		detail := packDetailResponse{packInfo: *pack, Subpacks: availableSubpacks(filepath.Join(data, packFolder(kind), folder), manifest)}
		if pack.Active {
			if index := packReferenceIndex(refs, *pack); index >= 0 {
				detail.SelectedSubpack = refs[index].Subpack
			}
		}
		respond(w, 200, detail)
		return
	}
	if r.Method == http.MethodPut {
		if pack.BuiltIn {
			bad(w, 403, "Bedrock-provided packs are read-only")
			return
		}
		if a.backup != nil && a.backup.capturing(name) {
			bad(w, 409, "Server is being captured for backup")
			return
		}
		if a.status(r.Context(), name) != "stopped" {
			bad(w, 409, "Stop the server before changing a subpack")
			return
		}
		if !pack.Active {
			bad(w, 409, "Activate this pack before choosing a subpack")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var request struct {
			Subpack string `json:"subpack"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			bad(w, 400, "Invalid subpack selection")
			return
		}
		data, world, err := a.packPaths(name)
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
		packDir := filepath.Join(data, packFolder(kind), folder)
		manifest, err := readPackManifest(filepath.Join(packDir, "manifest.json"))
		if err != nil || packKind(manifest) != kind || !strings.EqualFold(manifest.Header.UUID, pack.UUID) {
			bad(w, 409, "Pack changed; reload its details")
			return
		}
		if request.Subpack != "" {
			valid := false
			for _, option := range availableSubpacks(packDir, manifest) {
				if option.Folder == request.Subpack {
					valid = true
					break
				}
			}
			if !valid {
				bad(w, 400, "Subpack is not declared in this pack's manifest or its folder is missing")
				return
			}
		}
		refsPath := filepath.Join(world, packFile(kind))
		refs, err := readPackRefs(refsPath)
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
		index := packReferenceIndex(refs, *pack)
		if index < 0 {
			bad(w, 409, "Pack selection changed; reload its details")
			return
		}
		refs[index].Subpack = request.Subpack
		if err := writePackRefs(refsPath, refs); err != nil {
			bad(w, 500, err.Error())
			return
		}
		respond(w, 200, map[string]string{"message": "Subpack saved. Restart the server to apply it."})
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
	owner, ownerErr := a.dimensionPackOwner(name, pack.UUID)
	if ownerErr != nil {
		bad(w, 409, ownerErr.Error())
		return
	}
	if owner != "" {
		bad(w, 409, "This pack belongs to "+owner+"; use Remove dimension + packs in Resources")
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
	assetRoot := filepath.Join(a.Root, name, ".mcui")
	if err := os.RemoveAll(filepath.Join(assetRoot, "archived-assets", kind, folder)); err != nil {
		bad(w, 500, "Pack deleted, but archived assets could not be removed: "+err.Error())
		return
	}
	if err := os.Remove(filepath.Join(assetRoot, "asset-state", kind, folder+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		bad(w, 500, "Pack deleted, but asset index could not be removed: "+err.Error())
		return
	}
	respond(w, 200, map[string]string{"message": "Pack deleted. Restart the server before playing."})
}
