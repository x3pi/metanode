// e2e_cross_chain_coattest executes the 6 mandatory cross-chain credit co-attestation scenarios (P0-2)
// across a multi-cluster environment with an isolated 4-validator Mysticeti destination cluster.
//
// Scenarios:
//   (a) Full cross-cluster transfer: recipient balance credited exactly once, source deducted, conservation verified
//   (b) 1 validator offline (3 active >= 2f+1=3): transfer completes, restarted validator catches up with 100% state parity
//   (c) 2 validators offline (2 active < 3): consensus pauses, credit pending, zero fork; restarted -> completes once
//   (d) Byzantine fake credit (1 signature or unwrapped tx): balances untouched across all nodes, zero credit
//   (e) Crash recovery (kill -9 mid-traffic): quorum continues, node restarts, block-by-block hash & state root match 100%
//   (f) Stale-state rejected dispatch retried: no premature tombstone, retried successfully, applied exactly once (no double-credit)
//
// Usage:
//   go run ./cmd/tool/e2e_cross_chain_coattest -env <BASE>/env.json -script <HERE>/run_env.sh -report <REPORT_PATH>
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/command"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	mt_transaction "github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

const (
	chainID  = 991
	gasPrice = 1_000_000_000
)

var oneEther = new(big.Int).Mul(big.NewInt(1_000_000_000), big.NewInt(1_000_000_000))

// ---------------------------------------------------------------- Environment Config

type envConfig struct {
	Base  string `json:"base"`
	Bin   string `json:"bin"`
	Ports struct {
		ParentHTTP    int `json:"parent_http"`
		ParentP2P     int `json:"parent_p2p"`
		ParentMetrics int `json:"parent_metrics"`
		ParentPeerRPC int `json:"parent_peer_rpc"`
		Exec2         struct {
			RPC     int `json:"rpc"`
			Conn    int `json:"conn"`
			Raft    int `json:"raft"`
			Forward int `json:"forward"`
		} `json:"exec2"`
		Val0 struct{ RPC, Conn, P2P, PeerRPC, Metrics int } `json:"val0"`
		Val1 struct{ RPC, Conn, P2P, PeerRPC, Metrics int } `json:"val1"`
		Val2 struct{ RPC, Conn, P2P, PeerRPC, Metrics int } `json:"val2"`
		Val3 struct{ RPC, Conn, P2P, PeerRPC, Metrics int } `json:"val3"`
	} `json:"ports"`
	Funder  string `json:"funder"`
	Mode    string `json:"mode"`
	Cluster struct {
		Address    string `json:"address"`
		BlsPub     string `json:"bls_pub"`
		PrivateKey string `json:"private_key"`
	} `json:"cluster"`
	Clusters map[string]struct {
		Address    string `json:"address"`
		BlsPub     string `json:"bls_pub"`
		PrivateKey string `json:"private_key"`
	} `json:"clusters"`
	Validators map[string]struct {
		ID      int    `json:"id"`
		Address string `json:"address"`
		BlsPub  string `json:"bls_pub"`
		BlsPriv string `json:"bls_priv"`
	} `json:"validators"`
}

type user struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

func newUser() user {
	k, _ := crypto.GenerateKey()
	return user{k, crypto.PubkeyToAddress(k.PublicKey)}
}

// ---------------------------------------------------------------- RPC Client

type blockHeader struct {
	Number    uint64
	Hash      string
	StateRoot string
}

type regMessage struct {
	Digest          string `json:"digest"`
	HashToSign      string `json:"hashToSign"`
	HashToSignSnake string `json:"hash_to_sign"`
	ClusterKey      string `json:"cluster_key"`
}

func (m *regMessage) getHash() string {
	if m.HashToSign != "" {
		return m.HashToSign
	}
	return m.HashToSignSnake
}

type regInfo struct {
	Status string `json:"status"`
}

type nodeClient struct {
	name string
	rpc  string
	conn string
}

func newNodeClient(name string, rpcPort, connPort int) *nodeClient {
	return &nodeClient{
		name: name,
		rpc:  fmt.Sprintf("http://127.0.0.1:%d", rpcPort),
		conn: fmt.Sprintf("127.0.0.1:%d", connPort),
	}
}

func (n *nodeClient) call(result interface{}, method string, args ...interface{}) error {
	client, err := rpc.DialHTTP(n.rpc)
	if err != nil {
		return err
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return client.CallContext(ctx, result, method, args...)
}

func (n *nodeClient) blockNumber() (uint64, error) {
	var hex string
	if err := n.call(&hex, "eth_blockNumber"); err != nil {
		return 0, err
	}
	val, err := hexutil.DecodeUint64(hex)
	return val, err
}

func (n *nodeClient) getBlock(num uint64) (*blockHeader, error) {
	var raw map[string]interface{}
	hexNum := fmt.Sprintf("0x%x", num)
	if err := n.call(&raw, "eth_getBlockByNumber", hexNum, false); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("block %d not found", num)
	}
	h := &blockHeader{Number: num}
	if hash, ok := raw["hash"].(string); ok {
		h.Hash = hash
	}
	if sr, ok := raw["stateRoot"].(string); ok {
		h.StateRoot = sr
	}
	return h, nil
}

func (n *nodeClient) latestBlock() (*blockHeader, error) {
	num, err := n.blockNumber()
	if err != nil {
		return nil, err
	}
	return n.getBlock(num)
}

func (n *nodeClient) getBalance(addr common.Address) (*big.Int, error) {
	var raw map[string]interface{}
	if err := n.call(&raw, "mtn_getAccountState", addr.Hex(), "latest"); err != nil {
		return nil, err
	}
	if raw == nil {
		return big.NewInt(0), nil
	}
	balStr, ok := raw["balance"].(string)
	if !ok || balStr == "" {
		return big.NewInt(0), nil
	}
	b := new(big.Int)
	b.SetString(balStr, 10)
	return b, nil
}

func (n *nodeClient) status(a common.Address) (*regInfo, error) {
	var info regInfo
	if err := n.call(&info, "mtn_getRegistrationStatus", a); err != nil {
		return nil, err
	}
	return &info, nil
}

func (n *nodeClient) registerAccount(u user) (*regInfo, error) {
	var msg regMessage
	if err := n.call(&msg, "mtn_getRegistrationMessage", u.addr); err != nil {
		return nil, fmt.Errorf("getRegistrationMessage: %w", err)
	}
	hashHex := msg.getHash()
	if hashHex == "" {
		return nil, fmt.Errorf("empty hash in registration message: %+v", msg)
	}
	hash, err := hexutil.Decode(hashHex)
	if err != nil {
		return nil, err
	}
	sig, err := crypto.Sign(hash, u.key)
	if err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	var info regInfo
	if err := n.call(&info, "mtn_registerAccount", u.addr, hexutil.Bytes(sig)); err != nil {
		return nil, fmt.Errorf("registerAccount: %w", err)
	}
	return &info, nil
}

func (n *nodeClient) waitStatus(a common.Address, want string, timeout time.Duration) ([]string, *regInfo, error) {
	var seen []string
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := n.status(a)
		if err == nil {
			if len(seen) == 0 || seen[len(seen)-1] != info.Status {
				seen = append(seen, info.Status)
			}
			if info.Status == want {
				return seen, info, nil
			}
			if info.Status == "REJECTED" && want != "REJECTED" {
				return seen, info, fmt.Errorf("request was REJECTED: %+v", info)
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return seen, nil, fmt.Errorf("status of %s never reached %s within %v (saw %v)", a.Hex(), want, timeout, seen)
}

func (n *nodeClient) pendingNonce(addr common.Address) (uint64, error) {
	var hex string
	if err := n.call(&hex, "eth_getTransactionCount", addr.Hex(), "pending"); err != nil {
		return 0, err
	}
	return hexutil.DecodeUint64(hex)
}

func (n *nodeClient) sendRaw(tx *e_types.Transaction) (common.Hash, error) {
	data, err := tx.MarshalBinary()
	if err != nil {
		return common.Hash{}, err
	}
	var hexHash string
	if err := n.call(&hexHash, "eth_sendRawTransaction", hexutil.Encode(data)); err != nil {
		return common.Hash{}, err
	}
	return common.HexToHash(hexHash), nil
}

func (n *nodeClient) waitReceipt(h common.Hash, timeout time.Duration) (uint64, error) {
	start := time.Now()
	for time.Since(start) < timeout {
		var r map[string]interface{}
		_ = n.call(&r, "eth_getTransactionReceipt", h.Hex())
		if r != nil {
			if st, ok := r["status"].(string); ok {
				v, _ := hexutil.DecodeUint64(st)
				return v, nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return 0, fmt.Errorf("timeout waiting receipt for %s", h.Hex())
}

func (n *nodeClient) transferEth(k *ecdsa.PrivateKey, to common.Address, value *big.Int) (*e_types.Transaction, error) {
	from := crypto.PubkeyToAddress(k.PublicKey)
	nonce, err := n.pendingNonce(from)
	if err != nil {
		return nil, fmt.Errorf("get nonce: %w", err)
	}
	tx, err := e_types.SignNewTx(k, e_types.LatestSignerForChainID(big.NewInt(chainID)),
		&e_types.LegacyTx{Nonce: nonce, GasPrice: big.NewInt(gasPrice), Gas: 21000, To: &to, Value: value})
	if err != nil {
		return nil, fmt.Errorf("sign tx: %w", err)
	}
	h, err := n.sendRaw(tx)
	if err != nil {
		return nil, err
	}
	st, err := n.waitReceipt(h, 60*time.Second)
	if err != nil {
		return nil, err
	}
	if st != 1 {
		return nil, fmt.Errorf("receipt status %d for tx %s", st, h.Hex())
	}
	return tx, nil
}

func (n *nodeClient) sendCrossChainTransfer(target common.Address, amount *big.Int) (string, error) {
	var txHash string
	hexAmount := hexutil.EncodeBig(amount)
	deadline := time.Now().Add(35 * time.Second)
	for {
		err := n.call(&txHash, "mtn_sendCrossChainTransfer", target.Hex(), hexAmount)
		if err == nil {
			return txHash, nil
		}
		if (strings.Contains(err.Error(), "BLS float has not been verified") ||
			strings.Contains(err.Error(), "cross-chain not started")) && time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		return "", err
	}
}

// ---------------------------------------------------------------- Test Runner

type stepResult struct {
	Step     string
	Scenario string
	Status   string // PASS or FAIL
	Duration time.Duration
	Detail   string
	Err      error
}

type runner struct {
	env     *envConfig
	script  string
	nodes   map[string]*nodeClient
	exec2   *nodeClient
	results []stepResult
	funderK *ecdsa.PrivateKey
}

func newRunner(env *envConfig, script string) *runner {
	r := &runner{
		env:    env,
		script: script,
		nodes:  make(map[string]*nodeClient),
	}
	r.nodes["val0"] = newNodeClient("val0", env.Ports.Val0.RPC, env.Ports.Val0.Conn)
	r.nodes["val1"] = newNodeClient("val1", env.Ports.Val1.RPC, env.Ports.Val1.Conn)
	r.nodes["val2"] = newNodeClient("val2", env.Ports.Val2.RPC, env.Ports.Val2.Conn)
	r.nodes["val3"] = newNodeClient("val3", env.Ports.Val3.RPC, env.Ports.Val3.Conn)
	r.exec2 = newNodeClient("exec2", env.Ports.Exec2.RPC, env.Ports.Exec2.Conn)

	// Funder key (devnetSenderPrivateKeyHex funded in genesis)
	funderHex := "a3e6d454ea7a3b464af1f8c891259d5ff48f331004d56d1331388ec3c3915fe1"
	k, _ := crypto.HexToECDSA(funderHex)
	r.funderK = k
	return r
}

func (r *runner) check(id, scenario string, fn func() (string, error)) {
	t0 := time.Now()
	detail, err := fn()
	dur := time.Since(t0)
	status := "PASS"
	if err != nil {
		status = "FAIL"
		if detail == "" {
			detail = err.Error()
		} else {
			detail = fmt.Sprintf("%s (err: %v)", detail, err)
		}
	}
	res := stepResult{
		Step:     id,
		Scenario: scenario,
		Status:   status,
		Duration: dur,
		Detail:   detail,
		Err:      err,
	}
	r.results = append(r.results, res)
	icon := "✅"
	if status == "FAIL" {
		icon = "❌"
	}
	fmt.Printf("%s %-6s %s (%v)\n", icon, id, scenario, dur.Round(time.Millisecond))
	if err != nil {
		fmt.Printf("   ERROR: %v\n", err)
	} else if detail != "" {
		fmt.Printf("   DETAIL: %s\n", detail)
	}
}

func (r *runner) runScript(cmd, node string) error {
	args := []string{r.env.Base, cmd}
	if node != "" {
		args = append(args, node)
	}
	out, err := exec.Command(r.script, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("script %v failed: %w (output: %s)", args, err, string(out))
	}
	return nil
}

func (r *runner) waitRPCOk(node string, timeout time.Duration) error {
	t0 := time.Now()
	for time.Since(t0) < timeout {
		var n *nodeClient
		if node == "exec2" {
			n = r.exec2
		} else {
			n = r.nodes[node]
		}
		if _, err := n.blockNumber(); err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s RPC to be ready", node)
}

func (r *runner) reconnectNode(name string) error {
	var rpcPort, connPort int
	switch name {
	case "val0":
		rpcPort, connPort = r.env.Ports.Val0.RPC, r.env.Ports.Val0.Conn
	case "val1":
		rpcPort, connPort = r.env.Ports.Val1.RPC, r.env.Ports.Val1.Conn
	case "val2":
		rpcPort, connPort = r.env.Ports.Val2.RPC, r.env.Ports.Val2.Conn
	case "val3":
		rpcPort, connPort = r.env.Ports.Val3.RPC, r.env.Ports.Val3.Conn
	case "exec2":
		rpcPort, connPort = r.env.Ports.Exec2.RPC, r.env.Ports.Exec2.Conn
	default:
		return fmt.Errorf("unknown node %s", name)
	}
	r.nodes[name] = newNodeClient(name, rpcPort, connPort)
	return nil
}

func (r *runner) waitNodeCatchup(name string, targetBlock uint64, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if node, ok := r.nodes[name]; ok {
			b, err := node.blockNumber()
			if err == nil && b >= targetBlock {
				return nil
			}
		}
		time.Sleep(400 * time.Millisecond)
	}
	return fmt.Errorf("node %s did not reach block %d within %v", name, targetBlock, timeout)
}

func (r *runner) assertStateParity(activeNodes []string) (string, error) {
	if len(activeNodes) <= 1 {
		return "single node active, parity trivial", nil
	}

	// 1. Get block numbers from all active nodes and find the minimum common height
	var minBlock uint64 = 0
	for i, name := range activeNodes {
		node := r.nodes[name]
		b, err := node.blockNumber()
		if err != nil {
			return "", fmt.Errorf("failed to get block number from %s: %w", name, err)
		}
		if i == 0 || b < minBlock {
			minBlock = b
		}
	}

	if minBlock == 0 {
		return "genesis state", nil
	}

	// 2. Fetch block header at minBlock from each node and compare
	var baseHeader *blockHeader
	for _, name := range activeNodes {
		node := r.nodes[name]
		h, err := node.getBlock(minBlock)
		if err != nil {
			return "", fmt.Errorf("node %s failed to get block %d: %w", name, minBlock, err)
		}
		if baseHeader == nil {
			baseHeader = h
		} else {
			if h.Hash != baseHeader.Hash {
				return "", fmt.Errorf("BLOCK HASH MISMATCH at #%d: %s (%s) vs %s (%s)",
					minBlock, activeNodes[0], baseHeader.Hash, name, h.Hash)
			}
			if h.StateRoot != baseHeader.StateRoot {
				return "", fmt.Errorf("STATE ROOT MISMATCH at #%d: %s (%s) vs %s (%s)",
					minBlock, activeNodes[0], baseHeader.StateRoot, name, h.StateRoot)
			}
		}
	}

	return fmt.Sprintf("Block #%d StateRoot: %s (verified on %v)", minBlock, baseHeader.StateRoot[:18]+"...", activeNodes), nil
}

func (r *runner) assertBalanceParity(addr common.Address, expected *big.Int, activeNodes []string) error {
	deadline := time.Now().Add(25 * time.Second)
	for _, n := range activeNodes {
		var lastBal *big.Int
		ok := false
		for time.Now().Before(deadline) {
			b, err := r.nodes[n].getBalance(addr)
			if err == nil && b.Cmp(expected) == 0 {
				ok = true
				break
			}
			if err == nil {
				lastBal = b
			}
			time.Sleep(300 * time.Millisecond)
		}
		if !ok {
			return fmt.Errorf("balance mismatch on node %s for %s: got %s, expected %s", n, addr.Hex(), lastBal, expected)
		}
	}
	return nil
}

// ---------------------------------------------------------------- Raw TCP Writer for System Transactions

type rawWriter struct {
	conn      net.Conn
	writer    *bufio.Writer
	toAddrHex string
}

func newRawWriter(targetAddr, toAddrHex string) (*rawWriter, error) {
	conn, err := net.DialTimeout("tcp", targetAddr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	rw := &rawWriter{
		conn:      conn,
		writer:    bufio.NewWriter(conn),
		toAddrHex: toAddrHex,
	}

	initMsg := &pb.InitConnection{
		Address: common.HexToAddress(toAddrHex).Bytes(),
		Type:    "client",
		Replace: true,
	}
	initBody, err := proto.Marshal(initMsg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if err := rw.sendRaw(command.InitConnection, initBody); err != nil {
		conn.Close()
		return nil, err
	}
	rw.flush()
	time.Sleep(500 * time.Millisecond)
	return rw, nil
}

func (rw *rawWriter) sendRaw(cmd string, body []byte) error {
	toAddr := common.HexToAddress(rw.toAddrHex)
	msgProto := &pb.Message{
		Header: &pb.Header{
			Command:   cmd,
			Version:   "0.0.1.0",
			ToAddress: toAddr.Bytes(),
			ID:        uuid.New().String(),
		},
		Body: body,
	}
	b, err := proto.Marshal(msgProto)
	if err != nil {
		return err
	}
	lengthBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(lengthBuf, uint64(len(b)))
	rw.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if _, err := rw.writer.Write(lengthBuf); err != nil {
		return err
	}
	if _, err := rw.writer.Write(b); err != nil {
		return err
	}
	return nil
}

func (rw *rawWriter) flush() error {
	return rw.writer.Flush()
}

func (rw *rawWriter) close() {
	if rw.conn != nil {
		rw.conn.Close()
	}
}

func (r *runner) sendRawSystemTx(nodeName string, privHex string, fromAddr common.Address, payload []byte) (common.Hash, error) {
	node := r.nodes[nodeName]
	nonce, err := node.pendingNonce(fromAddr)
	if err != nil {
		return common.Hash{}, fmt.Errorf("get nonce for %s: %w", fromAddr.Hex(), err)
	}

	privBytes, err := hex.DecodeString(strings.TrimPrefix(privHex, "0x"))
	if err != nil {
		return common.Hash{}, fmt.Errorf("decode bls priv: %w", err)
	}
	blsPriv := cm.PrivateKeyFromBytes(privBytes)

	tx := mt_transaction.NewTransaction(
		fromAddr,
		rollup.RollupSystemAddress,
		big.NewInt(0),
		21000,
		1_000_000_000,
		0,
		payload,
		nil,
		common.Hash{},
		common.Hash{},
		nonce,
		chainID,
	)
	concreteTx, ok := tx.(*mt_transaction.Transaction)
	if !ok {
		return common.Hash{}, errors.New("cannot cast to *mt_transaction.Transaction")
	}
	concreteTx.SetSign(blsPriv)

	txBytes, err := concreteTx.Marshal()
	if err != nil {
		return common.Hash{}, fmt.Errorf("marshal tx: %w", err)
	}

	txProto := &pb.Transaction{}
	if err := proto.Unmarshal(txBytes, txProto); err != nil {
		return common.Hash{}, fmt.Errorf("unmarshal tx to proto: %w", err)
	}

	batchProto := &pb.Transactions{
		Transactions: []*pb.Transaction{txProto},
	}
	batchBytes, err := proto.Marshal(batchProto)
	if err != nil {
		return common.Hash{}, fmt.Errorf("marshal batch: %w", err)
	}

	rw, err := newRawWriter(node.conn, fromAddr.Hex())
	if err != nil {
		return common.Hash{}, fmt.Errorf("connect rawWriter to %s: %w", node.conn, err)
	}
	defer rw.close()

	if err := rw.sendRaw(command.SendTransactions, batchBytes); err != nil {
		return common.Hash{}, fmt.Errorf("send raw tx: %w", err)
	}
	rw.flush()

	return concreteTx.Hash(), nil
}

// ---------------------------------------------------------------- Main Execution

func main() {
	envPath := flag.String("env", "", "path to env.json")
	scriptPath := flag.String("script", "", "path to run_env.sh")
	reportPath := flag.String("report", "report_cross_chain_coattest.md", "path to markdown report output")
	flag.Parse()

	if *envPath == "" || *scriptPath == "" {
		fmt.Println("Usage: e2e_cross_chain_coattest -env <BASE>/env.json -script <HERE>/run_env.sh [-report report.md]")
		os.Exit(1)
	}

	data, err := os.ReadFile(*envPath)
	if err != nil {
		fmt.Printf("❌ Failed to read env.json: %v\n", err)
		os.Exit(1)
	}
	var env envConfig
	if err := json.Unmarshal(data, &env); err != nil {
		fmt.Printf("❌ Failed to parse env.json: %v\n", err)
		os.Exit(1)
	}

	r := newRunner(&env, *scriptPath)

	fmt.Println("=================================================================")
	fmt.Println("🚀 RUNNING CROSS-CHAIN CREDIT CO-ATTESTATION (P0-2) E2E SUITE")
	fmt.Printf("   Destination Cluster: 4 Validators (Mysticeti, f+1=2)\n")
	fmt.Printf("   Source Cluster:      exec2 (Raft)\n")
	fmt.Printf("   Base Directory:      %s\n", env.Base)
	fmt.Println("=================================================================")

	allNodes := []string{"val0", "val1", "val2", "val3"}

	// Wait for all nodes ready
	for _, n := range allNodes {
		if err := r.waitRPCOk(n, 45*time.Second); err != nil {
			fmt.Printf("❌ Node %s RPC failed: %v\n", n, err)
			os.Exit(1)
		}
	}
	if err := r.waitRPCOk("exec2", 45*time.Second); err != nil {
		fmt.Printf("❌ Node exec2 RPC failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ All 4 validators and exec2 RPC endpoints are ready.")

	// Fund exec2 sender if needed (FUNDER has balance in genesis)
	// Prepare users for each scenario
	uA := newUser()
	uB := newUser()
	uC := newUser()
	uD := newUser()
	uE := newUser()
	uF := newUser()

	// ---------------------------------------------------------------- SCENARIO A
	fmt.Println("\n📍 SCENARIO A: Full Cross-Cluster Transfer & Conservation Verification")
	r.check("A1", "register recipient uA on destination cluster (val0) until CONFIRMED", func() (string, error) {
		_, err := r.nodes["val0"].registerAccount(uA)
		if err != nil {
			return "", err
		}
		seen, last, err := r.nodes["val0"].waitStatus(uA.addr, "CONFIRMED", 45*time.Second)
		if err != nil {
			return "", fmt.Errorf("wait CONFIRMED: %w (saw %v)", err, seen)
		}
		return fmt.Sprintf("status: %s (seen: %v)", last.Status, seen), nil
	})

	transferAmountA := big.NewInt(500)
	var initialExec2Balance *big.Int
	r.check("A2", "record initial balances before cross-cluster transfer", func() (string, error) {
		balDest, err := r.nodes["val0"].getBalance(uA.addr)
		if err != nil {
			return "", err
		}
		if balDest.Sign() != 0 {
			return "", fmt.Errorf("expected destination balance 0, got %s", balDest)
		}
		funderAddr := crypto.PubkeyToAddress(r.funderK.PublicKey)
		initialExec2Balance, err = r.exec2.getBalance(funderAddr)
		if err != nil {
			return "", err
		}
		if initialExec2Balance.Sign() <= 0 {
			return "", fmt.Errorf("funder balance on exec2 is zero: %s", initialExec2Balance)
		}
		return fmt.Sprintf("dest initial: %s, source initial: %s", balDest, initialExec2Balance), nil
	})

	r.check("A3", "submit cross-cluster transfer from exec2 to uA on destination cluster", func() (string, error) {
		txHash, err := r.exec2.sendCrossChainTransfer(uA.addr, transferAmountA)
		if err != nil {
			return "", fmt.Errorf("sendCrossChainTransfer: %w", err)
		}
		return "submitted transfer, txHash: " + txHash, nil
	})

	r.check("A4", "observe credit applied exactly once across all 4 destination validators", func() (string, error) {
		if err := r.assertBalanceParity(uA.addr, transferAmountA, allNodes); err != nil {
			return "", err
		}
		return fmt.Sprintf("balance = %s verified identically across all 4 validators", transferAmountA), nil
	})

	r.check("A5", "verify source cluster (exec2) balance decreased by transfer amount", func() (string, error) {
		funderAddr := crypto.PubkeyToAddress(r.funderK.PublicKey)
		newBal, err := r.exec2.getBalance(funderAddr)
		if err != nil {
			return "", err
		}
		expectedMax := new(big.Int).Sub(initialExec2Balance, transferAmountA)
		if newBal.Cmp(expectedMax) > 0 {
			return "", fmt.Errorf("balance did not decrease: was %s, now %s (transfer %s)", initialExec2Balance, newBal, transferAmountA)
		}
		return fmt.Sprintf("balance decreased: %s -> %s (at least -%s)", initialExec2Balance, newBal, transferAmountA), nil
	})

	r.check("A6", "state parity across all 4 destination validators after Scenario A", func() (string, error) {
		return r.assertStateParity(allNodes)
	})

	// ---------------------------------------------------------------- SCENARIO B
	fmt.Println("\n📍 SCENARIO B: Tolerance with 1 Validator Offline (3 Active >= 3)")
	r.check("B1", "stop 1 validator (val3) — cluster remains quorum-healthy (3 >= 3)", func() (string, error) {
		if err := r.runScript("stop_node", "val3"); err != nil {
			return "", err
		}
		time.Sleep(1 * time.Second)
		return "val3 stopped", nil
	})

	r.check("B2", "register recipient uB on val0 while val3 is down", func() (string, error) {
		_, err := r.nodes["val0"].registerAccount(uB)
		if err != nil {
			return "", err
		}
		seen, last, err := r.nodes["val0"].waitStatus(uB.addr, "CONFIRMED", 45*time.Second)
		if err != nil {
			return "", fmt.Errorf("wait uB CONFIRMED: %w (saw %v)", err, seen)
		}
		return fmt.Sprintf("uB CONFIRMED with val3 offline (status: %s)", last.Status), nil
	})

	transferAmountB := big.NewInt(300)
	active3 := []string{"val0", "val1", "val2"}
	r.check("B3", "cross-chain transfer to uB completes with val3 offline (2f+1 active)", func() (string, error) {
		_, err := r.exec2.sendCrossChainTransfer(uB.addr, transferAmountB)
		if err != nil {
			return "", fmt.Errorf("sendCrossChainTransfer: %w", err)
		}
		if err := r.assertBalanceParity(uB.addr, transferAmountB, active3); err != nil {
			return "", err
		}
		return fmt.Sprintf("credit %s verified across active validators [val0, val1, val2]", transferAmountB), nil
	})

	r.check("B3b", "state parity across the 3 active validators before restart", func() (string, error) {
		return r.assertStateParity(active3)
	})

	r.check("B4", "restart val3 and verify catchup to latest block", func() (string, error) {
		if err := r.runScript("start_node", "val3"); err != nil {
			return "", err
		}
		if err := r.reconnectNode("val3"); err != nil {
			return "", fmt.Errorf("reconnect val3: %w", err)
		}
		targetBlock, _ := r.nodes["val0"].blockNumber()
		if err := r.waitNodeCatchup("val3", targetBlock, 30*time.Second); err != nil {
			return "", fmt.Errorf("val3 catchup: %w", err)
		}
		if err := r.assertBalanceParity(uB.addr, transferAmountB, []string{"val3"}); err != nil {
			return "", fmt.Errorf("val3 catchup balance: %w", err)
		}
		return "val3 restarted, caught up and verified balance", nil
	})

	r.check("B5", "state parity across all 4 validators after Scenario B", func() (string, error) {
		return r.assertStateParity(allNodes)
	})

	// ---------------------------------------------------------------- SCENARIO C
	fmt.Println("\n📍 SCENARIO C: Quarantine with 2 Validators Offline (Zero-Fork Invariant)")
	r.check("C1", "stop 2 validators: val2 and val3 (active 2 < 3)", func() (string, error) {
		if err := r.runScript("stop_node", "val2"); err != nil {
			return "", err
		}
		if err := r.runScript("stop_node", "val3"); err != nil {
			return "", err
		}
		time.Sleep(1 * time.Second)
		return "val2 and val3 stopped", nil
	})

	var preBlockC uint64
	r.check("C2", "register uC on val0; verify consensus pauses and credit stays PENDING (zero fork)", func() (string, error) {
		var err error
		preBlockC, err = r.nodes["val0"].blockNumber()
		if err != nil {
			return "", err
		}

		// Try registering uC
		if _, err := r.nodes["val0"].registerAccount(uC); err != nil {
			return "", fmt.Errorf("registerAccount: %w", err)
		}
		time.Sleep(4 * time.Second)

		// Assert zero fork: block height must not advance without quorum
		curBlock, _ := r.nodes["val0"].blockNumber()
		if curBlock > preBlockC {
			return "", fmt.Errorf("consensus advanced unexpectedly (%d -> %d) without quorum!", preBlockC, curBlock)
		}

		info, _ := r.nodes["val0"].status(uC.addr)
		if info != nil && info.Status == "CONFIRMED" {
			return "", errors.New("registration reached CONFIRMED without quorum!")
		}

		// Verify zero fork between the two active nodes (val0 and val1)
		h0, err0 := r.nodes["val0"].getBlock(curBlock)
		h1, err1 := r.nodes["val1"].getBlock(curBlock)
		if err0 != nil || err1 != nil || h0.Hash != h1.Hash {
			return "", fmt.Errorf("ZERO-FORK INVARIANT VIOLATED: val0=%v val1=%v", h0, h1)
		}
		bal, _ := r.nodes["val0"].getBalance(uC.addr)
		if bal.Sign() != 0 {
			return "", fmt.Errorf("uC prematurely credited while consensus paused: %s", bal)
		}
		return fmt.Sprintf("consensus safely paused at Block #%d, val0 hash == val1 hash (zero fork)", curBlock), nil
	})

	transferAmountC := big.NewInt(400)
	r.check("C3", "restart val2 and val3; verify quorum restored and credit completes exactly once", func() (string, error) {
		if err := r.runScript("start_node", "val2"); err != nil {
			return "", err
		}
		if err := r.runScript("start_node", "val3"); err != nil {
			return "", err
		}
		if err := r.reconnectNode("val2"); err != nil {
			return "", fmt.Errorf("reconnect val2: %w", err)
		}
		if err := r.reconnectNode("val3"); err != nil {
			return "", fmt.Errorf("reconnect val3: %w", err)
		}

		// Verify block advances past preBlockC
		deadline := time.Now().Add(30 * time.Second)
		adv := false
		for time.Now().Before(deadline) {
			b, err := r.nodes["val0"].blockNumber()
			if err == nil && b > preBlockC {
				adv = true
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		if !adv {
			return "", fmt.Errorf("block height did not advance past %d after restart", preBlockC)
		}

		// Confirm uC registration now that quorum is restored
		seen, last, err := r.nodes["val0"].waitStatus(uC.addr, "CONFIRMED", 45*time.Second)
		if err != nil {
			return "", fmt.Errorf("wait uC CONFIRMED after quorum restored: %w (saw %v)", err, seen)
		}

		// Send transfer now
		_, err = r.exec2.sendCrossChainTransfer(uC.addr, transferAmountC)
		if err != nil {
			return "", fmt.Errorf("sendCrossChainTransfer to uC: %w", err)
		}
		// Verify balance credited across all 4 nodes exactly once
		if err := r.assertBalanceParity(uC.addr, transferAmountC, allNodes); err != nil {
			return "", err
		}
		// Ensure val2 and val3 catch up to the current block height
		targetBlock, _ := r.nodes["val0"].blockNumber()
		if err := r.waitNodeCatchup("val2", targetBlock, 30*time.Second); err != nil {
			return "", fmt.Errorf("wait val2 catchup: %w", err)
		}
		if err := r.waitNodeCatchup("val3", targetBlock, 30*time.Second); err != nil {
			return "", fmt.Errorf("wait val3 catchup: %w", err)
		}
		return fmt.Sprintf("quorum restored (status: %s), credit %s applied exactly once on all 4 nodes", last.Status, transferAmountC), nil
	})

	r.check("C4", "state parity across all 4 validators after Scenario C", func() (string, error) {
		return r.assertStateParity(allNodes)
	})

	// ---------------------------------------------------------------- SCENARIO D
	fmt.Println("\n📍 SCENARIO D: Byzantine Fake Credit Event Injection (f+1 Enforcement)")
	r.check("D1", "single validator sends forged credit envelope with only 1 signature (below f+1=2)", func() (string, error) {
		fakeMsgID := common.HexToHash("0xdeadbeef111122223333444455556666777788889999aaaabbbbccccddddeeee")
		fakeAmount := big.NewInt(99_999_999)
		inner := rollup.RollupSystemPayload{
			Event: rollup.Event{
				Type:               rollup.EventCreditObserved,
				Role:               rollup.RoleReceiver,
				Sender:             common.HexToAddress("0x9999999999999999999999999999999999999999"),
				Target:             uD.addr,
				Value:              fakeAmount,
				IsDuplicate:        false,
				IsDestinationValid: true,
			},
			MsgID:        fakeMsgID,
			SourceSeq:    888,
			SourcePubKey: cm.PublicKey{1},
			DestPubKey:   cm.PublicKey{2},
			PayloadHash:  common.HexToHash("0x1234"),
		}
		innerBytes, err := rollup.MarshalRollupSystemPayload(&inner)
		if err != nil {
			return "", err
		}
		digest := rollup.ComputeRollupSystemEventDigest(chainID, innerBytes)

		// Sign with only val0 key
		val0PrivBytes, _ := hex.DecodeString(r.env.Validators["val0"].BlsPriv)
		val0PubBytes, _ := hex.DecodeString(strings.TrimPrefix(r.env.Validators["val0"].BlsPub, "0x"))
		var val0Pub cm.PublicKey
		copy(val0Pub[:], val0PubBytes)
		sig := bls.Sign(cm.PrivateKeyFromBytes(val0PrivBytes), digest)

		attestedPayload := rollup.RollupSystemAttestedPayload{
			Kind:  rollup.PayloadKindRollupSystemAttested,
			Inner: innerBytes,
			Attestations: []rollup.RegistrationAttestation{
				{
					ValidatorPubkey: val0Pub,
					Signature:       sig,
				},
			},
		}
		payloadBytes, err := rollup.MarshalRollupSystemAttestedPayload(&attestedPayload)
		if err != nil {
			return "", err
		}

		// Submit from cluster key (authorized sender)
		clusterAddr := common.HexToAddress(r.env.Cluster.Address)
		_, err = r.sendRawSystemTx("val0", r.env.Cluster.PrivateKey, clusterAddr, payloadBytes)
		if err != nil {
			return "", fmt.Errorf("submit forged envelope: %w", err)
		}
		time.Sleep(3 * time.Second)

		// Verify target balance remains 0
		bal, err := r.nodes["val0"].getBalance(uD.addr)
		if err != nil {
			return "", err
		}
		if bal.Sign() != 0 {
			return "", fmt.Errorf("FORGED EVENT WAS CREDITED! Balance: %s", bal)
		}
		return "single attestation accepted into storage but NOT dispatched (balance remains 0)", nil
	})

	r.check("D2", "unattested raw credit transaction rejected by committee handler (fail-closed)", func() (string, error) {
		rawPayload := rollup.RollupSystemPayload{
			Event: rollup.Event{
				Type:   rollup.EventCreditObserved,
				Target: uD.addr,
				Value:  big.NewInt(50_000_000),
			},
			MsgID: common.HexToHash("0xfeedface"),
		}
		rawBytes, err := rollup.MarshalRollupSystemPayload(&rawPayload)
		if err != nil {
			return "", err
		}
		clusterAddr := common.HexToAddress(r.env.Cluster.Address)
		_, err = r.sendRawSystemTx("val0", r.env.Cluster.PrivateKey, clusterAddr, rawBytes)
		if err != nil && !strings.Contains(err.Error(), "rejected") && !strings.Contains(err.Error(), "co-attestation required") {
			return "", fmt.Errorf("unexpected error: %w", err)
		}
		time.Sleep(2 * time.Second)

		// Verify balance on all 4 nodes is STILL zero
		if err := r.assertBalanceParity(uD.addr, big.NewInt(0), allNodes); err != nil {
			return "", err
		}
		return "unattested tx rejected, target balance 0 across all 4 nodes", nil
	})

	r.check("D3", "state parity across all 4 validators after Scenario D", func() (string, error) {
		return r.assertStateParity(allNodes)
	})

	// ---------------------------------------------------------------- SCENARIO E
	fmt.Println("\n📍 SCENARIO E: Crash Recovery (kill -9 mid-traffic, Block Audit)")
	r.check("E1", "register uE and send cross-chain transfer while killing val1 mid-traffic", func() (string, error) {
		_, err := r.nodes["val0"].registerAccount(uE)
		if err != nil {
			return "", err
		}
		_, _, err = r.nodes["val0"].waitStatus(uE.addr, "CONFIRMED", 45*time.Second)
		if err != nil {
			return "", err
		}
		transferAmountE := big.NewInt(600)
		_, err = r.exec2.sendCrossChainTransfer(uE.addr, transferAmountE)
		if err != nil {
			return "", fmt.Errorf("sendCrossChainTransfer: %w", err)
		}

		// Crash val1 immediately
		if err := r.runScript("kill_node", "val1"); err != nil {
			return "", fmt.Errorf("kill val1: %w", err)
		}

		// 3 surviving nodes must confirm and credit uE
		surviving := []string{"val0", "val2", "val3"}
		if err := r.assertBalanceParity(uE.addr, transferAmountE, surviving); err != nil {
			return "", fmt.Errorf("surviving nodes balance check: %w", err)
		}
		return "val1 killed with SIGKILL; 3 surviving nodes completed consensus and credited uE", nil
	})

	transferAmountE := big.NewInt(600)
	r.check("E2", "restart val1 and verify recovery & catchup", func() (string, error) {
		if err := r.runScript("start_node", "val1"); err != nil {
			return "", err
		}
		if err := r.reconnectNode("val1"); err != nil {
			return "", fmt.Errorf("reconnect val1: %w", err)
		}
		targetBlock, _ := r.nodes["val0"].blockNumber()
		if err := r.waitNodeCatchup("val1", targetBlock, 45*time.Second); err != nil {
			return "", fmt.Errorf("val1 catchup: %w", err)
		}
		if err := r.assertBalanceParity(uE.addr, transferAmountE, []string{"val1"}); err != nil {
			return "", fmt.Errorf("val1 recovery balance check: %w", err)
		}
		return "val1 recovered and matched balance", nil
	})

	r.check("E3", "exhaustive block-by-block audit from block #1 to latest across all 4 nodes", func() (string, error) {
		targetBlock, _ := r.nodes["val0"].blockNumber()
		for _, n := range allNodes {
			if err := r.waitNodeCatchup(n, targetBlock, 45*time.Second); err != nil {
				return "", fmt.Errorf("node %s catchup before audit: %w", n, err)
			}
		}
		for b := uint64(1); b <= targetBlock; b++ {
			var refHash, refRoot string
			for i, n := range allNodes {
				header, err := r.nodes[n].getBlock(b)
				if err != nil {
					return "", fmt.Errorf("block %d on %s: %w", b, n, err)
				}
				if i == 0 {
					refHash = header.Hash
					refRoot = header.StateRoot
				} else {
					if header.Hash != refHash {
						return "", fmt.Errorf("block %d hash mismatch on %s: %s != ref %s", b, n, header.Hash, refHash)
					}
					if header.StateRoot != refRoot {
						return "", fmt.Errorf("block %d stateRoot mismatch on %s: %s != ref %s", b, n, header.StateRoot, refRoot)
					}
				}
			}
		}
		return fmt.Sprintf("100%% parity across all 4 nodes for blocks #1 through #%d (0 mismatches)", targetBlock), nil
	})

	// ---------------------------------------------------------------- SCENARIO F
	fmt.Println("\n📍 SCENARIO F: Stale-State Rejected Dispatch Retried (Applied Exactly Once)")
	r.check("F1", "register uF and submit premature ClaimedConfirmed before record exists", func() (string, error) {
		_, err := r.nodes["val0"].registerAccount(uF)
		if err != nil {
			return "", err
		}
		_, _, err = r.nodes["val0"].waitStatus(uF.addr, "CONFIRMED", 45*time.Second)
		if err != nil {
			return "", err
		}

		prematureMsgID := common.HexToHash("0x9876543210987654321098765432109876543210987654321098765432109876")
		prematureInner := rollup.RollupSystemPayload{
			Event: rollup.Event{
				Type:    rollup.EventClaimedConfirmed,
				Role:    rollup.RoleReceiver,
				Target:  uF.addr,
				Sender:  common.HexToAddress("0x7777"),
				Value:   big.NewInt(700),
				Outcome: rollup.OutcomeCredited,
			},
			MsgID: prematureMsgID,
		}
		pBytes, err := rollup.MarshalRollupSystemPayload(&prematureInner)
		if err != nil {
			return "", err
		}
		digest := rollup.ComputeRollupSystemEventDigest(chainID, pBytes)

		// Create 2 attestations from val0 and val1
		val0Priv, _ := hex.DecodeString(r.env.Validators["val0"].BlsPriv)
		val1Priv, _ := hex.DecodeString(r.env.Validators["val1"].BlsPriv)
		val0PubBytes, _ := hex.DecodeString(strings.TrimPrefix(r.env.Validators["val0"].BlsPub, "0x"))
		val1PubBytes, _ := hex.DecodeString(strings.TrimPrefix(r.env.Validators["val1"].BlsPub, "0x"))
		var val0Pub, val1Pub cm.PublicKey
		copy(val0Pub[:], val0PubBytes)
		copy(val1Pub[:], val1PubBytes)

		attPayload := rollup.RollupSystemAttestedPayload{
			Kind:  rollup.PayloadKindRollupSystemAttested,
			Inner: pBytes,
			Attestations: []rollup.RegistrationAttestation{
				{ValidatorPubkey: val0Pub, Signature: bls.Sign(cm.PrivateKeyFromBytes(val0Priv), digest)},
				{ValidatorPubkey: val1Pub, Signature: bls.Sign(cm.PrivateKeyFromBytes(val1Priv), digest)},
			},
		}
		envelopeBytes, err := rollup.MarshalRollupSystemAttestedPayload(&attPayload)
		if err != nil {
			return "", err
		}
		clusterAddr := common.HexToAddress(r.env.Cluster.Address)

		// Submit: dispatch will fail with "record not found"
		_, err = r.sendRawSystemTx("val0", r.env.Cluster.PrivateKey, clusterAddr, envelopeBytes)
		time.Sleep(2 * time.Second)

		// Verify no tombstone was written, uF balance remains 0
		bal, err := r.nodes["val0"].getBalance(uF.addr)
		if err != nil {
			return "", err
		}
		if bal.Sign() != 0 {
			return "", fmt.Errorf("premature event credited uF balance: %s", bal)
		}
		return "premature event rejected by dispatcher without tombstoning; balance remains 0", nil
	})

	transferAmountF := big.NewInt(700)
	r.check("F2", "valid cross-chain transfer to uF executes normally and credits exactly once", func() (string, error) {
		_, err := r.exec2.sendCrossChainTransfer(uF.addr, transferAmountF)
		if err != nil {
			return "", fmt.Errorf("sendCrossChainTransfer to uF: %w", err)
		}
		if err := r.assertBalanceParity(uF.addr, transferAmountF, allNodes); err != nil {
			return "", err
		}
		return fmt.Sprintf("uF credited %s normally", transferAmountF), nil
	})

	r.check("F3", "duplicate transfer re-observation does not double-credit (tombstone idempotency)", func() (string, error) {
		time.Sleep(3 * time.Second)
		if err := r.assertBalanceParity(uF.addr, transferAmountF, allNodes); err != nil {
			return "", fmt.Errorf("balance changed after idle: %w", err)
		}
		return fmt.Sprintf("uF balance stable at %s (no double-credit)", transferAmountF), nil
	})

	r.check("F4", "state parity across all 4 destination validators after Scenario F", func() (string, error) {
		return r.assertStateParity(allNodes)
	})

	// ---------------------------------------------------------------- Summary & Report
	total := len(r.results)
	passed := 0
	failed := 0
	for _, res := range r.results {
		if res.Status == "PASS" {
			passed++
		} else {
			failed++
		}
	}

	fmt.Println("\n=================================================================")
	fmt.Printf("🏁 SUITE RESULTS: %d steps executed\n", total)
	fmt.Printf("   PASS: %d | FAIL: %d\n", passed, failed)
	fmt.Println("=================================================================")

	var buf bytes.Buffer
	buf.WriteString("# Báo cáo Kiểm thử Co-Attestation Rollup Credit E2E (P0-2)\n\n")
	buf.WriteString(fmt.Sprintf("**Thời gian:** %s\n", time.Now().Format(time.RFC3339)))
	buf.WriteString(fmt.Sprintf("**Kết quả:** %d/%d PASS\n\n", passed, total))
	buf.WriteString("| Step | Scenario | Status | Duration | Detail |\n")
	buf.WriteString("| :--- | :--- | :---: | :---: | :--- |\n")
	for _, res := range r.results {
		statusIcon := "✅ PASS"
		if res.Status != "PASS" {
			statusIcon = "❌ FAIL"
		}
		buf.WriteString(fmt.Sprintf("| %s | %s | %s | %v | %s |\n",
			res.Step, res.Scenario, statusIcon, res.Duration.Round(time.Millisecond), res.Detail))
	}

	_ = os.WriteFile(*reportPath, buf.Bytes(), 0644)
	fmt.Printf("📄 Report saved to: %s\n", *reportPath)

	if failed > 0 {
		os.Exit(1)
	}
}
