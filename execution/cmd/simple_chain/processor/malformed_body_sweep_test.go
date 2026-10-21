package processor

import (
	"encoding/binary"
	"math/rand"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// Remote-DoS sweep: every route below decodes a body that an arbitrary TCP client controls. A panic in a worker takes the
// whole node down, so each handler must return (an error is fine) for any body. The corpus is fixed (boundary lengths,
// protobuf wire-format edge cases) plus seeded pseudo-random bodies, so a failure reproduces from the printed hex.

func malformedCorpus(seed int64, randomCases int) [][]byte {
	out := [][]byte{nil, {}, {0x00}, {0xff}, {0x80}, {0x0a}, {0x0a, 0xff}, {0x0a, 0xff, 0xff, 0xff, 0xff, 0x0f}}
	for _, n := range []int{1, 2, 19, 20, 21, 31, 32, 33, 64, 65, 96, 97, 255, 256, 1024, 70_000} {
		out = append(out, make([]byte, n))
		ones := make([]byte, n)
		for i := range ones {
			ones[i] = 0xff
		}
		out = append(out, ones)
	}
	// length-delimited field 1 claiming far more bytes than present, and a 10-byte varint overflow
	huge := []byte{0x0a}
	huge = binary.AppendUvarint(huge, 1<<40)
	out = append(out, huge, append(huge, 1, 2, 3))
	out = append(out, []byte{0x08, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f})
	// a type byte of an EIP-2718 envelope followed by garbage RLP
	for _, t := range []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x7f, 0xc0, 0xf8, 0xf9, 0xb8, 0xbf} {
		out = append(out, []byte{t}, []byte{t, 0xc0}, []byte{t, 0xf8, 0xff}, append([]byte{t}, make([]byte, 40)...))
	}
	r := rand.New(rand.NewSource(seed))
	for i := 0; i < randomCases; i++ {
		b := make([]byte, r.Intn(200))
		r.Read(b)
		out = append(out, b)
	}
	return out
}

func sweepRoute(t *testing.T, name, cmd string, h func(network.Request) error, newConn func() network.Connection, bodies [][]byte) {
	t.Helper()
	for i, body := range bodies {
		func() {
			defer func() {
				if r := recover(); r != nil {
					head := body
					if len(head) > 64 {
						head = head[:64]
					}
					t.Errorf("%s: handler panicked on body #%d (len %d, first bytes %x): %v", name, i, len(body), head, r)
				}
			}()
			_ = h(NewMockRequest(newConn(), NewMockMessage(cmd, body)))
		}()
	}
}

func TestMalformedBodySweep_NoRoutePanics(t *testing.T) {
	bodies := malformedCorpus(20261021, 1500)
	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	conn := func() network.Connection { return NewMockConnection(addr) }

	t.Run("registration", func(t *testing.T) {
		sp, _, tracker, _ := setupRegistrationTest(t)
		defer tracker.Stop()
		sweepRoute(t, "GetRegistrationMessage", command.GetRegistrationMessage, sp.ProcessGetRegistrationMessage, conn, bodies)
		sweepRoute(t, "RegisterAccount", command.RegisterAccount, sp.ProcessRegisterAccount, conn, bodies)
		sweepRoute(t, "GetRegistrationStatus", command.GetRegistrationStatus, sp.ProcessGetRegistrationStatus, conn, bodies)
	})

	t.Run("raw_tx_secp", func(t *testing.T) {
		tp := newTestTransactionProcessor(createTestChainState(true, 991), NewMockMessageSender())
		go func() { // drain so a valid-looking body can never block on the bounded queue
			for range tp.injectionQueue {
			}
		}()
		defer close(tp.injectionQueue)
		sweepRoute(t, "SendRawTransaction", command.SendRawTransaction, tp.ProcessRawTransactionFromClient, conn, bodies)
		sweepRoute(t, "SendRawTransactions", command.SendRawTransactions, tp.ProcessRawTransactionsFromClient, conn, bodies)
	})

	t.Run("subscribe", func(t *testing.T) {
		sp := &SubscribeProcessor{
			subscribers:                    make(map[common.Address][]network.Connection),
			mapConnectionSubcribeAddresses: make(map[network.Connection][]common.Address),
			messageSender:                  NewMockMessageSender(),
		}
		sweepRoute(t, "SubscribeToAddress", command.SubscribeToAddress, sp.ProcessSubscribeToAddress, conn, bodies)
	})
}
