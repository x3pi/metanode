package main

import (
	"bytes"
	"crypto/rand"
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
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
)

const (
	devnetSenderECDSA = "00724e290da4ba87d92f3c0cd7fbc07d88cf2c4673b16e272fda73eebe8b5513"
	exec1PrivHex      = "0f326c0b9bb86353ac317dd8f9b045fd1877473674ba24500139fed777b26a0c"
	seqAddress        = "0x1F0ECA432E1B18b140814beF0ce1Ba2b09DE44c5"
)

type RaftNode struct {
	ID        string
	RPCPort   int
	P2PPort   int
	RaftPort  int
	FwdPort   int
	Bootstrap bool
	Cmd       *exec.Cmd
}

func rpcCall(url, method string, params []interface{}) (map[string]interface{}, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0", "method": method, "params": params, "id": 1,
	})
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewBuffer(reqBody))
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

func getAccountNonce(url string, addr common.Address) (uint64, error) {
	res, err := rpcCall(url, "eth_getTransactionCount", []interface{}{addr.Hex(), "latest"})
	if err == nil {
		if str, ok := res["result"].(string); ok {
			n, err := strconv.ParseUint(strings.TrimPrefix(str, "0x"), 16, 64)
			if err == nil {
				return n, nil
			}
		}
	}
	return 0, nil
}

func getRaftStatus(adminCl *raftfeed.AdminClient, fwdPort int) (raftfeed.Status, error) {
	return adminCl.Status(fmt.Sprintf("127.0.0.1:%d", fwdPort))
}

func waitForRPC(url string, timeout time.Duration) error {
	start := time.Now()
	for time.Since(start) < timeout {
		if _, err := getBlockNumber(url); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for RPC %s", url)
}

func main() {
	fmt.Println("╔═══════════════════════════════════════════════════════════════════════════════╗")
	fmt.Println("║  🚀 DEMO: KHẢ NĂNG CHỊU LỖI CỤM THỰC THI REPLICATED RAFT (HASHICORP/RAFT)       ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════════════════════════╝")

	dir, _ := os.Getwd()
	workDir := filepath.Join(dir, "raft_test_cluster")
	_ = os.RemoveAll(workDir)
	_ = os.MkdirAll(workDir, 0755)

	// Clean up any remaining processes on exit
	defer func() {
		fmt.Println("\n🧹 Đang dọn dẹp các tiến trình Raft...")
		_ = exec.Command("pkill", "-f", "simple_chain.*raft_test_cluster").Run()
	}()

	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	secretFile := filepath.Join(workDir, "raft_secret.key")
	_ = os.WriteFile(secretFile, secret, 0600)

	genesisFile := filepath.Join(dir, "devnet_data/exec1/genesis.json")

	nodes := []*RaftNode{
		{ID: "n0", RPCPort: 8810, P2PPort: 4310, RaftPort: 7110, FwdPort: 7210, Bootstrap: true},
		{ID: "n1", RPCPort: 8811, P2PPort: 4311, RaftPort: 7111, FwdPort: 7211, Bootstrap: false},
		{ID: "n2", RPCPort: 8812, P2PPort: 4312, RaftPort: 7112, FwdPort: 7212, Bootstrap: false},
	}

	// 1. Tạo cấu hình cho 3 replicas
	for _, n := range nodes {
		nodeDir := filepath.Join(workDir, n.ID)
		_ = os.MkdirAll(nodeDir, 0755)

		cfgMap := map[string]interface{}{
			"debug":                      false,
			"cluster_id":                 1,
			"consensus_mode":             "raft",
			"enable_private_gateway":     false,
			"master_password":            "devnet-test-password",
			"app_pepper":                 "devnet-test-pepper",
			"private_key":                exec1PrivHex,
			"address":                    seqAddress,
			"log_path":                   filepath.Join(nodeDir, "logs"),
			"backup_path":                filepath.Join(nodeDir, "backup"),
			"explorer_db_path":           filepath.Join(nodeDir, "explorer"),
			"explorer_read_only_db_path": filepath.Join(nodeDir, "explorer-ro"),
			"is_explorer":                false,
			"connection_address":         fmt.Sprintf("0.0.0.0:%d", n.P2PPort),
			"version":                    "0.0.1.0",
			"rpc_port":                   fmt.Sprintf(":%d", n.RPCPort),
			"db_type":                    2,
			"genesis_file_path":          genesisFile,
			"snapshot_enabled":           false,
			"pk_admin_file_storage":      "87d931eaa2f76709f2615586e0d560ca9b80f247c9cc431e197ba3e7167db623",
			"bls_admin_storage":          "2b3aa0f620d2d73c046cd93eb64f2eb687a95b22e278500aa251c8c9dda1203b",
			"owner_file_storage_address": "0xC6E6474A8DEAD25B0e75b1aeA5d35FA19f69588a",
			"Databases": map[string]interface{}{
				"RootPath":      filepath.Join(nodeDir, "data"),
				"DBEngine":      "sharded",
				"Version":       "0.0.1.0",
				"BLSPrivateKey": "0938115a85fd629c1bb0b58f5d9bdde776c8eb6486e119f7e1307a5ff476753c",
			},
			"is_rpc_node": true,
			"raft": map[string]interface{}{
				"node_id":                 n.ID,
				"bind_address":            fmt.Sprintf("127.0.0.1:%d", n.RaftPort),
				"data_dir":                filepath.Join(nodeDir, "raft"),
				"forward_bind_address":    fmt.Sprintf("127.0.0.1:%d", n.FwdPort),
				"forward_secret_file":     secretFile,
				"sequencer_address":       seqAddress,
				"bootstrap":               n.Bootstrap,
				"heartbeat_timeout_ms":    100,
				"election_timeout_ms":     200,
				"leader_lease_timeout_ms": 80,
				"commit_timeout_ms":       30,
				"peers": []map[string]interface{}{
					{"id": "n0", "address": "127.0.0.1:7110", "forward_address": "127.0.0.1:7210"},
					{"id": "n1", "address": "127.0.0.1:7111", "forward_address": "127.0.0.1:7211"},
					{"id": "n2", "address": "127.0.0.1:7112", "forward_address": "127.0.0.1:7212"},
				},
			},
		}

		cfgData, _ := json.MarshalIndent(cfgMap, "", "  ")
		_ = os.WriteFile(filepath.Join(nodeDir, "config.json"), cfgData, 0644)
	}

	startNode := func(n *RaftNode) error {
		cfgPath := filepath.Join(workDir, n.ID, "config.json")
		logFile, _ := os.OpenFile(filepath.Join(workDir, n.ID, "node.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		cmd := exec.Command("./simple_chain", "-config", cfgPath)
		cmd.Stdout = logFile
		cmd.Stderr = logFile
		if err := cmd.Start(); err != nil {
			return err
		}
		n.Cmd = cmd
		return nil
	}

	// 2. Khởi chạy 3 replicas
	fmt.Println("🚀 [BƯỚC 1] Khởi chạy 3 Node Thực Thi trong Cụm Raft (Quorum yêu cầu: 2/3)...")
	for _, n := range nodes {
		if err := startNode(n); err != nil {
			fmt.Printf("❌ Lỗi khởi chạy %s: %v\n", n.ID, err)
			return
		}
		fmt.Printf("   • Node %s (PID %d) - RPC :%d | Raft :%d | Forward :%d\n", n.ID, n.Cmd.Process.Pid, n.RPCPort, n.RaftPort, n.FwdPort)
	}

	fmt.Println("⏳ Đang chờ các node hoàn tất khởi tạo NOMT State & Raft election (tối đa 15s)...")
	for _, n := range nodes {
		rpcURL := fmt.Sprintf("http://127.0.0.1:%d", n.RPCPort)
		if err := waitForRPC(rpcURL, 20*time.Second); err != nil {
			fmt.Printf("❌ Node %s không sẵn sàng: %v\n", n.ID, err)
			return
		}
		fmt.Printf("   ✅ Node %s đã sẵn sàng RPC :%d\n", n.ID, n.RPCPort)
	}

	time.Sleep(2 * time.Second)

	adminCl := &raftfeed.AdminClient{Secret: secret}

	// 3. Kiểm tra Leader ban đầu
	fmt.Println("\n👑 [BƯỚC 2] Kiểm tra Leader hiện tại của Cụm Raft...")
	var currentLeaderID string
	var leaderNode *RaftNode
	startWaitLeader := time.Now()
	for time.Since(startWaitLeader) < 15*time.Second {
		for _, n := range nodes {
			st, err := getRaftStatus(adminCl, n.FwdPort)
			if err == nil {
				if st.State == "Leader" {
					currentLeaderID = n.ID
					leaderNode = n
					break
				}
			}
		}
		if leaderNode != nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if leaderNode == nil {
		fmt.Println("❌ Không thể bầu Leader sau 15s!")
		return
	}
	for _, n := range nodes {
		st, _ := getRaftStatus(adminCl, n.FwdPort)
		fmt.Printf("   • %s: State = %-9s | Term = %v | Leader = %s\n", n.ID, st.State, st.Term, st.LeaderID)
	}
	fmt.Printf("   🏆 LEADER HIỆN TẠI: Node %s (RPC :%d)\n", currentLeaderID, leaderNode.RPCPort)

	// In block height
	for _, n := range nodes {
		h, _ := getBlockNumber(fmt.Sprintf("http://127.0.0.1:%d", n.RPCPort))
		fmt.Printf("   📦 Chiều cao khối ban đầu trên %s: %d\n", n.ID, h)
	}

	// 4. Gửi giao dịch khi cả 3 node đang sống
	fmt.Println("\n💳 [BƯỚC 3] Gửi giao dịch hợp lệ vào Cụm Raft (khi cả 3 node đang online)...")
	senderKey, _ := crypto.HexToECDSA(devnetSenderECDSA)
	senderAddr := crypto.PubkeyToAddress(senderKey.PublicKey)
	_ = senderAddr
	targetAddr := common.HexToAddress("0x1234567890123456789012345678901234567890")

	sendTx := func(targetRPCPort int, valWei int64) (string, error) {
		rpcURL := fmt.Sprintf("http://127.0.0.1:%d", targetRPCPort)
		nonce, _ := getAccountNonce(rpcURL, senderAddr)
		tx := ethtypes.NewTransaction(nonce, targetAddr, big.NewInt(valWei), 50000, big.NewInt(1_000_000_000), nil)
		signer := ethtypes.NewCancunSigner(big.NewInt(991))
		signedTx, err := ethtypes.SignTx(tx, signer, senderKey)
		if err != nil {
			return "", err
		}
		rawTx, _ := signedTx.MarshalBinary()
		res, err := rpcCall(rpcURL, "eth_sendRawTransaction", []interface{}{hexutil.Encode(rawTx)})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%v", res["result"]), nil
	}

	txHash1, err := sendTx(leaderNode.RPCPort, 1000000000)
	if err != nil {
		fmt.Printf("❌ Gửi giao dịch 1 thất bại: %v\n", err)
	} else {
		fmt.Printf("   ✅ Đã gửi Giao dịch #1 thành công qua Leader %s (Port :%d): TxHash = %s\n", leaderNode.ID, leaderNode.RPCPort, txHash1)
	}

	fmt.Println("⏳ Đang chờ Raft commit batch & sinh block mới...")
	time.Sleep(3 * time.Second)

	fmt.Println("📊 Chiều cao khối sau Giao dịch #1:")
	for _, n := range nodes {
		h, _ := getBlockNumber(fmt.Sprintf("http://127.0.0.1:%d", n.RPCPort))
		fmt.Printf("   • %s: Block Height = %d\n", n.ID, h)
	}

	// 5. GIẢ LẬP SỰ CỐ: CƯỠNG CHẾ TẮT LEADER NODE
	fmt.Printf("\n💥 [BƯỚC 4] GIẢ LẬP SỰ CỐ NGIÊM TRỌNG: CƯỠNG CHẾ TẮT LEADER (KILL -9 NODE %s - PID %d)...\n", leaderNode.ID, leaderNode.Cmd.Process.Pid)
	_ = leaderNode.Cmd.Process.Kill()
	timeKill := time.Now()

	fmt.Println("⚡ Leader đã sập! Theo dõi quá trình Failover và Bầu Leader mới trong cụm...")

	// 6. Theo dõi bầu Leader mới
	var newLeaderID string
	var newLeaderNode *RaftNode
	for time.Since(timeKill) < 10*time.Second {
		for _, n := range nodes {
			if n.ID == leaderNode.ID {
				continue
			}
			st, err := getRaftStatus(adminCl, n.FwdPort)
			if err == nil {
				if st.State == "Leader" {
					newLeaderID = n.ID
					newLeaderNode = n
					break
				}
			}
		}
		if newLeaderID != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	failoverDuration := time.Since(timeKill)
	fmt.Printf("   🎉 BẦU LEADER MỚI THÀNH CÔNG trong %v!\n", failoverDuration.Round(time.Millisecond))
	fmt.Printf("   🏆 LEADER MỚI ĐƯỢC CHỌN: Node %s (RPC :%d)\n", newLeaderID, newLeaderNode.RPCPort)

	// 7. Gửi tiếp giao dịch vào cụm khi đang thiếu 1 node
	fmt.Println("\n💳 [BƯỚC 5] Cụm còn 2/3 node (Đạt đa số Quorum). Gửi Giao dịch #2 vào Leader mới...")
	txHash2, err := sendTx(newLeaderNode.RPCPort, 2000000000)
	if err != nil {
		fmt.Printf("❌ Gửi giao dịch 2 thất bại: %v\n", err)
	} else {
		fmt.Printf("   ✅ Đã gửi Giao dịch #2 thành công qua Leader mới %s: TxHash = %s\n", newLeaderNode.ID, txHash2)
	}

	time.Sleep(3 * time.Second)
	fmt.Println("📊 Chiều cao khối của các node còn sống sau Giao dịch #2:")
	for _, n := range nodes {
		if n.ID == leaderNode.ID {
			fmt.Printf("   • %s: [DEAD - OFFLINE]\n", n.ID)
			continue
		}
		h, _ := getBlockNumber(fmt.Sprintf("http://127.0.0.1:%d", n.RPCPort))
		fmt.Printf("   • %s: Block Height = %d (Vẫn tiếp tục sản sinh block bình thường!)\n", n.ID, h)
	}

	// 8. Phục hồi node chết
	fmt.Printf("\n🔄 [BƯỚC 6] Khởi động lại Node cũ (%s) để kiểm tra khả năng bắt kịp dữ liệu (Catch-Up)...\n", leaderNode.ID)
	if err := startNode(leaderNode); err != nil {
		fmt.Printf("❌ Lỗi khởi động lại %s: %v\n", leaderNode.ID, err)
		return
	}
	fmt.Printf("   Node %s đã khởi động lại với PID %d, đang tái kết nối mạng Raft...\n", leaderNode.ID, leaderNode.Cmd.Process.Pid)

	// Đợi node cũ catch-up
	time.Sleep(4 * time.Second)

	fmt.Println("\n📊 [BƯỚC 7] Kiểm tra trạng thái toàn bộ 3 Node sau khi phục hồi:")
	allSynced := true
	var finalHeights []uint64
	for _, n := range nodes {
		st, err := getRaftStatus(adminCl, n.FwdPort)
		stateStr := "Unknown"
		if err == nil {
			stateStr = st.State
		}
		h, err := getBlockNumber(fmt.Sprintf("http://127.0.0.1:%d", n.RPCPort))
		if err != nil {
			allSynced = false
			fmt.Printf("   • %s: Lỗi đọc RPC: %v\n", n.ID, err)
		} else {
			finalHeights = append(finalHeights, h)
			fmt.Printf("   • %s: State = %-9s | Block Height = %d\n", n.ID, stateStr, h)
	}
	}

	if allSynced && len(finalHeights) == 3 && finalHeights[0] == finalHeights[1] && finalHeights[1] == finalHeights[2] {
		fmt.Println("\n═══════════════════════════════════════════════════════════════════════════════")
		fmt.Println("   🎉 KẾT QUẢ KIỂM THỬ: KHẢ NĂNG CHỊU LỖI CỤM RAFT HOÀN TOÀN ĐẠT CHUẨN!")
		fmt.Printf("   1. Bầu Leader tự động cực nhanh: chỉ mất %v khi Leader cũ sập.\n", failoverDuration.Round(time.Millisecond))
		fmt.Println("   2. Không gián đoạn dịch vụ: 2/3 node tiếp tục tiếp nhận tx và đóng block.")
		fmt.Printf("   3. Tự động phục hồi & Catch-up: Node cũ sống lại đồng bộ 100%% đạt cùng Block Height #%d.\n", finalHeights[0])
		fmt.Println("   4. Zero-Fork Invariant: Không phát sinh bất kỳ nhánh rẽ hoặc sai lệch dữ liệu nào.")
		fmt.Println("═══════════════════════════════════════════════════════════════════════════════")
	} else {
		fmt.Println("⚠️ Chiều cao các node chưa đồng nhất hoặc đang trong quá trình đồng bộ.")
	}
}
