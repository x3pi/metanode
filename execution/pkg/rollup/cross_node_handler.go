package rollup

import (
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

type AccountStateDB interface {
	GetBalance(addr common.Address) *big.Int
	SubBalance(addr common.Address, amount *big.Int)
	GetNonce(addr common.Address) uint64
	SetNonce(addr common.Address, nonce uint64)
}

type CrossNodeHandler struct {
	store   Store
	stateDB AccountStateDB
	fromKey cm.PublicKey

	// Using a simple lock for serialization. In true BlockSTM it would be handled differently.
	mu sync.Mutex
}

func NewCrossNodeHandler(store Store, stateDB AccountStateDB, fromKey cm.PublicKey) *CrossNodeHandler {
	return &CrossNodeHandler{
		store:   store,
		stateDB: stateDB,
		fromKey: fromKey,
	}
}

// ComputeMessageID calculates the deterministic ID of a cross-node message according to Parent Chain rules.
func ComputeMessageID(fromKey, toKey cm.PublicKey, sender, target common.Address, value *big.Int, payloadHash common.Hash, nonce uint64) common.Hash {
	digest := parentchain.ComputeTransferFloatMessage(fromKey, toKey, sender, target, value, payloadHash, nonce)
	return crypto.Keccak256Hash(digest)
}

// HandleTransfer processes a cross-node transfer request from a user.
func (h *CrossNodeHandler) HandleTransfer(
	toKey cm.PublicKey,
	sender common.Address,
	target common.Address,
	value *big.Int,
	payloadHash common.Hash,
) (common.Hash, error) {
	if value == nil || value.Sign() <= 0 {
		return common.Hash{}, fmt.Errorf("invalid value")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// 1. Check balance
	balance := h.stateDB.GetBalance(sender)
	if balance.Cmp(value) < 0 {
		return common.Hash{}, fmt.Errorf("insufficient balance: have %v, need %v", balance, value)
	}

	// 2. Generate source sequence
	seq, err := h.store.GetNextFloatSeq()
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to get next float seq: %w", err)
	}

	// 3. Compute deterministic MessageID
	msgID := ComputeMessageID(h.fromKey, toKey, sender, target, value, payloadHash, seq)

	// 4. Run State Machine Transition
	event := Event{
		Type:   EventTxSubmitted,
		Role:   RoleSender,
		Sender: sender,
		Target: target,
		Value:  value,
	}

	newState, actions, err := Next(StateNone, RoleSender, event)
	if err != nil {
		return common.Hash{}, fmt.Errorf("state transition failed: %w", err)
	}

	// 5. Apply Actions Atomically (In Memory / DB)
	for _, action := range actions {
		if action.Type == ActionDeductBalance {
			h.stateDB.SubBalance(action.Target, action.Amount)
		}
	}

	if err := h.store.IncrementFloatSeq(); err != nil {
		return common.Hash{}, fmt.Errorf("failed to increment float seq: %w", err)
	}
	h.stateDB.SetNonce(sender, h.stateDB.GetNonce(sender)+1)

	// 6. Create and Save Record
	record := &MessageRecord{
		MessageID:   msgID,
		Role:        RoleSender,
		State:       newState,
		Sender:      sender,
		Target:      target,
		Value:       value,
		SourceSeq:   seq,
		SourcePubKey: h.fromKey,
		DestPubKey:   toKey,
		PayloadHash: payloadHash,
	}

	if err := h.store.Put(record); err != nil {
		return common.Hash{}, fmt.Errorf("failed to save rollup record: %w", err)
	}

	return msgID, nil
}
