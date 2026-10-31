package tx_processor
import (
	"context"
	
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	eth "github.com/ethereum/go-ethereum/core/types"
	
	
	
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	
	"github.com/stretchr/testify/require"
)

func TestReviewV1DeleteThenAddTermSameTxPersistence(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	sharedContractV1 := common.HexToAddress("0x1000000000000000000000000000000000000188")
	forwardCodeV1 := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		512, 1024, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 1024, "RETURN",
	)
	seedContract(t, cs, sharedContractV1, big.NewInt(0), forwardCodeV1)

	// Block 1: Setup document with "v1_old_bytes"
	newDocData := makeXapianV1NewDocData(t, "v1_multi_db", []byte("v1_old_bytes"))
	signer := eth.LatestSignerForChainID(big.NewInt(991))
	signed0, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    1,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     newDocData,
	})
	require.NoError(t, err)
	tx0, err := transaction.NewTransactionFromEth(signed0)
	require.NoError(t, err)

	items0 := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx0), sharedContractV1), Tx: tx0}}
	_, r0, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items0, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r0, 1)
	require.Equal(t, 0, int(r0[0].Status()))
	require.Len(t, r0[0].Return(), 32, "setup transaction must return 32-byte docId")
	docId := new(big.Int).SetBytes(r0[0].Return())
	require.False(t, docId.Sign() == 0, "docId must be non-zero")

	// Commit Block 1 to disk
	mvm.CommitXapianTxBuffer(tx0.Hash().Bytes())
	mvm.DestroyAllXapianInstances()

	// Block 2: Intra-block multi-writer scenario on 0x107
	deleteABI, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"deleteDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"uint256"}]}]`))
 require.NoError(t, err)
 setDataData, err := deleteABI.Pack("deleteDocument", "v1_multi_db", docId)
 require.NoError(t, err)
	addTermData := makeXapianV1AddTermData(t, "v1_multi_db", docId, "v1_tag_chain")
	getDataData := makeXapianV1GetDataData(t, "v1_multi_db", docId)
 normal := confScript("CALLDATASIZE", 0, 0, "CALLDATACOPY", 512, 1024, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 60000, "CALL", "POP", "RETURNDATASIZE", 1024, "RETURN")
 prefix := confScript("CALLDATASIZE", []byte{0x15}, 0, "JUMPI")
 prefix = confScript("CALLDATASIZE", []byte{0x15}, len(prefix)+len(normal), "JUMPI")
 special := confScript([]byte{0x5b})
 special = append(special, confScript(confMemWrite(0, setDataData)...)...)
 special = append(special, confScript(32, 1024, len(setDataData), 0, 0, common.HexToAddress("0x107"), 60000, "CALL", "POP")...)
 special = append(special, confScript(confMemWrite(0, addTermData)...)...)
 special = append(special, confScript(32, 1024, len(addTermData), 0, 0, common.HexToAddress("0x107"), 60000, "CALL", "POP", "STOP")...)
 runtime := append(prefix, normal...)
 runtime = append(runtime, special...)
 seedContract(t, cs, sharedContractV1, big.NewInt(0), runtime)


	signedA, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    2,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     nil,
	})
	require.NoError(t, err)
	txA, err := transaction.NewTransactionFromEth(signedA)
	require.NoError(t, err)

	signedB, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    3,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     getDataData,
	})
	require.NoError(t, err)
	txB, err := transaction.NewTransactionFromEth(signedB)
	require.NoError(t, err)

	signedC, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    4,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     getDataData,
	})
	require.NoError(t, err)
	txC, err := transaction.NewTransactionFromEth(signedC)
	require.NoError(t, err)

	items2 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txA), sharedContractV1), Tx: txA},
		{ID: 1, Array: append(grouptxns.BuildDeterministicGroupAddrs(txB), sharedContractV1), Tx: txB},
		{ID: 2, Array: append(grouptxns.BuildDeterministicGroupAddrs(txC), sharedContractV1), Tx: txC},
	}

	_, r2, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r2, 3)
	require.Equal(t, 0, int(r2[0].Status()), "Tx A must succeed")
	require.Equal(t, 0, int(r2[1].Status()), "Tx B must succeed")
	require.Equal(t, 0, int(r2[2].Status()), "Tx C must succeed")

	// Tx C must return []byte("v1_new_bytes")
	getDataAbi, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"bytes"}]}]`))
	require.NoError(t, err)
	var returnedBytes []byte
	err = getDataAbi.UnpackIntoInterface(&returnedBytes, "getDataDocument", r2[2].Return())
	require.NoError(t, err)
	require.Empty(t, returnedBytes, "Deleted content must not survive in overlay")
 t.Logf("before commit content=%q", returnedBytes)

	// Commit Block 2
	mvm.CommitXapianTxBufferBatch([][]byte{
		txA.Hash().Bytes(),
		txB.Hash().Bytes(),
		txC.Hash().Bytes(),
	})

	// Destroy in-memory instances to force fresh reload from disk
	mvm.DestroyAllXapianInstances()

	// Block 3: Verify disk persistence on 0x107
	signedD, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    5,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     getDataData,
	})
	require.NoError(t, err)
	txD, err := transaction.NewTransactionFromEth(signedD)
	require.NoError(t, err)

	items3 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txD), sharedContractV1), Tx: txD},
	}
	_, r3, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items3, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r3, 1)
	require.Equal(t, 0, int(r3[0].Status()))

	var diskBytes []byte
	err = getDataAbi.UnpackIntoInterface(&diskBytes, "getDataDocument", r3[0].Return())
	require.NoError(t, err)
	t.Logf("after commit content=%q", diskBytes)
	require.Equal(t, returnedBytes, diskBytes, "Commit must preserve the content observed during execution")

	mvm.CommitXapianTxBuffer(txD.Hash().Bytes())
}

func TestReviewV1DeleteThenSetDataSameTxPersistence(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	sharedContractV1 := common.HexToAddress("0x1000000000000000000000000000000000000188")
	forwardCodeV1 := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		512, 1024, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 1024, "RETURN",
	)
	seedContract(t, cs, sharedContractV1, big.NewInt(0), forwardCodeV1)

	// Block 1: Setup document with "v1_old_bytes"
	newDocData := makeXapianV1NewDocData(t, "v1_multi_db", []byte("v1_old_bytes"))
	signer := eth.LatestSignerForChainID(big.NewInt(991))
	signed0, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce:    1,
		To:       &sharedContractV1,
		Gas:      3000000,
		GasPrice: big.NewInt(100000),
		Value:    big.NewInt(0),
		Data:     newDocData,
	})
	require.NoError(t, err)
	tx0, err := transaction.NewTransactionFromEth(signed0)
	require.NoError(t, err)

	items0 := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx0), sharedContractV1), Tx: tx0}}
	_, r0, _, _ := ProcessTransactionsOptimistic(
		context.Background(),
		cs,
		grouptxns.GroupTransactionsDeterministic(items0, cs.HasCode),
		blankHeader(),
		false,
		false,
		999,
		leader,
		false,
	)
	require.Len(t, r0, 1)
	require.Equal(t, 0, int(r0[0].Status()))
	docId := new(big.Int).SetBytes(r0[0].Return())

	mvm.CommitXapianTxBuffer(tx0.Hash().Bytes())
	mvm.DestroyAllXapianInstances()

	// Block 2: Same tx executes deleteDocument then setDataDocument("v1_brand_new_bytes")
	deleteABI, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"deleteDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"uint256"}]}]`))
	require.NoError(t, err)
	delData, err := deleteABI.Pack("deleteDocument", "v1_multi_db", docId)
	require.NoError(t, err)
	setDataBytes := makeXapianV1SetDataData(t, "v1_multi_db", docId, []byte("v1_brand_new_bytes"))
	getDataData := makeXapianV1GetDataData(t, "v1_multi_db", docId)

	normal := confScript("CALLDATASIZE", 0, 0, "CALLDATACOPY", 512, 1024, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 60000, "CALL", "POP", "RETURNDATASIZE", 1024, "RETURN")
	prefix := confScript("CALLDATASIZE", []byte{0x15}, 0, "JUMPI")
	prefix = confScript("CALLDATASIZE", []byte{0x15}, len(prefix)+len(normal), "JUMPI")
	special := confScript([]byte{0x5b})
	special = append(special, confScript(confMemWrite(0, delData)...)...)
	special = append(special, confScript(32, 1024, len(delData), 0, 0, common.HexToAddress("0x107"), 60000, "CALL", "POP")...)
	special = append(special, confScript(confMemWrite(0, setDataBytes)...)...)
	special = append(special, confScript(32, 1024, len(setDataBytes), 0, 0, common.HexToAddress("0x107"), 60000, "CALL", "POP", "STOP")...)
	runtime := append(prefix, normal...)
	runtime = append(runtime, special...)
	seedContract(t, cs, sharedContractV1, big.NewInt(0), runtime)

	signedA, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 2, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: nil,
	})
	require.NoError(t, err)
	txA, err := transaction.NewTransactionFromEth(signedA)
	require.NoError(t, err)

	signedB, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 3, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: getDataData,
	})
	require.NoError(t, err)
	txB, err := transaction.NewTransactionFromEth(signedB)
	require.NoError(t, err)

	items2 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txA), sharedContractV1), Tx: txA},
		{ID: 1, Array: append(grouptxns.BuildDeterministicGroupAddrs(txB), sharedContractV1), Tx: txB},
	}
	_, r2, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode),
		blankHeader(), false, false, 999, leader, false,
	)
	require.Len(t, r2, 2)
	require.Equal(t, 0, int(r2[0].Status()))
	require.Equal(t, 0, int(r2[1].Status()))

	getDataAbi, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"bytes"}]}]`))
	require.NoError(t, err)
	var returnedBytes []byte
	err = getDataAbi.UnpackIntoInterface(&returnedBytes, "getDataDocument", r2[1].Return())
	require.NoError(t, err)
	require.Equal(t, []byte("v1_brand_new_bytes"), returnedBytes)

	// Commit Block 2 & Destroy instances
	mvm.CommitXapianTxBufferBatch([][]byte{txA.Hash().Bytes(), txB.Hash().Bytes()})
	mvm.DestroyAllXapianInstances()

	// Block 3: Verify disk persistence
	signedC, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 4, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: getDataData,
	})
	require.NoError(t, err)
	txC, err := transaction.NewTransactionFromEth(signedC)
	require.NoError(t, err)

	items3 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txC), sharedContractV1), Tx: txC},
	}
	_, r3, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items3, cs.HasCode),
		blankHeader(), false, false, 999, leader, false,
	)
	require.Len(t, r3, 1)
	require.Equal(t, 0, int(r3[0].Status()))

	var diskBytes []byte
	err = getDataAbi.UnpackIntoInterface(&diskBytes, "getDataDocument", r3[0].Return())
	require.NoError(t, err)
	require.Equal(t, []byte("v1_brand_new_bytes"), diskBytes, "Post-commit must match pre-commit brand new data")

	mvm.CommitXapianTxBuffer(txC.Hash().Bytes())
}

func TestReviewV1DeleteThenAddTermThenDeleteSameTxPersistence(t *testing.T) {
	cs, key, _, leader := setupXapianChainEnv(t)

	sharedContractV1 := common.HexToAddress("0x1000000000000000000000000000000000000188")
	forwardCodeV1 := confScript(
		"CALLDATASIZE", 0, 0, "CALLDATACOPY",
		512, 1024, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 100000, "CALL", "POP",
		"RETURNDATASIZE", 1024, "RETURN",
	)
	seedContract(t, cs, sharedContractV1, big.NewInt(0), forwardCodeV1)

	// Block 1: Setup document with "v1_old_bytes"
	newDocData := makeXapianV1NewDocData(t, "v1_multi_db", []byte("v1_old_bytes"))
	signer := eth.LatestSignerForChainID(big.NewInt(991))
	signed0, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 1, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: newDocData,
	})
	require.NoError(t, err)
	tx0, err := transaction.NewTransactionFromEth(signed0)
	require.NoError(t, err)

	items0 := []grouptxns.Item{{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(tx0), sharedContractV1), Tx: tx0}}
	_, r0, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items0, cs.HasCode),
		blankHeader(), false, false, 999, leader, false,
	)
	require.Len(t, r0, 1)
	require.Equal(t, 0, int(r0[0].Status()))
	docId := new(big.Int).SetBytes(r0[0].Return())

	mvm.CommitXapianTxBuffer(tx0.Hash().Bytes())
	mvm.DestroyAllXapianInstances()

	// Block 2: Same tx executes deleteDocument -> addTermDocument -> deleteDocument
	deleteABI, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"deleteDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"uint256"}]}]`))
	require.NoError(t, err)
	delData, err := deleteABI.Pack("deleteDocument", "v1_multi_db", docId)
	require.NoError(t, err)
	addTermData := makeXapianV1AddTermData(t, "v1_multi_db", docId, "v1_temp_tag")
	getDataData := makeXapianV1GetDataData(t, "v1_multi_db", docId)

	normal := confScript("CALLDATASIZE", 0, 0, "CALLDATACOPY", 512, 1024, "CALLDATASIZE", 0, 0, common.HexToAddress("0x107"), 60000, "CALL", "POP", "RETURNDATASIZE", 1024, "RETURN")
	prefix := confScript("CALLDATASIZE", []byte{0x15}, 0, "JUMPI")
	prefix = confScript("CALLDATASIZE", []byte{0x15}, len(prefix)+len(normal), "JUMPI")
	special := confScript([]byte{0x5b})
	special = append(special, confScript(confMemWrite(0, delData)...)...)
	special = append(special, confScript(32, 1024, len(delData), 0, 0, common.HexToAddress("0x107"), 60000, "CALL", "POP")...)
	special = append(special, confScript(confMemWrite(0, addTermData)...)...)
	special = append(special, confScript(32, 1024, len(addTermData), 0, 0, common.HexToAddress("0x107"), 60000, "CALL", "POP")...)
	special = append(special, confScript(confMemWrite(0, delData)...)...)
	special = append(special, confScript(32, 1024, len(delData), 0, 0, common.HexToAddress("0x107"), 60000, "CALL", "POP", "STOP")...)
	runtime := append(prefix, normal...)
	runtime = append(runtime, special...)
	seedContract(t, cs, sharedContractV1, big.NewInt(0), runtime)

	signedA, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 2, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: nil,
	})
	require.NoError(t, err)
	txA, err := transaction.NewTransactionFromEth(signedA)
	require.NoError(t, err)

	signedB, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 3, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: getDataData,
	})
	require.NoError(t, err)
	txB, err := transaction.NewTransactionFromEth(signedB)
	require.NoError(t, err)

	items2 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txA), sharedContractV1), Tx: txA},
		{ID: 1, Array: append(grouptxns.BuildDeterministicGroupAddrs(txB), sharedContractV1), Tx: txB},
	}
	_, r2, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items2, cs.HasCode),
		blankHeader(), false, false, 999, leader, false,
	)
	require.Len(t, r2, 2)
	require.Equal(t, 0, int(r2[0].Status()))
	require.Equal(t, 0, int(r2[1].Status()))

	getDataAbi, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getDataDocument","inputs":[{"name":"dbname","type":"string"},{"name":"docId","type":"uint256"}],"outputs":[{"type":"bytes"}]}]`))
	require.NoError(t, err)
	var returnedBytes []byte
	err = getDataAbi.UnpackIntoInterface(&returnedBytes, "getDataDocument", r2[1].Return())
	require.NoError(t, err)
	require.Empty(t, returnedBytes, "Pre-commit read of deleted document must be empty")

	// Commit Block 2 & Destroy instances
	mvm.CommitXapianTxBufferBatch([][]byte{txA.Hash().Bytes(), txB.Hash().Bytes()})
	mvm.DestroyAllXapianInstances()

	// Block 3: Verify disk persistence (document must remain deleted)
	signedC, err := eth.SignNewTx(key, signer, &eth.LegacyTx{
		Nonce: 4, To: &sharedContractV1, Gas: 3000000, GasPrice: big.NewInt(100000), Value: big.NewInt(0), Data: getDataData,
	})
	require.NoError(t, err)
	txC, err := transaction.NewTransactionFromEth(signedC)
	require.NoError(t, err)

	items3 := []grouptxns.Item{
		{ID: 0, Array: append(grouptxns.BuildDeterministicGroupAddrs(txC), sharedContractV1), Tx: txC},
	}
	_, r3, _, _ := ProcessTransactionsOptimistic(
		context.Background(), cs, grouptxns.GroupTransactionsDeterministic(items3, cs.HasCode),
		blankHeader(), false, false, 999, leader, false,
	)
	require.Len(t, r3, 1)
	require.Equal(t, 0, int(r3[0].Status()))

	var diskBytes []byte
	err = getDataAbi.UnpackIntoInterface(&diskBytes, "getDataDocument", r3[0].Return())
	require.NoError(t, err)
	require.Empty(t, diskBytes, "Post-commit read of document deleted after addTerm must still be empty")

	mvm.CommitXapianTxBuffer(txC.Hash().Bytes())
}

