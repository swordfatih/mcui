package app

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerFilesBrowseCleanupUploadDownloadDelete(t *testing.T) {
	root := t.TempDir()
	server := filepath.Join(root, "server")
	data := filepath.Join(server, "bedrock_data")
	backup := filepath.Join(data, "backup-pre-1.26.52.3")
	world := filepath.Join(data, "worlds", "BedrockWorld", "db")
	for _, dir := range []string{backup, world, filepath.Join(data, "resource_packs", "vanilla_1.26.52"), filepath.Join(data, "resource_packs", "vanilla_base")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(data, "worlds", "BedrockWorld", "level.dat"), []byte("world"), 0644); err != nil {
		t.Fatal(err)
	}
	custom := filepath.Join(data, "resource_packs", "custom")
	if err := os.MkdirAll(custom, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(custom, "manifest.json"), []byte(`{"header":{"name":"Custom","uuid":"2cf066eb-1254-4b7d-affb-80fe3216b18c","version":[1,0,0]},"modules":[{"type":"resources"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "resource_packs", "vanilla_base", "manifest.json"), []byte(`{"header":{"name":"Vanilla base","uuid":"0e04d6bc-f46c-4d34-a35f-c2bf78e50b88","version":[1,0,0]},"modules":[{"type":"resources"}]}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "old.txt"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(server, "compose.yaml"), []byte("services:\n  mc:\n    image: itzg/minecraft-bedrock-server\n"), 0644); err != nil {
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
	base := "/api/server-files/server"
	review := httptest.NewRecorder()
	a.serverFiles(review, httptest.NewRequest("GET", base+"?cleanup=1", nil))
	if review.Code != 200 || !strings.Contains(review.Body.String(), "backup-pre-1.26.52.3") || !strings.Contains(review.Body.String(), "Unselected resource pack") || strings.Contains(review.Body.String(), "vanilla_1.26.52") || strings.Contains(review.Body.String(), "vanilla_base") {
		t.Fatalf("cleanup: %d %s", review.Code, review.Body.String())
	}
	list := httptest.NewRecorder()
	a.serverFiles(list, httptest.NewRequest("GET", base+"?path=bedrock_data", nil))
	if list.Code != 200 || !strings.Contains(list.Body.String(), "backup-pre-1.26.52.3") {
		t.Fatalf("list: %d %s", list.Code, list.Body.String())
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "new.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("uploaded")); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	upload := httptest.NewRecorder()
	request := httptest.NewRequest("POST", base+"?path=bedrock_data", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	a.serverFiles(upload, request)
	if upload.Code != 200 {
		t.Fatalf("upload: %d %s", upload.Code, upload.Body.String())
	}
	download := httptest.NewRecorder()
	a.serverFiles(download, httptest.NewRequest("GET", base+"?path=bedrock_data/new.txt&download=1", nil))
	if download.Code != 200 || download.Body.String() != "uploaded" {
		t.Fatalf("download: %d %s", download.Code, download.Body.String())
	}
	for _, rel := range []string{"bedrock_data", "bedrock_data/worlds", "bedrock_data/resource_packs/vanilla_1.26.52", "bedrock_data/resource_packs/vanilla_base", "compose.yaml"} {
		response := httptest.NewRecorder()
		a.serverFiles(response, httptest.NewRequest("DELETE", base+"?path="+rel, nil))
		if response.Code != 403 {
			t.Fatalf("protected %s: %d %s", rel, response.Code, response.Body.String())
		}
	}
	deleted := httptest.NewRecorder()
	a.serverFiles(deleted, httptest.NewRequest("DELETE", base+"?path=bedrock_data/backup-pre-1.26.52.3", nil))
	if deleted.Code != 200 {
		t.Fatalf("delete: %d %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("backup still exists: %v", err)
	}
}

func TestSearchServerFilesSkipsMetadataAndSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested", "deeper"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "deeper", "target.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".mcui"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcui", "target-secret.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "nested"), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	results, err := searchServerFiles(root, "target")
	if err != nil || len(results.Entries) != 1 || results.Entries[0].Path != "nested/deeper/target.json" {
		t.Fatalf("search: %+v, %v", results, err)
	}
}

func TestPreviewServerFileServesTextWithoutRenderingMarkup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.html")
	content := []byte("<script>alert(1)</script>")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	previewServerFile(w, path, info)
	if w.Code != 200 || w.Header().Get("Content-Type") != "text/plain; charset=utf-8" || w.Body.String() != string(content) {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
}
