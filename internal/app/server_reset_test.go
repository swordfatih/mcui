package app

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func putData(t *testing.T, root, name string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(name), 0644); err != nil {
		t.Fatal(err)
	}
}

func paths(items []resetEntry) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item.Path] = true
	}
	return out
}

func TestDataPolicyResetAndBackupAgree(t *testing.T) {
	for _, tc := range []struct {
		edition      string
		keep, remove []string
	}{
		{"bedrock", []string{"worlds/my-world/level.dat", "server.properties", "allowlist.json", "resource_packs/vanilla_custom/manifest.json", "behavior_packs/my_pack/manifest.json"}, []string{"resource_packs/vanilla_base/manifest.json", "behavior_packs/vanilla_base/manifest.json", "resource_packs/vanilla_1.26.52/manifest.json", "behavior_packs/server_library/manifest.json", "behavior_packs/experimental_1/manifest.json", "bedrock_server", "backup-pre-1/old"}},
		{"java", []string{"world/level.dat", "world_nether/level.dat", "adventure/maps/world/level.dat", "adventure/maps/world/region/r.0.0.mca", "server.properties", "whitelist.json", "mods/custom.jar"}, []string{"logs/latest.log", "libraries/x.jar", "adventure/notes.txt"}},
	} {
		t.Run(tc.edition, func(t *testing.T) {
			root := t.TempDir()
			for _, name := range append(append([]string{}, tc.keep...), tc.remove...) {
				putData(t, root, name)
			}
			plan, err := planData(root, tc.edition)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.keep {
				if !paths(plan.Keep)[name] {
					t.Errorf("not kept: %s", name)
				}
			}
			for _, name := range tc.remove {
				if !paths(plan.Delete)[name] {
					t.Errorf("not deleted: %s", name)
				}
			}
			backup := filepath.Join(t.TempDir(), "data")
			if err := copyBackupTree(root, backup, tc.edition); err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.keep {
				if _, err := os.Stat(filepath.Join(backup, name)); err != nil {
					t.Errorf("not backed up: %s: %v", name, err)
				}
			}
			for _, name := range tc.remove {
				if _, err := os.Stat(filepath.Join(backup, name)); !os.IsNotExist(err) {
					t.Errorf("backed up: %s: %v", name, err)
				}
			}
			if err := deleteDataFiles(root, plan); err != nil {
				t.Fatal(err)
			}
			for _, name := range tc.keep {
				if _, err := os.Stat(filepath.Join(root, name)); err != nil {
					t.Errorf("deleted: %s: %v", name, err)
				}
			}
			for _, name := range tc.remove {
				if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
					t.Errorf("still present: %s: %v", name, err)
				}
			}
		})
	}
}

func TestDataPlanRevisionAndSymlinks(t *testing.T) {
	root := t.TempDir()
	putData(t, root, "worlds/world/level.dat")
	first, err := planData(root, "bedrock")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	second, err := planData(root, "bedrock")
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision == second.Revision {
		t.Fatal("directory change did not change review revision")
	}
	if len(second.DeleteDirs) != 1 || second.DeleteDirs[0] != "empty" {
		t.Fatalf("empty folder missing from deletion plan: %+v", second.DeleteDirs)
	}
	java, err := planData(root, "java")
	if err != nil {
		t.Fatal(err)
	}
	if java.Revision == second.Revision {
		t.Fatal("edition change did not change review revision")
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := planData(root, "bedrock"); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := copyBackupTree(root, filepath.Join(t.TempDir(), "data"), "bedrock"); err == nil {
		t.Fatal("backup accepted symlink")
	}
}

func TestResetReviewRequiresCurrentRevision(t *testing.T) {
	root := t.TempDir()
	name := "home"
	serverDir := filepath.Join(root, name)
	data := filepath.Join(serverDir, "data")
	putData(t, data, "worlds/world/level.dat")
	putData(t, data, "bedrock_server")
	if err := os.WriteFile(filepath.Join(serverDir, "compose.yaml"), []byte("services: {}"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
case " $* " in
  *" config "*) printf '{"services":{"mc":{"image":"itzg/minecraft-bedrock-server:latest","volumes":[{"type":"bind","source":"%s","target":"/data"}]}}}' "$MCUI_TEST_DATA" ;;
  *" ps "*) : ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MCUI_TEST_DATA", data)
	a := &API{Root: root}
	get := httptest.NewRecorder()
	a.serverReset(get, httptest.NewRequest("GET", "/api/server-reset/home", nil))
	if get.Code != 200 {
		t.Fatalf("review: %d %s", get.Code, get.Body.String())
	}
	var review resetPlan
	if err := json.Unmarshal(get.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	putData(t, data, "new-runtime")
	post := func(revision string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		body, _ := json.Marshal(map[string]string{"confirm": name, "revision": revision})
		a.serverReset(w, httptest.NewRequest("POST", "/api/server-reset/home", strings.NewReader(string(body))))
		return w
	}
	if got := post(review.Revision); got.Code != 409 {
		t.Fatalf("stale review: %d %s", got.Code, got.Body.String())
	}
	if _, err := os.Stat(filepath.Join(data, "bedrock_server")); err != nil {
		t.Fatal("stale review deleted data")
	}
	get = httptest.NewRecorder()
	a.serverReset(get, httptest.NewRequest("GET", "/api/server-reset/home", nil))
	if err := json.Unmarshal(get.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	if got := post(review.Revision); got.Code != 200 {
		t.Fatalf("reset: %d %s", got.Code, got.Body.String())
	}
	if _, err := os.Stat(filepath.Join(data, "worlds/world/level.dat")); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"bedrock_server", "new-runtime"} {
		if _, err := os.Stat(filepath.Join(data, file)); !os.IsNotExist(err) {
			t.Fatalf("%s remains: %v", file, err)
		}
	}
	if _, err := os.Stat(filepath.Join(serverDir, "compose.yaml")); err != nil {
		t.Fatal("compose removed")
	}
}

func TestNamedVolumeResetUsesActualMountName(t *testing.T) {
	root := t.TempDir()
	name := "home"
	if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name, "compose.yaml"), []byte("services: {}"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
case " $* " in
  *" config "*) printf '{"services":{"mc":{"image":"itzg/minecraft-bedrock-server:latest","volumes":[{"type":"volume","source":"logical","target":"/data"}]}}}' ;;
  *" ps "*) echo container-id ;;
  *" inspect "*) echo '[{"Type":"volume","Name":"actual_project_logical","Destination":"/data"}]' ;;
  *" run "*) printf '%s\n' "$@" > "$MCUI_TEST_ARGS"; cat > "$MCUI_TEST_INPUT" ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	args := filepath.Join(bin, "args")
	input := filepath.Join(bin, "input")
	t.Setenv("MCUI_TEST_ARGS", args)
	t.Setenv("MCUI_TEST_INPUT", input)
	a := &API{Root: root}
	if err := a.deleteNamedVolumeFiles(context.Background(), name, resetPlan{Delete: []resetEntry{{Path: "bedrock_server"}}, DeleteDirs: []string{"old"}}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "actual_project_logical:/data") {
		t.Fatalf("wrong volume: %s", b)
	}
	b, err = os.ReadFile(input)
	if err != nil || strings.TrimSpace(string(b)) != "FYmVkcm9ja19zZXJ2ZXI=\nDb2xk" {
		t.Fatalf("wrong deletion input: %s %v", b, err)
	}
}
