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
	manifest := `{"header":{"name":"Custom","uuid":"` + uuid + `","version":[1,1,31]},"modules":[{"type":"resources"}]}`
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
