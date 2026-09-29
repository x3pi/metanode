package tx_processor

import (
	"context"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/account_state_db"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract_db"
	"github.com/meta-node-blockchain/meta-node/types"
)

// AccountStateAccessor mirrors rollup.AccountStateDB's shape structurally (this package
// deliberately does not import pkg/rollup for this interface, matching the existing
// decoupling pattern below). liveAccountStateAccessor implements it by wrapping the CURRENT
// barrier-tx call's own chainState.GetAccountStateDB() result, freshly, per call — never a
// reference captured once at App-construction time. See rollup.CrossNodeHandler.HandleTransfer's
// doc comment for why that staleness class of bug is dangerous here specifically: the account
// nonce/balance this touches is also what TxValidatorPool's own future-tx nonce-gap check reads,
// so a write landing in the wrong chainState instance diverges permanently and silently.
type AccountStateAccessor interface {
	GetBalance(address common.Address) *big.Int
	AddBalance(address common.Address, amount *big.Int) error
	SubBalance(address common.Address, amount *big.Int)
	GetNonce(address common.Address) uint64
	SetNonce(address common.Address, nonce uint64)
}

type liveAccountStateAccessor struct {
	db *account_state_db.AccountStateDB
}

// newLiveAccountStateAccessor wraps chainState.GetAccountStateDB() as called by the barrier-tx
// dispatch for THIS specific transaction — callers must not cache or reuse the result across
// calls.
func newLiveAccountStateAccessor(chainState *blockchain.ChainState) AccountStateAccessor {
	return &liveAccountStateAccessor{db: chainState.GetAccountStateDB()}
}

func (a *liveAccountStateAccessor) GetBalance(address common.Address) *big.Int {
	state, err := a.db.AccountState(address)
	if err != nil || state == nil {
		return big.NewInt(0)
	}
	return state.Balance()
}

func (a *liveAccountStateAccessor) AddBalance(address common.Address, amount *big.Int) error {
	return a.db.AddBalance(address, amount)
}

func (a *liveAccountStateAccessor) SubBalance(address common.Address, amount *big.Int) {
	_ = a.db.SubBalance(address, amount)
}

func (a *liveAccountStateAccessor) GetNonce(address common.Address) uint64 {
	state, err := a.db.AccountState(address)
	if err != nil || state == nil {
		return 0
	}
	return state.Nonce()
}

func (a *liveAccountStateAccessor) SetNonce(address common.Address, nonce uint64) {
	_ = a.db.SetNonce(address, nonce)
}

// liveSmartContractDB adapts *smart_contract_db.SmartContractDB to rollup.SmartContractDB,
// wrapping the barrier-tx call's own chainState.GetSmartContractDB() result. Has the exact same
// per-call freshness requirement as liveAccountStateAccessor above, and empirically confirmed to
// matter here specifically: rollup.CrossNodeHandler.HandleTransfer/HandleSystemEvent's
// store.Put(record) call went through app.chainState (the global/base instance, captured once)
// before this fix, and the resulting record was never visible to ScanNonTerminal() afterward —
// SendWorker found 0 non-terminal records on every tick, forever, confirmed via direct
// instrumentation, even though HandleTransfer itself reported success every time.
type liveSmartContractDB struct {
	db *smart_contract_db.SmartContractDB
}

// newLiveRollupStore wraps chainState.GetSmartContractDB() as called by the barrier-tx dispatch
// for THIS specific transaction — callers must not cache or reuse the result across calls.
func newLiveRollupStore(chainState *blockchain.ChainState) rollup.Store {
	return rollup.NewDBStore(&liveSmartContractDB{db: chainState.GetSmartContractDB()})
}

func (s *liveSmartContractDB) StorageValue(address common.Address, key common.Hash) ([]byte, bool) {
	val, found := s.db.StorageValue(address, key[:])
	if found && len(val) == 32 {
		emptyHash := common.Hash{}
		isEmpty := true
		for i := 0; i < 32; i++ {
			if val[i] != emptyHash[i] {
				isEmpty = false
				break
			}
		}
		if isEmpty {
			return nil, false
		}
	}
	return val, found
}

func (s *liveSmartContractDB) SetStorageValue(address common.Address, key common.Hash, value []byte) {
	_ = s.db.SetStorageValue(address, key[:], value)
}

// CrossChainTransferDispatcher defines the interface for triggering cross-node transfers
// via the Parent Chain clearing house. It is implemented by rollup.CrossNodeHandler.
type CrossChainTransferDispatcher interface {
	HandleTransfer(
		store rollup.Store,
		stateDB AccountStateAccessor,
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

	stateDB := newLiveAccountStateAccessor(chainState)

	if h.dispatcher == nil {
		logger.Error("❌ ParentChainGatewayHandler: Dispatcher not initialized")
		stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
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
	if len(data) < 100 {
		logger.Error("❌ ParentChainGatewayHandler: invalid data length")
		stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
		return h.errorReceipt(tx, "invalid data length for parent chain transfer (expected >= 100 bytes for pubkey + payloadHash + targetAddr)"), nil, nil
	}

	destPubKey := mt_common.PubkeyFromBytes(data[:48])
	payloadHash := common.BytesToHash(data[48:80])
	targetAddr := common.BytesToAddress(data[80:100])
	if targetAddr == (common.Address{}) {
		logger.Error("❌ ParentChainGatewayHandler: target address cannot be zero")
		stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
		return h.errorReceipt(tx, "target address cannot be zero"), nil, nil
	}

	// Calculate and pre-validate gas fee and total balance before deducting anything
	gasUsed := uint64(mt_common.TRANSFER_GAS_COST) // Use standard transfer gas cost
	gasFee := new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), tx.EffectiveGasPrice())
	totalRequired := new(big.Int).Add(tx.Amount(), gasFee)

	curBal := stateDB.GetBalance(tx.FromAddress())
	if curBal == nil || curBal.Cmp(totalRequired) < 0 {
		logger.Error("❌ ParentChainGatewayHandler: insufficient balance for transfer and gas")
		stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
		return h.errorReceipt(tx, "insufficient balance for transfer and gas"), nil, nil
	}

	// Route the transfer to the actual CrossNodeHandler logic.
	// NOTE: The dispatcher itself deducts the balance using AccountStateDB and records the transfer event.
	msgID, err := h.dispatcher.HandleTransfer(newLiveRollupStore(chainState), stateDB, destPubKey, tx.FromAddress(), targetAddr, tx.Amount(), payloadHash)
	if err != nil {
		logger.Error("❌ ParentChainGatewayHandler: Dispatcher failed: %v", err)
		stateDB.SetNonce(tx.FromAddress(), stateDB.GetNonce(tx.FromAddress())+1)
		return h.errorReceipt(tx, err.Error()), nil, nil
	}

	if gasFee.Sign() > 0 {
		stateDB.SubBalance(tx.FromAddress(), gasFee)
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
