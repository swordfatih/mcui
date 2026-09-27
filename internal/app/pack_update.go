package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type packUpdateSummary struct {
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	From     string `json:"from"`
	To       string `json:"to"`
	Disabled int    `json:"disabled"`
	Missing  int    `json:"missing"`
}

type packUpdatePreview struct {
	Revision string              `json:"revision"`
	Updates  []packUpdateSummary `json:"updates"`
	Warnings []string            `json:"warnings"`
}

type packUpdate struct {
	source, target        string
	uuid, kind, folder    string
	version               json.RawMessage
	statePath, archiveDir string
	newState, newArchive  string
	hasState              bool
	summary               packUpdateSummary
	warnings              []string
	metaStage             string
}

func packUpdateRevision(archiveHash string, updates []packUpdate, world string) (string, error) {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s\n", archiveHash)
	for _, update := range updates {
		fmt.Fprintf(hash, "%s\x00%s\n", update.kind, update.target)
		manifest, err := os.ReadFile(filepath.Join(update.target, "manifest.json"))
		if err != nil {
			return "", err
		}
		hash.Write(manifest)
		if update.statePath != "" {
			state, err := os.ReadFile(update.statePath)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
			hash.Write(state)
		}
	}
	for _, kind := range []string{"resource", "behavior"} {
		refs, err := os.ReadFile(filepath.Join(world, packFile(kind)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		fmt.Fprintf(hash, "\n%s\n", kind)
		hash.Write(refs)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func packVersionNumbers(raw json.RawMessage) ([3]int, error) {
	var result [3]int
	var parts []int
	if len(raw) > 0 && raw[0] == '[' {
		if err := json.Unmarshal(raw, &parts); err != nil {
			return result, err
		}
	} else {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return result, err
		}
		for _, part := range strings.Split(value, ".") {
			n, err := strconv.Atoi(part)
			if err != nil {
				return result, errors.New("Pack version must be numeric")
			}
			parts = append(parts, n)
		}
	}
	if len(parts) < 2 || len(parts) > 3 {
		return result, errors.New("Pack version must have two or three numbers")
	}
	for i, part := range parts {
		if part < 0 {
			return result, errors.New("Pack version cannot be negative")
		}
		result[i] = part
	}
	return result, nil
}

func comparePackVersions(newVersion, oldVersion json.RawMessage) (int, error) {
	next, err := packVersionNumbers(newVersion)
	if err != nil {
		return 0, err
	}
	previous, err := packVersionNumbers(oldVersion)
	if err != nil {
		return 0, err
	}
	for i := range next {
		if next[i] > previous[i] {
			return 1, nil
		}
		if next[i] < previous[i] {
			return -1, nil
		}
	}
	return 0, nil
}

func displayPackVersion(raw json.RawMessage) string {
	if len(raw) > 0 && raw[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return value
		}
	}
	var values []int
	if json.Unmarshal(raw, &values) == nil {
		parts := make([]string, len(values))
		for i, value := range values {
			parts[i] = strconv.Itoa(value)
		}
		return strings.Join(parts, ".")
	}
	return string(raw)
}

func preparePackUpdates(a *API, name, data string, listing packListing, candidates []string) ([]packUpdate, func(), error) {
	if len(candidates) == 0 {
		return nil, nil, errors.New("Archive has no pack manifests")
	}
	serverRoot := filepath.Join(a.Root, name)
	metaStage, err := os.MkdirTemp(serverRoot, ".mcui-update-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { _ = os.RemoveAll(metaStage) }
	updates := make([]packUpdate, 0, len(candidates))
	seen := map[string]bool{}
	for index, path := range candidates {
		manifest, err := readPackManifest(path)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("Invalid pack manifest: %w", err)
		}
		kind := packKind(manifest)
		if kind == "" {
			cleanup()
			return nil, nil, errors.New("Unsupported pack module type")
		}
		key := kind + ":" + strings.ToLower(manifest.Header.UUID)
		if seen[key] {
			cleanup()
			return nil, nil, errors.New("Archive contains duplicate pack UUIDs")
		}
		seen[key] = true
		var matched *packInfo
		for i := range listing.Packs {
			old := &listing.Packs[i]
			if old.Kind != kind || !strings.EqualFold(old.UUID, manifest.Header.UUID) {
				continue
			}
			if matched != nil {
				cleanup()
				return nil, nil, fmt.Errorf("Multiple installed %s packs have UUID %s", kind, manifest.Header.UUID)
			}
			matched = old
		}
		if matched == nil {
			cleanup()
			return nil, nil, fmt.Errorf("No installed %s pack matches UUID %s; install it as a new pack instead", kind, manifest.Header.UUID)
		}
		if matched.BuiltIn {
			cleanup()
			return nil, nil, errors.New("Bedrock-provided packs cannot be updated here")
		}
		order, err := comparePackVersions(manifest.Header.Version, matched.Version)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("Compare %s versions: %w", matched.Name, err)
		}
		if order <= 0 {
			cleanup()
			return nil, nil, fmt.Errorf("%s must have a higher version than %s", matched.Name, displayPackVersion(matched.Version))
		}
		folder := strings.TrimPrefix(matched.ID, kind+"/")
		if !safeFolderName(folder) {
			cleanup()
			return nil, nil, errors.New("Unsafe installed pack folder")
		}
		target := filepath.Join(data, packFolder(kind), folder)
		info, err := os.Lstat(target)
		if err != nil || !info.IsDir() {
			cleanup()
			return nil, nil, fmt.Errorf("Installed pack %s is missing", matched.Name)
		}
		update := packUpdate{source: filepath.Dir(path), target: target, uuid: manifest.Header.UUID, kind: kind, folder: folder, version: manifest.Header.Version, metaStage: metaStage, summary: packUpdateSummary{Kind: kind, Name: matched.Name, From: displayPackVersion(matched.Version), To: displayPackVersion(manifest.Header.Version)}}
		if kind == "resource" {
			update.statePath = filepath.Join(serverRoot, ".mcui", "asset-state", kind, folder+".json")
			update.archiveDir = filepath.Join(serverRoot, ".mcui", "archived-assets", kind, folder)
			update.newState = filepath.Join(metaStage, fmt.Sprintf("state-%d.json", index))
			update.newArchive = filepath.Join(metaStage, fmt.Sprintf("archive-%d", index))
			if err := prepareUpdatedAssetState(serverRoot, &update, manifest); err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("Apply disabled assets to %s: %w", matched.Name, err)
			}
		}
		updates = append(updates, update)
	}
	for i := range updates {
		for j := i + 1; j < len(updates); j++ {
			if strings.HasPrefix(updates[i].source, updates[j].source+string(os.PathSeparator)) || strings.HasPrefix(updates[j].source, updates[i].source+string(os.PathSeparator)) {
				cleanup()
				return nil, nil, errors.New("Nested pack manifests cannot be updated together")
			}
		}
	}
	return updates, cleanup, nil
}

func prepareUpdatedAssetState(serverRoot string, update *packUpdate, manifest packManifest) error {
	if err := safeAssetStorage(serverRoot, update.statePath); err != nil {
		return err
	}
	if err := safeAssetStorage(serverRoot, update.archiveDir); err != nil {
		return err
	}
	state, err := readAssetState(update.statePath, update.uuid)
	if err != nil {
		return err
	}
	update.summary.Disabled = len(state.Assets)
	if len(state.CatalogPatches) > 0 {
		update.warnings = append(update.warnings, "Pending catalog patches are retained; review them before restoring disabled files")
	}
	if len(state.Assets) == 0 {
		return nil
	}
	update.hasState = true
	next := assetState{UUID: state.UUID, Assets: make([]assetRecord, 0, len(state.Assets)), CatalogPatches: state.CatalogPatches}
	if err := os.MkdirAll(update.newArchive, 0755); err != nil {
		return err
	}
	layers := map[string]bool{"main": true}
	for _, sub := range manifest.Subpacks {
		if safeFolderName(sub.FolderName) {
			layers[sub.FolderName] = true
		}
	}
	byLayer := map[string][]assetRecord{}
	for _, record := range state.Assets {
		if !assetRelative(record.Path) || managedPackFile(record.Path) || (record.Layer != "main" && !safeFolderName(record.Layer)) {
			return errors.New("Invalid archived asset path")
		}
		oldFile, err := assetPath(filepath.Join(update.archiveDir, record.Layer), record.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(oldFile)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("Archived asset is missing: %s/%s", record.Layer, record.Path)
		}
		if record.Hash != "" {
			hash, err := assetHash(oldFile)
			if err != nil {
				return err
			}
			if hash != record.Hash {
				return fmt.Errorf("Archived asset changed: %s/%s", record.Layer, record.Path)
			}
		}
		newFile := filepath.Join(update.newArchive, record.Layer, filepath.FromSlash(record.Path))
		if err := os.MkdirAll(filepath.Dir(newFile), 0755); err != nil {
			return err
		}
		if err := copyFile(oldFile, newFile); err != nil {
			return err
		}
		record.Patches = nil
		byLayer[record.Layer] = append(byLayer[record.Layer], record)
	}
	for layer, records := range byLayer {
		layerDir, err := assetLayerPath(update.source, layer)
		if err != nil {
			return err
		}
		if !layers[layer] {
			update.warnings = append(update.warnings, "Subpack "+layer+" is absent from the new manifest; its disabled files remain saved")
			next.Assets = append(next.Assets, records...)
			continue
		}
		info, err := os.Lstat(layerDir)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("New subpack %s is missing", layer)
		}
		edits, revised, warnings, err := referenceEdits(layerDir, "archive", records)
		if err != nil {
			return err
		}
		update.warnings = append(update.warnings, warnings...)
		for _, edit := range edits {
			if err := writeAssetJSON(edit.Path, edit.After); err != nil {
				return err
			}
		}
		for _, record := range revised {
			path, err := assetPath(layerDir, record.Path)
			if err != nil {
				return err
			}
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				update.summary.Missing++
				next.Assets = append(next.Assets, record)
				continue
			}
			if err != nil || !info.Mode().IsRegular() {
				return fmt.Errorf("New asset is not a regular file: %s/%s", layer, record.Path)
			}
			hash, err := assetHash(path)
			if err != nil {
				return err
			}
			record.Hash, record.Size = hash, info.Size()
			dest := filepath.Join(update.newArchive, layer, filepath.FromSlash(record.Path))
			if err := moveAssetFile(path, dest); err != nil {
				return err
			}
			next.Assets = append(next.Assets, record)
		}
	}
	return writeAssetState(update.newState, next)
}

type packRename struct{ from, to string }

var errPackRollback = errors.New("pack update rollback failed")

func applyPackUpdates(world, stage string, updates []packUpdate) error {
	completed := []packRename{}
	move := func(from, to string) error {
		if err := os.MkdirAll(filepath.Dir(to), 0755); err != nil {
			return err
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
		completed = append(completed, packRename{from, to})
		return nil
	}
	rollback := func(cause error) error {
		var failures []error
		for i := len(completed) - 1; i >= 0; i-- {
			if err := os.Rename(completed[i].to, completed[i].from); err != nil {
				failures = append(failures, err)
			}
		}
		if len(failures) > 0 {
			return fmt.Errorf("update failed; inspect %s and %s: %w", stage, updates[0].metaStage, errors.Join(append([]error{errPackRollback, cause}, failures...)...))
		}
		return cause
	}
	for i, update := range updates {
		backup := filepath.Join(stage, fmt.Sprintf("old-pack-%d", i))
		if err := move(update.target, backup); err != nil {
			return rollback(err)
		}
		if err := move(update.source, update.target); err != nil {
			return rollback(err)
		}
		if !update.hasState {
			continue
		}
		oldArchive := filepath.Join(update.metaStage, fmt.Sprintf("old-archive-%d", i))
		if err := move(update.archiveDir, oldArchive); err != nil {
			return rollback(err)
		}
		if err := move(update.newArchive, update.archiveDir); err != nil {
			return rollback(err)
		}
		oldState := filepath.Join(update.metaStage, fmt.Sprintf("old-state-%d.json", i))
		if err := move(update.statePath, oldState); err != nil {
			return rollback(err)
		}
		if err := move(update.newState, update.statePath); err != nil {
			return rollback(err)
		}
	}
	type refChange struct {
		path          string
		before, after []packRef
	}
	var changes []refChange
	for _, kind := range []string{"resource", "behavior"} {
		path := filepath.Join(world, packFile(kind))
		refs, err := readPackRefs(path)
		if err != nil {
			return rollback(err)
		}
		before := append([]packRef(nil), refs...)
		changed := false
		for i := range refs {
			for _, update := range updates {
				if update.kind == kind && strings.EqualFold(refs[i].PackID, update.uuid) {
					refs[i].Version = update.version
					changed = true
				}
			}
		}
		if changed {
			changes = append(changes, refChange{path, before, refs})
		}
	}
	written := []refChange{}
	for _, change := range changes {
		if err := writePackRefs(change.path, change.after); err != nil {
			var failures []error
			for i := len(written) - 1; i >= 0; i-- {
				if restoreErr := writePackRefs(written[i].path, written[i].before); restoreErr != nil {
					failures = append(failures, restoreErr)
				}
			}
			if len(failures) > 0 {
				failures = append(failures, errPackRollback)
			}
			return rollback(errors.Join(append([]error{err}, failures...)...))
		}
		written = append(written, change)
	}
	return nil
}
