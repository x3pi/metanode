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
	"github.com/meta-node-blockchain/meta-node/pkg/network"
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

	// UnrelayedBatchesPersistPath (2026-09-05 review of PR #102's unrelayedBatches retry
	// mechanism): PR #102's in-memory-only retry queue closes the "destination chain briefly
	// restarts while the relayer PROCESS keeps running" gap, but not "the relayer's own process
	// restarts mid-retry" -- at that point the on-chain PendingOutboundMessages queue has ALREADY
	// been drained by the batchOutboundCommit() that produced the stuck batch, so a fresh
	// process's normal BatchAndRelay flow (which only ever looks at getPendingOutboundCount) can
	// never rediscover it: the batch would silently sit committed-but-unclaimed forever. Setting
	// this to a writable file path persists unrelayedBatches to disk on every change and reloads
	// it on startup, closing that gap too. Empty/unset (default) preserves the exact in-memory-only
	// behavior PR #102 shipped with.
	UnrelayedBatchesPersistPath string `json:"unrelayed_batches_persist_path,omitempty" yaml:"unrelayed_batches_persist_path,omitempty"`
}

// unrelayedBatch is a batchOutboundCommit() result not yet fully relayed (see BatchAndRelay).
// Fields are exported solely so persistence.go can encoding/json round-trip this type to/from
// disk -- it is otherwise only ever used inside this package.
type unrelayedBatch struct {
	CommitRoot common.Hash                     `json:"commit_root"`
	Epoch      uint64                          `json:"epoch"`
	Messages   []cross_chain.CrossChainMessage `json:"messages"`
}

// pendingRefund is a message known to have finalized as MessageStatusFailed on its destination
// chain, not yet successfully refunded on its source chain (see RelayerDaemon.pendingRefunds).
type pendingRefund struct {
	msg        cross_chain.CrossChainMessage
	commitRoot common.Hash
	proof      cross_chain.MerkleProof
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
	unrelayedBatches  map[string]*unrelayedBatch // key: "srcChainId:destChainId"
	// pendingRefunds tracks messages this daemon observed finalize as MessageStatusFailed on
	// their destination chain but has not yet successfully refunded on their source chain (mục
	// 2.4 / 2026-09-05 finding #1 fix) -- e.g. the failure-attestation QuorumCert wasn't at
	// quorum yet, or the refund() send itself failed transiently. Retried at the start of every
	// BatchAndRelay tick for that message's own source chain (see retryPendingRefunds).
	pendingRefunds map[common.Hash]*pendingRefund
	nonces         map[uint64]uint64
	nonceMu        sync.Mutex
	chainLocks     map[uint64]*sync.Mutex
	chainLocksMu   sync.Mutex
	stopCh         chan struct{}
	wg             sync.WaitGroup

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

	// RelayerDaemon manages its own poll intervals and retries; disable the 60s circuit breaker lockout
	// so restarts of local nodes or temporary blips do not permanently lock out the relayer.
	breakerCfg := &network.CircuitBreakerConfig{
		Disabled: true,
	}

	raClient, err := rootanchor.NewClient(cfg.RootAnchorURLs, breakerCfg)
	if err != nil {
		return nil, fmt.Errorf("connecting to Root Anchor: %w", err)
	}

	chainClients := make(map[uint64]*rootanchor.Client, len(cfg.ChainRPCURLs))
	for chainID, url := range cfg.ChainRPCURLs {
		c, err := rootanchor.NewClient([]string{url}, breakerCfg)
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
		unrelayedBatches:  loadUnrelayedBatches(cfg.UnrelayedBatchesPersistPath),
		pendingRefunds:    make(map[common.Hash]*pendingRefund),
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
	c, err := rootanchor.NewClient([]string{rpcURL}, &network.CircuitBreakerConfig{Disabled: true})
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

		// SECURITY FIX (2026-09-05, finding #1 / mục 2.4): claimMessage's transaction now
		// SUCCEEDS (receipt.Status == 1) even when the destination payload reverted -- it
		// finalizes the message as MessageStatusFailed instead of hard-reverting (see
		// gateway_handler.go's claimMessage case). A successful receipt therefore no longer
		// implies "value was delivered"; check the message's actual resolved status before
		// treating this as a normal successful relay.
		if finalStatus := d.getMessageStatus(ctx, destClient, msg.MessageID); finalStatus == cross_chain.MessageStatusFailed {
			logger.Info("💥 [RELAYER DAEMON] claimMessage for %s finalized as FAILED on chain %d (destination payload reverted) -- pursuing refund on source chain %d", msg.MessageID.Hex(), destChainID, msg.SourceChainID)
			if err := d.processFailedClaim(ctx, msg, commitRoot, msgProof); err != nil {
				logger.Warn("⚠️ [RELAYER DAEMON] refund pursuit for failed message %s did not complete: %v -- queued for retry on chain %d's next watch tick", msg.MessageID.Hex(), err, msg.SourceChainID)
				d.mu.Lock()
				if d.pendingRefunds == nil {
					d.pendingRefunds = make(map[common.Hash]*pendingRefund)
				}
				d.pendingRefunds[msg.MessageID] = &pendingRefund{msg: msg, commitRoot: commitRoot, proof: msgProof}
				d.mu.Unlock()
			}
			continue
		}

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
			// SECURITY FIX (2026-09-05, "Cross-Chain Ledger Inflation via Missing Reserve
			// Refund" finding): creditReserveAllocation now REQUIRES a real success QuorumCert
			// signed by destChainID's own registered committee -- see
			// GatewayEngine.CreditReserveAllocation's doc comment. Before this fix, this call
			// carried no proof at all that the message actually succeeded; anyone could inflate
			// Reserve's ledger for destChainID regardless of the real outcome.
			destRegistry, existsDest, errDestReg := d.rootAnchorClient.GetChainRegistry(ctx, destChainID)
			if errDestReg != nil || !existsDest || destRegistry == nil {
				logger.Warn("⚠️ [RELAYER DAEMON] creditReserveAllocation for %s: could not fetch chain %d registry: %v", msg.MessageID.Hex(), destChainID, errDestReg)
			} else if successCert, errCert := d.pollAndAggregateSuccessCert(ctx, destChainID, msg.MessageID, destRegistry.Epoch); errCert != nil {
				// Best-effort, fail-closed: Reserve's ledger simply stays un-credited until this
				// succeeds (safe -- never lets more value out than was ever really allocated),
				// resolvable by re-running this same call later since it is idempotent.
				logger.Warn("⚠️ [RELAYER DAEMON] creditReserveAllocation for %s: could not aggregate success cert (will not credit Reserve this tick): %v", msg.MessageID.Hex(), errCert)
			} else {
				creditCalldata, err := d.abi.Pack("creditReserveAllocation",
					msg.MessageID, new(big.Int).SetUint64(msg.SourceChainID), new(big.Int).SetUint64(msg.DestChainID),
					new(big.Int).SetUint64(msg.Sequence), msg.HopCount, msg.Sender, msg.Target,
					msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
					new(big.Int).SetUint64(msgProof.LeafIndex), toBytes32Slice(msgProof.Siblings), commitRoot,
					successCert.Epoch, []byte(successCert.AggregateSignature), []byte(successCert.SignerBitmap),
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
	}
	return nil
}

// processFailedClaim pursues mục 2.4's refund path for a message this daemon just observed
// finalize as MessageStatusFailed on its destination chain (2026-09-05 fix for
// note/cross_chain/security_audit_findings.md finding #1 "Permanent Lock of Funds / DoS on
// Payload Revert"): polls Root Anchor for the destination committee's failure-attestation shares
// (produced by tx_processor.MessageFailureAttestationWorker, running on each destination-chain
// validator once its own local execution deterministically finalized this same message as
// Failed), aggregates them into a real QuorumCert once quorum is reached, then submits refund()
// on the message's SOURCE chain -- completing the loop GatewayEngine.Refund() alone could never
// complete since nothing before this fix ever produced that cert in production.
func (d *RelayerDaemon) processFailedClaim(ctx context.Context, msg cross_chain.CrossChainMessage, commitRoot common.Hash, msgProof cross_chain.MerkleProof) error {
	destRegistry, exists, err := d.rootAnchorClient.GetChainRegistry(ctx, msg.DestChainID)
	if err != nil {
		return fmt.Errorf("getChainRegistry for dest chain %d: %w", msg.DestChainID, err)
	}
	if !exists || destRegistry == nil {
		return fmt.Errorf("dest chain %d not registered on Root Anchor", msg.DestChainID)
	}

	cert, err := d.pollAndAggregateFailureCert(ctx, msg.DestChainID, msg.MessageID, destRegistry.Epoch)
	if err != nil {
		return fmt.Errorf("aggregate failure QuorumCert: %w", err)
	}

	// Idempotent re-entry: a previous tick may already have refunded the source-chain side (Tip +
	// GasFee, and Value too for a non-2-hop message -- see the is2Hop check below) before failing
	// on the Reserve-side step that follows (Reserve temporarily unreachable, transient send
	// error, ...). Check status FIRST so a retry never re-submits refund() against an
	// already-Refunded message -- that transaction would revert (Refund()'s own "status must be
	// Pending" guard), which would look exactly like this whole attempt failed and mask the fact
	// that only the Reserve-side step below still needs to run.
	alreadyRefundedOnSource := false
	if sourceClient, ok := d.GetChainClient(msg.SourceChainID); ok {
		alreadyRefundedOnSource = d.getMessageStatus(ctx, sourceClient, msg.MessageID) == cross_chain.MessageStatusRefunded
	}

	if !alreadyRefundedOnSource {
		refundCalldata, err := d.abi.Pack("refund",
			msg.MessageID, new(big.Int).SetUint64(msg.SourceChainID), new(big.Int).SetUint64(msg.DestChainID),
			new(big.Int).SetUint64(msg.Sequence), msg.HopCount, msg.Sender, msg.Target,
			msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
			new(big.Int).SetUint64(msgProof.LeafIndex), toBytes32Slice(msgProof.Siblings), commitRoot,
			cert.Epoch, []byte(cert.AggregateSignature), []byte(cert.SignerBitmap),
		)
		if err != nil {
			return fmt.Errorf("pack refund: %w", err)
		}

		receipt, err := d.sendToChainAndWait(ctx, msg.SourceChainID, refundCalldata, 500_000)
		if err != nil {
			return fmt.Errorf("send refund tx: %w", err)
		}
		if receipt.Status != 1 {
			return fmt.Errorf("refund tx reverted: %s", DecodeRevertReason(receipt.Return))
		}
		logger.Info("💸 [RELAYER DAEMON] refunded message %s to sender %s on source chain %d", msg.MessageID.Hex(), msg.Sender.Hex(), msg.SourceChainID)
	}

	// CORRECTNESS FIX (2026-09-05, "Total Supply Deflation" follow-up to finding #2 -- see
	// note/cross_chain/security_audit_findings.md): for a 2-hop message (both SourceChainID and
	// DestChainID are real private chains, neither IS Reserve), refund() above deliberately does
	// NOT restore Value -- see the is2Hop comment on Refund()/the "refund" dispatch case in
	// gateway.go/gateway_handler.go. Value was debited from sourceChainID's allocation on
	// RESERVE's OWN ledger (attestCommit for a native-value commit can only ever be submitted to
	// Reserve -- see chooseAttestMethod/RelayBatch above), never touched on sourceChainID's own
	// node, so only Reserve's own refundReserveAllocation() can restore it: it reverses any credit
	// already made to destChainID (defensive -- CreditReserveAllocation's own success-cert
	// requirement should already make this a no-op for a genuinely FAILED message) and emits a
	// fresh Reserve->source outbound message carrying Value, which then flows through the exact
	// same attest/claim/creditReserveAllocation pipeline as any other transfer -- see
	// GatewayEngine.RefundReserveAllocation's own doc comment. Before this fix, NOTHING in
	// production ever called refundReserveAllocation at all (confirmed by grep) -- once refund()
	// above marked a 2-hop message Refunded on the source chain, its Value was gone for good.
	is2Hop := d.config.ReserveChainID != 0 && msg.SourceChainID != d.config.ReserveChainID && msg.DestChainID != d.config.ReserveChainID
	if !is2Hop {
		return nil
	}

	if reserveClient, ok := d.GetChainClient(d.config.ReserveChainID); ok {
		if d.getMessageStatus(ctx, reserveClient, msg.MessageID) == cross_chain.MessageStatusRefunded {
			// Already processed by an earlier tick / a different relayer -- idempotent no-op.
			return nil
		}
	}

	refundReserveCalldata, err := d.abi.Pack("refundReserveAllocation",
		msg.MessageID, new(big.Int).SetUint64(msg.SourceChainID), new(big.Int).SetUint64(msg.DestChainID),
		new(big.Int).SetUint64(msg.Sequence), msg.HopCount, msg.Sender, msg.Target,
		msg.AssetID, msg.Value, msg.Payload, msg.Tip, msg.GasFee, msg.Ordered,
		new(big.Int).SetUint64(msgProof.LeafIndex), toBytes32Slice(msgProof.Siblings), commitRoot,
		cert.Epoch, []byte(cert.AggregateSignature), []byte(cert.SignerBitmap),
	)
	if err != nil {
		return fmt.Errorf("pack refundReserveAllocation: %w", err)
	}
	reserveReceipt, err := d.sendToChainAndWait(ctx, d.config.ReserveChainID, refundReserveCalldata, 500_000)
	if err != nil {
		return fmt.Errorf("send refundReserveAllocation tx: %w", err)
	}
	if reserveReceipt.Status != 1 {
		return fmt.Errorf("refundReserveAllocation tx reverted: %s", DecodeRevertReason(reserveReceipt.Return))
	}
	logger.Info("💰 [RELAYER DAEMON] refundReserveAllocation for message %s reversed the Reserve-side allocation and queued a fresh Value refund from reserve chain %d back to source chain %d", msg.MessageID.Hex(), d.config.ReserveChainID, msg.SourceChainID)
	return nil
}

// pollAndAggregateFailureCert mirrors pollAndAggregateCommitCert exactly, polling for
// getMessageFailureAttestationShares instead of getCommitAttestationShares -- see that function's
// doc comment for the polling/threshold logic, identical here.
func (d *RelayerDaemon) pollAndAggregateFailureCert(
	ctx context.Context,
	destChainID uint64,
	messageID common.Hash,
	epoch uint64,
) (*cross_chain.QuorumCert, error) {
	reg, exists, err := d.rootAnchorClient.GetChainRegistry(ctx, destChainID)
	if err != nil {
		return nil, fmt.Errorf("getChainRegistry from Root Anchor: %w", err)
	}
	if !exists || reg == nil {
		return nil, fmt.Errorf("chain %d is not registered on Root Anchor", destChainID)
	}

	var totalStake uint64
	for _, v := range reg.Committee {
		totalStake += v.Stake
	}
	if totalStake == 0 {
		return nil, fmt.Errorf("committee for chain %d has 0 total stake", destChainID)
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

		pubkeys, sigs, err := d.rootAnchorClient.GetMessageFailureAttestationShares(ctx, destChainID, messageID, epoch)
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

	return nil, fmt.Errorf("quorum not reached for chain %d message %s after %d polls", destChainID, messageID.Hex(), maxIterations)
}

// pollAndAggregateSuccessCert mirrors pollAndAggregateFailureCert exactly, polling for
// getMessageSuccessAttestationShares instead -- the mirror-image cert
// GatewayEngine.CreditReserveAllocation now requires (2026-09-05 fix, "Cross-Chain Ledger
// Inflation via Missing Reserve Refund" finding).
func (d *RelayerDaemon) pollAndAggregateSuccessCert(
	ctx context.Context,
	destChainID uint64,
	messageID common.Hash,
	epoch uint64,
) (*cross_chain.QuorumCert, error) {
	reg, exists, err := d.rootAnchorClient.GetChainRegistry(ctx, destChainID)
	if err != nil {
		return nil, fmt.Errorf("getChainRegistry from Root Anchor: %w", err)
	}
	if !exists || reg == nil {
		return nil, fmt.Errorf("chain %d is not registered on Root Anchor", destChainID)
	}

	var totalStake uint64
	for _, v := range reg.Committee {
		totalStake += v.Stake
	}
	if totalStake == 0 {
		return nil, fmt.Errorf("committee for chain %d has 0 total stake", destChainID)
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

		pubkeys, sigs, err := d.rootAnchorClient.GetMessageSuccessAttestationShares(ctx, destChainID, messageID, epoch)
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

	return nil, fmt.Errorf("quorum not reached for chain %d message %s after %d polls", destChainID, messageID.Hex(), maxIterations)
}

// retryPendingRefunds retries every message queued in d.pendingRefunds whose SOURCE chain is
// sourceChainID (refund() is always submitted to the source chain -- this is naturally called
// once per tick by the exact WatchChainPair(sourceChainID, destChainID) loop that would have
// otherwise driven that chain's traffic anyway, so no separate watch loop is needed). Best-effort:
// errors are logged, never returned -- a stuck refund must never block this tick's normal
// batch/relay work.
func (d *RelayerDaemon) retryPendingRefunds(ctx context.Context, sourceChainID, destChainID uint64) {
	d.mu.Lock()
	var toRetry []*pendingRefund
	for _, pr := range d.pendingRefunds {
		if pr.msg.SourceChainID == sourceChainID {
			toRetry = append(toRetry, pr)
		}
	}
	d.mu.Unlock()

	for _, pr := range toRetry {
		if err := d.processFailedClaim(ctx, pr.msg, pr.commitRoot, pr.proof); err != nil {
			logger.Warn("⚠️ [RELAYER DAEMON] retry refund for %s still not complete: %v", pr.msg.MessageID.Hex(), err)
			continue
		}
		d.mu.Lock()
		delete(d.pendingRefunds, pr.msg.MessageID)
		d.mu.Unlock()
	}
}

// BatchAndRelay is the single unit of work a watch loop performs for one (sourceChainID,
// destChainID) pair: if there are real pending outbound() messages queued on sourceChainID for
// destChainID, submit a real batchOutboundCommit() there, then immediately relay the resulting
// batch (RelayBatch). Returns (0, nil) with no error when there was nothing pending -- not an
// error case, just nothing to do this tick.
func (d *RelayerDaemon) BatchAndRelay(ctx context.Context, sourceChainID, destChainID uint64) (int, error) {
	pairKey := fmt.Sprintf("%d:%d", sourceChainID, destChainID)

	// Retry any messages known to have finalized as Failed on destChainID but not yet
	// successfully refunded on sourceChainID (mục 2.4 / 2026-09-05 finding #1 fix) -- best-effort,
	// errors are logged but never block this tick's normal batch/relay work below.
	d.retryPendingRefunds(ctx, sourceChainID, destChainID)

	// Check if there is an unrelayed batch from a previous attempt (e.g. destination was restarting)
	d.mu.Lock()
	if d.unrelayedBatches == nil {
		d.unrelayedBatches = make(map[string]*unrelayedBatch)
	}
	pending := d.unrelayedBatches[pairKey]
	d.mu.Unlock()

	if pending != nil {
		if err := d.RelayBatch(ctx, sourceChainID, pending.CommitRoot, pending.Epoch, pending.Messages); err != nil {
			return len(pending.Messages), fmt.Errorf("retry relay batch %s: %w", pending.CommitRoot.Hex(), err)
		}
		d.mu.Lock()
		delete(d.unrelayedBatches, pairKey)
		snapshot := d.snapshotUnrelayedBatchesLocked()
		d.mu.Unlock()
		d.persistUnrelayedBatches(snapshot)
		return len(pending.Messages), nil
	}

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

	// Store in unrelayedBatches in case RelayBatch fails (e.g. destination chain is restarting) --
	// and persist to disk if configured, so a relayer PROCESS restart (not just a destination
	// chain restart) can still rediscover and retry this batch on the next startup instead of
	// leaking it forever (see UnrelayedBatchesPersistPath's doc comment).
	d.mu.Lock()
	if d.unrelayedBatches == nil {
		d.unrelayedBatches = make(map[string]*unrelayedBatch)
	}
	d.unrelayedBatches[pairKey] = &unrelayedBatch{
		CommitRoot: commitRoot,
		Epoch:      epoch,
		Messages:   messages,
	}
	snapshot := d.snapshotUnrelayedBatchesLocked()
	d.mu.Unlock()
	d.persistUnrelayedBatches(snapshot)

	if err := d.RelayBatch(ctx, sourceChainID, commitRoot, epoch, messages); err != nil {
		return messageCount, fmt.Errorf("relay batch %s: %w", commitRoot.Hex(), err)
	}

	d.mu.Lock()
	delete(d.unrelayedBatches, pairKey)
	snapshot = d.snapshotUnrelayedBatchesLocked()
	d.mu.Unlock()
	d.persistUnrelayedBatches(snapshot)

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
		if (b < 32 || b > 126) && b != '\n' && b != '\r' && b != '\t' {
			isPrintable = false
			break
		}
	}
	if isPrintable && len(data) > 0 {
		return string(data)
	}
	return hexutil.Encode(raw)
}
