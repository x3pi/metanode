package relayer_daemon

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
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

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
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
}

// RelayerDaemon is the automated production daemon that watches for cross-chain messages,
// aggregates BLS QuorumCerts from Root Anchor, and executes claims on destination chains.
type RelayerDaemon struct {
	mu                 sync.RWMutex
	config             DaemonConfig
	relayerKey         *ecdsa.PrivateKey
	relayerAddr        common.Address
	rootAnchorClient   *rootanchor.Client
	chainClients       map[uint64]*rootanchor.Client
	abi                abi.ABI
	processedMessages  map[common.Hash]bool
	attestedCommits    map[string]bool // key: "destChainId:commitRootHex"
	stopCh             chan struct{}
	wg                 sync.WaitGroup
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
		stopCh:            make(chan struct{}),
	}, nil
}

// Address returns the Relayer's Ethereum-compatible public address.
func (d *RelayerDaemon) Address() common.Address {
	return d.relayerAddr
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
	destClient, exists := d.chainClients[msg.DestChainID]
	if !exists {
		return common.Hash{}, fmt.Errorf("no RPC client configured for destination chain %d", msg.DestChainID)
	}

	chainIDBig, err := destClient.ChainID(ctx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("query destination chain ID: %w", err)
	}

	nonce, err := destClient.GetTransactionCount(ctx, d.relayerAddr)
	if err != nil {
		return common.Hash{}, fmt.Errorf("query relayer nonce on dest chain: %w", err)
	}

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

	gwAddr := mt_common.GATEWAY_CONTRACT_ADDRESS
	txData := &ethtypes.LegacyTx{
		Nonce:    nonce,
		GasPrice: big.NewInt(1_000_000_000), // 1 Gwei
		Gas:      500_000,
		To:       &gwAddr,
		Value:    big.NewInt(0),
		Data:     calldata,
	}

	signer := ethtypes.NewEIP155Signer(chainIDBig)
	signedTx, err := ethtypes.SignTx(ethtypes.NewTx(txData), signer, d.relayerKey)
	if err != nil {
		return common.Hash{}, fmt.Errorf("sign relay transaction: %w", err)
	}

	rawBytes, err := signedTx.MarshalBinary()
	if err != nil {
		return common.Hash{}, fmt.Errorf("marshal signed relay tx: %w", err)
	}

	txHash, err := destClient.SendRawTransaction(ctx, hexutil.Encode(rawBytes))
	if err != nil {
		return common.Hash{}, fmt.Errorf("broadcast verifyAndExecute tx: %w", err)
	}

	d.mu.Lock()
	d.processedMessages[msg.MessageID] = true
	d.mu.Unlock()

	logger.Info("🚀 [RELAYER DAEMON] successfully relayed message %s to chain %d (tx=%s)", msg.MessageID.Hex(), msg.DestChainID, txHash.Hex())
	return txHash, nil
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

	for i := 0; i < d.config.MaxPollIterations; i++ {
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

	return nil, fmt.Errorf("quorum not reached for chain %d epoch %d commit %s after %d polls", sourceChainID, epoch, commitRoot.Hex(), d.config.MaxPollIterations)
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
