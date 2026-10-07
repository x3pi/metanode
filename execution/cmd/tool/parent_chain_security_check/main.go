// parent_chain_security_check attacks a RUNNING parent chain with the forgeries that the removed legacy paths
// used to accept, and fails (exit 1) unless every attack is rejected and no float is minted.
//
//	go run ./cmd/tool/parent_chain_security_check -url http://127.0.0.1:8547
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

var failed bool

func check(name string, ok bool, detail string) {
	if ok {
		fmt.Printf("  ✅ %s\n", name)
		return
	}
	failed = true
	fmt.Printf("  ❌ %s — %s\n", name, detail)
}

func main() {
	url := flag.String("url", "http://127.0.0.1:8547", "parent chain node URL")
	expectClosed := flag.Bool("expect-closed", false, "the chain runs a production genesis: an unauthorized registerCluster must be rejected")
	flag.Parse()
	c := parentchain.NewHTTPClient(*url)

	fmt.Println("[1] The JSON transaction path is gone")
	resp, err := http.Post(*url+"/tx", "application/json", bytes.NewReader([]byte(`{"type":"DepositToFloat","amount":1000000}`)))
	if err == nil {
		resp.Body.Close()
	}
	check("POST /tx is rejected", err == nil && resp.StatusCode == http.StatusMethodNotAllowed, fmt.Sprintf("status=%v err=%v", statusOf(resp), err))

	resp, err = http.Post(*url+"/send_raw_transaction", "application/json", bytes.NewReader([]byte(`{"raw_tx":"0x0a0b"}`)))
	if err == nil {
		resp.Body.Close()
	}
	check("JSON body on /send_raw_transaction is rejected", err == nil && resp.StatusCode != http.StatusOK, fmt.Sprintf("status=%v err=%v", statusOf(resp), err))

	fmt.Println("[2] Forged deposits (mint attempts) are rejected by every node's execution")
	// The attacker is anyone: a fresh BLS key that self-registers an account (the same bootstrap every exec node
	// uses), so its transactions carry a valid signature over their hash and a valid nonce. The only thing wrong
	// with the deposits below is that the attacker is not an authorized, registered source cluster.
	atk := bls.GenerateKeyPair()
	atkAddr := atk.Address()
	victim := bls.GenerateKeyPair().PublicKey()
	amount := big.NewInt(1_000_000_000_000_000_000)
	other := bls.GenerateKeyPair()

	regDigest := parentchain.ComputeRegisterAccountMessage(atkAddr, atk.PublicKey())
	regTx, err := parentchain.BuildAndSignBLSTx(atk.PrivateKey(), atk.PublicKey(), parentchain.ParentChainGatewayAddress, 0,
		parentchain.EncodeRegisterAccountCallData(atkAddr, atk.PublicKey(), nil, bls.Sign(atk.PrivateKey(), regDigest)))
	if err != nil {
		fmt.Println("build register tx:", err)
		os.Exit(1)
	}
	regRaw, _ := proto.Marshal(regTx)
	regHash, err := c.SendRawTransaction(regRaw)
	check("attacker self-registers an ordinary account (allowed)", err == nil, fmt.Sprint(err))
	if rc, found := waitReceipt(c, regHash); !found || rc.Status != 1 {
		check("attacker registration committed", false, fmt.Sprintf("found=%v rc=%+v", found, rc))
		os.Exit(1)
	}

	var zero [48]byte
	idA := crypto.Keccak256Hash([]byte(fmt.Sprintf("forged-a-%d", time.Now().UnixNano())))
	idB := crypto.Keccak256Hash([]byte(fmt.Sprintf("forged-b-%d", time.Now().UnixNano())))
	idC := crypto.Keccak256Hash([]byte(fmt.Sprintf("forged-c-%d", time.Now().UnixNano())))
	digB := parentchain.ComputeDepositFloatMessage(victim, 101, atkAddr, atkAddr, amount, idB)
	digC := parentchain.ComputeDepositFloatMessage(victim, 101, atkAddr, atkAddr, amount, idC)
	attacks := []struct {
		name string
		data []byte
	}{
		{"deposit with NO source cluster (zero key)", parentchain.EncodeDepositToFloatCallData(zero, victim, 101, atkAddr, atkAddr, amount, idA, bls.Sign(other.PrivateKey(), []byte("x")))},
		{"deposit from an UNREGISTERED source with its own valid certificate", parentchain.EncodeDepositToFloatCallData(atk.PublicKey(), victim, 101, atkAddr, atkAddr, amount, idB, bls.Sign(atk.PrivateKey(), digB))},
		{"deposit naming another key as source, certificate forged by the attacker", parentchain.EncodeDepositToFloatCallData(other.PublicKey(), victim, 101, atkAddr, atkAddr, amount, idC, bls.Sign(atk.PrivateKey(), digC))},
	}
	var hashes []common.Hash
	for i, a := range attacks {
		tx, _ := parentchain.BuildAndSignBLSTx(atk.PrivateKey(), atk.PublicKey(), parentchain.ParentChainGatewayAddress, uint64(i+1), a.data)
		raw, _ := proto.Marshal(tx)
		got, err := c.SendRawTransaction(raw)
		check(fmt.Sprintf("attack %d submitted to ingress (%s)", i+1, a.name), err == nil, fmt.Sprint(err))
		hashes = append(hashes, got)
	}
	// A deposit with no transaction signature at all (the old "authenticated at the API boundary" mode).
	unsigned := &pb.Transaction{
		FromAddress: atkAddr.Bytes(),
		ToAddress:   parentchain.ParentChainGatewayAddress.Bytes(),
		Nonce:       []byte{0, 0, 0, 0, 0, 0, 0, 4},
		Data:        attacks[0].data,
		ChainID:     parentchain.ParentChainID,
	}
	rawU, _ := proto.Marshal(unsigned)
	hu, err := c.SendRawTransaction(rawU)
	check("unsigned deposit submitted to ingress", err == nil, fmt.Sprint(err))
	hashes = append(hashes, hu)

	fmt.Println("[3] Every forged transaction ends with a FAILED receipt (status 0) and no float exists for the victim")
	for i, h := range hashes {
		rc, found := waitReceipt(c, h)
		check(fmt.Sprintf("forged tx %d has a receipt", i+1), found, "not included in a block within 20s")
		if found {
			check(fmt.Sprintf("forged tx %d rejected (status=%d code=%d)", i+1, rc.Status, rc.ErrorCode), rc.Status == 0, "ACCEPTED — float may have been minted")
		}
	}
	key := parentchain.TreeKey(parentchain.NamespaceFloat, crypto.Keccak256Hash(victim[:]).Bytes())
	pr, err := c.GetProof(key)
	check("victim has no float entry (verified proof of absence)", err == nil && pr.Verified, fmt.Sprintf("err=%v verified=%v", err, pr.Verified))

	fmt.Println("[4] Cluster registration policy (a registered cluster's certificate authorizes deposits)")
	rogue := bls.GenerateKeyPair()
	rogueTx, _ := parentchain.BuildAndSignBLSTx(rogue.PrivateKey(), rogue.PublicKey(), parentchain.ParentChainGatewayAddress, 0,
		parentchain.EncodeRegisterClusterCallData(rogue.PublicKey(), 424242))
	rogueRaw, _ := proto.Marshal(rogueTx)
	rogueHash, err := c.SendRawTransaction(rogueRaw)
	check("rogue registerCluster submitted to ingress", err == nil, fmt.Sprint(err))
	if rc, found := waitReceipt(c, rogueHash); found {
		if *expectClosed {
			check(fmt.Sprintf("rogue registerCluster rejected by the genesis allow-list (code=%d)", rc.ErrorCode), rc.Status == 0 && rc.ErrorCode == 221, fmt.Sprintf("status=%d code=%d", rc.Status, rc.ErrorCode))
		} else if rc.Status == 1 {
			fmt.Println("  ⚠️  open_cluster_registration is ON: any key can register as a cluster and then mint float (devnet only)")
		} else {
			fmt.Printf("  ✅ rogue registerCluster rejected (code=%d)\n", rc.ErrorCode)
		}
	} else {
		check("rogue registerCluster has a receipt", false, "not included in a block within 20s")
	}

	if failed {
		fmt.Println("\n❌ SECURITY CHECK FAILED")
		os.Exit(1)
	}
	fmt.Println("\n✅ all forgeries rejected")
}

func statusOf(r *http.Response) interface{} {
	if r == nil {
		return nil
	}
	_, _ = io.Copy(io.Discard, r.Body)
	return r.StatusCode
}

func waitReceipt(c interface {
	GetReceipt(common.Hash) (*parentchain.Receipt, bool, error)
}, h common.Hash) (*parentchain.Receipt, bool) {
	for t := 0; t < 40; t++ {
		if rc, found, _ := c.GetReceipt(h); found {
			return rc, true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return nil, false
}
