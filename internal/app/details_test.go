package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServerDetailsSettingsPreserveCompose(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "world")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "compose.yaml")
	original := "# server settings\nservices:\n  mc:\n    image: itzg/minecraft-bedrock-server:latest\n    environment:\n      EULA: 'TRUE'\n      SERVER_NAME: Old\n    ports:\n      - '19132:19132/udp'\n    volumes:\n      - ./world-data:/data\n    restart: unless-stopped\n"
	if err := os.WriteFile(file, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := "#!/bin/sh\necho '{\"services\":{\"mc\":{\"image\":\"itzg/minecraft-bedrock-server:latest\",\"ports\":[{\"published\":\"19132\",\"target\":19132,\"protocol\":\"udp\"}]}}}'\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MCUI_PUBLIC_HOST", "mc.example.com")
	a := &API{Root: root}
	server, _, err := a.minecraftService("world")
	if err != nil || server.Port != 19132 || server.Host != "mc.example.com" {
		t.Fatalf("server address: %+v, %v", server, err)
	}
	get := httptest.NewRecorder()
	a.serverDetails(get, httptest.NewRequest("GET", "/api/server-details/world/settings", nil))
	if get.Code != 200 {
		t.Fatalf("get settings: %d %s", get.Code, get.Body.String())
	}
	var settings serverSettings
	if err := json.Unmarshal(get.Body.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	settings.Environment["SERVER_NAME"] = "New"
	settings.Environment["DIFFICULTY"] = "hard"
	settings.StopGracePeriod = "2m"
	body, _ := json.Marshal(settings)
	put := httptest.NewRecorder()
	a.serverDetails(put, httptest.NewRequest("PUT", "/api/server-details/world/settings", strings.NewReader(string(body))))
	if put.Code != 200 {
		t.Fatalf("save settings: %d %s", put.Code, put.Body.String())
	}
	result, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"# server settings", "SERVER_NAME: New", "DIFFICULTY: hard", "19132:19132/udp", "./world-data:/data", "restart: unless-stopped", "stop_grace_period: 2m"} {
		if !strings.Contains(string(result), expected) {
			t.Fatalf("missing %q in %s", expected, result)
		}
	}
	stale := httptest.NewRecorder()
	a.serverDetails(stale, httptest.NewRequest("PUT", "/api/server-details/world/settings", strings.NewReader(string(body))))
	if stale.Code != 409 {
		t.Fatalf("stale revision accepted: %d", stale.Code)
	}
	invalid := httptest.NewRecorder()
	var compose struct {
		YAML     string `json:"yaml"`
		Revision string `json:"revision"`
	}
	compose.YAML = "services: ["
	compose.Revision = revision(result)
	badBody, _ := json.Marshal(compose)
	a.serverDetails(invalid, httptest.NewRequest("PUT", "/api/server-details/world/compose", strings.NewReader(string(badBody))))
	if invalid.Code != 400 {
		t.Fatalf("invalid YAML accepted: %d", invalid.Code)
	}
}

func TestParsePlayerStatus(t *testing.T) {
	java, err := parsePlayerStatus("java", []byte(`{"server_info":{"version":{"name":"1.21"},"players":{"online":2,"max":20,"sample":[{"name":"Alex"},{"name":"Sam"}]}}}`))
	if err != nil || !java.Available || java.Online != 2 || java.Max != 20 || len(java.Players) != 2 || java.Players[0] != "Alex" {
		t.Fatalf("Java status: %+v, %v", java, err)
	}
	bedrock, err := parsePlayerStatus("bedrock", []byte("127.0.0.1:19132 : version=1.21.1 online=3 max=10"))
	if err != nil || !bedrock.Available || bedrock.Online != 3 || bedrock.Max != 10 || bedrock.Version != "1.21.1" {
		t.Fatalf("Bedrock status: %+v, %v", bedrock, err)
	}
}

func TestPlayerStatusUsesExplicitContainerName(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "bedrock_ana")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	compose := "services:\n  mc:\n    image: itzg/minecraft-bedrock-server\n    container_name: bedrock\n    ports:\n      - '19132:19132/udp'\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yaml"), []byte(compose), 0644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
  *" config --format json") echo '{"services":{"mc":{"image":"itzg/minecraft-bedrock-server","container_name":"bedrock","ports":[{"published":"19132","target":19132,"protocol":"udp"}]}}}' ;;
  "exec bedrock mc-monitor status-bedrock --host 127.0.0.1 --port 19132") echo '127.0.0.1:19132 : version=1.26.52 online=1 max=10' ;;
  "stats --no-stream --format {{json .}} bedrock") echo '{"CPUPerc":"2.10%","MemUsage":"512MiB / 2GiB","MemPerc":"25.00%","NetIO":"1MB / 2MB","BlockIO":"3MB / 4MB","PIDs":"18"}' ;;
  "exec bedrock du -sk /data") echo '1048576 /data' ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	a := &API{Root: root}
	status, err := a.playerStatus(context.Background(), "bedrock_ana")
	if err != nil || status.Online != 1 || status.Max != 10 {
		t.Fatalf("player status: %+v, %v", status, err)
	}
	resources, err := a.serverResources(context.Background(), "bedrock_ana", "resources")
	if err != nil {
		t.Fatal(err)
	}
	metrics, ok := resources.(resourceStatus)
	if !ok || metrics.CPU != "2.10%" || metrics.MemoryPercent != "25.00%" {
		t.Fatalf("resources: %+v", resources)
	}
	storage, err := a.serverResources(context.Background(), "bedrock_ana", "storage")
	if err != nil {
		t.Fatal(err)
	}
	usage, ok := storage.(map[string]any)
	if !ok || usage["bytes"] != int64(1073741824) {
		t.Fatalf("storage: %+v", storage)
	}
}

func TestServerCommandUsesEditionConsole(t *testing.T) {
	for _, edition := range []string{"bedrock", "java"} {
		t.Run(edition, func(t *testing.T) {
			root := t.TempDir()
			serverDir := filepath.Join(root, "world")
			if err := os.Mkdir(serverDir, 0755); err != nil {
				t.Fatal(err)
			}
			image := "itzg/minecraft-server:latest"
			tool := "rcon-cli"
			if edition == "bedrock" {
				image = "itzg/minecraft-bedrock-server:latest"
				tool = "send-command"
			}
			compose := "services:\n  mc:\n    image: " + image + "\n"
			if err := os.WriteFile(filepath.Join(serverDir, "compose.yaml"), []byte(compose), 0644); err != nil {
				t.Fatal(err)
			}
			bin := t.TempDir()
			config := filepath.Join(bin, "config.json")
			configJSON, _ := json.Marshal(map[string]any{"services": map[string]any{"mc": map[string]string{"image": image, "container_name": "custom-server"}}})
			if err := os.WriteFile(config, configJSON, 0644); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
case "$*" in
  *" config --format json") cat "$MCUI_TEST_CONFIG" ;;
  *" ps -q mc") echo custom-server ;;
  "inspect --format {{.State.Status}} custom-server") echo running ;;
  "inspect --format {{.Config.OpenStdin}} custom-server") echo "${MCUI_TEST_OPEN_STDIN:-true}" ;;
  "exec custom-server "*)
    if [ "$3" = rcon-cli ]; then
      [ "$4" = --host ] && [ "$5" = 127.0.0.1 ] || exit 1
      printf '%s\n' "$3" "$6" > "$MCUI_TEST_COMMAND"
      echo 'RCON reply'
    else
      printf '%s\n' "$3" "$4" > "$MCUI_TEST_COMMAND"
    fi ;;
  *) exit 1 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("MCUI_TEST_CONFIG", config)
			commandFile := filepath.Join(bin, "command")
			t.Setenv("MCUI_TEST_COMMAND", commandFile)
			t.Setenv("MCUI_TEST_OPEN_STDIN", "true")
			a := &API{Root: root}
			body, _ := json.Marshal(map[string]string{"command": "/say hello & friends"})
			response := httptest.NewRecorder()
			a.serverDetails(response, httptest.NewRequest(http.MethodPost, "/api/server-details/world/command", strings.NewReader(string(body))))
			if response.Code != http.StatusOK {
				t.Fatalf("command: %d %s", response.Code, response.Body.String())
			}
			got, err := os.ReadFile(commandFile)
			if err != nil || string(got) != tool+"\nsay hello & friends\n" {
				t.Fatalf("wrong container command: %q, %v", got, err)
			}
			if edition == "java" && !strings.Contains(response.Body.String(), "RCON reply") {
				t.Fatalf("missing RCON response: %s", response.Body.String())
			}
			invalid, _ := json.Marshal(map[string]string{"command": "say one\nstop"})
			response = httptest.NewRecorder()
			a.serverDetails(response, httptest.NewRequest(http.MethodPost, "/api/server-details/world/command", strings.NewReader(string(invalid))))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("multiline command accepted: %d %s", response.Code, response.Body.String())
			}
			if edition == "bedrock" {
				t.Setenv("MCUI_TEST_OPEN_STDIN", "false")
				response = httptest.NewRecorder()
				a.serverDetails(response, httptest.NewRequest(http.MethodPost, "/api/server-details/world/command", strings.NewReader(string(body))))
				if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "stdin_open") {
					t.Fatalf("closed stdin: %d %s", response.Code, response.Body.String())
				}
			}
		})
	}
}
