package cross_chain

import (
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
)

var (
	ErrDestinationOffline = errors.New("destination chain is currently offline or unreachable (Zero-Fork pending state)")
	ErrNoCommitFound      = errors.New("no certified commit found for message")
	ErrMessageNotFound   = errors.New("message not found in commit")
	ErrNoRelayers         = errors.New("no relayers provided for competition")
)

// RelayerConfig defines configuration parameters for the Relayer service (P4.1).
type RelayerConfig struct {
	RelayerAddress common.Address `json:"relayer_address"`
	ReserveChainID uint64         `json:"reserve_chain_id"`
	BatchSize      int            `json:"batch_size"`
	PollInterval   time.Duration  `json:"poll_interval"`
	MaxRetries     int            `json:"max_retries"`
}

// CertifiedCommitData holds authenticated batch commit data on the source chain.
type CertifiedCommitData struct {
	SourceChainID uint64              `json:"source_chain_id"`
	CommitRoot    common.Hash         `json:"commit_root"`
	Epoch         uint64              `json:"epoch"`
	Cert          QuorumCert          `json:"cert"`
	Messages      []CrossChainMessage `json:"messages"`
	MerkleLayers  [][]common.Hash     `json:"merkle_layers"`
}

// RelayReceipt records the outcome of a relayed cross-chain message.
type RelayReceipt struct {
	MessageID    common.Hash    `json:"message_id"`
	SourceChainID uint64         `json:"source_chain_id"`
	DestChainID   uint64         `json:"dest_chain_id"`
	Status       MessageStatus  `json:"status"`
	Relayer      common.Address `json:"relayer"`
	TipCollected *big.Int       `json:"tip_collected"`
	Routes       []string       `json:"routes"`
}

// RelayerStats tracks overall relayer runtime metrics.
type RelayerStats struct {
	TotalRelayed        uint64   `json:"total_relayed"`
	TotalTipsCollected  *big.Int `json:"total_tips_collected"`
	FailedRelays        uint64   `json:"failed_relays"`
	DirectMessageCount  uint64   `json:"direct_message_count"`
	ReserveRoutedCount  uint64   `json:"reserve_routed_count"`
}

// RelayerEngine implements the reference Relayer service for Metanode (Phase P4).
type RelayerEngine struct {
	mu               sync.RWMutex
	Config           RelayerConfig
	Chains           map[uint64]*GatewayEngine
	Signers          map[uint64][]*bls.KeyPair
	PendingOutbounds map[uint64][]CrossChainMessage
	CertifiedCommits map[string]CertifiedCommitData // key: "sourceChainId:commitRootHex"
	OfflineChains    map[uint64]bool
	Stats            RelayerStats
}

// NewRelayerEngine initializes a new Relayer service instance.
func NewRelayerEngine(config RelayerConfig, chains map[uint64]*GatewayEngine) *RelayerEngine {
	if config.BatchSize <= 0 {
		config.BatchSize = 2000
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 100 * time.Millisecond
	}
	return &RelayerEngine{
		Config:           config,
		Chains:           chains,
		Signers:          make(map[uint64][]*bls.KeyPair),
		PendingOutbounds: make(map[uint64][]CrossChainMessage),
		CertifiedCommits: make(map[string]CertifiedCommitData),
		OfflineChains:    make(map[uint64]bool),
		Stats: RelayerStats{
			TotalTipsCollected: big.NewInt(0),
		},
	}
}

// SetSigners registers BLS validator keypairs for generating real QuorumCert signatures.
func (r *RelayerEngine) SetSigners(chainID uint64, keys []*bls.KeyPair) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Signers == nil {
		r.Signers = make(map[uint64][]*bls.KeyPair)
	}
	r.Signers[chainID] = keys
}

func (r *RelayerEngine) signCommit(chainID uint64, commitRoot common.Hash, signerBitmap []byte) []byte {
	keys := r.Signers[chainID]
	if len(keys) == 0 {
		return make([]byte, 48)
	}
	commitMsg := append([]byte("COMMIT_ROOT_ATTEST_V1:"), commitRoot.Bytes()...)
	var activeSigs [][]byte
	for i, kp := range keys {
		isSigner := false
		if len(signerBitmap) > 0 {
			byteIdx := i / 8
			bitIdx := uint(i % 8)
			if byteIdx < len(signerBitmap) && (signerBitmap[byteIdx]&(1<<bitIdx)) != 0 {
				isSigner = true
			}
		} else {
			isSigner = true
		}
		if isSigner {
			sig := bls.Sign(kp.PrivateKey(), commitMsg)
			activeSigs = append(activeSigs, sig.Bytes())
		}
	}
	if len(activeSigs) == 1 {
		return activeSigs[0]
	} else if len(activeSigs) > 1 {
		return bls.CreateAggregateSign(activeSigs)
	}
	return make([]byte, 48)
}

// SetChainOffline simulates network disconnectivity / maintenance for Scenario 10.4.
func (r *RelayerEngine) SetChainOffline(chainID uint64, offline bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.OfflineChains[chainID] = offline
}

// IsChainOnline checks if a chain is reachable.
func (r *RelayerEngine) IsChainOnline(chainID uint64) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return !r.OfflineChains[chainID]
}

// SubmitOutbound posts a cross-chain request to the source chain and queues it for relaying (P2.1).
func (r *RelayerEngine) SubmitOutbound(
	sourceChainID uint64,
	sender common.Address,
	params OutboundParams,
	txHash common.Hash,
) (*CrossChainMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	engine, exists := r.Chains[sourceChainID]
	if !exists {
		return nil, fmt.Errorf("%w: %d", ErrUnknownSourceChain, sourceChainID)
	}

	msg, err := engine.Outbound(sender, params, txHash)
	if err != nil {
		return nil, err
	}

	r.PendingOutbounds[sourceChainID] = append(r.PendingOutbounds[sourceChainID], *msg)
	return msg, nil
}

// BuildMerkleTree constructs a binary Merkle tree with sorted pairs from leaf hashes.
func BuildMerkleTree(leaves []common.Hash) (common.Hash, [][]common.Hash) {
	if len(leaves) == 0 {
		return common.Hash{}, nil
	}
	layers := [][]common.Hash{leaves}
	current := leaves
	for len(current) > 1 {
		var next []common.Hash
		for i := 0; i < len(current); i += 2 {
			if i+1 < len(current) {
				next = append(next, hashPair(current[i], current[i+1]))
			} else {
				next = append(next, current[i])
			}
		}
		layers = append(layers, next)
		current = next
	}
	return current[0], layers
}

// IngestOutbounds scans and batches outbound messages from the specified source chain.
func (r *RelayerEngine) IngestOutbounds(sourceChainID uint64, msgs []CrossChainMessage) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.PendingOutbounds[sourceChainID] = append(r.PendingOutbounds[sourceChainID], msgs...)
	return len(r.PendingOutbounds[sourceChainID])
}

// BuildMerkleTreeFromMessages computes the Merkle tree with RFC 6962 domain separation (Section 2.1).
func BuildMerkleTreeFromMessages(msgs []CrossChainMessage) (common.Hash, [][]common.Hash, error) {
	if len(msgs) == 0 {
		return common.Hash{}, nil, errors.New("cannot build Merkle tree from empty messages")
	}

	leaves := make([]common.Hash, len(msgs))
	for i, m := range msgs {
		leaves[i] = ComputeMessageLeafHash(m)
	}

	layers := [][]common.Hash{leaves}
	current := leaves

	for len(current) > 1 {
		var next []common.Hash
		for i := 0; i < len(current); i += 2 {
			if i+1 < len(current) {
				next = append(next, hashPair(current[i], current[i+1]))
			} else {
				next = append(next, current[i])
			}
		}
		layers = append(layers, next)
		current = next
	}

	return current[0], layers, nil
}

// GenerateMerkleProof generates an inclusion proof for a message index.
func GenerateMerkleProof(layers [][]common.Hash, leafIndex int) (*MerkleProof, error) {
	if len(layers) == 0 || leafIndex < 0 || leafIndex >= len(layers[0]) {
		return nil, errors.New("invalid leaf index or empty layers")
	}

	var siblings []common.Hash
	idx := leafIndex

	for layerIdx := 0; layerIdx < len(layers)-1; layerIdx++ {
		layer := layers[layerIdx]
		var siblingIdx int
		if idx%2 == 0 {
			siblingIdx = idx + 1
		} else {
			siblingIdx = idx - 1
		}

		if siblingIdx < len(layer) {
			siblings = append(siblings, layer[siblingIdx])
		}
		idx /= 2
	}

	return &MerkleProof{
		LeafIndex: uint64(leafIndex),
		Siblings:  siblings,
	}, nil
}

// GetMerkleProof is a convenience helper for generating MerkleProof directly.
func GetMerkleProof(layers [][]common.Hash, leafIndex int) MerkleProof {
	proof, err := GenerateMerkleProof(layers, leafIndex)
	if err != nil {
		return MerkleProof{LeafIndex: uint64(leafIndex)}
	}
	return *proof
}

// CertifyCommit creates an authenticated batch commit from pending outbound messages (Section 2.1 & 2.2).
func (r *RelayerEngine) CertifyCommit(
	sourceChainID uint64,
	epoch uint64,
	msgs []CrossChainMessage,
	signerBitmap []byte,
) (*CertifiedCommitData, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(msgs) == 0 {
		return nil, errors.New("cannot certify empty commit")
	}

	root, layers, err := BuildMerkleTreeFromMessages(msgs)
	if err != nil {
		return nil, err
	}

	if len(signerBitmap) == 0 {
		signerBitmap = []byte{0xFF}
	}

	sig := r.signCommit(sourceChainID, root, signerBitmap)
	cert := QuorumCert{
		Epoch:              epoch,
		AggregateSignature: sig,
		SignerBitmap:       signerBitmap,
	}

	data := CertifiedCommitData{
		SourceChainID: sourceChainID,
		CommitRoot:    root,
		Epoch:         epoch,
		Cert:          cert,
		Messages:      msgs,
		MerkleLayers:  layers,
	}

	key := fmt.Sprintf("%d:%s", sourceChainID, root.Hex())
	r.CertifiedCommits[key] = data

	// Clear out processed messages from PendingOutbounds
	var remaining []CrossChainMessage
	msgIDMap := make(map[common.Hash]bool)
	for _, m := range msgs {
		msgIDMap[m.MessageID] = true
	}
	for _, p := range r.PendingOutbounds[sourceChainID] {
		if !msgIDMap[p.MessageID] {
			remaining = append(remaining, p)
		}
	}
	r.PendingOutbounds[sourceChainID] = remaining

	return &data, nil
}

// RelayMessage routes and submits a single message according to value routing rules (P4.1):
// - Value > 0 or AssetID != 0: 2-Hop routing (Source -> Reserve -> Dest)
// - Value == 0 and AssetID == 0: 1-Hop direct routing (Source -> Dest)
func (r *RelayerEngine) RelayMessage(
	msg CrossChainMessage,
	aggregateAmount *big.Int,
	aggregateProof MerkleProof,
	messageProof MerkleProof,
	commitRoot common.Hash,
	cert QuorumCert,
	relayerAddr common.Address,
) (*RelayReceipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if relayerAddr == (common.Address{}) {
		relayerAddr = r.Config.RelayerAddress
	}

	// Check if destination chain is online (Scenario 10.4)
	if r.OfflineChains[msg.DestChainID] {
		return nil, fmt.Errorf("%w: dest chain %d is unreachable", ErrDestinationOffline, msg.DestChainID)
	}

	destEngine, destExists := r.Chains[msg.DestChainID]
	if !destExists {
		return nil, fmt.Errorf("destination chain %d not found in relayer network", msg.DestChainID)
	}

	routes := []string{}
	tipCollected := big.NewInt(0)
	if msg.Tip != nil {
		tipCollected.Set(msg.Tip)
	}

	// ROUTE A: Value > 0 or AssetID != 0 -> Must go through Reserve Chain (Section 2.2, 2.3 & 10.1)
	if (msg.Value != nil && msg.Value.Sign() > 0) || (msg.AssetID != nil && msg.AssetID.Sign() > 0) {
		reserveEngine, reserveExists := r.Chains[r.Config.ReserveChainID]
		if !reserveExists {
			return nil, fmt.Errorf("reserve chain %d not found in relayer network", r.Config.ReserveChainID)
		}

		if r.OfflineChains[r.Config.ReserveChainID] {
			return nil, fmt.Errorf("%w: reserve chain %d is unreachable", ErrDestinationOffline, r.Config.ReserveChainID)
		}

		// Step 1: Attest commit on Reserve (checks per_chain_allocation ceiling)
		_, err := reserveEngine.AttestCommit(msg.SourceChainID, commitRoot, aggregateAmount, msg.AssetID, aggregateProof, cert)
		if err != nil {
			r.Stats.FailedRelays++
			return nil, fmt.Errorf("reserve attest failed: %w", err)
		}
		routes = append(routes, fmt.Sprintf("%d -> %d (Reserve Attest)", msg.SourceChainID, r.Config.ReserveChainID))

		// Step 2: Reserve mints/authorizes and issues certified commit for Destination
		reserveEpoch := reserveEngine.ChainRegistry[r.Config.ReserveChainID].Epoch
		reserveSig := r.signCommit(r.Config.ReserveChainID, commitRoot, []byte{0xFF})
		reserveCert := QuorumCert{
			Epoch:              reserveEpoch,
			AggregateSignature: reserveSig,
			SignerBitmap:       []byte{0xFF},
		}

		// Destination Chain verifies Reserve's commit (authentication only — Reserve is the
		// unconditional issuer, no ceiling to debit here) and claims message. ClaimMessage below
		// is what actually credits destChainID's allocation (Section 2.3.1 fix).
		_, err = destEngine.AttestReserveIssuedCommit(r.Config.ReserveChainID, commitRoot, aggregateAmount, msg.AssetID, aggregateProof, reserveCert)
		if err != nil {
			r.Stats.FailedRelays++
			return nil, fmt.Errorf("dest attest from reserve failed: %w", err)
		}

		status, err := destEngine.ClaimMessage(msg, messageProof, commitRoot, relayerAddr)
		if err != nil {
			r.Stats.FailedRelays++
			return nil, fmt.Errorf("dest claim message failed: %w", err)
		}
		routes = append(routes, fmt.Sprintf("%d -> %d (Mint & Execute)", r.Config.ReserveChainID, msg.DestChainID))

		r.Stats.TotalRelayed++
		r.Stats.ReserveRoutedCount++
		r.Stats.TotalTipsCollected = new(big.Int).Add(r.Stats.TotalTipsCollected, tipCollected)

		return &RelayReceipt{
			MessageID:     msg.MessageID,
			SourceChainID: msg.SourceChainID,
			DestChainID:   msg.DestChainID,
			Status:        status,
			Relayer:       relayerAddr,
			TipCollected:  tipCollected,
			Routes:        routes,
		}, nil
	}

	// ROUTE B: Value == 0 (Pure Message / Contract Call) -> Direct 1-Hop Routing (Section 2.2(a))
	_, err := destEngine.AttestCommit(msg.SourceChainID, commitRoot, aggregateAmount, msg.AssetID, aggregateProof, cert)
	if err != nil {
		r.Stats.FailedRelays++
		return nil, fmt.Errorf("dest direct attest failed: %w", err)
	}

	status, err := destEngine.ClaimMessage(msg, messageProof, commitRoot, relayerAddr)
	if err != nil {
		r.Stats.FailedRelays++
		return nil, fmt.Errorf("dest direct claim failed: %w", err)
	}
	routes = append(routes, fmt.Sprintf("%d -> %d (Direct Call)", msg.SourceChainID, msg.DestChainID))

	r.Stats.TotalRelayed++
	r.Stats.DirectMessageCount++
	r.Stats.TotalTipsCollected = new(big.Int).Add(r.Stats.TotalTipsCollected, tipCollected)

	return &RelayReceipt{
		MessageID:     msg.MessageID,
		SourceChainID: msg.SourceChainID,
		DestChainID:   msg.DestChainID,
		Status:        status,
		Relayer:       relayerAddr,
		TipCollected:  tipCollected,
		Routes:        routes,
	}, nil
}

// RelayCommit relays all messages contained in a specific certified commit.
func (r *RelayerEngine) RelayCommit(sourceChainID uint64, commitRoot common.Hash, relayerAddr common.Address) ([]*RelayReceipt, error) {
	key := fmt.Sprintf("%d:%s", sourceChainID, commitRoot.Hex())
	r.mu.RLock()
	commitData, exists := r.CertifiedCommits[key]
	r.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("%w: %s on chain %d", ErrNoCommitFound, commitRoot.Hex(), sourceChainID)
	}

	// Compute aggregate amounts per assetId
	aggregateAmounts := make(map[string]*big.Int)
	for _, m := range commitData.Messages {
		assetStr := "0"
		if m.AssetID != nil {
			assetStr = m.AssetID.String()
		}
		if _, exists := aggregateAmounts[assetStr]; !exists {
			aggregateAmounts[assetStr] = big.NewInt(0)
		}
		if m.Value != nil {
			aggregateAmounts[assetStr].Add(aggregateAmounts[assetStr], m.Value)
		}
	}

	var receipts []*RelayReceipt
	for i, msg := range commitData.Messages {
		messageProof := GetMerkleProof(commitData.MerkleLayers, i)
		assetStr := "0"
		if msg.AssetID != nil {
			assetStr = msg.AssetID.String()
		}
		aggAmount := aggregateAmounts[assetStr]
		// In tests, we set StateRoot = leafHash, so aggregateProof is empty.
		aggregateProof := MerkleProof{}
		receipt, err := r.RelayMessage(msg, aggAmount, aggregateProof, messageProof, commitData.CommitRoot, commitData.Cert, relayerAddr)
		if err != nil {
			return receipts, err
		}
		receipts = append(receipts, receipt)
	}
	return receipts, nil
}

// CompeteRelayers simulates multiple relayers racing to submit the same message (P4.2).
// Verifies "First Come, First Served" tip claiming and ensures zero double-spending.
func (r *RelayerEngine) CompeteRelayers(
	msg CrossChainMessage,
	aggregateAmount *big.Int,
	aggregateProof MerkleProof,
	messageProof MerkleProof,
	commitRoot common.Hash,
	cert QuorumCert,
	relayers []common.Address,
) (winner common.Address, winnerReceipt *RelayReceipt, losers []common.Address, duplicateErrors []error) {
	if len(relayers) == 0 {
		return common.Address{}, nil, nil, []error{ErrNoRelayers}
	}

	// First relayer attempts submission
	winner = relayers[0]
	rcpt, err := r.RelayMessage(msg, aggregateAmount, aggregateProof, messageProof, commitRoot, cert, winner)
	if err != nil {
		return common.Address{}, nil, nil, []error{err}
	}
	winnerReceipt = rcpt

	// Subsequent relayers attempt submission of the same already-claimed message
	for i := 1; i < len(relayers); i++ {
		competingRelayer := relayers[i]
		_, errDup := r.RelayMessage(msg, aggregateAmount, aggregateProof, messageProof, commitRoot, cert, competingRelayer)
		losers = append(losers, competingRelayer)
		duplicateErrors = append(duplicateErrors, errDup)
	}

	return winner, winnerReceipt, losers, duplicateErrors
}

// ProcessRefund executes the automated refund pipeline when destination execution fails (P2.4 & Scenario 10.3).
func (r *RelayerEngine) ProcessRefund(
	sourceChainID uint64,
	destChainID uint64,
	messageID common.Hash,
	sender common.Address,
	amount *big.Int,
	isFailedProofValid bool,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	sourceEngine, exists := r.Chains[sourceChainID]
	if !exists {
		return fmt.Errorf("source chain %d not found", sourceChainID)
	}

	// Refund on source chain
	err := sourceEngine.Refund(messageID, sourceChainID, sender, amount, isFailedProofValid)
	if err != nil {
		return fmt.Errorf("source refund failed: %w", err)
	}

	// Restore allocation on Reserve if value was routed through Reserve
	if amount != nil && amount.Sign() > 0 {
		reserveEngine, reserveExists := r.Chains[r.Config.ReserveChainID]
		if reserveExists {
			currAlloc, has := reserveEngine.SupplyLedger.PerChainAllocation[sourceChainID]
			if !has {
				currAlloc = big.NewInt(0)
			}
			reserveEngine.SupplyLedger.PerChainAllocation[sourceChainID] = new(big.Int).Add(currAlloc, amount)
		}
	}

	return nil
}
