package transaction

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

// ToEthSetCodeTx converts an internal Transaction into a go-ethereum EIP-7702
// SetCode transaction.
func ToEthSetCodeTx(tx *pb.Transaction) *types.Transaction {
	if tx.Type != types.SetCodeTxType {
		return nil
	}
	if tx.ChainID == 0 {
		return nil
	}
	if len(tx.ToAddress) == 0 {
		// SetCode txs can never be contract creation (EIP-7702).
		return nil
	}
	if tx.GasFeeCap == nil || tx.GasTipCap == nil {
		return nil
	}
	if len(tx.AuthorizationList) == 0 {
		return nil
	}

	toAddress := common.BytesToAddress(tx.ToAddress)

	var accessList types.AccessList
	if len(tx.AccessList) > 0 {
		accessList = toEthAccessList(tx.AccessList)
	}

	var nonceUint64 uint64
	if len(tx.Nonce) > 0 {
		nonceUint64 = new(big.Int).SetBytes(tx.Nonce).Uint64()
	}

	// tx.Data is the marshaled internal CallData proto (see FromEthSetCodeTx),
	// not raw EVM calldata — must unwrap it the same way ToEthTransaction()'s
	// IsCallContract()/CallData() branch does for other tx types. SetCode txs
	// can never be contract creation (enforced above), so IsCallContract() here
	// reduces to "Data is non-empty".
	data := tx.Data
	if len(data) > 0 {
		callData := &CallData{}
		if err := callData.Unmarshal(data); err == nil {
			data = callData.Input()
		}
	}

	innerTxData := &types.SetCodeTx{
		ChainID:    uint256.NewInt(tx.ChainID),
		Nonce:      nonceUint64,
		GasTipCap:  uint256.MustFromBig(new(big.Int).SetBytes(tx.GasTipCap)),
		GasFeeCap:  uint256.MustFromBig(new(big.Int).SetBytes(tx.GasFeeCap)),
		Gas:        tx.MaxGas,
		To:         toAddress,
		Value:      uint256.MustFromBig(new(big.Int).SetBytes(tx.Amount)),
		Data:       data,
		AccessList: accessList,
		AuthList:   ToEthAuthorizationList(tx.AuthorizationList),
	}

	sigV, sigR, sigS := extractSignature(tx)
	if sigR != nil && sigS != nil && sigV != nil {
		innerTxData.V = uint256.MustFromBig(sigV)
		innerTxData.R = uint256.MustFromBig(sigR)
		innerTxData.S = uint256.MustFromBig(sigS)
	}

	return types.NewTx(innerTxData)
}

// FromEthSetCodeTx converts a go-ethereum SetCode transaction into the
// internal protobuf Transaction. Structural conversion plus the shape rules
// EIP-7702 fixes unconditionally (no contract creation, at least one
// authorization tuple) — signature/chainID/nonce validation and applying the
// delegation designator to the authority's account are the execution
// pipeline's job (see pkg/blockchain/tx_processor/authorization.go), since
// they require state access this package doesn't otherwise have.
func FromEthSetCodeTx(ethTx *types.Transaction, pTx *pb.Transaction) error {
	if ethTx.Type() != types.SetCodeTxType {
		return errors.New("not an EIP-7702 transaction")
	}

	to := ethTx.To()
	if to == nil || *to == (common.Address{}) {
		return errors.New("EIP-7702 transaction cannot be a contract creation")
	}
	authList := ethTx.SetCodeAuthorizations()
	if len(authList) == 0 {
		return errors.New("EIP-7702 transaction must carry at least one authorization tuple")
	}

	pTx.Type = types.SetCodeTxType
	pTx.ToAddress = to.Bytes()
	pTx.Amount = ethTx.Value().Bytes()
	pTx.MaxGas = ethTx.Gas()

	nonceValue := ethTx.Nonce()
	nonceBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(nonceBytes, nonceValue)
	pTx.Nonce = nonceBytes

	// SetCode txs always have a real recipient (checked above), so this always
	// takes the CallData branch — the deploy-address argument is unreachable.
	data, err := prepareTransactionSpecificData(ethTx, common.Address{})
	if err != nil {
		return err
	}
	pTx.Data = data

	txChainID := ethTx.ChainId()
	if txChainID == nil || txChainID.Sign() <= 0 {
		return errors.New("EIP-7702 transaction is missing ChainID")
	}
	pTx.ChainID = txChainID.Uint64()

	if ethTx.GasTipCap() != nil {
		pTx.GasTipCap = ethTx.GasTipCap().Bytes()
	}
	if ethTx.GasFeeCap() != nil {
		pTx.GasFeeCap = ethTx.GasFeeCap().Bytes()
	}
	pTx.MaxGasPrice = 0

	pTx.AuthorizationList = fromEthAuthorizationList(authList)

	if len(ethTx.AccessList()) > 0 {
		pTx.AccessList = make([]*pb.AccessTuple, len(ethTx.AccessList()))
		for i, item := range ethTx.AccessList() {
			storageKeys := make([][]byte, len(item.StorageKeys))
			for j, key := range item.StorageKeys {
				storageKeys[j] = key.Bytes()
			}
			pTx.AccessList[i] = &pb.AccessTuple{
				Address:     item.Address.Bytes(),
				StorageKeys: storageKeys,
			}
		}
	} else {
		pTx.AccessList = nil
	}

	v, r, s := ethTx.RawSignatureValues()
	if v != nil && r != nil && s != nil {
		pTx.R = r.Bytes()
		pTx.S = s.Bytes()
		pTx.V = v.Bytes()

		signer := types.NewPragueSigner(txChainID)
		fromAddress, err := types.Sender(signer, ethTx)
		if err != nil {
			return fmt.Errorf("error deriving sender for EIP-7702 transaction: %w", err)
		}
		pTx.FromAddress = fromAddress.Bytes()

		var sig []byte
		sig = append(sig, r.Bytes()...)
		sig = append(sig, s.Bytes()...)
		if len(v.Bytes()) > 0 {
			sig = append(sig, v.Bytes()[len(v.Bytes())-1])
		} else {
			sig = append(sig, 0)
		}
		pTx.Sign = sig
	} else {
		pTx.R = nil
		pTx.S = nil
		pTx.V = nil
		pTx.Sign = nil
	}
	return nil
}

// ToEthAuthorizationList converts an EIP-7702 authorization list from its
// protobuf representation into go-ethereum's types.SetCodeAuthorization,
// preserving byte-for-byte the signature fields Authority() needs to recover
// the authorizing account. Exported so pkg/blockchain/tx_processor can reuse
// go-ethereum's own EIP-2098-independent ecrecover logic instead of
// reimplementing it — see authorization.go's processAuthorizationList.
func ToEthAuthorizationList(list []*pb.SetCodeAuthorization) []types.SetCodeAuthorization {
	out := make([]types.SetCodeAuthorization, len(list))
	for i, a := range list {
		out[i] = types.SetCodeAuthorization{
			ChainID: *uint256.NewInt(a.ChainID),
			Address: common.BytesToAddress(a.Address),
			Nonce:   a.Nonce,
			V:       yParityByte(a.YParity),
			R:       *uint256.MustFromBig(new(big.Int).SetBytes(a.R)),
			S:       *uint256.MustFromBig(new(big.Int).SetBytes(a.S)),
		}
	}
	return out
}

func fromEthAuthorizationList(list []types.SetCodeAuthorization) []*pb.SetCodeAuthorization {
	out := make([]*pb.SetCodeAuthorization, len(list))
	for i, a := range list {
		out[i] = &pb.SetCodeAuthorization{
			ChainID: a.ChainID.Uint64(),
			Address: a.Address.Bytes(),
			Nonce:   a.Nonce,
			YParity: []byte{a.V},
			R:       a.R.Bytes(),
			S:       a.S.Bytes(),
		}
	}
	return out
}

func yParityByte(b []byte) uint8 {
	if len(b) == 0 {
		return 0
	}
	return b[len(b)-1]
}
