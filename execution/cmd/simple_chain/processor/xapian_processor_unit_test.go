package processor

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	eth "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/block"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	mt_state "github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/pkg/trie_database"
	"github.com/meta-node-blockchain/meta-node/types"
)

// Minimal EVM forwarder calling Xapian V1 precompile 0x0107 with full calldata and returning full returndata.
var xapianPrecompileForwarderBytecode = []byte{
	0x36, 0x60, 0x00, 0x60, 0x00, 0x37, // CALLDATASIZE, PUSH1 0, PUSH1 0, CALLDATACOPY
	0x60, 0x00, 0x60, 0x00, 0x36, 0x60, 0x00, 0x60, 0x00, 0x61, 0x01, 0x07, 0x5a, 0xf1, 0x50, // CALL(GAS, 0x0107, 0, 0, CALLDATASIZE, 0, 0), POP
	0x3d, 0x60, 0x00, 0x60, 0x00, 0x3e, // RETURNDATACOPY(0, 0, RETURNDATASIZE)
	0x3d, 0x60, 0x00, 0xf3, // RETURN(0, RETURNDATASIZE)
}

func packXapianNewDocV1(t *testing.T, dbName string, docPayload []byte) []byte {
	t.Helper()
	a, err := abi.JSON(strings.NewReader(`[
		{"type":"function","name":"getOrCreateDb","inputs":[{"name":"dbname","type":"string"}],"outputs":[{"type":"bool"}]},
		{"type":"function","name":"newDocument","inputs":[{"name":"dbname","type":"string"},{"name":"data","type":"bytes"}],"outputs":[{"type":"uint256"}]},
		{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"bytes"}]}
	]`))
	require.NoError(t, err)
	out, err := a.Pack("newDocument", dbName, docPayload)
	require.NoError(t, err)
	return out
}

func packXapianGetOrCreateDbV1(t *testing.T, dbName string) []byte {
	t.Helper()
	a, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getOrCreateDb","inputs":[{"name":"dbname","type":"string"}],"outputs":[{"type":"bool"}]}]`))
	require.NoError(t, err)
	out, err := a.Pack("getOrCreateDb", dbName)
	require.NoError(t, err)
	return out
}

func setupProcessorXapianEnv(t *testing.T, contractAddr common.Address) (*BlockProcessor, func(nonce uint64, calldata []byte) types.Transaction, common.Address) {
	t.Helper()
	prevBlockNum := storage.GetLastBlockNumber()
	prevGEI := storage.GetLastGlobalExecIndex()
	t.Cleanup(func() {
		mvm.ClearAllXapianTxBuffers()
		mvm.DestroyAllXapianInstances()
		storage.ResetAllBlockCounters(prevBlockNum)
		storage.ForceSetLastGlobalExecIndex(prevGEI)
	})

	mvm.DestroyAllXapianInstances()
	mvm.ClearAllMVMApi()
	mvm.CallClearAllStateInstances()
	mvm.ConfigureXapianBasePath(t.TempDir())

	bp, _ := readRouteHarness(t)
	trie_database.CreateTrieDatabaseManager(bp.storageManager.GetStorageDatabaseTrie(), nil)
	restoreBC := blockchain.SwapBlockChainForTest(bp.chainState.GetBlockDatabase(), bp.storageManager)
	t.Cleanup(restoreBC)

	key, err := crypto.HexToECDSA("4f3edf983ac636a65a842ce7c78d9aa706d3b113bce9c46f30d7d21715b23b1d")
	require.NoError(t, err)
	sender := crypto.PubkeyToAddress(key.PublicKey)
	leader := common.HexToAddress("0xaaaa000000000000000000000000000000000001")

	seedAcc := func(addr common.Address, bal *big.Int, nonce uint64) {
		as := mt_state.NewAccountState(addr)
		as.AddBalance(bal)
		as.SetNonce(nonce)
		bp.chainState.GetAccountStateDB().SetState(as)
	}
	seedAcc(sender, new(big.Int).Lsh(big.NewInt(1), 90), 1)
	seedAcc(leader, big.NewInt(0), 0)
	seedAcc(contractAddr, big.NewInt(0), 1)

	codeHash := crypto.Keccak256Hash(xapianPrecompileForwarderBytecode)
	contractState, err := bp.chainState.GetAccountStateDB().AccountState(contractAddr)
	require.NoError(t, err)
	contractState.SetCodeHash(codeHash)
	bp.chainState.GetAccountStateDB().SetState(contractState)
	bp.chainState.GetSmartContractDB().SetCode(contractAddr, codeHash, xapianPrecompileForwarderBytecode)
	bp.chainState.GetSmartContractDB().Commit()

	signer := eth.LatestSignerForChainID(big.NewInt(991))
	mkTx := func(nonce uint64, calldata []byte) types.Transaction {
		signed, signErr := eth.SignNewTx(key, signer, &eth.LegacyTx{
			Nonce:    nonce,
			To:       &contractAddr,
			Gas:      3_000_000,
			GasPrice: big.NewInt(100_000),
			Value:    big.NewInt(0),
			Data:     calldata,
		})
		require.NoError(t, signErr)
		tx, wrapErr := transaction.NewTransactionFromEth(signed)
		require.NoError(t, wrapErr)
		return tx
	}

	return bp, mkTx, leader
}

func runSingleBlockTxs(t *testing.T, cs *blockchain.ChainState, txs []types.Transaction, leader common.Address, blockNum uint64) tx_processor.ProcessResult {
	t.Helper()
	items := make([]grouptxns.Item, len(txs))
	for i, tx := range txs {
		items[i] = grouptxns.Item{
			ID:    i,
			Array: grouptxns.BuildDeterministicGroupAddrs(tx),
			Tx:    tx,
		}
	}
	groups := grouptxns.GroupTransactionsDeterministic(items, cs.HasCode)
	res, err := tx_processor.ProcessTransactions(context.Background(), cs, groups, false, true, 1000+blockNum, leader, blockNum, true)
	require.NoError(t, err)
	return res
}

// Kiểm tra persistBackupDbAsync chuẩn hóa địa chỉ EIP-55 ("0xAbCd...") và chữ thường không tiền tố ("abcd...")
// về cùng một chỉ mục full_db_logs_latest-<addr> và gộp nhiều frame DB theo đúng thứ tự xác định.
func TestPersistBackupDbAsync_NormalizesAndMergesLatestFullDbLogsIndex(t *testing.T) {
	bp, _ := readRouteHarness(t)

	eip55Addr := "0xAbCdEf1234567890AbCdEf1234567890AbCdEf12"
	lowerNoPrefix := "abcdef1234567890abcdef1234567890abcdef12"

	hdr := block.NewBlockHeader(common.Hash{}, 7, common.HexToHash("0x11"), common.HexToHash("0x22"), common.Hash{}, common.Address{}, 7000, common.Hash{}, 0, 7)
	blk := block.NewBlock(hdr, nil, nil)

	job := CommitJob{
		Block: blk,
		ProcessResults: &tx_processor.ProcessResult{
			FullDbLogs: []map[string][]byte{
				{lowerNoPrefix: []byte("frame_from_mvm|")},
				{eip55Addr: []byte("frame_from_export_all")},
			},
		},
	}

	bp.persistBackupDbAsync(job)

	// 1. Kiểm tra block_data_topic-7 được ghi hợp lệ
	rawBackup, err := bp.storageManager.GetStorageBackupDb().Get([]byte("block_data_topic-7"))
	require.NoError(t, err)
	deserBackup, err := storage.DeserializeBackupDb(rawBackup)
	require.NoError(t, err)
	require.Equal(t, uint64(7), deserBackup.BockNumber)
	require.Len(t, deserBackup.FullDbLogs, 2)

	// 2. Kiểm tra GetLatestFullDbLogsForAddress tra cứu bằng chữ thường không "0x" lấy được cả 2 frame đã gộp
	gotLatest, ok := storage.GetLatestFullDbLogsForAddress(bp.storageManager.GetStorageBackupDb(), lowerNoPrefix)
	require.True(t, ok, "GetLatestFullDbLogsForAddress phải tìm thấy key đã ghi từ ExportAllXapianLogs EIP-55")
	require.Equal(t, "frame_from_mvm|frame_from_export_all", string(gotLatest))
}

// Kiểm tra AbortAllSpeculative, CleanGEI và clearXapianBuffersForTxs xóa sạch tx_buffers trong C++ XapianRegistry
// để các giao dịch speculative bị hủy/quét không bao giờ rò rỉ dữ liệu hay ghim cứng XapianManager::is_evictable().
func TestSpeculativeExecutor_AbortAndCleanGEI_ClearBufferedXapianWrites(t *testing.T) {
	contractAddr := common.HexToAddress("0x1000000000000000000000000000000000000901")
	bp, mkTx, leader := setupProcessorXapianEnv(t, contractAddr)
	se := NewSpeculativeExecutor(bp)
	bp.speculativeExecutor = se

	// Bước khởi tạo DB ở Block 1
	txInit := mkTx(1, packXapianGetOrCreateDbV1(t, "db_spec_leak"))
	resInit := runSingleBlockTxs(t, bp.chainState, []types.Transaction{txInit}, leader, 1)
	require.Len(t, resInit.Receipts, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, resInit.Receipts[0].Status())
	mvm.CommitXapianTxBuffer(txInit.Hash().Bytes())
	mvm.CommitAllXapianAtBlock(1)

	// Case A: Giao dịch chạy speculative tạo tx_buffers, sau đó bị CleanGEI quét bỏ (IsFinished = true)
	csSpecClean, err := bp.chainState.CloneSpeculative(bp.GetLastBlock().Header())
	require.NoError(t, err)
	txClean := mkTx(2, packXapianNewDocV1(t, "db_spec_leak", []byte("must_be_swept_by_clean_gei")))
	resClean := runSingleBlockTxs(t, csSpecClean, []types.Transaction{txClean}, leader, 2)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, resClean.Receipts[0].Status())

	se.activeSessions.Store(uint64(2), &SpeculativeResult{
		GEI:         2,
		BlockNum:    2,
		Txs:         []types.Transaction{txClean},
		ClonedState: csSpecClean,
		IsFinished:  true,
	})
	se.CleanGEI(2)

	// Nếu tx_buffers chưa bị xóa, CommitXapianTxBuffer + ExportAllXapianLogs sẽ xuất ra log của txClean!
	mvm.CommitXapianTxBuffer(txClean.Hash().Bytes())
	require.Empty(t, mvm.ExportAllXapianLogs(), "CleanGEI bắt buộc phải xóa sạch Xapian tx_buffers của session bị quét")

	// Case B: Giao dịch chạy speculative tạo tx_buffers, sau đó bị AbortAllSpeculative hủy toàn cục
	csSpecAbort, err := bp.chainState.CloneSpeculative(bp.GetLastBlock().Header())
	require.NoError(t, err)
	txAbort := mkTx(2, packXapianNewDocV1(t, "db_spec_leak", []byte("must_be_wiped_by_abort_all")))
	resAbort := runSingleBlockTxs(t, csSpecAbort, []types.Transaction{txAbort}, leader, 2)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, resAbort.Receipts[0].Status())

	respCh := make(chan *pb.ExecuteBlockResponse, 1)
	se.activeSessions.Store(uint64(3), &SpeculativeResult{
		GEI:         3,
		BlockNum:    3,
		Txs:         []types.Transaction{txAbort},
		ClonedState: csSpecAbort,
		AuthRespCh:  respCh,
		IsFinished:  true,
	})
	se.AbortAllSpeculative()

	mvm.CommitXapianTxBuffer(txAbort.Hash().Bytes())
	require.Empty(t, mvm.ExportAllXapianLogs(), "AbortAllSpeculative bắt buộc phải xóa sạch toàn bộ Xapian tx_buffers")
}

// Kiểm tra createBlockFromResults khi phát hiện globalExecIndex <= lastCommittedGEI (alreadyCommitted = true)
// vừa chặn ghi đúp vào Xapian DB vừa xóa sạch tx_buffers dư thừa của lần thực thi trùng lặp.
func TestBlockProcessor_CreateBlockFromResults_AlreadyCommittedGEIClearsXapianBuffers(t *testing.T) {
	contractAddr := common.HexToAddress("0x1000000000000000000000000000000000000902")
	bp, mkTx, leader := setupProcessorXapianEnv(t, contractAddr)
	bp.commitChannel = make(chan CommitJob, 4)

	txInit := mkTx(1, packXapianGetOrCreateDbV1(t, "db_redundant_gei"))
	resInit := runSingleBlockTxs(t, bp.chainState, []types.Transaction{txInit}, leader, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, resInit.Receipts[0].Status())
	mvm.CommitXapianTxBuffer(txInit.Hash().Bytes())
	mvm.CommitAllXapianAtBlock(1)

	// Thực thi giao dịch ghi doc mới trên bản clone sạch (đẩy dữ liệu vào tx_buffers[txDup])
	csDup, err := bp.chainState.CloneSpeculative(bp.GetLastBlock().Header())
	require.NoError(t, err)
	txDup := mkTx(2, packXapianNewDocV1(t, "db_redundant_gei", []byte("redundant_gei_doc")))
	procRes := runSingleBlockTxs(t, csDup, []types.Transaction{txDup}, leader, 1)
	require.Equal(t, pb.RECEIPT_STATUS_RETURNED, procRes.Receipts[0].Status())

	bp.chainState.SetAccountStateDB(csDup.GetAccountStateDB())
	bp.chainState.SetSmartContractDB(csDup.GetSmartContractDB())
	bp.chainState.SetStakeStateDB(csDup.GetStakeStateDB())

	// Đánh dấu GEI=5 đã được commit trước đó để kích hoạt nhánh alreadyCommitted trong createBlockFromResults
	storage.ResetAllBlockCounters(0)
	storage.ForceSetLastGlobalExecIndex(5)

	blk := bp.createBlockFromResults(procRes, 1, 1, true, "DUP-TEST", 2000, 5, 1, nil, leader)
	require.NotNil(t, blk)
	require.Empty(t, procRes.FullDbLogs, "alreadyCommitted=true không được phép xuất lại FullDbLogs")

	// Chứng minh tx_buffers[txDup] đã bị xóa sạch chứ không bị bỏ quên trong RAM C++
	mvm.CommitXapianTxBuffer(txDup.Hash().Bytes())
	require.Empty(t, mvm.ExportAllXapianLogs(), "createBlockFromResults phải dọn sạch tx_buffers khi bỏ qua GEI đã commit")
}

