package processor

import (
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// A public node serves many identical eth_call at once. Every off-chain execution needs its own MVM instance id even
// when the tx hash is the same, or they share (and clear) one instance.
func TestNewOffChainMVMID_UniqueForIdenticalConcurrentCalls(t *testing.T) {
	txHash := common.HexToHash("0xabcdef")
	const workers, each = 64, 2000
	ids := make(chan common.Address, workers*each)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				ids <- newOffChainMVMID(txHash)
			}
		}()
	}
	wg.Wait()
	close(ids)
	seen := make(map[common.Address]struct{}, workers*each)
	for id := range ids {
		if id[0] != 0xFD {
			t.Fatalf("id %s lost the 0xFD prefix that keeps it clear of real Xapian contracts", id.Hex())
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate off-chain MVM id %s among %d identical concurrent requests", id.Hex(), workers*each)
		}
		seen[id] = struct{}{}
	}
}
