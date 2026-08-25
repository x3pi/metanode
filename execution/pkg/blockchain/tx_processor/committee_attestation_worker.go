package tx_processor

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain/rootanchor"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// epochSignal is what HandleAdvanceEpochRequest (execution/executor/unix_socket_handler_epoch_state.go)
// hands off to CommitteeAttestationWorker — non-blocking, since that handler runs synchronously
// on the Rust-FFI-blocking path (see Milestone B's own caution about that path).
type epochSignal struct {
	newEpoch      uint64
	boundaryBlock uint64
}

// CommitteeAttestationWorker implements Milestone C: a real multi-validator BLS quorum-cert
// production pipeline for CommitteeUpdate, triggered from local epoch transitions. It uses Root
// Anchor itself as the rendezvous point for individual signature shares (submitCommitteeAttestation
// / getCommitteeAttestationShares / committeeUpdate — see gateway_handler.go) instead of building
// new P2P infrastructure: every validator of this chain already talks to Root Anchor via
// rootanchor.Client (Milestone B), and Go has no cross-organization P2P channel of its own (see
// the Milestone C plan doc's exploration findings).
type CommitteeAttestationWorker struct {
	chainState             *blockchain.ChainState
	client                 *rootanchor.Client
	localChainID           uint64         // this chain's own ID (config.ChainId) — NOT read from chainState.GetConfig(), which production code never populates (see loadGatewayEngine's identical config.ConfigApp-based derivation for the same reason)
	localAddress           common.Address // this validator's own eth address (config.Address) — how genesis ties an account's PublicKeyBls() to a validator identity
	blsPrivateKeyHex       string         // same key block_signer uses (config.Databases.BLSPrivateKey)
	submitterPrivateKeyHex string         // secp256k1 key used to sign/submit transactions TO Root Anchor (config.CrossChain.RootAnchorSubmitterPrivateKeyHex)

	signalChan chan epochSignal

	// pollInterval/maxPollAttempts bound how long a single epoch transition's attestation
	// round waits for quorum before giving up (it will simply be retried, from scratch, at the
	// next epoch transition — see the plan doc's documented no-catch-up limitation).
	pollInterval    time.Duration
	maxPollAttempts int
}

// NewCommitteeAttestationWorker builds a worker. All identity/key parameters are threaded in
// explicitly (not read from a global config) for testability.
func NewCommitteeAttestationWorker(
	chainState *blockchain.ChainState,
	client *rootanchor.Client,
	localChainID uint64,
	localAddress common.Address,
	blsPrivateKeyHex string,
	submitterPrivateKeyHex string,
) *CommitteeAttestationWorker {
	return &CommitteeAttestationWorker{
		chainState:             chainState,
		client:                 client,
		localChainID:           localChainID,
		localAddress:           localAddress,
		blsPrivateKeyHex:       blsPrivateKeyHex,
		submitterPrivateKeyHex: submitterPrivateKeyHex,
		signalChan:             make(chan epochSignal, 8),
		pollInterval:           5 * time.Second,
		maxPollAttempts:        12, // ~1 minute total per attempt round
	}
}

// OnEpochAdvanced is the callback wired to executor.RequestHandler
// (execution/cmd/simple_chain/processor/block_processor_network.go). MUST NOT block — it is
// called synchronously at the end of HandleAdvanceEpochRequest, on the Rust-FFI-blocking path.
func (w *CommitteeAttestationWorker) OnEpochAdvanced(newEpoch, boundaryBlock uint64) {
	select {
	case w.signalChan <- epochSignal{newEpoch: newEpoch, boundaryBlock: boundaryBlock}:
	default:
		logger.Warn("⚠️ [COMMITTEE ATTESTATION] signal channel full, dropping epoch %d transition signal (will retry at next epoch)", newEpoch)
	}
}

// Run blocks, processing epoch signals, until ctx is cancelled. Meant to be launched as
// `go worker.Run(ctx)`, same pattern as GatewayRegistryMonitor.Run.
func (w *CommitteeAttestationWorker) Run(ctx context.Context) {
	logger.Info("✅ Committee Attestation Worker started")
	for {
		select {
		case <-ctx.Done():
			logger.Info("Committee Attestation Worker stopped")
			return
		case sig := <-w.signalChan:
			w.handleEpochTransition(ctx, sig)
		}
	}
}

func (w *CommitteeAttestationWorker) handleEpochTransition(ctx context.Context, sig epochSignal) {
	localChainID := w.localChainID

	oldRegistry, exists, err := w.client.GetChainRegistry(ctx, localChainID)
	if err != nil {
		logger.Warn("⚠️ [COMMITTEE ATTESTATION] could not fetch this chain's registry from Root Anchor, skipping epoch %d: %v", sig.newEpoch, err)
		return
	}
	if !exists {
		// Not yet registered with Root Anchor (onboarding is a separate governance flow) —
		// nothing to attest to.
		return
	}

	// Am I (this validator's own min-pk key) a member of the OLD committee Root Anchor
	// currently trusts for this chain? Only OLD members may attest to a new one.
	myPubkeyBls, err := w.myPublicKeyBls()
	if err != nil {
		logger.Warn("⚠️ [COMMITTEE ATTESTATION] could not read own PublicKeyBls, skipping epoch %d: %v", sig.newEpoch, err)
		return
	}
	if !committeeContains(oldRegistry.Committee, myPubkeyBls) {
		// Not (yet) a member of the committee Root Anchor knows about — nothing for me to sign.
		return
	}

	// Ensure my own PoP is registered BEFORE assembling the new committee below —
	// buildNewCommittee looks up each member's PoP via getRegisteredPop and skips anyone who
	// doesn't have one yet (including possibly this validator itself, on its very first epoch
	// transition). Cheap and idempotent — safe to call every epoch.
	if err := w.ensureOwnPopRegistered(ctx, myPubkeyBls); err != nil {
		logger.Warn("⚠️ [COMMITTEE ATTESTATION] could not register own PoP, skipping epoch %d: %v", sig.newEpoch, err)
		return
	}

	newCommittee, err := w.buildNewCommittee(ctx, sig.boundaryBlock)
	if err != nil {
		logger.Warn("⚠️ [COMMITTEE ATTESTATION] could not assemble new committee for epoch %d: %v", sig.newEpoch, err)
		return
	}
	if len(newCommittee) == 0 {
		logger.Warn("⚠️ [COMMITTEE ATTESTATION] no active validators found at boundary block %d, skipping epoch %d", sig.boundaryBlock, sig.newEpoch)
		return
	}

	stateRoot, err := w.stateRootAtBlock(sig.boundaryBlock)
	if err != nil {
		logger.Warn("⚠️ [COMMITTEE ATTESTATION] could not read state root at boundary block %d, skipping epoch %d: %v", sig.boundaryBlock, sig.newEpoch, err)
		return
	}

	accountTreeRoot, err := w.accountTreeRootAtBlock(sig.boundaryBlock)
	if err != nil {
		logger.Warn("⚠️ [COMMITTEE ATTESTATION] could not compute account tree root at boundary block %d, skipping epoch %d: %v", sig.boundaryBlock, sig.newEpoch, err)
		return
	}

	payloadHash := cross_chain.ComputeCommitteeUpdateDigest(localChainID, sig.newEpoch, newCommittee, stateRoot, accountTreeRoot)

	if err := w.submitMyShare(ctx, localChainID, oldRegistry.Epoch, payloadHash); err != nil {
		logger.Warn("⚠️ [COMMITTEE ATTESTATION] could not submit attestation share for epoch %d: %v", sig.newEpoch, err)
		// Continue anyway — another validator's share may still reach quorum, and we can still
		// try to observe+submit the final committeeUpdate below.
	}

	w.pollAndFinalize(ctx, localChainID, oldRegistry, newCommittee, sig.newEpoch, stateRoot, accountTreeRoot, payloadHash)
}

// myPublicKeyBls reads this validator's own durable min-pk BLS public key — set at genesis via
// alloc[].publicKeyBls, or post-genesis via setBlsPublicKey() — keyed by config.Address, exactly
// how the genesis ceremony already ties an eth account to its BLS identity (see
// execution/pkg/cross_chain/ceremony's genesis_alloc/genesis_validator split).
func (w *CommitteeAttestationWorker) myPublicKeyBls() ([]byte, error) {
	as, err := w.chainState.GetAccountStateDB().AccountState(w.localAddress)
	if err != nil {
		return nil, fmt.Errorf("read own account state: %w", err)
	}
	pk := as.PublicKeyBls()
	if len(pk) == 0 {
		return nil, fmt.Errorf("account %s has no PublicKeyBls set", w.localAddress.Hex())
	}
	return pk, nil
}

// buildNewCommittee assembles []cross_chain.ValidatorEntry for every currently-active
// (non-jailed, non-zero-stake) validator of THIS chain, using each one's durable min-pk key —
// already available via AccountStateDB, no proto/DB plumbing needed (Milestone C finding).
func (w *CommitteeAttestationWorker) buildNewCommittee(ctx context.Context, boundaryBlock uint64) ([]cross_chain.ValidatorEntry, error) {
	validators, err := w.chainState.GetStakeStateDB().GetAllValidators()
	if err != nil {
		return nil, fmt.Errorf("GetAllValidators: %w", err)
	}

	const precision = 1_000_000_000_000_000_000 // matches unix_socket_handler_validators.go's /1e18 normalization
	precisionBig := big.NewInt(precision)

	var out []cross_chain.ValidatorEntry
	for _, v := range validators {
		if v.IsJailed() {
			continue
		}
		total := v.TotalStakedAmount()
		if total == nil || total.Sign() <= 0 {
			continue
		}
		stakeNormalized := new(big.Int).Div(total, precisionBig)
		if stakeNormalized.Sign() <= 0 {
			stakeNormalized = big.NewInt(1)
		}

		as, err := w.chainState.GetAccountStateDB().AccountState(v.Address())
		if err != nil {
			logger.Warn("⚠️ [COMMITTEE ATTESTATION] skipping validator %s: could not read account state: %v", v.Address().Hex(), err)
			continue
		}
		pubkeyBls := as.PublicKeyBls()
		if len(pubkeyBls) == 0 {
			logger.Warn("⚠️ [COMMITTEE ATTESTATION] skipping validator %s: no PublicKeyBls set", v.Address().Hex())
			continue
		}

		pop, err := w.client.GetRegisteredPop(ctx, pubkeyBls)
		if err != nil || len(pop) == 0 {
			logger.Warn("⚠️ [COMMITTEE ATTESTATION] skipping validator %s: no PoP registered on Root Anchor yet", v.Address().Hex())
			continue
		}

		out = append(out, cross_chain.ValidatorEntry{
			PubkeyBLS:    pubkeyBls,
			Stake:        stakeNormalized.Uint64(),
			PopSignature: pop,
		})
	}
	return out, nil
}

// stateRootAtBlock reads the account state root as of blockNumber. Deliberately uses
// chainState's OWN current header rather than the process-global blockchain.BlockChain
// singleton's number->hash index: by the time HandleAdvanceEpochRequest's epoch-advanced
// callback fires, chainState's current header IS the boundary block (this chain just committed
// it as the last block of the ending epoch) — no separate global lookup needed, and this stays
// correct in any context where a full BlockChain singleton isn't running (e.g. tests). If the
// current header does NOT match the expected boundary block, fail closed rather than risk
// submitting the wrong state root in a CommitteeUpdate.
func (w *CommitteeAttestationWorker) stateRootAtBlock(blockNumber uint64) (common.Hash, error) {
	headerPtr := w.chainState.GetcurrentBlockHeader()
	if headerPtr == nil {
		return common.Hash{}, fmt.Errorf("chainState has no current block header")
	}
	header := *headerPtr
	if header.BlockNumber() != blockNumber {
		return common.Hash{}, fmt.Errorf("chainState's current header is at block %d, expected boundary block %d", header.BlockNumber(), blockNumber)
	}
	return header.AccountStatesRoot(), nil
}

// accountTreeRootAtBlock walks the chain's full committed account set via AccountStateDB.GetAll(),
// constructs deterministic AccountLeaf entries sorted by address bytes, and derives the binary
// Merkle tree root using cross_chain.BuildAccountSnapshot.
func (w *CommitteeAttestationWorker) accountTreeRootAtBlock(blockNumber uint64) (common.Hash, error) {
	asDB := w.chainState.GetAccountStateDB()
	if asDB == nil {
		return common.Hash{}, fmt.Errorf("chainState has no AccountStateDB")
	}
	allAccounts, err := asDB.GetAll()
	if err != nil {
		return common.Hash{}, fmt.Errorf("GetAll accounts: %w", err)
	}
	if len(allAccounts) == 0 {
		return common.Hash{}, nil
	}
	leaves := make([]cross_chain.AccountLeaf, 0, len(allAccounts))
	for addr, as := range allAccounts {
		bal := big.NewInt(0)
		if as != nil && as.Balance() != nil {
			bal = as.Balance()
		}
		leaves = append(leaves, cross_chain.AccountLeaf{
			Account: addr,
			Balance: bal,
		})
	}
	root, _, err := cross_chain.BuildAccountSnapshot(leaves)
	if err != nil {
		return common.Hash{}, fmt.Errorf("BuildAccountSnapshot: %w", err)
	}
	return root, nil
}

func (w *CommitteeAttestationWorker) ensureOwnPopRegistered(ctx context.Context, myPubkeyBls []byte) error {
	existing, err := w.client.GetRegisteredPop(ctx, myPubkeyBls)
	if err == nil && len(existing) > 0 {
		return nil // already registered, idempotent no-op
	}

	privKey, pubKey, err := w.blsKeyPair()
	if err != nil {
		return err
	}
	popSig := cross_chain.PopSign(privKey, pubKey)

	h, err := GetGatewayHandler()
	if err != nil {
		return err
	}
	calldata, err := h.abi.Pack("registerCommitteePop", myPubkeyBls, popSig.Bytes())
	if err != nil {
		return fmt.Errorf("pack registerCommitteePop: %w", err)
	}
	_, err = w.signAndSubmit(ctx, calldata)
	return err
}

func (w *CommitteeAttestationWorker) submitMyShare(ctx context.Context, sourceChainID, oldEpoch uint64, payloadHash common.Hash) error {
	privKey, pubKey, err := w.blsKeyPair()
	if err != nil {
		return err
	}
	sig := bls.Sign(privKey, payloadHash.Bytes())

	h, err := GetGatewayHandler()
	if err != nil {
		return err
	}
	calldata, err := h.abi.Pack("submitCommitteeAttestation",
		new(big.Int).SetUint64(sourceChainID), oldEpoch, payloadHash, pubKey.Bytes(), sig.Bytes(),
	)
	if err != nil {
		return fmt.Errorf("pack submitCommitteeAttestation: %w", err)
	}
	_, err = w.signAndSubmit(ctx, calldata)
	return err
}

// pollAndFinalize polls getCommitteeAttestationShares until the OLD committee's stake threshold
// is reached (or maxPollAttempts is exhausted), then aggregates and submits the final
// committeeUpdate. Any validator may do this (not just a designated leader) — Root Anchor's own
// sequential-epoch check in cross_chain.ApplyCommitteeUpdate makes redundant submissions harmless.
func (w *CommitteeAttestationWorker) pollAndFinalize(
	ctx context.Context,
	sourceChainID uint64,
	oldRegistry *cross_chain.ChainRegistry,
	newCommittee []cross_chain.ValidatorEntry,
	newEpoch uint64,
	stateRoot common.Hash,
	accountTreeRoot common.Hash,
	payloadHash common.Hash,
) {
	var totalStake uint64
	for _, v := range oldRegistry.Committee {
		totalStake += v.Stake
	}
	threshold := (totalStake*2 + 2) / 3
	if oldRegistry.QuorumThreshold > 0 {
		threshold = (totalStake*oldRegistry.QuorumThreshold + 9999) / 10000
	}

	h, err := GetGatewayHandler()
	if err != nil {
		logger.Warn("⚠️ [COMMITTEE ATTESTATION] GetGatewayHandler: %v", err)
		return
	}

	for attempt := 0; attempt < w.maxPollAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return
		case <-time.After(w.pollInterval):
		}

		pubkeys, signatures, err := w.client.GetCommitteeAttestationShares(ctx, sourceChainID, oldRegistry.Epoch, payloadHash)
		if err != nil {
			logger.Warn("⚠️ [COMMITTEE ATTESTATION] poll attempt %d: %v", attempt, err)
			continue
		}

		var accumulated uint64
		for _, pk := range pubkeys {
			for _, v := range oldRegistry.Committee {
				if hex.EncodeToString(pk) == hex.EncodeToString(v.PubkeyBLS) {
					accumulated += v.Stake
					break
				}
			}
		}
		if accumulated < threshold || len(pubkeys) == 0 {
			continue
		}

		aggSignature := bls.CreateAggregateSign(signatures)

		newPubkeys := make([][]byte, len(newCommittee))
		newStakes := make([]uint64, len(newCommittee))
		newPops := make([][]byte, len(newCommittee))
		for i, v := range newCommittee {
			newPubkeys[i] = v.PubkeyBLS
			newStakes[i] = v.Stake
			newPops[i] = v.PopSignature
		}

		calldata, err := h.abi.Pack("committeeUpdate",
			new(big.Int).SetUint64(sourceChainID), newEpoch,
			newPubkeys, newStakes, newPops,
			oldRegistry.QuorumThreshold, stateRoot, accountTreeRoot, payloadHash,
			pubkeys, aggSignature,
		)
		if err != nil {
			logger.Warn("⚠️ [COMMITTEE ATTESTATION] pack committeeUpdate: %v", err)
			return
		}
		txHash, err := w.signAndSubmit(ctx, calldata)
		if err != nil {
			// Expected/harmless if another validator's committeeUpdate already landed first —
			// see cross_chain.ApplyCommitteeUpdate's sequential-epoch check.
			logger.Info("committeeUpdate submission for epoch %d did not land (may already be applied by another validator): %v", newEpoch, err)
			return
		}
		logger.Info("✅ [COMMITTEE ATTESTATION] submitted committeeUpdate for chain %d epoch %d, tx=%s", sourceChainID, newEpoch, txHash.Hex())
		return
	}
	logger.Warn("⚠️ [COMMITTEE ATTESTATION] gave up waiting for quorum on chain %d epoch %d after %d attempts (will retry at next epoch transition)",
		sourceChainID, newEpoch, w.maxPollAttempts)
}

func (w *CommitteeAttestationWorker) blsKeyPair() (mt_common.PrivateKey, mt_common.PublicKey, error) {
	privKey, pubKey, _ := bls.GenerateKeyPairFromSecretKey(w.blsPrivateKeyHex)
	if len(privKey.Bytes()) == 0 {
		return privKey, pubKey, fmt.Errorf("invalid BLS private key configured")
	}
	return privKey, pubKey, nil
}

// signAndSubmit builds, signs (secp256k1, EIP-155), and submits a transaction addressed to
// GATEWAY_CONTRACT_ADDRESS on Root Anchor — mirrors execution/cmd/tool/register_validator's
// sendValidatorTransaction pattern.
func (w *CommitteeAttestationWorker) signAndSubmit(ctx context.Context, calldata []byte) (common.Hash, error) {
	privateKey, err := crypto.HexToECDSA(w.submitterPrivateKeyHex)
	if err != nil {
		return common.Hash{}, fmt.Errorf("invalid RootAnchorSubmitterPrivateKeyHex: %w", err)
	}
	fromAddress := crypto.PubkeyToAddress(*privateKey.Public().(*ecdsa.PublicKey))

	nonce, err := w.client.GetTransactionCount(ctx, fromAddress)
	if err != nil {
		return common.Hash{}, fmt.Errorf("fetch nonce: %w", err)
	}
	chainID, err := w.client.ChainID(ctx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("fetch chain id: %w", err)
	}

	const gasLimit = uint64(2_000_000) // higher than register_validator's 500k: committeeUpdate carries a full committee array
	gasPrice := big.NewInt(20_000_000_000)

	tx := ethtypes.NewTransaction(nonce, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), gasLimit, gasPrice, calldata)
	signedTx, err := ethtypes.SignTx(tx, ethtypes.NewEIP155Signer(chainID), privateKey)
	if err != nil {
		return common.Hash{}, fmt.Errorf("sign transaction: %w", err)
	}
	rawTxBytes, err := signedTx.MarshalBinary()
	if err != nil {
		return common.Hash{}, fmt.Errorf("marshal signed transaction: %w", err)
	}
	return w.client.SubmitTransaction(ctx, rawTxBytes)
}

func committeeContains(committee []cross_chain.ValidatorEntry, pubkeyBls []byte) bool {
	for _, v := range committee {
		if hex.EncodeToString(v.PubkeyBLS) == hex.EncodeToString(pubkeyBls) {
			return true
		}
	}
	return false
}
