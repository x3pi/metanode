package tx_processor

import (
	"context"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

// RollupSystemEventDispatcher applies a rollup system event (JSON-encoded in the tx's call
// data) locally — e.g. crediting a Float account on EventCreditObserved. Implemented by an
// adapter in cmd/simple_chain/app.go that unmarshals the payload and calls
// rollup.CrossNodeHandler.HandleSystemEvent; kept as a plain []byte-in interface here (rather
// than importing pkg/rollup's Event type) purely to mirror ParentChainGatewayHandler's existing
// decoupling pattern, not because of any real import-cycle risk.
type RollupSystemEventDispatcher interface {
	HandleSystemEvent(store rollup.Store, stateDB AccountStateAccessor, data []byte) error
}

// AccountRegistryApplier applies account registration events to stateDB.
type AccountRegistryApplier interface {
	Apply(stateDB rollup.AccountStateRegistryDB, data []byte) error
}

// AttestedRegistryApplier is implemented by registry handlers that enforce the f+1 validator co-attestation rule.
// When the configured handler implements it, HandleTransaction uses it with the committee and the pending-attestation
// store derived from the chain state the tx executes against (so all replicas decide identically).
type AttestedRegistryApplier interface {
	ApplyAttested(stateDB rollup.AccountStateRegistryDB, store rollup.AttestationStore, committee rollup.CommitteeProvider, data []byte) error
}

// RollupSystemHandler handles transactions sent to rollup.RollupSystemAddress.
type RollupSystemHandler struct {
	dispatcher      RollupSystemEventDispatcher
	registryHandler AccountRegistryApplier
	// committeeFor (optional, tests) overrides how the validator committee is read for a chain state.
	committeeFor func(*blockchain.ChainState) rollup.CommitteeProvider
}

func (h *RollupSystemHandler) committee(cs *blockchain.ChainState) rollup.CommitteeProvider {
	if h.committeeFor != nil {
		return h.committeeFor(cs)
	}
	return newLiveCommitteeProvider(cs)
}

var (
	rollupSystemHandlerInstance *RollupSystemHandler
	onceRollupSystemHandler     sync.Once
)

// InitRollupSystemHandler initializes the singleton with a dispatcher and an optional registry handler.
func InitRollupSystemHandler(dispatcher RollupSystemEventDispatcher, registryHandlers ...AccountRegistryApplier) {
	onceRollupSystemHandler.Do(func() {
		var regHandler AccountRegistryApplier
		if len(registryHandlers) > 0 {
			regHandler = registryHandlers[0]
		}
		rollupSystemHandlerInstance = &RollupSystemHandler{
			dispatcher:      dispatcher,
			registryHandler: regHandler,
		}
	})
	if len(registryHandlers) > 0 && rollupSystemHandlerInstance != nil && rollupSystemHandlerInstance.registryHandler == nil {
		rollupSystemHandlerInstance.registryHandler = registryHandlers[0]
	}
}

// SetRegistryHandler sets or updates the registry handler on the singleton.
func (h *RollupSystemHandler) SetRegistryHandler(registryHandler AccountRegistryApplier) {
	h.registryHandler = registryHandler
}

// GetRollupSystemHandler returns the singleton handler.
func GetRollupSystemHandler() *RollupSystemHandler {
	return rollupSystemHandlerInstance
}

// HandleTransaction applies a rollup system event natively in Go, as a barrier transaction
// executed sequentially inside Block-STM (mirrors ParentChainGatewayHandler.HandleTransaction).
// Before this handler existed, transactions to RollupSystemAddress were never special-cased in
// TrueBlockSTM's barrier dispatch, so they silently executed as plain zero-value transfers —
// app.go's SetRollupInterceptor callback (wired to TxValidatorPool, a separate/legacy admission
// path, not the live execution engine) never actually fired, and EventCreditObserved never
// credited the destination's Float balance (found live: ReceiveWorker correctly proposed the
// event to the tx pool and it landed in a real block, but the balance stayed 0 forever).
// isAuthorizedRollupSystemSender reports whether tx may carry a rollup system event: its sender must be a BLS-native
// node identity (address derived from its own registered BLS public key, see isNodeBLSIdentity).
func isAuthorizedRollupSystemSender(chainState *blockchain.ChainState, tx types.Transaction) bool {
	as, err := chainState.GetAccountStateDB().AccountState(tx.FromAddress())
	if err != nil || as == nil {
		return false
	}
	return isNodeBLSIdentity(tx, as)
}

func (h *RollupSystemHandler) HandleTransaction(
	ctx context.Context,
	chainState *blockchain.ChainState,
	tx types.Transaction,
	toAddress common.Address,
	isReadOnly bool,
	blockTime uint64,
) (types.Receipt, types.ExecuteSCResult, error) {

	// AUTHORIZATION (applies to BOTH the account-registration branch and the rollup-event dispatcher): a system event is
	// applied from its payload alone. The receiver path CreditObserved -> RPCSubmitted -> ClaimedConfirmed credits
	// Target/Value straight from the event, and an account_registered payload only needs the (public) cluster key, so
	// without this check ANY account with gas money could mint balance or mark itself parent-registered (bypassing the
	// account gate). Only a BLS-native node identity may submit one. Pure function of the sender's state before this tx,
	// so every replica reaches the same verdict.
	if !isAuthorizedRollupSystemSender(chainState, tx) {
		logger.Error("❌ RollupSystemHandler: rejected system event from unauthorized sender %s (tx %s)", tx.FromAddress().Hex(), tx.Hash().Hex())
		denied := newLiveAccountStateAccessor(chainState)
		denied.SetNonce(tx.FromAddress(), denied.GetNonce(tx.FromAddress())+1)
		return h.errorReceipt(tx, transaction.UnauthorizedSystemSender.Description), nil, nil
	}

	gasUsed := uint64(mt_common.TRANSFER_GAS_COST)
	gasFee := new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), tx.EffectiveGasPrice())

	// Unlike ParentChainGatewayHandler's tx (which arrives via the ETH-wrapped
	// SendRawEthTransaction -> buildMetaTxFromEthTx path and so genuinely needs
	// CallData().Input() to unwrap the CallData envelope), this tx is built natively by
	// app.go's eventProposer via transaction.NewTransaction(..., eventData, ...) — eventData
	// is never wrapped in a CallData envelope in the first place, so tx.Data() IS the raw
	// payload here. Using CallData().Input() (copy-pasted from the other handler without
	// checking this) corrupted it into empty/truncated bytes (confirmed live: "unexpected end
	// of JSON input").
	data := tx.Data()
	stateDB := newLiveAccountStateAccessor(chainState)

	if gasFee.Sign() > 0 {
		curBal := stateDB.GetBalance(tx.FromAddress())
		if curBal == nil || curBal.Cmp(gasFee) < 0 {
			logger.Error("❌ RollupSystemHandler: insufficient balance for gas")
			stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
			return h.errorReceipt(tx, "insufficient balance for gas"), nil, nil
		}
	}

	if rollup.IsAccountRegistrationPayload(data) {
		if h.registryHandler == nil {
			logger.Error("❌ RollupSystemHandler: RegistryHandler not initialized")
			stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
			return h.errorReceipt(tx, "registryHandler not initialized"), nil, nil
		}
		var applyErr error
		if att, ok := h.registryHandler.(AttestedRegistryApplier); ok {
			applyErr = att.ApplyAttested(stateDB, rollup.NewDBAttestationStore(&liveSmartContractDB{db: chainState.GetSmartContractDB()}),
				newLiveCommitteeProvider(chainState), data)
		} else {
			applyErr = h.registryHandler.Apply(stateDB, data)
		}
		if err := applyErr; err != nil {
			logger.Error("❌ RollupSystemHandler: RegistryHandler.Apply failed: %v", err)
			stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
			return h.errorReceipt(tx, err.Error()), nil, nil
		}
	} else if rollup.IsRollupSystemAttestedPayload(data) {
		if h.dispatcher == nil {
			logger.Error("❌ RollupSystemHandler: Dispatcher not initialized")
			stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
			return h.errorReceipt(tx, "dispatcher not initialized"), nil, nil
		}
		var chainID uint64
		if chainState.GetConfig() != nil && chainState.GetConfig().ChainId != nil {
			chainID = chainState.GetConfig().ChainId.Uint64()
		}
		if chainID == 0 {
			chainID = parentchain.ParentChainID
		}
		err := rollup.ApplyAttestedSystemEvent(
			newLiveRollupStore(chainState),
			stateDB,
			&liveSmartContractDB{db: chainState.GetSmartContractDB()},
			newLiveCommitteeProvider(chainState),
			chainID,
			data,
			func(st rollup.Store, sdb rollup.AccountStateDB, inner []byte) error {
				return h.dispatcher.HandleSystemEvent(st, stateDB, inner)
			},
		)
		if err != nil {
			logger.Error("❌ RollupSystemHandler: ApplyAttestedSystemEvent failed: %v", err)
			stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
			return h.errorReceipt(tx, err.Error()), nil, nil
		}
	} else {
		if h.dispatcher == nil {
			logger.Error("❌ RollupSystemHandler: Dispatcher not initialized")
			stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
			return h.errorReceipt(tx, "dispatcher not initialized"), nil, nil
		}

		// Fail closed: if the committee cannot be read, an unattested event must NOT be applied (and every replica
		// must reach the same verdict). Only a committee of at most one validator needs no co-attestation.
		keys, committeeErr := h.committee(chainState).GetActiveCommitteeBLSKeys()
		if committeeErr != nil || len(keys) > 1 {
			logger.Error("❌ RollupSystemHandler: unattested system event rejected in multi-validator committee")
			stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
			return h.errorReceipt(tx, "unattested system event rejected: co-attestation required"), nil, nil
		}

		if err := h.dispatcher.HandleSystemEvent(newLiveRollupStore(chainState), stateDB, data); err != nil {
			logger.Error("❌ RollupSystemHandler: HandleSystemEvent failed: %v", err)
			// The tx was executed (and failed), so it must still consume its nonce, as on any
			// Ethereum-style chain. Without this, a rejected event (e.g. a stale-state race) left
			// the sender's on-chain nonce unchanged: the next system tx then needed the same
			// nonce, and eventProposer's in-flight bookkeeping for the failed nonce never cleared,
			// wedging all later system txs (seen live under 10 concurrent transfers: 8/10 never
			// credited). HandleSystemEvent's state-machine check runs before any write, so a
			// failed call leaves no partial state to roll back here.
			stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
			return h.errorReceipt(tx, err.Error()), nil, nil
		}
	}

	if gasFee.Sign() > 0 {
		stateDB.SubBalance(tx.FromAddress(), gasFee)
	}

	// runBarrierTx only VALIDATES tx.FromAddress()'s nonce against fromAccount.Nonce() before
	// dispatch — it never advances it afterward (unlike the parallel MVCC path, which calls
	// PlusOneNonce for every tx). ParentChainGatewayHandler gets this "for free" because its
	// dispatcher call happens to pass tx.FromAddress() as HandleTransfer's own transfer-sender
	// too, so HandleTransfer's SetNonce incidentally advances the barrier tx's sender. Here,
	// HandleSystemEvent's SubBalance/credit actions apply to the EVENT's Target address, never
	// to this system tx's own sender (app.go's eventProposer, e.g. exec2's own operating
	// address) — so without this, that address's nonce never moves past 0, and every
	// subsequent system tx after the very first one fails runBarrierTx's nonce check silently
	// (RollupSystemHandler.HandleTransaction is never even called again) and piles up as a
	// permanent "future" tx in TxValidatorPool. Confirmed live: exec2's own account nonce
	// stayed 0 forever while ReceiveWorker retried the same credit with climbing nonces (11,
	// 12, 13, ...), none of which ever got past the pool's own admission check.
	stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)

	rcp := receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), tx.ToAddress(), tx.Amount(),
		pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
		tx.EffectiveGasPrice().Uint64(), gasUsed, nil, 0, common.Hash{}, 0,
	)

	return rcp, nil, nil
}

func (h *RollupSystemHandler) errorReceipt(tx types.Transaction, errMsg string) types.Receipt {
	return receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), tx.ToAddress(), tx.Amount(),
		pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(errMsg), pb.EXCEPTION_NONE,
		tx.EffectiveGasPrice().Uint64(), 0, nil, 0, common.Hash{}, 0,
	)
}
