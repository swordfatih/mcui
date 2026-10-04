package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type dimensionSummary struct {
	Name   string `json:"name"`
	ID     int    `json:"id"`
	Chunks int    `json:"chunks"`
}
type dimensionPack struct {
	Name           string `json:"name"`
	Kind           string `json:"kind"`
	UUID           string `json:"uuid"`
	source, target string
	ref            packRef
}
type dimensionStatus struct {
	Installed  []installedDimension `json:"installed,omitempty"`
	ID         string               `json:"id,omitempty"`
	State      string               `json:"state"`
	Message    string               `json:"message,omitempty"`
	Dimensions []dimensionSummary   `json:"dimensions,omitempty"`
	Packs      []dimensionPack      `json:"packs,omitempty"`
	Completed  int                  `json:"completed,omitempty"`
	Total      int                  `json:"total,omitempty"`
}
type dimensionJob struct {
	status                     dimensionStatus // protected by dimensionMu
	stage, source, data, world string
	refs                       map[string][]packRef
	expires                    *time.Timer
}

func (a *API) dimensionState(name string) dimensionStatus {
	a.dimensionMu.Lock()
	defer a.dimensionMu.Unlock()
	if job := a.dimensions[name]; job != nil {
		return job.status
	}
	return dimensionStatus{State: "idle"}
}
func (a *API) dimensionBusy(name string) bool {
	switch a.dimensionState(name).State {
	case "uploading", "analyzing", "ready", "importing", "removing":
		return true
	}
	return false
}
func (a *API) rejectDimensionMutation(w http.ResponseWriter, name string) bool {
	if a.dimensionBusy(name) {
		bad(w, 409, "A custom dimension operation is in progress; wait for it to finish or cancel the review")
		return true
	}
	return false
}
func (a *API) dimensionProgress(job *dimensionJob, state, message string) {
	a.dimensionMu.Lock()
	defer a.dimensionMu.Unlock()
	job.status.State, job.status.Message = state, message
}
func (a *API) dimensionsHandler(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method == http.MethodGet {
		state, err := a.dimensionListing(name)
		if err != nil {
			bad(w, 500, err.Error())
			return
		}
		respond(w, 200, state)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", "GET, POST, DELETE")
		bad(w, 405, "Method not allowed")
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method == http.MethodDelete {
		a.dimensionMu.Lock()
		job := a.dimensions[name]
		if job == nil || job.status.ID != r.URL.Query().Get("id") || job.status.State != "ready" {
			a.dimensionMu.Unlock()
			bad(w, 409, "Only a ready import can be cancelled")
			return
		}
		job.expires.Stop()
		job.status.State, job.status.Message = "cancelled", "Import cancelled"
		a.dimensionMu.Unlock()
		os.RemoveAll(job.stage)
		respond(w, 200, a.dimensionState(name))
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var req struct {
			Action             string `json:"action"`
			Revision           string `json:"revision"`
			ID                 string `json:"id"`
			Dimension          string `json:"dimension"`
			BackupAcknowledged bool   `json:"backupAcknowledged"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || !req.BackupAcknowledged {
			bad(w, 400, "Acknowledge the backup warning before continuing")
			return
		}
		if req.Action == "remove" {
			a.startDimensionRemoval(w, r, name, req.Dimension, req.Revision)
			return
		}
		if req.Action != "" {
			bad(w, 400, "Unknown dimension action")
			return
		}
		a.dimensionMu.Lock()
		job := a.dimensions[name]
		if job == nil || job.status.ID != req.ID || job.status.State != "ready" {
			a.dimensionMu.Unlock()
			bad(w, 409, "Upload and review the archive again")
			return
		}
		found := false
		for _, dim := range job.status.Dimensions {
			if dim.Name == req.Dimension {
				found = true
			}
		}
		if !found {
			a.dimensionMu.Unlock()
			bad(w, 400, "Select a detected custom dimension")
			return
		}
		a.dimensionMu.Unlock()
		if a.status(r.Context(), name) != "stopped" {
			bad(w, 409, "Stop the server before importing a custom dimension")
			return
		}
		a.dimensionMu.Lock()
		job.expires.Stop()
		job.status.State, job.status.Message = "importing", "Validating dimension and destination"
		a.dimensionMu.Unlock()
		go a.applyDimension(name, job, req.Dimension)
		respond(w, 202, a.dimensionState(name))
		return
	}
	if a.rejectDimensionMutation(w, name) {
		return
	}
	if a.backup != nil && a.backup.active(name) {
		bad(w, 409, "Wait for the current backup to finish")
		return
	}
	data, world, err := a.packPaths(name)
	if err != nil {
		bad(w, 400, err.Error())
		return
	}
	if a.status(r.Context(), name) != "stopped" {
		bad(w, 409, "Stop the server before importing a custom dimension")
		return
	}
	// Stage on the data filesystem so pack-stack activation can use renames.
	stage, err := os.MkdirTemp(data, ".mcui-dimension-")
	if err != nil {
		bad(w, 500, err.Error())
		return
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(stage)
		}
	}()
	r.Body = http.MaxBytesReader(w, r.Body, maxArchiveBytes+(1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		if r.MultipartForm != nil {
			r.MultipartForm.RemoveAll()
		}
		bad(w, 400, "Archive upload exceeds 4 GiB or is invalid")
		return
	}
	defer r.MultipartForm.RemoveAll()
	source, header, err := r.FormFile("file")
	if err != nil {
		bad(w, 400, "Choose a world archive")
		return
	}
	defer source.Close()
	extension := strings.ToLower(filepath.Ext(header.Filename))
	if extension != ".zip" && extension != ".mcworld" && extension != ".mctemplate" {
		bad(w, 400, "Use .zip, .mcworld, or .mctemplate")
		return
	}
	archive := filepath.Join(stage, "archive"+extension)
	out, err := os.Create(archive)
	if err != nil {
		bad(w, 500, err.Error())
		return
	}
	n, copyErr := io.Copy(out, io.LimitReader(source, maxArchiveBytes+1))
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil || n > maxArchiveBytes {
		bad(w, 400, "Unable to save archive (maximum 4 GiB)")
		return
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		bad(w, 500, "Unable to create import ID")
		return
	}
	job := &dimensionJob{stage: stage, data: data, world: world, status: dimensionStatus{ID: hex.EncodeToString(token), State: "analyzing", Message: "Extracting world archive"}}
	a.dimensionMu.Lock()
	if a.dimensions == nil {
		a.dimensions = map[string]*dimensionJob{}
	}
	a.dimensions[name] = job
	a.dimensionMu.Unlock()
	keep = true
	go a.inspectDimension(name, job, archive)
	respond(w, 202, a.dimensionState(name))
}

func (a *API) inspectDimension(name string, job *dimensionJob, archive string) {
	fail := func(err error) { os.RemoveAll(job.stage); a.dimensionProgress(job, "failed", err.Error()) }
	root := filepath.Join(job.stage, "extracted")
	if err := os.Mkdir(root, 0755); err != nil {
		fail(err)
		return
	}
	if err := extract(archive, root); err != nil {
		fail(err)
		return
	}
	source, err := findWorld(root, "bedrock")
	if err != nil {
		fail(err)
		return
	}
	if info, err := os.Stat(filepath.Join(source, "db")); err != nil || !info.IsDir() {
		fail(errors.New("Archive must contain a Bedrock world with level.dat and db/"))
		return
	}
	job.source = source
	packs, refs, err := discoverDimensionPacks(source, job.data, job.world)
	if err != nil {
		fail(err)
		return
	}
	job.refs = refs
	a.dimensionMu.Lock()
	job.status.Packs = packs
	a.dimensionMu.Unlock()
	a.dimensionProgress(job, "analyzing", "Reading custom dimensions")
	result, err := a.runDimensionWorker(job, "inspect", "")
	if err != nil {
		fail(err)
		return
	}
	if len(result.Dimensions) == 0 {
		fail(errors.New("Archive contains no supported custom dimensions"))
		return
	}
	a.dimensionMu.Lock()
	job.status.Dimensions = result.Dimensions
	job.status.State, job.status.Message = "ready", "Review the dimension and make your own backup before importing"
	job.expires = time.AfterFunc(30*time.Minute, func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.dimensionMu.Lock()
		if job.status.State != "ready" {
			a.dimensionMu.Unlock()
			return
		}
		job.status.State, job.status.Message = "expired", "Import expired; upload the archive again"
		a.dimensionMu.Unlock()
		os.RemoveAll(job.stage)
	})
	a.dimensionMu.Unlock()
}

// Manifest modules identify packs regardless of folder names. The world template
// manifest is deliberately excluded. Preserve the source stack's subpack/order.
func discoverDimensionPacks(source, data, world string) ([]dimensionPack, map[string][]packRef, error) {
	candidates := map[string]dimensionPack{}
	err := filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == "db" {
			return filepath.SkipDir
		}
		if entry.IsDir() || entry.Name() != "manifest.json" || filepath.Dir(path) == source {
			return nil
		}
		manifest, err := readPackManifest(path)
		if err != nil {
			return fmt.Errorf("Pack manifest %s: %w", path, err)
		}
		kind := packKind(manifest)
		if kind == "" {
			return nil
		}
		id := strings.ToLower(manifest.Header.UUID)
		if _, ok := candidates[id]; ok {
			return errors.New("Archive contains duplicate pack UUIDs")
		}
		target := filepath.Join(data, packFolder(kind), "dimension-"+id)
		candidates[id] = dimensionPack{Name: resolvedPackName(filepath.Dir(path), manifest.Header.Name), Kind: kind, UUID: manifest.Header.UUID, source: filepath.Dir(path), target: target, ref: packRef{PackID: manifest.Header.UUID, Version: manifest.Header.Version}}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	var packs []dimensionPack
	refs := map[string][]packRef{}
	for _, kind := range []string{"behavior", "resource"} {
		incoming, err := readPackRefs(filepath.Join(source, packFile(kind)))
		if err != nil {
			return nil, nil, err
		}
		// Worlds without stack files can still contain a single unambiguous BP/RP.
		if len(incoming) == 0 {
			for _, p := range candidates {
				if p.Kind == kind {
					incoming = append(incoming, p.ref)
				}
			}
			if len(incoming) != 1 {
				return nil, nil, fmt.Errorf("Expected one %s pack or an explicit world pack stack", kind)
			}
		}
		existing, err := readPackRefs(filepath.Join(world, packFile(kind)))
		if err != nil {
			return nil, nil, err
		}
		refs[kind] = append([]packRef{}, existing...)
		installed, err := os.ReadDir(filepath.Join(data, packFolder(kind)))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, err
		}
		used := map[string]bool{}
		for _, ref := range incoming {
			id := strings.ToLower(ref.PackID)
			p, ok := candidates[id]
			if !ok || p.Kind != kind || !samePackVersion(p.ref.Version, ref.Version) {
				return nil, nil, fmt.Errorf("Archive is missing the referenced %s pack %s/version", kind, ref.PackID)
			}
			if used[id] {
				return nil, nil, errors.New("Duplicate pack reference")
			}
			used[id] = true
			for _, previous := range existing {
				if strings.EqualFold(previous.PackID, p.UUID) {
					return nil, nil, fmt.Errorf("Pack %s is already referenced by the destination", p.Name)
				}
			}
			for _, entry := range installed {
				m, e := readPackManifest(filepath.Join(data, packFolder(kind), entry.Name(), "manifest.json"))
				if e == nil && strings.EqualFold(m.Header.UUID, p.UUID) {
					return nil, nil, fmt.Errorf("Pack %s is already installed", p.Name)
				}
			}
			if _, err := os.Lstat(p.target); !errors.Is(err, os.ErrNotExist) {
				return nil, nil, fmt.Errorf("Pack destination already exists: %s", p.Name)
			}
			m, _ := readPackManifest(filepath.Join(p.source, "manifest.json"))
			if ref.Subpack != "" {
				valid := false
				for _, sub := range m.Subpacks {
					if sub.FolderName == ref.Subpack {
						valid = true
					}
				}
				if !valid {
					return nil, nil, fmt.Errorf("Unknown subpack %s", ref.Subpack)
				}
			}
			p.ref = ref
			packs = append(packs, p)
			refs[kind] = append(refs[kind], ref)
		}
	}
	// Do not guess which unrelated packs to install.
	if len(packs) != len(candidates) {
		return nil, nil, errors.New("Archive contains inactive or unrelated packs; export a world with only the required dimension packs")
	}
	for _, pack := range packs {
		manifest, err := readPackManifest(filepath.Join(pack.source, "manifest.json"))
		if err != nil {
			return nil, nil, err
		}
		for _, dependency := range manifest.Dependencies {
			if dependency.UUID == "" {
				continue
			} // Script API modules are supplied by Bedrock.
			dependencyPack, found := candidates[strings.ToLower(dependency.UUID)]
			if !found || !samePackVersion(dependencyPack.ref.Version, dependency.Version) {
				return nil, nil, fmt.Errorf("Pack %s has an unresolved pack dependency: %s", pack.Name, dependency.UUID)
			}
		}
	}
	return packs, refs, nil
}

type dimensionWorkerResult struct {
	Type       string             `json:"type"`
	Message    string             `json:"message"`
	Dimensions []dimensionSummary `json:"dimensions"`
	Completed  int                `json:"completed"`
	Total      int                `json:"total"`
}

func (a *API) runDimensionWorker(job *dimensionJob, mode, dimension string, extra ...string) (dimensionWorkerResult, error) {
	python := os.Getenv("MCUI_DIMENSION_PYTHON")
	if python == "" {
		python = "python3"
	}
	script := os.Getenv("MCUI_DIMENSION_SCRIPT")
	if script == "" {
		script = "scripts/import_dimension.py"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	args := append([]string{"-u", script, mode, "--source", job.source, "--target", job.world, "--dimension", dimension}, extra...)
	cmd := exec.CommandContext(ctx, python, args...)
	// Libraries may log during import; the worker redirects these to stderr.
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return dimensionWorkerResult{}, err
	}
	logFile, err := os.Create(filepath.Join(job.stage, "worker.log"))
	if err != nil {
		return dimensionWorkerResult{}, err
	}
	defer logFile.Close()
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		return dimensionWorkerResult{}, fmt.Errorf("Cannot start the Amulet worker: %w", err)
	}
	var result dimensionWorkerResult
	var workerError string
	scan := bufio.NewScanner(stdout)
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		var event dimensionWorkerResult
		if json.Unmarshal(scan.Bytes(), &event) != nil {
			continue
		}
		switch event.Type {
		case "result":
			result = event
		case "error":
			workerError = event.Message
		case "progress":
			a.dimensionMu.Lock()
			job.status.Message = event.Message
			job.status.Completed = event.Completed
			job.status.Total = event.Total
			a.dimensionMu.Unlock()
		}
	}
	scanErr := scan.Err()
	if scanErr != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if workerError != "" {
		return result, errors.New(workerError)
	}
	if waitErr != nil || scanErr != nil || result.Type != "result" {
		return result, errors.New("Amulet worker failed or timed out; verify the Python dependencies and archive format")
	}
	return result, nil
}
func (a *API) applyDimension(name string, job *dimensionJob, dimension string) {
	defer os.RemoveAll(job.stage)
	fail := func(err error) { a.dimensionProgress(job, "failed", err.Error()) }
	if a.status(context.Background(), name) != "stopped" {
		fail(errors.New("Server must remain stopped during import"))
		return
	}
	data, world, err := a.packPaths(name)
	if err != nil || data != job.data || world != job.world {
		fail(errors.New("Server world location changed; upload the archive again"))
		return
	}
	// Re-read destination references so an external edit made since review is
	// never silently replaced with the older snapshot.
	_, refs, err := discoverDimensionPacks(job.source, job.data, job.world)
	if err != nil {
		fail(err)
		return
	}
	job.refs = refs
	// Validate everything before installing packs or writing database records.
	if _, err := a.runDimensionWorker(job, "check", dimension); err != nil {
		fail(err)
		return
	}
	// Prepare stack files before database mutation.
	for kind, refs := range job.refs {
		if err := writePackRefs(filepath.Join(job.stage, packFile(kind)), refs); err != nil {
			fail(err)
			return
		}
	}
	record, err := a.recordDimensionImport(name, job, dimension)
	if err != nil {
		fail(err)
		return
	}
	installed := []string{}
	cleanupPacks := func() {
		for _, path := range installed {
			_ = os.RemoveAll(path)
		}
	}
	for _, pack := range job.status.Packs {
		if err := os.MkdirAll(filepath.Dir(pack.target), 0755); err != nil {
			cleanupPacks()
			fail(err)
			return
		}
		// Reserve a new directory; never remove or overwrite a pre-existing pack.
		if err := os.Mkdir(pack.target, 0755); err != nil {
			cleanupPacks()
			fail(err)
			return
		}
		installed = append(installed, pack.target)
		if err := copyTree(pack.source, pack.target); err != nil {
			cleanupPacks()
			fail(err)
			return
		}
	}
	if _, err := a.runDimensionWorker(job, "apply", dimension); err != nil {
		fail(fmt.Errorf("%w. Import may be incomplete; keep the server stopped and restore your backup before retrying", err))
		return
	}
	for kind := range job.refs {
		if err := os.Rename(filepath.Join(job.stage, packFile(kind)), filepath.Join(job.world, packFile(kind))); err != nil {
			fail(fmt.Errorf("Dimension copied but pack activation failed: %w. Restore your backup before retrying", err))
			return
		}
	}
	record.State = "installed"
	if err := a.writeDimensionRecord(name, record); err != nil {
		fail(err)
		return
	}
	a.dimensionProgress(job, "complete", "Custom dimension imported and packs activated. Start the server when ready.")
}
