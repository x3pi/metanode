// e2e_account_gate drives the end-to-end test of the secp-mode account gate against the isolated environment created by
// scripts/test/gate_e2e/gen_env.py (one Parent Chain + two execution clusters that share chain ID 991).
//
// Every step talks to real processes: eth_* / mtn_* JSON-RPC, the TCP client for type-0xFF transactions, the Parent Chain
// HTTP API and the nodes' log files (the only place a rejected TCP transaction is reported). Nothing is mocked.
//
//	go run ./cmd/tool/e2e_account_gate -env <BASE>/env.json [-report report.md]
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"

	client "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	c_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/types"
)

const (
	chainID      = 991
	gasPrice     = 1_000_000_000
	funderKeyHex = "a3e6d454ea7a3b464af1f8c891259d5ff48f331004d56d1331388ec3c3915fe1"
	registryPoll = 150 * time.Second
)

var oneEther = new(big.Int).Mul(big.NewInt(1_000_000_000), big.NewInt(1_000_000_000))

type envInfo struct {
	Base  string `json:"base"`
	Ports struct {
		ParentHTTP int                     `json:"parent_http"`
		Exec1      struct{ RPC, Conn int } `json:"exec1"`
		Exec2      struct{ RPC, Conn int } `json:"exec2"`
	} `json:"ports"`
	Clusters map[string]struct {
		Address string `json:"address"`
		BlsPub  string `json:"bls_pub"`
	} `json:"clusters"`
}

type cluster struct {
	name    string
	rpc     *rpc.Client
	conn    string // TCP address for type-0xFF
	logDir  string // the node's own log directory (<node>/logs/<date>/execution.log holds the application log)
	key     string // cluster BLS public key (hex)
	address string
	gateOn  bool
}

type step struct {
	ID, Name, Status, Detail string
	Took                     time.Duration
}

type runner struct {
	env   envInfo
	exec1 *cluster
	exec2 *cluster
	steps []step
	mu    sync.Mutex
}

func (r *runner) record(id, name, status, detail string, took time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.steps = append(r.steps, step{id, name, status, detail, took})
	icon := map[string]string{"PASS": "✅", "FAIL": "❌", "SKIP": "⏭️"}[status]
	fmt.Printf("%s %-6s %s — %s\n", icon, id, name, detail)
}

// check runs one step; a returned error is a FAIL, a panic is a FAIL, otherwise PASS with the returned detail.
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
		return false
	}
	r.record(id, name, "PASS", detail, time.Since(start))
	return true
}

func (r *runner) skip(id, name, why string) { r.record(id, name, "SKIP", why, 0) }

// ---------------------------------------------------------------- helpers

type user struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

func newUser() user {
	k, _ := crypto.GenerateKey()
	return user{k, crypto.PubkeyToAddress(k.PublicKey)}
}

func (c *cluster) call(result interface{}, method string, args ...interface{}) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return c.rpc.CallContext(ctx, result, method, args...)
}

func (c *cluster) blockNumber() (uint64, error) {
	var h hexutil.Uint64
	err := c.call(&h, "eth_blockNumber")
	return uint64(h), err
}

func (c *cluster) balance(a common.Address) (*big.Int, error) {
	var b hexutil.Big
	err := c.call(&b, "eth_getBalance", a, "latest")
	return (*big.Int)(&b), err
}

func (c *cluster) pendingNonce(a common.Address) (uint64, error) {
	var n hexutil.Uint64
	err := c.call(&n, "eth_getTransactionCount", a, "pending")
	return uint64(n), err
}

// signEth builds an EIP-155 legacy transfer signed for chain 991.
func signEth(k *ecdsa.PrivateKey, nonce uint64, to common.Address, value *big.Int, data []byte) (*e_types.Transaction, error) {
	return e_types.SignNewTx(k, e_types.LatestSignerForChainID(big.NewInt(chainID)),
		&e_types.LegacyTx{Nonce: nonce, GasPrice: big.NewInt(gasPrice), Gas: 21000 + uint64(len(data))*68, To: &to, Value: value, Data: data})
}

func (c *cluster) sendRaw(tx *e_types.Transaction) (common.Hash, error) {
	raw, err := tx.MarshalBinary()
	if err != nil {
		return common.Hash{}, err
	}
	var h common.Hash
	err = c.call(&h, "eth_sendRawTransaction", hexutil.Encode(raw))
	return h, err
}

// receipt returns (status, found). It reads the raw JSON so any extra fields of this node's receipts are tolerated.
func (c *cluster) receipt(h common.Hash) (uint64, bool, error) {
	var raw map[string]interface{}
	if err := c.call(&raw, "eth_getTransactionReceipt", h); err != nil {
		return 0, false, err
	}
	if raw == nil {
		return 0, false, nil
	}
	s, _ := raw["status"].(string)
	v, _ := hexutil.DecodeUint64(s)
	return v, true, nil
}

func (c *cluster) waitReceipt(h common.Hash, timeout time.Duration) (uint64, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st, ok, err := c.receipt(h)
		if err == nil && ok {
			return st, nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return 0, fmt.Errorf("no receipt for %s within %v", h.Hex(), timeout)
}

// transferEth signs, sends and waits for a successful receipt.
func (c *cluster) transferEth(k *ecdsa.PrivateKey, to common.Address, value *big.Int) (*e_types.Transaction, error) {
	from := crypto.PubkeyToAddress(k.PublicKey)
	nonce, err := c.pendingNonce(from)
	if err != nil {
		return nil, err
	}
	tx, err := signEth(k, nonce, to, value, nil)
	if err != nil {
		return nil, err
	}
	h, err := c.sendRaw(tx)
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}
	st, err := c.waitReceipt(h, 60*time.Second)
	if err != nil {
		return nil, err
	}
	if st != 1 {
		return nil, fmt.Errorf("receipt status %d for %s", st, h.Hex())
	}
	return tx, nil
}

func funder() user {
	k, _ := crypto.HexToECDSA(funderKeyHex)
	return user{k, crypto.PubkeyToAddress(k.PublicKey)}
}

type regInfo struct {
	Status      string `json:"status"`
	HomeCluster string `json:"homeCluster"`
	Reason      string `json:"reason"`
}

type regMessage struct {
	Digest     string `json:"digest"`
	HashToSign string `json:"hashToSign"`
}

// register is what a user (wallet/SDK) does: ask the node what to sign, sign, call mtn_registerAccount.
func (c *cluster) register(u user) (*regInfo, error) {
	var msg regMessage
	if err := c.call(&msg, "mtn_getRegistrationMessage", u.addr); err != nil {
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
	if err := c.call(&info, "mtn_registerAccount", u.addr, hexutil.Bytes(sig)); err != nil {
		return nil, fmt.Errorf("registerAccount: %w", err)
	}
	return &info, nil
}

func (c *cluster) status(a common.Address) (*regInfo, error) {
	var info regInfo
	if err := c.call(&info, "mtn_getRegistrationStatus", a); err != nil {
		return nil, err
	}
	return &info, nil
}

// waitStatus polls until the address reaches want, returning the sequence of distinct statuses it went through.
func (c *cluster) waitStatus(a common.Address, want string, timeout time.Duration) ([]string, *regInfo, error) {
	var seen []string
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := c.status(a)
		if err == nil {
			if len(seen) == 0 || seen[len(seen)-1] != info.Status {
				seen = append(seen, info.Status)
			}
			if info.Status == want {
				return seen, info, nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return seen, nil, fmt.Errorf("status of %s never reached %s within %v (saw %v)", a.Hex(), want, timeout, seen)
}

func (r *runner) parentAccount(a common.Address) (found bool, key string, err error) {
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/account?address=%s", r.env.Ports.ParentHTTP, a.Hex()))
	if err != nil {
		return false, "", err
	}
	defer resp.Body.Close()
	var out struct {
		FloatIdentityKey []byte `json:"float_identity_key"`
		Found            bool   `json:"found"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, "", err
	}
	return out.Found, "0x" + hex.EncodeToString(out.FloatIdentityKey), nil
}

// logMark remembers a log file size so a later search only looks at what happened afterwards.
func logMark(path string) int64 {
	st, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return st.Size()
}

// waitLog waits for substr to appear in the log after mark.
func waitLog(path string, mark int64, substr string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		f, err := os.Open(path)
		if err == nil {
			f.Seek(mark, io.SeekStart)
			data, _ := io.ReadAll(f)
			f.Close()
			if i := bytes.Index(data, []byte(substr)); i >= 0 {
				end := i + 220
				if end > len(data) {
					end = len(data)
				}
				return strings.ReplaceAll(string(data[i:end]), "\n", " "), nil
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	return "", fmt.Errorf("%q not found in %s within %v", substr, path, timeout)
}

// execLog is the newest application log file of the node (where rejected transactions are reported).
func (c *cluster) execLog() string {
	matches, _ := filepath.Glob(c.logDir + "/*/execution.log")
	best, bestTime := "", time.Time{}
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && st.ModTime().After(bestTime) {
			best, bestTime = m, st.ModTime()
		}
	}
	return best
}

func (c *cluster) tcpClient(u user) (*client.Client, error) {
	// The TCP connection has its own BLS identity (a random valid BLS key); the transactions themselves are secp-signed
	// with the user's key. (An ECDSA key is NOT a valid BLS private key about half of the time.)
	connKey := bls.GenerateKeyPair()
	cfg := &c_config.ClientConfig{
		Version_: "0.0.1.0", PrivateKey_: hex.EncodeToString(connKey.BytesPrivateKey()),
		ParentConnectionAddress: c.conn, ParentAddress: c.address, ChainId: chainID, ParentConnectionType: "client",
	}
	return client.NewClient(cfg)
}

// sendProto sends a type-0xFF transaction over TCP and returns its hash (without waiting for a receipt).
func (c *cluster) sendProto(u user, to common.Address, amount *big.Int) (*client.Client, common.Hash, error) {
	cl, err := c.tcpClient(u)
	if err != nil {
		return nil, common.Hash{}, err
	}
	// The TCP handshake completes asynchronously; retry only the "connection not ready yet" failure (a client-side
	// condition, the tx has not been sent) so a slow handshake is not reported as a node failure.
	var tx types.Transaction
	for i := 0; i < 20; i++ {
		time.Sleep(500 * time.Millisecond)
		tx, err = cl.SendSecpProtoTransactionNoWait(u.key, to, amount, 21000, gasPrice, nil)
		if err == nil || !strings.Contains(err.Error(), "realConnAddr") {
			break
		}
	}
	if err != nil {
		return nil, common.Hash{}, err
	}
	return cl, tx.Hash(), nil
}

// runRestartStep supports the kill -9 durability test driven by a shell script: the registration request must survive an
// execution-node crash and still reach CONFIRMED once the Parent Chain is reachable again.
func runRestartStep(c *cluster, step, keyHex string) int {
	k, err := crypto.HexToECDSA(keyHex)
	if err != nil {
		fmt.Println("bad -user-key:", err)
		return 2
	}
	u := user{k, crypto.PubkeyToAddress(k.PublicKey)}
	switch step {
	case "register":
		f := funder()
		if _, err := c.transferEth(f.key, u.addr, oneEther); err != nil {
			fmt.Println("funding:", err)
			return 1
		}
		info, err := c.register(u)
		if err != nil {
			fmt.Println("register:", err)
			return 1
		}
		fmt.Println("registered, status:", info.Status)
		return 0
	case "verify":
		path, info, err := c.waitStatus(u.addr, "CONFIRMED", 120*time.Second)
		fmt.Println("status path:", path)
		if err != nil || info == nil {
			fmt.Println("NOT CONFIRMED:", err)
			return 1
		}
		fmt.Println("CONFIRMED after restart")
		return 0
	}
	fmt.Println("unknown -restart-step", step)
	return 2
}

func errContains(err error, sub string) error {
	if err == nil {
		return fmt.Errorf("expected an error containing %q, got success", sub)
	}
	if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(sub)) {
		return fmt.Errorf("error %q does not contain %q (rejected for the wrong reason)", err.Error(), sub)
	}
	return nil
}

// ---------------------------------------------------------------- scenarios

func main() {
	envPath := flag.String("env", "", "path to env.json written by gen_env.py")
	report := flag.String("report", "", "write a markdown report to this file")
	restartStep := flag.String("restart-step", "", "durability test helper: 'register' (fund + register -user-key on exec1, print status) or 'verify' (wait for CONFIRMED)")
	userKeyHex := flag.String("user-key", "", "hex secp256k1 key of the user for -restart-step")
	flag.Parse()
	if *envPath == "" {
		fmt.Println("usage: e2e_account_gate -env <BASE>/env.json [-report file]")
		os.Exit(2)
	}
	r := &runner{}
	raw, err := os.ReadFile(*envPath)
	if err != nil || json.Unmarshal(raw, &r.env) != nil {
		fmt.Println("cannot read env:", err)
		os.Exit(2)
	}
	mk := func(name string, rpcPort, conn int) *cluster {
		c, err := rpc.Dial(fmt.Sprintf("http://127.0.0.1:%d", rpcPort))
		if err != nil {
			fmt.Println("dial", name, err)
			os.Exit(2)
		}
		cl := r.env.Clusters[name]
		return &cluster{name: name, rpc: c, conn: fmt.Sprintf("127.0.0.1:%d", conn), logDir: r.env.Base + "/" + name + "/logs",
			key: strings.ToLower(cl.BlsPub), address: cl.Address}
	}
	r.exec1 = mk("exec1", r.env.Ports.Exec1.RPC, r.env.Ports.Exec1.Conn)
	r.exec2 = mk("exec2", r.env.Ports.Exec2.RPC, r.env.Ports.Exec2.Conn)
	if *restartStep != "" {
		os.Exit(runRestartStep(r.exec1, *restartStep, *userKeyHex))
	}
	for _, c := range []*cluster{r.exec1, r.exec2} {
		var id struct {
			AccountGate bool `json:"accountGate"`
		}
		c.gateOn = c.call(&id, "mtn_getClusterIdentity") == nil && id.AccountGate
	}
	fmt.Printf("== account gate E2E: exec1 gate=%v exec2 gate=%v (exec2 gate off => replay CONTROL mode)\n\n", r.exec1.gateOn, r.exec2.gateOn)

	f := funder()
	u1, u2, u3 := newUser(), newUser(), newUser()
	to := common.HexToAddress("0x00000000000000000000000000000000000c0ffe")
	amt := new(big.Int).Div(oneEther, big.NewInt(100)) // 0.01

	// ---- E1: liveness + genesis account (registered at genesis) can send on both clusters
	r.check("E1", "liveness: genesis-registered funder funds users on both clusters", func() (string, error) {
		b0, _ := r.exec1.blockNumber()
		for _, u := range []user{u1, u2, u3} {
			for _, c := range []*cluster{r.exec1, r.exec2} {
				if _, err := c.transferEth(f.key, u.addr, oneEther); err != nil {
					return "", fmt.Errorf("%s funding %s: %w", c.name, u.addr.Hex(), err)
				}
			}
		}
		b1, _ := r.exec1.blockNumber()
		if b1 <= b0 {
			return "", fmt.Errorf("exec1 block number did not advance (%d -> %d)", b0, b1)
		}
		return fmt.Sprintf("exec1 block %d -> %d; 3 users funded on both clusters (same addresses, same nonce 0)", b0, b1), nil
	})

	// ---- E2: unregistered users are refused (ETH over RPC, 0xFF over TCP) with the gate's own error
	r.check("E2a", "unregistered user: ETH tx over RPC is refused with 'not registered', balance untouched", func() (string, error) {
		b0, _ := r.exec1.balance(u1.addr)
		tx, _ := signEth(u1.key, 0, to, amt, nil)
		_, err := r.exec1.sendRaw(tx)
		if e := errContains(err, "not registered"); e != nil {
			return "", e
		}
		b1, _ := r.exec1.balance(u1.addr)
		if b0.Cmp(b1) != 0 {
			return "", fmt.Errorf("balance changed %s -> %s", b0, b1)
		}
		return "refused: " + err.Error(), nil
	})
	r.check("E2b", "unregistered user: 0xFF tx over TCP is refused (node log shows the gate error)", func() (string, error) {
		mark := logMark(r.exec1.execLog())
		cl, h, err := r.exec1.sendProto(u1, to, amt)
		if err != nil {
			return "", err
		}
		_ = cl
		line, err := waitLog(r.exec1.execLog(), mark, "account not registered on parent chain", 20*time.Second)
		if err != nil {
			return "", err
		}
		if _, ok, _ := r.exec1.receipt(h); ok {
			return "", fmt.Errorf("a receipt exists for a gated transaction")
		}
		return "refused: " + line, nil
	})

	// ---- E3: the user talks only to the execution node; node + parent promote the account automatically
	r.check("E3a", "mtn_getClusterIdentity matches the cluster identity", func() (string, error) {
		var id struct {
			ClusterKey string `json:"clusterKey"`
			ChainID    uint64 `json:"chainId"`
		}
		if err := r.exec1.call(&id, "mtn_getClusterIdentity"); err != nil {
			return "", err
		}
		if strings.ToLower(id.ClusterKey) != r.exec1.key || id.ChainID != chainID {
			return "", fmt.Errorf("got %s chain %d, want %s chain %d", id.ClusterKey, id.ChainID, r.exec1.key, chainID)
		}
		return id.ClusterKey[:18] + "…", nil
	})
	var registered bool
	registered = r.check("E3b", "user registers with the node only; status goes PENDING -> CONFIRMED automatically", func() (string, error) {
		st0, err := r.exec1.status(u1.addr)
		if err != nil || st0.Status != "NONE" {
			return "", fmt.Errorf("initial status %v (%v), want NONE", st0, err)
		}
		info, err := r.exec1.register(u1)
		if err != nil {
			return "", err
		}
		if info.Status != "PENDING" && info.Status != "CONFIRMED" {
			return "", fmt.Errorf("registerAccount returned %q", info.Status)
		}
		seen, _, err := r.exec1.waitStatus(u1.addr, "CONFIRMED", registryPoll)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("status path %v (the user called nothing but mtn_registerAccount)", append([]string{info.Status}, seen...)), nil
	})
	r.check("E3c", "the Parent Chain registry names exec1 as the home cluster", func() (string, error) {
		found, key, err := r.parentAccount(u1.addr)
		if err != nil {
			return "", err
		}
		if !found || strings.ToLower(key) != r.exec1.key {
			return "", fmt.Errorf("parent registry found=%v key=%s, want exec1 %s", found, key, r.exec1.key)
		}
		return "parent /account -> " + key[:18] + "…", nil
	})

	// ---- E4: after registration the same kinds of transaction succeed
	var replayTx *e_types.Transaction
	if registered {
		r.check("E4a", "registered user: ETH tx over RPC succeeds with a receipt", func() (string, error) {
			b0, _ := r.exec1.balance(to)
			tx, err := r.exec1.transferEth(u1.key, to, amt)
			if err != nil {
				return "", err
			}
			replayTx = tx
			b1, _ := r.exec1.balance(to)
			if new(big.Int).Sub(b1, b0).Cmp(amt) != 0 {
				return "", fmt.Errorf("recipient received %s, want %s", new(big.Int).Sub(b1, b0), amt)
			}
			return fmt.Sprintf("tx %s nonce %d, recipient +%s", tx.Hash().Hex()[:12], tx.Nonce(), amt), nil
		})
		r.check("E4b", "registered user: 0xFF tx over TCP succeeds with a receipt", func() (string, error) {
			cl, h, err := r.exec1.sendProto(u1, to, amt)
			if err != nil {
				return "", err
			}
			rc, err := cl.WaitForReceipt(h, 60*time.Second)
			if err != nil || rc == nil {
				return "", fmt.Errorf("no receipt: %v", err)
			}
			return fmt.Sprintf("receipt status %v", rc.Status()), nil
		})
	} else {
		r.skip("E4", "registered user sends", "registration did not complete")
	}

	// ---- E5: cross-cluster replay (same chain ID, same address funded with the same nonce on both clusters)
	if replayTx != nil {
		if r.exec2.gateOn {
			r.check("E5", "REPLAY on exec2 (same chain ID 991, funded, same nonce) is refused: account not registered there", func() (string, error) {
				b0, _ := r.exec2.balance(u1.addr)
				_, err := r.exec2.sendRaw(replayTx)
				if e := errContains(err, "not registered"); e != nil {
					return "", e
				}
				b1, _ := r.exec2.balance(u1.addr)
				if b0.Cmp(b1) != 0 {
					return "", fmt.Errorf("exec2 balance changed %s -> %s", b0, b1)
				}
				return "refused: " + err.Error(), nil
			})
		} else {
			r.check("E5-control", "CONTROL (gate off on exec2): the very same signed tx IS replayed and executed", func() (string, error) {
				b0, _ := r.exec2.balance(u1.addr)
				h, err := r.exec2.sendRaw(replayTx)
				if err != nil {
					return "", fmt.Errorf("replay should have been accepted, got: %w", err)
				}
				if st, err := r.exec2.waitReceipt(h, 60*time.Second); err != nil || st != 1 {
					return "", fmt.Errorf("replay receipt: status %d err %v", st, err)
				}
				b1, _ := r.exec2.balance(u1.addr)
				if b0.Cmp(b1) <= 0 {
					return "", fmt.Errorf("balance did not drop: %s -> %s", b0, b1)
				}
				return fmt.Sprintf("replay executed: exec2 balance %s -> %s (this is the risk the gate removes)", b0, b1), nil
			})
		}
	}

	// ---- E6: the same address asks exec2 as well: the parent already chose exec1
	if r.exec2.gateOn && registered {
		r.check("E6", "same address registers on exec2 afterwards: REJECTED, home cluster is exec1, still cannot send there", func() (string, error) {
			if _, err := r.exec2.register(u1); err != nil {
				return "", err
			}
			_, info, err := r.exec2.waitStatus(u1.addr, "REJECTED", registryPoll)
			if err != nil {
				return "", err
			}
			if strings.ToLower(info.HomeCluster) != r.exec1.key {
				return "", fmt.Errorf("home cluster %s, want %s", info.HomeCluster, r.exec1.key)
			}
			tx, _ := signEth(u1.key, 0, to, amt, nil)
			_, err = r.exec2.sendRaw(tx)
			if e := errContains(err, "not registered"); e != nil {
				return "", e
			}
			return "REJECTED, home = exec1, tx refused", nil
		})
	}

	// ---- E7: two clusters race for a fresh address; the parent picks exactly one
	if r.exec2.gateOn {
		r.check("E7", "race: u2 registers on BOTH clusters at once; exactly one CONFIRMED, the other REJECTED", func() (string, error) {
			var wg sync.WaitGroup
			errs := make([]error, 2)
			for i, c := range []*cluster{r.exec1, r.exec2} {
				wg.Add(1)
				go func(i int, c *cluster) {
					defer wg.Done()
					_, errs[i] = c.register(u2)
				}(i, c)
			}
			wg.Wait()
			for _, e := range errs {
				if e != nil {
					return "", e
				}
			}
			deadline := time.Now().Add(registryPoll)
			var s1, s2 string
			for time.Now().Before(deadline) {
				i1, _ := r.exec1.status(u2.addr)
				i2, _ := r.exec2.status(u2.addr)
				if i1 != nil && i2 != nil {
					s1, s2 = i1.Status, i2.Status
					if (s1 == "CONFIRMED" && s2 == "REJECTED") || (s1 == "REJECTED" && s2 == "CONFIRMED") {
						break
					}
				}
				time.Sleep(time.Second)
			}
			if !((s1 == "CONFIRMED" && s2 == "REJECTED") || (s1 == "REJECTED" && s2 == "CONFIRMED")) {
				return "", fmt.Errorf("statuses exec1=%s exec2=%s, want exactly one CONFIRMED and one REJECTED", s1, s2)
			}
			winner, loser := r.exec1, r.exec2
			if s2 == "CONFIRMED" {
				winner, loser = r.exec2, r.exec1
			}
			if _, err := winner.transferEth(u2.key, to, amt); err != nil {
				return "", fmt.Errorf("winner %s cannot send: %w", winner.name, err)
			}
			tx, _ := signEth(u2.key, 0, to, amt, nil)
			if _, err := loser.sendRaw(tx); errContains(err, "not registered") != nil {
				return "", fmt.Errorf("loser %s should refuse: %v", loser.name, err)
			}
			found, key, _ := r.parentAccount(u2.addr)
			if !found || strings.ToLower(key) != winner.key {
				return "", fmt.Errorf("parent registry %v %s, want winner %s", found, key, winner.key)
			}
			return fmt.Sprintf("winner=%s loser=%s (parent registry agrees); winner sends, loser refuses", winner.name, loser.name), nil
		})
	}

	// ---- E8: system-event forgery against the LIVE node (P0): a registered, non-node account must not be able to
	// register others or mint by sending a rollup system event.
	if registered {
		r.check("E8a", "forged account_registered system event from a registered user is refused (unauthorized sender)", func() (string, error) {
			payload := fmt.Sprintf(`{"kind":"account_registered","user":"%s","cluster_key":"%s"}`, u3.addr.Hex(), strings.TrimPrefix(r.exec1.key, "0x"))
			nonce, _ := r.exec1.pendingNonce(u1.addr)
			rollupSystem := common.HexToAddress("0x0000000000000000000000000000000000000072")
			tx, err := signEth(u1.key, nonce, rollupSystem, big.NewInt(0), []byte(payload))
			if err != nil {
				return "", err
			}
			_, serr := r.exec1.sendRaw(tx)
			if e := errContains(serr, "unauthorized"); e != nil {
				return "", e
			}
			st, _ := r.exec1.status(u3.addr)
			if st != nil && st.Status == "CONFIRMED" {
				return "", fmt.Errorf("u3 became CONFIRMED through a forged event")
			}
			return "refused: " + serr.Error(), nil
		})
		r.check("E8b", "forged credit events (mint) from a registered user are refused; target balance stays 0", func() (string, error) {
			loot := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
			ev := fmt.Sprintf(`{"event":{"Type":"CreditObserved","Role":1,"Target":"%s","Value":1000000000000000000000000,"IsDestinationValid":true},"msg_id":"0x%064x","source_seq":1}`, loot.Hex(), 777)
			rollupSystem := common.HexToAddress("0x0000000000000000000000000000000000000072")
			nonce, _ := r.exec1.pendingNonce(u1.addr)
			tx, _ := signEth(u1.key, nonce, rollupSystem, big.NewInt(0), []byte(ev))
			_, serr := r.exec1.sendRaw(tx)
			if e := errContains(serr, "unauthorized"); e != nil {
				return "", e
			}
			b, _ := r.exec1.balance(loot)
			if b.Sign() != 0 {
				return "", fmt.Errorf("loot balance %s", b)
			}
			return "refused; loot balance 0", nil
		})
	}

	// ---- E9: health: no fork / panic indications anywhere
	r.check("E9", "health: parent reports no fork; no FORK/PANIC/DIVERGE in any node log", func() (string, error) {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/status", r.env.Ports.ParentHTTP))
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		var st struct {
			ForkDetected bool   `json:"fork_detected"`
			LastBlock    uint64 `json:"last_block"`
		}
		json.NewDecoder(resp.Body).Decode(&st)
		if st.ForkDetected {
			return "", fmt.Errorf("parent reports fork_detected")
		}
		logs := map[string][]byte{}
		for _, name := range []string{"parent", "exec1", "exec2"} {
			logs[name+" stdout"], _ = os.ReadFile(r.env.Base + "/logs/" + name + ".log")
		}
		for _, c := range []*cluster{r.exec1, r.exec2} {
			if p := c.execLog(); p != "" {
				logs[c.name+" execution.log"], _ = os.ReadFile(p)
			}
		}
		for name, data := range logs {
			for _, bad := range []string{"fork_guard", "DIVERGE", "FORK DETECTED", "panic:", "fatal error"} {
				if i := bytes.Index(data, []byte(bad)); i >= 0 {
					end := i + 160
					if end > len(data) {
						end = len(data)
					}
					return "", fmt.Errorf("%s contains %q: %s", name, bad, strings.ReplaceAll(string(data[i:end]), "\n", " "))
				}
			}
		}
		return fmt.Sprintf("parent last_block=%d fork_detected=false; logs clean", st.LastBlock), nil
	})

	// ---- summary
	pass, fail, skip := 0, 0, 0
	for _, s := range r.steps {
		switch s.Status {
		case "PASS":
			pass++
		case "FAIL":
			fail++
		default:
			skip++
		}
	}
	fmt.Printf("\n== RESULT: %d passed, %d failed, %d skipped\n", pass, fail, skip)
	if *report != "" {
		writeReport(*report, r, pass, fail, skip)
	}
	if fail > 0 {
		os.Exit(1)
	}
}

func writeReport(path string, r *runner, pass, fail, skip int) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Account gate E2E report\n\nRun: %s  \nexec1 gate=%v, exec2 gate=%v  \nResult: **%d passed, %d failed, %d skipped**\n\n",
		time.Now().UTC().Format(time.RFC3339), r.exec1.gateOn, r.exec2.gateOn, pass, fail, skip)
	b.WriteString("| ID | Step | Status | Took | Evidence (real output) |\n| :-- | :-- | :-: | :-: | :-- |\n")
	for _, s := range r.steps {
		d := strings.ReplaceAll(s.Detail, "|", "\\|")
		fmt.Fprintf(&b, "| %s | %s | %s | %v | %s |\n", s.ID, s.Name, s.Status, s.Took.Round(time.Millisecond), d)
	}
	os.WriteFile(path, []byte(b.String()), 0o644)
}
