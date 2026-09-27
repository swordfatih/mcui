package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeImportArchive(t *testing.T, files map[string]string) string {
	t.Helper()
	stage := t.TempDir()
	for name, value := range files {
		path := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := writeBackupArchive(stage, archive); err != nil {
		t.Fatal(err)
	}
	return archive
}

func TestCreateFromBackupKeepsUserDataAndNewCompose(t *testing.T) {
	archive := makeImportArchive(t, map[string]string{
		"compose.yaml": "old compose", "data/worlds/my-world/level.dat": "world", "data/server.properties": "level-name=my-world", "data/allowlist.json": "[]", "data/resource_packs/custom/manifest.json": "custom", "data/resource_packs/vanilla_1.26/manifest.json": "builtin", "data/bedrock_server": "runtime",
	})
	root := t.TempDir()
	a := &API{Root: root}
	if err := a.create(CreateRequest{Name: "restored", Edition: "bedrock", Port: 19140, BackupPath: archive, AcceptEULA: true}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "restored")
	for _, name := range []string{"worlds/my-world/level.dat", "server.properties", "allowlist.json", "resource_packs/custom/manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, "data", name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	if info, err := os.Stat(filepath.Join(dir, "data/worlds/my-world")); err != nil || info.Mode().Perm()&0005 != 0005 {
		t.Fatalf("imported world directory is not readable: %v, %v", info, err)
	}
	for _, name := range []string{"bedrock_server", "resource_packs/vanilla_1.26/manifest.json"} {
		if _, err := os.Stat(filepath.Join(dir, "data", name)); !os.IsNotExist(err) {
			t.Errorf("imported non-user file %s: %v", name, err)
		}
	}
	compose, err := os.ReadFile(filepath.Join(dir, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), `LEVEL_NAME: "my-world"`) || !strings.Contains(string(compose), "19140:19132/udp") || strings.Contains(string(compose), "old compose") {
		t.Fatalf("incorrect new compose: %s", compose)
	}
	if _, err := os.Stat(filepath.Join(dir, "last-backup.json")); !os.IsNotExist(err) {
		t.Fatalf("created backup marker: %v", err)
	}
}

func TestCreateFromJavaBackupKeepsCustomWorld(t *testing.T) {
	archive := makeImportArchive(t, map[string]string{"data/adventure/maps/world/level.dat": "world", "data/adventure/maps/world/region/r.0.0.mca": "chunk", "data/server.properties": "level-name=adventure/maps/world", "data/logs/latest.log": "runtime"})
	a := &API{Root: t.TempDir()}
	if err := a.create(CreateRequest{Name: "java", Edition: "java", Port: 25570, BackupPath: archive, AcceptEULA: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.Root, "java", "data", "adventure/maps/world/region/r.0.0.mca")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.Root, "java", "data", "logs/latest.log")); !os.IsNotExist(err) {
		t.Fatalf("runtime file imported: %v", err)
	}
}

func TestCreateFromUploadedBackup(t *testing.T) {
	archive := makeImportArchive(t, map[string]string{"data/world/level.dat": "world"})
	content, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for name, value := range map[string]string{"name": "uploaded", "edition": "java", "port": "25565", "acceptEula": "true"} {
		if err := form.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := form.CreateFormFile("backupFile", "backup.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	a := &API{Root: t.TempDir()}
	request := httptest.NewRequest("POST", "/api/servers", &body)
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	a.servers(response, request)
	if response.Code != 201 {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	if _, err := os.Stat(filepath.Join(a.Root, "uploaded", "data/world/level.dat")); err != nil {
		t.Fatal(err)
	}
}

func TestDriveURLImportUsesConfiguredRemote(t *testing.T) {
	archive := makeImportArchive(t, map[string]string{"data/world/level.dat": "world"})
	config := t.TempDir()
	if err := os.WriteFile(filepath.Join(config, "rclone.conf"), []byte("[drive]\ntype = drive\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCUI_BACKUP_DIR", config)
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = backend ] && [ "$2" = copyid ] && [ "$3" = drive: ] && [ "$4" = 1234567890ABCDEF ]; then cp "$MCUI_TEST_ARCHIVE" "$5"; else exit 1; fi
`
	if err := os.WriteFile(filepath.Join(bin, "rclone"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MCUI_TEST_ARCHIVE", archive)
	a := &API{Root: t.TempDir()}
	manager, err := newBackupManager(a)
	if err != nil {
		t.Fatal(err)
	}
	a.backup = manager
	if err := a.createWithContext(context.Background(), CreateRequest{Name: "drive", Edition: "java", Port: 25565, BackupURL: "https://drive.google.com/file/d/1234567890ABCDEF/view?usp=sharing", AcceptEULA: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(a.Root, "drive", "data/world/level.dat")); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{"http://drive.google.com/file/d/1234567890ABCDEF/view", "https://evil.example/file/d/1234567890ABCDEF", "https://drive.google.com.evil.example/file/d/1234567890ABCDEF/view"} {
		if _, err := parseDriveFileURL(url); err == nil {
			t.Errorf("accepted unsafe URL: %s", url)
		}
	}
}

func TestBackupImportRejectsWrongEditionAndRollsBack(t *testing.T) {
	archive := makeImportArchive(t, map[string]string{"data/world/level.dat": "world", "data/world/link": "ordinary"})
	a := &API{Root: t.TempDir()}
	if err := a.create(CreateRequest{Name: "bad", Edition: "bedrock", Port: 19132, BackupPath: archive, AcceptEULA: true}); err == nil {
		t.Fatal("accepted Java backup as Bedrock")
	}
	if _, err := os.Stat(filepath.Join(a.Root, "bad")); !os.IsNotExist(err) {
		t.Fatalf("failed import left server folder: %v", err)
	}
}

func TestBackupImportRejectsTarTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "unsafe.tar.gz")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tarWriter := tar.NewWriter(gz)
	content := []byte("escape")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "../escape", Mode: 0644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	a := &API{Root: t.TempDir()}
	if err := a.create(CreateRequest{Name: "unsafe", Edition: "java", Port: 25565, BackupPath: archive, AcceptEULA: true}); err == nil {
		t.Fatal("accepted archive traversal")
	}
	if _, err := os.Stat(filepath.Join(a.Root, "unsafe")); !os.IsNotExist(err) {
		t.Fatalf("failed import left server folder: %v", err)
	}
}
