package tx_processor

import (
	"math/big"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// Differential test for plain EVM contract calls: random ERC-20-shaped operations (transfer, approve, transferFrom, mint)
// between a handful of accounts that all hit the same few storage slots, mixed with native transfers between the same
// accounts and gateway barrier txs, so Block-STM has to resolve real storage conflicts. Same oracle as the other
// differential tests; the contract state is part of the account root.

var evmToken common.Address

func evmTokenTweak(t *testing.T, env barrierEnv, cs *blockchain.ChainState) {
	t.Helper()
	evmToken = deployTestWrappedAsset(t, cs, env.evm[0].addr, big.NewInt(1_000_000))
}

func evmTokenBlock(t *testing.T, r *rand.Rand, env barrierEnv) []mt_types.Transaction {
	gh, err := GetGatewayHandler()
	if err != nil {
		t.Fatal(err)
	}
	tokenABI := testWrappedAssetABI(t)
	wrap := func(in []byte, err error) []byte {
		if err != nil {
			t.Fatal(err)
		}
		d, err := transaction.NewCallData(in).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	nonces := map[common.Address]uint64{env.evm[0].addr: 1} // the deployer spent nonce 0
	next := func(a common.Address) uint64 { n := nonces[a]; nonces[a]++; return n }
	actors := env.evm // five accounts that share the token
	pick := func() common.Address { return actors[r.Intn(len(actors))].addr }
	var txs []mt_types.Transaction
	for i, n := 0, 6+r.Intn(14); i < n; i++ {
		from := pick()
		amt := big.NewInt(int64(1 + r.Intn(500)))
		var to common.Address
		var data []byte
		value := big.NewInt(0)
		switch r.Intn(8) {
		case 0, 1:
			to, data = evmToken, wrap(tokenABI.Pack("transfer", pick(), amt))
		case 2:
			to, data = evmToken, wrap(tokenABI.Pack("approve", pick(), big.NewInt(int64(100+r.Intn(1000)))))
		case 3, 4:
			to, data = evmToken, wrap(tokenABI.Pack("transferFrom", pick(), pick(), amt))
		case 5:
			to, data = evmToken, wrap(tokenABI.Pack("mint", pick(), amt))
		case 6: // native transfer between the same accounts
			to, value = pick(), big.NewInt(int64(1+r.Intn(1000)))
		default: // gateway barrier tx from one of the actors (native outbound)
			to, data = mt_common.GATEWAY_CONTRACT_ADDRESS, wrap(gh.abi.Pack("outbound", big.NewInt(102), env.users[r.Intn(len(env.users))], []byte{0xAD},
				big.NewInt(0), big.NewInt(int64(1+r.Intn(300))), big.NewInt(0), big.NewInt(0), uint8(1), false, uint64(0)))
		}
		if to == from {
			continue
		}
		txs = append(txs, transaction.NewTransaction(from, to, value, 600_000, 1, uint64(9800+i), data, nil, common.Hash{}, common.Hash{}, next(from), 991))
	}
	return txs
}

func TestExecDifferential_EVMTokenCalls_OptimisticVsTrueBlockSTMOnly(t *testing.T) {
	barrierContractStorage = true
	defer func() { barrierContractStorage = false }()
	runGatewayDifferential(t, 200, evmTokenBlock, evmTokenTweak, nil)
}
