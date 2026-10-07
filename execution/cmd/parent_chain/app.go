package main

import (
	"bytes"
	"fmt"
	"log"
	"path/filepath"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/parent_chain/processor"
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"google.golang.org/protobuf/proto"
)

type App struct {
	configPath     string
	rustConfigPath string
	dataDir        string
	httpAddr       string
	genesisPath    string

	store           parentchain.Store
	committer       parentchain.BlockCommitter
	genesis         *parentchain.Genesis
	protoValidators []*pb.ValidatorInfo

	blockProcessor *processor.BlockProcessor
	txBatcher      *processor.TxBatcher
	httpServer     *parentchain.HTTPServer
}

func NewApp(configPath, rustConfigPath, dataDir, httpAddr, genesisPath string) (*App, error) {
	dbPath := filepath.Join(dataDir, "parentchain_db")
	dbStore, err := parentchain.NewDBStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open DBStore: %w", err)
	}

	if genesisPath == "" {
		return nil, fmt.Errorf("genesis configuration path is required (use -genesis flag)")
	}

	gen, err := parentchain.LoadGenesis(genesisPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load genesis from %s: %w", genesisPath, err)
	}

	parentchain.SetParentChainID(gen.ChainID)
	protoValidators, err := gen.ToProtoValidators()
	if err != nil {
		return nil, fmt.Errorf("failed to parse genesis validators: %w", err)
	}
	log.Printf("Loaded genesis from %s: chain_id=%d, validators=%d", genesisPath, gen.ChainID, len(gen.Validators))

	policy, err := gen.ClusterPolicy()
	if err != nil {
		return nil, err
	}
	parentchain.SetClusterPolicy(policy)
	if policy.Open {
		log.Printf("⚠️ open_cluster_registration=true: ANY key may register as a cluster and certify deposits (devnet only)")
	} else {
		log.Printf("Cluster registration restricted to %d genesis clusters", len(policy.Allowed))
	}

	allocs, err := gen.FloatAllocations()
	if err != nil {
		return nil, err
	}
	if len(allocs) > 0 {
		dbStore.SetGenesisInit(func(st parentchain.Store) error { return parentchain.ApplyGenesisFloat(st, allocs) })
		log.Printf("Genesis float: %d account(s) will be created in block 1", len(allocs))
	}
	if policy.AllowDeposit {
		log.Printf("⚠️ allow_deposit_to_float=true: clusters can MINT float (devnet only)")
	} else {
		log.Printf("Float supply is fixed (depositToFloat disabled by genesis)")
	}

	tb := processor.NewTxBatcher(1000)
	httpServer := parentchain.NewHTTPServer(dbStore)
	httpServer.SetValidators(protoValidators)
	httpServer.SetTxChan(tb.Chan())
	bp := processor.NewBlockProcessor(dbStore)
	bp.SetForkCallback(func(fork bool) {
		httpServer.SetForkDetected(fork)
	})
	if bp.IsForkDetected() {
		httpServer.SetForkDetected(true)
	}

	return &App{
		configPath:      configPath,
		rustConfigPath:  rustConfigPath,
		dataDir:         dataDir,
		httpAddr:        httpAddr,
		genesisPath:     genesisPath,
		store:           dbStore,
		committer:       dbStore.Committer(),
		genesis:         gen,
		protoValidators: protoValidators,
		blockProcessor:  bp,
		txBatcher:       tb,
		httpServer:      httpServer,
	}, nil
}

func (a *App) Start() error {
	log.Println("Initializing FFI Bridge...")

	// Register the block processor to receive ExecutableBlocks
	blockQueue := a.blockProcessor.GetQueue()

	// Create RequestHandler with callbacks
	reqHandler := &executor.RequestHandler{}
	reqHandler.CustomGetEpochBoundaryDataCallback = func(request *pb.GetEpochBoundaryDataRequest) (*pb.EpochBoundaryData, error) {
		epochDuration := uint64(86400)
		epochTimestamp := uint64(1700000000000)
		if a.genesis != nil {
			if a.genesis.EpochDurationSeconds > 0 {
				epochDuration = a.genesis.EpochDurationSeconds
			}
			if a.genesis.EpochTimestampMs > 0 {
				epochTimestamp = a.genesis.EpochTimestampMs
			}
		}
		return &pb.EpochBoundaryData{
			Epoch:                 request.GetEpoch(),
			EpochStartTimestampMs: epochTimestamp,
			BoundaryBlock:         0,
			BoundaryGei:           0,
			Validators:            a.protoValidators,
			EpochDurationSeconds:  epochDuration,
		}, nil
	}

	reqHandler.CustomGetValidatorsCallback = func(request *pb.GetValidatorsAtBlockRequest) ([]*pb.ValidatorInfo, error) {
		return a.protoValidators, nil
	}

	reqHandler.CustomSyncBlocksCallback = func(request *pb.SyncBlocksRequest) (*pb.SyncBlocksResponse, error) {
		blocks := request.GetBlocks()
		var lastSynced uint64
		for _, bData := range blocks {
			bNum := bData.GetBlockNumber()
			if bNum == 0 {
				continue
			}

			// Check if already applied
			rec, found, err := a.committer.GetBlockRecord(bNum)
			if err != nil {
				return nil, err
			}
			if found {
				if !bytes.Equal(rec.BlockHash.Bytes(), bData.GetBlockHash()) ||
					!bytes.Equal(rec.Header.StateRoot.Bytes(), bData.GetStateRoot()) {
					return nil, fmt.Errorf("sync blocks fork conflict: block #%d hash/root mismatch", bNum)
				}
				lastSynced = bNum
				continue
			}

			// Apply block via BlockProcessor
			var exeBlock pb.ExecutableBlock
			if len(bData.GetRawBlockBytes()) > 0 {
				if err := proto.Unmarshal(bData.GetRawBlockBytes(), &exeBlock); err != nil {
					return nil, fmt.Errorf("sync block #%d invalid raw block bytes: %w", bNum, err)
				}
			} else {
				exeBlock = pb.ExecutableBlock{
					BlockNumber:       bNum,
					Epoch:             bData.GetEpoch(),
					CommitTimestampMs: bData.GetTimestampMs(),
					GlobalExecIndex:   bNum,
					CommitDigest:      bData.GetBlockHash(),
				}
			}

			resp := a.blockProcessor.ProcessBlock(&exeBlock)
			if !resp.Success {
				return nil, fmt.Errorf("sync block #%d execution failed: %s", bNum, resp.Error)
			}
			if !bytes.Equal(resp.StateRoot, bData.GetStateRoot()) {
				return nil, fmt.Errorf("sync block #%d state root mismatch with peer (local=%s, peer=%s)",
					bNum, common.BytesToHash(resp.StateRoot).Hex(), common.BytesToHash(bData.GetStateRoot()).Hex())
			}
			lastSynced = bNum
		}

		return &pb.SyncBlocksResponse{
			SyncedCount:     uint64(len(blocks)),
			LastSyncedBlock: lastSynced,
		}, nil
	}

	reqHandler.CustomGetBlocksRangeCallback = func(request *pb.GetBlocksRangeRequest) (*pb.GetBlocksRangeResponse, error) {
		from := request.GetFromBlock()
		to := request.GetToBlock()
		limit := 100
		if to > from+uint64(limit) {
			to = from + uint64(limit)
		}
		records, err := a.committer.GetBlockRecords(from, to, limit)
		if err != nil {
			return nil, err
		}
		var pbBlocks []*pb.BlockData
		for _, rec := range records {
			pbBlocks = append(pbBlocks, &pb.BlockData{
				BlockNumber:      rec.Header.Number,
				BlockHash:        rec.BlockHash.Bytes(),
				Epoch:            rec.Header.Epoch,
				TimestampMs:      rec.Header.TimestampMs,
				ParentHash:       rec.Header.ParentHash.Bytes(),
				StateRoot:        rec.Header.StateRoot.Bytes(),
				TransactionsRoot: rec.Header.TxsRoot.Bytes(),
				ReceiptsRoot:     rec.Header.ReceiptsRoot.Bytes(),
				RawBlockBytes:    rec.RawBlock,
			})
		}
		return &pb.GetBlocksRangeResponse{
			Blocks: pbBlocks,
			Count:  uint64(len(pbBlocks)),
		}, nil
	}

	if prog, err := a.committer.LastApplied(); err == nil && prog.LastBlock > 0 {
		if !a.blockProcessor.IsForkDetected() {
			storage.UpdateLastBlockNumber(prog.LastBlock)
			storage.UpdateLastGlobalExecIndex(prog.LastBlock)
			if rec, found, err := a.committer.GetBlockRecord(prog.LastBlock); err == nil && found {
				if rec.Header.CommitIndex > 0 {
					storage.UpdateLastHandledCommitIndex(rec.Header.CommitIndex)
				}
			}
			log.Printf("Loaded last block #%d from DBStore (state_root=%s, commit_index=%d)",
				prog.LastBlock, prog.LastStateRoot.Hex(), storage.GetLastHandledCommitIndex())
		} else {
			log.Printf("🚨 Node in QUARANTINED fork_detected state at startup: skipping storage block advance")
		}
	}

	reqHandler.CustomGetLastBlockNumberCallback = func(request *pb.GetLastBlockNumberRequest) (*pb.LastBlockNumberResponse, error) {
		lastBlock := storage.GetLastBlockNumber()
		lastGEI := storage.GetLastGlobalExecIndex()
		var lastHash []byte
		if rec, found, err := a.committer.GetBlockRecord(lastBlock); err == nil && found {
			lastHash = rec.BlockHash.Bytes()
		}
		return &pb.LastBlockNumberResponse{
			LastBlockNumber:        lastBlock,
			LastGlobalExecIndex:    lastGEI,
			IsReady:                true,
			LastExecutedCommitHash: lastHash,
			LastEpoch:              0,
		}, nil
	}

	storage.SetBlockchainInitDone()

	if err := executor.InitFFIBridge(a.rustConfigPath, a.dataDir, reqHandler, blockQueue); err != nil {
		return fmt.Errorf("failed to init FFI bridge: %w", err)
	}

	a.blockProcessor.Start()
	a.txBatcher.Start()

	log.Printf("Starting Parent Chain RPC server on %s", a.httpAddr)
	go func() {
		if err := a.httpServer.Start(a.httpAddr); err != nil {
			log.Fatalf("Parent Chain RPC server exited: %v", err)
		}
	}()

	return nil
}

func (a *App) Stop() {
	if a.txBatcher != nil {
		a.txBatcher.Stop()
	}
	if a.blockProcessor != nil {
		a.blockProcessor.Stop()
	}
}
