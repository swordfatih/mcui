package app

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func optionalTextureSetLayer(key string) bool {
	switch key {
	case "normal", "heightmap", "metalness_emissive_roughness", "metalness_emissive_roughness_subsurface":
		return true
	}
	return false
}

// Texture sets use image names relative to their own directory. Their optional
// material layers must not keep pointing at shared maps that MCUI disabled.
func textureSetReferenceEdits(layerDir, action string, records []assetRecord) ([]assetFileEdit, []assetPatch, []string, error) {
	files := map[string]bool{}
	selected := map[string]bool{}
	for _, r := range records {
		selected[r.Path] = true
	}
	if action == "archive" {
		err := filepath.WalkDir(layerDir, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				if path == filepath.Join(layerDir, "subpacks") {
					return filepath.SkipDir
				}
				return nil
			}
			if e.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(e.Name(), ".texture_set.json") {
				return nil
			}
			rel, err := filepath.Rel(layerDir, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if !selected[rel] {
				files[rel] = true
			}
			return nil
		})
		if err != nil {
			return nil, nil, nil, err
		}
	} else {
		for _, r := range records {
			for _, p := range r.Patches {
				if strings.HasSuffix(p.File, ".texture_set.json") {
					files[p.File] = true
				}
			}
		}
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var edits []assetFileEdit
	var patches []assetPatch
	var warnings []string
	for _, rel := range names {
		path, err := assetPath(layerDir, rel)
		if err != nil {
			return nil, nil, nil, err
		}
		before, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, nil, err
		}
		var doc map[string]json.RawMessage
		if err = json.Unmarshal(stripJSONComments(before), &doc); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", rel, err)
		}
		var layers map[string]json.RawMessage
		if err = json.Unmarshal(doc["minecraft:texture_set"], &layers); err != nil {
			return nil, nil, nil, fmt.Errorf("%s: %w", rel, err)
		}
		changed := false
		if action == "archive" {
			for key, raw := range layers {
				var ref string
				if json.Unmarshal(raw, &ref) != nil || strings.HasPrefix(ref, "#") {
					continue
				}
				ref = filepath.ToSlash(filepath.Join(filepath.Dir(rel), ref))
				var required []string
				for _, r := range records {
					if assetPathMatches(ref, r.Path) {
						required = append(required, r.Path)
					}
				}
				if len(required) == 0 {
					continue
				}
				if !optionalTextureSetLayer(key) {
					warnings = append(warnings, rel+": required "+key+" image is disabled")
					continue
				}
				sort.Strings(required)
				patches = append(patches, assetPatch{File: rel, Key: key, Layer: records[0].Layer, Value: raw, Requires: required})
				delete(layers, key)
				changed = true
			}
		} else {
			for _, r := range records {
				for _, p := range r.Patches {
					if p.File != rel {
						continue
					}
					if !optionalTextureSetLayer(p.Key) {
						return nil, nil, nil, fmt.Errorf("%s: invalid texture set layer %s", rel, p.Key)
					}
					if _, exists := layers[p.Key]; exists {
						return nil, nil, nil, fmt.Errorf("%s: texture set layer %s already exists", rel, p.Key)
					}
					layers[p.Key] = p.Value
					changed = true
				}
			}
		}
		if !changed {
			continue
		}
		doc["minecraft:texture_set"], err = json.Marshal(layers)
		if err != nil {
			return nil, nil, nil, err
		}
		after, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, nil, nil, err
		}
		edits = append(edits, assetFileEdit{Path: path, Before: before, After: append(after, '\n')})
	}
	return edits, patches, warnings, nil
}
