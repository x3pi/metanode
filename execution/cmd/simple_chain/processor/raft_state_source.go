package processor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	mt_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// raftStateSource makes a consistent snapshot of this node's database for a new replica (plan C4, state transfer
// over the network). It uses the same atomic primitives as the snapshot manager — Pebble checkpoints, NOMT native
// snapshots, reflink copies — with execution paused only for as long as they take (short on a reflink-capable
// filesystem), then resumes and lays the result out as a database root a node can start from.
type raftStateSource struct {
	bp        *BlockProcessor
	rootPath  string
	allowCopy bool // accept a full (non-reflink) copy, i.e. a long execution pause
	reflink   bool // decided per snapshot
}

// NewRaftStateSource returns the raftfeed.StateSource of this node; rootPath is databases.root_path.
func (bp *BlockProcessor) NewRaftStateSource(rootPath string, allowCopy bool) raftfeed.StateSource {
	return &raftStateSource{bp: bp, rootPath: rootPath, allowCopy: allowCopy}
}

// consensusDirs are the snapshot directories that live under <root>/consensus (the rest of a snapshot is under
// <root>/history). Same mapping as restore_node.sh.
var consensusDirs = map[string]bool{
	"account_state": true, "stake_db": true, "trie_database": true, "smart_contract_code": true,
	"smart_contract_storage": true, "backup_device_key_storage": true, "nomt_db": true, "xapian": true,
}

func (s *raftStateSource) Snapshot(dst string) (raftfeed.StateMeta, error) {
	base := filepath.Dir(dst)
	if err := sameFilesystem(s.rootPath, base); err != nil {
		return raftfeed.StateMeta{}, err
	}
	s.reflink = requireReflink(base) == nil
	if !s.reflink {
		if !s.allowCopy {
			return raftfeed.StateMeta{}, errReflinkNeeded
		}
		logger.Warn("📦 [RAFT-STATE] no reflink on this filesystem: the snapshot is a full copy and execution stays paused for its whole duration (state_transfer_allow_copy)")
	}
	tmp := dst + ".snap"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return raftfeed.StateMeta{}, err
	}
	defer os.RemoveAll(tmp)

	meta, err := s.capture(tmp)
	if err != nil {
		return raftfeed.StateMeta{}, err
	}
	if err := mapSnapshotLayout(tmp, dst); err != nil {
		_ = os.RemoveAll(dst)
		return raftfeed.StateMeta{}, err
	}
	if err := verifyCoverage(s.rootPath, dst); err != nil {
		_ = os.RemoveAll(dst)
		return raftfeed.StateMeta{}, err
	}
	return meta, nil
}

// capture pauses execution, makes everything durable, notes the block, takes the atomic copies into tmp and
// resumes. Whatever happens, execution is resumed before it returns.
func (s *raftStateSource) capture(tmp string) (raftfeed.StateMeta, error) {
	bp := s.bp
	start := time.Now()
	if err := bp.PauseExecution(); err != nil {
		return raftfeed.StateMeta{}, fmt.Errorf("cannot pause execution: %w", err)
	}
	resumed := false
	resume := func() {
		if !resumed {
			bp.ResumeExecution()
			resumed = true
			logger.Info("📦 [RAFT-STATE] execution resumed after %v", time.Since(start))
		}
	}
	defer resume()

	sm := bp.chainState.GetStorageManager()
	if sm == nil {
		return raftfeed.StateMeta{}, errors.New("no storage manager")
	}
	mvm.CommitAllXapian()
	if err := sm.FlushAll(); err != nil {
		return raftfeed.StateMeta{}, fmt.Errorf("flush: %w", err)
	}
	num := storage.GetLastBlockNumber()
	meta := raftfeed.StateMeta{LastBlock: num, GEI: storage.GetLastGlobalExecIndex()}
	if h, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(num); ok {
		meta.BlockHash = h.Hex()
	} else if num > 0 {
		return raftfeed.StateMeta{}, fmt.Errorf("no block hash for block %d", num)
	}
	if err := sm.CheckpointAll(tmp); err != nil {
		return raftfeed.StateMeta{}, fmt.Errorf("pebble checkpoint: %w", err)
	}
	if err := bp.chainState.CheckpointChangelogs(tmp); err != nil {
		return raftfeed.StateMeta{}, fmt.Errorf("changelog checkpoint: %w", err)
	}
	if err := mt_trie.SnapshotAllNomtDBs(tmp, s.reflink); err != nil {
		return raftfeed.StateMeta{}, fmt.Errorf("nomt snapshot: %w", err)
	}
	if x := filepath.Join(s.rootPath, "consensus", "xapian"); dirExists(x) {
		mode := "--reflink=always"
		if !s.reflink {
			mode = "--reflink=auto"
		}
		if out, err := exec.Command("cp", "-a", mode, x, filepath.Join(tmp, "xapian")).CombinedOutput(); err != nil {
			return raftfeed.StateMeta{}, fmt.Errorf("xapian reflink copy: %v: %s", err, out)
		}
	}
	resume()
	return meta, nil
}

// mapSnapshotLayout arranges a snapshot directory (checkpoint layout) as a database root: history/* stays under
// <root>/history, the consensus databases go under <root>/consensus, back_up is not part of the root. An entry it
// does not know is an error — silently dropping data would produce a replica that starts and is wrong.
func mapSnapshotLayout(snap, root string) error {
	if err := os.MkdirAll(filepath.Join(root, "consensus"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "history"), 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(snap)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		switch {
		case name == "back_up":
			continue
		case name == "history":
			kids, err := os.ReadDir(filepath.Join(snap, "history"))
			if err != nil {
				return err
			}
			for _, k := range kids {
				if err := os.Rename(filepath.Join(snap, "history", k.Name()), filepath.Join(root, "history", k.Name())); err != nil {
					return err
				}
			}
		case consensusDirs[name]:
			if err := os.Rename(filepath.Join(snap, name), filepath.Join(root, "consensus", name)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("snapshot contains %q, which has no place in the database root layout (unified shared DB not supported for state transfer?)", name)
		}
	}
	return nil
}

// verifyCoverage refuses a snapshot that lacks a database directory the live root has: a replica started from it
// would come up with a hole in its state.
func verifyCoverage(liveRoot, snapRoot string) error {
	var missing []string
	for _, group := range []string{"consensus", "history"} {
		entries, err := os.ReadDir(filepath.Join(liveRoot, group))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			if !dirExists(filepath.Join(snapRoot, group, e.Name())) {
				missing = append(missing, group+"/"+e.Name())
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("the snapshot lacks %s, which the live database root has", strings.Join(missing, ", "))
	}
	return nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// sameFilesystem: reflink copies and renames between the live root and the staging area need one filesystem.
func sameFilesystem(a, b string) error {
	var sa, sb syscall.Stat_t
	if err := syscall.Stat(a, &sa); err != nil {
		return fmt.Errorf("stat %s: %w", a, err)
	}
	if err := syscall.Stat(b, &sb); err != nil {
		return fmt.Errorf("stat %s: %w", b, err)
	}
	if sa.Dev != sb.Dev {
		return fmt.Errorf("state transfer staging area %s is not on the same filesystem as the database root %s", b, a)
	}
	return nil
}

// requireReflink refuses when the filesystem cannot clone files instantly: a full copy would keep execution paused
// for as long as it takes to copy the whole database.
func requireReflink(dir string) error {
	src := filepath.Join(dir, ".reflink_probe_src")
	dst := filepath.Join(dir, ".reflink_probe_dst")
	defer os.Remove(src)
	defer os.Remove(dst)
	if err := os.WriteFile(src, []byte("probe"), 0o644); err != nil {
		return err
	}
	if err := exec.Command("cp", "--reflink=always", src, dst).Run(); err != nil {
		return errReflinkNeeded
	}
	return nil
}

var errReflinkNeeded = errors.New("state transfer needs a reflink-capable filesystem (btrfs/xfs): a plain copy would keep execution paused for the whole copy; set raft.state_transfer_allow_copy to accept that pause, or use `prepare-replica` from a stopped peer")
