package executor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/types"
	"google.golang.org/protobuf/proto"
)

// HandleGetActiveValidatorsRequest processes a GetActiveValidatorsRequest and returns a ValidatorInfoList.
// Returns only active validators (not jailed and with stake > 0) for epoch transition.
func (rh *RequestHandler) HandleGetActiveValidatorsRequest(request *pb.GetActiveValidatorsRequest) (*pb.ValidatorInfoList, error) {
	logger.Info("Handling GetActiveValidatorsRequest for epoch transition")

	// Get all validators from current state
	if rh.chainState == nil || rh.chainState.GetStakeStateDB() == nil {
		return nil, fmt.Errorf("ChainState or StakeStateDB is not initialized")
	}
	validators, err := rh.chainState.GetStakeStateDB().GetAllValidators()
	if err != nil {
		return nil, fmt.Errorf("could not get all validators from stake DB: %w", err)
	}

	// CRITICAL: Sort validators by AuthorityKey (BLS public key) by bytes
	// to ensure consistent bit-level ordering with Rust.
	sort.SliceStable(validators, func(i, j int) bool {
		cmp := bytes.Compare(validators[i].AuthorityKey(), validators[j].AuthorityKey())
		if cmp == 0 {
			addrI := validators[i].Address().Hex()
			addrJ := validators[j].Address().Hex()
			if addrI == addrJ {
				return validators[i].P2PAddress() < validators[j].P2PAddress()
			}
			return addrI < addrJ
		}
		return cmp < 0
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

	validatorCacheKey := crypto.Keccak256([]byte(fmt.Sprintf("epoch_validators_at_block_%d", blockNumber)))

	// CRITICAL FOR SNAPSHOT: Verify block has been committed to DB
	// Ensures block is committed before returning validators (snapshot consistency)
	lastCommittedBlockNumber := storage.GetLastBlockNumber()
	logger.Debug("🔍 [SNAPSHOT] Block commit status: requested_block=%d, last_committed_block=%d", blockNumber, lastCommittedBlockNumber)

	// If block has not been committed to DB yet, return error (Rust will retry)
	if blockNumber > lastCommittedBlockNumber {
		errMsg := fmt.Sprintf("block %d has not been committed to DB yet (last committed: %d). Go executor is still processing this block", blockNumber, lastCommittedBlockNumber)
		logger.Warn("⚠️  [SNAPSHOT] %s", errMsg)
		return nil, fmt.Errorf(errMsg)
	}

	// Get block at blockNumber
	// Special handling for block 0 (genesis) — may not exist if Go Master hasn't initialized genesis yet
	var blockHash common.Hash
	var ok bool
	blockchainInstance := blockchain.GetBlockChainInstance()
	if blockchainInstance != nil {
		blockHash, ok = blockchainInstance.GetBlockHashByNumber(blockNumber)
	}
	logger.Debug("🔍 [SNAPSHOT] GetBlockHashByNumber(%d): ok=%v, hash=%s", blockNumber, ok, blockHash)
	var validators []state.ValidatorState
	var err error
	var blockData types.Block

	if !ok {
		if blockNumber == 0 {
			logger.Warn("Block 0 not found, getting validators from genesis.json to ensure fork-safe deterministic committee")

			validatorInfoList := &pb.ValidatorInfoList{}
			var genesisValidators []*pb.Validator

			if rh.genesisPath != "" {
				if genesisData, err := config.LoadGenesisData(rh.genesisPath); err == nil {
					genesisValidators = genesisData.Validators
					logger.Info("✅ [EPOCH] Loaded %d genesis validators directly from %s for deterministic epoch 0 committee", len(genesisValidators), rh.genesisPath)
				} else {
					logger.Error("🚨 [FORK-SAFETY] Could not load genesis.json: %v. This may cause consensus fork if validators changed since genesis!", err)
				}
			} else {
				logger.Error("🚨 [FORK-SAFETY] No genesis path configured! This may cause consensus fork if validators changed since genesis!")
			}

			if len(genesisValidators) > 0 {
				validatorsWithMinStake := 0
				for _, genVal := range genesisValidators {
					// Use 1000000 as deterministic starting stake (since genesis validators start with no stake)
					stakeNormalized := big.NewInt(1000000)
					validatorsWithMinStake++

					val := &pb.ValidatorInfo{
						Address:                    genVal.Address,
						Stake:                      stakeNormalized.String(),
						AuthorityKey:               genVal.AuthorityKey,
						ProtocolKey:                genVal.ProtocolKey,
						NetworkKey:                 genVal.NetworkKey,
						Name:                       genVal.Name,
						Description:                genVal.Description,
						Website:                    genVal.Website,
						Image:                      genVal.Image,
						CommissionRate:             genVal.CommissionRate,
						MinSelfDelegation:          "0",
						AccumulatedRewardsPerShare: "0",
						P2PAddress:                 genVal.P2PAddress,
					}
					validatorInfoList.Validators = append(validatorInfoList.Validators, val)
				}

				// CRITICAL: Sort validators by AuthorityKey (BLS public key) as bytes to ensure consistent ordering with Rust
				sort.SliceStable(validatorInfoList.Validators, func(i, j int) bool {
					cmp := bytes.Compare(validatorInfoList.Validators[i].AuthorityKey, validatorInfoList.Validators[j].AuthorityKey)
					if cmp == 0 {
						addrI := validatorInfoList.Validators[i].Address
						addrJ := validatorInfoList.Validators[j].Address
						if addrI == addrJ {
							return validatorInfoList.Validators[i].P2PAddress < validatorInfoList.Validators[j].P2PAddress
						}
						return addrI < addrJ
					}
					return cmp < 0
				})

				for i, val := range validatorInfoList.Validators {
					authKeyPreview := val.AuthorityKey
					if len(authKeyPreview) > 50 {
						authKeyPreview = authKeyPreview[:50]
					}
					logger.Debug("🔍 [EPOCH] 📤 [GO→RUST] Genesis ValidatorInfo[%d]: address=%s, stake=%s, name=%s, authority_key=%s",
						i, val.Address, val.Stake, val.Name, string(authKeyPreview)+"...")
				}
			} else {
				// Fallback to current state ONLY if genesis.json is missing (should not happen in prod)
				logger.Warn("⚠️ [EPOCH] Using current StakeStateDB as fallback for genesis committee. This is NOT fork-safe!")
				if rh.chainState == nil || rh.chainState.GetStakeStateDB() == nil {
					logger.Error("🚨 [FORK-SAFETY] ChainState or StakeStateDB is nil, cannot fallback to current state for genesis validators!")
					return nil, fmt.Errorf("cannot get validators from current state: ChainState or StakeStateDB is nil")
				}
				validators, err = rh.chainState.GetStakeStateDB().GetAllValidators()
				if err != nil {
					logger.Error("🔍 [EPOCH] ERROR getting validators from stake state DB: %v", err)
					return nil, fmt.Errorf("cannot get validators from current state (genesis not initialized): %w", err)
				}

				if len(validators) == 0 {
					logger.Warn("🔍 [EPOCH] ⚠️  WARNING: GetAllValidators() returned 0 validators! This means Go has not initialized genesis block or validators were not registered.")
				}

				// Sort
				sort.SliceStable(validators, func(i, j int) bool {
					cmp := bytes.Compare(validators[i].AuthorityKey(), validators[j].AuthorityKey())
					if cmp == 0 {
						addrI := validators[i].Address().Hex()
						addrJ := validators[j].Address().Hex()
						if addrI == addrJ {
							return validators[i].P2PAddress() < validators[j].P2PAddress()
						}
						return addrI < addrJ
					}
					return cmp < 0
				})

				for _, dbValidator := range validators {
					if dbValidator.IsJailed() {
						continue
					}
					totalStake := dbValidator.TotalStakedAmount()
					var stakeNormalized *big.Int
					if totalStake == nil || totalStake.Sign() <= 0 {
						stakeNormalized = big.NewInt(1000000)
					} else {
						const precision = 1_000_000_000_000_000_000
						stakeNormalized = new(big.Int).Div(totalStake, big.NewInt(precision))
						if stakeNormalized.Sign() <= 0 {
							stakeNormalized = big.NewInt(1)
						}
					}
					val := &pb.ValidatorInfo{
						Address:                    dbValidator.Address().Hex(),
						Stake:                      stakeNormalized.String(),
						AuthorityKey:               dbValidator.AuthorityKey(),
						ProtocolKey:                dbValidator.ProtocolKey(),
						NetworkKey:                 dbValidator.NetworkKey(),
						Name:                       dbValidator.Name(),
						Description:                dbValidator.Description(),
						Website:                    dbValidator.Website(),
						Image:                      dbValidator.Image(),
						CommissionRate:             dbValidator.CommissionRate(),
						MinSelfDelegation:          dbValidator.MinSelfDelegation().String(),
						AccumulatedRewardsPerShare: dbValidator.AccumulatedRewardsPerShare().String(),
						P2PAddress:                 dbValidator.P2PAddress(),
					}
					validatorInfoList.Validators = append(validatorInfoList.Validators, val)
				}
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

			logger.Debug("🔍 [EPOCH] Returning validators from genesis.json (genesis fallback): count=%d, epoch_timestamp_ms=%d, last_global_exec_index=0",
				len(validatorInfoList.Validators), validatorInfoList.EpochTimestampMs)

			if len(validatorInfoList.Validators) == 0 {
				logger.Warn("⚠️  No validators found in state! This may indicate:")
				logger.Warn("  1. Genesis validators not initialized")
				logger.Warn("  2. All validators are jailed or have no stake")
				logger.Warn("  3. Stake state DB is empty")
			}

			return validatorInfoList, nil
		}

		// FORK-SAFETY & CATCH-UP GAP FIX:
		// For NOMT backend, historical state is not stored, and we only query the live state.
		// If the requested blockNumber has been passed (lastCommittedBlockNumber >= blockNumber)
		// but the block itself was skipped/never-committed in Go due to fast-skip/catch-up,
		// we can safely query the live NOMT state instead of failing.
		if trie.GetStateBackend() == trie.BackendNOMT && lastCommittedBlockNumber >= blockNumber {
			logger.Info("ℹ️ [SNAPSHOT] Block %d not found in DB but height has been passed (lastCommitted=%d) with NOMT backend. Bypassing block existence check to query live state.", blockNumber, lastCommittedBlockNumber)

			if currentEpochData := rh.chainState.GetEpochValidators(rh.chainState.GetCurrentEpoch()); currentEpochData != nil {
				cachedList := &pb.ValidatorInfoList{}
				if unmarshalErr := proto.Unmarshal(currentEpochData, cachedList); unmarshalErr == nil {
					addrs := make([]string, 0, len(cachedList.Validators))
					for _, v := range cachedList.Validators {
						addrs = append(addrs, v.Address)
					}
					rh.chainState.GetStakeStateDB().RebuildKnownKeysFromValidatorList(addrs)
				} else if unmarshalErr := json.Unmarshal(currentEpochData, cachedList); unmarshalErr == nil {
					// Fallback to JSON for backward compatibility with old DB
					addrs := make([]string, 0, len(cachedList.Validators))
					for _, v := range cachedList.Validators {
						addrs = append(addrs, v.Address)
					}
					rh.chainState.GetStakeStateDB().RebuildKnownKeysFromValidatorList(addrs)
				}
			}

			validators, err = rh.chainState.GetStakeStateDB().GetAllValidators()
			if err != nil {
				return nil, fmt.Errorf("could not get all validators from stake DB at block %d: %w", blockNumber, err)
			}
		} else {
			return nil, fmt.Errorf("cannot find block hash for block number %d", blockNumber)
		}
	} else {
		blockData, err = rh.chainState.GetBlockDatabase().GetBlockByHash(blockHash)
		if err != nil {
			return nil, fmt.Errorf("could not get block data by hash %s: %w", blockHash, err)
		}

		// NOMT CACHE FIX: Check if we have cached validators for this block!
		if blockStorage := rh.storageManager.GetStorageBlock(); blockStorage != nil {
			if cachedData, err := blockStorage.Get(validatorCacheKey); err == nil && len(cachedData) > 0 {
				cachedList := &pb.ValidatorInfoList{}
				if err := proto.Unmarshal(cachedData, cachedList); err == nil {
					logger.Info("✅ [EPOCH] Loaded historical validators for block %d from cache (Crucial for NOMT)", blockNumber)
					return cachedList, nil
				} else if err := json.Unmarshal(cachedData, cachedList); err == nil {
					// Fallback to JSON for backward compatibility with old DB
					logger.Info("✅ [EPOCH] Loaded historical validators for block %d from cache using JSON fallback", blockNumber)
					return cachedList, nil
				}
			}
		}

		if trie.GetStateBackend() == trie.BackendNOMT {
			logger.Info("🔍 [EPOCH] Using LIVE StakeStateDB for NOMT backend (block=%d, NOMT has no historical roots)", blockNumber)

			if currentEpochData := rh.chainState.GetEpochValidators(rh.chainState.GetCurrentEpoch()); currentEpochData != nil {
				cachedList := &pb.ValidatorInfoList{}
				if unmarshalErr := proto.Unmarshal(currentEpochData, cachedList); unmarshalErr == nil {
					addrs := make([]string, 0, len(cachedList.Validators))
					for _, v := range cachedList.Validators {
						addrs = append(addrs, v.Address)
					}
					rh.chainState.GetStakeStateDB().RebuildKnownKeysFromValidatorList(addrs)
				} else if unmarshalErr := json.Unmarshal(currentEpochData, cachedList); unmarshalErr == nil {
					// Fallback to JSON for backward compatibility with old DB
					addrs := make([]string, 0, len(cachedList.Validators))
					for _, v := range cachedList.Validators {
						addrs = append(addrs, v.Address)
					}
					rh.chainState.GetStakeStateDB().RebuildKnownKeysFromValidatorList(addrs)
				}
			}

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
			defer chainStateAtBlock.Close()

			// Get all validators from state at this block
			validators, err = chainStateAtBlock.GetStakeStateDB().GetAllValidators()
		}
		if err != nil {
			return nil, fmt.Errorf("could not get all validators from stake DB at block %d: %w", blockNumber, err)
		}
	}

	// CRITICAL: Sort validators by AuthorityKey (BLS public key) by bytes
	// to ensure consistent bit-level ordering with Rust.
	sort.SliceStable(validators, func(i, j int) bool {
		cmp := bytes.Compare(validators[i].AuthorityKey(), validators[j].AuthorityKey())
		if cmp == 0 {
			addrI := validators[i].Address().Hex()
			addrJ := validators[j].Address().Hex()
			if addrI == addrJ {
				return validators[i].P2PAddress() < validators[j].P2PAddress()
			}
			return addrI < addrJ
		}
		return cmp < 0
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
		authKeyPreview := fmt.Sprintf("%x", val.AuthorityKey)
		if len(authKeyPreview) > 50 {
			authKeyPreview = authKeyPreview[:50] + "..."
		}
		logger.Debug("🔍 [EPOCH] 📤 [GO→RUST] ValidatorInfo[%d]: address=%s, stake=%s, name=%s, authority_key=%s, protocol_key=%x, network_key=%x",
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
		if blockData != nil {
			blockHeader := blockData.Header()
			epochTimestampMs = blockHeader.TimeStamp()
		}
		if epochTimestampMs == 0 {
			// FORK-SAFETY FIX: Block header has timestamp=0 (genesis or broken state or block skipped)
			// Using deterministic fallback based on blockNumber instead of time.Now()
			epochTimestampMs = blockNumber * 1000 // Deterministic: same across all nodes
			if epochTimestampMs == 0 {
				epochTimestampMs = 1 // Avoid zero for genesis block
			}
			logger.Error("🚨 [EPOCH TIMESTAMP] Block header timestamp is 0 or block is skipped! Using deterministic fallback: %d ms (block=%d)", epochTimestampMs, blockNumber)
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

	// Cache the validators to storage
	if blockStorage := rh.storageManager.GetStorageBlock(); blockStorage != nil {
		if serializedData, err := proto.Marshal(validatorInfoList); err == nil {
			if err := blockStorage.Put(validatorCacheKey, serializedData); err != nil {
				logger.Warn("⚠️ [EPOCH] Failed to cache validators to storage: %v", err)
			} else {
				logger.Debug("💾 [EPOCH] Cached historical validators for block %d to storage", blockNumber)
			}
		} else {
			logger.Warn("⚠️ [EPOCH] Failed to serialize validators to protobuf: %v", err)
		}
	}

	// CRITICAL FOR SNAPSHOT: Confirm block commitment to DB
	logger.Info("✅ [SNAPSHOT] Returning validators at block %d (COMMITTED TO DB): count=%d (skipped: %d jailed, %d had no stake but included with min stake=1), epoch_timestamp_ms=%d, last_global_exec_index=%d, last_committed_block=%d",
		blockNumber, len(validatorInfoList.Validators), skippedJailed, validatorsWithMinStake, epochTimestampMs, lastGlobalExecIndex, lastCommittedBlockNumber)
	return validatorInfoList, nil
}

// GetValidatorsAtBlockInternal is a helper that returns ValidatorInfoList at a specific block
func (rh *RequestHandler) GetValidatorsAtBlockInternal(blockNumber uint64) (*pb.ValidatorInfoList, error) {
	// Reuse existing GetValidatorsAtBlock logic
	request := &pb.GetValidatorsAtBlockRequest{BlockNumber: blockNumber}
	return rh.HandleGetValidatorsAtBlockRequest(request)
}
