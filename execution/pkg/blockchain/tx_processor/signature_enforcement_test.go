package tx_processor

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
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

// Batch path: many BLS txs, with two signatures swapped (each individually invalid, aggregate unchanged).
// Exactly those two must be dropped and everything else kept, i.e. batching never changes a tx's verdict.
func TestFilterInvalidSignatures_BatchPathExactVerdicts(t *testing.T) {
	cs := setupTestChainState(t)
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	const n = 300
	txs := make([]types.Transaction, n)
	for i := 0; i < n; i++ {
		from := common.BigToAddress(big.NewInt(int64(5000 + i)))
		tx, pub := createTestTx(from, common.HexToAddress("0x456"), big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)
		as := state.NewAccountState(from)
		as.AddBalance(big.NewInt(1_000_000_000_000_000))
		as.SetPublicKeyBls(pub)
		as.SetNonce(1)
		cs.GetAccountStateDB().SetState(as)
		txs[i] = tx
	}
	s1, s2 := txs[10].Sign().Bytes(), txs[200].Sign().Bytes()
	txs[10].(*transaction.Transaction).SetSignBytes(s2)
	txs[200].(*transaction.Transaction).SetSignBytes(s1)

	rotateVerifiedSignatures()
	rotateVerifiedSignatures()
	out := FilterInvalidSignatures(cs, groupsOf(txs...))
	if len(out) != n-2 {
		t.Fatalf("expected %d survivors, got %d", n-2, len(out))
	}
	for _, g := range out {
		for _, it := range g.Items {
			if it.Tx.Hash() == txs[10].Hash() || it.Tx.Hash() == txs[200].Hash() {
				t.Fatal("a swapped-signature tx survived")
			}
		}
	}
}

func TestFilterInvalidSignatures_BenchmarkSwitchIsDevnetOnly(t *testing.T) {
	cs := setupTestChainState(t)
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	victim := common.HexToAddress("0x1234")
	unsigned := transaction.NewTransaction(victim, common.HexToAddress("0x5678"), big.NewInt(2), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, p_common.TRANSFER_GAS_COST,
		[]byte{}, nil, common.Hash{}, common.Hash{}, 2, 1)

	t.Setenv("METANODE_DEVNET_SKIP_EXEC_SIG_FILTER", "true")
	t.Setenv("METANODE_DEVNET", "")
	if out := FilterInvalidSignatures(cs, groupsOf(unsigned)); len(out) != 0 {
		t.Fatal("benchmark switch without METANODE_DEVNET must not disable the filter")
	}
	t.Setenv("METANODE_DEVNET", "true")
	t.Setenv("NODE_ENV", "production")
	if out := FilterInvalidSignatures(cs, groupsOf(unsigned)); len(out) != 0 {
		t.Fatal("benchmark switch in production must not disable the filter")
	}
	t.Setenv("NODE_ENV", "")
	if out := FilterInvalidSignatures(cs, groupsOf(unsigned)); len(out) != 1 {
		t.Fatal("devnet benchmark switch should disable the filter")
	}
}

// One bad signature in a big chunk must be found by bisection (not by re-verifying the whole chunk one by
// one), while every other tx keeps its exact verdict.
func TestVerifySignatures_BisectsFailingChunk(t *testing.T) {
	cs := setupTestChainState(t)
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	const n = 256
	txs := make([]types.Transaction, n)
	for i := 0; i < n; i++ {
		from := common.BigToAddress(big.NewInt(int64(20000 + i)))
		tx, pub := createTestTx(from, common.HexToAddress("0x456"), big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)
		as := state.NewAccountState(from)
		as.AddBalance(big.NewInt(1_000_000_000_000_000))
		as.SetPublicKeyBls(pub)
		as.SetNonce(1)
		cs.GetAccountStateDB().SetState(as)
		txs[i] = tx
	}
	bad := 77
	other, _ := createTestTx(common.HexToAddress("0xdead"), common.HexToAddress("0x456"), big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)
	txs[bad].(*transaction.Transaction).SetSignBytes(other.Sign().Bytes())

	rotateVerifiedSignatures()
	rotateVerifiedSignatures()
	valid, st := verifySignatures(cs.GetAccountStateDB(), txs, nil)
	for i, v := range valid {
		if v == (i == bad) {
			t.Fatalf("tx %d verdict wrong (valid=%v)", i, v)
		}
	}
	if st.individual > 2*bisectMinSize {
		t.Fatalf("bisection should isolate one bad sig with <= %d individual checks, did %d", 2*bisectMinSize, st.individual)
	}
}

func TestPrewarmSignatureCache_FillsCache(t *testing.T) {
	cs := setupTestChainState(t)
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")
	from := common.HexToAddress("0xabcd")
	tx, pub := createTestTx(from, common.HexToAddress("0x456"), big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)
	as := state.NewAccountState(from)
	as.SetPublicKeyBls(pub)
	as.SetNonce(1)
	cs.GetAccountStateDB().SetState(as)

	rotateVerifiedSignatures()
	rotateVerifiedSignatures()
	if LoadVerifiedSignature(sigCacheKey(tx, pub)) {
		t.Fatal("cache should start cold")
	}
	PrewarmSignatureCache(cs, []types.Transaction{tx, nil}, nil)
	if !LoadVerifiedSignature(sigCacheKey(tx, pub)) {
		t.Fatal("prewarm should have cached the verified signature")
	}
}

func TestFilterInvalidSignatures_SecpProtoType0xFF(t *testing.T) {
	cs := setupTestChainState(t)
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")

	privKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("crypto.GenerateKey: %v", err)
	}
	from := crypto.PubkeyToAddress(privKey.PublicKey)
	to := common.HexToAddress("0x7890")

	// Account without BLS key (secp only)
	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1_000_000_000_000_000))
	as.SetNonce(1)
	cs.GetAccountStateDB().SetState(as)

	// Valid Type 0xFF tx
	good := &transaction.Transaction{}
	good.FromProto(&pb.Transaction{
		FromAddress: from.Bytes(),
		ToAddress:   to.Bytes(),
		Amount:      big.NewInt(100).Bytes(),
		Nonce:       []byte{0, 0, 0, 0, 0, 0, 0, 1},
		MaxGas:      p_common.TRANSFER_GAS_COST,
		MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID:     1337,
		Type:        0xFF,
	})
	if err := good.SignSecpProto(privKey); err != nil {
		t.Fatalf("SignSecpProto failed: %v", err)
	}

	// Forged Type 0xFF tx signed with a DIFFERENT private key
	otherKey, _ := crypto.GenerateKey()
	forged := &transaction.Transaction{}
	forged.FromProto(&pb.Transaction{
		FromAddress: from.Bytes(),
		ToAddress:   to.Bytes(),
		Amount:      big.NewInt(100).Bytes(),
		Nonce:       []byte{0, 0, 0, 0, 0, 0, 0, 1},
		MaxGas:      p_common.TRANSFER_GAS_COST,
		MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID:     1337,
		Type:        0xFF,
	})
	if err := forged.SignSecpProto(otherKey); err != nil {
		t.Fatalf("SignSecpProto failed: %v", err)
	}

	rotateVerifiedSignatures()
	rotateVerifiedSignatures()

	out := FilterInvalidSignatures(cs, groupsOf(good, forged))
	if len(out) != 1 || len(out[0].Items) != 1 || out[0].Items[0].Tx.Hash() != good.Hash() {
		t.Fatalf("expected only the genuine secp proto tx to survive, got %d groups", len(out))
	}
}

func TestFilterInvalidSignatures_SecpProtoType0xFF_WithAccountHavingBLSKey(t *testing.T) {
	cs := setupTestChainState(t)
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")

	privKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("crypto.GenerateKey: %v", err)
	}
	from := crypto.PubkeyToAddress(privKey.PublicKey)
	to := common.HexToAddress("0x7890")

	// Account WITH registered BLS key sending a Type 0xFF transaction
	_, blsPub := createTestTx(from, to, big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)
	as := state.NewAccountState(from)
	as.AddBalance(big.NewInt(1_000_000_000_000_000))
	as.SetPublicKeyBls(blsPub)
	as.SetNonce(1)
	cs.GetAccountStateDB().SetState(as)

	good := &transaction.Transaction{}
	good.FromProto(&pb.Transaction{
		FromAddress: from.Bytes(),
		ToAddress:   to.Bytes(),
		Amount:      big.NewInt(500).Bytes(),
		Nonce:       []byte{0, 0, 0, 0, 0, 0, 0, 1},
		MaxGas:      p_common.TRANSFER_GAS_COST,
		MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID:     1337,
		Type:        0xFF,
	})
	if err := good.SignSecpProto(privKey); err != nil {
		t.Fatalf("SignSecpProto failed: %v", err)
	}

	rotateVerifiedSignatures()
	rotateVerifiedSignatures()

	out := FilterInvalidSignatures(cs, groupsOf(good))
	if len(out) != 1 || len(out[0].Items) != 1 || out[0].Items[0].Tx.Hash() != good.Hash() {
		t.Fatalf("account with BLS key sending Type 0xFF secp tx must be accepted, got %d groups", len(out))
	}
}

func TestVerifySignatures_MixedBatchWithProto0xFF(t *testing.T) {
	cs := setupTestChainState(t)
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")

	const blsCount = 20
	const secpCount = 10
	total := blsCount + secpCount

	txs := make([]types.Transaction, total)

	// Create BLS txs
	for i := 0; i < blsCount; i++ {
		from := common.BigToAddress(big.NewInt(int64(70000 + i)))
		tx, pub := createTestTx(from, common.HexToAddress("0x456"), big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)
		as := state.NewAccountState(from)
		as.AddBalance(big.NewInt(1_000_000_000_000_000))
		as.SetPublicKeyBls(pub)
		as.SetNonce(1)
		cs.GetAccountStateDB().SetState(as)
		txs[i] = tx
	}

	// Create Secp Proto Type 0xFF txs
	for i := 0; i < secpCount; i++ {
		privKey, _ := crypto.GenerateKey()
		from := crypto.PubkeyToAddress(privKey.PublicKey)
		as := state.NewAccountState(from)
		as.AddBalance(big.NewInt(1_000_000_000_000_000))
		as.SetNonce(1)
		cs.GetAccountStateDB().SetState(as)

		tx := &transaction.Transaction{}
		tx.FromProto(&pb.Transaction{
			FromAddress: from.Bytes(),
			ToAddress:   common.HexToAddress("0x456").Bytes(),
			Amount:      big.NewInt(int64(10 + i)).Bytes(),
			Nonce:       []byte{0, 0, 0, 0, 0, 0, 0, 1},
			MaxGas:      p_common.TRANSFER_GAS_COST,
			MaxGasPrice: p_common.MINIMUM_BASE_FEE,
			ChainID:     1337,
			Type:        0xFF,
		})
		_ = tx.SignSecpProto(privKey)
		txs[blsCount+i] = tx
	}

	// Make 1 BLS tx bad and 1 Secp tx bad
	badBLS := 5
	otherTx, _ := createTestTx(common.HexToAddress("0xdead"), common.HexToAddress("0x456"), big.NewInt(1), p_common.TRANSFER_GAS_COST, p_common.MINIMUM_BASE_FEE, 1)
	txs[badBLS].(*transaction.Transaction).SetSignBytes(otherTx.Sign().Bytes())

	badSecp := blsCount + 3
	tamperedProto := txs[badSecp].(*transaction.Transaction).Proto().(*pb.Transaction)
	tamperedProto.Amount = big.NewInt(999999).Bytes()
	txs[badSecp].ClearCacheHash()

	rotateVerifiedSignatures()
	rotateVerifiedSignatures()

	valid, _ := verifySignatures(cs.GetAccountStateDB(), txs, nil)
	for i, v := range valid {
		expectedValid := (i != badBLS && i != badSecp)
		if v != expectedValid {
			t.Fatalf("tx %d expected valid=%v, got %v", i, expectedValid, v)
		}
	}
}

