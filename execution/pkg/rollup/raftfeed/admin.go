package raftfeed

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/hashicorp/raft"
)

// Operator surface of a replica (plan C4): status and membership/leadership operations over the same internal,
// HMAC-authenticated endpoint as the submit channel. The `rollup-cluster` tool drives it; every operation
// re-checks its own safety conditions here, so a client that skips its pre-checks still cannot break the cluster.
const (
	adminStatusPath   = "/raft/v1/admin/status"
	adminTransferPath = "/raft/v1/admin/transfer"
	adminAddPath      = "/raft/v1/admin/add"
	adminRemovePath   = "/raft/v1/admin/remove"

	// promoteMaxLag: a non-voter may become a voter only when its applied index is within this many entries of the
	// leader's commit index ("counts toward the majority only after it caught up").
	promoteMaxLag = 64

	drainWait    = 10 * time.Second
	transferWait = 10 * time.Second
	adminPoll    = 5 * time.Millisecond
)

// ServerInfo is one member of the Raft configuration.
type ServerInfo struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Voter   bool   `json:"voter"`
}

// Status is what a replica reports about itself (SEQUENCER schema 1.10, `check`).
type Status struct {
	NodeID            string       `json:"node_id"`
	State             string       `json:"state"`
	LeaderID          string       `json:"leader_id"`
	Term              uint64       `json:"term"`
	LastLogIndex      uint64       `json:"last_log_index"`
	CommitIndex       uint64       `json:"commit_index"`
	AppliedIndex      uint64       `json:"applied_index"`
	FirstLogIndex     uint64       `json:"first_log_index"`
	LastSnapshotIndex uint64       `json:"last_snapshot_index"`
	SnapshotBlock     uint64       `json:"snapshot_block"`
	LastBlock         uint64       `json:"last_block"`
	Servers           []ServerInfo `json:"servers"`
	Draining          bool         `json:"draining"`
	Failed            bool         `json:"failed"`
	SequencerAddress  string       `json:"sequencer_address"`
	Attested          uint64       `json:"attested"`
	Mismatches        uint64       `json:"mismatches"`
}

func statUint(m map[string]string, k string) uint64 {
	v, _ := strconv.ParseUint(m[k], 10, 64)
	return v
}

// Status reports this replica's view.
func (n *Node) Status() Status {
	st := n.raft.Stats()
	_, leaderID := n.raft.LeaderWithID()
	s := Status{
		NodeID:            n.cfg.NodeID,
		State:             n.raft.State().String(),
		LeaderID:          string(leaderID),
		Term:              statUint(st, "term"),
		LastLogIndex:      statUint(st, "last_log_index"),
		CommitIndex:       statUint(st, "commit_index"),
		AppliedIndex:      statUint(st, "applied_index"),
		LastSnapshotIndex: statUint(st, "last_snapshot_index"),
		SnapshotBlock:     n.fsm.snapshotBlock.Load(),
		Draining:          n.draining.Load(),
		Failed:            n.failed.Load(),
		SequencerAddress:  n.cfg.SequencerAddress,
		Attested:          n.attested.Load(),
		Mismatches:        n.mismatches.Load(),
	}
	if n.durable != nil {
		s.LastBlock = n.durable()
	}
	if n.logStore != nil {
		s.FirstLogIndex, _ = n.logStore.FirstIndex()
	}
	for _, sv := range n.servers() {
		s.Servers = append(s.Servers, ServerInfo{ID: string(sv.ID), Address: string(sv.Address), Voter: sv.Suffrage == raft.Voter})
	}
	return s
}

// authorizeAdmin checks the HMAC of an operator/peer request: MAC over (sender, ts, payload) where payload is the
// raw query string plus the body.
func (n *Node) authorizeAdmin(r *http.Request, body []byte) bool {
	sender := r.Header.Get(hdrNode)
	ts, err := strconv.ParseInt(r.Header.Get(hdrTs), 10, 64)
	if err != nil {
		return false
	}
	skew := n.now().Sub(time.UnixMilli(ts))
	if skew > maxForwardSkew || skew < -maxForwardSkew || !n.knownNode(sender) {
		return false
	}
	got, err := hex.DecodeString(r.Header.Get(hdrMac))
	if err != nil {
		return false
	}
	want, _ := hex.DecodeString(forwardMAC(n.secret, sender, ts, append([]byte(r.URL.RawQuery), body...)))
	return macEqual(got, want)
}

func (n *Node) adminHandler(op func(w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil || !n.authorizeAdmin(r, body) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		op(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type adminResult struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// leaderOnly answers 307 with the leader's endpoint when this replica is not the leader.
func (n *Node) leaderOnly(w http.ResponseWriter) bool {
	if n.raft.State() == raft.Leader {
		return true
	}
	if a, ok := n.leaderForwardAddr(); ok {
		w.Header().Set(hdrLeader, a)
		writeJSON(w, http.StatusTemporaryRedirect, adminResult{Error: "not the leader"})
	} else {
		writeJSON(w, http.StatusServiceUnavailable, adminResult{Error: "no leader"})
	}
	return false
}

func (n *Node) registerAdmin(mux *http.ServeMux) {
	mux.HandleFunc(adminStatusPath, n.adminHandler(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, n.Status())
	}))
	respond := func(w http.ResponseWriter, err error) {
		if err != nil {
			writeJSON(w, http.StatusConflict, adminResult{Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, adminResult{OK: true})
	}
	mux.HandleFunc(adminTransferPath, n.adminHandler(func(w http.ResponseWriter, r *http.Request) {
		if !n.leaderOnly(w) {
			return
		}
		respond(w, n.TransferLeadership(r.URL.Query().Get("to")))
	}))
	mux.HandleFunc(adminAddPath, n.adminHandler(func(w http.ResponseWriter, r *http.Request) {
		if !n.leaderOnly(w) {
			return
		}
		q := r.URL.Query()
		respond(w, n.AddMember(q.Get("id"), q.Get("addr"), q.Get("voter") == "1"))
	}))
	mux.HandleFunc(adminRemovePath, n.adminHandler(func(w http.ResponseWriter, r *http.Request) {
		if !n.leaderOnly(w) {
			return
		}
		respond(w, n.RemoveMember(r.URL.Query().Get("id")))
	}))
}

// peerStatus fetches another member's status over the internal channel.
func (n *Node) peerStatus(id raft.ServerID) (Status, error) {
	addr, ok := n.forwardAddrFor(id)
	if !ok {
		return Status{}, fmt.Errorf("no internal endpoint known for %s", id)
	}
	c := &AdminClient{Secret: n.secret, SenderID: n.cfg.NodeID, HTTP: n.client, Now: n.now}
	return c.Status(addr)
}

// drain stops accepting new batches and waits (bounded) until everything already accepted is committed and applied.
func (n *Node) drain() error {
	n.draining.Store(true)
	deadline := time.Now().Add(drainWait)
	for time.Now().Before(deadline) {
		if len(n.proposeQ) == 0 && len(n.inflight) == 0 && n.raft.AppliedIndex() >= n.raft.LastIndex() {
			return nil
		}
		time.Sleep(adminPoll)
	}
	n.draining.Store(false)
	return errors.New("could not drain the leader within the wait limit")
}

// TransferLeadership hands leadership to a voter after draining, so no accepted batch is in flight on the old leader.
func (n *Node) TransferLeadership(to string) error {
	if n.raft.State() != raft.Leader {
		return errors.New("not the leader")
	}
	if to == "" || to == n.cfg.NodeID {
		return errors.New("target must be another member")
	}
	var target *raft.Server
	for _, s := range n.servers() {
		if string(s.ID) == to {
			s := s
			target = &s
		}
	}
	if target == nil || target.Suffrage != raft.Voter {
		return fmt.Errorf("%s is not a voter of the cluster", to)
	}
	ps, err := n.peerStatus(target.ID)
	if err != nil {
		return fmt.Errorf("target %s is not reachable: %w", to, err)
	}
	if ps.Failed {
		return fmt.Errorf("target %s has failed", to)
	}
	if err := n.drain(); err != nil {
		return err
	}
	defer n.draining.Store(false)
	if err := n.raft.LeadershipTransferToServer(target.ID, target.Address).Error(); err != nil {
		return fmt.Errorf("leadership transfer: %w", err)
	}
	deadline := time.Now().Add(transferWait)
	for n.raft.State() == raft.Leader && time.Now().Before(deadline) {
		time.Sleep(adminPoll)
	}
	if n.raft.State() == raft.Leader {
		return errors.New("leadership did not move within the wait limit")
	}
	return nil
}

// AddMember adds a replica as a non-voter, or promotes an existing non-voter to voter once it is caught up.
// Adding straight to voter is refused: a replica that has not caught up must not count toward the majority.
func (n *Node) AddMember(id, addr string, voter bool) error {
	if n.raft.State() != raft.Leader {
		return errors.New("not the leader")
	}
	if id == "" || addr == "" {
		return errors.New("id and addr are required")
	}
	var existing *raft.Server
	for _, s := range n.servers() {
		if string(s.ID) == id {
			s := s
			existing = &s
		}
	}
	switch {
	case !voter:
		if existing != nil {
			return fmt.Errorf("%s is already a member", id)
		}
		if err := n.raft.AddNonvoter(raft.ServerID(id), raft.ServerAddress(addr), 0, 0).Error(); err != nil {
			return err
		}
	case existing == nil:
		return fmt.Errorf("%s is not a member yet: add it as a non-voter first and promote it after it caught up", id)
	case existing.Suffrage == raft.Voter:
		return nil // already a voter
	default:
		ps, err := n.peerStatus(existing.ID)
		if err != nil {
			return fmt.Errorf("cannot promote %s: %w", id, err)
		}
		if ps.Failed {
			return fmt.Errorf("cannot promote %s: it has failed", id)
		}
		commit := n.raft.CommitIndex()
		if ps.AppliedIndex+promoteMaxLag < commit {
			return fmt.Errorf("cannot promote %s: applied index %d is %d behind the commit index %d (limit %d)", id, ps.AppliedIndex, commit-ps.AppliedIndex, commit, promoteMaxLag)
		}
		// The Raft applied index only says the entries reached the FSM, which hands blocks to the execution pipeline:
		// a replica replaying a long log is "applied" long before it has EXECUTED. It must have executed (made
		// durable) nearly everything before it may count toward the majority.
		if mine := n.durable(); ps.LastBlock+promoteMaxLag < mine {
			return fmt.Errorf("cannot promote %s: it has executed up to block %d, %d behind this replica's block %d (limit %d)", id, ps.LastBlock, mine-ps.LastBlock, mine, promoteMaxLag)
		}
		if err := n.raft.AddVoter(existing.ID, existing.Address, 0, 0).Error(); err != nil {
			return err
		}
	}
	n.invalidateMembership()
	return nil
}

// RemoveMember removes a replica from the configuration. It refuses to remove the leader (transfer first) and
// any removal after which fewer live voters than the new majority would remain.
func (n *Node) RemoveMember(id string) error {
	if n.raft.State() != raft.Leader {
		return errors.New("not the leader")
	}
	if id == n.cfg.NodeID {
		return errors.New("cannot remove the leader: transfer leadership first")
	}
	servers := n.servers()
	var infos []ServerInfo
	live := map[string]bool{n.cfg.NodeID: true}
	for _, s := range servers {
		infos = append(infos, ServerInfo{ID: string(s.ID), Address: string(s.Address), Voter: s.Suffrage == raft.Voter})
		if string(s.ID) == n.cfg.NodeID {
			continue
		}
		if ps, err := n.peerStatus(s.ID); err == nil && !ps.Failed {
			live[string(s.ID)] = true
		}
	}
	if err := RemovalSafe(infos, live, id); err != nil {
		return err
	}
	if err := n.raft.RemoveServer(raft.ServerID(id), 0, 0).Error(); err != nil {
		return err
	}
	n.invalidateMembership()
	return nil
}

// RemovalSafe is the safety rule for removing a member (also used by the tool's pre-check and --dry-run):
// the member must exist and, afterwards, the live voters must still be a majority of the remaining voters.
func RemovalSafe(servers []ServerInfo, live map[string]bool, remove string) error {
	found := false
	voters, liveVoters := 0, 0
	for _, s := range servers {
		if s.ID == remove {
			found = true
			continue
		}
		if s.Voter {
			voters++
			if live[s.ID] {
				liveVoters++
			}
		}
	}
	if !found {
		return fmt.Errorf("%s is not a member", remove)
	}
	if voters == 0 {
		return errors.New("refusing to remove the last voter")
	}
	if liveVoters < voters/2+1 {
		return fmt.Errorf("removing %s would leave %d live voters of %d, below the majority %d: the cluster would lose its quorum", remove, liveVoters, voters, voters/2+1)
	}
	return nil
}
