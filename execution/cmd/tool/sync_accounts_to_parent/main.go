// Command sync_accounts_to_parent rebuilds the Parent Chain account registry of one execution cluster after a Parent
// Chain data loss. It only READS: the execution chain (blocks + registration status RPC) and, optionally, the
// current Parent Chain. The output is a genesis `account_registry` list that the Parent Chain applies in block 1.
//
// Sources, merged and de-duplicated:
//  1. account_registered / account_provisional system transactions (to RollupSystemAddress) in every block. Their
//     payload is only visible through RPC on nodes running the fixed RPC (raw input for rollup system txs);
//  2. every sender / recipient of a regular transaction in every block (with the account gate on, a sender must be
//     registered), so registered users are found even on nodes without the RPC fix;
//  3. an optional wallet file (genesis / dev wallets);
//  4. the registrations the (new) Parent Chain already holds for this cluster.
//
// Every candidate from (1)-(3) is checked with mtn_getRegistrationStatus: only CONFIRMED addresses (the on-chain
// ParentRegistered flag of this cluster) are kept. Addresses from (4) were accepted by the Parent Chain with the user's
// signature and are always kept.
//
// Usage:
//
//	go run ./cmd/tool/sync_accounts_to_parent \
//	  -rpc http://61.28.233.146:8750 -parent http://lan1.iqnb.com:28601 \
//	  -wallets wallets.txt -genesis parent_genesis.json -genesis-out parent_genesis.restored.json
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/rpc"
	"google.golang.org/protobuf/proto"

	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
)

const maxWorkers = 64

type rpcTx struct {
	From  common.Address  `json:"from"`
	To    *common.Address `json:"to"`
	Input hexutil.Bytes   `json:"input"`
}

type rpcBlock struct {
	Transactions []rpcTx `json:"transactions"`
}

// candidate records where an address was found.
type candidate struct {
	fromBlocks  bool
	fromActive  bool
	fromWallets bool
	fromParent  bool
}

type reportEntry struct {
	Address string `json:"address"`
	Status  string `json:"status"`
	Reason  string `json:"reason,omitempty"`
	Source  string `json:"source"`
}

type report struct {
	ClusterKey      string        `json:"cluster_key"`
	BlocksScanned   uint64        `json:"blocks_scanned"`
	BlocksMissing   []uint64      `json:"blocks_missing,omitempty"`
	FromBlocks      int           `json:"from_blocks"`
	FromActive      int           `json:"from_active"`
	NotRegistered   int           `json:"not_registered"` // candidates with status NONE (contracts, unregistered recipients)
	FromWallets     int           `json:"from_wallets"`
	FromParent      int           `json:"from_parent"`
	Included        int           `json:"included"`
	Excluded        []reportEntry `json:"excluded,omitempty"`
	StatusErrors    []reportEntry `json:"status_errors,omitempty"`
	GeneratedAtUnix int64         `json:"generated_at_unix"`
}

func main() {
	rpcURL := flag.String("rpc", "", "execution-chain JSON-RPC URL (required)")
	parentURL := flag.String("parent", "", "Parent Chain HTTP URL; its current registrations for this cluster are kept too")
	walletsPath := flag.String("wallets", "", "optional wallet file: JSON array of addresses or one address per line")
	from := flag.Uint64("from", 1, "first block to scan")
	to := flag.Uint64("to", 0, "last block to scan (0 = latest)")
	workers := flag.Int("workers", 16, "concurrent block fetchers (max 64)")
	out := flag.String("out", "registry_seed.json", "output: genesis account_registry array")
	genesisIn := flag.String("genesis", "", "optional Parent Chain genesis to merge account_registry into")
	genesisOut := flag.String("genesis-out", "", "where to write the merged genesis (required with -genesis)")
	flag.Parse()

	if *rpcURL == "" {
		log.Fatal("-rpc is required")
	}
	if *genesisIn != "" && *genesisOut == "" {
		log.Fatal("-genesis-out is required with -genesis")
	}
	if *genesisIn != "" && *genesisOut == *genesisIn {
		log.Fatal("-genesis-out must differ from -genesis (never overwrite the source genesis)")
	}
	if *workers < 1 {
		*workers = 1
	}
	if *workers > maxWorkers {
		*workers = maxWorkers
	}

	ctx := context.Background()
	client, err := rpc.DialContext(ctx, *rpcURL)
	if err != nil {
		log.Fatalf("dial %s: %v", *rpcURL, err)
	}
	defer client.Close()

	clusterKey := fetchClusterKey(ctx, client)
	clusterKeyHex := hexutil.Encode(clusterKey[:])
	log.Printf("cluster key: %s", clusterKeyHex)

	last := *to
	if last == 0 {
		var head hexutil.Uint64
		if err := client.CallContext(ctx, &head, "eth_blockNumber"); err != nil {
			log.Fatalf("eth_blockNumber: %v", err)
		}
		last = uint64(head)
	}
	if last < *from {
		log.Fatalf("empty range: from=%d to=%d", *from, last)
	}

	cands := map[common.Address]*candidate{}
	get := func(a common.Address) *candidate {
		c, ok := cands[a]
		if !ok {
			c = &candidate{}
			cands[a] = c
		}
		return c
	}

	rep := report{ClusterKey: clusterKeyHex, GeneratedAtUnix: time.Now().Unix()}

	// 1. Blocks.
	found, active, missing := scanBlocks(ctx, client, *from, last, *workers, clusterKey)
	rep.BlocksScanned = last - *from + 1
	rep.BlocksMissing = missing
	for a := range found {
		get(a).fromBlocks = true
	}
	for a := range active {
		get(a).fromActive = true
	}
	rep.FromBlocks = len(found)
	rep.FromActive = len(active)
	log.Printf("blocks %d..%d: %d registration payload(s), %d active address(es), %d block(s) missing/pruned",
		*from, last, len(found), len(active), len(missing))

	// 2. Wallet file.
	if *walletsPath != "" {
		ws, err := readWallets(*walletsPath)
		if err != nil {
			log.Fatalf("read wallets: %v", err)
		}
		for _, a := range ws {
			get(a).fromWallets = true
		}
		rep.FromWallets = len(ws)
		log.Printf("wallet file: %d address(es)", len(ws))
	}

	// 3. Current Parent Chain registrations of this cluster.
	if *parentURL != "" {
		ps, err := fetchParentRegistrations(*parentURL, clusterKeyHex)
		if err != nil {
			log.Fatalf("parent registrations: %v", err)
		}
		for _, a := range ps {
			get(a).fromParent = true
		}
		rep.FromParent = len(ps)
		log.Printf("parent chain: %d registration(s) for this cluster", len(ps))
	}

	// Verify against the execution chain's on-chain flag.
	addrs := make([]common.Address, 0, len(cands))
	for a := range cands {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool { return strings.ToLower(addrs[i].Hex()) < strings.ToLower(addrs[j].Hex()) })

	statuses := fetchStatuses(ctx, client, addrs, cands, *workers)
	var seed []parentchain.GenesisAccountRegistration
	for i, a := range addrs {
		c := cands[a]
		st := statuses[i]
		switch {
		case c.fromParent || st.Status == string(rollup.RegStatusConfirmed):
			seed = append(seed, parentchain.GenesisAccountRegistration{Address: a.Hex(), ClusterKey: clusterKeyHex})
		case st.err != nil:
			rep.StatusErrors = append(rep.StatusErrors, reportEntry{Address: a.Hex(), Reason: st.err.Error(), Source: c.source()})
		case st.Status == string(rollup.RegStatusNone) && !c.fromWallets && !c.fromBlocks:
			rep.NotRegistered++
		default:
			rep.Excluded = append(rep.Excluded, reportEntry{Address: a.Hex(), Status: st.Status, Reason: st.Reason, Source: c.source()})
		}
	}
	rep.Included = len(seed)
	if seed == nil {
		seed = []parentchain.GenesisAccountRegistration{}
	}

	writeJSON(*out, seed)
	writeJSON(*out+".report.json", rep)
	log.Printf("wrote %s (%d account(s)) and %s.report.json", *out, len(seed), *out)
	log.Printf("excluded (not CONFIRMED on execution chain): %d, status errors: %d", len(rep.Excluded), len(rep.StatusErrors))

	if *genesisIn != "" {
		if err := mergeGenesis(*genesisIn, *genesisOut, seed); err != nil {
			log.Fatalf("merge genesis: %v", err)
		}
		log.Printf("wrote merged genesis %s (validated with parentchain.LoadGenesis)", *genesisOut)
	}

	if len(rep.StatusErrors) > 0 || len(rep.BlocksMissing) > 0 {
		log.Printf("WARNING: incomplete input (status errors or missing blocks); see the report before deploying")
		os.Exit(2)
	}
}

func (c *candidate) source() string {
	var s []string
	if c.fromBlocks {
		s = append(s, "blocks")
	}
	if c.fromActive {
		s = append(s, "active")
	}
	if c.fromWallets {
		s = append(s, "wallets")
	}
	if c.fromParent {
		s = append(s, "parent")
	}
	return strings.Join(s, "+")
}

func fetchClusterKey(ctx context.Context, client *rpc.Client) cm.PublicKey {
	var id struct {
		ClusterKey string `json:"clusterKey"`
	}
	if err := client.CallContext(ctx, &id, "mtn_getClusterIdentity"); err != nil {
		log.Fatalf("mtn_getClusterIdentity: %v", err)
	}
	raw, err := hexutil.Decode(id.ClusterKey)
	if err != nil || len(raw) != len(cm.PublicKey{}) {
		log.Fatalf("invalid cluster key %q", id.ClusterKey)
	}
	var k cm.PublicKey
	copy(k[:], raw)
	return k
}

type statusResult struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
	err    error
}

// fetchStatuses queries mtn_getRegistrationStatus for every address that needs it, with a bounded worker pool.
// Results are indexed like addrs; Parent Chain-sourced addresses are skipped.
func fetchStatuses(ctx context.Context, client *rpc.Client, addrs []common.Address, cands map[common.Address]*candidate, workers int) []statusResult {
	out := make([]statusResult, len(addrs))
	jobs := make(chan int, workers*2)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				var st statusResult
				if err := client.CallContext(ctx, &st, "mtn_getRegistrationStatus", addrs[i]); err != nil {
					st.err = err
				}
				out[i] = st
			}
		}()
	}
	for i, a := range addrs {
		if cands[a].fromParent {
			continue
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return out
}

// scanBlocks fetches [from, to] with a bounded worker pool. It returns the users of this cluster's registration
// payloads, every sender / recipient of a regular transaction, and the block numbers that could not be read.
func scanBlocks(ctx context.Context, client *rpc.Client, from, to uint64, workers int, clusterKey cm.PublicKey) (map[common.Address]struct{}, map[common.Address]struct{}, []uint64) {
	jobs := make(chan uint64, workers*2)
	var mu sync.Mutex
	found := map[common.Address]struct{}{}
	active := map[common.Address]struct{}{}
	var missing []uint64
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				var b *rpcBlock
				err := client.CallContext(ctx, &b, "eth_getBlockByNumber", hexutil.EncodeUint64(n), true)
				if err != nil || b == nil {
					mu.Lock()
					missing = append(missing, n)
					mu.Unlock()
					continue
				}
				mu.Lock()
				for _, tx := range b.Transactions {
					if tx.To != nil && *tx.To == rollup.RollupSystemAddress {
						if user, ok := decodeRegistration(tx.Input, clusterKey); ok {
							found[user] = struct{}{}
						}
						continue
					}
					if tx.From != (common.Address{}) {
						active[tx.From] = struct{}{}
					}
					if tx.To != nil && *tx.To != (common.Address{}) {
						active[*tx.To] = struct{}{}
					}
				}
				mu.Unlock()
			}
		}()
	}

	for n := from; n <= to; n++ {
		jobs <- n
		if (n-from+1)%5000 == 0 {
			log.Printf("  scanned %d/%d blocks", n-from+1, to-from+1)
		}
	}
	close(jobs)
	wg.Wait()
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	return found, active, missing
}

// decodeRegistration returns the user of an account_registered / account_provisional payload for clusterKey.
// Other payloads sent to RollupSystemAddress (transfers, locks) are ignored.
func decodeRegistration(input []byte, clusterKey cm.PublicKey) (common.Address, bool) {
	var p pb.AccountRegistrationPayloadProto
	if err := proto.Unmarshal(input, &p); err != nil {
		return common.Address{}, false
	}
	if p.Kind != rollup.SystemPayloadKindAccountRegistered && p.Kind != rollup.SystemPayloadKindAccountProvisional {
		return common.Address{}, false
	}
	if len(p.User) != common.AddressLength || len(p.ClusterKey) != len(clusterKey) {
		return common.Address{}, false
	}
	var k cm.PublicKey
	copy(k[:], p.ClusterKey)
	if k != clusterKey {
		return common.Address{}, false
	}
	user := common.BytesToAddress(p.User)
	if user == (common.Address{}) {
		return common.Address{}, false
	}
	return user, true
}

func readWallets(path string) ([]common.Address, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var lines []string
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &lines); err != nil {
			return nil, fmt.Errorf("parse JSON array: %w", err)
		}
	} else {
		sc := bufio.NewScanner(strings.NewReader(trimmed))
		for sc.Scan() {
			lines = append(lines, sc.Text())
		}
	}
	var out []common.Address
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if !common.IsHexAddress(l) {
			return nil, fmt.Errorf("entry %d: invalid address %q", i+1, l)
		}
		out = append(out, common.HexToAddress(l))
	}
	return out, nil
}

func fetchParentRegistrations(base, clusterKeyHex string) ([]common.Address, error) {
	hc := &http.Client{Timeout: 15 * time.Second}
	base = strings.TrimRight(base, "/")
	var out []common.Address
	var cursor uint64
	for {
		url := fmt.Sprintf("%s/inbound_registrations?pubkey=%s&cursor=%d", base, clusterKeyHex, cursor)
		resp, err := hc.Get(url)
		if err != nil {
			return nil, err
		}
		var body struct {
			Events []struct {
				Seq         uint64         `json:"seq"`
				UserAddress common.Address `json:"user_address"`
			} `json:"events"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", url, err)
		}
		if len(body.Events) == 0 {
			return out, nil
		}
		next := cursor
		for _, ev := range body.Events {
			out = append(out, ev.UserAddress)
			if ev.Seq+1 > next {
				next = ev.Seq + 1
			}
		}
		if next == cursor {
			return nil, fmt.Errorf("parent cursor did not advance at %d", cursor)
		}
		cursor = next
	}
}

// mergeGenesis writes in + account_registry to out, keeping every other field as is, then validates the result.
func mergeGenesis(in, out string, seed []parentchain.GenesisAccountRegistration) error {
	data, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	var g map[string]json.RawMessage
	if err := json.Unmarshal(data, &g); err != nil {
		return fmt.Errorf("parse %s: %w", in, err)
	}
	reg, err := json.Marshal(seed)
	if err != nil {
		return err
	}
	g["account_registry"] = reg
	merged, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(merged, '\n'), 0o644); err != nil {
		return err
	}
	if _, err := parentchain.LoadGenesis(out); err != nil {
		return fmt.Errorf("merged genesis is invalid: %w", err)
	}
	return nil
}

func writeJSON(path string, v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Fatalf("encode %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		log.Fatalf("write %s: %v", path, err)
	}
}
