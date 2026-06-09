package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/fatal"
	"github.com/meta-node-blockchain/meta-node/pkg/filters"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction_pool"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
)

// initBlockchain initializes blockchain-related components
func (app *App) initBlockchain() error {
	logger.Info("initBlockchain started")

	var futureNomtRoot e_common.Hash
	wasCleanShutdown := app.WasCleanShutdown()

	blockDatabase := block.NewBlockDatabase(app.storageManager.GetStorageBlock())

	// Configure backup directory for lastBlock crash recovery file
	backupDir := app.config.BackupPath
	if backupDir == "" {
		backupDir = "./sample/node0/back_up"
	}
	blockDatabase.SetBackupDir(backupDir)

	app.transactionPool = transaction_pool.NewTransactionPool()

	// Set up event system
	app.eventSystem = filters.NewEventSystem()

	// Initialize last block or create genesis block

	lastBlock, err := blockDatabase.GetLastBlock()
	if err != nil {
		// ═══════════════════════════════════════════════════════════════════
		// SAFETY GUARD (Mar 2026): Prevent catastrophic genesis re-init
		//
		// ROOT CAUSE: When LazyPebbleDB data is lost on crash (lastBlockHashKey
		// not flushed to SST), GetLastBlock() fails. Previously, the code would
		// silently re-initialize genesis, DESTROYING all state (accounts,
		// contracts, stake). This is the most dangerous data loss scenario.
		//
		// FIX: Check if account_state has REAL data (SST files). Empty PebbleDB
		// directories are created by storage init even on --fresh start, so we
		// can't just check directory existence. SST files only exist when actual
		// data has been committed and flushed to disk.
		// ═══════════════════════════════════════════════════════════════════
		dataDir := app.config.Databases.RootPath
		blocksPath := dataDir + "/blocks"
		metadataPath := dataDir + "/metadata.json"
		hasExistingData := false

		// Check if any shard in blocks has SST files
		if info, statErr := os.Stat(blocksPath); statErr == nil && info.IsDir() {
			entries, _ := os.ReadDir(blocksPath)
			for _, entry := range entries {
				if !entry.IsDir() {
					continue
				}
				shardPath := blocksPath + "/" + entry.Name()
				shardEntries, _ := os.ReadDir(shardPath)
				for _, se := range shardEntries {
					if strings.HasSuffix(se.Name(), ".sst") {
						hasExistingData = true
						break
					}
				}
				if hasExistingData {
					break
				}
			}
		}
		if hasExistingData {
			if _, err := os.Stat(metadataPath); err == nil {
				logger.Warn("⚠️ GetLastBlock() failed, but metadata.json exists! Bypassing panic to recover from snapshot.")
				var md executor.SnapshotMetadata
				if metadataBytes, err := os.ReadFile(metadataPath); err == nil {
					if jsonErr := json.Unmarshal(metadataBytes, &md); jsonErr == nil {
						// Fallback: lấy stake root từ metadata hoặc NOMT database
						stakeRoot := e_common.HexToHash(md.StakeStatesRoot)
						if stakeRoot == (e_common.Hash{}) {
							// Old snapshot format: query NOMT directly
							if nomtRoot, ok := trie.GetNomtHandleRoot("stake_db"); ok {
								stakeRoot = nomtRoot
								logger.Info("📸 [SNAPSHOT] StakeStatesRoot from NOMT: %s", stakeRoot.Hex())
							}
						}

						app.startLastBlock = block.NewBlock(
							block.NewBlockHeader(e_common.HexToHash(""), md.BlockNumber, e_common.HexToHash(md.StateRoot), stakeRoot, e_common.HexToHash(""), e_common.Address{}, 0, trie.EmptyRootHash, uint64(md.Epoch), md.GlobalExecIndex),
							nil, nil,
						)
						app.startLastBlock.Header().SetCommitIndex(md.LastHandledCommitIdx)
					}
				}
				if app.startLastBlock != nil {
					storage.UpdateLastBlockNumber(app.startLastBlock.Header().BlockNumber())
					storage.UpdateLastGlobalExecIndex(app.startLastBlock.Header().GlobalExecIndex())
					app.GetAccountStateTrie(app.startLastBlock.Header().AccountStatesRoot())

					// ═══════════════════════════════════════════════════════════════
					// CRITICAL FIX: Verify and patch NOMT roots BEFORE NewChainStateWithGenesis.
					// GetAccountStateTrie() above lazily initializes the "account_state" NOMT handle.
					// We must also force "stake_db" handle init so GetNomtHandleRoot works.
					// ═══════════════════════════════════════════════════════════════
					
					// Force stake_db NOMT handle initialization by creating a temporary trie.
					// This ensures GetNomtHandleRoot("stake_db") works before NewChainStateWithGenesis.
					stakeStorage := app.storageManager.GetStorageStake()
					if _, initErr := trie.NewStateTrie(e_common.Hash{}, stakeStorage, true); initErr != nil {
						logger.Warn("⚠️ [SNAPSHOT] Failed to pre-init stake_db NOMT handle: %v", initErr)
					}

					// Now verify NOMT roots match header — BEFORE ChainState is created
					nomtAccountRoot, okAccount := trie.GetNomtHandleRoot("account_state")
					nomtStakeRoot, okStake := trie.GetNomtHandleRoot("stake_db")

					if okAccount && okStake {
						headerAccountRoot := app.startLastBlock.Header().AccountStatesRoot()
						headerStakeRoot := app.startLastBlock.Header().StakeStatesRoot()

						if nomtAccountRoot != headerAccountRoot || nomtStakeRoot != headerStakeRoot {
							logger.Warn("⚠️ [SNAPSHOT] NOMT root MISMATCH in metadata recovery path: account(nomt=%s header=%s), stake(nomt=%s header=%s). Wiping NOMT database and resetting block tip to genesis to force clean sync!",
								nomtAccountRoot.Hex()[:18], headerAccountRoot.Hex()[:18], nomtStakeRoot.Hex()[:18], headerStakeRoot.Hex()[:18])

							// Close all handles
							trie.CloseNomtDB()

							// Wipe nomt_db directory
							nomtDbDir := filepath.Join(app.config.Databases.RootPath, "consensus", "nomt_db")
							if err := os.RemoveAll(nomtDbDir); err != nil {
								logger.Error("❌ [STARTUP] Failed to wipe NOMT database directory at %s: %v", nomtDbDir, err)
							} else {
								logger.Info("✅ [STARTUP] Successfully wiped NOMT database directory at %s", nomtDbDir)
							}

							// Re-initialize startLastBlock to genesis block 0
							app.startLastBlock = block.NewBlock(
								block.NewBlockHeader(
									e_common.Hash{},
									0,
									trie.EmptyRootHash,
									e_common.Hash{},
									e_common.Hash{},
									e_common.Address{},
									app.genesis.Config.EpochTimestampMs,
									trie.EmptyRootHash,
									0,
								),
								nil,
								nil,
							)
							storage.ForceSetLastBlockNumber(0)
							storage.ForceSetLastGlobalExecIndex(0)
							storage.ForceSetLastHandledCommitIndex(0)
							storage.UpdateLastHandledCommitEpoch(0)
							storage.UpdateLastBlockNumber(0)
						}
					}

					// Now create ChainState with the CORRECTED header roots
					app.chainState, _ = blockchain.NewChainStateWithGenesis(app.storageManager, blockDatabase, app.startLastBlock.Header(), app.config, FreeFeeAddresses, &app.genesis.Config, app.config.BackupPath)

					// FORK-SAFETY: Also align epoch in the recovery path
					if app.startLastBlock.Header().Epoch() >= 0 {
						app.chainState.ForceAlignEpochFromBlockHeader(
							app.startLastBlock.Header().Epoch(),
							app.startLastBlock.Header().TimeStamp(),
							app.startLastBlock.Header().BlockNumber(),
						)
					}

					trie_database.CreateTrieDatabaseManager(app.storageManager.GetStorageDatabaseTrie(), app.chainState.GetAccountStateDB())
					blockchain.InitBlockChain(100, blockDatabase, app.storageManager)
					if app.chainState != nil {
						blockchain.GetBlockChainInstance().SetChangelogDB(app.chainState.GetChangelogDB())
					}
					goto SKIP_GENESIS
				}
			}

			logger.Error("🚨 [FATAL] GetLastBlock() failed but database (blocks/nomt_db) exists!")
			logger.Error("🚨 [FATAL] Data path: %s", dataDir)
			logger.Error("🚨 [FATAL] Error: %v", err)
			logger.Error("🚨 [FATAL] This indicates corrupted block database (lastBlockHashKey lost).")
			logger.Error("🚨 [FATAL] REFUSING to re-initialize genesis to prevent wiping all state data.")
			return fmt.Errorf("CORRUPTED BLOCK DATABASE: lastBlock not found but data exists at %s. Error: %v", dataDir, err)
		}
		
		fmt.Printf("No existing block found (fresh start), initializing genesis block\n")
		// No data directories → genuine fresh start
		logger.Info("No existing block found (fresh start), initializing genesis block")
		if initErr := app.initGenesisBlock(blockDatabase); initErr != nil {
			logger.Error("initGenesisBlock failed: %v", initErr)
			return initErr
		}
		// Initialize trie database manager
		trie_database.CreateTrieDatabaseManager(
			app.storageManager.GetStorageDatabaseTrie(),
			app.chainState.GetAccountStateDB())

		// Initialize blockchain
		blockchain.InitBlockChain(100, blockDatabase, app.storageManager)
		if app.chainState != nil {
			blockchain.GetBlockChainInstance().SetChangelogDB(app.chainState.GetChangelogDB())
		}
		blockchain.GetBlockChainInstance().SetBlockNumberToHash(uint64(app.startLastBlock.Header().BlockNumber()), app.startLastBlock.Header().Hash())
		blockchain.GetBlockChainInstance().Commit()
		logger.Info("lastblock header 1: %v", app.startLastBlock.Header())

		// Verify validators after genesis initialization
		allValidators, postErr := app.chainState.GetStakeStateDB().GetAllValidators()
		if postErr != nil {
			logger.Error("Failed to get validators after genesis: %v", postErr)
		} else {
			logger.Info("Post-genesis: Found %d validators in stake state DB", len(allValidators))
			for i, val := range allValidators {
				stake := val.TotalStakedAmount()
				logger.Info("  Validator %d: %s (name=%s, stake=%s)",
					i+1, val.Address().Hex(), val.Name(), stake.String())
			}
			if len(allValidators) == 0 {
				logger.Warn("No validators found after genesis initialization!")
			}
		}

	} else {
		// Use existing last block
		logger.Info("Using existing block (not init genesis)")
		app.startLastBlock = lastBlock
		logger.Info("lastblock header 2: %v (using existing block)", app.startLastBlock.Header())
		storage.UpdateLastBlockNumber(app.startLastBlock.Header().BlockNumber())

		// ─── Initialize LastGlobalExecIndex from block header ──────────
		// CRITICAL FOR SNAPSHOT RESTORE: GEI is normally persisted in backup_db,
		// but backup_db is NOT included in snapshots (it lives in BackupPath,
		// not RootPath). After a snapshot restore, backup_db is empty → GEI=0.
		// The BlockHeader stores GlobalExecIndex reliably, so we use it as the
		// authoritative source on startup. This guarantees Rust receives the
		// correct GEI during initialization and can resume epoch transitions.
		headerGEI := app.startLastBlock.Header().GlobalExecIndex()
		
		// Attempt to load from BackupDb as well
		var backupGEI uint64 = 0
		if app.storageManager != nil && app.storageManager.GetStorageBackupDb() != nil {
			if geiBytes, err := app.storageManager.GetStorageBackupDb().Get(storage.LastGlobalExecIndexHashKey.Bytes()); err == nil {
				if parsedGei, err := utils.BytesToUint64(geiBytes); err == nil {
					backupGEI = parsedGei
				}
			}
		}

		targetGEI := headerGEI
		if backupGEI > headerGEI {
			targetGEI = backupGEI
			logger.Info("✅ [STARTUP] Initialized LastGlobalExecIndex from BackupDb: %d (higher than header: %d). This accounts for empty commits.", targetGEI, headerGEI)
		} else if headerGEI > 0 {
			logger.Info("✅ [STARTUP] Initialized LastGlobalExecIndex from last block header: gei=%d (block=#%d)",
				headerGEI, app.startLastBlock.Header().BlockNumber())
		} else {
			// Fallback for legacy blocks that don't have GEI in header
			logger.Info("ℹ️  [STARTUP] Last block header has GlobalExecIndex=0 (legacy or genesis). GEI will be set by Rust on first commit.")
		}

		if targetGEI > 0 {
			storage.UpdateLastGlobalExecIndex(targetGEI)
		}

		// ─── Initialize LastExecutedCommitHash from BackupDb ──────────
		if app.storageManager != nil && app.storageManager.GetStorageBackupDb() != nil {
			if hashBytes, err := app.storageManager.GetStorageBackupDb().Get(storage.LastExecutedCommitHashKey.Bytes()); err == nil && len(hashBytes) > 0 {
				storage.UpdateLastExecutedCommitHash(hashBytes)
				logger.Info("✅ [STARTUP] Loaded LastExecutedCommitHash from BackupDb: %x", hashBytes)
			} else {
				// Use zero hash if not found (genesis or first upgrade)
				storage.UpdateLastExecutedCommitHash(make([]byte, 32))
				logger.Info("ℹ️  [STARTUP] Defaulted LastExecutedCommitHash to zero hash (not found in BackupDb)")
			}
		}

		// ─── Initialize LastHandledCommitIndex (EPOCH-AWARE) ──────────
		// FORK-SAFETY ROOT FIX: lastHandledCommitIndex is epoch-scoped.
		// On restart, we MUST validate that the persisted commit_index belongs
		// to the CURRENT epoch. If it belongs to a previous epoch, it's stale
		// and must be reset to 0 — using it would cause Rust to skip all
		// commits in the current epoch, leading to a fork.
		headerCommitIndex := uint32(app.startLastBlock.Header().CommitIndex())
		headerEpoch := app.startLastBlock.Header().Epoch()

		var backupCommitIndex uint32 = 0
		var backupCommitEpoch uint64 = 0
		if app.storageManager != nil && app.storageManager.GetStorageBackupDb() != nil {
			if commitIdxBytes, err := app.storageManager.GetStorageBackupDb().Get(storage.LastHandledCommitIndexHashKey.Bytes()); err == nil && len(commitIdxBytes) > 0 {
				if parsedIdx, err := utils.BytesToUint32(commitIdxBytes); err == nil {
					backupCommitIndex = parsedIdx
				}
			}
			if epochBytes, err := app.storageManager.GetStorageBackupDb().Get(storage.LastHandledCommitEpochHashKey.Bytes()); err == nil && len(epochBytes) > 0 {
				if parsedEpoch, err := utils.BytesToUint64(epochBytes); err == nil {
					backupCommitEpoch = parsedEpoch
				}
			}
		}

		// EPOCH VALIDATION: The commit_index is only valid if it belongs to the current epoch
		targetCommitIndex := uint32(0)
		if backupCommitEpoch == headerEpoch && backupCommitIndex > 0 {
			// Backup is from the same epoch — safe to use
			if backupCommitIndex > headerCommitIndex {
				targetCommitIndex = backupCommitIndex
				logger.Info("✅ [STARTUP] Initialized LastHandledCommitIndex from BackupDb: %d (epoch=%d, higher than header: %d). This prevents empty commit replays.", targetCommitIndex, backupCommitEpoch, headerCommitIndex)
			} else {
				targetCommitIndex = headerCommitIndex
				logger.Info("✅ [STARTUP] Initialized LastHandledCommitIndex from last block header: %d (epoch=%d)", headerCommitIndex, headerEpoch)
			}
		} else if backupCommitEpoch > 0 && backupCommitEpoch != headerEpoch {
			// STALE: Backup is from a DIFFERENT epoch — MUST reset to 0
			logger.Warn("🚨 [STARTUP] EPOCH MISMATCH: BackupDb has lastHandledCommitIndex=%d from epoch=%d, but current epoch=%d. RESETTING to 0 to prevent cross-epoch fork!",
				backupCommitIndex, backupCommitEpoch, headerEpoch)
			targetCommitIndex = 0
		} else if headerCommitIndex > 0 {
			// No epoch info in backup (legacy/first upgrade) — fall back to header
			logger.Info("✅ [STARTUP] Initialized LastHandledCommitIndex from last block header: %d (no epoch tracking in BackupDb)", headerCommitIndex)
			targetCommitIndex = headerCommitIndex
		} else {
			logger.Info("ℹ️  [STARTUP] Defaulted LastHandledCommitIndex to 0 (not found in header)")
		}

		if targetCommitIndex > 0 {
			storage.UpdateLastHandledCommitIndex(targetCommitIndex)
		} else {
			// Use ForceSet to allow reset to 0 (UpdateLastHandledCommitIndex has monotonic guard)
			storage.ForceSetLastHandledCommitIndex(0)
		}
		// Always persist the current epoch for next restart
		storage.UpdateLastHandledCommitEpoch(headerEpoch)

		// ─── Startup State Sync Logging ────────────────────────────────
		logger.Info("🔒 [STARTUP-SYNC] Go Master state loaded from LevelDB: block=%d, account_root=%s, stake_root=%s",
			app.startLastBlock.Header().BlockNumber(),
			app.startLastBlock.Header().AccountStatesRoot().Hex(),
			app.startLastBlock.Header().StakeStatesRoot().Hex())

		// Create account state trie from existing root and cache it
		_, err := app.GetAccountStateTrie(app.startLastBlock.Header().AccountStatesRoot())
		if err != nil {
			return fmt.Errorf("failed to create account state trie: %v", err)
		}

		if trie.GetStateBackend() == trie.BackendNOMT {
			// Pre-init stake_db NOMT handle so we can read both roots.
			stakeStorage := app.storageManager.GetStorageStake()
			if _, initErr := trie.NewStateTrie(e_common.Hash{}, stakeStorage, true); initErr != nil {
				logger.Warn("⚠️ [STARTUP] Failed to pre-init stake_db NOMT handle: %v", initErr)
			}

			nomtAccountRoot, okAccount := trie.GetNomtHandleRoot("account_state")
			nomtStakeRoot, okStake := trie.GetNomtHandleRoot("stake_db")

			if okAccount && okStake {
				headerAccountRoot := app.startLastBlock.Header().AccountStatesRoot()
				headerStakeRoot := app.startLastBlock.Header().StakeStatesRoot()

				logger.Info("🔍 [STARTUP] account_state NOMT root=%s, header AccountStatesRoot=%s | stake_db NOMT root=%s, header StakeStatesRoot=%s",
					nomtAccountRoot.Hex(), headerAccountRoot.Hex(), nomtStakeRoot.Hex(), headerStakeRoot.Hex())

				if nomtAccountRoot == (e_common.Hash{}) && headerAccountRoot != (e_common.Hash{}) {
					logger.Warn("⚠️ [STARTUP] account_state NOMT database is EMPTY (root=0x0) but header expects %s. "+
						"STARTUP-SYNC will fetch missing blocks and reconcile.",
						headerAccountRoot.Hex()[:18]+"...")
				}
				if nomtStakeRoot == (e_common.Hash{}) && headerStakeRoot != (e_common.Hash{}) {
					logger.Warn("⚠️ [STARTUP] stake_db NOMT database is EMPTY (root=0x0) but header expects %s. "+
						"STARTUP-SYNC will fetch missing blocks and reconcile.",
						headerStakeRoot.Hex()[:18]+"...")
				}

				emptyAccountRoot := trie.GetEmptyNomtRoot(10000000, false)
				emptyStakeRoot := trie.GetEmptyNomtRoot(64000, true)

				isEmptyAccountNomt := (nomtAccountRoot == (e_common.Hash{}) || nomtAccountRoot == emptyAccountRoot || nomtAccountRoot == emptyStakeRoot)
				isEmptyStakeNomt := (nomtStakeRoot == (e_common.Hash{}) || nomtStakeRoot == emptyAccountRoot || nomtStakeRoot == emptyStakeRoot)
				isEmptyNomt := isEmptyAccountNomt || isEmptyStakeNomt


				if isEmptyNomt && (headerAccountRoot != (e_common.Hash{}) || headerStakeRoot != (e_common.Hash{})) {
					logger.Warn("⚠️ [STARTUP] NOMT database is EMPTY (account=%s, stake=%s) but header expects (account=%s, stake=%s). "+
						"Aligning startup tip block height to genesis (block #0) to allow re-execution/reconcile.",
						nomtAccountRoot.Hex(), nomtStakeRoot.Hex(), headerAccountRoot.Hex()[:18]+"...", headerStakeRoot.Hex()[:18]+"...")
					
					// Load block 0 (genesis) from block database
					key := []byte("blockNumber_0")
					data, err := app.storageManager.GetStorageMapping().Get(key)
					if err == nil && data != nil && len(data) == 32 {
						blockHash := e_common.BytesToHash(data)
						blk0, err := blockDatabase.GetBlockByHash(blockHash)
						if err == nil && blk0 != nil {
							logger.Info("🛡️ [STARTUP] ✅ Successfully loaded genesis block #0. Resetting startup block tip to genesis.")
							app.startLastBlock = blk0
							storage.ForceSetLastBlockNumber(0)
							storage.ForceSetLastGlobalExecIndex(blk0.Header().GlobalExecIndex())
							storage.ForceSetLastHandledCommitIndex(uint32(blk0.Header().CommitIndex()))
							storage.UpdateLastHandledCommitEpoch(uint64(blk0.Header().Epoch()))
							
							// Also reset the last block number in backup db or storage if needed
							storage.UpdateLastBlockNumber(0)
						} else {
							logger.Error("❌ [STARTUP] Failed to load genesis block by hash %s: %v", blockHash.Hex(), err)
						}
					} else {
						logger.Error("❌ [STARTUP] Failed to find blockNumber_0 in LevelDB mapping: %v", err)
					}
				} else if (nomtAccountRoot != headerAccountRoot || nomtStakeRoot != headerStakeRoot) && (headerAccountRoot != (e_common.Hash{}) || headerStakeRoot != (e_common.Hash{})) {
					logger.Warn("🛡️ [STARTUP] NOMT Root MISMATCH: account(nomt=%s header=%s), stake(nomt=%s header=%s). Searching for correct matching block in LevelDB...",
						nomtAccountRoot.Hex()[:18], headerAccountRoot.Hex()[:18], nomtStakeRoot.Hex()[:18], headerStakeRoot.Hex()[:18])

					found := false
					for bn := app.startLastBlock.Header().BlockNumber(); bn > 0; bn-- {
						key := []byte(fmt.Sprintf("blockNumber_%d", bn))
						data, err := app.storageManager.GetStorageMapping().Get(key)
						if err != nil || data == nil || len(data) != 32 {
							continue
						}
						blockHash := e_common.BytesToHash(data)
						blk, err := blockDatabase.GetBlockByHash(blockHash)
						if err != nil || blk == nil {
							continue
						}
						// BOTH roots must match!
						if blk.Header().AccountStatesRoot() == nomtAccountRoot && blk.Header().StakeStatesRoot() == nomtStakeRoot {
							correctedGEI := blk.Header().GlobalExecIndex()
							logger.Warn("🛡️ [STARTUP] ✅ Found matching fallback block #%d (accountRoot=%s, stakeRoot=%s, GEI=%d). Aligning startup tip block height to this block.",
								bn, nomtAccountRoot.Hex()[:18]+"...", nomtStakeRoot.Hex()[:18]+"...", correctedGEI)
							app.startLastBlock = blk
							storage.ForceSetLastBlockNumber(bn)
							storage.ForceSetLastGlobalExecIndex(correctedGEI)
							storage.ForceSetLastHandledCommitIndex(uint32(blk.Header().CommitIndex()))
							storage.UpdateLastHandledCommitEpoch(uint64(blk.Header().Epoch()))
							found = true
							break
						}
					}

					if !found {
						if trie.GetStateBackend() == trie.BackendNOMT {
							logger.Warn("🛡️ [STARTUP] ⚠️ No matching block found in LevelDB for NOMT roots. Wiping NOMT database and resetting block tip to genesis to force clean sync!")

							// Close all handles
							trie.CloseNomtDB()

							// Wipe nomt_db directory
							nomtDbDir := filepath.Join(app.config.Databases.RootPath, "consensus", "nomt_db")
							if err := os.RemoveAll(nomtDbDir); err != nil {
								logger.Error("❌ [STARTUP] Failed to wipe NOMT database directory at %s: %v", nomtDbDir, err)
							} else {
								logger.Info("✅ [STARTUP] Successfully wiped NOMT database directory at %s", nomtDbDir)
							}

							// Reset startup block tip to genesis (block 0)
							key := []byte("blockNumber_0")
							data, err := app.storageManager.GetStorageMapping().Get(key)
							if err == nil && data != nil && len(data) == 32 {
								blockHash := e_common.BytesToHash(data)
								blk0, err := blockDatabase.GetBlockByHash(blockHash)
								if err == nil && blk0 != nil {
									logger.Info("🛡️ [STARTUP] ✅ Successfully loaded genesis block #0. Resetting startup block tip to genesis.")
									app.startLastBlock = blk0
									storage.ForceSetLastBlockNumber(0)
									storage.ForceSetLastGlobalExecIndex(blk0.Header().GlobalExecIndex())
									storage.ForceSetLastHandledCommitIndex(uint32(blk0.Header().CommitIndex()))
									storage.UpdateLastHandledCommitEpoch(uint64(blk0.Header().Epoch()))
									storage.UpdateLastBlockNumber(0)
								} else {
									logger.Fatal("🚨 [STARTUP] CRITICAL: Failed to load genesis block #0 by hash %s: %v", blockHash.Hex(), err)
								}
							} else {
								logger.Fatal("🚨 [STARTUP] CRITICAL: Failed to find blockNumber_0 in LevelDB mapping: %v", err)
							}
						} else {
							logger.Fatal("🚨 [STARTUP] CRITICAL DATABASE MISMATCH: NOMT roots (account=%s, stake=%s) do not match any block in LevelDB, and header mismatch exists (account=%s, stake=%s). Halting to prevent state fork!",
								nomtAccountRoot.Hex(), nomtStakeRoot.Hex(), headerAccountRoot.Hex(), headerStakeRoot.Hex())
						}
					}
				}
			}
		}

		app.chainState, err = blockchain.NewChainStateWithGenesis(app.storageManager, blockDatabase, app.startLastBlock.Header(), app.config, FreeFeeAddresses, &app.genesis.Config, app.config.BackupPath)
		if err != nil {
			return fmt.Errorf("failed NewChainState: %v", err)
		}

		if futureNomtRoot != (e_common.Hash{}) {
			app.chainState.SetFutureNomtRoot(futureNomtRoot)
			logger.Info("🛡️ [STARTUP] Registered future unaligned NOMT root %s in ChainState for catch-up bypass.", futureNomtRoot.Hex()[:18])
		}

		// FORK-SAFETY ROOT FIX: After ChainState loads (including LoadEpochData),
		// verify that the epoch matches the last block header. This prevents fork
		// after snapshot restore where LoadEpochData() returns stale epoch data.
		// Block headers contain authoritative epoch — the snapshot's blocks are the
		// ground truth for what epoch the node should be in.
		if app.startLastBlock != nil {
			app.chainState.ForceAlignEpochFromBlockHeader(
				app.startLastBlock.Header().Epoch(),
				app.startLastBlock.Header().TimeStamp(),
				app.startLastBlock.Header().BlockNumber(),
			)
		}

		// Note: SetBackupPath is no longer needed - backupPath is set in constructor

		// Initialize trie database manager
		trie_database.CreateTrieDatabaseManager(
			app.storageManager.GetStorageDatabaseTrie(),
			app.chainState.GetAccountStateDB())

		// Initialize blockchain
		blockchain.InitBlockChain(100, blockDatabase, app.storageManager)
		if app.chainState != nil {
			blockchain.GetBlockChainInstance().SetChangelogDB(app.chainState.GetChangelogDB())
		}
		blockchain.GetBlockChainInstance().SetBlockNumberToHash(uint64(app.startLastBlock.Header().BlockNumber()), app.startLastBlock.Header().Hash())
		blockchain.GetBlockChainInstance().Commit()

		// ═══════════════════════════════════════════════════════════════════
		// STARTUP MAPPING REBUILD (May 2026): Walk backwards from startLastBlock
		// through parentHash chain to rebuild any missing blockNumber→hash mappings.
		// These mappings can be lost if the node was SIGTERM'd/crashed before
		// dirtyStorage was flushed to PebbleDB. Without this, historical RPC
		// queries (eth_getTransactionCount, eth_getBalance at specific block)
		// fail with "block not found" after recovery.
		//
		// Depth is conditional on previous shutdown type:
		//   - Clean shutdown: verify last 50 blocks (fast sanity check, ~1ms)
		//   - Crash/SIGKILL:  full walk to genesis (recover all lost mappings)
		// ═══════════════════════════════════════════════════════════════════
		rebuildMaxBlocks := 0 // unlimited — full recovery walk
		if wasCleanShutdown {
			rebuildMaxBlocks = 50 // fast sanity check only
		}
		blockchain.GetBlockChainInstance().RebuildMappingsFromBlock(app.startLastBlock, rebuildMaxBlocks)
	}

SKIP_GENESIS:

	// ═══════════════════════════════════════════════════════════════════
	// STARTUP INTEGRITY CHECK (May 2026)
	// Verify critical data consistency BEFORE starting consensus/sync.
	// If data is corrupted beyond self-repair → warn ops team → exit.
	// Clean shutdown: light check (10 blocks)
	// Crash/SIGKILL: deep check (100 blocks)
	// ═══════════════════════════════════════════════════════════════════
	integrityDepth := 100 // deep check for crash recovery
	if wasCleanShutdown {
		integrityDepth = 10 // light check for clean shutdown
	}
	integrityResult := app.runStartupIntegrityCheck(integrityDepth)
	app.handleIntegrityResult(integrityResult) // exits if critical errors found

	// ═══════════════════════════════════════════════════════════════════
	// ATOMIC SNAPSHOT VERIFICATION (OPTION C)
	// ═══════════════════════════════════════════════════════════════════
	// Reads atomic snapshot metadata and forces perfect alignment between Go and Rust.
	// ═══════════════════════════════════════════════════════════════════
	if app.chainState != nil && app.chainState.GetAccountStateDB() != nil {
		nomtRoot := app.chainState.GetAccountStateDB().Trie().Hash()
		startStateRoot := app.startLastBlock.Header().AccountStatesRoot()

		// ═══════════════════════════════════════════════════════════════
		// FORK-DIAG (May 2026): Cross-check trie cached root vs direct NOMT handle root.
		// If these differ, the NomtStateTrie was constructed with a stale root and all
		// subsequent state reads will be inconsistent.
		// ═══════════════════════════════════════════════════════════════
		if nomtHandleRoot, ok := trie.GetNomtHandleRoot("account_state"); ok {
			if nomtHandleRoot != nomtRoot {
				logger.Error("🚨 [STARTUP] CRITICAL: NOMT handle root (%s) differs from trie cached root (%s)! "+
					"The AccountStateDB trie is stale.",
					nomtHandleRoot.Hex()[:18]+"...", nomtRoot.Hex()[:18]+"...")
				// Use the handle root as the authoritative source
				nomtRoot = nomtHandleRoot
			} else {
				logger.Info("✅ [STARTUP] NOMT handle root matches trie cached root: %s",
					nomtRoot.Hex()[:18]+"...")
			}
		}

		// ═══════════════════════════════════════════════════════════════
		// FORK-FIX (May 2026): Cross-check NOMT stake_db handle root.
		// This was previously MISSING — only AccountStatesRoot was verified,
		// leaving StakeStatesRoot=0x0 undetected after snapshot restore.
		// ═══════════════════════════════════════════════════════════════
		if nomtStakeHandleRoot, okStake := trie.GetNomtHandleRoot("stake_db"); okStake {
			headerStakeRoot := app.startLastBlock.Header().StakeStatesRoot()
			if nomtStakeHandleRoot == (e_common.Hash{}) && headerStakeRoot != (e_common.Hash{}) {
				logger.Warn("⚠️ [STARTUP] stake_db NOMT is EMPTY (root=0x0) but header expects %s. "+
					"STARTUP-SYNC will reconcile.",
					headerStakeRoot.Hex()[:18]+"...")
			}
			if nomtStakeHandleRoot != headerStakeRoot {
				logger.Error("🚨 [STARTUP] NOMT stake_db handle root (%s) differs from header StakeStatesRoot (%s)!",
					nomtStakeHandleRoot.Hex()[:18]+"...", headerStakeRoot.Hex()[:18]+"...")
			} else {
				logger.Info("✅ [STARTUP] NOMT stake_db handle root matches header: %s",
					nomtStakeHandleRoot.Hex()[:18]+"...")
			}
		} else {
			logger.Warn("⚠️ [STARTUP] stake_db NOMT handle not initialized, cannot verify StakeStatesRoot")
		}

		// Attempt to load metadata.json
		metadataPath := filepath.Join(app.config.Databases.RootPath, "metadata.json")
		var metadata *executor.SnapshotMetadata

		if metadataBytes, err := os.ReadFile(metadataPath); err == nil {
			var md executor.SnapshotMetadata
			if jsonErr := json.Unmarshal(metadataBytes, &md); jsonErr == nil {
				metadata = &md
				logger.Info("📸 [SNAPSHOT FIX] Loaded metadata.json: Block=%d, GEI=%d, StateRoot=%s",
					md.BlockNumber, md.GlobalExecIndex, md.StateRoot)
			}
		}

		if nomtRoot != (e_common.Hash{}) {
			if metadata != nil && metadata.StateRoot != "" {
				// We have atomic metadata. Enforce strict alignment!
				nomtRootHex := "0x" + nomtRoot.Hex()
				// Trim 0x if hex has it
				if len(nomtRootHex) > 2 && nomtRootHex[2:4] == "0x" {
					nomtRootHex = nomtRoot.Hex()
				}
				metadataRootHex := metadata.StateRoot
				if len(metadataRootHex) > 0 && metadataRootHex[:2] != "0x" {
					metadataRootHex = "0x" + metadataRootHex
				}

				if nomtRootHex != metadata.StateRoot && nomtRoot.Hex() != metadata.StateRoot {
					logger.Error("❌ [FATAL] Snapshot Restore Mismatch! NOMT root=%s, but metadata.json claims StateRoot=%s",
						nomtRoot.Hex(), metadata.StateRoot)
					fatal.Exit("FATAL: Snapshot restore failed. NOMT state corrupted or mismatched with metadata.")
				}

				// Enforce GEI and BlockNumber globally from metadata to prevent any inflation
				logger.Info("🛡️ [SNAPSHOT FIX] ✅ Restored perfectly aligned state from metadata (Block=%d, GEI=%d)",
					metadata.BlockNumber, metadata.GlobalExecIndex)

				storage.ForceSetLastGlobalExecIndex(metadata.GlobalExecIndex)
				storage.ForceSetLastBlockNumber(metadata.BlockNumber)
				// ALSO align the commit index to avoid Rust skipping/re-running commits
				storage.ForceSetLastHandledCommitIndex(uint32(metadata.LastHandledCommitIdx))
				storage.UpdateLastHandledCommitEpoch(uint64(metadata.Epoch))

				if metadata.RPCSupportedBlock > 0 {
					syncKey := []byte("rpc_sync_last_checked_block")
					buf := make([]byte, 8)
					binary.BigEndian.PutUint64(buf, metadata.RPCSupportedBlock)
					app.storageManager.GetStorageReceipt().Put(syncKey, buf)
					logger.Info("✅ [STARTUP] Initialized rpc_sync_last_checked_block to %d from snapshot metadata", metadata.RPCSupportedBlock)
				}

				// NOTE: GEIAuthority singleton is not initialized yet at this point in startup.
				// ForceSet calls above update the storage globals, which the singleton will
				// read on its first initialization (sync.Once in GetGEIAuthority).

				// Fix startLastBlock mapping
				blkHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(metadata.BlockNumber)
				if ok {
					if blk, err := blockDatabase.GetBlockByHash(blkHash); err == nil && blk != nil {
						app.startLastBlock = blk
					}
				}

				// Rename metadata.json so it's not processed again on next reboot
				if err := os.Rename(metadataPath, metadataPath+".applied"); err != nil {
					logger.Warn("⚠️ Failed to rename metadata.json to .applied: %v", err)
				} else {
					logger.Info("✅ Renamed metadata.json to metadata.json.applied")
				}
			} else if startStateRoot != (e_common.Hash{}) && nomtRoot != startStateRoot {
				// FALLBACK ONLY IF NO METADATA (for backward compatibility with old snapshots)
				logger.Warn("🛡️ [SNAPSHOT FIX] State mismatch! NOMT root=%s, startLastBlock #%d stateRoot=%s. "+
					"LevelDB has P2P-synced blocks beyond executed state. Searching for correct block...",
					nomtRoot.Hex()[:18]+"...", app.startLastBlock.Header().BlockNumber(),
					startStateRoot.Hex()[:18]+"...")

				found := false
				for bn := app.startLastBlock.Header().BlockNumber(); bn > 0; bn-- {
					blkHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(bn)
					if !ok {
						continue
					}
					blk, err := blockDatabase.GetBlockByHash(blkHash)
					if err != nil || blk == nil {
						continue
					}
					if blk.Header().AccountStatesRoot() == nomtRoot {
						correctedGEI := blk.Header().GlobalExecIndex()
						logger.Warn("🛡️ [SNAPSHOT FIX] ✅ Found matching fallback block #%d (stateRoot=%s, GEI=%d).",
							bn, nomtRoot.Hex()[:18]+"...", correctedGEI)
						app.startLastBlock = blk
						storage.ForceSetLastBlockNumber(bn)
						storage.ForceSetLastGlobalExecIndex(correctedGEI)
						found = true
						break
					}
				}
				if !found {
					logger.Warn("⚠️ [SNAPSHOT RECOVERY] NOMT root %s does not match LevelDB block #%d. "+
						"This occurs if the node was terminated before LevelDB flushed to disk. "+
						"STARTUP-SYNC will fetch missing blocks and reconcile the state.",
						nomtRoot.Hex()[:18]+"...", app.startLastBlock.Header().BlockNumber())
				}
			}
		}
		// ═══════════════════════════════════════════════════════════════
		// STARTUP DIAGNOSTIC: Read-only validation of GEI/CommitIndex alignment.
		// Upstream paths (lines 267-322 for normal boot, 487-490 for metadata boot)
		// already set these values authoritatively. This block only WARNS on mismatch.
		// ═══════════════════════════════════════════════════════════════
		if app.startLastBlock != nil {
			headerGEI := app.startLastBlock.Header().GlobalExecIndex()
			storageGEI := storage.GetLastGlobalExecIndex()
			headerCommit := app.startLastBlock.Header().CommitIndex()
			storageCommit := storage.GetLastHandledCommitIndex()

			if headerGEI != storageGEI {
				logger.Warn("⚠️ [STARTUP-DIAG] GEI alignment note: header=%d storage=%d (upstream should have handled this)",
					headerGEI, storageGEI)
			}
			if headerCommit > 0 && uint32(headerCommit) != storageCommit {
				logger.Warn("⚠️ [STARTUP-DIAG] CommitIndex alignment note: header=%d storage=%d (upstream should have handled this)",
					headerCommit, storageCommit)
			}
			logger.Info("✅ [STARTUP-DIAG] Final state: GEI=%d, CommitIndex=%d, Block=%d, Epoch=%d",
				storageGEI, storageCommit, app.startLastBlock.Header().BlockNumber(), app.startLastBlock.Header().Epoch())

			// ═══════════════════════════════════════════════════════════════
			// STARTUP MAPPING ALIGNMENT:
			// Re-register and durably flush mapping for startLastBlock tip.
			// This guards against sudden SIGTERM shut downs where the block itself
			// was stored in LevelDB but Pebble's mapping DB memtable was not flushed to disk.
			// ═══════════════════════════════════════════════════════════════
			bc := blockchain.GetBlockChainInstance()
			if bc != nil {
				blockNum := uint64(app.startLastBlock.Header().BlockNumber())
				blockHash := app.startLastBlock.Header().Hash()
				existingHash, ok := bc.GetBlockHashByNumber(blockNum)
				if !ok || existingHash != blockHash {
					logger.Info("🔄 [STARTUP-MAPPING] Restoring/correcting mapping for last startup block #%d: existing=%s -> expected=%s", blockNum, existingHash.Hex(), blockHash.Hex())
					_ = bc.SetBlockNumberToHash(blockNum, blockHash)
					_ = bc.Commit()
					if app.storageManager != nil && app.storageManager.GetStorageMapping() != nil {
						if err := app.storageManager.GetStorageMapping().Flush(); err != nil {
							logger.Error("❌ [STARTUP-MAPPING] Failed to durably flush mapping for block #%d: %v", blockNum, err)
						} else {
							logger.Info("✅ [STARTUP-MAPPING] Durable flush completed for corrected block #%d mapping.", blockNum)
						}
					}
				} else {
					logger.Info("✅ [STARTUP-MAPPING] Block #%d mapping already present and correct: %s", blockNum, existingHash.Hex())
				}
			}
		}
	}

	// EVENT-DRIVEN NOTIFICATION SETUP
	// Derive notification socket path from RustSendSocketPath (e.g. metanode-rpc-1.sock -> metanode-notification-1.sock)
	if app.config.RustSendSocketPath != "" {
		notificationSocketPath := strings.Replace(app.config.RustSendSocketPath, "rpc", "notification", 1)
		logger.Info("🔧 [EPOCH NOTIFIER] Configured notification socket path: %s", notificationSocketPath)

		notifier := executor.GetCommitteeNotifier()
		notifier.SetSocketPath(notificationSocketPath)

		// Wire up ChainState callback to Notifier
		if app.chainState != nil {
			app.chainState.SetEpochNotificationCallback(func(epoch, ts, boundary uint64) {
				logger.Info("📣 [EPOCH NOTIFIER] Callback triggered for epoch %d. Sending to Rust...", epoch)
				if err := notifier.NotifyEpochChange(epoch, ts, boundary); err != nil {
					logger.Warn("⚠️ [EPOCH NOTIFIER] Failed to send notification: %v", err)
				}
			})
		}
	} else {
		logger.Warn("⚠️ [EPOCH NOTIFIER] RustSendSocketPath is empty. Notification system disabled.")
	}

	return nil
}

// initGenesisBlock creates the genesis block if it doesn't exist
func (app *App) initGenesisBlock(blockDatabase *block.BlockDatabase) error {
	logger.Info("Starting genesis block initialization...")

	// CRITICAL: Use genesis timestamp from genesis.json for deterministic genesis hash
	// Block header timestamp is in SECONDS, genesis.json has EpochTimestampMs in MILLISECONDS
	var genesisTimestamp uint64
	if app.genesis != nil && app.genesis.Config.EpochTimestampMs > 0 {
		genesisTimestamp = app.genesis.Config.EpochTimestampMs // Use ms directly
		logger.Info("✅ [GENESIS] Using timestamp from genesis.json: %d ms",
			app.genesis.Config.EpochTimestampMs)
	} else {
		genesisTimestamp = 0
		logger.Warn("⚠️ [GENESIS] No timestamp in genesis.json, using 0")
	}

	// Create genesis block with timestamp from genesis.json
	app.startLastBlock = block.NewBlock(
		block.NewBlockHeader(
			e_common.Hash{},
			0,
			trie.EmptyRootHash,
			e_common.Hash{},
			e_common.Hash{},
			e_common.Address{},
			genesisTimestamp, // Use genesis timestamp instead of hardcoded 0
			trie.EmptyRootHash,
			0, // epoch = 0 for genesis block
		),
		nil,
		nil,
	)
	var err error
	app.chainState, err = blockchain.NewChainStateWithGenesis(app.storageManager, blockDatabase, app.startLastBlock.Header(), app.config, FreeFeeAddresses, &app.genesis.Config, app.config.BackupPath)
	if err != nil {
		return fmt.Errorf("failed NewChainState: %v", err)
	}
	// Note: SetBackupPath is no longer needed - backupPath is set in constructor
	// Set genesis accounts
	addressMap := make(map[e_common.Address]bool)
	for _, account := range app.genesis.Alloc {
		a := account.ToAccountState()
		if _, exists := addressMap[a.Address()]; exists {
			logger.Error("Duplicate address found in genesis allocation: ", a.Address())
			return fmt.Errorf("duplicate address in genesis allocation: %s", a.Address().Hex())
		}
		addressMap[a.Address()] = true
		a.PlusOneNonce()
		app.chainState.GetAccountStateDB().SetState(a)
	}

	// Commit state changes
	app.chainState.GetAccountStateDB().IntermediateRoot(true)

	hash, err := app.chainState.GetAccountStateDB().Commit()
	if err != nil {
		return fmt.Errorf("failed to commit genesis state: %v", err)
	}
	app.startLastBlock.Header().SetAccountStatesRoot(hash)

	// Verify account balances
	for _, account := range app.genesis.Alloc {
		a := account.ToAccountState()
		asChain, _ := app.chainState.GetAccountStateDB().AccountState(a.Address())
		if asChain.Balance().Cmp(a.Balance()) != 0 {
			logger.Error("Balance mismatch for address: ", asChain.Address())
			logger.Error("chain Balance: ", asChain.Balance())
			logger.Error("file Balance: ", a.Balance())
			return fmt.Errorf("error updating genesis accounts")
		}
	}
	// Chuyển đổi từ struct Protobuf sang struct state nội bộ
	cs := app.chainState.GetStakeStateDB()
	logger.Info("Registering %d validators from genesis.json...", len(app.genesis.Validators))
	for _, val := range app.genesis.Validators {
		minSelfDelegation := new(big.Int)
		minSelfDelegation, ok := minSelfDelegation.SetString(val.GetMinSelfDelegation(), 10)
		if !ok {
			return fmt.Errorf("invalid GetMinSelfDelegation value: %s", val.GetMinSelfDelegation())
		}
		// Ưu tiên dùng các trường mới (tương thích với committee.json), fallback về trường cũ
		name := val.GetHostname()
		if name == "" {
			name = val.GetName()
		}
		pubkeyBls := val.GetAuthorityKey()
		if len(pubkeyBls) == 0 {
			pubkeyBls = []byte(val.GetPubkeyBls())
		}
		pubkeySecp := val.GetProtocolKey()
		if len(pubkeySecp) == 0 {
			pubkeySecp = []byte(val.GetPubkeySecp())
		}
		networkKey := val.GetNetworkKey()
		if len(networkKey) == 0 {
			networkKey = pubkeySecp // Fallback to protocol_key if network_key not set
		}
		validatorAddress := e_common.HexToAddress(val.GetAddress())

		// CRITICAL: Register validator with separate protocol_key and network_key
		// This ensures compatibility with Rust committee.json format
		cs.CreateRegisterWithKeys(
			validatorAddress,
			name,
			val.GetDescription(),
			val.GetWebsite(),
			val.GetImage(),
			val.GetCommissionRate(),
			minSelfDelegation, // <-- đã chuyển đúng kiểu *big.Int
			val.GetPrimaryAddress(),
			val.GetWorkerAddress(),
			val.GetP2PAddress(),
			hex.EncodeToString(pubkeyBls), // Encode raw bytes to hex string for valid UTF-8
			pubkeySecp,        // protocol_key []byte
			networkKey,        // network_key []byte
			name,              // hostname
			pubkeyBls,         // authority_key []byte
		)

		// CRITICAL: Set initial stake from delegator_stakes in genesis.json
		// This ensures validators have stake > 0 so they can be returned by GetValidatorsAtBlockRequest
		// Without stake, validators will be filtered out and Rust won't be able to load committee

		// Parse delegator_stakes from genesis JSON - we need to read the raw JSON to get this data
		genesisFile, err := os.Open(app.config.GenesisFilePath)
		if err != nil {
			logger.Error("Failed to open genesis file for delegator_stakes: %v", err)
			return err
		}
		defer genesisFile.Close()

		var genesisRaw map[string]interface{}
		if err := json.NewDecoder(genesisFile).Decode(&genesisRaw); err != nil {
			logger.Error("Failed to parse genesis JSON: %v", err)
			return err
		}

		// Find validator in raw genesis data
		var delegatorStakesFromGenesis []map[string]interface{}
		if validatorsRaw, ok := genesisRaw["validators"].([]interface{}); ok {
			for _, valRaw := range validatorsRaw {
				if valMap, ok := valRaw.(map[string]interface{}); ok {
					if address, ok := valMap["address"].(string); ok && strings.EqualFold(address, validatorAddress.Hex()) {
						if delegatorStakes, ok := valMap["delegator_stakes"].([]interface{}); ok {
							for _, stakeRaw := range delegatorStakes {
								if stakeMap, ok := stakeRaw.(map[string]interface{}); ok {
									delegatorStakesFromGenesis = append(delegatorStakesFromGenesis, stakeMap)
								}
							}
						}
						break
					}
				}
			}
		}

		delegators := delegatorStakesFromGenesis
		logger.Info("Validator %s has %d delegators from genesis.json",
			validatorAddress.Hex(), len(delegators))

		if len(delegators) == 0 {
			logger.Warn("Validator %s has NO delegators in genesis.json! Stake will be 0.",
				validatorAddress.Hex())
		}

		totalStakeFromGenesis := big.NewInt(0)
		for i, delegatorStake := range delegators {
			delegatorAddrStr, ok := delegatorStake["address"].(string)
			if !ok {
				logger.Error("Invalid delegator address format for validator %s", validatorAddress.Hex())
				continue
			}
			delegatorAddress := e_common.HexToAddress(delegatorAddrStr)

			amountStr, ok := delegatorStake["amount"].(string)
			if !ok {
				logger.Error("Invalid stake amount format for validator %s, delegator %s", validatorAddress.Hex(), delegatorAddress.Hex())
				continue
			}

			stakeAmount := new(big.Int)
			stakeAmount, ok = stakeAmount.SetString(amountStr, 10)
			if !ok {
				logger.Error("Invalid stake amount for validator %s, delegator %s: %s",
					validatorAddress.Hex(), delegatorAddress.Hex(), amountStr)
				continue
			}
			if stakeAmount.Sign() <= 0 {
				logger.Warn("Zero or negative stake amount for validator %s, delegator %s, skipping",
					validatorAddress.Hex(), delegatorAddress.Hex())
				continue
			}

			totalStakeFromGenesis.Add(totalStakeFromGenesis, stakeAmount)
			logger.Info("Delegator[%d] for validator %s: address=%s, amount=%s (total so far=%s)",
				i, validatorAddress.Hex(), delegatorAddress.Hex(), stakeAmount.String(), totalStakeFromGenesis.String())

			// Delegate stake to validator (this sets TotalStakedAmount)
			if err := cs.Delegate(validatorAddress, delegatorAddress, stakeAmount); err != nil {
				logger.Error("Failed to delegate stake for validator %s, delegator %s: %v",
					validatorAddress.Hex(), delegatorAddress.Hex(), err)
				return fmt.Errorf("failed to set initial stake for validator %s: %v", validatorAddress.Hex(), err)
			}
			logger.Info("✅ Set initial stake for validator %s: delegator=%s, amount=%s",
				validatorAddress.Hex(), delegatorAddress.Hex(), stakeAmount.String())
		}

		// Verify total stake after all delegations
		vs, verifyErr := cs.GetValidator(validatorAddress)
		if verifyErr == nil && vs != nil {
			actualTotalStake := vs.TotalStakedAmount()
			if actualTotalStake.Cmp(totalStakeFromGenesis) != 0 {
				logger.Warn("Validator %s stake mismatch! Expected=%s, Actual=%s",
					validatorAddress.Hex(), totalStakeFromGenesis.String(), actualTotalStake.String())
			}
		}
	}
	logger.Info("Committing stake state...")
	hashStake, _ := cs.IntermediateRoot(true)
	commitHash, commitErr := cs.Commit()
	if commitErr != nil {
		logger.Error("Failed to commit stake state: %v", commitErr)
		return commitErr
	}
	logger.Info("Stake state committed successfully, hash=%s", commitHash.Hex())

	// GENESIS-FIX (May 2026): Use commitHash (post-Commit authoritative root)
	// instead of hashStake (pre-Commit IntermediateRoot).
	// ROOT CAUSE: On NOMT backend, IntermediateRoot() returns 0x0 BEFORE the
	// commit runs because NOMT only computes the real root during Commit().
	// Using hashStake=0x0 causes a false STARTUP error on every fresh boot:
	//   "NOMT stake_db handle root (0x7f2b...) differs from header StakeStatesRoot (0x0...)"
	// The self-healing code patches it, but the error log is misleading.
	stakeRoot := commitHash
	if stakeRoot == (e_common.Hash{}) && hashStake != (e_common.Hash{}) {
		// Fallback for non-NOMT backends where Commit returns empty but IntermediateRoot is valid
		stakeRoot = hashStake
	}
	app.startLastBlock.Header().SetStakeStatesRoot(stakeRoot)
	saveErr := blockDatabase.SaveLastBlock(app.startLastBlock)
	if saveErr != nil {
		logger.Error("❌ [GENESIS] Failed to SaveLastBlock: %v", saveErr)
	} else {
		// FORCE FLUSH the block storage immediately
		if flushErr := app.storageManager.GetStorageBlock().Flush(); flushErr != nil {
			logger.Error("❌ [GENESIS] Failed to flush block storage: %v", flushErr)
		} else {
			logger.Info("✅ [GENESIS] Block 0 flushed to disk successfully")
		}
	}
	logger.Info("Genesis block saved successfully")

	// Verify validators were actually saved by reading them back
	allValidators, verifyErr := cs.GetAllValidators()
	if verifyErr != nil {
		logger.Error("Failed to verify validators after commit: %v", verifyErr)
	} else {
		logger.Info("Verification: Found %d validators in stake state DB after commit", len(allValidators))
		if len(allValidators) == 0 {
			logger.Warn("No validators found after commit! This is a critical error.")
		} else {
			for i, val := range allValidators {
				stake := val.TotalStakedAmount()
				logger.Info("  Validator %d: address=%s, name=%s, stake=%s",
					i+1, val.Address().Hex(), val.Name(), stake.String())
			}
		}
	}

	logger.Info("Genesis block initialized successfully with %d validators", len(app.genesis.Validators))

	// NOTE (Apr 2026): NOMT CommitPayload is now handled synchronously inside
	// AccountStateDB.Commit() BEFORE the trie swap (see account_state_db_commit.go).
	// The previous PebbleDB genesis fallback code has been removed — it was a
	// workaround for the CommitPayload orphan bug that wasted 1.2GB of PebbleDB
	// on every fresh start. Sub nodes now correctly read genesis data from NOMT.

	return nil
}

// loadFreeFeeAddresses loads fee-free addresses from configuration
func (app *App) loadFreeFeeAddresses() {
	if reflect.TypeOf(app.config.FreeFeeAddresses).Kind() == reflect.Slice {
		if len(app.config.FreeFeeAddresses) > 0 {
			for _, addr := range app.config.FreeFeeAddresses {
				if len(addr) != 40 {
					logger.Warn("Invalid address length in FreeFeeAddresses: %s", addr)
					continue
				}
				key := e_common.HexToAddress(addr)
				FreeFeeAddresses[key] = struct{}{}
				// logger.Info("FreeFeeAddresses: ", key)
			}
		}
	} else {
		fatal.Exit("FreeFeeAddresses in config.json is not an array")
	}
}
