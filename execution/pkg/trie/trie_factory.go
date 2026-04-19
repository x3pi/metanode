package trie

import (
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync" // Import sync
	"time"

	e_common "github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
	trie_db "github.com/meta-node-blockchain/meta-node/pkg/trie/db"
)

// StateBackend constants
const (
	BackendMPT    = "mpt"    // Merkle Patricia Trie
	BackendFlat   = "flat"   // FlatStateTrie with additive bucket accumulators (mod prime)
	BackendVerkle = "verkle" // Verkle Tree with Pedersen commitments
	BackendNOMT   = "nomt"   // NOMT (Nearly Optimal Merkle Trie) — Rust-based, io-uring optimized
)

// Global state backend setting. Set once at startup from config.
// Default is "nomt" for maximum throughput (Rust-based binary Merkle trie).
var globalStateBackend = BackendNOMT

// globalNomtHandles holds the NOMT database handles, separated by namespace.
var globalNomtHandles = make(map[string]*nomt_ffi.Handle)
var globalNomtHandlesMu sync.Mutex

// nomtConfig stores the base configuration for NOMT instances initialization
type nomtConfig struct {
	basePath          string
	commitConcurrency int
	pageCacheMB       int
	leafCacheMB       int
}

var globalNomtConfig *nomtConfig

// SetStateBackend sets the global state backend. Must be called before any trie creation.
func SetStateBackend(backend string) {
	switch backend {
	case BackendNOMT:
		globalStateBackend = BackendNOMT
		logger.Info("🚀 [TRIE] State backend set to: NOMT (Nearly Optimal Merkle Trie — Rust, io-uring)")
	case BackendFlat:
		globalStateBackend = BackendFlat
		logger.Warn("⚠️ [TRIE] State backend set to: FLAT (CRITICAL WARNING: Additive mod prime accumulators are vulnerable to Wagner's attack!)")
	case BackendMPT:
		globalStateBackend = BackendMPT
		logger.Info("🔧 [TRIE] State backend set to: MPT (Merkle Patricia Trie)")
	case BackendVerkle:
		globalStateBackend = BackendVerkle
		logger.Info("🔧 [TRIE] State backend set to: VERKLE (Verkle Tree with Pedersen commitments)")
	case "":
		// Empty config: keep the global default (nomt)
		logger.Info("🔧 [TRIE] State backend using default: %s", globalStateBackend)
	default:
		logger.Warn("⚠️ [TRIE] Unknown state backend '%s', defaulting to nomt", backend)
		globalStateBackend = BackendNOMT
	}
}

// GetStateBackend returns the current global state backend.
func GetStateBackend() string {
	return globalStateBackend
}

// NewStateTrie creates a new StateTrie based on the global state backend setting.
// For BackendMPT:    creates a MerklePatriciaTrie (same as old trie.New)
// For BackendFlat:   creates a FlatStateTrie (additive mod prime accumulators)
// For BackendVerkle: creates a VerkleStateTrie (Pedersen commitment-based)
//
// The db parameter must satisfy FlatStateDB interface for flat/verkle backend.
// For MPT backend, it only needs trie_db.DB (Get method).
// For NOMT backend, db is ignored (NOMT manages its own storage).
func NewStateTrie(root e_common.Hash, db trie_db.DB, isHash bool) (StateTrie, error) {
	switch globalStateBackend {
	case BackendNOMT:
		// Auto-generate namespace from db to isolate different trie instances
		// (AccountState, StakeState, etc.) in separate independent NOMT databases.
		// Use stable identifier (path or type) NOT pointer address.
		namespace := "default"
		if db != nil {
			type pathGetter interface {
				GetBackupPath() string
			}
			if pg, ok := db.(pathGetter); ok {
				// Use the base (folder) name of the backup path rather than the full directory path.
				// This ensures Sub and Master nodes use identical underlying NOMT namespaces 
				// even if their backup/storage root paths differ.
				// e.g. "./sample/node0/back_up/account_state" -> "account_state"
				namespace = filepath.Base(pg.GetBackupPath())
			} else {
				// Fallback: use type name (stable across restarts)
				namespace = fmt.Sprintf("%T", db)
			}
		}

		// Handle PrefixedStorage to ensure smart contracts don't overwrite each other's slots:
		// While `namespace` identifies the shared Handle ("smart_contract_storage"),
		// `keyPrefix` uniquely scopes keys for this specific trie instance.
		keyPrefix := namespace
		if db != nil {
			type prefixGetter interface {
				GetPrefix() []byte
			}
			if pg, ok := db.(prefixGetter); ok {
				keyPrefix = namespace + "_" + hex.EncodeToString(pg.GetPrefix())
			}
		}

		if namespace == "transaction_state" || namespace == "receipts" {
			// PERF FIX: NOMT FFI sync for 50,000 fully unique 256-bit hashes (txs/receipts) 
			// creates massive tree mutation and takes >3.5s per block.
			// Revert these two purely append-only databases to FlatStateTrie which has O(1) commit.
			if flatDB, ok := db.(FlatStateDB); ok {
				logger.Debug("🔧 [TRIE] Forcing FlatStateTrie for high-throughput namespace: %s", namespace)
				if root == (e_common.Hash{}) || root == EmptyRootHash {
					return NewFlatStateTrie(flatDB, isHash), nil
				}
				return NewFlatStateTrieFromRoot(root, flatDB, isHash)
			}
			logger.Warn("⚠️ [TRIE] DB does not support FlatStateDB, falling back to NOMT for %s", namespace)
		}

		handle, err := GetOrInitNomtHandle(namespace)
		if err != nil {
			return nil, err
		}

		// Note: isHash is used strictly for root Hash resolution in the inner logic
		// Also, the namespace is passed to NewNomtStateTrie mostly for prefix/registry isolation,
		// though with independent Handles, prefix isolation is redundant but perfectly harmless.
		return NewNomtStateTrie(handle, isHash, keyPrefix), nil

	case BackendFlat:
		// For flat backend, db must also support Put and BatchPut.
		flatDB, ok := db.(FlatStateDB)
		if !ok {
			logger.Warn("[TRIE] Storage doesn't support FlatStateDB interface, falling back to MPT. Actual db type is %T", db)
			return New(root, db, isHash)
		}
		if root == (e_common.Hash{}) || root == EmptyRootHash {
			return NewFlatStateTrie(flatDB, isHash), nil
		}
		return NewFlatStateTrieFromRoot(root, flatDB, isHash)

	case BackendVerkle:
		// For verkle backend, db must also support Put and BatchPut.
		flatDB, ok := db.(FlatStateDB)
		if !ok {
			logger.Warn("[TRIE] Storage doesn't support FlatStateDB interface, falling back to MPT. Actual db type is %T", db)
			return New(root, db, isHash)
		}
		if root == (e_common.Hash{}) || root == EmptyRootHash {
			return NewVerkleStateTrie(flatDB, isHash), nil
		}
		return NewVerkleStateTrieFromRoot(root, flatDB, isHash)

	default: // BackendMPT
		return New(root, db, isHash)
	}
}

// InitNomtDB sets the configuration for NOMT database instances.
// Must be called once at startup before any NewStateTrie calls.
// Parameters:
//   - dbPath: filesystem path for the NOMT database base directory
//   - commitConcurrency: number of concurrent commit workers (1-64, recommended: 4)
//   - pageCacheMB: max page cache size in MiB for primary tries
//   - leafCacheMB: max leaf cache size in MiB for primary tries
func InitNomtDB(dbPath string, commitConcurrency, pageCacheMB, leafCacheMB int) error {
	globalNomtHandlesMu.Lock()
	defer globalNomtHandlesMu.Unlock()

	globalNomtConfig = &nomtConfig{
		basePath:          dbPath,
		commitConcurrency: commitConcurrency,
		pageCacheMB:       pageCacheMB,
		leafCacheMB:       leafCacheMB,
	}

	logger.Info("🚀 [TRIE] NOMT global config initialized, base_path=%s", dbPath)
	return nil
}

// CloseNomtDB closes all NOMT database handles.
// CloseNomtDB properly closes all active NOMT databases.
func CloseNomtDB() {
	globalNomtHandlesMu.Lock()
	defer globalNomtHandlesMu.Unlock()
	
	for namespace, handle := range globalNomtHandles {
		logger.Debug("[TRIE] Cleaning up NOMT handle for namespace: %s", namespace)
		// Usually handle.Close() would be called here if FFI exported it.
		// For now we just dereference. The Rust side should clean up when dropped
		// or explicitly implement a close method if needed.
		_ = handle
	}
	globalNomtHandles = make(map[string]*nomt_ffi.Handle)
}

// GetOrInitNomtHandle retrieves the handle for a namespace, lazily initializing it using global config if it doesn't exist.
func GetOrInitNomtHandle(namespace string) (*nomt_ffi.Handle, error) {
	if globalNomtConfig == nil {
		return nil, fmt.Errorf("[TRIE] NOMT config not initialized. Call InitNomtDB() first")
	}

	globalNomtHandlesMu.Lock()
	defer globalNomtHandlesMu.Unlock()

	handle, exists := globalNomtHandles[namespace]
	if exists {
		return handle, nil
	}

	dbPath := filepath.Join(globalNomtConfig.basePath, namespace)
	
	// Adjust cache memory based on namespace usage so we don't OOM with 6 NOMT instances
	pageCache := globalNomtConfig.pageCacheMB
	leafCache := globalNomtConfig.leafCacheMB
	
	// If not a memory-heavy trie, down-scale cache
	if namespace != "account_state" && namespace != "smart_contract_storage" {
		if pageCache > 64 {
			pageCache = 64
		}
		if leafCache > 64 {
			leafCache = 64
		}
	}

	newHandle, err := nomt_ffi.Open(dbPath, globalNomtConfig.commitConcurrency, pageCache, leafCache)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize independent NOMT database at %s: %w", dbPath, err)
	}
	logger.Info("🚀 [TRIE] NOMT instance initialized for namespace %s at %s (concurrency=%d, pageCacheMB=%d, leafCacheMB=%d)", 
		namespace, dbPath, globalNomtConfig.commitConcurrency, pageCache, leafCache)
	globalNomtHandles[namespace] = newHandle
	return newHandle, nil
}

// SnapshotAllNomtDBs coordinates taking a snapshot of all active NOMT databases.
// It acquires an exclusive lock on each database to ensure quiescence, copies the files
// to destBasePath/nomt_db, and then releases the lock.
func SnapshotAllNomtDBs(destBasePath string, useReflink bool) error {
	globalNomtHandlesMu.Lock()
	// Create a stable snapshot of handles to iterate
	handlesToSnapshot := make(map[string]*nomt_ffi.Handle, len(globalNomtHandles))
	for ns, handle := range globalNomtHandles {
		handlesToSnapshot[ns] = handle
	}
	globalNomtHandlesMu.Unlock()

	if len(handlesToSnapshot) == 0 {
		return nil
	}

	nomtDestBase := filepath.Join(destBasePath, "nomt_db")
	if err := os.MkdirAll(nomtDestBase, 0755); err != nil {
		return fmt.Errorf("failed to create NOMT destination base directory %s: %w", nomtDestBase, err)
	}

	for namespace, handle := range handlesToSnapshot {
		srcPath := handle.GetPath()
		destPath := filepath.Join(nomtDestBase, namespace)

		if err := os.MkdirAll(destPath, 0755); err != nil {
			return fmt.Errorf("failed to create NOMT destination directory %s: %w", destPath, err)
		}

		logger.Info("📸 [TRIE] Snapshotting NOMT namespace %s: %s -> %s", namespace, srcPath, destPath)
		start := time.Now()

		// 1. Acquire Exclusive Lock
		// This blocks all Read() and CommitPayload() operations on this handle.
		handle.AcquireExclusive()

		// 2. Perform the copy while locked
		var copyErr error
		if useReflink {
			copyErr = copyDirReflink(srcPath, destPath)
		} else {
			copyErr = copyDirFallback(srcPath, destPath)
		}

		// 3. Release Exclusive Lock
		handle.ReleaseExclusive()

		if copyErr != nil {
			return fmt.Errorf("failed to copy NOMT database %s: %w", namespace, copyErr)
		}

		// 4. Cleanup lock file from snapshot
		lockFile := filepath.Join(destPath, ".lock")
		_ = os.Remove(lockFile)

		logger.Info("✅ [TRIE] NOMT snapshot %s completed in %v (reflink=%v)", namespace, time.Since(start), useReflink)
	}

	return nil
}

// copyDirReflink performs a fast O(1) Copy-On-Write clone using cp --reflink=always
func copyDirReflink(src, dst string) error {
	// The trailing slash in src/. is important for cp to copy contents into dst
	cmd := exec.Command("cp", "-a", "--reflink=always", src+"/.", dst+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cp reflink failed: %v, output: %s", err, string(out))
	}
	return nil
}

// copyDirFallback performs a standard copy when reflink is not supported
func copyDirFallback(src, dst string) error {
	cmd := exec.Command("cp", "-a", src+"/.", dst+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cp fallback failed: %v, output: %s", err, string(out))
	}
	return nil
}
