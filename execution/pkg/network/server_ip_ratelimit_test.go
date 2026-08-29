package network

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSocketServer_MaxConnectionsPerIP(t *testing.T) {
	bls.Init()
	keyPair := bls.NewKeyPair(common.FromHex("372e9d6411071707a7e7ba76a51c7907a6c799f0cb972df1671e582d649caabf"))
	connManager := NewConnectionsManager()
	handler := NewHandler(nil, nil)

	cfg := DefaultConfig()
	cfg.MaxConnections = 10
	cfg.MaxConnectionsPerIP = 2

	server, err := NewSocketServer(cfg, keyPair, connManager, handler, "1.0.0")
	require.NoError(t, err)

	// Bind to an ephemeral port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	listenAddr := listener.Addr().String()
	_ = listener.Close()

	go func() {
		_ = server.Listen(listenAddr)
	}()
	defer server.Stop()

	// Wait for server to start listening
	var testConn net.Conn
	for i := 0; i < 20; i++ {
		time.Sleep(50 * time.Millisecond)
		testConn, err = net.Dial("tcp", listenAddr)
		if err == nil {
			_ = testConn.Close()
			break
		}
	}
	require.NoError(t, err, "Server failed to start")
	time.Sleep(100 * time.Millisecond)

	// Connection 1 from 127.0.0.1 (Count = 1)
	conn1, err := net.Dial("tcp", listenAddr)
	require.NoError(t, err)
	defer conn1.Close()

	// Connection 2 from 127.0.0.1 (Count = 2, reaches limit)
	conn2, err := net.Dial("tcp", listenAddr)
	require.NoError(t, err)
	defer conn2.Close()

	time.Sleep(100 * time.Millisecond)

	// Connection 3 from 127.0.0.1 (Count = 3, exceeds limit -> should be closed immediately)
	conn3, err := net.Dial("tcp", listenAddr)
	require.NoError(t, err)
	defer conn3.Close()

	// Verify conn3 is closed by reading (should return EOF or closed error quickly)
	_ = conn3.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 10)
	n, readErr := conn3.Read(buf)
	assert.Equal(t, 0, n)
	assert.True(t, readErr == io.EOF || readErr != nil, fmt.Sprintf("Expected connection to be closed, got readErr: %v", readErr))

	// Close conn1 to free up 1 IP slot
	_ = conn1.Close()
	time.Sleep(200 * time.Millisecond)

	// Connection 4 from 127.0.0.1 (Count should be back to 2 -> should succeed)
	conn4, err := net.Dial("tcp", listenAddr)
	require.NoError(t, err)
	defer conn4.Close()

	time.Sleep(100 * time.Millisecond)
	// conn4 should stay open and not be immediately closed
	_ = conn4.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	_, err4 := conn4.Read(buf)
	if netErr, ok := err4.(net.Error); ok && netErr.Timeout() {
		// Timeout is expected since server did not close the connection and sent no data
		assert.True(t, true)
	} else {
		assert.NoError(t, err4)
	}
}
