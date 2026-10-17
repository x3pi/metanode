package processor

import (
	"math/big"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/cmd/simple_chain/command"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/trie"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTxVirtualExecutor_ParseReadTx_Protobuf(t *testing.T) {
	v := &TxVirtualExecutor{}

	from := common.HexToAddress("0x1234567890123456789012345678901234567890")
	to := common.HexToAddress("0x0987654321098765432109876543210987654321")
	amount := big.NewInt(1000)

	tx := transaction.NewTransaction(
		from,
		to,
		amount,
		50000,
		1,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		1,
		0,
	).(*transaction.Transaction)

	txBytes, err := tx.Marshal()
	if err != nil {
		t.Fatalf("failed to marshal tx: %v", err)
	}

	parsedTx, err := v.parseReadTx(txBytes)
	if err != nil {
		t.Fatalf("parseReadTx failed for protobuf: %v", err)
	}
	if parsedTx.FromAddress() != from || parsedTx.ToAddress() != to {
		t.Fatalf("parsed protobuf tx address mismatch")
	}
}

func TestTxVirtualExecutor_ParseReadTx_EmptyBody(t *testing.T) {
	v := &TxVirtualExecutor{}
	_, err := v.parseReadTx([]byte{})
	if err == nil {
		t.Fatalf("expected error for empty body, got nil")
	}
}

func TestTxVirtualExecutor_ParseReadTx_ChainIDMatchAndMismatch(t *testing.T) {
	chainState := &blockchain.ChainState{}
	chainState.SetConfig(&config.SimpleChainConfig{
		ChainId: big.NewInt(1337),
	})
	v := &TxVirtualExecutor{
		chainState: chainState,
	}

	from := common.HexToAddress("0x1234567890123456789012345678901234567890")
	to := common.HexToAddress("0x0987654321098765432109876543210987654321")

	// Case 1: Matching chain ID 1337 -> MUST PASS
	txMatch := transaction.NewTransaction(
		from,
		to,
		big.NewInt(0),
		50000,
		1,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		1,
		1337,
	).(*transaction.Transaction)
	bytesMatch, _ := txMatch.Marshal()
	parsedMatch, err := v.parseReadTx(bytesMatch)
	if err != nil {
		t.Fatalf("expected matching chainId to succeed, got error: %v", err)
	}
	if parsedMatch.GetChainID() != 1337 {
		t.Fatalf("expected chainId 1337, got: %d", parsedMatch.GetChainID())
	}

	// Case 2: Chain ID = 0 when node requires 1337 -> MUST FAIL
	txZero := transaction.NewTransaction(
		from,
		to,
		big.NewInt(0),
		50000,
		1,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		1,
		0,
	).(*transaction.Transaction)
	bytesZero, _ := txZero.Marshal()
	_, err = v.parseReadTx(bytesZero)
	if err == nil {
		t.Fatalf("expected error for chainId=0 when node has chainId=1337, got nil")
	}
	if !strings.Contains(err.Error(), "chainId does not match node's") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Case 3: Tx with mismatched chain ID 9999 -> MUST FAIL
	txMismatch := transaction.NewTransaction(
		from,
		to,
		big.NewInt(0),
		50000,
		1,
		0,
		nil,
		nil,
		common.Hash{},
		common.Hash{},
		1,
		9999,
	).(*transaction.Transaction)

	bytesMismatch, _ := txMismatch.Marshal()
	_, err = v.parseReadTx(bytesMismatch)
	if err == nil {
		t.Fatalf("expected chainId mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "chainId does not match node's") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestPlainTransferEstimateGas(t *testing.T) {
	from := common.HexToAddress("0x1234567890123456789012345678901234567890")
	to := common.HexToAddress("0x0987654321098765432109876543210987654321")
	tx := transaction.NewTransaction(from, to, big.NewInt(1), 50000, 1, 0, nil, nil, common.Hash{}, common.Hash{}, 1, 0).(*transaction.Transaction)

	// A plain transfer owes only the minimum base fee.
	if got := plainTransferEstimateGas(tx); got != mt_common.MINIMUM_BASE_FEE {
		t.Fatalf("plain transfer: got %d, want %d", got, mt_common.MINIMUM_BASE_FEE)
	}

	// A SetCode tx also owes the intrinsic gas of every authorization tuple (same rule as HTTP eth_estimateGas).
	tx.SetAuthorizationList(transaction.FromEthAuthorizationList([]e_types.SetCodeAuthorization{{}, {}}))
	want := params.TxGas + 2*params.CallNewAccountGas + mt_common.MINIMUM_BASE_FEE
	if got := plainTransferEstimateGas(tx); got != want {
		t.Fatalf("SetCode tx with 2 tuples: got %d, want %d", got, want)
	}
}

// Clients matched on this wording before the read path accepted plain transfers; keep it stable.
func TestErrNotSmartContractCallWording(t *testing.T) {
	if errNotSmartContractCall != "not a smart contract call" {
		t.Fatalf("wording changed: %q", errNotSmartContractCall)
	}
}

func TestTxVirtualExecutor_ExecuteAndRespondReadTx_MockConnection(t *testing.T) {
	prevBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	t.Cleanup(func() { trie.SetStateBackend(prevBackend) })

	header := block.NewBlockHeader(
		common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{},
		common.Address{}, 0, common.Hash{}, 0,
	)
	accountStorage := storage.NewDummyStorage("")
	codeStorage := storage.NewDummyStorage("")
	scStorage := storage.NewDummyStorage("")
	cs, err := blockchain.NewChainStateRemote(header, accountStorage, codeStorage, scStorage, map[common.Address]struct{}{})
	if err != nil {
		t.Fatalf("failed to create chain state: %v", err)
	}
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId: big.NewInt(1),
	})

	from := common.HexToAddress("0x1111111111111111111111111111111111111111")
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")

	// Set initial balance for from and to
	asFrom, err := cs.GetAccountStateDB().AccountState(from)
	require.NoError(t, err)
	asFrom.AddBalance(big.NewInt(1_000_000_000))
	cs.GetAccountStateDB().PublicSetDirtyAccountState(asFrom)

	asTo, err := cs.GetAccountStateDB().AccountState(to)
	require.NoError(t, err)
	asTo.AddBalance(big.NewInt(500))
	cs.GetAccountStateDB().PublicSetDirtyAccountState(asTo)

	initialBalFrom := new(big.Int).Set(asFrom.Balance())
	initialBalTo := new(big.Int).Set(asTo.Balance())

	v := &TxVirtualExecutor{
		chainState:          cs,
		messageSender:       NewMockMessageSender(),
		readTxLimiter:       make(chan struct{}, 10),
		blockProcessingLock: &sync.RWMutex{},
	}

	tx := transaction.NewTransaction(
		from, to, big.NewInt(100), 50000, 100000, 0, nil, nil, common.Hash{}, common.Hash{}, 1, 1,
	).(*transaction.Transaction)

	// 1. eth_call (isEstimateGas = false) on an ordinary EOA (no smart contract):
	// Must respond with TransactionError command and errNotSmartContractCall ("not a smart contract call")
	conn1 := NewMockConnection(from)
	req1 := readTxRequest{
		conn:          conn1,
		tx:            tx,
		msgID:         "test-call-msg-id-1",
		isEstimateGas: false,
	}
	v.executeAndRespondReadTx(req1)

	require.Equal(t, 1, conn1.SentCount())
	lastMsg1 := conn1.LastSentMessage()
	require.NotNil(t, lastMsg1)
	assert.Equal(t, command.TransactionError, lastMsg1.Command())
	assert.Equal(t, "test-call-msg-id-1", lastMsg1.ID())

	errPb := &pb.TransactionHashWithError{}
	err = proto.Unmarshal(lastMsg1.Body(), errPb)
	require.NoError(t, err)
	assert.Equal(t, errNotSmartContractCall, errPb.Description)

	// 2. eth_estimateGas (isEstimateGas = true) on a plain transfer:
	// Must respond with Receipt command, RETURNED status, and gasUsed == MINIMUM_BASE_FEE
	conn2 := NewMockConnection(from)
	req2 := readTxRequest{
		conn:          conn2,
		tx:            tx,
		msgID:         "test-estimate-msg-id-2",
		isEstimateGas: true,
	}
	v.executeAndRespondReadTx(req2)

	require.Equal(t, 1, conn2.SentCount())
	lastMsg2 := conn2.LastSentMessage()
	require.NotNil(t, lastMsg2)
	assert.Equal(t, command.Receipt, lastMsg2.Command())
	assert.Equal(t, "test-estimate-msg-id-2", lastMsg2.ID())

	rcp2 := &receipt.Receipt{}
	err = rcp2.Unmarshal(lastMsg2.Body())
	require.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcp2.Status())
	assert.Equal(t, uint64(mt_common.MINIMUM_BASE_FEE), rcp2.GasUsed())

	// 3. Verify state safety: neither from nor to account balance must have changed!
	asFromPost, err := cs.GetAccountStateDB().AccountState(from)
	require.NoError(t, err)
	assert.Equal(t, initialBalFrom, asFromPost.Balance(), "from balance must not change during virtual execution")

	asToPost, err := cs.GetAccountStateDB().AccountState(to)
	require.NoError(t, err)
	assert.Equal(t, initialBalTo, asToPost.Balance(), "to balance must not change during virtual execution")
}

func TestTxVirtualExecutor_EstimateGas_EIP7702_And_StateImmutability(t *testing.T) {
	prevBackend := trie.GetStateBackend()
	trie.SetStateBackend(trie.BackendMPT)
	t.Cleanup(func() { trie.SetStateBackend(prevBackend) })

	header := block.NewBlockHeader(
		common.Hash{}, 0, common.Hash{}, common.Hash{}, common.Hash{},
		common.Address{}, 0, common.Hash{}, 0,
	)
	accountStorage := storage.NewDummyStorage("")
	codeStorage := storage.NewDummyStorage("")
	scStorage := storage.NewDummyStorage("")
	cs, err := blockchain.NewChainStateRemote(header, accountStorage, codeStorage, scStorage, map[common.Address]struct{}{})
	if err != nil {
		t.Fatalf("failed to create chain state: %v", err)
	}
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId: big.NewInt(1),
	})

	from := common.HexToAddress("0x3333333333333333333333333333333333333333")
	to := common.HexToAddress("0x4444444444444444444444444444444444444444")

	asFrom, err := cs.GetAccountStateDB().AccountState(from)
	require.NoError(t, err)
	asFrom.AddBalance(big.NewInt(10_000_000_000))
	cs.GetAccountStateDB().PublicSetDirtyAccountState(asFrom)

	asTo, err := cs.GetAccountStateDB().AccountState(to)
	require.NoError(t, err)
	asTo.AddBalance(big.NewInt(10_000_000))
	cs.GetAccountStateDB().PublicSetDirtyAccountState(asTo)

	// Set contract storage slot using cs.GetSmartContractDB()
	slotKey := common.HexToHash("0x01")
	slotVal := common.HexToHash("0x42")
	err = cs.GetSmartContractDB().SetStorageValue(to, slotKey.Bytes(), slotVal.Bytes())
	require.NoError(t, err)

	v := &TxVirtualExecutor{
		chainState:          cs,
		messageSender:       NewMockMessageSender(),
		readTxLimiter:       make(chan struct{}, 10),
		blockProcessingLock: &sync.RWMutex{},
	}

	// 1. EIP-7702 (SetCode) transaction estimate gas:
	// Verify calculation: TxGas + n*CallNewAccountGas + MINIMUM_BASE_FEE
	tx7702 := transaction.NewTransaction(
		from, from, big.NewInt(0), 100000, 100000, 0, nil, nil, common.Hash{}, common.Hash{}, 1, 1,
	).(*transaction.Transaction)
	nAuth := 3
	auths := make([]e_types.SetCodeAuthorization, nAuth)
	tx7702.SetAuthorizationList(transaction.FromEthAuthorizationList(auths))

	conn7702 := NewMockConnection(from)
	req7702 := readTxRequest{
		conn:          conn7702,
		tx:            tx7702,
		msgID:         "msg-eip7702-test",
		isEstimateGas: true,
	}
	v.executeAndRespondReadTx(req7702)

	require.Equal(t, 1, conn7702.SentCount())
	lastMsg := conn7702.LastSentMessage()
	require.NotNil(t, lastMsg)
	assert.Equal(t, command.Receipt, lastMsg.Command())
	assert.Equal(t, "msg-eip7702-test", lastMsg.ID())

	rcp := &receipt.Receipt{}
	err = rcp.Unmarshal(lastMsg.Body())
	require.NoError(t, err)
	assert.Equal(t, pb.RECEIPT_STATUS_RETURNED, rcp.Status())
	expectedGas := params.TxGas + uint64(nAuth)*params.CallNewAccountGas + mt_common.MINIMUM_BASE_FEE
	assert.Equal(t, expectedGas, rcp.GasUsed(), "EIP-7702 gas must include intrinsic auth gas + MINIMUM_BASE_FEE")

	// 2. Verify storage slots remain unmodified:
	storedVal, ok := cs.GetSmartContractDB().StorageValue(to, slotKey.Bytes())
	assert.True(t, ok)
	assert.Equal(t, slotVal.Bytes(), storedVal, "storage slot must remain unchanged after EstimateGas")
}
