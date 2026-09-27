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
	for _, name := range []string{"backup-pre-1.26.52", "Backup-older"} {
		dir := filepath.Join(volume, name)
		if err := os.Mkdir(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "old-pack.dat"), []byte("obsolete copy"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(world, "backup-world.zip"), []byte("old world"), 0644); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"resource_packs/vanilla_1.26.52/manifest.json": "builtin", "resource_packs/custom/manifest.json": "custom", "server.properties": "difficulty=normal", "bedrock_server": "runtime"} {
		path := filepath.Join(volume, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0644); err != nil {
			t.Fatal(err)
		}
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
case "$1" in
  lsf) if [ -f "$MCUI_TEST_REMOTE_LIST" ]; then cat "$MCUI_TEST_REMOTE_LIST"; fi ;;
  copyto) echo upload >> "$MCUI_TEST_OPERATIONS"; cp "$4" "$MCUI_TEST_ARCHIVE"; basename "$5" > "$MCUI_TEST_REMOTE_LIST" ;;
esac
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
	t.Setenv("MCUI_TEST_REMOTE_LIST", filepath.Join(bin, "remote-list"))
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
	if entries["data/worlds/world/level.dat"] != "world data" || entries["data/resource_packs/custom/manifest.json"] != "custom" || entries["data/server.properties"] != "difficulty=normal" {
		t.Fatalf("archive missing server files: %v", entries)
	}
	if entries["data/worlds/world/backup-world.zip"] != "old world" {
		t.Fatal("user file inside world omitted")
	}
	for _, name := range []string{"compose.yaml", "data/bedrock_server", "data/resource_packs/vanilla_1.26.52/manifest.json", "data/backup-pre-1.26.52/old-pack.dat"} {
		if _, ok := entries[name]; ok {
			t.Fatalf("archive contains non-user file %s", name)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".backup-stage", "bedrock-home")); !os.IsNotExist(err) {
		t.Fatalf("staging directory remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bedrock-home", "last-backup.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected local backup marker: %v", err)
	}
	nextManager, err := newBackupManager(a)
	if err != nil {
		t.Fatal(err)
	}
	if got := nextManager.state("bedrock-home"); got.State != "complete" || !strings.HasSuffix(got.SnapshotID, ".tar.gz") {
		t.Fatalf("last backup was not found on Drive: %+v", got)
	}
}

func TestBackupArchiveContainsOnlySelectedStage(t *testing.T) {
	stage := t.TempDir()
	source := t.TempDir()
	for _, dir := range []string{"worlds/world", "backup-pre-1.26.52", "resource_packs/vanilla_1.26.52", "resource_packs/custom"} {
		if err := os.MkdirAll(filepath.Join(source, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"worlds/world/level.dat": "world", "backup-pre-1.26.52/old.dat": "old", "resource_packs/vanilla_1.26.52/manifest.json": "builtin", "resource_packs/custom/manifest.json": "custom",
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := copyBackupTree(source, filepath.Join(stage, "data"), "bedrock"); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err := writeBackupArchive(stage, archive); err != nil {
		t.Fatal(err)
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
	entries := map[string]bool{}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = true
	}
	if !entries["data/worlds/world/level.dat"] || !entries["data/resource_packs/custom/manifest.json"] {
		t.Fatalf("archive omitted required files: %v", entries)
	}
	for _, name := range []string{"data/backup-pre-1.26.52/old.dat", "data/resource_packs/vanilla_1.26.52/manifest.json"} {
		if entries[name] {
			t.Fatalf("archive contains non-user file: %s", name)
		}
	}
}

func TestCopyBackupTreeKeepsPersistentData(t *testing.T) {
	source := t.TempDir()
	for _, dir := range []string{"worlds/world", "backup-pre-1.26.52", "worlds/world/Backups"} {
		if err := os.MkdirAll(filepath.Join(source, dir), 0755); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{
		"worlds/world/level.dat",
		"backup-pre-1.26.52/old.dat",
		"worlds/world/Backups/old.dat",
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0644); err != nil {
			t.Fatal(err)
		}
	}
	destination := filepath.Join(t.TempDir(), "data")
	if err := copyBackupTree(source, destination, "bedrock"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(destination, "worlds/world/level.dat")); err != nil {
		t.Fatalf("world missing from staged data: %v", err)
	}
	for _, name := range []string{"backup-pre-1.26.52"} {
		if _, err := os.Stat(filepath.Join(destination, name)); !os.IsNotExist(err) {
			t.Fatalf("backup entry was staged: %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(destination, "worlds/world/Backups/old.dat")); err != nil {
		t.Fatalf("world data missing: %v", err)
	}
}

func TestLatestBackupFromDriveListing(t *testing.T) {
	listing := "other-20260927T120000.000000000Z.tar.gz\nworld-20260926T120000.000000000Z.tar.gz\nworld-20260927T120000.000000000Z.tar.gz\nworld-bad.tar.gz\n"
	state := latestBackupFromListing("world", listing)
	if state.State != "complete" || state.SnapshotID != "world-20260927T120000.000000000Z.tar.gz" || state.CompletedAt == nil {
		t.Fatalf("wrong latest Drive backup: %+v", state)
	}
	if got := latestBackupFromListing("missing", listing); got.State != "idle" {
		t.Fatalf("unexpected backup: %+v", got)
	}
}

func TestRemoveLegacyBackupMarker(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "home")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "last-backup.json"), []byte(`{"state":"complete"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte("services: {}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := removeLegacyBackupMarkers(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "last-backup.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy marker remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.yaml")); err != nil {
		t.Fatal(err)
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
