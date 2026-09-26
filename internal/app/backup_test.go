package app

import (
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
	world := filepath.Join(root, "bedrock-home", "data", "worlds", "world")
	if err := os.MkdirAll(world, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(world, "level.dat"), []byte("world data"), 0644); err != nil {
		t.Fatal(err)
	}
	config := t.TempDir()
	t.Setenv("MCUI_BACKUP_DIR", config)
	if err := os.WriteFile(filepath.Join(config, "rclone.conf"), []byte("[drive]\ntype = drive\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "restic-password"), []byte("test password"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	operations := filepath.Join(bin, "operations")
	dockerScript := `#!/bin/sh
case "$1" in
  inspect) echo running ;;
  compose)
    case " $* " in
      *" config "*) echo '{"services":{"mc":{"image":"itzg/minecraft-bedrock-server:latest"}}}' ;;
      *" ps "*) echo container-id ;;
      *" stop "*) echo stop >> "$MCUI_TEST_OPERATIONS" ;;
      *" up "*) echo up >> "$MCUI_TEST_OPERATIONS" ;;
    esac ;;
esac
`
	resticScript := `#!/bin/sh
case "$1" in
  cat) exit 1 ;;
  init) echo init >> "$MCUI_TEST_OPERATIONS" ;;
  backup)
    echo backup >> "$MCUI_TEST_OPERATIONS"
    echo '{"message_type":"summary","snapshot_id":"snapshot-123"}' ;;
esac
`
	for name, content := range map[string]string{"docker": dockerScript, "restic": resticScript, "rclone": "#!/bin/sh\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MCUI_TEST_OPERATIONS", operations)
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
			if state.SnapshotID != "snapshot-123" {
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
	if strings.TrimSpace(string(b)) != "stop\nup\ninit\nbackup" {
		t.Fatalf("unexpected operation order: %s", b)
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
	if got := nextManager.state("bedrock-home"); got.State != "complete" || got.SnapshotID != "snapshot-123" {
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
