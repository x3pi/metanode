package proto // Phải trùng tên package với file .pb.go

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// --- Extension cho BurnPendingTx ---

// GetAmountBigInt converts Amount bytes to *big.Int
func (x *BurnPendingTx) GetAmountBigInt() *big.Int {
	if x == nil || x.Amount == nil {
		return big.NewInt(0)
	}
	return new(big.Int).SetBytes(x.Amount)
}

// GetFromAddressCommon converts FromAddress bytes to common.Address
func (x *BurnPendingTx) GetFromAddressCommon() common.Address {
	if x == nil || x.FromAddress == nil {
		return common.Address{}
	}
	return common.BytesToAddress(x.FromAddress)
}

// GetToAddressCommon converts ToAddress bytes to common.Address
func (x *BurnPendingTx) GetToAddressCommon() common.Address {
	if x == nil || x.ToAddress == nil {
		return common.Address{}
	}
	return common.BytesToAddress(x.ToAddress)
}

// GetTxHashCommon converts TxHash bytes to common.Hash
func (x *BurnPendingTx) GetTxHashCommon() common.Hash {
	if x == nil || x.TxHash == nil {
		return common.Hash{}
	}
	return common.BytesToHash(x.TxHash)
}

// --- Extension cho VerifyTransactionRequest ---
func (x *VerifyTransactionRequest) GetAmountBigInt() *big.Int {
	if x == nil || len(x.Amount) == 0 {
		return big.NewInt(0)
	}
	return new(big.Int).SetBytes(x.Amount)
}

func (x *VerifyTransactionRequest) GetFromAddressCommon() common.Address {
	// BytesToAddress rất nhanh, chi phí gần như bằng 0
	return common.BytesToAddress(x.FromAddress)
}

func (x *VerifyTransactionRequest) GetToAddressCommon() common.Address {
	return common.BytesToAddress(x.ToAddress)
}

func (x *VerifyTransactionRequest) GetTxHashCommon() common.Hash {
	return common.BytesToHash(x.TxHash)
}

// --- Extension cho MintWaitingTx ---

func (x *MintWaitingTx) GetAmountBigInt() *big.Int {
	if x == nil || len(x.Amount) == 0 {
		return big.NewInt(0)
	}
	return new(big.Int).SetBytes(x.Amount)
}
