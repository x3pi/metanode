package helppay

import (
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sync"
)

func generateTestKey(t *testing.T) (*ecdsa.PrivateKey, string, common.Address) {
	key, err := crypto.GenerateKey()
	require.NoError(t, err)
	keyHex := hex.EncodeToString(crypto.FromECDSA(key))
	addr := crypto.PubkeyToAddress(key.PublicKey)
	return key, keyHex, addr
}

func TestManager_AddRemoveWallet(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.HelpPayConfig{
		Enabled:     true,
		MinBalance:  "10000000000000000",
		ExtraAmount: "100000000000000000",
		ConfigFile:  filepath.Join(tmpDir, "helppay_test.json"),
	}

	mgr := NewManager(cfg, nil, nil, big.NewInt(991), tmpDir)
	mgr.Start()
	defer mgr.Stop()

	assert.False(t, mgr.IsEnabled(), "should be false with 0 wallets")
	assert.Equal(t, 0, mgr.ActiveWalletCount())

	_, keyHex1, addr1 := generateTestKey(t)
	addedAddr1, err := mgr.AddWallet(keyHex1, false)
	require.NoError(t, err)
	assert.Equal(t, addr1, addedAddr1)
	assert.True(t, mgr.IsEnabled(), "should be true with active wallet")
	assert.Equal(t, 1, mgr.ActiveWalletCount())
	assert.True(t, mgr.IsSponsorWallet(addr1))

	_, keyHex2, addr2 := generateTestKey(t)
	addedAddr2, err := mgr.AddWallet(keyHex2, false)
	require.NoError(t, err)
	assert.Equal(t, addr2, addedAddr2)
	assert.Equal(t, 2, mgr.ActiveWalletCount())

	wallets := mgr.GetWallets()
	assert.Equal(t, 2, len(wallets))

	err = mgr.RemoveWallet(addr1, false)
	require.NoError(t, err)
	assert.Equal(t, 1, mgr.ActiveWalletCount())
	assert.False(t, mgr.IsSponsorWallet(addr1))
	assert.True(t, mgr.IsSponsorWallet(addr2))
}

func TestManager_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	configFile := filepath.Join(tmpDir, "helppay_persist.json")
	cfg := config.HelpPayConfig{
		Enabled:     true,
		MinBalance:  "20000000000000000",
		ExtraAmount: "80000000000000000",
		ConfigFile:  configFile,
	}

	mgr1 := NewManager(cfg, nil, nil, big.NewInt(991), tmpDir)
	mgr1.Start()

	_, keyHex, addr := generateTestKey(t)
	_, err := mgr1.AddWallet(keyHex, false)
	require.NoError(t, err)

	mgr1.Stop()

	// New manager loading the same config file
	mgr2 := NewManager(config.HelpPayConfig{ConfigFile: configFile}, nil, nil, big.NewInt(991), tmpDir)
	mgr2.Start()
	defer mgr2.Stop()

	assert.Equal(t, 1, mgr2.ActiveWalletCount())
	assert.True(t, mgr2.IsSponsorWallet(addr))
	assert.Equal(t, "20000000000000000", mgr2.MinBalance().String())
	assert.Equal(t, "80000000000000000", mgr2.ExtraAmount().String())
}

func TestManager_ClusterSync(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	mgr1 := NewManager(config.HelpPayConfig{Enabled: true, ConfigFile: filepath.Join(tmpDir1, "node1.json")}, nil, nil, big.NewInt(991), tmpDir1)
	mgr2 := NewManager(config.HelpPayConfig{Enabled: true, ConfigFile: filepath.Join(tmpDir2, "node2.json")}, nil, nil, big.NewInt(991), tmpDir2)

	// Wire mgr1 broadcast to mgr2.ApplySync
	mgr1.SetSyncBroadcaster(func(payload []byte) error {
		return mgr2.ApplySync(payload)
	})

	mgr1.Start()
	mgr2.Start()
	defer mgr1.Stop()
	defer mgr2.Stop()

	// Add wallet on mgr1 with broadcast=true
	_, keyHex, addr := generateTestKey(t)
	_, err := mgr1.AddWallet(keyHex, true)
	require.NoError(t, err)

	// Allow goroutine time to run sync
	time.Sleep(100 * time.Millisecond)

	assert.True(t, mgr1.IsSponsorWallet(addr))
	assert.False(t, mgr2.IsSponsorWallet(addr), "wallets are isolated per node to avoid Nonce collisions and key leakage")

	// Set config on mgr1 with broadcast=true
	err = mgr1.SetConfig(true, "30000000000000000", "70000000000000000", true)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, "30000000000000000", mgr2.MinBalance().String())
	assert.Equal(t, "70000000000000000", mgr2.ExtraAmount().String())

	// Remove wallet on mgr1
	err = mgr1.RemoveWallet(addr, false)
	require.NoError(t, err)

	assert.False(t, mgr1.IsSponsorWallet(addr))
}

func TestManager_AdminAndToggle(t *testing.T) {
	tmpDir := t.TempDir()
	adminKey1, _, adminAddr1 := generateTestKey(t)
	_, _, adminAddr2 := generateTestKey(t)
	nonAdminKey, _, nonAdminAddr := generateTestKey(t)

	cfg := config.HelpPayConfig{
		Enabled:        false,
		MinBalance:     "10000000000000000",
		ExtraAmount:    "100000000000000000",
		AdminAddresses: []string{adminAddr1.Hex()},
		AdminAddress:   adminAddr2.Hex(),
		ConfigFile:     filepath.Join(tmpDir, "helppay_admin_test.json"),
	}

	mgr := NewManager(cfg, nil, nil, big.NewInt(991), tmpDir)
	mgr.Start()
	defer mgr.Stop()

	// 1. Verify admin recognition
	assert.True(t, mgr.HasAdminsConfigured())
	assert.True(t, mgr.IsAdmin(adminAddr1))
	assert.True(t, mgr.IsAdmin(adminAddr2))
	assert.False(t, mgr.IsAdmin(nonAdminAddr))
	assert.Equal(t, 2, len(mgr.GetAdmins()))

	// 2. Test signature verification
	ts := time.Now().Unix()
	msg := fmt.Sprintf("helppay:toggle:true:%d", ts)
	textHash := accounts.TextHash([]byte(msg))
	sig1, err := crypto.Sign(textHash, adminKey1)
	require.NoError(t, err)
	if sig1[64] < 27 {
		sig1[64] += 27
	}
	sigHex1 := hexutil.Encode(sig1)

	// Valid admin signature should succeed
	err = mgr.VerifyAdminSignature(msg, sigHex1, adminAddr1)
	assert.NoError(t, err)

	// Non-admin signature should fail
	sigNonAdmin, err := crypto.Sign(textHash, nonAdminKey)
	require.NoError(t, err)
	if sigNonAdmin[64] < 27 {
		sigNonAdmin[64] += 27
	}
	err = mgr.VerifyAdminSignature(msg, hexutil.Encode(sigNonAdmin), nonAdminAddr)
	assert.Error(t, err)

	// 3. Test SetEnabled toggle
	assert.False(t, mgr.IsEnabled())
	// Add wallet first so IsEnabled can become true when enabled=true
	_, keyHex, _ := generateTestKey(t)
	_, err = mgr.AddWallet(keyHex, false)
	require.NoError(t, err)

	err = mgr.SetEnabled(false, false)
	require.NoError(t, err)
	assert.False(t, mgr.IsEnabled(), "should be false after toggle off")

	err = mgr.SetEnabled(true, false)
	require.NoError(t, err)
	assert.True(t, mgr.IsEnabled(), "should be true after toggle on")
}

func TestManager_StartupCatchUpSync(t *testing.T) {
	tmpDir1 := t.TempDir()
	tmpDir2 := t.TempDir()

	mgr1 := NewManager(config.HelpPayConfig{
		Enabled:     true,
		MinBalance:  "25000000000000000",
		ExtraAmount: "65000000000000000",
		ConfigFile:  filepath.Join(tmpDir1, "node1.json"),
	}, nil, nil, big.NewInt(991), tmpDir1)
	mgr1.Start()
	defer mgr1.Stop()

	// Add 2 sponsor wallets to mgr1
	_, keyHex1, addr1 := generateTestKey(t)
	_, err := mgr1.AddWallet(keyHex1, false)
	require.NoError(t, err)

	_, keyHex2, addr2 := generateTestKey(t)
	_, err = mgr1.AddWallet(keyHex2, false)
	require.NoError(t, err)

	assert.Equal(t, 2, mgr1.ActiveWalletCount())

	// Node 2 starts with its own isolated wallet, and stateFetcher wired to mgr1.GetState()
	_, keyHex3, addr3 := generateTestKey(t)
	mgr2 := NewManager(config.HelpPayConfig{
		Enabled:    false,
		ConfigFile: filepath.Join(tmpDir2, "node2.json"),
	}, nil, nil, big.NewInt(991), tmpDir2)
	_, err = mgr2.AddWallet(keyHex3, false)
	require.NoError(t, err)

	mgr2.SetStateFetcher(func() ([]byte, error) {
		return mgr1.GetState()
	})

	mgr2.Start()
	defer mgr2.Stop()

	// CatchUpFromCluster triggers catch-up of cluster configs
	_, err = mgr2.CatchUpFromCluster()
	require.NoError(t, err)

	// Verify mgr2 caught up configuration (enabled, min_balance, extra_amount)
	// while preserving its own isolated local wallet (addr3) and never receiving mgr1's private keys
	assert.Equal(t, 1, mgr2.ActiveWalletCount())
	assert.True(t, mgr2.IsSponsorWallet(addr3))
	assert.False(t, mgr2.IsSponsorWallet(addr1), "mgr2 should not hold mgr1 wallets")
	assert.False(t, mgr2.IsSponsorWallet(addr2), "mgr2 should not hold mgr1 wallets")
	assert.True(t, mgr2.IsEnabled())
	assert.Equal(t, "25000000000000000", mgr2.MinBalance().String())
	assert.Equal(t, "65000000000000000", mgr2.ExtraAmount().String())
}

func TestManager_IPRateLimiting(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.HelpPayConfig{
		Enabled:     true,
		MinBalance:  "10000000000000000",
		ExtraAmount: "100000000000000000", // 0.1 ETH
		ConfigFile:  filepath.Join(tmpDir, "helppay_rate_limit.json"),
	}

	mgr := NewManager(cfg, nil, nil, big.NewInt(991), tmpDir)
	mgr.Start()
	defer mgr.Stop()

	// Add 1 sponsor wallet so IsEnabled is true
	_, keyHex, _ := generateTestKey(t)
	_, err := mgr.AddWallet(keyHex, false)
	require.NoError(t, err)

	clientIP := "203.0.113.42"
	now := time.Now()
	today := uint64(now.Unix() / 86400)

	// Set daily limit to 0.2 ETH (2 * 10^17 wei)
	mgr.SetMaxDailyPerIP(big.NewInt(200000000000000000))

	// Pre-fill ipUsage to 0.2 ETH (already at max limit)
	mgr.rateMu.Lock()
	mgr.ipUsage[clientIP] = &IPRateLimitRecord{
		DayNumber:  today,
		TotalTopUp: big.NewInt(200000000000000000),
	}
	mgr.rateMu.Unlock()

	rec := common.HexToAddress("0x1111111111111111111111111111111111111111")
	// Since clientIP has reached limit, CheckAndTopUpWithClient should reject with rate limit error
	err = mgr.CheckAndTopUpWithClient(rec, clientIP)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "daily rate limit exceeded for client IP")

	// Test cooldown: recipient with recent top-up within 30s
	mgr.rateMu.Lock()
	mgr.recipientUsage[rec] = &RecipientRateLimitRecord{
		DayNumber:     today,
		LastTopUpUnix: now.Unix() - 5, // 5 seconds ago (< 30s cooldown)
	}
	mgr.rateMu.Unlock()

	otherIP := "203.0.113.99"
	err = mgr.CheckAndTopUpWithClient(rec, otherIP)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is under cooldown")
}

func TestManager_CheckAndTopUp_ZeroFrictionOnboarding(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.HelpPayConfig{
		Enabled:     true,
		MinBalance:  "10000000000000000",
		ExtraAmount: "100000000000000000",
		ConfigFile:  filepath.Join(tmpDir, "helppay_test.json"),
	}

	mgr := NewManager(cfg, nil, nil, big.NewInt(991), tmpDir)
	mgr.Start()
	defer mgr.Stop()

	// 0 active wallets -> IsEnabled() is false -> CheckAndTopUp should return nil
	recipient := common.HexToAddress("0x1111111111111111111111111111111111111111")
	err := mgr.CheckAndTopUp(recipient)
	assert.NoError(t, err)

	// Add a sponsor wallet
	_, keyHex, sponsorAddr := generateTestKey(t)
	_, err = mgr.AddWallet(keyHex, false)
	require.NoError(t, err)
	assert.True(t, mgr.IsEnabled())

	// Top-up on sponsor wallet itself is ignored
	err = mgr.CheckAndTopUp(sponsorAddr)
	assert.NoError(t, err)
}

func TestManager_IsNonceError(t *testing.T) {
	assert.False(t, isNonceError(nil))
	assert.True(t, isNonceError(transaction.InvalidNonce))
	assert.True(t, isNonceError(transaction.ErrNonceTooLow))
	assert.True(t, isNonceError(transaction.ErrNonceTooHigh))
	assert.True(t, isNonceError(transaction.NonceConflictError))
	assert.True(t, isNonceError(transaction.ErrAlreadyKnown))
	assert.True(t, isNonceError(transaction.ErrReplacementUnderpriced))
	assert.True(t, isNonceError(fmt.Errorf("tx rejected: invalid nonce gap")))
	assert.True(t, isNonceError(fmt.Errorf("already exists in pool")))
	assert.False(t, isNonceError(fmt.Errorf("insufficient balance")))
}

type mockTxPoolAdderWithNonce struct {
	attempts int
	mu       sync.Mutex
	nonces   []uint64
}

func (m *mockTxPoolAdderWithNonce) AddTransactionToPool(tx mt_types.Transaction) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attempts++
	m.nonces = append(m.nonces, tx.GetNonce())
	if m.attempts == 1 {
		return int64(transaction.InvalidNonce.Code), transaction.InvalidNonce
	}
	return 0, nil
}

func (m *mockTxPoolAdderWithNonce) GetPendingNonce(addr common.Address, stateNonce uint64) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.attempts >= 1 {
		return stateNonce + 1
	}
	return stateNonce
}

func TestManager_GetPendingNonceInterface(t *testing.T) {
	mockPool := &mockTxPoolAdderWithNonce{}
	var adder TxPoolAdder = mockPool
	png, ok := adder.(PendingNonceGetter)
	require.True(t, ok)

	addr := common.HexToAddress("0x1111111111111111111111111111111111111111")
	assert.Equal(t, uint64(0), png.GetPendingNonce(addr, 0))
}

func TestManager_PreCheckAndAcquire(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.HelpPayConfig{
		Enabled:     true,
		MinBalance:  "10000000000000000",  // 0.01 ETH
		ExtraAmount: "100000000000000000", // 0.1 ETH
		ConfigFile:  filepath.Join(tmpDir, "helppay_precheck.json"),
	}

	mgr := NewManager(cfg, nil, nil, big.NewInt(991), tmpDir)
	mgr.Start()
	defer mgr.Stop()

	// 1. Thêm 1 ví sponsor để maxConcurrent = 1 * 20 = 20
	_, keyHex, _ := generateTestKey(t)
	_, err := mgr.AddWallet(keyHex, false)
	require.NoError(t, err)
	require.Equal(t, 1, mgr.ActiveWalletCount())

	fundedAddr := common.HexToAddress("0x9999999999999999999999999999999999999999")
	unfundedAddr := common.HexToAddress("0x8888888888888888888888888888888888888888")

	// 2. Test gas < 21000
	_, needsTopUp, err := mgr.PreCheckAndAcquire(unfundedAddr, 0, 20999, nil, "1.2.3.4", "RPC")
	assert.True(t, needsTopUp)
	assert.ErrorContains(t, err, "gas limit 20999 invalid")

	// 3. Test gas > 30000000
	_, needsTopUp, err = mgr.PreCheckAndAcquire(unfundedAddr, 0, 30000001, nil, "1.2.3.4", "RPC")
	assert.True(t, needsTopUp)
	assert.ErrorContains(t, err, "gas limit 30000001 invalid")

	// 4. Test txAmount > 0 (Ví 0 đồng đòi chuyển 10 ETH -> HelpPay chỉ tài trợ gas chứ không tài trợ amount -> từ chối ngay)
	tenETH, _ := new(big.Int).SetString("10000000000000000000", 10)
	_, needsTopUp, err = mgr.PreCheckAndAcquire(unfundedAddr, 0, 50000, tenETH, "1.2.3.4", "RPC")
	assert.True(t, needsTopUp)
	assert.ErrorContains(t, err, "insufficient funds for transfer amount")

	// 5. Test nonce > expectedNonce + 5 (expectedNonce = 0 vì chainState = nil)
	_, needsTopUp, err = mgr.PreCheckAndAcquire(unfundedAddr, 6, 50000, nil, "1.2.3.4", "TCP")
	assert.True(t, needsTopUp)
	assert.ErrorContains(t, err, "nonce too high")

	// 6. Test dynamic concurrency limiter (1 ví = max 20 slots)
	var releases []func()
	for i := 0; i < 20; i++ {
		rel, needs, errAcq := mgr.PreCheckAndAcquire(unfundedAddr, 0, 50000, nil, "1.2.3.4", "RPC")
		require.NoError(t, errAcq)
		require.True(t, needs)
		require.NotNil(t, rel)
		releases = append(releases, rel)
	}

	// Yêu cầu thứ 21 phải bị từ chối do quá tải
	_, _, errOverflow := mgr.PreCheckAndAcquire(unfundedAddr, 0, 50000, nil, "1.2.3.4", "RPC")
	assert.ErrorContains(t, errOverflow, "helppay concurrency limit reached")

	// Giải phóng 1 slot
	releases[0]()
	releases = releases[1:]

	// Sau khi giải phóng, yêu cầu mới lại được chấp thuận
	relAfter, needsAfter, errAfter := mgr.PreCheckAndAcquire(unfundedAddr, 0, 50000, nil, "1.2.3.4", "TCP")
	require.NoError(t, errAfter)
	require.True(t, needsAfter)
	require.NotNil(t, relAfter)
	releases = append(releases, relAfter)

	// Dọn dẹp các release còn lại
	for _, r := range releases {
		r()
	}

	// Tắt tính năng -> needsTopUp phải là false
	mgr.SetEnabled(false, false)
	relDisabled, needsDisabled, errDisabled := mgr.PreCheckAndAcquire(fundedAddr, 0, 50000, nil, "1.2.3.4", "RPC")
	assert.NoError(t, errDisabled)
	assert.False(t, needsDisabled)
	assert.Nil(t, relDisabled)
}


