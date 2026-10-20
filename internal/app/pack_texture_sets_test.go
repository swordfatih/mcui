package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTextureSetSharedMaterialReferences(t *testing.T) {
	layer := t.TempDir()
	rel := "textures/blocks/poplar_planks.texture_set.json"
	putUpdateAsset(t, layer, rel, `{"format_version":"1.21.30","minecraft:texture_set":{"color":"poplar_planks","normal":"planks_normal_weak","metalness_emissive_roughness":"planks_mer"}}`)
	records := []assetRecord{{Path: "textures/blocks/planks_normal_weak.png", Layer: "SP2"}, {Path: "textures/blocks/planks_normal_weak.tga", Layer: "SP2"}, {Path: "textures/blocks/planks_mer.png", Layer: "SP2"}}
	edits, _, patches, warnings, err := referenceEdits(layer, "archive", records)
	if err != nil || len(edits) != 1 || len(patches) != 2 || len(warnings) != 0 {
		t.Fatalf("edits=%d patches=%v warnings=%v err=%v", len(edits), patches, warnings, err)
	}
	var doc map[string]any
	if err = json.Unmarshal(edits[0].After, &doc); err != nil {
		t.Fatal(err)
	}
	layers := doc["minecraft:texture_set"].(map[string]any)
	if len(layers) != 1 || layers["color"] != "poplar_planks" {
		t.Fatalf("remaining layers: %v", layers)
	}
	if err = writeAssetJSON(edits[0].Path, edits[0].After); err != nil {
		t.Fatal(err)
	}
	ready, _, err := readyCatalogPatches(layer, records[:1], patches)
	if err != nil || len(ready) != 0 {
		t.Fatalf("partial restore: %v %v", ready, err)
	}
	ready, _, err = readyCatalogPatches(layer, records, patches)
	if err != nil || len(ready) != 2 {
		t.Fatalf("complete restore: %v %v", ready, err)
	}
	edits, _, _, _, err = referenceEdits(layer, "restore", append(records, assetRecord{Patches: ready}))
	if err != nil || len(edits) != 1 {
		t.Fatalf("restore: %v %v", edits, err)
	}
	if err = json.Unmarshal(edits[0].After, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc["minecraft:texture_set"].(map[string]any)) != 3 {
		t.Fatal("material layers not restored")
	}
}

func TestInstalledTextureSetReferenceRepair(t *testing.T) {
	root := os.Getenv("MCUI_TEST_MATERIAL_SNAPSHOT")
	if root == "" {
		t.Skip("set MCUI_TEST_MATERIAL_SNAPSHOT to an extracted installed pack snapshot")
	}
	data, err := os.ReadFile(filepath.Join(root, ".mcui/asset-state/resource/Actions_Stuff.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state assetState
	if err = json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	layer := filepath.Join(root, "bedrock_data/resource_packs/Actions_Stuff/subpacks/SP2")
	edits, next, _, warnings, err := planAssetReferenceRepair(layer, "SP2", state)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, edit := range edits {
		if filepath.Base(edit.Path) == "poplar_planks.texture_set.json" {
			found = true
		}
	}
	if !found {
		t.Fatal("poplar planks not repaired")
	}
	t.Logf("%d material files repaired; remaining warnings: %v", len(edits), warnings)
	if output := os.Getenv("MCUI_TEST_MATERIAL_EXPORT"); output != "" {
		type exportedEdit struct {
			Path          string
			Before, After []byte
		}
		var exported []exportedEdit
		for _, edit := range edits {
			rel, err := filepath.Rel(root, edit.Path)
			if err != nil {
				t.Fatal(err)
			}
			exported = append(exported, exportedEdit{filepath.ToSlash(rel), edit.Before, edit.After})
		}
		payload, err := json.Marshal(struct {
			Edits       []exportedEdit
			State       assetState
			BeforeState []byte
		}{exported, next, data})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(output, payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
