package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type packRef struct {
	PackID  string          `json:"pack_id"`
	Version json.RawMessage `json:"version"`
	Subpack string          `json:"subpack,omitempty"`
}
type packManifest struct {
	Header struct {
		Name    string          `json:"name"`
		UUID    string          `json:"uuid"`
		Version json.RawMessage `json:"version"`
	} `json:"header"`
	Modules []struct {
		Type string `json:"type"`
	} `json:"modules"`
	Dependencies []struct {
		UUID    string          `json:"uuid"`
		Version json.RawMessage `json:"version"`
	} `json:"dependencies"`
	Subpacks []struct {
		FolderName string `json:"folder_name"`
		Name       string `json:"name"`
	} `json:"subpacks"`
}
type packInfo struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	UUID      string          `json:"uuid"`
	Version   json.RawMessage `json:"version"`
	Kind      string          `json:"kind"`
	Active    bool            `json:"active"`
	Order     int             `json:"order"`
	LoadState string          `json:"loadState"`
	BuiltIn   bool            `json:"builtIn"`
	HasIcon   bool            `json:"hasIcon"`
}
type packListing struct {
	Packs   []packInfo `json:"packs"`
	World   string     `json:"world"`
	Running bool       `json:"running"`
}

func latestPackStack(logs string) string {
	var stack []string
	inStack := false
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "Pack Stack - ") {
			if !inStack {
				stack = nil
			}
			stack = append(stack, line)
			inStack = true
		} else {
			inStack = false
		}
	}
	return strings.Join(stack, "\n")
}

var packUUID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func packFile(kind string) string {
	if kind == "resource" {
		return "world_resource_packs.json"
	}
	return "world_behavior_packs.json"
}
func packFolder(kind string) string {
	if kind == "resource" {
		return "resource_packs"
	}
	return "behavior_packs"
}
func (a *API) packPaths(name string) (string, string, error) {
	server, _, err := a.minecraftService(name)
	if err != nil || server.Edition != "bedrock" {
		return "", "", errors.New("Packs are available for Bedrock servers")
	}
	data, err := a.serverDataSource(name)
	if err != nil {
		return "", "", err
	}
	if data == "" {
		return "", "", errors.New("Pack management requires a bind-mounted /data folder")
	}
	worlds := filepath.Join(data, "worlds")
	entries, err := os.ReadDir(worlds)
	if err != nil {
		return "", "", errors.New("Start the server once to create its world")
	}
	var found []string
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		world := filepath.Join(worlds, entry.Name())
		if info, err := os.Lstat(filepath.Join(world, "level.dat")); err == nil && info.Mode().IsRegular() {
			found = append(found, world)
		}
	}
	if len(found) != 1 {
		return "", "", fmt.Errorf("Expected one world under %s; found %d", worlds, len(found))
	}
	return data, found[0], nil
}
func readPackRefs(path string) ([]packRef, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []packRef{}, nil
	}
	if err != nil {
		return nil, err
	}
	var refs []packRef
	if err := json.Unmarshal(content, &refs); err != nil {
		return nil, fmt.Errorf("Invalid %s: %w", filepath.Base(path), err)
	}
	return refs, nil
}
func readPackManifest(path string) (packManifest, error) {
	var manifest packManifest
	content, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(stripJSONComments(content), &manifest); err != nil {
		return manifest, err
	}
	if !packUUID.MatchString(manifest.Header.UUID) || len(manifest.Header.Version) == 0 || len(manifest.Modules) == 0 {
		return manifest, errors.New("Invalid pack manifest")
	}
	return manifest, nil
}
func stripJSONComments(content []byte) []byte {
	clean := append([]byte(nil), content...)
	inString, escaped := false, false
	for i := 0; i < len(clean); i++ {
		if inString {
			if escaped {
				escaped = false
			} else if clean[i] == '\\' {
				escaped = true
			} else if clean[i] == '"' {
				inString = false
			}
			continue
		}
		if clean[i] == '"' {
			inString = true
			continue
		}
		if clean[i] != '/' || i+1 >= len(clean) {
			continue
		}
		if clean[i+1] == '/' {
			for i < len(clean) && clean[i] != '\n' {
				clean[i] = ' '
				i++
			}
		} else if clean[i+1] == '*' {
			clean[i], clean[i+1] = ' ', ' '
			i += 2
			for i+1 < len(clean) && !(clean[i] == '*' && clean[i+1] == '/') {
				if clean[i] != '\n' {
					clean[i] = ' '
				}
				i++
			}
			if i+1 < len(clean) {
				clean[i], clean[i+1] = ' ', ' '
				i++
			}
		}
	}
	return clean
}
func builtInPack(kind, folder string) bool {
	return serverDataPolicy.builtins.match("bedrock", kind+"_packs/"+folder)
}
func resolvedPackName(folder, key string) string {
	if key == "" {
		return folder
	}
	if !strings.Contains(key, ".") {
		return key
	}
	for _, language := range []string{"en_US.lang", "en_GB.lang"} {
		content, err := os.ReadFile(filepath.Join(folder, "texts", language))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
			if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
				continue
			}
			name, value, ok := strings.Cut(line, "=")
			if ok && strings.TrimSpace(name) == key && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return filepath.Base(folder)
}
func samePackVersion(a, b json.RawMessage) bool {
	var left, right any
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	versionText := func(value any) string {
		switch value := value.(type) {
		case string:
			return value
		case []any:
			parts := make([]string, len(value))
			for i, part := range value {
				parts[i] = fmt.Sprint(part)
			}
			return strings.Join(parts, ".")
		default:
			encoded, _ := json.Marshal(value)
			return string(encoded)
		}
	}
	return versionText(left) == versionText(right)
}
func applyPackRefs(packs []packInfo, first int, refs []packRef) {
	for order, ref := range refs {
		candidates := []int{}
		chosen := -1
		for i := first; i < len(packs); i++ {
			if !strings.EqualFold(packs[i].UUID, ref.PackID) {
				continue
			}
			candidates = append(candidates, i)
			if samePackVersion(packs[i].Version, ref.Version) {
				chosen = i
			}
		}
		if chosen < 0 && len(candidates) == 1 {
			chosen = candidates[0]
		}
		if chosen >= 0 {
			packs[chosen].Active = true
			packs[chosen].Order = order
		}
	}
}
func packKind(manifest packManifest) string {
	for _, module := range manifest.Modules {
		if module.Type == "resources" {
			return "resource"
		}
	}
	for _, module := range manifest.Modules {
		if module.Type == "data" || module.Type == "script" {
			return "behavior"
		}
	}
	return ""
}
func packID(kind, folder string) string { return kind + "/" + folder }
func (a *API) listPacks(name string) (packListing, error) {
	var result packListing
	data, world, err := a.packPaths(name)
	if err != nil {
		return result, err
	}
	result.World = filepath.Base(world)
	result.Running = a.status(context.Background(), name) == "running"
	result.Packs = []packInfo{}
	for _, kind := range []string{"resource", "behavior"} {
		refs, err := readPackRefs(filepath.Join(world, packFile(kind)))
		if err != nil {
			return result, err
		}
		entries, err := os.ReadDir(filepath.Join(data, packFolder(kind)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return result, err
		}
		first := len(result.Packs)
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			manifest, err := readPackManifest(filepath.Join(data, packFolder(kind), entry.Name(), "manifest.json"))
			if err != nil || packKind(manifest) != kind {
				continue
			}
			folder := filepath.Join(data, packFolder(kind), entry.Name())
			info := packInfo{ID: packID(kind, entry.Name()), Name: resolvedPackName(folder, manifest.Header.Name), UUID: manifest.Header.UUID, Version: manifest.Header.Version, Kind: kind, Order: -1, LoadState: "unverified", BuiltIn: builtInPack(kind, entry.Name())}
			if icon, err := os.Lstat(filepath.Join(folder, "pack_icon.png")); err == nil && icon.Mode().IsRegular() {
				info.HasIcon = true
			}
			result.Packs = append(result.Packs, info)
		}
		applyPackRefs(result.Packs, first, refs)
		if kind == "resource" {
			for i := first; i < len(result.Packs); i++ {
				if result.Packs[i].Active {
					result.Packs[i].LoadState = "Selected in world JSON"
				}
			}
		}
	}
	if result.Running {
		_, service, _ := a.minecraftService(name)
		args, _ := a.composeArgs(name)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", append(args, "logs", "--tail", "500", "--no-color", service)...).CombinedOutput()
		if err == nil {
			logs := string(out)
			stack := latestPackStack(logs)
			for i := range result.Packs {
				p := &result.Packs[i]
				if !p.Active || p.Kind != "behavior" {
					continue
				}
				if strings.Contains(strings.ToLower(stack), strings.ToLower(p.UUID)) {
					p.LoadState = "seen in Pack Stack"
				} else if stack != "" {
					p.LoadState = "not in latest Pack Stack"
				}
				if strings.Contains(strings.ToLower(logs), strings.ToLower(p.UUID)) && p.LoadState == "unverified" {
					p.LoadState = "mentioned in logs"
				}
			}
		}
	}
	return result, nil
}
func writePackRefs(path string, refs []packRef) error {
	content, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	mode := os.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcui-packs-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
func (a *API) packsHandler(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method == http.MethodGet {
		listing, err := a.listPacks(name)
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
		respond(w, 200, listing)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.rejectDimensionMutation(w, name) {
		return
	}
	if a.backup != nil && a.backup.capturing(name) {
		bad(w, 409, "Server is being captured for backup")
		return
	}
	if r.Method == http.MethodPut {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		var request struct {
			Resource []string `json:"resource"`
			Behavior []string `json:"behavior"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			bad(w, 400, "Invalid pack order")
			return
		}
		listing, err := a.listPacks(name)
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
		_, world, _ := a.packPaths(name)
		for _, item := range []struct {
			kind string
			ids  []string
		}{{"resource", request.Resource}, {"behavior", request.Behavior}} {
			seen := map[string]bool{}
			refs := []packRef{}
			requested := map[string]bool{}
			for _, id := range item.ids {
				requested[id] = true
			}
			for _, pack := range listing.Packs {
				if pack.Kind == item.kind && pack.BuiltIn && requested[pack.ID] != pack.Active {
					bad(w, 403, "Bedrock-provided packs are read-only")
					return
				}
			}
			existing, err := readPackRefs(filepath.Join(world, packFile(item.kind)))
			if err != nil {
				bad(w, 400, err.Error())
				return
			}
			for _, id := range item.ids {
				if seen[id] {
					bad(w, 400, "Duplicate pack in order")
					return
				}
				seen[id] = true
				found := false
				for _, pack := range listing.Packs {
					if pack.ID == id && pack.Kind == item.kind {
						ref := packRef{PackID: pack.UUID, Version: pack.Version}
						if previous := packReferenceIndex(existing, pack); previous >= 0 {
							ref = existing[previous]
							ref.Version = pack.Version
						}
						refs = append(refs, ref)
						found = true
						break
					}
				}
				if !found {
					bad(w, 400, "Unknown pack in order")
					return
				}
			}
			if err := writePackRefs(filepath.Join(world, packFile(item.kind)), refs); err != nil {
				bad(w, 500, err.Error())
				return
			}
		}
		respond(w, 200, map[string]string{"message": "Pack order saved. Restart the server to apply it."})
		return
	}
	if r.Method != http.MethodPost {
		bad(w, 405, "Method not allowed")
		return
	}
	data, world, err := a.packPaths(name)
	if err != nil {
		bad(w, 400, err.Error())
		return
	}
	stage, err := os.MkdirTemp(data, ".mcui-pack-*")
	if err != nil {
		bad(w, 500, err.Error())
		return
	}
	preserveStage := false
	defer func() {
		if !preserveStage {
			_ = os.RemoveAll(stage)
		}
	}()
	var source io.Reader
	var filename string
	var closeSource io.Closer
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, 256<<20)
		file, header, err := r.FormFile("file")
		if err != nil {
			bad(w, 400, "Choose an archive file")
			return
		}
		source, closeSource, filename = file, file, header.Filename
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var request struct {
			URL string `json:"url"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			bad(w, 400, "Enter a download URL")
			return
		}
		u, err := url.Parse(request.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			bad(w, 400, "Use a public HTTPS URL")
			return
		}
		filename = filepath.Base(u.Path)
		client := &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: nil, DialContext: publicDial}}
		response, err := client.Get(u.String())
		if err != nil {
			bad(w, 400, err.Error())
			return
		}
		if response.StatusCode != 200 {
			response.Body.Close()
			bad(w, 400, fmt.Sprintf("Download returned HTTP %d", response.StatusCode))
			return
		}
		source, closeSource = response.Body, response.Body
	}
	defer closeSource.Close()
	low := strings.ToLower(filename)
	if !strings.HasSuffix(low, ".zip") && !strings.HasSuffix(low, ".mcaddon") && !strings.HasSuffix(low, ".mcpack") && !strings.HasSuffix(low, ".tar.gz") && !strings.HasSuffix(low, ".tgz") {
		bad(w, 400, "Use .zip, .mcaddon, .mcpack, .tar.gz, or .tgz")
		return
	}
	archive := filepath.Join(stage, "upload"+filepath.Ext(low))
	if strings.HasSuffix(low, ".tar.gz") {
		archive = filepath.Join(stage, "upload.tar.gz")
	}
	if strings.HasSuffix(low, ".tgz") {
		archive = filepath.Join(stage, "upload.tgz")
	}
	output, err := os.Create(archive)
	if err != nil {
		bad(w, 500, err.Error())
		return
	}
	n, err := io.Copy(output, io.LimitReader(source, (256<<20)+1))
	output.Close()
	if err != nil || n > 256<<20 {
		bad(w, 400, "Archive exceeds 256 MiB limit")
		return
	}
	extracted := filepath.Join(stage, "extracted")
	if err := os.Mkdir(extracted, 0755); err != nil {
		bad(w, 500, err.Error())
		return
	}
	if err := extract(archive, extracted); err != nil {
		bad(w, 400, err.Error())
		return
	}
	// MCADDON files often contain MCPACK archives rather than loose manifests.
	for depth := 0; depth < 2; depth++ {
		var nested []string
		if err := filepath.WalkDir(extracted, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && (strings.HasSuffix(strings.ToLower(path), ".mcpack") || strings.HasSuffix(strings.ToLower(path), ".zip")) {
				nested = append(nested, path)
			}
			return nil
		}); err != nil {
			bad(w, 400, err.Error())
			return
		}
		if len(nested) == 0 {
			break
		}
		for _, path := range nested {
			destination := path + ".unpacked"
			if err := os.Mkdir(destination, 0755); err != nil {
				bad(w, 400, err.Error())
				return
			}
			if err := extract(path, destination); err != nil {
				bad(w, 400, err.Error())
				return
			}
			if err := os.Remove(path); err != nil {
				bad(w, 500, err.Error())
				return
			}
		}
	}
	var candidates []string
	_ = filepath.WalkDir(extracted, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "manifest.json" && !entry.IsDir() {
			candidates = append(candidates, path)
		}
		return nil
	})
	if len(candidates) == 0 {
		bad(w, 400, "Archive has no pack manifest.json")
		return
	}
	listing, err := a.listPacks(name)
	if err != nil {
		bad(w, 400, err.Error())
		return
	}
	if r.URL.Query().Get("mode") == "update" {
		if a.status(r.Context(), name) != "stopped" {
			bad(w, 409, "Stop the server before updating packs")
			return
		}
		archiveHash, err := assetHash(archive)
		if err != nil {
			bad(w, 500, err.Error())
			return
		}
		updates, cleanup, err := preparePackUpdates(a, name, data, listing, candidates)
		if err != nil {
			bad(w, 409, err.Error())
			return
		}
		preserveMeta := false
		defer func() {
			if !preserveMeta {
				cleanup()
			}
		}()
		revision, err := packUpdateRevision(archiveHash, updates, world)
		if err != nil {
			bad(w, 409, err.Error())
			return
		}
		preview := packUpdatePreview{Revision: revision, Updates: []packUpdateSummary{}, Warnings: []string{}}
		for _, item := range updates {
			preview.Updates = append(preview.Updates, item.summary)
			preview.Warnings = append(preview.Warnings, item.warnings...)
		}
		if r.URL.Query().Get("preview") == "1" {
			respond(w, 200, preview)
			return
		}
		if r.URL.Query().Get("expectedHash") != revision {
			bad(w, 409, "Pack data or archive changed since review; review the update again")
			return
		}
		if err := applyPackUpdates(world, stage, updates); err != nil {
			if errors.Is(err, errPackRollback) {
				preserveStage, preserveMeta = true, true
			}
			bad(w, 500, err.Error())
			return
		}
		respond(w, 200, map[string]any{"updated": len(updates), "updates": preview.Updates, "warnings": preview.Warnings})
		return
	}
	type install struct{ source, target string }
	var installs []install
	seen := map[string]bool{}
	for _, path := range candidates {
		manifest, err := readPackManifest(path)
		if err != nil {
			bad(w, 400, "Invalid pack manifest: "+err.Error())
			return
		}
		kind := packKind(manifest)
		if kind == "" {
			bad(w, 400, "Unsupported pack module type")
			return
		}
		folder := filepath.Dir(path)
		hash := sha256.Sum256([]byte(strings.ToLower(manifest.Header.UUID)))
		target := filepath.Join(data, packFolder(kind), hex.EncodeToString(hash[:8]))
		if seen[target] {
			bad(w, 409, "Archive contains duplicate pack UUIDs")
			return
		}
		for _, existing := range listing.Packs {
			if strings.EqualFold(existing.UUID, manifest.Header.UUID) {
				bad(w, 409, "Pack UUID is already installed")
				return
			}
		}
		seen[target] = true
		if _, err := os.Stat(target); err == nil {
			bad(w, 409, "Pack UUID is already installed")
			return
		}
		installs = append(installs, install{folder, target})
	}
	installed := 0
	for _, item := range installs {
		target, folder := item.target, item.source
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			bad(w, 500, err.Error())
			return
		}
		if err := os.Rename(folder, target); err != nil {
			bad(w, 500, err.Error())
			return
		}
		installed++
	}
	respond(w, 200, map[string]any{"installed": installed})
}

func publicDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if port != "443" {
		return nil, errors.New("Only HTTPS port 443 is allowed")
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if !address.IP.IsGlobalUnicast() || address.IP.IsPrivate() || address.IP.IsLoopback() || address.IP.IsLinkLocalUnicast() {
			continue
		}
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
	}
	return nil, errors.New("Download host must have a public address")
}
