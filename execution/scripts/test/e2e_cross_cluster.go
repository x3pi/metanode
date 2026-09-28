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

	// exec2's own FloatIdentityKey (Databases.BLSPrivateKey in devnet_data/exec2/config.json).
	exec2BLSHex := "2d21f977fd594b7587439a91ab1bb5523573f02e5ba7e3e61cd8e895470d400a"
	exec2BLSPriv, exec2PubKey, _ := bls.GenerateKeyPairFromSecretKey(exec2BLSHex)

	// A fresh target address that we register as belonging to exec2's cluster.
	targetPriv, _ := crypto.GenerateKey()
	targetAddr := crypto.PubkeyToAddress(targetPriv.PublicKey)

	fmt.Printf("1. Registering target %s to exec2's FloatIdentityKey %x...\n", targetAddr.Hex(), exec2PubKey[:6])
	digest := parentchain.ComputeRegisterAccountMessage(targetAddr, exec2PubKey)
	userSig, _ := crypto.Sign(crypto.Keccak256(digest), targetPriv)
	clusterSig := bls.Sign(exec2BLSPriv, digest)
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
					bal.SetString(trimHex(balStr), 16)
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
