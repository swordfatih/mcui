package app

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const brarchiveMagic uint64 = 0x267052a0b125277d

// normalizeOptimizedPack operates on an import's staging directory only. Bedrock
// reads optimized catalogs ahead of loose JSON; expand them before any asset
// indexing or edits so that both MCUI and the game use the same files.
func normalizeOptimizedPack(root string, materialized ...map[string]bool) error {
	manifestPath := filepath.Join(root, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest map[string]json.RawMessage
	if err := json.Unmarshal(stripJSONComments(data), &manifest); err != nil {
		return err
	}
	var header map[string]json.RawMessage
	if err := json.Unmarshal(manifest["header"], &header); err != nil {
		return err
	}
	if _, optimized := header["pack_optimization_version"]; !optimized {
		return nil
	}
	files, err := contentFiles(root)
	if err != nil {
		return err
	}
	archives := []string{}
	written := map[string]bool{}
	listed := map[string]bool{}
	if len(materialized) > 0 {
		written = materialized[0]
	}
	if len(materialized) > 1 {
		listed = materialized[1]
	}
	var expanded int64
	for _, rel := range files {
		prefix, archive, ok := strings.Cut(rel, "__brarchive/")
		if !ok || !strings.HasSuffix(archive, ".brarchive") {
			continue
		}
		if prefix != "" && !strings.HasSuffix(prefix, "/") {
			continue
		}
		parent := prefix + strings.TrimSuffix(archive, ".brarchive")
		if !assetRelative(parent) {
			return errors.New("Invalid optimized archive path")
		}
		if err := expandBRArchive(root, rel, parent, &expanded, written, listed); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		archives = append(archives, rel)
	}
	if len(archives) == 0 {
		return errors.New("Pack declares optimization but contains no supported .brarchive files")
	}
	for _, rel := range archives {
		if err := os.Remove(filepath.Join(root, rel)); err != nil {
			return err
		}
	}
	delete(header, "pack_optimization_version")
	manifest["header"], err = json.Marshal(header)
	if err != nil {
		return err
	}
	data, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	return writeAssetJSON(manifestPath, append(data, '\n'))
}

func expandBRArchive(root, archive, parent string, expanded *int64, written, listed map[string]bool) error {
	f, err := os.Open(filepath.Join(root, archive))
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	var header [16]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return err
	}
	if binary.LittleEndian.Uint64(header[:8]) != brarchiveMagic || binary.LittleEndian.Uint32(header[12:]) != 1 {
		return errors.New("Unsupported optimized archive format")
	}
	count := int64(binary.LittleEndian.Uint32(header[8:12]))
	content := 16 + count*256
	if count > 100000 || content > info.Size() {
		return errors.New("Invalid optimized archive index")
	}
	seen := map[string]bool{}
	for i := int64(0); i < count; i++ {
		var entry [256]byte
		if _, err := f.ReadAt(entry[:], 16+i*256); err != nil {
			return err
		}
		n := int(entry[0])
		if n == 0 || n > 247 {
			return errors.New("Invalid optimized filename")
		}
		name := string(entry[1 : 1+n])
		if !utf8.ValidString(name) || strings.ContainsRune(name, 0) || !assetRelative(name) || filepath.Base(name) != name || seen[name] {
			return errors.New("Unsafe or duplicate optimized filename")
		}
		seen[name] = true
		offset := int64(binary.LittleEndian.Uint32(entry[248:252]))
		size := int64(binary.LittleEndian.Uint32(entry[252:]))
		if offset > info.Size()-content || size > info.Size()-content-offset {
			return errors.New("Optimized entry exceeds archive bounds")
		}
		listed[parent+"/"+name] = true
		// Empty entries list external files. Never replace their bytes with an
		// empty file, including files intentionally disabled in this pack.
		if size == 0 {
			continue
		}
		if size > 256<<20 || *expanded > maxArchiveBytes-size {
			return errors.New("Expanded pack exceeds size limit")
		}
		*expanded += size
		path, err := assetPath(root, parent+"/"+name)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		payload, err := io.ReadAll(io.NewSectionReader(f, content+offset, size))
		if err != nil {
			return err
		}
		if strings.HasSuffix(strings.ToLower(name), ".json") && !json.Valid(stripJSONComments(payload)) {
			return fmt.Errorf("%s contains compiled JSON that MCUI cannot safely edit", name)
		}
		if err := os.WriteFile(path, payload, 0644); err != nil {
			return err
		}
		written[parent+"/"+name] = true
	}
	return nil
}
