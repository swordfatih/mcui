package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLatestPackStackUsesCompleteNewestGroup(t *testing.T) {
	logs := strings.Join([]string{
		"Pack Stack - [01] old (id: old)",
		"server started",
		"Pack Stack - [01] first (id: first)",
		"Pack Stack - [02] second (id: second)",
		"server ready",
	}, "\n")
	stack := latestPackStack(logs)
	if strings.Contains(stack, "old") || !strings.Contains(stack, "first") || !strings.Contains(stack, "second") {
		t.Fatalf("wrong latest stack: %q", stack)
	}
}

func TestPackReferencesKeepOrderAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "world_resource_packs.json")
	refs := []packRef{{PackID: "first", Version: []byte(`[1,2,3]`)}, {PackID: "second", Version: []byte(`[4,5,6]`)}}
	if err := writePackRefs(path, refs); err != nil {
		t.Fatal(err)
	}
	read, err := readPackRefs(path)
	if err != nil {
		t.Fatal(err)
	}
	var version []int
	if len(read) == 2 {
		_ = json.Unmarshal(read[1].Version, &version)
	}
	if len(read) != 2 || read[0].PackID != "first" || len(version) != 3 || version[0] != 4 || version[2] != 6 {
		t.Fatalf("wrong references: %+v", read)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestPackNameLocalizationAndVersionMatching(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "pack-folder")
	if err := os.MkdirAll(filepath.Join(folder, "texts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "texts", "en_US.lang"), []byte("pack.name=Real Resource Pack\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := resolvedPackName(folder, "pack.name"); got != "Real Resource Pack" {
		t.Fatalf("resolved name = %q", got)
	}
	if samePackVersion([]byte(`[1,2,3]`), []byte(`[1,2,4]`)) {
		t.Fatal("different versions matched")
	}
	if !samePackVersion([]byte(`[1, 2, 3]`), []byte(`[1,2,3]`)) {
		t.Fatal("same versions did not match")
	}
}
