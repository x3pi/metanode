// test_account_model verifies, on a live parent-chain cluster started with the matching genesis, the production
// float model: genesis balances, plain BLS-to-BLS transfers, the 1000-unit cluster registration rule, and no mint.
//
//	go run ./cmd/tool/test_account_model -print-genesis   # prints the genesis fields to merge into parent_genesis.json
//	go run ./cmd/tool/test_account_model                  # runs the checks against 127.0.0.1:18601-18604
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"google.golang.org/protobuf/proto"
)

// Fixed, public, test-only secrets so the genesis and this tool agree on the keys. Never use them for real value.
var secrets = map[string]string{
	"A": "1111111111111111111111111111111111111111111111111111111111111111", // 1500 units at genesis
	"B": "2222222222222222222222222222222222222222222222222222222222222222", // 10 units at genesis
	"C": "3333333333333333333333333333333333333333333333333333333333333333", // nothing at genesis
}

var nodes = []string{"http://127.0.0.1:18601", "http://127.0.0.1:18602", "http://127.0.0.1:18603", "http://127.0.0.1:18604"}

func unit(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

type acct struct {
	priv cm.PrivateKey
	pub  cm.PublicKey
	addr common.Address
}

func load(name string) acct {
	p, pub, a := bls.GenerateKeyPairFromSecretKey(secrets[name])
	return acct{p, pub, a}
}

func fail(format string, a ...interface{}) {
	fmt.Printf("❌ "+format+"\n", a...)
	os.Exit(1)
}

func getJSON(url string, out interface{}) {
	r, err := http.Get(url)
	if err != nil {
		fail("GET %s: %v", url, err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(b, out); err != nil {
		fail("GET %s: bad JSON %q", url, string(b))
	}
}

func balance(node string, k cm.PublicKey) *big.Int {
	var d struct {
		Balance string `json:"balance"`
	}
	getJSON(fmt.Sprintf("%s/float?pubkey=%s", node, hex.EncodeToString(k[:])), &d)
	v, _ := new(big.Int).SetString(d.Balance, 10)
	return v
}

func supply(node string) *big.Int {
	var d struct {
		Total string `json:"total_supply"`
	}
	getJSON(fmt.Sprintf("%s/float?pubkey=%s", node, hex.EncodeToString(make([]byte, 48))), &d)
	v, _ := new(big.Int).SetString(d.Total, 10)
	return v
}

func nonce(a acct) uint64 {
	var d struct {
		Nonce uint64 `json:"nonce"`
	}
	getJSON(fmt.Sprintf("%s/nonce?address=%s", nodes[0], a.addr.Hex()), &d)
	return d.Nonce
}

// send submits the tx to node 0 and waits for its receipt on every node (they must all agree).
func send(a acct, data []byte) (status uint8, code uint32) {
	tx, err := parentchain.BuildAndSignBLSTx(a.priv, a.pub, parentchain.ParentChainGatewayAddress, nonce(a), data)
	if err != nil {
		fail("build tx: %v", err)
	}
	raw, _ := proto.Marshal(tx)
	r, err := http.Post(nodes[0]+"/send_raw_transaction", "application/octet-stream", bytes.NewReader(raw))
	if err != nil {
		fail("send: %v", err)
	}
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 {
		fail("send rejected at admission: HTTP %d %s", r.StatusCode, string(body))
	}
	h := parentchain.ComputeTxHash(tx)
	deadline := time.Now().Add(60 * time.Second)
	var first *parentchain.Receipt
	for _, n := range nodes {
		for {
			var d struct {
				Receipt *parentchain.Receipt `json:"receipt"`
				Found   bool                 `json:"found"`
			}
			getJSON(fmt.Sprintf("%s/receipt?hash=%s", n, h.Hex()), &d)
			if d.Found && d.Receipt != nil {
				if first == nil {
					first = d.Receipt
				} else if first.Status != d.Receipt.Status || first.ErrorCode != d.Receipt.ErrorCode {
					fail("nodes disagree on the receipt of %s", h.Hex())
				}
				break
			}
			if time.Now().After(deadline) {
				fail("no receipt for %s on %s", h.Hex(), n)
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
	return first.Status, uint32(first.ErrorCode)
}

func expectBalance(label string, k cm.PublicKey, want *big.Int) {
	for _, n := range nodes {
		if got := balance(n, k); got.Cmp(want) != 0 {
			fail("%s on %s = %s, want %s", label, n, got, want)
		}
	}
	fmt.Printf("  ✅ %s = %s on all 4 nodes\n", label, want)
}

func expectSupply(want *big.Int) {
	for _, n := range nodes {
		if got := supply(n); got.Cmp(want) != 0 {
			fail("total supply on %s = %s, want %s", n, got, want)
		}
	}
	fmt.Printf("  ✅ total supply = %s (unchanged)\n", want)
}

func parity() {
	var first struct {
		LastBlock uint64 `json:"last_block"`
		Root      string `json:"state_root"`
	}
	for i, n := range nodes {
		var st struct {
			LastBlock uint64 `json:"last_block"`
			Root      string `json:"state_root"`
		}
		for t := 0; t < 60; t++ {
			getJSON(n+"/status", &st)
			if i == 0 || (st.LastBlock == first.LastBlock && st.Root == first.Root) {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if i == 0 {
			first = st
		} else if st.LastBlock != first.LastBlock || st.Root != first.Root {
			fail("node %d differs: block %d root %s vs block %d root %s", i, st.LastBlock, st.Root, first.LastBlock, first.Root)
		}
	}
	fmt.Printf("  ✅ 4-node parity at block #%d, state_root %s\n", first.LastBlock, first.Root[:18])
}

func main() {
	printGenesis := flag.Bool("print-genesis", false, "print the genesis fields for the test keys")
	flag.Parse()
	A, B, C := load("A"), load("B"), load("C")
	if *printGenesis {
		out := map[string]interface{}{
			"open_cluster_registration": false,
			"clusters":                  []string{},
			"allow_deposit_to_float":    false,
			"min_float_to_register":     unit(1000).String(),
			"float_accounts": []map[string]string{
				{"bls_public_key": hex.EncodeToString(A.pub[:]), "balance": unit(1500).String()},
				{"bls_public_key": hex.EncodeToString(B.pub[:]), "balance": unit(10).String()},
			},
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return
	}

	fmt.Println("[1] Genesis float exists and the supply equals it")
	expectBalance("A", A.pub, unit(1500))
	expectBalance("B", B.pub, unit(10))
	expectBalance("C", C.pub, big.NewInt(0))
	total := unit(1510)
	expectSupply(total)

	fmt.Println("[2] Plain transfer A -> C (300 units)")
	if s, c := send(A, parentchain.EncodeTransferBalanceCallData(C.pub, unit(300))); s != 1 {
		fail("transfer A->C failed: status %d code %d", s, c)
	}
	expectBalance("A", A.pub, unit(1200))
	expectBalance("C", C.pub, unit(300))
	expectSupply(total)

	fmt.Println("[3] Overdraft is rejected and changes nothing")
	if s, c := send(B, parentchain.EncodeTransferBalanceCallData(A.pub, unit(11))); s != 0 || c != 224 {
		fail("overdraft: status %d code %d, want 0/224", s, c)
	}
	expectBalance("B", B.pub, unit(10))
	expectBalance("A", A.pub, unit(1200))

	fmt.Println("[4] C (300 units) cannot register as a cluster: below the 1000-unit rule")
	if s, c := send(C, parentchain.EncodeRegisterClusterCallData(C.pub, 77)); s != 0 || c != 221 {
		fail("register below threshold: status %d code %d, want 0/221", s, c)
	}
	fmt.Println("  ✅ rejected with 221")

	fmt.Println("[5] After A -> C (800 units), C holds 1100 and CAN register")
	if s, c := send(A, parentchain.EncodeTransferBalanceCallData(C.pub, unit(800))); s != 1 {
		fail("transfer A->C(800) failed: status %d code %d", s, c)
	}
	expectBalance("C", C.pub, unit(1100))
	if s, c := send(C, parentchain.EncodeRegisterClusterCallData(C.pub, 77)); s != 1 {
		fail("register at/above threshold: status %d code %d", s, c)
	}
	fmt.Println("  ✅ C registered as a cluster")

	fmt.Println("[6] No mint: even a registered cluster cannot depositToFloat")
	msg := common.HexToHash("0xabc123")
	amt := unit(5)
	cert := bls.Sign(C.priv, parentchain.ComputeDepositFloatMessage(C.pub, 77, common.Address{}, common.Address{}, amt, msg))
	if s, c := send(C, parentchain.EncodeDepositToFloatCallData(C.pub, C.pub, 77, common.Address{}, common.Address{}, amt, msg, cert)); s != 0 || c != 222 {
		fail("deposit: status %d code %d, want 0/222", s, c)
	}
	expectSupply(total)

	fmt.Println("[7] Everything above is bit-identical on all nodes")
	parity()
	fmt.Println("\n🎉 ACCOUNT MODEL VERIFIED LIVE: genesis float, BLS transfers, 1000-unit registration, fixed supply")
}
