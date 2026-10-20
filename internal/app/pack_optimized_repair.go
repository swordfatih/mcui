package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func isOptimizedPack(root string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		return false, err
	}
	var doc struct {
		Header struct {
			Optimization json.RawMessage `json:"pack_optimization_version"`
		} `json:"header"`
	}
	if err := json.Unmarshal(stripJSONComments(data), &doc); err != nil {
		return false, err
	}
	return len(doc.Header.Optimization) > 0, nil
}

// Repair an existing optimized installation as a whole staged pack: archived
// catalogs overwrite loose ones, while loose catalogs need their saved entries
// restored before disabled assets can be reapplied without losing restoration.
func prepareOptimizedAssetRepair(serverRoot, packDir, statePath, archiveDir string, state assetState) (packUpdate, string, []string, error) {
	u := packUpdate{}
	stage, err := os.MkdirTemp(serverRoot, ".mcui-optimized-repair-")
	if err != nil {
		return u, "", nil, err
	}
	fail := func(err error) (packUpdate, string, []string, error) { os.RemoveAll(stage); return u, "", nil, err }
	source := filepath.Join(stage, "source")
	if err := copyTree(packDir, source); err != nil {
		return fail(err)
	}
	materialized := map[string]bool{}
	listed := map[string]bool{}
	if err := normalizeOptimizedPack(source, materialized, listed); err != nil {
		return fail(err)
	}
	byLayer := map[string][]assetRecord{}
	for _, record := range state.Assets {
		byLayer[record.Layer] = append(byLayer[record.Layer], record)
	}
	for layer, records := range byLayer {
		layerDir, err := assetLayerPath(source, layer)
		if err != nil {
			return fail(err)
		}
		if _, err := os.Stat(layerDir); os.IsNotExist(err) {
			continue
		}
		prefix := ""
		if layer != "main" {
			prefix = "subpacks/" + layer + "/"
		}
		for i := range records {
			// External texture entries have no payload in the binary archive.
			// Recover disabled bytes in staging before reapplying the index;
			// files absent from the new archive remain archived as before.
			if listed[prefix+records[i].Path] {
				path, err := assetPath(layerDir, records[i].Path)
				if err != nil {
					return fail(err)
				}
				if _, err := os.Stat(path); os.IsNotExist(err) {
					old, err := assetPath(archiveDir, layer+"/"+records[i].Path)
					if err != nil {
						return fail(err)
					}
					if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
						return fail(err)
					}
					if err := copyFile(old, path); err != nil {
						return fail(err)
					}
				}
			}
			patches := []assetPatch{}
			for _, patch := range records[i].Patches {
				if !materialized[prefix+patch.File] {
					patches = append(patches, patch)
				}
			}
			records[i].Patches = patches
		}
		pending := []assetPatch{}
		selected := map[string]bool{}
		for _, r := range records {
			selected[r.Path] = true
		}
		for _, patch := range state.CatalogPatches {
			belongs := patch.Layer == layer
			if patch.Layer == "" && len(patch.Requires) > 0 {
				belongs = true
				for _, path := range patch.Requires {
					if !selected[path] {
						belongs = false
					}
				}
			}
			if belongs && !materialized[prefix+patch.File] {
				pending = append(pending, patch)
			}
		}
		records = append(records, assetRecord{Layer: layer, Patches: pending})
		edits, _, _, _, err := referenceEdits(layerDir, "restore", records)
		if err != nil {
			return fail(fmt.Errorf("Could not recover saved catalog entries: %w", err))
		}
		for _, edit := range edits {
			if err := writeAssetJSON(edit.Path, edit.After); err != nil {
				return fail(err)
			}
		}
	}
	manifest, err := readPackManifest(filepath.Join(source, "manifest.json"))
	if err != nil {
		return fail(err)
	}
	u = packUpdate{source: source, target: packDir, uuid: manifest.Header.UUID, kind: "resource", folder: filepath.Base(packDir), version: manifest.Header.Version, statePath: statePath, archiveDir: archiveDir, newState: filepath.Join(stage, "asset-state.json"), newArchive: filepath.Join(stage, "archived-assets"), metaStage: stage}
	if err := prepareUpdatedAssetState(serverRoot, &u, manifest); err != nil {
		return fail(err)
	}
	changes := []string{fmt.Sprintf("Expanded %d archived files and removed optimized catalog overrides", len(materialized)), fmt.Sprintf("Rebuilt references and reapplied %d disabled files across all pack layers", u.summary.Disabled-u.summary.Missing)}
	return u, stage, changes, nil
}
