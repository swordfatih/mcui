package dimension

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/midnightfreddie/goleveldb/leveldb"
	"github.com/midnightfreddie/goleveldb/leveldb/opt"
)

var dimensionsKey = []byte("DimensionNameIdTable")
var metadataKey = []byte("LevelChunkMetaDataDictionary")
var biomeKey = []byte("BiomeIdsTable")
var customName = regexp.MustCompile(`^[a-z0-9_]+:[a-z0-9_]+$`)
var chunkTags = map[byte]bool{43: true, 44: true, 45: true, 46: true, 47: true, 48: true, 49: true, 50: true, 51: true, 52: true, 53: true, 54: true, 55: true, 56: true, 57: true, 58: true, 59: true, 60: true, 61: true, 62: true, 63: true, 64: true, 65: true, 110: true, 111: true, 112: true, 113: true, 118: true, 119: true, 120: true}

type record struct{ key, value []byte }
type World struct {
	path string
	db   *leveldb.DB
}

func Open(path string) (*World, error) {
	levelPath := filepath.Join(path, "level.dat")
	raw, err := os.ReadFile(levelPath)
	if err != nil {
		return nil, fmt.Errorf("read level.dat: %w", err)
	}
	if len(raw) < 8 {
		return nil, errors.New("invalid Bedrock level.dat")
	}
	n := binary.LittleEndian.Uint32(raw[4:8])
	if int(n) != len(raw)-8 {
		return nil, errors.New("invalid Bedrock level.dat length")
	}
	if _, err = readNBT(raw[8:]); err != nil {
		return nil, fmt.Errorf("invalid Bedrock level.dat NBT: %w", err)
	}
	if info, statErr := os.Stat(filepath.Join(path, "db", "CURRENT")); statErr != nil || !info.Mode().IsRegular() {
		return nil, errors.New("expected a complete unencrypted Bedrock world with db/CURRENT")
	}
	db, err := leveldb.OpenFile(filepath.Join(path, "db"), &opt.Options{Compression: opt.NoCompression})
	if err != nil {
		return nil, fmt.Errorf("open Bedrock LevelDB: %w", err)
	}
	return &World{path: path, db: db}, nil
}
func (w *World) Close() error { return w.db.Close() }
func (w *World) Get(k []byte) ([]byte, error) {
	v, e := w.db.Get(k, nil)
	if e == leveldb.ErrNotFound {
		return nil, nil
	}
	return v, e
}
func (w *World) Put(k, v []byte) error { return w.db.Put(k, v, nil) }
func (w *World) Delete(k []byte) error { return w.db.Delete(k, nil) }
func (w *World) Records(fn func([]byte, []byte) error) error {
	it := w.db.NewIterator(nil, nil)
	defer it.Release()
	for it.Next() {
		k := bytes.Clone(it.Key())
		v := bytes.Clone(it.Value())
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return it.Error()
}
func (w *World) Batch(records []record) error {
	b := new(leveldb.Batch)
	for _, r := range records {
		b.Put(r.key, r.value)
	}
	return w.db.Write(b, nil)
}
func (w *World) DeleteBatch(keys [][]byte) error {
	b := new(leveldb.Batch)
	for _, k := range keys {
		b.Delete(k)
	}
	return w.db.Write(b, nil)
}

func table(w *World) (tag, compound, error) {
	raw, err := w.Get(dimensionsKey)
	if err != nil {
		return tag{}, nil, err
	}
	var root tag
	if raw == nil {
		root = tag{10, compound{"entries": tag{10, compound{}}}}
	} else {
		root, err = readNBT(raw)
		if err != nil {
			return tag{}, nil, err
		}
	}
	m, ok := asCompound(root)
	if !ok {
		return tag{}, nil, errors.New("unsupported dimension registry")
	}
	entries, ok := asCompound(m["entries"])
	if !ok {
		return tag{}, nil, errors.New("unsupported dimension registry")
	}
	seen := map[int32]bool{}
	for name, idTag := range entries {
		id, ok := intValue(idTag)
		if !customName.MatchString(name) || strings.HasPrefix(name, "minecraft:") || !ok || id < 1000 || seen[id] {
			return tag{}, nil, errors.New("unsupported or duplicate custom dimension registration")
		}
		seen[id] = true
	}
	return root, entries, nil
}
func dimForKey(k []byte) (int32, bool) {
	if len(k) < 13 {
		return 0, false
	}
	t := k[12]
	valid := false
	switch len(k) {
	case 13:
		valid = chunkTags[t] && t != 47 && t != 110 && t != 111 && t != 120
	case 14:
		valid = t == 47 || t == 110
	case 15:
		valid = t == 110 || t == 111
	case 21:
		valid = t == 120
	}
	if !valid {
		return 0, false
	}
	return int32(binary.LittleEndian.Uint32(k[8:12])), true
}
func digestID(k []byte) (int32, bool) {
	if len(k) != 16 || !bytes.HasPrefix(k, []byte("digp")) {
		return 0, false
	}
	return int32(binary.LittleEndian.Uint32(k[12:])), true
}
func chunkCoords(k []byte) string { return string(k[:8]) }

func Inspect(path string) ([]map[string]any, error) {
	w, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer w.Close()
	_, entries, err := table(w)
	if err != nil {
		return nil, err
	}
	counts := map[int32]map[string]bool{}
	names := map[string]int32{}
	for n, t := range entries {
		id, _ := intValue(t)
		counts[id] = map[string]bool{}
		names[n] = id
	}
	err = w.Records(func(k, _ []byte) error {
		if id, ok := dimForKey(k); ok {
			if chunks, exists := counts[id]; exists {
				chunks[chunkCoords(k)] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(names))
	for name, id := range names {
		out = append(out, map[string]any{"name": name, "id": id, "chunks": len(counts[id])})
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return out, nil
}

func data3DBiomes(raw []byte) (map[uint32]bool, error) {
	if len(raw) < 512 {
		return nil, errors.New("truncated Data3D heightmap")
	}
	offset := 512
	ids := map[uint32]bool{}
	for offset < len(raw) {
		flags := raw[offset]
		offset++
		bits := int(flags >> 1)
		if bits == 127 {
			continue
		}
		if !map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 8: true, 16: true}[bits] || flags&1 == 0 {
			return nil, errors.New("unsupported biome palette encoding")
		}
		size := 1
		if bits > 0 {
			per := 32 / bits
			offset += ((4096 + per - 1) / per) * 4
			if offset+4 > len(raw) {
				return nil, errors.New("truncated biome palette")
			}
			size = int(binary.LittleEndian.Uint32(raw[offset:]))
			offset += 4
		}
		if size < 1 || size > 65536 || offset+size*4 > len(raw) {
			return nil, errors.New("invalid biome palette size")
		}
		for i := 0; i < size; i++ {
			ids[binary.LittleEndian.Uint32(raw[offset+i*4:])] = true
		}
		offset += size * 4
	}
	return ids, nil
}
