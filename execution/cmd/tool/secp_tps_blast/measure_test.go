package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

var testTarget = common.HexToAddress("0x000000000000000000000000000000000000dEaD")

// ---- pure computation ------------------------------------------------------------------------------------------------

func TestComputeMeasurement_CountsAndWindows(t *testing.T) {
	t0 := time.Unix(1000, 0)
	in := measureInput{
		BytesSent:    1000,
		TInjectStart: t0,
		TInjectEnd:   t0.Add(2 * time.Second),
		Obs: []blockObs{
			{Number: 1, TotalTxs: 300, ObservedAt: t0.Add(500 * time.Millisecond)},
			{Number: 2, TotalTxs: 300, ObservedAt: t0.Add(1500 * time.Millisecond)},
			{Number: 3, TotalTxs: 100, ObservedAt: t0.Add(3 * time.Second)}, // after injection ended: drain
		},
		OursPerBlock:  map[uint64]int{1: 300, 2: 300, 3: 100},
		TotalPerBlock: map[uint64]int{1: 300, 2: 300, 3: 100},
	}
	m := computeMeasurement(in)
	if m.CommittedOurs != 700 || m.LossVsSent != 300 {
		t.Fatalf("committed=%d loss=%d", m.CommittedOurs, m.LossVsSent)
	}
	if m.OursByInjectEnd != 600 || m.BacklogAtInjectEnd != 400 {
		t.Fatalf("by inject end=%d backlog=%d", m.OursByInjectEnd, m.BacklogAtInjectEnd)
	}
	if got := m.TPSDuringInjection; got < 299.9 || got > 300.1 { // 600 / 2s
		t.Fatalf("tps during injection=%v", got)
	}
	if got := m.TPSEndToEnd; got < 233.2 || got > 233.5 { // 700 / 3s
		t.Fatalf("tps end to end=%v", got)
	}
	if m.DrainSeconds < 0.99 || m.DrainSeconds > 1.01 {
		t.Fatalf("drain=%v", m.DrainSeconds)
	}
}

// The old sustained formula divided everything committed through the drain window by the injection time only.
// Same facts as above: it would report 700/2s = 350 tx/s although only 300 tx/s were committed while injecting
// and the real end-to-end rate is 233 tx/s. This test pins that the new numbers differ from the old formula.
func TestLegacySustainedFormulaOverstates(t *testing.T) {
	legacy := 700.0 / 2.0
	t0 := time.Unix(1000, 0)
	m := computeMeasurement(measureInput{
		BytesSent: 1000, TInjectStart: t0, TInjectEnd: t0.Add(2 * time.Second),
		Obs:          []blockObs{{1, 300, t0.Add(500 * time.Millisecond)}, {2, 300, t0.Add(1500 * time.Millisecond)}, {3, 100, t0.Add(3 * time.Second)}},
		OursPerBlock: map[uint64]int{1: 300, 2: 300, 3: 100}, TotalPerBlock: map[uint64]int{1: 300, 2: 300, 3: 100},
	})
	if !(legacy > m.TPSEndToEnd*1.4 && legacy > m.TPSDuringInjection) {
		t.Fatalf("expected the legacy formula (%v) to overstate end-to-end (%v) and during-injection (%v)", legacy, m.TPSEndToEnd, m.TPSDuringInjection)
	}
}

func TestParseIngress(t *testing.T) {
	txt := "# HELP x\nmaster_raw_eth_txs_received_total 1234\nmaster_raw_eth_txs_rejected_total{code=\"72\"} 3\nmaster_raw_eth_txs_rejected_total{code=\"77\"} 2\nother 9\n"
	c, err := parseIngress(strings.NewReader(txt))
	if err != nil || c.Received != 1234 || c.Rejected != 5 {
		t.Fatalf("got %+v err=%v", c, err)
	}
	if _, err := parseIngress(strings.NewReader("nothing 1\n")); err == nil {
		t.Fatal("must report absence of the received counter")
	}
}

// ---- fake chain served over JSON-RPC ---------------------------------------------------------------------------------

type fakeTx struct{ From, To common.Address }

type fakeChain struct {
	mu       sync.Mutex
	blocks   [][]fakeTx      // index i => block number i+1 (genesis #0 is empty)
	hashTag  string          // distinguishes a forked validator
	flaky    int32           // the first N eth_getBlockTransactionCountByNumber calls fail with an RPC error
	failFull map[uint64]bool // eth_getBlockByNumber(fullTx=true) fails for these blocks (models a timeout on a huge block)
}

func (c *fakeChain) head(lag int) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	h := len(c.blocks) - lag
	if h < 0 {
		h = 0
	}
	return uint64(h)
}

func (c *fakeChain) serve(lag int, tag string, metricsReceived float64) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			fmt.Fprintf(w, "master_raw_eth_txs_received_total %v\nmaster_raw_eth_txs_rejected_total{code=\"1\"} 0\n", metricsReceived)
			return
		}
		var req rpcReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := func(v interface{}) {
			b, _ := json.Marshal(v)
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, b)
		}
		blockN := func() (uint64, bool) {
			s, _ := req.Params[0].(string)
			n, err := hexToUint(s)
			return n, err == nil
		}
		switch req.Method {
		case "eth_blockNumber":
			reply(fmt.Sprintf("0x%x", c.head(lag)))
		case "eth_getBlockTransactionCountByNumber":
			if atomic.AddInt32(&c.flaky, -1) >= 0 {
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"transient"}}`)
				return
			}
			n, _ := blockN()
			c.mu.Lock()
			defer c.mu.Unlock()
			if n == 0 || int(n) > len(c.blocks)-lag {
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":null}`)
				return
			}
			reply(fmt.Sprintf("0x%x", len(c.blocks[n-1])))
		case "eth_getBlockByNumber":
			n, _ := blockN()
			full, _ := req.Params[1].(bool)
			c.mu.Lock()
			defer c.mu.Unlock()
			if full && c.failFull[n] {
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"timeout"}}`)
				return
			}
			if int(n) > len(c.blocks)-lag {
				fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":null}`)
				return
			}
			blk := map[string]interface{}{"hash": fmt.Sprintf("0xhash%d%s", n, tag), "stateRoot": fmt.Sprintf("0xroot%d%s", n, tag)}
			var txs []interface{}
			if n >= 1 {
				for _, tx := range c.blocks[n-1] {
					if full {
						txs = append(txs, map[string]string{"from": tx.From.Hex(), "to": tx.To.Hex()})
					} else {
						txs = append(txs, "0xtx")
					}
				}
			}
			blk["transactions"] = txs
			reply(blk)
		default:
			fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":null}`)
		}
	}))
}

func makeWallets(n int) ([]common.Address, map[common.Address]struct{}) {
	var list []common.Address
	set := map[common.Address]struct{}{}
	for i := 0; i < n; i++ {
		a := common.HexToAddress("0xabcdef0000000000000000000000000000000000")
		a[19] = byte(i + 1)
		a[18] = byte((i + 1) >> 8)
		list = append(list, a)
		set[a] = struct{}{}
	}
	return list, set
}

// commitTxs appends blocks of `per` txs every `every`, committing only the txs for which keep(i) is true, and
// optionally adds a foreign (non-wallet) tx to every block.
func (c *fakeChain) commitTxs(wallets []common.Address, total, per int, every time.Duration, keep func(i int) bool, foreign bool) {
	i := 0
	for i < total {
		var blk []fakeTx
		for j := 0; j < per && i < total; j++ {
			if keep(i) {
				blk = append(blk, fakeTx{From: wallets[i%len(wallets)], To: testTarget})
			}
			i++
		}
		if foreign {
			// A stranger sending to OUR target address, and one of OUR wallets sending elsewhere: neither may be counted as ours.
			blk = append(blk, fakeTx{From: common.HexToAddress("0xf00dd00dcafe"), To: testTarget})
			blk = append(blk, fakeTx{From: wallets[0], To: common.HexToAddress("0x02")})
		}
		c.mu.Lock()
		c.blocks = append(c.blocks, blk)
		c.mu.Unlock()
		time.Sleep(every)
	}
}

func testCfg(urls []string) Config {
	return Config{RPCUrls: urls, QuietSec: 1, MaxWaitSec: 10, ParityWaitSec: 1, PollMs: 5}
}

func runScenario(t *testing.T, total int, keep func(i int) bool, lag []int, tags []string) ChainMeasurement {
	return runScenarioFlaky(t, total, keep, lag, tags, 0)
}

func runScenarioFlaky(t *testing.T, total int, keep func(i int) bool, lag []int, tags []string, flaky int32) ChainMeasurement {
	return runScenarioFull(t, total, keep, lag, tags, flaky, nil)
}

func runScenarioFull(t *testing.T, total int, keep func(i int) bool, lag []int, tags []string, flaky int32, failFull map[uint64]bool) ChainMeasurement {
	t.Helper()
	wlist, wset := makeWallets(50)
	chain := &fakeChain{flaky: flaky, failFull: failFull}
	var urls []string
	for i := range lag {
		srv := chain.serve(lag[i], tags[i], float64(total))
		t.Cleanup(srv.Close)
		urls = append(urls, srv.URL)
	}
	cfg := testCfg(urls)
	before := scrapeAll(urls)
	w := newChainWatcher(urls, 0, 5*time.Millisecond)
	w.start()
	t0 := time.Now()
	go chain.commitTxs(wlist, total, 100, 15*time.Millisecond, keep, true)
	time.Sleep(30 * time.Millisecond) // "injection" window
	tEnd := time.Now()
	m, _ := finalizeMeasurement(cfg, w, 0, measureInput{BytesSent: total, TInjectStart: t0, TInjectEnd: tEnd}, wset, testTarget, before, true)
	return m
}

func TestFakeChain_AllCommitted(t *testing.T) {
	m := runScenario(t, 1000, func(int) bool { return true }, []int{0, 0, 0}, []string{"", "", ""})
	if m.CommittedOurs != 1000 || m.LossVsSent != 0 {
		t.Fatalf("committed=%d loss=%d (%s)", m.CommittedOurs, m.LossVsSent, m.StopReason)
	}
	if m.CommittedTotalTxs <= m.CommittedOurs {
		t.Fatalf("foreign txs must be counted in total but not in ours: total=%d ours=%d", m.CommittedTotalTxs, m.CommittedOurs)
	}
	if !strings.HasPrefix(m.ParityStatus, "OK") || !m.IngressMetricsOK {
		t.Fatalf("parity=%q ingress ok=%v", m.ParityStatus, m.IngressMetricsOK)
	}
}

// NEGATIVE CONTROL for the old method: 10% of our txs are dropped by the chain. The sample-extrapolation formula
// only looks at hashes that DID commit (they all confirm), so it reports zero loss; the counted measurement must see it.
func TestFakeChain_TenPercentDropped_CountedLossVsLegacyBlindness(t *testing.T) {
	m := runScenario(t, 1000, func(i int) bool { return i%10 != 0 }, []int{0, 0, 0}, []string{"", "", ""})
	if m.CommittedOurs != 900 || m.LossVsSent != 100 {
		t.Fatalf("committed=%d loss=%d", m.CommittedOurs, m.LossVsSent)
	}
	if m.LossPctVsSent < 9.9 || m.LossPctVsSent > 10.1 {
		t.Fatalf("loss pct=%v", m.LossPctVsSent)
	}
	// Old formula: sampleConfirmed/sampleTotal = 1 (sampled txs are among the committed ones) => extrapolated == submitted.
	legacyConfirmed := func(injected, sampleTotal, sampleConfirmed int) int {
		est := int(float64(sampleConfirmed) * float64(injected) / float64(sampleTotal))
		if est > injected {
			est = injected
		}
		return est
	}
	if legacyConfirmed(1000, 50, 50) != 1000 {
		t.Fatal("test premise broken: legacy formula should report no loss")
	}
}

func TestFakeChain_NothingCommitted(t *testing.T) {
	m := runScenario(t, 300, func(int) bool { return false }, []int{0, 0, 0}, []string{"", "", ""})
	if m.CommittedOurs != 0 || m.LossVsSent != 300 || m.TPSEndToEnd != 0 {
		t.Fatalf("committed=%d loss=%d tps=%v", m.CommittedOurs, m.LossVsSent, m.TPSEndToEnd)
	}
}

func TestFakeChain_LaggingValidatorIsReportedNotSkipped(t *testing.T) {
	m := runScenario(t, 500, func(int) bool { return true }, []int{0, 0, 3}, []string{"", "", ""})
	if !strings.HasPrefix(m.ParityStatus, "INCOMPLETE") || !strings.Contains(m.ParityStatus, "<") {
		t.Fatalf("a lagging validator must be named with its height (\"at #h < #E\"), got %q", m.ParityStatus)
	}
}

func TestFakeChain_ForkedValidatorDetected(t *testing.T) {
	m := runScenario(t, 500, func(int) bool { return true }, []int{0, 0, 0}, []string{"", "", "FORK"})
	if !strings.HasPrefix(m.ParityStatus, "FORK") {
		t.Fatalf("expected FORK, got %q", m.ParityStatus)
	}
}

// A transient RPC failure while reading a block's tx count must never make the watcher skip that block.
func TestFakeChain_TransientCountErrorsDoNotLoseBlocks(t *testing.T) {
	m := runScenarioFlaky(t, 1000, func(int) bool { return true }, []int{0, 0, 0}, []string{"", "", ""}, 8)
	if m.CommittedOurs != 1000 || m.LossVsSent != 0 {
		t.Fatalf("committed=%d loss=%d", m.CommittedOurs, m.LossVsSent)
	}
}

// A block whose transactions cannot be fetched (huge block / timeout) must make committed_ours an explicit LOWER bound with an
// upper bound that covers the whole block, never a silent undercount reported as loss.
func TestFakeChain_UnfetchedBlockGivesBounds(t *testing.T) {
	m := runScenarioFull(t, 1000, func(int) bool { return true }, []int{0, 0, 0}, []string{"", "", ""}, 0, map[uint64]bool{2: true})
	if m.OursExact || len(m.UnfetchedBlocks) != 1 || m.UnfetchedBlocks[0] != 2 {
		t.Fatalf("exact=%v unfetched=%v", m.OursExact, m.UnfetchedBlocks)
	}
	if m.CommittedOurs >= 1000 || m.OursUpperBound < 1000 {
		t.Fatalf("lower=%d upper=%d (true ours = 1000)", m.CommittedOurs, m.OursUpperBound)
	}
	if m.TPSEndToEndUpper <= m.TPSEndToEnd {
		t.Fatalf("upper-bound tps %v must exceed lower-bound tps %v", m.TPSEndToEndUpper, m.TPSEndToEnd)
	}
	if m.CommittedTotalTxs <= 1000 {
		t.Fatalf("live total must still be exact: %d", m.CommittedTotalTxs)
	}
}

// A chain can pause longer than the quiet window while validators still hold received-but-uncommitted txs
// (seen live: a pause > 8 s, then 19k more txs committed). The quiet rule must not end the measurement then.
func TestWaitQuiesce_DoesNotStopWhileReceivedTxsArePending(t *testing.T) {
	newW := func() *chainWatcher {
		w := &chainWatcher{}
		w.totalSeen, w.lastTxBlk = 500, time.Now()
		go func() { // the chain resumes after a pause longer than quiet
			time.Sleep(2500 * time.Millisecond)
			w.mu.Lock()
			w.totalSeen, w.lastTxBlk = 1000, time.Now()
			w.mu.Unlock()
		}()
		return w
	}
	pending := func(w *chainWatcher) func() (int, bool) {
		return func() (int, bool) { return 1000, true } // validators received 1000; only 500 seen in blocks so far
	}
	w := newW()
	reason := w.waitQuiesce(1000, 100*time.Millisecond, 300*time.Millisecond, 20*time.Second, pending(w))
	if !strings.Contains(reason, "1000 >= 1000") {
		t.Fatalf("stopped too early: %s", reason)
	}
	// Negative control: without received info the plain quiet rule fires during the pause.
	w2 := newW()
	reason = w2.waitQuiesce(1000, 100*time.Millisecond, 300*time.Millisecond, 20*time.Second, nil)
	if !strings.Contains(reason, "no new tx-bearing block") {
		t.Fatalf("expected the quiet rule without received info, got: %s", reason)
	}
}
