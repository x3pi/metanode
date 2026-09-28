package parentchain

import (
	"encoding/json"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

type TxType string

const (
	TxTypeDepositToFloat  TxType = "DepositToFloat"
	TxTypeTransferFloat   TxType = "TransferFloat"
	TxTypeMarkClaimed     TxType = "MarkClaimed"
	TxTypeReclaimFloat    TxType = "ReclaimFloat"
	TxTypeRegisterAccount TxType = "RegisterAccount"
	TxTypeSubmitStateRoot TxType = "SubmitStateRoot"
)

// ParentChainTx represents a native transaction on the Parent Chain.
type ParentChainTx struct {
	Type TxType `json:"type"`

	// Common fields
	MsgID common.Hash `json:"msgID,omitempty"`
	Cert  []byte      `json:"cert,omitempty"` // For cm.Sign

	// DepositToFloat fields
	PubKey   []byte   `json:"pubKey,omitempty"` // For cm.PublicKey
	ChainID  uint64   `json:"chainID,omitempty"`
	Amount   *big.Int `json:"amount,omitempty"`

	// TransferFloat fields
	ToPubKey    []byte         `json:"toPubKey,omitempty"` // For cm.PublicKey
	Sender      common.Address `json:"sender,omitempty"`
	Target      common.Address `json:"target,omitempty"`
	Payload     []byte         `json:"payload,omitempty"`
	IsRefund    bool           `json:"isRefund,omitempty"`
	Nonce       uint64         `json:"nonce,omitempty"`
	Fee         *big.Int       `json:"fee,omitempty"`

	// MarkClaimed / ReclaimFloat fields
	Outcome FloatOutcome `json:"outcome,omitempty"`
	
	// RegisterAccount fields
	UserAddress common.Address `json:"userAddress,omitempty"`
	UserSig     []byte         `json:"userSig,omitempty"`

	// SubmitStateRoot fields
	Epoch     uint64      `json:"epoch,omitempty"`
	StateRoot common.Hash `json:"stateRoot,omitempty"`
}

func (tx *ParentChainTx) Marshal() ([]byte, error) {
	return json.Marshal(tx)
}

func UnmarshalParentChainTx(data []byte) (*ParentChainTx, error) {
	var tx ParentChainTx
	err := json.Unmarshal(data, &tx)
	if err != nil {
		return nil, err
	}
	return &tx, nil
}
