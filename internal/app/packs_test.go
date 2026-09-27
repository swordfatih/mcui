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
	refs := []packRef{{PackID: "first", Version: []byte(`[1,2,3]`), Subpack: "SP2"}, {PackID: "second", Version: []byte(`[4,5,6]`)}}
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
	if len(read) != 2 || read[0].PackID != "first" || read[0].Subpack != "SP2" || len(version) != 3 || version[0] != 4 || version[2] != 6 {
		t.Fatalf("wrong references: %+v", read)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestPackActivationUsesUUIDWithVersionFallback(t *testing.T) {
	const uuid = "2cf066eb-1254-4b7d-affb-80fe3216b18c"
	refs := []packRef{{PackID: uuid, Version: []byte(`[1,1,31]`), Subpack: "SP2"}}
	packs := []packInfo{{UUID: uuid, Version: []byte(`"1.1.31"`), Order: -1}}
	applyPackRefs(packs, 0, refs)
	if !packs[0].Active || packs[0].Order != 0 {
		t.Fatalf("pack not active: %+v", packs[0])
	}
	multiple := []packInfo{{UUID: uuid, Version: []byte(`[1,1,30]`), Order: -1}, {UUID: uuid, Version: []byte(`[1,1,31]`), Order: -1}}
	applyPackRefs(multiple, 0, refs)
	if multiple[0].Active || !multiple[1].Active {
		t.Fatalf("wrong version active: %+v", multiple)
	}
}

func TestBedrockProvidedPackFoldersAndCommentedManifest(t *testing.T) {
	for _, folder := range []string{"vanilla", "vanilla_1.21.60", "chemistry", "chemistry_1.21.20", "editor"} {
		if !builtInPack("resource", folder) {
			t.Fatalf("%s should be Bedrock-provided", folder)
		}
	}
	for _, folder := range []string{"experimental_creator_cameras", "experimental_poi", "server_library", "server_ui_library", "server_editor_library"} {
		if !builtInPack("behavior", folder) {
			t.Fatalf("%s should be Bedrock-provided", folder)
		}
	}
	for _, folder := range []string{"my-vanilla-pack", "vanilla-custom", "actions-and-stuff", "HostileMobsReducer", "LongerDays"} {
		if builtInPack("behavior", folder) || builtInPack("resource", folder) {
			t.Fatalf("%s should not be classified", folder)
		}
	}
	manifest := "{\"header\":{\"name\":\"pack.name\",\"uuid\":\"a4df0cb3-17be-4163-88d7-fcf7002b935d\",\"version\":[1,21,20]},\"modules\":[{\"type\":\"resources\"}],\"dependencies\":[{// Chemistry behavior pack\n\"uuid\":\"34a4d6dd-3b78-48c8-88cd-ff6dfe36458c\",\"url\":\"https://example.com/a//b\"}]}"
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	parsed, err := readPackManifest(path)
	if err != nil || parsed.Header.Name != "pack.name" || packKind(parsed) != "resource" {
		t.Fatalf("commented manifest: %+v, %v", parsed, err)
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
	if !samePackVersion([]byte(`"1.2.3"`), []byte(`[1,2,3]`)) {
		t.Fatal("string and array versions did not match")
	}
}
