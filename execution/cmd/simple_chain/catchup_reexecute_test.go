package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// mockNomtCatchupPayload tracks method calls for verifying two-phase commit & discard logic
type mockNomtCatchupPayload struct {
	discardCount        atomic.Int32
	commitAsyncCount    atomic.Int32
	writeChangelogCount atomic.Int32
	setBlockNumCount    atomic.Int32
	lastBlockNum        atomic.Uint64
	writeChangelogErr   error
	waitErr             error
}

func (m *mockNomtCatchupPayload) Discard() {
	m.discardCount.Add(1)
}

func (m *mockNomtCatchupPayload) SetBlockNumber(bn uint64) {
	m.setBlockNumCount.Add(1)
	m.lastBlockNum.Store(bn)
}

func (m *mockNomtCatchupPayload) WriteChangelog() error {
	m.writeChangelogCount.Add(1)
	return m.writeChangelogErr
}

func (m *mockNomtCatchupPayload) CommitAsync() {
	m.commitAsyncCount.Add(1)
}

func (m *mockNomtCatchupPayload) Wait() error {
	return m.waitErr
}

func TestCatchupReexecution_AccountRootMismatch_TriggersDiscard(t *testing.T) {
	sc := &mockNomtCatchupPayload{}
	acct := &mockNomtCatchupPayload{}
	stake := &mockNomtCatchupPayload{}

	expectedAcctRoot := e_common.HexToHash("0x1111")
	actualAcctRoot := e_common.HexToHash("0x2222") // mismatch!
	stakeRoot := e_common.HexToHash("0x3333")

	err := commitVerifiedCatchupBlock(
		2,
		expectedAcctRoot,
		stakeRoot,
		sc,
		acct,
		actualAcctRoot,
		stake,
		stakeRoot,
		nil,
		nil,
		nil,
		nil,
	)

	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "account root mismatch"))

	// All payloads must be discarded
	assert.Equal(t, int32(1), sc.discardCount.Load(), "SC payload must be discarded")
	assert.Equal(t, int32(1), acct.discardCount.Load(), "Account payload must be discarded")
	assert.Equal(t, int32(1), stake.discardCount.Load(), "Stake payload must be discarded")

	// No async commit must have been called
	assert.Equal(t, int32(0), sc.commitAsyncCount.Load())
	assert.Equal(t, int32(0), acct.commitAsyncCount.Load())
	assert.Equal(t, int32(0), stake.commitAsyncCount.Load())
}

func TestCatchupReexecution_StakeRootMismatch_TriggersDiscard(t *testing.T) {
	sc := &mockNomtCatchupPayload{}
	acct := &mockNomtCatchupPayload{}
	stake := &mockNomtCatchupPayload{}

	acctRoot := e_common.HexToHash("0x1111")
	expectedStakeRoot := e_common.HexToHash("0x3333")
	actualStakeRoot := e_common.HexToHash("0x4444") // mismatch!

	err := commitVerifiedCatchupBlock(
		2,
		acctRoot,
		expectedStakeRoot,
		sc,
		acct,
		acctRoot,
		stake,
		actualStakeRoot,
		nil,
		nil,
		nil,
		nil,
	)

	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "stake root mismatch"))

	// All payloads must be discarded
	assert.Equal(t, int32(1), sc.discardCount.Load())
	assert.Equal(t, int32(1), acct.discardCount.Load())
	assert.Equal(t, int32(1), stake.discardCount.Load())

	// No async commit must have been called
	assert.Equal(t, int32(0), sc.commitAsyncCount.Load())
	assert.Equal(t, int32(0), acct.commitAsyncCount.Load())
	assert.Equal(t, int32(0), stake.commitAsyncCount.Load())
}

func TestCatchupReexecution_RootsMatch_PersistsAndWaits(t *testing.T) {
	sc := &mockNomtCatchupPayload{}
	acct := &mockNomtCatchupPayload{}
	stake := &mockNomtCatchupPayload{}

	acctRoot := e_common.HexToHash("0x1111")
	stakeRoot := e_common.HexToHash("0x2222")
	const testBlockNum = uint64(5)

	var persistedAccount bool
	var persistedStake bool

	accountPersist := func() error {
		persistedAccount = true
		return nil
	}
	stakePersist := func() error {
		persistedStake = true
		return nil
	}

	err := commitVerifiedCatchupBlock(
		testBlockNum,
		acctRoot,
		stakeRoot,
		sc,
		acct,
		acctRoot,
		stake,
		stakeRoot,
		accountPersist,
		stakePersist,
		nil,
		nil,
	)

	require.NoError(t, err)

	// Discard must NOT be called
	assert.Equal(t, int32(0), sc.discardCount.Load())
	assert.Equal(t, int32(0), acct.discardCount.Load())
	assert.Equal(t, int32(0), stake.discardCount.Load())

	// SetBlockNumber must be called with testBlockNum
	assert.Equal(t, testBlockNum, sc.lastBlockNum.Load())
	assert.Equal(t, testBlockNum, acct.lastBlockNum.Load())
	assert.Equal(t, testBlockNum, stake.lastBlockNum.Load())

	// WriteChangelog and CommitAsync must be called for all payloads
	assert.Equal(t, int32(1), sc.writeChangelogCount.Load())
	assert.Equal(t, int32(1), acct.writeChangelogCount.Load())
	assert.Equal(t, int32(1), stake.writeChangelogCount.Load())

	assert.Equal(t, int32(1), sc.commitAsyncCount.Load())
	assert.Equal(t, int32(1), acct.commitAsyncCount.Load())
	assert.Equal(t, int32(1), stake.commitAsyncCount.Load())

	// Database persist closures must be called
	assert.True(t, persistedAccount)
	assert.True(t, persistedStake)

	// Storage tracking must be updated
	assert.Equal(t, testBlockNum, storage.GetLastNomtCommittedBlock())
}

func TestCatchupReexecution_WriteChangelogError_TriggersDiscard(t *testing.T) {
	sc := &mockNomtCatchupPayload{
		writeChangelogErr: errors.New("simulated sc changelog write error"),
	}
	acct := &mockNomtCatchupPayload{}
	stake := &mockNomtCatchupPayload{}

	acctRoot := e_common.HexToHash("0x1111")
	stakeRoot := e_common.HexToHash("0x2222")

	err := commitVerifiedCatchupBlock(
		10,
		acctRoot,
		stakeRoot,
		sc,
		acct,
		acctRoot,
		stake,
		stakeRoot,
		nil,
		nil,
		nil,
		nil,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to write SC changelog")

	// Fail-closed: Discard must be called on all payloads even though roots matched
	assert.Equal(t, int32(1), sc.discardCount.Load())
	assert.Equal(t, int32(1), acct.discardCount.Load())
	assert.Equal(t, int32(1), stake.discardCount.Load())
}

func TestCatchupReexecution_PersistAsyncError_TriggersDiscard(t *testing.T) {
	sc := &mockNomtCatchupPayload{}
	acct := &mockNomtCatchupPayload{}
	stake := &mockNomtCatchupPayload{}

	acctRoot := e_common.HexToHash("0x1111")
	stakeRoot := e_common.HexToHash("0x2222")

	accountPersist := func() error {
		return errors.New("simulated account persist error")
	}

	err := commitVerifiedCatchupBlock(
		11,
		acctRoot,
		stakeRoot,
		sc,
		acct,
		acctRoot,
		stake,
		stakeRoot,
		accountPersist,
		nil,
		nil,
		nil,
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to persist AccountStateDB")

	// Fail-closed: Discard must be called on all payloads
	assert.Equal(t, int32(1), sc.discardCount.Load())
	assert.Equal(t, int32(1), acct.discardCount.Load())
	assert.Equal(t, int32(1), stake.discardCount.Load())
}
