package main

// measure.go — chain-based throughput measurement.
//
// Why this exists: the old tool derived "confirmed" by extrapolating a 50-hash receipt sample, treated "bytes written
// to the TCP socket" as "submitted", divided the burst count by the time AFTER injection, and (sustained mode) divided
// everything committed during injection AND the drain window by the injection time only. Nothing here extrapolates:
// every number is a count of transactions found in blocks, with the wall-clock instant the block was first observed.
//
// Nothing in this file influences the system under test; the watcher only reads (eth_blockNumber,
// eth_getBlockTransactionCountByNumber, /metrics). The quiescence rules below only decide when the MEASUREMENT stops.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// blockObs is one block as seen by the reference validator.
type blockObs struct {
	Number     uint64
	TotalTxs   int
	ObservedAt time.Time
}

// timelineEntry is the raw evidence a third party can use to recompute every throughput number.
type timelineEntry struct {
	Block        uint64  `json:"block"`
	Ours         int     `json:"ours"`
	Total        int     `json:"total"`
	MsSinceStart float64 `json:"ms_since_inject_start"`
}

// ChainMeasurement is the counted (not estimated) outcome of one injection run.
type ChainMeasurement struct {
	Method string `json:"method"`

	BytesSentTxs      int      `json:"bytes_sent_txs"`             // txs written to socket / accepted by eth_sendRawTransaction
	CommittedOurs     int      `json:"committed_ours"`             // txs in blocks (to == target, from in our wallet set)
	CommittedTotalTxs int      `json:"committed_total_txs"`        // every tx in the same blocks (live count, exact)
	OursExact         bool     `json:"committed_ours_exact"`       // false when some block's transactions could not be fetched
	OursUpperBound    int      `json:"committed_ours_upper_bound"` // committed_ours + txs of unfetched blocks
	UnfetchedBlocks   []uint64 `json:"unfetched_blocks,omitempty"`
	LossVsSent        int      `json:"loss_vs_sent"` // BytesSentTxs - CommittedOurs (may be negative => investigate)
	LossPctVsSent     float64  `json:"loss_pct_vs_sent"`

	InjectSeconds   float64 `json:"inject_seconds"`
	DrainSeconds    float64 `json:"drain_seconds"`      // last our-block observed - inject end (>= 0)
	EndToEndSeconds float64 `json:"end_to_end_seconds"` // last our-block observed - inject start

	OursByInjectEnd    int     `json:"ours_committed_by_inject_end"`
	BacklogAtInjectEnd int     `json:"backlog_at_inject_end"` // BytesSentTxs - OursByInjectEnd
	TPSDuringInjection float64 `json:"tps_during_injection"`  // OursByInjectEnd / InjectSeconds
	TPSEndToEnd        float64 `json:"tps_end_to_end"`        // CommittedOurs / EndToEndSeconds (lower bound when !committed_ours_exact)
	TPSEndToEndUpper   float64 `json:"tps_end_to_end_upper_bound"`
	TPSActiveCommit    float64 `json:"tps_active_commit_window"` // CommittedOurs / (last - first our-block observed)
	FirstCommitSeconds float64 `json:"first_commit_seconds_after_inject_start"`

	ReceivedByValidator map[string]float64 `json:"received_by_validator,omitempty"` // delta of master_raw_eth_txs_received_total
	RejectedByValidator map[string]float64 `json:"rejected_by_validator,omitempty"`
	IngressMetricsOK    bool               `json:"ingress_metrics_ok"`

	ValidatorHeads map[string]uint64 `json:"validator_heads"`
	ParityStatus   string            `json:"parity_status"`
	StopReason     string            `json:"measurement_stop_reason"`

	Timeline []timelineEntry `json:"commit_timeline,omitempty"`
}

// ---------------------------------------------------------------------------------------------------------------------
// Pure computation (unit-tested with fabricated observations; no network)
// ---------------------------------------------------------------------------------------------------------------------

type measureInput struct {
	BytesSent     int
	TInjectStart  time.Time
	TInjectEnd    time.Time
	Obs           []blockObs // reference validator, ascending block numbers
	OursPerBlock  map[uint64]int
	TotalPerBlock map[uint64]int
	Unfetched     []uint64 // blocks whose transactions could not be fetched (their `ours` is unknown)
}

func computeMeasurement(in measureInput) ChainMeasurement {
	m := ChainMeasurement{
		Method:        "counted from blocks (to==target && from in wallet set); wall-clock = first observation of the block by the reference validator",
		BytesSentTxs:  in.BytesSent,
		InjectSeconds: in.TInjectEnd.Sub(in.TInjectStart).Seconds(),
	}
	var firstOurs, lastOurs time.Time
	unfetched := make(map[uint64]bool, len(in.Unfetched))
	for _, n := range in.Unfetched {
		unfetched[n] = true
	}
	m.OursExact = len(unfetched) == 0
	m.UnfetchedBlocks = in.Unfetched
	for _, o := range in.Obs {
		ours := in.OursPerBlock[o.Number]
		total := in.TotalPerBlock[o.Number]
		if total == 0 && o.TotalTxs > total {
			total = o.TotalTxs
		}
		m.CommittedOurs += ours
		m.CommittedTotalTxs += total
		if unfetched[o.Number] {
			m.OursUpperBound += total // every tx of that block might be ours; the lower bound counts none of them
		} else {
			m.OursUpperBound += ours
		}
		if ours > 0 || (unfetched[o.Number] && total > 0) {
			if firstOurs.IsZero() || o.ObservedAt.Before(firstOurs) {
				firstOurs = o.ObservedAt
			}
			if o.ObservedAt.After(lastOurs) {
				lastOurs = o.ObservedAt
			}
			if !o.ObservedAt.After(in.TInjectEnd) {
				m.OursByInjectEnd += ours
			}
		}
		if ours > 0 || total > 0 {
			m.Timeline = append(m.Timeline, timelineEntry{
				Block: o.Number, Ours: ours, Total: total,
				MsSinceStart: float64(o.ObservedAt.Sub(in.TInjectStart).Microseconds()) / 1000.0,
			})
		}
	}
	m.LossVsSent = m.BytesSentTxs - m.CommittedOurs
	if m.BytesSentTxs > 0 {
		m.LossPctVsSent = 100.0 * float64(m.LossVsSent) / float64(m.BytesSentTxs)
	}
	m.BacklogAtInjectEnd = m.BytesSentTxs - m.OursByInjectEnd
	if m.InjectSeconds > 0 {
		m.TPSDuringInjection = float64(m.OursByInjectEnd) / m.InjectSeconds
	}
	if !lastOurs.IsZero() {
		m.EndToEndSeconds = lastOurs.Sub(in.TInjectStart).Seconds()
		if d := lastOurs.Sub(in.TInjectEnd).Seconds(); d > 0 {
			m.DrainSeconds = d
		}
		if m.EndToEndSeconds > 0 {
			m.TPSEndToEnd = float64(m.CommittedOurs) / m.EndToEndSeconds
			m.TPSEndToEndUpper = float64(m.OursUpperBound) / m.EndToEndSeconds
		}
		m.FirstCommitSeconds = firstOurs.Sub(in.TInjectStart).Seconds()
		if w := lastOurs.Sub(firstOurs).Seconds(); w > 0 {
			m.TPSActiveCommit = float64(m.CommittedOurs) / w
		}
	}
	return m
}

// ---------------------------------------------------------------------------------------------------------------------
// Watcher: reads heads of every validator and the tx count of every new block of the reference validator.
// ---------------------------------------------------------------------------------------------------------------------

type chainWatcher struct {
	urls       []string
	poll       time.Duration
	mu         sync.Mutex
	obs        []blockObs
	heads      []uint64
	lastTxBlk  time.Time // last time the reference validator showed a block with >= 1 tx
	totalSeen  int
	stop       chan struct{}
	wg         sync.WaitGroup
	nextToRead uint64
}

func newChainWatcher(urls []string, startBlock uint64, poll time.Duration) *chainWatcher {
	return &chainWatcher{urls: urls, poll: poll, heads: make([]uint64, len(urls)), stop: make(chan struct{}), nextToRead: startBlock + 1}
}

func hexToUint(s string) (uint64, error) {
	return strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
}

func (w *chainWatcher) start() {
	for i := range w.urls {
		w.wg.Add(1)
		go w.loop(i)
	}
}

func (w *chainWatcher) Stop() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
	w.wg.Wait()
}

func (w *chainWatcher) loop(i int) {
	defer w.wg.Done()
	t := time.NewTicker(w.poll)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
		}
		head, err := getBlockNumber(w.urls[i])
		seenAt := time.Now()
		if err != nil {
			continue
		}
		w.mu.Lock()
		if head > w.heads[i] {
			w.heads[i] = head
		}
		w.mu.Unlock()
		if i != 0 {
			continue
		}
		for {
			w.mu.Lock()
			n := w.nextToRead
			w.mu.Unlock()
			if n > head {
				break
			}
			cnt, err := blockTxCount(w.urls[0], n)
			if err != nil {
				break // retry on the next tick; never skip a block
			}
			w.mu.Lock()
			w.obs = append(w.obs, blockObs{Number: n, TotalTxs: cnt, ObservedAt: seenAt})
			w.totalSeen += cnt
			if cnt > 0 {
				w.lastTxBlk = seenAt
			}
			w.nextToRead = n + 1
			w.mu.Unlock()
		}
	}
}

func blockTxCount(url string, n uint64) (int, error) {
	res, err := rpcCallTo(url, "eth_getBlockTransactionCountByNumber", fmt.Sprintf("0x%x", n))
	if err != nil {
		return 0, err
	}
	if string(res) == "null" {
		return 0, fmt.Errorf("block %d not available", n)
	}
	var s string
	if err := json.Unmarshal(res, &s); err != nil {
		return 0, err
	}
	v, err := hexToUint(s)
	return int(v), err
}

// waitQuiesce blocks until the measurement can stop. It only ends the MEASUREMENT.
//   - done when the reference validator has shown >= expectedTxs txs in total and nothing new arrived for settle, or
//   - done when no tx-bearing block was seen for quiet, or
//   - done at maxWait.
func (w *chainWatcher) waitQuiesce(expectedTxs int, settle, quiet, maxWait time.Duration, received func() (int, bool)) string {
	begin := time.Now()
	// The quiet rule must not fire while validators report txs they received that no block has shown yet: a chain can
	// pause for longer than quiet in the middle of draining and then continue (seen live: 19k txs committed after an
	// 8 s pause). received is polled at most every 2 s; when it is unavailable the plain quiet rule applies.
	var recvAt time.Time
	recvN, recvOK := 0, false
	for {
		time.Sleep(25 * time.Millisecond)
		now := time.Now()
		w.mu.Lock()
		total, last := w.totalSeen, w.lastTxBlk
		w.mu.Unlock()
		if now.Sub(begin) >= maxWait {
			return fmt.Sprintf("max-wait %v reached (committed_total_seen=%d expected=%d)", maxWait, total, expectedTxs)
		}
		if last.IsZero() {
			if now.Sub(begin) >= quiet {
				return fmt.Sprintf("no tx-bearing block seen within quiet window %v", quiet)
			}
			continue
		}
		if total >= expectedTxs && now.Sub(last) >= settle {
			return fmt.Sprintf("reference validator showed %d >= %d txs and was idle for %v", total, expectedTxs, settle)
		}
		if now.Sub(last) >= quiet {
			if received != nil && now.Sub(recvAt) >= 2*time.Second {
				recvN, recvOK = received()
				recvAt = now
			}
			if recvOK && recvN > total {
				continue // validators hold received txs that are not in any block yet: keep waiting (bounded by maxWait)
			}
			return fmt.Sprintf("no new tx-bearing block for %v (committed_total_seen=%d expected=%d)", quiet, total, expectedTxs)
		}
	}
}

func (w *chainWatcher) snapshot() ([]blockObs, []uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := append([]blockObs(nil), w.obs...)
	h := append([]uint64(nil), w.heads...)
	return o, h
}

// ---------------------------------------------------------------------------------------------------------------------
// Post-hoc exact count of our transactions in the observed blocks (chain is idle, full blocks are fetched once).
// ---------------------------------------------------------------------------------------------------------------------

type fullBlock struct {
	Transactions []struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"transactions"`
}

// fullBlockTimeout is generous on purpose: a block of ~30k txs is tens of MB of JSON that the node serializes on demand.
const fullBlockTimeout = 180 * time.Second

func countOursInBlocks(url string, blocks []uint64, wallets map[common.Address]struct{}, target common.Address) (ours, total map[uint64]int, unfetched []uint64, err error) {
	ours = make(map[uint64]int, len(blocks))
	total = make(map[uint64]int, len(blocks))
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, n := range blocks {
		wg.Add(1)
		sem <- struct{}{}
		go func(n uint64) {
			defer wg.Done()
			defer func() { <-sem }()
			var fb fullBlock
			var lastErr error
			for attempt := 0; attempt < 5; attempt++ {
				res, e := rpcCallToTimeout(url, fullBlockTimeout, "eth_getBlockByNumber", fmt.Sprintf("0x%x", n), true)
				if e == nil && string(res) != "null" {
					if e = json.Unmarshal(res, &fb); e == nil {
						lastErr = nil
						break
					}
				}
				lastErr = e
				time.Sleep(100 * time.Millisecond)
			}
			mu.Lock()
			defer mu.Unlock()
			if lastErr != nil {
				unfetched = append(unfetched, n)
				if firstErr == nil {
					firstErr = fmt.Errorf("block %d: %w", n, lastErr)
				}
				return
			}
			c := 0
			for _, t := range fb.Transactions {
				if common.HexToAddress(t.To) == target {
					if _, ok := wallets[common.HexToAddress(t.From)]; ok {
						c++
					}
				}
			}
			ours[n], total[n] = c, len(fb.Transactions)
		}(n)
	}
	wg.Wait()
	sort.Slice(unfetched, func(i, j int) bool { return unfetched[i] < unfetched[j] })
	return ours, total, unfetched, firstErr
}

// ---------------------------------------------------------------------------------------------------------------------
// Validator-side ingress counters (from /metrics) — "what the validators received", not "what the client wrote".
// ---------------------------------------------------------------------------------------------------------------------

type ingressCounters struct{ Received, Rejected float64 }

func scrapeIngress(rpcURL string) (ingressCounters, error) {
	var c ingressCounters
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(strings.TrimRight(rpcURL, "/") + "/metrics")
	if err != nil {
		return c, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return c, fmt.Errorf("/metrics status %d", resp.StatusCode)
	}
	return parseIngress(resp.Body)
}

func parseIngress(r io.Reader) (ingressCounters, error) {
	var c ingressCounters
	found := false
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "master_raw_eth_txs_received_total"):
			if v, ok := lastFloat(line); ok {
				c.Received += v
				found = true
			}
		case strings.HasPrefix(line, "master_raw_eth_txs_rejected_total"):
			if v, ok := lastFloat(line); ok {
				c.Rejected += v
			}
		}
	}
	if !found {
		return c, fmt.Errorf("master_raw_eth_txs_received_total not present")
	}
	return c, sc.Err()
}

func lastFloat(line string) (float64, bool) {
	f := strings.Fields(line)
	if len(f) < 2 {
		return 0, false
	}
	v, err := strconv.ParseFloat(f[len(f)-1], 64)
	return v, err == nil
}

func scrapeAll(urls []string) map[string]ingressCounters {
	out := make(map[string]ingressCounters, len(urls))
	for _, u := range urls {
		if c, err := scrapeIngress(u); err == nil {
			out[u] = c
		}
	}
	return out
}

// ---------------------------------------------------------------------------------------------------------------------
// Parity: EVERY validator must reach the reference end block; a validator that does not is reported, never skipped.
// ---------------------------------------------------------------------------------------------------------------------

func checkParity(urls []string, endBlock uint64, wait time.Duration) (status string, heads map[string]uint64, ok bool) {
	heads = make(map[string]uint64, len(urls))
	deadline := time.Now().Add(wait)
	for {
		behind := ""
		for _, u := range urls {
			h, err := getBlockNumber(u)
			if err != nil {
				behind = fmt.Sprintf("%s unreachable (%v)", u, err)
				continue
			}
			heads[u] = h
			if h < endBlock && behind == "" {
				behind = fmt.Sprintf("%s at #%d < #%d", u, h, endBlock)
			}
		}
		if behind == "" {
			break
		}
		if time.Now().After(deadline) {
			return "INCOMPLETE: " + behind, heads, false
		}
		time.Sleep(200 * time.Millisecond)
	}
	hashes := map[string][]string{}
	roots := map[string][]string{}
	for _, u := range urls {
		res, err := rpcCallTo(u, "eth_getBlockByNumber", fmt.Sprintf("0x%x", endBlock), false)
		if err != nil || string(res) == "null" {
			return fmt.Sprintf("INCOMPLETE: %s did not return block #%d", u, endBlock), heads, false
		}
		var b struct{ Hash, StateRoot string }
		if err := json.Unmarshal(res, &b); err != nil || b.Hash == "" {
			return fmt.Sprintf("INCOMPLETE: %s returned an unparsable block #%d", u, endBlock), heads, false
		}
		hashes[b.Hash] = append(hashes[b.Hash], u)
		roots[b.StateRoot] = append(roots[b.StateRoot], u)
	}
	if len(hashes) > 1 || len(roots) > 1 {
		return fmt.Sprintf("FORK at #%d: %d distinct block hashes, %d distinct state roots", endBlock, len(hashes), len(roots)), heads, false
	}
	return fmt.Sprintf("OK: all %d validators agree on hash+stateRoot of #%d", len(urls), endBlock), heads, true
}

// finalizeMeasurement runs after injection: wait for the chain to settle, count our txs per block, compute the numbers.
func finalizeMeasurement(cfg Config, w *chainWatcher, startBlock uint64, in measureInput, wallets map[common.Address]struct{}, target common.Address, before map[string]ingressCounters, verifyParity bool) (ChainMeasurement, uint64) {
	reason := w.waitQuiesce(in.BytesSent,
		1500*time.Millisecond,
		time.Duration(cfg.QuietSec)*time.Second,
		time.Duration(cfg.MaxWaitSec)*time.Second,
		func() (int, bool) {
			now := scrapeAll(cfg.RPCUrls)
			if len(now) != len(cfg.RPCUrls) || len(before) != len(cfg.RPCUrls) {
				return 0, false
			}
			sum := 0.0
			for u, c := range now {
				sum += c.Received - before[u].Received
			}
			return int(sum), true
		})
	w.Stop()
	obs, _ := w.snapshot()
	blocks := make([]uint64, 0, len(obs))
	for _, o := range obs {
		if o.TotalTxs > 0 {
			blocks = append(blocks, o.Number)
		}
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i] < blocks[j] })
	ours, total, unfetched, err := countOursInBlocks(cfg.RPCUrls[0], blocks, wallets, target)
	in.Obs, in.OursPerBlock, in.TotalPerBlock, in.Unfetched = obs, ours, total, unfetched
	m := computeMeasurement(in)
	m.StopReason = reason
	if err != nil {
		m.StopReason += fmt.Sprintf(" | WARNING: could not fetch %d block(s) (first error: %v); committed_ours is a LOWER BOUND, see committed_ours_upper_bound", len(unfetched), err)
	}

	after := scrapeAll(cfg.RPCUrls)
	m.ReceivedByValidator, m.RejectedByValidator = map[string]float64{}, map[string]float64{}
	okAll := len(before) == len(cfg.RPCUrls) && len(after) == len(cfg.RPCUrls)
	for u, a := range after {
		if b, ok := before[u]; ok {
			m.ReceivedByValidator[u] = a.Received - b.Received
			m.RejectedByValidator[u] = a.Rejected - b.Rejected
		}
	}
	m.IngressMetricsOK = okAll

	var endBlock uint64
	if n := len(obs); n > 0 {
		endBlock = obs[n-1].Number
	} else {
		endBlock = startBlock
	}
	m.ParityStatus = "not requested"
	m.ValidatorHeads = map[string]uint64{}
	if verifyParity && len(cfg.RPCUrls) > 0 {
		st, heads, _ := checkParity(cfg.RPCUrls, endBlock, time.Duration(cfg.ParityWaitSec)*time.Second)
		m.ParityStatus, m.ValidatorHeads = st, heads
	}
	return m, endBlock
}

// printMeasurement prints the authoritative (counted) numbers with their definitions.
func printMeasurement(m ChainMeasurement) {
	fmt.Printf("   • Total Submitted:    %d txs (= bytes_sent_txs: client wrote to transport / got RPC ok; NOT a validator-side number)\n", m.BytesSentTxs)
	if m.IngressMetricsOK {
		var rec, rej float64
		urls := make([]string, 0, len(m.ReceivedByValidator))
		for u := range m.ReceivedByValidator {
			urls = append(urls, u)
		}
		sort.Strings(urls)
		for _, u := range urls {
			rec += m.ReceivedByValidator[u]
			rej += m.RejectedByValidator[u]
			fmt.Printf("       - validator %s: received=%.0f rejected=%.0f (delta of master_raw_eth_txs_*_total)\n", u, m.ReceivedByValidator[u], m.RejectedByValidator[u])
		}
		fmt.Printf("   • received_by_validators: %.0f (rejected %.0f)\n", rec, rej)
	} else {
		fmt.Printf("   • received_by_validators: NOT MEASURABLE (a validator's /metrics was unavailable)\n")
	}
	bound := ""
	if !m.OursExact {
		bound = fmt.Sprintf(" (LOWER BOUND: %d block(s) not fetched; upper bound %d)", len(m.UnfetchedBlocks), m.OursUpperBound)
	}
	fmt.Printf("   • committed_ours (counted in blocks): %d%s | committed_total_txs in same blocks: %d\n", m.CommittedOurs, bound, m.CommittedTotalTxs)
	fmt.Printf("   • loss_vs_sent: %d (%.2f%%)%s\n", m.LossVsSent, m.LossPctVsSent, map[bool]string{true: "  <-- NEGATIVE: investigate (stale txs from an earlier round or a counting error)", false: ""}[m.LossVsSent < 0])
	fmt.Printf("   • TPS end-to-end (committed_ours / (last our-block observed - inject start = %.3fs)): %.2f tx/s\n", m.EndToEndSeconds, m.TPSEndToEnd)
	fmt.Printf("   • TPS during injection (ours committed by inject end = %d, over %.3fs): %.2f tx/s | backlog at inject end: %d | drain after injection: %.3fs\n",
		m.OursByInjectEnd, m.InjectSeconds, m.TPSDuringInjection, m.BacklogAtInjectEnd, m.DrainSeconds)
	fmt.Printf("   • TPS active commit window (first..last our-block): %.2f tx/s | first commit %.3fs after inject start\n", m.TPSActiveCommit, m.FirstCommitSeconds)
	fmt.Printf("   • measurement stop: %s\n", m.StopReason)
	fmt.Printf("   • parity: %s\n", m.ParityStatus)
}
