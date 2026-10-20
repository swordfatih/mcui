package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOptimizedRepairInstalledSnapshot(t *testing.T) {
	snapshot := os.Getenv("MCUI_TEST_INSTALLED_SNAPSHOT")
	if snapshot == "" {
		t.Skip("set MCUI_TEST_INSTALLED_SNAPSHOT to a complete installed pack and asset-state snapshot")
	}
	root := t.TempDir()
	if err := extract(snapshot, root); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(root, "bedrock_data/resource_packs/Actions_Stuff")
	statePath := filepath.Join(root, ".mcui/asset-state/resource/Actions_Stuff.json")
	archiveDir := filepath.Join(root, ".mcui/archived-assets/resource/Actions_Stuff")
	state, err := readAssetState(statePath, updateResourceUUID)
	if err != nil {
		t.Fatal(err)
	}
	u, stage, changes, err := prepareOptimizedAssetRepair(root, pack, statePath, archiveDir, state)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	next, err := readAssetState(u.newState, state.UUID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range next.Assets {
		layer, _ := assetLayerPath(u.source, r.Layer)
		if _, err := os.Stat(filepath.Join(layer, r.Path)); !os.IsNotExist(err) {
			t.Fatalf("disabled file was reintroduced: %s", r.Path)
		}
		archived := filepath.Join(u.newArchive, r.Layer, r.Path)
		hash, err := assetHash(archived)
		if err != nil || hash != r.Hash {
			t.Fatalf("lost or changed restore copy: %s %v", r.Path, err)
		}
	}
	files, err := contentFiles(u.source)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range files {
		if strings.HasSuffix(p, ".brarchive") {
			t.Fatalf("stale optimized archive remains: %s", p)
		}
	}
	if len(u.warnings) > 0 {
		t.Fatalf("remaining warnings: %v", u.warnings)
	}
	t.Logf("verified installed repair: %+v; %v", u.summary, changes)
	// Optional review artifact, built only from the real archived bytes verified
	// above. It contains the repaired pack, index, and complete restoration data.
	if output := os.Getenv("MCUI_TEST_REPAIR_ARTIFACT"); output != "" {
		f, err := os.Create(output)
		if err != nil {
			t.Fatal(err)
		}
		gz := gzip.NewWriter(f)
		tw := tar.NewWriter(gz)
		for _, item := range [][2]string{{u.source, "bedrock_data/resource_packs/Actions_Stuff"}, {u.newArchive, ".mcui/archived-assets/resource/Actions_Stuff"}, {u.newState, ".mcui/asset-state/resource/Actions_Stuff.json"}} {
			err := filepath.WalkDir(item[0], func(path string, d os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if d.IsDir() {
					return nil
				}
				rel, err := filepath.Rel(item[0], path)
				if err != nil {
					return err
				}
				name := item[1]
				if rel != "." {
					name += "/" + filepath.ToSlash(rel)
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				h := &tar.Header{Name: name, Mode: 0644, Size: info.Size()}
				if err := tw.WriteHeader(h); err != nil {
					return err
				}
				in, err := os.Open(path)
				if err != nil {
					return err
				}
				_, err = io.Copy(tw, in)
				closeErr := in.Close()
				if err != nil {
					return err
				}
				return closeErr
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		t.Logf("saved verified repair artifact: %s", output)
	}
}

func testBRArchive(entries map[string][]byte) []byte {
	index := make([]byte, 16+len(entries)*256)
	binary.LittleEndian.PutUint64(index, brarchiveMagic)
	binary.LittleEndian.PutUint32(index[8:], uint32(len(entries)))
	binary.LittleEndian.PutUint32(index[12:], 1)
	var content bytes.Buffer
	i := 0
	for name, data := range entries {
		row := index[16+i*256 : 16+(i+1)*256]
		row[0] = byte(len(name))
		copy(row[1:248], name)
		binary.LittleEndian.PutUint32(row[248:], uint32(content.Len()))
		binary.LittleEndian.PutUint32(row[252:], uint32(len(data)))
		content.Write(data)
		i++
	}
	return append(index, content.Bytes()...)
}

func TestOptimizedPackExpandsAuthoritativeCatalogsAndPreservesExternalTextures(t *testing.T) {
	root := t.TempDir()
	putUpdateAsset(t, root, "manifest.json", `{"header":{"pack_optimization_version":"0.1.0","version":[1,0,0]},"modules":[]}`)
	putUpdateAsset(t, root, "textures/terrain_texture.json", `{"texture_data":{}}`)
	putUpdateAsset(t, root, "textures/example.png", "external PNG bytes")
	putUpdateAsset(t, root, "__brarchive/textures.brarchive", string(testBRArchive(map[string][]byte{"example.png": nil, "terrain_texture.json": []byte(`{"texture_data":{"example":{"textures":"textures/example"}}}`)})))
	written := map[string]bool{}
	if err := normalizeOptimizedPack(root, written); err != nil {
		t.Fatal(err)
	}
	if !written["textures/terrain_texture.json"] || written["textures/example.png"] {
		t.Fatal(written)
	}
	data, _ := os.ReadFile(filepath.Join(root, "textures/terrain_texture.json"))
	if !strings.Contains(string(data), "textures/example") {
		t.Fatal("archived catalog was ignored")
	}
	image, _ := os.ReadFile(filepath.Join(root, "textures/example.png"))
	if string(image) != "external PNG bytes" {
		t.Fatal("listing erased texture")
	}
	if optimized, err := isOptimizedPack(root); err != nil || optimized {
		t.Fatalf("still optimized: %v %v", optimized, err)
	}
	if _, err := os.Stat(filepath.Join(root, "__brarchive/textures.brarchive")); !os.IsNotExist(err) {
		t.Fatal("stale archive remains")
	}
}

func TestOptimizedPackRejectsUnsafeOrTruncatedArchives(t *testing.T) {
	for _, kind := range []string{"traversal", "bounds", "version", "compiled JSON"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			putUpdateAsset(t, root, "manifest.json", `{"header":{"pack_optimization_version":"0.1.0"}}`)
			name := "example.json"
			content := []byte(`{}`)
			if kind == "traversal" {
				name = "../escape.json"
			}
			if kind == "compiled JSON" {
				content = []byte{0, 1, 2, 3}
			}
			data := testBRArchive(map[string][]byte{name: content})
			if kind == "bounds" {
				binary.LittleEndian.PutUint32(data[16+252:], 0xffffffff)
			}
			if kind == "version" {
				binary.LittleEndian.PutUint32(data[12:], 99)
			}
			putUpdateAsset(t, root, "__brarchive/textures.brarchive", string(data))
			if err := normalizeOptimizedPack(root); err == nil {
				t.Fatal("invalid archive accepted")
			}
		})
	}
}

func TestOptimizedRepairLocalPack(t *testing.T) {
	archive, snapshot := os.Getenv("MCUI_TEST_UPDATE_ARCHIVE"), os.Getenv("MCUI_TEST_OPTIMIZED_SNAPSHOT")
	if archive == "" || snapshot == "" {
		t.Skip("set MCUI_TEST_UPDATE_ARCHIVE and MCUI_TEST_OPTIMIZED_SNAPSHOT")
	}
	root := t.TempDir()
	pack := filepath.Join(root, "pack")
	if err := os.MkdirAll(pack, 0755); err != nil {
		t.Fatal(err)
	}
	if err := extract(archive, pack); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var state assetState
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		payload, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(h.Name, "asset-state/resource/Actions_Stuff.json") {
			if err := json.Unmarshal(payload, &state); err != nil {
				t.Fatal(err)
			}
		}
		if _, rel, ok := strings.Cut(h.Name, "resource_packs/Actions_Stuff/"); ok {
			putUpdateAsset(t, pack, rel, string(payload))
		}
	}
	if len(state.Assets) == 0 {
		t.Fatal("snapshot has no disabled assets")
	}
	for i := range state.Assets {
		r := &state.Assets[i]
		r.Hash = ""
		putUpdateAsset(t, filepath.Join(root, "archived"), r.Layer+"/"+r.Path, "previous archived bytes")
		layerDir, err := assetLayerPath(pack, r.Layer)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(layerDir, r.Path)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	statePath := filepath.Join(root, "state.json")
	if err := writeAssetState(statePath, state); err != nil {
		t.Fatal(err)
	}
	u, stage, changes, err := prepareOptimizedAssetRepair(root, pack, statePath, filepath.Join(root, "archived"), state)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(stage)
	t.Logf("optimized repair: %+v; changes=%v warnings=%v", u.summary, changes, u.warnings)
	if len(u.warnings) != 0 {
		t.Fatalf("remaining warnings: %v", u.warnings)
	}
	next, err := readAssetState(u.newState, state.UUID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range next.Assets {
		if _, err := os.Stat(filepath.Join(u.newArchive, r.Layer, r.Path)); err != nil {
			t.Fatalf("lost archived asset %s: %v", r.Path, err)
		}
		layerDir, _ := assetLayerPath(u.source, r.Layer)
		if _, err := os.Stat(filepath.Join(layerDir, r.Path)); !os.IsNotExist(err) {
			t.Fatalf("disabled asset was reintroduced: %s", r.Path)
		}
	}
	if optimized, err := isOptimizedPack(pack); err != nil || !optimized {
		t.Fatal("preview mutated live pack")
	}
	world := filepath.Join(root, "world")
	os.MkdirAll(world, 0755)
	for _, kind := range []string{"resource", "behavior"} {
		if err := writePackRefs(filepath.Join(world, packFile(kind)), nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyPackUpdates(world, stage, []packUpdate{u}); err != nil {
		t.Fatal(err)
	}
	if optimized, err := isOptimizedPack(pack); err != nil || optimized {
		t.Fatalf("pack not converted: %v %v", optimized, err)
	}
	refs, err := readPackRefs(filepath.Join(world, packFile("resource")))
	if err != nil || len(refs) != 0 {
		t.Fatal("repair activated a disabled pack")
	}
}
