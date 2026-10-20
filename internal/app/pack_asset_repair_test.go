package app

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAssetReferenceRepairAPIPreviewsAndRepairsExistingPack(t *testing.T) {
	a, pack, _, _ := setupPackUpdateServer(t)
	putUpdateAsset(t, pack, "textures/terrain_texture.json", `{"texture_data":{"other":{"textures":"textures/other"}}}`)
	putUpdateAsset(t, pack, "blocks.json", `{"stone":{"textures":{"up":"hidden","side":"other"}}}`)
	statePath := filepath.Join(a.Root, "server/.mcui/asset-state/resource/actions-resource.json")
	before, _ := os.ReadFile(statePath)
	for _, body := range []string{`{"action":"repair","layer":"missing"}`, `{"action":"repair","layer":"main","paths":["textures/blocks/hidden.png"]}`} {
		w := httptest.NewRecorder()
		a.packAssets(w, httptest.NewRequest("POST", "/api/pack-assets/server/resource/actions-resource", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("invalid repair accepted: %d %s", w.Code, w.Body.String())
		}
	}
	preview := httptest.NewRecorder()
	a.packAssets(preview, httptest.NewRequest("POST", "/api/pack-assets/server/resource/actions-resource", strings.NewReader(`{"action":"repair","layer":"main","preview":true}`)))
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), "blocks.json") {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
	}
	after, _ := os.ReadFile(statePath)
	if string(before) != string(after) {
		t.Fatal("preview modified disabled index")
	}
	blocks, _ := os.ReadFile(filepath.Join(pack, "blocks.json"))
	if !strings.Contains(string(blocks), `"stone"`) {
		t.Fatal("preview modified blocks")
	}
	w := httptest.NewRecorder()
	a.packAssets(w, httptest.NewRequest("POST", "/api/pack-assets/server/resource/actions-resource", strings.NewReader(`{"action":"repair","layer":"main"}`)))
	if w.Code != 200 {
		t.Fatalf("repair: %d %s", w.Code, w.Body.String())
	}
	blocks, _ = os.ReadFile(filepath.Join(pack, "blocks.json"))
	if strings.Contains(string(blocks), `"stone"`) {
		t.Fatal("dangling block retained")
	}
	state, err := readAssetState(statePath, updateResourceUUID)
	if err != nil || len(state.CatalogPatches) != 1 || len(state.Assets) != 1 {
		t.Fatalf("repair lost restore information: %+v %v", state, err)
	}
}

func TestAssetReferenceRepairRemovesDanglingVanillaOverridesAndRestoresThem(t *testing.T) {
	layer := t.TempDir()
	putUpdateAsset(t, layer, "textures/terrain_texture.json", `{"texture_data":{"active":{"textures":"textures/active"}}}`)
	putUpdateAsset(t, layer, "blocks.json", `{"acacia_double_slab":{"textures":{"up":"removed","side":"active"}},"minecraft:stone":{"textures":"removed_multi"},"demo:custom":{"textures":"removed"},"dirt":{"textures":"active"}}`)
	state := assetState{UUID: updateResourceUUID, Assets: []assetRecord{
		{Path: "textures/first.png", Layer: "SP2", Patches: []assetPatch{{File: "textures/terrain_texture.json", Key: "removed", Value: json.RawMessage(`{"textures":"textures/first"}`)}}},
		{Path: "textures/second.png", Layer: "SP2"},
	}, CatalogPatches: []assetPatch{{Layer: "SP2", File: "textures/terrain_texture.json", Key: "removed_multi", Requires: []string{"textures/first.png", "textures/second.png"}, Value: json.RawMessage(`{"textures":["textures/first","textures/second"]}`)}}}
	before, _ := os.ReadFile(filepath.Join(layer, "blocks.json"))
	edits, next, changes, warnings, err := planAssetReferenceRepair(layer, "SP2", state)
	if err != nil || len(edits) != 1 || len(changes) != 2 || len(warnings) != 1 {
		t.Fatalf("edits=%d changes=%v warnings=%v err=%v", len(edits), changes, warnings, err)
	}
	stillBefore, _ := os.ReadFile(filepath.Join(layer, "blocks.json"))
	if string(stillBefore) != string(before) {
		t.Fatal("preview mutated catalogs")
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := applyAssetReferenceRepair(statePath, edits, next); err != nil {
		t.Fatal(err)
	}
	var blocks map[string]any
	after, _ := os.ReadFile(filepath.Join(layer, "blocks.json"))
	json.Unmarshal(after, &blocks)
	if len(blocks) != 2 || blocks["demo:custom"] == nil || blocks["dirt"] == nil {
		t.Fatalf("wrong blocks retained: %s", after)
	}
	edits, again, _, _, err := planAssetReferenceRepair(layer, "SP2", next)
	if err != nil || len(edits) != 0 || !reflect.DeepEqual(again, next) {
		t.Fatalf("repair is not idempotent: edits=%d err=%v", len(edits), err)
	}
	ready, _, err := readyCatalogPatches(layer, next.Assets, next.CatalogPatches)
	if err != nil || len(ready) != 3 {
		t.Fatalf("restore dependencies: %+v %v", ready, err)
	}
	records := append(append([]assetRecord(nil), next.Assets...), assetRecord{Patches: ready})
	edits, _, _, _, err = referenceEdits(layer, "restore", records)
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range edits {
		if filepath.Base(edit.Path) == "blocks.json" {
			var original, restored any
			json.Unmarshal(before, &original)
			json.Unmarshal(edit.After, &restored)
			if !reflect.DeepEqual(original, restored) {
				t.Fatalf("restored blocks differ: %s", edit.After)
			}
		}
	}
}

func TestAssetReferenceRepairRollsBackWhenStateCannotBeSaved(t *testing.T) {
	root := t.TempDir()
	putUpdateAsset(t, root, "blocks.json", `{"stone":{"textures":"removed"}}`)
	path := filepath.Join(root, "blocks.json")
	before, _ := os.ReadFile(path)
	putUpdateAsset(t, root, "not-a-directory", "file")
	err := applyAssetReferenceRepair(filepath.Join(root, "not-a-directory/state.json"), []assetFileEdit{{Path: path, Before: before, After: []byte(`{}`)}}, assetState{Assets: []assetRecord{{Path: "texture.png"}}})
	if err == nil {
		t.Fatal("expected state-write failure")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("catalog was not rolled back")
	}
}

func TestAssetReferenceRepairLocalSnapshot(t *testing.T) {
	snapshot := os.Getenv("MCUI_TEST_REPAIR_SNAPSHOT")
	if snapshot == "" {
		t.Skip("set MCUI_TEST_REPAIR_SNAPSHOT to a JSON-only pack snapshot directory")
	}
	root := t.TempDir()
	layerRoot := filepath.Join(root, "pack")
	for _, rel := range []string{"state.json", "blocks.json", "textures/terrain_texture.json"} {
		data, err := os.ReadFile(filepath.Join(snapshot, rel))
		if err != nil {
			t.Fatal(err)
		}
		if rel == "state.json" {
			putUpdateAsset(t, root, rel, string(data))
		} else {
			putUpdateAsset(t, layerRoot, rel, string(data))
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, "state.json"))
	var state assetState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	edits, next, changes, warnings, err := planAssetReferenceRepair(layerRoot, "SP2", state)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("repair: %d catalogs, %d changed references, warnings=%v", len(edits), len(changes), warnings)
	if err := applyAssetReferenceRepair(filepath.Join(root, "state.json"), edits, next); err != nil {
		t.Fatal(err)
	}
	edits, _, _, _, err = planAssetReferenceRepair(layerRoot, "SP2", next)
	if err != nil || len(edits) != 0 {
		t.Fatalf("repeat repair edits=%d err=%v", len(edits), err)
	}
}
