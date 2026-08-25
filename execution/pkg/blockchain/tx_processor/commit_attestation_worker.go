package tx_processor

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain/rootanchor"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// commitSignal represents a finalized commit root event requiring multi-validator BLS attestation.
type commitSignal struct {
	sourceChainID uint64
	epoch         uint64
	commitRoot    common.Hash
}

// CommitAttestationWorker implements Milestone F: a real multi-validator BLS quorum-cert
// production pipeline for cross-chain commit roots (attestCommit). It uses Root Anchor as the
// bulletin board for collecting individual signature shares (submitCommitAttestation /
// getCommitAttestationShares) and aggregates them into a real QuorumCert with a deterministic
// SignerBitmap once the committee quorum threshold is reached.
type CommitAttestationWorker struct {
	chainState             *blockchain.ChainState
	client                 *rootanchor.Client
	localChainID           uint64         // this chain's own ID
	localAddress           common.Address // this validator's own eth address
	blsPrivateKeyHex       string         // BLS private key for signing commit root
	submitterPrivateKeyHex string         // secp256k1 key for Root Anchor transactions

	signalChan chan commitSignal

	pollInterval    time.Duration
	maxPollAttempts int
}

// NewCommitAttestationWorker builds a CommitAttestationWorker. All identity and key parameters
// are threaded in explicitly for testability.
func NewCommitAttestationWorker(
	chainState *blockchain.ChainState,
	client *rootanchor.Client,
	localChainID uint64,
	localAddress common.Address,
	blsPrivateKeyHex string,
	submitterPrivateKeyHex string,
) *CommitAttestationWorker {
	return &CommitAttestationWorker{
		chainState:             chainState,
		client:                 client,
		localChainID:           localChainID,
		localAddress:           localAddress,
		blsPrivateKeyHex:       blsPrivateKeyHex,
		submitterPrivateKeyHex: submitterPrivateKeyHex,
		signalChan:             make(chan commitSignal, 32),
		pollInterval:           2 * time.Second,
		maxPollAttempts:        15, // ~30 seconds total per attempt round
	}
}

// SetPollConfig overrides the default polling interval and max attempts (useful for fast unit tests).
func (w *CommitAttestationWorker) SetPollConfig(interval time.Duration, maxAttempts int) {
	w.pollInterval = interval
	w.maxPollAttempts = maxAttempts
}

// OnCommitFinalized enqueues a finalized commit root for BLS signing and submission.
// Non-blocking.
func (w *CommitAttestationWorker) OnCommitFinalized(sourceChainID, epoch uint64, commitRoot common.Hash) {
	select {
	case w.signalChan <- commitSignal{sourceChainID: sourceChainID, epoch: epoch, commitRoot: commitRoot}:
	default:
		logger.Warn("⚠️ [COMMIT ATTESTATION] signal channel full, dropping commit root %s signal", commitRoot.Hex())
	}
}

// Run blocks, processing commit signals, until ctx is cancelled.
func (w *CommitAttestationWorker) Run(ctx context.Context) {
	logger.Info("✅ Commit Attestation Worker started")
	for {
		select {
		case <-ctx.Done():
			logger.Info("Commit Attestation Worker stopped")
			return
		case sig := <-w.signalChan:
			w.handleCommit(ctx, sig)
		}
	}
}

func (w *CommitAttestationWorker) handleCommit(ctx context.Context, sig commitSignal) {
	registry, exists, err := w.client.GetChainRegistry(ctx, sig.sourceChainID)
	if err != nil {
		logger.Warn("⚠️ [COMMIT ATTESTATION] could not fetch chain %d registry from Root Anchor: %v", sig.sourceChainID, err)
		return
	}
	if !exists {
		logger.Warn("⚠️ [COMMIT ATTESTATION] chain %d is not registered on Root Anchor", sig.sourceChainID)
		return
	}

	myPubkeyBls, err := w.myPublicKeyBls()
	if err != nil {
		logger.Warn("⚠️ [COMMIT ATTESTATION] could not read own PublicKeyBls: %v", err)
		return
	}
	if !committeeContains(registry.Committee, myPubkeyBls) {
		// Not a member of the current committee — nothing for this node to sign
		return
	}

	if err := w.submitMyShare(ctx, sig.sourceChainID, sig.epoch, sig.commitRoot); err != nil {
		logger.Warn("⚠️ [COMMIT ATTESTATION] could not submit share for commit %s: %v", sig.commitRoot.Hex(), err)
	}

	_, _ = w.PollAndAggregate(ctx, sig.sourceChainID, sig.epoch, sig.commitRoot)
}

// SubmitMyShare signs the commit root with this validator's BLS key and submits the share to Root Anchor.
func (w *CommitAttestationWorker) SubmitMyShare(ctx context.Context, sourceChainID, epoch uint64, commitRoot common.Hash) error {
	return w.submitMyShare(ctx, sourceChainID, epoch, commitRoot)
}

func (w *CommitAttestationWorker) submitMyShare(ctx context.Context, sourceChainID, epoch uint64, commitRoot common.Hash) error {
	privKey, pubKey, err := w.blsKeyPair()
	if err != nil {
		return err
	}
	commitMsg := cross_chain.ComputeCommitRootAttestMessage(commitRoot)
	sig := bls.Sign(privKey, commitMsg)

	h, err := GetGatewayHandler()
	if err != nil {
		return err
	}
	calldata, err := h.abi.Pack("submitCommitAttestation",
		new(big.Int).SetUint64(sourceChainID), epoch, commitRoot, pubKey.Bytes(), sig.Bytes(),
	)
	if err != nil {
		return fmt.Errorf("pack submitCommitAttestation: %w", err)
	}
	_, err = w.signAndSubmit(ctx, calldata)
	return err
}

// PollAndAggregate polls Root Anchor for signature shares until the current committee's quorum
// threshold is met, then aggregates them into a complete QuorumCert.
func (w *CommitAttestationWorker) PollAndAggregate(
	ctx context.Context,
	sourceChainID, epoch uint64,
	commitRoot common.Hash,
) (*cross_chain.QuorumCert, error) {
	registry, exists, err := w.client.GetChainRegistry(ctx, sourceChainID)
	if err != nil {
		return nil, fmt.Errorf("fetch registry: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("chain %d not registered", sourceChainID)
	}

	var totalStake uint64
	for _, v := range registry.Committee {
		totalStake += v.Stake
	}
	if totalStake == 0 {
		return nil, cross_chain.ErrZeroTotalStake
	}
	threshold := (totalStake*2 + 2) / 3
	if registry.QuorumThreshold > 0 {
		threshold = (totalStake*registry.QuorumThreshold + 9999) / 10000
	}

	for attempt := 0; attempt < w.maxPollAttempts; attempt++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(w.pollInterval):
		}

		pubkeys, signatures, err := w.client.GetCommitAttestationShares(ctx, sourceChainID, epoch, commitRoot)
		if err != nil {
			logger.Warn("⚠️ [COMMIT ATTESTATION] poll attempt %d failed: %v", attempt, err)
			continue
		}

		var accumulated uint64
		var votingPubkeys [][]byte
		var votingSignatures [][]byte

		for i, pk := range pubkeys {
			for _, v := range registry.Committee {
				if bytes.Equal(pk, v.PubkeyBLS) {
					accumulated += v.Stake
					votingPubkeys = append(votingPubkeys, pk)
					votingSignatures = append(votingSignatures, signatures[i])
					break
				}
			}
		}

		if accumulated >= threshold && len(votingPubkeys) > 0 {
			aggSignature := bls.CreateAggregateSign(votingSignatures)
			bitmap := cross_chain.BuildSignerBitmap(registry.Committee, votingPubkeys)

			cert := &cross_chain.QuorumCert{
				Epoch:              epoch,
				AggregateSignature: aggSignature,
				SignerBitmap:       bitmap,
			}
			logger.Info("✅ [COMMIT ATTESTATION] successfully produced QuorumCert for chain %d epoch %d commit %s (accumulated stake %d >= threshold %d)",
				sourceChainID, epoch, commitRoot.Hex(), accumulated, threshold)
			return cert, nil
		}
	}

	return nil, fmt.Errorf("gave up waiting for quorum on chain %d commit %s after %d attempts",
		sourceChainID, commitRoot.Hex(), w.maxPollAttempts)
}

func (w *CommitAttestationWorker) myPublicKeyBls() ([]byte, error) {
	if w.chainState == nil {
		_, pubKey, err := w.blsKeyPair()
		if err != nil {
			return nil, err
		}
		return pubKey.Bytes(), nil
	}
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

func (w *CommitAttestationWorker) blsKeyPair() (mt_common.PrivateKey, mt_common.PublicKey, error) {
	privKey, pubKey, _ := bls.GenerateKeyPairFromSecretKey(w.blsPrivateKeyHex)
	if len(privKey.Bytes()) == 0 {
		return privKey, pubKey, fmt.Errorf("invalid BLS private key configured")
	}
	return privKey, pubKey, nil
}

func (w *CommitAttestationWorker) signAndSubmit(ctx context.Context, calldata []byte) (common.Hash, error) {
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

	const gasLimit = uint64(500_000)
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
