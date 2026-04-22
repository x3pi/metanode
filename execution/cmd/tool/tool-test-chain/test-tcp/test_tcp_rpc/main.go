package main

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/tool/tool-test-chain/test-tcp/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

// ===================== HELPERS =====================

// protoReceiptToRpcReceipt chuyển pb.Receipt (proto) → pb.RpcReceipt (để giữ interface thống nhất)
func protoReceiptToRpcReceipt(rcpt *pb.Receipt) *pb.RpcReceipt {
	if rcpt == nil {
		return nil
	}

	gasUsed := fmt.Sprintf("0x%x", rcpt.GasUsed)
	txHash := common.BytesToHash(rcpt.TransactionHash).Hex()

	return &pb.RpcReceipt{
		TransactionHash: txHash,
		Status:          rcpt.Status,
		GasUsed:         gasUsed,
	}
}

// waitReceiptPoll đợi receipt qua RPC poll (fallback khi không có receipt inline).
func waitReceiptPoll(tcpClient *client_tcp.Client, txHash string) *pb.RpcReceipt {
	if txHash == "" {
		return nil
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		receipt, err := tcpClient.RpcGetTransactionReceipt(txHash)
		if err != nil {
			fmt.Printf("  ❌ Receipt error: %v\n", err)
			return nil
		}
		if receipt != nil {
			fmt.Printf("  ✅ Receipt (poll): status=%s, gasUsed=%s, logs=%d\n",
				receipt.Status, receipt.GasUsed, len(receipt.Logs))
			return receipt
		}
		select {
		case <-timer.C:
			fmt.Println("  ⚠️ Timeout waiting for receipt")
			return nil
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// sendTxAndWait tạo, ký, gửi transaction + đợi receipt.
// Server trả receipt trực tiếp trong RPC response (proto Receipt bytes).
func sendTxAndWait(
	tcpClient *client_tcp.Client,
	privKey *ecdsa.PrivateKey,
	fromAddr common.Address,
	toAddr common.Address,
	parsedABI abi.ABI,
	signer e_types.Signer,
	method string,
	args ...interface{},
) (string, *pb.RpcReceipt) {
	nonce, err := tcpClient.GetNonce(fromAddr)
	if err != nil {
		fmt.Printf("  ❌ GetNonce: %v\n", err)
		return "", nil
	}

	inputData, err := parsedABI.Pack(method, args...)
	if err != nil {
		fmt.Printf("  ❌ Pack %s: %v\n", method, err)
		return "", nil
	}

	tx := e_types.NewTransaction(nonce, toAddr, big.NewInt(0), 20000000, big.NewInt(10000000), inputData)
	signedTx, _ := e_types.SignTx(tx, signer, privKey)
	rawTxBytes, _ := signedTx.MarshalBinary()
	rawTxHex := "0x" + hex.EncodeToString(rawTxBytes)

	txHash, rcptBytes, err := tcpClient.RpcSendRawTransactionWithReceipt(rawTxHex)
	if err != nil {
		fmt.Printf("  ❌ SendTx %s: %v\n", method, err)
		return "", nil
	}
	fmt.Printf("  ✅ txHash: %s\n", txHash)

	// Nếu có receipt bytes → parse proto Receipt
	if len(rcptBytes) > 0 {
		protoReceipt := &pb.Receipt{}
		if unmarshalErr := proto.Unmarshal(rcptBytes, protoReceipt); unmarshalErr == nil {
			rpcReceipt := protoReceiptToRpcReceipt(protoReceipt)
			fmt.Printf("  ✅ Receipt: status=%s, gasUsed=%s\n", rpcReceipt.Status, rpcReceipt.GasUsed)
			return txHash, rpcReceipt
		}
	}

	// Intercepted TX (chỉ có txHash) → poll RPC
	receipt := waitReceiptPoll(tcpClient, txHash)
	return txHash, receipt
}

// sendTxAndWaitRPC gửi TX và poll RPC để nhận receipt.
// Dùng cho các TX bị intercepted bởi RPC proxy (vd: confirmAccountWithoutSign)
func sendTxAndWaitRPC(
	tcpClient *client_tcp.Client,
	privKey *ecdsa.PrivateKey,
	fromAddr common.Address,
	toAddr common.Address,
	parsedABI abi.ABI,
	signer e_types.Signer,
	method string,
	args ...interface{},
) (string, *pb.RpcReceipt) {
	nonce, err := tcpClient.GetNonce(fromAddr)
	if err != nil {
		fmt.Printf("  ❌ GetNonce: %v\n", err)
		return "", nil
	}

	inputData, err := parsedABI.Pack(method, args...)
	if err != nil {
		fmt.Printf("  ❌ Pack %s: %v\n", method, err)
		return "", nil
	}

	tx := e_types.NewTransaction(nonce, toAddr, big.NewInt(0), 20000000, big.NewInt(10000000), inputData)
	signedTx, _ := e_types.SignTx(tx, signer, privKey)
	rawTxBytes, _ := signedTx.MarshalBinary()
	rawTxHex := "0x" + hex.EncodeToString(rawTxBytes)

	txHash, err := tcpClient.RpcSendRawTransaction(rawTxHex)
	if err != nil {
		fmt.Printf("  ❌ SendTx %s: %v\n", method, err)
		return "", nil
	}
	fmt.Printf("  ✅ txHash: %s\n", txHash)

	receipt := waitReceiptPoll(tcpClient, txHash)
	return txHash, receipt
}

// sendTxExpectError gửi transaction mà mong đợi lỗi (revert test)
func sendTxExpectError(
	tcpClient *client_tcp.Client,
	privKey *ecdsa.PrivateKey,
	fromAddr common.Address,
	toAddr common.Address,
	parsedABI abi.ABI,
	signer e_types.Signer,
	method string,
	args ...interface{},
) {
	nonce, err := tcpClient.GetNonce(fromAddr)
	if err != nil {
		fmt.Printf("  ❌ GetNonce: %v\n", err)
		return
	}

	inputData, err := parsedABI.Pack(method, args...)
	if err != nil {
		fmt.Printf("  ❌ Pack %s: %v\n", method, err)
		return
	}

	tx := e_types.NewTransaction(nonce, toAddr, big.NewInt(0), 20000000, big.NewInt(10000000), inputData)
	signedTx, _ := e_types.SignTx(tx, signer, privKey)
	rawTxBytes, _ := signedTx.MarshalBinary()
	rawTxHex := "0x" + hex.EncodeToString(rawTxBytes)

	txHash, err := tcpClient.RpcSendRawTransaction(rawTxHex)
	if err != nil {
		fmt.Printf("  ✅ Transaction reverted as expected!\n")
		fmt.Printf("     Error: %v\n", err)
		return
	}
	fmt.Printf("  ⚠️ Transaction did NOT revert (txHash=%s), checking receipt...\n", txHash)
	receipt := waitReceiptPoll(tcpClient, txHash)
	if receipt != nil && receipt.Status != pb.RECEIPT_STATUS_RETURNED {
		fmt.Printf("  ✅ Receipt shows revert: status=%s\n", receipt.Status)
	} else if receipt != nil {
		fmt.Printf("  ❌ Transaction succeeded unexpectedly: status=%s\n", receipt.Status)
	}
}

// sendTxFreeGas gửi transaction bị intercepted bởi RPC (không có receipt).
// Các hàm free gas (addAuthorizedWallet, addContractFreeGas, …) trả về txHash trực tiếp.
// Trả về (txHash, true) nếu thành công, ("" false) nếu lỗi.
func sendTxFreeGas(
	tcpClient *client_tcp.Client,
	privKey *ecdsa.PrivateKey,
	fromAddr common.Address,
	toAddr common.Address,
	parsedABI abi.ABI,
	signer e_types.Signer,
	method string,
	args ...interface{},
) (string, bool) {
	nonce, err := tcpClient.GetNonce(fromAddr)
	if err != nil {
		fmt.Printf("  ❌ GetNonce: %v\n", err)
		return "", false
	}

	inputData, err := parsedABI.Pack(method, args...)
	if err != nil {
		fmt.Printf("  ❌ Pack %s: %v\n", method, err)
		return "", false
	}

	tx := e_types.NewTransaction(nonce, toAddr, big.NewInt(0), 20000000, big.NewInt(10000000), inputData)
	signedTx, _ := e_types.SignTx(tx, signer, privKey)
	rawTxBytes, _ := signedTx.MarshalBinary()
	rawTxHex := "0x" + hex.EncodeToString(rawTxBytes)

	txHash, err := tcpClient.RpcSendRawTransaction(rawTxHex)
	if err != nil {
		fmt.Printf("  ❌ %s: %v\n", method, err)
		return "", false
	}
	fmt.Printf("  ✅ %s → txHash: %s\n", method, txHash)
	return txHash, true
}

// sendTxFreeGasExpectError gửi tx free gas và mộng đợi lỗi.
func sendTxFreeGasExpectError(
	tcpClient *client_tcp.Client,
	privKey *ecdsa.PrivateKey,
	fromAddr common.Address,
	toAddr common.Address,
	parsedABI abi.ABI,
	signer e_types.Signer,
	method string,
	args ...interface{},
) {
	nonce, err := tcpClient.GetNonce(fromAddr)
	if err != nil {
		fmt.Printf("  ❌ GetNonce: %v\n", err)
		return
	}

	inputData, _ := parsedABI.Pack(method, args...)
	tx := e_types.NewTransaction(nonce, toAddr, big.NewInt(0), 20000000, big.NewInt(10000000), inputData)
	signedTx, _ := e_types.SignTx(tx, signer, privKey)
	rawTxBytes, _ := signedTx.MarshalBinary()
	rawTxHex := "0x" + hex.EncodeToString(rawTxBytes)

	_, err = tcpClient.RpcSendRawTransaction(rawTxHex)
	if err != nil {
		fmt.Printf("  ✅ REVERT (expected): %v\n", err)
	} else {
		fmt.Printf("  ❌ Expected revert but %s succeeded!\n", method)
	}
}

// makeEventHandler tạo callback log event chung
func makeEventHandler(eventName string, wg *sync.WaitGroup, once *sync.Once) func([]byte) {
	return func(eventData []byte) {
		event := &pb.RpcEvent{}
		if err := proto.Unmarshal(eventData, event); err != nil {
			fmt.Printf("  ❌ Failed to parse RpcEvent: %v\n", err)
			return
		}

		fmt.Printf("\n  📡 [%s] EVENT RECEIVED!\n", eventName)
		fmt.Printf("  ├─ SubscriptionID: %s\n", event.SubscriptionId)
		if event.Log != nil {
			fmt.Printf("  ├─ Contract:       %s\n", event.Log.Address)
			fmt.Printf("  ├─ BlockNumber:    %s\n", event.Log.BlockNumber)
			fmt.Printf("  ├─ TxHash:         %s\n", event.Log.TransactionHash)
			fmt.Printf("  ├─ Topics:         %v\n", event.Log.Topics)
			fmt.Printf("  └─ Data:           %s\n", event.Log.Data)
		}

		if once != nil && wg != nil {
			once.Do(func() {
				wg.Done()
			})
		}
	}
}

// ===================== TEST: Demo Contract =====================

func testDemoContract(
	tcpClient *client_tcp.Client,
	demoABI abi.ABI,
	contractAddr common.Address,
	privKey *ecdsa.PrivateKey,
	fromAddr common.Address,
	signer e_types.Signer,
) {
	fmt.Println("\n╔══════════════════════════════════════════════════════╗")
	fmt.Println("║  TEST: Demo Contract                                 ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")

	// 1. Read current value
	fmt.Println("\n─── 1. getValue (trước) ───")
	getValueData, _ := demoABI.Pack("getValue")
	resultBytes, err := tcpClient.RpcEthCall(contractAddr, getValueData)
	if err != nil {
		fmt.Printf("  ❌ %v\n", err)
		return
	}
	results, _ := demoABI.Unpack("getValue", resultBytes)
	oldValue := results[0].(*big.Int)
	fmt.Printf("  ✅ getValue() = %s\n", oldValue.String())

	// // 2. Subscribe 2 events
	// fmt.Println("\n─── 2. Subscribe ValueChanged + ValueIncreased ───")
	// var wgChanged, wgIncreased sync.WaitGroup
	// var onceChanged, onceIncreased sync.Once
	// wgChanged.Add(1)
	// wgIncreased.Add(1)

	// sub1, _ := tcpClient.RpcSubscribe(
	// 	[]string{contractAddr.Hex()},
	// 	[]string{demoABI.Events["ValueChanged"].ID.Hex()},
	// 	makeEventHandler("ValueChanged", &wgChanged, &onceChanged),
	// )
	// fmt.Printf("  ✅ ValueChanged subID=%s\n", sub1)

	// sub2, _ := tcpClient.RpcSubscribe(
	// 	[]string{contractAddr.Hex()},
	// 	[]string{demoABI.Events["ValueIncreased"].ID.Hex()},
	// 	makeEventHandler("ValueIncreased", &wgIncreased, &onceIncreased),
	// )
	// fmt.Printf("  ✅ ValueIncreased subID=%s\n", sub2)

	// 3. setValue(789)
	// fmt.Println("\n─── 3. setValue(789) ───")
	// sendTxAndWait(tcpClient, mainClient, privKey, fromAddr, contractAddr, demoABI, signer, "setValue", big.NewInt(789))

	// 4. increaseValue(100)
	fmt.Println("\n─── 4. increaseValue(100) ───")
	sendTxAndWait(tcpClient, privKey, fromAddr, contractAddr, demoABI, signer, "increaseValue", big.NewInt(100))

	// // 5. Wait events
	// fmt.Println("\n─── 5. Wait events (max 10s) ───")
	// allDone := make(chan struct{})
	// go func() {
	// 	wgChanged.Wait()
	// 	wgIncreased.Wait()
	// 	close(allDone)
	// }()
	// select {
	// case <-allDone:
	// 	fmt.Println("  ✅ Both events received!")
	// case <-time.After(10 * time.Second):
	// 	fmt.Println("  ⚠️ Timeout")
	// }

	// // 6. Unsubscribe
	// tcpClient.RpcUnsubscribe(sub1)
	// tcpClient.RpcUnsubscribe(sub2)

	// 7. Verify value
	fmt.Println("\n─── 6. getValue (sau) ───")
	resultBytes2, _ := tcpClient.RpcEthCall(contractAddr, getValueData)
	results2, _ := demoABI.Unpack("getValue", resultBytes2)
	newValue := results2[0].(*big.Int)
	fmt.Printf("  ✅ getValue() = %s", newValue.String())
	if newValue.Int64() == 889 {
		fmt.Println(" ✅ (789 + 100 = 889)")
	} else {
		fmt.Println()
	}

	// 8. Test Revert: increaseValue với giá trị quá lớn (nếu contract có require)
	// fmt.Println("\n─── 7. Test REVERT: increaseValue(0) — expect revert ───")
	// fmt.Println("  ℹ️  Gửi increaseValue(0) — nếu contract yêu cầu amount > 0 sẽ revert")
	// sendTxExpectError(tcpClient, mainClient, privKey, fromAddr, contractAddr, demoABI, signer, "increaseValue", big.NewInt(0))

	// 9. Test Revert: eth_call với invalid function
	// fmt.Println("\n─── 8. Test REVERT: eth_call với data rỗng ───")
	// _, err = tcpClient.RpcEthCall(contractAddr, []byte{0x00, 0x00, 0x00, 0x00})
	// if err != nil {
	// 	fmt.Printf("  ✅ eth_call reverted as expected: %v\n", err)
	// } else {
	// 	fmt.Println("  ⚠️ eth_call did not revert")
	// }

	// 10. Verify value unchanged after reverts
	// fmt.Println("\n─── 9. getValue (sau revert) — verify unchanged ───")
	// resultBytes3, _ := tcpClient.RpcEthCall(contractAddr, getValueData)
	// results3, _ := demoABI.Unpack("getValue", resultBytes3)
	// afterRevertValue := results3[0].(*big.Int)
	// fmt.Printf("  ✅ getValue() = %s", afterRevertValue.String())
	// if afterRevertValue.Cmp(newValue) == 0 {
	// 	fmt.Println(" ✅ (unchanged after revert)")
	// } else {
	// 	fmt.Println(" ❌ (value changed unexpectedly!)")
	// }
}

// ===================== TEST: BLS Registration =====================

func testBlsRegistration(
	tcpClient *client_tcp.Client,
	accountABI abi.ABI,
	accountContract common.Address,
	adminPrivKey *ecdsa.PrivateKey,
	adminAddr common.Address,
	signer e_types.Signer,
	count int,
	outFile string,
) {
	bls.Init()
	fmt.Println("\n╔══════════════════════════════════════════════════════╗")
	fmt.Printf("║  TEST: BLS Registration + Confirm (%d keys)           ║\n", count)
	fmt.Println("╚══════════════════════════════════════════════════════╝")

	type KeyGenResult struct {
		Address       string `json:"address"`
		PrivateKey    string `json:"private_key"`
		BlsPubKey     string `json:"bls_pub_key"`
		BlsPrivateKey string `json:"bls_private_key"`
	}
	var results []KeyGenResult

	for i := 0; i < count; i++ {
		fmt.Printf("\n─── Generating Key %d of %d ───\n", i+1, count)
		// Step 1: Tạo private key mới
		newPrivKey, err := crypto.GenerateKey()
		if err != nil {
			fmt.Printf("  ❌ GenerateKey: %v\n", err)
			continue
		}
		newAddr := crypto.PubkeyToAddress(newPrivKey.PublicKey)
		newPrivKeyHex := hex.EncodeToString(crypto.FromECDSA(newPrivKey))
		fmt.Printf("  ✅ New address:     %s\n", newAddr.Hex())
		fmt.Printf("  ✅ New private key: %s\n", newPrivKeyHex)

		// Step 2: Generate BLS KeyPair locally
		blsKeyPair := bls.GenerateKeyPair()
		blsPubKey := blsKeyPair.BytesPublicKey()
		blsPrivKey := blsKeyPair.BytesPrivateKey()

		serverBlsPubKey := "0x" + hex.EncodeToString(blsPubKey)
		fmt.Printf("  ✅ Locally generated BLS PublicKey: %s (%d bytes)\n", serverBlsPubKey, len(blsPubKey))

		if len(blsPubKey) == 0 {
			fmt.Println("  ❌ BLS pubkey rỗng, bỏ qua")
			continue
		}

		// Step 3: Subscribe RegisterBls + setBlsPublicKey
		var wgRegister sync.WaitGroup
		wgRegister.Add(1)

		// Gửi setBlsPublicKey
		nonce, _ := tcpClient.GetNonce(newAddr)
		inputData, _ := accountABI.Pack("setBlsPublicKey", blsPubKey)
		tx := e_types.NewTransaction(nonce, accountContract, big.NewInt(0), 20000000, big.NewInt(10000000), inputData)
		signedTx, _ := e_types.SignTx(tx, signer, newPrivKey)
		rawTxBytes, _ := signedTx.MarshalBinary()
		txHash, err := tcpClient.RpcSendRawTransaction("0x" + hex.EncodeToString(rawTxBytes))
		if err != nil {
			fmt.Printf("  ❌ setBlsPublicKey: %v\n", err)
			continue
		}
		fmt.Printf("  ✅ setBlsPublicKey sent: txHash=%s\n", txHash)

		// Đợi RegisterBls event
		fmt.Println("  ⏳ Waiting for RegisterBls event (max 10s)...")
		regDone := make(chan struct{})
		go func() {
			wgRegister.Wait()
			close(regDone)
		}()

		// Step 4: confirmAccountWithoutSign (admin confirm)
		// Dùng RPC poll vì TX này bị intercepted bởi RPC proxy → receipt về qua kênh RPC,
		// không push qua TCP direct (port 4200). sendTxAndWait sẽ timeout nếu dùng directClient.
		fmt.Printf("  ℹ️  Admin %s confirming %s...\n", adminAddr.Hex(), newAddr.Hex())
		txHash2, receipt2 := sendTxAndWaitRPC(
			tcpClient, adminPrivKey, adminAddr, accountContract,
			accountABI, signer,
			"confirmAccountWithoutSign", newAddr,
		)
		if receipt2 != nil {
			fmt.Printf("  ✅ confirmAccountWithoutSign OK: txHash=%s\n", txHash2)
		} else {
			fmt.Println("  ⚠️ confirmAccountWithoutSign — receipt not available (may be intercepted)")
		}

		results = append(results, KeyGenResult{
			Address:       newAddr.Hex(),
			PrivateKey:    newPrivKeyHex,
			BlsPubKey:     serverBlsPubKey,
			BlsPrivateKey: hex.EncodeToString(blsPrivKey),
		})

		// Nghỉ 1s giữa các lần tạo
		if i < count-1 {
			time.Sleep(1 * time.Second)
		}
	}

	fmt.Println("\n  ✅ BLS Registration flow completed!")
	if len(results) > 0 {
		outBytes, err := json.MarshalIndent(results, "", "  ")
		if err == nil {
			err = os.WriteFile(outFile, outBytes, 0644)
			if err == nil {
				fmt.Printf("  💾 Đã lưu thành công %d keys vào file: %s\n", len(results), outFile)
			} else {
				fmt.Printf("  ❌ Không thể lưu file %s: %v\n", outFile, err)
			}
		}
	}
}

// ===================== TEST: Free Gas Admin Management =====================

func testFreeGasAdmin(
	tcpClient *client_tcp.Client,
	accountABI abi.ABI,
	accountContract common.Address,
	// root owner
	ownerPrivKey *ecdsa.PrivateKey,
	ownerAddr common.Address,
	// user được cấp quyền add contract
	userPrivKey *ecdsa.PrivateKey,
	userAddr common.Address,
	signer e_types.Signer,
) {
	fmt.Println("\n╔══════════════════════════════════════════════════════╗")
	fmt.Println("║  TEST: Free Gas Admin Management                     ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")
	fmt.Printf("  Root Owner : %s\n", ownerAddr.Hex())
	fmt.Printf("  User        : %s\n", userAddr.Hex())

	// ─── Dummy contract addresses dùng để test ───
	contract1 := common.HexToAddress("0x1111111111111111111111111111111111111111")
	contract2 := common.HexToAddress("0x2222222222222222222222222222222222222222")

	// helper: gọi eth_call getAllAdmins / getAllAuthorizedWallets / getAllContractFreeGas / getMyContracts
	ethCallList := func(label string, callerPrivKey *ecdsa.PrivateKey, callerAddr common.Address, method string, args ...interface{}) {
		data, err := accountABI.Pack(method, args...)
		if err != nil {
			fmt.Printf("  ❌ Pack %s: %v\n", method, err)
			return
		}
		// eth_call dùng fromAddress từ callerAddr — cần sign giả
		// TCP eth_call lấy fromAddress từ caller context nên ta cần gửi qua sendTx eth_call-style
		// Thực tế client dùng RpcEthCallFrom nếu có, hoặc ta tạo signed tx nhưng gọi eth_call.
		// Dưới đây dùng RpcEthCall với callerAddr (client_tcp tự đính kèm from address)
		_ = callerPrivKey // unused, client picks from identity
		_ = callerAddr
		result, err := tcpClient.RpcEthCall(accountContract, data)
		if err != nil {
			fmt.Printf("  ❌ eth_call %s (%s): %v\n", method, label, err)
			return
		}
		fmt.Printf("  ✅ %s → %s\n", label, string(result))
	}

	// ═══════════════════════════════════════════════════════════
	// PHASE 1: Root Owner thêm user vào Authorized Wallets
	// ═══════════════════════════════════════════════════════════
	fmt.Println("\n─── Phase 1: Root Owner cấp quyền cho User ───")

	fmt.Printf("  ► addAuthorizedWallet(%s)\n", userAddr.Hex())
	if _, ok := sendTxFreeGas(tcpClient, ownerPrivKey, ownerAddr, accountContract, accountABI, signer,
		"addAuthorizedWallet", userAddr); !ok {
		fmt.Println("  ⚠️  addAuthorizedWallet thất bại (có thể đã tồn tại)")
	}

	// ─── getAllAuthorizedWallets (root owner call) ───
	fmt.Println("\n─── getAllAuthorizedWallets (page=0, size=10) ───")
	ethCallList("authorized wallets", ownerPrivKey, ownerAddr,
		"getAllAuthorizedWallets", big.NewInt(0), big.NewInt(10))

	// ═══════════════════════════════════════════════════════════
	// PHASE 2: User thêm contracts vào Free Gas
	// ═══════════════════════════════════════════════════════════
	fmt.Println("\n─── Phase 2: User thêm contracts ───")

	fmt.Printf("  ► addContractFreeGas(%s)\n", contract1.Hex())
	sendTxFreeGas(tcpClient, userPrivKey, userAddr, accountContract, accountABI, signer,
		"addContractFreeGas", contract1)

	fmt.Printf("  ► addContractFreeGas(%s)\n", contract2.Hex())
	sendTxFreeGas(tcpClient, userPrivKey, userAddr, accountContract, accountABI, signer,
		"addContractFreeGas", contract2)

	// ─── getMyContracts — Owner query danh sách contract của user bằng cách trả adder=userAddr
	// (eth_call từ client luôn dùng fromAddress của client = owner,
	//  nhưng ta chỉ định adder=userAddr để lấy contract của user)
	fmt.Println("\n─── getMyContracts (query contracts của user, page=0, size=10) ───")
	ethCallList("my contracts", ownerPrivKey, ownerAddr,
		"getMyContracts", userAddr, big.NewInt(0), big.NewInt(10))

	// ═══════════════════════════════════════════════════════════
	// PHASE 3: Thử nghiệm phân quyền — User khác không remove được
	// ═══════════════════════════════════════════════════════════
	fmt.Println("\n─── Phase 3: Root Owner (KHÔNG phải adder) remove contract của User ─── (expect: OK vì root owner có quyền tuyệt đối)")
	fmt.Printf("  ► removeContractFreeGas(%s) bởi root owner\n", contract2.Hex())
	sendTxFreeGas(tcpClient, ownerPrivKey, ownerAddr, accountContract, accountABI, signer,
		"removeContractFreeGas", contract2)

	// ─── getMyContracts sau khi xóa 1 ───
	fmt.Println("\n─── getMyContracts (sau khi xóa contract2) ───")
	ethCallList("my contracts after remove", userPrivKey, userAddr,
		"getMyContracts", userAddr, big.NewInt(0), big.NewInt(10))

	// ═══════════════════════════════════════════════════════════
	// PHASE 4: User tự remove contract của mình
	// ═══════════════════════════════════════════════════════════
	fmt.Println("\n─── Phase 4: User tự remove contract của mình ───")
	fmt.Printf("  ► removeContractFreeGas(%s) bởi user\n", contract1.Hex())
	sendTxFreeGas(tcpClient, userPrivKey, userAddr, accountContract, accountABI, signer,
		"removeContractFreeGas", contract1)

	// ─── getMyContracts sau khi xóa hết ───
	fmt.Println("\n─── getMyContracts (sau khi xóa hết) ───")
	ethCallList("my contracts (empty)", ownerPrivKey, ownerAddr,
		"getMyContracts", userAddr, big.NewInt(0), big.NewInt(10))

	// ═══════════════════════════════════════════════════════════
	// PHASE 5: Root Owner thu hồi quyền của User
	// ═══════════════════════════════════════════════════════════
	fmt.Println("\n─── Phase 5: Root Owner thu hồi quyền User ───")
	fmt.Printf("  ► removeAuthorizedWallet(%s)\n", userAddr.Hex())
	sendTxFreeGas(tcpClient, ownerPrivKey, ownerAddr, accountContract, accountABI, signer,
		"removeAuthorizedWallet", userAddr)

	// ─── User thử thêm contract sau khi bị thu hồi → expect error ───
	fmt.Println("\n─── Phase 5b: User thêm contract sau khi bị thu hồi (expect REVERT) ───")
	sendTxFreeGasExpectError(tcpClient, userPrivKey, userAddr, accountContract, accountABI, signer,
		"addContractFreeGas", contract1)

	// ─── getAllAuthorizedWallets cuối cùng ───
	fmt.Println("\n─── getAllAuthorizedWallets (final) ───")
	ethCallList("authorized wallets (final)", ownerPrivKey, ownerAddr,
		"getAllAuthorizedWallets", big.NewInt(0), big.NewInt(10))

	fmt.Println("\n  ✅ Free Gas Admin test completed!")
}

// ===================== TEST: Chain Direct =====================

func testChainDirect(tcpClient *client_tcp.Client) {
	fmt.Println("\n╔══════════════════════════════════════════════════════╗")
	fmt.Println("║  TEST: Chain-Direct (ChainId, BlockNumber, GetLogs)  ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")

	// 1. ChainGetChainId
	fmt.Println("\n─── 1. ChainGetChainId ───")
	chainId, err := tcpClient.ChainGetChainId()
	if err != nil {
		fmt.Printf("  ❌ ChainGetChainId: %v\n", err)
	} else {
		fmt.Printf("  ✅ ChainId = %d\n", chainId)
	}

	// 2. ChainGetBlockNumber
	fmt.Println("\n─── 2. ChainGetBlockNumber ───")
	blockNum, err := tcpClient.ChainGetBlockNumber()
	if err != nil {
		fmt.Printf("  ❌ ChainGetBlockNumber: %v\n", err)
	} else {
		fmt.Printf("  ✅ BlockNumber = %d\n", blockNum)
	}

	// 3. ChainGetLogs (latest block range)
	fmt.Println("\n─── 3. ChainGetLogs ───")
	if blockNum > 0 {
		fromBlock := fmt.Sprintf("0x%x", blockNum-1)
		toBlock := fmt.Sprintf("0x%x", blockNum)
		fmt.Printf("  ℹ️  Querying logs from block %s to %s\n", fromBlock, toBlock)

		logsResp, err := tcpClient.ChainGetLogs(nil, fromBlock, toBlock, nil, nil)
		if err != nil {
			fmt.Printf("  ❌ ChainGetLogs: %v\n", err)
		} else {
			fmt.Printf("  ✅ Got %d logs\n", len(logsResp.Logs))
			for i, log := range logsResp.Logs {
				fmt.Printf("    [%d] addr=%x block=%d txHash=%x topics=%d\n",
					i,
					log.Address[:6],
					log.BlockNumber,
					log.TransactionHash[:6],
					len(log.Topics),
				)
				if i >= 4 {
					fmt.Printf("    ... (%d more)\n", len(logsResp.Logs)-5)
					break
				}
			}
		}
	} else {
		fmt.Println("  ⚠️ BlockNumber = 0, skipping GetLogs test")
	}
}

// ===================== TEST: Transfer (Native coin) =====================

func testTransfer(
	senderClient *client_tcp.Client,
	receiverClient *client_tcp.Client,
	senderPrivKey *ecdsa.PrivateKey,
	senderAddr common.Address,
	receiverAddr common.Address,
	signer e_types.Signer,
) {
	fmt.Println("\n╔══════════════════════════════════════════════════════╗")
	fmt.Println("║  TEST: Native Transfer (receipt forwarding)          ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")
	fmt.Printf("  Sender:   %s\n", senderAddr.Hex())
	fmt.Printf("  Receiver: %s\n", receiverAddr.Hex())

	// Channel để receiver đợi receipt forwarded từ RPC server
	receivedReceipt := make(chan []byte, 1)

	// Đăng ký callback nhận "Receipt" command trên receiver client
	// (receiver client sẽ nhận receipt qua command "Receipt" khi sender chuyển tiền)
	receiverClient.GetClientContext().Handler.RegisterReceiptCallback(func(data []byte) {
		fmt.Printf("\n  📨 [RECEIVER] Got forwarded receipt! (%d bytes)\n", len(data))
		rcpt := &pb.Receipt{}
		if err := proto.Unmarshal(data, rcpt); err == nil {
			txHash := common.BytesToHash(rcpt.TransactionHash).Hex()
			from := common.BytesToAddress(rcpt.FromAddress).Hex()
			to := common.BytesToAddress(rcpt.ToAddress).Hex()
			fmt.Printf("  ├─ TxHash: %s\n", txHash)
			fmt.Printf("  ├─ From:   %s\n", from)
			fmt.Printf("  ├─ To:     %s\n", to)
			amt := new(big.Int).SetBytes(rcpt.Amount)
			fmt.Printf("  ├─ Amount: %s\n", amt.String())
			fmt.Printf("  ├─ Status: %d\n", rcpt.Status)
			fmt.Printf("  └─ GasUsed: %d\n", rcpt.GasUsed)
		}
		receivedReceipt <- data
	})

	// Sender chuyển 1 wei cho receiver
	amount := big.NewInt(1) // 1 wei
	fmt.Printf("\n─── Sender chuyển %s wei cho Receiver ───\n", amount.String())

	nonce, err := senderClient.GetNonce(senderAddr)
	if err != nil {
		fmt.Printf("  ❌ GetNonce: %v\n", err)
		return
	}

	tx := e_types.NewTransaction(nonce, receiverAddr, amount, 21000, big.NewInt(10000000), nil)
	signedTx, _ := e_types.SignTx(tx, signer, senderPrivKey)
	rawTxBytes, _ := signedTx.MarshalBinary()
	rawTxHex := "0x" + hex.EncodeToString(rawTxBytes)

	txHash, rcptBytes, err := senderClient.RpcSendRawTransactionWithReceipt(rawTxHex)
	if err != nil {
		fmt.Printf("  ❌ SendTx: %v\n", err)
		return
	}
	fmt.Printf("  ✅ Sender txHash: %s\n", txHash)

	// Parse sender receipt
	senderRcpt := &pb.Receipt{}
	if err := proto.Unmarshal(rcptBytes, senderRcpt); err == nil {
		rpcReceipt := protoReceiptToRpcReceipt(senderRcpt)
		fmt.Printf("  ✅ Sender receipt: status=%s, gasUsed=%s\n", rpcReceipt.Status, rpcReceipt.GasUsed)
	}

	// Đợi receiver nhận receipt forwarded (max 10s)
	fmt.Println("\n─── Đợi Receiver nhận receipt forwarded (max 10s) ───")
	select {
	case <-receivedReceipt:
		fmt.Println("  ✅ Receiver đã nhận receipt forwarded thành công!")
	case <-time.After(10 * time.Second):
		fmt.Println("  ⚠️ Timeout: Receiver không nhận được receipt forwarded")
	}

	fmt.Println("\n  ✅ Transfer test completed!")
}

// ===================== MAIN =====================

func main() {
	logger.SetConfig(&logger.LoggerConfig{
		Flag:    logger.FLAG_INFO,
		Outputs: []*os.File{os.Stdout},
	})

	configPath := flag.String("config", "config-test.json", "Path to TCP RPC client config")
	receiverConfigPath := flag.String("receiver-config", "config-main.json", "Path to receiver TCP client config (for transfer test)")
	testSuite := flag.String("test", "all", "Test suite: demo, bls, chain, transfer, all")
	blsCount := flag.Int("count", 1, "Number of BLS keys to generate (for -test bls)")
	outJson := flag.String("out", "bls_keys.json", "Output JSON file for generated BLS keys")
	flag.Parse()

	fmt.Println("╔══════════════════════════════════════════════════════╗")
	fmt.Println("║       TCP-RPC Test Suite (Proto)                     ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")

	cfgRaw, _ := tcp_config.LoadConfig(*configPath)
	cfg := cfgRaw.(*tcp_config.ClientConfig)

	// Khởi tạo RPC client (dùng cho gửi transaction, eth_call, subscribe, ...)
	tcpClient, err := client_tcp.NewClient(cfg)
	if err != nil {
		logger.Error("Failed to create TCP RPC client: %v", err)
		os.Exit(1)
	}
	time.Sleep(1 * time.Second)

	// Common setup
	ethPrivKey, _ := crypto.HexToECDSA(cfg.EthPrivateKey)
	fromAddr := crypto.PubkeyToAddress(ethPrivKey.PublicKey)
	chainIdBig := big.NewInt(int64(cfg.ChainId))
	signer := e_types.NewEIP155Signer(chainIdBig)

	fmt.Printf("\n  Admin address: %s\n", fromAddr.Hex())
	fmt.Printf("  Chain ID: %d\n", cfg.ChainId)

	switch *testSuite {
	case "freegas", "admin":
		accountABI, _ := abi.JSON(strings.NewReader(accountAbiJSON))
		accountContract := common.HexToAddress("0x00000000000000000000000000000000D844bb55")
		// User được cấp quyền
		userPrivKey, err := crypto.HexToECDSA("fb64857fe95b55dff91a11d2da0c8db2dddb29f617d3d1ddaa9a9880733d5407")
		if err != nil {
			fmt.Printf("  ❌ Invalid user private key: %v\n", err)
			os.Exit(1)
		}
		userAddr := common.HexToAddress("0x5e582475A504998c5631E12A5a2585D2B1911812")
		testFreeGasAdmin(tcpClient, accountABI, accountContract,
			ethPrivKey, fromAddr,
			userPrivKey, userAddr,
			signer)
	case "demo":
		demoAbiBytes, _ := os.ReadFile(cfg.DemoAbiPath_)
		demoABI, _ := abi.JSON(strings.NewReader(string(demoAbiBytes)))
		contractAddr := common.HexToAddress(cfg.DemoContractAddress)
		testDemoContract(tcpClient, demoABI, contractAddr, ethPrivKey, fromAddr, signer)
	case "bls":
		accountABI, _ := abi.JSON(strings.NewReader(accountAbiJSON))
		accountContract := common.HexToAddress("0x00000000000000000000000000000000D844bb55")
		testBlsRegistration(tcpClient, accountABI, accountContract, ethPrivKey, fromAddr, signer, *blsCount, *outJson)
	case "chain":
		if !strings.HasSuffix(cfg.ParentConnectionAddress, ":4200") {
			fmt.Printf("\n  ❌ Chain-direct test yêu cầu kết nối trực tiếp đến chain (port 4200)\n")
			fmt.Printf("     Config hiện tại: %s\n", cfg.ParentConnectionAddress)
			fmt.Printf("     Hãy đổi parent_connection_address thành \" 139.59.243.85:4200\" trong config\n")
			os.Exit(1)
		}
		testChainDirect(tcpClient)
	case "all":
		// Chain-direct test
		testChainDirect(tcpClient)

		// Demo test
		demoAbiBytes, _ := os.ReadFile(cfg.DemoAbiPath_)
		demoABI, _ := abi.JSON(strings.NewReader(string(demoAbiBytes)))
		contractAddr := common.HexToAddress(cfg.DemoContractAddress)
		testDemoContract(tcpClient, demoABI, contractAddr, ethPrivKey, fromAddr, signer)

		// BLS test
		accountABI, _ := abi.JSON(strings.NewReader(accountAbiJSON))
		accountContract := common.HexToAddress("0x00000000000000000000000000000000D844bb55")
		testBlsRegistration(tcpClient, accountABI, accountContract, ethPrivKey, fromAddr, signer, *blsCount, *outJson)

	case "transfer":
		// Load receiver config
		rcvrCfgRaw, rcvrErr := tcp_config.LoadConfig(*receiverConfigPath)
		if rcvrErr != nil {
			fmt.Printf("  ❌ Load receiver config (%s): %v\n", *receiverConfigPath, rcvrErr)
			os.Exit(1)
		}
		rcvrCfg := rcvrCfgRaw.(*tcp_config.ClientConfig)
		receiverClient, rcvrConnErr := client_tcp.NewClient(rcvrCfg)
		if rcvrConnErr != nil {
			fmt.Printf("  ❌ Create receiver client: %v\n", rcvrConnErr)
			os.Exit(1)
		}
		time.Sleep(1 * time.Second)
		receiverAddr := common.HexToAddress("0x5e582475A504998c5631E12A5a2585D2B1911812")
		fmt.Printf("  Receiver address: %s\n", receiverAddr.Hex())
		testTransfer(tcpClient, receiverClient, ethPrivKey, fromAddr, receiverAddr, signer)
	default:
		fmt.Printf("  ❌ Unknown test suite: %s (use: demo, bls, chain, freegas, all)\n", *testSuite)
	}

	fmt.Println("\n╔══════════════════════════════════════════════════════╗")
	fmt.Println("║       All tests completed!                           ║")
	fmt.Println("╚══════════════════════════════════════════════════════╝")
}

// Account ABI (chỉ các function cần dùng)
const accountAbiJSON = `[
	{
		"anonymous": false,
		"inputs": [
			{"indexed": false, "internalType": "address", "name": "account", "type": "address"},
			{"indexed": false, "internalType": "uint256", "name": "time", "type": "uint256"},
			{"indexed": false, "internalType": "bytes", "name": "publicKey", "type": "bytes"},
			{"indexed": false, "internalType": "string", "name": "message", "type": "string"}
		],
		"name": "RegisterBls",
		"type": "event"
	},
	{
		"anonymous": false,
		"inputs": [
			{"indexed": false, "internalType": "address", "name": "account", "type": "address"},
			{"indexed": false, "internalType": "uint256", "name": "time", "type": "uint256"},
			{"indexed": false, "internalType": "string", "name": "message", "type": "string"}
		],
		"name": "AccountConfirmed",
		"type": "event"
	},
	{"inputs":[{"internalType":"bytes","name":"_publicKey","type":"bytes"}],"name":"setBlsPublicKey","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"_account","type":"address"}],"name":"confirmAccountWithoutSign","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[],"name":"getPublickeyBls","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"walletAddress","type":"address"}],"name":"addAuthorizedWallet","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"walletAddress","type":"address"}],"name":"removeAuthorizedWallet","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"uint256","name":"page","type":"uint256"},{"internalType":"uint256","name":"pageSize","type":"uint256"}],"name":"getAllAuthorizedWallets","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"adminAddress","type":"address"}],"name":"addAdmin","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"adminAddress","type":"address"}],"name":"removeAdmin","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"uint256","name":"page","type":"uint256"},{"internalType":"uint256","name":"pageSize","type":"uint256"}],"name":"getAllAdmins","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"contractAddress","type":"address"}],"name":"addContractFreeGas","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"contractAddress","type":"address"}],"name":"removeContractFreeGas","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"internalType":"address","name":"adder","type":"address"},{"internalType":"uint256","name":"page","type":"uint256"},{"internalType":"uint256","name":"pageSize","type":"uint256"}],"name":"getMyContracts","outputs":[],"stateMutability":"nonpayable","type":"function"}
]`
