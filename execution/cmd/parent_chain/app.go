package main

import (
	"fmt"
	"log"
	"path/filepath"
	"time"
	
	"github.com/meta-node-blockchain/meta-node/executor"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	"github.com/meta-node-blockchain/meta-node/cmd/parent_chain/processor"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/ethereum/go-ethereum/common"
)

type App struct {
	configPath     string
	rustConfigPath string
	dataDir        string
	httpAddr       string

	store          parentchain.Store
	blockProcessor *processor.BlockProcessor
	txBatcher      *processor.TxBatcher
	httpServer     *parentchain.HTTPServer
}

func NewApp(configPath, rustConfigPath, dataDir, httpAddr string) (*App, error) {
	dbPath := filepath.Join(dataDir, "parentchain_db")
	store, err := parentchain.NewDBStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open DBStore: %w", err)
	}
	tb := processor.NewTxBatcher(1000)
	httpServer := parentchain.NewHTTPServer(store, tb.Chan())
	bp := processor.NewBlockProcessor(store, httpServer.NotifyTxResult)

	return &App{
		configPath:     configPath,
		rustConfigPath: rustConfigPath,
		dataDir:        dataDir,
		httpAddr:       httpAddr,
		store:          store,
		blockProcessor: bp,
		txBatcher:      tb,
		httpServer:     httpServer,
	}, nil
}

func (a *App) Start() error {
	log.Println("Initializing FFI Bridge...")

	// Register the block processor to receive ExecutableBlocks
	blockQueue := a.blockProcessor.GetQueue()

	// Create RequestHandler with minimal callbacks
	reqHandler := &executor.RequestHandler{}
	reqHandler.CustomGetEpochBoundaryDataCallback = func(request *pb.GetEpochBoundaryDataRequest) (*pb.EpochBoundaryData, error) {
		// Mock validator for parent chain devnet using actual node-0 keys
		protocolKeyBytes := common.Hex2Bytes("7cdfc1340f0f1728c4de1725ca3c6791882316547a336ee3a5ba1cabb5fce3b6")
		networkKeyBytes := common.Hex2Bytes("8d9fe40cd34f06c657503dbc15cc4c2cb67ebc26426c426852f741f33811bee8")
		blsPubKeyBytes := common.Hex2Bytes("805562d9bf84b6ebec07e59676eeb883b160ff287c71f98bc19c0b115682855cf83d2cbe24cff49a2a50a3cc16053331006509172909f2913e1de18f7724128f6edbbde91244e6b7f3b89510b65f7c00e1293fb548c7e2b7e9f3bba800f7cd0a")
		
		validators := []*pb.ValidatorInfo{
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
		
		epochTimestamp := uint64(time.Now().UnixMilli())
		return &pb.EpochBoundaryData{
			Epoch:                 request.GetEpoch(),
			EpochStartTimestampMs: epochTimestamp,
			BoundaryBlock:         0,
			BoundaryGei:           0,
			Validators:            validators,
			EpochDurationSeconds:  900,
		}, nil
	}
	
	reqHandler.CustomGetValidatorsCallback = func(request *pb.GetValidatorsAtBlockRequest) ([]*pb.ValidatorInfo, error) {
		protocolKeyBytes := common.Hex2Bytes("7cdfc1340f0f1728c4de1725ca3c6791882316547a336ee3a5ba1cabb5fce3b6")
		networkKeyBytes := common.Hex2Bytes("8d9fe40cd34f06c657503dbc15cc4c2cb67ebc26426c426852f741f33811bee8")
		blsPubKeyBytes := common.Hex2Bytes("805562d9bf84b6ebec07e59676eeb883b160ff287c71f98bc19c0b115682855cf83d2cbe24cff49a2a50a3cc16053331006509172909f2913e1de18f7724128f6edbbde91244e6b7f3b89510b65f7c00e1293fb548c7e2b7e9f3bba800f7cd0a")
		return []*pb.ValidatorInfo{
			{
				Address:      "0x7e615e4a500ab42b7bb3fdbb62fbb8bd10385fc5",
				Stake:        "1000000000000000000",
				AuthorityKey: blsPubKeyBytes,
				ProtocolKey:  protocolKeyBytes,
				NetworkKey:   networkKeyBytes,
				Name:         "node-0",
				P2PAddress:   "/ip4/127.0.0.1/tcp/19300",
			},
		}, nil
	}

	reqHandler.CustomSyncBlocksCallback = func(request *pb.SyncBlocksRequest) (*pb.SyncBlocksResponse, error) {
		blocks := request.GetBlocks()
		var lastSynced uint64
		for _, blockData := range blocks {
			lastSynced = blockData.GetBlockNumber()
		}
		// Parent Chain relies on persistent DB and doesn't re-execute transactions during block sync.
		// Simply acknowledge the sync request to unblock consensus startup.
		return &pb.SyncBlocksResponse{
			SyncedCount:     uint64(len(blocks)),
			LastSyncedBlock: lastSynced,
		}, nil
	}

	reqHandler.CustomGetBlocksRangeCallback = func(request *pb.GetBlocksRangeRequest) (*pb.GetBlocksRangeResponse, error) {
		// Parent Chain does not serve blocks to the network yet.
		// Return an empty response to prevent panic when consensus requests blocks.
		return &pb.GetBlocksRangeResponse{
			Blocks: []*pb.BlockData{},
			Count:  0,
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
