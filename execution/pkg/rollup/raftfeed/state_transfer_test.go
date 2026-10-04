package raftfeed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
)

// treeSource writes a fixed tree: nested dirs, an empty file and a multi-megabyte file.
type treeSource struct{ calls int }

func (s *treeSource) Snapshot(dst string) (StateMeta, error) {
	s.calls++
	files := map[string][]byte{
		"consensus/account_state/000001.sst": bytes.Repeat([]byte("acct"), 1<<20), // 4 MiB
		"consensus/account_state/MANIFEST":   []byte("manifest-v1"),
		"consensus/nomt_db/x/ht":             bytes.Repeat([]byte{7}, 100_000),
		"history/blocks/CURRENT":             []byte("MANIFEST-000001\n"),
		"history/empty":                      {},
	}
	// a huge sparse file like a NOMT hash table: 300 MiB logical, two small data regions
	sp := filepath.Join(dst, "consensus", "nomt_db", "account_state", "ht")
	if err := os.MkdirAll(filepath.Dir(sp), 0o755); err != nil {
		return StateMeta{}, err
	}
	sf, err := os.Create(sp)
	if err != nil {
		return StateMeta{}, err
	}
	if err := sf.Truncate(300 << 20); err != nil {
		return StateMeta{}, err
	}
	sf.WriteAt(bytes.Repeat([]byte{1}, 3<<20), 5<<20)
	sf.WriteAt(bytes.Repeat([]byte{2}, 1<<20), 250<<20)
	sf.Close()
	for rel, b := range files {
		p := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return StateMeta{}, err
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return StateMeta{}, err
		}
	}
	return StateMeta{LastBlock: 42, BlockHash: "0xabc", GEI: 42}, nil
}

func sameTree(t *testing.T, a, b string) {
	t.Helper()
	list := func(root string) map[string]string {
		m := map[string]string{}
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() {
				size, _, sum, derr := describeFile(p)
				if derr != nil {
					t.Fatal(derr)
				}
				rel, _ := filepath.Rel(root, p)
				m[rel] = strconv.FormatInt(size, 10) + ":" + sum
			}
			return nil
		})
		return m
	}
	la, lb := list(a), list(b)
	if len(la) != len(lb) {
		t.Fatalf("file count differs: %d vs %d", len(la), len(lb))
	}
	for k, v := range la {
		if lb[k] != v {
			t.Fatalf("file %s differs", k)
		}
	}
}

// diskBytes is what a file really occupies (not its logical size).
func diskBytes(t *testing.T, p string) int64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(p, &st); err != nil {
		t.Fatal(err)
	}
	return st.Blocks * 512
}

func startDonor(t *testing.T, src StateSource) (*harness, *member) {
	t.Helper()
	h := newHarness(t, 3)
	h.startAll()
	l := h.leader()
	for _, m := range h.m {
		if m.node != nil && src != nil {
			m.node.state.source = src
		}
	}
	return h, l
}

func TestStateTransfer_EndToEnd(t *testing.T) {
	src := &treeSource{}
	h, l := startDonor(t, src)
	dest := filepath.Join(t.TempDir(), "newroot")
	man, err := h.admin().FetchState(l.fwdAddr, dest, FetchStateOptions{Workers: 3})
	if err != nil {
		t.Fatal(err)
	}
	if man.Meta.LastBlock != 42 || len(man.Files) != 6 {
		t.Fatalf("manifest: %+v", man)
	}
	staged := filepath.Join(l.node.state.base, man.ID, "root")
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatal("the donor kept its staging copy after the transfer")
	}
	if l.node.state.active != "" {
		t.Fatal("the donor still considers the transfer active")
	}
	// rebuild the expected tree and compare
	ref := filepath.Join(t.TempDir(), "ref")
	if _, err := (&treeSource{}).Snapshot(ref); err != nil {
		t.Fatal(err)
	}
	sameTree(t, ref, dest)
	// the sparse file stayed sparse: 300 MiB logical, only its data regions on disk
	ht := filepath.Join(dest, "consensus/nomt_db/account_state/ht")
	if st, err := os.Stat(ht); err != nil || st.Size() != 300<<20 {
		t.Fatalf("sparse file size wrong: %v %v", st, err)
	}
	if got := diskBytes(t, ht); got > 16<<20 {
		t.Fatalf("the receiver allocated %d bytes for a sparse file with ~4 MiB of data", got)
	}
	if _, err := os.Stat(dest + stateMarkerSuffix); !os.IsNotExist(err) {
		t.Fatal("the unfinished-transfer marker was left behind")
	}
}

func TestStateTransfer_ResumesKeepingVerifiedFilesAndFinishingParts(t *testing.T) {
	h, l := startDonor(t, &treeSource{})
	dest := filepath.Join(t.TempDir(), "newroot")
	ref := filepath.Join(t.TempDir(), "ref")
	if _, err := (&treeSource{}).Snapshot(ref); err != nil {
		t.Fatal(err)
	}
	// an earlier, interrupted run: one file complete, one half downloaded, a marker
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(dest, "consensus/account_state"), 0o755))
	full, _ := os.ReadFile(filepath.Join(ref, "consensus/account_state/MANIFEST"))
	must(os.WriteFile(filepath.Join(dest, "consensus/account_state/MANIFEST"), full, 0o644))
	big, _ := os.ReadFile(filepath.Join(ref, "consensus/account_state/000001.sst"))
	must(os.WriteFile(filepath.Join(dest, "consensus/account_state/000001.sst.part"), big[:len(big)/2], 0o644))
	must(os.WriteFile(dest+stateMarkerSuffix, []byte("{}"), 0o600))
	// a file of an OLDER snapshot that the donor no longer has, and an orphan part
	must(os.MkdirAll(filepath.Join(dest, "history/blocks"), 0o755))
	must(os.WriteFile(filepath.Join(dest, "history/blocks/000000-old.sst"), []byte("stale"), 0o644))
	must(os.WriteFile(filepath.Join(dest, "history/blocks/orphan.part"), []byte("junk"), 0o644))
	if _, err := h.admin().FetchState(l.fwdAddr, dest, FetchStateOptions{}); err != nil {
		t.Fatal(err)
	}
	sameTree(t, ref, dest) // stale leftovers are gone: same file set as the donor's snapshot
}

func TestStateTransfer_RefusesNonEmptyDestinationAndDisabledDonor(t *testing.T) {
	h, l := startDonor(t, &treeSource{})
	dest := t.TempDir()
	os.WriteFile(filepath.Join(dest, "stray"), []byte("x"), 0o644)
	if _, err := h.admin().FetchState(l.fwdAddr, dest, FetchStateOptions{}); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("expected a refusal for a non-empty destination, got %v", err)
	}
	for _, m := range h.m {
		m.node.state.source = nil
	}
	if _, err := h.admin().FetchState(l.fwdAddr, filepath.Join(t.TempDir(), "d"), FetchStateOptions{}); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("expected a refusal from a donor without state transfer, got %v", err)
	}
}

func TestStateTransfer_DonorEndpointsRejectBadRequests(t *testing.T) {
	h, l := startDonor(t, &treeSource{})
	c := h.admin()
	// no transfer active
	if code, _, _, _ := c.do(http.MethodGet, l.fwdAddr, stateFilePath, "id=nope&path=x", nil); code != http.StatusNotFound {
		t.Fatalf("no transfer: %d", code)
	}
	// start one and keep it active (prepare by hand)
	code, _, body, err := c.do(http.MethodPost, l.fwdAddr, statePreparePath, "", nil)
	if err != nil || code != http.StatusOK {
		t.Fatalf("prepare: %d %v", code, err)
	}
	id := jsonField(t, body, "id")
	if code, _, _, _ := c.do(http.MethodPost, l.fwdAddr, statePreparePath, "", nil); code != http.StatusConflict {
		t.Fatalf("a second concurrent prepare must be refused, got %d", code)
	}
	// a real file just OUTSIDE the served root: traversal must not reach it
	os.WriteFile(filepath.Join(l.node.state.base, id, "secret.txt"), []byte("TOP-SECRET"), 0o600)
	os.WriteFile(filepath.Join(l.node.state.base, "outside.txt"), []byte("TOP-SECRET"), 0o600)
	for _, bad := range []string{"../secret.txt", "../../outside.txt", "history/../../secret.txt", "/etc/passwd", ".."} {
		if code, _, b, _ := c.do(http.MethodGet, l.fwdAddr, stateFilePath, "id="+id+"&path="+url.QueryEscape(bad), nil); code == http.StatusOK || strings.Contains(string(b), "TOP-SECRET") {
			t.Fatalf("path %q was served (HTTP %d)", bad, code)
		}
	}
	if code, _, _, _ := c.do(http.MethodGet, l.fwdAddr, stateFilePath, "id=wrong&path=history/empty", nil); code != http.StatusNotFound {
		t.Fatalf("wrong id: %d", code)
	}
	// unauthenticated
	req, _ := http.NewRequest(http.MethodGet, "http://"+l.fwdAddr+stateFilePath+"?id="+id+"&path=history/empty", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated file request: %d", resp.StatusCode)
	}
	if code, _, _, _ := c.do(http.MethodGet, l.fwdAddr, stateFilePath, "id="+id+"&path=history/empty", nil); code != http.StatusOK {
		t.Fatalf("a valid request was refused: %d", code)
	}
	c.do(http.MethodPost, l.fwdAddr, stateReleasePath, "id="+id, nil)
	if _, err := os.Stat(filepath.Join(l.node.state.base, id)); !os.IsNotExist(err) {
		t.Fatal("release did not remove the staging copy")
	}
}

func jsonField(t *testing.T, body []byte, key string) string {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	s, _ := m[key].(string)
	return s
}

// A forged manifest (wrong MAC) is never trusted, and a file whose bytes do not match the manifest is rejected and
// discarded.
func TestStateTransfer_ClientRejectsForgedManifestAndCorruptFiles(t *testing.T) {
	secret := bytes.Repeat([]byte("k"), 32)
	c := &AdminClient{Secret: secret, SenderID: "admin"}
	man := `{"id":"x","donor_id":"n0","meta":{"last_block":1},"files":[{"path":"a","size":3,"sha256":"00"}]}`
	forged := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(hdrNode, "n0")
		w.Header().Set(hdrTs, strconv.FormatInt(time.Now().UnixMilli(), 10)) // fresh timestamp: only the MAC is wrong
		w.Header().Set(hdrMac, "deadbeef")
		w.Write([]byte(man))
	}))
	defer forged.Close()
	if _, err := c.FetchState(strings.TrimPrefix(forged.URL, "http://"), filepath.Join(t.TempDir(), "d"), FetchStateOptions{}); err == nil || !strings.Contains(err.Error(), "not authentic") {
		t.Fatalf("a forged manifest was accepted: %v", err)
	}

	bytesServed := []byte("abc")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(bytesServed)) // honours Range: 206
	}))
	defer srv.Close()
	dest := t.TempDir()
	good := sha256.Sum256([]byte("xyz")) // the manifest says "xyz", the donor serves "abc"
	f := StateFile{Path: "a", Size: 3, Extents: []Extent{{0, 3}}, SHA256: hex.EncodeToString(good[:])}
	err := c.fetchOne(strings.TrimPrefix(srv.URL, "http://"), "x", dest, f)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("a corrupt file was accepted: %v", err)
	}
	if _, e := os.Stat(filepath.Join(dest, "a")); !os.IsNotExist(e) {
		t.Fatal("the corrupt file was installed")
	}
	if _, e := os.Stat(filepath.Join(dest, "a.part")); !os.IsNotExist(e) {
		t.Fatal("the corrupt part was kept")
	}
}

// The point of the feature: an EMPTY replica joins a cluster whose Raft log is already compacted, getting its
// state from a running peer over the network, and ends up with exactly the same chain.
func TestStateTransfer_EmptyReplicaJoinsCompactedCluster(t *testing.T) {
	h := newHarness(t, 3)
	h.mutate = func(rc *config.RaftConfig) { rc.TrailingLogs = 5; rc.SnapshotThreshold = 1 << 30 }
	h.startAll()
	l := h.leader()
	for i := 0; i < 40; i++ {
		h.submit(l, testBatch(t, uint64(i)))
	}
	h.waitBlocks(40, h.m...)
	if err := l.node.raft.Snapshot().Error(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "log compaction", 10*time.Second, func() bool { return l.node.Status().FirstLogIndex > 1 })

	n3 := h.newMember("n3")
	h.join = map[string]bool{"n3": true}
	// 1. fetch the state from the running leader
	stage := filepath.Join(t.TempDir(), "root")
	man, err := h.admin().FetchState(l.fwdAddr, stage, FetchStateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if man.Meta.LastBlock < 40 {
		t.Fatalf("snapshot at block %d, expected >= 40", man.Meta.LastBlock)
	}
	loadDiskState(t, stage, n3)
	// keep the cluster moving while the new replica boots
	for i := 40; i < 55; i++ {
		h.submit(h.leader(), testBatch(t, uint64(i)))
	}
	h.start(n3)
	if _, err := h.admin().AddReplica(h.members()[:3], Member{ID: "n3", RaftAddr: "n3", AdminAddr: n3.fwdAddr}, false); err != nil {
		t.Fatalf("add-replica: %v", err)
	}
	h.waitBlocks(55, h.m...)
	time.Sleep(300 * time.Millisecond)
	h.assertIdentical(h.m...)
}
