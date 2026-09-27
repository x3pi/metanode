package raftfeed

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/hashicorp/raft"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// Follower -> leader submit channel (SEQUENCER_SCHEMAS §1.5). A dedicated internal port, authenticated with an
// HMAC over (node id, timestamp, body hash). The timestamp only limits replay of the internal channel; it never
// reaches a batch.
const (
	submitPath     = "/raft/v1/submit"
	hdrNode        = "X-Rollup-Node"
	hdrTs          = "X-Rollup-Ts"
	hdrMac         = "X-Rollup-Mac"
	hdrLeader      = "X-Rollup-Leader"
	maxForwardSkew = 30 * time.Second
	// the client must outlast the leader's own wait for the commit
	forwardTimeout = submitCommitTimeout + 3*time.Second
	maxForwardBody = 64 << 20 // hard cap on what the internal port will read
)

func forwardMAC(secret []byte, nodeID string, tsMs int64, body []byte) string {
	sum := sha256.Sum256(body)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(nodeID))
	m.Write([]byte(strconv.FormatInt(tsMs, 10)))
	m.Write(sum[:])
	return hex.EncodeToString(m.Sum(nil))
}

// submitStatus is what the leader answers for a batch (mirrors the HTTP codes of §1.5).
type submitStatus int

const (
	statusAccepted submitStatus = http.StatusOK
	statusRedirect submitStatus = http.StatusTemporaryRedirect
	statusTooLarge submitStatus = http.StatusRequestEntityTooLarge
	statusFull     submitStatus = http.StatusTooManyRequests
	statusNoLeader submitStatus = http.StatusServiceUnavailable
	// statusUncommitted: queued but not committed (leadership lost, or no quorum within submitCommitTimeout).
	statusUncommitted submitStatus = http.StatusGatewayTimeout
)

func (n *Node) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxForwardBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	sender := r.Header.Get(hdrNode)
	ts, err := strconv.ParseInt(r.Header.Get(hdrTs), 10, 64)
	skew := n.now().Sub(time.UnixMilli(ts))
	_, known := n.forwardAddr[raft.ServerID(sender)]
	mac, macErr := hex.DecodeString(r.Header.Get(hdrMac))
	want, _ := hex.DecodeString(forwardMAC(n.secret, sender, ts, body))
	if err != nil || macErr != nil || !known || skew > maxForwardSkew || skew < -maxForwardSkew || !hmac.Equal(mac, want) {
		logger.Error("❌ [RAFT-FWD] rejected submit from %q (bad mac / unknown node / clock skew %v)", sender, skew)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch st := n.submitLocal(body); st {
	case statusRedirect:
		if addr, ok := n.leaderForwardAddr(); ok {
			w.Header().Set(hdrLeader, addr)
		}
		w.WriteHeader(int(st))
	default:
		w.WriteHeader(int(st))
	}
}

// forwardToLeader sends the batch to the current leader. It returns true only when the leader accepted it.
func (n *Node) forwardToLeader(batch []byte) bool {
	addr, ok := n.leaderForwardAddr()
	if !ok {
		return false
	}
	ts := n.now().UnixMilli()
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+submitPath, bytes.NewReader(batch))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(hdrNode, n.cfg.NodeID)
	req.Header.Set(hdrTs, strconv.FormatInt(ts, 10))
	req.Header.Set(hdrMac, forwardMAC(n.secret, n.cfg.NodeID, ts, batch))
	resp, err := n.client.Do(req)
	if err != nil {
		return false // leader unreachable: the caller keeps the batch and retries
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	switch resp.StatusCode {
	case http.StatusOK:
		return true
	case http.StatusRequestEntityTooLarge:
		// Retrying can never succeed and the forwarder retries a false forever: drop loudly instead.
		n.dropped.Add(1)
		logger.Error("❌ [RAFT-FWD] leader refused a batch as too large; dropping it")
		return true
	case http.StatusUnauthorized:
		logger.Error("❌ [RAFT-FWD] leader rejected our MAC/clock (check forward_secret_file and system time)")
	}
	return false // 307 (leader moved), 429 (queue full), 503 (no leader): backpressure, keep the batch
}

func (n *Node) leaderForwardAddr() (string, bool) {
	_, id := n.raft.LeaderWithID()
	if id == "" {
		return "", false
	}
	a, ok := n.forwardAddr[id]
	return a, ok
}

func macEqual(a, b []byte) bool { return hmac.Equal(a, b) }
