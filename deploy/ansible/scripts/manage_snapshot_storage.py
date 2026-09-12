#!/usr/bin/env python3
"""Grow or recreate the single-device snapshot filesystem managed by this deploy."""
import argparse
from decimal import Decimal
import fcntl
import json
import os
from pathlib import Path
import re
import shutil
import subprocess

MOUNT = Path('/mnt/metanode_snapshots')
IMAGE = Path('/opt/metanode_cluster_btrfs.img')
LV = Path('/dev/ubuntu-vg/metanode_data')


def run(*args):
    return subprocess.run(args, capture_output=True, text=True, check=True).stdout.strip()


def size_bytes(value):
    match = re.fullmatch(r'(\d+(?:\.\d+)?)\s*([KMGT])B?', value.upper())
    if not match:
        raise ValueError('Size must include K/M/G/T, for example 600G or 1T')
    size = int(Decimal(match[1]) * 1024 ** ('KMGT'.index(match[2]) + 1))
    if size < 256 * 1024**2 or size % (1024**2):
        raise ValueError('Size must be at least 256M and a whole number of MiB')
    return size


def nodes(value):
    if not value:
        return set()
    if not re.fullmatch(r'\d+(,\d+)*', value):
        raise ValueError('Invalid node IDs')
    return {int(n) for n in value.split(',')}


def mounts():
    def flatten(rows):
        for row in rows:
            yield row
            yield from flatten(row.get('children', []))
    return list(flatten(json.loads(run('findmnt', '-J', '-o', 'TARGET,SOURCE,FSTYPE,MAJ:MIN'))['filesystems']))


def fs_size():
    output = run('btrfs', 'filesystem', 'show', '--raw', str(MOUNT))
    devices = re.findall(r'devid\s+\d+\s+size\s+(\d+)', output)
    if len(devices) != 1 or not re.search(r'Total devices\s+1\b', output):
        raise ValueError('Only single-device BTRFS is supported')
    return int(devices[0])


def plan(args):
    requested = size_bytes(args.size)
    active, snapshots = nodes(args.nodes), nodes(args.snapshot_nodes)
    rows = mounts()
    root = next((r for r in rows if r['target'] == str(MOUNT)), None)
    if root is None:
        if active & snapshots and (IMAGE.exists() or LV.exists()):
            if IMAGE.exists():
                subprocess.run(['mount', '-t', 'btrfs', '-o', 'loop', str(IMAGE), str(MOUNT)], capture_output=True)
            elif LV.exists():
                subprocess.run(['mount', '-t', 'btrfs', str(LV), str(MOUNT)], capture_output=True)
            rows = mounts()
            root = next((r for r in rows if r['target'] == str(MOUNT)), None)
        if root is None:
            if active & snapshots and (IMAGE.exists() or LV.exists()):
                raise ValueError('Existing storage is unmounted; mount it at /mnt/metanode_snapshots before resizing/cleaning')
            return None  # Initial provisioning remains owned by node_setup.
    if root['fstype'] != 'btrfs':
        if active & snapshots:
            raise ValueError('Snapshot mount is not BTRFS; refusing to modify it')
        return None

    entries = list(MOUNT.iterdir())
    stored = {int(p.name[5:]) for p in entries if re.fullmatch(r'node-\d+', p.name)}
    if not active & (snapshots | stored):
        return None
    device = root['source'].split('[', 1)[0]
    if device.startswith('/dev/loop'):
        backing = Path(run('losetup', '-n', '-O', 'BACK-FILE', device))
        if backing != IMAGE or IMAGE.is_symlink() or not IMAGE.is_file() or IMAGE.stat().st_nlink != 1:
            raise ValueError('Refusing to modify an unmanaged loop image')
        kind = 'loop'
        current = IMAGE.stat().st_size
        target = requested
        free = shutil.disk_usage(IMAGE.parent).free
        available = free + (IMAGE.stat().st_blocks * 512 if args.clean else 0)
        needed = target if args.clean else max(0, target - IMAGE.stat().st_blocks * 512)
    elif os.path.realpath(device) == os.path.realpath(LV) and LV.exists():
        kind = 'lvm'
        current = int(Decimal(run('lvs', '--noheadings', '--units', 'b', '--nosuffix', '-o', 'lv_size', str(LV))))
        extent = int(Decimal(run('vgs', '--noheadings', '--units', 'b', '--nosuffix', '-o', 'vg_extent_size', 'ubuntu-vg')))
        target = ((requested + extent - 1) // extent) * extent
        available = int(Decimal(run('vgs', '--noheadings', '--units', 'b', '--nosuffix', '-o', 'vg_free', 'ubuntu-vg')))
        needed = max(0, target - current)
    else:
        raise ValueError('Refusing to modify storage other than the managed image or ubuntu-vg/metanode_data')
    filesystem = fs_size()
    if not args.clean and target < max(current, filesystem):
        raise ValueError('Shrinking with data is not supported; retain the current size or use a full storage clean')
    changed = args.clean or target > current or filesystem < target
    if changed and needed > available:
        raise ValueError(f'Insufficient space: need {needed} bytes, available {available}')
    bind_targets = []
    if args.clean:
        if stored - active:
            raise ValueError(f'Shared storage also contains nodes {sorted(stored - active)}; include them in clean')
        if any(not re.fullmatch(r'node-\d+', p.name) or p.is_symlink() or not p.is_dir() for p in entries):
            raise ValueError('Unknown entries in snapshot storage; refusing to format')
        expected = {str(Path(args.install_dir) / f'node-{n}' / 'data') for n in active}
        for row in rows:
            if row['maj:min'] == root['maj:min'] and row['target'] != str(MOUNT):
                if row['target'] not in expected:
                    raise ValueError(f"Storage is also mounted at {row['target']}; refusing to format")
                bind_targets.append(row['target'])
            elif row['target'].startswith(str(MOUNT) + '/') or any(row['target'].startswith(p + '/') for p in expected):
                raise ValueError('Nested mount detected; refusing to format')
    return dict(kind=kind, device=device, target=target, current=current,
                changed=changed, bind_targets=bind_targets)


def apply(args, state):
    if not state or not state['changed']:
        return
    target, device = state['target'], state['device']
    if args.clean:
        # A busy filesystem must fail here; never use lazy/forced unmount before mkfs.
        for path in state['bind_targets']:
            run('umount', path)
        run('umount', str(MOUNT))
        if state['kind'] == 'loop':
            if device:
                # umount may have automatically detached autoclear loop devices.
                subprocess.run(['losetup', '-d', device], capture_output=True)
            attached = run('losetup', '-j', str(IMAGE))
            for dev in re.findall(r'/dev/loop\d+', attached):
                subprocess.run(['losetup', '-d', dev], capture_output=True)
            if run('losetup', '-j', str(IMAGE)):
                raise ValueError('Loop image is still attached; refusing to truncate')
            run('truncate', '-s', '0', str(IMAGE))
            run('truncate', '-s', str(target), str(IMAGE))
            run('mkfs.btrfs', '-f', str(IMAGE))
            run('mount', '-t', 'btrfs', '-o', 'loop', str(IMAGE), str(MOUNT))
        else:
            if target != state['current']:
                run('lvresize', '--yes', '--force', '-L', f'{target}B', str(LV))
            run('mkfs.btrfs', '-f', str(LV))
            run('mount', '-t', 'btrfs', str(LV), str(MOUNT))
    else:
        if target > state['current']:
            if state['kind'] == 'loop':
                run('truncate', '-s', str(target), str(IMAGE))
            else:
                run('lvextend', '-L', f'{target}B', str(LV))
        if state['kind'] == 'loop':
            run('losetup', '-c', device)
        run('btrfs', 'filesystem', 'resize', 'max', str(MOUNT))
    if fs_size() != target:
        raise ValueError('BTRFS size verification failed; deployment stopped')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--size', required=True)
    parser.add_argument('--nodes', required=True)
    parser.add_argument('--snapshot-nodes', default='')
    parser.add_argument('--install-dir', default='/opt/metanode')
    parser.add_argument('--clean', action='store_true')
    parser.add_argument('--check', action='store_true')
    parser.add_argument('--verify', action='store_true')
    args = parser.parse_args()
    try:
        if args.verify:
            args.clean = False
            state = plan(args)
            if state is None or state['changed']:
                raise ValueError('Mounted managed BTRFS does not have the requested size')
        elif args.check:
            state = plan(args)
        else:
            with open('/run/lock/metanode-snapshot-storage.lock', 'w') as lock:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                state = plan(args)
                apply(args, state)
        print(json.dumps({'changed': bool(state and state['changed']), 'check': args.check,
                          'target_bytes': state['target'] if state else None}))
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        detail = error.stderr if isinstance(error, subprocess.CalledProcessError) else str(error)
        parser.exit(1, f'Snapshot storage: {detail}\n')


if __name__ == '__main__':
    main()
