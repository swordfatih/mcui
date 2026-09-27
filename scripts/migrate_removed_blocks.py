#!/usr/bin/env python3
"""Import legacy SP2/textures/blocks/removed_blocks into MCUI's asset archive.

Run without --apply to inspect the proposed changes. Stop the server before
applying. The script never edits live pack catalogs or archived file contents.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile


def texture_key(value):
    for suffix in (".png", ".tga", ".jpg", ".jpeg"):
        if value.endswith(suffix):
            return value[: -len(suffix)]
    return value


def references(value):
    if isinstance(value, str):
        return [value]
    if isinstance(value, list):
        return [item for child in value for item in references(child)]
    if isinstance(value, dict):
        return [item for child in value.values() for item in references(child)]
    return []


def read_json(path):
    return json.loads(path.read_text(encoding="utf-8"))


def add_patch(record, patch):
    if patch not in record["patches"]:
        record["patches"].append(patch)


def patches_for(records, layer_dir, catalog_patches):
    by_key = {}
    for record in records:
        by_key.setdefault(texture_key(record["path"]), []).append(record)
        record.setdefault("patches", [])

    by_path = {record["path"]: record for record in records}
    alias_requires = {}
    removed_aliases = set()
    for record in records:
        for patch in record["patches"]:
            if patch["file"] == "textures/terrain_texture.json":
                alias_requires[patch["key"]] = [record["path"]]
                removed_aliases.add(patch["key"])
    for patch in catalog_patches:
        if patch["file"] == "textures/terrain_texture.json":
            alias_requires[patch["key"]] = patch["requires"]
            removed_aliases.add(patch["key"])

    terrain = layer_dir / "textures/terrain_texture.json"
    old_terrain = terrain.with_name(terrain.name + ".before_removed_paths")
    if terrain.is_file() and old_terrain.is_file():
        current = read_json(terrain).get("texture_data", {})
        original = read_json(old_terrain).get("texture_data", {})
        for key, value in original.items():
            if key in current:
                continue
            removed_aliases.add(key)
            paths = [texture_key(path) for path in references(value.get("textures", []))] if isinstance(value, dict) else []
            if not paths:
                continue
            requirements = set()
            unresolved = False
            for path in paths:
                matches = by_key.get(path, [])
                if len(matches) == 1:
                    requirements.add(matches[0]["path"])
                elif len(matches) > 1:
                    unresolved = True
                elif not any((layer_dir / (path + ext)).is_file() for ext in ("", ".png", ".tga", ".jpg", ".jpeg", ".webp")):
                    unresolved = True
            if unresolved or not requirements:
                continue
            required = sorted(requirements)
            alias_requires[key] = required
            patch = {"file": "textures/terrain_texture.json", "key": key, "value": value}
            if len(required) == 1:
                add_patch(by_path[required[0]], patch)
            else:
                add_patch({"patches": catalog_patches}, {**patch, "requires": required})

    texture_list = layer_dir / "textures/textures_list.json"
    old_list = texture_list.with_name(texture_list.name + ".before_removed_paths")
    if texture_list.is_file() and old_list.is_file():
        current = read_json(texture_list)
        original = read_json(old_list)
        for index, value in enumerate(original):
            if value in current or not isinstance(value, str):
                continue
            matches = by_key.get(texture_key(value), [])
            if len(matches) == 1:
                add_patch(matches[0], {"file": "textures/textures_list.json", "index": index, "value": value})

    blocks = layer_dir / "blocks.json"
    old_blocks = blocks.with_name(blocks.name + ".before_removed_paths")
    unresolved_blocks = 0
    if blocks.is_file() and old_blocks.is_file():
        current = read_json(blocks)
        original = read_json(old_blocks)
        for key, value in original.items():
            if key == "format_version" or key in current or not isinstance(value, dict):
                continue
            aliases = references(value.get("textures", {})) + references(value.get("carried_textures", {}))
            affected = [alias for alias in aliases if alias in removed_aliases]
            if not affected:
                continue
            if not all(alias in alias_requires for alias in affected):
                unresolved_blocks += 1
                continue
            required = sorted({path for alias in affected for path in alias_requires[alias]})
            patch = {"file": "blocks.json", "key": key, "value": value}
            if len(required) == 1:
                add_patch(by_path[required[0]], patch)
            else:
                add_patch({"patches": catalog_patches}, {**patch, "requires": required})
    return unresolved_blocks


def migrate(server, pack, layer, apply, repair_index=False):
    server = server.expanduser().resolve(strict=True)
    if not server.is_dir() or not (server / "bedrock_data").is_dir():
        raise ValueError("Expected a server root containing bedrock_data")
    if any(name in ("", ".", "..") or "/" in name or "\\" in name for name in (pack, layer)):
        raise ValueError("Pack and layer must be simple folder names")
    pack_dir = server / "bedrock_data/resource_packs" / pack
    layer_dir = pack_dir / "subpacks" / layer
    source_dir = layer_dir / "textures/blocks/removed_blocks"
    if not repair_index and (not source_dir.is_dir() or source_dir.is_symlink()):
        raise ValueError(f"Legacy folder not found: {source_dir}")
    uuid = read_json(pack_dir / "manifest.json")["header"]["uuid"]
    archive = server / ".mcui/archived-assets/resource" / pack / layer
    state_path = server / ".mcui/asset-state/resource" / f"{pack}.json"
    state = read_json(state_path) if state_path.exists() else {"uuid": uuid, "assets": []}
    state.setdefault("catalog_patches", [])
    if state["uuid"].lower() != uuid.lower():
        raise ValueError("Existing archive index belongs to another pack UUID")
    existing = {(item["layer"], item["path"]) for item in state["assets"]}
    moves = []
    if repair_index:
        records = [item for item in state["assets"] if item["layer"] == layer and item["path"].startswith("textures/blocks/")]
        if not records:
            if source_dir.is_dir():
                raise ValueError("Nothing has been imported yet. Run this command without --repair-index to preview the initial import, then add --apply.")
            raise ValueError("No indexed block textures found to repair, and the legacy removed_blocks folder is missing")
    else:
        for path in sorted(source_dir.rglob("*")):
            if path.is_symlink():
                raise ValueError(f"Symlink found in legacy folder: {path}")
            if not path.is_file():
                continue
            relative = path.relative_to(source_dir)
            asset_path = (Path("textures/blocks") / relative).as_posix()
            dest = archive / asset_path
            if dest.exists() or (layer_dir / asset_path).exists() or (layer, asset_path) in existing:
                raise ValueError(f"Destination or index already exists for {asset_path}")
            record = {"path": asset_path, "layer": layer, "size": path.stat().st_size,
                      "hash": hashlib.sha256(path.read_bytes()).hexdigest(), "patches": []}
            moves.append((path, dest, record))
        if not moves:
            raise ValueError("No files found in removed_blocks")
        records = [record for _, _, record in moves]
    before_patches = sum(len(record.get("patches", [])) for record in records) + len(state["catalog_patches"])
    unresolved_blocks = patches_for(records, layer_dir, state["catalog_patches"])
    if not repair_index:
        print(f"{len(moves)} files will move from {source_dir} into {archive}")
    else:
        print(f"Updating existing archive index at {state_path}")
    catalog_count = sum(len(record['patches']) for record in records) + len(state["catalog_patches"])
    print(f"{catalog_count} catalog entries can be restored later by MCUI")
    if unresolved_blocks:
        print(f"{unresolved_blocks} block entries have unmapped removed aliases and need manual review")
    if not apply:
        print("Dry run only. Stop the server, then rerun with --apply.")
        return
    completed = []
    try:
        for src, dest, _ in moves:
            dest.parent.mkdir(parents=True, exist_ok=True)
            shutil.move(str(src), str(dest))
            completed.append((src, dest))
        if not repair_index:
            state["assets"].extend(records)
        state_path.parent.mkdir(parents=True, exist_ok=True)
        fd, temp = tempfile.mkstemp(prefix=".asset-state-", dir=state_path.parent)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as out:
                json.dump(state, out, indent=2)
                out.write("\n")
                out.flush()
                os.fsync(out.fileno())
            os.replace(temp, state_path)
        finally:
            if os.path.exists(temp):
                os.unlink(temp)
    except Exception:
        for src, dest in reversed(completed):
            shutil.move(str(dest), str(src))
        raise
    print(f"{'Indexed' if repair_index else 'Imported'} {len(records)} files and {catalog_count - before_patches} new catalog references. Refresh the pack's Images section.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("server", type=Path, help="Server root, such as ~/servers/bedrock_ana")
    parser.add_argument("pack", help="Resource-pack folder, such as Actions_Stuff")
    parser.add_argument("layer", help="Subpack folder, such as SP2")
    parser.add_argument("--apply", action="store_true", help="Move files and write the MCUI archive index")
    parser.add_argument("--repair-index", action="store_true", help="Add saved catalog references to an existing MCUI archive index")
    args = parser.parse_args()
    try:
        migrate(args.server, args.pack, args.layer, args.apply, args.repair_index)
    except (OSError, ValueError, KeyError, json.JSONDecodeError) as exc:
        sys.exit(f"Migration stopped: {exc}")
