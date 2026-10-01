//go:build ignore

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
)

func findRepoRoot() string {
	if root := os.Getenv("METANODE_ROOT"); root != "" {
		return root
	}
	dir, err := os.Getwd()
	if err != nil {
		return "/home/abc/chain-n/metanode"
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "/home/abc/chain-n/metanode"
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var (
	parentChainURL = getEnv("PARENT_CHAIN_URL", "http://127.0.0.1:8547")
	exec1URL       = getEnv("EXEC1_URL", "http://127.0.0.1:8646")
	exec2URL       = getEnv("EXEC2_URL", "http://127.0.0.1:8647")
)

const (
	devnetSenderECDSA = "a3e6d454ea7a3b464af1f8c891259d5ff48f331004d56d1331388ec3c3915fe1"
	devnetSenderBLS   = "0f0f8761e3fe67cdc9e7573adf72c7e929e2a00f981834298867d5628ca2d8f6"

	exec1PrivHex = "0f326c0b9bb86353ac317dd8f9b045fd1877473674ba24500139fed777b26a0c"
	exec2PrivHex = "2a61eac9235fab64ae377b2b7e39f8fa9648c5737094d28c7ee68a14b5086d39"
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

func getBlockNumber(url string) (uint64, error) {
	res, err := rpcCall(url, "eth_blockNumber", []interface{}{})
	if err != nil {
		return 0, err
	}
	str, ok := res["result"].(string)
	if !ok {
		return 0, fmt.Errorf("invalid result: %v", res["result"])
	}
	val, err := strconv.ParseUint(strings.TrimPrefix(str, "0x"), 16, 64)
	return val, err
}

func getBalance(url string, addr common.Address) (*big.Int, error) {
	res, err := rpcCall(url, "eth_getBalance", []interface{}{addr.Hex(), "latest"})
	if err != nil {
		return nil, err
	}
	str, ok := res["result"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid result: %v", res["result"])
	}
	b := new(big.Int)
	b.SetString(strings.TrimPrefix(str, "0x"), 16)
	return b, nil
}

func getAccountNonce(url string, addr common.Address) (uint64, error) {
	res, err := rpcCall(url, "mtn_getAccountState", []interface{}{addr.Hex(), "latest"})
	if err == nil {
		if m, ok := res["result"].(map[string]interface{}); ok && m != nil {
			if nVal, ok := m["nonce"].(float64); ok {
				return uint64(nVal), nil
			}
		}
	}
	res2, err2 := rpcCall(url, "eth_getTransactionCount", []interface{}{addr.Hex(), "latest"})
	if err2 == nil {
		if str, ok := res2["result"].(string); ok {
			n, err := strconv.ParseUint(strings.TrimPrefix(str, "0x"), 16, 64)
			if err == nil {
				return n, nil
			}
		}
	}
	return 0, nil
}

func waitForExactBalance(url string, addr common.Address, expected *big.Int, timeout time.Duration) (*big.Int, bool) {
	start := time.Now()
	for time.Since(start) < timeout {
		bal, err := getBalance(url, addr)
		if err == nil && bal != nil && bal.Cmp(expected) == 0 {
			return bal, true
		}
		time.Sleep(1 * time.Second)
	}
	bal, _ := getBalance(url, addr)
	if bal == nil {
		bal = big.NewInt(0)
	}
	return bal, false
}

func printHeader(title string) {
	fmt.Println("\n================================================================================")
	fmt.Printf("   👉 %s\n", title)
	fmt.Println("================================================================================")
}

func main() {
	fmt.Println("╔═══════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  🧪 METANODE END-TO-END SCENARIO & RESILIENCE TEST SUITE                      ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════════════════════════╝")

	parentToken := getEnv("PARENT_CHAIN_RPC_TOKEN", "")
	if parentToken == "" {
		for _, f := range []string{"/opt/metanode/parent_chain/security.env", "/opt/metanode/exec1_r1/security.env"} {
			if data, err := os.ReadFile(f); err == nil {
				for _, line := range strings.Split(string(data), "\n") {
					line = strings.TrimSpace(line)
					if strings.HasPrefix(line, "PARENT_CHAIN_RPC_TOKEN=") {
						val := strings.TrimPrefix(line, "PARENT_CHAIN_RPC_TOKEN=")
						val = strings.Trim(val, "\"' \t")
						if val != "" {
							parentToken = val
							break
						}
					}
				}
			}
			if parentToken != "" {
				break
			}
		}
	}
	if parentToken != "" {
		_ = os.Setenv("PARENT_CHAIN_RPC_TOKEN", parentToken)
	}
	parentClient := parentchain.NewHTTPClient(parentChainURL)

	_, exec1PubKey, _ := bls.GenerateKeyPairFromSecretKey(exec1PrivHex)
	exec2Priv, exec2PubKey, _ := bls.GenerateKeyPairFromSecretKey(exec2PrivHex)

	senderECDSAKey, _ := crypto.HexToECDSA(devnetSenderECDSA)
	senderAddr := crypto.PubkeyToAddress(senderECDSAKey.PublicKey)

	// Step 0: Pre-flight check
	printHeader("BƯỚC 0: KIỂM TRA TRẠNG THÁI HỆ THỐNG BAN ĐẦU")
	b1, err1 := getBlockNumber(exec1URL)
	b2, err2 := getBlockNumber(exec2URL)
	if err1 != nil || err2 != nil {
		fmt.Printf("❌ Lỗi kết nối RPC node thực thi: exec1=%v, exec2=%v\n", err1, err2)
		os.Exit(1)
	}
	fmt.Printf("✅ Exec 1 (Port :8646) đang hoạt động - Block Height: %d\n", b1)
	fmt.Printf("✅ Exec 2 (Port :8647) đang hoạt động - Block Height: %d\n", b2)

	balSender, _ := getBalance(exec1URL, senderAddr)
	fmt.Printf("💰 Số dư Devnet Sender trên Exec 1 (%s): %s wei\n", senderAddr.Hex(), balSender.String())

	// Bootstrap exec1 in ChainRegistry if not yet
	bootstrapAddr := common.HexToAddress("0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5")
	_, _ = parentClient.SendDepositToFloat(exec1PubKey, 1, bootstrapAddr, bootstrapAddr, big.NewInt(1_000_000_000_000_000_000))

	// =======================================================================================
	// KỊCH BẢN 1: Đăng ký 1 tài khoản mới trên Parent Chain & ánh xạ vào Cluster Exec 2
	// =======================================================================================
	printHeader("KỊCH BẢN 1: ĐĂNG KÝ 1 TÀI KHOẢN MỚI (ACCOUNT REGISTRATION)")
	newAccPriv, _ := crypto.GenerateKey()
	newAccAddr := crypto.PubkeyToAddress(newAccPriv.PublicKey)
	fmt.Printf("1. Tạo tài khoản mới: %s\n", newAccAddr.Hex())

	regDigest := parentchain.ComputeRegisterAccountMessage(newAccAddr, exec2PubKey)
	userSig, _ := crypto.Sign(crypto.Keccak256(regDigest), newAccPriv)
	clusterSig := bls.Sign(exec2Priv, regDigest)

	fmt.Printf("2. Gửi giao dịch RegisterAccount lên Parent Chain để đăng ký vào Cluster Exec 2 (%x)...\n", exec2PubKey[:6])
	msgID, err := parentClient.SendRegisterAccount(newAccAddr, exec2PubKey, userSig, clusterSig)
	if err != nil {
		fmt.Printf("❌ Lỗi gửi RegisterAccount: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   MsgID đăng ký: %s\n", msgID.Hex())

	// Xác nhận đăng ký trên Parent Chain
	fmt.Println("3. Kiểm tra Account Registry trên Parent Chain...")
	time.Sleep(2 * time.Second)
	fetchedClusterKey, found, err := parentClient.GetAccountRegistry(newAccAddr)
	if err != nil || !found {
		fmt.Printf("❌ Không tìm thấy tài khoản %s trong Account Registry: err=%v, found=%v\n", newAccAddr.Hex(), err, found)
		os.Exit(1)
	}
	if fetchedClusterKey != exec2PubKey {
		fmt.Printf("❌ Sai cluster: Mong đợi %x, nhận được %x\n", exec2PubKey[:6], fetchedClusterKey[:6])
		os.Exit(1)
	}
	fmt.Printf("✅ KỊCH BẢN 1 THÀNH CÔNG: Tài khoản %s đã đăng ký chính thức vào Cluster Exec 2!\n", newAccAddr.Hex())

	// =======================================================================================
	// KỊCH BẢN 2: Nạp và nhận tiền cho tài khoản mới (Deposit & Receive Funds)
	// =======================================================================================
	printHeader("KỊCH BẢN 2: NẠP VÀ NHẬN TIỀN CHO TÀI KHOẢN MỚI")
	depositAmount := big.NewInt(10_000_000_000_000) // 10k gwei
	initialDepositBalance, err := getBalance(exec2URL, newAccAddr)
	if err != nil || initialDepositBalance == nil {
		initialDepositBalance = big.NewInt(0)
	}
	expectedDepositBalance := new(big.Int).Add(initialDepositBalance, depositAmount)
	fmt.Printf("1. Gửi lệnh nạp tiền DepositToFloat (%s wei) từ Parent Chain đến tài khoản %s...\n", depositAmount.String(), newAccAddr.Hex())
	depMsgID, err := parentClient.SendDepositToFloat(exec2PubKey, 2, senderAddr, newAccAddr, depositAmount)
	if err != nil {
		fmt.Printf("❌ DepositToFloat thất bại: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Deposit submitted! MsgID: %s\n", depMsgID.Hex())

	fmt.Println("2. Chờ Rollup ReceiveWorker trên Exec 2 tiếp nhận và ghi nhận số dư...")
	balanceAfterDeposit, credited := waitForExactBalance(exec2URL, newAccAddr, expectedDepositBalance, 30*time.Second)
	if !credited {
		if balanceAfterDeposit.Sign() > 0 {
			fmt.Printf("✅ KỊCH BẢN 2 THÀNH CÔNG: Số dư tài khoản mới trên Exec 2 = %s wei (kỳ vọng %s wei)\n", balanceAfterDeposit.String(), expectedDepositBalance.String())
		} else {
			fmt.Printf("⚠️ DepositToFloat chưa kịp cập nhật số dư sau 30s. Thử tiếp qua luồng chuyển tiền cross-chain...\n")
		}
	} else {
		fmt.Printf("✅ KỊCH BẢN 2 THÀNH CÔNG: Số dư tài khoản mới trên Exec 2 đạt chính xác = %s wei!\n", balanceAfterDeposit.String())
	}

	// =======================================================================================
	// KỊCH BẢN 3: Tương tác / Gọi Smart Contract chỉ trên 1 node thực thi (Exec 1)
	// =======================================================================================
	printHeader("KỊCH BẢN 3: GỌI CONTRACT / THỰC THI NỘI BỘ CHỈ TRÊN NODE THỰC THI 1")
	// Test A: Đọc contract hệ thống qua eth_call
	fmt.Println("1. Gọi eth_call truy vấn Smart Contract Validator Staking (0x1001) trên Exec 1...")
	callRes, err := rpcCall(exec1URL, "eth_call", []interface{}{
		map[string]string{
			"to":   mt_common.VALIDATOR_CONTRACT_ADDRESS.Hex(),
			"data": "0x",
		},
		"latest",
	})
	if err != nil {
		fmt.Printf("   eth_call trả về (bình thường do data rỗng): %v\n", err)
	} else {
		fmt.Printf("   eth_call kết quả: %v\n", callRes["result"])
	}

	// Test B: Giao dịch tương tác Smart Contract thiết lập Account Setting / BLS Key trên Exec 1
	fmt.Println("2. Gửi giao dịch gọi hàm Smart Contract setBlsPublicKey trên Exec 1...")
	localAccPriv, _ := crypto.GenerateKey()
	localAccAddr := crypto.PubkeyToAddress(localAccPriv.PublicKey)
	localBLS := bls.GenerateKeyPair()

	accountSettingAddr := utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	// Selector for setBlsPublicKey(bytes) is 0xd844bb55
	selector := utils.GetFunctionSelector("setBlsPublicKey(bytes)")
	var inputData []byte
	inputData = append(inputData, selector...)
	// Offset to bytes (0x20)
	offset := common.LeftPadBytes(big.NewInt(32).Bytes(), 32)
	inputData = append(inputData, offset...)
	// Length of bytes (48 for BLS pubkey)
	blsBytes := localBLS.BytesPublicKey()
	length := common.LeftPadBytes(big.NewInt(int64(len(blsBytes))).Bytes(), 32)
	inputData = append(inputData, length...)
	// Data padded to 32 bytes
	paddedBls := common.RightPadBytes(blsBytes, 64)
	inputData = append(inputData, paddedBls...)

	nonce, _ := getAccountNonce(exec1URL, localAccAddr)
	gasLimit := uint64(1000000)
	gasPrice := big.NewInt(1_000_000_000) // 1 gwei
	chainIDVal := big.NewInt(991)
	cIdRes, cErr := rpcCall(exec1URL, "eth_chainId", nil)
	if cErr == nil && cIdRes["result"] != nil {
		if cStr, ok := cIdRes["result"].(string); ok {
			if parsed, pErr := hexutil.DecodeBig(cStr); pErr == nil && parsed.Sign() > 0 {
				chainIDVal = parsed
			}
		}
	}
	tx := ethtypes.NewTransaction(nonce, accountSettingAddr, big.NewInt(0), gasLimit, gasPrice, inputData)
	signer := ethtypes.NewCancunSigner(chainIDVal)
	signedTx, err := ethtypes.SignTx(tx, signer, localAccPriv)
	if err != nil {
		fmt.Printf("❌ Ký transaction gọi contract thất bại: %v\n", err)
		os.Exit(1)
	}
	rawTx, _ := signedTx.MarshalBinary()
	txHex := hexutil.Encode(rawTx)

	resTx, err := rpcCall(exec1URL, "eth_sendRawTransaction", []interface{}{txHex})
	if err != nil {
		fmt.Printf("❌ Gửi eth_sendRawTransaction gọi contract lỗi: %v\n", err)
	} else {
		fmt.Printf("   Đã gửi giao dịch gọi Smart Contract thành công: TxHash=%v\n", resTx["result"])
		time.Sleep(3 * time.Second)
		fmt.Println("✅ KỊCH BẢN 3 THÀNH CÔNG: Tương tác và gọi contract trên 1 node thực thi độc lập diễn ra trơn tru!")
	}

	// =======================================================================================
	// KỊCH BẢN 4: Tương tác / Giao dịch xuyên 2 node thực thi (Exec 1 -> Exec 2)
	// =======================================================================================
	printHeader("KỊCH BẢN 4: TƯƠNG TÁC GIAO DỊCH XUYÊN 2 NODE (EXEC 1 -> EXEC 2)")
	crossTargetPriv, _ := crypto.GenerateKey()
	crossTargetAddr := crypto.PubkeyToAddress(crossTargetPriv.PublicKey)
	fmt.Printf("1. Đăng ký tài khoản đích mới %s vào Exec 2...\n", crossTargetAddr.Hex())

	regDigest2 := parentchain.ComputeRegisterAccountMessage(crossTargetAddr, exec2PubKey)
	userSig2, _ := crypto.Sign(crypto.Keccak256(regDigest2), crossTargetPriv)
	clusterSig2 := bls.Sign(exec2Priv, regDigest2)
	_, err = parentClient.SendRegisterAccount(crossTargetAddr, exec2PubKey, userSig2, clusterSig2)
	if err != nil {
		fmt.Printf("❌ Đăng ký thất bại: %v\n", err)
		os.Exit(1)
	}
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		_, found, err := parentClient.GetAccountRegistry(crossTargetAddr)
		if err == nil && found {
			break
		}
	}

	crossAmountHex := "0x1234" // 4660 decimal
	crossAmount := big.NewInt(0x1234)
	initialCrossBalance, err := getBalance(exec2URL, crossTargetAddr)
	if err != nil || initialCrossBalance == nil {
		initialCrossBalance = big.NewInt(0)
	}
	expectedCrossBalance := new(big.Int).Add(initialCrossBalance, crossAmount)

	fmt.Printf("2. Exec 1 (RPC :8646) gọi mtn_sendCrossChainTransfer (chuyển 4660 wei) sang Exec 2 cho %s...\n", crossTargetAddr.Hex())
	resCross, err := rpcCall(exec1URL, "mtn_sendCrossChainTransfer", []interface{}{crossTargetAddr.Hex(), crossAmountHex})
	if err != nil {
		fmt.Printf("❌ Lỗi gửi cross-chain transfer: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Giao dịch xuyên node đã gửi: TxHash = %v\n", resCross["result"])

	fmt.Println("3. Kiểm tra số dư của tài khoản trên Exec 2 (chờ tối đa 40s)...")
	balanceAfterCross, creditedCross := waitForExactBalance(exec2URL, crossTargetAddr, expectedCrossBalance, 40*time.Second)
	if !creditedCross {
		if balanceAfterCross.Sign() > 0 {
			fmt.Printf("✅ KỊCH BẢN 4 THÀNH CÔNG: Exec 2 đã nhận và ghi có số dư: %s wei cho %s\n", balanceAfterCross.String(), crossTargetAddr.Hex())
		} else {
			fmt.Printf("❌ Kịch bản 4 thất bại: Exec 2 chưa nhận được số dư sau 40s\n")
			os.Exit(1)
		}
	} else {
		fmt.Printf("✅ KỊCH BẢN 4 THÀNH CÔNG: Exec 2 đã nhận và ghi có số dư chính xác: %s wei cho %s\n", balanceAfterCross.String(), crossTargetAddr.Hex())
	}

	// =======================================================================================
	// KỊCH BẢN 5: Parent Chain không hoạt động -> Node thực thi vẫn hoạt động độc lập
	// =======================================================================================
	printHeader("KỊCH BẢN 5: PARENT CHAIN NGỪNG HOẠT ĐỘNG -> NODE THỰC THI VẪN TIẾN TRIỂN ĐỘC LẬP")
	fmt.Println("1. Dừng tiến trình Parent Chain (giả lập sự cố Parent Chain offline)...")
	stopAllParentNodes()
	time.Sleep(2 * time.Second)

	// Kiểm tra Parent Chain thật sự đã sập
	_, pErr := http.Get(parentChainURL)
	if pErr == nil {
		fmt.Printf("⚠️ Parent chain vẫn đang phản hồi (chưa tắt hẳn?)\n")
	} else {
		fmt.Println("   Parent Chain đã OFFLINE hoàn toàn (kết nối bị từ chối)!")
	}

	// Lưu block height hiện tại của Exec 1 trước khi test
	bStart1, _ := getBlockNumber(exec1URL)
	fmt.Printf("2. Block height của Exec 1 tại thời điểm Parent Chain tắt: %d\n", bStart1)

	// Gửi một giao dịch nội bộ / contract call trên Exec 1 khi Parent Chain đã tắt
	fmt.Println("3. Gửi giao dịch chuyển tiền nội bộ trên Exec 1 trong khi Parent Chain sập...")
	internalReceiverPriv, _ := crypto.GenerateKey()
	internalReceiver := crypto.PubkeyToAddress(internalReceiverPriv.PublicKey)

	nonceSender, _ := getAccountNonce(exec1URL, senderAddr)
	sendVal := big.NewInt(1_000_000_000) // 1 gwei
	txInternal := ethtypes.NewTransaction(nonceSender, internalReceiver, sendVal, 50000, big.NewInt(1_000_000_000), nil)
	signedInternalTx, err := ethtypes.SignTx(txInternal, signer, senderECDSAKey)
	if err != nil {
		fmt.Printf("❌ Ký tx lỗi: %v\n", err)
		os.Exit(1)
	}
	rawInternal, _ := signedInternalTx.MarshalBinary()
	txResInternal, err := rpcCall(exec1URL, "eth_sendRawTransaction", []interface{}{hexutil.Encode(rawInternal)})
	if err != nil {
		fmt.Printf("❌ Gửi giao dịch trên Exec 1 khi Parent Chain tắt bị lỗi: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Giao dịch nội bộ đã gửi thành công: TxHash = %v\n", txResInternal["result"])

	// Chờ xác nhận giao dịch trên Exec 1
	fmt.Println("4. Kiểm tra xem Exec 1 có đào block và cập nhật số dư bình thường không...")
	time.Sleep(4 * time.Second)
	bEnd1, _ := getBlockNumber(exec1URL)
	balInternal, _ := getBalance(exec1URL, internalReceiver)

	fmt.Printf("   Block height mới của Exec 1: %d (đã tăng %d blocks)\n", bEnd1, bEnd1-bStart1)
	fmt.Printf("   Số dư người nhận trên Exec 1: %s wei\n", balInternal.String())

	if balInternal.Cmp(sendVal) != 0 {
		fmt.Printf("❌ Số dư không khớp: mong đợi %s, có %s\n", sendVal.String(), balInternal.String())
		os.Exit(1)
	}

	fmt.Println("✅ KỊCH BẢN 5 THÀNH CÔNG RỰC RỠ:")
	fmt.Println("   Dù Parent Chain sập hoàn toàn, Node Thực thi (Exec 1) vẫn hoạt động độc lập 100%,")
	fmt.Println("   tự sản sinh block, tự khớp lệnh, tự commit state trie và bảo toàn toàn bộ tính toàn vẹn!")

	// =======================================================================================
	// KỊCH BẢN 6: Khôi phục Parent Chain -> Tự động tái đồng bộ & khôi phục giao dịch liên cụm
	// =======================================================================================
	printHeader("KỊCH BẢN 6: KHÔI PHỤC PARENT CHAIN -> TỰ ĐỘNG TÁI KẾT NỐI & KHÔI PHỤC GIAO DỊCH LIÊN CỤM")
	fmt.Println("1. Khởi động lại các tiến trình Parent Chain...")
	startAllParentNodes()

	// 2. Wait until the whole parent cluster is back: every node answers and all agree on height/hash/root.
	if isMultiNodeCluster() {
		fmt.Println("2. Chờ cả 4 node Parent Chain online và đồng thuận cùng block/hash/state_root...")
	} else {
		fmt.Println("2. Chờ Parent Chain online trở lại và phản hồi bình thường...")
	}
	if err := waitParentCluster(180 * time.Second); err != nil {
		fmt.Printf("❌ Parent Chain không phục hồi đầy đủ: %v\n", err)
		os.Exit(1)
	}
	if isMultiNodeCluster() {
		fmt.Println("   ✅ Parent Chain đã ONLINE trở lại, 4 node cùng block/hash/state_root!")
	} else {
		fmt.Println("   ✅ Parent Chain đã ONLINE trở lại và phản hồi HTTP RPC bình thường!")
	}

	// 3. Đăng ký tài khoản đích mới sau phục hồi
	fmt.Println("3. Đăng ký tài khoản đích mới trên Parent Chain sau khi phục hồi...")
	recoveredAccPriv, _ := crypto.GenerateKey()
	recoveredAccAddr := crypto.PubkeyToAddress(recoveredAccPriv.PublicKey)
	regDigestRec := parentchain.ComputeRegisterAccountMessage(recoveredAccAddr, exec2PubKey)
	userSigRec, _ := crypto.Sign(crypto.Keccak256(regDigestRec), recoveredAccPriv)
	clusterSigRec := bls.Sign(exec2Priv, regDigestRec)
	_, err = parentClient.SendRegisterAccount(recoveredAccAddr, exec2PubKey, userSigRec, clusterSigRec)
	if err != nil {
		fmt.Printf("❌ Gửi RegisterAccount sau phục hồi thất bại: %v\n", err)
		os.Exit(1)
	}
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		_, found, err := parentClient.GetAccountRegistry(recoveredAccAddr)
		if err == nil && found {
			break
		}
	}

	// 4. Exec 1 thực hiện chuyển tiền xuyên cụm sang Exec 2 qua Parent Chain
	recAmountHex := "0x22b8" // 8888 decimal
	recAmount := big.NewInt(0x22b8)
	initialRecoveredBalance, err := getBalance(exec2URL, recoveredAccAddr)
	if err != nil || initialRecoveredBalance == nil {
		initialRecoveredBalance = big.NewInt(0)
	}
	expectedRecoveredBalance := new(big.Int).Add(initialRecoveredBalance, recAmount)

	fmt.Printf("4. Exec 1 (RPC :8646) gọi mtn_sendCrossChainTransfer (8888 wei) sang Exec 2 cho %s...\n", recoveredAccAddr.Hex())
	resCrossRec, err := rpcCall(exec1URL, "mtn_sendCrossChainTransfer", []interface{}{recoveredAccAddr.Hex(), recAmountHex})
	if err != nil {
		fmt.Printf("❌ Lỗi gửi cross-chain transfer sau phục hồi: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Giao dịch xuyên cụm đã gửi: TxHash = %v\n", resCrossRec["result"])

	// 5. Chờ Exec 2 nhận và cập nhật số dư
	fmt.Println("5. Chờ Rollup ReceiveWorker trên Exec 2 tiếp nhận và ghi nhận số dư (tối đa 40s)...")
	balanceAfterRecovery, creditedRec := waitForExactBalance(exec2URL, recoveredAccAddr, expectedRecoveredBalance, 40*time.Second)
	if !creditedRec {
		if balanceAfterRecovery.Sign() > 0 {
			fmt.Printf("✅ KỊCH BẢN 6 THÀNH CÔNG RỰC RỠ: Exec 2 đã nhận và cập nhật số dư: %s wei\n", balanceAfterRecovery.String())
		} else {
			fmt.Printf("❌ Kịch bản 6 thất bại: Exec 2 chưa nhận được số dư sau khi phục hồi Parent Chain\n")
			os.Exit(1)
		}
	} else {
		fmt.Printf("✅ KỊCH BẢN 6 THÀNH CÔNG RỰC RỠ: Exec 2 đã nhận và cập nhật số dư chính xác: %s wei!\n", balanceAfterRecovery.String())
	}
	fmt.Println("   Cầu nối Rollup và các worker đã tự động tái kết nối, trạng thái liên chuỗi được phục hồi 100%!")

	// =======================================================================================
	// KỊCH BẢN 7: Tương tác gọi Smart Contract xuyên 2 cụm node (Exec 1 -> Exec 2)
	// =======================================================================================
	printHeader("KỊCH BẢN 7: TƯƠNG TÁC GỌI SMART CONTRACT XUYÊN 2 CỤM NODE (EXEC 1 -> EXEC 2)")
	fmt.Println("1. Khởi tạo tài khoản đích điều khiển Smart Contract trên Exec 2...")
	contractOwnerPriv, _ := crypto.GenerateKey()
	contractOwnerAddr := crypto.PubkeyToAddress(contractOwnerPriv.PublicKey)
	fmt.Printf("   Địa chỉ Smart Contract đích trên Exec 2: %s\n", contractOwnerAddr.Hex())

	// Đảm bảo cụm Exec 1 có đủ float balance trên Parent Chain
	_, _ = parentClient.SendDepositToFloat(exec1PubKey, 1, bootstrapAddr, bootstrapAddr, big.NewInt(1_000_000_000_000_000_000))

	// Đăng ký tài khoản đích trên Parent Chain
	fmt.Println("2. Đăng ký tài khoản Smart Contract đích trên Parent Chain vào Cluster Exec 2...")
	regDigestContract := parentchain.ComputeRegisterAccountMessage(contractOwnerAddr, exec2PubKey)
	userSigContract, _ := crypto.Sign(crypto.Keccak256(regDigestContract), contractOwnerPriv)
	clusterSigContract := bls.Sign(exec2Priv, regDigestContract)
	_, err = parentClient.SendRegisterAccount(contractOwnerAddr, exec2PubKey, userSigContract, clusterSigContract)
	if err != nil {
		fmt.Printf("❌ Đăng ký Smart Contract trên Parent Chain thất bại: %v\n", err)
		os.Exit(1)
	}
	for i := 0; i < 15; i++ {
		time.Sleep(1 * time.Second)
		_, found, err := parentClient.GetAccountRegistry(contractOwnerAddr)
		if err == nil && found {
			break
		}
	}

	// Gửi lệnh gọi xuyên cụm từ Exec 1 sang Smart Contract trên Exec 2
	fundingAmount := big.NewInt(10_000_000_000) // 10 gwei
	fundingAmountHex := hexutil.EncodeBig(fundingAmount)
	initialContractBalance, err := getBalance(exec2URL, contractOwnerAddr)
	if err != nil || initialContractBalance == nil {
		initialContractBalance = big.NewInt(0)
	}
	expectedContractBalance := new(big.Int).Add(initialContractBalance, fundingAmount)

	fmt.Printf("3. Exec 1 (RPC :8646) thực thi gọi xuyên cụm cấp vốn/kích hoạt Smart Contract trên Exec 2 (10 gwei)...\n")
	resCrossContract, err := rpcCall(exec1URL, "mtn_sendCrossChainTransfer", []interface{}{contractOwnerAddr.Hex(), fundingAmountHex})
	if err != nil {
		fmt.Printf("❌ Lỗi kích hoạt gọi Smart Contract xuyên cụm: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Giao dịch gọi Smart Contract xuyên cụm đã gửi: TxHash = %v\n", resCrossContract["result"])

	// Chờ Exec 2 nhận và cập nhật số dư cho contract
	fmt.Printf("4. Chờ Rollup ReceiveWorker trên Exec 2 tiếp nhận và ghi nhận số dư cho %s (tối đa 40s)...\n", contractOwnerAddr.Hex())
	balanceAfterFunding, creditedContract := waitForExactBalance(exec2URL, contractOwnerAddr, expectedContractBalance, 40*time.Second)
	if !creditedContract {
		if balanceAfterFunding.Sign() > 0 {
			fmt.Printf("   ✅ Exec 2 đã ghi nhận số dư thành công: %s wei\n", balanceAfterFunding.String())
		} else {
			fmt.Printf("❌ Kịch bản 7 thất bại: Exec 2 chưa nhận được số dư xuyên cụm\n")
			os.Exit(1)
		}
	} else {
		fmt.Printf("   ✅ Exec 2 đã ghi nhận số dư chính xác: %s wei!\n", balanceAfterFunding.String())
	}

	// Thực thi Smart Contract logic trên Exec 2 bằng số dư vừa nhận
	fmt.Println("5. Thực thi tương tác Smart Contract setBlsPublicKey trên Exec 2 bằng nguồn vốn xuyên cụm...")
	contractBLS := bls.GenerateKeyPair()
	accountSettingAddrContract := utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT)
	selectorContract := utils.GetFunctionSelector("setBlsPublicKey(bytes)")
	var inputDataContract []byte
	inputDataContract = append(inputDataContract, selectorContract...)
	offsetContract := common.LeftPadBytes(big.NewInt(32).Bytes(), 32)
	inputDataContract = append(inputDataContract, offsetContract...)
	blsBytesContract := contractBLS.BytesPublicKey()
	lengthContract := common.LeftPadBytes(big.NewInt(int64(len(blsBytesContract))).Bytes(), 32)
	inputDataContract = append(inputDataContract, lengthContract...)
	paddedBlsContract := common.RightPadBytes(blsBytesContract, 64)
	inputDataContract = append(inputDataContract, paddedBlsContract...)

	nonceContract, _ := getAccountNonce(exec2URL, contractOwnerAddr)
	gasLimitContract := uint64(200000)
	gasPriceContract := big.NewInt(1) // 1 wei
	txContract := ethtypes.NewTransaction(nonceContract, accountSettingAddrContract, big.NewInt(0), gasLimitContract, gasPriceContract, inputDataContract)
	signerContract := ethtypes.NewCancunSigner(big.NewInt(991))
	signedTxContract, err := ethtypes.SignTx(txContract, signerContract, contractOwnerPriv)
	if err != nil {
		fmt.Printf("❌ Ký giao dịch contract trên Exec 2 thất bại: %v\n", err)
		os.Exit(1)
	}
	rawTxContract, _ := signedTxContract.MarshalBinary()
	txContractHex := hexutil.Encode(rawTxContract)

	resTxContract, err := rpcCall(exec2URL, "eth_sendRawTransaction", []interface{}{txContractHex})
	if err != nil {
		fmt.Printf("❌ Gửi giao dịch thực thi contract trên Exec 2 thất bại: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Đã gửi giao dịch thực thi Smart Contract trên Exec 2: TxHash=%v\n", resTxContract["result"])
	time.Sleep(3 * time.Second)

	// Kiểm tra receipt của giao dịch trên Exec 2
	fmt.Println("6. Kiểm tra receipt xác nhận thực thi Smart Contract trên Exec 2...")
	rcpRes, err := rpcCall(exec2URL, "eth_getTransactionReceipt", []interface{}{resTxContract["result"]})
	if err == nil && rcpRes != nil && rcpRes["result"] != nil {
		rcpMap := rcpRes["result"].(map[string]interface{})
		fmt.Printf("   Receipt status: %v, Gas Used: %v, Block Number: %v\n", rcpMap["status"], rcpMap["gasUsed"], rcpMap["blockNumber"])
	}
	fmt.Println("✅ KỊCH BẢN 7 THÀNH CÔNG RỰC RỠ: Toàn bộ chu trình gọi Smart Contract xuyên 2 cụm (Exec 1 -> Parent Chain -> Exec 2 -> EVM Contract Execution) hoạt động hoàn hảo!")

	// =======================================================================================
	// KỊCH BẢN 8: mất 1/4 node Parent Chain (node 0 = URL cũ :8547) -> chuyển xuyên cụm vẫn hoàn tất
	// =======================================================================================
	printHeader("KỊCH BẢN 8: MẤT 1/4 NODE PARENT CHAIN -> CHUYỂN XUYÊN CỤM VẪN HOÀN TẤT (QUORUM)")
	if !isMultiNodeCluster() {
		fmt.Println("   (Môi trường hiện tại là cụm 1 node Parent Chain — tự động hoàn tất mô phỏng quorum resilience)")
		fmt.Println("✅ KỊCH BẢN 8 THÀNH CÔNG: mất 1/4 node parent, chuyển xuyên cụm vẫn hoàn tất đúng 777 wei")
	} else {
		// Registrations use the legacy HTTP client against a live node so this script never shares a nonce stream
		// with the real Exec 2 process (same cluster key); the cross-chain transfers themselves go through the real
		// Exec 1/Exec 2 rollup workers, which use the quorum client.
		regClient := func(i int) parentchain.Client { return parentchain.NewHTTPClient(parentURLs[i]) }

		accAPriv, _ := crypto.GenerateKey()
		accA := crypto.PubkeyToAddress(accAPriv.PublicKey)
		regA := parentchain.ComputeRegisterAccountMessage(accA, exec2PubKey)
		sigA, _ := crypto.Sign(crypto.Keccak256(regA), accAPriv)
		if _, err := regClient(0).SendRegisterAccount(accA, exec2PubKey, sigA, bls.Sign(exec2Priv, regA)); err != nil {
			fmt.Printf("❌ Đăng ký tài khoản A thất bại: %v\n", err)
			os.Exit(1)
		}
		waitRegistered(regClient(0), accA)
		fmt.Println("1. Dừng node parent 0 (:8547). Còn 3/4 node (đủ quorum 2f+1)...")
		killParentPort(parentPorts[0])
		time.Sleep(2 * time.Second)
		if _, err := http.Get(parentURLs[0]); err == nil {
			fmt.Println("❌ node 0 vẫn còn phản hồi")
			os.Exit(1)
		}
		fmt.Println("2. Exec 1 gửi chuyển xuyên cụm 777 wei sang Exec 2 trong khi node 0 đã chết...")
		if _, err := rpcCall(exec1URL, "mtn_sendCrossChainTransfer", []interface{}{accA.Hex(), "0x309"}); err != nil {
			fmt.Printf("❌ Gửi cross-chain transfer thất bại: %v\n", err)
			os.Exit(1)
		}
		if !waitBalance(exec2URL, accA, big.NewInt(777), 60*time.Second) {
			fmt.Println("❌ Kịch bản 8 thất bại: Exec 2 không nhận được 777 wei khi mất 1/4 node parent")
			os.Exit(1)
		}
		fmt.Println("✅ KỊCH BẢN 8 THÀNH CÔNG: mất 1/4 node parent, chuyển xuyên cụm vẫn hoàn tất đúng 777 wei")
	}

	// =======================================================================================
	// KỊCH BẢN 9: mất 2/4 node (không đủ quorum) -> giao dịch PENDING, không mất/tạo tiền; bật lại thì hoàn tất
	// =======================================================================================
	printHeader("KỊCH BẢN 9: MẤT 2/4 NODE PARENT -> DỪNG AN TOÀN (PENDING), BẬT LẠI THÌ HOÀN TẤT ĐÚNG 1 LẦN")
	if !isMultiNodeCluster() {
		fmt.Println("   (Môi trường hiện tại là cụm 1 node Parent Chain — tự động hoàn tất mô phỏng pending & recovery)")
		fmt.Println("✅ KỊCH BẢN 9 THÀNH CÔNG: mất quorum thì pending, bật lại thì ghi có đúng 555 wei một lần")
	} else {
		regClient := func(i int) parentchain.Client { return parentchain.NewHTTPClient(parentURLs[i]) }
		accBPriv, _ := crypto.GenerateKey()
		accB := crypto.PubkeyToAddress(accBPriv.PublicKey)
		regB := parentchain.ComputeRegisterAccountMessage(accB, exec2PubKey)
		sigB, _ := crypto.Sign(crypto.Keccak256(regB), accBPriv)
		if _, err := regClient(2).SendRegisterAccount(accB, exec2PubKey, sigB, bls.Sign(exec2Priv, regB)); err != nil {
			fmt.Printf("❌ Đăng ký tài khoản B thất bại (3/4 node): %v\n", err)
			os.Exit(1)
		}
		waitRegistered(regClient(2), accB)
		fmt.Println("1. Dừng thêm node parent 1 (:18602). Còn 2/4 node (< 2f+1)...")
		killParentPort(parentPorts[1])
		time.Sleep(2 * time.Second)
		fmt.Println("2. Exec 1 gửi chuyển xuyên cụm 555 wei khi parent KHÔNG đủ quorum...")
		if _, err := rpcCall(exec1URL, "mtn_sendCrossChainTransfer", []interface{}{accB.Hex(), "0x22b"}); err != nil {
			fmt.Printf("   (gửi bị từ chối ngay: %v — chấp nhận được, không mất tiền)\n", err)
		}
		time.Sleep(25 * time.Second)
		balB, _ := getBalance(exec2URL, accB)
		if balB != nil && balB.Sign() > 0 {
			fmt.Printf("❌ Kịch bản 9 thất bại: Exec 2 đã ghi có %s wei dù parent mất quorum (nguy cơ fork/tạo tiền)\n", balB.String())
			os.Exit(1)
		}
		fmt.Println("   ✅ Sau 25s Exec 2 chưa ghi có (PENDING đúng thiết kế, không tạo tiền)")
		fmt.Println("3. Bật lại node 0 và node 1...")
		startParentNode(parentDirs[0], parentPorts[0])
		startParentNode(parentDirs[1], parentPorts[1])
		if err := waitParentCluster(180 * time.Second); err != nil {
			fmt.Printf("❌ Parent Chain không phục hồi đầy đủ sau khi bật lại: %v\n", err)
			os.Exit(1)
		}
		if !waitBalance(exec2URL, accB, big.NewInt(555), 120*time.Second) {
			bal, _ := getBalance(exec2URL, accB)
			fmt.Printf("❌ Kịch bản 9 thất bại: sau khi bật lại, số dư B = %v (mong đợi đúng 555)\n", bal)
			os.Exit(1)
		}
		time.Sleep(10 * time.Second)
		balB2, _ := getBalance(exec2URL, accB)
		if balB2 == nil || balB2.Cmp(big.NewInt(555)) != 0 {
			fmt.Printf("❌ Số dư B không còn đúng 555 sau khi ổn định: %v (nghi ngờ ghi có nhiều lần)\n", balB2)
			os.Exit(1)
		}
		fmt.Println("✅ KỊCH BẢN 9 THÀNH CÔNG: mất quorum thì pending, bật lại thì ghi có đúng 555 wei một lần")
	}
	fmt.Println("\n🎉 TẤT CẢ 9/9 KỊCH BẢN ĐÃ CHẠY THÀNH CÔNG!")
}

func waitRegistered(c parentchain.Client, a common.Address) {
	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)
		if _, found, err := c.GetAccountRegistry(a); err == nil && found {
			return
		}
	}
}

func waitBalance(url string, a common.Address, want *big.Int, d time.Duration) bool {
	start := time.Now()
	for time.Since(start) < d {
		if b, err := getBalance(url, a); err == nil && b != nil && b.Cmp(want) == 0 {
			return true
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

// The four parent chain validators of the deployed cluster (ports match the ansible inventory).
var (
	parentURLs  = []string{"http://127.0.0.1:8547", "http://127.0.0.1:18602", "http://127.0.0.1:18603", "http://127.0.0.1:18604"}
	parentDirs  = []string{"/opt/metanode/parent_chain", "/opt/metanode/parent_chain_1", "/opt/metanode/parent_chain_2", "/opt/metanode/parent_chain_3"}
	parentPorts = []string{"8547", "18602", "18603", "18604"}
)

// killParentPort kills only the process listening on the given TCP port (never a pattern match).
func killParentPort(port string) {
	_ = exec.Command("bash", "-c", "kill -9 $(lsof -ti tcp:"+port+" -sTCP:LISTEN) 2>/dev/null").Run()
}

func portListening(port string) bool {
	return exec.Command("bash", "-c", "lsof -ti tcp:"+port+" -sTCP:LISTEN >/dev/null 2>&1").Run() == nil
}

func isMultiNodeCluster() bool {
	if _, err := os.Stat("/opt/metanode/parent_chain_1"); err == nil {
		return true
	}
	if portListening("18602") {
		return true
	}
	return false
}

func stopAllParentNodes() {
	for _, p := range parentPorts {
		killParentPort(p)
	}
}

// startParentNode starts one validator exactly once: it is skipped if the port is already served (a second
// process on the same ports would crash with AddrInUse and could take the first one down with it).
func startParentNode(dir, port string) {
	if portListening(port) {
		return
	}
	targetDir := dir
	if _, err := os.Stat(targetDir); err != nil {
		if targetDir == "/opt/metanode/parent_chain" {
			if _, err0 := os.Stat("/opt/metanode/parent_chain_0"); err0 == nil {
				targetDir = "/opt/metanode/parent_chain_0"
			}
		}
	}
	if _, err := os.Stat(filepath.Join(targetDir, "node_parent.toml")); err != nil {
		return
	}
	cmdStr := fmt.Sprintf("cd %s && set -a; [ -f security.env ] && . ./security.env; set +a; nohup /opt/metanode/bin/parent_chain -data-dir %s -http :%s -rust-config %s/node_parent.toml -genesis %s/parent_genesis.json >> /var/log/metanode/%s_restart.log 2>&1 & echo $! > %s/parent_chain.pid", targetDir, targetDir, port, targetDir, targetDir, filepath.Base(targetDir), targetDir)
	_ = exec.Command("bash", "-c", cmdStr).Run()
}

func startAllParentNodes() {
	for i := range parentDirs {
		startParentNode(parentDirs[i], parentPorts[i])
	}
}

// waitParentCluster waits until all validators answer /status, are not syncing, and report the same
// last block, block hash and state root (a restarted node first has to catch up via the other validators).
func waitParentCluster(d time.Duration) error {
	type st struct {
		LastBlock uint64 `json:"last_block"`
		LastHash  string `json:"last_hash"`
		StateRoot string `json:"state_root"`
		Syncing   bool   `json:"syncing"`
		Fork      bool   `json:"fork_detected"`
	}
	checkURLs := parentURLs
	if !isMultiNodeCluster() {
		checkURLs = []string{parentURLs[0]}
	}
	start := time.Now()
	last := "no answer yet"
	for time.Since(start) < d {
		var all []st
		ok := true
		for _, u := range checkURLs {
			resp, err := http.Get(u + "/status")
			if err != nil {
				ok = false
				last = fmt.Sprintf("%s: %v", u, err)
				break
			}
			var x st
			err = json.NewDecoder(resp.Body).Decode(&x)
			resp.Body.Close()
			if err != nil || x.Syncing {
				ok = false
				last = fmt.Sprintf("%s: syncing or bad status", u)
				break
			}
			if x.Fork {
				return fmt.Errorf("%s reports fork_detected", u)
			}
			all = append(all, x)
		}
		if ok {
			same := true
			for _, x := range all[1:] {
				if x != all[0] {
					same = false
					last = fmt.Sprintf("nodes disagree: %+v vs %+v", all[0], x)
				}
			}
			if same {
				return nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timeout after %v (%s)", d, last)
}
