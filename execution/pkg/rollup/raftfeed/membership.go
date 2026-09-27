package raftfeed

import (
	"sync"
	"time"

	"github.com/hashicorp/raft"
)

// adminNodeID is the sender id of operator tooling (`rollup-cluster`). It is not a cluster member; what
// authenticates it is the same shared HMAC secret as every member.
const adminNodeID = "admin"

const membershipCacheTTL = 300 * time.Millisecond

// membership is a short-lived cache of the Raft configuration (GetConfiguration is a round trip to the Raft
// main loop, and the HTTP handlers ask for it on every request from a member that is not in the static map).
type membership struct {
	mu      sync.Mutex
	at      time.Time
	servers []raft.Server
}

// servers returns the current Raft configuration (voters and non-voters).
func (n *Node) servers() []raft.Server {
	n.member.mu.Lock()
	defer n.member.mu.Unlock()
	if time.Since(n.member.at) < membershipCacheTTL && n.member.servers != nil {
		return n.member.servers
	}
	if f := n.raft.GetConfiguration(); f.Error() == nil {
		n.member.servers = append([]raft.Server(nil), f.Configuration().Servers...)
		n.member.at = time.Now()
	}
	return n.member.servers
}

// invalidateMembership forces the next servers() call to re-read the configuration (after a membership change).
func (n *Node) invalidateMembership() {
	n.member.mu.Lock()
	n.member.at = time.Time{}
	n.member.mu.Unlock()
}

func (n *Node) voters() []raft.Server {
	var out []raft.Server
	for _, s := range n.servers() {
		if s.Suffrage == raft.Voter {
			out = append(out, s)
		}
	}
	return out
}

// forwardAddrFor resolves the internal endpoint (submit / hash / admin) of a member: the static peers list
// first, then the derived host:(raft port + forward_port_offset) for members added at runtime.
func (n *Node) forwardAddrFor(id raft.ServerID) (string, bool) {
	if a, ok := n.forwardAddr[id]; ok && a != "" {
		return a, true
	}
	if n.forwardAddrOf != nil {
		if a, ok := n.forwardAddrOf(id); ok {
			return a, true
		}
	}
	if n.cfg.ForwardPortOffset > 0 {
		for _, s := range n.servers() {
			if s.ID == id {
				return derivedForwardAddr(string(s.Address), n.cfg.ForwardPortOffset)
			}
		}
	}
	return "", false
}

// knownNode reports whether a sender id may use the internal endpoints: the operator tool, a member of the
// static peers list, or a member of the current Raft configuration.
func (n *Node) knownNode(id string) bool {
	if id == adminNodeID {
		return true
	}
	if _, ok := n.forwardAddr[raft.ServerID(id)]; ok {
		return true
	}
	for _, s := range n.servers() {
		if string(s.ID) == id {
			return true
		}
	}
	return false
}
