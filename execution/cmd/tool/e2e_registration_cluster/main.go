// e2e_registration_cluster drives the end-to-end test of account registration (provisional activation, Parent Chain
// sync, batching, cross-cluster race, rate limit, parent outage) against a RUNNING deployment, e.g. the ansible devnet
// of deploy/ansible_clusters/inventory.yml (defaults below). Every step talks to the real nodes over JSON-RPC and to the
// Parent Chain HTTP API; nothing is mocked. Every number in the report is measured in this run.
//
//	go run ./cmd/tool/e2e_registration_cluster -report /tmp/reg_e2e.md
//
// Disruptive steps are opt-in:
//
//	-rate-check            hammer mtn_registerAccount on exec2 until the per-IP limit answers (blocks this IP ~1 min)
//	-parent-stop-cmd/-parent-start-cmd   shell commands that stop/start ALL Parent Chain nodes (outage scenario)
package main

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

type rpcNode struct {
	name string
	url  string
	key  string // cluster BLS public key (hex), from mtn_getClusterIdentity
}

type regInfo struct {
	Status      string `json:"status"`
	HomeCluster string `json:"homeCluster"`
	Reason      string `json:"reason"`
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

func (n *rpcNode) call(result interface{}, method string, params ...interface{}) error {
	if params == nil {
		params = []interface{}{}
	}
	body, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	resp, err := httpClient.Post(n.url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("%s: bad response %q", method, string(raw))
	}
	if out.Error != nil {
		return errors.New(out.Error.Message)
	}
	if result != nil {
		return json.Unmarshal(out.Result, result)
	}
	return nil
}

type user struct {
	key  *ecdsa.PrivateKey
	addr common.Address
}

func newUser() user {
	k, _ := crypto.GenerateKey()
	return user{key: k, addr: crypto.PubkeyToAddress(k.PublicKey)}
}

// register does what a wallet does: fetch the message, sign the hash, call mtn_registerAccount.
func (n *rpcNode) register(u user) (*regInfo, error) {
	var msg struct {
		HashToSign string `json:"hashToSign"`
	}
	if err := n.call(&msg, "mtn_getRegistrationMessage", u.addr); err != nil {
		return nil, err
	}
	sig, err := crypto.Sign(common.FromHex(msg.HashToSign), u.key)
	if err != nil {
		return nil, err
	}
	var info regInfo
	return &info, n.call(&info, "mtn_registerAccount", u.addr, hexutil.Bytes(sig))
}

func (n *rpcNode) status(a common.Address) (*regInfo, error) {
	var info regInfo
	return &info, n.call(&info, "mtn_getRegistrationStatus", a)
}

// waitStatus polls until the status is one of want, returning it and how long it took.
func (n *rpcNode) waitStatus(a common.Address, timeout time.Duration, want ...string) (*regInfo, time.Duration, error) {
	start := time.Now()
	var last *regInfo
	for time.Since(start) < timeout {
		info, err := n.status(a)
		if err == nil {
			last = info
			for _, w := range want {
				if info.Status == w {
					return info, time.Since(start), nil
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	if last == nil {
		return nil, time.Since(start), fmt.Errorf("no status within %s", timeout)
	}
	return last, time.Since(start), fmt.Errorf("status %s (reason %q) after %s, want %v", last.Status, last.Reason, timeout, want)
}

func parentNonce(parentURL, blsPubHex string) (uint64, error) {
	addr := common.BytesToAddress(crypto.Keccak256(common.FromHex(blsPubHex))[12:])
	resp, err := httpClient.Get(fmt.Sprintf("%s/nonce?address=%s", parentURL, addr.Hex()))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Nonce uint64 `json:"nonce"`
	}
	return out.Nonce, json.NewDecoder(resp.Body).Decode(&out)
}

type step struct{ ID, Name, Status, Detail string }

type runner struct{ steps []step }

func (r *runner) run(id, name string, f func() (string, error)) bool {
	fmt.Printf("▶ %s %s ...\n", id, name)
	detail, err := f()
	st := "PASS"
	if err != nil {
		st, detail = "FAIL", strings.TrimSpace(detail+" "+err.Error())
	}
	fmt.Printf("  %s %s\n", st, detail)
	r.steps = append(r.steps, step{id, name, st, detail})
	return err == nil
}

func (r *runner) skip(id, name, why string) {
	fmt.Printf("▶ %s %s ... SKIP (%s)\n", id, name, why)
	r.steps = append(r.steps, step{id, name, "SKIP", why})
}

func shell(cmd string) error {
	c := exec.Command("bash", "-c", cmd)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	return c.Run()
}

func durStats(ds []time.Duration) string {
	if len(ds) == 0 {
		return "n/a"
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return fmt.Sprintf("min=%s p50=%s max=%s (n=%d)", ds[0].Round(time.Millisecond), ds[len(ds)/2].Round(time.Millisecond),
		ds[len(ds)-1].Round(time.Millisecond), len(ds))
}

func main() {
	exec1URL := flag.String("exec1", "http://127.0.0.1:8747", "JSON-RPC of a cluster-1 replica")
	exec2URL := flag.String("exec2", "http://127.0.0.1:8750", "JSON-RPC of a cluster-2 replica")
	parentURL := flag.String("parent", "http://127.0.0.1:18601", "Parent Chain HTTP API")
	nUsers := flag.Int("users", 10, "users registered concurrently in the batch step")
	timeout := flag.Duration("timeout", 3*time.Minute, "max wait for each status transition")
	rateCheck := flag.Bool("rate-check", false, "verify the per-IP registration limit on exec2 (blocks this IP for ~1 minute)")
	rateMax := flag.Int("rate-max", 200, "max calls made by -rate-check before giving up")
	stopCmd := flag.String("parent-stop-cmd", "", "shell command stopping ALL Parent Chain nodes (enables the outage step)")
	startCmd := flag.String("parent-start-cmd", "", "shell command starting them again")
	report := flag.String("report", "", "write a markdown report here")
	flag.Parse()

	exec1 := &rpcNode{name: "exec1", url: *exec1URL}
	exec2 := &rpcNode{name: "exec2", url: *exec2URL}
	r := &runner{}
	started := time.Now()

	// S1: both clusters answer, gate on, distinct cluster keys.
	ok := r.run("S1", "cluster identity", func() (string, error) {
		for _, n := range []*rpcNode{exec1, exec2} {
			var id struct {
				ClusterKey  string `json:"clusterKey"`
				AccountGate bool   `json:"accountGate"`
			}
			if err := n.call(&id, "mtn_getClusterIdentity"); err != nil {
				return n.name, err
			}
			if !id.AccountGate {
				return n.name, errors.New("account gate is off")
			}
			n.key = id.ClusterKey
		}
		if exec1.key == exec2.key {
			return "", errors.New("both endpoints report the same cluster key")
		}
		return fmt.Sprintf("exec1=%s… exec2=%s…", exec1.key[:18], exec2.key[:18]), nil
	})
	if !ok {
		finish(r, *report, started)
		return
	}

	// S2: N concurrent registrations. Measures time-to-usable (PROVISIONAL or CONFIRMED) and time-to-CONFIRMED, and
	// how many Parent Chain transactions the cluster spent (nonce delta: < N means batching is active).
	r.run("S2", fmt.Sprintf("%d concurrent registrations on exec1", *nUsers), func() (string, error) {
		nonceBefore, nerr := parentNonce(*parentURL, exec1.key)
		users := make([]user, *nUsers)
		usable := make([]time.Duration, *nUsers)
		confirmed := make([]time.Duration, *nUsers)
		errs := make([]error, *nUsers)
		var wg sync.WaitGroup
		for i := range users {
			users[i] = newUser()
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				t0 := time.Now()
				if _, err := exec1.register(users[i]); err != nil {
					errs[i] = err
					return
				}
				if _, _, err := exec1.waitStatus(users[i].addr, *timeout, "PROVISIONAL", "CONFIRMED"); err != nil {
					errs[i] = err
					return
				}
				usable[i] = time.Since(t0)
				if _, _, err := exec1.waitStatus(users[i].addr, *timeout, "CONFIRMED"); err != nil {
					errs[i] = err
					return
				}
				confirmed[i] = time.Since(t0)
			}(i)
		}
		wg.Wait()
		failed := 0
		var firstErr error
		for _, e := range errs {
			if e != nil {
				failed++
				if firstErr == nil {
					firstErr = e
				}
			}
		}
		parentTxs := "n/a"
		if nonceAfter, err := parentNonce(*parentURL, exec1.key); err == nil && nerr == nil {
			parentTxs = fmt.Sprintf("%d (nonce %d→%d)", nonceAfter-nonceBefore, nonceBefore, nonceAfter)
		}
		detail := fmt.Sprintf("usable: %s; confirmed: %s; parent txs by exec1: %s", durStats(nonZero(usable)), durStats(nonZero(confirmed)), parentTxs)
		if failed > 0 {
			return detail, fmt.Errorf("%d/%d users failed, first: %v", failed, *nUsers, firstErr)
		}
		return detail, nil
	})

	// S3: the same address registered on both clusters at once: exactly one wins, the other reports REJECTED with the
	// winner's key.
	r.run("S3", "same address on exec1 and exec2 concurrently", func() (string, error) {
		u := newUser()
		var wg sync.WaitGroup
		var e1, e2 error
		wg.Add(2)
		go func() { defer wg.Done(); _, e1 = exec1.register(u) }()
		go func() { defer wg.Done(); _, e2 = exec2.register(u) }()
		wg.Wait()
		if e1 != nil || e2 != nil {
			return "", fmt.Errorf("register: exec1=%v exec2=%v", e1, e2)
		}
		s1, _, err1 := exec1.waitStatus(u.addr, *timeout, "CONFIRMED", "REJECTED")
		s2, _, err2 := exec2.waitStatus(u.addr, *timeout, "CONFIRMED", "REJECTED")
		if err1 != nil || err2 != nil {
			return "", fmt.Errorf("exec1=%v exec2=%v", err1, err2)
		}
		detail := fmt.Sprintf("exec1=%s exec2=%s", s1.Status, s2.Status)
		switch {
		case s1.Status == "CONFIRMED" && s2.Status == "REJECTED":
			if !strings.EqualFold(s2.HomeCluster, exec1.key) {
				return detail, fmt.Errorf("exec2 names home %s, want exec1", s2.HomeCluster)
			}
		case s2.Status == "CONFIRMED" && s1.Status == "REJECTED":
			if !strings.EqualFold(s1.HomeCluster, exec2.key) {
				return detail, fmt.Errorf("exec1 names home %s, want exec2", s1.HomeCluster)
			}
		default:
			return detail, errors.New("want exactly one CONFIRMED and one REJECTED")
		}
		return detail, nil
	})

	// S4: Parent Chain outage: registration is usable (PROVISIONAL) while the parent is down and is CONFIRMED after it
	// returns, without any user action.
	if *stopCmd != "" && *startCmd != "" {
		r.run("S4", "parent outage → provisional → recovery", func() (string, error) {
			if err := shell(*stopCmd); err != nil {
				return "", fmt.Errorf("parent stop command: %w", err)
			}
			restarted := false
			defer func() {
				if !restarted {
					_ = shell(*startCmd)
				}
			}()
			u := newUser()
			t0 := time.Now()
			if _, err := exec1.register(u); err != nil {
				return "", err
			}
			st, took, err := exec1.waitStatus(u.addr, *timeout, "PROVISIONAL")
			if err != nil {
				return "", fmt.Errorf("while parent is down: %w", err)
			}
			usable := took
			if err := shell(*startCmd); err != nil {
				return "", fmt.Errorf("parent start command: %w", err)
			}
			restarted = true
			if _, _, err := exec1.waitStatus(u.addr, *timeout, "CONFIRMED"); err != nil {
				return "", fmt.Errorf("after parent restart: %w", err)
			}
			return fmt.Sprintf("usable during outage after %s (%s); confirmed %s after registering", usable.Round(time.Millisecond),
				st.Status, time.Since(t0).Round(time.Millisecond)), nil
		})
	} else {
		r.skip("S4", "parent outage → provisional → recovery", "pass -parent-stop-cmd and -parent-start-cmd to run it")
	}

	// S5: per-IP registration limit (last: it blocks this IP on exec2 for about a minute).
	if *rateCheck {
		r.run("S5", "per-IP registration rate limit on exec2", func() (string, error) {
			for i := 1; i <= *rateMax; i++ {
				_, err := exec2.register(newUser())
				if err != nil && strings.Contains(err.Error(), "too many registration requests") {
					return fmt.Sprintf("limited after %d accepted calls", i-1), nil
				}
				if err != nil {
					return "", fmt.Errorf("call %d: %w", i, err)
				}
			}
			return "", fmt.Errorf("no limit after %d calls (is REGISTRATION_RATE_PER_MIN 0, or the new build not deployed?)", *rateMax)
		})
	} else {
		r.skip("S5", "per-IP registration rate limit on exec2", "pass -rate-check to run it")
	}

	finish(r, *report, started)
}

func nonZero(ds []time.Duration) []time.Duration {
	var out []time.Duration
	for _, d := range ds {
		if d > 0 {
			out = append(out, d)
		}
	}
	return out
}

func finish(r *runner, report string, started time.Time) {
	pass, fail, skip := 0, 0, 0
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Registration E2E report\n\n- Run at: %s\n- Duration: %s\n\n| Step | Name | Result | Detail |\n|---|---|---|---|\n",
		started.Format(time.RFC3339), time.Since(started).Round(time.Second))
	for _, s := range r.steps {
		switch s.Status {
		case "PASS":
			pass++
		case "FAIL":
			fail++
		default:
			skip++
		}
		fmt.Fprintf(&sb, "| %s | %s | %s | %s |\n", s.ID, s.Name, s.Status, strings.ReplaceAll(s.Detail, "|", "/"))
	}
	fmt.Fprintf(&sb, "\nPASS=%d FAIL=%d SKIP=%d\n", pass, fail, skip)
	fmt.Print("\n" + sb.String())
	if report != "" {
		if err := os.WriteFile(report, []byte(sb.String()), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
		}
	}
	if fail > 0 {
		os.Exit(1)
	}
}
