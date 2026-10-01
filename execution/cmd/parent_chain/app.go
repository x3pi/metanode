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

	var gen *parentchain.Genesis
	var protoValidators []*pb.ValidatorInfo

	if genesisPath != "" {
		if g, err := parentchain.LoadGenesis(genesisPath); err == nil {
			gen = g
			if pv, err := g.ToProtoValidators(); err == nil {
				protoValidators = pv
				log.Printf("Loaded genesis from %s: chain_id=%d, validators=%d", genesisPath, g.ChainID, len(g.Validators))
			}
		} else {
			log.Printf("⚠️ Failed to load genesis from %s: %v. Using default committee.", genesisPath, err)
		}
	}

	if len(protoValidators) == 0 {
		// Fallback for single node devnet / backward compatibility
		protocolKeyBytes := common.Hex2Bytes("7cdfc1340f0f1728c4de1725ca3c6791882316547a336ee3a5ba1cabb5fce3b6")
		networkKeyBytes := common.Hex2Bytes("8d9fe40cd34f06c657503dbc15cc4c2cb67ebc26426c426852f741f33811bee8")
		blsPubKeyBytes := common.Hex2Bytes("805562d9bf84b6ebec07e59676eeb883b160ff287c71f98bc19c0b115682855cf83d2cbe24cff49a2a50a3cc16053331006509172909f2913e1de18f7724128f6edbbde91244e6b7f3b89510b65f7c00e1293fb548c7e2b7e9f3bba800f7cd0a")
		protoValidators = []*pb.ValidatorInfo{
			{
				Address:      "0x7e615e4a500ab42b7bb3fdbb62fbb8bd10385fc5",
				Stake:        "1000000000000000000",
				AuthorityKey: blsPubKeyBytes,
				ProtocolKey:  protocolKeyBytes,
				NetworkKey:   networkKeyBytes,
				Name:         "node-0",
				P2PAddress:   "/ip4/127.0.0.1/tcp/19300",
			},
		}
	}

	tb := processor.NewTxBatcher(1000)
	httpServer := parentchain.NewHTTPServer(dbStore, tb.Chan())
	httpServer.SetValidators(protoValidators)
	httpServer.SetProtoTxChan(tb.ProtoChan())
	bp := processor.NewBlockProcessor(dbStore, httpServer.NotifyTxResult)

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
		storage.UpdateLastBlockNumber(prog.LastBlock)
		storage.UpdateLastGlobalExecIndex(prog.LastBlock)
		log.Printf("Loaded last block #%d from DBStore (state_root=%s)", prog.LastBlock, prog.LastStateRoot.Hex())
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
