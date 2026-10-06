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

// P0-1b: Verify that arbitrary accounts that register as a validator with zero stake
// (e.g. calling registerValidator without stake delegation) CANNOT enter the active validator committee.
func TestP0_1b_ZeroStakeOrUnstakedValidatorCannotEnterCommittee(t *testing.T) {
	bls.Init()
	cs := setupTestChainState(t)

	// 1. Setup a genuine validator with positive stake (1000 MTN) and BLS key
	valKP := bls.GenerateKeyPair()
	valAcc := state.NewAccountState(valKP.Address())
	valAcc.AddBalance(big.NewInt(1_000_000_000_000_000_000))
	valAcc.SetPublicKeyBls(valKP.PublicKey().Bytes())
	cs.GetAccountStateDB().SetState(valAcc)
	addTestCommitteeValidator(t, cs, valKP)

	// 2. Setup an attacker account that registers as a validator with ZERO stake (like registerValidator)
	attackerKP := bls.GenerateKeyPair()
	attackerAcc := state.NewAccountState(attackerKP.Address())
	attackerAcc.AddBalance(big.NewInt(1_000_000_000_000_000_000))
	attackerAcc.SetPublicKeyBls(attackerKP.PublicKey().Bytes())
	cs.GetAccountStateDB().SetState(attackerAcc)

	// Attacker registers in stakeStateDB but has 0 stake
	attackerAddr := attackerKP.Address()
	require.NoError(t, cs.GetStakeStateDB().CreateRegisterWithKeys(
		attackerAddr, "attacker-val", "Malicious Validator", "http://bad.org", "", 0,
		big.NewInt(0), "127.0.0.1:6201", "127.0.0.1:4013", "/ip4/127.0.0.1/tcp/9101",
		"", []byte{0x04}, []byte{0x05}, "attacker-node", attackerKP.PublicKey().Bytes(),
	))

	// 3. Setup another attacker account that registers with zero stake and no BLS key
	attacker2KP := bls.GenerateKeyPair()
	attacker2Addr := attacker2KP.Address()
	require.NoError(t, cs.GetStakeStateDB().CreateRegisterWithKeys(
		attacker2Addr, "attacker-2", "", "", "", 0,
		big.NewInt(0), "127.0.0.1:6202", "127.0.0.1:4014", "/ip4/127.0.0.1/tcp/9102",
		"", []byte{0x06}, []byte{0x07}, "attacker-2-node", []byte{0x08},
	))

	flushTestStake(t, cs)

	// Check 1: GetAllValidators() returns all entries, sorted by stake descending (valKP first)
	allVals, err := cs.GetStakeStateDB().GetAllValidators()
	require.NoError(t, err)
	require.Len(t, allVals, 3)
	require.Equal(t, valKP.Address(), allVals[0].Address(), "staked validator must be sorted first")

	// Check 2: LiveCommitteeProvider MUST filter out zero-stake validators
	activeKeys, err := NewLiveCommitteeProvider(cs).GetActiveCommitteeBLSKeys()
	require.NoError(t, err)
	require.Len(t, activeKeys, 1, "only genuine staked validator must enter active committee")
	require.Equal(t, valKP.PublicKey(), activeKeys[0])

	// Check 3: VerifyNodeCommitteeKey flags the zero-stake registered account
	isVal, warn := VerifyNodeCommitteeKey(cs, attackerAddr, common.Address{}, attackerKP.PublicKey())
	require.True(t, isVal, "attacker is registered in stake state")
	require.Contains(t, warn, "zero or non-positive stake (0): attestations will not be accepted by peers")

	// Check 4: Light point-lookup also detects zero-stake validator
	isValLight, warnLight, err := VerifyNodeCommitteeKeyLight(cs, attackerAddr, common.Address{}, attackerKP.PublicKey())
	require.NoError(t, err)
	require.True(t, isValLight)
	require.Contains(t, warnLight, "zero or non-positive stake (0): attestations will not be accepted by peers")
}
