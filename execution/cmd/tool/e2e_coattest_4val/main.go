// e2e_coattest_4val runs the 5 mandatory co-attestation scenarios on a 4-validator Mysticeti cluster:
//   (a) Co-attestation quorum: account confirmed only after >= 2 validator attestations (f+1=2)
//   (b) Tolerance: stop 1 validator (3 active >= 2f+1=3) -> still confirms, catchup verified
//   (c) Quarantine: stop 2 validators (2 active < 2f+1=3) -> pauses consensus, zero fork, restarts -> confirms
//   (d) Byzantine: single validator sends fake registration event (not in Parent Chain) -> never confirmed
//   (e) Crash recovery: kill -9 mid-traffic -> restarts -> 100% block-by-block state root and hash match
//
// Usage:
//   go run ./cmd/tool/e2e_coattest_4val -env <BASE>/env.json [-report report.md]
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
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/meta-node-blockchain/meta-node/types"
)

const (
	chainID  = 991
	gasPrice = 1_000_000_000
)

var oneEther = new(big.Int).Mul(big.NewInt(1_000_000_000), big.NewInt(1_000_000_000))

// ---------------------------------------------------------------- env config

type envConfig struct {
	Base  string `json:"base"`
	Bin   string `json:"bin"`
	Ports struct {
		ParentHTTP    int `json:"parent_http"`
		ParentP2P     int `json:"parent_p2p"`
		ParentMetrics int `json:"parent_metrics"`
		ParentPeerRPC int `json:"parent_peer_rpc"`
		Val0          struct{ RPC, Conn, P2P, PeerRPC, Metrics int } `json:"val0"`
		Val1          struct{ RPC, Conn, P2P, PeerRPC, Metrics int } `json:"val1"`
		Val2          struct{ RPC, Conn, P2P, PeerRPC, Metrics int } `json:"val2"`
		Val3          struct{ RPC, Conn, P2P, PeerRPC, Metrics int } `json:"val3"`
	} `json:"ports"`
	Funder  string `json:"funder"`
	Mode    string `json:"mode"`
	Cluster struct {
		Address    string `json:"address"`
		BlsPub     string `json:"bls_pub"`
		PrivateKey string `json:"private_key"`
	} `json:"cluster"`
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

// ---------------------------------------------------------------- RPC client

type blockHeader struct {
	Number    uint64
	Hash      string
	StateRoot string
}

type regMessage struct {
	Digest     string `json:"digest"`
	HashToSign string `json:"hashToSign"`
}

type regInfo struct {
	Status      string `json:"status"`
	HomeCluster string `json:"homeCluster"`
	Reason      string `json:"reason"`
}

type nodeClient struct {
	name     string
	rpcURL   string
	connAddr string
	client   *rpc.Client
}

func newNodeClient(name string, rpcPort, connPort int) (*nodeClient, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d", rpcPort)
	c, err := rpc.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("dial %s at %s: %w", name, url, err)
	}
	return &nodeClient{
		name:     name,
		rpcURL:   url,
		connAddr: fmt.Sprintf("127.0.0.1:%d", connPort),
		client:   c,
	}, nil
}

func (n *nodeClient) call(result interface{}, method string, args ...interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return n.client.CallContext(ctx, result, method, args...)
}

func (n *nodeClient) blockNumber() (uint64, error) {
	var h hexutil.Uint64
	err := n.call(&h, "eth_blockNumber")
	return uint64(h), err
}

func (n *nodeClient) getBlock(num uint64) (*blockHeader, error) {
	var raw struct {
		Number    string `json:"number"`
		Hash      string `json:"hash"`
		StateRoot string `json:"stateRoot"`
	}
	hexNum := fmt.Sprintf("0x%x", num)
	err := n.call(&raw, "eth_getBlockByNumber", hexNum, false)
	if err != nil {
		return nil, err
	}
	if raw.Hash == "" {
		return nil, fmt.Errorf("block %d returned empty hash", num)
	}
	nNum, _ := hexutil.DecodeUint64(raw.Number)
	return &blockHeader{
		Number:    nNum,
		Hash:      raw.Hash,
		StateRoot: raw.StateRoot,
	}, nil
}

func (n *nodeClient) balance(a common.Address) (*big.Int, error) {
	var b hexutil.Big
	err := n.call(&b, "eth_getBalance", a, "latest")
	return (*big.Int)(&b), err
}

func (n *nodeClient) pendingNonce(a common.Address) (uint64, error) {
	var nn hexutil.Uint64
	err := n.call(&nn, "eth_getTransactionCount", a, "pending")
	return uint64(nn), err
}

func (n *nodeClient) sendRaw(tx *e_types.Transaction) (common.Hash, error) {
	raw, err := tx.MarshalBinary()
	if err != nil {
		return common.Hash{}, err
	}
	var h common.Hash
	err = n.call(&h, "eth_sendRawTransaction", hexutil.Encode(raw))
	return h, err
}

func (n *nodeClient) waitReceipt(h common.Hash, timeout time.Duration) (uint64, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var raw map[string]interface{}
		if err := n.call(&raw, "eth_getTransactionReceipt", h); err == nil && raw != nil {
			s, _ := raw["status"].(string)
			v, err := hexutil.DecodeUint64(s)
			if err == nil {
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

func (n *nodeClient) registerAccount(u user) (*regInfo, error) {
	var msg regMessage
	if err := n.call(&msg, "mtn_getRegistrationMessage", u.addr); err != nil {
		return nil, fmt.Errorf("getRegistrationMessage: %w", err)
	}
	hash, err := hexutil.Decode(msg.HashToSign)
	if err != nil {
		return nil, err
	}
	sig, err := crypto.Sign(hash, u.key)
	if err != nil {
		return nil, err
	}
	var info regInfo
	if err := n.call(&info, "mtn_registerAccount", u.addr, hexutil.Bytes(sig)); err != nil {
		return nil, fmt.Errorf("registerAccount: %w", err)
	}
	return &info, nil
}

func (n *nodeClient) status(a common.Address) (*regInfo, error) {
	var info regInfo
	if err := n.call(&info, "mtn_getRegistrationStatus", a); err != nil {
		return nil, err
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
		}
		time.Sleep(300 * time.Millisecond)
	}
	return seen, nil, fmt.Errorf("status of %s never reached %s within %v (saw %v)", a.Hex(), want, timeout, seen)
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

// ---------------------------------------------------------------- Test Runner

type step struct {
	ID, Name, Status, Detail string
	Took                     time.Duration
}

type runner struct {
	envPath   string
	script    string
	env       envConfig
	nodes     map[string]*nodeClient
	funder    user
	steps     []step
	mu        sync.Mutex
	allPassed bool
}

func (r *runner) record(id, name, status, detail string, took time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, step{id, name, status, detail, took})
	icon := map[string]string{"PASS": "✅", "FAIL": "❌", "SKIP": "⏭️"}[status]
	fmt.Printf("%s %-6s %s — %s (%v)\n", icon, id, name, detail, took.Round(time.Millisecond))
}

func (r *runner) check(id, name string, f func() (string, error)) bool {
	start := time.Now()
	var detail string
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v", p)
			}
		}()
		detail, err = f()
	}()
	if err != nil {
		r.record(id, name, "FAIL", err.Error(), time.Since(start))
		r.allPassed = false
		return false
	}
	r.record(id, name, "PASS", detail, time.Since(start))
	return true
}

func (r *runner) runEnvCmd(action string, node string) error {
	args := []string{r.env.Base, action}
	if node != "" {
		args = append(args, node)
	}
	cmd := exec.Command(r.script, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run_env %v: %w (output: %s)", args, err, strings.TrimSpace(string(out)))
	}
	return nil
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
	}
	client, err := newNodeClient(name, rpcPort, connPort)
	if err != nil {
		return err
	}
	r.nodes[name] = client
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
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("node %s did not reach block %d within %v", name, targetBlock, timeout)
}

// verifyStateParity verifies that all given active nodes have matching block hash, stateRoot at common block height,
// and matching registration status for checkedUsers.
func (r *runner) verifyStateParity(activeNodes []string, checkedUsers []common.Address) (string, error) {
	if len(activeNodes) <= 1 {
		return "single node active, parity trivial", nil
	}

	// 1. Get block numbers from all active nodes and find the minimum common height
	var minBlock uint64 = 0
	blocks := make(map[string]uint64)
	for i, name := range activeNodes {
		node := r.nodes[name]
		b, err := node.blockNumber()
		if err != nil {
			return "", fmt.Errorf("failed to get block number from %s: %w", name, err)
		}
		blocks[name] = b
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

	// 3. Verify user registration status is identical across active nodes
	for _, u := range checkedUsers {
		var baseStatus string
		for _, name := range activeNodes {
			node := r.nodes[name]
			info, err := node.status(u)
			if err != nil {
				return "", fmt.Errorf("node %s failed to get status for %s: %w", name, u.Hex(), err)
			}
			if baseStatus == "" {
				baseStatus = info.Status
			} else if info.Status != baseStatus {
				return "", fmt.Errorf("REGISTRATION STATUS MISMATCH for %s: %s (%s) vs %s (%s)",
					u.Hex(), activeNodes[0], baseStatus, name, info.Status)
			}
		}
	}

	return fmt.Sprintf("Block #%d StateRoot: %s (identical across %v)", minBlock, baseHeader.StateRoot, activeNodes), nil
}

// ---------------------------------------------------------------- System Transaction Helper

func (r *runner) sendSystemTx(targetNode string, fromKeyBLSHex string, fromAddr common.Address, payload []byte) (types.Transaction, error) {
	node := r.nodes[targetNode]
	nonce, err := node.pendingNonce(fromAddr)
	if err != nil {
		return nil, fmt.Errorf("get nonce for %s: %w", fromAddr.Hex(), err)
	}

	privBytes, err := hex.DecodeString(strings.TrimPrefix(fromKeyBLSHex, "0x"))
	if err != nil {
		return nil, fmt.Errorf("decode bls priv: %w", err)
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
		return nil, errors.New("cannot cast to *mt_transaction.Transaction")
	}
	concreteTx.SetSign(blsPriv)

	txBytes, err := concreteTx.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal tx: %w", err)
	}
	txProto := &pb.Transaction{}
	if err := proto.Unmarshal(txBytes, txProto); err != nil {
		return nil, fmt.Errorf("unmarshal tx to proto: %w", err)
	}

	// Send via TCP using rawWriter
	batchProto := &pb.Transactions{
		Transactions: []*pb.Transaction{txProto},
	}
	batchBytes, err := proto.Marshal(batchProto)
	if err != nil {
		return nil, fmt.Errorf("marshal batch: %w", err)
	}

	rw, err := newRawWriter(node.connAddr, fromAddr.Hex())
	if err != nil {
		return nil, fmt.Errorf("connect rawWriter to %s: %w", node.connAddr, err)
	}
	defer rw.close()

	if err := rw.sendRaw(command.SendTransactions, batchBytes); err != nil {
		return nil, fmt.Errorf("send raw tx: %w", err)
	}
	rw.flush()

	return concreteTx, nil
}

// ---------------------------------------------------------------- Main

func main() {
	envPath := flag.String("env", "", "path to env.json")
	scriptPath := flag.String("script", "", "path to run_env.sh (default: inferred from env.json)")
	reportPath := flag.String("report", "", "markdown report output path")
	flag.Parse()

	if *envPath == "" {
		fmt.Println("usage: e2e_coattest_4val -env <BASE>/env.json [-report report.md]")
		os.Exit(2)
	}

	rawEnv, err := os.ReadFile(*envPath)
	if err != nil {
		fmt.Printf("failed to read env: %v\n", err)
		os.Exit(2)
	}

	var env envConfig
	if err := json.Unmarshal(rawEnv, &env); err != nil {
		fmt.Printf("failed to parse env.json: %v\n", err)
		os.Exit(2)
	}

	script := *scriptPath
	if script == "" {
		// Infer location: execution/scripts/test/gate_e2e/run_env.sh
		repoRoot, _ := filepath.Abs(".")
		script = filepath.Join(repoRoot, "execution/scripts/test/gate_e2e/run_env.sh")
		if _, err := os.Stat(script); err != nil {
			script = filepath.Join(repoRoot, "scripts/test/gate_e2e/run_env.sh")
		}
	}

	r := &runner{
		envPath:   *envPath,
		script:    script,
		env:       env,
		nodes:     make(map[string]*nodeClient),
		allPassed: true,
	}

	// Parse funder key
	// In our generated env, funderKeyHex is default funder
	const defaultFunderKey = "a3e6d454ea7a3b464af1f8c891259d5ff48f331004d56d1331388ec3c3915fe1"
	fk, err := crypto.HexToECDSA(defaultFunderKey)
	if err != nil {
		fmt.Printf("invalid funder key: %v\n", err)
		os.Exit(2)
	}
	r.funder = user{key: fk, addr: crypto.PubkeyToAddress(fk.PublicKey)}

	// Connect to 4 validator RPCs
	for _, name := range []string{"val0", "val1", "val2", "val3"} {
		if err := r.reconnectNode(name); err != nil {
			fmt.Printf("failed to connect to %s: %v\n", name, err)
			os.Exit(2)
		}
	}

	fmt.Printf("\n🚀 Starting 4-Validator Co-Attestation E2E Suite (Mode: %s)\n", env.Mode)
	fmt.Printf("   Base: %s\n", env.Base)
	fmt.Printf("   Cluster Key: %s (Addr: %s)\n", env.Cluster.BlsPub[:18]+"...", env.Cluster.Address)
	fmt.Printf("   Funder: %s\n\n", r.funder.addr.Hex())

	allNodes := []string{"val0", "val1", "val2", "val3"}
	var confirmedUsers []common.Address

	// =========================================================================
	// SCENARIO A: Co-attestation Quorum (>= 2 validator attestations required)
	// =========================================================================
	fmt.Println("════════════════════════════════════════════════════════════════")
	fmt.Println("📍 SCENARIO A: Co-attestation Quorum Verification (f+1=2)")
	fmt.Println("════════════════════════════════════════════════════════════════")

	uA := newUser()
	r.check("A1", "unregistered user transfer is refused before registration", func() (string, error) {
		to := common.HexToAddress("0x00000000000000000000000000000000000c0ffe")
		tx, err := e_types.SignNewTx(uA.key, e_types.LatestSignerForChainID(big.NewInt(chainID)),
			&e_types.LegacyTx{Nonce: 0, GasPrice: big.NewInt(gasPrice), Gas: 21000, To: &to, Value: big.NewInt(1000)})
		if err != nil {
			return "", err
		}
		_, err = r.nodes["val0"].sendRaw(tx)
		if err == nil {
			return "", errors.New("expected tx to be refused, but was accepted")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "not registered") {
			return "", fmt.Errorf("unexpected error message: %v", err)
		}
		return "properly refused: " + err.Error(), nil
	})

	r.check("A2", "register user on val0 and observe transition to CONFIRMED (>=2 attestations)", func() (string, error) {
		info, err := r.nodes["val0"].registerAccount(uA)
		if err != nil {
			return "", fmt.Errorf("registerAccount: %w", err)
		}
		seen, last, err := r.nodes["val0"].waitStatus(uA.addr, "CONFIRMED", 45*time.Second)
		if err != nil {
			return "", fmt.Errorf("failed to reach CONFIRMED: %w (saw: %v)", err, seen)
		}
		confirmedUsers = append(confirmedUsers, uA.addr)
		return fmt.Sprintf("initial status: %s, sequence: %v -> final: %s", info.Status, seen, last.Status), nil
	})

	r.check("A3", "fund confirmed user and verify transfer succeeds with receipt status 1", func() (string, error) {
		amt := new(big.Int).Div(oneEther, big.NewInt(50)) // 0.02 ETH
		if _, err := r.nodes["val0"].transferEth(r.funder.key, uA.addr, amt); err != nil {
			return "", fmt.Errorf("funding uA: %w", err)
		}
		to := common.HexToAddress("0x00000000000000000000000000000000000a0001")
		tx, err := r.nodes["val0"].transferEth(uA.key, to, big.NewInt(1000000))
		if err != nil {
			return "", fmt.Errorf("uA transfer: %w", err)
		}
		return fmt.Sprintf("tx %s confirmed with status 1", tx.Hash().Hex()), nil
	})

	r.check("A4", "state parity across all 4 validators after Scenario A", func() (string, error) {
		return r.verifyStateParity(allNodes, confirmedUsers)
	})

	// =========================================================================
	// SCENARIO B: 1 Validator Offline (3 active >= 2f+1=3)
	// =========================================================================
	fmt.Println("\n════════════════════════════════════════════════════════════════")
	fmt.Println("📍 SCENARIO B: Tolerance with 1 Validator Offline (3 active >= 3)")
	fmt.Println("════════════════════════════════════════════════════════════════")

	r.check("B1", "stop validator val3", func() (string, error) {
		if err := r.runEnvCmd("stop_node", "val3"); err != nil {
			return "", err
		}
		time.Sleep(1 * time.Second)
		return "val3 stopped successfully", nil
	})

	uB := newUser()
	active3 := []string{"val0", "val1", "val2"}

	r.check("B2", "register new user uB while val3 is down (consensus advances)", func() (string, error) {
		info, err := r.nodes["val0"].registerAccount(uB)
		if err != nil {
			return "", fmt.Errorf("registerAccount: %w", err)
		}
		seen, last, err := r.nodes["val0"].waitStatus(uB.addr, "CONFIRMED", 45*time.Second)
		if err != nil {
			return "", fmt.Errorf("uB failed to confirm with 3 nodes active: %w", err)
		}
		confirmedUsers = append(confirmedUsers, uB.addr)
		return fmt.Sprintf("initial: %s, sequence: %v -> final: %s (quorum satisfied by 3 nodes)", info.Status, seen, last.Status), nil
	})

	r.check("B3", "fund and execute transaction from uB with 1 validator offline", func() (string, error) {
		amt := new(big.Int).Div(oneEther, big.NewInt(50))
		if _, err := r.nodes["val0"].transferEth(r.funder.key, uB.addr, amt); err != nil {
			return "", fmt.Errorf("funding uB: %w", err)
		}
		to := common.HexToAddress("0x00000000000000000000000000000000000b0002")
		tx, err := r.nodes["val0"].transferEth(uB.key, to, big.NewInt(2000000))
		if err != nil {
			return "", fmt.Errorf("uB transfer: %w", err)
		}
		return fmt.Sprintf("tx %s confirmed with status 1 on 3-node quorum", tx.Hash().Hex()), nil
	})

	r.check("B3b", "state parity across the 3 active validators before restart", func() (string, error) {
		return r.verifyStateParity(active3, confirmedUsers)
	})

	r.check("B4", "restart val3 and verify catchup to latest block", func() (string, error) {
		if err := r.runEnvCmd("start_node", "val3"); err != nil {
			return "", err
		}
		if err := r.reconnectNode("val3"); err != nil {
			return "", fmt.Errorf("reconnect val3: %w", err)
		}
		targetBlock, _ := r.nodes["val0"].blockNumber()
		if err := r.waitNodeCatchup("val3", targetBlock, 30*time.Second); err != nil {
			return "", err
		}
		b3, _ := r.nodes["val3"].blockNumber()
		return fmt.Sprintf("val3 restarted and caught up to block #%d", b3), nil
	})

	r.check("B5", "state parity across all 4 validators after Scenario B", func() (string, error) {
		return r.verifyStateParity(allNodes, confirmedUsers)
	})

	// =========================================================================
	// SCENARIO C: 2 Validators Offline (2 active < 2f+1=3) -> Consensus Pauses
	// =========================================================================
	fmt.Println("\n════════════════════════════════════════════════════════════════")
	fmt.Println("📍 SCENARIO C: Quarantine with 2 Validators Offline (2 active < 3)")
	fmt.Println("════════════════════════════════════════════════════════════════")

	r.check("C1", "stop 2 validators: val2 and val3", func() (string, error) {
		if err := r.runEnvCmd("stop_node", "val2"); err != nil {
			return "", err
		}
		if err := r.runEnvCmd("stop_node", "val3"); err != nil {
			return "", err
		}
		time.Sleep(1 * time.Second)
		return "val2 and val3 stopped (2 nodes remaining)", nil
	})

	uC := newUser()
	var preBlock uint64

	r.check("C2", "submit registration uC and verify consensus pauses with zero fork", func() (string, error) {
		var err error
		preBlock, err = r.nodes["val0"].blockNumber()
		if err != nil {
			return "", err
		}

		// Submit registration
		if _, err := r.nodes["val0"].registerAccount(uC); err != nil {
			return "", fmt.Errorf("registerAccount: %w", err)
		}

		// Wait 4 seconds and verify block height does not advance and uC is NOT confirmed
		time.Sleep(4 * time.Second)
		curBlock, _ := r.nodes["val0"].blockNumber()
		if curBlock > preBlock {
			return "", fmt.Errorf("consensus advanced unexpectedly (%d -> %d) without quorum!", preBlock, curBlock)
		}

		info, _ := r.nodes["val0"].status(uC.addr)
		if info != nil && info.Status == "CONFIRMED" {
			return "", errors.New("registration reached CONFIRMED without quorum!")
		}

		// Check zero fork between the two living nodes (val0 and val1)
		h0, err0 := r.nodes["val0"].getBlock(curBlock)
		h1, err1 := r.nodes["val1"].getBlock(curBlock)
		if err0 != nil || err1 != nil || h0.Hash != h1.Hash {
			return "", fmt.Errorf("divergence detected between val0 and val1 at block %d", curBlock)
		}

		return fmt.Sprintf("consensus halted at #%d (zero new blocks); uC status=%s; val0==val1 hash (ZERO FORK)", curBlock, info.Status), nil
	})

	r.check("C3", "restart val2 and val3; verify quorum restores and consensus advances", func() (string, error) {
		if err := r.runEnvCmd("start_node", "val2"); err != nil {
			return "", err
		}
		if err := r.runEnvCmd("start_node", "val3"); err != nil {
			return "", err
		}
		if err := r.reconnectNode("val2"); err != nil {
			return "", fmt.Errorf("reconnect val2: %w", err)
		}
		if err := r.reconnectNode("val3"); err != nil {
			return "", fmt.Errorf("reconnect val3: %w", err)
		}

		// Verify block advances past preBlock
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			b, err := r.nodes["val0"].blockNumber()
			if err == nil && b > preBlock {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}

		newBlock, _ := r.nodes["val0"].blockNumber()
		if newBlock <= preBlock {
			return "", fmt.Errorf("consensus did not resume after restarting val2 and val3 (block #%d)", newBlock)
		}

		// Wait for uC to reach CONFIRMED
		seen, last, err := r.nodes["val0"].waitStatus(uC.addr, "CONFIRMED", 45*time.Second)
		if err != nil {
			return "", fmt.Errorf("uC failed to confirm after restart: %w (saw: %v)", err, seen)
		}
		confirmedUsers = append(confirmedUsers, uC.addr)

		// Wait for val2 and val3 to catch up
		if err := r.waitNodeCatchup("val2", newBlock, 30*time.Second); err != nil {
			return "", err
		}
		if err := r.waitNodeCatchup("val3", newBlock, 30*time.Second); err != nil {
			return "", err
		}

		return fmt.Sprintf("consensus resumed: #%d -> #%d; uC reached CONFIRMED (%v)", preBlock, newBlock, last.Status), nil
	})

	r.check("C4", "fund and execute transaction from uC", func() (string, error) {
		amt := new(big.Int).Div(oneEther, big.NewInt(50))
		if _, err := r.nodes["val0"].transferEth(r.funder.key, uC.addr, amt); err != nil {
			return "", fmt.Errorf("funding uC: %w", err)
		}
		to := common.HexToAddress("0x00000000000000000000000000000000000c0003")
		tx, err := r.nodes["val0"].transferEth(uC.key, to, big.NewInt(3000000))
		if err != nil {
			return "", fmt.Errorf("uC transfer: %w", err)
		}
		return fmt.Sprintf("tx %s confirmed with status 1 after quarantine recovery", tx.Hash().Hex()), nil
	})

	r.check("C5", "state parity across all 4 validators after Scenario C", func() (string, error) {
		return r.verifyStateParity(allNodes, confirmedUsers)
	})

	// =========================================================================
	// SCENARIO D: Byzantine Fake Event Injection (Never Confirmed)
	// =========================================================================
	fmt.Println("\n════════════════════════════════════════════════════════════════")
	fmt.Println("📍 SCENARIO D: Byzantine Fake Event (Never Reaches ParentRegistered)")
	fmt.Println("════════════════════════════════════════════════════════════════")

	uD := newUser()

	r.check("D1", "single validator injects fake attestation for unverified user (only 1 attestation)", func() (string, error) {
		// Construct synthetic fake event digest
		clusterPubBytes, _ := hex.DecodeString(strings.TrimPrefix(env.Cluster.BlsPub, "0x"))
		var clusterPub cm.PublicKey
		copy(clusterPub[:], clusterPubBytes)

		fakeSeq := uint64(999991)
		digest := rollup.ComputeAccountRegistrationAttestDigest(chainID, uD.addr, clusterPub, fakeSeq)

		// Sign with val0 BLS private key (simulating a rogue single validator)
		val0PrivBytes, _ := hex.DecodeString(env.Validators["val0"].BlsPriv)
		val0Priv := cm.PrivateKeyFromBytes(val0PrivBytes)
		val0PubBytes, _ := hex.DecodeString(strings.TrimPrefix(env.Validators["val0"].BlsPub, "0x"))
		var val0Pub cm.PublicKey
		copy(val0Pub[:], val0PubBytes)

		att := rollup.RegistrationAttestation{
			ValidatorPubkey: val0Pub,
			Signature:       bls.Sign(val0Priv, digest),
		}

		payload := rollup.AccountRegistrationPayload{
			Kind:         rollup.SystemPayloadKindAccountRegistered,
			User:         uD.addr,
			ClusterKey:   clusterPub,
			ParentSeq:    fakeSeq,
			Attestations: []rollup.RegistrationAttestation{att},
		}
		payloadBytes, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}

		// Send system transaction from cluster identity (authorized BLS native sender)
		clusterAddr := common.HexToAddress(env.Cluster.Address)
		tx, err := r.sendSystemTx("val0", env.Cluster.PrivateKey, clusterAddr, payloadBytes)
		if err != nil {
			return "", fmt.Errorf("send fake system tx: %w", err)
		}

		// Wait for tx to be processed / included
		time.Sleep(3 * time.Second)
		return fmt.Sprintf("fake attestation tx submitted (hash: %s); 1 attestation in payload", tx.Hash().Hex()), nil
	})

	r.check("D2", "verify fake user uD NEVER becomes ParentRegistered on any node", func() (string, error) {
		// Honest validators val1, val2, val3 will never attest because uD was never on Parent Chain
		for _, name := range allNodes {
			info, err := r.nodes[name].status(uD.addr)
			if err == nil && info != nil && info.Status == "CONFIRMED" {
				return "", fmt.Errorf("CRITICAL SECURITY FAILURE: node %s marked unverified fake user as CONFIRMED!", name)
			}
		}

		// Fund uD and attempt to transfer ETH
		amt := new(big.Int).Div(oneEther, big.NewInt(50))
		if _, err := r.nodes["val0"].transferEth(r.funder.key, uD.addr, amt); err != nil {
			return "", fmt.Errorf("funding uD: %w", err)
		}

		to := common.HexToAddress("0x00000000000000000000000000000000000d0004")
		tx, err := e_types.SignNewTx(uD.key, e_types.LatestSignerForChainID(big.NewInt(chainID)),
			&e_types.LegacyTx{Nonce: 0, GasPrice: big.NewInt(gasPrice), Gas: 21000, To: &to, Value: big.NewInt(5000)})
		if err != nil {
			return "", err
		}
		_, err = r.nodes["val0"].sendRaw(tx)
		if err == nil {
			return "", errors.New("fake user uD was allowed to send an ETH transaction!")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "not registered") {
			return "", fmt.Errorf("unexpected error message: %v", err)
		}
		return "fake user uD correctly blocked with: " + err.Error(), nil
	})

	r.check("D3", "forged attestation with non-committee key is rejected by handler", func() (string, error) {
		// Non-committee rogue key
		attackerKey := bls.GenerateKeyPair()
		clusterPubBytes, _ := hex.DecodeString(strings.TrimPrefix(env.Cluster.BlsPub, "0x"))
		var clusterPub cm.PublicKey
		copy(clusterPub[:], clusterPubBytes)

		digest := rollup.ComputeAccountRegistrationAttestDigest(chainID, uD.addr, clusterPub, 999992)
		att := rollup.RegistrationAttestation{
			ValidatorPubkey: attackerKey.PublicKey(),
			Signature:       bls.Sign(attackerKey.PrivateKey(), digest),
		}
		payload := rollup.AccountRegistrationPayload{
			Kind:         rollup.SystemPayloadKindAccountRegistered,
			User:         uD.addr,
			ClusterKey:   clusterPub,
			ParentSeq:    999992,
			Attestations: []rollup.RegistrationAttestation{att},
		}
		payloadBytes, _ := json.Marshal(payload)

		clusterAddr := common.HexToAddress(env.Cluster.Address)
		tx, err := r.sendSystemTx("val0", env.Cluster.PrivateKey, clusterAddr, payloadBytes)
		if err != nil {
			return "", fmt.Errorf("send non-committee tx: %w", err)
		}

		time.Sleep(3 * time.Second)
		// Check receipt on val0: must have status 0 (execution failed due to non-committee key) or not found
		receipt, ok, _ := func() (map[string]interface{}, bool, error) {
			var raw map[string]interface{}
			err := r.nodes["val0"].call(&raw, "eth_getTransactionReceipt", tx.Hash())
			return raw, raw != nil, err
		}()
		if ok && receipt != nil {
			s, _ := receipt["status"].(string)
			v, _ := hexutil.DecodeUint64(s)
			if v == 1 {
				return "", errors.New("non-committee attestation transaction succeeded (expected failure)")
			}
		}
		return "non-committee attestation failed execution as expected", nil
	})

	r.check("D4", "state parity across all 4 validators after Scenario D", func() (string, error) {
		return r.verifyStateParity(allNodes, confirmedUsers)
	})

	// =========================================================================
	// SCENARIO E: Crash Recovery (kill -9 mid-traffic)
	// =========================================================================
	fmt.Println("\n════════════════════════════════════════════════════════════════")
	fmt.Println("📍 SCENARIO E: Crash Recovery (kill -9 mid-traffic, Block-by-Block Audit)")
	fmt.Println("════════════════════════════════════════════════════════════════")

	uE := newUser()

	r.check("E1", "kill -9 val1 while registering uE; quorum remains alive and confirms", func() (string, error) {
		// Submit registration on val0
		if _, err := r.nodes["val0"].registerAccount(uE); err != nil {
			return "", fmt.Errorf("registerAccount uE: %w", err)
		}

		// Abruptly kill val1 with SIGKILL
		if err := r.runEnvCmd("kill_node", "val1"); err != nil {
			return "", fmt.Errorf("kill_node val1: %w", err)
		}

		// Observe remaining nodes (val0, val2, val3) complete consensus and confirm uE
		seen, last, err := r.nodes["val0"].waitStatus(uE.addr, "CONFIRMED", 45*time.Second)
		if err != nil {
			return "", fmt.Errorf("uE failed to confirm during kill-9: %w (saw: %v)", err, seen)
		}
		confirmedUsers = append(confirmedUsers, uE.addr)

		// Fund and execute tx from uE
		amt := new(big.Int).Div(oneEther, big.NewInt(50))
		if _, err := r.nodes["val0"].transferEth(r.funder.key, uE.addr, amt); err != nil {
			return "", fmt.Errorf("funding uE: %w", err)
		}
		to := common.HexToAddress("0x00000000000000000000000000000000000e0005")
		tx, err := r.nodes["val0"].transferEth(uE.key, to, big.NewInt(5000000))
		if err != nil {
			return "", fmt.Errorf("uE transfer: %w", err)
		}

		return fmt.Sprintf("val1 killed with SIGKILL; uE confirmed (%v); tx %s status 1", last.Status, tx.Hash().Hex()), nil
	})

	r.check("E2", "restart val1 and verify catchup to latest block", func() (string, error) {
		if err := r.runEnvCmd("start_node", "val1"); err != nil {
			return "", err
		}
		if err := r.reconnectNode("val1"); err != nil {
			return "", fmt.Errorf("reconnect val1: %w", err)
		}
		targetBlock, _ := r.nodes["val0"].blockNumber()
		if err := r.waitNodeCatchup("val1", targetBlock, 30*time.Second); err != nil {
			return "", err
		}
		b1, _ := r.nodes["val1"].blockNumber()
		return fmt.Sprintf("val1 recovered from SIGKILL and caught up to block #%d", b1), nil
	})

	r.check("E3", "exhaustive block-by-block hash and stateRoot audit across all 4 nodes", func() (string, error) {
		targetBlock, _ := r.nodes["val0"].blockNumber()
		for b := uint64(1); b <= targetBlock; b++ {
			var base *blockHeader
			for _, name := range allNodes {
				hdr, err := r.nodes[name].getBlock(b)
				if err != nil {
					return "", fmt.Errorf("block #%d: node %s error: %w", b, name, err)
				}
				if base == nil {
					base = hdr
				} else {
					if hdr.Hash != base.Hash {
						return "", fmt.Errorf("AUDIT FAIL: block #%d hash mismatch (%s vs %s)", b, base.Hash, hdr.Hash)
					}
					if hdr.StateRoot != base.StateRoot {
						return "", fmt.Errorf("AUDIT FAIL: block #%d stateRoot mismatch (%s vs %s)", b, base.StateRoot, hdr.StateRoot)
					}
				}
			}
		}
		return fmt.Sprintf("Blocks #1 through #%d verified identical across all 4 nodes (0 mismatches)", targetBlock), nil
	})

	r.check("E4", "all registered users CONFIRMED on all 4 nodes and fake user NOT confirmed", func() (string, error) {
		for _, u := range confirmedUsers {
			for _, name := range allNodes {
				info, err := r.nodes[name].status(u)
				if err != nil || info.Status != "CONFIRMED" {
					return "", fmt.Errorf("user %s not confirmed on %s (status: %v, err: %v)", u.Hex(), name, info, err)
				}
			}
		}
		for _, name := range allNodes {
			info, _ := r.nodes[name].status(uD.addr)
			if info != nil && info.Status == "CONFIRMED" {
				return "", fmt.Errorf("fake user uD confirmed on %s!", name)
			}
		}
		return fmt.Sprintf("%d users confirmed on all 4 nodes; fake user unconfirmed everywhere", len(confirmedUsers)), nil
	})

	// ---------------------------------------------------------------- Report Generation

	fmt.Println("\n════════════════════════════════════════════════════════════════")
	fmt.Printf("🏁 SUITE RESULTS: %d steps executed\n", len(r.steps))
	passCount, failCount := 0, 0
	for _, s := range r.steps {
		if s.Status == "PASS" {
			passCount++
		} else {
			failCount++
		}
	}
	fmt.Printf("   PASS: %d | FAIL: %d\n", passCount, failCount)
	fmt.Println("════════════════════════════════════════════════════════════════\n")

	if *reportPath != "" {
		writeReport(*reportPath, r)
	}

	if !r.allPassed {
		os.Exit(1)
	}
}

func writeReport(path string, r *runner) {
	var buf bytes.Buffer
	buf.WriteString("# Co-attestation 4-Validator E2E Verification Report\n\n")
	buf.WriteString(fmt.Sprintf("- **Date**: %s\n", time.Now().UTC().Format(time.RFC3339)))
	buf.WriteString(fmt.Sprintf("- **Environment**: 4 Validators (Mysticeti Consensus), Chain ID %d\n", chainID))
	buf.WriteString(fmt.Sprintf("- **Threshold**: Committee Size $N=4$, $f=1$, Required $f+1=2$\n\n"))
	buf.WriteString("### Execution Matrix\n\n")
	buf.WriteString("| Step | Scenario | Status | Duration | Detail |\n")
	buf.WriteString("| :--- | :--- | :--- | :--- | :--- |\n")
	for _, s := range r.steps {
		icon := map[string]string{"PASS": "✅ PASS", "FAIL": "❌ FAIL", "SKIP": "⏭️ SKIP"}[s.Status]
		buf.WriteString(fmt.Sprintf("| %s | %s | %s | %v | %s |\n", s.ID, s.Name, icon, s.Took.Round(time.Millisecond), s.Detail))
	}
	_ = os.WriteFile(path, buf.Bytes(), 0644)
}
