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
	SendDepositToFloat(
		pubKey cm.PublicKey,
		destClusterID uint64,
		sender, target common.Address,
		amount *big.Int,
	) (common.Hash, error)

	SendTransferFloat(
		pubKey, destPubKey cm.PublicKey,
		destClusterID uint64,
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
	
	SendRegisterAccount(userAddress common.Address, floatIdentityKey cm.PublicKey, userSig []byte, clusterSig cm.Sign) (common.Hash, error)
	GetAccountRegistry(userAddress common.Address) (cm.PublicKey, bool, error)

	SendSubmitStateRoot(clusterPubKey cm.PublicKey, epoch uint64, stateRoot common.Hash, cert cm.Sign) (common.Hash, error)
	GetStateRoot(clusterPubKey cm.PublicKey, epoch uint64) (common.Hash, bool, error)

	GetBlockByNumber(number uint64) (BlockRecord, bool, error)
	GetBlockByHash(hash common.Hash) (BlockRecord, bool, error)
	GetTransaction(txHash common.Hash) (uint64, uint32, bool, error)
	GetReceipt(txHash common.Hash) (*Receipt, bool, error)
	GetStatus() (ChainStatus, error)
	GetProof(key [32]byte) (ProofResult, error)
	SendRawTransaction(rawTx []byte) (common.Hash, error)
}

type ChainStatus struct {
	LastBlock    uint64      `json:"last_block"`
	LastHash     common.Hash `json:"last_hash"`
	StateRoot    common.Hash `json:"state_root"`
	Syncing      bool        `json:"syncing"`
	ForkDetected bool        `json:"fork_detected"`
}

type ProofResult struct {
	Key       common.Hash `json:"key"`
	Proof     []byte      `json:"proof"`
	StateRoot common.Hash `json:"state_root"`
	Verified  bool        `json:"verified"`
}

