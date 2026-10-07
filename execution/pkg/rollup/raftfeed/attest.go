package raftfeed

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// Cross-replica check of the executed chain. Raft only guarantees that every replica applies the same batches in
// the same order; whether they also EXECUTE them to the same state is up to the execution layer. Each replica
// periodically compares the header hash of a recent block (which covers the state roots) with its peers'. A
// replica whose hash differs from a value held by a majority of the cluster stops (fail closed); with no majority
// (peers down, lagging or split) it decides nothing and stays pending — it never guesses.
const (
	hashPath     = "/raft/v1/blockhash"
	attestStride = 10 // checkpoints are multiples of this block number
)

// attestInterval only paces the periodic check; it never decides a dispatch (a variable so tests can speed it up).
var attestInterval = 1 * time.Second

// BlockHashFunc returns the header hash of a committed block, ok=false if this replica does not have it (yet).
type BlockHashFunc func(blockNumber uint64) (common.Hash, bool)

func (n *Node) handleBlockHash(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("n")
	num, err := strconv.ParseUint(q, 10, 64)
	sender := r.Header.Get(hdrNode)
	ts, tsErr := strconv.ParseInt(r.Header.Get(hdrTs), 10, 64)
	skew := n.now().Sub(time.UnixMilli(ts))
	known := n.knownNode(sender)
	got, macErr := hex.DecodeString(r.Header.Get(hdrMac))
	want, _ := hex.DecodeString(forwardMAC(n.secret, sender, ts, hashPath, []byte(q)))
	if err != nil || tsErr != nil || macErr != nil || !known || skew > maxForwardSkew || skew < -maxForwardSkew || !macEqual(got, want) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if n.blockHash == nil {
		http.Error(w, "unavailable", http.StatusNotFound)
		return
	}
	h, ok := n.blockHash(num)
	if !ok {
		http.Error(w, "unknown block", http.StatusNotFound)
		return
	}
	_, _ = w.Write([]byte(h.Hex()))
}

func (n *Node) fetchPeerHash(addr string, num uint64) (string, bool) {
	q := strconv.FormatUint(num, 10)
	ts := n.now().UnixMilli()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+hashPath+"?n="+q, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set(hdrNode, n.cfg.NodeID)
	req.Header.Set(hdrTs, strconv.FormatInt(ts, 10))
	req.Header.Set(hdrMac, forwardMAC(n.secret, n.cfg.NodeID, ts, hashPath, []byte(q)))
	resp, err := n.client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 128))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func (n *Node) attestLoop() {
	t := time.NewTicker(attestInterval)
	defer t.Stop()
	var last uint64
	for {
		select {
		case <-n.stop:
			return
		case <-t.C:
			if num := n.attestCheckpoint(); num != 0 && num != last {
				if n.attestOnce(num) {
					last = num
				}
			}
		}
	}
}

func (n *Node) attestCheckpoint() uint64 {
	if n.blockHash == nil || n.durable == nil {
		return 0
	}
	return n.durable() / attestStride * attestStride
}

// attestOnce compares block num with the peers. It returns true when the check reached a verdict for this block
// (a majority value exists), false when it must be retried later.
func (n *Node) attestOnce(num uint64) bool {
	mine, ok := n.blockHash(num)
	if !ok {
		return false
	}
	votes := map[string]int{}
	voters := n.voters()
	for _, s := range voters {
		if string(s.ID) == n.cfg.NodeID {
			votes[mine.Hex()]++ // this replica votes only while it is a voter
			continue
		}
		addr, ok := n.forwardAddrFor(s.ID)
		if !ok {
			continue
		}
		if h, ok := n.fetchPeerHash(addr, num); ok {
			votes[h]++
		}
	}
	// A non-voter (a replica still catching up after being added) does not vote, but it is checked against the
	// voters' majority all the same: a joiner with a wrong copied state must stop before it is promoted.
	majority := len(voters)/2 + 1
	for h, c := range votes {
		if c < majority {
			continue
		}
		if h != mine.Hex() {
			n.mismatches.Add(1)
			n.fatal(fmt.Errorf("block %d hash %s differs from the value %s held by %d of %d replicas: this replica executed a different state", num, mine.Hex(), h, c, len(voters)))
		}
		n.attested.Add(1)
		logger.Info("✅ [RAFT-ATTEST] block %d hash %s matches the majority (%d of %d replicas)", num, h[:12], c, len(voters))
		return true
	}
	logger.Debug("[RAFT-ATTEST] block %d: no majority value yet (%v)", num, votes)
	return false
}
