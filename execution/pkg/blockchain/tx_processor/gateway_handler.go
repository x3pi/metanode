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
	// Milestone C fields: nil after unmarshaling a blob written before they existed.
	if engine.PendingCommitteeAttestations == nil {
		engine.PendingCommitteeAttestations = make(map[string][]cross_chain.CommitteeAttestationShare)
	}
	if engine.RegisteredPops == nil {
		engine.RegisteredPops = make(map[string][]byte)
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
	case "outbound", "attestCommit", "claimMessage", "refund",
		"registerCommitteePop", "submitCommitteeAttestation", "committeeUpdate":
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

	case "committeeUpdate":
		sourceChainID := mustUint64(args[0])
		newEpoch := mustUint64(args[1])
		newCommitteePubkeys := mustBytesSlice(args[2])
		newCommitteeStakes := mustUint64Slice(args[3])
		newCommitteePopSignatures := mustBytesSlice(args[4])
		quorumThreshold := mustUint64(args[5])
		stateRoot := mustHash(args[6])
		payloadHash := mustHash(args[7])
		aggPubkeys := mustBytesSlice(args[8])
		aggSignature := mustBytes(args[9])

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
		// could change newCommittee/stateRoot/newEpoch after signatures were already collected
		// over a different payloadHash.
		expectedDigest := cross_chain.ComputeCommitteeUpdateDigest(sourceChainID, newEpoch, newCommittee, stateRoot)
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

	default:
		return nil, fmt.Errorf("unhandled gateway view method: %s", method.Name)
	}
}

// committeeAttestationKey identifies one in-progress CommitteeUpdate's share collection —
// GatewayEngine.PendingCommitteeAttestations is keyed by this string (Milestone C).
func committeeAttestationKey(sourceChainID, epoch uint64, payloadHash common.Hash) string {
	return fmt.Sprintf("%d:%d:%s", sourceChainID, epoch, payloadHash.Hex())
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
