#!/usr/bin/env python3
"""Append one Bedrock custom dimension using Amulet Core's raw database access.

No world conversion, level.dat save, backup, or existing-dimension writes.
Unknown layouts and numeric ID collisions are rejected before any writes.
Sources:
 https://learn.microsoft.com/minecraft/creator/documents/actorstorage
 https://github.com/Mojang/minecraft-creator-tools
 https://github.com/8Crafter-Studios/mcbe-leveldb (dimension/metadata schemas)
"""
import argparse
from contextlib import contextmanager, redirect_stdout
import copy
import json
import re
import sqlite3
import struct
import sys
import tempfile
from pathlib import Path

# Amulet logs during import. Keep stdout exclusively for the worker protocol.
with redirect_stdout(sys.stderr):
    from amulet.level.formats.leveldb_world.format import BedrockLevelDAT
    from leveldb import LevelDB
    from amulet_nbt import CompoundTag, IntTag, ListTag, NamedTag, ReadContext, load as load_nbt, utf8_escape_decoder, utf8_escape_encoder

DIMENSIONS = b"DimensionNameIdTable"
METADATA = b"LevelChunkMetaDataDictionary"
BIOMES = b"BiomeIdsTable"
CHUNK_TAGS = set(range(43, 66)) | {112, 113, 118, 119, 120, 110, 111}


def emit(kind, **fields):
    print(json.dumps({"type": kind, **fields}), flush=True)


def nbt(raw):
    value = load_nbt(raw, compressed=False, little_endian=True, string_decoder=utf8_escape_decoder)
    if not isinstance(value.tag, CompoundTag):
        raise ValueError("Expected a compound NBT record")
    return value


def serialize(value):
    return value.save_to(compressed=False, little_endian=True, string_encoder=utf8_escape_encoder)


def get(db, key):
    try:
        return db.get(key)
    except KeyError:
        return None


@contextmanager
def open_world(path):
    path = Path(path)
    if not (path / "level.dat").is_file() or not (path / "db" / "CURRENT").is_file():
        raise ValueError("Expected an unencrypted Bedrock world with level.dat and a complete db/")
    # Core's LevelDBFormat.open() allocates an actor session and saves level.dat.
    # Use Core to validate metadata, then its Mojang LevelDB binding directly so
    # even inspection leaves world settings and entity counters untouched.
    BedrockLevelDAT.from_file(str(path / "level.dat"))
    db = LevelDB(str(path / "db"), create_if_missing=False)
    try:
        yield db
    finally:
        db.close()


def dimension_table(db):
    raw = get(db, DIMENSIONS)
    tag = nbt(raw) if raw is not None else NamedTag(CompoundTag({"entries": CompoundTag()}))
    entries = tag.compound.get("entries")
    if not isinstance(entries, CompoundTag):
        raise ValueError("Unsupported DimensionNameIdTable layout")
    ids = set()
    for name, number in entries.items():
        if not re.fullmatch(r"[a-z0-9_]+:[a-z0-9_]+", name) or name.startswith("minecraft:") or not isinstance(number, IntTag) or number.py_int < 1000:
            raise ValueError("Unsupported custom dimension registration")
        if number.py_int in ids:
            raise ValueError("Duplicate numeric dimension ID")
        ids.add(number.py_int)
    return tag


def chunk_dimension(key):
    if len(key) < 13:
        return None
    tag = key[12]
    valid = ((len(key) == 13 and tag in CHUNK_TAGS - {47, 110, 111, 120})
             or (len(key) == 14 and tag in {47, 110})
             or (len(key) == 15 and tag in {110, 111})
             or (len(key) == 21 and tag == 120))
    if valid:
        return struct.unpack_from("<i", key, 8)[0]
    return None


def digest_dimension(key):
    return struct.unpack_from("<i", key, 12)[0] if len(key) == 16 and key.startswith(b"digp") else None


def inspect(db):
    entries = dimension_table(db).compound["entries"]
    chunks = {number.py_int: set() for number in entries.values()}
    for key in db.keys():
        dim = chunk_dimension(key)
        if dim in chunks:
            chunks[dim].add(key[:8])
    return [{"name": name, "id": number.py_int, "chunks": len(chunks[number.py_int])} for name, number in sorted(entries.items())]


def biome_ids(raw):
    """Read the palettes after Data3D's 256 int16 height values; do not translate."""
    if len(raw) < 512:
        raise ValueError("Truncated Data3D heightmap")
    offset, result = 512, set()
    while offset < len(raw):
        flags = raw[offset]
        offset += 1
        bits = flags >> 1
        if bits == 127:
            continue
        if bits not in (0, 1, 2, 3, 4, 5, 6, 8, 16) or not flags & 1:
            raise ValueError("Unsupported biome palette encoding")
        size = 1
        if bits:
            offset += ((4096 + (32 // bits) - 1) // (32 // bits)) * 4
            if offset + 4 > len(raw):
                raise ValueError("Truncated biome palette")
            size = struct.unpack_from("<I", raw, offset)[0]
            offset += 4
        if size < 1 or size > 65536 or offset + size * 4 > len(raw):
            raise ValueError("Invalid biome palette size")
        result.update(struct.unpack_from(f"<{size}I", raw, offset))
        offset += size * 4
    return result


def metadata_entries(raw):
    if raw is None:
        return {}
    if len(raw) < 4:
        raise ValueError("Truncated chunk metadata dictionary")
    count = struct.unpack_from("<I", raw)[0]
    offset = 4
    result = {}
    for _ in range(count):
        key = raw[offset:offset + 8]
        if len(key) != 8 or key in result:
            raise ValueError("Invalid chunk metadata hash")
        offset += 8
        context = ReadContext()
        load_nbt(raw[offset:], compressed=False, little_endian=True, string_decoder=utf8_escape_decoder, read_context=context)
        end = offset + context.offset
        result[key] = raw[offset:end]
        offset = end
    if offset != len(raw):
        raise ValueError("Unsupported chunk metadata dictionary layout")
    return result


def collect_unique_ids(tag):
    result = set()
    if isinstance(tag, CompoundTag):
        for key, value in tag.items():
            if key == "UniqueID":
                result.add(value.py_int)
            else:
                result.update(collect_unique_ids(value))
    elif isinstance(tag, ListTag):
        for value in tag:
            result.update(collect_unique_ids(value))
    return result


class Plan:
    """Disk-backed write set, so large dimensions don't become a giant RAM batch."""
    def __init__(self, path):
        self.db = sqlite3.connect(path)
        self.db.execute("CREATE TABLE changes (key BLOB PRIMARY KEY, value BLOB NOT NULL)")

    def add(self, target, key, value, shared=False):
        if not shared and get(target, key) is not None:
            raise ValueError(f"Destination record collision ({key.hex()}); nothing was imported")
        previous = self.db.execute("SELECT value FROM changes WHERE key=?", (key,)).fetchone()
        if previous is not None:
            if previous[0] != value:
                raise ValueError("Conflicting source database records")
            return
        self.db.execute("INSERT INTO changes VALUES (?, ?)", (key, value))

    def apply(self, target):
        self.db.commit()
        total = self.db.execute("SELECT count(*) FROM changes").fetchone()[0]
        batch, size, completed = {}, 0, 0
        for key, value in self.db.execute("SELECT key,value FROM changes ORDER BY key"):
            batch[key] = value
            size += len(key) + len(value)
            if size >= 4 << 20:
                target.putBatch(batch)
                completed += len(batch)
                emit("progress", message="Copying custom dimension", completed=completed, total=total)
                batch, size = {}, 0
        if batch:
            target.putBatch(batch)
            completed += len(batch)
        emit("progress", message="Dimension data copied", completed=completed, total=total)

    def close(self):
        self.db.close()


def prepare(source, target, name, plan):
    source_table = dimension_table(source)
    source_entries = source_table.compound["entries"]
    if name not in source_entries:
        raise ValueError("Custom dimension not found in the archive")
    dimension = source_entries[name].py_int
    destination_table = dimension_table(target)
    destination_entries = destination_table.compound["entries"]
    if name in destination_entries:
        raise ValueError("A dimension with this name already exists")
    if dimension in [value.py_int for value in destination_entries.values()]:
        raise ValueError("Numeric dimension ID is already used; automatic remapping is not supported")
    # Reject orphaned destination records, too, before creating a registration.
    for key in target.keys():
        if chunk_dimension(key) == dimension or digest_dimension(key) == dimension:
            raise ValueError("Destination already has records for this dimension ID")

    chunks, hashes, biomes, actors, imported_ids, digest_chunks = set(), set(), set(), set(), set(), set()
    for key in source.keys():
        if len(key) in (13, 14, 15, 21) and struct.unpack_from("<i", key, 8)[0] == dimension and chunk_dimension(key) is None:
            raise ValueError("Unsupported record layout in the selected dimension")
        if chunk_dimension(key) == dimension:
            value = source.get(key)
            chunks.add(key[:8])
            tag = key[12]
            if tag == 63:
                if len(value) != 8:
                    raise ValueError("Invalid chunk metadata reference")
                hashes.add(value)
            elif tag == 43:
                biomes.update(biome_ids(value))
            elif tag == 50:
                # Legacy entity streams need a separate ID/reference migration.
                if value:
                    raise ValueError("Legacy entity storage is unsupported; open and save the source in current Bedrock first")
            plan.add(target, key, value)
        elif digest_dimension(key) == dimension:
            digest_chunks.add(key[4:12])
            value = source.get(key)
            if len(value) % 8:
                raise ValueError("Invalid entity digest")
            actors.update(value[i:i+8] for i in range(0, len(value), 8))
            plan.add(target, key, value)
    if not chunks:
        raise ValueError("Selected custom dimension contains no saved chunks")
    if not digest_chunks <= chunks:
        raise ValueError("Custom dimension has entity digests without matching chunks")
    for actor in actors:
        key = b"actorprefix" + actor
        value = get(source, key)
        if value is None:
            raise ValueError("Custom dimension references a missing entity")
        actor_tag = nbt(value).tag
        actor_dimension = actor_tag.get("DimensionId")
        if actor_dimension is not None and actor_dimension.py_int != dimension:
            raise ValueError("An entity digest references an entity in another dimension")
        imported_ids.update(collect_unique_ids(actor_tag))
        plan.add(target, key, value)
    # Per-dimension data can contain limbo entities. Never copy other dimensions.
    value = get(source, name.encode())
    if value is not None:
        imported_ids.update(collect_unique_ids(nbt(value).tag))
        plan.add(target, name.encode(), value)
    for key in target.keys():
        if key.startswith(b"actorprefix") or key in (b"Overworld", b"Nether", b"TheEnd", b"AutonomousEntities") or key.decode("utf-8", errors="replace") in destination_entries:
            if imported_ids & collect_unique_ids(nbt(target.get(key)).tag):
                raise ValueError("Entity IDs conflict with the destination; automatic remapping is not supported")

    # Merge only metadata hashes referenced by imported chunks.
    if hashes:
        source_meta = metadata_entries(get(source, METADATA))
        dest_meta = metadata_entries(get(target, METADATA))
        for key in hashes:
            if key not in source_meta:
                raise ValueError("Missing metadata for an imported chunk")
            if key in dest_meta and dest_meta[key] != source_meta[key]:
                raise ValueError("Chunk metadata hash collision")
            dest_meta[key] = source_meta[key]
        raw = struct.pack("<I", len(dest_meta)) + b"".join(k + v for k, v in dest_meta.items())
        plan.add(target, METADATA, raw, shared=True)

    # Custom biome runtime IDs are embedded in the copied palettes. Append only
    # referenced mappings; reject conflicting IDs instead of changing old chunks.
    custom_biomes = {value for value in biomes if value >= 30000}
    if custom_biomes:
        raw = get(source, BIOMES)
        if raw is None:
            raise ValueError("Missing custom biome ID table")
        incoming = nbt(raw).compound.get("list")
        raw = get(target, BIOMES)
        table = nbt(raw) if raw is not None else NamedTag(CompoundTag({"list": ListTag()}))
        existing = table.compound.get("list")
        if not isinstance(incoming, ListTag) or not isinstance(existing, ListTag):
            raise ValueError("Unsupported biome ID table layout")
        for biome_id in custom_biomes:
            matches = [item for item in incoming if item["id"].py_int == biome_id]
            if len(matches) != 1:
                raise ValueError("Custom biome mapping is missing or ambiguous")
            item = matches[0]
            conflicts = [old for old in existing if old["id"] == item["id"] or old["name"] == item["name"]]
            if conflicts and (len(conflicts) != 1 or conflicts[0] != item):
                raise ValueError("Custom biome IDs conflict with the destination")
            if not conflicts:
                existing.append(copy.deepcopy(item))
        plan.add(target, BIOMES, serialize(table), shared=True)
    destination_entries[name] = IntTag(dimension)
    plan.add(target, DIMENSIONS, serialize(destination_table), shared=True)
    return {"name": name, "id": dimension, "chunks": len(chunks)}


class RemovalPlan(Plan):
    def __init__(self, path):
        self.db = sqlite3.connect(path)
        self.db.execute("CREATE TABLE changes (key BLOB PRIMARY KEY, value BLOB)")

    def remove(self, key):
        self.db.execute("INSERT OR IGNORE INTO changes VALUES (?, NULL)", (key,))

    def apply(self, target):
        self.db.commit()
        total = self.db.execute("SELECT count(*) FROM changes").fetchone()[0]
        # Remove dimension data first; registration goes last so retries can
        # still identify a partially deleted dimension after an interruption.
        for index, (key,) in enumerate(self.db.execute("SELECT key FROM changes WHERE value IS NULL"), 1):
            target.delete(key)
            if index % 1000 == 0:
                emit("progress", message="Removing custom dimension", completed=index, total=total)
        for key, value in self.db.execute("SELECT key,value FROM changes WHERE value IS NOT NULL"):
            if key != DIMENSIONS:
                target.put(key, value)
        row = self.db.execute("SELECT value FROM changes WHERE key=?", (DIMENSIONS,)).fetchone()
        if row:
            target.put(DIMENSIONS, row[0])
        emit("progress", message="Dimension removed", completed=total, total=total)


def prepare_removal(target, name, expected_id, plan, allow_missing=False):
    table = dimension_table(target)
    entries = table.compound["entries"]
    if expected_id < 1000 or name.startswith("minecraft:") or not re.fullmatch(r"[a-z0-9_]+:[a-z0-9_]+", name):
        raise ValueError("Only a tracked custom dimension can be removed")
    if name in entries:
        if entries[name].py_int != expected_id:
            raise ValueError("Dimension ID changed; refusing removal")
    elif not allow_missing:
        raise ValueError("Tracked dimension is missing from this world")
    if any(value.py_int == expected_id for key, value in entries.items() if key != name):
        raise ValueError("Dimension ID is now owned by another dimension")
    actors, other_actors, hashes, other_hashes, chunks = set(), set(), set(), set(), set()
    for key in target.keys():
        dim = chunk_dimension(key)
        if dim == expected_id:
            plan.remove(key)
            chunks.add(key[:8])
        elif len(key) in (13, 14, 15, 21) and struct.unpack_from("<i", key, 8)[0] == expected_id:
            raise ValueError("Unsupported chunk record; nothing was removed")
        if (len(key) == 13 and key[12] == 63) or (len(key) == 9 and key[8] == 63):
            (hashes if dim == expected_id else other_hashes).add(target.get(key))
        if key.startswith(b"digp") and len(key) in (12, 16):
            value = target.get(key)
            if len(value) % 8:
                raise ValueError("Invalid entity digest; nothing was removed")
            ids = {value[i:i+8] for i in range(0, len(value), 8)}
            if digest_dimension(key) == expected_id:
                actors.update(ids)
                plan.remove(key)
            else:
                other_actors.update(ids)
        if key.startswith((b"player_", b"legacy_console_player_", b"~local_player")):
            player = nbt(target.get(key)).compound
            if any(getattr(player.get(field), "py_int", None) == expected_id for field in ("DimensionId", "SpawnDimension")):
                raise ValueError("A player is saved in this dimension or has a spawn point there. Move them and their spawn to another dimension, then stop the server and retry")
        if key.startswith(b"tickingarea"):
            area = nbt(target.get(key)).compound
            if getattr(area.get("Dimension"), "py_int", None) == expected_id:
                plan.remove(key)
        if key.startswith((f"VILLAGE_{expected_id}_".encode(), f"chunk_loaded_request_{expected_id}_".encode(), f"poi.{expected_id}.regions.".encode())):
            plan.remove(key)
        if key == f"poi.{expected_id}.regions".encode():
            plan.remove(key)
    if actors & other_actors:
        raise ValueError("Entities are shared with another dimension; nothing was removed")
    for key in target.keys():
        if not key.startswith(b"actorprefix"):
            continue
        actor = nbt(target.get(key)).compound
        dimension = getattr(actor.get("DimensionId"), "py_int", None)
        if dimension == expected_id or key[len(b"actorprefix"):] in actors:
            if key[len(b"actorprefix"):] in other_actors or (dimension is not None and dimension != expected_id):
                raise ValueError("Entity dimension references conflict; nothing was removed")
            plan.remove(key)
    plan.remove(name.encode())
    raw_metadata = get(target, METADATA)
    if raw_metadata is not None:
        metadata = metadata_entries(raw_metadata)
        removed = hashes - other_hashes
        kept = {key: value for key, value in metadata.items() if key not in removed}
        if len(kept) != len(metadata):
            plan.add(target, METADATA, struct.pack("<I", len(kept)) + b"".join(key + value for key, value in kept.items()), shared=True)
    # Biome IDs and other world-wide state may be used outside this dimension.
    # Keep those mappings rather than rewriting any remaining chunk or player.
    if name in entries:
        del entries[name]
    plan.add(target, DIMENSIONS, serialize(table), shared=True)
    return {"name": name, "id": expected_id, "chunks": len(chunks)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("mode", choices=("inspect", "check", "apply", "remove-check", "remove"))
    parser.add_argument("--source", default="")
    parser.add_argument("--target", required=True)
    parser.add_argument("--dimension", default="")
    parser.add_argument("--dimension-id", type=int, default=0)
    parser.add_argument("--allow-missing", action="store_true")
    args = parser.parse_args()
    if args.mode in ("remove-check", "remove"):
        with open_world(args.target) as target, tempfile.TemporaryDirectory(prefix="mcui-dimension-remove-") as stage:
            plan = RemovalPlan(str(Path(stage) / "plan.sqlite"))
            try:
                result = prepare_removal(target, args.dimension, args.dimension_id, plan, args.allow_missing)
                if args.mode == "remove":
                    plan.apply(target)
                emit("result", dimensions=[result])
            finally:
                plan.close()
        return
    with open_world(args.source) as source:
        if args.mode == "inspect":
            emit("result", dimensions=inspect(source))
            return
        with open_world(args.target) as target, tempfile.TemporaryDirectory(prefix="mcui-dimension-plan-") as stage:
            plan = Plan(str(Path(stage) / "plan.sqlite"))
            try:
                emit("progress", message="Checking dimension records and conflicts")
                result = prepare(source, target, args.dimension, plan)
                if args.mode == "apply":
                    plan.apply(target)
                emit("result", dimensions=[result])
            finally:
                plan.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        emit("error", message=str(exc))
        sys.exit(1)
