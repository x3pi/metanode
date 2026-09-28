package tx_processor

import (
	"context"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/types"
)

// RollupSystemEventDispatcher applies a rollup system event (JSON-encoded in the tx's call
// data) locally — e.g. crediting a Float account on EventCreditObserved. Implemented by an
// adapter in cmd/simple_chain/app.go that unmarshals the payload and calls
// rollup.CrossNodeHandler.HandleSystemEvent; kept as a plain []byte-in interface here (rather
// than importing pkg/rollup's Event type) purely to mirror ParentChainGatewayHandler's existing
// decoupling pattern, not because of any real import-cycle risk.
type RollupSystemEventDispatcher interface {
	HandleSystemEvent(data []byte) error
}

// RollupSystemHandler handles transactions sent to rollup.RollupSystemAddress.
type RollupSystemHandler struct {
	dispatcher RollupSystemEventDispatcher
}

var (
	rollupSystemHandlerInstance *RollupSystemHandler
	onceRollupSystemHandler     sync.Once
)

// InitRollupSystemHandler initializes the singleton with a dispatcher.
func InitRollupSystemHandler(dispatcher RollupSystemEventDispatcher) {
	onceRollupSystemHandler.Do(func() {
		rollupSystemHandlerInstance = &RollupSystemHandler{dispatcher: dispatcher}
	})
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
func (h *RollupSystemHandler) HandleTransaction(
	ctx context.Context,
	chainState *blockchain.ChainState,
	tx types.Transaction,
	toAddress common.Address,
	isReadOnly bool,
	blockTime uint64,
) (types.Receipt, types.ExecuteSCResult, error) {

	if h.dispatcher == nil {
		logger.Error("❌ RollupSystemHandler: Dispatcher not initialized")
		return h.errorReceipt(tx, "dispatcher not initialized"), nil, nil
	}

	// Unlike ParentChainGatewayHandler's tx (which arrives via the ETH-wrapped
	// SendRawEthTransaction -> buildMetaTxFromEthTx path and so genuinely needs
	// CallData().Input() to unwrap the CallData envelope), this tx is built natively by
	// app.go's eventProposer via transaction.NewTransaction(..., eventData, ...) — eventData
	// is never wrapped in a CallData envelope in the first place, so tx.Data() IS the raw
	// payload here. Using CallData().Input() (copy-pasted from the other handler without
	// checking this) corrupted it into empty/truncated bytes (confirmed live: "unexpected end
	// of JSON input").
	data := tx.Data()
	if err := h.dispatcher.HandleSystemEvent(data); err != nil {
		logger.Error("❌ RollupSystemHandler: HandleSystemEvent failed: %v", err)
		return h.errorReceipt(tx, err.Error()), nil, nil
	}

	rcp := receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), tx.ToAddress(), tx.Amount(),
		pb.RECEIPT_STATUS_RETURNED, nil, pb.EXCEPTION_NONE,
		tx.EffectiveGasPrice().Uint64(), uint64(mt_common.TRANSFER_GAS_COST), nil, 0, common.Hash{}, 0,
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
