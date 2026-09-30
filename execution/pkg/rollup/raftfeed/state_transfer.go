package raftfeed

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// State transfer (plan C4, "truyền trạng thái qua mạng"): a NEW replica whose DB is empty can get a consistent copy
// of a RUNNING peer's state over the internal HMAC channel, instead of having to copy the data directory of a
// stopped peer. The donor makes a consistent snapshot through its StateSource (execution paused for the short time
// the atomic checkpoints/reflinks take, then resumed), hashes every file, and serves the files; the receiver
// verifies every file against the MAC-authenticated manifest and only then starts with join_existing_chain.

const (
	statePreparePath = "/raft/v1/admin/state/prepare"
	stateFilePath    = "/raft/v1/admin/state/file"
	stateReleasePath = "/raft/v1/admin/state/release"

	stateMarkerSuffix = ".state_transfer.json"
	hashWorkers       = 4
)

// StateMeta describes the block the snapshot was taken at.
type StateMeta struct {
	LastBlock uint64 `json:"last_block"`
	BlockHash string `json:"block_hash"`
	GEI       uint64 `json:"gei"`
}

// StateSource produces a consistent copy of this node's state.
type StateSource interface {
	// Snapshot creates dstRoot (which must not exist) holding the node's database root in the layout a node starts
	// from (databases.root_path). It pauses execution only as long as the atomic copy needs and resumes before
	// returning. It must fail (never return a partial copy) when it cannot make a consistent one.
	Snapshot(dstRoot string) (StateMeta, error)
}

// Extent is a range of a file that holds data; everything outside the extents of a file is a hole (zeros).
type Extent struct {
	Off int64 `json:"off"`
	Len int64 `json:"len"`
}

// StateFile is one file of a snapshot. NOMT files are huge and sparse (tens of GB logical, a few hundred MB of
// data), so a file is described by its size and its DATA extents only; SHA256 covers the bytes of the extents in
// order, and the receiver recreates the file sparse.
type StateFile struct {
	Path    string   `json:"path"` // relative, slash-separated
	Size    int64    `json:"size"`
	Extents []Extent `json:"extents"`
	SHA256  string   `json:"sha256"`
}

// StateManifest is authenticated by the donor's MAC (header) and lists everything the receiver must fetch.
type StateManifest struct {
	ID      string      `json:"id"`
	DonorID string      `json:"donor_id"`
	Meta    StateMeta   `json:"meta"`
	Files   []StateFile `json:"files"`
}

type stateTransfer struct {
	mu     sync.Mutex
	base   string // staging directory (same filesystem as the database root so reflinks/renames are cheap)
	source StateSource
	active string // id being served, "" when none
}

func (n *Node) registerState(mux *http.ServeMux) {
	mux.HandleFunc(statePreparePath, n.adminHandler(n.handleStatePrepare))
	mux.HandleFunc(stateFilePath, n.adminHandler(n.handleStateFile))
	mux.HandleFunc(stateReleasePath, n.adminHandler(n.handleStateRelease))
}

func (n *Node) stateEnabled() bool { return n.state.source != nil && n.state.base != "" }

func (n *Node) handleStatePrepare(w http.ResponseWriter, r *http.Request) {
	if !n.stateEnabled() {
		writeJSON(w, http.StatusNotImplemented, adminResult{Error: "state transfer is not enabled on this replica"})
		return
	}
	if n.failed.Load() {
		writeJSON(w, http.StatusConflict, adminResult{Error: "this replica has failed"})
		return
	}
	n.state.mu.Lock()
	defer n.state.mu.Unlock()
	if n.state.active != "" {
		writeJSON(w, http.StatusConflict, adminResult{Error: "another state transfer is in progress"})
		return
	}
	id := strconv.FormatInt(n.now().UnixNano(), 36)
	dir := filepath.Join(n.state.base, id)
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		writeJSON(w, http.StatusInternalServerError, adminResult{Error: err.Error()})
		return
	}
	meta, err := n.state.source.Snapshot(root)
	if err != nil {
		_ = os.RemoveAll(dir)
		logger.Error("❌ [RAFT-STATE] snapshot failed: %v", err)
		writeJSON(w, http.StatusConflict, adminResult{Error: "snapshot failed: " + err.Error()})
		return
	}
	files, err := hashTree(root)
	if err != nil {
		_ = os.RemoveAll(dir)
		writeJSON(w, http.StatusInternalServerError, adminResult{Error: "hashing the snapshot failed: " + err.Error()})
		return
	}
	man := StateManifest{ID: id, DonorID: n.cfg.NodeID, Meta: meta, Files: files}
	body, _ := json.Marshal(man)
	ts := n.now().UnixMilli()
	w.Header().Set(hdrNode, n.cfg.NodeID)
	w.Header().Set(hdrTs, strconv.FormatInt(ts, 10))
	w.Header().Set(hdrMac, forwardMAC(n.secret, n.cfg.NodeID, ts, statePreparePath, body))
	w.Header().Set("Content-Type", "application/json")
	n.state.active = id
	logger.Info("📦 [RAFT-STATE] snapshot %s ready: block %d, %d files", id, meta.LastBlock, len(files))
	_, _ = w.Write(body)
}

func (n *Node) handleStateFile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n.state.mu.Lock()
	active := n.state.active
	n.state.mu.Unlock()
	if active == "" || q.Get("id") != active {
		http.Error(w, "no such transfer", http.StatusNotFound)
		return
	}
	rel := filepath.Clean(filepath.FromSlash(q.Get("path")))
	if rel == "." || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	full := filepath.Join(n.state.base, active, "root", rel)
	f, err := os.Open(full)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		http.Error(w, "not a regular file", http.StatusNotFound)
		return
	}
	http.ServeContent(w, r, "", st.ModTime(), f) // Range requests => resumable
}

func (n *Node) handleStateRelease(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	n.state.mu.Lock()
	defer n.state.mu.Unlock()
	if id == "" || id != n.state.active {
		writeJSON(w, http.StatusNotFound, adminResult{Error: "no such transfer"})
		return
	}
	_ = os.RemoveAll(filepath.Join(n.state.base, id))
	n.state.active = ""
	writeJSON(w, http.StatusOK, adminResult{OK: true})
}

// cleanStaging removes leftovers of an earlier run (a transfer never survives a restart).
func (n *Node) cleanStaging() {
	if n.state.base == "" {
		return
	}
	entries, err := os.ReadDir(n.state.base)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.RemoveAll(filepath.Join(n.state.base, e.Name()))
	}
}

// hashTree lists and hashes every regular file below root.
func hashTree(root string) ([]StateFile, error) {
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	out := make([]StateFile, len(paths))
	errs := make([]error, len(paths))
	sem := make(chan struct{}, hashWorkers)
	var wg sync.WaitGroup
	for i, p := range paths {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, p string) {
			defer wg.Done()
			defer func() { <-sem }()
			size, exts, sum, err := describeFile(p)
			rel, _ := filepath.Rel(root, p)
			out[i], errs[i] = StateFile{Path: filepath.ToSlash(rel), Size: size, Extents: exts, SHA256: sum}, err
		}(i, p)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}

// dataExtents returns the data ranges of f (SEEK_DATA/SEEK_HOLE); a filesystem without hole support reports the
// whole file as data. Extents separated by less than coalesceGap are merged (the gap is then transferred as
// zeros), which keeps the number of requests small for a fragmented sparse file.
func dataExtents(f *os.File, size int64) ([]Extent, error) {
	const seekData, seekHole, coalesceGap = 3, 4, 1 << 20
	var out []Extent
	off := int64(0)
	for off < size {
		start, err := f.Seek(off, seekData)
		if err != nil {
			if errors.Is(err, syscall.ENXIO) {
				break // no more data
			}
			if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
				return []Extent{{0, size}}, nil // no hole support: the whole file is data
			}
			return nil, err
		}
		end, err := f.Seek(start, seekHole)
		if err != nil {
			return nil, err
		}
		if end > size {
			end = size
		}
		if n := len(out); n > 0 && start-(out[n-1].Off+out[n-1].Len) < coalesceGap {
			out[n-1].Len = end - out[n-1].Off
		} else {
			out = append(out, Extent{Off: start, Len: end - start})
		}
		off = end
	}
	if size > 0 && len(out) == 0 {
		return nil, nil // entirely a hole
	}
	return out, nil
}

// digestExtents hashes the bytes of the given extents of f in order.
func digestExtents(f *os.File, exts []Extent) (string, error) {
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for _, e := range exts {
		for done := int64(0); done < e.Len; {
			n := int64(len(buf))
			if e.Len-done < n {
				n = e.Len - done
			}
			m, err := f.ReadAt(buf[:n], e.Off+done)
			if m > 0 {
				h.Write(buf[:m])
				done += int64(m)
			}
			if err != nil && int64(m) < n {
				return "", err
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// describeFile returns size, data extents and the digest of a file.
func describeFile(path string) (int64, []Extent, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, nil, "", err
	}
	exts, err := dataExtents(f, st.Size())
	if err != nil {
		return 0, nil, "", err
	}
	sum, err := digestExtents(f, exts)
	return st.Size(), exts, sum, err
}

// ---- client ------------------------------------------------------------------------------------------------------

// FetchStateOptions tunes FetchState.
type FetchStateOptions struct {
	Workers int
}

// FetchState downloads a consistent snapshot of the donor at donorAddr into destRoot (the future
// databases.root_path of the new replica) and verifies every file. destRoot must not exist, or hold the
// unfinished result of an earlier FetchState (resumed: files that already match their hash are kept).
func (c *AdminClient) FetchState(donorAddr, destRoot string, opts FetchStateOptions) (StateManifest, error) {
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	marker := destRoot + stateMarkerSuffix
	if entries, err := os.ReadDir(destRoot); err == nil && len(entries) > 0 {
		if _, merr := os.Stat(marker); merr != nil {
			return StateManifest{}, fmt.Errorf("refused: %s is not empty and is not the unfinished result of a state transfer", destRoot)
		}
	}
	long := &AdminClient{Secret: c.Secret, SenderID: c.SenderID, Now: c.Now, HTTP: &http.Client{Timeout: 2 * time.Hour, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}

	// 1. ask the donor for a snapshot (brief execution pause on the donor) and authenticate the manifest
	code, hdr, body, err := long.do(http.MethodPost, donorAddr, statePreparePath, "", nil)
	if err != nil {
		return StateManifest{}, err
	}
	if code != http.StatusOK {
		var r adminResult
		_ = json.Unmarshal(body, &r)
		return StateManifest{}, fmt.Errorf("donor refused: %s (HTTP %d)", r.Error, code)
	}
	var man StateManifest
	if err := json.Unmarshal(body, &man); err != nil {
		return StateManifest{}, fmt.Errorf("manifest: %w", err)
	}
	ts, _ := strconv.ParseInt(hdr.Get(hdrTs), 10, 64)
	skew := c.now().Sub(time.UnixMilli(ts))
	mac, _ := hex.DecodeString(hdr.Get(hdrMac))
	want, _ := hex.DecodeString(forwardMAC(c.Secret, hdr.Get(hdrNode), ts, statePreparePath, body))
	if skew > maxForwardSkew || skew < -maxForwardSkew || !macEqual(mac, want) || hdr.Get(hdrNode) != man.DonorID {
		return StateManifest{}, errors.New("the manifest is not authentic (bad MAC, donor id or clock skew): aborting")
	}
	release := func() {
		_, _, _, _ = long.do(http.MethodPost, donorAddr, stateReleasePath, url.Values{"id": {man.ID}}.Encode(), nil)
	}
	defer release()

	// 2. fetch + verify every file
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		return man, err
	}
	if err := os.WriteFile(marker, body, 0o600); err != nil {
		return man, err
	}
	jobs := make(chan StateFile)
	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	fail := func(e error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = e
		}
		errMu.Unlock()
	}
	for i := 0; i < opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if err := long.fetchOne(donorAddr, man.ID, destRoot, f); err != nil {
					fail(fmt.Errorf("%s: %w", f.Path, err))
				}
			}
		}()
	}
	for _, f := range man.Files {
		errMu.Lock()
		stop := firstErr != nil
		errMu.Unlock()
		if stop {
			break
		}
		jobs <- f
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return man, fmt.Errorf("state transfer incomplete (re-run to resume): %w", firstErr)
	}
	// 3. nothing extra, nothing missing
	got := map[string]bool{}
	_ = filepath.WalkDir(destRoot, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			rel, _ := filepath.Rel(destRoot, p)
			got[filepath.ToSlash(rel)] = true
		}
		return nil
	})
	for _, f := range man.Files {
		if !got[f.Path] {
			return man, fmt.Errorf("file %s is missing after the transfer", f.Path)
		}
		delete(got, f.Path)
	}
	// Leftovers of an earlier attempt at an OLDER snapshot (files the donor no longer has) must not survive: a stale
	// SST next to the new MANIFEST would corrupt the database. destRoot is ours (marker-guarded), so remove them.
	for extra := range got {
		if err := os.Remove(filepath.Join(destRoot, filepath.FromSlash(extra))); err != nil {
			return man, fmt.Errorf("cannot remove stale file %s: %w", extra, err)
		}
	}
	_ = os.Remove(marker)
	return man, nil
}

// verifyFile reports whether the file at path matches the manifest entry: same size, and the digest of its data
// extents. Holes are zeros by definition, so they need not be read.
func verifyFile(path string, f StateFile) bool {
	fh, err := os.Open(path)
	if err != nil {
		return false
	}
	defer fh.Close()
	st, err := fh.Stat()
	if err != nil || st.Size() != f.Size {
		return false
	}
	sum, err := digestExtents(fh, f.Extents)
	return err == nil && sum == f.SHA256
}

// fetchOne downloads one file into a .part (sparse: only the data extents are requested and written), verifies it
// against the manifest and renames it into place. A file that already matches is kept (resume).
func (c *AdminClient) fetchOne(addr, id, destRoot string, f StateFile) error {
	dst := filepath.Join(destRoot, filepath.FromSlash(f.Path))
	if verifyFile(dst, f) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	part := dst + ".part"
	_ = os.Remove(part)
	out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if err := out.Truncate(f.Size); err != nil { // sparse: nothing is allocated for the holes
		out.Close()
		return err
	}
	for _, e := range f.Extents {
		for off := e.Off; off < e.Off+e.Len; {
			n := e.Off + e.Len - off
			if n > maxRangeBytes {
				n = maxRangeBytes
			}
			if err := c.fetchRange(addr, id, f.Path, off, n, out); err != nil {
				out.Close()
				return err
			}
			off += n
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if !verifyFile(part, f) {
		_ = os.Remove(part) // never keep a corrupt part
		return errors.New("checksum mismatch: the downloaded data does not match the manifest")
	}
	return os.Rename(part, dst)
}

const maxRangeBytes = 64 << 20

// fetchRange requests bytes [off, off+n) of a file and writes them at the same offset of out.
func (c *AdminClient) fetchRange(addr, id, path string, off, n int64, out *os.File) error {
	q := url.Values{"id": {id}, "path": {path}}.Encode()
	ts := c.now().UnixMilli()
	req, err := http.NewRequest(http.MethodGet, "http://"+addr+stateFilePath+"?"+q, nil)
	if err != nil {
		return err
	}
	req.Header.Set(hdrNode, c.sender())
	req.Header.Set(hdrTs, strconv.FormatInt(ts, 10))
	req.Header.Set(hdrMac, forwardMAC(c.Secret, c.sender(), ts, stateFilePath, []byte(q)))
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+n-1))
	resp, err := c.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("HTTP %d for bytes %d-%d", resp.StatusCode, off, off+n-1)
	}
	w := io.NewOffsetWriter(out, off)
	got, err := io.Copy(w, resp.Body)
	if err != nil {
		return err
	}
	if got != n {
		return fmt.Errorf("short read: %d of %d bytes", got, n)
	}
	return nil
}
