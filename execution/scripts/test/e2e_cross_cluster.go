//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

func rpcCall(url, method string, params []interface{}) (map[string]interface{}, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "method": method, "params": params, "id": 1,
	})
	resp, err := http.Post(url, "application/json", bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var res map[string]interface{}
	if err := json.Unmarshal(body, &res); err != nil {
		return nil, err
	}
	if errResp, ok := res["error"]; ok {
		return nil, fmt.Errorf("rpc error: %v", errResp)
	}
	return res, nil
}

func main() {
	parentClient := parentchain.NewHTTPClient("http://127.0.0.1:8547")

	// exec1/exec2's real FloatIdentityKey is app.keyPair.PublicKey() (cmd/simple_chain/app_network.go:
	// app.keyPair = bls.NewKeyPair(config.PrivateKey)) — the config.json TOP-LEVEL "private_key"
	// field, NOT Databases.BLSPrivateKey (a separate, unrelated key used only for the local
	// blsKeyStore/MetaTx-signing path). Using the wrong field here silently registers/bootstraps
	// a pubkey that exec1/exec2's SendWorker/CrossNodeHandler/ReceiveWorker never actually use,
	// so RPC calls "succeed" but never correspond to the real running identity (found live: two
	// full rounds of registering the "wrong" exec2 pubkey before catching this).
	exec2Hex := "2a61eac9235fab64ae377b2b7e39f8fa9648c5737094d28c7ee68a14b5086d39"
	exec2Priv, exec2PubKey, _ := bls.GenerateKeyPairFromSecretKey(exec2Hex)

	exec1Hex := "0f326c0b9bb86353ac317dd8f9b045fd1877473674ba24500139fed777b26a0c"
	_, exec1PubKey, _ := bls.GenerateKeyPairFromSecretKey(exec1Hex)

	// Bootstrap: TransferFloat refuses to lazy-create a SOURCE chain's ChainRegistry entry
	// ("no lazy create for source, since it has funds" — pkg/parentchain/state.go), and this
	// entry is only ever created as a side effect of DepositToFloat (the RECEIVING side, see
	// ensureChainRegistry's call sites). A brand-new devnet chain identity like exec1 here has
	// never been the destination of a prior deposit, so without this step step 3 below fails
	// with "TransferFloat: unknown source chain" forever. DepositToFloat itself is deliberately
	// permissionless at the RPC layer (no cert check — "Gateway handles it", per handleTx) so
	// this is a legitimate bootstrap call, not a workaround.
	bootstrapAddr := common.HexToAddress("0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5") // exec1's own address
	fmt.Println("0. Bootstrapping exec1's own ChainRegistry entry via DepositToFloat...")
	depMsgID, err := parentClient.SendDepositToFloat(exec1PubKey, 1, bootstrapAddr, bootstrapAddr, big.NewInt(1_000_000_000_000_000_000))
	if err != nil {
		fmt.Printf("   DepositToFloat failed: %v\n", err)
		return
	}
	fmt.Printf("   Bootstrap deposit submitted: %s\n", depMsgID.Hex())

	// A fresh target address that we register as belonging to exec2's cluster.
	targetPriv, _ := crypto.GenerateKey()
	targetAddr := crypto.PubkeyToAddress(targetPriv.PublicKey)

	fmt.Printf("1. Registering target %s to exec2's FloatIdentityKey %x...\n", targetAddr.Hex(), exec2PubKey[:6])
	digest := parentchain.ComputeRegisterAccountMessage(targetAddr, exec2PubKey)
	userSig, _ := crypto.Sign(crypto.Keccak256(digest), targetPriv)
	clusterSig := bls.Sign(exec2Priv, digest)
	msgID, err := parentClient.SendRegisterAccount(targetAddr, exec2PubKey, userSig, clusterSig)
	if err != nil {
		fmt.Printf("RegisterAccount failed: %v\n", err)
		return
	}
	fmt.Printf("   RegisterAccount submitted: %s\n", msgID.Hex())

	fmt.Println("2. Confirming Account Registry lookup resolves to exec2...")
	fetched, found, err := parentClient.GetAccountRegistry(targetAddr)
	if err != nil || !found {
		fmt.Printf("   NOT FOUND (err=%v) -- registration did not land yet\n", err)
		return
	}
	if fetched != exec2PubKey {
		fmt.Printf("   MISMATCH: registry has %x, expected %x\n", fetched[:6], exec2PubKey[:6])
		return
	}
	fmt.Println("   OK -- registry correctly points at exec2.")

	fmt.Printf("3. exec1 (RPC :8646) sends cross-node TransferFloat of 777 to %s...\n", targetAddr.Hex())
	res, err := rpcCall("http://localhost:8646", "mtn_sendCrossChainTransfer",
		[]interface{}{targetAddr.Hex(), "0x309"}) // 0x309 = 777
	if err != nil {
		fmt.Printf("   send failed: %v\n", err)
		return
	}
	fmt.Printf("   submitted, MessageID: %v\n", res["result"])

	fmt.Println("4. Polling exec2 (RPC :8647) for the credited balance (up to 60s)...")
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		res2, err := rpcCall("http://localhost:8647", "mtn_getAccountState", []interface{}{targetAddr.Hex(), "latest"})
		if err == nil {
			if m, ok := res2["result"].(map[string]interface{}); ok && m != nil {
				if balStr, ok := m["balance"].(string); ok {
					bal := new(big.Int)
					// mtn_getAccountState's "balance" is big.Int.String() (decimal), NOT a
					// 0x-prefixed hex string like most other RPC fields in this codebase --
					// parsing it as base 16 silently mangled the reported amount (777 decimal
					// read back as "1911" here, since "777" is also a valid hex literal).
					bal.SetString(balStr, 10)
					if bal.Sign() > 0 {
						fmt.Printf("   SUCCESS: exec2 balance for %s = %s\n", targetAddr.Hex(), bal.String())
						return
					}
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Println("   TIMEOUT: balance never appeared on exec2 within 60s")
	_ = common.Address{}
}

func trimHex(s string) string {
	if len(s) >= 2 && s[0:2] == "0x" {
		return s[2:]
	}
	return s
}
