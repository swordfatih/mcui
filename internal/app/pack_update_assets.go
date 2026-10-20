package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Reconcile within one layer only. Exact paths win; moved paths must have one
// unambiguous basename, and a destination cannot belong to two records.
func reconcileUpdatedAssets(layerDir, archiveDir, layer string, records []assetRecord, state assetState) ([]assetRecord, int, int, []string, error) {
	files, err := contentFiles(layerDir)
	if err != nil {
		return nil, 0, 0, nil, err
	}
	available := map[string]bool{}
	byName := map[string][]string{}
	for _, file := range files {
		if strings.HasPrefix(file, "subpacks/") || managedPackFile(file) {
			continue
		}
		available[file] = true
		byName[filepath.Base(file)] = append(byName[filepath.Base(file)], file)
	}
	selected := map[string]bool{}
	unmatchedNames := map[string]int{}
	for _, record := range records {
		selected[record.Path] = true
		if !available[record.Path] {
			unmatchedNames[filepath.Base(record.Path)]++
		}
	}
	moved, added := 0, 0
	warnings := []string{}
	for i := range records {
		old := records[i].Path
		if available[old] {
			continue
		}
		candidates := byName[filepath.Base(old)]
		if len(candidates) == 0 {
			continue
		}
		if len(candidates) != 1 || unmatchedNames[filepath.Base(old)] != 1 || selected[candidates[0]] {
			warnings = append(warnings, fmt.Sprintf("%s/%s: moved asset is ambiguous; kept its archived copy", layer, old))
			continue
		}
		dest := candidates[0]
		if err := os.MkdirAll(filepath.Dir(filepath.Join(archiveDir, dest)), 0755); err != nil {
			return nil, 0, 0, nil, err
		}
		if err := moveAssetFile(filepath.Join(archiveDir, old), filepath.Join(archiveDir, dest)); err != nil {
			return nil, 0, 0, nil, err
		}
		records[i].Path = dest
		selected[dest] = true
		moved++
	}

	// Saved alias patches prove the complete old alias was disabled. Follow that
	// identity to new variants rather than guessing from similar filenames.
	aliases := map[string]bool{}
	oldPaths := map[string]bool{}
	for _, record := range state.Assets {
		if record.Layer != layer {
			continue
		}
		oldPaths[record.Path] = true
		for _, patch := range record.Patches {
			if patch.File == "textures/terrain_texture.json" {
				aliases[patch.Key] = true
			}
		}
	}
	for _, patch := range state.CatalogPatches {
		belongs := patch.Layer == layer
		if patch.Layer == "" && len(patch.Requires) > 0 {
			belongs = true
			for _, path := range patch.Requires {
				if !oldPaths[path] {
					belongs = false
				}
			}
		}
		if belongs && patch.File == "textures/terrain_texture.json" {
			aliases[patch.Key] = true
		}
	}
	data, err := os.ReadFile(filepath.Join(layerDir, "textures/terrain_texture.json"))
	if os.IsNotExist(err) {
		data = []byte(`{"texture_data":{}}`)
		err = nil
	}
	if err != nil {
		return nil, 0, 0, nil, err
	}
	var terrain struct {
		Textures map[string]struct {
			Textures any `json:"textures"`
		} `json:"texture_data"`
	}
	if err := json.Unmarshal(stripJSONComments(data), &terrain); err != nil {
		return nil, 0, 0, nil, err
	}
	add := map[string]bool{}
	// A disabled image's material definition must also be disabled even when
	// there is no terrain alias patch (vanilla texture paths need no alias).
	for path := range selected {
		stem := assetTexturePath(path)
		if stem != path && available[stem+".texture_set.json"] {
			add[stem+".texture_set.json"] = true
		}
	}
	for alias := range aliases {
		for _, ref := range terrainTexturePaths(terrain.Textures[alias].Textures) {
			if !assetRelative(ref) {
				continue
			}
			candidates := []string{}
			for _, ext := range []string{"", ".png", ".tga", ".jpg", ".jpeg"} {
				if available[ref+ext] {
					candidates = append(candidates, ref+ext)
				}
			}
			if len(candidates) == 0 {
				warnings = append(warnings, fmt.Sprintf("%s: texture %s for disabled alias %s could not be resolved uniquely", layer, ref, alias))
				continue
			}
			// PNG and TGA files can share a texture path. Disable every format
			// behind a fully disabled alias instead of leaving an active alternate.
			for _, candidate := range candidates {
				add[candidate] = true
				stem := assetTexturePath(candidate)
				for _, suffix := range []string{".texture_set.json", "_mer.png", "_mers.png", "_normal.png"} {
					if available[stem+suffix] {
						add[stem+suffix] = true
					}
				}
			}
		}
	}
	paths := make([]string, 0, len(add))
	for path := range add {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if selected[path] {
			continue
		}
		records = append(records, assetRecord{Path: path, Layer: layer})
		selected[path] = true
		added++
	}
	sort.Strings(warnings)
	return records, moved, added, warnings, nil
}
