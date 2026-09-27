package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReferenceEditsRestoresSavedBlockEntry(t *testing.T) {
	layer := t.TempDir()
	path := filepath.Join(layer, "blocks.json")
	if err := os.WriteFile(path, []byte(`{"format_version":"1.19.30"}`), 0644); err != nil {
		t.Fatal(err)
	}
	value := json.RawMessage(`{"textures":"slab_alias"}`)
	records := []assetRecord{{Path: "textures/blocks/slab/example.png", Patches: []assetPatch{{File: "blocks.json", Key: "demo:slab", Value: value}}}}
	edits, _, _, err := referenceEdits(layer, "restore", records)
	if err != nil || len(edits) != 1 || !strings.Contains(string(edits[0].After), `"demo:slab"`) {
		t.Fatalf("restore edits: %+v, %v", edits, err)
	}
	if err := os.WriteFile(path, []byte(`{"demo:slab":{"textures":"changed"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := referenceEdits(layer, "restore", records); err == nil {
		t.Fatal("expected conflict with existing block entry")
	}
}
