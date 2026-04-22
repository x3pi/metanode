package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type PeerInfoResponse struct {
	NodeID                int    `json:"node_id"`
	Epoch                 uint64 `json:"epoch"`
	LastBlock             uint64 `json:"last_block"`
	LastGlobalExecIndex   uint64 `json:"last_global_exec_index"`
	NetworkAddress        string `json:"network_address"`
	TimestampMs           uint64 `json:"timestamp_ms"`
	StateRoot             string `json:"state_root"`
}

var ports = []int{19200, 19201, 19202, 19203, 19204}

func main() {
	fmt.Println("🚀 Bắt đầu quét StateRoot qua giao thức Peer Attestation P2P...")
	for {
		for i, port := range ports {
			queryStateRoot(i, port)
		}
		fmt.Println("-----------------------------------------------------------------------------------------------------------------------------------------")
		time.Sleep(5 * time.Second)
	}
}

func queryStateRoot(nodeID int, port int) {
	url := fmt.Sprintf("http://127.0.0.1:%d/peer_info", port)
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		fmt.Printf("❌ Mất kết nối tới Node %d (Port %d)\n", nodeID, port)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("⚠️  Node %d (Port %d) lỗi đọc dữ liệu\n", nodeID, port)
		return
	}

	var info PeerInfoResponse
	err = json.Unmarshal(body, &info)
	if err != nil {
		fmt.Printf("⚠️  Node %d (Port %d) lỗi parse JSON\n", nodeID, port)
		return
	}

	fmt.Printf("✅ Node %d | Khối: %-5d | Epoch: %-3d | StateRoot: %s\n", nodeID, info.LastBlock, info.Epoch, info.StateRoot)
}
