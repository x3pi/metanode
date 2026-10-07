package tx_processor

import (
	"math/big"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// A binding-proven cache hit may skip the envelope binding; a plain (binding-unproven) entry must NOT, and a
// tampered copy of a cached tx must never ride on the genuine tx's entry.
func TestFilterInvalidSignatures_BoundCacheNeverSkipsUnprovenBinding(t *testing.T) {
	cs := setupTestChainState(t)
	cs.GetConfig().TxSignatureMode = config.TxSignatureModeSecp
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")

	good, _, from := createSignedEIP1559Tx(t, 1, 0)
	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1_000_000_000_000_000))
	cs.GetAccountStateDB().SetState(as)

	rotateVerifiedSignatures()
	rotateVerifiedSignatures()

	// pass 1 verifies fully, pass 2 is served from the binding-proven entry
	for pass := 1; pass <= 2; pass++ {
		if out := FilterInvalidSignatures(cs, groupsOf(good)); len(out) != 1 {
			t.Fatalf("pass %d: genuine tx must survive", pass)
		}
	}
	if !LoadVerifiedSignature(boundSigKey(sigCacheKey(good, nil))) {
		t.Fatal("expected a binding-proven cache entry after a full verification")
	}

	// tampered proto (same RawEnvelope): different ProtoHash => must be dropped despite the warm cache
	p := proto.Clone(good.Proto().(*pb.Transaction)).(*pb.Transaction)
	p.Amount = big.NewInt(999).Bytes()
	tampered := &transaction.Transaction{}
	tampered.FromProto(p)
	if out := FilterInvalidSignatures(cs, groupsOf(good, tampered)); len(out) != 1 || out[0].Items[0].Tx.Hash() != good.Hash() || len(out[0].Items) != 1 {
		t.Fatalf("tampered copy must be dropped, got %d groups", len(out))
	}

	// a PLAIN entry (e.g. stored by a path that never ran the binding) must not let a mismatching tx through
	p2 := proto.Clone(good.Proto().(*pb.Transaction)).(*pb.Transaction)
	p2.MaxGas++
	bad := &transaction.Transaction{}
	bad.FromProto(p2)
	StoreVerifiedSignature(sigCacheKey(bad, nil))
	if out := FilterInvalidSignatures(cs, groupsOf(bad)); len(out) != 0 {
		t.Fatalf("plain cache entry must not bypass the envelope binding, got %d groups", len(out))
	}
}

func TestFilterInvalidSignatures_BoundHitsTrackedAndLogged(t *testing.T) {
	cs := setupTestChainState(t)
	cs.GetConfig().TxSignatureMode = config.TxSignatureModeSecp
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")

	good, _, from := createSignedEIP1559Tx(t, 1, 0)
	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1_000_000_000_000_000))
	cs.GetAccountStateDB().SetState(as)

	rotateVerifiedSignatures()
	rotateVerifiedSignatures()

	// 1. Pass 1: Cold verification via verifySignatures
	valid, st := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{good}, nil, sigPolicyOf(cs))
	require.True(t, valid[0])
	assert.Equal(t, int64(0), st.boundHits, "cold pass must have 0 bound hits")
	assert.Equal(t, int64(1), st.individual, "cold pass must verify individually")

	// 2. Pass 2: Warm verification via verifySignatures hitting boundSigKey
	valid, st = verifySignatures(cs.GetAccountStateDB(), []types.Transaction{good}, nil, sigPolicyOf(cs))
	require.True(t, valid[0])
	assert.Equal(t, int64(1), st.boundHits, "warm pass must increment boundHits")
	assert.Equal(t, int64(0), st.individual, "warm pass must not increment individual")

	// 3. Test FilterInvalidSignatures updates Prometheus metric
	boundHitsBefore := testutil.ToFloat64(metrics.SigFilterBoundHitsTotal)
	txsBefore := testutil.ToFloat64(metrics.SigFilterTxsTotal)

	out := FilterInvalidSignatures(cs, groupsOf(good))
	require.Len(t, out, 1)

	boundHitsAfter := testutil.ToFloat64(metrics.SigFilterBoundHitsTotal)
	txsAfter := testutil.ToFloat64(metrics.SigFilterTxsTotal)

	assert.Equal(t, boundHitsBefore+1, boundHitsAfter, "Prometheus bound hits counter must increment by 1")
	assert.Equal(t, txsBefore+1, txsAfter, "Prometheus total txs counter must increment by 1")
}
