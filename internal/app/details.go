package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"
)

type serverSettings struct {
	Image           string            `json:"image"`
	Restart         string            `json:"restart"`
	StopGracePeriod string            `json:"stopGracePeriod"`
	Environment     map[string]string `json:"environment"`
	Revision        string            `json:"revision"`
}

func yamlValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func setYAMLValue(node *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content[i+1] = value
			return
		}
	}
	node.Content = append(node.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

func setOptionalYAMLValue(node *yaml.Node, key, value string) {
	if value != "" {
		setYAMLValue(node, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
		return
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			node.Content = append(node.Content[:i], node.Content[i+2:]...)
			return
		}
	}
}

func (a *API) detailFile(name string) (string, []byte, *yaml.Node, *yaml.Node, error) {
	file, err := a.composeFile(name)
	if err != nil {
		return "", nil, nil, nil, err
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return "", nil, nil, nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return "", nil, nil, nil, err
	}
	_, service, err := a.minecraftService(name)
	if err != nil {
		return "", nil, nil, nil, err
	}
	if len(document.Content) == 0 {
		return "", nil, nil, nil, errors.New("empty Compose document")
	}
	services := yamlValue(document.Content[0], "services")
	settings := yamlValue(services, service)
	if settings == nil || settings.Kind != yaml.MappingNode {
		return "", nil, nil, nil, errors.New("Minecraft service must be a Compose mapping")
	}
	return file, content, &document, settings, nil
}

func revision(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func (a *API) serverDetails(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/server-details/"), "/")
	if len(parts) != 2 || !safeFolderName(parts[0]) {
		bad(w, 404, "Not found")
		return
	}
	name, action := parts[0], parts[1]
	if _, _, err := a.minecraftService(name); err != nil {
		bad(w, 404, "Server not found")
		return
	}
	switch action {
	case "resources", "storage":
		if r.Method != http.MethodGet {
			bad(w, 405, "Method not allowed")
			return
		}
		value, err := a.serverResources(r.Context(), name, action)
		if err != nil {
			respond(w, 200, map[string]any{"available": false, "reason": err.Error()})
			return
		}
		respond(w, 200, value)
	case "players":
		if r.Method != http.MethodGet {
			bad(w, 405, "Method not allowed")
			return
		}
		status, err := a.playerStatus(r.Context(), name)
		if err != nil {
			respond(w, 200, map[string]any{"available": false, "reason": err.Error()})
			return
		}
		respond(w, 200, status)
	case "logs":
		if r.Method != http.MethodGet {
			bad(w, 405, "Method not allowed")
			return
		}
		_, service, _ := a.minecraftService(name)
		args, _ := a.composeArgs(name)
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", append(args, "logs", "--tail", "200", "--no-color", service)...).CombinedOutput()
		if err != nil {
			bad(w, 502, "Docker logs: "+cleanError(out))
			return
		}
		respond(w, 200, map[string]string{"logs": string(out)})
	case "command":
		if r.Method != http.MethodPost {
			bad(w, 405, "Method not allowed")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 2048)
		var request struct {
			Command string `json:"command"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			bad(w, 400, "Invalid command request")
			return
		}
		command := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(request.Command), "/"))
		if command == "" || len(command) > 1024 || strings.IndexFunc(command, unicode.IsControl) >= 0 {
			bad(w, 400, "Enter a single command of at most 1024 bytes")
			return
		}
		if a.backup != nil && a.backup.capturing(name) {
			bad(w, 409, "Server is being captured for backup")
			return
		}
		if a.status(r.Context(), name) != "running" {
			bad(w, 409, "Start the server before sending commands")
			return
		}
		output, err := a.sendServerCommand(r.Context(), name, command)
		if err != nil {
			status := 502
			if errors.Is(err, errBedrockStdinClosed) {
				status = 409
			}
			bad(w, status, err.Error())
			return
		}
		respond(w, 200, map[string]string{"output": output})
	case "compose", "settings":
		if r.Method != http.MethodGet && r.Method != http.MethodPut {
			bad(w, 405, "Method not allowed")
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if r.Method == http.MethodPut && a.backup != nil && a.backup.capturing(name) {
			bad(w, 409, "Server is being captured for backup")
			return
		}
		file, content, document, service, err := a.detailFile(name)
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
		if r.Method == http.MethodGet {
			if action == "compose" {
				respond(w, 200, map[string]string{"yaml": string(content), "revision": revision(content)})
				return
			}
			env := map[string]string{}
			environment := yamlValue(service, "environment")
			if environment != nil {
				switch environment.Kind {
				case yaml.MappingNode:
					for i := 0; i+1 < len(environment.Content); i += 2 {
						env[environment.Content[i].Value] = environment.Content[i+1].Value
					}
				case yaml.SequenceNode:
					for _, item := range environment.Content {
						key, value, ok := strings.Cut(item.Value, "=")
						if ok {
							env[key] = value
						}
					}
				}
			}
			image := yamlValue(service, "image")
			value := ""
			if image != nil {
				value = image.Value
			}
			restart, grace := "", ""
			if node := yamlValue(service, "restart"); node != nil {
				restart = node.Value
			}
			if node := yamlValue(service, "stop_grace_period"); node != nil {
				grace = node.Value
			}
			respond(w, 200, serverSettings{Image: value, Restart: restart, StopGracePeriod: grace, Environment: env, Revision: revision(content)})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if action == "compose" {
			var req struct {
				YAML     string `json:"yaml"`
				Revision string `json:"revision"`
			}
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				bad(w, 400, "Invalid request")
				return
			}
			if req.Revision != revision(content) {
				bad(w, 409, "Compose file changed; reload before saving")
				return
			}
			content = []byte(req.YAML)
		} else {
			var req serverSettings
			if json.NewDecoder(r.Body).Decode(&req) != nil {
				bad(w, 400, "Invalid request")
				return
			}
			if req.Revision != revision(content) {
				bad(w, 409, "Compose file changed; reload before saving")
				return
			}
			if req.Image == "" || len(req.Environment) > 256 {
				bad(w, 400, "Invalid settings")
				return
			}
			switch req.Restart {
			case "", "no", "always", "on-failure", "unless-stopped":
			default:
				bad(w, 400, "Invalid restart policy")
				return
			}
			setYAMLValue(service, "image", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: req.Image})
			setOptionalYAMLValue(service, "restart", req.Restart)
			setOptionalYAMLValue(service, "stop_grace_period", req.StopGracePeriod)
			env := yamlValue(service, "environment")
			if env == nil || env.Kind != yaml.MappingNode {
				env = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			retained := make([]*yaml.Node, 0, len(env.Content))
			for i := 0; i+1 < len(env.Content); i += 2 {
				if value, ok := req.Environment[env.Content[i].Value]; ok {
					node := env.Content[i+1]
					node.Value = value
					node.Tag = "!!str"
					retained = append(retained, env.Content[i], node)
					delete(req.Environment, env.Content[i].Value)
				}
			}
			env.Content = retained
			keys := make([]string, 0, len(req.Environment))
			for key := range req.Environment {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				value := req.Environment[key]
				if key == "" || strings.ContainsAny(key, "=\n\r") {
					bad(w, 400, "Invalid environment key")
					return
				}
				setYAMLValue(env, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value})
			}
			setYAMLValue(service, "environment", env)
			content, err = yaml.Marshal(document)
			if err != nil {
				bad(w, 400, err.Error())
				return
			}
		}
		if err := a.saveCompose(file, content, name); err != nil {
			bad(w, 400, err.Error())
			return
		}
		respond(w, 200, map[string]string{"revision": revision(content)})
	default:
		bad(w, 404, "Not found")
	}
}

var errBedrockStdinClosed = errors.New("Bedrock console input is disabled. Add stdin_open: true and tty: true in Infrastructure → Compose source, then stop and start the server")

func (a *API) sendServerCommand(parent context.Context, name, command string) (string, error) {
	server, service, err := a.minecraftService(name)
	if err != nil {
		return "", err
	}
	config, err := a.readComposeConfig(name)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	id, err := a.serviceContainer(ctx, name, service, config)
	if err != nil {
		return "", err
	}
	args := []string{"exec", id}
	if server.Edition == "bedrock" {
		stdin, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Config.OpenStdin}}", id).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("Check Bedrock console input: %s", cleanError(stdin))
		}
		if strings.TrimSpace(string(stdin)) != "true" {
			return "", errBedrockStdinClosed
		}
		args = append(args, "send-command", command)
	} else {
		args = append(args, "rcon-cli", "--host", "127.0.0.1", command)
	}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("Send command: %s", cleanError(out))
	}
	return strings.TrimSpace(string(out)), nil
}

type playerStatus struct {
	Available bool     `json:"available"`
	Online    int      `json:"online"`
	Max       int      `json:"max"`
	Version   string   `json:"version,omitempty"`
	Players   []string `json:"players"`
}

type resourceStatus struct {
	Available     bool   `json:"available"`
	CPU           string `json:"cpu"`
	Memory        string `json:"memory"`
	MemoryPercent string `json:"memoryPercent"`
	Network       string `json:"network"`
	DiskIO        string `json:"diskIO"`
	Processes     string `json:"processes"`
}

func (a *API) serviceContainer(ctx context.Context, name, service string, config composeConfig) (string, error) {
	if id := config.Services[service].ContainerName; id != "" {
		return id, nil
	}
	args, err := a.composeArgs(name)
	if err != nil {
		return "", err
	}
	out, err := exec.CommandContext(ctx, "docker", append(args, "ps", "-q", service)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("find server container: %s", cleanError(out))
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return "", errors.New("Server container is unavailable")
	}
	return id, nil
}

func (a *API) serverResources(parent context.Context, name, kind string) (any, error) {
	_, service, err := a.minecraftService(name)
	if err != nil {
		return nil, err
	}
	config, err := a.readComposeConfig(name)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	id, err := a.serviceContainer(ctx, name, service, config)
	if err != nil {
		return nil, err
	}
	if kind == "storage" {
		out, err := exec.CommandContext(ctx, "docker", "exec", id, "du", "-sk", "/data").CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("measure /data: %s", cleanError(out))
		}
		fields := strings.Fields(string(out))
		if len(fields) == 0 {
			return nil, errors.New("Storage measurement is empty")
		}
		kilobytes, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return nil, err
		}
		return map[string]any{"available": true, "bytes": kilobytes * 1024}, nil
	}
	out, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{json .}}", id).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("Docker stats: %s", cleanError(out))
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, errors.New("Container has no live resource data")
	}
	var data struct {
		CPU           string `json:"CPUPerc"`
		Memory        string `json:"MemUsage"`
		MemoryPercent string `json:"MemPerc"`
		Network       string `json:"NetIO"`
		DiskIO        string `json:"BlockIO"`
		Processes     string `json:"PIDs"`
	}
	if err := json.Unmarshal(out, &data); err != nil {
		return nil, err
	}
	return resourceStatus{Available: true, CPU: data.CPU, Memory: data.Memory, MemoryPercent: data.MemoryPercent, Network: data.Network, DiskIO: data.DiskIO, Processes: data.Processes}, nil
}

var bedrockStatusPattern = regexp.MustCompile(`version=([^\s]+) online=([0-9]+) max=([0-9]+)`)

func parsePlayerStatus(edition string, output []byte) (playerStatus, error) {
	result := playerStatus{Available: true, Players: []string{}}
	if edition == "bedrock" {
		parts := bedrockStatusPattern.FindStringSubmatch(string(output))
		if len(parts) != 4 {
			return playerStatus{}, errors.New("Bedrock status has an unexpected format")
		}
		result.Version = parts[1]
		result.Online, _ = strconv.Atoi(parts[2])
		result.Max, _ = strconv.Atoi(parts[3])
		return result, nil
	}
	var data struct {
		ServerInfo struct {
			Version struct {
				Name string `json:"name"`
			} `json:"version"`
			Players struct {
				Online int `json:"online"`
				Max    int `json:"max"`
				Sample []struct {
					Name string `json:"name"`
				} `json:"sample"`
			} `json:"players"`
		} `json:"server_info"`
	}
	if err := json.Unmarshal(output, &data); err != nil {
		return playerStatus{}, err
	}
	result.Online = data.ServerInfo.Players.Online
	result.Max = data.ServerInfo.Players.Max
	if result.Max == 0 {
		return playerStatus{}, errors.New("Java server has not provided player status yet")
	}
	result.Version = data.ServerInfo.Version.Name
	for _, player := range data.ServerInfo.Players.Sample {
		if player.Name != "" && len(result.Players) < 20 {
			result.Players = append(result.Players, player.Name)
		}
	}
	return result, nil
}

func (a *API) playerStatus(parent context.Context, name string) (playerStatus, error) {
	server, service, err := a.minecraftService(name)
	if err != nil {
		return playerStatus{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 7*time.Second)
	defer cancel()
	config, err := a.readComposeConfig(name)
	if err != nil {
		return playerStatus{}, err
	}
	id, err := a.serviceContainer(ctx, name, service, config)
	if err != nil {
		return playerStatus{}, err
	}
	command := []string{"exec", id, "mc-monitor"}
	port := 25565
	if server.Edition == "bedrock" {
		port = 19132
	}
	for _, published := range config.Services[service].Ports {
		if (server.Edition == "bedrock" && published.Protocol == "udp") || (server.Edition == "java" && (published.Protocol == "tcp" || published.Protocol == "")) {
			port = published.Target
			break
		}
	}
	if server.Edition == "bedrock" {
		command = append(command, "status-bedrock", "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	} else {
		command = append(command, "status", "--json", "--timeout", "5s", "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	}
	out, err := exec.CommandContext(ctx, "docker", command...).CombinedOutput()
	if err != nil {
		return playerStatus{}, fmt.Errorf("Player status unavailable: %s", cleanError(out))
	}
	return parsePlayerStatus(server.Edition, out)
}

func (a *API) saveCompose(file string, content []byte, name string) error {
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil {
		return err
	}
	if len(document.Content) == 0 {
		return errors.New("empty Compose document")
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".compose-edit-*.yaml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	args := []string{"compose", "-p", projectName(name), "-f", tmp.Name(), "config", "--format", "json"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("invalid Compose file: %s", cleanError(out))
	}
	var config composeConfig
	if err := json.Unmarshal(out, &config); err != nil {
		return err
	}
	if _, _, err := minecraftServiceFromConfig(name, config); err != nil {
		return err
	}
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
