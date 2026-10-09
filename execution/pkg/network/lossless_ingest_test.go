package network

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/google/uuid"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func testFrame(t *testing.T, command string, body []byte) []byte {
	t.Helper()
	b, err := proto.Marshal(&pb.Message{
		Header: &pb.Header{Command: command, Version: "0.0.1.0", ID: uuid.New().String()},
		Body:   body,
	})
	require.NoError(t, err)
	out := make([]byte, 8, 8+len(b))
	binary.LittleEndian.PutUint64(out, uint64(len(b)))
	return append(out, b...)
}

func testInitFrame(t *testing.T) []byte {
	t.Helper()
	body, err := proto.Marshal(&pb.InitConnection{Address: common.HexToAddress("0x01").Bytes(), Type: "client"})
	require.NoError(t, err)
	return testFrame(t, "InitConnection", body)
}

// startIngestServer starts a real SocketServer whose SendRawTransactions route counts deliveries (and may be slow).
func startIngestServer(t *testing.T, cfg *Config, onTx func()) (addr string, delivered *atomic.Int64) {
	t.Helper()
	bls.Init()
	keyPair := bls.NewKeyPair(common.FromHex("372e9d6411071707a7e7ba76a51c7907a6c799f0cb972df1671e582d649caabf"))
	delivered = &atomic.Int64{}
	routes := map[string]func(network.Request) error{
		"InitConnection": func(network.Request) error { return nil },
		"SendRawTransactions": func(network.Request) error {
			if onTx != nil {
				onTx()
			}
			delivered.Add(1)
			return nil
		},
	}
	server, err := NewSocketServer(cfg, keyPair, NewConnectionsManager(), NewHandler(routes, nil), "1.0.0")
	require.NoError(t, err)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr = l.Addr().String()
	_ = l.Close()
	go func() { _ = server.Listen(addr) }()
	t.Cleanup(server.Stop)

	for i := 0; i < 40; i++ {
		time.Sleep(50 * time.Millisecond)
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			break
		}
	}
	time.Sleep(100 * time.Millisecond)
	return addr, delivered
}

// closeGracefully is how a well-behaved client leaves: it stops writing (FIN) and reads whatever the server sent until
// EOF. Closing a socket that still holds unread incoming data makes the kernel send RST instead, which discards
// whatever the server had received but not yet read; that is plain TCP behaviour and no server code can recover it.
func closeGracefully(t *testing.T, conn net.Conn) {
	t.Helper()
	if tc, ok := conn.(*net.TCPConn); ok {
		require.NoError(t, tc.CloseWrite())
	}
	_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	_, _ = io.Copy(io.Discard, conn)
	_ = conn.Close()
}

func smallIngestConfig() *Config {
	cfg := DefaultConfig()
	cfg.HandlerWorkerPoolSize = 2 // few workers, tiny queues: the handler is the bottleneck
	cfg.RequestChanSize = 8       // central queue
	cfg.ConnRequestChanSize = 4   // per-connection buffer
	return cfg
}

func waitDelivered(t *testing.T, delivered *atomic.Int64, want int64, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if delivered.Load() >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.Equal(t, want, delivered.Load(), "transaction submissions were dropped")
}

// A client that writes many batches faster than the handler can take them and then closes must still have EVERY batch
// delivered: nothing may be dropped because the queues are small, and nothing queued may be abandoned at EOF.
func TestIngest_SlowHandlerClientClosesImmediately_NoBatchLost(t *testing.T) {
	addr, delivered := startIngestServer(t, smallIngestConfig(), func() { time.Sleep(5 * time.Millisecond) })

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	_, err = conn.Write(testInitFrame(t))
	require.NoError(t, err)

	const n = 300
	for i := 0; i < n; i++ {
		_, err = conn.Write(testFrame(t, "SendRawTransactions", []byte{byte(i)}))
		require.NoError(t, err)
	}
	closeGracefully(t, conn)

	waitDelivered(t, delivered, n, 30*time.Second)
}

// Several clients at once, each closing right after writing.
func TestIngest_ManyConnectionsConcurrent_NoBatchLost(t *testing.T) {
	addr, delivered := startIngestServer(t, smallIngestConfig(), func() { time.Sleep(2 * time.Millisecond) })

	const conns, per = 4, 100
	var wg sync.WaitGroup
	for c := 0; c < conns; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.Dial("tcp", addr)
			if !assertNoErr(t, err) {
				return
			}
			_, _ = conn.Write(testInitFrame(t))
			for i := 0; i < per; i++ {
				if _, err := conn.Write(testFrame(t, "SendRawTransactions", []byte{byte(i)})); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
			closeGracefully(t, conn)
		}()
	}
	wg.Wait()
	waitDelivered(t, delivered, conns*per, 30*time.Second)
}

func assertNoErr(t *testing.T, err error) bool {
	t.Helper()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
		return false
	}
	return true
}

// The read deadline must not kill a connection whose reader was merely waiting on backpressure: time spent blocked on a
// full queue is not client inactivity.
func TestIngest_LongBackpressureDoesNotTripIdleTimeout(t *testing.T) {
	cfg := smallIngestConfig()
	cfg.AcceptedConnectionIdleTimeout = 1 * time.Second
	var first sync.Once
	addr, delivered := startIngestServer(t, cfg, func() {
		first.Do(func() { time.Sleep(3 * time.Second) }) // the handler stalls for 3x the idle timeout
	})

	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()
	_, err = conn.Write(testInitFrame(t))
	require.NoError(t, err)

	const n = 40
	for i := 0; i < n; i++ {
		_, err = conn.Write(testFrame(t, "SendRawTransactions", []byte{byte(i)}))
		require.NoError(t, err)
	}
	waitDelivered(t, delivered, n, 30*time.Second)

	// The connection must still be usable afterwards.
	_, err = conn.Write(testFrame(t, "SendRawTransactions", []byte{0xff}))
	require.NoError(t, err)
	waitDelivered(t, delivered, n+1, 10*time.Second)
}
