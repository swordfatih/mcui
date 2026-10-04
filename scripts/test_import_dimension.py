"""Integration tests using Amulet's real Mojang LevelDB binding."""
import io
from contextlib import redirect_stdout
from pathlib import Path
import struct
import json
import subprocess
import sys
import tempfile
import unittest

from amulet_nbt import CompoundTag, IntTag, ShortTag, LongTag, StringTag, ListTag, NamedTag
from leveldb import LevelDB
import import_dimension as worker


def encoded(values):
    return worker.serialize(NamedTag(CompoundTag(values)))


def table(values):
    return encoded({"entries": CompoundTag({key: IntTag(value) for key, value in values.items()})})


def chunk(dimension, tag, x=0, z=0):
    return struct.pack("<ii", x, z) + (struct.pack("<i", dimension) if dimension else b"") + bytes([tag])


def make_world(path, records):
    path.mkdir()
    level = encoded({"LevelName": StringTag("Untouched"), "worldStartCount": LongTag(123), "lastOpenedWithVersion": ListTag([IntTag(1), IntTag(21), IntTag(80)])})
    (path / 'level.dat').write_bytes(struct.pack('<II', 10, len(level)) + level)
    db = LevelDB(str(path / 'db'), True)
    db.putBatch(records)
    db.close()


class DimensionImportTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source, self.target = self.root / 'source', self.root / 'target'
        self.actor = struct.pack('>ii', 12, 45)
        self.actor_value = encoded({'UniqueID': LongTag(-51539607507), 'identifier': StringTag('spark:moa'), 'DimensionId': IntTag(1000)})
        self.hash = b'custom01'
        self.metadata = encoded({'DimensionName': StringTag('spark:aether')})
        self.original_hash = b'original'
        self.original_metadata = encoded({'DimensionName': StringTag('Overworld')})
        self.records = {
            worker.DIMENSIONS: table({'spark:aether': 1000, 'other:source': 1002}),
            chunk(1000, 44): b'\x29',
            chunk(1000, 47) + b'\x00': b'custom blocks kept byte for byte',
            chunk(1000, 49): encoded({'id': StringTag('Chest'), 'x': IntTag(1)}),
            chunk(1000, 43): b'\0' * 512 + b'\x01' + struct.pack('<I', 30000),
            chunk(1000, 63): self.hash,
            worker.METADATA: struct.pack('<I', 2) + self.hash + self.metadata + b'unused01' + encoded({'DimensionName': StringTag('other:source')}),
            worker.BIOMES: encoded({'list': ListTag([CompoundTag({'id': ShortTag(30000), 'name': StringTag('spark:forest')}), CompoundTag({'id': ShortTag(30001), 'name': StringTag('other:unused')})])}),
            b'digp' + struct.pack('<iii', 0, 0, 1000): self.actor,
            b'actorprefix' + self.actor: self.actor_value,
            b'spark:aether': encoded({}),
            chunk(0, 44): b'source overworld',
            chunk(1, 44): b'source nether',
            chunk(2, 44): b'source end',
            chunk(1002, 44): b'unselected dimension',
            b'~local_player': b'source player',
            b'scoreboard': b'source scoreboard',
            b'DynamicProperties': b'source script state',
        }
        self.existing = {
            worker.DIMENSIONS: table({'existing:dimension': 1001}),
            worker.METADATA: struct.pack('<I', 1) + self.original_hash + self.original_metadata,
            chunk(0, 44): b'original overworld',
            chunk(1, 44): b'original nether',
            chunk(2, 44): b'original end',
            chunk(1001, 44): b'original custom dimension',
            b'~local_player': b'original player',
            b'scoreboard': b'original scoreboard',
            b'DynamicProperties': b'original script state',
        }
        make_world(self.source, self.records)
        make_world(self.target, self.existing)
        self.source_level = (self.source / 'level.dat').read_bytes()
        self.target_level = (self.target / 'level.dat').read_bytes()

    def read_target(self):
        with worker.open_world(self.target) as db:
            return dict(db.items())

    def apply(self):
        with worker.open_world(self.source) as source, worker.open_world(self.target) as target:
            plan = worker.Plan(str(self.root / 'plan.sqlite'))
            try:
                worker.prepare(source, target, 'spark:aether', plan)
                with redirect_stdout(io.StringIO()):
                    plan.apply(target)
            finally:
                plan.close()

    def test_import_only_selected_dimension_preserving_all_existing_records(self):
        self.apply()
        result = self.read_target()
        for key, value in self.existing.items():
            if key not in (worker.DIMENSIONS, worker.METADATA):
                self.assertEqual(value, result[key], key)
        self.assertEqual(self.records[chunk(1000,47)+b'\x00'], result[chunk(1000,47)+b'\x00'])
        self.assertEqual(self.actor_value, result[b'actorprefix'+self.actor])
        self.assertEqual(self.records[chunk(1000,49)], result[chunk(1000,49)])
        self.assertNotIn(chunk(1002,44), result)
        self.assertEqual(set(worker.nbt(result[worker.DIMENSIONS]).compound['entries']), {'spark:aether','existing:dimension'})
        self.assertEqual(worker.metadata_entries(result[worker.METADATA]), {self.original_hash:self.original_metadata, self.hash:self.metadata})
        biome_list=worker.nbt(result[worker.BIOMES]).compound['list']
        self.assertEqual(len(biome_list),1)
        self.assertEqual(biome_list[0]['name'].py_str,'spark:forest')
        self.assertEqual(self.target_level,(self.target/'level.dat').read_bytes())
        self.assertEqual(self.source_level,(self.source/'level.dat').read_bytes())

    def test_inspection_does_not_write_world_metadata(self):
        with worker.open_world(self.source) as source:
            dimensions=worker.inspect(source)
        self.assertEqual(dimensions,[{'name':'other:source','id':1002,'chunks':1},{'name':'spark:aether','id':1000,'chunks':1}])
        self.assertEqual(self.source_level,(self.source/'level.dat').read_bytes())

    def test_dimension_collision_rejects_before_any_writes(self):
        with worker.open_world(self.target) as target:
            target.put(worker.DIMENSIONS,table({'existing:dimension':1000}))
        before=self.read_target()
        with self.assertRaisesRegex(ValueError,'dimension ID'):
            self.apply()
        self.assertEqual(before,self.read_target())

    def test_entity_collision_rejects_before_any_writes(self):
        with worker.open_world(self.target) as target:
            target.put(b'actorprefix'+self.actor,self.actor_value)
        before=self.read_target()
        with self.assertRaisesRegex(ValueError,'collision'):
            self.apply()
        self.assertEqual(before,self.read_target())

    def test_missing_actor_rejects_before_any_writes(self):
        with worker.open_world(self.source) as source:
            source.delete(b'actorprefix'+self.actor)
        with self.assertRaisesRegex(ValueError,'missing entity'):
            self.apply()
        self.assertEqual(self.existing,self.read_target())

    def test_biome_conflict_rejects_before_any_writes(self):
        with worker.open_world(self.target) as target:
            target.put(worker.BIOMES,encoded({'list':ListTag([CompoundTag({'id':ShortTag(30000),'name':StringTag('existing:biome')})])}))
        before=self.read_target()
        with self.assertRaisesRegex(ValueError,'biome IDs conflict'):
            self.apply()
        self.assertEqual(before,self.read_target())

    def test_invalid_metadata_is_rejected(self):
        with self.assertRaises(Exception):
            worker.metadata_entries(struct.pack('<I',1)+b'bad')

    def test_worker_cli_protocol(self):
        script = Path(__file__).with_name('import_dimension.py')
        for mode in ('inspect', 'check', 'apply'):
            result = subprocess.run([sys.executable, str(script), mode,
                                     '--source', str(self.source), '--target', str(self.target),
                                     '--dimension', 'spark:aether'], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            events = [json.loads(line) for line in result.stdout.splitlines()]
            self.assertEqual(events[-1]['type'], 'result')
            if mode == 'check':
                self.assertEqual(self.existing, self.read_target())
        self.assertIn(chunk(1000, 44), self.read_target())
        self.assertEqual(self.target_level, (self.target/'level.dat').read_bytes())

    def removal(self, apply=True):
        with worker.open_world(self.target) as target:
            with tempfile.TemporaryDirectory() as stage:
                plan = worker.RemovalPlan(str(Path(stage) / 'remove.sqlite'))
                try:
                    worker.prepare_removal(target, 'spark:aether', 1000, plan)
                    if apply:
                        with redirect_stdout(io.StringIO()):
                            plan.apply(target)
                finally:
                    plan.close()

    def test_removal_deletes_grown_dimension_and_preserves_other_world_data(self):
        self.apply()
        with worker.open_world(self.target) as target:
            target.put(b'~local_player', encoded({'DimensionId':IntTag(0),'SpawnDimension':IntTag(0)}))
            target.put(chunk(1000, 44, 50, 50), b'newly explored chunk')
            target.put(b'tickingarea-owned', encoded({'Dimension':IntTag(1000)}))
            target.put(b'tickingarea-existing', encoded({'Dimension':IntTag(0)}))
        before = self.read_target()
        self.removal()
        after = self.read_target()
        for key, value in before.items():
            if worker.chunk_dimension(key) == 1000 or worker.digest_dimension(key) == 1000 or key in (b'actorprefix'+self.actor,b'spark:aether',b'tickingarea-owned',worker.DIMENSIONS,worker.METADATA):
                continue
            self.assertEqual(value, after[key], key)
        self.assertFalse(any(worker.chunk_dimension(key) == 1000 for key in after))
        self.assertNotIn(b'actorprefix'+self.actor,after)
        self.assertNotIn(b'spark:aether',after)
        self.assertNotIn(b'tickingarea-owned',after)
        self.assertEqual(after[worker.DIMENSIONS],self.existing[worker.DIMENSIONS])
        self.assertEqual(after[worker.METADATA],self.existing[worker.METADATA])
        self.assertEqual(self.target_level,(self.target/'level.dat').read_bytes())

    def test_removal_blocks_players_and_spawn_points_without_writes(self):
        self.apply()
        for field in ('DimensionId','SpawnDimension'):
            with worker.open_world(self.target) as target:
                target.put(b'~local_player',encoded({field:IntTag(1000)}))
            before=self.read_target()
            with self.assertRaisesRegex(ValueError,'player is saved'):
                self.removal()
            self.assertEqual(before,self.read_target())

    def test_removal_refuses_changed_dimension_id(self):
        self.apply()
        with worker.open_world(self.target) as target:
            target.put(worker.DIMENSIONS,table({'spark:aether':1003}))
        before=self.read_target()
        with self.assertRaisesRegex(ValueError,'ID changed'):
            self.removal()
        self.assertEqual(before,self.read_target())

    def test_removal_cli(self):
        self.apply()
        with worker.open_world(self.target) as target:
            target.put(b'~local_player',encoded({'DimensionId':IntTag(0)}))
        before=self.read_target()
        for mode in ('remove-check','remove'):
            result=subprocess.run([sys.executable,str(Path(__file__).with_name('import_dimension.py')),mode,'--target',str(self.target),'--dimension','spark:aether','--dimension-id','1000'],capture_output=True,text=True)
            self.assertEqual(result.returncode,0,result.stdout+result.stderr)
            self.assertEqual(json.loads(result.stdout.splitlines()[-1])['type'],'result')
            if mode=='remove-check':self.assertEqual(before,self.read_target())
        self.assertNotIn(chunk(1000,44),self.read_target())


if __name__=='__main__':
    unittest.main()
