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

// messageFailureSignal represents a locally-finalized MessageStatusFailed event requiring
// multi-validator BLS attestation before the source chain can ever refund the sender.
type messageFailureSignal struct {
	sourceChainID uint64
	destChainID   uint64
	messageID     common.Hash
	epoch         uint64
}

// MessageFailureAttestationWorker is the production pipeline this project's own security audit
// (note/cross_chain/security_audit_findings.md finding #1) found completely missing: mục 2.4
// point 2 of note/cross_chain_root_anchor_architecture.md requires a real failure-confirmation
// QuorumCert before a source chain may ever refund a sender, but no code anywhere ever produced
// one in production -- GatewayEngine.Refund() verified a cert whose only real-world source was a
// unit test signing directly with a validator's private key. This worker closes that gap,
// mirroring CommitAttestationWorker (Milestone F) exactly: it uses Root Anchor as the bulletin
// board for collecting individual signature shares (submitMessageFailureAttestation /
// getMessageFailureAttestationShares) once a validator's OWN local execution deterministically
// observed FinalizeFailedAfterExecutionRevert succeed for a message (never speculatively -- see
// MessageFailedCallback's wiring, the exact analogue of CommitFinalizedCallback).
type MessageFailureAttestationWorker struct {
	chainState             *blockchain.ChainState
	client                 *rootanchor.Client
	localAddress           common.Address
	blsPrivateKeyHex       string
	submitterPrivateKeyHex string

	signalChan chan messageFailureSignal

	pollInterval    time.Duration
	maxPollAttempts int
}

// NewMessageFailureAttestationWorker builds a MessageFailureAttestationWorker. All identity and
// key parameters are threaded in explicitly for testability, mirroring NewCommitAttestationWorker.
func NewMessageFailureAttestationWorker(
	chainState *blockchain.ChainState,
	client *rootanchor.Client,
	localAddress common.Address,
	blsPrivateKeyHex string,
	submitterPrivateKeyHex string,
) *MessageFailureAttestationWorker {
	return &MessageFailureAttestationWorker{
		chainState:             chainState,
		client:                 client,
		localAddress:           localAddress,
		blsPrivateKeyHex:       blsPrivateKeyHex,
		submitterPrivateKeyHex: submitterPrivateKeyHex,
		signalChan:             make(chan messageFailureSignal, 32),
		pollInterval:           2 * time.Second,
		maxPollAttempts:        15,
	}
}

// SetPollConfig overrides the default polling interval and max attempts (useful for fast unit tests).
func (w *MessageFailureAttestationWorker) SetPollConfig(interval time.Duration, maxAttempts int) {
	w.pollInterval = interval
	w.maxPollAttempts = maxAttempts
}

// OnMessageFailed enqueues a locally-finalized Failed message for BLS signing and submission.
// Non-blocking -- mirrors CommitAttestationWorker.OnCommitFinalized exactly.
func (w *MessageFailureAttestationWorker) OnMessageFailed(sourceChainID, destChainID uint64, messageID common.Hash, epoch uint64) {
	logger.Info("📢 [MESSAGE FAILURE ATTESTATION] OnMessageFailed received: message=%s, sourceChain=%d, destChain=%d, epoch=%d", messageID.Hex(), sourceChainID, destChainID, epoch)
	select {
	case w.signalChan <- messageFailureSignal{sourceChainID: sourceChainID, destChainID: destChainID, messageID: messageID, epoch: epoch}:
	default:
		logger.Warn("⚠️ [MESSAGE FAILURE ATTESTATION] signal channel full, dropping message %s signal", messageID.Hex())
	}
}

// Run blocks, processing failure signals, until ctx is cancelled.
func (w *MessageFailureAttestationWorker) Run(ctx context.Context) {
	logger.Info("✅ Message Failure Attestation Worker started")
	for {
		select {
		case <-ctx.Done():
			logger.Info("Message Failure Attestation Worker stopped")
			return
		case sig := <-w.signalChan:
			w.handleFailure(ctx, sig)
		}
	}
}

func (w *MessageFailureAttestationWorker) handleFailure(ctx context.Context, sig messageFailureSignal) {
	logger.Info("⚙️ [MESSAGE FAILURE ATTESTATION] handling message=%s, destChain=%d, epoch=%d", sig.messageID.Hex(), sig.destChainID, sig.epoch)
	registry, exists, err := w.client.GetChainRegistry(ctx, sig.destChainID)
	if err != nil {
		logger.Warn("⚠️ [MESSAGE FAILURE ATTESTATION] could not fetch chain %d registry from Root Anchor: %v", sig.destChainID, err)
		return
	}
	if !exists {
		logger.Warn("⚠️ [MESSAGE FAILURE ATTESTATION] chain %d is not registered on Root Anchor", sig.destChainID)
		return
	}

	myPubkeyBls, err := w.myPublicKeyBls()
	if err != nil {
		logger.Warn("⚠️ [MESSAGE FAILURE ATTESTATION] could not read own PublicKeyBls: %v", err)
		return
	}
	if !committeeContains(registry.Committee, myPubkeyBls) {
		logger.Warn("⚠️ [MESSAGE FAILURE ATTESTATION] validator %s (bls=%x) is not in committee for chain %d (committee len=%d)", w.localAddress.Hex(), myPubkeyBls, sig.destChainID, len(registry.Committee))
		return
	}

	txHash, err := w.submitMyShare(ctx, sig.destChainID, sig.messageID, sig.epoch)
	if err != nil {
		logger.Warn("⚠️ [MESSAGE FAILURE ATTESTATION] could not submit share for message %s: %v", sig.messageID.Hex(), err)
	} else {
		logger.Info("✅ [MESSAGE FAILURE ATTESTATION] successfully submitted share (tx=%s) for message %s to Root Anchor!", txHash.Hex(), sig.messageID.Hex())
	}
}

// SubmitMyShare signs the message-failure digest with this validator's BLS key and submits the
// share to Root Anchor.
func (w *MessageFailureAttestationWorker) SubmitMyShare(ctx context.Context, destChainID uint64, messageID common.Hash, epoch uint64) error {
	_, err := w.submitMyShare(ctx, destChainID, messageID, epoch)
	return err
}

func (w *MessageFailureAttestationWorker) submitMyShare(ctx context.Context, destChainID uint64, messageID common.Hash, epoch uint64) (common.Hash, error) {
	privKey, pubKey, err := w.blsKeyPair()
	if err != nil {
		return common.Hash{}, err
	}
	failureDigest := cross_chain.ComputeMessageFailureAttestMessage(messageID, destChainID)
	sig := bls.Sign(privKey, failureDigest)

	h, err := GetGatewayHandler()
	if err != nil {
		return common.Hash{}, err
	}
	calldata, err := h.abi.Pack("submitMessageFailureAttestation",
		new(big.Int).SetUint64(destChainID), messageID, epoch, pubKey.Bytes(), sig.Bytes(),
	)
	if err != nil {
		return common.Hash{}, fmt.Errorf("pack submitMessageFailureAttestation: %w", err)
	}
	return w.signAndSubmit(ctx, calldata)
}

// PollAndAggregate polls Root Anchor for signature shares until destChainID's committee quorum
// threshold is met, then aggregates them into a complete QuorumCert -- mirrors
// CommitAttestationWorker.PollAndAggregate exactly.
func (w *MessageFailureAttestationWorker) PollAndAggregate(
	ctx context.Context,
	destChainID uint64,
	messageID common.Hash,
	epoch uint64,
) (*cross_chain.QuorumCert, error) {
	registry, exists, err := w.client.GetChainRegistry(ctx, destChainID)
	if err != nil {
		return nil, fmt.Errorf("fetch registry: %w", err)
	}
	if !exists {
		return nil, fmt.Errorf("chain %d not registered", destChainID)
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

		pubkeys, signatures, err := w.client.GetMessageFailureAttestationShares(ctx, destChainID, messageID, epoch)
		if err != nil {
			logger.Warn("⚠️ [MESSAGE FAILURE ATTESTATION] poll attempt %d failed: %v", attempt, err)
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
			logger.Info("✅ [MESSAGE FAILURE ATTESTATION] successfully produced QuorumCert for chain %d message %s (accumulated stake %d >= threshold %d)",
				destChainID, messageID.Hex(), accumulated, threshold)
			return cert, nil
		}
	}

	return nil, fmt.Errorf("gave up waiting for quorum on chain %d message %s after %d attempts",
		destChainID, messageID.Hex(), w.maxPollAttempts)
}

func (w *MessageFailureAttestationWorker) myPublicKeyBls() ([]byte, error) {
	if w.blsPrivateKeyHex != "" {
		_, pubKey, err := w.blsKeyPair()
		if err == nil && len(pubKey.Bytes()) > 0 {
			return pubKey.Bytes(), nil
		}
	}
	if w.chainState != nil {
		as, err := w.chainState.GetAccountStateDB().AccountState(w.localAddress)
		if err == nil && as != nil && len(as.PublicKeyBls()) > 0 {
			return as.PublicKeyBls(), nil
		}
	}
	return nil, fmt.Errorf("account %s has no PublicKeyBls set and no valid BLS private key configured", w.localAddress.Hex())
}

func (w *MessageFailureAttestationWorker) blsKeyPair() (mt_common.PrivateKey, mt_common.PublicKey, error) {
	privKey, pubKey, _ := bls.GenerateKeyPairFromSecretKey(w.blsPrivateKeyHex)
	if len(privKey.Bytes()) == 0 {
		return privKey, pubKey, fmt.Errorf("invalid BLS private key configured")
	}
	return privKey, pubKey, nil
}

func (w *MessageFailureAttestationWorker) signAndSubmit(ctx context.Context, calldata []byte) (common.Hash, error) {
	privateKey, err := crypto.HexToECDSA(w.submitterPrivateKeyHex)
	if err != nil {
		return common.Hash{}, fmt.Errorf("invalid submitterPrivateKeyHex: %w", err)
	}
	fromAddress := crypto.PubkeyToAddress(*privateKey.Public().(*ecdsa.PublicKey))

	chainID, err := w.client.ChainID(ctx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("fetch chain id: %w", err)
	}

	const gasLimit = uint64(500_000)
	gasPrice := big.NewInt(20_000_000_000)

	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		select {
		case <-ctx.Done():
			return common.Hash{}, ctx.Err()
		default:
		}

		if attempt > 0 {
			jitterMs := 300 + (time.Now().UnixNano() % 400)
			time.Sleep(time.Duration(jitterMs*int64(attempt)) * time.Millisecond)
		}

		nonce, err := w.client.GetPendingTransactionCount(ctx, fromAddress)
		if err != nil {
			lastErr = fmt.Errorf("fetch pending nonce: %w", err)
			continue
		}

		tx := ethtypes.NewTransaction(nonce, mt_common.GATEWAY_CONTRACT_ADDRESS, big.NewInt(0), gasLimit, gasPrice, calldata)
		signedTx, err := ethtypes.SignTx(tx, ethtypes.NewEIP155Signer(chainID), privateKey)
		if err != nil {
			return common.Hash{}, fmt.Errorf("sign transaction: %w", err)
		}
		rawTxBytes, err := signedTx.MarshalBinary()
		if err != nil {
			return common.Hash{}, fmt.Errorf("marshal signed transaction: %w", err)
		}
		hash, err := w.client.SubmitTransaction(ctx, rawTxBytes)
		if err == nil {
			return hash, nil
		}
		lastErr = err
		logger.Warn("⚠️ [MESSAGE FAILURE ATTESTATION] submit share attempt %d failed: %v (will retry with fresh nonce)", attempt+1, err)
	}
	return common.Hash{}, lastErr
}
