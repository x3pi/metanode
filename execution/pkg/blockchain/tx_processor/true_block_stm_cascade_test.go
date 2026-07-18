package tx_processor

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/mvcc"
	mt_state "github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/types"
)

// newDummySTM builds a TrueBlockSTM with n empty tx slots, for tests that
// exercise the bookkeeping methods (registerReaders/cascadeInvalidate/
// cleanupStaleWrites) directly without going through Process()/execOne.
func newDummySTM(n int) *TrueBlockSTM {
	return NewTrueBlockSTM(make([]types.Transaction, n))
}

// ─── registerReaders ───

func TestRegisterReaders_TracksMultipleIndices(t *testing.T) {
	stm := newDummySTM(3)
	addr := common.Address{0xD4}

	stm.registerReaders(0, map[common.Address]mvcc.ReadVersion{addr: {}}, nil)
	stm.registerReaders(2, map[common.Address]mvcc.ReadVersion{addr: {}}, nil)

	stm.addrReadersMu.Lock()
	readers := stm.addrReaders[addr]
	stm.addrReadersMu.Unlock()

	if len(readers) != 2 {
		t.Fatalf("expected 2 registered readers, got %d", len(readers))
	}
	if _, ok := readers[0]; !ok {
		t.Error("expected index 0 registered as reader")
	}
	if _, ok := readers[2]; !ok {
		t.Error("expected index 2 registered as reader")
	}
}

func TestRegisterReaders_StorageKeys(t *testing.T) {
	stm := newDummySTM(2)
	sKey := "0xabc" + "slot1"

	stm.registerReaders(1, nil, map[string]mvcc.ReadVersion{sKey: {}})

	stm.scReadersMu.Lock()
	readers := stm.scReaders[sKey]
	stm.scReadersMu.Unlock()

	if _, ok := readers[1]; !ok {
		t.Fatalf("expected index 1 registered as reader of %q", sKey)
	}
}

// ─── cascadeInvalidate: targeted invalidation (B1) ───

func TestCascadeInvalidate_OnlyTargetsRegisteredReadersAfterWriter(t *testing.T) {
	stm := newDummySTM(6)
	addrX := common.Address{0xAA}
	addrOther := common.Address{0xBB}

	// tx0 and tx2 and tx4 all read addrX. tx0 sits BEFORE the writer (index 1)
	// and must never be touched by the cascade regardless of registration —
	// only readers with index > writer index matter.
	stm.registerReaders(0, map[common.Address]mvcc.ReadVersion{addrX: {}}, nil)
	stm.registerReaders(2, map[common.Address]mvcc.ReadVersion{addrX: {}}, nil)
	stm.registerReaders(4, map[common.Address]mvcc.ReadVersion{addrX: {}}, nil)
	// tx5 reads a DIFFERENT address — a write to addrX must never touch it.
	stm.registerReaders(5, map[common.Address]mvcc.ReadVersion{addrOther: {}}, nil)

	// Seed states: {0,2,3,4,5} start Validated(3); index1 is the writer itself
	// and is handled separately by the caller (execOne), not by cascade.
	for _, i := range []int{0, 2, 3, 4, 5} {
		atomic.StoreUint64(&stm.txState[i], packState(0, 3))
	}

	validateCh := make(chan uint32, 10)
	doneCh := make(chan struct{})
	var activeTasks int32

	stm.cascadeInvalidate(context.Background(), 1, map[common.Address]bool{addrX: true}, nil, validateCh, &activeTasks, doneCh)

	close(validateCh)
	pushed := map[uint32]bool{}
	for idx := range validateCh {
		pushed[idx] = true
	}

	want := map[uint32]bool{2: true, 4: true}
	if len(pushed) != len(want) {
		t.Fatalf("expected pushes %v, got %v", want, pushed)
	}
	for idx := range want {
		if !pushed[idx] {
			t.Errorf("expected index %d to be pushed to validateCh", idx)
		}
	}

	for _, i := range []int{2, 4} {
		inc, st := unpackState(atomic.LoadUint64(&stm.txState[i]))
		if st != 1 || inc != 1 {
			t.Errorf("index %d: expected downgraded state (inc=1,status=1), got (inc=%d,status=%d)", i, inc, st)
		}
	}
	// index 0 (before writer), 3 (never registered as a reader), 5 (different
	// address) must be untouched.
	for _, i := range []int{0, 3, 5} {
		inc, st := unpackState(atomic.LoadUint64(&stm.txState[i]))
		if st != 3 || inc != 0 {
			t.Errorf("index %d: expected untouched state (inc=0,status=3), got (inc=%d,status=%d)", i, inc, st)
		}
	}

	if activeTasks != int32(len(want)) {
		t.Errorf("activeTasks = %d, want %d", activeTasks, len(want))
	}
}

func TestCascadeInvalidate_NoRegisteredReadersIsNoop(t *testing.T) {
	stm := newDummySTM(3)
	atomic.StoreUint64(&stm.txState[2], packState(0, 3))

	validateCh := make(chan uint32, 10)
	doneCh := make(chan struct{})
	var activeTasks int32

	stm.cascadeInvalidate(context.Background(), 1, map[common.Address]bool{{0xEE}: true}, nil, validateCh, &activeTasks, doneCh)

	select {
	case idx := <-validateCh:
		t.Fatalf("expected no pushes, got index %d", idx)
	default:
	}
	if activeTasks != 0 {
		t.Errorf("activeTasks = %d, want 0", activeTasks)
	}
}

// ─── cleanupStaleWrites: diff-based cleanup after re-execution (B3) ───

func TestCleanupStaleWrites_OnlyRemovesOrphanedAccountKeys(t *testing.T) {
	stm := newDummySTM(2)
	addrA := common.Address{0xA1}
	addrB := common.Address{0xB2}

	stm.accountMap.Write(addrA, mvcc.Version(0), mt_state.NewAccountState(addrA))
	stm.accountMap.Write(addrB, mvcc.Version(0), mt_state.NewAccountState(addrB))

	_, _, widABefore, _ := stm.accountMap.Read(addrA, 1)

	oldWriteSet := map[common.Address]bool{addrA: true, addrB: true}
	newWriteSet := map[common.Address]bool{addrA: true} // B no longer written by the new incarnation

	stm.cleanupStaleWrites(0, oldWriteSet, newWriteSet, nil, nil)

	// A: untouched by cleanup — same WriteID as before proves it wasn't
	// deleted-then-rewritten (which would otherwise spuriously invalidate any
	// reader who already validated against this exact value).
	_, verA, widAAfter, _ := stm.accountMap.Read(addrA, 1)
	if verA != 0 || widAAfter != widABefore {
		t.Errorf("addrA should be untouched: verA=%d widBefore=%d widAfter=%d", verA, widABefore, widAAfter)
	}

	// B: orphaned by the new incarnation — must be cleaned up.
	stateB, verB, _, _ := stm.accountMap.Read(addrB, 1)
	if stateB != nil || verB != mvcc.BaseVersion {
		t.Errorf("addrB should have been cleaned up, got state=%v ver=%d", stateB, verB)
	}
}

func TestCleanupStaleWrites_OnlyRemovesOrphanedStorageKeys(t *testing.T) {
	stm := newDummySTM(2)
	addr := common.Address{0xC3}
	stm.storageMap.Write(addr, "slot1", mvcc.Version(0), []byte("v1"))
	stm.storageMap.Write(addr, "slot2", mvcc.Version(0), []byte("v2"))

	sKey1 := addr.Hex() + "slot1"
	sKey2 := addr.Hex() + "slot2"

	oldSet := map[string]bool{sKey1: true, sKey2: true}
	newSet := map[string]bool{sKey1: true}

	stm.cleanupStaleWrites(0, nil, nil, oldSet, newSet)

	if val, ver, _, _ := stm.storageMap.Read(addr, "slot1", 1); ver != 0 || string(val) != "v1" {
		t.Errorf("slot1 should be untouched, got val=%s ver=%d", val, ver)
	}
	if val, ver, _, _ := stm.storageMap.Read(addr, "slot2", 1); val != nil || ver != mvcc.BaseVersion {
		t.Errorf("slot2 should have been cleaned up, got val=%v ver=%d", val, ver)
	}
}

func TestCleanupStaleWrites_EmptyOldWriteSetIsNoop(t *testing.T) {
	stm := newDummySTM(1)
	addr := common.Address{0xF1}
	stm.accountMap.Write(addr, mvcc.Version(0), mt_state.NewAccountState(addr))

	// Nothing to clean up if there was no previous incarnation.
	stm.cleanupStaleWrites(0, nil, map[common.Address]bool{addr: true}, nil, nil)

	if state, ver, _, _ := stm.accountMap.Read(addr, 1); state == nil || ver != 0 {
		t.Errorf("expected addr to remain, got state=%v ver=%d", state, ver)
	}
}
