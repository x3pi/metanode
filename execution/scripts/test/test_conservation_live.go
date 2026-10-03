package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/common/hexutil"
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
		return nil, fmt.Errorf("json unmarshal failed: %w (body: %s)", err, string(body))
	}
	if errResp, ok := res["error"]; ok {
		return nil, fmt.Errorf("rpc error: %v", errResp)
	}
	return res, nil
}

type ConservationStatus struct {
	Mode       string `json:"mode"`
	Verified   bool   `json:"verified"`
	Blocked    bool   `json:"blocked"`
	Allow      bool   `json:"allow"`
	OK         bool   `json:"ok"`
	Stable     bool   `json:"stable"`
	Supply     string `json:"supply"`
	Float      string `json:"float"`
	Diff       string `json:"diff"`
	Pending    string `json:"pending"`
	Reason     string `json:"reason"`
	HaltReason string `json:"halt_reason"`
}

func getConservation(url string) (*ConservationStatus, error) {
	res, err := rpcCall(url, "mtn_getConservation", []interface{}{})
	if err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(res["result"])
	var st ConservationStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func main() {
	exec1URL := "http://127.0.0.1:8646"
	exec2URL := "http://127.0.0.1:8647"
	parentURLs := []string{"http://127.0.0.1:8547", "http://127.0.0.1:18602", "http://127.0.0.1:18603", "http://127.0.0.1:18604"}

	fmt.Println("================================================================================")
	fmt.Println("🧪 LIVE VERIFICATION: BLS CONSERVATION GUARD (P9.2)")
	fmt.Println("================================================================================")

	// Step 1: Initial state check
	fmt.Println("\n--- BƯỚC 1: Kiểm tra trạng thái ban đầu của Exec 1 & Exec 2 ---")
	st1, err := getConservation(exec1URL)
	if err != nil {
		fmt.Printf("❌ Lỗi lấy mtn_getConservation trên Exec 1: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Exec 1: mode=%s, verified=%v, ok=%v, blocked=%v, allow=%v, diff=%s, pending=%s\n",
		st1.Mode, st1.Verified, st1.OK, st1.Blocked, st1.Allow, st1.Diff, st1.Pending)
	fmt.Printf("       supply=%s, float=%s\n", st1.Supply, st1.Float)

	if !st1.OK || st1.Blocked || !st1.Allow || st1.Diff != "0" {
		fmt.Printf("❌ Exec 1 không ở trạng thái bảo toàn hoàn hảo lúc nghỉ: diff=%s ok=%v\n", st1.Diff, st1.OK)
		os.Exit(1)
	}
	fmt.Println("✅ PASS: Exec 1 bảo toàn hoàn hảo (diff = 0, ok = true, blocked = false).")

	st2, err := getConservation(exec2URL)
	if err != nil {
		fmt.Printf("❌ Lỗi lấy mtn_getConservation trên Exec 2: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Exec 2: mode=%s, verified=%v, ok=%v, blocked=%v, allow=%v, diff=%s, pending=%s\n",
		st2.Mode, st2.Verified, st2.OK, st2.Blocked, st2.Allow, st2.Diff, st2.Pending)
	if !st2.OK || st2.Blocked || !st2.Allow || st2.Diff != "0" {
		fmt.Printf("❌ Exec 2 không ở trạng thái bảo toàn hoàn hảo lúc nghỉ: diff=%s ok=%v\n", st2.Diff, st2.OK)
		os.Exit(1)
	}
	fmt.Println("✅ PASS: Exec 2 bảo toàn hoàn hảo (diff = 0, ok = true, blocked = false).")

	// Setup Parent Chain Client using exec2's registered cluster key
	exec2PrivHex := "2a61eac9235fab64ae377b2b7e39f8fa9648c5737094d28c7ee68a14b5086d39"
	exec2Priv, exec2PubKey, _ := bls.GenerateKeyPairFromSecretKey(exec2PrivHex)

	parentClient := parentchain.NewQuorumClient(parentURLs, exec2Priv, exec2PubKey)

	// Step 2: Register target address on Exec 2
	fmt.Println("\n--- BƯỚC 2: Đăng ký tài khoản nhận trên Exec 2 ---")
	targetPriv, _ := crypto.GenerateKey()
	targetAddr := crypto.PubkeyToAddress(targetPriv.PublicKey)
	fmt.Printf("Tài khoản đích: %s (thuộc Exec 2: %x)\n", targetAddr.Hex(), exec2PubKey[:6])

	regDigest := parentchain.ComputeRegisterAccountMessage(targetAddr, exec2PubKey)
	userSig, _ := crypto.Sign(crypto.Keccak256(regDigest), targetPriv)
	clusterSig := bls.Sign(exec2Priv, regDigest)

	msgID, err := parentClient.SendRegisterAccount(targetAddr, exec2PubKey, userSig, clusterSig)
	if err != nil {
		fmt.Printf("❌ SendRegisterAccount failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("RegisterAccount submitted: MsgID = %s. Chờ xác nhận trên Parent Chain...\n", msgID.Hex())

	registered := false
	for i := 0; i < 20; i++ {
		time.Sleep(1 * time.Second)
		fetched, found, err := parentClient.GetAccountRegistry(targetAddr)
		if err == nil && found && fetched == exec2PubKey {
			registered = true
			break
		}
	}
	if !registered {
		fmt.Println("❌ Không thể đăng ký tài khoản trên Parent Chain sau 20s")
		os.Exit(1)
	}
	fmt.Println("✅ PASS: Tài khoản đích đã đăng ký thành công vào Exec 2 trên Parent Chain.")

	// Step 3: Send Cross-Chain Transfer from Exec 1 to Exec 2 under ENFORCE mode
	fmt.Println("\n--- BƯỚC 3: Gửi giao dịch Cross-Chain Transfer ở chế độ ENFORCE ---")
	transferAmount := big.NewInt(5000000000000000) // 0.005 MTN
	transferAmountHex := hexutil.EncodeBig(transferAmount)
	fmt.Printf("Gửi %s wei từ Exec 1 sang Exec 2...\n", transferAmount.String())

	sendRes, err := rpcCall(exec1URL, "mtn_sendCrossChainTransfer", []interface{}{targetAddr.Hex(), transferAmountHex})
	if err != nil {
		fmt.Printf("❌ mtn_sendCrossChainTransfer thất bại: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Tx đã gửi: TxHash = %v\n", sendRes["result"])

	// Monitor conservation during flight
	fmt.Println("Theo dõi mtn_getConservation trên Exec 1 trong khi chuyển tiền đang bay:")
	startWait := time.Now()
	observedInFlight := false
	settled := false

	for time.Since(startWait) < 60*time.Second {
		st, err := getConservation(exec1URL)
		if err == nil {
			diffBig, _ := new(big.Int).SetString(st.Diff, 10)
			pendingBig, _ := new(big.Int).SetString(st.Pending, 10)

			// Invariant verification: diff must be in [0, pending]
			if diffBig.Sign() < 0 {
				fmt.Printf("🚨 VI PHẠM BẤT BIẾN: diff < 0! (diff=%s, pending=%s)\n", st.Diff, st.Pending)
				os.Exit(1)
			}
			if diffBig.Cmp(pendingBig) > 0 {
				fmt.Printf("🚨 VI PHẠM BẤT BIẾN: diff > pending! (diff=%s, pending=%s)\n", st.Diff, st.Pending)
				os.Exit(1)
			}
			if diffBig.Sign() > 0 || pendingBig.Sign() > 0 {
				observedInFlight = true
				fmt.Printf("  ⏳ [IN-FLIGHT] ok=%v, blocked=%v, diff=%s, pending=%s (thỏa mãn 0 <= diff <= pending)\n",
					st.OK, st.Blocked, st.Diff, st.Pending)
			} else {
				if observedInFlight {
					fmt.Printf("  🏁 [SETTLED]   ok=%v, blocked=%v, diff=%s, pending=%s\n", st.OK, st.Blocked, st.Diff, st.Pending)
					settled = true
					break
				}
			}
		}
		time.Sleep(1 * time.Second)
	}

	if !settled {
		st, _ := getConservation(exec1URL)
		if st.Diff == "0" && st.OK {
			settled = true
		}
	}

	// Verify balance credited on Exec 2
	balRes, err := rpcCall(exec2URL, "eth_getBalance", []interface{}{targetAddr.Hex(), "latest"})
	if err != nil {
		fmt.Printf("❌ Lỗi lấy số dư trên Exec 2: %v\n", err)
		os.Exit(1)
	}
	balHex := balRes["result"].(string)
	balBig, _ := hexutil.DecodeBig(balHex)
	fmt.Printf("Số dư nhận được trên Exec 2: %s wei (kỳ vọng: %s wei)\n", balBig.String(), transferAmount.String())
	if balBig.Cmp(transferAmount) != 0 {
		fmt.Printf("❌ Số dư không khớp!\n")
		os.Exit(1)
	}
	fmt.Println("✅ PASS: Giao dịch cross-chain hoàn tất trọn vẹn, bất biến [0 <= diff <= pending] luôn được bảo toàn!")

	// Final rest check on Exec 1 & Exec 2
	st1Final, _ := getConservation(exec1URL)
	st2Final, _ := getConservation(exec2URL)
	fmt.Printf("Exec 1 lúc nghỉ: diff=%s, ok=%v, blocked=%v\n", st1Final.Diff, st1Final.OK, st1Final.Blocked)
	fmt.Printf("Exec 2 lúc nghỉ: diff=%s, ok=%v, blocked=%v\n", st2Final.Diff, st2Final.OK, st2Final.Blocked)

	if st1Final.Diff != "0" || !st1Final.OK || st2Final.Diff != "0" || !st2Final.OK {
		fmt.Println("❌ Trạng thái lúc nghỉ không bảo toàn hoàn hảo!")
		os.Exit(1)
	}
	fmt.Println("✅ PASS: Cả 2 cụm đều trở về trạng thái nghỉ tuyệt đối: diff = 0, ok = true.")

	fmt.Println("\n================================================================================")
	fmt.Println("🎉 HOÀN THÀNH KIỂM CHỨNG BẢO TOÀN SỐNG TRÊN CỤM (P9.2) THÀNH CÔNG RỰC RỠ!")
	fmt.Println("================================================================================")
}
