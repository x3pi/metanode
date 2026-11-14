package raftfeed

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// laggingCluster simulates the local tip advancing by `rate` blocks per tick while peers trail it by `lag`
// blocks: a block reaches a verdict only once a majority has applied it.
type laggingCluster struct {
	tick, rate, lag uint64
	asked           []uint64
	verdicts        []uint64
}

func (c *laggingCluster) tip() uint64 { return c.tick * c.rate }

func (c *laggingCluster) checkpoint() uint64 { return c.tip() / attestStride * attestStride }

func (c *laggingCluster) once(num uint64) bool {
	c.asked = append(c.asked, num)
	if c.tip() >= num+c.lag { // peers have applied num
		c.verdicts = append(c.verdicts, num)
		return true
	}
	return false
}

func runAttest(c *laggingCluster, ticks int) {
	var last, pending uint64
	for i := 0; i < ticks; i++ {
		c.tick++
		last, pending = attestStep(last, pending, c.checkpoint, c.once)
	}
}

// legacyAttestStep is the pre-fix loop body (always chase the newest checkpoint), kept to prove the starvation
// the fix removes.
func legacyAttestStep(last uint64, checkpoint func() uint64, once func(uint64) bool) uint64 {
	if num := checkpoint(); num != 0 && num != last {
		if once(num) {
			return num
		}
	}
	return last
}

func TestAttestStep_NoStarvationUnderSteadyLoad(t *testing.T) {
	// 50 blocks/tick, peers 30 blocks behind: the newest block is never on a majority when first asked.
	c := &laggingCluster{rate: 50, lag: 30}
	runAttest(c, 100)
	require.NotEmpty(t, c.verdicts, "attestation must reach verdicts under steady load")
	assert.GreaterOrEqual(t, len(c.verdicts), 45, "a pending block must be resolved on the next tick, got %d verdicts in 100 ticks", len(c.verdicts))

	legacy := &laggingCluster{rate: 50, lag: 30}
	var last uint64
	for i := 0; i < 100; i++ {
		legacy.tick++
		last = legacyAttestStep(last, legacy.checkpoint, legacy.once)
	}
	assert.Empty(t, legacy.verdicts, "sanity: the legacy loop starves in this scenario (that is the bug)")
}

func TestAttestStep_RetriesSameBlockUntilVerdict(t *testing.T) {
	c := &laggingCluster{rate: 1, lag: 3}
	var last, pending uint64
	c.tick = 5
	last, pending = attestStep(last, pending, c.checkpoint, c.once)
	require.Equal(t, uint64(5), pending, "block 5 is not on a majority yet: it must stay pending")
	require.Equal(t, uint64(0), last)
	for i := 0; i < 3; i++ {
		c.tick++
		last, pending = attestStep(last, pending, c.checkpoint, c.once)
	}
	assert.Equal(t, []uint64{5, 5, 5, 5}, c.asked, "the pending block must be retried, never abandoned for a newer one")
	assert.Equal(t, uint64(5), last)
	assert.Equal(t, uint64(0), pending)
}

func TestAttestStep_NoMajorityStaysPendingForever(t *testing.T) {
	// Peers never apply anything (down / partitioned): nothing is decided, nothing advances.
	c := &laggingCluster{rate: 10, lag: 1 << 62}
	var last, pending uint64
	for i := 0; i < 50; i++ {
		c.tick++
		last, pending = attestStep(last, pending, c.checkpoint, c.once)
	}
	assert.Equal(t, uint64(0), last, "no verdict without a majority")
	assert.Equal(t, uint64(10), pending, "the first checkpoint stays pending")
	assert.Empty(t, c.verdicts)
}

func TestAttestStep_IdleChainDoesNotReCheck(t *testing.T) {
	calls := 0
	once := func(uint64) bool { calls++; return true }
	last, pending := attestStep(0, 0, func() uint64 { return 7 }, once)
	require.Equal(t, uint64(7), last)
	for i := 0; i < 5; i++ {
		last, pending = attestStep(last, pending, func() uint64 { return 7 }, once)
	}
	assert.Equal(t, 1, calls, "an already attested tip must not be re-asked every tick")
	assert.Equal(t, uint64(0), pending)

	_, _ = attestStep(0, 0, func() uint64 { return 0 }, func(uint64) bool { t.Fatal("block 0 must never be attested"); return false })
}

// With attestStride 1 the newest durable block is the checkpoint; a coarser stride would hide up to
// attestStride-1 blocks of divergence until the next multiple.
func TestAttestStride_ChecksNewestBlock(t *testing.T) {
	require.Equal(t, uint64(1), uint64(attestStride))
	c := &laggingCluster{rate: 7, lag: 0}
	runAttest(c, 3)
	assert.Equal(t, []uint64{7, 14, 21}, c.verdicts)
}
