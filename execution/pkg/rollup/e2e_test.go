package rollup

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

// InMemoryParentChain acts as the Parent Chain network for E2E testing.
type InMemoryParentChain struct {
	mu        sync.Mutex
	store     parentchain.Store
	transfers map[uint64][]*parentchain.TransferEvent // destClusterID -> transfers
	nodeKeys  map[uint64]*bls.KeyPair
	accounts  map[common.Address]cm.PublicKey
}

func NewInMemoryParentChain() *InMemoryParentChain {
	return &InMemoryParentChain{
		store:     parentchain.NewMemoryStore(),
		transfers: make(map[uint64][]*parentchain.TransferEvent),
		nodeKeys:  make(map[uint64]*bls.KeyPair),
		accounts:  make(map[common.Address]cm.PublicKey),
	}
}

// ParentChainClientAdapter adapts InMemoryParentChain to parentchain.Client
type ParentChainClientAdapter struct {
	chain   *InMemoryParentChain
	chainID uint64
	delay   time.Duration // simulate latency or failures
}

func (a *ParentChainClientAdapter) SendDepositToFloat(
	pubKey cm.PublicKey,
	destClusterID uint64,
	sender, target common.Address,
	amount *big.Int,
) (common.Hash, error) {
	msgID := common.BytesToHash([]byte(fmt.Sprintf("deposit_adapter_%d", time.Now().UnixNano())))
	// We call DepositToFloat directly to simulate what the gateway would do
	err := parentchain.DepositToFloat(
		a.chain.store,
		pubKey,
		destClusterID,
		sender,
		target,
		amount,
		msgID,
		uint64(time.Now().Unix()),
	)
	return msgID, err
}

func (a *ParentChainClientAdapter) SendTransferFloat(
	pubKey, destPubKey cm.PublicKey,
	destClusterID uint64,
	sender, target common.Address,
	amount, gasFee *big.Int,
	nonce uint64,
	cert []byte,
	isRefund bool,
) (common.Hash, error) {
	if a.delay > 0 {
		time.Sleep(a.delay)
	}
	a.chain.mu.Lock()
	defer a.chain.mu.Unlock()

	var sig cm.Sign
	copy(sig[:], cert)
	
	blockTime := uint64(time.Now().Unix())

	msgID, err := parentchain.TransferFloat(
		a.chain.store,
		pubKey, destPubKey,
		destClusterID,
		sender, target,
		amount,
		gasFee,
		nil, // payload
		nonce,
		sig,
		isRefund,
		0,
		blockTime,
	)
	if err != nil {
		fmt.Printf("[DEBUG-ADAPTER] TransferFloat failed for nonce=%d, sender=%x, target=%x: %v\n", nonce, sender, target, err)
		return common.Hash{}, err
	}

	event := &parentchain.TransferEvent{
		MsgID:       msgID,
		SourcePubKey: pubKey,
		DestPubKey:   destPubKey,
		SourceSeq:   nonce,
		Sender:      sender,
		Target:      target,
		Amount:      amount,
		PayloadHash: crypto.Keccak256Hash(nil),
		BlockTime:   blockTime,
		IsRefund:    isRefund,
	}

	var destID uint64
	for id, kp := range a.chain.nodeKeys {
		if kp.PublicKey() == destPubKey {
			destID = id
		}
	}
	if destID == 0 {
		destID = destClusterID // Fallback
	}

	a.chain.transfers[destID] = append(a.chain.transfers[destID], event)
	return msgID, nil
}

func (a *ParentChainClientAdapter) SendMarkClaimed(msgID common.Hash, outcome parentchain.FloatOutcome, cert []byte) (common.Hash, error) {
	if a.delay > 0 {
		time.Sleep(a.delay)
	}
	a.chain.mu.Lock()
	defer a.chain.mu.Unlock()

	var sig cm.Sign
	copy(sig[:], cert)
	err := parentchain.MarkClaimed(a.chain.store, msgID, outcome, sig)
	return msgID, err
}

func (a *ParentChainClientAdapter) SendReclaimFloat(msgID common.Hash, cert []byte) (common.Hash, error) {
	a.chain.mu.Lock()
	defer a.chain.mu.Unlock()

	var sig cm.Sign
	copy(sig[:], cert)
	
	// Mock time to be +100s so it's guaranteed to be eligible
	blockTime := uint64(time.Now().Unix()) + 100
	err := parentchain.ReclaimFloat(a.chain.store, msgID, sig, blockTime, 60)
	return msgID, err
}

func (a *ParentChainClientAdapter) GetInboundTransfers(pubKey cm.PublicKey, cursor uint64) ([]*parentchain.TransferEvent, uint64, error) {
	a.chain.mu.Lock()
	defer a.chain.mu.Unlock()

	events := a.chain.transfers[a.chainID]
	if cursor >= uint64(len(events)) {
		return nil, cursor, nil
	}

	res := events[cursor:]
	return res, uint64(len(events)), nil
}

func (a *ParentChainClientAdapter) GetTransferRecord(msgID common.Hash) (parentchain.FloatTransferRecord, bool, error) {
	a.chain.mu.Lock()
	defer a.chain.mu.Unlock()
	return a.chain.store.GetTransferRecord(msgID)
}

func (a *ParentChainClientAdapter) GetAccountRegistry(userAddress common.Address) (cm.PublicKey, bool, error) {
	a.chain.mu.Lock()
	defer a.chain.mu.Unlock()
	pubKey, ok := a.chain.accounts[userAddress]
	return pubKey, ok, nil
}

func (a *ParentChainClientAdapter) SendRegisterAccount(userAddress common.Address, pubKey cm.PublicKey, signBytes []byte, sign cm.Sign) (common.Hash, error) {
	a.chain.mu.Lock()
	defer a.chain.mu.Unlock()
	a.chain.accounts[userAddress] = pubKey
	return common.Hash{}, nil
}

func (a *ParentChainClientAdapter) GetClaimed(msgID common.Hash) (parentchain.FloatOutcome, error) {
	a.chain.mu.Lock()
	defer a.chain.mu.Unlock()
	outcome, err := a.chain.store.GetClaimed(msgID)
	return outcome, err
}

func (a *ParentChainClientAdapter) GetFloatSeq(pubKey cm.PublicKey) (uint64, error) {
	a.chain.mu.Lock()
	defer a.chain.mu.Unlock()
	hash := crypto.Keccak256Hash(pubKey[:])
	return a.chain.store.GetFloatSeq(hash)
}

func (a *ParentChainClientAdapter) SendSubmitStateRoot(clusterPubKey cm.PublicKey, epoch uint64, stateRoot common.Hash, cert cm.Sign) (common.Hash, error) {
	return common.Hash{}, nil
}

func (a *ParentChainClientAdapter) GetStateRoot(clusterPubKey cm.PublicKey, epoch uint64) (common.Hash, bool, error) {
	return common.Hash{}, false, nil
}

// RollupNode represents a single rollup cluster with its workers
type RollupNode struct {
	ChainID uint64
	StateDB AccountStateDB
	Store   Store
	
	sendWorker    *SendWorker
	receiveWorker *ReceiveWorker
	reclaimWorker *ReclaimWorker
	blsKeyPair    *bls.KeyPair
}

func NewRollupNode(chainID uint64, parentChain *InMemoryParentChain, kp *bls.KeyPair, destPubKey cm.PublicKey, destClusterID uint64) *RollupNode {
	stateDB := newMockAccountStateDB()
	scDB := &mockDB{data: make(map[common.Address]map[common.Hash][]byte)}
	store := NewDBStore(scDB)
	
	client := &ParentChainClientAdapter{chain: parentChain, chainID: chainID}
	
	sendW := NewSendWorker(store, client, kp, destPubKey, destClusterID)
	recW := NewReceiveWorker(store, stateDB, client, kp)
	reclaimW := NewReclaimWorker(store, stateDB, client, kp)
	
	return &RollupNode{
		ChainID:       chainID,
		StateDB:       stateDB,
		Store:         store,
		sendWorker:    sendW,
		receiveWorker: recW,
		reclaimWorker: reclaimW,
		blsKeyPair:    kp,
	}
}

func (n *RollupNode) Start() {}
func (n *RollupNode) Stop() {}

func setupClusterFloatBalance(t *testing.T, parentChain *InMemoryParentChain, kp *bls.KeyPair, chainID uint64, amount *big.Int) {
	msgID := common.BytesToHash([]byte(fmt.Sprintf("deposit_init_%d", chainID)))
	err := parentchain.DepositToFloat(parentChain.store, kp.PublicKey(), chainID, common.Address{}, common.Address{}, amount, msgID, uint64(time.Now().Unix()))
	if err != nil {
		t.Fatalf("setupClusterFloatBalance failed: %v", err)
	}
}

func TestE2E_HappyPathTransfer(t *testing.T) {
	parentChain := NewInMemoryParentChain()
	
	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()
	parentChain.nodeKeys[1] = kp1
	parentChain.nodeKeys[2] = kp2
	
	sender := common.HexToAddress("0xaaa")
	target := common.HexToAddress("0xbbb")
	
	parentChain.accounts[sender] = kp1.PublicKey()
	parentChain.accounts[target] = kp2.PublicKey()
	
	setupClusterFloatBalance(t, parentChain, kp1, 1, big.NewInt(10000))
	setupClusterFloatBalance(t, parentChain, kp2, 2, big.NewInt(10000))

	node1 := NewRollupNode(1, parentChain, kp1, kp2.PublicKey(), 2)
	node2 := NewRollupNode(2, parentChain, kp2, kp1.PublicKey(), 1)
	
	// Fund node 1 sender
	node1.StateDB.(*mockAccountStateDB).balances[sender] = big.NewInt(1000)
	
	handler := NewCrossNodeHandler(node1.blsKeyPair.PublicKey())
	msgID, err := handler.HandleTransfer(node1.Store, node1.StateDB, node2.blsKeyPair.PublicKey(), sender, target, big.NewInt(500), crypto.Keccak256Hash(nil))
	if err != nil {
		t.Fatalf("Failed to handle transfer: %v", err)
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	success := false
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("Timeout waiting for E2E transfer")
		case <-time.After(50 * time.Millisecond):
			node1.sendWorker.processPending()
			node2.receiveWorker.pollAndProcess()
			
			bal1 := node1.StateDB.GetBalance(sender)
			bal2 := node2.StateDB.GetBalance(target)
			rec2, f2, _ := node2.Store.Get(msgID)
			
			// DEBUG LOGGING
			// t.Logf("bal1: %v, bal2: %v, f2: %v, rec2.State: %v", bal1, bal2, f2, rec2.State)
			
			if bal1.Cmp(big.NewInt(500)) == 0 && bal2.Cmp(big.NewInt(500)) == 0 {
				if f2 && rec2.State == StateCredited {
					success = true
				}
			}
		}
		if success {
			break
		}
	}
}

func TestE2E_TransferRefund(t *testing.T) {
	parentChain := NewInMemoryParentChain()
	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()
	parentChain.nodeKeys[1] = kp1
	parentChain.nodeKeys[2] = kp2
	
	setupClusterFloatBalance(t, parentChain, kp1, 1, big.NewInt(10000))
	setupClusterFloatBalance(t, parentChain, kp2, 2, big.NewInt(10000))
	
	sender := common.HexToAddress("0xaaa")
	target := common.HexToAddress("0xbbb")
	
	parentChain.accounts[sender] = kp1.PublicKey()
	parentChain.accounts[target] = kp2.PublicKey()
	
	node1 := NewRollupNode(1, parentChain, kp1, kp2.PublicKey(), 2)
	node2 := NewRollupNode(2, parentChain, kp2, kp1.PublicKey(), 1)
	
	node2.receiveWorker.Validator = func(addr common.Address) bool {
		return false
	}
	
	node1.StateDB.(*mockAccountStateDB).balances[sender] = big.NewInt(1000)
	
	handler := NewCrossNodeHandler(node1.blsKeyPair.PublicKey())
	_, err := handler.HandleTransfer(node1.Store, node1.StateDB, node2.blsKeyPair.PublicKey(), sender, target, big.NewInt(500), crypto.Keccak256Hash(nil))
	if err != nil {
		t.Fatalf("HandleTransfer failed: %v", err)
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	success := false
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("Timeout waiting for E2E refund")
		case <-time.After(50 * time.Millisecond):
			node1.sendWorker.processPending()
			node2.receiveWorker.pollAndProcess()
			node1.receiveWorker.pollAndProcess()
			
			bal1 := node1.StateDB.GetBalance(sender)
			if bal1.Cmp(big.NewInt(1000)) == 0 {
				success = true
			}
		}
		if success {
			break
		}
	}
}

func TestE2E_TransferReclaimWon(t *testing.T) {
	parentChain := NewInMemoryParentChain()
	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()
	parentChain.nodeKeys[1] = kp1
	parentChain.nodeKeys[2] = kp2
	
	setupClusterFloatBalance(t, parentChain, kp1, 1, big.NewInt(10000))
	setupClusterFloatBalance(t, parentChain, kp2, 2, big.NewInt(10000))

	sender := common.HexToAddress("0xaaa")
	target := common.HexToAddress("0xbbb")
	
	parentChain.accounts[sender] = kp1.PublicKey()
	parentChain.accounts[target] = kp2.PublicKey()

	node1 := NewRollupNode(1, parentChain, kp1, kp2.PublicKey(), 2)
	node2 := NewRollupNode(2, parentChain, kp2, kp1.PublicKey(), 1)
	node1.StateDB.(*mockAccountStateDB).balances[sender] = big.NewInt(1000)
	
	handler := NewCrossNodeHandler(node1.blsKeyPair.PublicKey())
	msgID, err := handler.HandleTransfer(node1.Store, node1.StateDB, node2.blsKeyPair.PublicKey(), sender, target, big.NewInt(500), crypto.Keccak256Hash(nil))
	if err != nil {
		t.Fatalf("Failed to handle transfer: %v", err)
	}
	
	node1.sendWorker.processPending() // step 1: submit
	node1.sendWorker.processPending() // step 2: poll-confirm (StateSendSubmitted -> StateSentConfirmed)
	// Deliberately DO NOT process node2 receive worker. It "ignores" the transfer.

	// Ensure ReclaimWorker thinks time has passed by advancing time in the DB
	// We'll just let the mock logic in ReclaimWorker trigger (mocking time.Now() is tricky without modifying checkAndReclaim).
	// Actually ReclaimWorker uses uint64(time.Now().Unix()).
	// For testing, let's artificially hack the record's blocktime so it thinks it is very old.
	parentChain.mu.Lock()
	transferList := parentChain.transfers[2]
	for _, tx := range transferList {
		if tx.MsgID == msgID {
			tx.BlockTime -= 100 // push it 100 seconds in the past
		}
	}
	parentChain.mu.Unlock()
	// Hack the transfer record in memory store too
	prec, found, _ := parentChain.store.GetTransferRecord(msgID)
	if found {
		prec.ConfirmedAtBlockTime = uint64(time.Now().Unix()) - 100
		parentChain.store.SetTransferRecord(msgID, prec)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	success := false
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("Timeout waiting for Reclaim to win")
		case <-time.After(50 * time.Millisecond):
			node1.reclaimWorker.processReclaims()
			bal1 := node1.StateDB.GetBalance(sender)
			rec1, _, _ := node1.Store.Get(msgID)
			if bal1.Cmp(big.NewInt(1000)) == 0 {
				if rec1 != nil && rec1.State == StateConfirmedRefunded {
					success = true
				}
			}
		}
		if success {
			break
		}
	}
}

func TestE2E_TransferReclaimLost_DoubleCreditPrevention(t *testing.T) {
	parentChain := NewInMemoryParentChain()
	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()
	parentChain.nodeKeys[1] = kp1
	parentChain.nodeKeys[2] = kp2
	
	setupClusterFloatBalance(t, parentChain, kp1, 1, big.NewInt(10000))
	setupClusterFloatBalance(t, parentChain, kp2, 2, big.NewInt(10000))
	sender := common.HexToAddress("0xaaa")
	target := common.HexToAddress("0xbbb")
	
	parentChain.accounts[sender] = kp1.PublicKey()
	parentChain.accounts[target] = kp2.PublicKey()

	node1 := NewRollupNode(1, parentChain, kp1, kp2.PublicKey(), 2)
	node2 := NewRollupNode(2, parentChain, kp2, kp1.PublicKey(), 1)
	
	// Node 2 rejects destination
	node2.receiveWorker.Validator = func(addr common.Address) bool { return false }
	
	node1.StateDB.(*mockAccountStateDB).balances[sender] = big.NewInt(1000)
	
	handler := NewCrossNodeHandler(node1.blsKeyPair.PublicKey())
	msgID, err := handler.HandleTransfer(node1.Store, node1.StateDB, node2.blsKeyPair.PublicKey(), sender, target, big.NewInt(500), crypto.Keccak256Hash(nil))
	if err != nil {
		t.Fatalf("Failed to handle transfer: %v", err)
	}
	
	node1.sendWorker.processPending() // step 1: submit
	node1.sendWorker.processPending() // step 2: poll-confirm (StateSendSubmitted -> StateSentConfirmed)

	// Hack time to allow Reclaim to be sent
	parentChain.mu.Lock()
	transferList := parentChain.transfers[2]
	for _, tx := range transferList {
		if tx.MsgID == msgID {
			tx.BlockTime -= 100 // push it 100 seconds in the past
		}
	}
	parentChain.mu.Unlock()
	prec, found, _ := parentChain.store.GetTransferRecord(msgID)
	if found {
		prec.ConfirmedAtBlockTime = uint64(time.Now().Unix()) - 100
		parentChain.store.SetTransferRecord(msgID, prec)
	}

	// 1. Node 2 processes it and sends a refund, calling MarkClaimed(Refund)
	node2.receiveWorker.pollAndProcess() // step 1: submit MarkClaimed(Refund)
	node2.receiveWorker.pollAndProcess() // step 2: poll-confirm + actually send the compensating Transfer

	// 2. Node 1 tries to reclaim. It submits Reclaim (this will fail in ParentChain because MarkClaimed(Refund) was already processed)
	node1.reclaimWorker.processReclaims()

	// Let Node 1 start processing the Refund transfer (submit MarkClaimed(Credited) for it)
	node1.receiveWorker.pollAndProcess()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	success := false
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("Timeout waiting for DoubleCreditPrevention test")
		case <-time.After(50 * time.Millisecond):
			node1.reclaimWorker.processReclaims() // keep polling reclaim outcome
			node1.receiveWorker.pollAndProcess()  // keep polling to actually credit the incoming refund

			bal1 := node1.StateDB.GetBalance(sender)
			rec1, _, _ := node1.Store.Get(msgID)
			
			// Balance must NOT exceed 1000 (No double credit)
			if bal1.Cmp(big.NewInt(1000)) > 0 {
				t.Fatalf("Double credit detected! Balance is %v", bal1)
			}
			if bal1.Cmp(big.NewInt(1000)) == 0 && rec1 != nil && rec1.State == StateConfirmedRefunded {
				success = true
			}
		}
		if success {
			break
		}
	}
}

func TestE2E_Refund_DoubleCreditPrevention(t *testing.T) {
	parentChain := NewInMemoryParentChain()
	kp1 := bls.GenerateKeyPair()
	kp2 := bls.GenerateKeyPair()
	parentChain.nodeKeys[1] = kp1
	parentChain.nodeKeys[2] = kp2
	
	setupClusterFloatBalance(t, parentChain, kp1, 1, big.NewInt(10000))
	setupClusterFloatBalance(t, parentChain, kp2, 2, big.NewInt(10000))

	sender := common.HexToAddress("0xaaa")
	target := common.HexToAddress("0xbbb")
	
	parentChain.accounts[sender] = kp1.PublicKey()
	parentChain.accounts[target] = kp2.PublicKey()

	node1 := NewRollupNode(1, parentChain, kp1, kp2.PublicKey(), 2)
	node2 := NewRollupNode(2, parentChain, kp2, kp1.PublicKey(), 1)
	
	// Node 2 rejects destination
	node2.receiveWorker.Validator = func(addr common.Address) bool { return false }
	
	node1.StateDB.(*mockAccountStateDB).balances[sender] = big.NewInt(1000)
	
	handler := NewCrossNodeHandler(node1.blsKeyPair.PublicKey())
	msgID, err := handler.HandleTransfer(node1.Store, node1.StateDB, node2.blsKeyPair.PublicKey(), sender, target, big.NewInt(500), crypto.Keccak256Hash(nil))
	if err != nil {
		t.Fatalf("Failed to handle transfer: %v", err)
	}
	
	node1.sendWorker.processPending() // step 1: submit
	node1.sendWorker.processPending() // step 2: poll-confirm (StateSendSubmitted -> StateSentConfirmed)

	// 1. Node 2 processes it and sends a refund, calling MarkClaimed(Refund)
	node2.receiveWorker.pollAndProcess() // step 1: submit MarkClaimed(Refund)
	node2.receiveWorker.pollAndProcess() // step 2: poll-confirm + actually send the compensating Transfer
	node2.sendWorker.processPending()

	// 2. Node 1 receives Refund (submit MarkClaimed(Credited), then poll-confirm + credit)
	node1.receiveWorker.pollAndProcess()
	node1.receiveWorker.pollAndProcess()

	// 3. Hack time to allow Reclaim to trigger checkReclaimOutcome
	parentChain.mu.Lock()
	transferList := parentChain.transfers[2]
	for _, tx := range transferList {
		if tx.MsgID == msgID {
			tx.BlockTime -= 100 // push it 100 seconds in the past
		}
	}
	parentChain.mu.Unlock()
	prec, found, _ := parentChain.store.GetTransferRecord(msgID)
	if found {
		prec.ConfirmedAtBlockTime = uint64(time.Now().Unix()) - 100
		parentChain.store.SetTransferRecord(msgID, prec)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	success := false
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("Timeout waiting for DoubleCreditPrevention test")
		case <-time.After(50 * time.Millisecond):
			// Call processReclaims to simulate the bug scenario
			// Prior to the fix, this would double credit!
			node1.reclaimWorker.processReclaims() 
			
			bal1 := node1.StateDB.GetBalance(sender)
			rec1, _, _ := node1.Store.Get(msgID)
			
			// Balance must NOT exceed 1000 (No double credit)
			if bal1.Cmp(big.NewInt(1000)) > 0 {
				t.Fatalf("Double credit detected! Balance is %v", bal1)
			}
			if bal1.Cmp(big.NewInt(1000)) == 0 && rec1 != nil && rec1.State == StateConfirmedRefunded {
				success = true
			}
		}
		if success {
			break
		}
	}
}
