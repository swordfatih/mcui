package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReferenceEditsRestoresSavedBlockEntry(t *testing.T) {
	layer := t.TempDir()
	path := filepath.Join(layer, "blocks.json")
	if err := os.WriteFile(path, []byte(`{"format_version":"1.19.30"}`), 0644); err != nil {
		t.Fatal(err)
	}
	value := json.RawMessage(`{"textures":"slab_alias"}`)
	records := []assetRecord{{Path: "textures/blocks/slab/example.png", Patches: []assetPatch{{File: "blocks.json", Key: "demo:slab", Value: value}}}}
	edits, _, _, _, err := referenceEdits(layer, "restore", records)
	if err != nil || len(edits) != 1 || !strings.Contains(string(edits[0].After), `"demo:slab"`) {
		t.Fatalf("restore edits: %+v, %v", edits, err)
	}
	if err := os.WriteFile(path, []byte(`{"demo:slab":{"textures":"changed"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := referenceEdits(layer, "restore", records); err == nil {
		t.Fatal("expected conflict with existing block entry")
	}
}

func TestCatalogPatchWaitsForAllTextures(t *testing.T) {
	layer := t.TempDir()
	first := "textures/blocks/first.png"
	second := "textures/blocks/second.png"
	patch := assetPatch{File: "blocks.json", Key: "demo:block", Value: json.RawMessage(`{"textures":{"top":"first","side":"second"}}`), Requires: []string{first, second}}
	ready, warnings, err := readyCatalogPatches(layer, []assetRecord{{Path: first}}, []assetPatch{patch})
	if err != nil || len(ready) != 0 || len(warnings) != 1 {
		t.Fatalf("early restore: %+v, %+v, %v", ready, warnings, err)
	}
	path := filepath.Join(layer, second)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("texture"), 0644); err != nil {
		t.Fatal(err)
	}
	ready, warnings, err = readyCatalogPatches(layer, []assetRecord{{Path: first}}, []assetPatch{patch})
	if err != nil || len(ready) != 1 || len(warnings) != 0 {
		t.Fatalf("complete restore: %+v, %+v, %v", ready, warnings, err)
	}
}

func TestReferenceEditsRemovesOnlyCompleteVanillaBlockOverrides(t *testing.T) {
	layer := t.TempDir()
	if err := os.MkdirAll(filepath.Join(layer, "textures"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layer, "textures", "terrain_texture.json"), []byte(`{"texture_data":{"first":{"textures":"textures/blocks/first"},"second":{"textures":"textures/blocks/second"},"other":{"textures":"textures/blocks/other"}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layer, "blocks.json"), []byte(`{"format_version":"1.19.30","minecraft:complete":{"textures":{"top":"first","side":"second"}},"minecraft:mixed":{"textures":{"top":"first","side":"other"}},"demo:custom":{"textures":"first"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	records := []assetRecord{{Path: "textures/blocks/first.png", Layer: "SP2"}, {Path: "textures/blocks/second.png", Layer: "SP2"}}
	edits, _, patches, warnings, err := referenceEdits(layer, "archive", records)
	if err != nil || len(edits) != 2 || len(patches) != 1 {
		t.Fatalf("archive references: edits=%d patches=%+v warnings=%+v err=%v", len(edits), patches, warnings, err)
	}
	if patches[0].Key != "minecraft:complete" || patches[0].Layer != "SP2" || len(patches[0].Requires) != 2 {
		t.Fatalf("wrong block restore patch: %+v", patches[0])
	}
	var after map[string]json.RawMessage
	for _, edit := range edits {
		if filepath.Base(edit.Path) == "blocks.json" && json.Unmarshal(edit.After, &after) != nil {
			t.Fatal("invalid edited blocks.json")
		}
	}
	if _, exists := after["minecraft:complete"]; exists {
		t.Fatal("complete block override was retained")
	}
	if _, exists := after["minecraft:mixed"]; !exists {
		t.Fatal("mixed block override was removed")
	}
	if _, exists := after["demo:custom"]; !exists {
		t.Fatal("custom block override was removed")
	}
	if len(warnings) != 2 {
		t.Fatalf("incomplete block references were not reported: %+v", warnings)
	}
}

func TestReferenceEditsUsesPreviouslyDisabledAliases(t *testing.T) {
	layer := t.TempDir()
	if err := os.MkdirAll(filepath.Join(layer, "textures"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layer, "textures", "terrain_texture.json"), []byte(`{"texture_data":{"second":{"textures":"textures/blocks/second"}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layer, "blocks.json"), []byte(`{"minecraft:slab":{"textures":{"top":"first","side":"second"}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	prior := assetRecord{Path: "textures/blocks/first.png", Layer: "SP2", Patches: []assetPatch{{File: "textures/terrain_texture.json", Key: "first"}}}
	selected := []assetRecord{{Path: "textures/blocks/second.png", Layer: "SP2"}}
	_, _, patches, _, err := referenceEdits(layer, "archive", selected, prior)
	if err != nil || len(patches) != 1 || len(patches[0].Requires) != 2 {
		t.Fatalf("prior disabled alias was not considered: %+v %v", patches, err)
	}
}
