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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.True(t, mgr2.IsSponsorWallet(addr), "wallet should have been synced to mgr2")

	// Set config on mgr1 with broadcast=true
	err = mgr1.SetConfig(true, "30000000000000000", "70000000000000000", true)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, "30000000000000000", mgr2.MinBalance().String())
	assert.Equal(t, "70000000000000000", mgr2.ExtraAmount().String())

	// Remove wallet on mgr1 with broadcast=true
	err = mgr1.RemoveWallet(addr, true)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)
	assert.False(t, mgr1.IsSponsorWallet(addr))
	assert.False(t, mgr2.IsSponsorWallet(addr), "wallet removal should have been synced to mgr2")
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

	// Node 2 starts with 0 wallets, but has stateFetcher wired to mgr1.GetState()
	mgr2 := NewManager(config.HelpPayConfig{
		Enabled:    false,
		ConfigFile: filepath.Join(tmpDir2, "node2.json"),
	}, nil, nil, big.NewInt(991), tmpDir2)

	mgr2.SetStateFetcher(func() ([]byte, error) {
		return mgr1.GetState()
	})

	mgr2.Start()
	defer mgr2.Stop()

	// CatchUpFromCluster triggers catch-up
	added, err := mgr2.CatchUpFromCluster()
	require.NoError(t, err)
	assert.Equal(t, 2, added)

	// Verify mgr2 caught up completely
	assert.Equal(t, 2, mgr2.ActiveWalletCount())
	assert.True(t, mgr2.IsSponsorWallet(addr1))
	assert.True(t, mgr2.IsSponsorWallet(addr2))
	assert.True(t, mgr2.IsEnabled())
	assert.Equal(t, "25000000000000000", mgr2.MinBalance().String())
	assert.Equal(t, "65000000000000000", mgr2.ExtraAmount().String())
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

