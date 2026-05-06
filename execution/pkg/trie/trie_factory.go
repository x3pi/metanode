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
			type prefixGetter interface {
				GetPrefix() []byte
			}

			pg, isPathGetter := db.(pathGetter)
			if isPathGetter && pg.GetBackupPath() != "" {
				// Use the base (folder) name of the backup path rather than the full directory path.
				// This ensures Sub and Master nodes use identical underlying NOMT namespaces 
				// even if their backup/storage root paths differ.
				namespace = filepath.Base(pg.GetBackupPath())
			} else if prg, ok := db.(prefixGetter); ok {
				prefixBytes := prg.GetPrefix()
				prefixStr := string(prefixBytes)

				// Determine namespace based on the global SharedDB prefix.
				if prefixStr == "ac:" {
					namespace = "account_state"
				} else if prefixStr == "sc:" {
					namespace = "smart_contract_storage"
				} else if prefixStr == "st:" {
					namespace = "stake_db"
				} else if prefixStr == "rc:" {
					namespace = "receipts"
				} else if prefixStr == "tx:" {
					namespace = "transaction_state"
				} else if prefixStr == "bl:" {
					namespace = "blocks"
				} else if len(prefixBytes) == 20 {
					// 20-byte prefix means it's a Smart Contract's PrefixedStorage wrapper.
					namespace = "smart_contract_storage"
				} else {
					namespace = fmt.Sprintf("%T_%x", db, prefixBytes)
				}
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
				prefixBytes := pg.GetPrefix()
				// Only append to keyPrefix if it's a Smart Contract address (20 bytes).
				// We DO NOT want to append global prefixes like "ac:" or "sc:",
				// because global domains are already isolated by the `namespace` Handle.
				if len(prefixBytes) == 20 {
					keyPrefix = namespace + "_" + hex.EncodeToString(prefixBytes)
				}
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
// CRITICAL: Must call handle.Close() to flush pending data via nomt_close FFI.
func CloseNomtDB() {
	globalNomtHandlesMu.Lock()
	defer globalNomtHandlesMu.Unlock()
	
	for namespace, handle := range globalNomtHandles {
		logger.Info("[TRIE] Closing NOMT handle for namespace: %s", namespace)
		handle.Close() // Properly flush and close via FFI nomt_close()
	}
	globalNomtHandles = make(map[string]*nomt_ffi.Handle)
	logger.Info("[TRIE] All NOMT handles closed")
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

	// Ensure the database directory exists before calling FFI.
	// This prevents "os error 2" (No such file or directory) during snapshot restore
	// if wget failed to download empty NOMT directories.
	if err := os.MkdirAll(dbPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create NOMT database directory at %s: %w", dbPath, err)
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
// Uses the Checkpoint API to copy files while the database remains OPEN —
// no Close/Reopen needed, eliminating ~700ms overhead and os error 11 lock issues.
//
// PREREQUISITE: The caller (SnapshotManager) MUST have already called:
//   PauseExecution() + WaitForPersistence() to ensure no active sessions or pending I/O.
func SnapshotAllNomtDBs(destBasePath string, useReflink bool) error {
	globalNomtHandlesMu.Lock()
	// Create a stable snapshot of handles to iterate
	handlesList := make([]struct {
		namespace string
		handle    *nomt_ffi.Handle
	}, 0, len(globalNomtHandles))
	for ns, handle := range globalNomtHandles {
		handlesList = append(handlesList, struct {
			namespace string
			handle    *nomt_ffi.Handle
		}{ns, handle})
	}
	globalNomtHandlesMu.Unlock()

	if len(handlesList) == 0 {
		return nil
	}

	nomtDestBase := filepath.Join(destBasePath, "nomt_db")
	if err := os.MkdirAll(nomtDestBase, 0755); err != nil {
		return fmt.Errorf("failed to create NOMT destination base directory %s: %w", nomtDestBase, err)
	}

	for _, entry := range handlesList {
		destPath := filepath.Join(nomtDestBase, entry.namespace)

		// ═══════════════════════════════════════════════════════════════
		// NON-BLOCKING CHECKPOINT IS DISABLED due to hard-link corruption.
		// Hard-linking memory-mapped files that are modified in-place 
		// destroys snapshot atomicity. We MUST use CloseForSnapshot 
		// and reflink/copy.
		// ═══════════════════════════════════════════════════════════════
		logger.Info("📸 [TRIE] Snapshotting NOMT namespace %s (Close+Copy API): %s -> %s", entry.namespace, entry.handle.GetPath(), destPath)
		start := time.Now()

		entry.handle.CloseForSnapshot()

		if mkdirErr := os.MkdirAll(destPath, 0755); mkdirErr != nil {
			return fmt.Errorf("failed to create dest dir for NOMT snapshot: %w", mkdirErr)
		}

		var copyErr error
		if useReflink {
			copyErr = copyDirReflink(entry.handle.GetPath(), destPath)
		} else {
			copyErr = copyDirFallback(entry.handle.GetPath(), destPath)
		}

		if reopenErr := entry.handle.ReopenAfterSnapshot(); reopenErr != nil {
			return fmt.Errorf("CRITICAL: failed to reopen NOMT namespace %s after snapshot: %w", entry.namespace, reopenErr)
		}

		if copyErr != nil {
			return fmt.Errorf("NOMT directory copy failed for %s: %w", entry.namespace, copyErr)
		}

		// CRITICAL FIX: The NOMT Checkpoint API only copies the B-Tree data files,
		// but the knownKeys registry is stored in a separate file by nomt_state_trie.go.
		// If we don't explicitly copy this file, GetAll() will return empty arrays after restore.
		srcRegistryPath := filepath.Join(filepath.Dir(entry.handle.GetPath()), "nomt_registry_"+entry.namespace+".bin")
		dstRegistryPath := filepath.Join(nomtDestBase, "nomt_registry_"+entry.namespace+".bin")
		if data, err := os.ReadFile(srcRegistryPath); err == nil {
			if writeErr := os.WriteFile(dstRegistryPath, data, 0644); writeErr != nil {
				logger.Warn("📸 [TRIE] Failed to copy registry file %s: %v", entry.namespace, writeErr)
			} else {
				logger.Info("✅ [TRIE] Copied registry file for namespace: %s", entry.namespace)
			}
		}

		logger.Info("✅ [TRIE] NOMT checkpoint %s completed in %v", entry.namespace, time.Since(start))
	}

	return nil
}

// copyDirReflink performs a fast O(1) Copy-On-Write clone using cp --reflink=auto
func copyDirReflink(src, dst string) error {
	// The trailing slash in src/. is important for cp to copy contents into dst
	cmd := exec.Command("cp", "-a", "--reflink=auto", src+"/.", dst+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cp reflink failed: %v, output: %s", err, string(out))
	}
	return nil
}

// GetNomtHandleRoot returns the current NOMT Merkle root for a given namespace.
// Returns zero hash and false if the namespace hasn't been initialized or backend is not NOMT.
// This is used for diagnostic verification after STARTUP-SYNC batch applies.
func GetNomtHandleRoot(namespace string) (e_common.Hash, bool) {
	if globalStateBackend != BackendNOMT {
		return e_common.Hash{}, false
	}

	globalNomtHandlesMu.Lock()
	handle, exists := globalNomtHandles[namespace]
	globalNomtHandlesMu.Unlock()

	if !exists || handle == nil {
		return e_common.Hash{}, false
	}

	rootBytes, err := handle.Root()
	if err != nil {
		return e_common.Hash{}, false
	}

	return e_common.BytesToHash(rootBytes[:]), true
}

// copyDirFallback performs a standard copy when reflink is not supported
func copyDirFallback(src, dst string) error {
	cmd := exec.Command("cp", "-a", src+"/.", dst+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("cp fallback failed: %v, output: %s", err, string(out))
	}
	return nil
}
