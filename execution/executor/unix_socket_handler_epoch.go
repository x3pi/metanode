package executor

import (
	"fmt"
	"math/big"
	"sort"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
)

// HandleGetActiveValidatorsRequest processes a GetActiveValidatorsRequest and returns a ValidatorInfoList.
// Returns only active validators (not jailed and with stake > 0) for epoch transition.
func (rh *RequestHandler) HandleGetActiveValidatorsRequest(request *pb.GetActiveValidatorsRequest) (*pb.ValidatorInfoList, error) {
	logger.Info("Handling GetActiveValidatorsRequest for epoch transition")

	// Get all validators from current state
	validators, err := rh.chainState.GetStakeStateDB().GetAllValidators()
	if err != nil {
		return nil, fmt.Errorf("could not get all validators from stake DB: %w", err)
	}

	// CRITICAL: Sort validators by AuthorityKey (BLS public key) as STRING to ensure consistent ordering with Rust
	// Rust uses: sorted_validators.sort_by(|a, b| a.authority_key.cmp(&b.authority_key))
	// Go must use the SAME string comparison to produce identical ordering
	sort.Slice(validators, func(i, j int) bool {
		return validators[i].AuthorityKey() < validators[j].AuthorityKey()
	})

	// Filter: only active validators (not jailed, with stake > 0)
	validatorInfoList := &pb.ValidatorInfoList{}
	for _, dbValidator := range validators {
		// Skip jailed validators
		if dbValidator.IsJailed() {
			logger.Debug("Skipping jailed validator", "address", dbValidator.Address().Hex())
			continue
		}

		// Skip validators with zero stake
		totalStake := dbValidator.TotalStakedAmount()
		if totalStake == nil || totalStake.Sign() <= 0 {
			logger.Debug("Skipping validator with zero stake", "address", dbValidator.Address().Hex())
			continue
		}

		// Map validator to ValidatorInfo (fields needed for committee.json + metadata + fees/rewards)
		// CRITICAL: Use separate protocol_key and network_key for committee.json compatibility
		protocolKey := dbValidator.ProtocolKey()
		networkKey := dbValidator.NetworkKey()
		// Normalize stake: divide by 10^18 (same as Rust)
		// Example: 1000000000000000000 (1 token) -> "1"
		const precision = 1_000_000_000_000_000_000 // 10^18
		stakeNormalized := new(big.Int).Div(totalStake, big.NewInt(precision))
		if stakeNormalized.Sign() <= 0 {
			stakeNormalized = big.NewInt(1) // Minimum stake of 1
		}
		val := &pb.ValidatorInfo{
			// FIXED: Address = Ethereum wallet address (matches ValidatorState.Address())
			Address:      dbValidator.Address().Hex(),
			Stake:        stakeNormalized.String(),
			AuthorityKey: dbValidator.AuthorityKey(),
			ProtocolKey:  protocolKey, // Protocol key (Ed25519) - compatible with committee.json
			NetworkKey:   networkKey,  // Network key (Ed25519) - compatible with committee.json

			// Metadata
			Name:        dbValidator.Name(),
			Description: dbValidator.Description(),
			Website:     dbValidator.Website(),
			Image:       dbValidator.Image(),

			// Commission and rewards fields
			CommissionRate:             dbValidator.CommissionRate(),
			MinSelfDelegation:          dbValidator.MinSelfDelegation().String(),
			AccumulatedRewardsPerShare: dbValidator.AccumulatedRewardsPerShare().String(),

			// NEW: P2P address for committee.json network communication
			P2PAddress: dbValidator.P2PAddress(),
		}
		validatorInfoList.Validators = append(validatorInfoList.Validators, val)
	}

	logger.Info("Returning active validators for epoch transition", "count", len(validatorInfoList.Validators))
	return validatorInfoList, nil
}

// HandleGetValidatorsAtBlockRequest processes a GetValidatorsAtBlockRequest and returns a ValidatorInfoList.
// Retrieves validators at a specific block (block 0 for genesis, last_global_exec_index for epoch transition).
// CRITICAL FOR SNAPSHOT: Only returns validators when block has been committed to DB (ensures snapshot consistency).
func (rh *RequestHandler) HandleGetValidatorsAtBlockRequest(request *pb.GetValidatorsAtBlockRequest) (*pb.ValidatorInfoList, error) {
	blockNumber := request.GetBlockNumber()
	logger.Info("🔍 [SNAPSHOT] Handling GetValidatorsAtBlockRequest for block %d (Rust checking if Go executor has processed this block)", blockNumber)
	logger.Debug("🔍 [SNAPSHOT] Handling GetValidatorsAtBlockRequest for block %d", blockNumber)

	// CRITICAL FOR SNAPSHOT: Verify block has been committed to DB
	// Ensures block is committed before returning validators (snapshot consistency)
	lastCommittedBlockNumber := storage.GetLastBlockNumber()
	logger.Info("🔍 [SNAPSHOT] Block commit status: requested_block=%d, last_committed_block=%d", blockNumber, lastCommittedBlockNumber)
	logger.Debug("🔍 [SNAPSHOT] Block commit status: requested_block=%d, last_committed_block=%d", blockNumber, lastCommittedBlockNumber)

	// If block has not been committed to DB yet, return error (Rust will retry)
	if blockNumber > lastCommittedBlockNumber {
		errMsg := fmt.Sprintf("block %d has not been committed to DB yet (last committed: %d). Go executor is still processing this block", blockNumber, lastCommittedBlockNumber)
		logger.Warn("⚠️  [SNAPSHOT] %s", errMsg)
		logger.Warn("⚠️  [SNAPSHOT] %s", errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	// Get block at blockNumber
	// Special handling for block 0 (genesis) — may not exist if Go Master hasn't initialized genesis yet
	blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(blockNumber)
	logger.Debug("🔍 [SNAPSHOT] GetBlockHashByNumber(%d): ok=%v, hash=%s", blockNumber, ok, blockHash)
	logger.Info("🔍 [SNAPSHOT] GetBlockHashByNumber(%d): ok=%v, hash=%s", blockNumber, ok, blockHash)
	if !ok {
		if blockNumber == 0 {
			// Block 0 doesn't exist yet — Go Master may not have initialized genesis block
			// In this case, get validators from current state (genesis)
			logger.Warn("Block 0 not found, getting validators from current state (genesis fallback)")
			// Fallback: get validators from current state instead of block 0
			logger.Debug("🔍 [EPOCH] Getting validators from stake state DB...")
			logger.Info("🔍 [DEBUG] Getting validators from stake state DB...")
			validators, err := rh.chainState.GetStakeStateDB().GetAllValidators()
			if err != nil {
				logger.Error("🔍 [EPOCH] ERROR getting validators from stake state DB: %v", err)
				logger.Error("🔍 [DEBUG] ERROR getting validators from stake state DB: %v", err)
				return nil, fmt.Errorf("cannot get validators from current state (genesis not initialized): %w", err)
			}

			logger.Debug("🔍 [EPOCH] Found %d total validators in state (before filtering)", len(validators))
			logger.Info("🔍 [DEBUG] Found %d total validators in state (before filtering)", len(validators))

			if len(validators) == 0 {
				logger.Warn("🔍 [EPOCH] ⚠️  WARNING: GetAllValidators() returned 0 validators! This means Go has not initialized genesis block or validators were not registered.")
				logger.Warn("🔍 [DEBUG] ⚠️  WARNING: GetAllValidators() returned 0 validators! This means Go has not initialized genesis block or validators were not registered.")
			} else {
				// Log details of each validator found
				for i, val := range validators {
					stake := val.TotalStakedAmount()
					logger.Debug("🔍 [EPOCH] Validator[%d] from DB: address=%s, name=%s, stake=%s, jailed=%v, p2p=%s",
						i, val.Address().Hex(), val.Name(), stake.String(), val.IsJailed(), val.P2PAddress())
					logger.Info("🔍 [DEBUG] Validator[%d] from DB: address=%s, name=%s, stake=%s, jailed=%v, p2p=%s",
						i, val.Address().Hex(), val.Name(), stake.String(), val.IsJailed(), val.P2PAddress())
				}
			}

			// CRITICAL: Sort validators by AuthorityKey (BLS public key) as STRING to ensure consistent ordering with Rust
			// Rust uses: sorted_validators.sort_by(|a, b| a.authority_key.cmp(&b.authority_key))
			// Go must use the SAME string comparison to produce identical ordering
			sort.Slice(validators, func(i, j int) bool {
				return validators[i].AuthorityKey() < validators[j].AuthorityKey()
			})

			validatorInfoList := &pb.ValidatorInfoList{}
			skippedJailed := 0
			validatorsWithMinStake := 0
			for _, dbValidator := range validators {
				if dbValidator.IsJailed() {
					skippedJailed++
					logger.Debug("Skipping jailed validator: %s", dbValidator.Address().Hex())
					continue
				}
				totalStake := dbValidator.TotalStakedAmount()

				// CRITICAL FIX: For genesis (block 0), include validators even if they have no stake yet
				// This allows Rust to start up and create committee from genesis validators
				// Validators will get stake through delegation later
				var stakeNormalized *big.Int
				if totalStake == nil || totalStake.Sign() <= 0 {
					// Genesis validators may not have stake yet - use minimum stake of 1000000
					logger.Info("Genesis validator %s has no stake yet, using minimum stake of 1000000", dbValidator.Address().Hex())
					stakeNormalized = big.NewInt(1000000)
					validatorsWithMinStake++
				} else {
					const precision = 1_000_000_000_000_000_000
					stakeNormalized = new(big.Int).Div(totalStake, big.NewInt(precision))
					if stakeNormalized.Sign() <= 0 {
						stakeNormalized = big.NewInt(1)
						validatorsWithMinStake++
					}
				}

				// CRITICAL: Use separate protocol_key and network_key for committee.json compatibility
				protocolKey := dbValidator.ProtocolKey()
				networkKey := dbValidator.NetworkKey()
				val := &pb.ValidatorInfo{
					Address:                    dbValidator.Address().Hex(), // FIXED: Ethereum wallet address
					Stake:                      stakeNormalized.String(),
					AuthorityKey:               dbValidator.AuthorityKey(),
					ProtocolKey:                protocolKey, // Protocol key (Ed25519) - compatible with committee.json
					NetworkKey:                 networkKey,  // Network key (Ed25519) - compatible with committee.json
					Name:                       dbValidator.Name(),
					Description:                dbValidator.Description(),
					Website:                    dbValidator.Website(),
					Image:                      dbValidator.Image(),
					CommissionRate:             dbValidator.CommissionRate(),
					MinSelfDelegation:          dbValidator.MinSelfDelegation().String(),
					AccumulatedRewardsPerShare: dbValidator.AccumulatedRewardsPerShare().String(),
					P2PAddress:                 dbValidator.P2PAddress(), // NEW: P2P address for committee
				}

				// CRITICAL: Log ValidatorInfo exactly as Rust will receive it
				authKeyPreview := val.AuthorityKey
				if len(authKeyPreview) > 50 {
					authKeyPreview = authKeyPreview[:50] + "..."
				}
				logger.Debug("🔍 [EPOCH] 📤 [GO→RUST] ValidatorInfo[%d]: address=%s, stake=%s, name=%s, authority_key=%s, protocol_key=%s, network_key=%s, p2p_address='%s'",
					len(validatorInfoList.Validators), val.Address, val.Stake, val.Name, authKeyPreview, val.ProtocolKey, val.NetworkKey, val.P2PAddress)
				logger.Info("🔍 [DEBUG] 📤 [GO→RUST] ValidatorInfo[%d]: address=%s, stake=%s, name=%s, authority_key=%s, protocol_key=%s, network_key=%s, p2p_address='%s'",
					len(validatorInfoList.Validators), val.Address, val.Stake, val.Name, authKeyPreview, val.ProtocolKey, val.NetworkKey, val.P2PAddress)

				validatorInfoList.Validators = append(validatorInfoList.Validators, val)
			}

			// Set epoch_timestamp_ms and last_global_exec_index for genesis
			// Load genesis timestamp for genesis block
			if rh.genesisPath != "" {
				if genesisData, err := config.LoadGenesisData(rh.genesisPath); err == nil {
					if genesisData.Config.EpochTimestampMs > 0 {
						validatorInfoList.EpochTimestampMs = genesisData.Config.EpochTimestampMs
						logger.Info("✅ Loaded epoch_timestamp_ms from genesis.json: %d", validatorInfoList.EpochTimestampMs)
					} else {
						// FORK-SAFETY FIX: Deterministic fallback instead of time.Now()
						validatorInfoList.EpochTimestampMs = 1
						logger.Error("🚨 [FORK-SAFETY] No epoch_timestamp_ms in genesis.json! Using deterministic fallback=1. Fix genesis config.")
					}
				} else {
					validatorInfoList.EpochTimestampMs = 1
					logger.Error("🚨 [FORK-SAFETY] Could not load genesis.json: %v. Using deterministic fallback=1", err)
				}
			} else {
				validatorInfoList.EpochTimestampMs = 1
				logger.Error("🚨 [FORK-SAFETY] No genesis path configured. Using deterministic fallback=1")
			}
			validatorInfoList.LastGlobalExecIndex = 0 // Genesis block

			logger.Info("🔍 [DEBUG] Returning validators from current state (genesis fallback): count=%d (skipped: %d jailed, %d had no stake but included with min stake=1), epoch_timestamp_ms=%d, last_global_exec_index=0",
				len(validatorInfoList.Validators), skippedJailed, validatorsWithMinStake, validatorInfoList.EpochTimestampMs)
			logger.Debug("🔍 [EPOCH] Returning validators from current state: count=%d (skipped: %d jailed, %d had no stake but included with min stake=1)",
				len(validatorInfoList.Validators), skippedJailed, validatorsWithMinStake)

			if len(validatorInfoList.Validators) == 0 {
				logger.Warn("⚠️  No validators found in state! This may indicate:")
				logger.Warn("  1. Genesis validators not initialized")
				logger.Warn("  2. All validators are jailed or have no stake")
				logger.Warn("  3. Stake state DB is empty")
			}

			return validatorInfoList, nil
		}
		return nil, fmt.Errorf("cannot find block hash for block number %d", blockNumber)
	}

	blockData, err := rh.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
	if err != nil {
		return nil, fmt.Errorf("could not get block data by hash %s: %w", blockHash, err)
	}

	// ═══════════════════════════════════════════════════════════════════════
	// NOMT FIX: NOMT doesn't support historical state lookups.
	// Creating a temporary ChainState at a historical block creates a new
	// NomtStateTrie that reads from the SAME global state but may not have
	// the knownKeys registry populated correctly.
	// For NOMT, use the live StakeStateDB directly — it always has the
	// correct knownKeys and reads current state (which is the only state
	// available in NOMT anyway).
	// ═══════════════════════════════════════════════════════════════════════
	var validators []state.ValidatorState

	if trie.GetStateBackend() == trie.BackendNOMT {
		logger.Info("🔍 [EPOCH] Using LIVE StakeStateDB for NOMT backend (block=%d, NOMT has no historical roots)", blockNumber)
		validators, err = rh.chainState.GetStakeStateDB().GetAllValidators()
	} else {
		// MPT/Flat/Verkle: Create historical ChainState at specific block root
		blockDatabase := block.NewBlockDatabase(rh.storageManager.GetStorageBlock())
		chainStateAtBlock, csErr := blockchain.NewChainState(
			rh.storageManager,
			blockDatabase,
			blockData.Header(),
			rh.chainState.GetConfig(),
			rh.chainState.GetFreeFeeAddress(),
			"", // Empty backupPath for temporary chain state
		)
		if csErr != nil {
			return nil, fmt.Errorf("could not create chain state at block %d: %w", blockNumber, csErr)
		}

		// Get all validators from state at this block
		validators, err = chainStateAtBlock.GetStakeStateDB().GetAllValidators()
	}
	if err != nil {
		return nil, fmt.Errorf("could not get all validators from stake DB at block %d: %w", blockNumber, err)
	}

	// CRITICAL: Sort validators by AuthorityKey (BLS public key) as STRING to ensure consistent ordering with Rust
	// Rust uses: sorted_validators.sort_by(|a, b| a.authority_key.cmp(&b.authority_key))
	// Go must use the SAME string comparison to produce identical ordering
	sort.Slice(validators, func(i, j int) bool {
		return validators[i].AuthorityKey() < validators[j].AuthorityKey()
	})

	// Filter: only active validators (not jailed, with stake > 0)
	// CRITICAL FIX: For block 0 (genesis), include validators even if they have no stake yet
	validatorInfoList := &pb.ValidatorInfoList{}
	skippedJailed := 0
	validatorsWithMinStake := 0
	for _, dbValidator := range validators {
		// Skip jailed validators
		if dbValidator.IsJailed() {
			skippedJailed++
			logger.Debug("Skipping jailed validator at block %d", blockNumber, "address", dbValidator.Address().Hex())
			continue
		}

		// CRITICAL FIX: For genesis (block 0), include validators even if they have no stake yet
		// This allows Rust to start up and create committee from genesis validators
		// Validators will get stake through delegation later
		totalStake := dbValidator.TotalStakedAmount()
		var stakeNormalized *big.Int
		if totalStake == nil || totalStake.Sign() <= 0 {
			// Genesis validators may not have stake yet - use minimum stake of 1000
			if blockNumber == 0 {
				logger.Info("Genesis validator %s has no stake yet, using minimum stake of 1000000", dbValidator.Address().Hex())
				logger.Debug("🔍 [EPOCH] Genesis validator %s has no stake yet, using minimum stake of 1000000", dbValidator.Address().Hex())
				stakeNormalized = big.NewInt(1000000)
				validatorsWithMinStake++
			} else {
				// For non-genesis blocks, skip validators with zero stake
				logger.Debug("Skipping validator with zero stake at block %d", blockNumber, "address", dbValidator.Address().Hex())
				continue
			}
		} else {
			const precision = 1_000_000_000_000_000_000
			stakeNormalized = new(big.Int).Div(totalStake, big.NewInt(precision))
			if stakeNormalized.Sign() <= 0 {
				stakeNormalized = big.NewInt(1)
				validatorsWithMinStake++
			}
		}

		// Map validator to ValidatorInfo
		// CRITICAL: Use separate protocol_key and network_key for committee.json compatibility
		protocolKey := dbValidator.ProtocolKey()
		networkKey := dbValidator.NetworkKey()
		val := &pb.ValidatorInfo{
			Address:                    dbValidator.Address().Hex(), // FIXED: Ethereum wallet address
			Stake:                      stakeNormalized.String(),
			AuthorityKey:               dbValidator.AuthorityKey(),
			ProtocolKey:                protocolKey, // Protocol key (Ed25519) - compatible with committee.json
			NetworkKey:                 networkKey,  // Network key (Ed25519) - compatible with committee.json
			Name:                       dbValidator.Name(),
			Description:                dbValidator.Description(),
			Website:                    dbValidator.Website(),
			Image:                      dbValidator.Image(),
			CommissionRate:             dbValidator.CommissionRate(),
			MinSelfDelegation:          dbValidator.MinSelfDelegation().String(),
			AccumulatedRewardsPerShare: dbValidator.AccumulatedRewardsPerShare().String(),
			P2PAddress:                 dbValidator.P2PAddress(), // NEW: P2P address for committee
		}

		// CRITICAL: Log ValidatorInfo exactly as Rust will receive it
		authKeyPreview := val.AuthorityKey
		if len(authKeyPreview) > 50 {
			authKeyPreview = authKeyPreview[:50] + "..."
		}
		logger.Debug("🔍 [EPOCH] 📤 [GO→RUST] ValidatorInfo[%d]: address=%s, stake=%s, name=%s, authority_key=%s, protocol_key=%s, network_key=%s",
			len(validatorInfoList.Validators), val.Address, val.Stake, val.Name, authKeyPreview, val.ProtocolKey, val.NetworkKey)
		logger.Info("🔍 [DEBUG] 📤 [GO→RUST] ValidatorInfo[%d]: address=%s, stake=%s, name=%s, authority_key=%s, protocol_key=%s, network_key=%s",
			len(validatorInfoList.Validators), val.Address, val.Stake, val.Name, authKeyPreview, val.ProtocolKey, val.NetworkKey)

		validatorInfoList.Validators = append(validatorInfoList.Validators, val)
	}

	// Get epoch_timestamp_ms and last_global_exec_index from Go chain state
	// epoch_timestamp_ms: epoch start timestamp from Go state (authoritative)
	// last_global_exec_index: tracks all commits including empty ones

	// CRITICAL FIX: Get epoch timestamp from Go chain state, not block header
	epochTimestampMs := rh.chainState.GetCurrentEpochStartTimestampMs()
	if epochTimestampMs == 0 {
		// Fallback: always derive from boundary block header (deterministic across nodes)
		blockHeader := blockData.Header()
		epochTimestampMs = blockHeader.TimeStamp() * 1000 // Convert seconds to milliseconds
		if epochTimestampMs == 0 {
			// FORK-SAFETY FIX: Block header has timestamp=0 (genesis or broken state)
			// Using deterministic fallback based on blockNumber instead of time.Now()
			epochTimestampMs = blockNumber * 1000 // Deterministic: same across all nodes
			if epochTimestampMs == 0 {
				epochTimestampMs = 1 // Avoid zero for genesis block
			}
			logger.Error("🚨 [EPOCH TIMESTAMP] Block header timestamp is 0! Using deterministic fallback: %d ms (block=%d)", epochTimestampMs, blockNumber)
		} else {
			logger.Info("✅ [EPOCH TIMESTAMP] Derived from boundary block %d header: %d ms", blockNumber, epochTimestampMs)
		}
	} else {
		logger.Info("✅ [EPOCH TIMESTAMP] Using persisted epoch timestamp from Go state: %d ms", epochTimestampMs)
	}

	// LastGlobalExecIndex is now decoupled from blockNumber
	// Use the persisted LastGlobalExecIndex for epoch transition accuracy
	lastGlobalExecIndex := storage.GetLastGlobalExecIndex()
	if lastGlobalExecIndex == 0 {
		// Fallback: legacy mode (blockNumber == globalExecIndex)
		lastGlobalExecIndex = blockNumber
	}
	validatorInfoList.EpochTimestampMs = epochTimestampMs
	validatorInfoList.LastGlobalExecIndex = lastGlobalExecIndex

	// CRITICAL FOR SNAPSHOT: Confirm block commitment to DB
	logger.Info("✅ [SNAPSHOT] Returning validators at block %d (COMMITTED TO DB): count=%d (skipped: %d jailed, %d had no stake but included with min stake=1), epoch_timestamp_ms=%d (adjusted for genesis), last_global_exec_index=%d, last_committed_block=%d",
		blockNumber, len(validatorInfoList.Validators), skippedJailed, validatorsWithMinStake, epochTimestampMs, lastGlobalExecIndex, lastCommittedBlockNumber)
	logger.Info("✅ [SNAPSHOT] Returning validators at block %d (COMMITTED TO DB): count=%d (skipped: %d jailed, %d had no stake but included with min stake=1), last_committed_block=%d",
		blockNumber, len(validatorInfoList.Validators), skippedJailed, validatorsWithMinStake, lastCommittedBlockNumber)
	return validatorInfoList, nil
}

// HandleGetLastBlockNumberRequest processes a GetLastBlockNumberRequest and returns a LastBlockNumberResponse.
// Used to initialize next_expected_index in Rust executor_client.
// CRITICAL FIX: Validate that block hash exists before returning, not just the counter!
func (rh *RequestHandler) HandleGetLastBlockNumberRequest(request *pb.GetLastBlockNumberRequest) (*pb.LastBlockNumberResponse, error) {
	logger.Debug("🔍 [INIT] Handling GetLastBlockNumberRequest (Rust executor_client initializing next_expected_index)")

	// Get last block number from counter
	counterBlockNumber := storage.GetLastBlockNumber()
	logger.Debug("🔍 [INIT] Block counter from storage: %d", counterBlockNumber)

	// CRITICAL FIX: Validate that block hash actually exists!
	// Counter can be advanced before block data is stored (especially during epoch transitions).
	// We need to find the highest block number that has actual data.
	validatedBlockNumber := counterBlockNumber

	// Guard against nil blockchain instance (during early startup or tests)
	blockchainInstance := blockchain.GetBlockChainInstance()
	if blockchainInstance != nil {
		for validatedBlockNumber > 0 {
			_, ok := blockchainInstance.GetBlockHashByNumber(validatedBlockNumber)
			if ok {
				break // Found a block with valid hash
			}
			logger.Warn("⚠️ [INIT] Block %d has no hash data, checking %d", validatedBlockNumber, validatedBlockNumber-1)
			validatedBlockNumber--
		}
	} else {
		logger.Warn("⚠️ [INIT] Blockchain instance is nil, skipping block hash validation")
		validatedBlockNumber = 0
	}

	if validatedBlockNumber != counterBlockNumber {
		logger.Warn("⚠️ [INIT] Block counter=%d but validated block=%d (counter ahead of actual data)",
			counterBlockNumber, validatedBlockNumber)
	}

	// FIX: Return the actual block number from Go's DB, not the LastGlobalExecIndex.
	// This ensures that `catchup` block syncer does not loop infinitely trying to fetch
	// empty blocks that were skipped by Go Master.
	returnBlockNumber := validatedBlockNumber
	// logger.Info("✅ [INIT] Using validated LastBlockNumber: %d (counter was %d)", returnBlockNumber, counterBlockNumber)

	// Also return LastGlobalExecIndex (tracks ALL commits including empty ones)
	// CRITICAL: Rust uses this for epoch transition SYNC WAIT comparison
	// BlockNumber tracks only non-empty commits, GEI tracks ALL commits
	lastGEI := storage.GetLastGlobalExecIndex()

	// FIX 4: Determine if Go Master is fully ready.
	// It's ready if the blockchain instance is initialized and database loading completes.
	isReady := true
	if blockchainInstance == nil {
		isReady = false
		logger.Warn("⚠️ [INIT] Go Master Blockchain instance is nil (DB not fully loaded yet).")
	}

	response := &pb.LastBlockNumberResponse{
		LastBlockNumber:     returnBlockNumber,
		LastGlobalExecIndex: lastGEI,
		IsReady:             isReady,
	}

	logger.Debug("✅ [INIT] Returning last block number for Rust: block=%d, gei=%d (counter=%d, validated=%d, is_ready=%v)",
		returnBlockNumber, lastGEI, counterBlockNumber, validatedBlockNumber, isReady)
	return response, nil
}

// HandleGetCurrentEpochRequest processes a GetCurrentEpochRequest and returns the current epoch from Go state (Sui-style)
func (rh *RequestHandler) HandleGetCurrentEpochRequest(request *pb.GetCurrentEpochRequest) (*pb.GetCurrentEpochResponse, error) {
	logger.Info("🔍 [GET CURRENT EPOCH] Handling GetCurrentEpochRequest from Rust")

	// Get current epoch from blockchain state
	currentEpoch := rh.chainState.GetCurrentEpoch()
	logger.Info("🔍 [GET CURRENT EPOCH] Current epoch from Go state", "epoch", currentEpoch)

	// NOTE: SaveEpochData() removed here - it was debug code causing unnecessary I/O
	// Epoch data is already saved correctly during AdvanceEpoch

	response := &pb.GetCurrentEpochResponse{
		Epoch: currentEpoch,
	}

	logger.Info("✅ [GET CURRENT EPOCH] Returning current epoch to Rust", "epoch", currentEpoch)
	return response, nil
}

// HandleGetEpochStartTimestampRequest processes a GetEpochStartTimestampRequest and returns epoch start timestamp (Sui-style)
func (rh *RequestHandler) HandleGetEpochStartTimestampRequest(request *pb.GetEpochStartTimestampRequest) (*pb.GetEpochStartTimestampResponse, error) {
	logger.Info("Handling GetEpochStartTimestampRequest (Sui-style epoch transition)", "epoch", request.Epoch)

	// Get epoch start timestamp from blockchain state
	// This should be stored in the blockchain state similar to how Sui stores epoch_start_timestamp_ms
	epochTimestamp, err := rh.chainState.GetEpochStartTimestamp(request.Epoch)
	if err != nil {
		return nil, fmt.Errorf("could not get epoch start timestamp for epoch %d: %w", request.Epoch, err)
	}

	logger.Info("Epoch start timestamp from Go state", "epoch", request.Epoch, "timestamp_ms", epochTimestamp)

	response := &pb.GetEpochStartTimestampResponse{
		TimestampMs: epochTimestamp,
	}

	return response, nil
}

// HandleAdvanceEpochRequest processes a AdvanceEpochRequest and advances Go state epoch (Sui-style completion)
func (rh *RequestHandler) HandleAdvanceEpochRequest(request *pb.AdvanceEpochRequest) (*pb.AdvanceEpochResponse, error) {
	logger.Info("Handling AdvanceEpochRequest (Sui-style epoch transition completion)",
		"new_epoch", request.NewEpoch,
		"timestamp_ms", request.EpochStartTimestampMs,
		"boundary_block", request.BoundaryBlock,
		"boundary_gei", request.BoundaryGei)

	// ═══════════════════════════════════════════════════════════════════
	// THE EPOCH GUARD: Prevent duplicate advances & log divergence
	// ═══════════════════════════════════════════════════════════════════
	currentEpoch := rh.chainState.GetCurrentEpoch()
	lastCommittedBlock := storage.GetLastBlockNumber()
	lastGEI := storage.GetLastGlobalExecIndex()
	
	if request.NewEpoch <= currentEpoch && request.NewEpoch > 0 {
		// Rust loop/monitor fired a duplicate advance.
		// We silently accept but DO NOT modify Go state, to let Rust proceed.
		logger.Warn("🛡️ [EPOCH GUARD] Duplicate AdvanceEpoch rejected! Target Epoch %d, but Go is already at Epoch %d.", request.NewEpoch, currentEpoch)
		return &pb.AdvanceEpochResponse{
			NewEpoch:              currentEpoch,
			EpochStartTimestampMs: rh.chainState.GetCurrentEpochStartTimestampMs(),
		}, nil
	}

	if request.BoundaryBlock > 0 && request.BoundaryBlock < lastCommittedBlock {
		logger.Warn("⚠️ [EPOCH GUARD] Boundary Block %d < Go Last Block %d. Allowing (likely a recovery replay).", request.BoundaryBlock, lastCommittedBlock)
	}
	
	if request.BoundaryGei > 0 && lastGEI > request.BoundaryGei {
		logger.Warn("⚠️ [EPOCH GUARD] Boundary GEI %d < Go Last GEI %d. Go has already executed blocks into this epoch!", request.BoundaryGei, lastGEI)
	}

	// CRITICAL FIX: If Rust sends timestamp_ms=0 (provisional placeholder),
	// derive the actual timestamp from the boundary block header.
	// This prevents Go from storing 0ms and returning it in get_epoch_boundary_data,
	// which would break consensus genesis block hash calculation.
	timestampMs := request.EpochStartTimestampMs
	if timestampMs == 0 && request.BoundaryBlock > 0 {
		logger.Info("⚠️ [ADVANCE EPOCH] Received timestamp_ms=0 (provisional). Deriving from boundary block %d header.", request.BoundaryBlock)
		blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(request.BoundaryBlock)
		if ok {
			blockData, err := rh.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
			if err == nil {
				headerTs := blockData.Header().TimeStamp()
				if headerTs > 0 {
					timestampMs = headerTs * 1000 // Convert seconds to milliseconds
					logger.Info("✅ [ADVANCE EPOCH] Derived timestamp from boundary block %d header: %d ms", request.BoundaryBlock, timestampMs)
				}
			}
		}
		if timestampMs == 0 {
			// FORK-SAFETY FIX (G-C2): Use genesis config timestamp as deterministic fallback.
			// time.Now() differs between nodes → epoch_start_timestamp_ms divergence → fork.
			if rh.genesisPath != "" {
				if genesisData, gErr := config.LoadGenesisData(rh.genesisPath); gErr == nil && genesisData.Config.EpochTimestampMs > 0 {
					timestampMs = genesisData.Config.EpochTimestampMs
					logger.Warn("⚠️ [ADVANCE EPOCH] Used genesis epoch_timestamp_ms as deterministic fallback: %d ms", timestampMs)
				}
			}
			if timestampMs == 0 {
				// No deterministic source available — this is a critical configuration error
				logger.Error("🚨 [ADVANCE EPOCH] CRITICAL: Cannot derive deterministic epoch timestamp! " +
					"boundary_block timestamp=0, genesis epoch_timestamp_ms=0. " +
					"This will cause fork divergence. Fix genesis.json to include epoch_timestamp_ms.")
				// Use boundary_block * 1000 as last resort (at least it's deterministic across nodes)
				timestampMs = request.BoundaryBlock * 1000
				if timestampMs == 0 {
					timestampMs = 1 // Avoid storing 0, use minimal deterministic value
				}
				logger.Warn("⚠️ [ADVANCE EPOCH] Using boundary_block-derived fallback timestamp: %d ms", timestampMs)
			}
		}
	}

	// Advance epoch in Go state with explicit boundary_block from Rust
	// This ensures deterministic epoch boundary instead of fallback to storage.GetLastBlockNumber()
	err := rh.chainState.AdvanceEpochWithBoundary(request.NewEpoch, timestampMs, request.BoundaryBlock, request.BoundaryGei)
	if err != nil {
		return nil, fmt.Errorf("could not advance epoch to %d: %w", request.NewEpoch, err)
	}

	logger.Info("✅ Successfully advanced Go state epoch",
		"new_epoch", request.NewEpoch,
		"timestamp_ms", timestampMs,
		"boundary_block", request.BoundaryBlock,
		"boundary_gei", request.BoundaryGei)

	// 📸 Notify snapshot manager about epoch transition
	if rh.snapshotManager != nil {
		rh.snapshotManager.OnEpochAdvanced(request.BoundaryBlock, request.NewEpoch)
	}

	response := &pb.AdvanceEpochResponse{
		NewEpoch:              request.NewEpoch,
		EpochStartTimestampMs: timestampMs,
	}

	return response, nil
}

// HandleGetEpochBoundaryDataRequest processes a GetEpochBoundaryDataRequest and returns unified epoch boundary data
// This is the single authoritative source for epoch transition data, ensuring consistency
func (rh *RequestHandler) HandleGetEpochBoundaryDataRequest(request *pb.GetEpochBoundaryDataRequest) (*pb.EpochBoundaryData, error) {
	epoch := request.GetEpoch()
	logger.Info("📊 [EPOCH BOUNDARY] Handling GetEpochBoundaryDataRequest", "epoch", epoch)

	// Get epoch boundary block and GEI
	currentEpoch := rh.chainState.GetCurrentEpoch()
	boundaryBlock, fromHistory := rh.chainState.GetEpochBoundaryBlock(epoch)
	boundaryGei := rh.chainState.GetEpochBoundaryGei(epoch)

	// SPECIAL CASE: Only epoch 0 uses boundary=0 (genesis)
	if epoch == 0 && !fromHistory {
		boundaryBlock = 0
		fromHistory = true
		logger.Info("✅ [EPOCH BOUNDARY] Using genesis boundary (block=0) for epoch 0")
	}

	if !fromHistory && epoch >= 1 {
		errMsg := fmt.Sprintf("epoch %d boundary block not stored (current_epoch=%d). "+
			"This node may not have witnessed the epoch transition. "+
			"Rust should fetch from peer or wait for sync to complete.", epoch, currentEpoch)
		logger.Error("❌ [EPOCH BOUNDARY] %s", errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	// =============================================================================
	// SYNC-AWARE TIMESTAMP: Handle when boundary block not yet synced
	// =============================================================================
	var epochTimestamp uint64
	var syncComplete bool = true

	if epoch == 0 {
		// EPOCH 0: Genesis epoch - use genesis config timestamp
		epochTimestamp = rh.chainState.GetCurrentEpochStartTimestampMs()
		if epochTimestamp == 0 {
			if rh.genesisPath != "" {
				if genesisData, err := config.LoadGenesisData(rh.genesisPath); err == nil {
					if genesisData.Config.EpochTimestampMs > 0 {
						epochTimestamp = genesisData.Config.EpochTimestampMs
						logger.Info("✅ [EPOCH BOUNDARY] Loaded genesis timestamp from genesis.json: %d ms", epochTimestamp)
					}
				}
			}
			if epochTimestamp == 0 {
				// FORK-SAFETY FIX (G-C3): Genesis MUST provide epoch_timestamp_ms.
				// time.Now() is non-deterministic across nodes → fork.
				logger.Error("🚨 [EPOCH BOUNDARY] CRITICAL: No genesis timestamp found! " +
					"genesis.json must include epoch_timestamp_ms for deterministic consensus. " +
					"Using fallback value 1 to avoid crash, but this indicates misconfiguration.")
				epochTimestamp = 1 // Deterministic (same across all nodes) but signals misconfiguration
			}
		}
		logger.Info("✅ [EPOCH BOUNDARY] Epoch 0 using GENESIS timestamp: %d ms", epochTimestamp)
	} else {
		// EPOCH N (N >= 1): Try to use boundary block header timestamp
		blockHash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(boundaryBlock)
		if ok {
			boundaryBlockData, err := rh.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
			if err == nil {
				epochTimestamp = boundaryBlockData.Header().TimeStamp() * 1000
				logger.Info("✅ [EPOCH BOUNDARY] Epoch %d using BOUNDARY BLOCK %d timestamp: %d ms (deterministic)",
					epoch, boundaryBlock, epochTimestamp)
			} else {
				// Block hash exists but data not readable - use stored timestamp
				epochTimestamp = rh.chainState.GetCurrentEpochStartTimestampMs()
				syncComplete = false
				logger.Warn("⚠️ [EPOCH BOUNDARY] Epoch %d: boundary block %d hash exists but data not readable. "+
					"Using stored timestamp %d ms.", epoch, boundaryBlock, epochTimestamp)
			}
		} else {
			// Block not synced yet - use stored timestamp from advance_epoch
			epochTimestamp = rh.chainState.GetCurrentEpochStartTimestampMs()
			syncComplete = false
			logger.Warn("⚠️ [EPOCH BOUNDARY] Epoch %d: boundary block %d not yet synced. "+
				"Using stored timestamp %d ms (sync pending).", epoch, boundaryBlock, epochTimestamp)
		}
	}

	// Get validators at boundary block (or current state if sync incomplete)
	var validators *pb.ValidatorInfoList
	var err error

	if syncComplete || epoch == 0 {
		validators, err = rh.GetValidatorsAtBlockInternal(boundaryBlock)
	} else {
		// Sync not complete - return validators at current state
		// This is acceptable because Rust has already verified the epoch transition
		lastBlock := storage.GetLastBlockNumber()
		logger.Warn("⚠️ [EPOCH BOUNDARY] Sync incomplete, using validators at block %d instead of %d",
			lastBlock, boundaryBlock)
		validators, err = rh.GetValidatorsAtBlockInternal(lastBlock)
	}

	if err != nil {
		logger.Error("❌ [EPOCH BOUNDARY] Failed to get validators at boundary block", "epoch", epoch, "block", boundaryBlock, "error", err)
		return nil, fmt.Errorf("failed to get validators at block %d: %w", boundaryBlock, err)
	}

	// 🔍 DIAGNOSTIC: Log detailed committee information
	logger.Info("📊 [EPOCH BOUNDARY] === UNIFIED COMMITTEE DATA FOR EPOCH %d ===", epoch)
	logger.Info("   📊 boundary_block: %d", boundaryBlock)
	logger.Info("   📊 epoch_timestamp_ms: %d (sync_complete=%v)", epochTimestamp, syncComplete)
	logger.Info("   📊 validator_count: %d", len(validators.Validators))
	logger.Info("📊 [EPOCH BOUNDARY] === END COMMITTEE DATA ===")

	logger.Info("✅ [EPOCH BOUNDARY] Returning epoch boundary data",
		"epoch", epoch,
		"timestamp_ms", epochTimestamp,
		"boundary_block", boundaryBlock,
		"validator_count", len(validators.Validators),
		"sync_complete", syncComplete)

	// Load epoch_duration_seconds from genesis config (authoritative source for all nodes)
	var epochDurationSeconds uint64 = 900 // default 15 minutes
	if rh.genesisPath != "" {
		if genesisData, err := config.LoadGenesisData(rh.genesisPath); err == nil {
			if genesisData.Config.EpochDurationSeconds > 0 {
				epochDurationSeconds = genesisData.Config.EpochDurationSeconds
				logger.Info("✅ [EPOCH BOUNDARY] Loaded epoch_duration_seconds from genesis: %ds", epochDurationSeconds)
			}
		}
	}

	return &pb.EpochBoundaryData{
		Epoch:                 epoch,
		EpochStartTimestampMs: epochTimestamp,
		BoundaryBlock:         boundaryBlock,
		BoundaryGei:           boundaryGei,
		Validators:            validators.Validators,
		EpochDurationSeconds:  epochDurationSeconds,
	}, nil
}

// GetValidatorsAtBlockInternal is a helper that returns ValidatorInfoList at a specific block
func (rh *RequestHandler) GetValidatorsAtBlockInternal(blockNumber uint64) (*pb.ValidatorInfoList, error) {
	// Reuse existing GetValidatorsAtBlock logic
	request := &pb.GetValidatorsAtBlockRequest{BlockNumber: blockNumber}
	return rh.HandleGetValidatorsAtBlockRequest(request)
}

// ============================================================================
// CLEAN TRANSITION HANDOFF APIs
// These APIs ensure no gaps or overlaps between sync and consensus modes
// ============================================================================

// HandleSetConsensusStartBlockRequest - Called by Rust before starting consensus
// Tells Go: "Consensus will produce blocks starting from block_number"
// Go will verify sync has completed up to block_number - 1
func (rh *RequestHandler) HandleSetConsensusStartBlockRequest(request *pb.SetConsensusStartBlockRequest) (*pb.SetConsensusStartBlockResponse, error) {
	blockNumber := request.GetBlockNumber()
	logger.Info("🔄 [TRANSITION HANDOFF] SetConsensusStartBlock: consensus will start at block %d", blockNumber)

	// Get current last block from Go storage
	lastSyncBlock := storage.GetLastBlockNumber()
	expectedSyncBlock := blockNumber - 1

	// Verify sync has caught up to the expected block
	if lastSyncBlock < expectedSyncBlock {
		errMsg := fmt.Sprintf("sync not caught up: last_sync_block=%d, expected=%d (consensus_start-1)", lastSyncBlock, expectedSyncBlock)
		logger.Warn("⚠️ [TRANSITION HANDOFF] %s", errMsg)
		return &pb.SetConsensusStartBlockResponse{
			Success:       false,
			LastSyncBlock: lastSyncBlock,
			Message:       errMsg,
		}, nil
	}

	logger.Info("✅ [TRANSITION HANDOFF] Sync caught up: last_sync_block=%d >= expected=%d", lastSyncBlock, expectedSyncBlock)

	// Store the consensus start block for future reference
	// This can be used to ensure consensus blocks are processed correctly
	storage.SetConsensusStartBlock(blockNumber)

	return &pb.SetConsensusStartBlockResponse{
		Success:       true,
		LastSyncBlock: lastSyncBlock,
		Message:       fmt.Sprintf("Sync complete up to block %d, consensus can start at block %d", lastSyncBlock, blockNumber),
	}, nil
}

// HandleSetSyncStartBlockRequest - Called by Rust when consensus ends
// Tells Go: "Consensus ended at last_consensus_block, sync should start from last_consensus_block + 1"
func (rh *RequestHandler) HandleSetSyncStartBlockRequest(request *pb.SetSyncStartBlockRequest) (*pb.SetSyncStartBlockResponse, error) {
	lastConsensusBlock := request.GetLastConsensusBlock()
	syncStartBlock := lastConsensusBlock + 1
	logger.Info("🔄 [TRANSITION HANDOFF] SetSyncStartBlock: consensus ended at block %d, sync will start from block %d", lastConsensusBlock, syncStartBlock)

	// Get current last block from Go storage
	currentLastBlock := storage.GetLastBlockNumber()

	// Verify the transition makes sense
	if currentLastBlock > lastConsensusBlock {
		// Go has processed more blocks than consensus claims
		logger.Warn("⚠️ [TRANSITION HANDOFF] Unexpected state: current_last_block=%d > last_consensus_block=%d",
			currentLastBlock, lastConsensusBlock)
	}

	// Store sync start block for network sync to use
	storage.SetSyncStartBlock(syncStartBlock)

	logger.Info("✅ [TRANSITION HANDOFF] Sync start block set to %d (consensus ended at %d)", syncStartBlock, lastConsensusBlock)

	return &pb.SetSyncStartBlockResponse{
		Success:        true,
		SyncStartBlock: syncStartBlock,
		Message:        fmt.Sprintf("Sync will start from block %d", syncStartBlock),
	}, nil
}

// HandleWaitForSyncToBlockRequest - Called by Rust to wait for Go sync to reach a specific block
// Used during SyncOnly -> Validator transition to ensure sync is complete before consensus starts
func (rh *RequestHandler) HandleWaitForSyncToBlockRequest(request *pb.WaitForSyncToBlockRequest) (*pb.WaitForSyncToBlockResponse, error) {
	targetBlock := request.GetTargetBlock()
	timeoutSeconds := request.GetTimeoutSeconds()

	// Default timeout if not specified
	if timeoutSeconds == 0 {
		timeoutSeconds = 30 // 30 seconds default
	}

	logger.Info("⏳ [TRANSITION HANDOFF] WaitForSyncToBlock: waiting for sync to reach block %d (timeout: %ds)", targetBlock, timeoutSeconds)

	// Check immediately if already reached
	currentBlock := storage.GetLastBlockNumber()
	if currentBlock >= targetBlock {
		logger.Info("✅ [TRANSITION HANDOFF] Already at or past target block: current=%d, target=%d", currentBlock, targetBlock)
		return &pb.WaitForSyncToBlockResponse{
			Reached:      true,
			CurrentBlock: currentBlock,
			Message:      fmt.Sprintf("Already at block %d (target: %d)", currentBlock, targetBlock),
		}, nil
	}

	// Poll until target is reached or timeout
	deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
	pollInterval := 100 * time.Millisecond

	for time.Now().Before(deadline) {
		currentBlock = storage.GetLastBlockNumber()
		if currentBlock >= targetBlock {
			logger.Info("✅ [TRANSITION HANDOFF] Target block reached: current=%d, target=%d", currentBlock, targetBlock)
			return &pb.WaitForSyncToBlockResponse{
				Reached:      true,
				CurrentBlock: currentBlock,
				Message:      fmt.Sprintf("Reached block %d (target: %d)", currentBlock, targetBlock),
			}, nil
		}
		time.Sleep(pollInterval)
	}

	// Timeout
	logger.Warn("⚠️ [TRANSITION HANDOFF] Timeout waiting for sync: current=%d, target=%d", currentBlock, targetBlock)
	return &pb.WaitForSyncToBlockResponse{
		Reached:      false,
		CurrentBlock: currentBlock,
		Message:      fmt.Sprintf("Timeout after %ds: current=%d, target=%d", timeoutSeconds, currentBlock, targetBlock),
	}, nil
}

// ============================================================================
// BLOCK SYNC APIs (SyncOnly Block Synchronization)
// These APIs enable SyncOnly nodes to fetch and sync blocks from the Go Master
// ============================================================================

// HandleGetBlocksRangeRequest processes a GetBlocksRangeRequest and returns blocks in range
// This is used by peer nodes to fetch blocks for synchronization
func (rh *RequestHandler) HandleGetBlocksRangeRequest(request *pb.GetBlocksRangeRequest) (*pb.GetBlocksRangeResponse, error) {
	fromBlock := request.GetFromBlock()
	toBlock := request.GetToBlock()

	logger.Debug("📦 [BLOCK SYNC] Handling GetBlocksRangeRequest: from=%d, to=%d", fromBlock, toBlock)

	// Limit batch size to prevent DoS
	maxBatch := uint64(500)
	if toBlock-fromBlock+1 > maxBatch {
		toBlock = fromBlock + maxBatch - 1
		logger.Info("📦 [BLOCK SYNC] Limited batch size to %d blocks (from=%d, to=%d)", maxBatch, fromBlock, toBlock)
	}

	blockDatabase := block.NewBlockDatabase(rh.storageManager.GetStorageBlock())
	bc := blockchain.GetBlockChainInstance()
	lastBlockNumber := storage.GetLastBlockNumber()

	// ═══════════════════════════════════════════════════════════════════════════
	// CRITICAL FIX (Mar 2026): Storage counter may be stale!
	// Consensus commitWorker calls SaveLastBlock (updates lastBlockHashKey)
	// but the counter (storage.GetLastBlockNumber) may not reflect all blocks.
	// Use GetLastBlock() to determine the actual latest block number.
	// ═══════════════════════════════════════════════════════════════════════════
	lastBlock, lastBlockErr := blockDatabase.GetLastBlock()
	if lastBlockErr == nil && lastBlock != nil {
		actualBlockNum := lastBlock.Header().BlockNumber()
		if actualBlockNum > lastBlockNumber {
			logger.Info("📦 [BLOCK SYNC] Counter stale: counter=%d, actual=%d (using actual)", lastBlockNumber, actualBlockNum)
			lastBlockNumber = actualBlockNum

			// Rebuild missing block number → hash mappings for blocks between
			// old counter and actual. Walk backwards from lastBlock using parent hash.
			blk := lastBlock
			for blk != nil {
				bNum := blk.Header().BlockNumber()
				if bNum == 0 {
					break
				}
				// Check if mapping already exists
				if _, ok := bc.GetBlockHashByNumber(bNum); ok {
					break // Already mapped, stop
				}
				// Set the mapping
				bc.SetBlockNumberToHash(bNum, blk.Header().Hash())
				// Walk to parent
				parentHash := blk.Header().LastBlockHash()
				parentBlk, err := blockDatabase.GetBlockByHash(parentHash)
				if err != nil {
					break
				}
				blk = parentBlk
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// BLOCK NUMBER MODE ONLY (Mar 2026):
	// fetch_blocks_from_peer ALWAYS sends block numbers (not GEI).
	// GEI mode was causing infinite dedup loops: if fromBlock didn't exist
	// in GetBlockHashByNumber, it fell back to GEI binary search which
	// returned blocks with header block_numbers BELOW fromBlock → dedup
	// → counter never advances → sync stuck forever.
	//
	// If fromBlock doesn't exist, scan forward to find first available block.
	// ═══════════════════════════════════════════════════════════════════════════
	// Find the actual starting block number (scan forward if fromBlock doesn't exist)
	startBlock := fromBlock
	if _, ok := bc.GetBlockHashByNumber(fromBlock); !ok {
		// Scan forward to find any block with number >= fromBlock
		found := false
		for probe := fromBlock; probe <= lastBlockNumber; probe++ {
			if _, ok := bc.GetBlockHashByNumber(probe); ok {
				startBlock = probe
				found = true
				break
			}
		}
		if !found {
			logger.Debug("📦 [BLOCK SYNC] No blocks found >= %d (lastBlock=%d)", fromBlock, lastBlockNumber)
		}
	}
	logger.Debug("📦 [BLOCK SYNC] Using BlockNumber mode: from=%d (start=%d), to=%d (lastBlock=%d)", fromBlock, startBlock, toBlock, lastBlockNumber)

	var blocks []*pb.BlockData

	// ── BLOCK NUMBER MODE (ONLY) ──
	// Iterate sequentially by BlockNumber from startBlock to min(toBlock, lastBlockNumber)
	upperBound := toBlock
	if upperBound > lastBlockNumber {
		upperBound = lastBlockNumber
	}
	for blockNum := startBlock; blockNum <= upperBound; blockNum++ {
		if uint64(len(blocks)) >= maxBatch {
			break
		}
		blockHash, ok := bc.GetBlockHashByNumber(blockNum)
		if !ok {
			continue
		}
		blk, err := blockDatabase.GetBlockByHash(blockHash)
		if err != nil {
			continue
		}

		rawBlockBytes, err := blk.Marshal()
		if err != nil {
			logger.Warn("📦 [BLOCK SYNC] Failed to marshal block %d: %v", blockNum, err)
			continue
		}
		header := blk.Header()
		blockData := &pb.BlockData{
			BlockNumber:      header.BlockNumber(),
			BlockHash:        header.Hash().Bytes(),
			Epoch:            header.Epoch(),
			TimestampMs:      header.TimeStamp() * 1000,
			ParentHash:       header.LastBlockHash().Bytes(),
			StateRoot:        header.AccountStatesRoot().Bytes(),
			TransactionsRoot: header.TransactionsRoot().Bytes(),
			ReceiptsRoot:     header.ReceiptRoot().Bytes(),
			RawBlockBytes:    rawBlockBytes,
		}
		// ═══════════════════════════════════════════════════════════════════
		// CRITICAL (Mar 2026): Ensure backup data is included for EVERY block.
		// broadcastWorker may lag behind commitWorker — backup data isn't
		// ready yet when this handler runs. Without backup, the receiver's
		// Sub node gets stuck forever waiting for the missing block.
		//
		// Strategy:
		//  1. Check BackupDb for existing backup data
		//  2. If not found, wait briefly (broadcastWorker may be processing)
		//  3. If still not found after retries, STOP — don't serve this block
		//     or any subsequent blocks. Requester will retry later.
		// ═══════════════════════════════════════════════════════════════════
		backupStorage := rh.storageManager.GetStorageBackupDb()
		var backupData []byte
		if backupStorage != nil {
			primaryKey := []byte(fmt.Sprintf("block_data_topic-%d", blockNum))
			data, getErr := backupStorage.Get(primaryKey)
			if getErr != nil || len(data) == 0 {
				legacyKey := []byte(fmt.Sprintf("backup_%d", blockNum))
				data, getErr = backupStorage.Get(legacyKey)
			}
			if getErr == nil && len(data) > 0 {
				backupData = data
			} else {
				// Backup not ready — broadcastWorker may be lagging.
				// Wait briefly with polling (up to 500ms, 50ms intervals).
				for retry := 0; retry < 10; retry++ {
					time.Sleep(50 * time.Millisecond)
					data, getErr = backupStorage.Get(primaryKey)
					if getErr == nil && len(data) > 0 {
						backupData = data
						logger.Info("📦 [BLOCK SYNC] Block #%d backup found after %dms wait", blockNum, (retry+1)*50)
						break
					}
				}
				if len(backupData) == 0 {
					// Still no backup after 500ms — broadcastWorker too far behind.
					// STOP serving here. Requester will retry this range later.
					logger.Warn("📦 [BLOCK SYNC] Block #%d backup NOT ready (broadcastWorker lagging). Stopping at block #%d (served %d blocks)",
						blockNum, blockNum-1, len(blocks))
					break
				}
			}
		}
		blockData.BackupData = backupData
		blocks = append(blocks, blockData)
	}

	count := uint64(len(blocks))
	logger.Debug("📦 [BLOCK SYNC] Returning %d blocks (from=%d, to=%d)", count, fromBlock, toBlock)

	return &pb.GetBlocksRangeResponse{
		Blocks: blocks,
		Count:  count,
		Error:  "",
	}, nil
}

// HandleSyncBlocksRequest processes a SyncBlocksRequest and syncs blocks to local storage
// This is used by nodes to receive blocks fetched from peers via Rust orchestration
func (rh *RequestHandler) HandleSyncBlocksRequest(request *pb.SyncBlocksRequest) (*pb.SyncBlocksResponse, error) {
	blocks := request.GetBlocks()
	blockCount := len(blocks)

	logger.Info("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Handling SyncBlocksRequest: block_count=%d", blockCount)

	if blockCount == 0 {
		return &pb.SyncBlocksResponse{
			SyncedCount:     0,
			LastSyncedBlock: 0,
			Error:           "No blocks to sync",
		}, nil
	}

	blockDatabase := block.NewBlockDatabase(rh.storageManager.GetStorageBlock())
	bc := blockchain.GetBlockChainInstance()

	var executedCount uint64 = 0
	var lastExecutedBlock uint64 = 0
	var lastExecutedGEI uint64 = 0

	for _, blockData := range blocks {
		rawBytes := blockData.GetRawBlockBytes()
		backupBytes := blockData.GetBackupData()

		if len(rawBytes) == 0 {
			logger.Warn("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Block (wire_num=%d) has no raw_block_bytes, skipping", blockData.GetBlockNumber())
			continue
		}

		// Unmarshal the raw block
		blk := &block.Block{}
		if err := blk.Unmarshal(rawBytes); err != nil {
			logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to unmarshal block (wire_num=%d): %v", blockData.GetBlockNumber(), err)
			continue
		}

		header := blk.Header()
		blockHash := header.Hash()
		blockNum := header.BlockNumber()
		blockGEI := header.GlobalExecIndex()

		// ═══════════════════════════════════════════════════════════════════════════
		// DEDUPLICATION: Skip blocks already executed (GEI-based, not block-number based)
		// ═══════════════════════════════════════════════════════════════════════════
		currentGEI := storage.GetLastGlobalExecIndex()
		if blockGEI > 0 && blockGEI <= currentGEI {
			logger.Debug("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Block #%d (GEI=%d) already executed (current_gei=%d), skipping",
				blockNum, blockGEI, currentGEI)
			executedCount++
			if blockNum > lastExecutedBlock {
				lastExecutedBlock = blockNum
			}
			if blockGEI > lastExecutedGEI {
				lastExecutedGEI = blockGEI
			}
			// Still persist backup data for Sub nodes
			if len(backupBytes) > 0 {
				rh.persistBackupForSub(backupBytes, blockNum)
			}
			continue
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// STEP 1: Apply BackupDb state batches to LevelDB (Account, Code, SC, etc.)
		// This writes the pre-computed state diffs so NOMT can rebuild from them.
		// ═══════════════════════════════════════════════════════════════════════════
		if len(backupBytes) > 0 {
			backupDb, deserErr := storage.DeserializeBackupDb(backupBytes)
			if deserErr != nil {
				logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to deserialize BackUpDb for block #%d: %v", blockNum, deserErr)
			} else {
				if applyErr := rh.applyBackupDbBatches(&backupDb); applyErr != nil {
					logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to apply BackUpDb for block #%d: %v", blockNum, applyErr)
				}
			}
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// STEP 2: Save block to LevelDB (by hash + number→hash mapping)
		// ═══════════════════════════════════════════════════════════════════════════
		if err := blockDatabase.SaveBlockByHash(blk); err != nil {
			logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to save block #%d: %v", blockNum, err)
			continue
		}
		if err := bc.SetBlockNumberToHash(blockNum, blockHash); err != nil {
			logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to set block→hash mapping for block #%d: %v", blockNum, err)
		}
		for _, txHash := range blk.Transactions() {
			bc.SetTxHashMapBlockNumber(txHash, blockNum)
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// STEP 3: REBUILD NOMT TRIES — this is the KEY difference from store-only mode.
		// CommitBlockState(WithRebuildTries) reloads the trie from the state that was
		// just written to LevelDB by applyBackupDbBatches. After this call, NOMT's
		// persistent root matches the block's stateRoot.
		// ═══════════════════════════════════════════════════════════════════════════
		if _, err := rh.chainState.CommitBlockState(blk,
			blockchain.WithRebuildTries(),
			blockchain.WithPersistToDB(),
		); err != nil {
			logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to CommitBlockState for block #%d: %v", blockNum, err)
			// Continue anyway — partial state is better than no state
		} else {
			logger.Debug("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] ✅ CommitBlockState for block #%d (stateRoot=%s)",
				blockNum, header.AccountStatesRoot().Hex()[:18]+"...")
		}

		// Verify stateRoot matches peer's stateRoot:
		localRoot := rh.chainState.GetAccountStateDB().Trie().Hash()
		expectedRoot := header.AccountStatesRoot()
		if localRoot != expectedRoot && expectedRoot != (common.Hash{}) {
			logger.Error("🚨 [STATE VERIFY] Block #%d stateRoot MISMATCH! local=%s expected=%s. HALTING sync.",
				blockNum, localRoot.Hex(), expectedRoot.Hex())
			return &pb.SyncBlocksResponse{Error: "stateRoot mismatch"}, fmt.Errorf("stateRoot mismatch at block %d", blockNum)
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// STEP 4: Update in-memory state pointers
		// ═══════════════════════════════════════════════════════════════════════════
		headerCopy := blk.Header()
		rh.chainState.SetcurrentBlockHeader(&headerCopy)
		rh.chainState.CheckAndUpdateEpochFromBlock(header.Epoch(), header.TimeStamp())

		// Save as lastBlock in LevelDB (lastBlockHashKey)
		if err := blockDatabase.SaveLastBlock(blk); err != nil {
			logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to SaveLastBlock for #%d: %v", blockNum, err)
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// STEP 5: Update persistent counters (block number + GEI)
		// These must advance AFTER CommitBlockState so that any query to Go's GEI
		// returns a value that reflects actually-executed NOMT state.
		// ═══════════════════════════════════════════════════════════════════════════
		storage.UpdateLastBlockNumber(blockNum)
		if blockGEI > 0 {
			storage.UpdateLastGlobalExecIndex(blockGEI)
		}

		// FORK-SAFETY (Apr 2026): Clear Go-side read caches.
		// applyBackupDbBatches writes directly to NOMT/PebbleDB, bypassing AccountStateDB.
		// Without this, loadedAccounts and lruCache retain stale pre-sync data,
		// causing RPC queries and transaction validation to use old values
		// on newly executed blocks — making them appear diverged.
		// ═══════════════════════════════════════════════════════════════════════════
		rh.chainState.InvalidateAllState()
		// Clear MVM caches so smart contract reads see the new state
		mvm.ClearAllMVMApi()
		mvm.CallClearAllStateInstances()

		executedCount++
		lastExecutedBlock = blockNum
		if blockGEI > lastExecutedGEI {
			lastExecutedGEI = blockGEI
		}

		// ═══════════════════════════════════════════════════════════════════════════
		// STEP 6: Persist backup data for Sub nodes
		// Backup persisted to PebbleDB so Sub can read via 3-tier recovery.
		// Master MUST also broadcast these to sub nodes during execute mode, otherwise
		// Sub nodes get stuck since Master is not running the normal commitWorker loop.
		// ═══════════════════════════════════════════════════════════════════════════
		if len(backupBytes) > 0 {
			rh.persistBackupForSub(backupBytes, blockNum)

			if rh.broadcastCallback != nil {
				rh.broadcastCallback(blk, backupBytes, blockNum, len(blk.Transactions()))
			}
		}

		logger.Info("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] ✅ Executed block #%d: hash=%s, epoch=%d, gei=%d, txs=%d",
			blockNum, blockHash.Hex()[:18]+"...", header.Epoch(), blockGEI, len(blk.Transactions()))

		if executedCount%100 == 0 {
			logger.Info("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Progress: executed %d/%d blocks", executedCount, blockCount)
		}
	}

	// Commit block number→hash mappings
	if err := bc.Commit(); err != nil {
		logger.Error("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] Failed to commit block number mappings: %v", err)
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// RUST CONTROL (Apr 2026 Architectural Fix):
	// Instead of autonomous Go polling via `syncStateFromDBRefresher`, Rust via
	// this EXECUTE command explicitly governs memory state advancement.
	// ═══════════════════════════════════════════════════════════════════════════
	if executedCount > 0 && rh.updateLastBlockCallback != nil {
		if hash, ok := bc.GetBlockHashByNumber(lastExecutedBlock); ok {
			if lastBlk, err := blockDatabase.GetBlockByHash(hash); err == nil && lastBlk != nil {
				rh.updateLastBlockCallback(lastBlk)
			}
		}
	}

	logger.Info("🚀 [SNAPSHOT-RESUME] [EXECUTE SYNC] ✅ Completed: executed %d/%d blocks, last_block=#%d, last_gei=%d",
		executedCount, blockCount, lastExecutedBlock, lastExecutedGEI)

	return &pb.SyncBlocksResponse{
		SyncedCount:     executedCount,
		LastSyncedBlock: lastExecutedBlock,
		LastExecutedGei: lastExecutedGEI,
		Error:           "",
	}, nil
}

// persistBackupForSub saves backup data to PebbleDB for Sub node recovery.
func (rh *RequestHandler) persistBackupForSub(backupBytes []byte, blockNum uint64) {
	backupStorage := rh.storageManager.GetStorageBackupDb()
	if backupStorage == nil {
		return
	}
	backupKey := []byte(fmt.Sprintf("block_data_topic-%d", blockNum))
	if putErr := backupStorage.Put(backupKey, backupBytes); putErr != nil {
		logger.Error("❌ [EXECUTE SYNC] Failed to persist backup for block #%d: %v", blockNum, putErr)
	}
	legacyKey := []byte(fmt.Sprintf("backup_%d", blockNum))
	backupStorage.Put(legacyKey, backupBytes)
}

// applyBackupDbBatches applies all state batch data from a BackUpDb to local LevelDB storages.
// This mirrors applyBlockBatch from BlockProcessor but is adapted for the executor package.
// It writes Account, Block, Code, SmartContract, Receipt, Transaction, StakeState, and TrieDB batches.
func (rh *RequestHandler) applyBackupDbBatches(backupDb *storage.BackUpDb) error {
	// Map batch data fields to their corresponding storages
	type batchEntry struct {
		name    string
		data    []byte
		storage storage.Storage
	}

	entries := []batchEntry{
		{"Block", backupDb.BockBatch, rh.storageManager.GetStorageBlock()},
		{"Account", backupDb.AccountBatch, rh.storageManager.GetStorageAccount()},
		{"Code", backupDb.CodeBatchPut, rh.storageManager.GetStorageCode()},
		{"SmartContract", backupDb.SmartContractBatch, rh.storageManager.GetStorageSmartContract()},
		{"SC Storage", backupDb.SmartContractStorageBatch, rh.storageManager.GetStorageSmartContract()},
		{"Receipt", backupDb.ReceiptBatchPut, rh.storageManager.GetStorageReceipt()},
		{"Transaction", backupDb.TxBatchPut, rh.storageManager.GetStorageTransaction()},
		{"StakeState", backupDb.StakeState, rh.storageManager.GetStorageStake()},
	}

	aggregatedBatches := make(map[string][][2][]byte)

	for _, entry := range entries {
		if len(entry.data) > 0 {
			deserialized, err := storage.DeserializeBatch(entry.data)
			if err != nil {
				return fmt.Errorf("error deserializing batch '%s' for block %d: %w", entry.name, backupDb.BockNumber, err)
			}
			if len(deserialized) > 0 {
				aggregatedBatches[entry.name] = deserialized
			}
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════
	// PERFORMANCE & CORRECTNESS OPTIMIZATION (Apr 2026):
	// Intercept NOMT batches BEFORE they hit PebbleDB so they are processed
	// natively by the C++ NOMT engine. If we bypass this, the NOMT database
	// becomes completely unaware of the state updates, leading to stale
	// 'nomt_read' queries later (fixing persistent nonce mismatches on restart!!).
	// ═══════════════════════════════════════════════════════════════════════════
	if err := trie.ApplyNomtReplicationBatches(aggregatedBatches); err != nil {
		return fmt.Errorf("error replicating NOMT batches in applyBackupDbBatches: %w", err)
	}

	// Now apply the remaining batches (where 'nomt:' prefixed keys were stripped)
	for _, entry := range entries {
		if batch, ok := aggregatedBatches[entry.name]; ok && len(batch) > 0 && entry.storage != nil {
			if err := entry.storage.BatchPut(batch); err != nil {
				return fmt.Errorf("error writing batch '%s' for block %d: %w", entry.name, backupDb.BockNumber, err)
			}
		}
	}

	// Apply TrieDB batches
	if len(backupDb.TrieDatabaseBatchPut) > 0 {
		if err := rh.applyTrieDbBatches(backupDb.TrieDatabaseBatchPut, backupDb.BockNumber); err != nil {
			return fmt.Errorf("error writing TrieDB batches for block %d: %w", backupDb.BockNumber, err)
		}
	}

	// ═══════════════════════════════════════════════════════════════════════════════
	// CRITICAL FIX: Replay MVM FullDbLogs to ensure C++ VM database is consistent
	// Without this, smart contract storage reads return wrong values after sync,
	// causing accountStatesRoot divergence (fork) on the first locally-executed block.
	// This matches the behavior of applyBlockBatch() in block_processor_batch.go.
	// ═══════════════════════════════════════════════════════════════════════════════
	if len(backupDb.FullDbLogs) > 0 {
		for _, logMap := range backupDb.FullDbLogs {
			mvm.CallReplayFullDbLogs(logMap)
		}
		logger.Debug("📥 [BLOCK SYNC] ✅ Replayed %d FullDbLogs entries for block %d", len(backupDb.FullDbLogs), backupDb.BockNumber)
	}

	// Apply mapping batch
	if len(backupDb.MapppingBatch) > 0 {
		mappingStorage := rh.storageManager.GetStorageMapping()
		if mappingStorage != nil {
			deserialized, err := storage.DeserializeBatch(backupDb.MapppingBatch)
			if err != nil {
				return fmt.Errorf("error deserializing mapping batch for block %d: %w", backupDb.BockNumber, err)
			}
			if len(deserialized) > 0 {
				if err := mappingStorage.BatchPut(deserialized); err != nil {
					return fmt.Errorf("error writing mapping batch for block %d: %w", backupDb.BockNumber, err)
				}
			}
		}
	}

	return nil
}

// applyTrieDbBatches applies TrieDB batch data from a BackUpDb to the local TrieDB LevelDB storages.
// Each key in the map corresponds to a sub-database path under the Trie root.
func (rh *RequestHandler) applyTrieDbBatches(trieDbBatches map[string][]byte, blockNum uint64) error {
	for key, value := range trieDbBatches {
		if len(value) == 0 {
			continue
		}
		deserialized, err := storage.DeserializeBatch(value)
		if err != nil {
			return fmt.Errorf("error deserializing TrieDB batch '%s' for block %d: %w", key, blockNum, err)
		}
		if len(deserialized) == 0 {
			continue
		}
		// Open TrieDB at the configured path (matching getTrieDBFromPool pattern)
		databasePath := config.ConfigApp.Databases.RootPath + config.ConfigApp.Databases.Trie.Path + "/" + key
		database, err := storage.NewShardelDB(databasePath, 1, 2, config.ConfigApp.DBType, databasePath)
		if err != nil {
			return fmt.Errorf("error creating TrieDB '%s' for block %d: %w", key, blockNum, err)
		}
		if err := database.Open(); err != nil {
			return fmt.Errorf("error opening TrieDB '%s' for block %d: %w", key, blockNum, err)
		}
		if err := database.BatchPut(deserialized); err != nil {
			database.Close()
			return fmt.Errorf("error writing TrieDB batch '%s' for block %d: %w", key, blockNum, err)
		}
		database.Close()
	}
	return nil
}
