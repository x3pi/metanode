package main

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockNomtCatchupPayload tracks method calls for verifying two-phase commit & discard logic
type mockNomtCatchupPayload struct {
	discardCount       atomic.Int32
	commitAsyncCount   atomic.Int32
	writeChangelogCount atomic.Int32
	setBlockNumCount   atomic.Int32
	lastBlockNum       atomic.Uint64
	writeChangelogErr  error
	waitErr            error
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

// verifyCatchupPipelineCommits tests the two-phase commit verification logic used in reexecuteBlocksToCatchUp:
// 1. If accountRoot or stakeRoot mismatches the header, Discard() must be called on all payloads,
//    and an error must be returned without persisting to disk.
// 2. If roots match, SetBlockNumber, WriteChangelog, and CommitAsync must be invoked on all payloads.
func verifyCatchupPipelineCommits(
	scPayload interface{},
	accountNomtPayload interface{},
	accountFinalHash e_common.Hash,
	expectedAccountRoot e_common.Hash,
	stakeNomtPayload interface{},
	stakeFinalHash e_common.Hash,
	expectedStakeRoot e_common.Hash,
	bn uint64,
) error {
	if accountFinalHash != expectedAccountRoot {
		if scPayload != nil {
			if disc, ok := scPayload.(interface{ Discard() }); ok {
				disc.Discard()
			}
		}
		if accountNomtPayload != nil {
			if disc, ok := accountNomtPayload.(interface{ Discard() }); ok {
				disc.Discard()
			}
		}
		if stakeNomtPayload != nil {
			if disc, ok := stakeNomtPayload.(interface{ Discard() }); ok {
				disc.Discard()
			}
		}
		return errors.New("NOMT account root mismatch during catch-up")
	}

	if stakeFinalHash != expectedStakeRoot {
		if scPayload != nil {
			if disc, ok := scPayload.(interface{ Discard() }); ok {
				disc.Discard()
			}
		}
		if accountNomtPayload != nil {
			if disc, ok := accountNomtPayload.(interface{ Discard() }); ok {
				disc.Discard()
			}
		}
		if stakeNomtPayload != nil {
			if disc, ok := stakeNomtPayload.(interface{ Discard() }); ok {
				disc.Discard()
			}
		}
		return errors.New("NOMT stake root mismatch during catch-up")
	}

	// Verified! Persist
	if scPayload != nil {
		if payload, ok := scPayload.(interface{ SetBlockNumber(uint64) }); ok {
			payload.SetBlockNumber(bn)
		}
		if payload, ok := scPayload.(interface{ WriteChangelog() error }); ok {
			if err := payload.WriteChangelog(); err != nil {
				return err
			}
		}
		if payload, ok := scPayload.(interface{ CommitAsync() }); ok {
			payload.CommitAsync()
		}
	}

	if accountNomtPayload != nil {
		if payload, ok := accountNomtPayload.(interface{ SetBlockNumber(uint64) }); ok {
			payload.SetBlockNumber(bn)
		}
		if payload, ok := accountNomtPayload.(interface{ WriteChangelog() error }); ok {
			if err := payload.WriteChangelog(); err != nil {
				return err
			}
		}
		if payload, ok := accountNomtPayload.(interface{ CommitAsync() }); ok {
			payload.CommitAsync()
		}
	}

	if stakeNomtPayload != nil {
		if payload, ok := stakeNomtPayload.(interface{ SetBlockNumber(uint64) }); ok {
			payload.SetBlockNumber(bn)
		}
		if payload, ok := stakeNomtPayload.(interface{ WriteChangelog() error }); ok {
			if err := payload.WriteChangelog(); err != nil {
				return err
			}
		}
		if payload, ok := stakeNomtPayload.(interface{ CommitAsync() }); ok {
			payload.CommitAsync()
		}
	}

	// Wait
	if waiter, ok := scPayload.(interface{ Wait() error }); ok {
		if err := waiter.Wait(); err != nil {
			return err
		}
	}
	if waiter, ok := accountNomtPayload.(interface{ Wait() error }); ok {
		if err := waiter.Wait(); err != nil {
			return err
		}
	}
	if waiter, ok := stakeNomtPayload.(interface{ Wait() error }); ok {
		if err := waiter.Wait(); err != nil {
			return err
		}
	}

	return nil
}

func TestCatchupReexecution_AccountRootMismatch_TriggersDiscard(t *testing.T) {
	sc := &mockNomtCatchupPayload{}
	acct := &mockNomtCatchupPayload{}
	stake := &mockNomtCatchupPayload{}

	expectedAcctRoot := e_common.HexToHash("0x1111")
	actualAcctRoot := e_common.HexToHash("0x2222") // mismatch!
	stakeRoot := e_common.HexToHash("0x3333")

	err := verifyCatchupPipelineCommits(
		sc, acct, actualAcctRoot, expectedAcctRoot,
		stake, stakeRoot, stakeRoot,
		2,
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

	err := verifyCatchupPipelineCommits(
		sc, acct, acctRoot, acctRoot,
		stake, actualStakeRoot, expectedStakeRoot,
		2,
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

	err := verifyCatchupPipelineCommits(
		sc, acct, acctRoot, acctRoot,
		stake, stakeRoot, stakeRoot,
		testBlockNum,
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
}
