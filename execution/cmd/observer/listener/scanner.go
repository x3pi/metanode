package listener

import (
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/contracts/cross_chain_contract"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/processor"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// PendingBatchData chứa thông tin batch bị pending để resweeper xử lý và failover
type PendingBatchData struct {
	TargetIndex int
	RemoteChain tcp_config.RemoteChain
	Events      []cross_chain_contract.EmbassyEventInput
	Timestamp   time.Time
	SubmitBlock uint64      // Block number trên local chain lúc batchSubmit được gửi (để Resweeper quét từ block này)
	TxHash      common.Hash // Thẻ lưu txHash của submitBatch để theo dõi
}

// scanProgressUpdate là yêu cầu cập nhật lastBlock lên config SMC sau khi gửi batch thành công
type scanProgressUpdate struct {
	chainId   uint64
	lastBlock uint64
}

type CrossChainScanner struct {
	localClients []*client_tcp.Client // Phục vụ deterministic routing và failover
	cfg          *tcp_config.ClientConfig
	processor    *processor.SupervisorProcessor
	walletPool   *client_tcp.WalletPool
	scanInterval time.Duration
	// Chan nhận yêu cầu cập nhật lastBlock — goroutine riêng xử lý tuần tự để tránh nonce conflict
	progressCh chan scanProgressUpdate
	// localBlockCh: nhận block number từ receipt watcher khi batchSubmit TX được confirm trên local chain
	localBlockCh   chan uint64
	txHashToWallet sync.Map
	pendingBatches sync.Map                        // map[[32]byte]*PendingBatchData
	remoteClients  map[uint64][]*client_tcp.Client // client pool cho từng remote chain
}

func NewCrossChainScanner(
	cfg *tcp_config.ClientConfig,
	proc *processor.SupervisorProcessor,
) *CrossChainScanner {
	s := &CrossChainScanner{
		cfg:          cfg,
		processor:    proc,
		scanInterval: 1 * time.Second,
		progressCh:   make(chan scanProgressUpdate, 256),
	}
	s.localClients = make([]*client_tcp.Client, 0)
	if len(cfg.ChainNodes) > 0 {
		for _, nodeAddr := range cfg.ChainNodes {
			nodeCfg := *cfg
			nodeCfg.ParentConnectionAddress = nodeAddr

			newCli, err := client_tcp.NewClientNonBlocking(&nodeCfg)
			if err == nil {
				s.localClients = append(s.localClients, newCli)
			} else {
				logger.Error("Failed to init node client %s: %v", nodeAddr, err)
			}
		}
	}

	s.remoteClients = make(map[uint64][]*client_tcp.Client)
	for _, rc := range cfg.RemoteChains {
		clients := make([]*client_tcp.Client, 0)
		for _, nodeAddr := range rc.ChainNodes {
			nodeCfg := *cfg
			nodeCfg.ParentConnectionAddress = nodeAddr
			nodeCfg.ConnectionAddress_ = "" // client mode only

			newCli, err := client_tcp.NewClientNonBlocking(&nodeCfg)
			if err == nil {
				clients = append(clients, newCli)
			} else {
				logger.Error("Failed to init remote client %s: %v", nodeAddr, err)
			}
		}
		s.remoteClients[rc.NationId] = clients
	}

	return s
}

// GetActiveClient tìm và trả về client đang có kết nối (sống).
// Sẽ dò bắt đầu từ preferredIndex, nếu chết sẽ tự động nhảy vòng tròn (round-robin) qua node kế tiếp.
// Tính năng này giúp các hàm đùn đẩy công việc linh hoạt ngay cả khi đứt kết nối liên tục.
func (s *CrossChainScanner) GetActiveClient(preferredIndex int) (*client_tcp.Client, int) {
	if len(s.localClients) == 0 {
		return nil, -1
	}
	total := len(s.localClients)
	// Tránh chia cho 0 hoặc index âm
	if preferredIndex < 0 {
		preferredIndex = 0
	}

	for i := 0; i < total; i++ {
		idx := (preferredIndex + i) % total
		cli := s.localClients[idx]
		if cli.IsParentConnected() {
			return cli, idx
		}
	}
	// Fallback trường hợp tất cả đều sập (vẫn trả về node ban đầu để thử)
	idx := preferredIndex % total
	return s.localClients[idx], idx
}

// GetActiveRemoteClient tìm client sống cho một remote chain cụ thể
func (s *CrossChainScanner) GetActiveRemoteClient(nationId uint64, preferredIndex int) (*client_tcp.Client, int) {
	clients, ok := s.remoteClients[nationId]
	if !ok || len(clients) == 0 {
		return nil, -1
	}
	total := len(clients)
	if preferredIndex < 0 {
		preferredIndex = 0
	}

	for i := 0; i < total; i++ {
		idx := (preferredIndex + i) % total
		cli := clients[idx]
		if cli.IsParentConnected() {
			return cli, idx
		}
	}
	// Fallback
	idx := preferredIndex % total
	return clients[idx], idx
}

// SetWalletPool cài đặt wallet pool từ ngoài (gọi trước Start nếu muốn dùng nhiều ví).
func (s *CrossChainScanner) SetWalletPool(pool *client_tcp.WalletPool) {
	s.walletPool = pool
}

// Start khởi động:
//  1. Goroutine watcher nhận receipt → MarkReady wallet
//  2. Goroutine xử lý progressCh → gửi updateScanProgress lên config SMC
//  3. 1 goroutine scan cho mỗi remote_chain trong config
func (s *CrossChainScanner) Start() {
	if s.walletPool == nil {
		const totalPoolSize = 16
		embassyAddr := common.HexToAddress(s.cfg.ParentAddress) // unique per embassy instance (từ BLS private key riêng)
		logger.Info("ParentAddress %v", embassyAddr)
		allAddrs := client_tcp.DeriveEmbassyWalletAddresses(embassyAddr, totalPoolSize)
		for j, addr := range allAddrs {
			logger.Info("🔑 [Scanner] Derived wallet embassy=%s idx=%d → %s",
				embassyAddr.Hex()[:10], j, addr.Hex())
		}

		s.walletPool = client_tcp.NewWalletPool(allAddrs)
		logger.Info("🏦 [Scanner] WalletPool initialized: %d derived wallets (embassy=%s)",
			len(allAddrs), embassyAddr.Hex())

		// FETCH INITIAL NONCES
		cli, idx := s.GetActiveClient(0)
		logger.Info("⏳ [Scanner] Fetching initial nonces from Node[%d]...", idx)
		for _, w := range s.walletPool.Wallets {
			for {
				nonce, err := cli.ChainGetNonce(w.Address())
				if err == nil {
					w.ExpectedNonce = nonce
					logger.Info("✅ [Scanner] Initialized wallet %s expectedNonce = %d", w.Address().Hex()[:10], nonce)
					break
				}
				logger.Warn("⚠️ [Scanner] Failed to fetch initial nonce for wallet %s: %v. Retrying in 1s...", w.Address().Hex()[:10], err)
				time.Sleep(1 * time.Second)
				cli, idx = s.GetActiveClient(idx) // Thử đổi client khác nếu node hiện tại chết
			}
		}
	}

	// Receipt watcher: Bộ theo dõi thông minh tự đảo Node khi lỗi
	s.localBlockCh = make(chan uint64, 100)
	go s.runSmartWatcher()

	// Progress updater: cập nhật lastBlock lên config SMC (tuần tự, tránh nonce conflict)
	go s.runProgressUpdater()
	go s.runResweeper()

	if len(s.cfg.RemoteChains) == 0 {
		logger.Warn("⚠️  [Scanner] No remote_chains configured, scanner idle")
		return
	}
	// Đọc block đã scan từ config contract để resume đúng vị trí
	initialProgress := s.loadInitialScanProgress()

	for _, rc := range s.cfg.RemoteChains {
		rc := rc // capture loop variable
		connAddr := rc.ConnectionAddress
		logger.Info("🚀 [Scanner] Starting scan goroutine: %s (nationId=%d, addr=%s, resumeBlock=%d)",
			rc.Name, rc.NationId, connAddr, initialProgress[rc.NationId])
		go s.runChainScanner(rc, connAddr, initialProgress[rc.NationId])
	}
}
