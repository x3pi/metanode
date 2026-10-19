package tx_processor

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A tx that the Block-STM executes speculatively against the base state and later has to reject (here: a duplicate-nonce
// tx with a gas limit below intrinsic gas) writes the sender's account, which a LATER tx of the same sender may read. When
// the rejected tx is re-executed it writes nothing and its old write is deleted, so every reader of that write must be
// re-validated. It was not: the sender's next tx kept results computed from the phantom write, silently losing the
// previous tx's debit (or keeping the rejected tx's fee), so the same block gave different balances from run to run
// and two validators could disagree. Found by the mixed-block differential test; reproduced in ~7% of runs.
func TestTrueBlockSTM_RejectedSpeculativeTx_DoesNotCorruptLaterTxOfSameSender(t *testing.T) {
	accts := diffAccounts()
	a6 := accts[5].addr // starts with 900_000_000_000
	f2 := common.BigToAddress(big.NewInt(0xF002))
	mk := func(to common.Address, nonce uint64, amt int64, gas, price, id uint64) mt_types.Transaction {
		return transaction.NewTransaction(a6, to, big.NewInt(amt), gas, price, id, nil, nil, common.Hash{}, common.Hash{}, nonce, parityChainID)
	}
	txs := []mt_types.Transaction{
		mk(a6, 0, 1, 20999, 1, 1003),         // A: duplicate nonce 0, gas limit below intrinsic -> rejected once B is applied
		mk(f2, 0, 1000, 21000, 100000, 1006), // B: the tx that wins nonce 0
		mk(a6, 1, 0, 21000, 300000, 1007),    // C: nonce 1
	}

	// B pays 45000 gas at price 100000 (4.5e9) and sends 1000; C pays 20000 gas at price 300000 (6e9); A changes nothing.
	const want = 900_000_000_000 - 1000 - 4_500_000_000 - 6_000_000_000
	outcomes := map[string]int{}
	for i := 0; i < 300; i++ {
		o := diffRun(t, true, accts, txs)
		outcomes[strings.Join(o.accounts, ";")]++
		for _, line := range o.accounts {
			if strings.Contains(line, a6.Hex()) {
				require.Contains(t, line, "bal="+big.NewInt(want).String(), "run %d: sender balance must be the sequential result", i)
				require.Contains(t, line, "nonce=2", "run %d", i)
			}
		}
	}
	assert.Len(t, outcomes, 1, "the same block must always produce the same state")
}
