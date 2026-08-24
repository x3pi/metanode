package tx_processor

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/types"
)

// GatewayHandler dispatches transactions sent to GATEWAY_CONTRACT_ADDRESS to the
// already-implemented, unit-tested business logic in pkg/cross_chain.GatewayEngine
// (note/cross_chain_root_anchor_architecture.md mục 11.3). It is the Go-native
// "barrier transaction" counterpart of ValidatorHandler — see true_block_stm.go's
// isBarrierTx/runBarrierTx, which routes GATEWAY_CONTRACT_ADDRESS txs here instead of
// through the parallel MVCC workers or the C++ MVM precompile dispatcher.
//
// STORAGE MODEL (Milestone A of the wiring plan, see plan doc for the fuller rationale):
// GatewayEngine's state (ChainRegistry, GlobalSupplyLedger, AttestedCommits, MessageStatus, ...)
// is round-tripped through chainState.GetSmartContractDB() as a single JSON blob at a fixed
// storage slot on GATEWAY_CONTRACT_ADDRESS — i.e. the SAME per-account storage trie every regular
// Solidity contract already uses (SetStorageValue/StorageValue, already integrated with the
// commit/state-root/persistence pipeline), NOT a bespoke trie database like StakeStateDB. This
// reuses proven infrastructure instead of a new, fork-safety-critical one, at the cost of
// deserializing the whole state on every Gateway transaction — acceptable while the registry/
// ledger stay small (tens to low hundreds of chains); revisit with per-key storage slots if that
// stops being true.
type GatewayHandler struct {
	abi abi.ABI
}

var (
	gatewayHandlerInstance *GatewayHandler
	onceGateway            sync.Once
)

// GetGatewayHandler returns the singleton GatewayHandler, parsing the ABI on first use.
func GetGatewayHandler() (*GatewayHandler, error) {
	var err error
	onceGateway.Do(func() {
		var parsedABI abi.ABI
		parsedABI, err = abi.JSON(strings.NewReader(abi_contract.GatewayABI))
		if err != nil {
			return
		}
		gatewayHandlerInstance = &GatewayHandler{abi: parsedABI}
	})
	if err != nil {
		return nil, err
	}
	return gatewayHandlerInstance, nil
}

// gatewayStateStorageKey is the single fixed storage slot (on GATEWAY_CONTRACT_ADDRESS) holding
// the JSON-serialized GatewayEngine state. Keccak256, matching every other storage-key derivation
// convention used across this codebase's contract storage (see smart_contract_db.go).
var gatewayStateStorageKey = crypto.Keccak256([]byte("gateway_engine_state_v1"))

// loadGatewayEngine deserializes GatewayEngine state from chainState, or returns a fresh,
// unconfigured engine (empty ChainRegistry, zero-supply ledger) if this is the first write ever
// made to GATEWAY_CONTRACT_ADDRESS on this chain.
func loadGatewayEngine(chainState *blockchain.ChainState) (*cross_chain.GatewayEngine, error) {
	localChainID := uint64(0)
	if config.ConfigApp != nil && config.ConfigApp.ChainId != nil {
		localChainID = config.ConfigApp.ChainId.Uint64()
	}

	scDB := chainState.GetSmartContractDB()
	data, ok := scDB.StorageValue(mt_common.GATEWAY_CONTRACT_ADDRESS, gatewayStateStorageKey)
	// StorageValue's `ok` is false ONLY on a genuine read failure (e.g. the storage trie for
	// GATEWAY_CONTRACT_ADDRESS can't be loaded from the given root — corrupt/missing data). That
	// is NOT the same as "no Gateway state written yet" (which StorageValue reports as ok=true
	// with a zero-filled sentinel value, smart_contract_db.go's StorageValue). Conflating the two
	// — as an earlier version of this function did — would make a genuine storage read failure
	// silently look like "fresh chain, start from empty state", letting a transaction proceed
	// against a blank GatewayEngine (wrong committee/ledger/registry) instead of failing loudly.
	// On different nodes hitting this differently (e.g. a transient disk issue on one validator),
	// that divergence is exactly the kind of thing that causes a fork.
	if !ok {
		return nil, fmt.Errorf("read GatewayEngine state from chainState: storage trie for %s unavailable (see logged cause)", mt_common.GATEWAY_CONTRACT_ADDRESS.Hex())
	}
	if len(data) == 0 || allZero(data) {
		emptyLedger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(0), map[uint64]*big.Int{})
		if err != nil {
			return nil, fmt.Errorf("bootstrap empty GlobalSupplyLedger: %w", err)
		}
		return cross_chain.NewGatewayEngine(localChainID, map[uint64]cross_chain.ChainRegistry{}, emptyLedger), nil
	}

	var engine cross_chain.GatewayEngine
	if err := json.Unmarshal(data, &engine); err != nil {
		return nil, fmt.Errorf("unmarshal GatewayEngine state: %w", err)
	}
	engine.LocalChainID = localChainID
	if engine.SupplyLedger == nil {
		emptyLedger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(0), map[uint64]*big.Int{})
		if err != nil {
			return nil, fmt.Errorf("bootstrap empty GlobalSupplyLedger: %w", err)
		}
		engine.SupplyLedger = emptyLedger
	}
	return &engine, nil
}

// allZero reports whether data is exactly common.Hash{}.Bytes() — SmartContractDB.StorageValue
// returns that as a sentinel "read, but empty" value (smart_contract_db.go:StorageValue).
func allZero(data []byte) bool {
	for _, b := range data {
		if b != 0 {
			return false
		}
	}
	return true
}

// saveGatewayEngine serializes engine and writes it back to chainState. Bootstraps
// GATEWAY_CONTRACT_ADDRESS as a contract-bearing account on first write — SmartContractDB.
// CommitAllStorage silently skips committing the storage of any address whose AccountState has no
// SmartContractState, so without this the very first write on a fresh chain would look like it
// succeeded (no error from SetStorageValue, which only touches the in-memory trie cache) but never
// actually land in the state root or survive a restart.
func saveGatewayEngine(chainState *blockchain.ChainState, engine *cross_chain.GatewayEngine) error {
	accountStateDB := chainState.GetAccountStateDB()
	as, err := accountStateDB.AccountState(mt_common.GATEWAY_CONTRACT_ADDRESS)
	if err != nil {
		return fmt.Errorf("load Gateway account state: %w", err)
	}
	if as.SmartContractState() == nil {
		as.SetSmartContractState(state.NewEmptySmartContractState())
		accountStateDB.SetState(as)
	}

	data, err := json.Marshal(engine)
	if err != nil {
		return fmt.Errorf("marshal GatewayEngine state: %w", err)
	}
	if err := chainState.GetSmartContractDB().SetStorageValue(mt_common.GATEWAY_CONTRACT_ADDRESS, gatewayStateStorageKey, data); err != nil {
		return fmt.Errorf("write GatewayEngine state: %w", err)
	}
	return nil
}

func (h *GatewayHandler) HandleTransaction(
	ctx context.Context, chainState *blockchain.ChainState, tx types.Transaction,
	toAddress common.Address, enableTrace bool, blockTime uint64,
) (types.Receipt, types.ExecuteSCResult, bool) {
	inputData := tx.CallData().Input()
	if len(inputData) < 4 {
		return createErrorReceipt(tx, toAddress, fmt.Errorf("invalid gateway calldata: too short")), nil, true
	}

	method, err := h.abi.MethodById(inputData[:4])
	if err != nil {
		return createErrorReceipt(tx, toAddress, fmt.Errorf("unknown gateway method selector: %w", err)), nil, true
	}

	switch method.Name {
	case "outbound", "attestCommit", "claimMessage", "refund":
		eventLogs, returnData, logicErr := h.handleWrite(chainState, tx, method, inputData[4:])
		if logicErr != nil {
			logger.Error("GatewayHandler.%s failed: %v", method.Name, logicErr)
			return HandleRevertedTransaction(ctx, chainState, tx, toAddress, toAddress, blockTime, enableTrace, logicErr.Error())
		}
		return HandleSuccessTransaction(ctx, chainState, tx, toAddress, toAddress, blockTime, enableTrace, eventLogs, returnData)
	default:
		returnData, errCall := h.handleView(chainState, method, inputData[4:])
		if errCall != nil {
			logger.Error("GatewayHandler.%s failed: %v", method.Name, errCall)
			return HandleRevertedTransaction(ctx, chainState, tx, toAddress, toAddress, blockTime, enableTrace, errCall.Error())
		}
		return HandleSuccessTransaction(ctx, chainState, tx, toAddress, toAddress, blockTime, enableTrace, nil, returnData)
	}
}

// HandleOffChainQuery services eth_call-style read-only requests (view methods) without going
// through the barrier-tx commit path — mirrors ValidatorHandler's off-chain query pattern
// (see transaction_processor_offchain.go).
func (h *GatewayHandler) HandleOffChainQuery(chainState *blockchain.ChainState, tx types.Transaction) ([]byte, error) {
	inputData := tx.CallData().Input()
	if len(inputData) < 4 {
		return nil, fmt.Errorf("invalid gateway calldata: too short")
	}
	method, err := h.abi.MethodById(inputData[:4])
	if err != nil {
		return nil, fmt.Errorf("unknown gateway method selector: %w", err)
	}
	return h.handleView(chainState, method, inputData[4:])
}

// HandleOffChainQueryResult wraps HandleOffChainQuery's raw ABI-packed output into a
// types.ExecuteSCResult, matching the (tx, chainState) types.ExecuteSCResult signature
// transaction_processor_offchain.go's eth_call dispatch sites expect — see
// ValidatorHandler.HandleOffChainQuery (validation_query.go) for the exact precedent this
// mirrors (Milestone B: wiring GATEWAY_CONTRACT_ADDRESS into the same eth_call dispatch that
// VALIDATOR_CONTRACT_ADDRESS already uses).
func (h *GatewayHandler) HandleOffChainQueryResult(tx types.Transaction, chainState *blockchain.ChainState) (types.ExecuteSCResult, error) {
	returnData, err := h.HandleOffChainQuery(chainState, tx)
	return smart_contract.NewExecuteSCResult(
		tx.Hash(),
		pb.RECEIPT_STATUS_TRANSACTION_ERROR,
		pb.EXCEPTION_NONE,
		returnData,
		0, // GasUsed is 0 — view function
		common.Hash{},
		make(map[string][]byte),
		make(map[string][]byte),
		make(map[string][]byte),
		make(map[string][]byte),
		make(map[string][]byte),
		make(map[string]common.Address),
		make(map[string][]byte),
		make(map[common.Address][]common.Address),
		make(map[common.Address][][2][]byte),
		[]types.EventLog{},
	), err
}

func (h *GatewayHandler) handleWrite(
	chainState *blockchain.ChainState, tx types.Transaction, method *abi.Method, argData []byte,
) ([]types.EventLog, []byte, error) {
	engine, err := loadGatewayEngine(chainState)
	if err != nil {
		return nil, nil, err
	}

	args, err := method.Inputs.Unpack(argData)
	if err != nil {
		return nil, nil, fmt.Errorf("unpack %s input: %w", method.Name, err)
	}

	var eventLogs []types.EventLog

	switch method.Name {
	case "outbound":
		params := cross_chain.OutboundParams{
			DestChainID: mustUint64(args[0]),
			Target:      mustAddress(args[1]),
			Payload:     mustBytes(args[2]),
			AssetID:     mustBigInt(args[3]),
			Value:       mustBigInt(args[4]),
			Tip:         mustBigInt(args[5]),
			HopCount:    mustUint8(args[6]),
			Ordered:     mustBool(args[7]),
		}
		msg, err := engine.Outbound(tx.FromAddress(), params, tx.Hash())
		if err != nil {
			return nil, nil, err
		}
		if event, ok := h.abi.Events["MessageSent"]; ok {
			eventData, packErr := event.Inputs.NonIndexed().Pack(msg.Sequence)
			if packErr == nil {
				eventLogs = append(eventLogs, smart_contract.NewEventLog(
					tx.Hash(), tx.ToAddress(), eventData,
					[][]byte{event.ID.Bytes(), msg.MessageID.Bytes(), leftPadUint64(msg.DestChainID)},
				))
			}
		}

	case "attestCommit":
		cert := cross_chain.QuorumCert{
			Epoch:              mustUint64(args[3]),
			AggregateSignature: hexutil.Bytes(mustBytes(args[4])),
			SignerBitmap:       hexutil.Bytes(mustBytes(args[5])),
		}
		if _, err := engine.AttestCommit(mustUint64(args[0]), mustHash(args[1]), mustBigInt(args[2]), cert); err != nil {
			return nil, nil, err
		}

	case "claimMessage":
		msg := cross_chain.CrossChainMessage{
			MessageID:     mustHash(args[0]),
			SourceChainID: mustUint64(args[1]),
			DestChainID:   mustUint64(args[2]),
			Sequence:      mustUint64(args[3]),
			HopCount:      mustUint8(args[4]),
			Sender:        mustAddress(args[5]),
			Target:        mustAddress(args[6]),
			AssetID:       mustBigInt(args[7]),
			Value:         mustBigInt(args[8]),
			Payload:       mustBytes(args[9]),
			Tip:           mustBigInt(args[10]),
			Ordered:       mustBool(args[11]),
		}
		proof := cross_chain.MerkleProof{
			LeafIndex: mustBigInt(args[12]).Uint64(),
			Siblings:  mustHashSlice(args[13]),
		}
		commitRoot := mustHash(args[14])
		status, err := engine.ClaimMessage(msg, proof, commitRoot, tx.FromAddress())
		if err != nil {
			return nil, nil, err
		}
		if event, ok := h.abi.Events["MessageStatusChanged"]; ok {
			eventData, packErr := event.Inputs.NonIndexed().Pack(uint8(status))
			if packErr == nil {
				eventLogs = append(eventLogs, smart_contract.NewEventLog(
					tx.Hash(), tx.ToAddress(), eventData,
					[][]byte{event.ID.Bytes(), msg.MessageID.Bytes()},
				))
			}
		}

	case "refund":
		if err := engine.Refund(mustHash(args[0]), mustUint64(args[1]), mustAddress(args[2]), mustBigInt(args[3]), mustBool(args[4])); err != nil {
			return nil, nil, err
		}

	default:
		return nil, nil, fmt.Errorf("unhandled gateway write method: %s", method.Name)
	}

	if err := saveGatewayEngine(chainState, engine); err != nil {
		return nil, nil, err
	}
	return eventLogs, nil, nil
}

func (h *GatewayHandler) handleView(chainState *blockchain.ChainState, method *abi.Method, argData []byte) ([]byte, error) {
	engine, err := loadGatewayEngine(chainState)
	if err != nil {
		return nil, err
	}

	switch method.Name {
	case "getMessageStatus":
		args, err := method.Inputs.Unpack(argData)
		if err != nil {
			return nil, fmt.Errorf("unpack getMessageStatus input: %w", err)
		}
		status := engine.GetMessageStatus(mustHash(args[0]))
		return method.Outputs.Pack(uint8(status))

	case "getOriginalSender":
		sender, sourceChainID, err := engine.GetOriginalSender()
		if err != nil {
			return nil, err
		}
		return method.Outputs.Pack(sender, new(big.Int).SetUint64(sourceChainID))

	case "isCalledByGateway":
		return method.Outputs.Pack(engine.IsCalledByGateway())

	case "getChainRegistry":
		args, err := method.Inputs.Unpack(argData)
		if err != nil {
			return nil, fmt.Errorf("unpack getChainRegistry input: %w", err)
		}
		chainID := mustUint64(args[0])

		// engine is a fresh, single-goroutine local deserialization from loadGatewayEngine()
		// above (see its doc comment) — not a value shared across concurrent callers, so no
		// locking is needed to read its ChainRegistry map here.
		registry, exists := engine.ChainRegistry[chainID]

		if !exists {
			return method.Outputs.Pack(
				false,
				[][]byte{}, []uint64{}, [][]byte{},
				uint64(0), uint64(0), common.Address{}, [32]byte{}, "", uint64(0),
			)
		}

		pubkeys := make([][]byte, len(registry.Committee))
		stakes := make([]uint64, len(registry.Committee))
		popSignatures := make([][]byte, len(registry.Committee))
		for i, v := range registry.Committee {
			pubkeys[i] = v.PubkeyBLS
			stakes[i] = v.Stake
			popSignatures[i] = v.PopSignature
		}

		return method.Outputs.Pack(
			true,
			pubkeys, stakes, popSignatures,
			registry.Epoch, registry.QuorumThreshold, registry.GatewayContract,
			[32]byte(registry.StateRoot), registry.ArchivalEndpoint, registry.RegisteredAt,
		)

	default:
		return nil, fmt.Errorf("unhandled gateway view method: %s", method.Name)
	}
}

// --- ABI arg conversion helpers ---
// go-ethereum's abi.Unpack returns interface{} values whose concrete Go type is exactly what the
// Solidity type maps to (uint256 -> *big.Int, bytes32 -> [32]byte, bytes -> []byte, address ->
// common.Address, bool -> bool, uintN(N<=64) -> uintN). [32]byte is NOT common.Hash (distinct
// named types with the same underlying array), so bytes32 args need an explicit conversion.

func mustBigInt(v interface{}) *big.Int {
	b, _ := v.(*big.Int)
	if b == nil {
		return big.NewInt(0)
	}
	return b
}

func mustUint64(v interface{}) uint64 {
	if b, ok := v.(*big.Int); ok {
		return b.Uint64()
	}
	u, _ := v.(uint64)
	return u
}

func mustUint8(v interface{}) uint8 {
	u, _ := v.(uint8)
	return u
}

func mustBool(v interface{}) bool {
	b, _ := v.(bool)
	return b
}

func mustAddress(v interface{}) common.Address {
	a, _ := v.(common.Address)
	return a
}

func mustBytes(v interface{}) []byte {
	b, _ := v.([]byte)
	return b
}

func mustHash(v interface{}) common.Hash {
	if arr, ok := v.([32]byte); ok {
		return common.Hash(arr)
	}
	return common.Hash{}
}

func mustHashSlice(v interface{}) []common.Hash {
	arrs, ok := v.([][32]byte)
	if !ok {
		return nil
	}
	out := make([]common.Hash, len(arrs))
	for i, a := range arrs {
		out[i] = common.Hash(a)
	}
	return out
}

func leftPadUint64(v uint64) []byte {
	return new(big.Int).SetUint64(v).FillBytes(make([]byte, 32))
}
