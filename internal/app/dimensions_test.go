package app

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const dimensionBP = "00000000-0000-4000-8000-000000000001"
const dimensionRP = "00000000-0000-4000-8000-000000000002"

func dimensionArchive(t *testing.T, extra map[string]string) []byte {
	t.Helper()
	files := map[string]string{
		"wrapped/world/level.dat": "level", "wrapped/world/db/CURRENT": "MANIFEST-000001",
		"wrapped/world/manifest.json":                    updateManifest("Template", "00000000-0000-4000-8000-000000000003", "world_template", `[1,0,0]`),
		"wrapped/world/behavior_packs/bp0/manifest.json": updateManifest("Dimension BP", dimensionBP, "data", `[1,0,0]`),
		"wrapped/world/resource_packs/rp0/manifest.json": `{"header":{"name":"Dimension RP","uuid":"` + dimensionRP + `","version":[1,0,0]},"modules":[{"type":"resources"}],"subpacks":[{"folder_name":"16x","name":"16x"}]}`,
		"wrapped/world/world_behavior_packs.json":        `[{"pack_id":"` + dimensionBP + `","version":[1,0,0]}]`,
		"wrapped/world/world_resource_packs.json":        `[{"pack_id":"` + dimensionRP + `","version":[1,0,0],"subpack":"16x"}]`,
	}
	for k, v := range extra {
		files[k] = v
	}
	var body bytes.Buffer
	writer := zip.NewWriter(&body)
	for path, value := range files {
		f, err := writer.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(value)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}
func postDimension(t *testing.T, a *API, archive []byte, filename string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	file, err := form.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	file.Write(archive)
	form.Close()
	req := httptest.NewRequest("POST", "/api/server-details/server/dimensions", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	a.dimensionsHandler(response, req, "server")
	return response
}
func waitDimension(t *testing.T, a *API, state string) dimensionStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current := a.dimensionState("server")
		if current.State == state {
			return current
		}
		if current.State == "failed" {
			t.Fatalf("import failed: %s", current.Message)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s: %+v", state, a.dimensionState("server"))
	return dimensionStatus{}
}
func fakeDimensionWorker(t *testing.T) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "worker.sh")
	// The Go/worker protocol is tested here; Python tests exercise real LevelDBs.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' '{\"type\":\"result\",\"dimensions\":[{\"name\":\"spark:aether\",\"id\":1000,\"chunks\":3}]}'\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCUI_DIMENSION_PYTHON", "/bin/sh")
	t.Setenv("MCUI_DIMENSION_SCRIPT", script)
}
func TestDimensionImportReviewWarningAndPackActivation(t *testing.T) {
	a, _, _, world := setupPackUpdateServer(t)
	fakeDimensionWorker(t)
	response := postDimension(t, a, dimensionArchive(t, nil), "Aether.mctemplate")
	if response.Code != 202 {
		t.Fatalf("upload: %d %s", response.Code, response.Body.String())
	}
	status := waitDimension(t, a, "ready")
	if len(status.Packs) != 2 || len(status.Dimensions) != 1 {
		t.Fatalf("unexpected discovery: %+v", status)
	}
	oldLevel, _ := os.ReadFile(filepath.Join(world, "level.dat"))
	oldRefs, _ := os.ReadFile(filepath.Join(world, packFile("resource")))
	// API callers must acknowledge the warning, too.
	response = httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/server-details/server/dimensions", strings.NewReader(`{"id":"`+status.ID+`","dimension":"spark:aether"}`))
	a.dimensionsHandler(response, req, "server")
	if response.Code != 400 {
		t.Fatalf("accepted missing warning acknowledgement: %d", response.Code)
	}
	refs, _ := os.ReadFile(filepath.Join(world, packFile("resource")))
	if !bytes.Equal(oldRefs, refs) {
		t.Fatal("review modified packs")
	}
	// Start and mutations cannot run while the archive is awaiting confirmation.
	response = httptest.NewRecorder()
	a.action(response, httptest.NewRequest("POST", "/api/servers/server/start", nil))
	if response.Code != 409 {
		t.Fatalf("start was not blocked: %d", response.Code)
	}
	response = httptest.NewRecorder()
	a.packsHandler(response, httptest.NewRequest("PUT", "/packs", strings.NewReader(`{}`)), "server")
	if response.Code != 409 {
		t.Fatalf("pack edit was not blocked: %d", response.Code)
	}
	response = httptest.NewRecorder()
	req = httptest.NewRequest("POST", "/api/server-details/server/dimensions", strings.NewReader(`{"id":"`+status.ID+`","dimension":"spark:aether","backupAcknowledged":true}`))
	a.dimensionsHandler(response, req, "server")
	if response.Code != 202 {
		t.Fatalf("apply: %d %s", response.Code, response.Body.String())
	}
	waitDimension(t, a, "complete")
	for _, kind := range []string{"resource", "behavior"} {
		refs, err := readPackRefs(filepath.Join(world, packFile(kind)))
		if err != nil {
			t.Fatal(err)
		}
		if len(refs) != 2 {
			t.Fatalf("wrong refs: %+v", refs)
		}
		if kind == "resource" && (refs[0].PackID != updateResourceUUID || refs[0].Subpack != "fancy" || refs[1].Subpack != "16x") {
			t.Fatalf("lost stack order/subpacks: %+v", refs)
		}
	}
	level, _ := os.ReadFile(filepath.Join(world, "level.dat"))
	if !bytes.Equal(oldLevel, level) {
		t.Fatal("changed level.dat")
	}
	if a.dimensionBusy("server") {
		t.Fatal("import still locks server")
	}
}
func TestDimensionCancelRemovesStage(t *testing.T) {
	a, _, _, _ := setupPackUpdateServer(t)
	fakeDimensionWorker(t)
	response := postDimension(t, a, dimensionArchive(t, nil), "world.mcworld")
	if response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	state := waitDimension(t, a, "ready")
	a.dimensionMu.Lock()
	stage := a.dimensions["server"].stage
	a.dimensionMu.Unlock()
	response = httptest.NewRecorder()
	a.dimensionsHandler(response, httptest.NewRequest("DELETE", "/dimensions?id="+state.ID, nil), "server")
	if response.Code != 200 || a.dimensionBusy("server") {
		t.Fatal("cancel failed")
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("stage retained after cancellation")
	}
}
func TestDimensionArchiveRejectsTraversal(t *testing.T) {
	a, _, _, _ := setupPackUpdateServer(t)
	fakeDimensionWorker(t)
	response := postDimension(t, a, dimensionArchive(t, map[string]string{"../escape": "bad"}), "world.zip")
	if response.Code != 202 {
		t.Fatal(response.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if a.dimensionState("server").State == "failed" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("unsafe archive was not rejected")
}
func TestDimensionPackUUIDConflict(t *testing.T) {
	a, rp, _, world := setupPackUpdateServer(t)
	source := t.TempDir()
	archive := filepath.Join(t.TempDir(), "world.zip")
	os.WriteFile(archive, dimensionArchive(t, nil), 0644)
	if err := extract(archive, source); err != nil {
		t.Fatal(err)
	}
	source = filepath.Join(source, "wrapped/world")
	os.WriteFile(filepath.Join(rp, "manifest.json"), []byte(updateManifest("Already installed", dimensionRP, "resources", `[1,0,0]`)), 0644)
	_, _, err := discoverDimensionPacks(source, filepath.Join(a.Root, "server/data"), world)
	if err == nil || !strings.Contains(err.Error(), "already installed") {
		t.Fatalf("expected UUID conflict: %v", err)
	}
}
func TestDimensionUploadRequiresStoppedServer(t *testing.T) {
	a, _, _, _ := setupPackUpdateServer(t)
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	script := "#!/bin/sh\ncase \"$*\" in\n *\"config --format json\") cat \"$MCUI_TEST_CONFIG\" ;;\n *\"ps -q mc\") echo container ;;\n *\"inspect\"*) echo running ;;\nesac\n"
	os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755)
	response := postDimension(t, a, dimensionArchive(t, nil), "world.zip")
	if response.Code != 409 {
		t.Fatalf("accepted running server: %d", response.Code)
	}
	var value map[string]string
	json.Unmarshal(response.Body.Bytes(), &value)
	if !strings.Contains(value["error"], "Stop") {
		t.Fatal(value)
	}
}
