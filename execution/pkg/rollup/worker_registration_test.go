package rollup

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/stretchr/testify/assert"
)

type mockRegistrationParentClient struct {
	mockParentChainClient
	regMu  sync.Mutex
	events []*parentchain.AccountRegisteredEvent
}

func (m *mockRegistrationParentClient) GetInboundAccountRegistrations(pubKey cm.PublicKey, cursor uint64) ([]*parentchain.AccountRegisteredEvent, uint64, error) {
	m.regMu.Lock()
	defer m.regMu.Unlock()

	if cursor >= uint64(len(m.events)) {
		return nil, cursor, nil
	}
	res := m.events[cursor:]
	return res, uint64(len(m.events)), nil
}

func TestRegistrationWorker_W1_Unit(t *testing.T) {
	var clusterKey cm.PublicKey
	copy(clusterKey[:], []byte("cluster_pub_key_32_bytes_long!!"))

	t.Run("W1_CursorOnlyAdvancesWhenConfirmedInStateDB", func(t *testing.T) {
		db := newMockRegistryStateDB()
		client := &mockRegistrationParentClient{}

		userA := common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
		userB := common.HexToAddress("0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
		userC := common.HexToAddress("0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC")

		client.events = []*parentchain.AccountRegisteredEvent{
			{Seq: 0, UserAddress: userA, ClusterKey: clusterKey, ParentBlock: 100},
			{Seq: 1, UserAddress: userB, ClusterKey: clusterKey, ParentBlock: 101},
			{Seq: 2, UserAddress: userC, ClusterKey: clusterKey, ParentBlock: 102},
		}

		worker := NewRegistrationWorker(db, client, nil, clusterKey)

		var proposedUsers []common.Address
		worker.EventProposer = func(payload []byte) error {
			var p AccountRegistrationPayload
			if err := p.Unmarshal(payload); err == nil {
				proposedUsers = append(proposedUsers, p.User)
			}
			return nil
		}

		// Initial poll: none are registered in stateDB yet
		worker.PollAndProcess()
		assert.Equal(t, uint64(0), worker.Cursor(), "Cursor must NOT advance if stateDB does not have them registered")
		assert.Equal(t, 3, len(proposedUsers), "All 3 must have been proposed")

		// Now UserA is registered in stateDB, but UserB is NOT yet
		db.SetParentRegistered(userA, true)
		proposedUsers = nil
		worker.PollAndProcess()
		assert.Equal(t, uint64(1), worker.Cursor(), "Cursor must advance past UserA (to 1), but STOP before UserB")
		assert.Equal(t, 2, len(proposedUsers), "Only UserB and UserC should be re-proposed")

		// Now UserB is registered in stateDB
		db.SetParentRegistered(userB, true)
		proposedUsers = nil
		worker.PollAndProcess()
		assert.Equal(t, uint64(2), worker.Cursor(), "Cursor must advance past UserB (to 2)")
		assert.Equal(t, 1, len(proposedUsers), "Only UserC should be re-proposed")

		// Now UserC is registered in stateDB
		db.SetParentRegistered(userC, true)
		proposedUsers = nil
		worker.PollAndProcess()
		assert.Equal(t, uint64(3), worker.Cursor(), "Cursor must advance past UserC (to 3)")
		assert.Equal(t, 0, len(proposedUsers), "None should be proposed since all are registered")
	})

	t.Run("W1_BoundedBatch10kEvents", func(t *testing.T) {
		db := newMockRegistryStateDB()
		client := &mockRegistrationParentClient{}

		// Populate 10,000 events
		const totalEvents = 10000
		for i := 0; i < totalEvents; i++ {
			addr := common.HexToAddress(fmt.Sprintf("0x%040x", i+1))
			client.events = append(client.events, &parentchain.AccountRegisteredEvent{
				Seq:         uint64(i),
				UserAddress: addr,
				ClusterKey:  clusterKey,
				ParentBlock: uint64(1000 + i),
			})
		}

		worker := NewRegistrationWorker(db, client, nil, clusterKey)
		proposedCount := 0
		worker.EventProposer = func(payload []byte) error {
			proposedCount++
			return nil
		}

		// First poll cycle should process at most maxRegistrationBatchSize (256)
		worker.PollAndProcess()
		assert.LessOrEqual(t, proposedCount, maxRegistrationBatchSize, "Must bound batch to maxRegistrationBatchSize")
		assert.Equal(t, maxRegistrationBatchSize, proposedCount)
	})

	t.Run("W1_NoGoroutineLeak", func(t *testing.T) {
		db := newMockRegistryStateDB()
		client := &mockRegistrationParentClient{}

		// Let GC settle baseline goroutine count
		runtime.GC()
		baselineGoroutines := runtime.NumGoroutine()

		for i := 0; i < 20; i++ {
			w := NewRegistrationWorker(db, client, nil, clusterKey)
			w.SetInterval(10 * time.Millisecond)
			w.Start()
			w.WakeUp()
			time.Sleep(5 * time.Millisecond)
			w.Stop()
		}

		runtime.GC()
		currentGoroutines := runtime.NumGoroutine()
		assert.InDelta(t, baselineGoroutines, currentGoroutines, 3, "Goroutine count must not leak after workers stop")
	})
}
