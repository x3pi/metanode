package raftfeed

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AdminClient talks to the internal endpoints of replicas (status, leadership and membership operations) and
// implements the operator workflows of plan C4 (`rollup-cluster`): every workflow computes its steps first, refuses
// when a safety condition does not hold, and only executes them when it is not a dry run.
type AdminClient struct {
	Secret   []byte
	SenderID string // defaults to "admin"
	HTTP     *http.Client
	Now      func() time.Time
	// CatchUpWait bounds how long AddReplica waits for a new replica to catch up before it refuses to promote it.
	CatchUpWait time.Duration
}

// Member is one replica as the operator knows it.
type Member struct {
	ID        string `json:"id"`
	RaftAddr  string `json:"raft_addr"`
	AdminAddr string `json:"admin_addr"` // internal endpoint (forward address)
}

func (c *AdminClient) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (c *AdminClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *AdminClient) sender() string {
	if c.SenderID != "" {
		return c.SenderID
	}
	return adminNodeID
}

// do sends an authenticated request; the MAC covers the raw query and the body.
func (c *AdminClient) do(method, addr, path, rawQuery string, body []byte) (int, http.Header, []byte, error) {
	ts := c.now().UnixMilli()
	u := "http://" + addr + path
	if rawQuery != "" {
		u += "?" + rawQuery
	}
	req, err := http.NewRequest(method, u, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set(hdrNode, c.sender())
	req.Header.Set(hdrTs, strconv.FormatInt(ts, 10))
	req.Header.Set(hdrMac, forwardMAC(c.Secret, c.sender(), ts, path, append([]byte(rawQuery), body...)))
	resp, err := c.client().Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, resp.Header, b, nil
}

// Status fetches a replica's status.
func (c *AdminClient) Status(addr string) (Status, error) {
	code, _, b, err := c.do(http.MethodGet, addr, adminStatusPath, "", nil)
	if err != nil {
		return Status{}, err
	}
	if code != http.StatusOK {
		return Status{}, fmt.Errorf("status: HTTP %d", code)
	}
	var s Status
	if err := json.Unmarshal(b, &s); err != nil {
		return Status{}, err
	}
	return s, nil
}

// BlockHash asks a replica for the header hash of block n (ok=false when it does not have it).
func (c *AdminClient) BlockHash(addr string, n uint64) (string, bool) {
	q := strconv.FormatUint(n, 10)
	ts := c.now().UnixMilli()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+hashPath+"?n="+q, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set(hdrNode, c.sender())
	req.Header.Set(hdrTs, strconv.FormatInt(ts, 10))
	req.Header.Set(hdrMac, forwardMAC(c.Secret, c.sender(), ts, hashPath, []byte(q)))
	resp, err := c.client().Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 128))
	return string(b), true
}

// op posts a leader-only operation, following one "not the leader" redirect.
func (c *AdminClient) op(addr, path string, q url.Values) error {
	for hop := 0; hop < 2; hop++ {
		code, hdr, b, err := c.do(http.MethodPost, addr, path, q.Encode(), nil)
		if err != nil {
			return err
		}
		switch code {
		case http.StatusOK:
			return nil
		case http.StatusTemporaryRedirect:
			if l := hdr.Get(hdrLeader); l != "" {
				addr = l
				continue
			}
		}
		var r adminResult
		_ = json.Unmarshal(b, &r)
		if r.Error == "" {
			r.Error = fmt.Sprintf("HTTP %d", code)
		}
		return errors.New(r.Error)
	}
	return errors.New("no leader reachable")
}

// ---- cluster view ---------------------------------------------------------------------------------------------

// ReplicaReport is one line of the `check` output.
type ReplicaReport struct {
	ID         string `json:"id"`
	Reachable  bool   `json:"reachable"`
	Error      string `json:"error,omitempty"`
	Voter      bool   `json:"voter"`
	State      string `json:"state,omitempty"`
	Status     Status `json:"status"`
	LagEntries int64  `json:"lag_entries"` // leader commit index - this replica's applied index
	LagBlocks  int64  `json:"lag_blocks"`  // leader's last durable block - this replica's (execution progress)
}

// CheckReport is the machine-readable result of `check` (SEQUENCER schema 1.10). It changes nothing.
type CheckReport struct {
	Leader              string          `json:"leader"`
	Term                uint64          `json:"term"`
	Replicas            []ReplicaReport `json:"replicas"`
	CheckpointBlock     uint64          `json:"checkpoint_block"`
	StateRootConsistent *bool           `json:"state_root_consistent"` // nil when there is no common checkpoint yet
	KeyConsistent       bool            `json:"key_consistent"`
	QuorumOK            bool            `json:"quorum_ok"`
	Problems            []string        `json:"problems,omitempty"`
}

func (r *CheckReport) replica(id string) *ReplicaReport {
	for i := range r.Replicas {
		if r.Replicas[i].ID == id {
			return &r.Replicas[i]
		}
	}
	return nil
}

// Check gathers the status of every member and derives the cluster-level verdicts.
func (c *AdminClient) Check(members []Member) CheckReport {
	rep := CheckReport{KeyConsistent: true}
	byID := map[string]Member{}
	for _, m := range members {
		byID[m.ID] = m
		rr := ReplicaReport{ID: m.ID}
		s, err := c.Status(m.AdminAddr)
		if err != nil {
			rr.Error = err.Error()
		} else {
			rr.Reachable, rr.Status, rr.State = true, s, s.State
		}
		rep.Replicas = append(rep.Replicas, rr)
	}
	sort.Slice(rep.Replicas, func(i, j int) bool { return rep.Replicas[i].ID < rep.Replicas[j].ID })

	// The configuration as the leader sees it decides who is a voter.
	var leader *ReplicaReport
	for i := range rep.Replicas {
		if rep.Replicas[i].Reachable && rep.Replicas[i].State == "Leader" {
			if leader != nil {
				rep.Problems = append(rep.Problems, "more than one replica reports itself leader (a stale leader is still up)")
			}
			leader = &rep.Replicas[i]
		}
	}
	voter := map[string]bool{}
	if leader != nil {
		rep.Leader, rep.Term = leader.ID, leader.Status.Term
		for _, s := range leader.Status.Servers {
			voter[s.ID] = s.Voter
		}
	} else {
		rep.Problems = append(rep.Problems, "no leader")
	}
	voters, liveVoters := 0, 0
	minBlock, haveBlock := uint64(0), false
	seq := ""
	for i := range rep.Replicas {
		r := &rep.Replicas[i]
		r.Voter = voter[r.ID]
		if r.Voter {
			voters++
		}
		if !r.Reachable {
			if r.Voter {
				rep.Problems = append(rep.Problems, fmt.Sprintf("voter %s is unreachable", r.ID))
			}
			continue
		}
		if r.Status.Failed {
			rep.Problems = append(rep.Problems, fmt.Sprintf("replica %s has failed (stopped itself)", r.ID))
		} else if r.Voter {
			liveVoters++
		}
		if leader != nil {
			r.LagEntries = int64(leader.Status.CommitIndex) - int64(r.Status.AppliedIndex)
			r.LagBlocks = int64(leader.Status.LastBlock) - int64(r.Status.LastBlock)
		}
		if seq == "" {
			seq = r.Status.SequencerAddress
		} else if r.Status.SequencerAddress != seq {
			rep.KeyConsistent = false
			rep.Problems = append(rep.Problems, "replicas disagree on the sequencer address (different signing keys)")
		}
		if !haveBlock || r.Status.LastBlock < minBlock {
			minBlock, haveBlock = r.Status.LastBlock, true
		}
	}
	rep.QuorumOK = voters > 0 && liveVoters >= voters/2+1
	if !rep.QuorumOK {
		rep.Problems = append(rep.Problems, "the live voters are not a majority")
	}
	rep.CheckpointBlock = minBlock / attestStride * attestStride
	if rep.CheckpointBlock > 0 {
		hashes := map[string]int{}
		asked := 0
		for _, r := range rep.Replicas {
			if !r.Reachable {
				continue
			}
			if h, ok := c.BlockHash(byID[r.ID].AdminAddr, rep.CheckpointBlock); ok {
				hashes[h]++
				asked++
			}
		}
		if asked > 0 {
			ok := len(hashes) == 1
			rep.StateRootConsistent = &ok
			if !ok {
				rep.Problems = append(rep.Problems, fmt.Sprintf("replicas hold different hashes for block %d", rep.CheckpointBlock))
			}
		}
	}
	return rep
}

func (r *CheckReport) leaderMember(members []Member) (Member, bool) {
	for _, m := range members {
		if m.ID == r.Leader {
			return m, true
		}
	}
	return Member{}, false
}

func memberByID(members []Member, id string) (Member, bool) {
	for _, m := range members {
		if m.ID == id {
			return m, true
		}
	}
	return Member{}, false
}

// ---- workflows --------------------------------------------------------------------------------------------------

// TransferLeader moves leadership to voter `to` after draining the leader. It refuses when the target is not a
// reachable, caught-up voter or the cluster has no quorum. Returns the steps (all of them, also on a dry run).
func (c *AdminClient) TransferLeader(members []Member, to string, dryRun bool) ([]string, error) {
	rep := c.Check(members)
	if rep.Leader == "" {
		return nil, errors.New("refused: no leader")
	}
	if !rep.QuorumOK {
		return nil, errors.New("refused: the live voters are not a majority")
	}
	if to == rep.Leader {
		return nil, fmt.Errorf("refused: %s is already the leader", to)
	}
	t := rep.replica(to)
	if t == nil || !t.Voter {
		return nil, fmt.Errorf("refused: %s is not a voter of the cluster", to)
	}
	if !t.Reachable || t.Status.Failed {
		return nil, fmt.Errorf("refused: target %s is not healthy (%s)", to, t.Error)
	}
	if t.LagEntries > promoteMaxLag || t.LagBlocks > promoteMaxLag {
		return nil, fmt.Errorf("refused: target %s is behind the leader (%d entries, %d executed blocks; limit %d)", to, t.LagEntries, t.LagBlocks, promoteMaxLag)
	}
	steps := []string{
		fmt.Sprintf("drain %s (stop accepting new batches, wait until everything accepted is applied)", rep.Leader),
		fmt.Sprintf("transfer leadership %s -> %s", rep.Leader, to),
		fmt.Sprintf("wait until %s reports itself leader", to),
		fmt.Sprintf("%s resumes as follower (its Submit forwards to the new leader)", rep.Leader),
	}
	if dryRun {
		return steps, nil
	}
	lm, _ := rep.leaderMember(members)
	if err := c.op(lm.AdminAddr, adminTransferPath, url.Values{"to": {to}}); err != nil {
		return steps, err
	}
	tm, _ := memberByID(members, to)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := c.Status(tm.AdminAddr); err == nil && s.State == "Leader" {
			return steps, nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return steps, fmt.Errorf("%s did not become leader", to)
}

// AddReplica adds `nm` (a running replica started with join_existing_chain and a state copied from a stopped
// peer, or with an empty state when the leader's log is complete) as a non-voter, waits until it caught up, checks
// that its state is this chain's, and only then promotes it to voter.
func (c *AdminClient) AddReplica(members []Member, nm Member, dryRun bool) ([]string, error) {
	all := append(append([]Member(nil), members...), nm)
	rep := c.Check(members)
	if rep.Leader == "" || !rep.QuorumOK {
		return nil, errors.New("refused: the cluster has no leader or no quorum")
	}
	lm, _ := rep.leaderMember(members)
	ls := rep.replica(rep.Leader).Status
	for _, s := range ls.Servers {
		if s.ID == nm.ID && s.Voter {
			return nil, fmt.Errorf("refused: %s is already a voter", nm.ID)
		}
	}
	ns, err := c.Status(nm.AdminAddr)
	if err != nil {
		return nil, fmt.Errorf("refused: the new replica %s is not reachable at %s: %w", nm.ID, nm.AdminAddr, err)
	}
	if ns.Failed {
		return nil, fmt.Errorf("refused: the new replica %s has failed", nm.ID)
	}
	if ns.LastBlock > ls.LastBlock+promoteMaxLag {
		return nil, fmt.Errorf("refused: the new replica's state (block %d) is ahead of the leader's (block %d): it is not a copy of this chain", ns.LastBlock, ls.LastBlock)
	}
	if ls.FirstLogIndex > 1 && ns.LastBlock < ls.SnapshotBlock {
		return nil, fmt.Errorf("refused: the leader's log is compacted (first index %d, snapshot covers block %d) and the new replica's state stops at block %d: hold snapshots (rollup-cluster hold-snapshots on), copy a fresh state (fetch-state / prepare-replica), start the replica and retry", ls.FirstLogIndex, ls.SnapshotBlock, ns.LastBlock)
	}
	if err := c.sameChainAt(all, lm, nm, ns); err != nil {
		return nil, err
	}
	steps := []string{
		fmt.Sprintf("add %s (%s) as a NON-voter", nm.ID, nm.RaftAddr),
		fmt.Sprintf("wait until %s applied within %d entries of the commit index AND executed within %d blocks of the leader", nm.ID, promoteMaxLag, promoteMaxLag),
		fmt.Sprintf("check %s's block hash equals the leader's at its tip", nm.ID),
		fmt.Sprintf("promote %s to voter (it counts toward the majority only from here)", nm.ID),
		"check the cluster",
	}
	if dryRun {
		return steps, nil
	}
	already := false
	for _, s := range ls.Servers {
		if s.ID == nm.ID {
			already = true
		}
	}
	if !already {
		if err := c.op(lm.AdminAddr, adminAddPath, url.Values{"id": {nm.ID}, "addr": {nm.RaftAddr}, "voter": {"0"}}); err != nil {
			return steps, err
		}
	}
	wait := c.CatchUpWait
	if wait == 0 {
		wait = 120 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		cur := c.Check(members)
		l := cur.replica(cur.Leader)
		s, err := c.Status(nm.AdminAddr)
		if err == nil && l != nil && !s.Failed && s.AppliedIndex+promoteMaxLag >= l.Status.CommitIndex && s.LastBlock+promoteMaxLag >= l.Status.LastBlock {
			break
		}
		if err == nil && s.Failed {
			return steps, fmt.Errorf("the new replica %s stopped itself (failed) while catching up", nm.ID)
		}
		if time.Now().After(deadline) {
			return steps, fmt.Errorf("the new replica %s did not catch up within %v; it stays a non-voter", nm.ID, wait)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := c.sameChainAt(all, lm, nm, Status{}); err != nil {
		return steps, err
	}
	if err := c.op(lm.AdminAddr, adminAddPath, url.Values{"id": {nm.ID}, "addr": {nm.RaftAddr}, "voter": {"1"}}); err != nil {
		return steps, err
	}
	return steps, nil
}

// sameChainAt compares the new replica's block hash with the leader's at the new replica's tip: a state copied
// from another chain (or a corrupted copy) is refused before it can vote.
func (c *AdminClient) sameChainAt(all []Member, lm, nm Member, ns Status) error {
	if ns.LastBlock == 0 {
		cur, err := c.Status(nm.AdminAddr)
		if err != nil {
			return err
		}
		ns = cur
	}
	if ns.LastBlock == 0 {
		return nil // empty state: it will replay the log from the start
	}
	mine, ok := c.BlockHash(nm.AdminAddr, ns.LastBlock)
	if !ok {
		return fmt.Errorf("refused: the new replica cannot report the hash of its own tip block %d", ns.LastBlock)
	}
	theirs, ok := c.BlockHash(lm.AdminAddr, ns.LastBlock)
	if !ok {
		return fmt.Errorf("refused: the leader has no block %d to compare with", ns.LastBlock)
	}
	if mine != theirs {
		return fmt.Errorf("refused: block %d differs between the new replica (%s) and the leader (%s): its state is not a copy of this chain", ns.LastBlock, mine[:14], theirs[:14])
	}
	return nil
}

// RemoveReplica removes a replica. It refuses when the live voters would no longer be a majority; removing the
// leader needs transferFirst (then leadership moves to the most caught-up other voter first).
func (c *AdminClient) RemoveReplica(members []Member, id string, transferFirst, dryRun bool) ([]string, error) {
	rep := c.Check(members)
	if rep.Leader == "" {
		return nil, errors.New("refused: no leader")
	}
	var infos []ServerInfo
	live := map[string]bool{}
	for _, s := range rep.replica(rep.Leader).Status.Servers {
		infos = append(infos, s)
	}
	for _, r := range rep.Replicas {
		if r.Reachable && !r.Status.Failed {
			live[r.ID] = true
		}
	}
	if err := RemovalSafe(infos, live, id); err != nil {
		return nil, fmt.Errorf("refused: %w", err)
	}
	var steps []string
	lm, _ := rep.leaderMember(members)
	if id == rep.Leader {
		if !transferFirst {
			return nil, errors.New("refused: this replica is the leader; use --transfer-first")
		}
		var best *ReplicaReport
		for i := range rep.Replicas {
			r := &rep.Replicas[i]
			if r.ID != id && r.Voter && r.Reachable && !r.Status.Failed && (best == nil || r.Status.AppliedIndex > best.Status.AppliedIndex) {
				best = r
			}
		}
		if best == nil {
			return nil, errors.New("refused: no healthy voter to take over leadership")
		}
		s, err := c.TransferLeader(members, best.ID, dryRun)
		if err != nil {
			return nil, err
		}
		steps = append(steps, s...)
		if !dryRun {
			nl, _ := memberByID(members, best.ID)
			lm = nl
		}
	}
	steps = append(steps, fmt.Sprintf("remove %s from the configuration", id), "check the cluster")
	if dryRun {
		return steps, nil
	}
	if err := c.op(lm.AdminAddr, adminRemovePath, url.Values{"id": {id}}); err != nil {
		return steps, err
	}
	return steps, nil
}

// FormatSteps renders a plan for humans.
func FormatSteps(steps []string) string {
	var b strings.Builder
	for i, s := range steps {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
	}
	return b.String()
}

// ShortHTTPClient is a client for probes that must fail fast (is that peer really stopped?).
func ShortHTTPClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// HoldSnapshots asks every reachable member to stop (on) / resume (off) automatic Raft snapshots. Use it around
// building a new replica from a state copy: hold, fetch-state (or prepare-replica), start it, add-replica, release.
func (c *AdminClient) HoldSnapshots(members []Member, on bool) []error {
	v := "0"
	if on {
		v = "1"
	}
	var errs []error
	for _, m := range members {
		code, _, b, err := c.do(http.MethodPost, m.AdminAddr, adminSnapHoldPath, url.Values{"hold": {v}}.Encode(), nil)
		switch {
		case err != nil:
			errs = append(errs, fmt.Errorf("%s: %w", m.ID, err))
		case code != http.StatusOK:
			var r adminResult
			_ = json.Unmarshal(b, &r)
			errs = append(errs, fmt.Errorf("%s: %s (HTTP %d)", m.ID, r.Error, code))
		}
	}
	return errs
}
