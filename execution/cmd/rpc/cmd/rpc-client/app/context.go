package app

import (
	"fmt"

	ethCommon "github.com/ethereum/go-ethereum/common"
	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/config"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/internal/ws_interceptor"
	"github.com/syndtr/goleveldb/leveldb"

	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/store"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/connection_manager"
	"github.com/meta-node-blockchain/meta-node/pkg/debug"
	"github.com/meta-node-blockchain/meta-node/pkg/ldb_storage"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	"github.com/meta-node-blockchain/meta-node/pkg/rpc_client"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

type Context struct {
	// RPC Client
	ClientRpc *rpc_client.ClientRPC

	// Private Key Store
	PKS *store.PrivateKeyStore

	// TCP Client
	ClientTcp *client_tcp.Client

	// Configurations
	Cfg    *config.Config
	TcpCfg *tcp_config.ClientConfig

	// LevelDB Storage for BLS wallets
	LdbBlsWallet    *ldb_storage.LevelDBStorage
	LdbNotification *storage.NotificationStorage

	// Contract Free Gas Storage
	LdbContractFreeGas *storage.ContractFreeGasStorage

	// Transaction Storage
	LdbRobotTransaction *storage.RobotTransaction

	// Artifact Registry Storage
	LdbArtifactRegistry *storage.ArtifactStorage

	// Node BLS keys
	NodeBlsPrivateKey common.PrivateKey
	NodeBlsPublicKey  common.PublicKey

	SubInterceptor *ws_interceptor.SubscriptionInterceptor

	ErrorDecoder *debug.ErrorDecoder

	// Chain TCP Connection Pool (shared pool cho internal operations)
	ChainPool *connection_manager.ChainConnectionPool
}

// New tạo Application Context với tất cả dependencies
func New(cfg *config.Config, tcpCfg *tcp_config.ClientConfig) (*Context, error) {
	// 1. Initialize Private Key Store
	pkStore, err := store.NewPrivateKeyStore(cfg.MasterPassword, cfg.AppPepper)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize PrivateKeyStore: %w", err)
	}
	// 2. Load Node BLS key
	if cfg.PrivateKey == "" {
		return nil, fmt.Errorf("node's BLS private key is missing in config")
	}
	keyPair := bls.NewKeyPair(ethCommon.FromHex(cfg.PrivateKey))
	if keyPair == nil {
		return nil, fmt.Errorf("invalid BLS private key in config: key is invalid or out of bounds")
	}
	// 3. Create RPC client
	clientRpc, err := rpc_client.NewClientRPC(cfg.RPCServerURL, cfg.WSSServerURL, cfg.PrivateKey, cfg.ChainId)
	if err != nil {
		pkStore.Close()
		return nil, fmt.Errorf("failed to create RPC client: %w", err)
	}
	clientRpc.ChainId = cfg.ChainId
	// 4. Create TCP client
	clientTcp, err := client_tcp.NewClient(tcpCfg)
	if err != nil {
		pkStore.Close()
		return nil, fmt.Errorf("failed to create TCP client: %w", err)
	}
	// 5. Initialize LevelDB for BLS wallets
	ldbBlsWallets, err := ldb_storage.NewLevelDBStorage(cfg.LdbBlsWalletsPath)
	if err != nil {
		pkStore.Close()
		return nil, fmt.Errorf("failed to initialize LevelDB: %w", err)
	}
	db, err := leveldb.OpenFile(cfg.LdbNotificationPath, nil)
	if err != nil {
		return nil, fmt.Errorf("lỗi mở LevelDB tại '%s': %w", cfg.LdbNotificationPath, err)
	}
	ldbNotification := storage.NewNotificationStorage(db)

	// 6. Initialize LevelDB for Contract Free Gas
	ldbContractFreeGas, err := ldb_storage.NewLevelDBStorage(cfg.LdbContractFreeGasPath)
	if err != nil {
		pkStore.Close()
		ldbBlsWallets.Close()
		return nil, fmt.Errorf("failed to initialize Contract Free Gas LevelDB: %w", err)
	}
	contractFreeGasStorage := storage.NewContractFreeGasStorage(ldbContractFreeGas)
	contractFreeGasStorage.AddContract(ethCommon.HexToAddress(cfg.ContractsInterceptor[0]), ethCommon.HexToAddress(cfg.OwnerRpcAddress))

	// 7. Initialize LevelDB for Transaction Storage
	var transactionStorage *storage.RobotTransaction
	if cfg.LdbRobotTransactionPath != "" {
		txDB, err := leveldb.OpenFile(cfg.LdbRobotTransactionPath, nil)
		if err != nil {
			pkStore.Close()
			ldbBlsWallets.Close()
			return nil, fmt.Errorf("lỗi mở LevelDB transaction tại '%s': %w", cfg.LdbRobotTransactionPath, err)
		}
		transactionStorage = storage.NewTransactionStorage(txDB)
		logger.Info("✅ Transaction storage initialized at: %s", cfg.LdbRobotTransactionPath)
	}

	// 8. Initialize LevelDB for Artifact Registry
	var artifactStorage *storage.ArtifactStorage
	if cfg.LdbArtifactRegistryPath != "" {
		artifactDB, err := leveldb.OpenFile(cfg.LdbArtifactRegistryPath, nil)
		if err != nil {
			pkStore.Close()
			ldbBlsWallets.Close()
			if transactionStorage != nil {
				transactionStorage.Close()
			}
			return nil, fmt.Errorf("lỗi mở LevelDB artifact registry tại '%s': %w", cfg.LdbArtifactRegistryPath, err)
		}
		artifactStorage = storage.NewArtifactStorage(artifactDB)
		logger.Info("✅ Artifact Registry storage initialized at: %s", cfg.LdbArtifactRegistryPath)
	}
	// 9. Initialize Error Decoder
	errorDecoder := debug.NewErrorDecoder(artifactStorage)

	subInterceptor := ws_interceptor.NewSubscriptionInterceptor(cfg)
	ctx := &Context{
		ClientRpc:           clientRpc,
		PKS:                 pkStore,
		ClientTcp:           clientTcp,
		Cfg:                 cfg,
		TcpCfg:              tcpCfg,
		LdbBlsWallet:        ldbBlsWallets,
		LdbNotification:     ldbNotification,
		LdbContractFreeGas:  contractFreeGasStorage,
		LdbRobotTransaction: transactionStorage,
		LdbArtifactRegistry: artifactStorage,
		NodeBlsPrivateKey:   keyPair.PrivateKey(),
		NodeBlsPublicKey:    keyPair.PublicKey(),
		SubInterceptor:      subInterceptor,
		ErrorDecoder:        errorDecoder,
	}

	// 10. Initialize Chain Connection Pool (TCP)
	if tcpCfg != nil && tcpCfg.ParentConnectionAddress != "" {
		msgSender := network.NewMessageSender("1.0.0")
		chainPool, err := connection_manager.NewChainConnectionPool(
			tcpCfg.ParentConnectionAddress,
			10, // 10 connections trong pool
			msgSender,
		)
		if err != nil {
			logger.Warn("⚠️ Failed to initialize ChainConnectionPool: %v (falling back to HTTP)", err)
		} else {
			ctx.ChainPool = chainPool
			logger.Info("✅ ChainConnectionPool initialized: %d connections to %s", chainPool.ActiveCount(), tcpCfg.ParentConnectionAddress)
		}
	}

	return ctx, nil
}

// Close đóng tất cả resources
func (ctx *Context) Close() error {
	logger.Info("Closing application context...")

	var errors []error

	if ctx.ChainPool != nil {
		ctx.ChainPool.Close()
	}

	if ctx.PKS != nil {
		if err := ctx.PKS.Close(); err != nil {
			logger.Error("Failed to close PrivateKeyStore: %v", err)
			errors = append(errors, err)
		}
	}

	if ctx.LdbBlsWallet != nil {
		ctx.LdbBlsWallet.Close()
	}

	if ctx.LdbRobotTransaction != nil {
		if err := ctx.LdbRobotTransaction.Close(); err != nil {
			logger.Error("Failed to close TransactionStorage: %v", err)
			errors = append(errors, err)
		}
	}

	if ctx.LdbArtifactRegistry != nil {
		if err := ctx.LdbArtifactRegistry.Close(); err != nil {
			logger.Error("Failed to close ArtifactRegistry: %v", err)
			errors = append(errors, err)
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("errors during context close: %v", errors)
	}
	return nil
}
