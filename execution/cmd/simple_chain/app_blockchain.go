package main

import (
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
		nomtPath := dataDir + "/nomt_db/account_state"
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
		
		// Also check NOMT HT file presence
		if _, err := os.Stat(nomtPath + "/ht"); err == nil {
			hasExistingData = true
		}

		if hasExistingData {
			if _, err := os.Stat(metadataPath); err == nil {
				logger.Warn("⚠️ GetLastBlock() failed, but metadata.json exists! Bypassing panic to recover from snapshot.")
				var md executor.SnapshotMetadata
				if metadataBytes, err := os.ReadFile(metadataPath); err == nil {
					if jsonErr := json.Unmarshal(metadataBytes, &md); jsonErr == nil {
						app.startLastBlock = block.NewBlock(
							block.NewBlockHeader(e_common.HexToHash(""), md.BlockNumber, e_common.HexToHash(md.StateRoot), e_common.HexToHash(""), e_common.HexToHash(""), e_common.Address{}, 0, trie.EmptyRootHash, uint64(md.Epoch), md.GlobalExecIndex),
							nil, nil,
						)
					}
				}
				if app.startLastBlock != nil {
					storage.UpdateLastBlockNumber(app.startLastBlock.Header().BlockNumber())
					storage.UpdateLastGlobalExecIndex(app.startLastBlock.Header().GlobalExecIndex())
					app.GetAccountStateTrie(app.startLastBlock.Header().AccountStatesRoot())
					app.chainState, _ = blockchain.NewChainStateWithGenesis(app.storageManager, blockDatabase, app.startLastBlock.Header(), app.config, FreeFeeAddresses, &app.genesis.Config, app.config.BackupPath)
					trie_database.CreateTrieDatabaseManager(app.storageManager.GetStorageDatabaseTrie(), app.chainState.GetAccountStateDB())
					blockchain.InitBlockChain(100, blockDatabase, app.storageManager)
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
			logger.Info("✅ [STARTUP] BackupDb GEI (%d) is higher than block header GEI (%d). Using BackupDb value to preserve empty commits.", backupGEI, headerGEI)
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

		// ─── Initialize LastHandledCommitIndex ──────────
		headerCommitIndex := uint32(app.startLastBlock.Header().CommitIndex())
		
		var backupCommitIndex uint32 = 0
		if app.storageManager != nil && app.storageManager.GetStorageBackupDb() != nil {
			if commitIdxBytes, err := app.storageManager.GetStorageBackupDb().Get(storage.LastHandledCommitIndexHashKey.Bytes()); err == nil && len(commitIdxBytes) > 0 {
				if parsedIdx, err := utils.BytesToUint32(commitIdxBytes); err == nil {
					backupCommitIndex = parsedIdx
				}
			}
		}

		targetCommitIndex := headerCommitIndex
		if backupCommitIndex > headerCommitIndex {
			targetCommitIndex = backupCommitIndex
			logger.Info("✅ [STARTUP] BackupDb CommitIndex (%d) is higher than block header CommitIndex (%d). Using BackupDb value.", backupCommitIndex, headerCommitIndex)
		} else if headerCommitIndex > 0 {
			logger.Info("✅ [STARTUP] Initialized LastHandledCommitIndex from last block header: %d", headerCommitIndex)
		} else {
			logger.Info("ℹ️  [STARTUP] Defaulted LastHandledCommitIndex to 0 (not found in BackupDb or header)")
		}

		if targetCommitIndex > 0 {
			storage.UpdateLastHandledCommitIndex(targetCommitIndex)
		} else {
			storage.UpdateLastHandledCommitIndex(0)
		}

		// ─── Startup State Sync Logging ────────────────────────────────
		logger.Info("🔒 [STARTUP-SYNC] Go Master state loaded from LevelDB: block=%d, account_root=%s",
			app.startLastBlock.Header().BlockNumber(),
			app.startLastBlock.Header().AccountStatesRoot().Hex())

		// Create account state trie from existing root and cache it
		_, err := app.GetAccountStateTrie(app.startLastBlock.Header().AccountStatesRoot())
		if err != nil {
			return fmt.Errorf("failed to create account state trie: %v", err)
		}

		app.chainState, err = blockchain.NewChainStateWithGenesis(app.storageManager, blockDatabase, app.startLastBlock.Header(), app.config, FreeFeeAddresses, &app.genesis.Config, app.config.BackupPath)
		if err != nil {
			return fmt.Errorf("failed NewChainState: %v", err)
		}
		// Note: SetBackupPath is no longer needed - backupPath is set in constructor

		// Initialize trie database manager
		trie_database.CreateTrieDatabaseManager(
			app.storageManager.GetStorageDatabaseTrie(),
			app.chainState.GetAccountStateDB())

		// Initialize blockchain
		blockchain.InitBlockChain(100, blockDatabase, app.storageManager)
	}

SKIP_GENESIS:

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
					panic("FATAL: Snapshot restore failed. NOMT state corrupted or mismatched with metadata.")
				}

				// Enforce GEI and BlockNumber globally from metadata to prevent any inflation
				logger.Info("🛡️ [SNAPSHOT FIX] ✅ Restored perfectly aligned state from metadata (Block=%d, GEI=%d)",
					metadata.BlockNumber, metadata.GlobalExecIndex)

				storage.ForceSetLastGlobalExecIndex(metadata.GlobalExecIndex)
				storage.ForceSetLastBlockNumber(metadata.BlockNumber)

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
					logger.Error("🛡️ [SNAPSHOT FIX] ⚠️ Could not find fallback block matching NOMT root %s! "+
						"GEI may be inflated. Snapshot restore may produce forks.",
						nomtRoot.Hex()[:18]+"...")
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
		genesisTimestamp = app.genesis.Config.EpochTimestampMs / 1000 // Convert ms to seconds
		logger.Info("✅ [GENESIS] Using timestamp from genesis.json: %d ms -> %d s",
			app.genesis.Config.EpochTimestampMs, genesisTimestamp)
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
		if pubkeyBls == "" {
			pubkeyBls = val.GetPubkeyBls()
		}
		pubkeySecp := val.GetProtocolKey()
		if pubkeySecp == "" {
			pubkeySecp = val.GetPubkeySecp()
		}
		networkKey := val.GetNetworkKey()
		if networkKey == "" {
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
			pubkeyBls,
			pubkeySecp, // protocol_key (Ed25519)
			networkKey, // network_key (Ed25519)
			name,       // hostname (use validator name)
			pubkeyBls,  // authority_key (BLS public key)
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

	app.startLastBlock.Header().SetStakeStatesRoot(hashStake)
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
				logger.Info("FreeFeeAddresses: ", key)
			}
		}
	} else {
		logger.Error("[FATAL] FreeFeeAddresses in config.json is not an array")
		logger.SyncFileLog()
		os.Exit(1)
	}
}
