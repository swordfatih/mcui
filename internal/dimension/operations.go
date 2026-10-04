package dimension

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

type Progress func(message string, completed, total int)
type Result struct {
	Name   string `json:"name"`
	ID     int32  `json:"id"`
	Chunks int    `json:"chunks"`
}

var metaName = regexp.MustCompile(`^[a-z0-9_]+:[a-z0-9_]+$`)

func mapValue(t tag, key string) (tag, bool) {
	m, ok := asCompound(t)
	if !ok {
		return tag{}, false
	}
	v, ok := m[key]
	return v, ok
}
func nbtInt(t tag) (int64, bool) {
	switch v := t.value.(type) {
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	}
	return 0, false
}
func walkIDs(t tag, out map[int64]bool) {
	if m, ok := asCompound(t); ok {
		for k, v := range m {
			if k == "UniqueID" {
				if id, ok := nbtInt(v); ok {
					out[id] = true
				}
			} else {
				walkIDs(v, out)
			}
		}
		return
	}
	if l, ok := t.value.(list); ok {
		for _, v := range l.values {
			walkIDs(v, out)
		}
	}
}
func readMeta(raw []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	if raw == nil {
		return out, nil
	}
	if len(raw) < 4 {
		return nil, errors.New("truncated chunk metadata dictionary")
	}
	n := binary.LittleEndian.Uint32(raw)
	off := 4
	if n > 1<<20 {
		return nil, errors.New("invalid chunk metadata dictionary")
	}
	for i := uint32(0); i < n; i++ {
		if off+8 > len(raw) {
			return nil, errors.New("truncated chunk metadata hash")
		}
		key := string(raw[off : off+8])
		off += 8
		if _, exists := out[key]; exists {
			return nil, errors.New("duplicate chunk metadata hash")
		}
		_, used, e := readNBTPrefix(raw[off:])
		if e != nil {
			return nil, e
		}
		out[key] = bytes.Clone(raw[off : off+used])
		off += used
	}
	if off != len(raw) {
		return nil, errors.New("unsupported chunk metadata dictionary layout")
	}
	return out, nil
}
func writeMeta(m map[string][]byte) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(keys)))
	for _, k := range keys {
		b.WriteString(k)
		b.Write(m[k])
	}
	return b.Bytes()
}

// RunImport first validates every record, then applies it in bounded batches.
// check=false performs the same validation before writing anything.
func RunImport(sourcePath, targetPath, name string, apply bool, progress Progress) (Result, error) {
	if !metaName.MatchString(name) || strings.HasPrefix(name, "minecraft:") {
		return Result{}, errors.New("invalid custom dimension name")
	}
	s, e := Open(sourcePath)
	if e != nil {
		return Result{}, e
	}
	defer s.Close()
	d, e := Open(targetPath)
	if e != nil {
		return Result{}, e
	}
	defer d.Close()
	sroot, sentries, e := table(s)
	if e != nil {
		return Result{}, e
	}
	_ = sroot
	idTag, ok := sentries[name]
	if !ok {
		return Result{}, errors.New("custom dimension not found in archive")
	}
	dim, _ := intValue(idTag)
	_, dentries, e := table(d)
	if e != nil {
		return Result{}, e
	}
	if _, ok = dentries[name]; ok {
		return Result{}, errors.New("a dimension with this name already exists")
	}
	for _, v := range dentries {
		n, _ := intValue(v)
		if n == dim {
			return Result{}, errors.New("numeric dimension ID is already used")
		}
	}
	if dim < 1000 {
		return Result{}, errors.New("invalid custom dimension ID")
	}
	e = d.Records(func(k, _ []byte) error {
		if id, ok := dimForKey(k); ok && id == dim {
			return errors.New("destination already has records for this dimension ID")
		}
		if id, ok := digestID(k); ok && id == dim {
			return errors.New("destination already has records for this dimension ID")
		}
		return nil
	})
	if e != nil {
		return Result{}, e
	}
	hashes := map[string]bool{}
	biomes := map[uint32]bool{}
	actors := map[string]bool{}
	importedIDs := map[int64]bool{}
	digestChunks := map[string]bool{}
	chunks := map[string]bool{}
	count := 0
	checkErr := s.Records(func(k, v []byte) error {
		kid, chunk := dimForKey(k)
		did, digest := digestID(k)
		if (len(k) == 13 || len(k) == 14 || len(k) == 15 || len(k) == 21) && int32(binary.LittleEndian.Uint32(k[8:12])) == dim && !chunk {
			return errors.New("unsupported record layout in selected dimension")
		}
		if chunk && kid == dim {
			if old, err := d.Get(k); err != nil {
				return err
			} else if old != nil {
				return errors.New("destination record collision; nothing was imported")
			}
			chunks[chunkCoords(k)] = true
			count++
			switch k[12] {
			case 63:
				if len(v) != 8 {
					return errors.New("invalid chunk metadata reference")
				}
				hashes[string(v)] = true
			case 43:
				ids, err := data3DBiomes(v)
				if err != nil {
					return err
				}
				for n := range ids {
					biomes[n] = true
				}
			case 50:
				if len(v) > 0 {
					return errors.New("legacy entity storage is unsupported; save source in current Bedrock first")
				}
			}
		}
		if digest && did == dim {
			if len(v)%8 != 0 {
				return errors.New("invalid entity digest")
			}
			digestChunks[string(k[4:12])] = true
			for i := 0; i < len(v); i += 8 {
				actors[string(v[i:i+8])] = true
			}
			if old, err := d.Get(k); err != nil {
				return err
			} else if old != nil {
				return errors.New("destination entity digest collision")
			}
		}
		return nil
	})
	if checkErr != nil {
		return Result{}, checkErr
	}
	if count == 0 {
		return Result{}, errors.New("selected custom dimension contains no saved chunks")
	}
	for key := range digestChunks {
		if !chunks[key] {
			return Result{}, errors.New("entity digest without matching chunk")
		}
	}
	for actor := range actors {
		key := append([]byte("actorprefix"), []byte(actor)...)
		v, err := s.Get(key)
		if err != nil {
			return Result{}, err
		}
		if v == nil {
			return Result{}, errors.New("custom dimension references a missing entity")
		}
		a, err := readNBT(v)
		if err != nil {
			return Result{}, err
		}
		if x, ok := mapValue(a, "DimensionId"); ok {
			n, _ := nbtInt(x)
			if n != int64(dim) {
				return Result{}, errors.New("entity digest references another dimension")
			}
		}
		walkIDs(a, importedIDs)
		if old, err := d.Get(key); err != nil {
			return Result{}, err
		} else if old != nil {
			return Result{}, errors.New("destination entity collision")
		}
	}
	perDimension, e := s.Get([]byte(name))
	if e != nil {
		return Result{}, e
	}
	if perDimension != nil {
		if old, err := d.Get([]byte(name)); err != nil {
			return Result{}, err
		} else if old != nil {
			return Result{}, errors.New("destination per-dimension record collision")
		}
		value, err := readNBT(perDimension)
		if err != nil {
			return Result{}, errors.New("invalid per-dimension entity record")
		}
		walkIDs(value, importedIDs)
	}
	if len(importedIDs) > 0 {
		checkKeys := map[string]bool{"Overworld": true, "Nether": true, "TheEnd": true, "AutonomousEntities": true}
		for n := range dentries {
			checkKeys[n] = true
		}
		checkErr = d.Records(func(k, v []byte) error {
			if !bytes.HasPrefix(k, []byte("actorprefix")) && !checkKeys[string(k)] {
				return nil
			}
			value, err := readNBT(v)
			if err != nil {
				return nil
			}
			ids := map[int64]bool{}
			walkIDs(value, ids)
			for id := range ids {
				if importedIDs[id] {
					return errors.New("entity IDs conflict with destination; automatic remapping is unsupported")
				}
			}
			return nil
		})
		if checkErr != nil {
			return Result{}, checkErr
		}
	}
	meta := map[string][]byte{}
	var metadataBytes []byte
	if len(hashes) > 0 {
		meta, e = readMeta(func() []byte { v, _ := d.Get(metadataKey); return v }())
		if e != nil {
			return Result{}, e
		}
		sourceMeta, e := readMeta(func() []byte { v, _ := s.Get(metadataKey); return v }())
		if e != nil {
			return Result{}, e
		}
		for h := range hashes {
			v, ok := sourceMeta[h]
			if !ok {
				return Result{}, errors.New("missing chunk metadata")
			}
			if old, ok := meta[h]; ok && !bytes.Equal(old, v) {
				return Result{}, errors.New("chunk metadata hash collision")
			}
			meta[h] = v
		}
		metadataBytes = writeMeta(meta)
	}
	biomeRoot := tag{}
	biomeList := list{kind: 10}
	if len(biomes) > 0 {
		raw, _ := s.Get(biomeKey)
		incoming, err := readNBT(raw)
		if err != nil {
			return Result{}, errors.New("missing custom biome ID table")
		}
		// Biome tables are compounds containing a list. Find and merge referenced IDs.
		im, _ := asCompound(incoming)
		iv, ok := im["list"]
		if !ok {
			return Result{}, errors.New("unsupported biome ID table")
		}
		il, ok := iv.value.(list)
		if !ok {
			return Result{}, errors.New("unsupported biome ID table")
		}
		raw, _ = d.Get(biomeKey)
		if raw == nil {
			biomeRoot = tag{10, compound{"list": tag{9, biomeList}}}
		} else {
			biomeRoot, err = readNBT(raw)
			if err != nil {
				return Result{}, err
			}
		}
		bm, _ := asCompound(biomeRoot)
		bv := bm["list"]
		biomeList, ok = bv.value.(list)
		if !ok {
			return Result{}, errors.New("unsupported destination biome table")
		}
		for id := range biomes {
			if id < 30000 {
				continue
			}
			var found tag
			matches := 0
			for _, it := range il.values {
				m, _ := asCompound(it)
				ni, _ := nbtInt(m["id"])
				if uint32(ni) == id {
					found = it
					matches++
				}
			}
			if matches != 1 {
				return Result{}, errors.New("custom biome mapping missing or ambiguous")
			}
			fm, _ := asCompound(found)
			nameTag := fm["name"]
			conflict := false
			for _, it := range biomeList.values {
				m, _ := asCompound(it)
				oldID, _ := nbtInt(m["id"])
				oldName, _ := m["name"].value.(string)
				newID, _ := nbtInt(fm["id"])
				newName, _ := nameTag.value.(string)
				if oldID == newID || oldName == newName {
					if oldID != newID || oldName != newName || !reflect.DeepEqual(it, found) {
						return Result{}, errors.New("custom biome ID conflict")
					}
					conflict = true
				}
			}
			if !conflict {
				biomeList.values = append(biomeList.values, found)
			}
		}
		bm["list"] = tag{9, biomeList}
	}
	// Validate registry, biome and metadata changes before copying payload records.
	droot, entries, _ := table(d)
	dm, _ := asCompound(droot)
	entries[name] = idTag
	dm["entries"] = tag{10, entries}
	dimBytes, e := writeNBT(tag{10, dm})
	if e != nil {
		return Result{}, e
	}
	var biomeBytes []byte
	if len(biomes) > 0 {
		biomeBytes, e = writeNBT(biomeRoot)
		if e != nil {
			return Result{}, e
		}
	}
	if !apply {
		return Result{name, dim, len(chunks)}, nil
	}
	batch := make([]record, 0, 512)
	bytesIn := 0
	completed := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := d.Batch(batch); err != nil {
			return err
		}
		completed += len(batch)
		batch = batch[:0]
		bytesIn = 0
		if progress != nil {
			progress("Copying custom dimension", completed, count+len(actors))
		}
		return nil
	}
	e = s.Records(func(k, v []byte) error {
		kid, c := dimForKey(k)
		did, g := digestID(k)
		if (c && kid == dim) || (g && did == dim) {
			batch = append(batch, record{bytes.Clone(k), bytes.Clone(v)})
			bytesIn += len(k) + len(v)
			if bytesIn >= 4<<20 {
				return flush()
			}
		}
		return nil
	})
	if e != nil {
		return Result{}, e
	}
	for actor := range actors {
		key := append([]byte("actorprefix"), []byte(actor)...)
		v, _ := s.Get(key)
		batch = append(batch, record{key, v})
		if len(batch) >= 512 {
			if e = flush(); e != nil {
				return Result{}, e
			}
		}
	}
	if perDimension != nil {
		batch = append(batch, record{[]byte(name), perDimension})
	}
	if e = flush(); e != nil {
		return Result{}, e
	}
	for _, kv := range []record{{metadataKey, metadataBytes}, {biomeKey, biomeBytes}, {dimensionsKey, dimBytes}} {
		if kv.value == nil {
			continue
		}
		if e = d.Put(kv.key, kv.value); e != nil {
			return Result{}, e
		}
	}
	return Result{name, dim, len(chunks)}, nil
}

// Remove deletes the records belonging to one registered custom dimension.
// Player and spawn references are checked before the first write.
func Remove(path, name string, wantID int32, allowMissing, apply bool, progress Progress) (Result, error) {
	if !metaName.MatchString(name) || strings.HasPrefix(name, "minecraft:") || wantID < 1000 {
		return Result{}, errors.New("only a tracked custom dimension can be removed")
	}
	w, e := Open(path)
	if e != nil {
		return Result{}, e
	}
	defer w.Close()
	root, entries, e := table(w)
	if e != nil {
		return Result{}, e
	}
	actual, registered := entries[name]
	if registered {
		n, _ := intValue(actual)
		if n != wantID {
			return Result{}, errors.New("dimension ID changed; refusing removal")
		}
	} else if !allowMissing {
		return Result{}, errors.New("tracked dimension is missing from this world")
	}
	for other, v := range entries {
		if other != name {
			n, _ := intValue(v)
			if n == wantID {
				return Result{}, errors.New("dimension ID is registered to another dimension")
			}
		}
	}
	deleteKeys := map[string][]byte{}
	removedHashes, otherHashes, actors, otherActors := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	chunks := map[string]bool{}
	e = w.Records(func(k, v []byte) error {
		id, isChunk := dimForKey(k)
		if isChunk && id == wantID {
			deleteKeys[string(k)] = bytes.Clone(k)
			chunks[chunkCoords(k)] = true
		} else if len(k) == 13 || len(k) == 14 || len(k) == 15 || len(k) == 21 {
			if int32(binary.LittleEndian.Uint32(k[8:12])) == wantID && !isChunk {
				return errors.New("unsupported chunk record; nothing was removed")
			}
		}
		if (len(k) == 13 && k[12] == 63) || (len(k) == 9 && k[8] == 63) {
			if isChunk && id == wantID {
				removedHashes[string(v)] = true
			} else {
				otherHashes[string(v)] = true
			}
		}
		if d, ok := digestID(k); ok {
			if len(v)%8 != 0 {
				return errors.New("invalid entity digest; nothing was removed")
			}
			for i := 0; i < len(v); i += 8 {
				if d == wantID {
					actors[string(v[i:i+8])] = true
				} else {
					otherActors[string(v[i:i+8])] = true
				}
			}
			if d == wantID {
				deleteKeys[string(k)] = bytes.Clone(k)
			}
		}
		if bytes.HasPrefix(k, []byte("player_")) || bytes.HasPrefix(k, []byte("legacy_console_player_")) || bytes.HasPrefix(k, []byte("~local_player")) {
			p, err := readNBT(v)
			if err == nil {
				for _, field := range []string{"DimensionId", "SpawnDimension"} {
					if t, ok := mapValue(p, field); ok {
						n, _ := nbtInt(t)
						if n == int64(wantID) {
							return errors.New("a player is saved in this dimension or has a spawn point there; move them and their spawn, stop the server, and retry")
						}
					}
				}
			}
		}
		if bytes.HasPrefix(k, []byte("tickingarea")) {
			p, err := readNBT(v)
			if err == nil {
				if t, ok := mapValue(p, "Dimension"); ok {
					n, _ := nbtInt(t)
					if n == int64(wantID) {
						deleteKeys[string(k)] = bytes.Clone(k)
					}
				}
			}
		}
		for _, prefix := range [][]byte{[]byte(fmt.Sprintf("VILLAGE_%d_", wantID)), []byte(fmt.Sprintf("chunk_loaded_request_%d_", wantID)), []byte(fmt.Sprintf("poi.%d.regions.", wantID))} {
			if bytes.HasPrefix(k, prefix) {
				deleteKeys[string(k)] = bytes.Clone(k)
			}
		}
		if bytes.Equal(k, []byte(fmt.Sprintf("poi.%d.regions", wantID))) {
			deleteKeys[string(k)] = bytes.Clone(k)
		}
		return nil
	})
	if e != nil {
		return Result{}, e
	}
	for actor := range actors {
		if otherActors[actor] {
			return Result{}, errors.New("entities are shared with another dimension; nothing was removed")
		}
	}
	e = w.Records(func(k, v []byte) error {
		if !bytes.HasPrefix(k, []byte("actorprefix")) {
			return nil
		}
		p, err := readNBT(v)
		if err != nil {
			return err
		}
		dimTag, has := mapValue(p, "DimensionId")
		dim := int64(-1)
		if has {
			dim, _ = nbtInt(dimTag)
		}
		actor := string(k[len("actorprefix"):])
		if dim == int64(wantID) || actors[actor] {
			if otherActors[actor] || (has && dim != int64(wantID)) {
				return errors.New("entity dimension references conflict; nothing was removed")
			}
			deleteKeys[string(k)] = bytes.Clone(k)
		}
		return nil
	})
	if e != nil {
		return Result{}, e
	}
	metaRaw, e := w.Get(metadataKey)
	if e != nil {
		return Result{}, e
	}
	var metadataUpdate []byte
	if metaRaw != nil {
		m, err := readMeta(metaRaw)
		if err != nil {
			return Result{}, err
		}
		for h := range removedHashes {
			if !otherHashes[h] {
				delete(m, h)
			}
		}
		if len(m) != lenMustReadMeta(metaRaw) {
			metadataUpdate = writeMeta(m)
		}
	}
	if registered {
		delete(entries, name)
		cm, _ := asCompound(root)
		cm["entries"] = tag{10, entries}
	}
	if !apply {
		return Result{name, wantID, len(chunks)}, nil
	}
	if metadataUpdate != nil {
		if e = w.Put(metadataKey, metadataUpdate); e != nil {
			return Result{}, e
		}
	}
	keys := make([][]byte, 0, len(deleteKeys))
	for _, k := range deleteKeys {
		keys = append(keys, k)
	}
	batch := make([][]byte, 0, 512)
	done := 0
	for _, k := range keys {
		batch = append(batch, k)
		if len(batch) == 512 {
			if e = w.DeleteBatch(batch); e != nil {
				return Result{}, e
			}
			done += len(batch)
			batch = batch[:0]
			if progress != nil {
				progress("Removing custom dimension", done, len(keys))
			}
		}
	}
	if len(batch) > 0 {
		if e = w.DeleteBatch(batch); e != nil {
			return Result{}, e
		}
	}
	if e = w.Delete([]byte(name)); e != nil {
		return Result{}, e
	}
	if registered {
		encoded, err := writeNBT(root)
		if err != nil {
			return Result{}, err
		}
		if e = w.Put(dimensionsKey, encoded); e != nil {
			return Result{}, e
		}
	}
	return Result{name, wantID, len(chunks)}, nil
}
func lenMustReadMeta(raw []byte) int {
	m, e := readMeta(raw)
	if e != nil {
		return -1
	}
	return len(m)
}
