package tx_processor

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
)

func TestVerifyNodeCommitteeKey(t *testing.T) {
	bls.Init()
	cs := setupTestChainState(t)

	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()
	otherKP := bls.GenerateKeyPair()

	// 1. Validator 1: healthy, registered in stakeDB, account has matching PublicKeyBls
	node1 := state.NewAccountState(kp1.Address())
	node1.AddBalance(big.NewInt(1_000_000_000_000_000_000))
	node1.SetPublicKeyBls(kp1.PublicKey().Bytes())
	cs.GetAccountStateDB().SetState(node1)
	addTestCommitteeValidator(t, cs, kp1)

	// 2. Validator 2: registered in stakeDB, but account has MISMATCHED PublicKeyBls
	node2 := state.NewAccountState(kp2.Address())
	node2.AddBalance(big.NewInt(1_000_000_000_000_000_000))
	node2.SetPublicKeyBls(otherKP.PublicKey().Bytes()) // Mismatched key in state!
	cs.GetAccountStateDB().SetState(node2)
	addTestCommitteeValidator(t, cs, kp2)

	flushTestStake(t, cs)

	// Scenario A: Healthy validator 1 with matching key
	isVal, warn := VerifyNodeCommitteeKey(cs, kp1.Address(), common.Address{}, kp1.PublicKey())
	require.True(t, isVal)
	require.Empty(t, warn, "healthy validator should have no warnings")

	// Scenario B: Validator 2 whose on-chain PublicKeyBls doesn't match node's attestation key
	isVal, warn = VerifyNodeCommitteeKey(cs, kp2.Address(), common.Address{}, kp2.PublicKey())
	require.True(t, isVal)
	require.Contains(t, warn, "does not match node attestation key")

	// Scenario C: Non-validator node (observer / RPC node)
	nonVal := bls.GenerateKeyPair()
	isVal, warn = VerifyNodeCommitteeKey(cs, nonVal.Address(), common.Address{}, nonVal.PublicKey())
	require.False(t, isVal, "non-validator node should report isValidator=false")
	require.Empty(t, warn)

	// Scenario D: Node address passed via cfgAddr instead of nodeAddr
	isVal, warn = VerifyNodeCommitteeKey(cs, common.HexToAddress("0x9999999999999999999999999999999999999999"), kp1.Address(), kp1.PublicKey())
	require.True(t, isVal)
	require.Empty(t, warn)

	// The light (point-lookup) variant must agree with the exact one on every scenario above.
	for _, c := range []struct {
		name    string
		node    common.Address
		cfg     common.Address
		key     cm.PublicKey
		isVal   bool
		warning string
	}{
		{"healthy", kp1.Address(), common.Address{}, kp1.PublicKey(), true, ""},
		{"key mismatch", kp2.Address(), common.Address{}, kp2.PublicKey(), true, "does not match node attestation key"},
		{"non validator", nonVal.Address(), common.Address{}, nonVal.PublicKey(), false, ""},
		{"via cfg address", common.HexToAddress("0x9999999999999999999999999999999999999999"), kp1.Address(), kp1.PublicKey(), true, ""},
	} {
		isVal, warn, err := VerifyNodeCommitteeKeyLight(cs, c.node, c.cfg, c.key)
		require.NoError(t, err, c.name)
		require.Equal(t, c.isVal, isVal, c.name)
		if c.warning == "" {
			require.Empty(t, warn, c.name)
		} else {
			require.Contains(t, warn, c.warning, c.name)
		}
	}
}

// Validators that share one BLS key are a single signer: the committee must count distinct keys, otherwise the f+1
// threshold is computed from an inflated N and could never be reached.
func TestLiveCommitteeProviderCountsDistinctKeys(t *testing.T) {
	bls.Init()
	cs := setupTestChainState(t)
	shared := bls.GenerateKeyPair()
	for i := 0; i < 4; i++ {
		kp := bls.GenerateKeyPair() // distinct validator address each
		acc := state.NewAccountState(kp.Address())
		acc.AddBalance(big.NewInt(1_000_000_000_000_000_000))
		acc.SetPublicKeyBls(shared.PublicKey().Bytes()) // all share one key (set first: the key is write-once)
		cs.GetAccountStateDB().SetState(acc)
		addTestCommitteeValidator(t, cs, kp)
	}
	flushTestStake(t, cs)
	keys, err := NewLiveCommitteeProvider(cs).GetActiveCommitteeBLSKeys()
	require.NoError(t, err)
	require.Len(t, keys, 1, "4 validators with one shared key are 1 signer")
	require.Equal(t, shared.PublicKey(), keys[0])
}
