package parentchain

import (
	"crypto/ecdsa"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

func generateBLSKey() (cm.PrivateKey, cm.PublicKey) {
	keyPair := bls.GenerateKeyPair()
	return keyPair.PrivateKey(), keyPair.PublicKey()
}

func generateECDSAKey() (*ecdsa.PrivateKey, common.Address) {
	priv, _ := crypto.GenerateKey()
	addr := crypto.PubkeyToAddress(priv.PublicKey)
	return priv, addr
}

func TestParentChainState(t *testing.T) {
	store := NewMemoryStore()
	
	priv1, pub1 := generateBLSKey()
	priv2, pub2 := generateBLSKey()
	
	userPriv, userAddr := generateECDSAKey()
	_, userAddr2 := generateECDSAKey()
	
	hash1 := crypto.Keccak256Hash(pub1[:])
	hash2 := crypto.Keccak256Hash(pub2[:])
	
	// Test: RegisterAccount
	t.Run("RegisterAccount missing 1 of 2 signatures rejected", func(t *testing.T) {
		digest := ComputeRegisterAccountMessage(userAddr, pub1)
		hash := crypto.Keccak256Hash(digest)
		userSig, _ := crypto.Sign(hash.Bytes(), userPriv)
		clusterSig := bls.Sign(priv1, digest)
		
		// missing user sig (bad sig)
		err := RegisterAccount(store, userAddr, pub1, []byte("bad sig"), clusterSig)
		if err == nil {
			t.Error("Expected error for bad user sig")
		}
		
		// missing cluster sig (bad sig)
		err = RegisterAccount(store, userAddr, pub1, userSig, bls.Sign(priv2, digest))
		if err == nil {
			t.Error("Expected error for bad cluster sig")
		}
	})
	
	t.Run("RegisterAccount valid then twice rejected", func(t *testing.T) {
		digest := ComputeRegisterAccountMessage(userAddr, pub1)
		hash := crypto.Keccak256Hash(digest)
		userSig, _ := crypto.Sign(hash.Bytes(), userPriv)
		clusterSig := bls.Sign(priv1, digest)
		
		err := RegisterAccount(store, userAddr, pub1, userSig, clusterSig)
		if err != nil {
			t.Fatalf("Failed to register account: %v", err)
		}
		
		// twice rejected
		err = RegisterAccount(store, userAddr, pub1, userSig, clusterSig)
		if !errors.Is(err, ErrAccountAlreadyRegistered) {
			t.Errorf("Expected ErrAccountAlreadyRegistered, got %v", err)
		}
	})
	
	// Test: DepositToFloat auto-creates ChainRegistry+NodeFloatAccount entry
	t.Run("DepositToFloat to new FloatIdentityKey", func(t *testing.T) {
		depositAmount := big.NewInt(1000)
		msgID := common.HexToHash("0x123")
		err := DepositToFloat(store, pub1, 101, common.Address{}, common.Address{}, depositAmount, msgID, 1)
		if err != nil {
			t.Fatalf("Failed to deposit: %v", err)
		}
		
		// verify auto-create
		_, found, err := store.GetChainRegistry(hash1)
		if err != nil || !found {
			t.Errorf("Chain registry entry not created")
		}
		
		bal, _ := store.GetFloat(hash1)
		if bal.Cmp(depositAmount) != 0 {
			t.Errorf("Expected balance %s, got %s", depositAmount.String(), bal.String())
		}
		
		// Invariant check
		if err := CheckFloatSupplyInvariant(store); err != nil {
			t.Errorf("Invariant failed: %v", err)
		}
	})
	
	// Test: TransferFloat
	t.Run("TransferFloat to new FloatIdentityKey", func(t *testing.T) {
		store := NewMemoryStore()
		DepositToFloat(store, pub1, 101, common.Address{}, common.Address{}, big.NewInt(1000), common.HexToHash("0x123"), 1)
		
		nonce := uint64(0)
		amount := big.NewInt(400)
		payloadHash := crypto.Keccak256Hash(nil)
		
		digest := ComputeTransferFloatMessage(pub1, pub2, userAddr, userAddr2, amount, nil, payloadHash, nonce)
		cert := bls.Sign(priv1, digest)
		
		msgID, err := TransferFloat(store, pub1, pub2, 102, userAddr, userAddr2, amount, nil, nil, nonce, cert, false, 50, 2)
		if err != nil {
			t.Fatalf("Failed to transfer: %v", err)
		}
		
		// verify auto-create of pub2
		_, found, err := store.GetChainRegistry(hash2)
		if err != nil || !found {
			t.Errorf("Chain registry entry not created for dest")
		}
		
		bal1, _ := store.GetFloat(hash1) // 1000 - 400 = 600
		bal2, _ := store.GetFloat(hash2) // 400
		
		if bal1.Cmp(big.NewInt(600)) != 0 {
			t.Errorf("Expected bal1 600, got %v", bal1)
		}
		if bal2.Cmp(big.NewInt(400)) != 0 {
			t.Errorf("Expected bal2 400, got %v", bal2)
		}
		
		// Invariant check
		if err := CheckFloatSupplyInvariant(store); err != nil {
			t.Errorf("Invariant failed: %v", err)
		}
		
		// Test: MarkClaimed (Claimed lần 2 bị từ chối)
		markDigest := ComputeMarkClaimedMessage(msgID, FloatOutcomeCredited)
		markCert := bls.Sign(priv2, markDigest)
		
		err = MarkClaimed(store, msgID, FloatOutcomeCredited, markCert)
		if err != nil {
			t.Fatalf("MarkClaimed failed: %v", err)
		}
		
		err = MarkClaimed(store, msgID, FloatOutcomeCredited, markCert)
		if err == nil {
			t.Error("Expected error on second MarkClaimed")
		}
		
		// Test: Reclaim after Claimed bị từ chối
		reclaimDigest := ComputeReclaimFloatMessage(msgID, pub1)
		reclaimCert := bls.Sign(priv1, reclaimDigest)
		
		err = ReclaimFloat(store, msgID, reclaimCert, 10, 5)
		if err == nil {
			t.Error("Expected error on Reclaim after Claimed")
		}
	})
	
	// Test: ReclaimFloat before timeout rejected
	t.Run("ReclaimFloat before timeout rejected", func(t *testing.T) {
		store := NewMemoryStore()
		DepositToFloat(store, pub1, 101, common.Address{}, common.Address{}, big.NewInt(1000), common.HexToHash("0x123"), 1)
		
		nonce := uint64(0)
		amount := big.NewInt(100)
		payloadHash := crypto.Keccak256Hash(nil)
		
		digest := ComputeTransferFloatMessage(pub1, pub2, userAddr, userAddr2, amount, nil, payloadHash, nonce)
		cert := bls.Sign(priv1, digest)
		
		msgID, err := TransferFloat(store, pub1, pub2, 102, userAddr, userAddr2, amount, nil, nil, nonce, cert, false, 0, 10)
		if err != nil {
			t.Fatalf("Failed to transfer: %v", err)
		}
		
		reclaimDigest := ComputeReclaimFloatMessage(msgID, pub1)
		reclaimCert := bls.Sign(priv1, reclaimDigest)
		
		// reclaim at time 14, timeout is 5 (requires 10+5=15)
		err = ReclaimFloat(store, msgID, reclaimCert, 14, 5)
		if err != ErrFloatReclaimTooEarly {
			t.Errorf("Expected ErrFloatReclaimTooEarly, got %v", err)
		}
		
		// success at time 15
		err = ReclaimFloat(store, msgID, reclaimCert, 15, 5)
		if err != nil {
			t.Errorf("Reclaim failed: %v", err)
		}
		
		// verify balances
		bal1, _ := store.GetFloat(hash1) // 1000 - 100 + 100 = 1000
		bal2, _ := store.GetFloat(hash2) // 0 + 100 - 100 = 0
		if bal1.Cmp(big.NewInt(1000)) != 0 || bal2.Cmp(big.NewInt(0)) != 0 {
			t.Errorf("Balances not restored correctly")
		}
		
		// Invariant check
		if err := CheckFloatSupplyInvariant(store); err != nil {
			t.Errorf("Invariant failed: %v", err)
		}
	})
	
	// Test: Refund not blocked by velocity
	t.Run("Refund not blocked by velocity", func(t *testing.T) {
		// bal1 is 1000, 20% is 200. Sending 250 should fail if not refund.
		store := NewMemoryStore()
		DepositToFloat(store, pub1, 101, common.Address{}, common.Address{}, big.NewInt(1000), common.HexToHash("0x123"), 1)
		
		nonce := uint64(0)
		amount := big.NewInt(250)
		
		digest := ComputeTransferFloatMessage(pub1, pub2, userAddr, userAddr2, amount, nil, crypto.Keccak256Hash(nil), nonce)
		cert := bls.Sign(priv1, digest)
		
		_, err := TransferFloat(store, pub1, pub2, 102, userAddr, userAddr2, amount, nil, nil, nonce, cert, false, 20, 20)
		if err == nil {
			t.Error("Expected velocity limit error for normal transfer")
		}
		
		// If it is a refund, it should pass
		msgID, err := TransferFloat(store, pub1, pub2, 102, userAddr, userAddr2, amount, nil, nil, nonce, cert, true, 20, 20)
		if err != nil {
			t.Errorf("Refund transfer failed: %v", err)
		}
		
		// Mark it claimed
		markDigest := ComputeMarkClaimedMessage(msgID, FloatOutcomeCredited)
		markCert := bls.Sign(priv2, markDigest)
		_ = MarkClaimed(store, msgID, FloatOutcomeCredited, markCert)
	})
	
	// Test: Persist qua reload (Clone)
	t.Run("Persist qua reload", func(t *testing.T) {
		store := NewMemoryStore()
		DepositToFloat(store, pub1, 101, common.Address{}, common.Address{}, big.NewInt(1000), common.HexToHash("0x123"), 1)
		nonce := uint64(0)
		digest := ComputeTransferFloatMessage(pub1, pub2, userAddr, userAddr2, big.NewInt(450), nil, crypto.Keccak256Hash(nil), nonce)
		cert := bls.Sign(priv1, digest)
		TransferFloat(store, pub1, pub2, 102, userAddr, userAddr2, big.NewInt(450), nil, nil, nonce, cert, false, 50, 2)
		
		reloadedStore := store.Clone()
		
		// Verify state is kept
		bal1, _ := reloadedStore.GetFloat(hash1)
		bal2, _ := reloadedStore.GetFloat(hash2)
		if bal1.Cmp(big.NewInt(550)) != 0 {
			t.Errorf("Expected 550 after clone, got %v", bal1)
		}
		if bal2.Cmp(big.NewInt(450)) != 0 {
			t.Errorf("Expected 450 after clone, got %v", bal2)
		}
		
		if err := CheckFloatSupplyInvariant(reloadedStore); err != nil {
			t.Errorf("Invariant failed after clone: %v", err)
		}
	})
	
	// Test: SubmitStateRoot
	t.Run("SubmitStateRoot valid and duplicate", func(t *testing.T) {
		epoch := uint64(42)
		stateRoot := common.HexToHash("0xabc123")
		digest := ComputeSubmitStateRootMessage(pub1, epoch, stateRoot)
		cert := bls.Sign(priv1, digest)
		
		err := SubmitStateRoot(store, pub1, epoch, stateRoot, cert)
		if err != nil {
			t.Fatalf("Failed to submit state root: %v", err)
		}
		
		// check it's saved
		hash := crypto.Keccak256Hash(pub1[:])
		savedRoot, found, _ := store.GetStateRoot(hash, epoch)
		if !found {
			t.Fatalf("State root not found after submit")
		}
		if savedRoot != stateRoot {
			t.Errorf("Expected root %x, got %x", stateRoot, savedRoot)
		}
		
		// duplicate submission should fail
		err = SubmitStateRoot(store, pub1, epoch, stateRoot, cert)
		if err == nil {
			t.Errorf("Expected error for duplicate state root submission")
		}
		
		// submit with wrong sig
		wrongCert := bls.Sign(priv2, digest)
		err = SubmitStateRoot(store, pub1, epoch+1, stateRoot, wrongCert)
		if err == nil || !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("Expected ErrInvalidSignature, got %v", err)
		}
	})
}
