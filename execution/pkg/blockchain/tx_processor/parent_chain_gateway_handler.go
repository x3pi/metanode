package tx_processor

import (
	"context"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/types"
)

// CrossChainTransferDispatcher defines the interface for triggering cross-node transfers
// via the Parent Chain clearing house. It is implemented by rollup.CrossNodeHandler.
type CrossChainTransferDispatcher interface {
	HandleTransfer(
		toKey mt_common.PublicKey,
		sender common.Address,
		target common.Address,
		value *big.Int,
		payloadHash common.Hash,
	) (common.Hash, error)
}

// ParentChainGatewayHandler handles transactions sent to PARENT_CHAIN_GATEWAY_CONTRACT_ADDRESS.
type ParentChainGatewayHandler struct {
	dispatcher CrossChainTransferDispatcher
}

var (
	parentChainGatewayHandlerInstance *ParentChainGatewayHandler
	onceParentChainGateway            sync.Once
)

// InitParentChainGatewayHandler initializes the singleton with a dispatcher.
func InitParentChainGatewayHandler(dispatcher CrossChainTransferDispatcher) {
	onceParentChainGateway.Do(func() {
		parentChainGatewayHandlerInstance = &ParentChainGatewayHandler{
			dispatcher: dispatcher,
		}
	})
}

// GetParentChainGatewayHandler returns the singleton handler.
func GetParentChainGatewayHandler() *ParentChainGatewayHandler {
	return parentChainGatewayHandlerInstance
}

// HandleTransaction processes a cross-node transfer natively in Go.
// This is a "barrier transaction" executed sequentially inside Block-STM.
func (h *ParentChainGatewayHandler) HandleTransaction(
	ctx context.Context,
	chainState *blockchain.ChainState,
	tx types.Transaction,
	toAddress common.Address,
	isReadOnly bool,
	blockTime uint64,
) (types.Receipt, types.ExecuteSCResult, error) {

	if h.dispatcher == nil {
		logger.Error("❌ ParentChainGatewayHandler: Dispatcher not initialized")
		return h.errorReceipt(tx, "dispatcher not initialized"), nil, nil
	}

	// tx.Data() returns the raw wrapped bytes (still carrying the CallData envelope's own
	// prefix) — every other handler in this package correctly unwraps via CallData().Input()
	// instead. Using tx.Data() here caused a reproducible 2-byte offset: the on-chain tx input
	// was byte-perfect (verified via eth_getTransactionByHash) but this handler read the target
	// address 2 bytes early, picking up payloadHash's own trailing 2 bytes as a prefix and
	// losing the target's last 2 bytes — SendWorker then failed to route to the corrupted
	// address, forever.
	data := tx.CallData().Input()
	if len(data) < 48 {
		logger.Error("❌ ParentChainGatewayHandler: invalid data length")
		return h.errorReceipt(tx, "invalid data length for parent chain transfer (expected >= 48 bytes pubkey)"), nil, nil
	}

	destPubKey := mt_common.PubkeyFromBytes(data[:48])
	var payloadHash common.Hash
	if len(data) >= 80 {
		payloadHash = common.BytesToHash(data[48:80])
	}
	var targetAddr common.Address
	if len(data) >= 100 {
		targetAddr = common.BytesToAddress(data[80:100])
	}

	// Route the transfer to the actual CrossNodeHandler logic.
	// NOTE: The dispatcher itself deducts the balance using AccountStateDB and records the transfer event.
	msgID, err := h.dispatcher.HandleTransfer(destPubKey, tx.FromAddress(), targetAddr, tx.Amount(), payloadHash)
	if err != nil {
		logger.Error("❌ ParentChainGatewayHandler: Dispatcher failed: %v", err)
		return h.errorReceipt(tx, err.Error()), nil, nil
	}

	// Calculate and charge gas fee (barrier TX must still pay gas for processing).
	// Because HandleTransfer deducts the transfer amount, we just deduct the gas fee separately.
	gasUsed := uint64(mt_common.TRANSFER_GAS_COST) // Use standard transfer gas cost
	gasFee := new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), tx.EffectiveGasPrice())

	baseAccountDB := chainState.GetAccountStateDB()
	if gasFee.Sign() > 0 {
		err = baseAccountDB.SubBalance(tx.FromAddress(), gasFee)
		if err != nil {
			logger.Error("❌ ParentChainGatewayHandler: insufficient balance for gas")
			return h.errorReceipt(tx, "insufficient balance for gas"), nil, nil
		}
	}

	// Note: HandleTransfer has already bumped the nonce inside the underlying stateDB.
	
	logger.Info("✅ ParentChainGatewayHandler: Transfer queued via barrier tx. MsgID: %s, To: %x", msgID.Hex(), destPubKey.Bytes()[:8])

	rcp := receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), tx.ToAddress(), tx.Amount(),
		pb.RECEIPT_STATUS_RETURNED, msgID.Bytes(), pb.EXCEPTION_NONE,
		tx.EffectiveGasPrice().Uint64(), gasUsed, nil, 0, common.Hash{}, 0,
	)

	return rcp, nil, nil
}

func (h *ParentChainGatewayHandler) errorReceipt(tx types.Transaction, errMsg string) types.Receipt {
	return receipt.NewReceipt(
		tx.Hash(), tx.FromAddress(), tx.ToAddress(), tx.Amount(),
		pb.RECEIPT_STATUS_TRANSACTION_ERROR, []byte(errMsg), pb.EXCEPTION_NONE,
		tx.EffectiveGasPrice().Uint64(), 0, nil, 0, common.Hash{}, 0,
	)
}
