package parentchain

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// TransferEvent represents an observed incoming transfer on the Parent Chain.
type TransferEvent struct {
	MsgID       common.Hash
	SourcePubKey cm.PublicKey
	DestPubKey   cm.PublicKey
	SourceSeq   uint64
	Sender      common.Address
	Target      common.Address
	Amount      *big.Int
	PayloadHash common.Hash
	BlockTime   uint64
	IsRefund    bool
}

// Client is the minimal RPC client interface to interact with Parent Chain.
type Client interface {
	SendTransferFloat(
		pubKey, destPubKey cm.PublicKey,
		destChainID uint64,
		sender, target common.Address,
		amount, gasFee *big.Int,
		nonce uint64,
		cert []byte,
		isRefund bool,
	) (common.Hash, error)

	SendMarkClaimed(msgID common.Hash, outcome FloatOutcome, cert []byte) (common.Hash, error)
	
	SendReclaimFloat(msgID common.Hash, cert []byte) (common.Hash, error)
	
	// GetInboundTransfers returns incoming transfers and the new cursor
	GetInboundTransfers(pubKey cm.PublicKey, cursor uint64) ([]*TransferEvent, uint64, error)
	
	GetTransferRecord(msgID common.Hash) (FloatTransferRecord, bool, error)
	GetClaimed(msgID common.Hash) (FloatOutcome, error)
	GetFloatSeq(pubKey cm.PublicKey) (uint64, error)
}
