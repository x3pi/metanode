// Package rootanchor implements Milestone B of the Root Anchor wiring plan (see
// note/cross_chain_root_anchor_architecture.md and the Milestone B plan doc): a real network RPC
// channel from a private chain to an external "Root Anchor" network.
//
// Root Anchor is deployed as an ordinary Metanode network (mục 3 of the design doc — the same
// tooling built in Milestone D), running the same GatewayHandler every private chain runs
// (Milestone A). "Talking to Root Anchor" is therefore nothing more than talking JSON-RPC
// (eth_call / eth_sendRawTransaction) to another Metanode node — no new wire protocol, no new
// proto. This intentionally lives entirely in Go: an earlier version of the wiring plan sketched
// this channel in Rust, but investigation for Milestone B found no usable Rust precedent
// (consensus/metanode/src/node/peer_go_client.rs is dead code coupled to the internal Go<->Rust
// FFI proto; consensus/metanode/src/node/rpc_circuit_breaker.rs's record_success/record_failure
// are confirmed to never be called outside #[cfg(test)]) while Go already has everything needed
// and proven in production: execution/pkg/network.CircuitBreaker (wired in pkg/network/handler.go)
// and the JSON-RPC client pattern in execution/cmd/tool/register_validator/main.go.
package rootanchor

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
)

// ErrCircuitOpen is returned when the circuit breaker is open and no RPC was attempted — the
// caller should treat this exactly like a network failure (Zero-Fork: cross-chain paused, local
// chain unaffected — see mục 5.4 of the design doc).
var ErrCircuitOpen = fmt.Errorf("root anchor RPC circuit breaker is open")

// DefaultTimeout bounds a single RPC attempt. Callers (the periodic refresh worker) run on a
// background goroutine, never on a consensus-critical or FFI-blocking path, so a generous but
// finite timeout is appropriate — see the Milestone B plan doc for why this must never be called
// from Rust's epoch-transition path.
const DefaultTimeout = 10 * time.Second

// Client talks to a Root Anchor network's JSON-RPC endpoint(s). Safe for concurrent use.
type Client struct {
	urls    []string
	breaker *network.CircuitBreaker
	abi     abi.ABI
	timeout time.Duration
}

// NewClient builds a Client. urls are tried in order on each call (first success wins); at least
// one URL is required. breakerConfig may be nil to use network.DefaultCircuitBreakerConfig().
func NewClient(urls []string, breakerConfig *network.CircuitBreakerConfig) (*Client, error) {
	if len(urls) == 0 {
		return nil, fmt.Errorf("rootanchor.NewClient: at least one RPC URL is required")
	}
	parsedABI, err := abi.JSON(strings.NewReader(abi_contract.GatewayABI))
	if err != nil {
		return nil, fmt.Errorf("rootanchor.NewClient: parsing GatewayABI: %w", err)
	}
	return &Client{
		urls:    urls,
		breaker: network.NewCircuitBreaker(breakerConfig),
		abi:     parsedABI,
		timeout: DefaultTimeout,
	}, nil
}

// GetChainRegistry reads a chain's ChainRegistry entry from Root Anchor via eth_call to
// getChainRegistry(). Returns (nil, false, nil) if Root Anchor reports the chain is not
// registered (not an error — see gateway_handler.go's exists bool). Returns a non-nil error only
// on a genuine RPC/decode failure or an open circuit breaker.
func (c *Client) GetChainRegistry(ctx context.Context, chainID uint64) (*cross_chain.ChainRegistry, bool, error) {
	calldata, err := c.abi.Pack("getChainRegistry", new(big.Int).SetUint64(chainID))
	if err != nil {
		return nil, false, fmt.Errorf("pack getChainRegistry: %w", err)
	}

	result, err := c.ethCall(ctx, calldata)
	if err != nil {
		return nil, false, err
	}

	outValues, err := c.abi.Unpack("getChainRegistry", result)
	if err != nil {
		return nil, false, fmt.Errorf("unpack getChainRegistry output: %w", err)
	}
	// 2026-09-04: getChainRegistry grew 2 more trailing outputs (genesisWallet, genesisDigest) for
	// the deterministic-genesis design (see ChainRegistry.GenesisWallet/GenesisDigest's own doc
	// comments) -- 13, not the original 11.
	if len(outValues) != 13 {
		return nil, false, fmt.Errorf("getChainRegistry: expected 13 output values, got %d", len(outValues))
	}

	exists, _ := outValues[0].(bool)
	if !exists {
		return nil, false, nil
	}

	pubkeys, _ := outValues[1].([][]byte)
	stakes, _ := outValues[2].([]uint64)
	popSignatures, _ := outValues[3].([][]byte)
	epoch, _ := outValues[4].(uint64)
	quorumThreshold, _ := outValues[5].(uint64)
	gatewayContract, _ := outValues[6].(common.Address)
	stateRootRaw, _ := outValues[7].([32]byte)
	accountTreeRootRaw, _ := outValues[8].([32]byte)
	archivalEndpoint, _ := outValues[9].(string)
	registeredAt, _ := outValues[10].(uint64)
	genesisWallet, _ := outValues[11].(common.Address)
	genesisDigestRaw, _ := outValues[12].([32]byte)

	if len(pubkeys) != len(stakes) || len(pubkeys) != len(popSignatures) {
		return nil, false, fmt.Errorf("getChainRegistry: mismatched committee array lengths (pubkeys=%d stakes=%d popSignatures=%d)",
			len(pubkeys), len(stakes), len(popSignatures))
	}
	committee := make([]cross_chain.ValidatorEntry, len(pubkeys))
	for i := range pubkeys {
		committee[i] = cross_chain.ValidatorEntry{
			PubkeyBLS:    pubkeys[i],
			Stake:        stakes[i],
			PopSignature: popSignatures[i],
		}
	}

	registry := &cross_chain.ChainRegistry{
		ChainID:          chainID,
		Committee:        committee,
		Epoch:            epoch,
		QuorumThreshold:  quorumThreshold,
		GatewayContract:  gatewayContract,
		StateRoot:        common.Hash(stateRootRaw),
		AccountTreeRoot:  common.Hash(accountTreeRootRaw),
		ArchivalEndpoint: archivalEndpoint,
		RegisteredAt:     registeredAt,
		GenesisWallet:    genesisWallet,
		GenesisDigest:    common.Hash(genesisDigestRaw),
	}
	return registry, true, nil
}

// GetRegisteredPop reads a pubkey's registered Proof-of-Possession from Root Anchor via eth_call
// to getRegisteredPop() (Milestone C). Returns an empty slice, not an error, if nothing has been
// registered for that pubkey yet.
func (c *Client) GetRegisteredPop(ctx context.Context, pubkeyBls []byte) ([]byte, error) {
	calldata, err := c.abi.Pack("getRegisteredPop", pubkeyBls)
	if err != nil {
		return nil, fmt.Errorf("pack getRegisteredPop: %w", err)
	}
	result, err := c.ethCall(ctx, calldata)
	if err != nil {
		return nil, err
	}
	outValues, err := c.abi.Unpack("getRegisteredPop", result)
	if err != nil {
		return nil, fmt.Errorf("unpack getRegisteredPop output: %w", err)
	}
	if len(outValues) != 1 {
		return nil, fmt.Errorf("getRegisteredPop: expected 1 output value, got %d", len(outValues))
	}
	pop, _ := outValues[0].([]byte)
	return pop, nil
}

// GetCommitteeAttestationShares reads the currently-collected BLS attestation shares for a
// pending CommitteeUpdate from Root Anchor via eth_call to getCommitteeAttestationShares()
// (Milestone C).
func (c *Client) GetCommitteeAttestationShares(ctx context.Context, sourceChainID, oldEpoch uint64, payloadHash common.Hash) (pubkeys [][]byte, signatures [][]byte, err error) {
	calldata, err := c.abi.Pack("getCommitteeAttestationShares", new(big.Int).SetUint64(sourceChainID), oldEpoch, payloadHash)
	if err != nil {
		return nil, nil, fmt.Errorf("pack getCommitteeAttestationShares: %w", err)
	}
	result, err := c.ethCall(ctx, calldata)
	if err != nil {
		return nil, nil, err
	}
	outValues, err := c.abi.Unpack("getCommitteeAttestationShares", result)
	if err != nil {
		return nil, nil, fmt.Errorf("unpack getCommitteeAttestationShares output: %w", err)
	}
	if len(outValues) != 2 {
		return nil, nil, fmt.Errorf("getCommitteeAttestationShares: expected 2 output values, got %d", len(outValues))
	}
	pubkeys, _ = outValues[0].([][]byte)
	signatures, _ = outValues[1].([][]byte)
	return pubkeys, signatures, nil
}

// GetCommitAttestationShares reads the currently-collected BLS attestation shares for a
// pending commit root from Root Anchor via eth_call to getCommitAttestationShares()
// (Milestone F).
func (c *Client) GetCommitAttestationShares(ctx context.Context, sourceChainID, epoch uint64, commitRoot common.Hash) (pubkeys [][]byte, signatures [][]byte, err error) {
	calldata, err := c.abi.Pack("getCommitAttestationShares", new(big.Int).SetUint64(sourceChainID), epoch, commitRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("pack getCommitAttestationShares: %w", err)
	}
	result, err := c.ethCall(ctx, calldata)
	if err != nil {
		return nil, nil, err
	}
	outValues, err := c.abi.Unpack("getCommitAttestationShares", result)
	if err != nil {
		return nil, nil, fmt.Errorf("unpack getCommitAttestationShares output: %w", err)
	}
	if len(outValues) != 2 {
		return nil, nil, fmt.Errorf("getCommitAttestationShares: expected 2 output values, got %d", len(outValues))
	}
	pubkeys, _ = outValues[0].([][]byte)
	signatures, _ = outValues[1].([][]byte)
	return pubkeys, signatures, nil
}

// SubmitTransaction sends a pre-signed raw transaction to Root Anchor via eth_sendRawTransaction.
// Deliberately generic — it does not know or care what the transaction does. This is the
// transport Milestone C's CommitteeUpdate submission will use; building that payload and deciding
// when to call this is entirely Milestone C's job, not this package's.
func (c *Client) SubmitTransaction(ctx context.Context, signedRawTx []byte) (common.Hash, error) {
	if !c.breaker.CanExecute() {
		return common.Hash{}, ErrCircuitOpen
	}

	var txHashHex string
	err := c.callRPC(ctx, "eth_sendRawTransaction", &txHashHex, hexutil.Encode(signedRawTx))
	if err != nil {
		c.breaker.RecordFailure()
		return common.Hash{}, err
	}
	c.breaker.RecordSuccess()
	return common.HexToHash(txHashHex), nil
}

// GetTransactionCount returns address's current nonce on Root Anchor (eth_getTransactionCount,
// "latest") — needed to build a new transaction to submit via SubmitTransaction. Added in
// Milestone C, the first consumer that needs to construct (not just relay) a signed transaction.
func (c *Client) GetTransactionCount(ctx context.Context, address common.Address) (uint64, error) {
	if !c.breaker.CanExecute() {
		return 0, ErrCircuitOpen
	}
	var result hexutil.Uint64
	err := c.callRPC(ctx, "eth_getTransactionCount", &result, address.Hex(), "latest")
	if err != nil {
		c.breaker.RecordFailure()
		return 0, err
	}
	c.breaker.RecordSuccess()
	return uint64(result), nil
}

// GetPendingTransactionCount returns address's pending nonce on Root Anchor (eth_getTransactionCount,
// "pending"). This is crucial for RelayerDaemon to avoid double-submits or nonce-too-low errors
// when submitting multiple transactions quickly or recovering from a crash while txs are pending.
func (c *Client) GetPendingTransactionCount(ctx context.Context, address common.Address) (uint64, error) {
	if !c.breaker.CanExecute() {
		return 0, ErrCircuitOpen
	}
	var result hexutil.Uint64
	err := c.callRPC(ctx, "eth_getTransactionCount", &result, address.Hex(), "pending")
	if err != nil {
		c.breaker.RecordFailure()
		return 0, err
	}
	c.breaker.RecordSuccess()
	return uint64(result), nil
}

// ChainID returns Root Anchor's own chain ID (eth_chainId) — needed to build an EIP-155-signed
// transaction addressed to it (Milestone C).
func (c *Client) ChainID(ctx context.Context) (*big.Int, error) {
	if !c.breaker.CanExecute() {
		return nil, ErrCircuitOpen
	}
	var result hexutil.Big
	err := c.callRPC(ctx, "eth_chainId", &result)
	if err != nil {
		c.breaker.RecordFailure()
		return nil, err
	}
	c.breaker.RecordSuccess()
	return (*big.Int)(&result), nil
}

// ethCall performs a read-only eth_call against GATEWAY_CONTRACT_ADDRESS with the given calldata,
// wrapped by the circuit breaker.
func (c *Client) ethCall(ctx context.Context, calldata []byte) ([]byte, error) {
	if !c.breaker.CanExecute() {
		return nil, ErrCircuitOpen
	}

	callObject := map[string]interface{}{
		"to":   mt_common.GATEWAY_CONTRACT_ADDRESS.Hex(),
		"data": hexutil.Encode(calldata),
	}

	var resultHex hexutil.Bytes
	err := c.callRPC(ctx, "eth_call", &resultHex, callObject, "latest")
	if err != nil {
		c.breaker.RecordFailure()
		return nil, err
	}
	c.breaker.RecordSuccess()
	return resultHex, nil
}

// EthCallGateway executes a raw read-only call against Root Anchor's Gateway contract.
func (c *Client) EthCallGateway(ctx context.Context, calldata []byte) ([]byte, error) {
	return c.ethCall(ctx, calldata)
}

// SendRawTransaction broadcasts a signed raw transaction to the network.
func (c *Client) SendRawTransaction(ctx context.Context, rawTxHex string) (common.Hash, error) {
	if !c.breaker.CanExecute() {
		return common.Hash{}, ErrCircuitOpen
	}
	var txHashStr string
	err := c.callRPC(ctx, "eth_sendRawTransaction", &txHashStr, rawTxHex)
	if err != nil {
		c.breaker.RecordFailure()
		return common.Hash{}, err
	}
	c.breaker.RecordSuccess()
	return common.HexToHash(txHashStr), nil
}

// TxReceipt is the minimal subset of a Metanode transaction receipt this package needs: standard
// Ethereum "status" plus the ABI-packed return/revert data this project's RPC always populates
// for BOTH successful writes (e.g. batchOutboundCommit's (commitRoot, messageCount)) and reverted
// ones (a standard Error(string) encoding) -- confirmed by reading a real receipt live, not
// assumed from the Ethereum JSON-RPC spec (which doesn't define this field at all).
type TxReceipt struct {
	Status hexutil.Uint64 `json:"status"`
	Return hexutil.Bytes  `json:"return"`
}

// TransactionReceipt polls eth_getTransactionReceipt once. Returns (nil, nil) while the
// transaction is still pending (a JSON-RPC null result is not itself an error) -- callers poll
// this in a loop with their own backoff, matching every other poll loop in this package.
func (c *Client) TransactionReceipt(ctx context.Context, txHash common.Hash) (*TxReceipt, error) {
	if !c.breaker.CanExecute() {
		return nil, ErrCircuitOpen
	}
	var receipt *TxReceipt
	err := c.callRPC(ctx, "eth_getTransactionReceipt", &receipt, txHash.Hex())
	if err != nil {
		c.breaker.RecordFailure()
		return nil, err
	}
	c.breaker.RecordSuccess()
	return receipt, nil
}

// callRPC tries each configured URL in order, returning the first success. It does NOT itself
// touch the circuit breaker — callers record success/failure once for the whole attempt (trying
// multiple URLs for one logical call is not itself a failure signal).
func (c *Client) callRPC(ctx context.Context, method string, result interface{}, args ...interface{}) error {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var lastErr error
	for _, url := range c.urls {
		client, err := rpc.DialContext(callCtx, url)
		if err != nil {
			lastErr = fmt.Errorf("dial %s: %w", url, err)
			continue
		}
		err = client.CallContext(callCtx, result, method, args...)
		client.Close()
		if err != nil {
			lastErr = fmt.Errorf("%s @ %s: %w", method, url, err)
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no root anchor RPC URLs configured")
	}
	return lastErr
}

// State exposes the circuit breaker's current state for observability/metrics.
func (c *Client) State() network.CircuitBreakerState {
	return c.breaker.GetState()
}
