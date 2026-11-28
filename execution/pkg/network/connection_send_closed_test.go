package network

import (
	"net"
	"testing"
	"time"

	"github.com/meta-node-blockchain/meta-node/types/network"
	"github.com/stretchr/testify/require"
)

// A sender holding a reference to sendChan while the connection is torn down must get ErrDisconnected, not a panic
// ("send on closed channel" used to crash the whole node when a receipt broadcast raced a client disconnect).
func TestSendMessage_ClosedSendChanReturnsErrDisconnected(t *testing.T) {
	ch := make(chan network.Message, 1)
	close(ch)
	c := &Connection{
		config:          &Config{WriteTimeout: 50 * time.Millisecond, SendChanSize: 1},
		sendChan:        ch,
		cachedConnected: true,
		metaLastUpdate:  time.Now(),
	}

	require.NotPanics(t, func() {
		err := c.SendMessage(nil)
		require.ErrorIs(t, err, ErrDisconnected)
	})
}

func TestConnection_TcpAddrsClearedOnDisconnect(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()

	cfg := DefaultConfig()
	cfg.ConnSendChanSize = 16
	conn, err := ConnectionFromTcpConnection(serverConn, cfg)
	require.NoError(t, err)

	require.True(t, conn.IsConnect())
	require.NotNil(t, conn.TcpRemoteAddr())
	require.NotNil(t, conn.TcpLocalAddr())

	require.NoError(t, conn.Disconnect())
	require.False(t, conn.IsConnect())
	require.Nil(t, conn.TcpRemoteAddr())
	require.Nil(t, conn.TcpLocalAddr())
}
