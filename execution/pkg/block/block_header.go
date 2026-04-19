package block

import (
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

type BlockHeader struct {
	lastBlockHash      common.Hash
	blockNumber        uint64
	accountStatesRoot  common.Hash
	stakeStatesRoot    common.Hash // Bổ sung StakeStatesRoot
	receiptRoot        common.Hash
	leaderAddress      common.Address
	timeStamp          uint64
	aggregateSignature []byte
	transactionsRoot   common.Hash
	epoch              uint64 // Bổ sung epoch field
	globalExecIndex    uint64 // Maps Go block number → Rust consensus commit index
}

func NewBlockHeader(
	lastBlockHash common.Hash,
	blockNumber uint64,
	accountStatesRoot common.Hash,
	stakeStatesRoot common.Hash,
	receiptRoot common.Hash,
	leaderAddress common.Address,
	timeStamp uint64,
	transactionsRoot common.Hash,
	epoch uint64,
	globalExecIndex ...uint64, // Optional: maps Go block → Rust commit index
) *BlockHeader {
	var gei uint64
	if len(globalExecIndex) > 0 {
		gei = globalExecIndex[0]
	}
	return &BlockHeader{
		lastBlockHash:     lastBlockHash,
		blockNumber:       blockNumber,
		accountStatesRoot: accountStatesRoot,
		stakeStatesRoot:   stakeStatesRoot,
		receiptRoot:       receiptRoot,
		leaderAddress:     leaderAddress,
		timeStamp:         timeStamp,
		transactionsRoot:  transactionsRoot,
		epoch:             epoch,
		globalExecIndex:   gei,
	}
}

func (b *BlockHeader) LastBlockHash() common.Hash {
	return b.lastBlockHash
}

func (b *BlockHeader) BlockNumber() uint64 {
	return b.blockNumber
}

func (b *BlockHeader) AccountStatesRoot() common.Hash {
	return b.accountStatesRoot
}

func (b *BlockHeader) StakeStatesRoot() common.Hash { // Bổ sung StakeStatesRoot
	return b.stakeStatesRoot
}

func (b *BlockHeader) ReceiptRoot() common.Hash {
	return b.receiptRoot
}

func (b *BlockHeader) TransactionsRoot() common.Hash {
	return b.transactionsRoot
}

func (b *BlockHeader) LeaderAddress() common.Address {
	return b.leaderAddress
}

func (b *BlockHeader) TimeStamp() uint64 {
	return b.timeStamp
}

func (b *BlockHeader) AggregateSignature() []byte {
	return b.aggregateSignature
}

func (b *BlockHeader) Epoch() uint64 {
	return b.epoch
}

func (b *BlockHeader) GlobalExecIndex() uint64 {
	return b.globalExecIndex
}

func (b *BlockHeader) SetGlobalExecIndex(gei uint64) {
	b.globalExecIndex = gei
}

func (b *BlockHeader) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(b.Proto())
}

func (b *BlockHeader) Unmarshal(bData []byte) error {
	pbBlockHeader := &pb.BlockHeader{}
	if err := proto.Unmarshal(bData, pbBlockHeader); err != nil {
		return err
	}
	b.FromProto(pbBlockHeader)
	return nil
}

func (b *BlockHeader) Hash() common.Hash {
	// FORK-SAFETY: Hash computed WITHOUT lastBlockHash to prevent
	// hash chain divergence when nodes restart/sync from different points.
	// lastBlockHash is still stored in the header for chain linking,
	// but does NOT affect the block identity hash.
	// NOTE: GlobalExecIndex IS included — it acts as a fork-detection canary.
	// If GEI diverges between nodes, hash mismatch alerts to a problem.
	pbHeader := &pb.BlockHeader{
		// NOTE: LastBlockHash deliberately EXCLUDED from hash
		BlockNumber:       b.blockNumber,
		AccountStatesRoot: b.accountStatesRoot.Bytes(),
		StakeStatesRoot:   b.stakeStatesRoot.Bytes(),
		ReceiptRoot:       b.receiptRoot.Bytes(),
		LeaderAddress:     b.leaderAddress.Bytes(),
		TimeStamp:         b.timeStamp,
		TransactionsRoot:  b.transactionsRoot.Bytes(),
		Epoch:             b.epoch,
		GlobalExecIndex:   b.globalExecIndex,
	}
	bData, _ := proto.MarshalOptions{Deterministic: true}.Marshal(pbHeader)
	return crypto.Keccak256Hash(bData)
}

func (b *BlockHeader) Proto() *pb.BlockHeader {
	return &pb.BlockHeader{
		LastBlockHash:     b.lastBlockHash.Bytes(),
		BlockNumber:       b.blockNumber,
		AccountStatesRoot: b.accountStatesRoot.Bytes(),
		StakeStatesRoot:   b.stakeStatesRoot.Bytes(),
		ReceiptRoot:       b.receiptRoot.Bytes(),
		LeaderAddress:     b.leaderAddress.Bytes(),
		TimeStamp:         b.timeStamp,
		TransactionsRoot:  b.transactionsRoot.Bytes(),
		Epoch:             b.epoch,
		GlobalExecIndex:   b.globalExecIndex,
	}
}

func (b *BlockHeader) FromProto(pbBlockHeader *pb.BlockHeader) {
	b.lastBlockHash = common.BytesToHash(pbBlockHeader.LastBlockHash)
	b.blockNumber = pbBlockHeader.BlockNumber
	b.accountStatesRoot = common.BytesToHash(pbBlockHeader.AccountStatesRoot)
	b.stakeStatesRoot = common.BytesToHash(pbBlockHeader.StakeStatesRoot)
	b.receiptRoot = common.BytesToHash(pbBlockHeader.ReceiptRoot)
	b.leaderAddress = common.BytesToAddress(pbBlockHeader.LeaderAddress)
	b.timeStamp = pbBlockHeader.TimeStamp
	b.aggregateSignature = pbBlockHeader.AggregateSignature
	b.transactionsRoot = common.BytesToHash(pbBlockHeader.TransactionsRoot)
	b.epoch = pbBlockHeader.Epoch
	b.globalExecIndex = pbBlockHeader.GlobalExecIndex
}

func (b *BlockHeader) SetAccountStatesRoot(hash common.Hash) {
	b.accountStatesRoot = hash
}

func (b *BlockHeader) SetStakeStatesRoot(hash common.Hash) {
	b.stakeStatesRoot = hash
}

// SetAggregateSignature sets the aggregate BLS signature on the block header.
// Called by Master node after signing the block hash.
func (b *BlockHeader) SetAggregateSignature(sig []byte) {
	b.aggregateSignature = sig
}

// HashWithoutSignature computes the block hash EXCLUDING the AggregateSignature field.
// This is used for signing: hash the block without sig → sign the hash → set sig.
// Without this, the hash would change after setting the signature, making verification impossible.
func (b *BlockHeader) HashWithoutSignature() common.Hash {
	// FORK-SAFETY: Hash computed WITHOUT lastBlockHash AND AggregateSignature.
	// This matches Hash() which also excludes lastBlockHash.
	// GlobalExecIndex IS included as a fork-detection canary.
	pbHeader := &pb.BlockHeader{
		// NOTE: LastBlockHash deliberately EXCLUDED (fork-safety)
		BlockNumber:       b.blockNumber,
		AccountStatesRoot: b.accountStatesRoot.Bytes(),
		StakeStatesRoot:   b.stakeStatesRoot.Bytes(),
		ReceiptRoot:       b.receiptRoot.Bytes(),
		LeaderAddress:     b.leaderAddress.Bytes(),
		TimeStamp:         b.timeStamp,
		TransactionsRoot:  b.transactionsRoot.Bytes(),
		Epoch:             b.epoch,
		GlobalExecIndex:   b.globalExecIndex,
		// NOTE: AggregateSignature deliberately EXCLUDED
	}
	bData, _ := proto.MarshalOptions{Deterministic: true}.Marshal(pbHeader)
	return crypto.Keccak256Hash(bData)
}

func (b *BlockHeader) String() string {
	str := fmt.Sprintf(`
BlockHeader{
  LastBlockHash: %v,
  BlockNumber: %d,
  AccountStatesRoot: %v,
  StakeStatesRoot: %v,
  ReceiptRoot: %v,
  TransactionsRoot: %v,
  LeaderAddress: %v,
  TimeStamp: %d,
  AggregateSignature: %v,
  Epoch: %d,
  GlobalExecIndex: %d
}
`, b.lastBlockHash, b.blockNumber, b.accountStatesRoot, b.stakeStatesRoot, b.receiptRoot, b.TransactionsRoot(), b.leaderAddress, b.timeStamp, b.aggregateSignature, b.epoch, b.globalExecIndex)
	return str
}
