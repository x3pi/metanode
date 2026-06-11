package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ═══════════════════════════════════════════════════════════════════════════════
// STARTUP INTEGRITY CHECKER (May 2026)
//
// Chạy TRƯỚC khi node bắt đầu consensus/sync. Verify tất cả dữ liệu critical:
//   - LastBlock loadable & consistent
//   - Block chain integrity (parentHash chain valid for last N blocks)
//   - NOMT account/stake trie roots match header
//   - Block data retrievable by hash
//   - BlockNumber→hash mappings exist
//
// Nếu check FAIL ở mức CRITICAL → log hướng dẫn restore snapshot → exit(1)
// Nếu check FAIL ở mức WARNING → log cảnh báo → tiếp tục chạy (self-healing)
// ═══════════════════════════════════════════════════════════════════════════════

// IntegrityCheckResult holds results of the startup integrity check.
type IntegrityCheckResult struct {
	CriticalErrors []string // Errors that MUST be fixed before node can run
	Warnings       []string // Issues that can be self-healed or are non-fatal
	CheckedBlocks  int      // Number of blocks successfully verified
}

// runStartupIntegrityCheck performs comprehensive data verification.
// Returns the check results. The caller decides whether to exit or continue.
//
// checkDepth: how many recent blocks to verify (0 = skip block chain walk)
// wasCleanShutdown: if true, only run lightweight checks
func (app *App) runStartupIntegrityCheck(checkDepth int) *IntegrityCheckResult {
	result := &IntegrityCheckResult{}

	logger.Info("═══════════════════════════════════════════════════════════")
	logger.Info("🔍 [INTEGRITY] Starting data integrity check (depth=%d)...", checkDepth)
	logger.Info("═══════════════════════════════════════════════════════════")

	// ──────────────────────────────────────────────────────────────────
	// CHECK 1: LastBlock is loadable
	// ──────────────────────────────────────────────────────────────────
	if app.startLastBlock == nil {
		result.CriticalErrors = append(result.CriticalErrors,
			"LastBlock is nil — database may be corrupted or genesis was not created")
		return result // Can't continue without lastBlock
	}

	lastBlockNum := app.startLastBlock.Header().BlockNumber()
	lastBlockHash := app.startLastBlock.Header().Hash()
	logger.Info("✅ [INTEGRITY] CHECK 1/5: LastBlock loaded: #%d hash=%s",
		lastBlockNum, lastBlockHash.Hex()[:18]+"...")

	// ──────────────────────────────────────────────────────────────────
	// CHECK 2: Block chain integrity — walk parentHash chain
	// Verify each block is retrievable and parentHash links correctly
	// ──────────────────────────────────────────────────────────────────
	if checkDepth > 0 && app.chainState != nil {
		blockDB := app.chainState.GetBlockDatabase()
		if blockDB != nil {
			brokenChainAt := app.verifyBlockChain(blockDB, app.startLastBlock, checkDepth, result)
			if brokenChainAt > 0 {
				result.CriticalErrors = append(result.CriticalErrors,
					fmt.Sprintf("Block chain is broken at block #%d — blocks before this point may be missing or corrupted", brokenChainAt))
			} else {
				logger.Info("✅ [INTEGRITY] CHECK 2/5: Block chain verified for %d blocks (from #%d back)",
					result.CheckedBlocks, lastBlockNum)
			}
		} else {
			result.Warnings = append(result.Warnings, "BlockDatabase not available — skipping block chain check")
		}
	} else {
		logger.Info("⏭️  [INTEGRITY] CHECK 2/5: Block chain walk skipped (depth=0 or no chainState)")
	}

	// ──────────────────────────────────────────────────────────────────
	// CHECK 3: BlockNumber→Hash mapping exists for lastBlock
	// ──────────────────────────────────────────────────────────────────
	bc := blockchain.GetBlockChainInstance()
	if bc != nil {
		hash, ok := bc.GetBlockHashByNumber(lastBlockNum)
		if !ok {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("BlockNumber→Hash mapping missing for lastBlock #%d (will be rebuilt)", lastBlockNum))
		} else if hash != lastBlockHash {
			result.CriticalErrors = append(result.CriticalErrors,
				fmt.Sprintf("BlockNumber→Hash mapping MISMATCH for #%d: expected %s, got %s",
					lastBlockNum, lastBlockHash.Hex()[:18]+"...", hash.Hex()[:18]+"..."))
		} else {
			logger.Info("✅ [INTEGRITY] CHECK 3/5: Block mapping for #%d verified", lastBlockNum)
		}
	}

	// ──────────────────────────────────────────────────────────────────
	// CHECK 4: NOMT Account State trie root matches header
	// ──────────────────────────────────────────────────────────────────
	headerStateRoot := app.startLastBlock.Header().AccountStatesRoot()
	if headerStateRoot != (common.Hash{}) {
		if nomtRoot, ok := trie.GetNomtHandleRoot("account_state"); ok {
			// Attempt to load metadata.json to check if this is a snapshot recovery
			var metadata *executor.SnapshotMetadata
			metadataPath := filepath.Join(app.config.Databases.RootPath, "metadata.json")
			if metadataBytes, err := os.ReadFile(metadataPath); err == nil {
				var md executor.SnapshotMetadata
				if jsonErr := json.Unmarshal(metadataBytes, &md); jsonErr == nil {
					metadata = &md
				}
			}

			isSnapshotRecovery := false
			if metadata != nil && metadata.StateRoot != "" {
				nomtRootHex := nomtRoot.Hex()
				metadataRootHex := metadata.StateRoot
				if !strings.HasPrefix(nomtRootHex, "0x") {
					nomtRootHex = "0x" + nomtRootHex
				}
				if !strings.HasPrefix(metadataRootHex, "0x") {
					metadataRootHex = "0x" + metadataRootHex
				}

				emptyAccountRoot := trie.GetEmptyNomtRoot(10000000, false)
				emptyStakeRoot := trie.GetEmptyNomtRoot(64000, true)

				isZeroHashHex := func(h string) bool {
					trimmed := strings.TrimPrefix(strings.ToLower(h), "0x")
					if trimmed == "" {
						return true
					}
					for i := 0; i < len(trimmed); i++ {
						if trimmed[i] != '0' {
							return false
						}
					}
					return true
				}

				isMetadataZero := isZeroHashHex(metadata.StateRoot)
				isNomtZeroOrEmpty := nomtRoot == (common.Hash{}) || nomtRoot == emptyAccountRoot || nomtRoot == emptyStakeRoot
				isBlock0ZeroState := metadata.BlockNumber == 0 && isMetadataZero
				isNomtHeaderMatch := nomtRoot == headerStateRoot

				if (isMetadataZero && isNomtZeroOrEmpty) || (isBlock0ZeroState && isNomtHeaderMatch) || strings.ToLower(nomtRootHex) == strings.ToLower(metadataRootHex) {
					isSnapshotRecovery = true
				}
			}

			if nomtRoot != headerStateRoot && !isSnapshotRecovery {
				result.CriticalErrors = append(result.CriticalErrors,
					fmt.Sprintf("NOMT account_state root MISMATCH: header=%s, NOMT=%s — state is corrupted",
						headerStateRoot.Hex()[:18]+"...", nomtRoot.Hex()[:18]+"..."))
			} else if isSnapshotRecovery {
				logger.Info("✅ [INTEGRITY] CHECK 4/5: NOMT account_state root matches snapshot metadata StateRoot (%s). Bypassing header state mismatch.",
					metadata.StateRoot)
			} else {
				logger.Info("✅ [INTEGRITY] CHECK 4/5: NOMT account_state root matches header: %s",
					nomtRoot.Hex()[:18]+"...")
			}
		} else {
			// NOMT not initialized yet — this is OK during early startup
			logger.Info("⏭️  [INTEGRITY] CHECK 4/5: NOMT account_state not initialized yet — skipped")
		}
	} else {
		logger.Info("⏭️  [INTEGRITY] CHECK 4/5: Header has empty stateRoot — skipped (genesis?)")
	}

	// ──────────────────────────────────────────────────────────────────
	// CHECK 5: Global state counters consistency
	// ──────────────────────────────────────────────────────────────────
	lastBlockNumber := storage.GetLastBlockNumber()
	if lastBlockNumber < lastBlockNum {
		result.Warnings = append(result.Warnings,
			fmt.Sprintf("lastBlockNumber in RAM (%d) < lastBlock header (%d) — will be corrected by UpdateLastBlockNumber",
				lastBlockNumber, lastBlockNum))
	}
	logger.Info("✅ [INTEGRITY] CHECK 5/5: Global state counters: lastBlockNumber=%d, lastBlock=#%d, GEI=%d",
		lastBlockNumber, lastBlockNum, app.startLastBlock.Header().GlobalExecIndex())

	// ──────────────────────────────────────────────────────────────────
	// SUMMARY
	// ──────────────────────────────────────────────────────────────────
	logger.Info("═══════════════════════════════════════════════════════════")
	if len(result.CriticalErrors) > 0 {
		logger.Error("🚨 [INTEGRITY] %d CRITICAL ERROR(s) detected:", len(result.CriticalErrors))
		for i, err := range result.CriticalErrors {
			logger.Error("   ❌ [%d/%d] %s", i+1, len(result.CriticalErrors), err)
		}
	}
	if len(result.Warnings) > 0 {
		logger.Warn("⚠️  [INTEGRITY] %d WARNING(s) detected:", len(result.Warnings))
		for i, w := range result.Warnings {
			logger.Warn("   ⚠️  [%d/%d] %s", i+1, len(result.Warnings), w)
		}
	}
	if len(result.CriticalErrors) == 0 && len(result.Warnings) == 0 {
		logger.Info("✅ [INTEGRITY] All checks passed — data is consistent")
	}
	logger.Info("═══════════════════════════════════════════════════════════")

	return result
}

// verifyBlockChain walks backwards from startBlock verifying:
//   - Each block is retrievable from DB by its hash
//   - parentHash of block N matches hash of block N-1
//
// Returns the block number where the chain broke (0 if no break).
func (app *App) verifyBlockChain(blockDB interface{ GetBlockByHash(common.Hash) (types.Block, error) }, startBlock types.Block, maxBlocks int, result *IntegrityCheckResult) uint64 {
	current := startBlock
	checked := 0
	
	lastPrunedBlock := uint64(0)
	if bc := blockchain.GetBlockChainInstance(); bc != nil {
		lastPrunedBlock = bc.GetLastPrunedBlockNumber()
	}

	for i := 0; i < maxBlocks; i++ {
		blockNum := current.Header().BlockNumber()
		if blockNum == 0 {
			// Reached genesis — chain is complete
			checked++
			break
		}

		if blockNum <= lastPrunedBlock + 1 && lastPrunedBlock > 0 {
			// Reached pruning boundary - chain before this is already pruned
			logger.Info("✅ [INTEGRITY] Reached pruned boundary at block #%d (lastPruned=%d). Chain walk complete.", blockNum, lastPrunedBlock)
			checked++
			break
		}

		parentHash := current.Header().LastBlockHash()
		if parentHash == (common.Hash{}) {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("Block #%d has empty parentHash — possibly genesis or corrupted header", blockNum))
			break
		}

		parentBlock, err := blockDB.GetBlockByHash(parentHash)
		if err != nil {
			logger.Error("❌ [INTEGRITY] Block #%d references parent %s but parent NOT FOUND in DB: %v",
				blockNum, parentHash.Hex()[:18]+"...", err)
			result.CheckedBlocks = checked
			return blockNum
		}

		parentNum := parentBlock.Header().BlockNumber()
		if parentNum != blockNum-1 {
			logger.Error("❌ [INTEGRITY] Block #%d references parent %s which has blockNumber=%d (expected %d)",
				blockNum, parentHash.Hex()[:18]+"...", parentNum, blockNum-1)
			result.CheckedBlocks = checked
			return blockNum
		}

		checked++
		current = parentBlock
	}

	result.CheckedBlocks = checked
	return 0 // No break
}

// handleIntegrityResult processes the integrity check result.
// If critical errors are found and cannot be self-healed → log snapshot restore instructions and exit.
// If only warnings → log and continue (self-healing mechanisms will fix them).
func (app *App) handleIntegrityResult(result *IntegrityCheckResult) {
	if len(result.CriticalErrors) == 0 {
		return // All good
	}

	// ═══════════════════════════════════════════════════════════════════
	// CRITICAL: Data corruption detected — node CANNOT safely continue.
	// Print actionable instructions for the ops team.
	// ═══════════════════════════════════════════════════════════════════
	logger.Error("╔══════════════════════════════════════════════════════════════╗")
	logger.Error("║  🚨 CRITICAL: DATA INTEGRITY CHECK FAILED                  ║")
	logger.Error("║  Node CANNOT safely start with corrupted data.             ║")
	logger.Error("║                                                            ║")
	logger.Error("║  ACTION REQUIRED:                                          ║")
	logger.Error("║  1. Stop this node immediately                             ║")
	logger.Error("║  2. Restore from the latest snapshot:                       ║")
	logger.Error("║     ./mtn-orchestrator.sh restore-node <node_id>           ║")
	logger.Error("║  3. If no snapshot available, re-sync from other nodes:     ║")
	logger.Error("║     ./mtn-orchestrator.sh resync-node <node_id>            ║")
	logger.Error("║  4. If this is the only node, contact the dev team.        ║")
	logger.Error("║                                                            ║")
	logger.Error("║  Errors found:                                             ║")
	for _, err := range result.CriticalErrors {
		logger.Error("║  ❌ %-55s ║", truncateStr(err, 55))
	}
	logger.Error("╚══════════════════════════════════════════════════════════════╝")

	// ═══════════════════════════════════════════════════════════════════
	// SENTINEL FILE for CI/CD Detection (May 2026)
	// Write /tmp/MTN_INTEGRITY_FAILED so that test-node-recovery-gap.sh
	// and ci_recovery_monitor.py can distinguish between:
	//   - Regular crash (panic, OOM) → "Node crashed"
	//   - Data integrity failure     → "Node refused to start due to corrupted data"
	// The sentinel includes error details for Telegram notifications.
	// ═══════════════════════════════════════════════════════════════════
	sentinelContent := "DATA_INTEGRITY_CHECK_FAILED\n"
	sentinelContent += fmt.Sprintf("timestamp=%d\n", os.Getpid())
	sentinelContent += fmt.Sprintf("node_address=%s\n", app.config.Address)
	sentinelContent += fmt.Sprintf("root_path=%s\n", app.config.Databases.RootPath)
	for i, err := range result.CriticalErrors {
		sentinelContent += fmt.Sprintf("error_%d=%s\n", i+1, err)
	}
	sentinelContent += "action=RESTORE_FROM_SNAPSHOT\n"
	if writeErr := os.WriteFile("/tmp/MTN_INTEGRITY_FAILED", []byte(sentinelContent), 0644); writeErr != nil {
		logger.Error("⚠️ Failed to write integrity sentinel file: %v", writeErr)
	}

	// EXIT with error code 78 (EX_CONFIG) — data is not suitable for operation
	logger.Error("🛑 Exiting with code 78 (EX_CONFIG) — restore from snapshot to fix.")
	os.Exit(78)
}

// truncateStr truncates a string to maxLen, adding "..." if truncated.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
