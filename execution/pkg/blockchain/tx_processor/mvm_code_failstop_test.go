package tx_processor

import (
	"context"
	"math/big"
	"os"
	"os/exec"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/stretchr/testify/require"
)

// End to end: a token is deployed, then its bytecode is removed from the code store (a damaged or incomplete store, e.g. a
// snapshot restored without the code database) and the token is called through the real MVM. The call still "succeeds"
// (a call to an address without code does), and with METANODE_FAILSTOP_ON_UNREADABLE_CODE set the node must halt instead.
// The scenario runs in a child process because the halt is os.Exit.

const wipedCodeHelperEnv = "METANODE_TEST_WIPED_CODE_HELPER"

func TestMVM_UnreadableCode_FailStopEndToEnd(t *testing.T) {
	if os.Getenv(wipedCodeHelperEnv) == "1" {
		runWipedCodeScenario(t)
		return
	}
	run := func(failStop string) (string, int) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMVM_UnreadableCode_FailStopEndToEnd$", "-test.v")
		cmd.Env = append(os.Environ(), wipedCodeHelperEnv+"=1", "METANODE_FAILSTOP_ON_UNREADABLE_CODE="+failStop)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatalf("running the child: %v", err)
		}
		return string(out), code
	}

	out, code := run("")
	require.Equal(t, 0, code, "without the opt-in the node keeps running: %s", tail(out))
	require.Contains(t, out, "is not readable from the code store", "the unreadable code must at least be logged")
	require.NotContains(t, out, "FATAL EXIT")

	out, code = run("true")
	require.Equal(t, 1, code, "with the opt-in the node must halt (os.Exit(1)): %s", tail(out))
	require.Contains(t, out, "FATAL EXIT")
	require.Contains(t, out, "is not readable from the code store")
}

func tail(s string) string {
	if len(s) > 1200 {
		return s[len(s)-1200:]
	}
	return s
}

func runWipedCodeScenario(t *testing.T) {
	cs, _, codeStorage, _ := newPersistentTestChainState(t)
	deployer := common.HexToAddress("0x4444444444444444444444444444444444444444")
	token := deployTestWrappedAsset(t, cs, deployer, big.NewInt(1000))
	as, err := cs.GetAccountStateDB().AccountState(token)
	require.NoError(t, err)
	codeHash := as.SmartContractState().CodeHash()
	require.NotEqual(t, common.Hash{}, codeHash)
	require.NoError(t, codeStorage.Delete(codeHash.Bytes()))

	// the call goes through GlobalStateGet, which is where the unreadable code is noticed
	calldata, err := testWrappedAssetABI(t).Pack("balanceOf", deployer)
	require.NoError(t, err)
	caller := common.HexToAddress("0xCA11CA11CA11CA11CA11CA11CA11CA11CA11CA1")
	callTx := newHighGasTx(caller, token, 0, big.NewInt(0), calldata)
	_, mvmE := createVmProcessorForGateway(context.Background(), cs, callTx, 0)
	h := *cs.GetcurrentBlockHeader()
	leader := h.LeaderAddress()
	if leader == (common.Address{}) {
		leader = caller
	}
	res := mvmE.Execute(caller.Bytes(), token.Bytes(), calldata, big.NewInt(0), callTx.MaxGasPrice(), callTx.MaxGas(), h.TimeStamp(), mt_common.BLOCK_GAS_LIMIT, uint64(0),
		mt_common.MINIMUM_BASE_FEE, h.BlockNumber()+1, leader, mvmE.GetKey(), callTx.Hash().Bytes(), []common.Address{}, false, false)
	// reached only when the node did not halt
	t.Logf("call survived: status=%v", res.Status)
}
