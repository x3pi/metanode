package tx_processor

import (
	"crypto/ecdsa"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/config"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/pkg/transaction"
	"github.com/meta-node-blockchain/meta-node/types"
)

func makeSecpProtoTx(t *testing.T, key interface{}, from, to common.Address, amount *big.Int, data []byte, nonce uint64, chainID uint64) types.Transaction {
	t.Helper()
	amountBytes := []byte{}
	if amount != nil && amount.Sign() > 0 {
		amountBytes = amount.Bytes()
	}
	nonceBytes := make([]byte, 8)
	big.NewInt(int64(nonce)).FillBytes(nonceBytes)

	tx := &transaction.Transaction{}
	var toBytes []byte
	if to != (common.Address{}) {
		toBytes = to.Bytes()
	}
	tx.FromProto(&pb.Transaction{
		FromAddress: from.Bytes(),
		ToAddress:   toBytes,
		Amount:      amountBytes,
		Data:        data,
		Nonce:       nonceBytes,
		MaxGas:      100000,
		MaxGasPrice: p_common.MINIMUM_BASE_FEE,
		ChainID:     chainID,
		Type:        0xFF,
	})
	if ecdsaKey, ok := key.(*ecdsa.PrivateKey); ok {
		require.NoError(t, tx.SignSecpProto(ecdsaKey))
	} else if k, ok := key.(*[32]byte); ok {
		priv, err := crypto.ToECDSA(k[:])
		require.NoError(t, err)
		require.NoError(t, tx.SignSecpProto(priv))
	}
	return tx
}

// encodeERC20Transfer builds calldata for transfer(address to, uint256 value)
func encodeERC20Transfer(to common.Address, amount *big.Int) []byte {
	data := make([]byte, 4+32+32)
	copy(data[:4], erc20TransferSelector)
	copy(data[4+12:36], to.Bytes())
	if amount != nil {
		amount.FillBytes(data[36:68])
	}
	return data
}

// encodeERC20TransferFrom builds calldata for transferFrom(address from, address to, uint256 value)
func encodeERC20TransferFrom(from, to common.Address, amount *big.Int) []byte {
	data := make([]byte, 4+32+32+32)
	copy(data[:4], erc20TransferFromSelector)
	copy(data[4+12:36], from.Bytes())
	copy(data[36+12:68], to.Bytes())
	if amount != nil {
		amount.FillBytes(data[68:100])
	}
	return data
}

func setupTokenContract(cs *blockchain.ChainState, tokenAddr common.Address) {
	asToken := state.NewAccountState(tokenAddr)
	asToken.SetSmartContractState(state.NewEmptySmartContractState())
	cs.GetAccountStateDB().SetState(asToken)
}

// G01: Sender not registered, has balance -> rejected with AccountNotRegistered (code 69)
func TestRecipientGate_G01_SenderUnregisteredWithBalance(t *testing.T) {
	chainID := big.NewInt(991)
	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(senderKey.PublicKey)
	to := common.HexToAddress("0x1111111111111111111111111111111111111111")

	tx := makeSecpProtoTx(t, senderKey, from, to, big.NewInt(100), nil, 1, chainID.Uint64())

	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})

	asSender := state.NewAccountState(from)
	asSender.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSender.SetNonce(1)
	asSender.SetParentRegistered(false) // Unregistered!
	cs.GetAccountStateDB().SetState(asSender)

	asRec := state.NewAccountState(to)
	asRec.SetParentRegistered(true) // Recipient is registered
	cs.GetAccountStateDB().SetState(asRec)

	pol := sigPolicyOf(cs)

	// Admission must reject with AccountNotRegistered (69)
	admitErr := VerifyTransaction(tx, cs, asSender)
	require.NotNil(t, admitErr)
	assert.Equal(t, transaction.AccountNotRegistered.Code, admitErr.Code)

	// Filter must drop
	valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
	require.Len(t, valid, 1)
	assert.False(t, valid[0])
}

// G02: Valid sender sends native to recipient with no state or not registered -> rejected with RecipientNotRegistered (code 91)
func TestRecipientGate_G02_NativeTransferUnregisteredRecipient(t *testing.T) {
	chainID := big.NewInt(991)
	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(senderKey.PublicKey)
	to := common.HexToAddress("0x2222222222222222222222222222222222222222")

	tx := makeSecpProtoTx(t, senderKey, from, to, big.NewInt(100), nil, 1, chainID.Uint64())

	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})
	pol := sigPolicyOf(cs)

	asSender := state.NewAccountState(from)
	asSender.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSender.SetNonce(1)
	asSender.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asSender)

	// Subtest a: recipient has NO state at all
	t.Run("recipient has no state", func(t *testing.T) {
		admitErr := VerifyTransaction(tx, cs, asSender)
		require.NotNil(t, admitErr)
		assert.Equal(t, transaction.RecipientNotRegistered.Code, admitErr.Code)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.False(t, valid[0], "execution filter must drop tx to unregistered recipient")
	})

	// Subtest b: recipient has state (e.g. balance) but ParentRegistered = false
	t.Run("recipient has state but ParentRegistered=false", func(t *testing.T) {
		asRec := state.NewAccountState(to)
		asRec.AddBalance(big.NewInt(500))
		asRec.SetParentRegistered(false)
		cs.GetAccountStateDB().SetState(asRec)

		admitErr := VerifyTransaction(tx, cs, asSender)
		require.NotNil(t, admitErr)
		assert.Equal(t, transaction.RecipientNotRegistered.Code, admitErr.Code)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.False(t, valid[0])
	})
}

// G03: Sender & recipient confirmed/registered -> direct native transfer passes
func TestRecipientGate_G03_NativeTransferRegisteredRecipient(t *testing.T) {
	chainID := big.NewInt(991)
	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(senderKey.PublicKey)
	to := common.HexToAddress("0x3333333333333333333333333333333333333333")

	tx := makeSecpProtoTx(t, senderKey, from, to, big.NewInt(100), nil, 1, chainID.Uint64())

	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})
	pol := sigPolicyOf(cs)

	asSender := state.NewAccountState(from)
	asSender.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSender.SetNonce(1)
	asSender.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asSender)

	asRec := state.NewAccountState(to)
	asRec.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asRec)

	admitErr := VerifyTransaction(tx, cs, asSender)
	assert.Nil(t, admitErr, "VerifyTransaction must pass for registered recipient")

	valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
	require.Len(t, valid, 1)
	assert.True(t, valid[0], "execution filter must accept tx")
}

// G04: ERC-20 transfer to unregistered recipient -> rejected with RecipientNotRegistered (code 91)
func TestRecipientGate_G04_ERC20TransferUnregisteredRecipient(t *testing.T) {
	chainID := big.NewInt(991)
	tokenContract := common.HexToAddress("0x1111111111111111111111111111111111110001")
	unregRecipient := common.HexToAddress("0x2222222222222222222222222222222222220002")

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(senderKey.PublicKey)

	calldata := encodeERC20Transfer(unregRecipient, big.NewInt(1000))

	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})
	pol := sigPolicyOf(cs)

	asSender := state.NewAccountState(from)
	asSender.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSender.SetNonce(1)
	asSender.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asSender)
	setupTokenContract(cs, tokenContract)

	// Subtest 1: Type 0xFF proto tx
	t.Run("Type 0xFF ERC-20 transfer", func(t *testing.T) {
		tx := makeSecpProtoTx(t, senderKey, from, tokenContract, big.NewInt(0), calldata, 1, chainID.Uint64())

		admitErr := VerifyTransaction(tx, cs, asSender)
		require.NotNil(t, admitErr)
		assert.Equal(t, transaction.RecipientNotRegistered.Code, admitErr.Code)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.False(t, valid[0])
	})

	// Subtest 2: Raw Ethereum EIP-1559 transaction
	t.Run("Raw Ethereum EIP-1559 ERC-20 transfer", func(t *testing.T) {
		rawTx := e_types.NewTx(&e_types.DynamicFeeTx{
			ChainID:   chainID,
			Nonce:     1,
			GasTipCap: big.NewInt(1_000_000_000),
			GasFeeCap: big.NewInt(20_000_000_000),
			Gas:       100000,
			To:        &tokenContract,
			Value:     big.NewInt(0),
			Data:      calldata,
		})
		signer := e_types.LatestSignerForChainID(chainID)
		signedRawTx, err := e_types.SignTx(rawTx, signer, senderKey)
		require.NoError(t, err)

		ethMetaTx, err := transaction.NewTransactionFromEth(signedRawTx)
		require.NoError(t, err)

		admitErr := VerifyTransaction(ethMetaTx, cs, asSender)
		require.NotNil(t, admitErr)
		assert.Equal(t, transaction.RecipientNotRegistered.Code, admitErr.Code)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{ethMetaTx}, nil, pol)
		require.Len(t, valid, 1)
		assert.False(t, valid[0])
	})
}

// G05: ERC-20 direct transfer to registered recipient -> passes
func TestRecipientGate_G05_ERC20DirectTransferValid(t *testing.T) {
	chainID := big.NewInt(991)
	tokenContract := common.HexToAddress("0x1111111111111111111111111111111111110001")
	regRecipient := common.HexToAddress("0x2222222222222222222222222222222222220002")

	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(senderKey.PublicKey)

	calldata := encodeERC20Transfer(regRecipient, big.NewInt(1000))

	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})
	pol := sigPolicyOf(cs)

	asSender := state.NewAccountState(from)
	asSender.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSender.SetNonce(1)
	asSender.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asSender)

	asRec := state.NewAccountState(regRecipient)
	asRec.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asRec)
	setupTokenContract(cs, tokenContract)

	tx := makeSecpProtoTx(t, senderKey, from, tokenContract, big.NewInt(0), calldata, 1, chainID.Uint64())

	admitErr := VerifyTransaction(tx, cs, asSender)
	assert.Nil(t, admitErr, "VerifyTransaction must pass for registered recipient")

	valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
	require.Len(t, valid, 1)
	assert.True(t, valid[0], "execution filter must accept tx")
}

// G06: Token owner locked / unregistered, but spender has allowance -> direct transferFrom blocked
func TestRecipientGate_G06_ERC20TransferFromLockedOwner(t *testing.T) {
	chainID := big.NewInt(991)
	tokenContract := common.HexToAddress("0x1111111111111111111111111111111111110001")
	ownerAddr := common.HexToAddress("0x3333333333333333333333333333333333330003")
	recipientAddr := common.HexToAddress("0x4444444444444444444444444444444444440004")

	spenderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	spender := crypto.PubkeyToAddress(spenderKey.PublicKey)

	calldata := encodeERC20TransferFrom(ownerAddr, recipientAddr, big.NewInt(1000))

	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})
	pol := sigPolicyOf(cs)

	asSpender := state.NewAccountState(spender)
	asSpender.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSpender.SetNonce(1)
	asSpender.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asSpender)

	asRecipient := state.NewAccountState(recipientAddr)
	asRecipient.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asRecipient)
	setupTokenContract(cs, tokenContract)

	tx := makeSecpProtoTx(t, spenderKey, spender, tokenContract, big.NewInt(0), calldata, 1, chainID.Uint64())

	// Subtest a: Owner is LOCKED -> rejected with AccountLocked (code 92)
	t.Run("owner is locked", func(t *testing.T) {
		asOwner := state.NewAccountState(ownerAddr)
		asOwner.SetParentRegistered(true)
		asOwner.SetLocked(true)
		cs.GetAccountStateDB().SetState(asOwner)

		admitErr := VerifyTransaction(tx, cs, asSpender)
		require.NotNil(t, admitErr)
		assert.Equal(t, transaction.AccountLocked.Code, admitErr.Code)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.False(t, valid[0])
	})

	// Subtest b: Owner is unregistered -> rejected with AccountNotRegistered (code 69)
	t.Run("owner is unregistered", func(t *testing.T) {
		asOwner := state.NewAccountState(ownerAddr)
		asOwner.SetParentRegistered(false)
		asOwner.SetLocked(false)
		cs.GetAccountStateDB().SetState(asOwner)

		admitErr := VerifyTransaction(tx, cs, asSpender)
		require.NotNil(t, admitErr)
		assert.Equal(t, transaction.AccountNotRegistered.Code, admitErr.Code)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.False(t, valid[0])
	})

	// Subtest c: Owner registered, recipient locked -> rejected with AccountLocked (code 92)
	t.Run("recipient is locked", func(t *testing.T) {
		asOwner := state.NewAccountState(ownerAddr)
		asOwner.SetParentRegistered(true)
		asOwner.SetLocked(false)
		cs.GetAccountStateDB().SetState(asOwner)

		asRecipient.SetLocked(true)
		cs.GetAccountStateDB().SetState(asRecipient)

		admitErr := VerifyTransaction(tx, cs, asSpender)
		require.NotNil(t, admitErr)
		assert.Equal(t, transaction.AccountLocked.Code, admitErr.Code)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.False(t, valid[0])
	})

	// Subtest d: Owner registered, recipient unregistered -> rejected with RecipientNotRegistered (code 91)
	t.Run("recipient is unregistered", func(t *testing.T) {
		asOwner := state.NewAccountState(ownerAddr)
		asOwner.SetParentRegistered(true)
		asOwner.SetLocked(false)
		cs.GetAccountStateDB().SetState(asOwner)

		asRecipient.SetParentRegistered(false)
		asRecipient.SetLocked(false)
		cs.GetAccountStateDB().SetState(asRecipient)

		admitErr := VerifyTransaction(tx, cs, asSpender)
		require.NotNil(t, admitErr)
		assert.Equal(t, transaction.RecipientNotRegistered.Code, admitErr.Code)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.False(t, valid[0])
	})

	// Subtest e: All registered and unlocked -> passes
	t.Run("all registered and unlocked", func(t *testing.T) {
		asOwner := state.NewAccountState(ownerAddr)
		asOwner.SetParentRegistered(true)
		asOwner.SetLocked(false)
		cs.GetAccountStateDB().SetState(asOwner)

		asRecipient.SetParentRegistered(true)
		asRecipient.SetLocked(false)
		cs.GetAccountStateDB().SetState(asRecipient)

		admitErr := VerifyTransaction(tx, cs, asSpender)
		assert.Nil(t, admitErr)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.True(t, valid[0])
	})
}

// G07: Short/malformed ABI calldata, similar selector, call value = 0 -> no panic, no unintentional bypass
func TestRecipientGate_G07_CalldataValidation(t *testing.T) {
	chainID := big.NewInt(991)
	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(senderKey.PublicKey)
	contractAddr := common.HexToAddress("0xContract000000000000000000000000000001")

	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})
	pol := sigPolicyOf(cs)

	asSender := state.NewAccountState(from)
	asSender.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSender.SetNonce(1)
	asSender.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asSender)
	setupTokenContract(cs, contractAddr)

	// 1. Selector is transfer (0xa9059cbb) but length < 68 bytes -> InvalidData
	t.Run("truncated transfer calldata", func(t *testing.T) {
		shortData := []byte{0xa9, 0x05, 0x9c, 0xbb, 0x01, 0x02}
		tx := makeSecpProtoTx(t, senderKey, from, contractAddr, big.NewInt(0), shortData, 1, chainID.Uint64())
		admitErr := VerifyTransaction(tx, cs, asSender)
		require.NotNil(t, admitErr)
		assert.Equal(t, transaction.InvalidData.Code, admitErr.Code)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.False(t, valid[0])
	})

	// 2. Transfer calldata length 68 but bad address padding (non-zero bytes in 4..16) -> InvalidData
	t.Run("bad address padding in transfer", func(t *testing.T) {
		badPaddingData := encodeERC20Transfer(common.HexToAddress("0x123"), big.NewInt(100))
		badPaddingData[5] = 0xFF // Corrupt padding byte
		tx := makeSecpProtoTx(t, senderKey, from, contractAddr, big.NewInt(0), badPaddingData, 1, chainID.Uint64())
		admitErr := VerifyTransaction(tx, cs, asSender)
		require.NotNil(t, admitErr)
		assert.Equal(t, transaction.InvalidData.Code, admitErr.Code)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.False(t, valid[0])
	})

	// 3. Selector is transferFrom (0x23b87266) but length < 100 bytes -> InvalidData
	t.Run("truncated transferFrom calldata", func(t *testing.T) {
		shortData := []byte{0x23, 0xb8, 0x72, 0x66, 0x01, 0x02}
		tx := makeSecpProtoTx(t, senderKey, from, contractAddr, big.NewInt(0), shortData, 1, chainID.Uint64())
		admitErr := VerifyTransaction(tx, cs, asSender)
		require.NotNil(t, admitErr)
		assert.Equal(t, transaction.InvalidData.Code, admitErr.Code)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.False(t, valid[0])
	})

	// 4. Calldata shorter than 4 bytes, value = 0 -> no panic, passes
	t.Run("short non-selector call with value=0", func(t *testing.T) {
		tx := makeSecpProtoTx(t, senderKey, from, contractAddr, big.NewInt(0), []byte{0x01, 0x02}, 1, chainID.Uint64())
		admitErr := VerifyTransaction(tx, cs, asSender)
		assert.Nil(t, admitErr)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.True(t, valid[0])
	})

	// 5. Arbitrary non-transfer selector (e.g. approve 0x095ea7b3), value = 0 -> no panic, passes
	t.Run("non-transfer contract call", func(t *testing.T) {
		approveData := make([]byte, 68)
		copy(approveData[:4], []byte{0x09, 0x5e, 0xa7, 0xb3})
		tx := makeSecpProtoTx(t, senderKey, from, contractAddr, big.NewInt(0), approveData, 1, chainID.Uint64())
		admitErr := VerifyTransaction(tx, cs, asSender)
		assert.Nil(t, admitErr)

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
		require.Len(t, valid, 1)
		assert.True(t, valid[0])
	})
}

// G08: Deploy and system exemptions
func TestRecipientGate_G08_DeployAndSystemExemptions(t *testing.T) {
	chainID := big.NewInt(991)
	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(senderKey.PublicKey)

	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})
	pol := sigPolicyOf(cs)

	asSender := state.NewAccountState(from)
	asSender.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSender.SetNonce(1)
	asSender.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asSender)

	// 1. Contract deployment (ToAddress == 0x0) with native value: exempt from recipient gate
	t.Run("contract deployment with value", func(t *testing.T) {
		deployTx := makeSecpProtoTx(t, senderKey, from, common.Address{}, big.NewInt(100), []byte{0x60, 0x80, 0x60, 0x40}, 1, chainID.Uint64())
		admitErr := VerifyTransaction(deployTx, cs, asSender)
		assert.Nil(t, admitErr, "deployment must be exempt from recipient gate")

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{deployTx}, nil, pol)
		require.Len(t, valid, 1)
		assert.True(t, valid[0])
	})

	// 2. Call to EVM precompile (e.g. 0x01 ecrecover)
	t.Run("call to EVM precompile", func(t *testing.T) {
		precompile := common.BytesToAddress([]byte{0x01})
		precompileTx := makeSecpProtoTx(t, senderKey, from, precompile, big.NewInt(100), nil, 1, chainID.Uint64())
		admitErr := VerifyTransaction(precompileTx, cs, asSender)
		assert.Nil(t, admitErr, "precompile must be exempt from recipient gate")

		valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{precompileTx}, nil, pol)
		require.Len(t, valid, 1)
		assert.True(t, valid[0])
	})

	// 3. User attempting to forge a system tx to RollupSystemAddress -> rejected with UnauthorizedSystemSender
	t.Run("unauthorized user to RollupSystemAddress", func(t *testing.T) {
		sysFakeTx := makeSecpProtoTx(t, senderKey, from, rollup.RollupSystemAddress, big.NewInt(0), nil, 1, chainID.Uint64())
		admitErr := VerifyTransaction(sysFakeTx, cs, asSender)
		require.NotNil(t, admitErr)
		assert.Equal(t, transaction.UnauthorizedSystemSender.Code, admitErr.Code)
	})
}

// G09: Hot signature cache does not bypass recipient/owner gate when recipient becomes unregistered/locked
func TestRecipientGate_G09_SignatureCacheNoPoison(t *testing.T) {
	chainID := big.NewInt(991)
	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(senderKey.PublicKey)
	to := common.HexToAddress("0x9999999999999999999999999999999999999999")

	tx := makeSecpProtoTx(t, senderKey, from, to, big.NewInt(100), nil, 1, chainID.Uint64())

	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})
	pol := sigPolicyOf(cs)

	asSender := state.NewAccountState(from)
	asSender.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSender.SetNonce(1)
	asSender.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asSender)

	// Step 1: Initially recipient is registered
	asRec := state.NewAccountState(to)
	asRec.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asRec)

	rotateVerifiedSignatures()
	rotateVerifiedSignatures()

	// Initial submission: succeeds and heats cache
	require.Nil(t, VerifyTransaction(tx, cs, asSender))
	valid, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
	require.True(t, valid[0])

	// Signature cache is now hot!
	cacheKey := sigCacheKey(tx, nil)
	require.True(t, LoadVerifiedSignature(cacheKey), "signature must be in cache")

	// Step 2: Recipient is now LOCKED or UNREGISTERED
	asRec.SetLocked(true)
	cs.GetAccountStateDB().SetState(asRec)

	// Step 3: Re-evaluating the EXACT SAME transaction must fail gate despite cache hit!
	admitErr := VerifyTransaction(tx, cs, asSender)
	require.NotNil(t, admitErr, "must reject locked recipient despite cached signature")
	assert.Equal(t, transaction.AccountLocked.Code, admitErr.Code)

	valid2, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
	require.Len(t, valid2, 1)
	assert.False(t, valid2[0], "execution filter must drop tx despite cached signature")
}

// G10: Pre-block state consistency: recipient registered in block N only takes effect in block N+1
func TestRecipientGate_G10_PreBlockStateEvaluation(t *testing.T) {
	chainID := big.NewInt(991)
	senderKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	from := crypto.PubkeyToAddress(senderKey.PublicKey)
	to := common.HexToAddress("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")

	tx := makeSecpProtoTx(t, senderKey, from, to, big.NewInt(100), nil, 1, chainID.Uint64())

	cs := setupTestChainState(t)
	cs.SetConfig(&config.SimpleChainConfig{
		ChainId:         chainID,
		TxSignatureMode: config.TxSignatureModeSecp,
		AccountGate:     config.AccountGateParentRegistered,
	})
	pol := sigPolicyOf(cs)

	asSender := state.NewAccountState(from)
	asSender.AddBalance(big.NewInt(1_000_000_000_000_000))
	asSender.SetNonce(1)
	asSender.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(asSender)

	// Block N Pre-state: Recipient is NOT registered
	preBlockRec := state.NewAccountState(to)
	preBlockRec.SetParentRegistered(false)
	cs.GetAccountStateDB().SetState(preBlockRec)

	// Block N evaluation: must be dropped
	validN, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
	require.Len(t, validN, 1)
	assert.False(t, validN[0], "must drop in block N evaluated against pre-block state")

	// Block N commit: Recipient registration committed
	postBlockRec := preBlockRec.Copy().(types.AccountState)
	postBlockRec.SetParentRegistered(true)
	cs.GetAccountStateDB().SetState(postBlockRec)

	// Block N+1 evaluation: must pass
	validN1, _ := verifySignatures(cs.GetAccountStateDB(), []types.Transaction{tx}, nil, pol)
	require.Len(t, validN1, 1)
	assert.True(t, validN1[0], "must accept in block N+1 after registration commits")
	assert.Nil(t, VerifyTransaction(tx, cs, asSender), "admission must also pass for block N+1")
}
