package network

import (
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
