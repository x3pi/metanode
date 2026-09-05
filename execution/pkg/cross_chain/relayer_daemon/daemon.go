package relayer_daemon

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain/rootanchor"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// DaemonConfig contains live RPC endpoints and credentials for RelayerDaemon.
type DaemonConfig struct {
	RelayerKeyHex     string            `json:"relayer_key_hex" yaml:"relayer_key_hex"`
	RootAnchorURLs    []string          `json:"root_anchor_urls" yaml:"root_anchor_urls"`
	ChainRPCURLs      map[uint64]string `json:"chain_rpc_urls" yaml:"chain_rpc_urls"`
	PollInterval      time.Duration     `json:"poll_interval" yaml:"poll_interval"`
	MaxPollIterations int               `json:"max_poll_iterations" yaml:"max_poll_iterations"`
	// ReserveChainID identifies which configured chain is the system's Reserve -- when
	// RelayBatch's sourceChainID equals this, it packs attestReserveIssuedCommit instead of the
	// normal attestCommit, since a Reserve-issued commit is exempt from the ceiling check (C8)
	// that would otherwise make attestCommit fail on any non-Reserve claiming chain. Zero/unset
	// means this daemon never sends attestReserveIssuedCommit -- every batch uses the normal
	// attestCommit path, matching pre-2026-08-28 behavior exactly (opt-in, not a default-on
	// behavior change).
	ReserveChainID uint64 `json:"reserve_chain_id,omitempty" yaml:"reserve_chain_id,omitempty"`

	// --- Gas pricing (2026-09-05 production-readiness review) ---
	// Every send used to use a hardcoded 1 Gwei gas price with zero fee-market awareness. See
	// resolveGasPrice's doc comment in gas_price.go for the full rationale and fallback chain.

	// FallbackGasPriceWei is used when a chain's eth_gasPrice RPC call fails or returns a
	// non-positive value. Defaults to 1_000_000_000 (1 Gwei, the old hardcoded constant) when nil
	// or non-positive, so leaving this unset reproduces pre-2026-09-05 behavior exactly in the
	// failure case.
	FallbackGasPriceWei *big.Int `json:"fallback_gas_price_wei,omitempty" yaml:"fallback_gas_price_wei,omitempty"`
	// GasPriceBumpPercent inflates the RPC-suggested gas price by this percent (e.g. 110 = +10%)
	// to reduce the odds of a transaction sitting unmined during a fee spike between suggestion
	// and broadcast. Values <=100 (including 0/unset) mean no bump.
	GasPriceBumpPercent uint64 `json:"gas_price_bump_percent,omitempty" yaml:"gas_price_bump_percent,omitempty"`
	// MaxGasPriceWei caps the gas price this daemon will ever pay, regardless of what the RPC
	// suggests or how large GasPriceBumpPercent is -- a safety ceiling against a compromised or
	// buggy RPC endpoint returning an absurd suggestion and draining the relayer's balance on a
	// single transaction. nil/non-positive means no cap.
	MaxGasPriceWei *big.Int `json:"max_gas_price_wei,omitempty" yaml:"max_gas_price_wei,omitempty"`
	// GasPriceCacheTTL controls how long a chain's suggested gas price is reused before an
	// eth_gasPrice RPC is issued again, so a tight burst of sends to the same chain (e.g.
	// RelayBatch's several attest/claim calls) doesn't re-query for every single one. Defaults to
	// 5s when <= 0.
	GasPriceCacheTTL time.Duration `json:"gas_price_cache_ttl,omitempty" yaml:"gas_price_cache_ttl,omitempty"`

	// --- Watch-loop backoff (2026-09-05 production-readiness review) ---

	// MaxPollBackoff caps the exponential backoff WatchChainPair applies after consecutive
	// errors polling/relaying a chain pair -- see backoffDuration in metrics.go. Defaults to 30s
	// when <= 0.
	MaxPollBackoff time.Duration `json:"max_poll_backoff" yaml:"max_poll_backoff"`
}

// RelayerDaemon is the automated production daemon that watches for cross-chain messages,
// aggregates BLS QuorumCerts from Root Anchor, and executes claims on destination chains.
type RelayerDaemon struct {
	mu                sync.RWMutex
	config            DaemonConfig
	relayerKey        *ecdsa.PrivateKey
	relayerAddr       common.Address
	rootAnchorClient  *rootanchor.Client
	chainClients      map[uint64]*rootanchor.Client
	abi               abi.ABI
	processedMessages map[common.Hash]bool
	attestedCommits   map[string]bool // key: "destChainId:commitRootHex"
	watchedPairs      map[string]bool // key: "srcChainId:destChainId"
	watchedPairsMu    sync.Mutex
	nonces            map[uint64]uint64
	nonceMu           sync.Mutex
	chainLocks        map[uint64]*sync.Mutex
	chainLocksMu      sync.Mutex
	stopCh            chan struct{}
	wg                sync.WaitGroup

	// --- Observability/production-readiness state (2026-09-05 review, see metrics.go) ---
	startedAt       time.Time
	gasPriceCache   map[uint64]gasPriceCacheEntry
	gasPriceCacheMu sync.Mutex
	healthMu        sync.Mutex
	pairHealth      map[string]*pairHealth
	balancesMu      sync.RWMutex
	balances        map[uint64]*big.Int
}

// NewRelayerDaemon instantiates a new live RelayerDaemon.
func NewRelayerDaemon(cfg DaemonConfig) (*RelayerDaemon, error) {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.MaxPollIterations <= 0 {
		cfg.MaxPollIterations = 30
	}

	cleanKey := strings.TrimPrefix(cfg.RelayerKeyHex, "0x")
	keyBytes, err := hex.DecodeString(cleanKey)
	if err != nil {
		return nil, fmt.Errorf("invalid relayer ECDSA private key hex: %w", err)
	}
	relKey, err := crypto.ToECDSA(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("parse relayer ECDSA key: %w", err)
	}
	relAddr := crypto.PubkeyToAddress(relKey.PublicKey)

	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	if err != nil {
		return nil, fmt.Errorf("parsing GatewayABI: %w", err)
	}

	raClient, err := rootanchor.NewClient(cfg.RootAnchorURLs, nil)
	if err != nil {
		return nil, fmt.Errorf("connecting to Root Anchor: %w", err)
	}

	chainClients := make(map[uint64]*rootanchor.Client, len(cfg.ChainRPCURLs))
	for chainID, url := range cfg.ChainRPCURLs {
		c, err := rootanchor.NewClient([]string{url}, nil)
		if err != nil {
			return nil, fmt.Errorf("connecting to chain %d @ %s: %w", chainID, url, err)
		}
		chainClients[chainID] = c
	}

	return &RelayerDaemon{
		config:            cfg,
		relayerKey:        relKey,
		relayerAddr:       relAddr,
		rootAnchorClient:  raClient,
		chainClients:      chainClients,
		abi:               parsedABI,
		processedMessages: make(map[common.Hash]bool),
		attestedCommits:   make(map[string]bool),
		nonces:            make(map[uint64]uint64),
		chainLocks:        make(map[uint64]*sync.Mutex),
		stopCh:            make(chan struct{}),
		startedAt:         time.Now(),
		gasPriceCache:     make(map[uint64]gasPriceCacheEntry),
		pairHealth:        make(map[string]*pairHealth),
		balances:          make(map[uint64]*big.Int),
	}, nil
}

func (d *RelayerDaemon) getChainLock(chainID uint64) *sync.Mutex {
	d.chainLocksMu.Lock()
	defer d.chainLocksMu.Unlock()
	if d.chainLocks == nil {
		d.chainLocks = make(map[uint64]*sync.Mutex)
	}
	lock, exists := d.chainLocks[chainID]
	if !exists {
		lock = &sync.Mutex{}
		d.chainLocks[chainID] = lock
	}
	return lock
}

// Address returns the Relayer's Ethereum-compatible public address.
func (d *RelayerDaemon) Address() common.Address {
	return d.relayerAddr
}

// GetChainClient returns the rootanchor.Client for chainID in a thread-safe manner.
func (d *RelayerDaemon) GetChainClient(chainID uint64) (*rootanchor.Client, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	client, exists := d.chainClients[chainID]
	return client, exists
}

// ConfiguredChains returns a snapshot of all configured chain IDs.
func (d *RelayerDaemon) ConfiguredChains() []uint64 {
	d.mu.RLock()
	defer d.mu.RUnlock()
	chains := make([]uint64, 0, len(d.chainClients))
	for id := range d.chainClients {
		chains = append(chains, id)
	}
	return chains
}

// AddChain dynamically registers a new chain RPC client at runtime and spawns watchers if ctx is provided.
func (d *RelayerDaemon) AddChain(ctx context.Context, chainID uint64, rpcURL string) error {
	d.mu.Lock()
	if _, exists := d.chainClients[chainID]; exists {
		d.mu.Unlock()
		return nil
	}
	c, err := rootanchor.NewClient([]string{rpcURL}, nil)
	if err != nil {
		d.mu.Unlock()
		return fmt.Errorf("connecting to chain %d @ %s: %w", chainID, rpcURL, err)
	}
	if d.chainClients == nil {
		d.chainClients = make(map[uint64]*rootanchor.Client)
	}
	d.chainClients[chainID] = c
	if d.config.ChainRPCURLs == nil {
		d.config.ChainRPCURLs = make(map[uint64]string)
	}
	d.config.ChainRPCURLs[chainID] = rpcURL

	existingChains := make([]uint64, 0, len(d.chainClients))
	for id := range d.chainClients {
		if id != chainID {
			existingChains = append(existingChains, id)
		}
	}
	d.mu.Unlock()

	logger.Info("✨ [DYNAMIC RELAYER] Discovered new chain %d (@ %s) — auto-spawning watchers!", chainID, rpcURL)

	if ctx != nil {
		for _, other := range existingChains {
			go d.WatchChainPair(ctx, chainID, other)
			go d.WatchChainPair(ctx, other, chainID)
		}
	}
	return nil
}

// RelayMessage handles the full attestation and dispatch cycle for a single cross-chain message.
func (d *RelayerDaemon) RelayMessage(
	ctx context.Context,
	msg cross_chain.CrossChainMessage,
	commitRoot common.Hash,
	epoch uint64,
	aggregateProof cross_chain.MerkleProof,
	messageProof cross_chain.MerkleProof,
) (common.Hash, error) {
	d.mu.Lock()
	if d.processedMessages[msg.MessageID] {
		d.mu.Unlock()
		return common.Hash{}, fmt.Errorf("message %s already processed by daemon", msg.MessageID.Hex())
	}
	d.mu.Unlock()

	// Step 1: Poll Root Anchor for BLS shares until QuorumCert is produced
	cert, err := d.pollAndAggregateCommitCert(ctx, msg.SourceChainID, epoch, commitRoot)
	if err != nil {
		return common.Hash{}, fmt.Errorf("poll and aggregate QuorumCert: %w", err)
	}

	// Step 2: Submit verifyAndExecute to destination chain
	aggSiblings := make([][32]byte, len(aggregateProof.Siblings))
	for i, s := range aggregateProof.Siblings {
		aggSiblings[i] = s
	}
	msgSiblings := make([][32]byte, len(messageProof.Siblings))
	for i, s := range messageProof.Siblings {
		msgSiblings[i] = s
	}

	calldata, err := d.abi.Pack("verifyAndExecute",
		msg.MessageID,
		new(big.Int).SetUint64(msg.SourceChainID),
		new(big.Int).SetUint64(msg.DestChainID),
		new(big.Int).SetUint64(msg.Sequence),
		msg.HopCount,
		msg.Sender,
		msg.Target,
		msg.AssetID,
		msg.Value,
		msg.Payload,
		msg.Tip,
		msg.GasFee,
		msg.Ordered,
		new(big.Int).SetUint64(aggregateProof.LeafIndex),
		aggSiblings,
		new(big.Int).SetUint64(messageProof.LeafIndex),
		msgSiblings,
		commitRoot,
		cert.Epoch,
		[]byte(cert.AggregateSignature),
		[]byte(cert.SignerBitmap),
	)
	if err != nil {
		return common.Hash{}, fmt.Errorf("pack verifyAndExecute calldata: %w", err)
	}

	txHash, err := d.sendToChain(ctx, msg.DestChainID, calldata, 500_000)
	if err != nil {
		return common.Hash{}, fmt.Errorf("broadcast verifyAndExecute tx: %w", err)
	}

	d.mu.Lock()
	d.processedMessages[msg.MessageID] = true
	d.mu.Unlock()

	logger.Info("🚀 [RELAYER DAEMON] successfully relayed message %s to chain %d (tx=%s)", msg.MessageID.Hex(), msg.DestChainID, txHash.Hex())
	return txHash, nil
}

// sendToChain signs calldata with the relayer's own key and broadcasts it to chainID's
// configured RPC endpoint, using the same per-destination-chain nonce cache (seeded from the
// real *pending* nonce, not just confirmed) and failure-recovery rules RelayMessage always used
// -- factored out so every Gateway write this daemon submits (verifyAndExecute, attestCommit,
// claimMessage, batchOutboundCommit) shares one nonce-safety implementation instead of
// duplicating it per call site.
func (d *RelayerDaemon) sendToChain(ctx context.Context, chainID uint64, calldata []byte, gasLimit uint64) (common.Hash, error) {
	client, exists := d.GetChainClient(chainID)
	if !exists {
		return common.Hash{}, fmt.Errorf("no RPC client configured for chain %d", chainID)
	}

	chainIDBig, err := client.ChainID(ctx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("query chain ID: %w", err)
	}

	var nonce uint64
	d.nonceMu.Lock()
	cached, exists := d.nonces[chainID]
	if exists {
		nonce = cached
		d.nonces[chainID] = cached + 1
		d.nonceMu.Unlock()
	} else {
		d.nonceMu.Unlock()
		pendingNonce, errPending := client.GetPendingTransactionCount(ctx, d.relayerAddr)
		if errPending != nil {
			return common.Hash{}, fmt.Errorf("query relayer pending nonce: %w", errPending)
		}
		nonce = pendingNonce
		d.nonceMu.Lock()
		d.nonces[chainID] = nonce + 1
		d.nonceMu.Unlock()
	}

	gwAddr := mt_common.GATEWAY_CONTRACT_ADDRESS
	txData := &ethtypes.LegacyTx{
		Nonce:    nonce,
		GasPrice: d.resolveGasPrice(ctx, chainID, client),
		Gas:      gasLimit,
		To:       &gwAddr,
		Value:    big.NewInt(0),
		Data:     calldata,
	}

	signer := ethtypes.NewEIP155Signer(chainIDBig)
	signedTx, err := ethtypes.SignTx(ethtypes.NewTx(txData), signer, d.relayerKey)
	if err != nil {
		return common.Hash{}, fmt.Errorf("sign transaction: %w", err)
	}

	rawBytes, err := signedTx.MarshalBinary()
	if err != nil {
		return common.Hash{}, fmt.Errorf("marshal signed tx: %w", err)
	}

	txHash, err := client.SendRawTransaction(ctx, hexutil.Encode(rawBytes))
	if err != nil {
		errLower := strings.ToLower(err.Error())
		if strings.Contains(errLower, "already exists in pool") || strings.Contains(errLower, "already known") {
			return signedTx.Hash(), nil
		}
		d.nonceMu.Lock()
		delete(d.nonces, chainID)
		d.nonceMu.Unlock()
		return common.Hash{}, err
	}
	return txHash, nil
}

// sendToChainAndWait submits calldata via sendToChain and polls for its receipt, returning the
// packed return/revert data (see rootanchor.TxReceipt's doc comment) once the transaction is no
// longer pending.
func (d *RelayerDaemon) sendToChainAndWait(ctx context.Context, chainID uint64, calldata []byte, gasLimit uint64) (*rootanchor.TxReceipt, error) {
	chainLock := d.getChainLock(chainID)
	chainLock.Lock()
	defer chainLock.Unlock()

	txHash, err := d.sendToChain(ctx, chainID, calldata, gasLimit)
	if err != nil {
		return nil, err
	}
	client, exists := d.GetChainClient(chainID)
	if !exists {
		return nil, fmt.Errorf("no RPC client configured for chain %d", chainID)
	}
	maxIterations := d.config.MaxPollIterations
	if d.config.PollInterval > 0 && time.Duration(maxIterations)*d.config.PollInterval < 10*time.Second {
		maxIterations = int((10 * time.Second) / d.config.PollInterval)
	}
	for i := 0; i < maxIterations; i++ {
		receipt, err := client.TransactionReceipt(ctx, txHash)
		if err == nil && receipt != nil {
			return receipt, nil
		}
		select {
		case <-time.After(d.config.PollInterval):
		case <-ctx.Done():
			d.nonceMu.Lock()
			delete(d.nonces, chainID)
			d.nonceMu.Unlock()
			return nil, ctx.Err()
		case <-d.stopCh:
			d.nonceMu.Lock()
			delete(d.nonces, chainID)
			d.nonceMu.Unlock()
			return nil, fmt.Errorf("relayer daemon stopping")
		}
	}
	d.nonceMu.Lock()
	delete(d.nonces, chainID)
	d.nonceMu.Unlock()
	return nil, fmt.Errorf("timed out waiting for receipt of tx %s on chain %d", txHash.Hex(), chainID)
}

// toBytes32Slice converts Merkle sibling hashes into the [32]byte array ABI encoding expects.
func toBytes32Slice(hashes []common.Hash) [][32]byte {
	out := make([][32]byte, len(hashes))
	for i, h := range hashes {
		out[i] = h
	}
	return out
}

// getMessageStatus reads a message's on-chain status via a real eth_call against the Gateway
// contract on the given chain. Returns cross_chain.MessageStatusPending (0) on any query error,
// so a transient RPC failure never silently causes RelayBatch to skip a message it should relay.
func (d *RelayerDaemon) getMessageStatus(ctx context.Context, client *rootanchor.Client, messageID common.Hash) cross_chain.MessageStatus {
	calldata, err := d.abi.Pack("getMessageStatus", messageID)
	if err != nil {
		return cross_chain.MessageStatusPending
	}
	result, err := client.EthCallGateway(ctx, calldata)
	if err != nil {
		return cross_chain.MessageStatusPending
	}
	out, err := d.abi.Unpack("getMessageStatus", result)
	if err != nil || len(out) == 0 {
		return cross_chain.MessageStatusPending
	}
	status, ok := out[0].(uint8)
	if !ok {
		return cross_chain.MessageStatusPending
	}
	return cross_chain.MessageStatus(status)
}

// chooseAttestMethod returns which Gateway ABI method a batch's attestation should use: a commit
// whose sourceChainID IS the configured Reserve is exempt from the ceiling check (C8) -- Reserve
// is the unconditional issuer -- so it needs attestReserveIssuedCommit; the claiming (dest) chain
// would otherwise reject a plain attestCommit unless IT happened to be the Reserve too (see
// note/cross_chain_stake_and_value_flow.md for the full A->Reserve->B routing picture this makes
// possible). reserveChainID==0 (unconfigured) always returns attestCommit, matching the exact
// pre-2026-08-28 behavior -- this is an opt-in capability, not a default-on behavior change.
// Pulled out as its own pure function so this one decision is directly unit-testable without
// standing up RelayBatch's full RPC-mocking harness.
func chooseAttestMethod(sourceChainID, reserveChainID uint64) string {
	if reserveChainID != 0 && sourceChainID == reserveChainID {
		return "attestReserveIssuedCommit"
	}
	return "attestCommit"
}

// RelayBatch relays every message in a batch produced by batchOutboundCommit() (all sharing one
// destination chain -- see GatewayEngine.PendingOutboundMessages' doc comment for why batches
// are always scoped to a single (sourceChain, destChain) pair). Groups messages by AssetID and
// calls attestCommit() once per distinct asset with the real aggregate amount for that asset
// within this commit (idempotent -- AttestCommit's own write-once guard makes a redundant call
// from a second concurrent relayer harmless), then claimMessage() once per message, skipping any
// whose on-chain status is no longer Pending (already resolved by another relayer, or a previous
// partial run of this same daemon after a restart).
func (d *RelayerDaemon) RelayBatch(
	ctx context.Context,
	sourceChainID uint64,
	commitRoot common.Hash,
	epoch uint64,
	messages []cross_chain.CrossChainMessage,
) error {
	if len(messages) == 0 {
		return fmt.Errorf("RelayBatch: empty message batch")
	}
	destChainID := messages[0].DestChainID
	destClient, exists := d.GetChainClient(destChainID)
	if !exists {
		return fmt.Errorf("no RPC client configured for destination chain %d", destChainID)
	}

	rebuiltRoot, layers, aggAmounts, aggIndex, err := cross_chain.BuildCommitTree(messages)
	if err != nil {
		return fmt.Errorf("rebuild commit tree: %w", err)
	}
	if rebuiltRoot != commitRoot {
		return fmt.Errorf("rebuilt commit tree root %s does not match expected %s -- message list is not what was actually committed", rebuiltRoot.Hex(), commitRoot.Hex())
	}

	cert, err := d.pollAndAggregateCommitCert(ctx, sourceChainID, epoch, commitRoot)
	if err != nil {
		return fmt.Errorf("poll and aggregate QuorumCert: %w", err)
	}

	attestedAssets := make(map[string]bool)
	for _, msg := range messages {
		assetIDStr := "0"
		if msg.AssetID != nil {
			assetIDStr = msg.AssetID.String()
		}
		if attestedAssets[assetIDStr] {
			continue
		}
		attestedAssets[assetIDStr] = true

		idx, ok := aggIndex[assetIDStr]
		if !ok {
			return fmt.Errorf("no aggregate leaf found for assetId %s in this commit", assetIDStr)
		}
		assetIDBig, ok := new(big.Int).SetString(assetIDStr, 10)
		if !ok {
			return fmt.Errorf("invalid assetId string %q", assetIDStr)
		}
		aggProof := cross_chain.GetMerkleProof(layers, idx)

		if d.config.ReserveChainID != 0 && sourceChainID != d.config.ReserveChainID && aggAmounts[assetIDStr].Sign() > 0 {
			// 2-Hop Value Routing (Phase 1):
			// 1. Submit attestCommit to the Reserve Chain (Root Anchor) to enforce & debit source's allocation
			reserveCalldata, err := d.abi.Pack("attestCommit",
				new(big.Int).SetUint64(sourceChainID), commitRoot, aggAmounts[assetIDStr], assetIDBig,
				new(big.Int).SetUint64(aggProof.LeafIndex), toBytes32Slice(aggProof.Siblings),
				cert.Epoch, []byte(cert.AggregateSignature), []byte(cert.SignerBitmap),
			)
			if err != nil {
				return fmt.Errorf("pack attestCommit on reserve for assetId %s: %w", assetIDStr, err)
			}
			reserveReceipt, err := d.sendToChainAndWait(ctx, d.config.ReserveChainID, reserveCalldata, 3_000_000)
			if err != nil {
				return fmt.Errorf("attestCommit on reserve (%d) for assetId %s: %w", d.config.ReserveChainID, assetIDStr, err)
			}
			if reserveReceipt.Status != 1 {
				logger.Info("ℹ️ [RELAYER DAEMON] reserve attestCommit for chain %d asset %s reverted: %s", sourceChainID, assetIDStr, DecodeRevertReason(reserveReceipt.Return))
			}

			// 2. If destination is a private chain (not the Reserve itself), submit attestReserveIssuedCommit to destination
			if destChainID != d.config.ReserveChainID {
				destCalldata, err := d.abi.Pack("attestReserveIssuedCommit",
					new(big.Int).SetUint64(sourceChainID), commitRoot, aggAmounts[assetIDStr], assetIDBig,
					new(big.Int).SetUint64(aggProof.LeafIndex), toBytes32Slice(aggProof.Siblings),
					cert.Epoch, []byte(cert.AggregateSignature), []byte(cert.SignerBitmap),
				)
				if err != nil {
					return fmt.Errorf("pack attestReserveIssuedCommit for assetId %s: %w", assetIDStr, err)
				}
				destReceipt, err := d.sendToChainAndWait(ctx, destChainID, destCalldata, 3_000_000)
				if err != nil {
					return fmt.Errorf("attestReserveIssuedCommit on dest (%d) for assetId %s: %w", destChainID, assetIDStr, err)
				}
				if destReceipt.Status != 1 {
					logger.Info("ℹ️ [RELAYER DAEMON] dest attestReserveIssuedCommit for chain %d asset %s reverted: %s", destChainID, assetIDStr, DecodeRevertReason(destReceipt.Return))
				}
			}
		} else {
			// Direct 1-hop (Source is Reserve, destination is Reserve, or zero-value pure contract call)
			attestMethod := chooseAttestMethod(sourceChainID, d.config.ReserveChainID)
			attestCalldata, err := d.abi.Pack(attestMethod,
				new(big.Int).SetUint64(sourceChainID), commitRoot, aggAmounts[assetIDStr], assetIDBig,
				new(big.Int).SetUint64(aggProof.LeafIndex), toBytes32Slice(aggProof.Siblings),
				cert.Epoch, []byte(cert.AggregateSignature), []byte(cert.SignerBitmap),
			)
			if err != nil {
				return fmt.Errorf("pack %s for assetId %s: %w", attestMethod, assetIDStr, err)
			}
			receipt, err := d.sendToChainAndWait(ctx, destChainID, attestCalldata, 3_000_000)
			if err != nil {
				return fmt.Errorf("%s for assetId %s: %w", attestMethod, assetIDStr, err)
			}
			if receipt.Status != 1 {
				logger.Info("ℹ️ [RELAYER DAEMON] %s for chain %d asset %s reverted: %s", attestMethod, sourceChainID, assetIDStr, DecodeRevertReason(receipt.Return))
			}
		}
	}

	for i, msg := range messages {
		if status := d.getMessageStatus(ctx, destClient, msg.MessageID); status != cross_chain.MessageStatusPending {
			continue
		}
		msgProof := cross_chain.GetMerkleProof(layers, i)
		claimCalldata, err := d.abi.Pack("claimMessage",
			msg.MessageID, new(big.Int).SetUint64(msg.SourceChainID), new(big.Int).SetUint64(msg.DestChainID),
			new(big.Int).SetUint64(msg.Sequence), msg.HopCount, msg.Sender, msg.Target,
			msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
			new(big.Int).SetUint64(msgProof.LeafIndex), toBytes32Slice(msgProof.Siblings), commitRoot,
		)
		if err != nil {
			return fmt.Errorf("pack claimMessage for %s: %w", msg.MessageID.Hex(), err)
		}
		receipt, err := d.sendToChainAndWait(ctx, destChainID, claimCalldata, 3_000_000)
		if err != nil {
			logger.Warn("⚠️ [RELAYER DAEMON] claimMessage for %s failed to send: %v", msg.MessageID.Hex(), err)
			continue
		}
		if receipt.Status != 1 {
			logger.Warn("⚠️ [RELAYER DAEMON] claimMessage for %s reverted: %s", msg.MessageID.Hex(), DecodeRevertReason(receipt.Return))
			continue
		}
		d.mu.Lock()
		d.processedMessages[msg.MessageID] = true
		d.mu.Unlock()
		logger.Info("🚀 [RELAYER DAEMON] relayed message %s to chain %d via batch %s", msg.MessageID.Hex(), destChainID, commitRoot.Hex())

		// Destination-side counterpart of step 1's attestCommit debit (2026-09-04 finding, see
		// GatewayEngine.CreditReserveAllocation's doc comment): claimMessage above just credited
		// destChainID's own LOCAL PerChainAllocation copy, on destChainID's own separate node --
		// never Reserve's authoritative one, unless destChainID happens to BE Reserve (in which
		// case claimMessage already ran on Reserve's own node and this is correctly skipped
		// below). Left uncredited, Reserve's ledger permanently under-records what destChainID
		// really, legitimately holds, which can later spuriously reject destChainID's own
		// legitimate outbound transfers against an artificially-low ceiling. Best-effort: a failed
		// or unsent credit here does not roll back the real, already-settled claim above (the
		// user's funds already landed) -- it only risks a future ceiling false-reject, which is
		// safe (fails closed, never lets more value out than was ever really allocated) and can be
		// resolved by re-running this same call later since it is idempotent.
		if d.config.ReserveChainID != 0 && destChainID != d.config.ReserveChainID && msg.Value != nil && msg.Value.Sign() > 0 {
			creditCalldata, err := d.abi.Pack("creditReserveAllocation",
				msg.MessageID, new(big.Int).SetUint64(msg.SourceChainID), new(big.Int).SetUint64(msg.DestChainID),
				new(big.Int).SetUint64(msg.Sequence), msg.HopCount, msg.Sender, msg.Target,
				msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
				new(big.Int).SetUint64(msgProof.LeafIndex), toBytes32Slice(msgProof.Siblings), commitRoot,
			)
			if err != nil {
				logger.Warn("⚠️ [RELAYER DAEMON] pack creditReserveAllocation for %s: %v", msg.MessageID.Hex(), err)
			} else {
				creditReceipt, err := d.sendToChainAndWait(ctx, d.config.ReserveChainID, creditCalldata, 3_000_000)
				if err != nil {
					logger.Warn("⚠️ [RELAYER DAEMON] creditReserveAllocation for %s failed to send: %v", msg.MessageID.Hex(), err)
				} else if creditReceipt.Status != 1 {
					logger.Warn("⚠️ [RELAYER DAEMON] creditReserveAllocation for %s reverted: %s", msg.MessageID.Hex(), DecodeRevertReason(creditReceipt.Return))
				} else {
					logger.Info("💰 [RELAYER DAEMON] credited chain %d's allocation on Reserve for message %s", destChainID, msg.MessageID.Hex())
				}
			}
		}
	}
	return nil
}

// BatchAndRelay is the single unit of work a watch loop performs for one (sourceChainID,
// destChainID) pair: if there are real pending outbound() messages queued on sourceChainID for
// destChainID, submit a real batchOutboundCommit() there, then immediately relay the resulting
// batch (RelayBatch). Returns (0, nil) with no error when there was nothing pending -- not an
// error case, just nothing to do this tick.
func (d *RelayerDaemon) BatchAndRelay(ctx context.Context, sourceChainID, destChainID uint64) (int, error) {
	sourceClient, exists := d.GetChainClient(sourceChainID)
	if !exists {
		return 0, fmt.Errorf("no RPC client configured for source chain %d", sourceChainID)
	}

	countCalldata, err := d.abi.Pack("getPendingOutboundCount", new(big.Int).SetUint64(destChainID))
	if err != nil {
		return 0, fmt.Errorf("pack getPendingOutboundCount: %w", err)
	}
	countResult, err := sourceClient.EthCallGateway(ctx, countCalldata)
	if err != nil {
		return 0, fmt.Errorf("getPendingOutboundCount: %w", err)
	}
	countOut, err := d.abi.Unpack("getPendingOutboundCount", countResult)
	if err != nil {
		return 0, fmt.Errorf("unpack getPendingOutboundCount: %w", err)
	}
	if countOut[0].(*big.Int).Sign() <= 0 {
		return 0, nil
	}

	batchCalldata, err := d.abi.Pack("batchOutboundCommit", new(big.Int).SetUint64(destChainID))
	if err != nil {
		return 0, fmt.Errorf("pack batchOutboundCommit: %w", err)
	}
	receipt, err := d.sendToChainAndWait(ctx, sourceChainID, batchCalldata, 3_000_000)
	if err != nil {
		return 0, fmt.Errorf("batchOutboundCommit: %w", err)
	}
	if receipt.Status != 1 {
		return 0, fmt.Errorf("batchOutboundCommit reverted: %x", receipt.Return)
	}
	batchOut, err := d.abi.Unpack("batchOutboundCommit", receipt.Return)
	if err != nil {
		return 0, fmt.Errorf("unpack batchOutboundCommit return: %w", err)
	}
	commitRoot := common.Hash(batchOut[0].([32]byte))
	messageCount := int(batchOut[1].(*big.Int).Int64())

	getBatchCalldata, err := d.abi.Pack("getCommitBatch", commitRoot)
	if err != nil {
		return 0, fmt.Errorf("pack getCommitBatch: %w", err)
	}
	getBatchResult, err := sourceClient.EthCallGateway(ctx, getBatchCalldata)
	if err != nil {
		return 0, fmt.Errorf("getCommitBatch: %w", err)
	}
	getBatchOut, err := d.abi.Unpack("getCommitBatch", getBatchResult)
	if err != nil {
		return 0, fmt.Errorf("unpack getCommitBatch: %w", err)
	}
	if !getBatchOut[0].(bool) {
		return 0, fmt.Errorf("getCommitBatch: chain reports commit %s does not exist right after batching it", commitRoot.Hex())
	}
	epoch := getBatchOut[1].(uint64)
	var messages []cross_chain.CrossChainMessage
	if err := json.Unmarshal(getBatchOut[2].([]byte), &messages); err != nil {
		return 0, fmt.Errorf("unmarshal committed batch messages: %w", err)
	}

	logger.Info("📦 [RELAYER DAEMON] batched %d outbound message(s) from chain %d to chain %d, commitRoot=%s", messageCount, sourceChainID, destChainID, commitRoot.Hex())
	if err := d.RelayBatch(ctx, sourceChainID, commitRoot, epoch, messages); err != nil {
		return messageCount, fmt.Errorf("relay batch %s: %w", commitRoot.Hex(), err)
	}
	return messageCount, nil
}

// WatchChainPair loops BatchAndRelay for one (sourceChainID, destChainID) pair until ctx is
// cancelled or Stop() is called -- the real, permissionless watch loop cross_chain_relayer was
// missing entirely (it used to just construct a RelayerDaemon and block on a shutdown signal,
// never calling RelayMessage on its own). Errors are logged, not fatal -- a transient RPC hiccup
// on one tick must not kill the whole watch loop.
func (d *RelayerDaemon) WatchChainPair(ctx context.Context, sourceChainID, destChainID uint64) {
	pairKey := fmt.Sprintf("%d:%d", sourceChainID, destChainID)
	d.watchedPairsMu.Lock()
	if d.watchedPairs == nil {
		d.watchedPairs = make(map[string]bool)
	}
	if d.watchedPairs[pairKey] {
		d.watchedPairsMu.Unlock()
		return
	}
	d.watchedPairs[pairKey] = true
	d.watchedPairsMu.Unlock()

	d.wg.Add(1)
	defer func() {
		d.watchedPairsMu.Lock()
		delete(d.watchedPairs, pairKey)
		d.watchedPairsMu.Unlock()
		d.wg.Done()
	}()
	logger.Info("👀 [RELAYER DAEMON] watching chain %d -> chain %d for outbound messages", sourceChainID, destChainID)

	srcLabel := fmt.Sprintf("%d", sourceChainID)
	dstLabel := fmt.Sprintf("%d", destChainID)
	basePoll := d.config.PollInterval
	if basePoll <= 0 {
		basePoll = 500 * time.Millisecond
	}
	maxBackoff := d.config.MaxPollBackoff
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		default:
		}

		wait := basePoll
		if n, err := d.BatchAndRelay(ctx, sourceChainID, destChainID); err != nil {
			consecutive := d.recordPairFailure(pairKey, err)
			relayWatchErrorsTotal.WithLabelValues(srcLabel, dstLabel).Inc()
			relayConsecutiveErrors.WithLabelValues(srcLabel, dstLabel).Set(float64(consecutive))
			wait = backoffDuration(basePoll, maxBackoff, consecutive)
			logger.Warn("⚠️ [RELAYER DAEMON] batch/relay chain %d -> %d failed (consecutive=%d, next retry in %s): %v", sourceChainID, destChainID, consecutive, wait, err)
		} else {
			d.recordPairSuccess(pairKey)
			relayConsecutiveErrors.WithLabelValues(srcLabel, dstLabel).Set(0)
			relayLastSuccessTimestamp.WithLabelValues(srcLabel, dstLabel).SetToCurrentTime()
			if n > 0 {
				relayMessagesRelayedTotal.WithLabelValues(srcLabel, dstLabel).Add(float64(n))
				logger.Info("✅ [RELAYER DAEMON] relayed %d message(s) from chain %d to chain %d", n, sourceChainID, destChainID)
			}
		}

		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		case <-d.stopCh:
			return
		}
	}
}

// pollAndAggregateCommitCert queries Root Anchor for commit attestation shares and aggregates them.
func (d *RelayerDaemon) pollAndAggregateCommitCert(
	ctx context.Context,
	sourceChainID uint64,
	epoch uint64,
	commitRoot common.Hash,
) (*cross_chain.QuorumCert, error) {
	reg, exists, err := d.rootAnchorClient.GetChainRegistry(ctx, sourceChainID)
	if err != nil {
		return nil, fmt.Errorf("getChainRegistry from Root Anchor: %w", err)
	}
	if !exists || reg == nil {
		return nil, fmt.Errorf("chain %d is not registered on Root Anchor", sourceChainID)
	}

	var totalStake uint64
	for _, v := range reg.Committee {
		totalStake += v.Stake
	}
	if totalStake == 0 {
		return nil, fmt.Errorf("committee for chain %d has 0 total stake", sourceChainID)
	}

	threshold := (totalStake*2 + 2) / 3
	if reg.QuorumThreshold > 0 {
		threshold = (totalStake*reg.QuorumThreshold + 9999) / 10000
	}

	maxIterations := d.config.MaxPollIterations
	if d.config.PollInterval > 0 && time.Duration(maxIterations)*d.config.PollInterval < 20*time.Second {
		maxIterations = int((20 * time.Second) / d.config.PollInterval)
	}

	for i := 0; i < maxIterations; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.stopCh:
			return nil, fmt.Errorf("relayer daemon stopping")
		default:
		}

		pubkeys, sigs, err := d.rootAnchorClient.GetCommitAttestationShares(ctx, sourceChainID, epoch, commitRoot)
		if err == nil && len(pubkeys) > 0 {
			var accumulatedStake uint64
			var validPubkeys [][]byte
			var validSigs [][]byte

			for j := 0; j < len(pubkeys) && j < len(sigs); j++ {
				pk := pubkeys[j]
				sigBytes := sigs[j]
				for _, v := range reg.Committee {
					if bytes.Equal(v.PubkeyBLS, pk) {
						accumulatedStake += v.Stake
						validPubkeys = append(validPubkeys, pk)
						validSigs = append(validSigs, sigBytes)
						break
					}
				}
			}

			if accumulatedStake >= threshold && len(validSigs) > 0 {
				var aggSig []byte
				if len(validSigs) == 1 {
					aggSig = validSigs[0]
				} else {
					aggSig = bls.CreateAggregateSign(validSigs)
				}
				bitmap := cross_chain.BuildSignerBitmap(reg.Committee, validPubkeys)
				return &cross_chain.QuorumCert{
					Epoch:              epoch,
					AggregateSignature: aggSig,
					SignerBitmap:       bitmap,
				}, nil
			}
		}

		select {
		case <-time.After(d.config.PollInterval):
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-d.stopCh:
			return nil, fmt.Errorf("relayer daemon stopping")
		}
	}

	return nil, fmt.Errorf("quorum not reached for chain %d epoch %d commit %s after %d polls", sourceChainID, epoch, commitRoot.Hex(), maxIterations)
}

// Stop gracefully signals the daemon to stop.
func (d *RelayerDaemon) Stop() {
	select {
	case <-d.stopCh:
	default:
		close(d.stopCh)
	}
	d.wg.Wait()
}

// DecodeRevertReason decodes EVM revert return bytes (whether raw ABI encoded Error(string) or hex string)
// into a readable human-friendly message.
func DecodeRevertReason(raw []byte) string {
	if len(raw) == 0 {
		return "empty revert data"
	}
	data := raw
	// If data starts with "0x" (ASCII '0','x'), unhex it first
	if len(data) >= 2 && data[0] == '0' && (data[1] == 'x' || data[1] == 'X') {
		decoded, err := hex.DecodeString(string(data[2:]))
		if err == nil {
			data = decoded
		}
	}
	// Check standard Error(string) selector: 0x08c379a0
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x08, 0xc3, 0x79, 0xa0}) {
		if len(data) >= 68 {
			strLen := new(big.Int).SetBytes(data[36:68]).Uint64()
			if uint64(len(data)) >= 68+strLen {
				return string(data[68 : 68+strLen])
			}
		}
	}
	// Check Panic(uint256) selector: 0x4e487b71
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x4e, 0x48, 0x7b, 0x71}) {
		if len(data) >= 36 {
			panicCode := new(big.Int).SetBytes(data[4:36])
			return fmt.Sprintf("Panic(0x%x)", panicCode)
		}
	}
	// If printable ASCII, return as string
	isPrintable := true
	for _, b := range data {
		if b < 32 && b != '\n' && b != '\r' && b != '\t' {
			isPrintable = false
			break
		}
	}
	if isPrintable && len(data) > 0 {
		return string(data)
	}
	return hexutil.Encode(raw)
}
