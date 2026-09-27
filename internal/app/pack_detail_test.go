package app

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackDetailIconAndDelete(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "server", "data")
	world := filepath.Join(data, "worlds", "world")
	pack := filepath.Join(data, "resource_packs", "custom")
	for _, path := range []string{world, pack} {
		if err := os.MkdirAll(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(world, "level.dat"), []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}
	const uuid = "2cf066eb-1254-4b7d-affb-80fe3216b18c"
	manifest := `{"header":{"name":"Custom","uuid":"` + uuid + `","version":[1,1,31]},"modules":[{"type":"resources"}],"subpacks":[{"folder_name":"SP2","name":"Fancy"}]}`
	if err := os.WriteFile(filepath.Join(pack, "manifest.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pack, "pack_icon.png"), []byte("icon"), 0644); err != nil {
		t.Fatal(err)
	}
	refsPath := filepath.Join(world, "world_resource_packs.json")
	if err := writePackRefs(refsPath, []packRef{{PackID: uuid, Version: []byte(`[1,1,31]`), Subpack: "SP2"}}); err != nil {
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
	a := &API{Root: root}
	base := "/api/server-packs/server/resource/custom"
	get := httptest.NewRecorder()
	a.serverPack(get, httptest.NewRequest("GET", base, nil))
	if get.Code != 200 || !strings.Contains(get.Body.String(), `"hasIcon":true`) || !strings.Contains(get.Body.String(), `"active":true`) {
		t.Fatalf("detail: %d %s", get.Code, get.Body.String())
	}
	icon := httptest.NewRecorder()
	a.serverPack(icon, httptest.NewRequest("GET", base+"/icon", nil))
	if icon.Code != 200 || icon.Body.String() != "icon" {
		t.Fatalf("icon: %d %s", icon.Code, icon.Body.String())
	}
	layer := filepath.Join(pack, "subpacks", "SP2")
	if err := os.MkdirAll(filepath.Join(layer, "textures", "blocks"), 0755); err != nil {
		t.Fatal(err)
	}
	texture := filepath.Join(layer, "textures", "blocks", "sample.png")
	if err := os.WriteFile(texture, []byte("sample image"), 0644); err != nil {
		t.Fatal(err)
	}
	atlas := filepath.Join(layer, "textures", "terrain_texture.json")
	if err := os.WriteFile(atlas, []byte(`{"texture_data":{"sample":{"textures":"textures/blocks/sample"},"sample_extra":{"textures":"textures/blocks/sample_extra"}}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(layer, "blocks.json"), []byte(`{"format_version":"1.19.30","demo:block":{"textures":"sample"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	assetURL := "/api/pack-assets/server/resource/custom"
	assetList := httptest.NewRecorder()
	a.packAssets(assetList, httptest.NewRequest("GET", assetURL, nil))
	if assetList.Code != 200 || !strings.Contains(assetList.Body.String(), `"id":"SP2"`) || !strings.Contains(assetList.Body.String(), `"selected":true`) {
		t.Fatalf("assets: %d %s", assetList.Code, assetList.Body.String())
	}
	request := `{"action":"archive","layer":"SP2","paths":["textures/blocks/sample.png"]}`
	preview := httptest.NewRecorder()
	a.packAssets(preview, httptest.NewRequest("POST", assetURL, strings.NewReader(strings.TrimSuffix(request, "}")+`,"preview":true}`)))
	if preview.Code != 200 || !strings.Contains(preview.Body.String(), `"edits":1`) || !strings.Contains(preview.Body.String(), `still uses texture alias sample`) {
		t.Fatalf("preview: %d %s", preview.Code, preview.Body.String())
	}
	if _, err := os.Stat(texture); err != nil {
		t.Fatalf("preview changed texture: %v", err)
	}
	archived := httptest.NewRecorder()
	a.packAssets(archived, httptest.NewRequest("POST", assetURL, strings.NewReader(request)))
	if archived.Code != 200 {
		t.Fatalf("archive: %d %s", archived.Code, archived.Body.String())
	}
	if _, err := os.Stat(texture); !os.IsNotExist(err) {
		t.Fatalf("texture still present: %v", err)
	}
	content, _ := os.ReadFile(atlas)
	if strings.Contains(string(content), `"sample"`) || !strings.Contains(string(content), `"sample_extra"`) {
		t.Fatalf("atlas after archive: %s", content)
	}
	restored := httptest.NewRecorder()
	a.packAssets(restored, httptest.NewRequest("POST", assetURL, strings.NewReader(strings.Replace(request, `"archive"`, `"restore"`, 1))))
	if restored.Code != 200 {
		t.Fatalf("restore: %d %s", restored.Code, restored.Body.String())
	}
	if _, err := os.Stat(texture); err != nil {
		t.Fatalf("texture not restored: %v", err)
	}
	content, _ = os.ReadFile(atlas)
	if !strings.Contains(string(content), `"sample"`) {
		t.Fatalf("atlas not restored: %s", content)
	}
	statePath := filepath.Join(root, "server", ".mcui", "asset-state", "resource", "custom.json")
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("state should be removed: %v", err)
	}
	deleted := httptest.NewRecorder()
	a.serverPack(deleted, httptest.NewRequest("DELETE", base, nil))
	if deleted.Code != 200 {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(pack); !os.IsNotExist(err) {
		t.Fatalf("pack folder still exists: %v", err)
	}
	refs, err := readPackRefs(refsPath)
	if err != nil || len(refs) != 0 {
		t.Fatalf("world refs: %+v, %v", refs, err)
	}
}

func TestPackDetailPageLoadsOnRefresh(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.html")
	if err := os.WriteFile(index, []byte("app shell"), 0644); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	serverPageHandler(index)(response, httptest.NewRequest("GET", "/servers/bedrock_ana/packs/resource/custom", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), "app shell") {
		t.Fatalf("pack page: %d %s", response.Code, response.Body.String())
	}
}
