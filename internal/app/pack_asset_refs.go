package app

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

type assetPatch struct {
	File  string          `json:"file"`
	Key   string          `json:"key,omitempty"`
	Index int             `json:"index,omitempty"`
	Value json.RawMessage `json:"value"`
}
type assetFileEdit struct {
	Path          string
	Before, After []byte
}

func assetTexturePath(value string) string {
	for _, suffix := range []string{".png", ".tga", ".jpg", ".jpeg"} {
		value = strings.TrimSuffix(value, suffix)
	}
	return value
}
func assetPathMatches(reference, asset string) bool {
	return assetTexturePath(reference) == assetTexturePath(asset)
}
func texturePaths(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, item := range v {
			out = append(out, texturePaths(item)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, item := range v {
			out = append(out, texturePaths(item)...)
		}
		return out
	}
	return nil
}
func referenceEdits(layerDir string, action string, records []assetRecord) ([]assetFileEdit, []assetRecord, []string, error) {
	byPath := map[string]int{}
	for i, record := range records {
		byPath[record.Path] = i
	}
	warnings := []string{}
	edits := []assetFileEdit{}
	files := []string{"textures/terrain_texture.json", "textures/flipbook_textures.json", "textures/flipbook_texture.json", "textures/textures_list.json"}
	for _, rel := range files {
		path, err := assetPath(layerDir, rel)
		if err != nil {
			return nil, nil, nil, err
		}
		before, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			if action == "restore" {
				for _, record := range records {
					for _, patch := range record.Patches {
						if patch.File == rel {
							return nil, nil, nil, fmt.Errorf("%s is missing; restore its catalog before restoring this asset", rel)
						}
					}
				}
			}
			continue
		}
		if err != nil {
			return nil, nil, nil, err
		}
		if _, selected := byPath[rel]; selected && action == "archive" {
			continue
		}
		var after []byte
		changed := false
		if rel == "textures/terrain_texture.json" {
			var doc map[string]json.RawMessage
			if err := json.Unmarshal(stripJSONComments(before), &doc); err != nil {
				return nil, nil, nil, fmt.Errorf("%s: %w", rel, err)
			}
			var textureData map[string]json.RawMessage
			if err := json.Unmarshal(doc["texture_data"], &textureData); err != nil {
				continue
			}
			for key, raw := range textureData {
				if action == "archive" {
					var item any
					if json.Unmarshal(raw, &item) != nil {
						continue
					}
					textureEntry, ok := item.(map[string]any)
					if !ok {
						continue
					}
					paths := texturePaths(textureEntry["textures"])
					matching := -1
					other := false
					for _, ref := range paths {
						found := false
						for asset, i := range byPath {
							if assetPathMatches(ref, asset) {
								matching = i
								found = true
								break
							}
						}
						if !found {
							other = true
						}
					}
					if matching >= 0 {
						if other || len(paths) > 1 {
							warnings = append(warnings, rel+": shared texture alias "+key+" needs review")
							continue
						}
						records[matching].Patches = append(records[matching].Patches, assetPatch{File: rel, Key: key, Value: raw})
						delete(textureData, key)
						changed = true
					}
				}
			}
			if action == "restore" {
				for _, record := range records {
					for _, patch := range record.Patches {
						if patch.File != rel {
							continue
						}
						if _, exists := textureData[patch.Key]; exists {
							return nil, nil, nil, fmt.Errorf("%s: alias %s already exists", rel, patch.Key)
						}
						textureData[patch.Key] = patch.Value
						changed = true
					}
				}
			}
			doc["texture_data"], err = json.Marshal(textureData)
			if err != nil {
				return nil, nil, nil, err
			}
			after, err = json.MarshalIndent(doc, "", "  ")
			if err != nil {
				return nil, nil, nil, err
			}
		} else {
			var list []json.RawMessage
			if err := json.Unmarshal(stripJSONComments(before), &list); err != nil {
				return nil, nil, nil, fmt.Errorf("%s: %w", rel, err)
			}
			if action == "archive" {
				kept := make([]json.RawMessage, 0, len(list))
				for index, raw := range list {
					var ref string
					if rel == "textures/textures_list.json" {
						_ = json.Unmarshal(raw, &ref)
					} else {
						var item struct {
							FlipbookTexture string `json:"flipbook_texture"`
						}
						_ = json.Unmarshal(raw, &item)
						ref = item.FlipbookTexture
					}
					match := -1
					for asset, i := range byPath {
						if ref != "" && assetPathMatches(ref, asset) {
							match = i
							break
						}
					}
					if match < 0 {
						kept = append(kept, raw)
						continue
					}
					records[match].Patches = append(records[match].Patches, assetPatch{File: rel, Index: index, Value: raw})
					changed = true
				}
				list = kept
			} else {
				var patches []assetPatch
				for _, record := range records {
					for _, patch := range record.Patches {
						if patch.File == rel {
							patches = append(patches, patch)
						}
					}
				}
				for i := 0; i < len(patches); i++ {
					for j := i + 1; j < len(patches); j++ {
						if patches[j].Index < patches[i].Index {
							patches[i], patches[j] = patches[j], patches[i]
						}
					}
				}
				for _, patch := range patches {
					for _, existing := range list {
						if string(existing) == string(patch.Value) {
							return nil, nil, nil, fmt.Errorf("%s: entry already exists", rel)
						}
					}
					at := patch.Index
					if at > len(list) {
						at = len(list)
					}
					list = append(list, nil)
					copy(list[at+1:], list[at:])
					list[at] = patch.Value
					changed = true
				}
			}
			after, err = json.MarshalIndent(list, "", "  ")
			if err != nil {
				return nil, nil, nil, err
			}
		}
		if !changed {
			continue
		}
		after = append(after, '\n')
		if string(after) != string(before) {
			edits = append(edits, assetFileEdit{Path: path, Before: before, After: after})
		}
	}
	if action == "archive" {
		aliases := map[string]bool{}
		for _, record := range records {
			for _, patch := range record.Patches {
				if patch.File == "textures/terrain_texture.json" {
					aliases[patch.Key] = true
				}
			}
		}
		if len(aliases) > 0 {
			if content, err := os.ReadFile(filepath.Join(layerDir, "blocks.json")); err == nil {
				var blocks map[string]json.RawMessage
				if json.Unmarshal(stripJSONComments(content), &blocks) == nil {
					for block, raw := range blocks {
						if block == "format_version" {
							continue
						}
						var item map[string]any
						if json.Unmarshal(raw, &item) != nil {
							continue
						}
						for _, field := range []string{"textures", "carried_textures"} {
							for _, alias := range texturePaths(item[field]) {
								if aliases[alias] {
									warnings = append(warnings, "blocks.json: "+block+" still uses texture alias "+alias)
								}
							}
						}
					}
				}
			}
		}
		known := map[string]bool{}
		for _, file := range files {
			known[file] = true
		}
		_ = filepath.WalkDir(layerDir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if path == filepath.Join(layerDir, "subpacks") {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
				return nil
			}
			rel, err := filepath.Rel(layerDir, path)
			if err != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if known[rel] || rel == "manifest.json" {
				return nil
			}
			info, err := entry.Info()
			if err != nil || info.Size() > 2<<20 {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			for asset := range byPath {
				if strings.Contains(string(content), assetTexturePath(asset)) {
					warnings = append(warnings, "Possible reference in "+rel+" to "+asset)
					break
				}
			}
			return nil
		})
	}
	return edits, records, warnings, nil
}
