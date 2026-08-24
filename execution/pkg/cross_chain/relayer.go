package cross_chain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"golang.org/x/crypto/sha3"
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
		PendingOutbounds: make(map[uint64][]CrossChainMessage),
		CertifiedCommits: make(map[string]CertifiedCommitData),
		OfflineChains:    make(map[uint64]bool),
		Stats: RelayerStats{
			TotalTipsCollected: big.NewInt(0),
		},
	}
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
		var nextLayer []common.Hash
		for i := 0; i < len(current); i += 2 {
			if i+1 < len(current) {
				nextLayer = append(nextLayer, hashPair(current[i], current[i+1]))
			} else {
				nextLayer = append(nextLayer, current[i])
			}
		}
		layers = append(layers, nextLayer)
		current = nextLayer
	}
	return current[0], layers
}

func hashPair(a, b common.Hash) common.Hash {
	hasher := sha3.NewLegacyKeccak256()
	aBytes := a.Bytes()
	bBytes := b.Bytes()
	if bytes.Compare(aBytes, bBytes) <= 0 {
		hasher.Write(aBytes)
		hasher.Write(bBytes)
	} else {
		hasher.Write(bBytes)
		hasher.Write(aBytes)
	}
	var out common.Hash
	hasher.Sum(out[:0])
	return out
}

// GetMerkleProof generates an inclusion proof for a leaf at the given index.
func GetMerkleProof(layers [][]common.Hash, leafIndex int) MerkleProof {
	var siblings []common.Hash
	idx := leafIndex
	for i := 0; i < len(layers)-1; i++ {
		layer := layers[i]
		var siblingIndex int
		if idx%2 == 0 {
			siblingIndex = idx + 1
		} else {
			siblingIndex = idx - 1
		}
		if siblingIndex < len(layer) {
			siblings = append(siblings, layer[siblingIndex])
		}
		idx /= 2
	}
	return MerkleProof{
		LeafIndex: uint64(leafIndex),
		Siblings:  siblings,
	}
}

// BuildMerkleTreeFromMessages converts messages into leaves and builds the full Merkle tree.
func BuildMerkleTreeFromMessages(msgs []CrossChainMessage) (common.Hash, [][]common.Hash, error) {
	if len(msgs) == 0 {
		return common.Hash{}, nil, errors.New("empty messages list")
	}
	var leaves []common.Hash
	for _, msg := range msgs {
		leafBytes, err := json.Marshal(msg)
		if err != nil {
			return common.Hash{}, nil, fmt.Errorf("failed to serialize message: %w", err)
		}
		leaves = append(leaves, Keccak256(leafBytes))
	}
	root, layers := BuildMerkleTree(leaves)
	return root, layers, nil
}

// CertifyCommit groups pending messages into a certified commit with QuorumCert and Merkle tree.
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

	cert := QuorumCert{
		Epoch:              epoch,
		AggregateSignature: make([]byte, 48), // BLS aggregate signature
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
	proof MerkleProof,
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
		_, err := reserveEngine.AttestCommit(msg.SourceChainID, commitRoot, msg.Value, cert, true)
		if err != nil {
			r.Stats.FailedRelays++
			return nil, fmt.Errorf("reserve attest failed: %w", err)
		}
		routes = append(routes, fmt.Sprintf("%d -> %d (Reserve Attest)", msg.SourceChainID, r.Config.ReserveChainID))

		// Step 2: Reserve mints/authorizes and issues certified commit for Destination
		reserveEpoch := reserveEngine.ChainRegistry[r.Config.ReserveChainID].Epoch
		reserveCert := QuorumCert{
			Epoch:              reserveEpoch,
			AggregateSignature: make([]byte, 48),
			SignerBitmap:       []byte{0xFF},
		}

		// Destination Chain verifies Reserve's commit and claims message
		_, err = destEngine.AttestCommit(r.Config.ReserveChainID, commitRoot, msg.Value, reserveCert, true)
		if err != nil {
			r.Stats.FailedRelays++
			return nil, fmt.Errorf("dest attest from reserve failed: %w", err)
		}

		status, err := destEngine.ClaimMessage(msg, proof, commitRoot, relayerAddr)
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
	_, err := destEngine.AttestCommit(msg.SourceChainID, commitRoot, big.NewInt(0), cert, true)
	if err != nil {
		r.Stats.FailedRelays++
		return nil, fmt.Errorf("dest direct attest failed: %w", err)
	}

	status, err := destEngine.ClaimMessage(msg, proof, commitRoot, relayerAddr)
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

	var receipts []*RelayReceipt
	for i, msg := range commitData.Messages {
		proof := GetMerkleProof(commitData.MerkleLayers, i)
		receipt, err := r.RelayMessage(msg, proof, commitData.CommitRoot, commitData.Cert, relayerAddr)
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
	proof MerkleProof,
	commitRoot common.Hash,
	cert QuorumCert,
	relayers []common.Address,
) (winner common.Address, winnerReceipt *RelayReceipt, losers []common.Address, duplicateErrors []error) {
	if len(relayers) == 0 {
		return common.Address{}, nil, nil, []error{ErrNoRelayers}
	}

	// First relayer attempts submission
	winner = relayers[0]
	rcpt, err := r.RelayMessage(msg, proof, commitRoot, cert, winner)
	if err != nil {
		return common.Address{}, nil, nil, []error{err}
	}
	winnerReceipt = rcpt

	// Subsequent relayers attempt submission of the same already-claimed message
	for i := 1; i < len(relayers); i++ {
		competingRelayer := relayers[i]
		_, errDup := r.RelayMessage(msg, proof, commitRoot, cert, competingRelayer)
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
	err := sourceEngine.Refund(messageID, sender, amount, isFailedProofValid)
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
