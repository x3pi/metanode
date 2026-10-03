package rollup

import (
	"fmt"
	"log"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

// Cluster conservation invariant.
//
// The cluster's BLS identity represents ALL accounts of the cluster on the Parent Chain, so the sum of every account
// balance in the cluster must always equal the BLS float balance on the Parent Chain. Cross-cluster transfers move
// float between BLS identities and the same amount (value + fee, the fee is burned on both sides) moves out of / into
// the cluster's accounts, so:
//
//	at rest:      float(BLS) == sum(accounts)
//	in flight:    0 <= float(BLS) - sum(accounts) <= pending
//
// The difference is never negative and is bounded by what is still in flight, because
//   - a sender is debited here BEFORE the Parent Chain removes the float (float stays higher), and
//   - a receiver is credited here only AFTER the Parent Chain has added the float (float already higher).
//
// pending = sum(value+fee of non-terminal sender records) + sum(value of non-terminal receiver records)
//
//	+ sum(amount of inbound events not yet processed).
// A float BELOW the accounts means coins exist in the cluster that nothing backs; a float ABOVE accounts+pending means
// backed coins vanished from the cluster. Either one is a conservation violation.

// FloatReader reads the cluster's float balance from the Parent Chain (quorum verified).
type FloatReader interface {
	GetFloat(pubKey cm.PublicKey) (*big.Int, uint64, error)
}

// ConservationInputs wires the checker to the cluster and the Parent Chain.
type ConservationInputs struct {
	Store      Store
	Client     parentchain.Client
	Float      FloatReader
	BLS        cm.PublicKey
	// TotalSupply returns the sum of every account balance (including pending balances) of the cluster.
	TotalSupply func() (*big.Int, error)
	// RecvCursor is the inbound cursor of the ReceiveWorker (events below it are already processed).
	RecvCursor func() uint64
}

// ConservationResult is one measurement.
type ConservationResult struct {
	Supply  *big.Int // sum of the cluster's accounts
	Float   *big.Int // BLS float on the Parent Chain
	Pending *big.Int // upper bound of what can legitimately be in flight
	Diff    *big.Int // Float - Supply
	Stable  bool     // false: activity kept changing the in-flight set, the measurement is inconclusive
	OK      bool     // only meaningful when Stable
	Reason  string
}

func (r ConservationResult) String() string {
	return fmt.Sprintf("supply=%s float=%s diff=%s pending=%s stable=%v ok=%v %s", r.Supply, r.Float, r.Diff, r.Pending, r.Stable, r.OK, r.Reason)
}

type inflightSnapshot struct {
	seq         uint64
	fingerprint string
	pending     *big.Int
}

func snapshot(in ConservationInputs) (inflightSnapshot, error) {
	recs, err := in.Store.ScanNonTerminal()
	if err != nil {
		return inflightSnapshot{}, err
	}
	seq, err := in.Store.GetNextFloatSeq()
	if err != nil {
		return inflightSnapshot{}, err
	}
	pending := new(big.Int)
	keys := make([]string, 0, len(recs))
	for _, r := range recs {
		keys = append(keys, fmt.Sprintf("%s:%d", r.MessageID.Hex(), r.State))
		amt := new(big.Int)
		if r.Value != nil {
			amt.Add(amt, r.Value)
		}
		if r.Role == RoleSender && r.GasFee != nil {
			amt.Add(amt, r.GasFee)
		}
		pending.Add(pending, amt)
	}
	sort.Strings(keys)

	cursor := uint64(0)
	if in.RecvCursor != nil {
		cursor = in.RecvCursor()
	}
	events, _, err := in.Client.GetInboundTransfers(in.BLS, cursor)
	if err != nil {
		return inflightSnapshot{}, err
	}
	keys = append(keys, fmt.Sprintf("inbound:%d@%d", len(events), cursor))
	for _, e := range events {
		if e != nil && e.Amount != nil {
			pending.Add(pending, e.Amount)
		}
	}
	return inflightSnapshot{seq: seq, fingerprint: strings.Join(keys, "|"), pending: pending}, nil
}

// CheckConservation measures the invariant once. It reads the in-flight set before and after reading the float and the
// accounts and only reports a conclusive result when nothing moved in between (otherwise it retries, and finally
// reports Stable=false: inconclusive, never a verdict).
func CheckConservation(in ConservationInputs) (ConservationResult, error) {
	if in.Store == nil || in.Client == nil || in.Float == nil || in.TotalSupply == nil {
		return ConservationResult{}, fmt.Errorf("conservation: incomplete inputs")
	}
	var last ConservationResult
	for attempt := 0; attempt < 5; attempt++ {
		before, err := snapshot(in)
		if err != nil {
			return ConservationResult{}, err
		}
		// Float first, then accounts: a transfer that starts in between can only make the float lag behind the
		// accounts, which is exactly the direction the pending bound covers.
		f, _, err := in.Float.GetFloat(in.BLS)
		if err != nil {
			return ConservationResult{}, err
		}
		s, err := in.TotalSupply()
		if err != nil {
			return ConservationResult{}, err
		}
		after, err := snapshot(in)
		if err != nil {
			return ConservationResult{}, err
		}
		diff := new(big.Int).Sub(f, s)
		last = ConservationResult{Supply: s, Float: f, Pending: before.pending, Diff: diff}
		if before.seq != after.seq || before.fingerprint != after.fingerprint {
			last.Reason = "in-flight set changed while measuring"
			time.Sleep(300 * time.Millisecond)
			continue
		}
		last.Stable = true
		switch {
		case diff.Sign() < 0:
			last.Reason = "float is BELOW the cluster's accounts: coins in the cluster are not backed on the Parent Chain"
		case diff.Cmp(before.pending) > 0:
			last.Reason = "float is ABOVE accounts + in-flight: backed coins are missing from the cluster's accounts"
		default:
			last.OK = true
		}
		return last, nil
	}
	return last, nil
}

// ConservationMode says what a violation does.
type ConservationMode string

const (
	// ConservationEnforce blocks cross-chain activity until a conclusive OK measurement exists and again after a
	// confirmed violation (halt-not-guess). Production default.
	ConservationEnforce ConservationMode = "enforce"
	// ConservationWarn only logs. Devnet, where float is minted by deposits and does not mirror the cluster's accounts.
	ConservationWarn ConservationMode = "warn"
	ConservationOff  ConservationMode = "off"
)

// ParseConservationMode parses a mode string; empty means enforce.
func ParseConservationMode(s string) (ConservationMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "enforce":
		return ConservationEnforce, nil
	case "warn":
		return ConservationWarn, nil
	case "off":
		return ConservationOff, nil
	}
	return "", fmt.Errorf("unknown conservation mode %q (use enforce|warn|off)", s)
}

// violationConfirmations is how many consecutive conclusive violations are needed before cross-chain is blocked: the
// accounts are read from the committed trie, which can lag the latest block by a moment.
const violationConfirmations = 3

// ConservationGuard holds the verdict of the periodic measurements and gates cross-chain activity.
type ConservationGuard struct {
	mode ConservationMode

	mu         sync.RWMutex
	verified   bool // a conclusive OK measurement exists
	violations int  // consecutive conclusive violations
	blocked    bool
	last       ConservationResult
	lastErr    error
}

func NewConservationGuard(mode ConservationMode) *ConservationGuard {
	return &ConservationGuard{mode: mode}
}

// Observe records one measurement and returns whether the guard's verdict changed.
func (g *ConservationGuard) Observe(res ConservationResult, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.last, g.lastErr = res, err
	switch {
	case err != nil || !res.Stable:
		// Inconclusive: neither confirms nor refutes. Keep the previous verdict.
	case res.OK:
		g.verified, g.violations, g.blocked = true, 0, false
	default:
		g.violations++
		if g.violations >= violationConfirmations {
			g.blocked = true
		}
	}
}

// Allow reports whether cross-chain activity may proceed.
func (g *ConservationGuard) Allow() error {
	if g == nil || g.mode != ConservationEnforce {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.blocked {
		return fmt.Errorf("cross-chain halted: the cluster's BLS float does not match its accounts (%s)", g.last)
	}
	if !g.verified {
		return fmt.Errorf("cross-chain not started: the cluster's BLS float has not been verified against its accounts yet")
	}
	return nil
}

// Status returns the last measurement for monitoring.
func (g *ConservationGuard) Status() (last ConservationResult, lastErr error, verified, blocked bool, mode ConservationMode) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.last, g.lastErr, g.verified, g.blocked, g.mode
}

// Run measures immediately, then every interval, until stop is closed.
func (g *ConservationGuard) Run(in ConservationInputs, interval time.Duration, stop <-chan struct{}) {
	if g == nil || g.mode == ConservationOff {
		return
	}
	measure := func() {
		res, err := CheckConservation(in)
		g.Observe(res, err)
		switch {
		case err != nil:
			log.Printf("🟡 [CONSERVATION] cannot measure yet: %v", err)
		case !res.Stable:
			log.Printf("🟡 [CONSERVATION] inconclusive (activity): %s", res)
		case res.OK:
			log.Printf("✅ [CONSERVATION] cluster BLS float matches its accounts: %s", res)
		default:
			log.Printf("🚨 [CONSERVATION] VIOLATION (%d/%d): %s", g.violationsCount(), violationConfirmations, res)
		}
	}
	// Until the first conclusive verdict, measure quickly.
	for {
		select {
		case <-stop:
			return
		default:
		}
		measure()
		g.mu.RLock()
		settled := g.verified || g.blocked
		g.mu.RUnlock()
		wait := interval
		if !settled {
			wait = 3 * time.Second
		}
		select {
		case <-stop:
			return
		case <-time.After(wait):
		}
	}
}

func (g *ConservationGuard) violationsCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.violations
}
