package transaction

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

const (
	// MaxStandardTxEnvelopeSize is the maximum allowed size for standard (non-blob) EIP-2718 envelopes (128 KB).
	MaxStandardTxEnvelopeSize = 128 * 1024
	// MaxRawEthTxEnvelopeSize is the maximum envelope size for EIP-4844 blob transactions with KZG sidecars (1 MB).
	MaxRawEthTxEnvelopeSize = 1024 * 1024
	// MaxBatchTxCount is the maximum number of transactions allowed in a single TCP batch.
	MaxBatchTxCount = 1000
	// MaxInitCodeSize is the maximum allowed size for contract creation bytecode (48 KB per EIP-3860).
	MaxInitCodeSize = 49152
	// MaxAccessListTuples is the maximum allowed number of access list tuples to prevent DoS.
	MaxAccessListTuples = 1024
)

var (
	secp256k1N     = crypto.S256().Params().N
	secp256k1halfN = new(big.Int).Rsh(secp256k1N, 1)
)

// ValidateEthTxEnvelope validates an Ethereum transaction envelope before admission:
// 1. Transaction must not be nil.
// 2. Reject pre-EIP-155 transactions (must be protected by EIP-155 with non-nil positive chain ID).
// 3. Chain ID must match expectedChainId if expectedChainId is provided (> 0).
// 4. Signature values (r, s, v) must be present, positive, r < N, and s <= N/2 (anti-malleability per EIP-2).
// 5. Sender address must be recoverable and non-zero.
func ValidateEthTxEnvelope(ethTx *e_types.Transaction, expectedChainId *big.Int) error {
	if ethTx == nil {
		return fmt.Errorf("%w: transaction is nil", InvalidTransaction)
	}

	// 1. Reject pre-EIP-155 unprotected transactions
	if !ethTx.Protected() {
		return ErrPreEIP155
	}

	chainID := ethTx.ChainId()
	if chainID == nil || chainID.Sign() <= 0 {
		return fmt.Errorf("%w: transaction missing chain ID", InvalidChainId)
	}

	// 2. Verify chain ID matches expected
	if expectedChainId != nil && expectedChainId.Sign() > 0 {
		if chainID.Cmp(expectedChainId) != 0 {
			return fmt.Errorf("%w: invalid chain ID: expected %d, got %d", InvalidChainId, expectedChainId, chainID)
		}
	}

	// 3. Verify signature values
	v, r, s := ethTx.RawSignatureValues()
	if v == nil || r == nil || s == nil {
		return fmt.Errorf("%w: transaction missing signature values (v, r, s)", InvalidSignSecp)
	}

	if r.Sign() <= 0 || s.Sign() <= 0 {
		return fmt.Errorf("%w: signature r and s must be positive", InvalidSignSecp)
	}

	if r.Cmp(secp256k1N) >= 0 {
		return fmt.Errorf("%w: signature r exceeds curve order N", InvalidSignSecp)
	}

	// EIP-2: Malleable signature check: s must be in lower half of curve order (s <= N/2)
	if s.Cmp(secp256k1halfN) > 0 {
		return ErrMalleableSignature
	}

	// 4. Recover sender address
	signer := e_types.LatestSignerForChainID(chainID)
	from, err := e_types.Sender(signer, ethTx)
	if err != nil {
		return fmt.Errorf("%w: failed to recover sender: %v", ErrSenderRecovery, err)
	}
	if from == (common.Address{}) {
		return fmt.Errorf("%w: recovered sender address cannot be zero", ErrInvalidSender)
	}

	// 5. EIP-3860: Reject initcode exceeding MaxInitCodeSize (49152 bytes)
	if ethTx.To() == nil && len(ethTx.Data()) > MaxInitCodeSize {
		return fmt.Errorf("%w: initcode size %d exceeds max allowed %d", ErrMaxInitCodeSizeExceeded, len(ethTx.Data()), MaxInitCodeSize)
	}

	// 6. Reject oversized access lists to prevent CPU/memory exhaustion DoS
	if len(ethTx.AccessList()) > MaxAccessListTuples {
		return fmt.Errorf("%w: access list tuples %d exceeds max allowed %d", InvalidTransaction, len(ethTx.AccessList()), MaxAccessListTuples)
	}

	return nil
}

// ValidateProtoEnvelopeBinding verifies that when RawEnvelope is present, every single
// execution-relevant field in pTx matches the canonical representation derived directly
// from RawEnvelope. This closes P0-9 where an attacker keeps a valid RawEnvelope
// but mutates proto fields (To, Amount, Nonce, MaxGas, MaxGasPrice, GasFeeCap, GasTipCap,
// Data/CallData/DeployData, Type, ChainID, R/S/V, AccessList, BlobVersionedHashes,
// MaxFeePerBlobGas, AuthorizationList).
func ValidateProtoEnvelopeBinding(pTx *pb.Transaction) error {
	return validateProtoEnvelopeBinding(pTx, nil)
}

// validateEnvelopeBindingTx is ValidateProtoEnvelopeBinding for a tx object: it reuses the tx's memoized envelope
// decode (and the sender already recovered on it) instead of decoding + ecrecover-ing again on every call.
func validateEnvelopeBindingTx(t *Transaction) error {
	if t == nil {
		return errors.New("transaction is nil")
	}
	return validateProtoEnvelopeBinding(t.proto, t)
}

// validateProtoEnvelopeBinding: owner (optional) supplies the memoized envelope decode; the checks are identical.
func validateProtoEnvelopeBinding(pTx *pb.Transaction, owner *Transaction) error {
	if pTx == nil {
		return errors.New("transaction proto is nil")
	}
	if len(pTx.RawEnvelope) == 0 {
		return nil // Non-envelope transaction (e.g. system BLS transaction)
	}

	var ethTx *e_types.Transaction
	var decErr error
	if owner != nil {
		ethTx, decErr = owner.envelopeEthTx()
	} else {
		ethTx = new(e_types.Transaction)
		decErr = ethTx.UnmarshalBinary(pTx.RawEnvelope)
	}
	if decErr != nil {
		return fmt.Errorf("%w: failed to unmarshal RawEnvelope: %v", ErrInvalidEnvelope, decErr)
	}

	return validateDirectEnvelopeBinding(pTx, ethTx)
}

// validateDirectEnvelopeBinding verifies that every execution-relevant field in pTx matches
// the envelope data in ethTx directly, without constructing an intermediate pb.Transaction.
// This significantly reduces GC overhead and memory allocations on high-throughput ingress paths.
func validateDirectEnvelopeBinding(pTx *pb.Transaction, ethTx *e_types.Transaction) error {
	var (
		signer  e_types.Signer
		convErr error
		txType  = ethTx.Type()
	)

	switch txType {
	case e_types.LegacyTxType:
		txChainID := ethTx.ChainId()
		if txChainID != nil && txChainID.Sign() != 0 {
			signer = e_types.NewEIP155Signer(txChainID)
		} else {
			signer = e_types.HomesteadSigner{}
		}

	case e_types.AccessListTxType:
		txChainID := ethTx.ChainId()
		if txChainID == nil {
			convErr = errors.New("giao dịch EIP-2930 từ Ethereum thiếu ChainID")
		} else if ethTx.GasPrice() == nil {
			convErr = errors.New("giao dịch EIP-2930 từ Ethereum thiếu GasPrice")
		} else {
			signer = e_types.NewLondonSigner(txChainID)
		}

	case e_types.DynamicFeeTxType:
		txChainID := ethTx.ChainId()
		if txChainID == nil {
			convErr = errors.New("EIP-1559 transaction from Ethereum is missing ChainID")
		} else {
			signer = e_types.NewLondonSigner(txChainID)
		}

	case e_types.BlobTxType:
		to := ethTx.To()
		if to == nil || *to == (common.Address{}) {
			convErr = errors.New("EIP-4844 transaction cannot be a contract creation")
		} else if blobHashes := ethTx.BlobHashes(); len(blobHashes) == 0 {
			convErr = errors.New("EIP-4844 transaction must carry at least one blob hash")
		} else if len(blobHashes) > p_common.MAX_BLOBS_PER_TX {
			convErr = fmt.Errorf("EIP-4844 transaction carries %d blobs, exceeds the %d/tx limit", len(blobHashes), p_common.MAX_BLOBS_PER_TX)
		} else if txChainID := ethTx.ChainId(); txChainID == nil || txChainID.Sign() <= 0 {
			convErr = errors.New("EIP-4844 transaction is missing ChainID")
		} else {
			signer = e_types.NewCancunSigner(txChainID)
		}

	case e_types.SetCodeTxType:
		to := ethTx.To()
		if to == nil || *to == (common.Address{}) {
			convErr = errors.New("EIP-7702 transaction cannot be a contract creation")
		} else if authList := ethTx.SetCodeAuthorizations(); len(authList) == 0 {
			convErr = errors.New("EIP-7702 transaction must carry at least one authorization tuple")
		} else if txChainID := ethTx.ChainId(); txChainID == nil || txChainID.Sign() <= 0 {
			convErr = errors.New("EIP-7702 transaction is missing ChainID")
		} else {
			signer = e_types.NewPragueSigner(txChainID)
		}

	default:
		convErr = errors.New("unsupported Ethereum transaction type")
	}

	if convErr != nil {
		return fmt.Errorf("%w: failed to convert EthTx to canonical proto: %v", ErrInvalidEnvelope, convErr)
	}

	// 1. FromAddress
	fromAddress, err := e_types.Sender(signer, ethTx)
	if err != nil {
		return fmt.Errorf("%w: failed to convert EthTx to canonical proto: %v", ErrInvalidEnvelope, err)
	}
	if !bytes.Equal(pTx.FromAddress, fromAddress.Bytes()) {
		return fmt.Errorf("%w: FromAddress mismatch", ErrEnvelopeBindingMismatch)
	}

	// 2. ToAddress
	if ethTx.To() != nil {
		if !bytes.Equal(pTx.ToAddress, ethTx.To().Bytes()) {
			return fmt.Errorf("%w: ToAddress mismatch", ErrEnvelopeBindingMismatch)
		}
	} else {
		if len(pTx.ToAddress) != 0 {
			return fmt.Errorf("%w: ToAddress mismatch", ErrEnvelopeBindingMismatch)
		}
	}

	// 3. Amount
	var amtBytes []byte
	if ethTx.Value() != nil {
		amtBytes = ethTx.Value().Bytes()
	}
	if !bytes.Equal(pTx.Amount, amtBytes) {
		return fmt.Errorf("%w: Amount mismatch", ErrEnvelopeBindingMismatch)
	}

	// 4. Nonce
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], ethTx.Nonce())
	if !bytes.Equal(pTx.Nonce, nonceBytes[:]) {
		return fmt.Errorf("%w: Nonce mismatch", ErrEnvelopeBindingMismatch)
	}

	// 5. MaxGas
	if pTx.MaxGas != ethTx.Gas() {
		return fmt.Errorf("%w: MaxGas mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, pTx.MaxGas, ethTx.Gas())
	}

	// 6. MaxGasPrice, 7. GasFeeCap, 8. GasTipCap
	switch txType {
	case e_types.LegacyTxType, e_types.AccessListTxType:
		expectedPrice := uint64(0)
		if ethTx.GasPrice() != nil {
			expectedPrice = ethTx.GasPrice().Uint64()
		}
		if pTx.MaxGasPrice != expectedPrice {
			return fmt.Errorf("%w: MaxGasPrice mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, pTx.MaxGasPrice, expectedPrice)
		}
		if len(pTx.GasFeeCap) != 0 {
			return fmt.Errorf("%w: GasFeeCap mismatch", ErrEnvelopeBindingMismatch)
		}
		if len(pTx.GasTipCap) != 0 {
			return fmt.Errorf("%w: GasTipCap mismatch", ErrEnvelopeBindingMismatch)
		}
	case e_types.DynamicFeeTxType, e_types.BlobTxType, e_types.SetCodeTxType:
		if pTx.MaxGasPrice != 0 {
			return fmt.Errorf("%w: MaxGasPrice mismatch: got %d, expected 0", ErrEnvelopeBindingMismatch, pTx.MaxGasPrice)
		}
		var expectedFee, expectedTip []byte
		if ethTx.GasFeeCap() != nil {
			expectedFee = ethTx.GasFeeCap().Bytes()
		}
		if ethTx.GasTipCap() != nil {
			expectedTip = ethTx.GasTipCap().Bytes()
		}
		if !bytes.Equal(pTx.GasFeeCap, expectedFee) {
			return fmt.Errorf("%w: GasFeeCap mismatch", ErrEnvelopeBindingMismatch)
		}
		if !bytes.Equal(pTx.GasTipCap, expectedTip) {
			return fmt.Errorf("%w: GasTipCap mismatch", ErrEnvelopeBindingMismatch)
		}
	}

	// 9. Data (covers CallData / DeployData)
	txData := ethTx.Data()
	if len(txData) == 0 {
		if len(pTx.Data) != 0 {
			return fmt.Errorf("%w: Data mismatch", ErrEnvelopeBindingMismatch)
		}
	} else {
		storageAddr := common.HexToAddress("0xda7284fac5e804f8b9d71aa39310f0f86776b51d")
		if txType == e_types.BlobTxType || txType == e_types.SetCodeTxType {
			storageAddr = common.Address{}
		}
		expectedData, err := prepareTransactionSpecificData(ethTx, storageAddr)
		if err != nil {
			return fmt.Errorf("%w: failed to convert EthTx to canonical proto: %v", ErrInvalidEnvelope, err)
		}
		if !bytes.Equal(pTx.Data, expectedData) {
			return fmt.Errorf("%w: Data mismatch", ErrEnvelopeBindingMismatch)
		}
	}

	// 10. Type
	if pTx.Type != uint64(txType) {
		return fmt.Errorf("%w: Type mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, pTx.Type, txType)
	}

	// 11. ChainID
	var expectedChainID uint64
	if ethTx.ChainId() != nil {
		expectedChainID = ethTx.ChainId().Uint64()
	}
	if pTx.ChainID != expectedChainID {
		return fmt.Errorf("%w: ChainID mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, pTx.ChainID, expectedChainID)
	}

	// 12. R, S, V and 17. Sign (R || S || V)
	v, r, s := ethTx.RawSignatureValues()
	var rBytes, sBytes, vBytes []byte
	if r != nil {
		rBytes = r.Bytes()
	}
	if s != nil {
		sBytes = s.Bytes()
	}
	if v != nil {
		vBytes = v.Bytes()
	}
	if !bytes.Equal(pTx.R, rBytes) {
		return fmt.Errorf("%w: R mismatch", ErrEnvelopeBindingMismatch)
	}
	if !bytes.Equal(pTx.S, sBytes) {
		return fmt.Errorf("%w: S mismatch", ErrEnvelopeBindingMismatch)
	}
	if !bytes.Equal(pTx.V, vBytes) {
		return fmt.Errorf("%w: V mismatch", ErrEnvelopeBindingMismatch)
	}

	if v != nil && r != nil && s != nil {
		var recoveryID byte
		if txType == e_types.LegacyTxType {
			txChainID := ethTx.ChainId()
			if txChainID != nil && txChainID.Sign() != 0 {
				expectedVBase := new(big.Int).Mul(txChainID, big.NewInt(2))
				expectedVBase.Add(expectedVBase, big.NewInt(35))
				vTmp := new(big.Int).Sub(v, expectedVBase)
				recoveryID = byte(vTmp.Uint64())
			} else {
				recoveryID = byte(v.Uint64() - 27)
			}
		} else {
			if len(vBytes) > 0 {
				recoveryID = vBytes[len(vBytes)-1]
			} else {
				recoveryID = 0
			}
		}

		sigLen := len(rBytes) + len(sBytes) + 1
		var expectedSign []byte
		if sigLen <= 65 {
			var sigBuf [65]byte
			copy(sigBuf[0:], rBytes)
			copy(sigBuf[len(rBytes):], sBytes)
			sigBuf[len(rBytes)+len(sBytes)] = recoveryID
			expectedSign = sigBuf[:sigLen]
		} else {
			expectedSign = make([]byte, 0, sigLen)
			expectedSign = append(expectedSign, rBytes...)
			expectedSign = append(expectedSign, sBytes...)
			expectedSign = append(expectedSign, recoveryID)
		}

		if len(expectedSign) > 0 && !bytes.Equal(pTx.Sign, expectedSign) {
			return fmt.Errorf("%w: Sign mismatch", ErrEnvelopeBindingMismatch)
		}
	} else if len(pTx.Sign) > 0 {
		return fmt.Errorf("%w: Sign mismatch", ErrEnvelopeBindingMismatch)
	}

	// 13. AccessList
	ethAL := ethTx.AccessList()
	if len(pTx.AccessList) != len(ethAL) {
		return fmt.Errorf("%w: AccessList length mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, len(pTx.AccessList), len(ethAL))
	}
	for i := range ethAL {
		if !bytes.Equal(pTx.AccessList[i].Address, ethAL[i].Address.Bytes()) {
			return fmt.Errorf("%w: AccessList[%d].Address mismatch", ErrEnvelopeBindingMismatch, i)
		}
		if len(pTx.AccessList[i].StorageKeys) != len(ethAL[i].StorageKeys) {
			return fmt.Errorf("%w: AccessList[%d].StorageKeys length mismatch", ErrEnvelopeBindingMismatch, i)
		}
		for j := range ethAL[i].StorageKeys {
			if !bytes.Equal(pTx.AccessList[i].StorageKeys[j], ethAL[i].StorageKeys[j].Bytes()) {
				return fmt.Errorf("%w: AccessList[%d].StorageKeys[%d] mismatch", ErrEnvelopeBindingMismatch, i, j)
			}
		}
	}

	// 14. BlobVersionedHashes & 15. MaxFeePerBlobGas
	if txType == e_types.BlobTxType {
		blobHashes := ethTx.BlobHashes()
		if len(pTx.BlobVersionedHashes) != len(blobHashes) {
			return fmt.Errorf("%w: BlobVersionedHashes length mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, len(pTx.BlobVersionedHashes), len(blobHashes))
		}
		for i, h := range blobHashes {
			if !bytes.Equal(pTx.BlobVersionedHashes[i], h.Bytes()) {
				return fmt.Errorf("%w: BlobVersionedHashes[%d] mismatch", ErrEnvelopeBindingMismatch, i)
			}
		}
		var expectedBlobFee []byte
		if ethTx.BlobGasFeeCap() != nil {
			expectedBlobFee = ethTx.BlobGasFeeCap().Bytes()
		}
		if !bytes.Equal(pTx.MaxFeePerBlobGas, expectedBlobFee) {
			return fmt.Errorf("%w: MaxFeePerBlobGas mismatch", ErrEnvelopeBindingMismatch)
		}
	} else {
		if len(pTx.BlobVersionedHashes) != 0 {
			return fmt.Errorf("%w: BlobVersionedHashes length mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, len(pTx.BlobVersionedHashes), 0)
		}
		if len(pTx.MaxFeePerBlobGas) != 0 {
			return fmt.Errorf("%w: MaxFeePerBlobGas mismatch", ErrEnvelopeBindingMismatch)
		}
	}

	// 16. AuthorizationList
	if txType == e_types.SetCodeTxType {
		authList := ethTx.SetCodeAuthorizations()
		if len(pTx.AuthorizationList) != len(authList) {
			return fmt.Errorf("%w: AuthorizationList length mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, len(pTx.AuthorizationList), len(authList))
		}
		for i, a := range authList {
			pAuth := pTx.AuthorizationList[i]
			authChainID := a.ChainID.Uint64()
			rAuthBytes := a.R.Bytes()
			sAuthBytes := a.S.Bytes()
			if pAuth.ChainID != authChainID || !bytes.Equal(pAuth.Address, a.Address.Bytes()) || pAuth.Nonce != a.Nonce ||
				len(pAuth.YParity) != 1 || pAuth.YParity[0] != a.V || !bytes.Equal(pAuth.R, rAuthBytes) || !bytes.Equal(pAuth.S, sAuthBytes) {
				return fmt.Errorf("%w: AuthorizationList[%d] mismatch", ErrEnvelopeBindingMismatch, i)
			}
		}
	} else {
		if len(pTx.AuthorizationList) != 0 {
			return fmt.Errorf("%w: AuthorizationList length mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, len(pTx.AuthorizationList), 0)
		}
	}

	// 18. Unbound fields
	if pTx.MaxTimeUse != 0 {
		return fmt.Errorf("%w: MaxTimeUse mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, pTx.MaxTimeUse, 0)
	}
	if len(pTx.LastDeviceKey) != 0 {
		return fmt.Errorf("%w: LastDeviceKey mismatch", ErrEnvelopeBindingMismatch)
	}
	if len(pTx.NewDeviceKey) != 0 {
		return fmt.Errorf("%w: NewDeviceKey mismatch", ErrEnvelopeBindingMismatch)
	}
	if pTx.ReadOnly {
		return fmt.Errorf("%w: ReadOnly mismatch", ErrEnvelopeBindingMismatch)
	}

	return nil
}

// validateCanonicalEnvelopeBinding constructs the canonical protobuf transaction from ethTx
// and verifies every field matches pTx. Kept for differential testing against validateDirectEnvelopeBinding.
func validateCanonicalEnvelopeBinding(pTx *pb.Transaction, ethTx *e_types.Transaction) error {
	canonicalPb := &pb.Transaction{}
	var convErr error
	switch ethTx.Type() {
	case e_types.LegacyTxType:
		convErr = FromEthLegacyTx(ethTx, canonicalPb)
	case e_types.AccessListTxType:
		convErr = FromEthEIP2930Tx(ethTx, canonicalPb)
	case e_types.DynamicFeeTxType:
		convErr = FromEthEIP1559Tx(ethTx, canonicalPb)
	case e_types.BlobTxType:
		convErr = FromEthBlobTx(ethTx, canonicalPb)
	case e_types.SetCodeTxType:
		convErr = FromEthSetCodeTx(ethTx, canonicalPb)
	default:
		convErr = errors.New("unsupported Ethereum transaction type")
	}
	if convErr != nil {
		return fmt.Errorf("%w: failed to convert EthTx to canonical proto: %v", ErrInvalidEnvelope, convErr)
	}

	// 1. FromAddress
	if !bytes.Equal(pTx.FromAddress, canonicalPb.FromAddress) {
		return fmt.Errorf("%w: FromAddress mismatch", ErrEnvelopeBindingMismatch)
	}

	// 2. ToAddress
	if !bytes.Equal(pTx.ToAddress, canonicalPb.ToAddress) {
		return fmt.Errorf("%w: ToAddress mismatch", ErrEnvelopeBindingMismatch)
	}

	// 3. Amount
	if !bytes.Equal(pTx.Amount, canonicalPb.Amount) {
		return fmt.Errorf("%w: Amount mismatch", ErrEnvelopeBindingMismatch)
	}

	// 4. Nonce
	if !bytes.Equal(pTx.Nonce, canonicalPb.Nonce) {
		return fmt.Errorf("%w: Nonce mismatch", ErrEnvelopeBindingMismatch)
	}

	// 5. MaxGas
	if pTx.MaxGas != canonicalPb.MaxGas {
		return fmt.Errorf("%w: MaxGas mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, pTx.MaxGas, canonicalPb.MaxGas)
	}

	// 6. MaxGasPrice
	if pTx.MaxGasPrice != canonicalPb.MaxGasPrice {
		return fmt.Errorf("%w: MaxGasPrice mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, pTx.MaxGasPrice, canonicalPb.MaxGasPrice)
	}

	// 7. GasFeeCap
	if !bytes.Equal(pTx.GasFeeCap, canonicalPb.GasFeeCap) {
		return fmt.Errorf("%w: GasFeeCap mismatch", ErrEnvelopeBindingMismatch)
	}

	// 8. GasTipCap
	if !bytes.Equal(pTx.GasTipCap, canonicalPb.GasTipCap) {
		return fmt.Errorf("%w: GasTipCap mismatch", ErrEnvelopeBindingMismatch)
	}

	// 9. Data (covers CallData / DeployData)
	if !bytes.Equal(pTx.Data, canonicalPb.Data) {
		return fmt.Errorf("%w: Data mismatch", ErrEnvelopeBindingMismatch)
	}

	// 10. Type
	if pTx.Type != canonicalPb.Type {
		return fmt.Errorf("%w: Type mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, pTx.Type, canonicalPb.Type)
	}

	// 11. ChainID
	if pTx.ChainID != canonicalPb.ChainID {
		return fmt.Errorf("%w: ChainID mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, pTx.ChainID, canonicalPb.ChainID)
	}

	// 12. R, S, V
	if !bytes.Equal(pTx.R, canonicalPb.R) {
		return fmt.Errorf("%w: R mismatch", ErrEnvelopeBindingMismatch)
	}
	if !bytes.Equal(pTx.S, canonicalPb.S) {
		return fmt.Errorf("%w: S mismatch", ErrEnvelopeBindingMismatch)
	}
	if !bytes.Equal(pTx.V, canonicalPb.V) {
		return fmt.Errorf("%w: V mismatch", ErrEnvelopeBindingMismatch)
	}

	// 13. AccessList
	if len(pTx.AccessList) != len(canonicalPb.AccessList) {
		return fmt.Errorf("%w: AccessList length mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, len(pTx.AccessList), len(canonicalPb.AccessList))
	}
	for i := range pTx.AccessList {
		if !bytes.Equal(pTx.AccessList[i].Address, canonicalPb.AccessList[i].Address) {
			return fmt.Errorf("%w: AccessList[%d].Address mismatch", ErrEnvelopeBindingMismatch, i)
		}
		if len(pTx.AccessList[i].StorageKeys) != len(canonicalPb.AccessList[i].StorageKeys) {
			return fmt.Errorf("%w: AccessList[%d].StorageKeys length mismatch", ErrEnvelopeBindingMismatch, i)
		}
		for j := range pTx.AccessList[i].StorageKeys {
			if !bytes.Equal(pTx.AccessList[i].StorageKeys[j], canonicalPb.AccessList[i].StorageKeys[j]) {
				return fmt.Errorf("%w: AccessList[%d].StorageKeys[%d] mismatch", ErrEnvelopeBindingMismatch, i, j)
			}
		}
	}

	// 14. BlobVersionedHashes
	if len(pTx.BlobVersionedHashes) != len(canonicalPb.BlobVersionedHashes) {
		return fmt.Errorf("%w: BlobVersionedHashes length mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, len(pTx.BlobVersionedHashes), len(canonicalPb.BlobVersionedHashes))
	}
	for i := range pTx.BlobVersionedHashes {
		if !bytes.Equal(pTx.BlobVersionedHashes[i], canonicalPb.BlobVersionedHashes[i]) {
			return fmt.Errorf("%w: BlobVersionedHashes[%d] mismatch", ErrEnvelopeBindingMismatch, i)
		}
	}

	// 15. MaxFeePerBlobGas
	if !bytes.Equal(pTx.MaxFeePerBlobGas, canonicalPb.MaxFeePerBlobGas) {
		return fmt.Errorf("%w: MaxFeePerBlobGas mismatch", ErrEnvelopeBindingMismatch)
	}

	// 16. AuthorizationList
	if len(pTx.AuthorizationList) != len(canonicalPb.AuthorizationList) {
		return fmt.Errorf("%w: AuthorizationList length mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, len(pTx.AuthorizationList), len(canonicalPb.AuthorizationList))
	}
	for i := range pTx.AuthorizationList {
		a := pTx.AuthorizationList[i]
		e := canonicalPb.AuthorizationList[i]
		if a.ChainID != e.ChainID || !bytes.Equal(a.Address, e.Address) || a.Nonce != e.Nonce ||
			!bytes.Equal(a.YParity, e.YParity) || !bytes.Equal(a.R, e.R) || !bytes.Equal(a.S, e.S) {
			return fmt.Errorf("%w: AuthorizationList[%d] mismatch", ErrEnvelopeBindingMismatch, i)
		}
	}

	// 17. Sign (R || S || V)
	if len(canonicalPb.Sign) > 0 && !bytes.Equal(pTx.Sign, canonicalPb.Sign) {
		return fmt.Errorf("%w: Sign mismatch", ErrEnvelopeBindingMismatch)
	}

	// 18. Fields no EIP-2718 envelope can carry must keep their canonical (zero) values. They are still read by
	// execution (ReadOnly switches the VM to read-only mode, NewDeviceKey is written into the sender's account
	// state), so an attacker must not be able to set them on a copy of a genuinely signed envelope.
	if pTx.MaxTimeUse != canonicalPb.MaxTimeUse {
		return fmt.Errorf("%w: MaxTimeUse mismatch: got %d, expected %d", ErrEnvelopeBindingMismatch, pTx.MaxTimeUse, canonicalPb.MaxTimeUse)
	}
	if !bytes.Equal(pTx.LastDeviceKey, canonicalPb.LastDeviceKey) {
		return fmt.Errorf("%w: LastDeviceKey mismatch", ErrEnvelopeBindingMismatch)
	}
	if !bytes.Equal(pTx.NewDeviceKey, canonicalPb.NewDeviceKey) {
		return fmt.Errorf("%w: NewDeviceKey mismatch", ErrEnvelopeBindingMismatch)
	}
	if pTx.ReadOnly != canonicalPb.ReadOnly {
		return fmt.Errorf("%w: ReadOnly mismatch", ErrEnvelopeBindingMismatch)
	}

	return nil
}

// ValidateEnvelopeBinding validates that tx's proto fields match its RawEnvelope.
func ValidateEnvelopeBinding(tx types.Transaction) error {
	if tx == nil {
		return errors.New("transaction is nil")
	}
	pTx, ok := tx.Proto().(*pb.Transaction)
	if !ok || pTx == nil {
		return nil
	}
	if concrete, ok := tx.(*Transaction); ok && concrete.proto == pTx {
		return validateEnvelopeBindingTx(concrete)
	}
	return ValidateProtoEnvelopeBinding(pTx)
}

// ClassifyEthTxError maps an envelope validation, mempool, or execution error into an explicit typed TransactionError.
// It checks errors.As first for typed TransactionError instances, and falls back to string pattern matching.
func ClassifyEthTxError(err error) *TransactionError {
	if err == nil {
		return nil
	}
	var te *TransactionError
	if errors.As(err, &te) {
		return te
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "envelope binding") || strings.Contains(msg, "binding mismatch"):
		return ErrEnvelopeBindingMismatch
	case strings.Contains(msg, "raw envelope") || strings.Contains(msg, "invalid raw envelope"):
		return ErrInvalidEnvelope
	case strings.Contains(msg, "pre-eip-155"):
		return ErrPreEIP155
	case strings.Contains(msg, "chain id"):
		return InvalidChainId
	case strings.Contains(msg, "malleable signature") || strings.Contains(msg, "curve order / 2"):
		return ErrMalleableSignature
	case strings.Contains(msg, "recover sender") || strings.Contains(msg, "recovered sender"):
		return ErrSenderRecovery
	case strings.Contains(msg, "exceeds max") || strings.Contains(msg, "envelope size"):
		return ErrExceedsMaxEnvelopeSize
	case strings.Contains(msg, "too many transactions") || strings.Contains(msg, "max batch"):
		return ErrExceedsMaxBatchSize
	case strings.Contains(msg, "decode") || strings.Contains(msg, "rlp format"):
		return ErrDecodeRawEth
	case strings.Contains(msg, "already known") || strings.Contains(msg, "already exists"):
		return ErrAlreadyKnown
	case strings.Contains(msg, "nonce too high"):
		return ErrNonceTooHigh
	case strings.Contains(msg, "nonce too low") || strings.Contains(msg, "nonce"):
		return ErrNonceTooLow
	case strings.Contains(msg, "replacement transaction underpriced") || strings.Contains(msg, "underpriced"):
		return ErrReplacementUnderpriced
	case strings.Contains(msg, "intrinsic gas"):
		return ErrIntrinsicGasTooLow
	case strings.Contains(msg, "exceeds block gas limit"):
		return ErrExceedsBlockGasLimit
	case strings.Contains(msg, "max initcode size exceeded"):
		return ErrMaxInitCodeSizeExceeded
	case strings.Contains(msg, "gas limit reached"):
		return ErrGasLimitReached
	case strings.Contains(msg, "transaction type not supported"):
		return ErrTxTypeNotSupported
	case strings.Contains(msg, "invalid sender"):
		return ErrInvalidSender
	case strings.Contains(msg, "balance") || strings.Contains(msg, "funds") || strings.Contains(msg, "max fee"):
		return ErrInsufficientFunds
	case strings.Contains(msg, "gas price") || strings.Contains(msg, "base fee"):
		return InvalidMaxGasPrice
	default:
		return InvalidTransaction
	}
}

// GethStandardRPCError maps an error (typed TransactionError, wrapped error, or standard error)
// to the geth-standard JSON-RPC error code and message.
// Standard error code for tx rejection in geth is -32000.
func GethStandardRPCError(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	if errors.Is(err, ErrInvalidBlobProof) {
		return -32000, ErrInvalidBlobProof.Error()
	}
	var te *TransactionError
	if !errors.As(err, &te) {
		te = ClassifyEthTxError(err)
	}

	switch te.Code {
	case ErrNonceTooLow.Code, InvalidNonce.Code:
		return -32000, "nonce too low"
	case ErrNonceTooHigh.Code:
		return -32000, "nonce too high"
	case ErrInsufficientFunds.Code, ErrInsufficientBalance.Code, InvalidMaxFee.Code, InvalidAmount.Code:
		return -32000, "insufficient funds for gas * price + value"
	case ErrAlreadyKnown.Code:
		return -32000, "already known"
	case ErrReplacementUnderpriced.Code:
		return -32000, "replacement transaction underpriced"
	case ErrIntrinsicGasTooLow.Code, InvalidMaxGas.Code:
		return -32000, "intrinsic gas too low"
	case ErrExceedsBlockGasLimit.Code:
		return -32000, "exceeds block gas limit"
	case ErrInvalidSender.Code, InvalidSign.Code, InvalidSignSecp.Code, ErrSenderRecovery.Code:
		return -32000, "invalid sender"
	case ErrTxTypeNotSupported.Code:
		return -32000, "transaction type not supported"
	case ErrMaxInitCodeSizeExceeded.Code:
		return -32000, "max initcode size exceeded"
	case ErrGasLimitReached.Code:
		return -32000, "gas limit reached"
	case InvalidMaxGasPrice.Code:
		return -32000, "transaction underpriced"
	case InvalidChainId.Code:
		return -32000, "invalid chain id"
	case ErrPreEIP155.Code:
		return -32000, "pre-EIP-155 unprotected transactions are not allowed"
	case ErrMalleableSignature.Code:
		return -32000, "malleable signature: s exceeds curve order / 2 (EIP-2)"
	case ErrExceedsMaxEnvelopeSize.Code:
		return -32000, "transaction envelope exceeds maximum allowed size"
	case ErrDecodeRawEth.Code:
		return -32000, "failed to decode raw Ethereum transaction envelope"
	case ErrEnvelopeBindingMismatch.Code:
		return -32000, "transaction fields do not match raw envelope"
	case ErrInvalidEnvelope.Code:
		return -32000, "invalid raw envelope bytes"
	default:
		return -32000, te.Description
	}
}
