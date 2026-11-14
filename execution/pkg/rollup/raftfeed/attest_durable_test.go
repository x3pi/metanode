package raftfeed

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newHashEndpointNode builds the minimum Node the block-hash endpoint needs. Its BlockHash, like the
// production one (BlockChain.GetBlockHashByNumber), answers for every mapped block, durable or not.
func newHashEndpointNode(durable *atomic.Uint64) *Node {
	now := time.Now()
	return &Node{
		secret:  []byte("test-secret-test-secret-test-secr"),
		now:     func() time.Time { return now },
		durable: durable.Load,
		blockHash: func(n uint64) (common.Hash, bool) {
			if n == 0 || n > 100 { // mapping exists for 1..100, durable or not
				return common.Hash{}, false
			}
			var h common.Hash
			binary.BigEndian.PutUint64(h[24:], n)
			return h, true
		},
	}
}

func askHash(t *testing.T, n *Node, num uint64) (int, string) {
	t.Helper()
	q := strconv.FormatUint(num, 10)
	ts := n.now().UnixMilli()
	req := httptest.NewRequest(http.MethodGet, hashPath+"?n="+q, nil)
	req.Header.Set(hdrNode, adminNodeID)
	req.Header.Set(hdrTs, strconv.FormatInt(ts, 10))
	req.Header.Set(hdrMac, forwardMAC(n.secret, adminNodeID, ts, hashPath, []byte(q)))
	rec := httptest.NewRecorder()
	n.handleBlockHash(rec, req)
	return rec.Code, rec.Body.String()
}

func TestHashEndpoint_OnlyServesDurableBlocks(t *testing.T) {
	var durable atomic.Uint64
	durable.Store(40)
	n := newHashEndpointNode(&durable)

	code, body := askHash(t, n, 40)
	require.Equal(t, http.StatusOK, code, "durable tip must be served: %s", body)
	want, _ := n.blockHash(40)
	assert.Equal(t, want.Hex(), body)

	code, _ = askHash(t, n, 41)
	assert.Equal(t, http.StatusNotFound, code, "a mapped but not yet durable block must not be voted on")
	code, _ = askHash(t, n, 100)
	assert.Equal(t, http.StatusNotFound, code)

	// Once the commit finishes and durable() advances, the same block is served.
	durable.Store(41)
	code, body = askHash(t, n, 41)
	require.Equal(t, http.StatusOK, code)
	want, _ = n.blockHash(41)
	assert.Equal(t, want.Hex(), body)
}

func TestHashEndpoint_UnknownBlockStillNotFound(t *testing.T) {
	var durable atomic.Uint64
	durable.Store(500) // durable beyond the mapping: the mapping lookup decides
	n := newHashEndpointNode(&durable)
	code, _ := askHash(t, n, 0)
	assert.Equal(t, http.StatusNotFound, code)
	code, _ = askHash(t, n, 101)
	assert.Equal(t, http.StatusNotFound, code)
}

func TestHashEndpoint_AuthCheckedBeforeDurability(t *testing.T) {
	var durable atomic.Uint64
	durable.Store(40)
	n := newHashEndpointNode(&durable)
	req := httptest.NewRequest(http.MethodGet, hashPath+"?n=1", nil)
	req.Header.Set(hdrNode, adminNodeID)
	req.Header.Set(hdrTs, strconv.FormatInt(n.now().UnixMilli(), 10))
	req.Header.Set(hdrMac, "00")
	rec := httptest.NewRecorder()
	n.handleBlockHash(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
