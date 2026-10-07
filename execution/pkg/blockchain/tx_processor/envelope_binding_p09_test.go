package tx_processor

import (
	"bytes"
	"crypto/sha256"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/holiman/uint256"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// createSignedEIP1559Tx creates a genuine signed EIP-1559 Ethereum transaction with rich fields
// (AccessList, CallData, GasFeeCap, GasTipCap) converted to MetaNode types.Transaction.
func createSignedEIP1559Tx(t *testing.T, chainID int64, nonce uint64) (types.Transaction, *e_types.Transaction, common.Address) {
	t.Helper()
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(privKey.PublicKey)
	to := common.HexToAddress("0x7890123456789012345678901234567890123456")

	signer := e_types.LatestSignerForChainID(big.NewInt(chainID))
	txData := &e_types.DynamicFeeTx{
		ChainID:   big.NewInt(chainID),
		Nonce:     nonce,
		GasTipCap: big.NewInt(1_000_000),
		GasFeeCap: big.NewInt(int64(p_common.MINIMUM_BASE_FEE * 2)),
		Gas:       p_common.TRANSFER_GAS_COST * 2,
		To:        &to,
		Value:     big.NewInt(1_000_000_000),
		Data:      []byte("p09-payload-test-data"),
		AccessList: e_types.AccessList{
			{
				Address: common.HexToAddress("0x1111111111111111111111111111111111111111"),
				StorageKeys: []common.Hash{
					common.HexToHash("0xaaaa"),
					common.HexToHash("0xbbbb"),
				},
			},
		},
	}
	ethTx, err := e_types.SignNewTx(privKey, signer, txData)
	require.NoError(t, err)

	metaTx, err := transaction.NewTransactionFromEth(ethTx)
	require.NoError(t, err)

	return metaTx, ethTx, from
}

// createSignedBlobTx creates a signed EIP-4844 Blob transaction for testing BlobVersionedHashes / MaxFeePerBlobGas.
func createSignedBlobTx(t *testing.T, chainID int64, nonce uint64) (types.Transaction, *e_types.Transaction, common.Address) {
	t.Helper()
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(privKey.PublicKey)
	to := common.HexToAddress("0x7890123456789012345678901234567890123456")

	var blob kzg4844.Blob
	commitment, err := kzg4844.BlobToCommitment(&blob)
	require.NoError(t, err)
	proof, err := kzg4844.ComputeBlobProof(&blob, commitment)
	require.NoError(t, err)
	versionedHash := common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &commitment))

	signer := e_types.LatestSignerForChainID(big.NewInt(chainID))
	txData := &e_types.BlobTx{
		ChainID:    uint256.NewInt(uint64(chainID)),
		Nonce:      nonce,
		GasTipCap:  uint256.NewInt(1_000_000),
		GasFeeCap:  uint256.NewInt(uint64(p_common.MINIMUM_BASE_FEE * 2)),
		Gas:        p_common.TRANSFER_GAS_COST * 2,
		To:         to,
		Value:      uint256.NewInt(100),
		Data:       []byte("blob-tx-data"),
		BlobFeeCap: uint256.NewInt(20_000_000),
		BlobHashes: []common.Hash{versionedHash},
		Sidecar: &e_types.BlobTxSidecar{
			Blobs:       []kzg4844.Blob{blob},
			Commitments: []kzg4844.Commitment{commitment},
			Proofs:      []kzg4844.Proof{proof},
		},
	}
	ethTx, err := e_types.SignNewTx(privKey, signer, txData)
	require.NoError(t, err)

	metaTx, err := transaction.NewTransactionFromEth(ethTx)
	require.NoError(t, err)

	return metaTx, ethTx, from
}

// createSignedSetCodeTx creates a signed EIP-7702 SetCode transaction for testing AuthorizationList.
func createSignedSetCodeTx(t *testing.T, chainID int64, nonce uint64) (types.Transaction, *e_types.Transaction, common.Address) {
	t.Helper()
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(privKey.PublicKey)
	to := common.HexToAddress("0x7890123456789012345678901234567890123456")

	authPrivKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	authTarget := common.HexToAddress("0x2222222222222222222222222222222222222222")

	auth, err := e_types.SignSetCode(authPrivKey, e_types.SetCodeAuthorization{
		ChainID: *uint256.NewInt(uint64(chainID)),
		Address: authTarget,
		Nonce:   0,
	})
	require.NoError(t, err)

	signer := e_types.LatestSignerForChainID(big.NewInt(chainID))
	txData := &e_types.SetCodeTx{
		ChainID:   uint256.NewInt(uint64(chainID)),
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(1_000_000),
		GasFeeCap: uint256.NewInt(uint64(p_common.MINIMUM_BASE_FEE * 2)),
		Gas:       p_common.TRANSFER_GAS_COST * 2,
		To:        to,
		Value:     uint256.NewInt(50),
		Data:      []byte("set-code-data"),
		AuthList:  []e_types.SetCodeAuthorization{auth},
	}
	ethTx, err := e_types.SignNewTx(privKey, signer, txData)
	require.NoError(t, err)

	metaTx, err := transaction.NewTransactionFromEth(ethTx)
	require.NoError(t, err)

	return metaTx, ethTx, from
}

// createSignedDeployTx creates a signed contract deployment transaction (To == nil) for testing DeployData.
func createSignedDeployTx(t *testing.T, chainID int64, nonce uint64) (types.Transaction, *e_types.Transaction, common.Address) {
	t.Helper()
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(privKey.PublicKey)

	signer := e_types.LatestSignerForChainID(big.NewInt(chainID))
	txData := &e_types.DynamicFeeTx{
		ChainID:   big.NewInt(chainID),
		Nonce:     nonce,
		GasTipCap: big.NewInt(1_000_000),
		GasFeeCap: big.NewInt(int64(p_common.MINIMUM_BASE_FEE * 2)),
		Gas:       100_000,
		To:        nil, // Contract creation
		Value:     big.NewInt(0),
		Data:      []byte{0x60, 0x80, 0x60, 0x40, 0x52, 0x34, 0x80, 0x15, 0x60, 0x0f}, // bytecode
	}
	ethTx, err := e_types.SignNewTx(privKey, signer, txData)
	require.NoError(t, err)

	metaTx, err := transaction.NewTransactionFromEth(ethTx)
	require.NoError(t, err)

	return metaTx, ethTx, from
}

// cloneProto returns an exact copy of a protobuf Transaction message.
func cloneProto(p *pb.Transaction) *pb.Transaction {
	if p == nil {
		return nil
	}
	return proto.Clone(p).(*pb.Transaction)
}

// TestP09_TableDriven_MutatedFieldsRejected verifies that mutating ANY single execution field
// while retaining the valid RawEnvelope causes rejection at BOTH mempool admission (VerifyTransaction)
// AND block execution (FilterInvalidSignatures / BlockSTM path).
func TestP09_TableDriven_MutatedFieldsRejected(t *testing.T) {
	cs := setupTestChainState(t)
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")

	chainID := int64(1)
	baseMetaTx, baseEthTx, senderAddr := createSignedEIP1559Tx(t, chainID, 1)

	// Fund sender in ChainState
	as := state.NewAccountState(senderAddr)
	as.AddBalance(big.NewInt(1_000_000_000_000_000))
	as.SetNonce(1)
	cs.GetAccountStateDB().SetState(as)

	// Setup recipient contract state so IsCallContract() doesn't fail
	toAddr := common.HexToAddress("0x7890123456789012345678901234567890123456")
	toAcc := state.NewAccountState(toAddr)
	toAcc.SetSmartContractState(state.NewSmartContractState(
		p_common.PubkeyFromBytes(make([]byte, 48)),
		toAddr,
		common.HexToHash("0xae77"),
		common.HexToHash("0xc880"),
		common.HexToHash("0x1234"),
	))
	cs.GetAccountStateDB().SetState(toAcc)

	// 1. Sanity: genuine transaction passes all checks and has canonical hash matching go-ethereum
	require.NoError(t, transaction.ValidateEnvelopeBinding(baseMetaTx))
	require.True(t, baseMetaTx.ValidEthSign())
	assert.Equal(t, baseEthTx.Hash(), baseMetaTx.Hash(), "canonical hash must equal go-ethereum tx.Hash()")
	assert.Nil(t, VerifyTransaction(baseMetaTx, cs, nil), "genuine tx must be accepted at admission")

	filtered := FilterInvalidSignatures(cs, groupsOf(baseMetaTx))
	require.Len(t, filtered, 1, "genuine tx must pass block execution filter")
	require.Equal(t, baseMetaTx.Hash(), filtered[0].Items[0].Tx.Hash())

	baseProto := baseMetaTx.Proto().(*pb.Transaction)

	// Also generate blob and setcode txs for fields 23, 24, 26
	blobMetaTx, _, blobSender := createSignedBlobTx(t, chainID, 1)
	asBlob := state.NewAccountState(blobSender)
	asBlob.AddBalance(big.NewInt(1_000_000_000_000_000))
	asBlob.SetNonce(1)
	cs.GetAccountStateDB().SetState(asBlob)
	blobProto := blobMetaTx.Proto().(*pb.Transaction)

	setCodeMetaTx, _, setCodeSender := createSignedSetCodeTx(t, chainID, 1)
	asSetCode := state.NewAccountState(setCodeSender)
	asSetCode.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSetCode.SetNonce(1)
	cs.GetAccountStateDB().SetState(asSetCode)
	setCodeProto := setCodeMetaTx.Proto().(*pb.Transaction)

	deployMetaTx, _, deploySender := createSignedDeployTx(t, chainID, 1)
	asDeploy := state.NewAccountState(deploySender)
	asDeploy.AddBalance(big.NewInt(1_000_000_000_000_000))
	asDeploy.SetNonce(1)
	cs.GetAccountStateDB().SetState(asDeploy)
	deployProto := deployMetaTx.Proto().(*pb.Transaction)

	testCases := []struct {
		name       string
		baseProto  *pb.Transaction
		mutate     func(p *pb.Transaction)
		senderAcc  common.Address
	}{
		{
			name:      "mutate FromAddress",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.FromAddress = common.HexToAddress("0xbadbadbadbadbadbadbadbadbadbadbadbad0001").Bytes()
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate ToAddress",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.ToAddress = common.HexToAddress("0xbadbadbadbadbadbadbadbadbadbadbadbad0002").Bytes()
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate Amount",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.Amount = big.NewInt(999_999_999_999_999).Bytes()
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate Nonce",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.Nonce = []byte{0, 0, 0, 0, 0, 0, 0, 99}
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate MaxGas",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.MaxGas = 999_999
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate MaxGasPrice",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.MaxGasPrice = 555_555
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate GasFeeCap",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.GasFeeCap = big.NewInt(999_999_999).Bytes()
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate GasTipCap",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.GasTipCap = big.NewInt(888_888_888).Bytes()
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate Data (CallData)",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.Data = []byte("tampered-data-content")
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate Data (DeployData)",
			baseProto: deployProto,
			mutate: func(p *pb.Transaction) {
				p.Data = []byte("tampered-deploy-code")
			},
			senderAcc: deploySender,
		},
		{
			name:      "mutate Type",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.Type = 0 // change from type 2 to type 0
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate ChainID",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.ChainID = 9999
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate R",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.R = bytes.Repeat([]byte{0xaa}, 32)
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate S",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.S = bytes.Repeat([]byte{0xbb}, 32)
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate V",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.V = []byte{0x05}
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate AccessList",
			baseProto: baseProto,
			mutate: func(p *pb.Transaction) {
				p.AccessList = []*pb.AccessTuple{
					{
						Address: common.HexToAddress("0xbad0bad0bad0bad0bad0bad0bad0bad0bad0bad0").Bytes(),
					},
				}
			},
			senderAcc: senderAddr,
		},
		{
			name:      "mutate BlobVersionedHashes",
			baseProto: blobProto,
			mutate: func(p *pb.Transaction) {
				p.BlobVersionedHashes = [][]byte{common.HexToHash("0xbad0000000000000000000000000000000000000000000000000000000000001").Bytes()}
			},
			senderAcc: blobSender,
		},
		{
			name:      "mutate MaxFeePerBlobGas",
			baseProto: blobProto,
			mutate: func(p *pb.Transaction) {
				p.MaxFeePerBlobGas = big.NewInt(999_999_999).Bytes()
			},
			senderAcc: blobSender,
		},
		{
			name:      "mutate AuthorizationList",
			baseProto: setCodeProto,
			mutate: func(p *pb.Transaction) {
				p.AuthorizationList = []*pb.SetCodeAuthorization{
					{
						ChainID: 1,
						Address: common.HexToAddress("0xbad0bad0bad0bad0bad0bad0bad0bad0bad0bad0").Bytes(),
						Nonce:   99,
					},
				}
			},
			senderAcc: setCodeSender,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mutatedProto := cloneProto(tc.baseProto)
			tc.mutate(mutatedProto)

			mutatedTx := transaction.TransactionFromProto(mutatedProto)

			// 1. ValidateEnvelopeBinding must fail
			err := transaction.ValidateEnvelopeBinding(mutatedTx)
			assert.Error(t, err, "ValidateEnvelopeBinding must fail for mutated field")

			// 2. ValidEthSign must return false
			assert.False(t, mutatedTx.ValidEthSign(), "ValidEthSign must return false for mutated field")

			// 3. Admission gate VerifyTransaction must reject
			verr := VerifyTransaction(mutatedTx, cs, nil)
			assert.NotNil(t, verr, "admission VerifyTransaction must reject mutated transaction")

			// 4. Block execution filter FilterInvalidSignatures must drop the transaction
			droppedGroups := FilterInvalidSignatures(cs, groupsOf(mutatedTx))
			assert.Empty(t, droppedGroups, "BlockSTM FilterInvalidSignatures must drop mutated transaction")
		})
	}
}

// TestP09_CacheRegression_ForgedTxCannotRideOnGenuineCache verifies that once a genuine transaction
// is verified and stored in signature cache, a forged variant with the same RawEnvelope CANNOT
// ride on the genuine cache entry and is still rejected at both admission and execution.
func TestP09_CacheRegression_ForgedTxCannotRideOnGenuineCache(t *testing.T) {
	cs := setupTestChainState(t)
	t.Setenv("SKIP_MEMPOOL_SIG_VERIFY", "false")

	chainID := int64(1)
	genuineTx, _, senderAddr := createSignedEIP1559Tx(t, chainID, 1)

	as := state.NewAccountState(senderAddr)
	as.AddBalance(big.NewInt(1_000_000_000_000_000))
	as.SetNonce(1)
	cs.GetAccountStateDB().SetState(as)

	toAddr := common.HexToAddress("0x7890123456789012345678901234567890123456")
	toAcc := state.NewAccountState(toAddr)
	toAcc.SetSmartContractState(state.NewSmartContractState(
		p_common.PubkeyFromBytes(make([]byte, 48)),
		toAddr,
		common.HexToHash("0xae77"),
		common.HexToHash("0xc880"),
		common.HexToHash("0x1234"),
	))
	cs.GetAccountStateDB().SetState(toAcc)

	// Step 1: Verify and execute genuine transaction -> populates sigCache
	require.Nil(t, VerifyTransaction(genuineTx, cs, nil))
	kept := FilterInvalidSignatures(cs, groupsOf(genuineTx))
	require.Len(t, kept, 1)

	// Step 2: Create forged variant (tamper ToAddress and Amount while keeping RawEnvelope)
	genuineProto := genuineTx.Proto().(*pb.Transaction)
	forgedProto := cloneProto(genuineProto)
	forgedProto.ToAddress = common.HexToAddress("0x9999999999999999999999999999999999999999").Bytes()
	forgedProto.Amount = big.NewInt(888_888_888).Bytes()
	forgedTx := transaction.TransactionFromProto(forgedProto)

	// The hash of forgedTx is still keccak(RawEnvelope), identical to genuineTx
	require.Equal(t, genuineTx.Hash(), forgedTx.Hash(), "hash is keccak256(RawEnvelope)")

	// Step 3: Verify that forgedTx does NOT hit cache and is rejected at admission
	assert.NotNil(t, VerifyTransaction(forgedTx, cs, nil), "forged tx must be rejected at admission despite genuine tx in cache")

	// Step 4: Verify that forgedTx is dropped by FilterInvalidSignatures
	dropped := FilterInvalidSignatures(cs, groupsOf(forgedTx))
	assert.Empty(t, dropped, "forged tx must be dropped at block execution despite genuine tx in cache")
}

// FuzzEnvelopeBindingMutations tests that arbitrary mutations to protobuf fields
// while preserving a valid RawEnvelope are never accepted as valid.
func FuzzEnvelopeBindingMutations(f *testing.F) {
	chainID := int64(1)
	key, err := crypto.GenerateKey()
	if err != nil {
		f.Fatal(err)
	}
	to := common.HexToAddress("0x3333333333333333333333333333333333333333")
	signer := e_types.LatestSignerForChainID(big.NewInt(chainID))
	ethTx, err := e_types.SignNewTx(key, signer, &e_types.DynamicFeeTx{
		ChainID:   big.NewInt(chainID),
		Nonce:     1,
		GasTipCap: big.NewInt(1000),
		GasFeeCap: big.NewInt(200000),
		Gas:       21000,
		To:        &to,
		Value:     big.NewInt(500),
		Data:      []byte("fuzz-seed-data"),
	})
	if err != nil {
		f.Fatal(err)
	}
	baseTx, err := transaction.NewTransactionFromEth(ethTx)
	if err != nil {
		f.Fatal(err)
	}
	rawEnvelope := baseTx.RawEnvelope()

	f.Add([]byte("bad-to-addr"), []byte("99999"), uint64(10), uint64(50000))

	f.Fuzz(func(t *testing.T, mutTo []byte, mutAmount []byte, mutNonce uint64, mutGas uint64) {
		p := &pb.Transaction{
			RawEnvelope: rawEnvelope,
			ToAddress:   mutTo,
			Amount:      mutAmount,
			MaxGas:      mutGas,
		}
		// If fields do not match canonical values derived from rawEnvelope,
		// ValidateProtoEnvelopeBinding MUST reject.
		err := transaction.ValidateProtoEnvelopeBinding(p)
		if err == nil {
			// If it somehow passed, verify all fields really matched
			canonicalEthTx := new(e_types.Transaction)
			if uerr := canonicalEthTx.UnmarshalBinary(rawEnvelope); uerr == nil {
				canonicalPb := &pb.Transaction{}
				if cerr := transaction.FromEthTransaction(canonicalEthTx, canonicalPb); cerr == nil {
					assert.True(t, bytes.Equal(p.ToAddress, canonicalPb.ToAddress))
					assert.True(t, bytes.Equal(p.Amount, canonicalPb.Amount))
					assert.Equal(t, p.MaxGas, canonicalPb.MaxGas)
				}
			}
		} else {
			assert.Error(t, err)
		}
	})
}

// TestP09_CrossGeth_Parity verifies that for all supported Ethereum transaction types
// (Type 0 Legacy, Type 1 AccessList, Type 2 DynamicFee, Type 3 BlobTx, Type 4 SetCodeTx),
// the converted MetaNode transaction produces the exact same hash as go-ethereum (tx.Hash()),
// passes ValidateEnvelopeBinding, and verifies ValidEthSign deterministically.
func TestP09_CrossGeth_Parity(t *testing.T) {
	chainID := int64(1)
	privKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(privKey.PublicKey)
	to := common.HexToAddress("0x9876543210987654321098765432109876543210")
	signer := e_types.LatestSignerForChainID(big.NewInt(chainID))

	// Type 0: Legacy
	tx0, err := e_types.SignNewTx(privKey, signer, &e_types.LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(20_000_000_000),
		Gas:      21000,
		To:       &to,
		Value:    big.NewInt(1000),
		Data:     nil,
	})
	require.NoError(t, err)

	// Type 1: AccessList (EIP-2930)
	tx1, err := e_types.SignNewTx(privKey, signer, &e_types.AccessListTx{
		ChainID:  big.NewInt(chainID),
		Nonce:    2,
		GasPrice: big.NewInt(20_000_000_000),
		Gas:      25000,
		To:       &to,
		Value:    big.NewInt(2000),
		Data:     []byte("access-list-tx"),
		AccessList: e_types.AccessList{
			{Address: to, StorageKeys: []common.Hash{common.HexToHash("0x1234")}},
		},
	})
	require.NoError(t, err)

	// Type 2: DynamicFee (EIP-1559)
	tx2, err := e_types.SignNewTx(privKey, signer, &e_types.DynamicFeeTx{
		ChainID:   big.NewInt(chainID),
		Nonce:     3,
		GasTipCap: big.NewInt(1_000_000),
		GasFeeCap: big.NewInt(int64(p_common.MINIMUM_BASE_FEE * 2)),
		Gas:       30000,
		To:        &to,
		Value:     big.NewInt(3000),
		Data:      []byte("dynamic-fee-tx"),
	})
	require.NoError(t, err)

	// Type 3: BlobTx (EIP-4844)
	metaBlob, tx3, _ := createSignedBlobTx(t, chainID, 4)

	// Type 4: SetCodeTx (EIP-7702)
	metaSetCode, tx4, _ := createSignedSetCodeTx(t, chainID, 5)

	testCases := []struct {
		name    string
		ethTx   *e_types.Transaction
		metaTx  types.Transaction
	}{
		{name: "Type 0 Legacy", ethTx: tx0},
		{name: "Type 1 AccessList", ethTx: tx1},
		{name: "Type 2 DynamicFee", ethTx: tx2},
		{name: "Type 3 BlobTx", ethTx: tx3, metaTx: metaBlob},
		{name: "Type 4 SetCodeTx", ethTx: tx4, metaTx: metaSetCode},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mTx := tc.metaTx
			if mTx == nil {
				var convErr error
				mTx, convErr = transaction.NewTransactionFromEth(tc.ethTx)
				require.NoError(t, convErr)
			}

			// 1. Canonical Hash parity: MetaNode tx.Hash() == Geth ethTx.Hash()
			assert.Equal(t, tc.ethTx.Hash(), mTx.Hash(), "MetaNode hash must match go-ethereum tx.Hash()")

			// 2. Signature verification
			assert.True(t, mTx.ValidEthSign(), "ValidEthSign must succeed")

			// 3. Envelope binding validation
			assert.NoError(t, transaction.ValidateEnvelopeBinding(mTx), "ValidateEnvelopeBinding must pass")

			// 4. Parity of extracted fields
			if tc.ethTx.To() != nil {
				assert.Equal(t, *tc.ethTx.To(), mTx.ToAddress(), "ToAddress must match")
			}
			assert.Equal(t, tc.ethTx.Value(), mTx.Amount(), "Amount must match")
			assert.Equal(t, tc.ethTx.Nonce(), mTx.GetNonce(), "Nonce must match")
			assert.Equal(t, tc.ethTx.Gas(), mTx.MaxGas(), "MaxGas must match")
			assert.Equal(t, uint64(tc.ethTx.Type()), mTx.GetType(), "Type must match")

			// Verify FromAddress matches recovered sender
			if tc.ethTx == tx0 || tc.ethTx == tx1 || tc.ethTx == tx2 {
				assert.Equal(t, from, mTx.FromAddress(), "FromAddress must match signer")
			}
		})
	}
}
