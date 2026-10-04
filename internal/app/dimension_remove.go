package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var dimensionNamePattern = regexp.MustCompile(`^[a-z0-9_]+:[a-z0-9_]+$`)

type ownedDimensionPack struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	UUID   string `json:"uuid"`
	Folder string `json:"folder"`
}
type installedDimension struct {
	Format   int                  `json:"format"`
	Name     string               `json:"name"`
	ID       int                  `json:"id"`
	World    string               `json:"world"`
	State    string               `json:"state"`
	Packs    []ownedDimensionPack `json:"packs"`
	Revision string               `json:"revision,omitempty"`
}

func (a *API) dimensionRecordPath(server, dimension string) (string, error) {
	if !safeFolderName(server) || !dimensionNamePattern.MatchString(dimension) || strings.HasPrefix(dimension, "minecraft:") {
		return "", errors.New("Invalid custom dimension name")
	}
	hash := sha256.Sum256([]byte(dimension))
	root := filepath.Join(a.Root, server)
	path := filepath.Join(root, ".mcui", "dimensions", hex.EncodeToString(hash[:])+".json")
	if err := safeAssetStorage(root, path); err != nil {
		return "", err
	}
	return path, nil
}
func (a *API) readDimensionRecords(server string) ([]installedDimension, error) {
	root := filepath.Join(a.Root, server)
	dir := filepath.Join(root, ".mcui", "dimensions")
	if err := safeAssetStorage(root, dir); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []installedDimension{}, nil
	}
	if err != nil {
		return nil, err
	}
	records := []installedDimension{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := safeAssetStorage(root, path); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var record installedDimension
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, fmt.Errorf("Invalid dimension ownership record: %w", err)
		}
		expected, err := a.dimensionRecordPath(server, record.Name)
		if err != nil || expected != path || record.Format != 1 || record.ID < 1000 || record.ID > 2147483647 || !filepath.IsAbs(record.World) || len(record.Packs) == 0 {
			return nil, errors.New("Invalid dimension ownership record")
		}
		if record.State != "installed" && record.State != "installing" && record.State != "removing" {
			return nil, errors.New("Unknown dimension ownership state")
		}
		seen := map[string]bool{}
		for _, pack := range record.Packs {
			uuid := strings.ToLower(pack.UUID)
			if (pack.Kind != "resource" && pack.Kind != "behavior") || !packUUID.MatchString(uuid) || pack.Folder != "dimension-"+uuid || seen[uuid] {
				return nil, errors.New("Invalid dimension pack ownership")
			}
			seen[uuid] = true
		}
		record.Revision = revision(data)
		records = append(records, record)
	}
	return records, nil
}
func (a *API) writeDimensionRecord(server string, record installedDimension) error {
	path, err := a.dimensionRecordPath(server, record.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	record.Revision = ""
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".dimension-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
func (a *API) dimensionListing(server string) (dimensionStatus, error) {
	state := a.dimensionState(server)
	records, err := a.readDimensionRecords(server)
	state.Installed = records
	return state, err
}

// Called with a.mu held. Deletion is restricted to ownership records written by
// the importer, never a user-supplied folder path or guessed pack association.
func (a *API) startDimensionRemoval(w http.ResponseWriter, r *http.Request, server, dimension, expectedRevision string) {
	if a.rejectDimensionMutation(w, server) {
		return
	}
	if a.backup != nil && a.backup.active(server) {
		bad(w, 409, "Wait for the current backup to finish")
		return
	}
	if a.status(r.Context(), server) != "stopped" {
		bad(w, 409, "Stop the server before removing a custom dimension")
		return
	}
	data, world, err := a.packPaths(server)
	if err != nil {
		bad(w, 400, err.Error())
		return
	}
	records, err := a.readDimensionRecords(server)
	if err != nil {
		bad(w, 409, err.Error())
		return
	}
	var record *installedDimension
	for i := range records {
		if records[i].Name == dimension {
			record = &records[i]
			break
		}
	}
	if record == nil || record.Revision != expectedRevision {
		bad(w, 409, "Dimension ownership changed or is unavailable; reload Resources")
		return
	}
	if record.World != world {
		bad(w, 409, "This dimension belongs to a different world")
		return
	}
	if _, err := a.validateDimensionRemoval(server, data, *record); err != nil {
		bad(w, 409, err.Error())
		return
	}
	stage, err := os.MkdirTemp(data, ".mcui-dimension-remove-")
	if err != nil {
		bad(w, 500, err.Error())
		return
	}
	job := &dimensionJob{stage: stage, data: data, world: world, status: dimensionStatus{ID: filepath.Base(stage), State: "removing", Message: "Checking dimension and pack ownership"}}
	a.dimensionMu.Lock()
	if a.dimensions == nil {
		a.dimensions = map[string]*dimensionJob{}
	}
	a.dimensions[server] = job
	a.dimensionMu.Unlock()
	go a.removeDimension(server, job, *record)
	respond(w, 202, a.dimensionState(server))
}

func (a *API) validateDimensionRemoval(server, data string, record installedDimension) (map[string][]packRef, error) {
	owners, err := a.readDimensionRecords(server)
	if err != nil {
		return nil, err
	}
	owned := map[string]bool{}
	for _, pack := range record.Packs {
		owned[strings.ToLower(pack.UUID)] = true
	}
	for _, other := range owners {
		if other.Name == record.Name {
			continue
		}
		for _, pack := range other.Packs {
			if owned[strings.ToLower(pack.UUID)] {
				return nil, fmt.Errorf("Pack %s is also owned by dimension %s", pack.Name, other.Name)
			}
		}
	}
	refs := map[string][]packRef{}
	for _, kind := range []string{"behavior", "resource"} {
		path := filepath.Join(record.World, packFile(kind))
		if err := safeAssetStorage(data, path); err != nil {
			return nil, err
		}
		current, err := readPackRefs(path)
		if err != nil {
			return nil, err
		}
		refs[kind] = []packRef{}
		for _, ref := range current {
			if !owned[strings.ToLower(ref.PackID)] {
				refs[kind] = append(refs[kind], ref)
			}
		}
		dir := filepath.Join(data, packFolder(kind))
		if err := safeAssetStorage(data, dir); err != nil {
			return nil, err
		}
		folders, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, folder := range folders {
			if !folder.IsDir() {
				continue
			}
			manifest, err := readPackManifest(filepath.Join(dir, folder.Name(), "manifest.json"))
			if err != nil {
				continue
			}
			if owned[strings.ToLower(manifest.Header.UUID)] {
				matched := false
				for _, pack := range record.Packs {
					if pack.Kind == kind && pack.Folder == folder.Name() && strings.EqualFold(pack.UUID, manifest.Header.UUID) {
						matched = true
					}
				}
				if !matched {
					return nil, errors.New("An owned pack was moved or duplicated; restore its original folder before removal")
				}
				continue
			}
			for _, dependency := range manifest.Dependencies {
				if owned[strings.ToLower(dependency.UUID)] {
					return nil, fmt.Errorf("Pack %s depends on this dimension's packs; remove that dependent pack first", manifest.Header.Name)
				}
			}
		}
	}
	for _, pack := range record.Packs {
		folder := filepath.Join(data, packFolder(pack.Kind), pack.Folder)
		if err := safeAssetStorage(data, filepath.Join(folder, "manifest.json")); err != nil {
			return nil, err
		}
		if _, err := os.Lstat(folder); !errors.Is(err, os.ErrNotExist) {
			manifest, err := readPackManifest(filepath.Join(folder, "manifest.json"))
			if err != nil || !strings.EqualFold(manifest.Header.UUID, pack.UUID) || packKind(manifest) != pack.Kind {
				return nil, fmt.Errorf("Pack %s changed identity; refusing to remove it", pack.Name)
			}
		}
		root := filepath.Join(a.Root, server)
		for _, path := range dimensionPackMetadata(root, pack) {
			if err := safeAssetStorage(root, path); err != nil {
				return nil, err
			}
		}
	}
	return refs, nil
}
func dimensionPackMetadata(root string, pack ownedDimensionPack) []string {
	return []string{filepath.Join(root, ".mcui", "asset-state", pack.Kind, pack.Folder+".json"), filepath.Join(root, ".mcui", "archived-assets", pack.Kind, pack.Folder)}
}
func (a *API) removeDimension(server string, job *dimensionJob, record installedDimension) {
	defer os.RemoveAll(job.stage)
	fail := func(err error) { a.dimensionProgress(job, "failed", err.Error()) }
	data, world, err := a.packPaths(server)
	if err != nil || data != job.data || world != job.world || a.status(context.Background(), server) != "stopped" {
		fail(errors.New("Server world changed or is no longer stopped"))
		return
	}
	refs, err := a.validateDimensionRemoval(server, data, record)
	if err != nil {
		fail(err)
		return
	}
	args := []string{"--dimension-id", strconv.Itoa(record.ID)}
	if record.State != "installed" {
		args = append(args, "--allow-missing")
	}
	if _, err := a.runDimensionWorker(job, "remove-check", record.Name, args...); err != nil {
		fail(err)
		return
	}
	for kind, value := range refs {
		if err := writePackRefs(filepath.Join(job.stage, packFile(kind)), value); err != nil {
			fail(err)
			return
		}
	}
	record.State = "removing"
	if err := a.writeDimensionRecord(server, record); err != nil {
		fail(err)
		return
	}
	partial := func(err error) {
		fail(fmt.Errorf("Removal is incomplete: %w. Keep the server stopped; retry removal or restore your own backup", err))
	}
	if _, err := a.runDimensionWorker(job, "remove", record.Name, args...); err != nil {
		partial(err)
		return
	}
	for kind := range refs {
		if err := os.Rename(filepath.Join(job.stage, packFile(kind)), filepath.Join(world, packFile(kind))); err != nil {
			partial(err)
			return
		}
	}
	for _, pack := range record.Packs {
		paths := append([]string{filepath.Join(data, packFolder(pack.Kind), pack.Folder)}, dimensionPackMetadata(filepath.Join(a.Root, server), pack)...)
		for _, path := range paths {
			if err := os.RemoveAll(path); err != nil {
				partial(err)
				return
			}
		}
	}
	path, err := a.dimensionRecordPath(server, record.Name)
	if err != nil {
		partial(err)
		return
	}
	if err := os.Remove(path); err != nil {
		partial(err)
		return
	}
	a.dimensionProgress(job, "complete", "Custom dimension and its behavior/resource packs removed. Start the server when ready.")
}

// Keep dimension packs associated with their world data. Updates/subpack changes
// remain available, but direct pack deletion would strand the dimension.
func (a *API) dimensionPackOwner(server, uuid string) (string, error) {
	records, err := a.readDimensionRecords(server)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		for _, pack := range record.Packs {
			if strings.EqualFold(pack.UUID, uuid) {
				return record.Name, nil
			}
		}
	}
	return "", nil
}

// Used before import writes, so interrupted imports retain an explicit owner.
func (a *API) recordDimensionImport(server string, job *dimensionJob, dimension string) (installedDimension, error) {
	record := installedDimension{Format: 1, Name: dimension, World: job.world, State: "installing"}
	for _, item := range job.status.Dimensions {
		if item.Name == dimension {
			record.ID = item.ID
		}
	}
	if record.ID < 1000 {
		return record, errors.New("Invalid custom dimension ID")
	}
	for _, pack := range job.status.Packs {
		record.Packs = append(record.Packs, ownedDimensionPack{Name: pack.Name, Kind: pack.Kind, UUID: pack.UUID, Folder: filepath.Base(pack.target)})
	}
	path, err := a.dimensionRecordPath(server, dimension)
	if err != nil {
		return record, err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return record, errors.New("Dimension ownership already exists; remove the previous import before retrying")
	}
	return record, a.writeDimensionRecord(server, record)
}
