package network

import (
	"context"
	"sync/atomic"
)

// TxAdmissionGate blocks until the node can accept more transactions (or ctx is done). It implements backpressure
// for transaction-submission commands: the connection's reader stops reading, so TCP flow control slows the client
// down instead of the node dropping or disconnecting it. Raw-tx clients are fire-and-forget (they never read the
// response), so a dropped batch is a silent nonce gap for every sender in it.
type TxAdmissionGate func(ctx context.Context) error

var txAdmissionGate atomic.Pointer[TxAdmissionGate]

// SetTxAdmissionGate installs (or, with nil, removes) the gate used by every SocketServer in this process.
func SetTxAdmissionGate(g TxAdmissionGate) {
	if g == nil {
		txAdmissionGate.Store(nil)
		return
	}
	txAdmissionGate.Store(&g)
}

func loadTxAdmissionGate() TxAdmissionGate {
	if p := txAdmissionGate.Load(); p != nil {
		return *p
	}
	return nil
}

// isTxSubmissionCommand reports the commands that carry client transactions. They are delivered losslessly (see
// SocketServer.HandleConnection); every other command keeps the non-blocking, drop-when-full behaviour.
func isTxSubmissionCommand(cmd string) bool {
	return cmd == "SendRawTransaction" || cmd == "SendRawTransactions"
}
