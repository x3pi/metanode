package helppay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
)

const (
	defaultMinBalance        = "10000000000000000"    // 0.01 ETH (10^16 wei)
	defaultExtraAmount       = "100000000000000000"   // 0.1 ETH (10^17 wei)
	defaultMaxDailyPerIP     = "10000000000000000000" // 10 ETH per IP per day (10 * 10^18 wei)
	defaultRecipientCooldown = 30 * time.Second
	queueCapacity            = 1000
	defaultTimeout           = 5 * time.Second
)

// Manager manages a pool of sponsor wallets (ví chuyển tiền hộ).
// It dispatches top-up tasks to whichever wallet worker is idle, preventing
// cross-wallet blocking and avoiding nonce collisions on individual wallets.
type Manager struct {
	mu             sync.RWMutex
	enabled        bool
	minBalance     *big.Int
	extraAmount    *big.Int
	chainID        *big.Int
	configPath     string
	maxTimeout     time.Duration
	adminAddresses map[common.Address]struct{}

	// Rate limiting & anti-drain protection
	rateMu         sync.Mutex
	maxDailyPerIP  *big.Int
	ipUsage        map[string]*IPRateLimitRecord
	recipientUsage map[common.Address]*RecipientRateLimitRecord

	chainState  *blockchain.ChainState
	txPoolAdder TxPoolAdder

	wallets         map[common.Address]*WalletWorker
	staticWallets   map[common.Address]struct{} // ví được khai báo từ static config (không bị xóa bởi cluster sync)
	sponsorJobQueue chan *SponsorGasTask        // Hàng đợi công việc nạp gas phân phối tới các worker ví chuyển hộ rảnh rỗi
	inflightMu      sync.Map                    // map[common.Address]*sync.Mutex (chống nạp trùng lặp đồng thời)
	inflightTopUps  atomic.Int64                // Đếm số lượng yêu cầu nạp gas đang xử lý trên toàn node (RPC + TCP)
	lastOverloadedAlertAt atomic.Int64          // Unix timestamp lần cuối bắn cảnh báo Telegram khi hàng đợi quá tải

	ctx             context.Context
	cancel          context.CancelFunc
	started         bool
	syncBroadcaster func(payload []byte) error
	stateFetcher    func() ([]byte, error)
}

// NewManager creates a new HelpPay sponsor wallet manager.
func NewManager(
	cfg config.HelpPayConfig,
	chainState *blockchain.ChainState,
	txPoolAdder TxPoolAdder,
	chainID *big.Int,
	rootPath string,
) *Manager {
	ctx, cancel := context.WithCancel(context.Background())

	minBal := new(big.Int)
	if cfg.MinBalance != "" {
		if _, ok := minBal.SetString(cfg.MinBalance, 10); !ok {
			minBal.SetString(defaultMinBalance, 10)
		}
	} else {
		minBal.SetString(defaultMinBalance, 10)
	}

	extraAmt := new(big.Int)
	if cfg.ExtraAmount != "" {
		if _, ok := extraAmt.SetString(cfg.ExtraAmount, 10); !ok {
			extraAmt.SetString(defaultExtraAmount, 10)
		}
	} else {
		extraAmt.SetString(defaultExtraAmount, 10)
	}

	cid := big.NewInt(991)
	if chainID != nil && chainID.Sign() > 0 {
		cid = new(big.Int).Set(chainID)
	}

	configPath := cfg.ConfigFile
	if configPath == "" {
		configPath = filepath.Join(rootPath, "helppay_wallets.json")
	}

	admins := make(map[common.Address]struct{})
	for _, a := range cfg.AdminAddresses {
		a = strings.TrimSpace(a)
		if common.IsHexAddress(a) {
			admins[common.HexToAddress(a)] = struct{}{}
		}
	}
	if cfg.AdminAddress != "" {
		a := strings.TrimSpace(cfg.AdminAddress)
		if common.IsHexAddress(a) {
			admins[common.HexToAddress(a)] = struct{}{}
		}
	}

	maxDailyIP, ok := new(big.Int).SetString(defaultMaxDailyPerIP, 10)
	if !ok {
		maxDailyIP = new(big.Int).Mul(big.NewInt(10), big.NewInt(1000000000000000000))
	}

	m := &Manager{
		enabled:         cfg.Enabled,
		minBalance:      minBal,
		extraAmount:     extraAmt,
		chainID:         cid,
		configPath:      configPath,
		maxTimeout:      defaultTimeout,
		adminAddresses:  admins,
		maxDailyPerIP:   maxDailyIP,
		ipUsage:         make(map[string]*IPRateLimitRecord),
		recipientUsage:  make(map[common.Address]*RecipientRateLimitRecord),
		chainState:      chainState,
		txPoolAdder:     txPoolAdder,
		wallets:         make(map[common.Address]*WalletWorker),
		staticWallets:   make(map[common.Address]struct{}),
		sponsorJobQueue: make(chan *SponsorGasTask, queueCapacity),
		ctx:             ctx,
		cancel:          cancel,
	}

	// Pre-load wallets from static config if specified
	for _, w := range cfg.Wallets {
		if strings.TrimSpace(w.PrivateKey) != "" {
			addr, err := m.addWalletInternal(w.PrivateKey)
			if err != nil {
				logger.Warn("⚠️ [HelpPay] Failed to load wallet from static config: %v", err)
			} else {
				m.staticWallets[addr] = struct{}{}
			}
		}
	}

	return m
}

// SetSyncBroadcaster sets the function used to broadcast state updates across the Raft cluster.
func (m *Manager) SetSyncBroadcaster(fn func(payload []byte) error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.syncBroadcaster = fn
}

// SetStateFetcher sets the function used to fetch the full HelpPay state from Raft cluster peers.
func (m *Manager) SetStateFetcher(fn func() ([]byte, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stateFetcher = fn
}

// Start loads persisted wallets from disk and starts worker goroutines for all active wallets.
func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.started {
		return
	}
	m.started = true

	// Load dynamic wallets persisted on disk
	if err := m.loadFromDiskLocked(); err != nil {
		logger.Debug("[HelpPay] No previous dynamic config loaded from %s: %v", m.configPath, err)
	}

	// Start a worker loop for each wallet
	for _, worker := range m.wallets {
		if worker.cancel == nil {
			wCtx, wCancel := context.WithCancel(m.ctx)
			worker.cancel = wCancel
			go m.workerLoop(wCtx, worker)
		}
	}

	logger.Info("✅ [HelpPay] Manager started with %d sponsor wallets (enabled=%v, minBalance=%s, extraAmount=%s)",
		len(m.wallets), m.enabled, m.minBalance.String(), m.extraAmount.String())

	// Launch startup catch-up sync with peers in background if a state fetcher is configured
	if m.stateFetcher != nil {
		go m.startStartupCatchUp()
	}
}

func (m *Manager) startStartupCatchUp() {
	backoffs := []time.Duration{
		500 * time.Millisecond,
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
	}
	for i, d := range backoffs {
		select {
		case <-m.ctx.Done():
			return
		case <-time.After(d):
		}

		added, err := m.CatchUpFromCluster()
		if err == nil {
			logger.Info("🔄 [HelpPay] Startup cluster catch-up succeeded on attempt %d (%d new wallets synced)", i+1, added)
			return
		}
		logger.Debug("[HelpPay] Catch-up sync attempt %d/%d not ready: %v", i+1, len(backoffs), err)
	}
	logger.Debug("[HelpPay] Startup catch-up completed, keeping local disk state")
}

// Stop shuts down all wallet workers and saves state.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.started {
		return
	}
	m.started = false
	m.cancel()

	for _, w := range m.wallets {
		if w.cancel != nil {
			w.cancel()
			w.cancel = nil
		}
	}
	if err := m.saveToDiskLocked(); err != nil {
		logger.Warn("⚠️ [HelpPay] Failed to save state to disk: %v", err)
	}
	logger.Info("🛑 [HelpPay] Manager stopped")
}

// IsEnabled reports whether help-pay top-up is active and has at least one sponsor wallet.
func (m *Manager) IsEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.enabled && len(m.wallets) > 0
}

// MinBalance returns the configured minimum balance threshold.
func (m *Manager) MinBalance() *big.Int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return new(big.Int).Set(m.minBalance)
}

// ExtraAmount returns the configured top-up amount.
func (m *Manager) ExtraAmount() *big.Int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return new(big.Int).Set(m.extraAmount)
}

// MaxDailyPerIP returns the maximum daily HelpPay allocation allowed per client IP.
func (m *Manager) MaxDailyPerIP() *big.Int {
	m.rateMu.Lock()
	defer m.rateMu.Unlock()
	return new(big.Int).Set(m.maxDailyPerIP)
}

// SetMaxDailyPerIP sets the maximum daily HelpPay allocation allowed per client IP.
func (m *Manager) SetMaxDailyPerIP(limit *big.Int) {
	m.rateMu.Lock()
	defer m.rateMu.Unlock()
	m.maxDailyPerIP = new(big.Int).Set(limit)
}

// IsSponsorWallet checks if an address belongs to the sponsor wallet pool.
func (m *Manager) IsSponsorWallet(addr common.Address) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.wallets[addr]
	return exists
}

// ActiveWalletCount returns the number of registered sponsor wallets.
func (m *Manager) ActiveWalletCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.wallets)
}

// IsAdmin checks if the given address is authorized as an admin.
// If no admin addresses are configured in the config file, any caller is permitted.
func (m *Manager) IsAdmin(addr common.Address) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.adminAddresses) == 0 {
		return true
	}
	_, ok := m.adminAddresses[addr]
	return ok
}

// HasAdminsConfigured reports whether at least one admin address is configured.
func (m *Manager) HasAdminsConfigured() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.adminAddresses) > 0
}

// GetAdmins returns the list of configured admin addresses in hex.
func (m *Manager) GetAdmins() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	res := make([]string, 0, len(m.adminAddresses))
	for a := range m.adminAddresses {
		res = append(res, a.Hex())
	}
	return res
}

// VerifyAdminSignature verifies that the given signature was produced by an authorized admin
// for expectedMsg. Supports standard Ethereum personal_sign (EIP-191) as well as raw keccak256 hash.
func (m *Manager) VerifyAdminSignature(expectedMsg string, sigHex string, claimedAdmin common.Address) error {
	m.mu.RLock()
	hasAdmins := len(m.adminAddresses) > 0
	_, isConfiguredAdmin := m.adminAddresses[claimedAdmin]
	m.mu.RUnlock()

	if hasAdmins && !isConfiguredAdmin {
		return fmt.Errorf("claimed address %s is not an authorized admin", claimedAdmin.Hex())
	}

	cleanSig := strings.TrimSpace(sigHex)
	sigBytes, err := hexutil.Decode(cleanSig)
	if err != nil {
		return fmt.Errorf("invalid signature hex: %w", err)
	}
	if len(sigBytes) != 65 {
		return fmt.Errorf("invalid signature length: expected 65, got %d", len(sigBytes))
	}

	s := make([]byte, 65)
	copy(s, sigBytes)
	if s[64] >= 27 {
		s[64] -= 27
	}
	if s[64] != 0 && s[64] != 1 {
		return fmt.Errorf("invalid signature recovery byte: %d", s[64])
	}

	// 1. Try Ethereum personal_sign TextHash
	textHash := accounts.TextHash([]byte(expectedMsg))
	if pub, err := crypto.SigToPub(textHash, s); err == nil {
		recovered := crypto.PubkeyToAddress(*pub)
		if recovered == claimedAdmin {
			return nil
		}
	}

	// 2. Try raw Keccak256 hash
	rawHash := crypto.Keccak256Hash([]byte(expectedMsg))
	if pub, err := crypto.SigToPub(rawHash.Bytes(), s); err == nil {
		recovered := crypto.PubkeyToAddress(*pub)
		if recovered == claimedAdmin {
			return nil
		}
	}

	return fmt.Errorf("signature verification failed: recovered signer does not match %s", claimedAdmin.Hex())
}

// SetEnabled toggles help-pay on/off and syncs across the cluster.
func (m *Manager) SetEnabled(enabled bool, broadcast bool) error {
	m.mu.Lock()
	m.enabled = enabled
	if err := m.saveToDiskLocked(); err != nil {
		logger.Warn("⚠️ [HelpPay] Failed to save state to disk: %v", err)
	}

	var broadcaster func(payload []byte) error
	if broadcast {
		broadcaster = m.syncBroadcaster
	}
	m.mu.Unlock()

	logger.Info("⚙️ [HelpPay] Feature toggled: enabled=%v", enabled)

	if broadcaster != nil {
		payload, _ := json.Marshal(SyncPayload{
			Action:  ActionSetConfig,
			Enabled: &enabled,
		})
		go func() {
			_ = broadcaster(payload)
		}()
	}

	return nil
}

// GetBalance returns the current on-chain balance of an address (including pending balance).
func (m *Manager) GetBalance(address common.Address) *big.Int {
	if m.chainState == nil || m.chainState.GetAccountStateDB() == nil {
		return big.NewInt(0)
	}
	as, err := m.chainState.GetAccountStateDB().AccountStateReadOnly(address)
	if err != nil || as == nil {
		return big.NewInt(0)
	}
	bal := new(big.Int)
	if as.Balance() != nil {
		bal.Add(bal, as.Balance())
	}
	if as.PendingBalance() != nil {
		bal.Add(bal, as.PendingBalance())
	}
	return bal
}

// NeedsTopUp checks if recipient's balance is below threshold and eligible for sponsor gas.
func (m *Manager) NeedsTopUp(recipient common.Address) bool {
	if !m.IsEnabled() || m.IsSponsorWallet(recipient) {
		return false
	}
	return m.GetBalance(recipient).Cmp(m.MinBalance()) < 0
}

// PreCheckAndAcquire thực hiện kiểm tra các điều kiện an toàn rẻ tiền (Gas limit, Nonce hợp lệ)
// và cấp phát slot giới hạn đồng thời (ActiveWalletCount * 20) dùng chung cho cả RPC và TCP.
//
// QUY TẮC BẢO VỆ VÍ ĐÃ ĐỦ TIỀN:
// Nếu số dư của ví >= minBalance (needsTopUp = false), hàm trả về ngay lập tức (nil, false, nil)
// mà KHÔNG tốn tài nguyên kiểm tra, KHÔNG chiếm slot in-flight, KHÔNG làm nghẽn giao dịch ví có tiền.
//
// Nếu ví thiếu tiền (needsTopUp = true):
// - Kiểm tra Gas limit [21.000, 30.000.000]
// - Kiểm tra Nonce [expectedNonce, expectedNonce + 5]
// - Kiểm tra giới hạn concurrency (ActiveWalletCount * 20)
// - Trả về hàm release() để giải phóng slot khi xử lý xong (caller BẮT BUỘC defer release())
func (m *Manager) PreCheckAndAcquire(
	fromAddr common.Address,
	txNonce uint64,
	txGas uint64,
	txAmount *big.Int,
	clientIP string,
	ingressType string,
) (func(), bool, error) {
	if !m.IsEnabled() || !m.NeedsTopUp(fromAddr) {
		return nil, false, nil
	}

	// 1. Kiểm tra Gas limit rẻ tiền: Intrinsic gas tối thiểu 21.000; tối đa theo block gas limit 30.000.000.
	if txGas < 21000 || txGas > 30000000 {
		return nil, true, fmt.Errorf("transaction gas limit %d invalid (must be between 21000 and 30000000)", txGas)
	}

	// 2. HelpPay CHỈ tài trợ phí gas (extraAmount), TUYỆT ĐỐI KHÔNG tài trợ giá trị chuyển amount/value!
	// Nếu giao dịch đòi chuyển tiền (txAmount > 0) mà số dư của ví (kể cả sau khi nhận gas extraAmount)
	// vẫn không đủ để trang trải txAmount, từ chối ngay lập tức để tránh kẻ gian rút tỉa gas sponsor.
	if txAmount != nil && txAmount.Sign() > 0 {
		currentBal := m.GetBalance(fromAddr)
		projectedBal := new(big.Int).Add(currentBal, m.ExtraAmount())
		if projectedBal.Cmp(txAmount) < 0 {
			return nil, true, fmt.Errorf("insufficient funds for transfer amount: tx wants to transfer %s wei, but account has %s wei (HelpPay only sponsors gas, not transfer amount)",
				txAmount.String(), currentBal.String())
		}
	}

	// 3. Kiểm tra Nonce rẻ tiền: từ chối nếu nonce cũ (< expectedNonce) hoặc quá xa (> expectedNonce + 5).
	var expectedNonce uint64
	if m.chainState != nil {
		if asDB := m.chainState.GetAccountStateDB(); asDB != nil {
			if as, err := asDB.AccountStateReadOnly(fromAddr); err == nil && as != nil {
				expectedNonce = as.Nonce()
			}
		}
	}
	if txNonce < expectedNonce {
		return nil, true, fmt.Errorf("nonce too low: address %s has nonce %d, tx has nonce %d", fromAddr.Hex(), expectedNonce, txNonce)
	}
	if txNonce > expectedNonce+5 {
		return nil, true, fmt.Errorf("nonce too high for unfunded account: address %s has nonce %d, tx has nonce %d (max ahead is +5)", fromAddr.Hex(), expectedNonce, txNonce)
	}

	// 3. Dynamic Bounded Concurrency: ActiveWalletCount * 20
	walletCount := m.ActiveWalletCount()
	if walletCount <= 0 {
		walletCount = 1
	}
	maxConcurrent := int64(walletCount * 20)

	currentInflight := m.inflightTopUps.Add(1)
	if currentInflight > maxConcurrent {
		m.inflightTopUps.Add(-1)
		m.sendOverloadedTeleAlert(ingressType, clientIP, walletCount, maxConcurrent)
		return nil, true, fmt.Errorf("helppay concurrency limit reached (%d in flight for %d wallets), please retry", maxConcurrent, walletCount)
	}

	release := func() {
		m.inflightTopUps.Add(-1)
	}
	return release, true, nil
}

func (m *Manager) sendOverloadedTeleAlert(ingressType, clientIP string, walletCount int, maxConcurrent int64) {
	now := time.Now().Unix()
	last := m.lastOverloadedAlertAt.Load()
	if now-last > 60 { // Rate limit: at most once every 60s
		if m.lastOverloadedAlertAt.CompareAndSwap(last, now) {
			logger.Telegram(fmt.Sprintf("⚠️ [HelpPay Alert][%s] Hàng đợi top-up đầy (%d/%d in-flight, %d ví sponsor)! Các ví sponsor đang quá tải hoặc không đủ số lượng ví trả hộ (IP: %s).", ingressType, maxConcurrent, maxConcurrent, walletCount, clientIP))
		}
	}
}

// CheckAndTopUp inspects recipient balance and triggers automated gas top-up if below threshold.
func (m *Manager) CheckAndTopUp(recipient common.Address) error {
	return m.CheckAndTopUpWithClient(recipient, "")
}

// CheckAndTopUpWithClient inspects recipient balance, validates client IP rate limits (max 10 ETH/day),
// enforces recipient cooldowns, and performs automated top-up.
func (m *Manager) CheckAndTopUpWithClient(recipient common.Address, clientIP string) error {
	if !m.NeedsTopUp(recipient) {
		return nil
	}

	cleanIP := strings.TrimSpace(clientIP)
	now := time.Now()
	today := uint64(now.Unix() / 86400)

	m.rateMu.Lock()
	// 1. IP Rate Limiting: enforce maximum 10 ETH per IP per day (configurable via maxDailyPerIP)
	if cleanIP != "" && cleanIP != "127.0.0.1" && cleanIP != "::1" && cleanIP != "localhost" {
		rec, exists := m.ipUsage[cleanIP]
		if !exists || rec.DayNumber != today {
			rec = &IPRateLimitRecord{
				DayNumber:  today,
				TotalTopUp: big.NewInt(0),
			}
			m.ipUsage[cleanIP] = rec
		}
		newTotal := new(big.Int).Add(rec.TotalTopUp, m.ExtraAmount())
		if newTotal.Cmp(m.maxDailyPerIP) > 0 {
			m.rateMu.Unlock()
			return fmt.Errorf("helppay: daily rate limit exceeded for client IP %s (allocated %s wei today, max %s wei/day)",
				cleanIP, rec.TotalTopUp.String(), m.maxDailyPerIP.String())
		}
	}

	// 2. Recipient Cooldown: prevent repeated top-up draining on the same address within cooldown period
	if rRec, exists := m.recipientUsage[recipient]; exists && rRec.DayNumber == today {
		if now.Unix()-rRec.LastTopUpUnix < int64(defaultRecipientCooldown/time.Second) {
			m.rateMu.Unlock()
			return fmt.Errorf("helppay: recipient %s is under cooldown (last top-up %ds ago, please wait %v)",
				recipient.Hex(), now.Unix()-rRec.LastTopUpUnix, defaultRecipientCooldown)
		}
	}
	m.rateMu.Unlock()

	// Execute top-up
	err := m.TopUp(recipient)
	if err != nil {
		return err
	}

	// Record allocation on success
	m.rateMu.Lock()
	if cleanIP != "" && cleanIP != "127.0.0.1" && cleanIP != "::1" && cleanIP != "localhost" {
		if rec, exists := m.ipUsage[cleanIP]; exists && rec.DayNumber == today {
			rec.TotalTopUp.Add(rec.TotalTopUp, m.ExtraAmount())
		}
	}
	rRec, exists := m.recipientUsage[recipient]
	if !exists || rRec.DayNumber != today {
		rRec = &RecipientRateLimitRecord{DayNumber: today}
		m.recipientUsage[recipient] = rRec
	}
	rRec.LastTopUpUnix = now.Unix()
	rRec.CountToday++
	m.rateMu.Unlock()

	return nil
}

// TopUp enqueues a top-up request to the wallet worker pool and waits for block confirmation.
// Requests for the same recipient are deduplicated so concurrent transactions do not cause duplicate transfers.
func (m *Manager) TopUp(recipient common.Address) error {
	val, _ := m.inflightMu.LoadOrStore(recipient, &sync.Mutex{})
	mu := val.(*sync.Mutex)
	mu.Lock()
	defer func() {
		mu.Unlock()
		m.inflightMu.Delete(recipient)
	}()

	// Re-check balance after obtaining the per-recipient lock
	if m.GetBalance(recipient).Cmp(m.MinBalance()) >= 0 {
		return nil
	}

	resultCh := make(chan error, 1)
	req := &SponsorGasTask{
		Recipient: recipient,
		Amount:    m.ExtraAmount(),
		ResultCh:  resultCh,
	}

	select {
	case m.sponsorJobQueue <- req:
	case <-time.After(5 * time.Second):
		return errors.New("helppay sponsor job queue is full or timed out")
	}

	// Wait for worker result
	select {
	case err := <-resultCh:
		return err
	case <-time.After(m.maxTimeout):
		return fmt.Errorf("helppay topUp timed out waiting for transaction receipt (%v)", m.maxTimeout)
	case <-m.ctx.Done():
		return errors.New("helppay manager stopped while waiting for topUp")
	}
}

// workerLoop runs the task consumer for a single sponsor wallet.
// When idle, it immediately takes the next available request from sponsorJobQueue.
func (m *Manager) workerLoop(ctx context.Context, w *WalletWorker) {
	logger.Info("🚀 [HelpPay] Worker started for sponsor wallet %s", w.address.Hex())
	defer logger.Info("🛑 [HelpPay] Worker stopped for sponsor wallet %s", w.address.Hex())

	for {
		select {
		case <-ctx.Done():
			return
		case req := <-m.sponsorJobQueue:
			m.processTopUpRequest(w, req)
		}
	}
}

func isNonceError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, transaction.InvalidNonce) ||
		errors.Is(err, transaction.ErrNonceTooLow) ||
		errors.Is(err, transaction.ErrNonceTooHigh) ||
		errors.Is(err, transaction.NonceConflictError) ||
		errors.Is(err, transaction.ErrAlreadyKnown) ||
		errors.Is(err, transaction.ErrReplacementUnderpriced) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "nonce") ||
		strings.Contains(msg, "already known") ||
		strings.Contains(msg, "already exists") ||
		strings.Contains(msg, "replacement underpriced")
}

func (m *Manager) getSponsorWalletNonce(addr common.Address) (uint64, error) {
	if m.chainState == nil || m.chainState.GetAccountStateDB() == nil {
		return 0, errors.New("chain state account database not available")
	}
	as, err := m.chainState.GetAccountStateDB().AccountState(addr)
	if err != nil || as == nil {
		return 0, fmt.Errorf("sponsor wallet %s state not found: %v", addr.Hex(), err)
	}
	nonce := as.Nonce()

	// Query next available pending nonce from mempool to avoid collisions with in-flight transactions
	if png, ok := m.txPoolAdder.(PendingNonceGetter); ok {
		nonce = png.GetPendingNonce(addr, nonce)
	}
	return nonce, nil
}

// processTopUpRequest processes a top-up request on worker w.
// If worker w has insufficient funds or encounters persistent nonce failures, it attempts to re-queue the job to another idle sponsor wallet.
// If all wallets in the pool lack funds, it returns a detailed diagnostic error listing all wallets and their balances.
func (m *Manager) processTopUpRequest(w *WalletWorker, req *SponsorGasTask) {
	err := m.executeTransfer(w, req)
	if err != nil {
		// If balance is low or nonce error persisted on this wallet, and there are other sponsor wallets to try, re-queue
		if (strings.Contains(err.Error(), "insufficient balance") || isNonceError(err) || strings.Contains(err.Error(), "timed out")) && req.attempts < m.ActiveWalletCount()-1 {
			req.attempts++
			logger.Warn("⚠️ [HelpPay] Sponsor wallet %s failed with %v, re-queueing to another wallet in pool (attempt %d/%d)",
				w.address.Hex(), err, req.attempts, m.ActiveWalletCount())
			select {
			case m.sponsorJobQueue <- req:
				return // Re-queued successfully, do not return to req.ResultCh yet!
			default:
			}
		}

		var finalErr error
		if strings.Contains(err.Error(), "insufficient balance") {
			m.mu.RLock()
			walletList := make([]string, 0, len(m.wallets))
			for addr := range m.wallets {
				bal := m.GetBalance(addr)
				walletList = append(walletList, fmt.Sprintf("%s (balance: %s wei)", addr.Hex(), bal.String()))
			}
			m.mu.RUnlock()
			finalErr = fmt.Errorf("all sponsor wallets have insufficient balance: [%s], needed %s wei for top-up",
				strings.Join(walletList, ", "), req.Amount.String())
		} else {
			finalErr = err
		}

		req.ResultCh <- finalErr
		return
	}

	req.ResultCh <- nil
}

// executeTransfer constructs, signs, submits and awaits receipt for a top-up transaction.
// When a nonce collision or mismatch is detected, it reloads the freshest nonce from state & mempool and retries.
func (m *Manager) executeTransfer(w *WalletWorker, req *SponsorGasTask) error {
	toAddr := req.Recipient
	amount := req.Amount

	const maxNonceRetries = 3

	for attempt := 0; attempt < maxNonceRetries; attempt++ {
		// 1. Verify sponsor wallet balance
		if m.chainState == nil || m.chainState.GetAccountStateDB() == nil {
			return errors.New("chain state account database not available")
		}
		as, err := m.chainState.GetAccountStateDB().AccountState(w.address)
		if err != nil || as == nil {
			return fmt.Errorf("sponsor wallet %s state not found: %v", w.address.Hex(), err)
		}
		if as.Balance().Cmp(amount) < 0 {
			return fmt.Errorf("sponsor wallet %s has insufficient balance (has %s wei, needed %s wei)",
				w.address.Hex(), as.Balance().String(), amount.String())
		}

		// 2. Load freshest nonce (chain state + pending mempool txs)
		nonce, err := m.getSponsorWalletNonce(w.address)
		if err != nil {
			return err
		}

		// 3. Build and sign Ethereum transaction with sequential nonce
		gasLimit := uint64(21000)
		gasPrice := big.NewInt(1000000000) // 1 Gwei
		signer := types.LatestSignerForChainID(m.chainID)
		ethTx := types.NewTransaction(nonce, toAddr, amount, gasLimit, gasPrice, nil)
		signedTx, err := types.SignTx(ethTx, signer, w.privKey)
		if err != nil {
			return fmt.Errorf("failed to sign transfer tx from %s: %w", w.address.Hex(), err)
		}

		metaTxIface, err := transaction.NewTransactionFromEth(signedTx)
		if err != nil {
			return fmt.Errorf("failed to convert ethTx to metaTx: %w", err)
		}
		metaTx, ok := metaTxIface.(*transaction.Transaction)
		if !ok {
			return fmt.Errorf("unexpected metaTx type: %T", metaTxIface)
		}

		ethTxHash := signedTx.Hash()
		metaTxHash := metaTx.Hash()

		// 4. Submit to pool with automatic nonce-reload retry
		if m.txPoolAdder != nil {
			_, err = m.txPoolAdder.AddTransactionToPool(metaTx)
			if err != nil {
				if isNonceError(err) && attempt < maxNonceRetries-1 {
					logger.Warn("⚠️ [HelpPay] Nonce error for sponsor wallet %s (used nonce=%d, err=%v). Reloading nonce and retrying (attempt %d/%d)...",
						w.address.Hex(), nonce, err, attempt+1, maxNonceRetries)
					time.Sleep(50 * time.Millisecond)
					continue
				}
				return fmt.Errorf("failed to add transfer tx to pool: %w", err)
			}
		}

		if bc := blockchain.GetBlockChainInstance(); bc != nil {
			rawEth, _ := signedTx.MarshalBinary()
			bc.AddTxToCache(ethTxHash, rawEth)
			_ = bc.SetEthHashMapblsHash(ethTxHash, metaTxHash)
		}

		logger.Info("💸 [HelpPay] Top-up tx submitted: from=%s to=%s amount=%s ethHash=%s nonce=%d",
			w.address.Hex(), toAddr.Hex(), amount.String(), ethTxHash.Hex(), nonce)

		// 5. Await block commitment
		deadline := time.Now().Add(m.maxTimeout)
		searchHash := ethTxHash
		confirmed := false

		for time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
			bc := blockchain.GetBlockChainInstance()
			if bc == nil {
				continue
			}
			blsHash, isEth := bc.GetEthHashMapblsHash(ethTxHash)
			if isEth {
				searchHash = blsHash
			}
			blockNum, ok := bc.GetBlockNumberByTxHashFast(searchHash)
			if ok && blockNum <= storage.GetLastBlockNumber() {
				w.totalSent.Add(1)
				w.lastUsed.Store(time.Now().Unix())
				logger.Info("✅ [HelpPay] Top-up tx confirmed in block %d: from=%s to=%s amount=%s (hash=%s)",
					blockNum, w.address.Hex(), toAddr.Hex(), amount.String(), ethTxHash.Hex())
				confirmed = true
				break
			}
		}

		if confirmed {
			return nil
		}

		// If timed out waiting for confirmation, reload state and retry with fresh nonce if attempts remain
		if attempt < maxNonceRetries-1 {
			logger.Warn("⚠️ [HelpPay] Top-up tx %s confirmation timed out (nonce=%d). Reloading nonce and retrying...", ethTxHash.Hex(), nonce)
			time.Sleep(50 * time.Millisecond)
			continue
		}

		return fmt.Errorf("timed out waiting for top-up tx %s block confirmation", ethTxHash.Hex())
	}

	return fmt.Errorf("failed to execute transfer from %s after %d attempts", w.address.Hex(), maxNonceRetries)
}


// AddWallet adds a new sponsor wallet using an ECDSA private key to this local node.
// Wallets are managed locally per-node for Nonce isolation and security (never broadcasted across peers).
func (m *Manager) AddWallet(privKeyHex string, broadcast bool) (common.Address, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	addr, err := m.addWalletInternal(privKeyHex)
	if err != nil {
		return common.Address{}, err
	}

	// Auto-enable if it was disabled and a wallet is added
	m.enabled = true
	if err := m.saveToDiskLocked(); err != nil {
		logger.Warn("⚠️ [HelpPay] Failed to save state to disk: %v", err)
	}

	logger.Info("🔑 [HelpPay] Added local sponsor wallet %s", addr.Hex())
	return addr, nil
}

func (m *Manager) addWalletInternal(privKeyHex string) (common.Address, error) {
	cleanKey := strings.TrimSpace(privKeyHex)
	cleanKey = strings.TrimPrefix(cleanKey, "0x")
	cleanKey = strings.TrimPrefix(cleanKey, "0X")

	privKey, err := crypto.HexToECDSA(cleanKey)
	if err != nil {
		return common.Address{}, fmt.Errorf("invalid ECDSA private key: %w", err)
	}

	addr := crypto.PubkeyToAddress(privKey.PublicKey)
	if existing, exists := m.wallets[addr]; exists {
		if existing.cancel == nil && m.started {
			wCtx, wCancel := context.WithCancel(m.ctx)
			existing.cancel = wCancel
			go m.workerLoop(wCtx, existing)
		}
		return addr, nil
	}

	worker := &WalletWorker{
		address:    addr,
		privKey:    privKey,
		privKeyHex: cleanKey,
	}

	m.wallets[addr] = worker
	if m.started {
		wCtx, wCancel := context.WithCancel(m.ctx)
		worker.cancel = wCancel
		go m.workerLoop(wCtx, worker)
	}

	logger.Info("🔑 [HelpPay] Added sponsor wallet %s", addr.Hex())
	return addr, nil
}

// RemoveWallet removes a sponsor wallet by address from this local node.
// Wallets are managed locally per-node: removal is strictly local and never broadcasted across peers.
func (m *Manager) RemoveWallet(addr common.Address, broadcast bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	worker, exists := m.wallets[addr]
	if !exists {
		return fmt.Errorf("wallet %s not found", addr.Hex())
	}

	if worker.cancel != nil {
		worker.cancel()
		worker.cancel = nil
	}
	delete(m.wallets, addr)
	if err := m.saveToDiskLocked(); err != nil {
		logger.Warn("⚠️ [HelpPay] Failed to save state to disk: %v", err)
	}

	logger.Info("🗑️ [HelpPay] Removed local sponsor wallet %s", addr.Hex())
	return nil
}

// GetWallets returns status and statistics for all registered sponsor wallets.
func (m *Manager) GetWallets() []WalletStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]WalletStatus, 0, len(m.wallets))
	for addr, w := range m.wallets {
		bal := m.GetBalance(addr)
		lastUsedStr := ""
		if ts := w.lastUsed.Load(); ts > 0 {
			lastUsedStr = time.Unix(ts, 0).Format(time.RFC3339)
		}

		result = append(result, WalletStatus{
			Address:   addr.Hex(),
			Balance:   bal.String(),
			IsActive:  w.cancel != nil,
			TotalSent: w.totalSent.Load(),
			LastUsed:  lastUsedStr,
		})
	}
	return result
}

// SetConfig updates help-pay configuration and persists it.
func (m *Manager) SetConfig(enabled bool, minBalance string, extraAmount string, broadcast bool) error {
	m.mu.Lock()

	m.enabled = enabled
	if minBalance != "" {
		if b, ok := new(big.Int).SetString(minBalance, 10); ok {
			m.minBalance = b
		}
	}
	if extraAmount != "" {
		if b, ok := new(big.Int).SetString(extraAmount, 10); ok {
			m.extraAmount = b
		}
	}

	if err := m.saveToDiskLocked(); err != nil {
		logger.Warn("⚠️ [HelpPay] Failed to save state to disk: %v", err)
	}

	var broadcaster func(payload []byte) error
	if broadcast {
		broadcaster = m.syncBroadcaster
	}
	m.mu.Unlock()

	logger.Info("⚙️ [HelpPay] Configuration updated: enabled=%v, minBalance=%s, extraAmount=%s",
		enabled, m.minBalance.String(), m.extraAmount.String())

	if broadcaster != nil {
		payload, _ := json.Marshal(SyncPayload{
			Action:      ActionSetConfig,
			Enabled:     &enabled,
			MinBalance:  minBalance,
			ExtraAmount: extraAmount,
		})
		go func() {
			_ = broadcaster(payload)
		}()
	}

	return nil
}

// GetConfig returns the current configuration.
func (m *Manager) GetConfig() config.HelpPayConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	wallets := make([]config.HelpPayWalletConfig, 0, len(m.wallets))
	for addr := range m.wallets {
		wallets = append(wallets, config.HelpPayWalletConfig{
			Address: addr.Hex(),
		})
	}

	admins := make([]string, 0, len(m.adminAddresses))
	for a := range m.adminAddresses {
		admins = append(admins, a.Hex())
	}

	return config.HelpPayConfig{
		Enabled:        m.enabled,
		MinBalance:     m.minBalance.String(),
		ExtraAmount:    m.extraAmount.String(),
		AdminAddresses: admins,
		Wallets:        wallets,
		ConfigFile:     m.configPath,
	}
}

// ApplySync processes a sync payload received from a peer in the Raft cluster.
func (m *Manager) ApplySync(payload []byte) error {
	var sp SyncPayload
	if err := json.Unmarshal(payload, &sp); err != nil {
		return fmt.Errorf("failed to unmarshal helppay sync payload: %w", err)
	}

	logger.Info("🔄 [HelpPay] Received cluster sync action: %s from %s", sp.Action, sp.SenderNodeID)

	switch sp.Action {
	case ActionAddWallet, ActionRemoveWallet:
		// Security & Nonce isolation: Wallets are managed locally per-node from local configuration.
		// Wallet add/remove actions are never synchronized across the Raft cluster.
		logger.Debug("ℹ️ [HelpPay] %s ignored over Raft sync: wallets are managed locally per-node", sp.Action)
		return nil
	case ActionSetConfig:
		m.mu.RLock()
		en := m.enabled
		m.mu.RUnlock()
		if sp.Enabled != nil {
			en = *sp.Enabled
		}
		return m.SetConfig(en, sp.MinBalance, sp.ExtraAmount, false)
	default:
		return fmt.Errorf("unknown helppay sync action: %s", sp.Action)
	}
}

// GetState returns cluster state serialized as JSON for synchronization queries without private keys.
func (m *Manager) GetState() ([]byte, error) {
	wallets := m.GetWallets()
	m.mu.RLock()
	defer m.mu.RUnlock()

	maxDailyStr := defaultMaxDailyPerIP
	if m.maxDailyPerIP != nil {
		maxDailyStr = m.maxDailyPerIP.String()
	}

	resp := ClusterStateResponse{
		Enabled:       m.enabled,
		MinBalance:    m.minBalance.String(),
		ExtraAmount:   m.extraAmount.String(),
		MaxDailyPerIP: maxDailyStr,
		Wallets:       wallets,
	}
	return json.Marshal(resp)
}

func (m *Manager) saveToDiskLocked() error {
	if m.configPath == "" {
		return nil
	}

	items := make([]PersistedWalletItem, 0, len(m.wallets))
	for addr, w := range m.wallets {
		items = append(items, PersistedWalletItem{
			Address:    addr.Hex(),
			PrivateKey: w.privKeyHex,
			TotalSent:  w.totalSent.Load(),
		})
	}

	cfg := PersistedConfig{
		Enabled:     m.enabled,
		MinBalance:  m.minBalance.String(),
		ExtraAmount: m.extraAmount.String(),
		Wallets:     items,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(m.configPath)
	_ = os.MkdirAll(dir, 0755)

	// Ghi file nguyên tử (Atomic Write) qua file tạm để tránh corrupt khi mất điện/crash
	tmpFile := filepath.Join(dir, fmt.Sprintf(".%s.tmp.%d", filepath.Base(m.configPath), time.Now().UnixNano()))
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, m.configPath); err != nil {
		_ = os.Remove(tmpFile)
		return err
	}
	return nil
}

// CatchUpFromCluster fetches state from cluster peers using stateFetcher and merges it into local state.
func (m *Manager) CatchUpFromCluster() (int, error) {
	m.mu.RLock()
	fetcher := m.stateFetcher
	m.mu.RUnlock()

	if fetcher == nil {
		return 0, errors.New("no state fetcher configured")
	}

	data, err := fetcher()
	if err != nil {
		return 0, err
	}

	return m.ApplyClusterState(data)
}

// ApplyClusterState merges cluster configuration state into local manager state and persists it atomically.
// Wallets remain managed locally per-node for Nonce isolation and security (no private keys over the wire).
// Enforces safe ceilings to prevent malicious or misconfigured peers from draining sponsor wallets.
func (m *Manager) ApplyClusterState(data []byte) (int, error) {
	var resp ClusterStateResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		var cfg PersistedConfig
		if err2 := json.Unmarshal(data, &cfg); err2 != nil {
			return 0, fmt.Errorf("failed to unmarshal helppay cluster state: %w", err)
		}
		resp.Enabled = cfg.Enabled
		resp.MinBalance = cfg.MinBalance
		resp.ExtraAmount = cfg.ExtraAmount
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	updatedCount := 0
	ceilingExtra, _ := new(big.Int).SetString("5000000000000000000", 10)  // 5 ETH ceiling
	ceilingMin, _ := new(big.Int).SetString("10000000000000000000", 10)   // 10 ETH ceiling
	ceilingDaily, _ := new(big.Int).SetString("100000000000000000000", 10) // 100 ETH ceiling

	if resp.MinBalance != "" {
		if b, ok := new(big.Int).SetString(resp.MinBalance, 10); ok && b.Sign() >= 0 {
			if ceilingMin != nil && b.Cmp(ceilingMin) > 0 {
				logger.Warn("⚠️ [HelpPay] Cluster min_balance %s exceeds safe ceiling %s, capped", b.String(), ceilingMin.String())
				b = ceilingMin
			}
			m.minBalance = b
			updatedCount++
		}
	}
	if resp.ExtraAmount != "" {
		if b, ok := new(big.Int).SetString(resp.ExtraAmount, 10); ok && b.Sign() >= 0 {
			if ceilingExtra != nil && b.Cmp(ceilingExtra) > 0 {
				logger.Warn("⚠️ [HelpPay] Cluster extra_amount %s exceeds safe ceiling %s, capped", b.String(), ceilingExtra.String())
				b = ceilingExtra
			}
			m.extraAmount = b
			updatedCount++
		}
	}
	if resp.MaxDailyPerIP != "" {
		if b, ok := new(big.Int).SetString(resp.MaxDailyPerIP, 10); ok && b.Sign() >= 0 {
			if ceilingDaily != nil && b.Cmp(ceilingDaily) > 0 {
				logger.Warn("⚠️ [HelpPay] Cluster max_daily_per_IP %s exceeds safe ceiling %s, capped", b.String(), ceilingDaily.String())
				b = ceilingDaily
			}
			m.maxDailyPerIP = b
			updatedCount++
		}
	}
	if m.enabled != resp.Enabled {
		m.enabled = resp.Enabled
		updatedCount++
	}

	if err := m.saveToDiskLocked(); err != nil {
		logger.Warn("⚠️ [HelpPay] Failed to save state to disk: %v", err)
	}

	logger.Info("✅ [HelpPay] Cluster configuration applied (enabled: %t, min_balance: %s, extra_amount: %s, active local wallets: %d)",
		m.enabled, m.minBalance.String(), m.extraAmount.String(), len(m.wallets))

	return updatedCount, nil
}

func (m *Manager) loadFromDiskLocked() error {
	if m.configPath == "" {
		return nil
	}

	data, err := os.ReadFile(m.configPath)
	if err != nil {
		return err
	}

	var cfg PersistedConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}

	if cfg.MinBalance != "" {
		if b, ok := new(big.Int).SetString(cfg.MinBalance, 10); ok {
			m.minBalance = b
		}
	}
	if cfg.ExtraAmount != "" {
		if b, ok := new(big.Int).SetString(cfg.ExtraAmount, 10); ok {
			m.extraAmount = b
		}
	}
	m.enabled = cfg.Enabled

	for _, item := range cfg.Wallets {
		if item.PrivateKey != "" {
			addr, err := m.addWalletInternal(item.PrivateKey)
			if err == nil {
				if w, ok := m.wallets[addr]; ok {
					w.totalSent.Store(item.TotalSent)
				}
			}
		}
	}
	return nil
}
