package tx_processor

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

func groupsOf(txs ...types.Transaction) []grouptxns.RelativeGroup {
	var gs []grouptxns.RelativeGroup
	for i, tx := range txs {
		gs = append(gs, grouptxns.RelativeGroup{GroupID: i, Items: []grouptxns.Item{{ID: i, Tx: tx}}})
	}
	return gs
}

// A committed block must never execute a tx whose signature does not verify: a Byzantine proposer could
// otherwise include forged txs that every honest node executes.
func TestFilterInvalidSignatures_DropsForgedKeepsValid(t *testing.T) {
	cs := setupTestChainState(t)
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")

	victim := common.HexToAddress("0x1234")
	to := common.HexToAddress("0x5678")
	good, pub := createTestTx(victim, to, big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)

	as := state.NewAccountState(victim)
	as.AddBalance(big.NewInt(1_000_000_000_000_000))
	as.SetPublicKeyBls(pub)
	as.SetNonce(1)
	cs.GetAccountStateDB().SetState(as)

	// forged: same content, signature made by ANOTHER key
	forged, _ := createTestTx(victim, to, big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)
	// unsigned
	unsigned := transaction.NewTransaction(victim, to, big.NewInt(2), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, p_common.TRANSFER_GAS_COST,
		[]byte{}, nil, common.Hash{}, common.Hash{}, 2, 1)

	out := FilterInvalidSignatures(cs, groupsOf(good, forged, unsigned))
	if len(out) != 1 || len(out[0].Items) != 1 || out[0].Items[0].Tx.Hash() != good.Hash() {
		t.Fatalf("expected only the genuinely signed tx to survive, got %d groups", len(out))
	}
	// second pass is served from cache and must still reject the forged copy (cache keyed by hash+sig+key)
	out = FilterInvalidSignatures(cs, groupsOf(good, forged))
	if len(out) != 1 {
		t.Fatalf("forged tx must not ride on the genuine tx's cache entry, got %d groups", len(out))
	}
}

func TestFilterInvalidSignatures_DevnetBypassIsExplicit(t *testing.T) {
	cs := setupTestChainState(t)
	victim := common.HexToAddress("0x1234")
	unsigned := transaction.NewTransaction(victim, common.HexToAddress("0x5678"), big.NewInt(2), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, p_common.TRANSFER_GAS_COST,
		[]byte{}, nil, common.Hash{}, common.Hash{}, 2, 1)

	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "true")
	t.Setenv("METANODE_DEVNET", "")
	if out := FilterInvalidSignatures(cs, groupsOf(unsigned)); len(out) != 0 {
		t.Fatal("bypass without METANODE_DEVNET must not be honoured")
	}
	t.Setenv("METANODE_DEVNET", "true")
	t.Setenv("METANODE_ENV", "production")
	if out := FilterInvalidSignatures(cs, groupsOf(unsigned)); len(out) != 0 {
		t.Fatal("bypass in production must not be honoured")
	}
	t.Setenv("METANODE_ENV", "")
	if out := FilterInvalidSignatures(cs, groupsOf(unsigned)); len(out) != 1 {
		t.Fatal("explicit devnet bypass should keep the tx")
	}
}
