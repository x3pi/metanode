"""Storage command tests: no root access or real device mutations."""
import argparse
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parent))
import manage_snapshot_storage as storage

MIB = 1024**2


class StorageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.mount = self.root / 'snapshots'
        self.mount.mkdir()
        (self.mount / 'node-4').mkdir()
        self.image = self.root / 'storage.img'
        with self.image.open('wb') as file:
            file.truncate(512 * MIB)
        self.lv = self.root / 'lv'
        self.args = argparse.Namespace(size='1G', nodes='4', snapshot_nodes='4',
                                       clean=False, check=False, install_dir='/opt/metanode')
        self.rows = [dict(target=str(self.mount), source='/dev/loop77', fstype='btrfs', **{'maj:min': '7:77'}),
                     dict(target='/opt/metanode/node-4/data', source='/dev/loop77[/node-4]', fstype='btrfs', **{'maj:min': '7:77'})]
        for name, value in [('MOUNT', self.mount), ('IMAGE', self.image), ('LV', self.lv)]:
            patcher = patch.object(storage, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        self.mounts = patch.object(storage, 'mounts', return_value=self.rows).start()
        self.fs_size = patch.object(storage, 'fs_size', return_value=512 * MIB).start()
        self.free = patch.object(storage.shutil, 'disk_usage', return_value=argparse.Namespace(free=10 * 1024**3)).start()
        self.run = patch.object(storage, 'run', side_effect=self.command).start()
        self.addCleanup(patch.stopall)

    def command(self, *args):
        if args[:3] == ('losetup', '-n', '-O'):
            return str(self.image)
        if args[0] == 'lvs':
            return str(512 * MIB)
        if args[0] == 'vgs':
            return str(4 * MIB if 'vg_extent_size' in args else 10 * 1024**3)
        return ''

    def test_size_validation(self):
        self.assertEqual(storage.size_bytes('1.5T'), 1536 * 1024**3)
        for value in ['0G', '-1G', '1;touch /tmp/x', '256K', '1', '1.00001G']:
            with self.subTest(value=value), self.assertRaises(ValueError):
                storage.size_bytes(value)

    def test_grow_and_noop(self):
        self.assertTrue(storage.plan(self.args)['changed'])
        self.args.size = '512M'
        self.assertFalse(storage.plan(self.args)['changed'])

    def test_shrink_requires_clean(self):
        self.args.size = '256M'
        with self.assertRaisesRegex(ValueError, 'Shrinking'):
            storage.plan(self.args)
        self.args.clean = True
        self.assertEqual(storage.plan(self.args)['target'], 256 * MIB)

    def test_insufficient_space(self):
        self.free.return_value.free = 0
        with self.assertRaisesRegex(ValueError, 'Insufficient'):
            storage.plan(self.args)

    def test_shared_node_rejected_before_mutation(self):
        self.args.clean = True
        (self.mount / 'node-5').mkdir()
        with self.assertRaisesRegex(ValueError, 'Shared storage'):
            storage.plan(self.args)
        self.assertFalse(any(c.args[0] in ['umount', 'truncate', 'mkfs.btrfs'] for c in self.run.call_args_list))

    def test_full_shared_clean_allowed(self):
        self.args.clean = True
        self.args.nodes = '4,5'
        (self.mount / 'node-5').mkdir()
        self.assertTrue(storage.plan(self.args)['changed'])

    def test_foreign_mount_and_unknown_files_rejected(self):
        self.args.clean = True
        self.rows.append(dict(target='/other', source='/dev/loop77', fstype='btrfs', **{'maj:min': '7:77'}))
        with self.assertRaisesRegex(ValueError, 'also mounted'):
            storage.plan(self.args)
        self.rows.pop()
        (self.mount / 'unrelated').write_text('keep')
        with self.assertRaisesRegex(ValueError, 'Unknown entries'):
            storage.plan(self.args)

    def test_unmounted_existing_storage_rejected(self):
        self.mounts.return_value = []
        with self.assertRaisesRegex(ValueError, 'unmounted'):
            storage.plan(self.args)

    def test_other_node_does_not_touch_storage(self):
        self.args.nodes = '1'
        self.args.clean = True
        self.assertIsNone(storage.plan(self.args))

    def test_loop_grow_preserves_filesystem(self):
        state = storage.plan(self.args)
        self.run.reset_mock()
        self.fs_size.return_value = state['target']
        storage.apply(self.args, state)
        self.assertEqual([c.args[0] for c in self.run.call_args_list], ['truncate', 'losetup', 'btrfs'])

    def test_retry_after_backing_file_grew(self):
        self.args.size = '512M'
        self.fs_size.return_value = 256 * MIB
        state = storage.plan(self.args)
        self.run.reset_mock()
        self.fs_size.return_value = state['target']
        storage.apply(self.args, state)
        self.assertEqual([c.args[0] for c in self.run.call_args_list], ['losetup', 'btrfs'])

    def test_clean_unmount_failure_never_formats(self):
        self.args.clean = True
        state = storage.plan(self.args)
        self.run.reset_mock()
        self.run.side_effect = subprocess.CalledProcessError(1, ['umount'])
        with self.assertRaises(subprocess.CalledProcessError):
            storage.apply(self.args, state)
        self.assertEqual([c.args[0] for c in self.run.call_args_list], ['umount'])

    def test_loop_clean_orders_unmount_detach_format_mount(self):
        self.args.clean = True
        state = storage.plan(self.args)
        self.run.reset_mock()
        self.fs_size.return_value = state['target']
        storage.apply(self.args, state)
        self.assertEqual([c.args[0] for c in self.run.call_args_list],
                         ['umount', 'umount', 'losetup', 'losetup', 'truncate', 'truncate', 'mkfs.btrfs', 'mount'])

    def test_lvm_size_rounding_and_growth(self):
        self.lv.touch()
        self.rows[0]['source'] = str(self.lv)
        self.args.size = '513M'
        state = storage.plan(self.args)
        self.assertEqual(state['target'], 516 * MIB)
        self.run.reset_mock()
        self.fs_size.return_value = state['target']
        storage.apply(self.args, state)
        self.assertEqual([c.args[0] for c in self.run.call_args_list], ['lvextend', 'btrfs'])

    def test_lvm_clean_shrink_formats_only_after_unmount(self):
        self.lv.touch()
        self.rows[0]['source'] = str(self.lv)
        self.args.size, self.args.clean = '256M', True
        state = storage.plan(self.args)
        self.run.reset_mock()
        self.fs_size.return_value = state['target']
        storage.apply(self.args, state)
        self.assertEqual([c.args[0] for c in self.run.call_args_list], ['umount', 'umount', 'lvresize', 'mkfs.btrfs', 'mount'])

    def test_size_mismatch_fails(self):
        state = storage.plan(self.args)
        with self.assertRaisesRegex(ValueError, 'verification failed'):
            storage.apply(self.args, state)

    def test_remote_test_runner_preserves_genesis_and_omits_clean(self):
        script = (Path(__file__).resolve().parents[2] / 'test/test_remote_deploy.sh').read_text()
        start = script.index('UNZIP_EXCLUDE=""')
        end = script.index('# 6. GỬI GIAO DỊCH', start)
        commands = self.root / 'commands'
        harness = 'exec_remote() { printf "%s\\0" "$5" >> "$COMMANDS"; }\n' + script[start:end]
        for skip in ['true', 'false']:
            with self.subTest(skip_clean=skip):
                commands.write_bytes(b'')
                env = dict(os.environ, SKIP_CLEAN=skip, TARGET_DIR='/tmp/fixture-deploy',
                           COMMANDS=str(commands), ZIP_BASENAME='fixture.zip')
                subprocess.run(['bash', '-e', '-c', harness], env=env, capture_output=True, check=True)
                recorded = commands.read_text()
                if skip == 'true':
                    self.assertNotIn('--gen-keys', recorded)
                    self.assertNotIn('--clean', recorded)
                    self.assertIn('-x deploy/systemd/genesis.json', recorded)
                else:
                    self.assertIn('--gen-keys', recorded)
                    self.assertIn('--clean', recorded)


if __name__ == '__main__':
    unittest.main()
