package tx_processor

import "context"

// IRGate is called by ProcessTransactions after the transactions were executed and BEFORE the block's state
// roots are computed, i.e. before the first NOMT session of the block is opened. A non-nil error aborts the
// block's execution with that error and no session is ever opened.
//
// The speculative executor uses it to serialize the NOMT-touching tail of speculative executions in block
// order: a NOMT handle admits one session at a time and a finished session stays pending until its block is
// committed, so a LATER speculative block that reached its state roots first could hold the handle while an
// EARLIER block (re-executed after a parent-hash conflict) waits for it — and the earlier block has to commit
// before the later one can. The EVM part still runs fully in parallel.
type IRGate func(ctx context.Context) error

type irGateKey struct{}

// WithIRGate returns a context that makes ProcessTransactions call gate before computing state roots.
func WithIRGate(ctx context.Context, gate IRGate) context.Context {
	return context.WithValue(ctx, irGateKey{}, gate)
}

func irGateFrom(ctx context.Context) IRGate {
	if ctx == nil {
		return nil
	}
	g, _ := ctx.Value(irGateKey{}).(IRGate)
	return g
}
