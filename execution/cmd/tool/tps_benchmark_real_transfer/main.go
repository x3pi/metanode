// tps_benchmark_real_transfer measures TPS for REAL, successful native-coin
// transfers — as opposed to tps_benchmark_e2e, which (found 2026-09-02 while
// investigating why "confirmed" TPS looked suspiciously high) sends from
// freshly-generated, never-funded accounts. Those transactions get included
// in a block and counted as "confirmed" by that tool, but actually REVERT at
// execution (status=0x0, "SubTotalBalance: invalid sub balance amount") and
// charge zero real gas — meaning that tool's whole-session throughput numbers
// measured the cost of admit+order+include+immediately-revert, not the cost
// of a genuinely successful state-mutating transfer.
//
// This tool instead loads real, pre-funded accounts (gen_single_chain.py's
// dev_accounts.json — pass --dev-accounts N when generating the devnet to get
// enough of them) and sends real nonzero-value transfers, tracking each
// account's own on-chain nonce so multiple txs per account are valid. At the
// end it samples receipts to prove txs actually succeeded (status=0x1, real
// gasUsed) rather than just trusting inclusion.
//
// Usage:
//
//	tps_benchmark_real_transfer -node=http://127.0.0.1:19545 -chain-id=7777 \
//	    -accounts-file=/path/to/dev_accounts.json -txs-per-account=100 -workers=200
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

type DevAccount struct {
	PrivateKey string `json:"private_key"`
	Address    string `json:"address"`
}

func main() {
	node := flag.String("node", "http://127.0.0.1:19545", "RPC endpoint")
	chainID := flag.Uint64("chain-id", 7777, "chain id")
	accountsFile := flag.String("accounts-file", "", "path to dev_accounts.json (funded accounts)")
	txsPerAccount := flag.Int("txs-per-account", 10, "number of sequential transfers to send from each account")
	amountWei := flag.String("amount-wei", "1000", "amount of wei to actually transfer per tx (must be > 0 and << each account's funded balance)")
	workers := flag.Int("workers", 200, "concurrent send workers")
	settleSecs := flag.Int("settle", 60, "max seconds to wait for settlement")
	sampleReceipts := flag.Int("sample-receipts", 30, "number of random receipts to fetch and verify status=0x1 at the end")
	dumpSentHashes := flag.String("dump-sent-hashes", "", "if set, write every successfully-sent tx hash (one per line) to this path for later diffing against on-chain contents")
	latencySamples := flag.Int("latency-samples", 300, "number of tx indices, evenly spread across the whole run, to track send-to-confirm latency for (0 disables latency measurement)")
	latencyPollEvery := flag.Duration("latency-poll", 20*time.Millisecond, "receipt poll interval for latency-sampled txs")
	latencyMaxWait := flag.Duration("latency-max-wait", 60*time.Second, "max time to wait for a single latency-sampled tx's receipt")
	targetRate := flag.Int("target-rate", 0, "if set (tx/s), paces sending at this steady rate instead of max speed -- for measuring SUSTAINED latency at a realistic rate rather than burst-absorption latency (a full-speed burst's tail latency is dominated by queue depth, not real per-tx processing time)")
	latencyVerbose := flag.Bool("latency-verbose", false, "print each latency sample's (send-offset, latency) pair, sorted by send offset, to see how latency correlates with when a tx was sent during the run")
	flag.Parse()

	if *accountsFile == "" {
		fmt.Fprintln(os.Stderr, "-accounts-file is required (dev_accounts.json from gen_single_chain.py --dev-accounts N)")
		os.Exit(1)
	}

	raw, err := os.ReadFile(*accountsFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read accounts file:", err)
		os.Exit(1)
	}
	var devAccounts []DevAccount
	if err := json.Unmarshal(raw, &devAccounts); err != nil {
		fmt.Fprintln(os.Stderr, "parse accounts file:", err)
		os.Exit(1)
	}
	if len(devAccounts) == 0 {
		fmt.Fprintln(os.Stderr, "accounts file has 0 accounts")
		os.Exit(1)
	}

	amount, ok := new(big.Int).SetString(*amountWei, 10)
	if !ok {
		fmt.Fprintln(os.Stderr, "invalid -amount-wei:", *amountWei)
		os.Exit(1)
	}

	rpcClient := NewRPCClient(*node)
	destAddr := common.HexToAddress("0x00000000000000000000000000000000009999")

	fmt.Printf("Loaded %d funded accounts from %s\n", len(devAccounts), *accountsFile)
	fmt.Printf("Fetching starting nonces + balances for all accounts...\n")

	accounts := make([]struct {
		hexKey  string
		address common.Address
		nonce   uint64
	}, len(devAccounts))

	var zeroBalanceCount int32
	var wg sync.WaitGroup
	sem := make(chan struct{}, 64)
	for i, da := range devAccounts {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, da DevAccount) {
			defer wg.Done()
			defer func() { <-sem }()
			addr := common.HexToAddress(da.Address)
			nonce, err := rpcClient.GetTransactionCount(da.Address)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  ⚠️  account %s: getNonce failed: %v\n", da.Address, err)
			}
			bal, err := rpcClient.GetBalance(da.Address)
			if err == nil && (bal == "0x0" || bal == "0x") {
				atomic.AddInt32(&zeroBalanceCount, 1)
			}
			accounts[i].hexKey = da.PrivateKey
			accounts[i].address = addr
			accounts[i].nonce = nonce
		}(i, da)
	}
	wg.Wait()

	if zeroBalanceCount > 0 {
		fmt.Printf("  ⚠️  %d/%d accounts have ZERO balance — their txs will revert just like tps_benchmark_e2e's do. Use a devnet generated with enough --dev-accounts / --alloc-balance.\n", zeroBalanceCount, len(devAccounts))
	}

	totalTxs := len(accounts) * *txsPerAccount
	fmt.Printf("Building %d real-transfer transactions (%d accounts × %d txs each, %s wei/tx)...\n",
		totalTxs, len(accounts), *txsPerAccount, amount.String())

	payloads := make([][]byte, totalTxs)
	txHashes := make([]string, totalTxs)
	gasPrice := big.NewInt(1000000)
	signer := types.LatestSignerForChainID(new(big.Int).SetUint64(*chainID))

	numWorkers := runtime.NumCPU()
	if numWorkers > 32 {
		numWorkers = 32
	}
	var buildWg sync.WaitGroup
	chunk := (len(accounts) + numWorkers - 1) / numWorkers
	for w := 0; w < numWorkers; w++ {
		start := w * chunk
		end := start + chunk
		if start >= len(accounts) {
			break
		}
		if end > len(accounts) {
			end = len(accounts)
		}
		buildWg.Add(1)
		go func(start, end int) {
			defer buildWg.Done()
			for ai := start; ai < end; ai++ {
				ecdsaKey, err := crypto.HexToECDSA(strings.TrimPrefix(accounts[ai].hexKey, "0x"))
				if err != nil {
					continue
				}
				for j := 0; j < *txsPerAccount; j++ {
					idx := ai*(*txsPerAccount) + j
					nonce := accounts[ai].nonce + uint64(j)
					tx := types.NewTransaction(nonce, destAddr, amount, 21000, gasPrice, nil)
					signedTx, err := types.SignTx(tx, signer, ecdsaKey)
					if err != nil {
						continue
					}
					b, err := signedTx.MarshalBinary()
					if err != nil {
						continue
					}
					payloads[idx] = b
					txHashes[idx] = signedTx.Hash().Hex()
				}
			}
		}(start, end)
	}
	buildWg.Wait()

	fmt.Printf("✅ Built. Sending across %d workers...\n", *workers)

	startBlock, _ := rpcClient.GetBlockNumber()
	var sent, sendErrors int64

	// Partition by ACCOUNT rather than a flat shared work channel, and send
	// each account's transactions sequentially within its own goroutine.
	//
	// Found 2026-09-02 chasing why a real-transfer run still lost ~20% of
	// transactions even after the node accepted 100% of them (0 send
	// errors): the old code built payloads in nonce order per account but
	// then pushed them all into one channel drained by *workers generic
	// goroutines. With thousands of concurrent consumers pulling from that
	// single channel, whichever goroutine happens to win its HTTP round trip
	// first decides arrival order at the node -- completely unrelated to the
	// nonce order the channel was filled in. A later nonce for an account
	// could easily reach the node well before an earlier one, so the node
	// (correctly) parks it as a "future" transaction waiting for its
	// predecessor -- and if that predecessor itself is delayed the same way,
	// or hasn't even been picked up by a worker yet given a big enough
	// backlog, the wait can exceed the node's 5-minute future-tx TTL and the
	// transaction is dropped for good. This is a client bug, not something
	// the node should (or safely could) work around: a real wallet or
	// backend submitting its own account's nonces always sends them in
	// order for exactly this reason. Grouping work by account and sending
	// each account's nonces back-to-back on the SAME goroutine (still up to
	// *workers accounts concurrently) fixes it structurally: the node never
	// sees a nonce before its predecessor for any account this tool
	// controls, so nothing should ever need the future-tx path at all.
	numSendWorkers := *workers
	if numSendWorkers > len(accounts) {
		numSendWorkers = len(accounts) // more workers than accounts is meaningless here
	}
	if numSendWorkers < 1 {
		numSendWorkers = 1
	}
	acctCh := make(chan int, len(accounts))
	for ai := range accounts {
		acctCh <- ai
	}
	close(acctCh)

	// Per-index flag: did this specific built transaction actually get a
	// successful send ack? Only written by the single goroutine that owns
	// its account's indices (see partition-by-account above), so plain
	// writes are safe without atomics.
	sentOK := make([]bool, totalTxs)

	// Latency sampling: pick a fixed number of tx indices evenly spread
	// across the whole run (not just the first N, which would only measure
	// the ramp-up before the queue builds up) and record each one's real
	// send-to-first-confirmed wall-clock latency. Answers "how long does a
	// transaction actually take under this real load", separate from the
	// aggregate throughput numbers below.
	sampledForLatency := make([]bool, totalTxs)
	sendTimestamps := make([]time.Time, totalTxs)
	if *latencySamples > 0 && totalTxs > 0 {
		n := *latencySamples
		if n > totalTxs {
			n = totalTxs
		}
		step := float64(totalTxs) / float64(n)
		for i := 0; i < n; i++ {
			idx := int(float64(i) * step)
			if idx >= totalTxs {
				idx = totalTxs - 1
			}
			sampledForLatency[idx] = true
		}
	}

	// Optional pacing: a simple token-bucket so aggregate send rate across
	// all workers matches -target-rate instead of going as fast as
	// possible. Ticked at a fine enough granularity to stay accurate up to
	// tens of thousands of tx/s without meaningfully loading the tool
	// itself (each tick just does a non-blocking channel send).
	var rateLimiter chan struct{}
	if *targetRate > 0 {
		rateLimiter = make(chan struct{}, *targetRate)
		ticksPerSec := 200
		if *targetRate < ticksPerSec {
			ticksPerSec = *targetRate
		}
		tokensPerTick := *targetRate / ticksPerSec
		if tokensPerTick < 1 {
			tokensPerTick = 1
		}
		go func() {
			ticker := time.NewTicker(time.Second / time.Duration(ticksPerSec))
			defer ticker.Stop()
			for range ticker.C {
				for i := 0; i < tokensPerTick; i++ {
					select {
					case rateLimiter <- struct{}{}:
					default:
					}
				}
			}
		}()
		fmt.Printf("Pacing sends at target %d tx/s (paced, not max-speed burst)...\n", *targetRate)
	}

	// Latency tracking state: declared here (before sending starts) so each
	// sampled tx's poller can be spawned IMMEDIATELY when it's actually
	// sent, not after the whole send loop finishes.
	//
	// BUG FOUND AND FIXED (2026-09-03): the first version of this spawned
	// every poller goroutine in a separate loop AFTER sendWg.Wait() --
	// meaning a tx sent at t=0 didn't get its first poll attempt until the
	// ENTIRE injection phase finished (e.g. t=75s for a 75s-long paced
	// run), even though it may have confirmed within a few seconds of
	// being sent. Since a poller's very first attempt after that point
	// would immediately succeed (the tx was long since confirmed) and the
	// measured "latency" is time.Since(sentAt), this reported
	// send_offset + latency == total injection duration for EVERY sample
	// -- a suspiciously exact constant that was the tell (confirmed live:
	// 40 samples spanning a 75s run all summed to 75.00-75.02s). This is
	// the same class of bug already flagged in this project's own history
	// (never do timed-portion work whose cost scales with total run
	// length outside the actual per-item measurement) -- purely a
	// measurement artifact, not real node-side latency.
	type latencySample struct {
		sendOffsetMs float64 // time since injectStart when this tx was sent
		latencyMs    float64
	}
	var latencyMu sync.Mutex
	var latenciesMs []float64
	var latencyDetails []latencySample
	var latencyTimeouts int64
	var latencyWg sync.WaitGroup

	injectStart := time.Now()
	var sendWg sync.WaitGroup
	for w := 0; w < numSendWorkers; w++ {
		sendWg.Add(1)
		go func(workerIdx int) {
			defer sendWg.Done()
			// Stagger each worker's very first send instead of every one of
			// them firing in the same instant. Sending sequentially per
			// account (see above) means the natural staggering the old
			// flat-shared-queue design had for free is gone -- all workers'
			// first request used to land here at once, driving "system
			// overloaded" harder and for longer than a steady, ramping start
			// would. Spreading starts across ~1s per 1,000 workers costs at
			// most a few seconds of wall-clock time on the whole run but
			// measurably softens that opening spike.
			time.Sleep(time.Duration(workerIdx) * time.Millisecond)
			for ai := range acctCh {
				base := ai * (*txsPerAccount)
				for j := 0; j < *txsPerAccount; j++ {
					p := payloads[base+j]
					if p == nil {
						continue // signing failed earlier for this one; already excluded from totals
					}
					if rateLimiter != nil {
						<-rateLimiter
					}
					sendAttemptTime := time.Now()
					if _, err := rpcClient.SendRawTransaction(p); err != nil {
						n := atomic.AddInt64(&sendErrors, 1)
						if n <= 5 {
							fmt.Fprintf(os.Stderr, "SEND ERROR #%d: %v\n", n, err)
						}
						// Do not send this account's later nonces if an earlier
						// one failed outright (as opposed to being retried
						// transparently inside SendRawTransaction) -- they would
						// just queue as future transactions behind a nonce that
						// is never coming, recreating the exact problem this
						// change exists to avoid.
						break
					}
					sentOK[base+j] = true
					if sampledForLatency[base+j] {
						sendTimestamps[base+j] = sendAttemptTime
						// Spawn this sample's receipt poller RIGHT NOW, not
						// after the whole send loop finishes -- see the
						// bug-fix comment on the latency state declarations
						// above.
						latencyWg.Add(1)
						hash := txHashes[base+j]
						go func(hash string, sentAt time.Time) {
							defer latencyWg.Done()
							deadline := sentAt.Add(*latencyMaxWait)
							for time.Now().Before(deadline) {
								rcp, err := rpcClient.GetTransactionReceipt(hash)
								if err == nil && rcp != nil {
									lat := float64(time.Since(sentAt).Microseconds()) / 1000.0
									latencyMu.Lock()
									latenciesMs = append(latenciesMs, lat)
									latencyDetails = append(latencyDetails, latencySample{
										sendOffsetMs: float64(sentAt.Sub(injectStart).Microseconds()) / 1000.0,
										latencyMs:    lat,
									})
									latencyMu.Unlock()
									return
								}
								time.Sleep(*latencyPollEvery)
							}
							atomic.AddInt64(&latencyTimeouts, 1)
						}(hash, sendAttemptTime)
					}
					atomic.AddInt64(&sent, 1)
				}
			}
		}(w)
	}
	sendWg.Wait()
	injectTime := time.Since(injectStart)

	fmt.Printf("Injected %d txs in %v (%.0f tx/s, %d errors)\n",
		sent, injectTime, float64(sent)/injectTime.Seconds(), sendErrors)

	if *dumpSentHashes != "" {
		f, err := os.Create(*dumpSentHashes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  ⚠️  could not open -dump-sent-hashes path %s: %v\n", *dumpSentHashes, err)
		} else {
			w := 0
			for idx, ok := range sentOK {
				if ok {
					fmt.Fprintln(f, txHashes[idx])
					w++
				}
			}
			f.Close()
			fmt.Printf("Wrote %d successfully-sent tx hashes to %s\n", w, *dumpSentHashes)
		}
	}

	fmt.Printf("Waiting for settlement (max %ds)...\n", *settleSecs)
	settleStart := time.Now()
	deadline := time.Now().Add(time.Duration(*settleSecs) * time.Second)
	var lastCount int
	var lastProgress time.Time = time.Now()
	confirmedCount := 0
	for time.Now().Before(deadline) {
		endBlock, err := rpcClient.GetBlockNumber()
		if err == nil {
			count := 0
			for bn := startBlock + 1; bn <= endBlock; bn++ {
				blk, err := rpcClient.GetBlockByNumber(bn)
				if err == nil && blk != nil {
					count += len(blk.Transactions)
				}
			}
			confirmedCount = count
			if count != lastCount {
				lastCount = count
				lastProgress = time.Now()
				fmt.Printf("  [%.1fs] %d/%d confirmed in blocks\n", time.Since(settleStart).Seconds(), count, sent)
			}
			if count >= int(sent) {
				break
			}
		}
		if time.Since(lastProgress) > 12*time.Second {
			fmt.Println("  idle for 12s — stopping wait")
			break
		}
		time.Sleep(2 * time.Second)
	}
	settleTime := time.Since(settleStart)

	settleTPS := float64(confirmedCount) / settleTime.Seconds()
	e2eTPS := float64(confirmedCount) / (injectTime + settleTime).Seconds()

	fmt.Printf("\n═══ RESULTS ═══\n")
	fmt.Printf("Sent: %d | Confirmed in blocks: %d\n", sent, confirmedCount)
	fmt.Printf("Settle TPS: %.0f | End-to-end TPS: %.0f\n", settleTPS, e2eTPS)

	// Verify a random sample of receipts actually SUCCEEDED (status=0x1),
	// not just "included" — this is the whole point of this tool.
	fmt.Printf("\nSampling %d receipts to verify real success (status=0x1, gasUsed>0)...\n", *sampleReceipts)
	var validHashes []string
	for _, h := range txHashes {
		if h != "" {
			validHashes = append(validHashes, h)
		}
	}
	rand.Shuffle(len(validHashes), func(i, j int) { validHashes[i], validHashes[j] = validHashes[j], validHashes[i] })
	n := *sampleReceipts
	if n > len(validHashes) {
		n = len(validHashes)
	}
	successCount, revertCount, missingCount := 0, 0, 0
	for i := 0; i < n; i++ {
		rcp, err := rpcClient.GetTransactionReceipt(validHashes[i])
		if err != nil || rcp == nil {
			missingCount++
			continue
		}
		if rcp.Status == "0x1" {
			successCount++
		} else {
			revertCount++
			if revertCount <= 3 {
				retBytes, _ := hex.DecodeString(strings.TrimPrefix(rcp.Return, "0x"))
				fmt.Printf("  ❌ REVERT sample: gasUsed=%s return=%q\n", rcp.GasUsed, string(retBytes))
			}
		}
	}
	fmt.Printf("Sample of %d: %d succeeded (0x1), %d reverted (0x0), %d receipt not found\n",
		n, successCount, revertCount, missingCount)
	if successCount == n {
		fmt.Println("✅ ALL sampled transactions genuinely succeeded — this run measures real transfer throughput.")
	} else if successCount == 0 {
		fmt.Println("⚠️  NONE of the sampled transactions succeeded — check account balances / amount-wei.")
	}

	if *latencySamples > 0 {
		fmt.Printf("\nWaiting for %d latency-sampled tx receipts (max %s each)...\n", *latencySamples, *latencyMaxWait)
		latencyWg.Wait()
		latencyMu.Lock()
		samples := append([]float64(nil), latenciesMs...)
		latencyMu.Unlock()
		fmt.Printf("\n═══ LATENCY (send → first-confirmed, real load) ═══\n")
		fmt.Printf("Samples: %d confirmed, %d timed out (of %d attempted)\n",
			len(samples), atomic.LoadInt64(&latencyTimeouts), len(samples)+int(atomic.LoadInt64(&latencyTimeouts)))
		if len(samples) > 0 {
			sort.Float64s(samples)
			sum := 0.0
			for _, l := range samples {
				sum += l
			}
			fmt.Printf("Min:  %10.2f ms\n", samples[0])
			fmt.Printf("p50:  %10.2f ms\n", percentile(samples, 0.50))
			fmt.Printf("p90:  %10.2f ms\n", percentile(samples, 0.90))
			fmt.Printf("p99:  %10.2f ms\n", percentile(samples, 0.99))
			fmt.Printf("Max:  %10.2f ms\n", samples[len(samples)-1])
			fmt.Printf("Mean: %10.2f ms\n", sum/float64(len(samples)))
		}
		if *latencyVerbose {
			details := append([]latencySample(nil), latencyDetails...)
			sort.Slice(details, func(i, j int) bool { return details[i].sendOffsetMs < details[j].sendOffsetMs })
			fmt.Println("\n--- per-sample detail (sorted by send offset since injection start) ---")
			fmt.Println("send_offset_s  latency_s")
			for _, d := range details {
				fmt.Printf("%13.2f  %9.2f\n", d.sendOffsetMs/1000.0, d.latencyMs/1000.0)
			}
		}
		fmt.Println("════════════════════════════════════════════════")
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return sorted[idx]
}
