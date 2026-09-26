package app

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupCapturesRestartsAndUploads(t *testing.T) {
	root := t.TempDir()
	a := &API{Root: root}
	if err := a.create(CreateRequest{Name: "bedrock-home", Edition: "bedrock", Port: 19132, AcceptEULA: true}); err != nil {
		t.Fatal(err)
	}
	volume := filepath.Join(root, "external-volume")
	world := filepath.Join(volume, "worlds", "world")
	if err := os.MkdirAll(world, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(world, "level.dat"), []byte("world data"), 0644); err != nil {
		t.Fatal(err)
	}
	config := t.TempDir()
	t.Setenv("MCUI_TEST_DATA_VOLUME", volume)
	t.Setenv("MCUI_BACKUP_DIR", config)
	if err := os.WriteFile(filepath.Join(config, "rclone.conf"), []byte("[drive]\ntype = drive\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	operations := filepath.Join(bin, "operations")
	dockerScript := `#!/bin/sh
case "$1" in
  inspect) echo running ;;
  compose)
    case " $* " in
      *" config "*) printf '{"services":{"mc":{"image":"itzg/minecraft-bedrock-server:latest","volumes":[{"type":"bind","source":"%s","target":"/data"}]}}}\n' "$MCUI_TEST_DATA_VOLUME" ;;
      *" ps "*) echo container-id ;;
      *" stop "*) echo stop >> "$MCUI_TEST_OPERATIONS" ;;
      *" up "*) echo up >> "$MCUI_TEST_OPERATIONS" ;;
    esac ;;
esac
`
	rcloneScript := `#!/bin/sh
echo upload >> "$MCUI_TEST_OPERATIONS"
cp "$4" "$MCUI_TEST_ARCHIVE"
`
	for name, content := range map[string]string{"docker": dockerScript, "rclone": rcloneScript} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MCUI_TEST_OPERATIONS", operations)
	archive := filepath.Join(bin, "uploaded.tar.gz")
	t.Setenv("MCUI_TEST_ARCHIVE", archive)
	manager, err := newBackupManager(a)
	if err != nil {
		t.Fatal(err)
	}
	a.backup = manager
	if err := manager.start("bedrock-home"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		state := manager.state("bedrock-home")
		if state.State == "complete" {
			if !strings.HasPrefix(state.SnapshotID, "bedrock-home-") || !strings.HasSuffix(state.SnapshotID, ".tar.gz") {
				t.Fatalf("wrong snapshot: %+v", state)
			}
			break
		}
		if state.State == "failed" {
			t.Fatalf("backup failed: %+v", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if manager.state("bedrock-home").State != "complete" {
		t.Fatal("backup did not complete")
	}
	b, err := os.ReadFile(operations)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "stop\nup\nupload" {
		t.Fatalf("unexpected operation order: %s", b)
	}
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	entries := map[string]string{}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			content, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			entries[header.Name] = string(content)
		}
	}
	if entries["data/worlds/world/level.dat"] != "world data" || entries["compose.yaml"] == "" {
		t.Fatalf("archive missing server files: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(root, ".backup-stage", "bedrock-home")); !os.IsNotExist(err) {
		t.Fatalf("staging directory remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bedrock-home", "last-backup.json")); err != nil {
		t.Fatalf("server backup marker missing: %v", err)
	}
	nextManager, err := newBackupManager(a)
	if err != nil {
		t.Fatal(err)
	}
	if got := nextManager.state("bedrock-home"); got.State != "complete" || !strings.HasSuffix(got.SnapshotID, ".tar.gz") {
		t.Fatalf("last backup was not persisted: %+v", got)
	}
}
func TestBackupRequiresCredentials(t *testing.T) {
	t.Setenv("MCUI_BACKUP_DIR", t.TempDir())
	a := &API{Root: t.TempDir()}
	if err := a.create(CreateRequest{Name: "bedrock", Edition: "bedrock", Port: 19132, AcceptEULA: true}); err != nil {
		t.Fatal(err)
	}
	manager, err := newBackupManager(a)
	if err != nil {
		t.Fatal(err)
	}
	a.backup = manager
	if err := manager.start("bedrock"); err == nil {
		t.Fatal("accepted backup without credentials")
	}
}
