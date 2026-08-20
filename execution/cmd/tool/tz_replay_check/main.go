// tz_replay_check replays a real, already-committed block range through the
// exact production execution path (pkg/block_validator.ProcessBlock, which
// itself drives pkg/blockchain/tx_processor.ProcessTransactions — the same
// Block-STM engine a live node uses) under one execution mode, and writes a
// per-block/per-tx result summary to JSON. Running it twice against two
// independent copies of the same committed data (once --mode=cgo, once
// --mode=trustzone) and diffing the two JSON files with --compare is
// note/tee_dual_mode_execution_plan.md's "Giai đoạn 4" real-block
// differential replay — see that file's §9.36 for why this exists and what
// it does NOT cover (a real TA on hardware; see plan doc for that gap).
//
// This intentionally does NOT reuse cmd/simple_chain's NewApp(): that
// function's initProcessors() step starts the live Rust consensus engine
// (executor.InitFFIBridge), which would keep advancing the copied chain in
// the background during replay — exactly the singleton/concurrent-mutation
// hazard the plan's "2 tiến trình tách biệt" design was chosen to avoid.
// Instead this hand-replicates ONLY the subset of cmd/simple_chain's real
// init sequence that pkg/block_validator.ProcessBlock actually touches
// (confirmed by reading that function, not guessed): config/genesis load,
// state backend + execution mode switches, the same per-DB
// storage.NewShardelDB/NewDummyStorage calls app_storage.go makes, a
// ChainState, the TrieDatabaseManager singleton, and blockchain.InitBlockChain.
// It deliberately skips GEI/commit-index/epoch bookkeeping (app_blockchain.go's
// "existing block" branch) — ProcessBlock never reads any of that; it builds
// its own scoped ChainState from the target block's parent header.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	e_common "github.com/ethereum/go-ethereum/common"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/block_validator"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	mt_trie "github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
)

// txReplayResult captures exactly what "Giai đoạn 4" needs to diff per tx:
// pass/fail status, gas, and a hash of the return data (hashed, not stored
// raw, to keep result files small for return-heavy contract calls).
type txReplayResult struct {
	Hash       string `json:"hash"`
	Status     int32  `json:"status"`
	Exception  int32  `json:"exception"`
	GasUsed    uint64 `json:"gas_used"`
	ReturnHash string `json:"return_hash"`
}

type blockReplayResult struct {
	Number           uint64           `json:"number"`
	Root             string           `json:"root"`
	StakeStatesRoot  string           `json:"stake_states_root"`
	CallGetApiCalled bool             `json:"call_get_api_called"`
	Txs              []txReplayResult `json:"txs"`
}

type replayOutput struct {
	Mode   string              `json:"mode"`
	From   uint64              `json:"from"`
	To     uint64              `json:"to"`
	Blocks []blockReplayResult `json:"blocks"`
}

func main() {
	configPath := flag.String("config", "", "path to node's config.json (the one that produced the data being replayed)")
	from := flag.Uint64("from", 0, "first block number to replay (inclusive)")
	to := flag.Uint64("to", 0, "last block number to replay (inclusive)")
	mode := flag.String("mode", "", "execution mode override (cgo|trustzone|trustzone-hardware); empty = use config.json's own execution_mode")
	out := flag.String("out", "", "path to write replay result JSON")
	compareA := flag.String("compare-a", "", "compare mode: first replay result JSON")
	compareB := flag.String("compare-b", "", "compare mode: second replay result JSON")
	flag.Parse()

	if *compareA != "" || *compareB != "" {
		if *compareA == "" || *compareB == "" {
			fmt.Fprintln(os.Stderr, "tz_replay_check: -compare-a and -compare-b must both be set")
			os.Exit(2)
		}
		os.Exit(runCompare(*compareA, *compareB))
	}

	if *configPath == "" || *to < *from || *out == "" {
		fmt.Fprintln(os.Stderr, "tz_replay_check: -config, -from/-to (from<=to), and -out are required for replay mode")
		os.Exit(2)
	}

	if err := runReplay(*configPath, *from, *to, *mode, *out); err != nil {
		fmt.Fprintf(os.Stderr, "tz_replay_check: %v\n", err)
		os.Exit(1)
	}
}

func runReplay(configPath string, from, to uint64, modeOverride, outPath string) error {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("LoadConfig: %w", err)
	}
	genesis, err := config.LoadGenesisData(cfg.GenesisFilePath)
	if err != nil {
		return fmt.Errorf("LoadGenesisData: %w", err)
	}
	cfg.ChainId = genesis.Config.ChainId

	mode := cfg.ExecutionMode
	if modeOverride != "" {
		mode = modeOverride
	}

	// Same order as cmd/simple_chain/app.go's NewApp: state backend before
	// any trie creation, execution mode before any transaction processing.
	mt_trie.SetStateBackend(cfg.StateBackend)
	mvm.SetExecutionMode(mode)
	fmt.Printf("tz_replay_check: replaying blocks [%d..%d] under execution mode %q (config=%s)\n", from, to, mode, configPath)

	// Mirrors cmd/simple_chain/app.go's NewApp: NOMT must be initialized
	// before any NewStateTrie call, which NewChainStateWithGenesis below
	// makes immediately. Same defaults as app.go (128MB/128MB, concurrency 4)
	// when the config doesn't override them.
	if mt_trie.GetStateBackend() == mt_trie.BackendNOMT {
		nomtPath := config.JoinPathIfNotURL(cfg.Databases.RootPath, "/consensus/nomt_db")
		commitConcurrency := cfg.NomtCommitConcurrency
		if commitConcurrency <= 0 {
			commitConcurrency = 4
		}
		pageCacheMB := cfg.NomtPageCacheMB
		if pageCacheMB <= 0 {
			pageCacheMB = 128
		}
		leafCacheMB := cfg.NomtLeafCacheMB
		if leafCacheMB <= 0 {
			leafCacheMB = 128
		}
		if err := mt_trie.InitNomtDB(nomtPath, commitConcurrency, pageCacheMB, leafCacheMB); err != nil {
			return fmt.Errorf("InitNomtDB: %w", err)
		}
	}

	sm := storage.NewStorageManager()
	if err := initStorageDatabases(sm, cfg); err != nil {
		return fmt.Errorf("initStorageDatabases: %w", err)
	}

	blockDatabase := block.NewBlockDatabase(sm.GetStorageBlock())
	lastBlock, err := blockDatabase.GetLastBlock()
	if err != nil {
		return fmt.Errorf("GetLastBlock (was the source node shut down cleanly? see CLAUDE.md-style guidance in this repo's plan doc §Giai đoạn B): %w", err)
	}

	freeFeeAddresses := map[e_common.Address]struct{}{}
	for _, addr := range cfg.FreeFeeAddresses {
		if len(addr) == 40 {
			freeFeeAddresses[e_common.HexToAddress(addr)] = struct{}{}
		}
	}

	chainState, err := blockchain.NewChainStateWithGenesis(
		sm, blockDatabase, lastBlock.Header(), cfg, freeFeeAddresses, &genesis.Config, "skip_epoch_data",
	)
	if err != nil {
		return fmt.Errorf("NewChainStateWithGenesis: %w", err)
	}
	defer chainState.Close()

	trie_database.CreateTrieDatabaseManager(sm.GetStorageDatabaseTrie(), chainState.GetAccountStateDB())
	blockchain.InitBlockChain(100, blockDatabase, sm)

	freeFeeList := make([]e_common.Address, 0, len(freeFeeAddresses))
	for a := range freeFeeAddresses {
		freeFeeList = append(freeFeeList, a)
	}
	bv := block_validator.NewBlockValidator(sm, chainState, freeFeeList)

	ctx := context.Background()
	output := replayOutput{Mode: mode, From: from, To: to}

	for n := from; n <= to; n++ {
		hash, ok := blockchain.GetBlockChainInstance().GetBlockHashByNumber(n)
		if !ok {
			return fmt.Errorf("block %d: no hash mapping found", n)
		}
		blkIface, err := blockDatabase.GetBlockByHash(hash)
		if err != nil {
			return fmt.Errorf("block %d: GetBlockByHash: %w", n, err)
		}
		blkPtr, ok := blkIface.(*block.Block)
		if !ok {
			return fmt.Errorf("block %d: unexpected concrete block type %T", n, blkIface)
		}

		before := mvm.ExtensionCallGetApiCallCount()
		result, err := bv.ProcessBlock(ctx, *blkPtr)
		if err != nil {
			return fmt.Errorf("block %d: ProcessBlock: %w", n, err)
		}
		after := mvm.ExtensionCallGetApiCallCount()

		br := blockReplayResult{
			Number:           n,
			Root:             result.Root.Hex(),
			StakeStatesRoot:  result.StakeStatesRoot.Hex(),
			CallGetApiCalled: after != before,
		}
		for _, r := range result.Receipts {
			sum := sha256.Sum256(r.Return())
			br.Txs = append(br.Txs, txReplayResult{
				Hash:       r.TransactionHash().Hex(),
				Status:     int32(r.Status()),
				Exception:  int32(r.Exception()),
				GasUsed:    r.GasUsed(),
				ReturnHash: hex.EncodeToString(sum[:]),
			})
		}
		output.Blocks = append(output.Blocks, br)
		fmt.Printf("  block %d: root=%s txs=%d call_get_api=%v\n", n, br.Root, len(br.Txs), br.CallGetApiCalled)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	fmt.Printf("tz_replay_check: wrote %s\n", outPath)
	return nil
}

// initStorageDatabases mirrors cmd/simple_chain/app_storage.go's
// initStorageDatabases exactly (same per-DB paths, same nomt->DummyStorage
// substitution for account/stake/trie) — deliberately NOT importing that
// file (it's in package main of a different binary, unimportable) but kept
// in lockstep by hand; if that file's DB set/paths ever change, this must
// change with it.
func initStorageDatabases(sm *storage.StorageManager, cfg *config.SimpleChainConfig) error {
	type dbSpec struct {
		name      string
		subPath   string
		numShards int
		add       func(storage.Storage) error
	}
	specs := []dbSpec{
		{"account", config.PathAccountState, cfg.Databases.NumShardsDefault, sm.AddStorageAccount},
		{"receipts", config.PathReceipts, cfg.Databases.NumShardsDefault, sm.AddStorageReceipt},
		{"transaction state", config.PathTransactionState, cfg.Databases.NumShardsDefault, sm.AddStorageTransaction},
		{"device key", config.PathBackupDeviceKey, cfg.Databases.NumShardsDefault, sm.AddStorageBackupDeviceKey},
		{"smart contract", config.PathSmartContractStorage, cfg.Databases.NumShardsSmartContract, sm.AddStorageSmartContract},
		{"code", config.PathSmartContractCode, cfg.Databases.NumShardsCode, sm.AddStorageCode},
		{"trie", config.PathTrie, cfg.Databases.NumShardsDefault, sm.AddStorageDatabaseTrie},
		{"block", config.PathBlocks, cfg.Databases.NumShardsDefault, sm.AddStorageBlock},
		{"mapping", config.PathMapping, cfg.Databases.NumShardsDefault, sm.AddStorageMapping},
		{"stake", config.PathStake, cfg.Databases.NumShardsDefault, sm.AddStorageStake},
	}
	dummyBackends := map[string]bool{"account": true, "stake": true, "trie": true}
	for _, s := range specs {
		var db storage.Storage
		var err error
		fullBackupPath := cfg.BackupPath + s.subPath
		if cfg.StateBackend == "nomt" && dummyBackends[s.name] {
			db = storage.NewDummyStorage(fullBackupPath)
		} else {
			db, err = storage.NewShardelDB(
				config.JoinPathIfNotURL(cfg.Databases.RootPath, s.subPath),
				s.numShards, cfg.Databases.Parallelism, cfg.DBType, fullBackupPath,
			)
			if err != nil {
				return fmt.Errorf("%s: NewShardelDB: %w", s.name, err)
			}
			if err := db.Open(); err != nil {
				return fmt.Errorf("%s: Open: %w", s.name, err)
			}
		}
		if err := s.add(db); err != nil {
			return fmt.Errorf("%s: add: %w", s.name, err)
		}
	}
	return nil
}

// runCompare is the "kiểm chứng ngược" step: two replay result files must
// compare EQUAL when they genuinely are (same range, same mode-independent
// data) and MUST be reported as mismatched otherwise. Blocks where either
// run observed a CALL_GET_API call are skipped (known non-determinism, see
// extension.go's extensionCallGetApiCallCount doc comment) — reported, not
// silently dropped.
func runCompare(pathA, pathB string) int {
	a, err := loadReplayOutput(pathA)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tz_replay_check: compare: load %s: %v\n", pathA, err)
		return 1
	}
	b, err := loadReplayOutput(pathB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "tz_replay_check: compare: load %s: %v\n", pathB, err)
		return 1
	}

	byNumA := map[uint64]blockReplayResult{}
	for _, blk := range a.Blocks {
		byNumA[blk.Number] = blk
	}
	byNumB := map[uint64]blockReplayResult{}
	for _, blk := range b.Blocks {
		byNumB[blk.Number] = blk
	}

	nums := map[uint64]bool{}
	for n := range byNumA {
		nums[n] = true
	}
	for n := range byNumB {
		nums[n] = true
	}
	sorted := make([]uint64, 0, len(nums))
	for n := range nums {
		sorted = append(sorted, n)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	mismatches := 0
	skipped := 0
	compared := 0
	for _, n := range sorted {
		blkA, okA := byNumA[n]
		blkB, okB := byNumB[n]
		if !okA || !okB {
			fmt.Printf("MISMATCH block %d: present in A=%v B=%v\n", n, okA, okB)
			mismatches++
			continue
		}
		if blkA.CallGetApiCalled || blkB.CallGetApiCalled {
			fmt.Printf("SKIP block %d: CALL_GET_API observed (A=%v B=%v) — known non-determinism, excluded\n", n, blkA.CallGetApiCalled, blkB.CallGetApiCalled)
			skipped++
			continue
		}
		compared++
		if blkA.Root != blkB.Root {
			fmt.Printf("MISMATCH block %d: root A=%s B=%s\n", n, blkA.Root, blkB.Root)
			mismatches++
			continue
		}
		if blkA.StakeStatesRoot != blkB.StakeStatesRoot {
			fmt.Printf("MISMATCH block %d: stake_states_root A=%s B=%s\n", n, blkA.StakeStatesRoot, blkB.StakeStatesRoot)
			mismatches++
			continue
		}
		if len(blkA.Txs) != len(blkB.Txs) {
			fmt.Printf("MISMATCH block %d: tx count A=%d B=%d\n", n, len(blkA.Txs), len(blkB.Txs))
			mismatches++
			continue
		}
		for i := range blkA.Txs {
			ta, tb := blkA.Txs[i], blkB.Txs[i]
			if ta != tb {
				fmt.Printf("MISMATCH block %d tx %d: A=%+v B=%+v\n", n, i, ta, tb)
				mismatches++
			}
		}
	}

	fmt.Printf("tz_replay_check: compared %d blocks, skipped %d (CALL_GET_API), %d mismatch(es)\n", compared, skipped, mismatches)
	if mismatches > 0 {
		return 1
	}
	if compared == 0 {
		fmt.Fprintln(os.Stderr, "tz_replay_check: WARNING — zero blocks were actually compared (all skipped or absent); this proves nothing")
		return 1
	}
	return 0
}

func loadReplayOutput(path string) (replayOutput, error) {
	var out replayOutput
	f, err := os.Open(path)
	if err != nil {
		return out, err
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}
