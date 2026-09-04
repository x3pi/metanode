// Command count_onchain_txs independently verifies real, on-chain delivery
// of a load test by summing len(block.Transactions()) over a block range --
// ground truth, not a client-side "did I get a receipt back in time" guess.
//
// Exists because tps_benchmark_e2e's own -settle window is a fixed
// deadline: at large scale (millions of txs) the chain can still be
// actively draining its backlog well after that deadline passes, so its
// own txs_confirmed can under-report real delivery (found live 2026-09-03:
// a 4M-tx run reported 3,312,294 confirmed against a 120s settle window,
// while the chain kept processing in the background and every single tx
// had actually landed a couple minutes later -- confirmed with this exact
// tool). Rather than guess a settle window generous enough for whatever
// scale someone runs next, this tool waits for the chain to demonstrably
// stop producing new transactions (N consecutive empty blocks) and then
// reports the true total -- see run_load_test.sh, which runs this
// automatically after every benchmark.
//
// Fetches blocks CONCURRENTLY (a worker pool, like buildTransactions in
// tps_benchmark_e2e) and only ever re-scans blocks it hasn't already
// counted -- a first version fetched one block at a time and re-summed
// the entire range from `start` on every -wait-drain poll, which was fine
// at hundreds of blocks but fell badly behind once trailing empty blocks
// (produced on a timer even with nothing left to include) pushed the tip
// into the tens of thousands: found live 2026-09-03 chasing exactly that
// while verifying a 4M-tx run, where drain-detection itself became the
// slowest step.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
)

// countRange fetches [from, to] concurrently and returns each block's tx
// count indexed by block number, plus any error hit along the way.
func countRange(ctx context.Context, client *ethclient.Client, from, to uint64) (map[uint64]uint64, error) {
	if to < from {
		return map[uint64]uint64{}, nil
	}
	total := to - from + 1
	numWorkers := runtime.GOMAXPROCS(0) * 4
	if numWorkers > 64 {
		numWorkers = 64
	}
	if uint64(numWorkers) > total {
		numWorkers = int(total)
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	results := make(map[uint64]uint64, total)
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup

	blockCh := make(chan uint64, total)
	for n := from; n <= to; n++ {
		blockCh <- n
	}
	close(blockCh)

	wg.Add(numWorkers)
	for w := 0; w < numWorkers; w++ {
		go func() {
			defer wg.Done()
			for n := range blockCh {
				blk, err := client.BlockByNumber(ctx, new(big.Int).SetUint64(n))
				mu.Lock()
				if err != nil {
					if firstErr == nil {
						firstErr = fmt.Errorf("block %d: %w", n, err)
					}
				} else {
					results[n] = uint64(len(blk.Transactions()))
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return results, firstErr
}

func main() {
	node := flag.String("node", "http://127.0.0.1:18545", "RPC endpoint")
	start := flag.Uint64("start", 0, "start block (inclusive)")
	expected := flag.Uint64("expect", 0, "expected tx count; if >0, exits non-zero on mismatch")
	waitDrain := flag.Bool("wait-drain", true, "poll until the chain has gone quiet before counting")
	emptyBlocksNeeded := flag.Int("empty-blocks", 15, "consecutive empty blocks that count as \"drained\"")
	pollInterval := flag.Duration("poll-interval", 3*time.Second, "how often to re-check while draining")
	maxWait := flag.Duration("max-wait", 10*time.Minute, "give up waiting for drain after this long")
	flag.Parse()

	client, err := ethclient.Dial(*node)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx := context.Background()

	var total uint64
	nextBlock := *start   // first not-yet-counted block
	trailingEmpty := 0    // consecutive empty blocks counted so far, from the end
	deadline := time.Now().Add(*maxWait)

	for {
		tip, err := client.BlockNumber(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if tip >= nextBlock {
			counts, err := countRange(ctx, client, nextBlock, tip)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			for n := nextBlock; n <= tip; n++ {
				c := counts[n]
				total += c
				if c == 0 {
					trailingEmpty++
				} else {
					trailingEmpty = 0
				}
			}
			nextBlock = tip + 1
		}

		if !*waitDrain || trailingEmpty >= *emptyBlocksNeeded {
			fmt.Printf("✅ Drained: tip=%d start=%d total_txs=%d trailing_empty_blocks=%d\n", tip, *start, total, trailingEmpty)
			break
		}
		if time.Now().After(deadline) {
			fmt.Printf("⚠️ Gave up waiting for drain after %s: tip=%d total_txs=%d trailing_empty_blocks=%d (wanted %d)\n",
				maxWait.String(), tip, total, trailingEmpty, *emptyBlocksNeeded)
			break
		}
		fmt.Printf("⏳ Still draining: tip=%d total_txs=%d trailing_empty_blocks=%d/%d\n", tip, total, trailingEmpty, *emptyBlocksNeeded)
		time.Sleep(*pollInterval)
	}

	if *expected > 0 {
		if total == *expected {
			fmt.Printf("✅ VERIFIED: %d/%d txs landed on-chain (100%%)\n", total, *expected)
		} else {
			fmt.Printf("❌ MISMATCH: %d/%d txs landed on-chain (%.4f%%) -- %d missing\n",
				total, *expected, float64(total)/float64(*expected)*100, int64(*expected)-int64(total))
			os.Exit(1)
		}
	}
}
