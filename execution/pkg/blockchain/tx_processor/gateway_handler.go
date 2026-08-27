package tx_processor

import (
	"bytes"
	"context"
	"encoding/hex"
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
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/types"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/vm_processor"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
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

// CommitFinalizedCallback, if set, is invoked synchronously by the batchOutboundCommit() case
// whenever THIS node processes that transaction for real -- wires into
// CommitAttestationWorker.OnCommitFinalized (Milestone F), set from block_processor_core.go the
// same way SetEpochAdvancedCallback wires CommitteeAttestationWorker.OnEpochAdvanced
// (Milestone C). nil (the default) means this node isn't running a CommitAttestationWorker
// (RootAnchorRpcUrls/RootAnchorSubmitterPrivateKeyHex not configured) -- batchOutboundCommit()
// still works and still records the batch, it just has no local validator to auto-sign it.
var CommitFinalizedCallback func(sourceChainID, epoch uint64, commitRoot common.Hash)

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
		freshEngine := cross_chain.NewGatewayEngine(localChainID, map[uint64]cross_chain.ChainRegistry{}, emptyLedger)
		applyDevnetGovernanceTimelockOverride(freshEngine)
		applyGenesisCoordinatorConfig(freshEngine)
		applyReserveChainIDConfig(freshEngine)
		if err := applyMinRegistrationStakeConfig(freshEngine); err != nil {
			return nil, err
		}
		return freshEngine, nil
	}

	var engine cross_chain.GatewayEngine
	if err := json.Unmarshal(data, &engine); err != nil {
		return nil, fmt.Errorf("unmarshal GatewayEngine state: %w", err)
	}
	if localChainID != 0 {
		engine.LocalChainID = localChainID
	}
	if engine.SupplyLedger == nil {
		emptyLedger, err := cross_chain.NewGlobalSupplyLedger(big.NewInt(0), map[uint64]*big.Int{})
		if err != nil {
			return nil, fmt.Errorf("bootstrap empty GlobalSupplyLedger: %w", err)
		}
		engine.SupplyLedger = emptyLedger
	}
	// Milestone C fields: nil after unmarshaling a blob written before they existed.
	if engine.PendingCommitteeAttestations == nil {
		engine.PendingCommitteeAttestations = make(map[string][]cross_chain.CommitteeAttestationShare)
	}
	if engine.RegisteredPops == nil {
		engine.RegisteredPops = make(map[string][]byte)
	}
	applyDevnetGovernanceTimelockOverride(&engine)
	applyGenesisCoordinatorConfig(&engine)
	applyReserveChainIDConfig(&engine)
	if err := applyMinRegistrationStakeConfig(&engine); err != nil {
		return nil, err
	}
	return &engine, nil
}

// applyGenesisCoordinatorConfig is a no-op unless config.ConfigApp explicitly sets
// CrossChain.GenesisCoordinatorAddress AND the engine's GenesisCoordinator is still unset (the
// zero address) — see that field's own doc comment (pkg/config/config.go). Only ever sets the
// coordinator once, from the pristine/never-configured state: once a coordinator is recorded
// (whether from an earlier config application or restored from persisted state), it is locked in
// for the life of this GatewayEngine and a later config change cannot silently swap it out.
func applyGenesisCoordinatorConfig(engine *cross_chain.GatewayEngine) {
	if config.ConfigApp == nil || config.ConfigApp.CrossChain.GenesisCoordinatorAddress == "" {
		return
	}
	if engine.GenesisCoordinator != (common.Address{}) {
		return
	}
	engine.GenesisCoordinator = common.HexToAddress(config.ConfigApp.CrossChain.GenesisCoordinatorAddress)
}

// applyReserveChainIDConfig is a no-op unless config.ConfigApp.CrossChain.ReserveChainID is set
// — see that field's own doc comment (pkg/config/config.go) for why this is required (not
// opt-in like applyGenesisCoordinatorConfig) for any real cross-chain value deployment.
func applyReserveChainIDConfig(engine *cross_chain.GatewayEngine) {
	if config.ConfigApp == nil || config.ConfigApp.CrossChain.ReserveChainID == 0 {
		return
	}
	if engine.ReserveChainID != 0 {
		return
	}
	engine.ReserveChainID = config.ConfigApp.CrossChain.ReserveChainID
}

// applyMinRegistrationStakeConfig is a no-op unless config.ConfigApp.CrossChain
// .MinRegistrationStake is a non-empty, valid, positive decimal string — see that field's own
// doc comment (pkg/config/config.go) and GatewayEngine.MinRegistrationStake's for why this is
// opt-in (C6 mitigation, not a default-on rate limit). An empty string preserves the exact old
// permissionless-registration behavior. A non-empty but unparseable/non-positive string is a
// config mistake, not a silent no-op — it fails loudly at startup rather than deploying a chain
// that believes it configured a stake requirement but actually didn't.
func applyMinRegistrationStakeConfig(engine *cross_chain.GatewayEngine) error {
	raw := ""
	if config.ConfigApp != nil {
		raw = config.ConfigApp.CrossChain.MinRegistrationStake
	}
	if raw == "" {
		return nil
	}
	if engine.MinRegistrationStake != nil && engine.MinRegistrationStake.Sign() > 0 {
		return nil
	}
	amount, ok := new(big.Int).SetString(raw, 10)
	if !ok || amount.Sign() <= 0 {
		return fmt.Errorf("cross_chain.min_registration_stake_wei %q is not a valid positive base-10 integer", raw)
	}
	engine.MinRegistrationStake = amount
	return nil
}

// applyDevnetGovernanceTimelockOverride is a no-op unless config.ConfigApp explicitly sets
// CrossChain.DevnetGovernanceTimelockSecondsOverride — see that field's own doc comment
// (pkg/config/config.go) for why this exists and why it never affects a real production
// config.
func applyDevnetGovernanceTimelockOverride(engine *cross_chain.GatewayEngine) {
	if config.ConfigApp == nil {
		return
	}
	engine.ApplyGovernanceTimelockOverride(config.ConfigApp.CrossChain.DevnetGovernanceTimelockSecondsOverride)
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

func createVmProcessorForGateway(ctx context.Context, chainState *blockchain.ChainState, tx types.Transaction, blockTime uint64) (*vm_processor.VmProcessor, mvm.ExecutionEngine) {
	mvmId := tx.ToAddress()
	leaderAddr := common.Address{}
	if currentHeader := chainState.GetcurrentBlockHeader(); currentHeader != nil {
		leaderAddr = (*currentHeader).LeaderAddress()
	}

	// Clear any residual MVM API state to ensure a clean execution context for the barrier tx
	mvm.ClearMVMApi(mvmId)

	vmP := vm_processor.NewVmProcessor(chainState, mvmId, false, blockTime, leaderAddr)
	vmP.SetAccountStateDB(chainState.GetAccountStateDB())
	vmP.SetSmartContractDB(chainState.GetSmartContractDB())

	mvmE := mvm.NewExecutionEngine(mvmId, chainState.GetSmartContractDB(), chainState.GetAccountStateDB(), false)
	return vmP, mvmE
}

func processNativeMintBurnForGateway(
	ctx context.Context, chainState *blockchain.ChainState, tx types.Transaction,
	blockTime uint64, operationType uint64, amount *big.Int, from common.Address, to common.Address,
) error {
	vmP, mvmE := createVmProcessorForGateway(ctx, chainState, tx, blockTime)
	res, err := vmP.ProcessNativeMintBurn(ctx, tx, mvmE, operationType, amount, from, to)
	if err != nil {
		return err
	}
	if res.Status != pb.RECEIPT_STATUS_RETURNED {
		return fmt.Errorf("mvm execution failed with status %v", res.Status)
	}

	accountStateDB := chainState.GetAccountStateDB()
	for addrHex, addAmtBytes := range res.MapAddBalance {
		addr := common.HexToAddress(addrHex)
		amt := new(big.Int).SetBytes(addAmtBytes)
		if err := accountStateDB.AddBalance(addr, amt); err != nil {
			return err
		}
	}
	for addrHex, subAmtBytes := range res.MapSubBalance {
		addr := common.HexToAddress(addrHex)
		amt := new(big.Int).SetBytes(subAmtBytes)
		if err := accountStateDB.SubBalance(addr, amt); err != nil {
			return err
		}
	}
	return nil
}

func applyFullMvmResultToStateDB(chainState *blockchain.ChainState, res *mvm.MVMExecuteResult) error {
	accountStateDB := chainState.GetAccountStateDB()
	scDB := chainState.GetSmartContractDB()

	// 1. Balances
	for addrHex, addAmtBytes := range res.MapAddBalance {
		addr := common.HexToAddress(addrHex)
		amt := new(big.Int).SetBytes(addAmtBytes)
		if err := accountStateDB.AddBalance(addr, amt); err != nil {
			return err
		}
	}
	for addrHex, subAmtBytes := range res.MapSubBalance {
		addr := common.HexToAddress(addrHex)
		amt := new(big.Int).SetBytes(subAmtBytes)
		if err := accountStateDB.SubBalance(addr, amt); err != nil {
			return err
		}
	}

	// 2. Nonce
	for addrHex, nonceBytes := range res.MapNonce {
		addr := common.HexToAddress(addrHex)
		nonce := new(big.Int).SetBytes(nonceBytes).Uint64()
		accountStateDB.SetNonce(addr, nonce)
	}

	// 3. Code — new/changed bytecode (a CONTRACT_CALL payload that internally executes
	// CREATE/CREATE2, or the empty-Payload case, both leave state via MapCodeChange). Must be
	// applied via SetCode (address+codeHash+code together, so the code is actually retrievable
	// later by that hash) BEFORE the bare SetCodeHash fallback below — an address with a
	// genuinely new codeHash but no matching MapCodeChange entry (shouldn't happen, but keep
	// the two loops independent rather than assuming MapCodeChange is a superset) still gets
	// its hash recorded, just without code content, matching the previous behavior for that
	// case.
	for addrHex, code := range res.MapCodeChange {
		addr := common.HexToAddress(addrHex)
		codeHashBytes, ok := res.MapCodeHash[addrHex]
		if !ok {
			continue
		}
		scDB.SetCode(addr, common.BytesToHash(codeHashBytes), code)
	}
	for addrHex, hash := range res.MapCodeHash {
		addr := common.HexToAddress(addrHex)
		accountStateDB.SetCodeHash(addr, common.BytesToHash(hash))
	}

	// 4. Storage
	for addrHex, changes := range res.MapStorageChange {
		addr := common.HexToAddress(addrHex)
		var keys [][]byte
		var values [][]byte
		for keyHex, valueBytes := range changes {
			keys = append(keys, common.HexToHash(keyHex).Bytes())
			values = append(values, valueBytes)
		}
		if err := scDB.BatchSetStorageValues(addr, keys, values); err != nil {
			return err
		}
	}

	return nil
}

// isContractCall checks if the target address has code deployed in the state.
func isContractCall(chainState *blockchain.ChainState, target common.Address) bool {
	as, err := chainState.GetAccountStateDB().AccountState(target)
	if err != nil || as == nil {
		return false
	}
	scState := as.SmartContractState()
	if scState == nil {
		return false
	}
	return scState.CodeHash() != (common.Hash{})
}

// executeContractCallForGateway returns the real gas consumed by the call (mvmResult.GasUsed)
// alongside the error, so callers that locked a cross-chain gas budget up front (mục 2.6.5,
// see the CONTRACT_CALL call sites in claimMessage/verifyAndExecute) can settle/refund the
// unused portion. Most callers (custom-asset transfer/mint, which use fixed internally-built
// callData with no attacker-controlled gas-cost risk) simply ignore it.
func executeContractCallForGateway(
	ctx context.Context, chainState *blockchain.ChainState, tx types.Transaction,
	blockTime uint64, sender common.Address, target common.Address, payload []byte, amount *big.Int, gasLimit uint64,
) (uint64, error) {
	_, mvmE := createVmProcessorForGateway(ctx, chainState, tx, blockTime)

	lastBlockHeader := *chainState.GetcurrentBlockHeader()
	leaderAddr := lastBlockHeader.LeaderAddress()
	if leaderAddr == (common.Address{}) {
		leaderAddr = tx.ToAddress()
	}

	mvmResult := mvmE.Execute(
		sender.Bytes(),
		target.Bytes(),
		payload,
		amount,
		tx.MaxGasPrice(),
		gasLimit,
		lastBlockHeader.TimeStamp(),
		mt_common.BLOCK_GAS_LIMIT,
		blockTime,
		mt_common.MINIMUM_BASE_FEE,
		lastBlockHeader.BlockNumber()+1,
		leaderAddr,
		mvmE.GetKey(),
		tx.Hash().Bytes(),
		[]common.Address{}, // relatedAddresses
		false,              // isDebug
		false,              // isCache
	)

	if mvmResult.Status != pb.RECEIPT_STATUS_RETURNED {
		return mvmResult.GasUsed, fmt.Errorf("contract execution failed with status %v: %s", mvmResult.Status, mvmResult.Exception.String())
	}

	return mvmResult.GasUsed, applyFullMvmResultToStateDB(chainState, mvmResult)
}

// settleGasCappedContractCall executes a CONTRACT_CALL payload (mục 2.6.5) with execution gas
// capped by the message's locked GasFee, converted at mt_common.MINIMUM_BASE_FEE -- the same
// fixed base fee mvmE.Execute itself charges network-wide, so no cross-chain gas-price oracle is
// needed. Fails closed with a clear error if GasFee is missing/zero (no free gas for an
// attacker-controlled arbitrary payload -- mục 5.3 risk #9), rather than falling back to an
// unbounded tx.MaxGas() the way this call site used to. Any unused portion of GasFee (locked
// minus real gas actually consumed) is minted back to msg.Sender on THIS chain in the same
// settlement step -- a deliberate simplification of the doc's literal "hoàn qua message hoàn
// tiền, dùng chung cơ chế mục 2.4" wording, which would require a brand-new B->A reverse-
// attestation message type. This keeps the supply invariant identical (nothing is minted beyond
// what msg.Sender already had burned from them at outbound() time) at the cost of the leftover
// landing on the destination chain rather than travelling back to the source chain -- a UX/
// economics simplification, not a security one. Only used for the pure/native-message payload
// branch (Task 1.3); the custom-asset transfer()/mint() calls use fixed, non-attacker-controlled
// callData and are intentionally NOT gas-capped here.
func settleGasCappedContractCall(
	ctx context.Context, chainState *blockchain.ChainState, tx types.Transaction,
	blockTime uint64, msgSender common.Address, target common.Address, payload []byte, gasFee *big.Int,
) error {
	if gasFee == nil || gasFee.Sign() <= 0 {
		return fmt.Errorf("CONTRACT_CALL requires a locked gasFee (mục 2.6.5): got %v", gasFee)
	}
	gasCap := new(big.Int).Div(gasFee, big.NewInt(mt_common.MINIMUM_BASE_FEE)).Uint64()
	gasUsed, err := executeContractCallForGateway(ctx, chainState, tx, blockTime, msgSender, target, payload, big.NewInt(0), gasCap)
	if err != nil {
		return err
	}
	spent := new(big.Int).Mul(new(big.Int).SetUint64(gasUsed), big.NewInt(mt_common.MINIMUM_BASE_FEE))
	unused := new(big.Int).Sub(gasFee, spent)
	if unused.Sign() > 0 {
		if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 0, unused, tx.FromAddress(), msgSender); err != nil {
			return fmt.Errorf("refund unused gasFee: %w", err)
		}
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
	case "outbound", "attestCommit", "claimMessage", "refund",
		"registerCommitteePop", "submitCommitteeAttestation", "submitCommitAttestation", "committeeUpdate",
		"bootstrapFoundingChains", "batchOutboundCommit",
		"propose", "vote", "executeProposal", "registerAsset",
		"verifyAndExecute", "claimDeadChainBalance", "withdrawRelayerTip":
		eventLogs, returnData, logicErr := h.handleWrite(ctx, chainState, tx, method, inputData[4:], blockTime)
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
	ctx context.Context, chainState *blockchain.ChainState, tx types.Transaction, method *abi.Method, argData []byte, blockTime uint64,
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
	var returnData []byte

	switch method.Name {
	case "outbound":
		params := cross_chain.OutboundParams{
			DestChainID: mustUint64(args[0]),
			Target:      mustAddress(args[1]),
			Payload:     mustBytes(args[2]),
			AssetID:     mustBigInt(args[3]),
			Value:       mustBigInt(args[4]),
			Tip:         mustBigInt(args[5]),
			GasFee:      mustBigInt(args[6]),
			HopCount:    mustUint8(args[7]),
			Ordered:     mustBool(args[8]),
		}

		msg, err := engine.Outbound(tx.FromAddress(), params, tx.Hash())
		if err != nil {
			return nil, nil, err
		}

		// Balance mutations come last, after every check that can still fail — barrier TXs
		// write straight to AccountStateDB with no rollback-on-later-error (true_block_stm.go's
		// runBarrierTx: "no MVCC tracking, no retry-on-abort"), so any operation performed
		// AFTER a real burn/transferFrom that can itself fail would strand that burn/lock
		// permanently even though the whole TX reports as reverted. See
		// note/cross_chain_task1_native_value_fix_plan.md for the exploit shape this avoids.
		if params.AssetID == nil || params.AssetID.Sign() == 0 {
			// Task 1.1/1.3: native path — burn Value + Tip + GasFee together in ONE call so a
			// failure partway through (e.g. balance covers Tip+GasFee but not the full total)
			// can never leave part of it stuck-burned with the rest never taken. GasFee (mục
			// 2.6.5) is the cross-chain gas budget for a CONTRACT_CALL payload, settled at
			// claim time (see executeContractCallForGateway call sites in claimMessage/
			// verifyAndExecute) — always native regardless of AssetID, since it pays for
			// destination-chain EVM execution, not the bridged value itself.
			totalDeduct := big.NewInt(0)
			if params.Value != nil && params.Value.Sign() > 0 {
				totalDeduct.Add(totalDeduct, params.Value)
			}
			if params.Tip != nil && params.Tip.Sign() > 0 {
				totalDeduct.Add(totalDeduct, params.Tip)
			}
			if params.GasFee != nil && params.GasFee.Sign() > 0 {
				totalDeduct.Add(totalDeduct, params.GasFee)
			}
			if totalDeduct.Sign() > 0 {
				if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 1, totalDeduct, tx.FromAddress(), tx.ToAddress()); err != nil {
					return nil, nil, fmt.Errorf("outbound native burn failed: %w", err)
				}
			}
		} else {
			// Task 1.2: custom asset path — lock the asset (transferFrom, can fail on
			// insufficient allowance/balance in the token contract) FIRST, then burn the
			// native Tip LAST, so Tip can never be stuck-burned by a subsequent asset-lock
			// failure.
			if params.Value != nil && params.Value.Sign() > 0 {
				asset, err := engine.AssetRegistry.GetAsset(params.AssetID)
				if err != nil {
					return nil, nil, fmt.Errorf("failed to get asset info: %w", err)
				}
				sourceContract := asset.CanonicalContract
				if engine.LocalChainID != asset.HomeChainID {
					sourceContract = asset.WrappedContracts[engine.LocalChainID]
				}

				// Construct transferFrom(address,address,uint256)
				transferFromID := crypto.Keccak256Hash([]byte("transferFrom(address,address,uint256)")).Bytes()[:4]
				callData := make([]byte, 4+32+32+32)
				copy(callData[0:4], transferFromID)
				copy(callData[4:36], common.LeftPadBytes(tx.FromAddress().Bytes(), 32))
				copy(callData[36:68], common.LeftPadBytes(tx.ToAddress().Bytes(), 32))
				copy(callData[68:100], common.LeftPadBytes(params.Value.Bytes(), 32))

				// EXPERIMENTAL FIX (2026-08-25, found + fixed while writing the real-token
				// Task 1.2 acceptance test — see note/cross_chain_production_readiness_plan.md
				// Phase 0.7): msg.sender for this internal transferFrom() call MUST be the
				// Gateway contract itself, not tx.FromAddress() (the bridging user). A real
				// ERC-20's transferFrom checks allowance[from][msg.sender] — a user can only
				// ever sanely approve() the Gateway (a fixed, known address) as spender, never
				// their own tx sender address. With sender=tx.FromAddress() (== `from` too,
				// since outbound() always locks the caller's own tokens), this checked
				// allowance[user][user], which is never set by any real approval flow — the
				// custom-asset outbound path was unconditionally broken against any real
				// standards-compliant ERC-20, confirmed by a real deployed-contract test
				// reverting with ERR_EXECUTION_REVERTED before this fix.
				if _, err := executeContractCallForGateway(
					ctx, chainState, tx, blockTime, mt_common.GATEWAY_CONTRACT_ADDRESS, sourceContract, callData, big.NewInt(0), tx.MaxGas(),
				); err != nil {
					return nil, nil, fmt.Errorf("outbound custom asset transferFrom failed: %w", err)
				}
			}
			// Tip and GasFee are both always native (mục 2.6.5) regardless of AssetID — burn
			// them together for the same "no stuck-burn on partial failure" reason as the
			// native path above.
			nativeExtras := big.NewInt(0)
			if params.Tip != nil && params.Tip.Sign() > 0 {
				nativeExtras.Add(nativeExtras, params.Tip)
			}
			if params.GasFee != nil && params.GasFee.Sign() > 0 {
				nativeExtras.Add(nativeExtras, params.GasFee)
			}
			if nativeExtras.Sign() > 0 {
				if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 1, nativeExtras, tx.FromAddress(), tx.ToAddress()); err != nil {
					return nil, nil, fmt.Errorf("outbound native tip/gasFee burn failed: %w", err)
				}
			}
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
		assetId := mustBigInt(args[3])
		proof := cross_chain.MerkleProof{
			LeafIndex: mustBigInt(args[4]).Uint64(),
			Siblings:  mustHashSlice(args[5]),
		}
		cert := cross_chain.QuorumCert{
			Epoch:              mustUint64(args[6]),
			AggregateSignature: hexutil.Bytes(mustBytes(args[7])),
			SignerBitmap:       hexutil.Bytes(mustBytes(args[8])),
		}
		if _, err := engine.AttestCommit(mustUint64(args[0]), mustHash(args[1]), mustBigInt(args[2]), assetId, proof, cert); err != nil {
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
			GasFee:        mustBigInt(args[11]),
			Ordered:       mustBool(args[12]),
		}
		proof := cross_chain.MerkleProof{
			LeafIndex: mustBigInt(args[13]).Uint64(),
			Siblings:  mustHashSlice(args[14]),
		}
		commitRoot := mustHash(args[15])
		status, err := engine.ClaimMessage(msg, proof, commitRoot, tx.FromAddress())
		if err != nil {
			return nil, nil, err
		}

		// Task 1.3: Contract Call (only for Native or Pure messages)
		if (msg.AssetID == nil || msg.AssetID.Sign() == 0) && len(msg.Payload) > 0 && isContractCall(chainState, msg.Target) {
			// Sender of the internal EVM call is msg.Sender (the original sender on source chain)
			if err := settleGasCappedContractCall(
				ctx, chainState, tx, blockTime, msg.Sender, msg.Target, msg.Payload, msg.GasFee,
			); err != nil {
				return nil, nil, fmt.Errorf("claimMessage payload execution failed: %v", err)
			}
		} else if msg.GasFee != nil && msg.GasFee.Sign() > 0 {
			// No real CONTRACT_CALL happened (no code at Target, empty Payload, or this is a
			// custom-asset message) -- nothing to spend the locked gas budget on, refund it in
			// full rather than stranding it.
			if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 0, msg.GasFee, tx.FromAddress(), msg.Sender); err != nil {
				return nil, nil, fmt.Errorf("claimMessage gasFee refund failed: %v", err)
			}
		}

		if msg.AssetID == nil || msg.AssetID.Sign() == 0 {
			// Task 1.1: Real Native Mint / Credit to recipient (msg.Target)
			if msg.Value != nil && msg.Value.Sign() > 0 {
				if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 0, msg.Value, tx.FromAddress(), msg.Target); err != nil {
					return nil, nil, fmt.Errorf("claimMessage native mint failed: %v", err)
				}
			}
		} else {
			// Task 1.2: Custom Asset Mint/Unlock
			if msg.Value != nil && msg.Value.Sign() > 0 {
				if len(msg.Payload) != 20 {
					return nil, nil, fmt.Errorf("invalid payload for custom asset claim (expected 20 bytes recipient address, got %d)", len(msg.Payload))
				}
				recipient := common.BytesToAddress(msg.Payload)

				asset, err := engine.AssetRegistry.GetAsset(msg.AssetID)
				if err != nil {
					return nil, nil, fmt.Errorf("failed to get asset info: %w", err)
				}

				targetContract := msg.Target
				var callData []byte

				if engine.LocalChainID == asset.HomeChainID {
					// Unlock from vault: transfer(recipient, value)
					transferID := crypto.Keccak256Hash([]byte("transfer(address,uint256)")).Bytes()[:4]
					callData = make([]byte, 4+32+32)
					copy(callData[0:4], transferID)
					copy(callData[4:36], common.LeftPadBytes(recipient.Bytes(), 32))
					copy(callData[36:68], common.LeftPadBytes(msg.Value.Bytes(), 32))
				} else {
					// Mint wrapped token: mint(recipient, value)
					mintID := crypto.Keccak256Hash([]byte("mint(address,uint256)")).Bytes()[:4]
					callData = make([]byte, 4+32+32)
					copy(callData[0:4], mintID)
					copy(callData[4:36], common.LeftPadBytes(recipient.Bytes(), 32))
					copy(callData[36:68], common.LeftPadBytes(msg.Value.Bytes(), 32))
				}

				// EXPERIMENTAL FIX (2026-08-25, same finding as outbound's transferFrom fix
				// above): msg.sender for this internal transfer()/mint() call must be the
				// Gateway itself. transfer(recipient, value) moves balanceOf[msg.sender] — if
				// msg.sender were tx.FromAddress() (the relayer submitting claimMessage), the
				// vault-unlock branch would try to move the RELAYER's own token balance
				// (which doesn't hold the locked tokens — the Gateway does), not the vault's;
				// and any real access-controlled mint() would reject a non-Gateway caller.
				if _, err := executeContractCallForGateway(
					ctx, chainState, tx, blockTime, mt_common.GATEWAY_CONTRACT_ADDRESS, targetContract, callData, big.NewInt(0), tx.MaxGas(),
				); err != nil {
					return nil, nil, fmt.Errorf("claim custom asset execution failed: %w", err)
				}
			}
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
		msg := cross_chain.CrossChainMessage{
			MessageID:     mustHash(args[0]),
			SourceChainID: mustUint64(args[1]),
			DestChainID:   mustUint64(args[2]),
			Sequence:      mustBigInt(args[3]).Uint64(),
			HopCount:      mustUint8(args[4]),
			Sender:        mustAddress(args[5]),
			Target:        mustAddress(args[6]),
			AssetID:       mustBigInt(args[7]),
			Value:         mustBigInt(args[8]),
			Payload:       mustBytes(args[9]),
			Tip:           mustBigInt(args[10]),
			GasFee:        mustBigInt(args[11]),
			Ordered:       mustBool(args[12]),
		}
		proof := cross_chain.MerkleProof{
			LeafIndex: mustBigInt(args[13]).Uint64(),
			Siblings:  mustHashSlice(args[14]),
		}
		commitRoot := mustHash(args[15])
		failCert := cross_chain.QuorumCert{
			Epoch:              mustUint64(args[16]),
			AggregateSignature: hexutil.Bytes(mustBytes(args[17])),
			SignerBitmap:       hexutil.Bytes(mustBytes(args[18])),
		}
		if err := engine.Refund(msg, proof, commitRoot, failCert); err != nil {
			return nil, nil, err
		}

		// GasFee (mục 2.6.5): the message never executed at all on the destination chain (that's
		// exactly what this failure cert attests), so the entire locked gas budget is unused --
		// refund it in full alongside Value, regardless of AssetID (GasFee is always native).
		if msg.GasFee != nil && msg.GasFee.Sign() > 0 {
			if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 0, msg.GasFee, tx.FromAddress(), msg.Sender); err != nil {
				return nil, nil, fmt.Errorf("refund gasFee restoration failed: %v", err)
			}
		}

		// Task 1.1: Real Native Refund Credit back to original sender (msg.Sender) on source chain
		if msg.AssetID == nil || msg.AssetID.Sign() == 0 {
			if msg.Value != nil && msg.Value.Sign() > 0 {
				if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 0, msg.Value, tx.FromAddress(), msg.Sender); err != nil {
					return nil, nil, fmt.Errorf("refund native balance restoration failed: %v", err)
				}
			}
		} else {
			// Task 1.2: Custom Asset Refund
			if msg.Value != nil && msg.Value.Sign() > 0 {
				asset, err := engine.AssetRegistry.GetAsset(msg.AssetID)
				if err != nil {
					return nil, nil, fmt.Errorf("failed to get asset info: %w", err)
				}

				sourceContract := asset.CanonicalContract
				if engine.LocalChainID != asset.HomeChainID {
					sourceContract = asset.WrappedContracts[engine.LocalChainID]
				}

				var callData []byte
				if engine.LocalChainID == asset.HomeChainID {
					// Unlock from vault back to sender: transfer(sender, value)
					transferID := crypto.Keccak256Hash([]byte("transfer(address,uint256)")).Bytes()[:4]
					callData = make([]byte, 4+32+32)
					copy(callData[0:4], transferID)
					copy(callData[4:36], common.LeftPadBytes(msg.Sender.Bytes(), 32))
					copy(callData[36:68], common.LeftPadBytes(msg.Value.Bytes(), 32))
				} else {
					// Mint wrapped token back to sender (if failed outbound): mint(sender, value)
					mintID := crypto.Keccak256Hash([]byte("mint(address,uint256)")).Bytes()[:4]
					callData = make([]byte, 4+32+32)
					copy(callData[0:4], mintID)
					copy(callData[4:36], common.LeftPadBytes(msg.Sender.Bytes(), 32))
					copy(callData[36:68], common.LeftPadBytes(msg.Value.Bytes(), 32))
				}

				// EXPERIMENTAL FIX (2026-08-25, same finding as the other 3 custom-asset call
				// sites in this file): msg.sender must be the Gateway itself, not
				// tx.FromAddress() — see the outbound() transferFrom fix comment above for the
				// full reasoning.
				if _, err := executeContractCallForGateway(
					ctx, chainState, tx, blockTime, mt_common.GATEWAY_CONTRACT_ADDRESS, sourceContract, callData, big.NewInt(0), tx.MaxGas(),
				); err != nil {
					return nil, nil, fmt.Errorf("refund custom asset restoration failed: %w", err)
				}
			}
		}

		if event, ok := h.abi.Events["MessageRefunded"]; ok {
			eventData, packErr := event.Inputs.NonIndexed().Pack(msg.Value)
			if packErr == nil {
				eventLogs = append(eventLogs, smart_contract.NewEventLog(
					tx.Hash(), tx.ToAddress(), eventData,
					[][]byte{event.ID.Bytes(), msg.MessageID.Bytes()},
				))
			}
		}

	case "registerCommitteePop":
		// Milestone C: permissionless PoP registry — anyone may register a PoP for their OWN
		// key at any time (self-authenticating via PopVerify itself, no committee membership
		// check needed). See committeeUpdate below for why this durable registry exists: a
		// worker assembling a new committee only holds its own private key and cannot produce
		// PoP on behalf of other members.
		pubkeyBls := mustBytes(args[0])
		popSig := mustBytes(args[1])
		if valid, popErr := cross_chain.PopVerify(pubkeyBls, popSig); popErr != nil || !valid {
			return nil, nil, fmt.Errorf("registerCommitteePop: %w", cross_chain.ErrPopVerifyFailed)
		}
		engine.RegisteredPops[hex.EncodeToString(pubkeyBls)] = popSig

	case "submitCommitteeAttestation":
		sourceChainID := mustUint64(args[0])
		oldEpoch := mustUint64(args[1])
		payloadHash := mustHash(args[2])
		signerPubkeyBls := mustBytes(args[3])
		signature := mustBytes(args[4])

		registry, exists := engine.ChainRegistry[sourceChainID]
		if !exists {
			return nil, nil, fmt.Errorf("submitCommitteeAttestation: %w: chain %d", cross_chain.ErrUnknownSourceChain, sourceChainID)
		}
		// Fail-closed: the share must attest FROM the committee currently on file (the one
		// about to be replaced) — matches attestCommitInternal's epoch-mismatch convention
		// (gateway.go).
		if oldEpoch != registry.Epoch {
			return nil, nil, fmt.Errorf("submitCommitteeAttestation: %w: expected %d, got %d", cross_chain.ErrEpochMismatch, registry.Epoch, oldEpoch)
		}
		isMember := false
		for _, v := range registry.Committee {
			if bytes.Equal(v.PubkeyBLS, signerPubkeyBls) {
				isMember = true
				break
			}
		}
		if !isMember {
			return nil, nil, fmt.Errorf("submitCommitteeAttestation: signer is not a member of chain %d's current committee", sourceChainID)
		}
		pubKey := mt_common.PubkeyFromBytes(signerPubkeyBls)
		sig := mt_common.SignFromBytes(signature)
		if !bls.VerifySign(pubKey, sig, payloadHash.Bytes()) {
			return nil, nil, cross_chain.ErrInvalidBLSSignature
		}

		key := committeeAttestationKey(sourceChainID, oldEpoch, payloadHash)
		for _, s := range engine.PendingCommitteeAttestations[key] {
			if bytes.Equal(s.SignerPubkeyBLS, signerPubkeyBls) {
				return nil, nil, fmt.Errorf("submitCommitteeAttestation: pubkey already submitted a share for this update")
			}
		}
		engine.PendingCommitteeAttestations[key] = append(engine.PendingCommitteeAttestations[key], cross_chain.CommitteeAttestationShare{
			SignerPubkeyBLS: signerPubkeyBls,
			Signature:       signature,
		})

	case "submitCommitAttestation":
		sourceChainID := mustUint64(args[0])
		epoch := mustUint64(args[1])
		commitRoot := mustHash(args[2])
		signerPubkeyBls := mustBytes(args[3])
		signature := mustBytes(args[4])

		registry, exists := engine.ChainRegistry[sourceChainID]
		if !exists {
			return nil, nil, fmt.Errorf("submitCommitAttestation: %w: chain %d", cross_chain.ErrUnknownSourceChain, sourceChainID)
		}
		if epoch != registry.Epoch {
			return nil, nil, fmt.Errorf("submitCommitAttestation: %w: expected %d, got %d", cross_chain.ErrEpochMismatch, registry.Epoch, epoch)
		}
		isMember := false
		for _, v := range registry.Committee {
			if bytes.Equal(v.PubkeyBLS, signerPubkeyBls) {
				isMember = true
				break
			}
		}
		if !isMember {
			return nil, nil, fmt.Errorf("submitCommitAttestation: signer is not a member of chain %d's current committee", sourceChainID)
		}
		commitMsg := cross_chain.ComputeCommitRootAttestMessage(commitRoot)
		pubKey := mt_common.PubkeyFromBytes(signerPubkeyBls)
		sig := mt_common.SignFromBytes(signature)
		if !bls.VerifySign(pubKey, sig, commitMsg) {
			return nil, nil, cross_chain.ErrInvalidBLSSignature
		}

		key := commitAttestationKey(sourceChainID, epoch, commitRoot)
		for _, s := range engine.PendingCommitAttestations[key] {
			if bytes.Equal(s.SignerPubkeyBLS, signerPubkeyBls) {
				return nil, nil, fmt.Errorf("submitCommitAttestation: pubkey already submitted a share for this commit root")
			}
		}
		engine.PendingCommitAttestations[key] = append(engine.PendingCommitAttestations[key], cross_chain.CommitAttestationShare{
			SignerPubkeyBLS: signerPubkeyBls,
			Signature:       signature,
		})

	case "committeeUpdate":
		sourceChainID := mustUint64(args[0])
		newEpoch := mustUint64(args[1])
		newCommitteePubkeys := mustBytesSlice(args[2])
		newCommitteeStakes := mustUint64Slice(args[3])
		newCommitteePopSignatures := mustBytesSlice(args[4])
		quorumThreshold := mustUint64(args[5])
		stateRoot := mustHash(args[6])
		accountTreeRoot := mustHash(args[7])
		payloadHash := mustHash(args[8])
		aggPubkeys := mustBytesSlice(args[9])
		aggSignature := mustBytes(args[10])

		if len(newCommitteePubkeys) != len(newCommitteeStakes) || len(newCommitteePubkeys) != len(newCommitteePopSignatures) {
			return nil, nil, fmt.Errorf("committeeUpdate: mismatched new-committee array lengths (pubkeys=%d stakes=%d popSignatures=%d)",
				len(newCommitteePubkeys), len(newCommitteeStakes), len(newCommitteePopSignatures))
		}
		newCommittee := make([]cross_chain.ValidatorEntry, len(newCommitteePubkeys))
		for i := range newCommitteePubkeys {
			// Never trust a PoP embedded directly in this tx — require it to already match a
			// PoP independently registered via registerCommitteePop. Otherwise an attacker
			// submitting the final tx could fabricate a "valid-looking" PoP for a rogue key
			// they don't actually hold the matching secret for is exactly what PoP exists to
			// prevent, so it must come from the durable, separately-verified registry.
			registered := engine.RegisteredPops[hex.EncodeToString(newCommitteePubkeys[i])]
			if len(registered) == 0 || !bytes.Equal(registered, newCommitteePopSignatures[i]) {
				return nil, nil, fmt.Errorf("committeeUpdate: pubkey %x has no matching registered PoP (call registerCommitteePop first)", newCommitteePubkeys[i])
			}
			newCommittee[i] = cross_chain.ValidatorEntry{
				PubkeyBLS:    newCommitteePubkeys[i],
				Stake:        newCommitteeStakes[i],
				PopSignature: newCommitteePopSignatures[i],
			}
		}

		// Recompute the digest from the claimed inputs — must match exactly, or a submitter
		// could change newCommittee/stateRoot/accountTreeRoot/newEpoch after signatures were already collected
		// over a different payloadHash.
		expectedDigest := cross_chain.ComputeCommitteeUpdateDigest(sourceChainID, newEpoch, newCommittee, stateRoot, accountTreeRoot)
		if expectedDigest != payloadHash {
			return nil, nil, fmt.Errorf("committeeUpdate: payloadHash %s does not match recomputed digest %s", payloadHash.Hex(), expectedDigest.Hex())
		}

		registry, exists := engine.ChainRegistry[sourceChainID]
		if !exists {
			return nil, nil, fmt.Errorf("committeeUpdate: %w: chain %d", cross_chain.ErrUnknownSourceChain, sourceChainID)
		}

		// Real BLS quorum-cert verification against the OLD (currently on-file) committee —
		// same structure as attestCommitInternal's verification (gateway.go), generalized to a
		// caller-supplied signer list instead of a bitmap (aggPubkeys plays the same role).
		var totalStake uint64
		for _, v := range registry.Committee {
			totalStake += v.Stake
		}
		if totalStake == 0 {
			return nil, nil, cross_chain.ErrZeroTotalStake
		}
		seen := make(map[string]bool, len(aggPubkeys))
		var accumulatedStake uint64
		for _, pk := range aggPubkeys {
			key := hex.EncodeToString(pk)
			if seen[key] {
				return nil, nil, fmt.Errorf("committeeUpdate: duplicate signer in aggPubkeys")
			}
			seen[key] = true
			found := false
			for _, v := range registry.Committee {
				if bytes.Equal(v.PubkeyBLS, pk) {
					accumulatedStake += v.Stake
					found = true
					break
				}
			}
			if !found {
				return nil, nil, fmt.Errorf("committeeUpdate: aggPubkeys contains a key that is not a member of chain %d's current committee", sourceChainID)
			}
		}
		threshold := (totalStake*2 + 2) / 3
		if registry.QuorumThreshold > 0 {
			threshold = (totalStake*registry.QuorumThreshold + 9999) / 10000
		}
		if accumulatedStake < threshold || len(aggPubkeys) == 0 {
			return nil, nil, fmt.Errorf("committeeUpdate: %w: accumulated stake %d < threshold %d", cross_chain.ErrQuorumNotReached, accumulatedStake, threshold)
		}

		var sigValid bool
		if len(aggPubkeys) == 1 {
			pubKey := mt_common.PubkeyFromBytes(aggPubkeys[0])
			sig := mt_common.SignFromBytes(aggSignature)
			sigValid = bls.VerifySign(pubKey, sig, payloadHash.Bytes())
		} else {
			msgs := make([][]byte, len(aggPubkeys))
			for i := range msgs {
				msgs[i] = payloadHash.Bytes()
			}
			sigValid = bls.VerifyAggregateSign(aggPubkeys, aggSignature, msgs)
		}
		if !sigValid {
			return nil, nil, cross_chain.ErrInvalidBLSSignature
		}

		update := cross_chain.CommitteeUpdate{
			SourceChainID:   sourceChainID,
			NewEpoch:        newEpoch,
			NewCommittee:    newCommittee,
			QuorumThreshold: quorumThreshold,
			StateRoot:       stateRoot,
			AccountTreeRoot: accountTreeRoot,
			Cert: cross_chain.QuorumCert{
				Epoch:              registry.Epoch,
				AggregateSignature: aggSignature,
			},
		}
		// ApplyCommitteeUpdate takes map[uint64]*ChainRegistry; GatewayEngine.ChainRegistry is
		// map[uint64]ChainRegistry (value) — adapt at this call site rather than changing
		// ApplyCommitteeUpdate's signature (kept identical to its Rust mirror).
		regCopy := registry
		adapter := map[uint64]*cross_chain.ChainRegistry{sourceChainID: &regCopy}
		// isOldCertValid=true: the real verification above (membership+stake+signature) IS the
		// check this bool used to be a caller-supplied placeholder for.
		if err := cross_chain.ApplyCommitteeUpdate(adapter, update, true); err != nil {
			return nil, nil, err
		}
		engine.ChainRegistry[sourceChainID] = *adapter[sourceChainID]
		delete(engine.PendingCommitteeAttestations, committeeAttestationKey(sourceChainID, registry.Epoch, payloadHash))

	case "bootstrapFoundingChains":
		// See GatewayEngine.BootstrapFoundingChains's doc comment (pkg/cross_chain/gateway.go)
		// and this file's ABI doc comment for why this exists and why it's safe: self-closing
		// after the first successful call, requires >= MinFoundingChains, requires real PoP for
		// every committee member. Task 2: Pass tx.FromAddress() to restrict to GenesisCoordinator if set.
		payloads := mustBytesSlice(args[0])
		if err := engine.BootstrapFoundingChainsWithCaller(tx.FromAddress(), payloads); err != nil {
			return nil, nil, err
		}
		metrics.RegisteredChainCount.Set(float64(len(engine.ChainRegistry)))

	case "batchOutboundCommit":
		destChainID := mustUint64(args[0])
		epoch := chainState.GetCurrentEpoch()
		commitRoot, messages, err := engine.BatchOutboundCommit(destChainID, epoch)
		if err != nil {
			return nil, nil, err
		}
		packed, packErr := method.Outputs.Pack(commitRoot, big.NewInt(int64(len(messages))))
		if packErr != nil {
			return nil, nil, packErr
		}
		returnData = packed
		if event, ok := h.abi.Events["CommitBatched"]; ok {
			eventData, packErr := event.Inputs.NonIndexed().Pack(big.NewInt(int64(len(messages))), epoch)
			if packErr == nil {
				eventLogs = append(eventLogs, smart_contract.NewEventLog(
					tx.Hash(), tx.ToAddress(), eventData,
					[][]byte{event.ID.Bytes(), commitRoot.Bytes(), leftPadUint64(destChainID)},
				))
			}
		}
		// Wire into CommitAttestationWorker (Milestone F) so THIS node's own validator (if a
		// committee member) signs and submits a real BLS attestation share for the commit root
		// it just deterministically computed -- mirrors epochAdvancedCallback's synchronous,
		// in-process trigger pattern for CommitteeAttestationWorker (Milestone C). Every
		// validator node processes this same transaction identically, so every one of them
		// invokes its own local worker with the exact same (chainID, epoch, commitRoot).
		if CommitFinalizedCallback != nil {
			CommitFinalizedCallback(engine.LocalChainID, epoch, commitRoot)
		}

	case "propose":
		// Anti-spam: Require a fee (e.g. 0.1 native token) to propose
		fee := tx.Amount()
		requiredFee := big.NewInt(100_000_000_000_000_000) // 0.1 native token
		if fee == nil || fee.Cmp(requiredFee) < 0 {
			return nil, nil, fmt.Errorf("propose requires a fee of at least 0.1 native tokens to prevent spam (got %v)", fee)
		}

		engine.EnsureGovernance()
		kind := cross_chain.GovernanceProposalKind(mustUint8(args[0]))
		payload := mustBytes(args[1])
		// Security fix: args[2] (the ABI's declared "proposedAt") is a raw caller-supplied
		// value with nothing to cross-check it against — GovernanceEngine.Propose/Vote/Execute
		// were written as pure functions that trust whatever timestamp they're given, never
		// meant to be fed directly from unauthenticated calldata. A caller naming a future
		// timestamp here (and an equally fake future one at vote/executeProposal time) can walk
		// EffectiveAt arbitrarily far ahead and immediately satisfy it, bypassing the mandatory
		// 72h timelock outright — the same trust class of bug already fixed for voterChainID
		// above. Fail-closed like every other consensus-relevant value in this file: ignore the
		// caller's claim and always use the real, consensus-agreed block time instead. The ABI
		// parameter itself is left in place (removing it would be an ABI-breaking change with no
		// safety benefit); its value is simply never trusted.
		proposedAt := blockTime
		proposalID, err := engine.Governance.Propose(kind, payload, proposedAt)
		if err != nil {
			return nil, nil, err
		}
		// propose() is deliberately permissionless (all_remaining_fixes_plan.md Mục 2: gated
		// only at vote()/quorum, matching common bond-then-vote governance patterns and needed
		// for a new chain to self-nominate via ProposalRegisterChain without an existing chain
		// sponsoring it). Proposals has no TTL/cleanup, so surface its real size as a metric
		// instead of guessing at a rate-limit design with no production data behind it.
		metrics.GovernanceProposalCount.Set(float64(len(engine.Governance.Proposals)))
		packed, packErr := method.Outputs.Pack(proposalID)
		if packErr != nil {
			return nil, nil, packErr
		}
		returnData = packed

	case "vote":
		engine.EnsureGovernance()
		proposalID := mustHash(args[0])
		voterChainID := mustUint64(args[1])
		// Security fix: see the matching comment on "propose" above — args[2] is untrusted
		// caller-supplied input; always use the real block time instead.
		currentTimestamp := blockTime
		signerPubkeyBls := mustBytes(args[3])
		signature := mustBytes(args[4])

		// Security fix: GovernanceEngine.Vote itself trusts whatever voterChainID its caller
		// passes — it was never meant to be called from an unauthenticated public entry point.
		// Require proof that the caller actually speaks for voterChainID: a valid BLS signature
		// from a member of that chain's CURRENT committee (per Root Anchor's own ChainRegistry)
		// over this specific (proposalId, voterChainId) pair. Without this, any caller could cast
		// any registered chain's single governance vote just by naming its ID.
		voterRegistry, exists := engine.ChainRegistry[voterChainID]
		if !exists {
			return nil, nil, fmt.Errorf("vote: %w: chain %d", cross_chain.ErrUnknownSourceChain, voterChainID)
		}
		isMember := false
		for _, v := range voterRegistry.Committee {
			if bytes.Equal(v.PubkeyBLS, signerPubkeyBls) {
				isMember = true
				break
			}
		}
		if !isMember {
			return nil, nil, fmt.Errorf("vote: signer is not a member of chain %d's current committee", voterChainID)
		}
		voteMsg := cross_chain.ComputeGovernanceVoteMessage(proposalID, voterChainID)
		pubKey := mt_common.PubkeyFromBytes(signerPubkeyBls)
		sig := mt_common.SignFromBytes(signature)
		if !bls.VerifySign(pubKey, sig, voteMsg) {
			return nil, nil, cross_chain.ErrInvalidBLSSignature
		}

		status, err := engine.Governance.Vote(proposalID, voterChainID, currentTimestamp)
		if err != nil {
			return nil, nil, err
		}
		packed, packErr := method.Outputs.Pack(uint8(status))
		if packErr != nil {
			return nil, nil, packErr
		}
		returnData = packed

	case "executeProposal":
		engine.EnsureGovernance()
		proposalID := mustHash(args[0])
		// Security fix: see the matching comment on "propose" above — args[1] is untrusted
		// caller-supplied input; always use the real block time instead.
		currentTimestamp := blockTime
		if _, err := engine.ExecuteGovernanceProposal(proposalID, currentTimestamp); err != nil {
			return nil, nil, err
		}
		// C6 observability (note/cross_chain_attack_scenario_catalog.md): a ProposalRegisterChain
		// execution is one possible outcome of this call among several proposal kinds -- setting
		// this unconditionally after every successful execute is cheap and correct either way
		// (a no-op change in registry size for any other proposal kind).
		metrics.RegisteredChainCount.Set(float64(len(engine.ChainRegistry)))

	case "registerAsset":
		engine.EnsureGovernance()
		proposalID := mustHash(args[0])
		totalSupply := mustBigInt(args[1])
		proposal := engine.Governance.GetProposal(proposalID)
		if proposal == nil {
			return nil, nil, cross_chain.ErrProposalNotFound
		}
		if _, err := engine.AssetRegistry.RegisterAssetOnRootAnchor(proposal, totalSupply); err != nil {
			return nil, nil, err
		}

	case "verifyAndExecute":
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
			GasFee:        mustBigInt(args[11]),
			Ordered:       mustBool(args[12]),
		}
		aggregateProof := cross_chain.MerkleProof{
			LeafIndex: mustUint64(args[13]),
			Siblings:  mustHashSlice(args[14]),
		}
		messageProof := cross_chain.MerkleProof{
			LeafIndex: mustUint64(args[15]),
			Siblings:  mustHashSlice(args[16]),
		}
		commitRoot := mustHash(args[17])
		cert := cross_chain.QuorumCert{
			Epoch:              mustUint64(args[18]),
			AggregateSignature: mustBytes(args[19]),
			SignerBitmap:       mustBytes(args[20]),
		}

		status, err := engine.VerifyAndExecute(msg, aggregateProof, cert, messageProof, commitRoot, tx.FromAddress())
		if err != nil {
			return nil, nil, err
		}

		// Task 1.3: Contract Call (only for Native or Pure messages)
		if (msg.AssetID == nil || msg.AssetID.Sign() == 0) && len(msg.Payload) > 0 && isContractCall(chainState, msg.Target) {
			if err := settleGasCappedContractCall(
				ctx, chainState, tx, blockTime, msg.Sender, msg.Target, msg.Payload, msg.GasFee,
			); err != nil {
				return nil, nil, fmt.Errorf("verifyAndExecute payload execution failed: %v", err)
			}
		} else if msg.GasFee != nil && msg.GasFee.Sign() > 0 {
			// No real CONTRACT_CALL happened -- refund the locked gas budget in full.
			if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 0, msg.GasFee, tx.FromAddress(), msg.Sender); err != nil {
				return nil, nil, fmt.Errorf("verifyAndExecute gasFee refund failed: %v", err)
			}
		}

		if msg.AssetID == nil || msg.AssetID.Sign() == 0 {
			// Task 1.1: Real Native Mint / Credit to recipient (msg.Target)
			if msg.Value != nil && msg.Value.Sign() > 0 {
				if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 0, msg.Value, tx.FromAddress(), msg.Target); err != nil {
					return nil, nil, fmt.Errorf("verifyAndExecute native mint failed: %v", err)
				}
			}
		} else {
			// Task 1.2: Custom Asset Mint/Unlock
			if msg.Value != nil && msg.Value.Sign() > 0 {
				if len(msg.Payload) != 20 {
					return nil, nil, fmt.Errorf("invalid payload for custom asset claim (expected 20 bytes recipient address, got %d)", len(msg.Payload))
				}
				recipient := common.BytesToAddress(msg.Payload)

				asset, err := engine.AssetRegistry.GetAsset(msg.AssetID)
				if err != nil {
					return nil, nil, fmt.Errorf("failed to get asset info: %w", err)
				}

				targetContract := msg.Target
				var callData []byte

				if engine.LocalChainID == asset.HomeChainID {
					// Unlock from vault: transfer(recipient, value)
					transferID := crypto.Keccak256Hash([]byte("transfer(address,uint256)")).Bytes()[:4]
					callData = make([]byte, 4+32+32)
					copy(callData[0:4], transferID)
					copy(callData[4:36], common.LeftPadBytes(recipient.Bytes(), 32))
					copy(callData[36:68], common.LeftPadBytes(msg.Value.Bytes(), 32))
				} else {
					// Mint wrapped token: mint(recipient, value)
					mintID := crypto.Keccak256Hash([]byte("mint(address,uint256)")).Bytes()[:4]
					callData = make([]byte, 4+32+32)
					copy(callData[0:4], mintID)
					copy(callData[4:36], common.LeftPadBytes(recipient.Bytes(), 32))
					copy(callData[36:68], common.LeftPadBytes(msg.Value.Bytes(), 32))
				}

				// EXPERIMENTAL FIX (2026-08-25, same finding as the other 3 custom-asset call
				// sites in this file): msg.sender must be the Gateway itself, not
				// tx.FromAddress() — see the outbound() transferFrom fix comment above for the
				// full reasoning.
				if _, err := executeContractCallForGateway(
					ctx, chainState, tx, blockTime, mt_common.GATEWAY_CONTRACT_ADDRESS, targetContract, callData, big.NewInt(0), tx.MaxGas(),
				); err != nil {
					return nil, nil, fmt.Errorf("verifyAndExecute custom asset execution failed: %w", err)
				}
			}
		}

		// Pack failure is intentionally non-fatal here (unlike an ordinary early-return
		// error): the native mint / asset execution above has already mutated real balance
		// via a direct, non-rollback-able AccountStateDB/contract-storage write (barrier TXs
		// have no MVCC/no retry-on-abort — see the comment on the burn ordering in the
		// "outbound" case above). Returning an error at this point would revert the TX's
		// receipt/logs while the value transfer it's supposed to report already happened for
		// real — same failure shape as the bugs fixed elsewhere in this file, just for an
		// event log instead of the transfer itself. Emitting no event on a Pack failure (which
		// requires a corrupt/mismatched ABI, not a normal runtime condition) is safe; silently
		// reverting a delivered transfer is not.
		statusEvent := h.abi.Events["MessageStatusChanged"]
		if eventData, packErr := statusEvent.Inputs.NonIndexed().Pack(uint8(status)); packErr == nil {
			eventLogs = append(eventLogs, smart_contract.NewEventLog(
				tx.Hash(), tx.ToAddress(), eventData,
				[][]byte{statusEvent.ID.Bytes(), msg.MessageID.Bytes()},
			))
		} else {
			logger.Error("verifyAndExecute: pack MessageStatusChanged event failed (transfer already applied, not reverting): %v", packErr)
		}

	case "claimDeadChainBalance":
		deadChainID := mustUint64(args[0])
		account := mustAddress(args[1])
		amount := mustBigInt(args[2])
		proof := cross_chain.MerkleProof{
			LeafIndex: mustUint64(args[3]),
			Siblings:  mustHashSlice(args[4]),
		}
		accountLeafHash := mustHash(args[5])

		if err := engine.ClaimDeadChainBalance(deadChainID, account, amount, proof, accountLeafHash); err != nil {
			return nil, nil, err
		}

		// Task 1.1: Real Native Balance Credit for dead chain recovery
		if amount != nil && amount.Sign() > 0 {
			if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 0, amount, tx.FromAddress(), account); err != nil {
				return nil, nil, fmt.Errorf("claimDeadChainBalance native balance credit failed: %v", err)
			}
		}

	case "withdrawRelayerTip":
		amount, err := engine.WithdrawRelayerTip(tx.FromAddress())
		if err != nil {
			return nil, nil, err
		}
		// Pack the return value BEFORE the real mint below — Pack is a pure function of
		// `amount` (no chain-state dependency), so doing it first means a Pack failure is
		// caught while nothing has been mutated yet, instead of after a real, non-reversible
		// balance credit (barrier TXs have no MVCC/no retry-on-abort; see the "outbound"
		// case's comment above for why an error path is never allowed after a real mutation).
		packed, packErr := method.Outputs.Pack(amount)
		if packErr != nil {
			return nil, nil, packErr
		}
		if amount != nil && amount.Sign() > 0 {
			if err := processNativeMintBurnForGateway(ctx, chainState, tx, blockTime, 0, amount, tx.FromAddress(), tx.FromAddress()); err != nil {
				return nil, nil, fmt.Errorf("withdrawRelayerTip credit failed: %v", err)
			}
		}
		returnData = packed

	default:
		return nil, nil, fmt.Errorf("unhandled gateway write method: %s", method.Name)
	}

	if err := saveGatewayEngine(chainState, engine); err != nil {
		return nil, nil, err
	}
	return eventLogs, returnData, nil
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

	case "getPendingOutboundCount":
		args, err := method.Inputs.Unpack(argData)
		if err != nil {
			return nil, fmt.Errorf("unpack getPendingOutboundCount input: %w", err)
		}
		destChainID := mustUint64(args[0])
		count := len(engine.PendingOutboundMessages[destChainID])
		return method.Outputs.Pack(big.NewInt(int64(count)))

	case "getCommitBatch":
		args, err := method.Inputs.Unpack(argData)
		if err != nil {
			return nil, fmt.Errorf("unpack getCommitBatch input: %w", err)
		}
		commitRoot := mustHash(args[0])
		batch, exists := engine.CommittedBatches[commitRoot]
		if !exists {
			return method.Outputs.Pack(false, uint64(0), []byte{})
		}
		messagesJSON, err := json.Marshal(batch.Messages)
		if err != nil {
			return nil, fmt.Errorf("marshal committed batch messages: %w", err)
		}
		return method.Outputs.Pack(true, batch.Epoch, messagesJSON)

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
				uint64(0), uint64(0), common.Address{}, [32]byte{}, [32]byte{}, "", uint64(0),
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
			[32]byte(registry.StateRoot), [32]byte(registry.AccountTreeRoot), registry.ArchivalEndpoint, registry.RegisteredAt,
		)

	case "getRegisteredPop":
		args, err := method.Inputs.Unpack(argData)
		if err != nil {
			return nil, fmt.Errorf("unpack getRegisteredPop input: %w", err)
		}
		pubkeyBls := mustBytes(args[0])
		pop := engine.RegisteredPops[hex.EncodeToString(pubkeyBls)]
		if pop == nil {
			pop = []byte{}
		}
		return method.Outputs.Pack(pop)

	case "getCommitteeAttestationShares":
		args, err := method.Inputs.Unpack(argData)
		if err != nil {
			return nil, fmt.Errorf("unpack getCommitteeAttestationShares input: %w", err)
		}
		sourceChainID := mustUint64(args[0])
		oldEpoch := mustUint64(args[1])
		payloadHash := mustHash(args[2])

		shares := engine.PendingCommitteeAttestations[committeeAttestationKey(sourceChainID, oldEpoch, payloadHash)]
		pubkeys := make([][]byte, len(shares))
		signatures := make([][]byte, len(shares))
		for i, s := range shares {
			pubkeys[i] = s.SignerPubkeyBLS
			signatures[i] = s.Signature
		}
		return method.Outputs.Pack(pubkeys, signatures)

	case "getCommitAttestationShares":
		args, err := method.Inputs.Unpack(argData)
		if err != nil {
			return nil, fmt.Errorf("unpack getCommitAttestationShares input: %w", err)
		}
		sourceChainID := mustUint64(args[0])
		epoch := mustUint64(args[1])
		commitRoot := mustHash(args[2])

		shares := engine.PendingCommitAttestations[commitAttestationKey(sourceChainID, epoch, commitRoot)]
		pubkeys := make([][]byte, len(shares))
		signatures := make([][]byte, len(shares))
		for i, s := range shares {
			pubkeys[i] = s.SignerPubkeyBLS
			signatures[i] = s.Signature
		}
		return method.Outputs.Pack(pubkeys, signatures)

	case "getProposal":
		args, err := method.Inputs.Unpack(argData)
		if err != nil {
			return nil, fmt.Errorf("unpack getProposal input: %w", err)
		}
		engine.EnsureGovernance()
		proposalID := mustHash(args[0])
		proposal := engine.Governance.GetProposal(proposalID)
		status, exists := engine.Governance.GetStatus(proposalID)
		if !exists || proposal == nil {
			return method.Outputs.Pack(false, uint8(0), []byte{}, uint64(0), uint64(0), uint64(0), false, uint8(0))
		}
		return method.Outputs.Pack(true, uint8(proposal.Kind), proposal.Payload, proposal.VotesFor, proposal.ProposedAt, proposal.EffectiveAt, proposal.Executed, uint8(status))

	case "getAsset":
		args, err := method.Inputs.Unpack(argData)
		if err != nil {
			return nil, fmt.Errorf("unpack getAsset input: %w", err)
		}
		engine.EnsureGovernance()
		assetID := mustBigInt(args[0])
		entry, err := engine.AssetRegistry.GetAsset(assetID)
		if err != nil || entry == nil {
			return method.Outputs.Pack(false, new(big.Int), common.Address{}, false)
		}
		return method.Outputs.Pack(true, new(big.Int).SetUint64(entry.HomeChainID), entry.CanonicalContract, entry.Active)

	default:
		return nil, fmt.Errorf("unhandled gateway view method: %s", method.Name)
	}
}

// committeeAttestationKey identifies one in-progress CommitteeUpdate's share collection —
// GatewayEngine.PendingCommitteeAttestations is keyed by this string (Milestone C).
func committeeAttestationKey(sourceChainID, epoch uint64, payloadHash common.Hash) string {
	return fmt.Sprintf("%d:%d:%s", sourceChainID, epoch, payloadHash.Hex())
}

// commitAttestationKey identifies one in-progress commit root's share collection —
// GatewayEngine.PendingCommitAttestations is keyed by this string (Milestone F).
func commitAttestationKey(sourceChainID, epoch uint64, commitRoot common.Hash) string {
	return fmt.Sprintf("%d:%d:%s", sourceChainID, epoch, commitRoot.Hex())
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

func mustBytesSlice(v interface{}) [][]byte {
	b, _ := v.([][]byte)
	return b
}

func mustUint64Slice(v interface{}) []uint64 {
	u, _ := v.([]uint64)
	return u
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
