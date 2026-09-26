package app

import (
	"archive/zip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateBedrockAndDiscover(t *testing.T) {
	a := API{Root: t.TempDir()}
	world := filepath.Join(t.TempDir(), "save")
	if err := os.Mkdir(world, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(world, "level.dat"), []byte("test"), 0644); err != nil {
		t.Fatal(err)
	}
	req := CreateRequest{Name: "first-world", Edition: "bedrock", Port: 19132, WorldPath: world, AcceptEULA: true}
	if err := a.create(req); err != nil {
		t.Fatal(err)
	}
	got, err := a.readServer(req.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.Edition != "bedrock" {
		t.Fatalf("unexpected server: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(a.Root, req.Name, "data", "worlds", "world", "level.dat")); err != nil {
		t.Fatal(err)
	}
	compose, err := os.ReadFile(filepath.Join(a.Root, req.Name, "compose.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(compose), "stdin_open: true") || !strings.Contains(string(compose), "tty: true") {
		t.Fatalf("Bedrock console input missing from Compose: %s", compose)
	}
}

func TestDiscoverExistingComposeWithoutPort(t *testing.T) {
	a := API{Root: t.TempDir()}
	dir := filepath.Join(a.Root, "bedrock_ana")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  minecraft:\n    image: itzg/minecraft-server:java21\n    environment:\n      EULA: 'TRUE'\n"
	if err := os.WriteFile(filepath.Join(dir, "docker-compose.yaml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	server, service, err := a.minecraftService("bedrock_ana")
	if err != nil {
		t.Fatal(err)
	}
	if server.Edition != "java" || service != "minecraft" || server.Port != 0 {
		t.Fatalf("unexpected discovery: %+v, %s", server, service)
	}
	response := httptest.NewRecorder()
	a.servers(response, httptest.NewRequest("GET", "/api/servers", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"name":"bedrock_ana"`) {
		t.Fatalf("server not listed: %d %s", response.Code, response.Body.String())
	}
}
func TestRejectZipTraversal(t *testing.T) {
	archive := filepath.Join(t.TempDir(), "bad.zip")
	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	w, err := z.Create("../escape/level.dat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("test")); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	a := API{Root: t.TempDir()}
	err = a.create(CreateRequest{Name: "unsafe", Edition: "java", Port: 25565, WorldPath: archive, AcceptEULA: true})
	if err == nil || !strings.Contains(err.Error(), "Unsafe archive path") {
		t.Fatalf("expected traversal rejection, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.Root, "unsafe")); !os.IsNotExist(err) {
		t.Fatalf("failed create left directory: %v", err)
	}
}
func TestCreateValidation(t *testing.T) {
	a := API{Root: t.TempDir()}
	cases := []CreateRequest{{Name: "../escape", Edition: "bedrock", Port: 19132, AcceptEULA: true}, {Name: "test", Edition: "bedrock", Port: 0, AcceptEULA: true}, {Name: "test", Edition: "bedrock", Port: 19132, WorldPath: "relative.zip", AcceptEULA: true}, {Name: "test", Edition: "bedrock", Port: 19132}}
	for _, req := range cases {
		if err := a.create(req); err == nil {
			t.Fatalf("accepted invalid request: %+v", req)
		}
	}
}

func TestServersHTTP(t *testing.T) {
	a := API{Root: t.TempDir()}
	body := strings.NewReader(`{"name":"bedrock-home","edition":"bedrock","port":19132,"worldPath":"","acceptEula":true}`)
	createReq := httptest.NewRequest("POST", "/api/servers", body)
	createResp := httptest.NewRecorder()
	a.servers(createResp, createReq)
	if createResp.Code != 201 {
		t.Fatalf("create: %d %s", createResp.Code, createResp.Body.String())
	}
	listResp := httptest.NewRecorder()
	a.servers(listResp, httptest.NewRequest("GET", "/api/servers", nil))
	if listResp.Code != 200 || !strings.Contains(listResp.Body.String(), `"name":"bedrock-home"`) {
		t.Fatalf("list: %d %s", listResp.Code, listResp.Body.String())
	}
}

func TestServerPageDeepLink(t *testing.T) {
	index := filepath.Join(t.TempDir(), "index.html")
	if err := os.WriteFile(index, []byte("<html>mcui</html>"), 0644); err != nil {
		t.Fatal(err)
	}
	handler := serverPageHandler(index)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/servers/bedrock_ana", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "mcui") {
		t.Fatalf("deep link: %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/servers/bedrock_ana/extra", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("nested path accepted: %d", response.Code)
	}
}
