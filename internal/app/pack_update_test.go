package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const updateResourceUUID = "2cf066eb-1254-4b7d-affb-80fe3216b18c"
const updateBehaviorUUID = "a4df0cb3-17be-4163-88d7-fcf7002b935d"

func updateManifest(name, uuid, kind, version string) string {
	return `{"header":{"name":"` + name + `","uuid":"` + uuid + `","version":` + version + `},"modules":[{"type":"` + kind + `"}]}`
}

func makeAddon(t *testing.T, resourceUUID, version, texture string) []byte {
	t.Helper()
	var body bytes.Buffer
	z := zip.NewWriter(&body)
	files := map[string]string{
		"Resource/manifest.json":                 updateManifest("Actions Resource", resourceUUID, "resources", version),
		"Resource/blocks.json":                   `{"format_version":"1.19.30","minecraft:hidden":{"textures":"hidden","sound":"stone"},"minecraft:other":{"textures":"other"}}`,
		"Resource/textures/blocks/hidden.png":    texture,
		"Resource/textures/terrain_texture.json": `{"texture_data":{"hidden":{"textures":"textures/blocks/hidden"},"other":{"textures":"textures/blocks/other"}}}`,
		"Behavior/manifest.json":                 updateManifest("Actions Behavior", updateBehaviorUUID, "data", version),
		"Behavior/functions/new.mcfunction":      "say new",
	}
	for path, value := range files {
		writer, err := z.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func setupPackUpdateServer(t *testing.T) (*API, string, string, string) {
	t.Helper()
	root := t.TempDir()
	data := filepath.Join(root, "server", "data")
	world := filepath.Join(data, "worlds", "world")
	rp := filepath.Join(data, "resource_packs", "actions-resource")
	bp := filepath.Join(data, "behavior_packs", "actions-behavior")
	for _, dir := range []string{world, rp, bp} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	putData(t, world, "level.dat")
	putData(t, rp, "manifest.json")
	if err := os.WriteFile(filepath.Join(rp, "manifest.json"), []byte(updateManifest("Actions Resource", updateResourceUUID, "resources", `[1,10,0]`)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bp, "manifest.json"), []byte(updateManifest("Actions Behavior", updateBehaviorUUID, "data", `[1,10,0]`)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writePackRefs(filepath.Join(world, "world_resource_packs.json"), []packRef{{PackID: updateResourceUUID, Version: json.RawMessage(`[1,10,0]`), Subpack: "fancy"}}); err != nil {
		t.Fatal(err)
	}
	if err := writePackRefs(filepath.Join(world, "world_behavior_packs.json"), []packRef{{PackID: updateBehaviorUUID, Version: json.RawMessage(`[1,10,0]`)}}); err != nil {
		t.Fatal(err)
	}
	archived := filepath.Join(root, "server", ".mcui", "archived-assets", "resource", "actions-resource", "main", "textures", "blocks", "hidden.png")
	if err := os.MkdirAll(filepath.Dir(archived), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archived, []byte("old image"), 0644); err != nil {
		t.Fatal(err)
	}
	hash, err := assetHash(archived)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(root, "server", ".mcui", "asset-state", "resource", "actions-resource.json")
	state := assetState{UUID: updateResourceUUID, Assets: []assetRecord{{Path: "textures/blocks/hidden.png", Layer: "main", Size: 9, Hash: hash, Patches: []assetPatch{{File: "textures/terrain_texture.json", Key: "hidden", Value: json.RawMessage(`{"textures":"textures/blocks/hidden"}`)}}}}}
	if err := writeAssetState(statePath, state); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "server", "compose.yaml"), []byte("services:\n  mc:\n    image: itzg/minecraft-bedrock-server\n"), 0644); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(map[string]any{"services": map[string]any{"mc": map[string]any{"image": "itzg/minecraft-bedrock-server", "volumes": []map[string]string{{"type": "bind", "source": data, "target": "/data"}}}}})
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, config, 0644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n  *\" config --format json\") cat \"$MCUI_TEST_CONFIG\" ;;\n  *\" ps -q mc\") exit 0 ;;\n  *) exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MCUI_TEST_CONFIG", configPath)
	return &API{Root: root}, rp, bp, world
}

func postAddon(t *testing.T, a *API, query string, archive []byte) *httptest.ResponseRecorder {
	return postPackArchive(t, a, query, "actions.mcaddon", archive)
}

func postPackArchive(t *testing.T, a *API, query, filename string, archive []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "/api/server-details/server/packs?"+query, &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	w := httptest.NewRecorder()
	a.packsHandler(w, request, "server")
	return w
}

func TestSingleMCPACKUpdateKeepsAbsentDisabledAsset(t *testing.T) {
	a, rp, bp, _ := setupPackUpdateServer(t)
	var body bytes.Buffer
	z := zip.NewWriter(&body)
	for path, value := range map[string]string{
		"manifest.json":                 updateManifest("Actions Resource", updateResourceUUID, "resources", `[1,11,1]`),
		"textures/terrain_texture.json": `{"texture_data":{"other":{"textures":"textures/blocks/other"}}}`,
	} {
		writer, err := z.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	archive := body.Bytes()
	preview := postPackArchive(t, a, "mode=update&preview=1", "actions.mcpack", archive)
	if preview.Code != 200 {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
	}
	var plan packUpdatePreview
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Updates) != 1 || plan.Updates[0].Missing != 1 {
		t.Fatalf("missing asset not reported: %+v", plan)
	}
	result := postPackArchive(t, a, "mode=update&expectedHash="+plan.Revision, "actions.mcpack", archive)
	if result.Code != 200 {
		t.Fatalf("update: %d %s", result.Code, result.Body.String())
	}
	if content, err := os.ReadFile(filepath.Join(bp, "manifest.json")); err != nil || !strings.Contains(string(content), `[1,10,0]`) {
		t.Fatalf("unrelated behavior pack changed: %s %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(rp, "textures/blocks/hidden.png")); !os.IsNotExist(err) {
		t.Fatalf("disabled asset returned: %v", err)
	}
	archived := filepath.Join(a.Root, "server", ".mcui", "archived-assets", "resource", "actions-resource", "main", "textures", "blocks", "hidden.png")
	if content, err := os.ReadFile(archived); err != nil || string(content) != "old image" {
		t.Fatalf("saved old asset lost: %s %v", content, err)
	}
}

func TestPackUpdateReappliesSubpackAsset(t *testing.T) {
	a, rp, _, _ := setupPackUpdateServer(t)
	archived := filepath.Join(a.Root, "server", ".mcui", "archived-assets", "resource", "actions-resource", "SP2", "textures", "blocks", "slab", "hidden.png")
	if err := os.MkdirAll(filepath.Dir(archived), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archived, []byte("old slab"), 0644); err != nil {
		t.Fatal(err)
	}
	hash, err := assetHash(archived)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(a.Root, "server", ".mcui", "asset-state", "resource", "actions-resource.json")
	state, err := readAssetState(statePath, updateResourceUUID)
	if err != nil {
		t.Fatal(err)
	}
	state.Assets = append(state.Assets, assetRecord{Path: "textures/blocks/slab/hidden.png", Layer: "SP2", Hash: hash})
	if err := writeAssetState(statePath, state); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	z := zip.NewWriter(&body)
	for path, value := range map[string]string{
		"Resource/manifest.json":                                `{"header":{"name":"Actions Resource","uuid":"` + updateResourceUUID + `","version":[1,11,1]},"modules":[{"type":"resources"}],"subpacks":[{"folder_name":"SP2","name":"SP2"}]}`,
		"Resource/textures/blocks/hidden.png":                   "new main",
		"Resource/subpacks/SP2/textures/blocks/slab/hidden.png": "new slab",
	} {
		writer, err := z.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	archive := body.Bytes()
	preview := postPackArchive(t, a, "mode=update&preview=1", "actions.mcpack", archive)
	if preview.Code != 200 {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
	}
	var plan packUpdatePreview
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Updates) != 1 || plan.Updates[0].Disabled != 2 || plan.Updates[0].Missing != 0 {
		t.Fatalf("subpack asset not matched: %+v", plan)
	}
	result := postPackArchive(t, a, "mode=update&expectedHash="+plan.Revision, "actions.mcpack", archive)
	if result.Code != 200 {
		t.Fatalf("update: %d %s", result.Code, result.Body.String())
	}
	if _, err := os.Stat(filepath.Join(rp, "subpacks", "SP2", "textures", "blocks", "slab", "hidden.png")); !os.IsNotExist(err) {
		t.Fatalf("subpack texture returned: %v", err)
	}
	if content, err := os.ReadFile(archived); err != nil || string(content) != "new slab" {
		t.Fatalf("subpack archive was not refreshed: %s %v", content, err)
	}
}

func TestPackUpdateReappliesDisabledAssetsAndWorldVersions(t *testing.T) {
	a, rp, bp, world := setupPackUpdateServer(t)
	archive := makeAddon(t, updateResourceUUID, `[1,11,1]`, "new image")
	preview := postAddon(t, a, "mode=update&preview=1", archive)
	if preview.Code != 200 {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
	}
	var plan packUpdatePreview
	if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Updates) != 2 || len(plan.Revision) != 64 || plan.Updates[0].Folder == "" || plan.Updates[1].Folder == "" {
		t.Fatalf("wrong update plan: %+v", plan)
	}
	if content, _ := os.ReadFile(filepath.Join(rp, "manifest.json")); !strings.Contains(string(content), `[1,10,0]`) {
		t.Fatal("preview changed installed pack")
	}
	if got := postAddon(t, a, "mode=update&expectedHash=wrong", archive); got.Code != 409 {
		t.Fatalf("accepted stale review: %d %s", got.Code, got.Body.String())
	}
	result := postAddon(t, a, "mode=update&expectedHash="+plan.Revision, archive)
	if result.Code != 200 {
		t.Fatalf("update: %d %s", result.Code, result.Body.String())
	}
	var outcome struct {
		Updates []packUpdateSummary `json:"updates"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &outcome); err != nil {
		t.Fatal(err)
	}
	if len(outcome.Updates) != 2 || outcome.Updates[0].Disabled+outcome.Updates[1].Disabled != 1 {
		t.Fatalf("update result omitted disabled asset count: %+v", outcome)
	}
	for _, path := range []string{filepath.Join(rp, "manifest.json"), filepath.Join(bp, "manifest.json")} {
		if content, err := os.ReadFile(path); err != nil || !strings.Contains(string(content), `[1,11,1]`) {
			t.Fatalf("new manifest missing: %s %v", content, err)
		}
	}
	if _, err := os.Stat(filepath.Join(bp, "functions", "new.mcfunction")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rp, "textures", "blocks", "hidden.png")); !os.IsNotExist(err) {
		t.Fatalf("disabled texture returned: %v", err)
	}
	archived := filepath.Join(a.Root, "server", ".mcui", "archived-assets", "resource", "actions-resource", "main", "textures", "blocks", "hidden.png")
	if content, err := os.ReadFile(archived); err != nil || string(content) != "new image" {
		t.Fatalf("archive has old asset: %s %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(rp, "textures", "terrain_texture.json")); err != nil || strings.Contains(string(content), `"hidden"`) || !strings.Contains(string(content), `"other"`) {
		t.Fatalf("catalog not updated: %s %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(rp, "blocks.json")); err != nil || strings.Contains(string(content), `"minecraft:hidden"`) || !strings.Contains(string(content), `"minecraft:other"`) {
		t.Fatalf("block override not removed: %s %v", content, err)
	}
	for _, tc := range []struct{ kind, uuid string }{{"resource", updateResourceUUID}, {"behavior", updateBehaviorUUID}} {
		refs, err := readPackRefs(filepath.Join(world, packFile(tc.kind)))
		if err != nil || len(refs) != 1 || refs[0].PackID != tc.uuid || !samePackVersion(refs[0].Version, json.RawMessage(`[1,11,1]`)) {
			t.Fatalf("world refs not updated: %+v %v", refs, err)
		}
		if tc.kind == "resource" && refs[0].Subpack != "fancy" {
			t.Fatal("subpack selection lost")
		}
	}
	state, err := readAssetState(filepath.Join(a.Root, "server", ".mcui", "asset-state", "resource", "actions-resource.json"), updateResourceUUID)
	if err != nil || len(state.Assets) != 1 {
		t.Fatalf("asset state lost: %+v %v", state, err)
	}
	if hash, err := assetHash(archived); err != nil || state.Assets[0].Hash != hash {
		t.Fatalf("asset hash not refreshed: %+v %v", state.Assets[0], err)
	}
	restore := httptest.NewRecorder()
	a.packAssets(restore, httptest.NewRequest("POST", "/api/pack-assets/server/resource/actions-resource", strings.NewReader(`{"action":"restore","layer":"main","paths":["textures/blocks/hidden.png"]}`)))
	if restore.Code != 200 {
		t.Fatalf("restore new asset: %d %s", restore.Code, restore.Body.String())
	}
	if content, err := os.ReadFile(filepath.Join(rp, "textures/blocks/hidden.png")); err != nil || string(content) != "new image" {
		t.Fatalf("restored old asset: %s %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(rp, "textures/terrain_texture.json")); err != nil || !strings.Contains(string(content), `"hidden"`) {
		t.Fatalf("new catalog reference not restored: %s %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(rp, "blocks.json")); err != nil || !strings.Contains(string(content), `"minecraft:hidden"`) {
		t.Fatalf("block override not restored: %s %v", content, err)
	}
}

func TestPackUpdateRejectsWrongUUIDAndVersion(t *testing.T) {
	a, rp, _, _ := setupPackUpdateServer(t)
	for _, tc := range []struct{ uuid, version string }{{"11111111-1111-4111-8111-111111111111", `[1,11,1]`}, {updateResourceUUID, `[1,10,0]`}} {
		result := postAddon(t, a, "mode=update&preview=1", makeAddon(t, tc.uuid, tc.version, "new image"))
		if result.Code != 409 {
			t.Fatalf("accepted invalid update: %d %s", result.Code, result.Body.String())
		}
		if content, _ := os.ReadFile(filepath.Join(rp, "manifest.json")); !strings.Contains(string(content), `[1,10,0]`) {
			t.Fatal("invalid update changed installed pack")
		}
	}
}

func TestPackUpdateReviewExpiresWhenCurrentStateChanges(t *testing.T) {
	for _, changed := range []string{"asset state", "world refs"} {
		t.Run(changed, func(t *testing.T) {
			a, rp, _, world := setupPackUpdateServer(t)
			archive := makeAddon(t, updateResourceUUID, `[1,11,1]`, "new image")
			preview := postAddon(t, a, "mode=update&preview=1", archive)
			if preview.Code != 200 {
				t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
			}
			var plan packUpdatePreview
			if err := json.Unmarshal(preview.Body.Bytes(), &plan); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(world, "world_resource_packs.json")
			if changed == "asset state" {
				path = filepath.Join(a.Root, "server", ".mcui", "asset-state", "resource", "actions-resource.json")
			}
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("\n"); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			result := postAddon(t, a, "mode=update&expectedHash="+plan.Revision, archive)
			if result.Code != 409 {
				t.Fatalf("accepted stale review: %d %s", result.Code, result.Body.String())
			}
			if content, err := os.ReadFile(filepath.Join(rp, "manifest.json")); err != nil || !strings.Contains(string(content), `[1,10,0]`) {
				t.Fatalf("stale review changed installed pack: %s %v", content, err)
			}
		})
	}
}

func TestPackUpdateRollsBackWhenWorldReferencesChange(t *testing.T) {
	a, rp, _, world := setupPackUpdateServer(t)
	stage := t.TempDir()
	newPack := filepath.Join(stage, "new-resource")
	if err := os.MkdirAll(filepath.Join(newPack, "textures", "blocks"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newPack, "manifest.json"), []byte(updateManifest("Actions Resource", updateResourceUUID, "resources", `[1,11,1]`)), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newPack, "textures", "blocks", "hidden.png"), []byte("new image"), 0644); err != nil {
		t.Fatal(err)
	}
	listing, err := a.listPacks("server")
	if err != nil {
		t.Fatal(err)
	}
	updates, cleanup, err := preparePackUpdates(a, "server", filepath.Join(a.Root, "server", "data"), listing, []string{filepath.Join(newPack, "manifest.json")})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if err := os.WriteFile(filepath.Join(world, "world_resource_packs.json"), []byte("invalid JSON"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := applyPackUpdates(world, stage, updates); err == nil {
		t.Fatal("accepted changed world references")
	}
	if content, err := os.ReadFile(filepath.Join(rp, "manifest.json")); err != nil || !strings.Contains(string(content), `[1,10,0]`) {
		t.Fatalf("old pack not restored: %s %v", content, err)
	}
	archived := filepath.Join(a.Root, "server", ".mcui", "archived-assets", "resource", "actions-resource", "main", "textures", "blocks", "hidden.png")
	if content, err := os.ReadFile(archived); err != nil || string(content) != "old image" {
		t.Fatalf("old disabled asset not restored: %s %v", content, err)
	}
	if content, err := os.ReadFile(filepath.Join(newPack, "manifest.json")); err != nil || !strings.Contains(string(content), `[1,11,1]`) {
		t.Fatalf("new pack staging not restored: %s %v", content, err)
	}
}
