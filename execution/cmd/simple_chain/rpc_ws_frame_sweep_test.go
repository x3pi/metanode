package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
)

// The WebSocket wrapper parses RFC 6455 frames out of raw client bytes on the Read path of a connection goroutine that
// nothing recovers, so a panic there takes the whole node down. These sweeps feed it hostile byte streams (seeded, so a
// failure reproduces) and require: no panic, no hang, and a sane count for every payload.

func wsSweepWrapper(t *testing.T) (*wsRateLimitConn, func()) {
	t.Helper()
	rl := NewRPCRateLimiter(&config.RpcRateLimitConfig{Enabled: true, GlobalRate: 1e9, PerIpRate: 1e9, BlockDurationSecs: 1})
	serverConn, clientConn := net.Pipe()
	go func() { _, _ = io.Copy(io.Discard, clientConn) }() // swallow the close frames the wrapper may write
	w := newWSRateLimitConn(serverConn, nil, "203.0.113.7", rl)
	return w, func() { _ = serverConn.Close(); _ = clientConn.Close(); rl.Close() }
}

// hostileFrames returns byte streams built around the interesting header shapes plus plain random noise.
func hostileFrames(seed int64, n int) [][]byte {
	r := rand.New(rand.NewSource(seed))
	var out [][]byte
	opcodes := []byte{0x0, 0x1, 0x2, 0x8, 0x9, 0xA, 0x3, 0x7, 0xB, 0xF}
	for i := 0; i < n; i++ {
		b0 := opcodes[r.Intn(len(opcodes))]
		if r.Intn(2) == 0 {
			b0 |= 0x80 // FIN
		}
		if r.Intn(8) == 0 {
			b0 |= byte(r.Intn(8)) << 4 // reserved bits
		}
		var hdr []byte
		mask := r.Intn(2) == 0
		mbit := byte(0)
		if mask {
			mbit = 0x80
		}
		switch r.Intn(4) {
		case 0:
			hdr = []byte{b0, mbit | byte(r.Intn(126))}
		case 1:
			hdr = []byte{b0, mbit | 126, byte(r.Intn(256)), byte(r.Intn(256))}
		case 2:
			hdr = []byte{b0, mbit | 127}
			ext := make([]byte, 8)
			r.Read(ext)
			if r.Intn(2) == 0 {
				ext[0], ext[1], ext[2], ext[3], ext[4] = 0, 0, 0, 0, 0 // keep most declared lengths below the 8 MB cap
			}
			hdr = append(hdr, ext...)
		default:
			hdr = []byte{b0, byte(r.Intn(256))}
		}
		if mask {
			k := make([]byte, 4)
			r.Read(k)
			hdr = append(hdr, k...)
		}
		body := make([]byte, r.Intn(300))
		r.Read(body)
		if r.Intn(3) == 0 {
			body = []byte(`[{"jsonrpc":"2.0","method":"eth_call","id":1},{"id":2},"x",3,[4],null]`)
		}
		out = append(out, append(hdr, body...))
	}
	for i := 0; i < n/4; i++ {
		b := make([]byte, r.Intn(64))
		r.Read(b)
		out = append(out, b)
	}
	return out
}

func TestWSFrameParserSweep_NoPanicNoHang(t *testing.T) {
	for i, data := range hostileFrames(20261021, 2500) {
		w, closeAll := wsSweepWrapper(t)
		done := make(chan interface{}, 1)
		go func() {
			defer func() { done <- recover() }()
			w.rawIn.Write(data)
			for step := 0; step < 64; step++ { // bounded: each iteration consumes a frame or stops
				f, err := w.tryReadFrame()
				if err != nil || f == nil {
					return
				}
				if err := w.handleFrame(f); err != nil {
					return
				}
			}
		}()
		select {
		case p := <-done:
			if p != nil {
				head := data
				if len(head) > 48 {
					head = head[:48]
				}
				closeAll()
				t.Fatalf("frame parser panicked on stream #%d (len %d, first bytes %x): %v", i, len(data), head, p)
			}
		case <-time.After(5 * time.Second):
			closeAll()
			t.Fatalf("frame parser hung on stream #%d (len %d)", i, len(data))
		}
		closeAll()
	}
}

func TestCountBatchItems_NeverPanicsAndMatchesObjectCount(t *testing.T) {
	r := rand.New(rand.NewSource(20261022))
	// 1) arbitrary bytes: no panic, result >= 1
	for i := 0; i < 5000; i++ {
		b := make([]byte, r.Intn(400))
		r.Read(b)
		if r.Intn(2) == 0 && len(b) > 0 {
			b[0] = '['
		}
		if c := countBatchItems(b); c < 1 {
			t.Fatalf("countBatchItems(%x) = %d, want >= 1", b, c)
		}
	}
	// 2) valid arrays of request objects (what actually does work): the count must equal the element count, however the
	// objects are padded, nested or filled with strings containing structural characters
	for i := 0; i < 2000; i++ {
		n := 1 + r.Intn(40)
		var parts []string
		for k := 0; k < n; k++ {
			s := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"eth_call","params":[{"data":"%s"},[1,2,{"a":"]}[{"}]]}`, k, strings.Repeat(`\"`, r.Intn(3)))
			parts = append(parts, s)
		}
		sep := []string{",", " , ", ",\n", "\t,\r\n"}[r.Intn(4)]
		payload := "  [" + strings.Join(parts, sep) + "]  "
		var chk []json.RawMessage
		if err := json.Unmarshal([]byte(payload), &chk); err != nil || len(chk) != n {
			t.Fatalf("generator produced invalid JSON (n=%d): %v", n, err)
		}
		if got := countBatchItems([]byte(payload)); got != n {
			t.Fatalf("countBatchItems undercounts a valid batch: got %d, want %d\n%s", got, n, payload)
		}
	}
}
