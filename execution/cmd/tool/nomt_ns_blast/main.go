// nomt_ns_blast sends transactions that write the NOMT namespaces plain transfers never touch: "stake" sends delegate()
// calls (stake_db), "contract" deploys a tiny SSTORE contract and calls it with unique slots (smart_contract_storage).
// It exists to drive the ENOSPC fault-injection runs in note/evidence/nomt_enospc_20261020/.
package main

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/abi_contract"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// init code returning the 6-byte runtime  PUSH1 0 CALLDATALOAD DUP1 SSTORE STOP  (slot = value = first 32 calldata bytes)
var sstoreInit = common.FromHex("0x656000358055006000526006601af3")

type key struct {
	PrivateKey string `json:"private_key"`
}

func main() {
	rpcURL := flag.String("rpc", "", "RPC URL of a node that is caught up")
	keysPath := flag.String("keys", "", "generated_keys.json")
	mode := flag.String("mode", "stake", "stake | contract")
	n := flag.Int("n", 200, "number of accounts to use")
	skip := flag.Int("skip", 0, "first account index")
	per := flag.Int("per", 20, "txs per account")
	wait := flag.Int("wait-sec", 60, "seconds to wait for the pool to drain")
	flag.Parse()
	ctx := context.Background()
	cl, err := ethclient.Dial(*rpcURL)
	must(err)
	chainID, err := cl.ChainID(ctx)
	must(err)
	gp, err := cl.SuggestGasPrice(ctx)
	must(err)
	fee := new(big.Int).Mul(gp, big.NewInt(2))
	raw, err := os.ReadFile(*keysPath)
	must(err)
	var keys []key
	must(json.Unmarshal(raw, &keys))
	keys = keys[*skip : *skip+*n]

	va, err := abi.JSON(strings.NewReader(abi_contract.ValidationABI))
	must(err)
	var to common.Address
	var data func(i, k int) []byte
	var gas uint64
	var value *big.Int
	switch *mode {
	case "stake":
		call, err := va.Pack("validatorAddresses", big.NewInt(0))
		must(err)
		out, err := cl.CallContract(ctx, callMsg(mt_common.VALIDATOR_CONTRACT_ADDRESS, call), nil)
		must(err)
		res, err := va.Unpack("validatorAddresses", out)
		must(err)
		validator := res[0].(common.Address)
		fmt.Println("delegating to validator", validator.Hex())
		to, gas, value = mt_common.VALIDATOR_CONTRACT_ADDRESS, 400000, big.NewInt(1_000_000_000_000_000)
		input, err := va.Pack("delegate", validator)
		must(err)
		data = func(int, int) []byte { return input }
	case "contract":
		pk := mustKey(keys[0].PrivateKey)
		nonce, err := cl.PendingNonceAt(ctx, crypto.PubkeyToAddress(pk.PublicKey))
		must(err)
		tx := sign(types.NewTx(&types.DynamicFeeTx{ChainID: chainID, Nonce: nonce, GasTipCap: gp, GasFeeCap: fee, Gas: 300000, Value: big.NewInt(0), Data: sstoreInit}), chainID, pk)
		must(cl.SendTransaction(ctx, tx))
		var rc *types.Receipt
		for i := 0; i < 60 && rc == nil; i++ {
			time.Sleep(time.Second)
			rc, _ = cl.TransactionReceipt(ctx, tx.Hash())
		}
		if rc == nil || rc.ContractAddress == (common.Address{}) || rc.Status != 1 {
			must(fmt.Errorf("contract deploy failed: %+v", rc))
		}
		to, gas, value = rc.ContractAddress, 120000, big.NewInt(0)
		fmt.Println("contract at", to.Hex())
		data = func(i, k int) []byte {
			w := new(big.Int).SetUint64(uint64(*skip+i)<<20 | uint64(k) + 1)
			return common.LeftPadBytes(w.Bytes(), 32)
		}
	default:
		must(fmt.Errorf("unknown mode %q", *mode))
	}

	var sent, failed atomic.Int64
	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for i := range keys {
		i := i
		pk := mustKey(keys[i].PrivateKey)
		from := crypto.PubkeyToAddress(pk.PublicKey)
		nonce, err := cl.PendingNonceAt(ctx, from)
		must(err)
		if *mode == "contract" && i == 0 {
			nonce++ // the deploy above used the first nonce (pending nonce may not include it yet)
			if cur, err := cl.PendingNonceAt(ctx, from); err == nil && cur > nonce {
				nonce = cur
			}
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			for k := 0; k < *per; k++ {
				tx := sign(types.NewTx(&types.DynamicFeeTx{ChainID: chainID, Nonce: nonce + uint64(k), GasTipCap: gp, GasFeeCap: fee, Gas: gas, To: &to, Value: value, Data: data(i, k)}), chainID, pk)
				if err := cl.SendTransaction(ctx, tx); err != nil {
					failed.Add(1)
					continue
				}
				sent.Add(1)
			}
		}()
	}
	wg.Wait()
	fmt.Printf("mode=%s accounts=%d sent=%d send_errors=%d\n", *mode, len(keys), sent.Load(), failed.Load())
	time.Sleep(time.Duration(*wait) * time.Second)
}

func sign(tx *types.Transaction, chainID *big.Int, pk *ecdsa.PrivateKey) *types.Transaction {
	s, err := types.SignTx(tx, types.LatestSignerForChainID(chainID), pk)
	must(err)
	return s
}

func mustKey(h string) *ecdsa.PrivateKey {
	pk, err := crypto.HexToECDSA(strings.TrimPrefix(h, "0x"))
	must(err)
	return pk
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func callMsg(to common.Address, data []byte) ethereum.CallMsg {
	return ethereum.CallMsg{To: &to, Data: data}
}
