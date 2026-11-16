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
		return nil, uint64(len(m.events)), nil
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

	t.Run("W1_SelfHealing_ParentReset", func(t *testing.T) {
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
		worker.shrinkConfirmPolls = 3 // keep the persistent-shrink confirmation short in this test
		worker.SetAlertSink(func(string) {})
		var proposedUsers []common.Address
		worker.EventProposer = func(payload []byte) error {
			var p AccountRegistrationPayload
			if err := p.Unmarshal(payload); err == nil {
				proposedUsers = append(proposedUsers, p.User)
			}
			return nil
		}

		// Sync initial 3 events
		db.SetParentRegistered(userA, true)
		db.SetParentRegistered(userB, true)
		db.SetParentRegistered(userC, true)

		worker.PollAndProcess()
		assert.Equal(t, uint64(3), worker.Cursor(), "Cursor must advance to 3")

		// Case 1: Parent chain resets/re-creates with fewer events (total = 1 event for UserD at Seq 0)
		userD := common.HexToAddress("0xDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD")
		client.regMu.Lock()
		client.events = []*parentchain.AccountRegisteredEvent{
			{Seq: 0, UserAddress: userD, ClusterKey: clusterKey, ParentBlock: 200},
		}
		client.regMu.Unlock()

		// Worker is currently at cursor=3. Parent total is 1. A shorter log must persist for shrinkConfirmPolls
		// consecutive polls before it is treated as a reset (a lagging parent catches up before that).
		for i := 1; i < worker.shrinkConfirmPolls; i++ {
			assert.False(t, worker.PollAndProcess())
			assert.Equal(t, uint64(3), worker.Cursor(), "no reset before the shortfall is confirmed (poll %d)", i)
		}
		hasProgress := worker.PollAndProcess()
		assert.True(t, hasProgress, "Self-healing must return true to trigger immediate re-poll")
		assert.Equal(t, uint64(0), worker.Cursor(), "Worker cursor must be reset to 0 (not 1) so it doesn't skip Seq 0")

		// Next poll: queries from cursor=0 and receives UserD (Seq 0)
		proposedUsers = nil
		worker.PollAndProcess()
		assert.Equal(t, 1, len(proposedUsers), "UserD must be proposed")
		assert.Equal(t, userD, proposedUsers[0])
		assert.Equal(t, uint64(0), worker.Cursor(), "Cursor remains 0 until UserD is registered in stateDB")

		// After UserD is confirmed in stateDB, cursor advances to 1
		db.SetParentRegistered(userD, true)
		worker.PollAndProcess()
		assert.Equal(t, uint64(1), worker.Cursor(), "Cursor must advance to 1 after confirmation")

		// Case 2: Parent chain completely wiped (total = 0 events)
		client.regMu.Lock()
		client.events = nil
		client.regMu.Unlock()

		// Worker is at cursor=1. Parent total is 0.
		for i := 1; i < worker.shrinkConfirmPolls; i++ {
			worker.PollAndProcess()
			assert.Equal(t, uint64(1), worker.Cursor())
		}
		hasProgress = worker.PollAndProcess()
		assert.True(t, hasProgress, "Self-healing on total wipe must return true")
		assert.Equal(t, uint64(0), worker.Cursor(), "Worker cursor must reset to 0 on total wipe")

		// Subsequent poll with cursor=0 and total=0: normal idle state (nextCursor=0 == cursor=0)
		hasProgress = worker.PollAndProcess()
		assert.False(t, hasProgress, "Normal idle poll must return false")
		assert.Equal(t, uint64(0), worker.Cursor(), "Cursor remains 0")

		// Case 3: Parent chain wipes and re-creates with MORE events than worker's cursor (total > cursor).
		// Without fingerprint check on cursor-1, worker would poll from cursor=3 and skip Seq 0..2.
		var alertMessages []string
		worker.SetAlertSink(func(msg string) {
			alertMessages = append(alertMessages, msg)
		})

		// Setup initial state: 3 users synced (Seq 0: UserA, Seq 1: UserB, Seq 2: UserC)
		db.SetParentRegistered(userA, true)
		db.SetParentRegistered(userB, true)
		db.SetParentRegistered(userC, true)
		client.regMu.Lock()
		client.events = []*parentchain.AccountRegisteredEvent{
			{Seq: 0, UserAddress: userA, ClusterKey: clusterKey, ParentBlock: 300},
			{Seq: 1, UserAddress: userB, ClusterKey: clusterKey, ParentBlock: 301},
			{Seq: 2, UserAddress: userC, ClusterKey: clusterKey, ParentBlock: 302},
		}
		client.regMu.Unlock()

		worker.PollAndProcess()
		assert.Equal(t, uint64(3), worker.Cursor(), "Worker cursor must be 3")
		lastUser, lastSeq, hasLast := worker.LastKnownFingerprint()
		assert.True(t, hasLast)
		assert.Equal(t, userC, lastUser)
		assert.Equal(t, uint64(2), lastSeq)

		// Idle poll with matching fingerprint returns false and does not reset
		alertMessages = nil
		hasProgress = worker.PollAndProcess()
		assert.False(t, hasProgress, "Idle poll with matching fingerprint must return false")
		assert.Equal(t, uint64(3), worker.Cursor(), "Cursor remains 3")
		assert.Empty(t, alertMessages, "No alert on normal idle poll")

		// Parent chain wiped and immediately created with 5 NEW events (Seq 0..4) with DIFFERENT users!
		userN0 := common.HexToAddress("0x1000000000000000000000000000000000000000")
		userN1 := common.HexToAddress("0x2000000000000000000000000000000000000000")
		userN2 := common.HexToAddress("0x3000000000000000000000000000000000000000")
		userN3 := common.HexToAddress("0x4000000000000000000000000000000000000000")
		userN4 := common.HexToAddress("0x5000000000000000000000000000000000000000")

		client.regMu.Lock()
		client.events = []*parentchain.AccountRegisteredEvent{
			{Seq: 0, UserAddress: userN0, ClusterKey: clusterKey, ParentBlock: 400},
			{Seq: 1, UserAddress: userN1, ClusterKey: clusterKey, ParentBlock: 401},
			{Seq: 2, UserAddress: userN2, ClusterKey: clusterKey, ParentBlock: 402}, // Seq 2 is userN2 != userC!
			{Seq: 3, UserAddress: userN3, ClusterKey: clusterKey, ParentBlock: 403},
			{Seq: 4, UserAddress: userN4, ClusterKey: clusterKey, ParentBlock: 404},
		}
		client.regMu.Unlock()

		// Worker is at cursor=3. Parent total is 5 > 3.
		// pollAndProcess queries at cursor-1=2, compares events[0] (userN2) with lastKnown (userC).
		// Mismatch detected -> resets cursor to 0, triggers Telegram alert, and returns true.
		hasProgress = worker.PollAndProcess()
		assert.True(t, hasProgress, "Self-healing must trigger when history mismatch is detected")
		assert.Equal(t, uint64(0), worker.Cursor(), "Worker cursor must reset to 0 despite parent total (5) > cursor (3)")
		assert.NotEmpty(t, alertMessages, "Telegram alert must be fired on history mismatch")
		assert.Contains(t, alertMessages[0], "history mismatch")

		// Next poll queries from cursor=0 and processes all new users from Seq 0!
		proposedUsers = nil
		worker.PollAndProcess()
		assert.Equal(t, 5, len(proposedUsers), "All 5 new users from Seq 0 must be proposed without skipping")
		assert.Equal(t, userN0, proposedUsers[0])
		assert.Equal(t, userN1, proposedUsers[1])
		assert.Equal(t, userN2, proposedUsers[2])
		assert.Equal(t, userN3, proposedUsers[3])
		assert.Equal(t, userN4, proposedUsers[4])
	})

	// A parent quorum that is restarting/replaying briefly serves a SHORTER prefix of the same log. That must not reset
	// the cursor or raise an alert; once it catches up, the fingerprint matches and new events are processed normally.
	t.Run("W1_LaggingParent_NoFalseResetOrAlert", func(t *testing.T) {
		db := newMockRegistryStateDB()
		client := &mockRegistrationParentClient{}
		userA := common.HexToAddress("0xA1")
		userB := common.HexToAddress("0xB1")
		userC := common.HexToAddress("0xC1")
		userD := common.HexToAddress("0xD1")
		full := []*parentchain.AccountRegisteredEvent{
			{Seq: 0, UserAddress: userA, ClusterKey: clusterKey},
			{Seq: 1, UserAddress: userB, ClusterKey: clusterKey},
			{Seq: 2, UserAddress: userC, ClusterKey: clusterKey},
		}
		client.events = full
		for _, u := range []common.Address{userA, userB, userC} {
			db.SetParentRegistered(u, true)
		}

		worker := NewRegistrationWorker(db, client, nil, clusterKey)
		var alerts []string
		worker.SetAlertSink(func(m string) { alerts = append(alerts, m) })
		var proposed []common.Address
		worker.EventProposer = func(payload []byte) error {
			var p AccountRegistrationPayload
			if err := p.Unmarshal(payload); err == nil {
				proposed = append(proposed, p.User)
			}
			return nil
		}
		worker.PollAndProcess()
		assert.Equal(t, uint64(3), worker.Cursor())

		// Parent nodes restart and replay: for a while they only have the first event.
		client.regMu.Lock()
		client.events = full[:1]
		client.regMu.Unlock()
		for i := 0; i < registrationShrinkConfirmPolls-1; i++ {
			assert.False(t, worker.PollAndProcess())
		}
		assert.Equal(t, uint64(3), worker.Cursor(), "a lagging parent must not reset the cursor")
		assert.Empty(t, alerts, "a lagging parent must not raise an alert")

		// Caught up, plus one new registration.
		client.regMu.Lock()
		client.events = append(append([]*parentchain.AccountRegisteredEvent(nil), full...),
			&parentchain.AccountRegisteredEvent{Seq: 3, UserAddress: userD, ClusterKey: clusterKey})
		client.regMu.Unlock()
		worker.PollAndProcess()
		assert.Equal(t, []common.Address{userD}, proposed, "only the new event is proposed")
		assert.Equal(t, uint64(3), worker.Cursor(), "cursor waits for userD's on-chain confirmation")
		assert.Empty(t, alerts)

		// The streak was reset by the caught-up poll: another short episode starts counting from zero.
		client.regMu.Lock()
		client.events = full[:1]
		client.regMu.Unlock()
		for i := 0; i < registrationShrinkConfirmPolls-1; i++ {
			worker.PollAndProcess()
		}
		assert.Equal(t, uint64(3), worker.Cursor())
		assert.Empty(t, alerts)
	})
}
