package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func putUpdateAsset(t *testing.T, root, path, value string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(value), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateAssetsReconcilesMovesAndFullyDisabledAliasVariants(t *testing.T) {
	layer, archive := t.TempDir(), t.TempDir()
	putUpdateAsset(t, layer, "textures/blocks/slab/oak.png", "new oak")
	putUpdateAsset(t, layer, "textures/blocks/slab/oak_1.png", "new variation")
	putUpdateAsset(t, layer, "textures/blocks/slab/oak_1_mer.png", "new material")
	putUpdateAsset(t, archive, "textures/blocks/oak.png", "old oak")
	putUpdateAsset(t, layer, "textures/terrain_texture.json", `{"texture_data":{"oak":{"textures":["textures/blocks/slab/oak","textures/blocks/slab/oak_1"]},"other":{"textures":"textures/blocks/other"}}}`)
	state := assetState{Assets: []assetRecord{{Path: "textures/blocks/oak.png", Layer: "SP2", Patches: []assetPatch{{File: "textures/terrain_texture.json", Key: "oak"}}}}}
	records, moved, added, warnings, err := reconcileUpdatedAssets(layer, archive, "SP2", append([]assetRecord(nil), state.Assets...), state)
	if err != nil || moved != 1 || added != 2 || len(records) != 3 || len(warnings) != 0 {
		t.Fatalf("records=%+v moved=%d added=%d warnings=%v err=%v", records, moved, added, warnings, err)
	}
	if records[0].Path != "textures/blocks/slab/oak.png" {
		t.Fatal(records[0])
	}
	if data, err := os.ReadFile(filepath.Join(archive, records[0].Path)); err != nil || string(data) != "old oak" {
		t.Fatalf("lost archived copy: %s %v", data, err)
	}
}

func TestUpdateAssetsLeavesAmbiguousMovesAndOtherLayersAlone(t *testing.T) {
	for _, multipleOld := range []bool{false, true} {
		t.Run(map[bool]string{false: "multiple destinations", true: "multiple sources"}[multipleOld], func(t *testing.T) {
			layer, archive := t.TempDir(), t.TempDir()
			putUpdateAsset(t, layer, "textures/new/stone.png", "new")
			if !multipleOld {
				putUpdateAsset(t, layer, "textures/other/stone.png", "other")
			}
			records := []assetRecord{{Path: "textures/old/stone.png", Layer: "SP2"}}
			if multipleOld {
				records = append(records, assetRecord{Path: "textures/old2/stone.png", Layer: "SP2"})
			}
			for _, r := range records {
				putUpdateAsset(t, archive, r.Path, "old")
			}
			putUpdateAsset(t, layer, "textures/terrain_texture.json", `{"texture_data":{"other":{"textures":"textures/new/stone"}}}`)
			state := assetState{CatalogPatches: []assetPatch{{Layer: "SP1", File: "textures/terrain_texture.json", Key: "other"}}}
			got, moved, added, warnings, err := reconcileUpdatedAssets(layer, archive, "SP2", records, state)
			if err != nil || moved != 0 || added != 0 || len(warnings) != len(records) || len(got) != len(records) {
				t.Fatalf("%+v %d %d %v %v", got, moved, added, warnings, err)
			}
		})
	}
}

// Optional end-to-end check against a user's archive and disabled index, without
// modifying either. Old archived bytes are irrelevant to path reconciliation.
func TestUpdateAssetsLocalArchive(t *testing.T) {
	archive, index := os.Getenv("MCUI_TEST_UPDATE_ARCHIVE"), os.Getenv("MCUI_TEST_UPDATE_INDEX")
	if archive == "" || index == "" {
		t.Skip("set MCUI_TEST_UPDATE_ARCHIVE and MCUI_TEST_UPDATE_INDEX")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0755); err != nil {
		t.Fatal(err)
	}
	if err := extract(archive, source); err != nil {
		t.Fatal(err)
	}
	if err := normalizeOptimizedPack(source); err != nil {
		t.Fatal(err)
	}
	originalCatalogs := map[string]any{}
	files, err := contentFiles(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if filepath.Base(file) != "blocks.json" && filepath.Base(file) != "terrain_texture.json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(source, file))
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(stripJSONComments(data), &value); err != nil {
			t.Fatal(err)
		}
		originalCatalogs[file] = value
	}
	data, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	var state assetState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	for i := range state.Assets {
		state.Assets[i].Hash = ""
		putUpdateAsset(t, filepath.Join(root, "archived"), filepath.Join(state.Assets[i].Layer, state.Assets[i].Path), "old")
	}
	statePath := filepath.Join(root, "state.json")
	if err := writeAssetState(statePath, state); err != nil {
		t.Fatal(err)
	}
	manifest, err := readPackManifest(filepath.Join(source, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	u := packUpdate{source: source, statePath: statePath, archiveDir: filepath.Join(root, "archived"), newArchive: filepath.Join(root, "next-archive"), newState: filepath.Join(root, "next-state.json"), uuid: state.UUID}
	if err := prepareUpdatedAssetState(root, &u, manifest); err != nil {
		t.Fatal(err)
	}
	t.Logf("summary: %+v; warnings: %v", u.summary, u.warnings)
	next, err := readAssetState(u.newState, state.UUID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range next.Assets {
		path := filepath.Join(u.newArchive, a.Layer, a.Path)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("lost archived file %s: %v", path, err)
		}
		if hash, err := assetHash(path); err != nil || (a.Hash != "" && hash != a.Hash) {
			t.Fatalf("bad archived hash %s: %v", path, err)
		}
	}
	byLayer := map[string][]assetRecord{}
	for _, a := range next.Assets {
		byLayer[a.Layer] = append(byLayer[a.Layer], a)
	}
	for layer, records := range byLayer {
		layerDir, err := assetLayerPath(source, layer)
		if err != nil {
			t.Fatal(err)
		}
		ready, warnings, err := readyCatalogPatches(layerDir, records, next.CatalogPatches)
		if err != nil || len(warnings) != 0 {
			t.Fatalf("restore readiness: %v %v", warnings, err)
		}
		records = append(records, assetRecord{Patches: ready})
		edits, _, _, _, err := referenceEdits(layerDir, "restore", records)
		if err != nil {
			t.Fatal(err)
		}
		for _, edit := range edits {
			if err := writeAssetJSON(edit.Path, edit.After); err != nil {
				t.Fatal(err)
			}
		}
	}
	for file, original := range originalCatalogs {
		data, err := os.ReadFile(filepath.Join(source, file))
		if err != nil {
			t.Fatal(err)
		}
		var restored any
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(original, restored) {
			t.Fatalf("catalog did not round-trip: %s", file)
		}
	}
}

func TestReconcileDisabledTextureSetWithoutTerrainAlias(t *testing.T) {
	layer := t.TempDir()
	archive := t.TempDir()
	putUpdateAsset(t, layer, "textures/blocks/pointed_dripstone_up_tip.texture_set.json", `{"minecraft:texture_set":{"color":"pointed_dripstone_up_tip"}}`)
	records := []assetRecord{{Path: "textures/blocks/pointed_dripstone_up_tip.png", Layer: "SP2"}}
	next, _, added, warnings, err := reconcileUpdatedAssets(layer, archive, "SP2", records, assetState{Assets: records})
	if err != nil || added != 1 || len(next) != 2 || len(warnings) != 0 {
		t.Fatalf("next=%v added=%d warnings=%v err=%v", next, added, warnings, err)
	}
	if next[1].Path != "textures/blocks/pointed_dripstone_up_tip.texture_set.json" {
		t.Fatalf("wrong related file: %v", next[1])
	}
}
