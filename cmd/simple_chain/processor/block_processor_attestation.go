// @title processor/block_processor_attestation.go
// @markdown block_processor_attestation.go - State attestation for fork detection between Go nodes
package processor

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/block_signer"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// ══════════════════════════════════════════════════════════════════════════════
// STATE ATTESTATION PROTOCOL
//
// Every N blocks (attestation_interval from genesis):
// 1. Each node computes attestation_hash = Keccak256(blockNumber ‖ accountRoot ‖ stakeRoot)
// 2. Signs it with BLS private key
// 3. Sends attestation to peers via P2P (StateAttestationTopic)
// 4. Collects attestations from peers
// 5. Quorum check: if local state differs from ≥2/3 of peers → FORK DETECTED → HALT
//
// FORK BEHAVIOR: When fork is detected, the node logs 🚨 FORK DETECTED and
// halts block processing (os.Exit with error). This prevents the forked node
// from diverging further and alerts the operator.
// ══════════════════════════════════════════════════════════════════════════════

// StateAttestation represents a node's attestation of state at a given block.
type StateAttestation struct {
	BlockNumber     uint64      `json:"block_number"`
	AttestationHash common.Hash `json:"attestation_hash"` // Keccak256(blockNumber ‖ accountRoot ‖ stakeRoot)
	AccountRoot     common.Hash `json:"account_root"`
	StakeRoot       common.Hash `json:"stake_root"`
	BLSSignature    []byte      `json:"bls_signature"`     // BLS signature of attestation_hash
	ValidatorPubKey []byte      `json:"validator_pub_key"` // BLS public key of signer
	NodeAddress     string      `json:"node_address"`      // Node's address for identification
}

// attestationCollector tracks received attestations for quorum checking.
type attestationCollector struct {
	mu sync.Mutex
	// blockNumber → map[attestation_hash] → list of node addresses
	attestations map[uint64]map[common.Hash][]string
	// blockNumber → local attestation hash
	localAttestations map[uint64]common.Hash
	// Track total peer count for quorum calculation
	totalPeers int
	// Persistent storage for attestation records
	db storage.Storage
}

// newAttestationCollector creates a new attestation collector.
func newAttestationCollector(db storage.Storage) *attestationCollector {
	return &attestationCollector{
		attestations:      make(map[uint64]map[common.Hash][]string),
		localAttestations: make(map[uint64]common.Hash),
		db:                db,
	}
}

// persistAttestation saves an attestation record to LevelDB.
// Key format: att_{blockNumber}_{local|peer}_{nodeAddress}
func (ac *attestationCollector) persistAttestation(att StateAttestation, isLocal bool) {
	if ac.db == nil {
		return
	}
	tag := "peer"
	if isLocal {
		tag = "local"
	}
	key := []byte(fmt.Sprintf("state_att:%d:%s:%s", att.BlockNumber, tag, att.NodeAddress))
	data, err := json.Marshal(att)
	if err != nil {
		logger.Warn("⚠️  [ATTESTATION] Failed to marshal attestation for persist: %v", err)
		return
	}
	if err := ac.db.Put(key, data); err != nil {
		logger.Warn("⚠️  [ATTESTATION] Failed to persist attestation block #%d: %v", att.BlockNumber, err)
	}
}

// addAttestation records a peer's attestation and returns fork check result.
// Returns (isForkDetected, shouldHalt, message)
func (ac *attestationCollector) addAttestation(att StateAttestation, localHash common.Hash, totalPeers int) (bool, string) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	blockNum := att.BlockNumber
	if ac.attestations[blockNum] == nil {
		ac.attestations[blockNum] = make(map[common.Hash][]string)
	}

	// Store local hash
	ac.localAttestations[blockNum] = localHash

	// Store peer attestation
	ac.attestations[blockNum][att.AttestationHash] = append(
		ac.attestations[blockNum][att.AttestationHash], att.NodeAddress)

	// Count attestations for this block (including self)
	totalAttestations := 1 // Self
	for _, nodes := range ac.attestations[blockNum] {
		totalAttestations += len(nodes)
	}

	// Only do quorum check when we have enough attestations
	// Need at least 2 peers (3 nodes total including self) for meaningful check
	if totalPeers < 2 || totalAttestations < 2 {
		return false, ""
	}

	// Count how many nodes agree with us (our hash)
	agreeWithUs := 1 // Self always agrees with self
	if nodes, ok := ac.attestations[blockNum][localHash]; ok {
		agreeWithUs += len(nodes)
	}

	// Count how many nodes disagree with us
	disagreeCount := 0
	for hash, nodes := range ac.attestations[blockNum] {
		if hash != localHash {
			disagreeCount += len(nodes)
		}
	}

	// Fork detection: if ≥2/3 of total nodes have a DIFFERENT state than us
	// totalNodes = 1 (self) + totalPeers
	totalNodes := 1 + totalPeers
	quorumThreshold := (totalNodes * 2) / 3 // 2/3 quorum

	if disagreeCount >= quorumThreshold {
		msg := fmt.Sprintf("🚨 [FORK DETECTED] Block #%d: %d/%d nodes DISAGREE with our state! "+
			"local_hash=%s, agree=%d, disagree=%d, quorum=%d/%d",
			blockNum, disagreeCount, totalNodes,
			localHash.Hex()[:16]+"...",
			agreeWithUs, disagreeCount, quorumThreshold, totalNodes)
		return true, msg
	}

	return false, ""
}

// cleanup removes old attestations from memory.
// Disk records are kept permanently for audit trail.
func (ac *attestationCollector) cleanup(currentBlock uint64, keepBlocks uint64) {
	ac.mu.Lock()
	defer ac.mu.Unlock()

	cutoff := uint64(0)
	if currentBlock > keepBlocks {
		cutoff = currentBlock - keepBlocks
	}

	for blockNum := range ac.attestations {
		if blockNum < cutoff {
			delete(ac.attestations, blockNum)
			delete(ac.localAttestations, blockNum)
		}
	}
}

// ComputeAttestationHash computes the deterministic attestation hash from block state.
// Hash = Keccak256(blockNumber as 8-byte BE ‖ accountStateRoot ‖ stakeStateRoot)
func ComputeAttestationHash(blockNumber uint64, accountRoot, stakeRoot common.Hash) common.Hash {
	data := make([]byte, 8+32+32)
	binary.BigEndian.PutUint64(data[0:8], blockNumber)
	copy(data[8:40], accountRoot.Bytes())
	copy(data[40:72], stakeRoot.Bytes())
	return crypto.Keccak256Hash(data)
}

// checkAndLogAttestation is called from commitWorker after each block commit.
// It checks if the current block is an attestation checkpoint, computes state hash,
// signs + logs it, and broadcasts to peers.
func (bp *BlockProcessor) checkAndLogAttestation(blockNumber uint64) {
	if bp.chainState == nil {
		return
	}

	interval := bp.chainState.GetAttestationInterval()
	if interval == 0 {
		return // Attestation disabled
	}

	// Only attest at interval boundaries
	if blockNumber%interval != 0 {
		return
	}

	// Read current state roots
	headerPtr := bp.chainState.GetcurrentBlockHeader()
	if headerPtr == nil {
		logger.Warn("⚠️  [ATTESTATION] Cannot attest block #%d: no current block header", blockNumber)
		return
	}
	header := *headerPtr
	accountRoot := header.AccountStatesRoot()
	stakeRoot := header.StakeStatesRoot()

	// Compute attestation hash
	attestationHash := ComputeAttestationHash(blockNumber, accountRoot, stakeRoot)

	logger.Info("📋 [STATE ATTESTATION] Block #%d: attestation_hash=%s, account_root=%s, stake_root=%s",
		blockNumber,
		attestationHash.Hex()[:16]+"...",
		accountRoot.Hex()[:16]+"...",
		stakeRoot.Hex()[:16]+"...",
	)

	// Build attestation
	attestation := StateAttestation{
		BlockNumber:     blockNumber,
		AttestationHash: attestationHash,
		AccountRoot:     accountRoot,
		StakeRoot:       stakeRoot,
	}

	// Sign with BLS if signer is available
	if bp.blockSigner != nil {
		attestation.BLSSignature = bp.blockSigner.SignBlockHash(attestationHash)
		attestation.ValidatorPubKey = bp.blockSigner.PublicKey()
		attestation.NodeAddress = bp.blockSigner.Address().Hex()

		logger.Info("🔏 [STATE ATTESTATION] Block #%d: signed (sig_len=%d, address=%s)",
			blockNumber, len(attestation.BLSSignature), attestation.NodeAddress)
	} else {
		attestation.NodeAddress = bp.validatorAddress.Hex()
	}

	// Store in attestation collector for quorum tracking
	if bp.attestationCollector != nil {
		bp.attestationCollector.localAttestations[blockNumber] = attestationHash
		// Persist to disk
		bp.attestationCollector.persistAttestation(attestation, true)
		// Cleanup old attestations from memory (keep last 100 blocks)
		bp.attestationCollector.cleanup(blockNumber, 100)
	}

	// Broadcast attestation to peers
	bp.broadcastAttestation(attestation)
}

// broadcastAttestation sends attestation to all connected peers.
func (bp *BlockProcessor) broadcastAttestation(attestation StateAttestation) {
	if bp.node == nil || bp.messageSender == nil || bp.connectionsManager == nil {
		return
	}

	data, err := json.Marshal(attestation)
	if err != nil {
		logger.Error("❌ [ATTESTATION] Failed to marshal attestation: %v", err)
		return
	}

	// Send to all connected nodes (both master and child connections)
	masterConns := bp.connectionsManager.ConnectionsByType(
		p_common.MapConnectionTypeToIndex(p_common.MASTER_CONNECTION_TYPE))
	childConns := bp.connectionsManager.ConnectionsByType(
		p_common.MapConnectionTypeToIndex(p_common.CHILD_NODE_CONNECTION_TYPE))

	sent := 0
	for _, conn := range masterConns {
		if conn != nil && conn.IsConnect() {
			if err := bp.messageSender.SendBytes(conn, p_common.StateAttestationTopic, data); err == nil {
				sent++
			}
		}
	}
	for _, conn := range childConns {
		if conn != nil && conn.IsConnect() {
			if err := bp.messageSender.SendBytes(conn, p_common.StateAttestationTopic, data); err == nil {
				sent++
			}
		}
	}

	if sent > 0 {
		logger.Debug("📤 [ATTESTATION] Broadcast attestation for block #%d to %d peers",
			attestation.BlockNumber, sent)
	}
}

// ProcessStateAttestation handles incoming attestation messages from peers.
// This is registered as a P2P handler for StateAttestationTopic.
func (bp *BlockProcessor) ProcessStateAttestation(request network.Request) error {
	var peerAtt StateAttestation
	if err := json.Unmarshal(request.Message().Body(), &peerAtt); err != nil {
		logger.Error("❌ [ATTESTATION] Failed to unmarshal peer attestation: %v", err)
		return nil
	}

	logger.Info("📥 [ATTESTATION RECV] Block #%d: peer=%s, hash=%s",
		peerAtt.BlockNumber, peerAtt.NodeAddress, peerAtt.AttestationHash.Hex()[:16]+"...")

	// Verify BLS signature if present
	if len(peerAtt.BLSSignature) > 0 && len(peerAtt.ValidatorPubKey) > 0 {
		if !block_signer.VerifyBlockSignature(peerAtt.AttestationHash, peerAtt.BLSSignature, peerAtt.ValidatorPubKey) {
			logger.Error("🚨 [ATTESTATION] INVALID BLS SIGNATURE from peer %s for block #%d — ignoring",
				peerAtt.NodeAddress, peerAtt.BlockNumber)
			return nil // Invalid signature — ignore this attestation
		}
		logger.Debug("✅ [ATTESTATION] Valid BLS signature from peer %s", peerAtt.NodeAddress)
	}

	// Compute local attestation hash for this block
	headerPtr := bp.chainState.GetcurrentBlockHeader()
	if headerPtr == nil {
		return nil
	}
	header := *headerPtr

	// Only compare if we have reached this block
	if header.BlockNumber() < peerAtt.BlockNumber {
		logger.Debug("📋 [ATTESTATION] Skipping quorum check: local block=%d < peer block=%d",
			header.BlockNumber(), peerAtt.BlockNumber)
		return nil
	}

	localAccountRoot := header.AccountStatesRoot()
	localStakeRoot := header.StakeStatesRoot()
	localHash := ComputeAttestationHash(peerAtt.BlockNumber, localAccountRoot, localStakeRoot)

	// Quick mismatch check — log immediately
	if localHash != peerAtt.AttestationHash {
		logger.Error("🚨 [STATE MISMATCH] Block #%d: local=%s ≠ peer(%s)=%s",
			peerAtt.BlockNumber,
			localHash.Hex()[:16]+"...",
			peerAtt.NodeAddress,
			peerAtt.AttestationHash.Hex()[:16]+"...",
		)
		logger.Error("🚨 [STATE MISMATCH] Local:  account_root=%s, stake_root=%s",
			localAccountRoot.Hex()[:16]+"...", localStakeRoot.Hex()[:16]+"...")
		logger.Error("🚨 [STATE MISMATCH] Peer:   account_root=%s, stake_root=%s",
			peerAtt.AccountRoot.Hex()[:16]+"...", peerAtt.StakeRoot.Hex()[:16]+"...")
	} else {
		logger.Info("✅ [STATE MATCH] Block #%d: local ≡ peer(%s) — state consistent",
			peerAtt.BlockNumber, peerAtt.NodeAddress)
	}

	// Add to collector for quorum check
	if bp.attestationCollector != nil {
		// Persist peer attestation to disk
		bp.attestationCollector.persistAttestation(peerAtt, false)
		totalPeers := bp.countConnectedPeers()
		isFork, msg := bp.attestationCollector.addAttestation(peerAtt, localHash, totalPeers)

		if isFork {
			// ═══════════════════════════════════════════════════════════════
			// 🚨 FORK DETECTED — NODE HALT
			// This node's state differs from ≥2/3 of peers.
			// HALT to prevent further divergence.
			// ═══════════════════════════════════════════════════════════════
			logger.Error("═══════════════════════════════════════════════════")
			logger.Error(msg)
			logger.Error("🚨 [FORK HALT] This node's state is INCONSISTENT with the network majority!")
			logger.Error("🚨 [FORK HALT] Block #%d: local_state=%s",
				peerAtt.BlockNumber, localHash.Hex())
			logger.Error("🚨 [FORK HALT] local_account_root=%s", localAccountRoot.Hex())
			logger.Error("🚨 [FORK HALT] local_stake_root=%s", localStakeRoot.Hex())
			logger.Error("🚨 [FORK HALT] ACTION REQUIRED: Investigate state divergence and resync from a healthy node.")
			logger.Error("🚨 [FORK HALT] Halting node in 3 seconds...")
			logger.Error("═══════════════════════════════════════════════════")

			// Give time for logs to flush
			go func() {
				time.Sleep(3 * time.Second)
				logger.Error("🛑 [FORK HALT] Node halting NOW due to state fork at block #%d", peerAtt.BlockNumber)
				os.Exit(1)
			}()
		}
	}

	return nil
}

// countConnectedPeers counts total connected peers (master + child).
func (bp *BlockProcessor) countConnectedPeers() int {
	if bp.connectionsManager == nil {
		return 0
	}

	count := 0
	masterConns := bp.connectionsManager.ConnectionsByType(
		p_common.MapConnectionTypeToIndex(p_common.MASTER_CONNECTION_TYPE))
	childConns := bp.connectionsManager.ConnectionsByType(
		p_common.MapConnectionTypeToIndex(p_common.CHILD_NODE_CONNECTION_TYPE))

	for _, conn := range masterConns {
		if conn != nil && conn.IsConnect() {
			count++
		}
	}
	for _, conn := range childConns {
		if conn != nil && conn.IsConnect() {
			count++
		}
	}
	return count
}
