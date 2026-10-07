package rollup

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

// idempotentClient is the parent client mock with an inbound read that does not consume the events.
type idempotentClient struct {
	mockParentChainClient
	inbound []*parentchain.TransferEvent
}

func (c *idempotentClient) GetInboundTransfers(_ cm.PublicKey, cursor uint64) ([]*parentchain.TransferEvent, uint64, error) {
	if cursor >= uint64(len(c.inbound)) {
		return nil, cursor, nil
	}
	return c.inbound[cursor:], uint64(len(c.inbound)), nil
}

type fixedFloat struct{ v *big.Int }

func (f fixedFloat) GetFloat(cm.PublicKey) (*big.Int, uint64, error) { return new(big.Int).Set(f.v), 1, nil }

func newConsInputs(supply, float int64) (ConservationInputs, Store) {
	store := NewDBStore(&mockDB{data: make(map[common.Address]map[common.Hash][]byte)})
	return ConservationInputs{
		Store:       store,
		Client:      &idempotentClient{},
		Float:       fixedFloat{big.NewInt(float)},
		TotalSupply: func() (*big.Int, error) { return big.NewInt(supply), nil },
	}, store
}

func senderRecord(id string, state State, value, fee int64) *MessageRecord {
	return &MessageRecord{MessageID: crypto.Keccak256Hash([]byte(id)), Role: RoleSender, State: state, Value: big.NewInt(value), GasFee: big.NewInt(fee)}
}

func TestCheckConservation_AtRest(t *testing.T) {
	in, _ := newConsInputs(1000, 1000)
	res, err := CheckConservation(in)
	if err != nil || !res.Stable || !res.OK {
		t.Fatalf("equal float and accounts at rest must be OK: %v %v", res, err)
	}
}

func TestCheckConservation_FloatBelowAccountsIsAViolation(t *testing.T) {
	// This is exactly what the old fee leak produced: the Parent Chain removed value+fee, the cluster only value.
	in, _ := newConsInputs(1000, 900)
	res, _ := CheckConservation(in)
	if !res.Stable || res.OK || !strings.Contains(res.Reason, "BELOW") {
		t.Fatalf("float < accounts must be a violation: %v", res)
	}
}

func TestCheckConservation_FloatAboveAccountsNeedsInFlight(t *testing.T) {
	in, store := newConsInputs(400, 1000)
	if res, _ := CheckConservation(in); res.OK || !strings.Contains(res.Reason, "ABOVE") {
		t.Fatalf("a surplus with nothing in flight is a violation: %v", res)
	}
	// 500 value + 100 fee debited here, the Parent Chain has not removed the float yet: surplus 600 is exactly in flight.
	if err := store.Put(senderRecord("a", StateLocalAppliedPendingSend, 500, 100)); err != nil {
		t.Fatal(err)
	}
	if res, _ := CheckConservation(in); !res.OK || res.Pending.Int64() != 600 {
		t.Fatalf("a surplus equal to the in-flight transfer must be OK: %v", res)
	}
	in.Float = fixedFloat{big.NewInt(1001)}
	if res, _ := CheckConservation(in); res.OK {
		t.Fatalf("a surplus above the in-flight amount must be a violation: %v", res)
	}
}

func TestCheckConservation_UnprocessedInboundCountsAsInFlight(t *testing.T) {
	in, _ := newConsInputs(0, 500) // the Parent Chain credited 500 to the cluster, the accounts are not credited yet
	in.Client.(*idempotentClient).inbound = []*parentchain.TransferEvent{{Amount: big.NewInt(500)}}
	in.RecvCursor = func() uint64 { return 0 }
	if res, _ := CheckConservation(in); !res.OK {
		t.Fatalf("an inbound event not yet credited is in flight: %v", res)
	}
	in.RecvCursor = func() uint64 { return 1 } // processed => nothing may be in flight any more
	if res, _ := CheckConservation(in); res.OK {
		t.Fatalf("after the event was processed the surplus is a violation: %v", res)
	}
}

func TestCheckConservation_MovingActivityIsInconclusiveNotAVerdict(t *testing.T) {
	in, store := newConsInputs(1000, 900) // would be a violation if it held still
	n := 0
	in.TotalSupply = func() (*big.Int, error) {
		n++
		_ = store.Put(senderRecord("moving"+string(rune('a'+n)), StateLocalAppliedPendingSend, 1, 0))
		return big.NewInt(1000), nil
	}
	res, err := CheckConservation(in)
	if err != nil || res.Stable {
		t.Fatalf("a measurement taken while transfers keep appearing must be inconclusive: %v %v", res, err)
	}
}

func TestConservationGuard_Verdicts(t *testing.T) {
	ok := ConservationResult{Supply: big.NewInt(1), Float: big.NewInt(1), Pending: big.NewInt(0), Diff: big.NewInt(0), Stable: true, OK: true}
	bad := ConservationResult{Supply: big.NewInt(2), Float: big.NewInt(1), Pending: big.NewInt(0), Diff: big.NewInt(-1), Stable: true}
	unstable := ConservationResult{Supply: big.NewInt(2), Float: big.NewInt(1), Diff: big.NewInt(-1)}

	g := NewConservationGuard(ConservationEnforce)
	if g.Allow() == nil {
		t.Fatal("enforce must not allow cross-chain before the first conclusive OK")
	}
	g.Observe(unstable, nil)
	g.Observe(ConservationResult{}, errors.New("parent unreachable"))
	if g.Allow() == nil {
		t.Fatal("inconclusive measurements must not unlock cross-chain")
	}
	g.Observe(ok, nil)
	if err := g.Allow(); err != nil {
		t.Fatalf("a conclusive OK unlocks cross-chain: %v", err)
	}
	g.Observe(bad, nil)
	g.Observe(bad, nil)
	if g.Allow() != nil {
		t.Fatal("one or two violations are not enough (the trie may lag a block)")
	}
	g.Observe(unstable, nil) // inconclusive neither confirms nor clears
	g.Observe(bad, nil)
	if g.Allow() == nil {
		t.Fatal("three consecutive violations must halt cross-chain")
	}
	g.Observe(ok, nil)
	if g.Allow() != nil {
		t.Fatal("a conclusive OK lifts the halt")
	}

	for _, m := range []ConservationMode{ConservationWarn, ConservationOff} {
		w := NewConservationGuard(m)
		for i := 0; i < 5; i++ {
			w.Observe(bad, nil)
		}
		if w.Allow() != nil {
			t.Fatalf("mode %s must never block", m)
		}
	}
	if (*ConservationGuard)(nil).Allow() != nil {
		t.Fatal("a nil guard allows everything")
	}
	if _, err := ParseConservationMode("bogus"); err == nil {
		t.Fatal("unknown mode must be rejected")
	}
}

// End to end on the in-memory parent chain and two cluster nodes: with the cluster float aligned to the cluster's
// accounts at the start, the invariant holds exactly at rest after a delivered transfer and after a refunded one.
func TestConservation_HoldsAfterTransferAndRefund(t *testing.T) {
	for _, refund := range []bool{false, true} {
		parentChain := NewInMemoryParentChain()
		kp1, kp2 := bls.GenerateKeyPair(), bls.GenerateKeyPair()
		parentChain.nodeKeys[1], parentChain.nodeKeys[2] = kp1, kp2
		sender, target := common.HexToAddress("0xaaa"), common.HexToAddress("0xbbb")
		parentChain.accounts[sender], parentChain.accounts[target] = kp1.PublicKey(), kp2.PublicKey()

		// float(cluster) == sum(cluster accounts): cluster 1 holds 10000 (the sender), cluster 2 holds nothing.
		setupClusterFloatBalance(t, parentChain, kp1, 1, big.NewInt(10000))
		pub2 := kp2.PublicKey()
		_ = parentChain.store.SetChainRegistry(crypto.Keccak256Hash(pub2[:]), parentchain.ChainRegistryEntry{FloatIdentityKey: pub2, ClusterIDDescriptive: 2, Authorized: true})

		node1 := NewRollupNode(1, parentChain, kp1, kp2.PublicKey(), 2)
		node2 := NewRollupNode(2, parentChain, kp2, kp1.PublicKey(), 1)
		if refund {
			node2.receiveWorker.Validator = func(common.Address) bool { return false }
		}
		node1.StateDB.(*mockAccountStateDB).balances[sender] = big.NewInt(10000)

		sum := func(n *RollupNode) func() (*big.Int, error) {
			return func() (*big.Int, error) {
				db := n.StateDB.(*mockAccountStateDB)
				db.mu.Lock()
				defer db.mu.Unlock()
				s := new(big.Int)
				for _, b := range db.balances {
					s.Add(s, b)
				}
				return s, nil
			}
		}
		inputs := func(n *RollupNode, kp *bls.KeyPair, id uint64) ConservationInputs {
			return ConservationInputs{
				Store:       n.Store,
				Client:      &ParentChainClientAdapter{chain: parentChain, chainID: id},
				Float:       chainFloat{parentChain, kp.PublicKey()},
				BLS:         kp.PublicKey(),
				TotalSupply: sum(n),
				RecvCursor:  n.receiveWorker.getCursor,
			}
		}

		h := NewCrossNodeHandler(kp1.PublicKey())
		msgID, err := h.HandleTransfer(node1.Store, node1.StateDB, kp2.PublicKey(), sender, target, big.NewInt(500), crypto.Keccak256Hash(nil))
		if err != nil {
			t.Fatal(err)
		}
		// Right after the debit the float still backs the 600 that left the accounts: in flight, still conserved.
		if res, _ := CheckConservation(inputs(node1, kp1, 1)); !res.Stable || !res.OK || res.Diff.Int64() != 600 {
			t.Fatalf("refund=%v: in-flight state must satisfy the bound: %v", refund, res)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		settledNow := func() bool {
			if refund {
				// 10000 - (500 value + 100 fee) debited, then the value (500) is refunded; the fee is burned.
				return node1.StateDB.GetBalance(sender).Cmp(big.NewInt(9900)) == 0
			}
			return node2.StateDB.GetBalance(target).Cmp(big.NewInt(500)) == 0
		}
		for !settledNow() {
			select {
			case <-ctx.Done():
				t.Fatalf("refund=%v: transfer did not settle", refund)
			case <-time.After(50 * time.Millisecond):
			}
			node1.sendWorker.processPending()
			node2.receiveWorker.pollAndProcess()
			node1.receiveWorker.pollAndProcess()
		}
		_ = msgID
		// Let the workers finish their bookkeeping so nothing moves while we measure.
		for i := 0; i < 10; i++ {
			node1.sendWorker.processPending()
			node2.receiveWorker.pollAndProcess()
			node1.receiveWorker.pollAndProcess()
			time.Sleep(20 * time.Millisecond)
		}
		for i, n := range []struct {
			node *RollupNode
			kp   *bls.KeyPair
			id   uint64
		}{{node1, kp1, 1}, {node2, kp2, 2}} {
			res, err := CheckConservation(inputs(n.node, n.kp, n.id))
			if err != nil || !res.Stable || !res.OK || res.Diff.Sign() != 0 {
				t.Fatalf("refund=%v cluster %d at rest: float must equal accounts exactly: %v %v", refund, i+1, res, err)
			}
		}
	}
}

type chainFloat struct {
	chain *InMemoryParentChain
	key   cm.PublicKey
}

func (c chainFloat) GetFloat(cm.PublicKey) (*big.Int, uint64, error) {
	c.chain.mu.Lock()
	defer c.chain.mu.Unlock()
	b, err := c.chain.store.GetFloat(crypto.Keccak256Hash(c.key[:]))
	return b, 1, err
}
