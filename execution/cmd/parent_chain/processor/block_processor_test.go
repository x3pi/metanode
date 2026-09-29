package processor

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

func createExecutableTx(t *testing.T, tx *parentchain.ParentChainTx) *pb.TransactionExe {
	data, err := tx.Marshal()
	assert.NoError(t, err)

	pbTx := &pb.Transaction{
		Data: data,
	}
	pbTxBytes, err := proto.Marshal(pbTx)
	assert.NoError(t, err)

	return &pb.TransactionExe{
		Digest: pbTxBytes,
	}
}

func TestBlockProcessor_SubmitStateRoot(t *testing.T) {
	store := parentchain.NewMemoryStore()

	txResults := make(map[common.Hash]error)
	bp := NewBlockProcessor(store, func(msgID common.Hash, err error) {
		txResults[msgID] = err
	})

	blsKey := bls.GenerateKeyPair()
	pubKey := blsKey.PublicKey()
	clusterHash := crypto.Keccak256Hash(pubKey[:])

	// 1. First register the cluster via DepositToFloat
	depositMsgID := common.HexToHash("0x1111")
	depositTx := &parentchain.ParentChainTx{
		Type:      parentchain.TxTypeDepositToFloat,
		PubKey:    pubKey[:],
		ClusterID: 101,
		Sender:    common.HexToAddress("0xaaaa"),
		Target:    common.HexToAddress("0xbbbb"),
		Amount:    big.NewInt(1000),
		MsgID:     depositMsgID,
	}

	// 2. Submit state root for epoch 1
	epoch := uint64(1)
	stateRoot := common.HexToHash("0x99998888")
	digest := parentchain.ComputeSubmitStateRootMessage(pubKey, epoch, stateRoot)
	sig := bls.Sign(blsKey.PrivateKey(), digest)
	rootMsgID := common.HexToHash("0x2222")

	rootTx := &parentchain.ParentChainTx{
		Type:      parentchain.TxTypeSubmitStateRoot,
		PubKey:    pubKey[:],
		Epoch:     epoch,
		StateRoot: stateRoot,
		Cert:      sig[:],
		MsgID:     rootMsgID,
	}

	block := &pb.ExecutableBlock{
		BlockNumber:        1,
		GlobalExecIndex:    1,
		CommitTimestampMs:  uint64(time.Now().UnixMilli()),
		Transactions: []*pb.TransactionExe{
			createExecutableTx(t, depositTx),
			createExecutableTx(t, rootTx),
		},
	}

	bp.processBlock(block)

	// Verify deposit succeeded
	assert.Nil(t, txResults[depositMsgID])
	bal, err := store.GetFloat(clusterHash)
	assert.NoError(t, err)
	assert.Equal(t, big.NewInt(1000), bal)

	// Verify state root was submitted and saved
	assert.Nil(t, txResults[rootMsgID])
	savedRoot, found, err := store.GetStateRoot(clusterHash, epoch)
	assert.NoError(t, err)
	assert.True(t, found, "state root should be found in store")
	assert.Equal(t, stateRoot, savedRoot)
}

func TestBlockProcessor_SubmitStateRoot_Errors(t *testing.T) {
	store := parentchain.NewMemoryStore()

	txResults := make(map[common.Hash]error)
	bp := NewBlockProcessor(store, func(msgID common.Hash, err error) {
		txResults[msgID] = err
	})

	blsKey := bls.GenerateKeyPair()
	pubKey := blsKey.PublicKey()

	// Case 1: Unknown cluster (no deposit/register yet)
	epoch := uint64(1)
	stateRoot := common.HexToHash("0x1111")
	digest := parentchain.ComputeSubmitStateRootMessage(pubKey, epoch, stateRoot)
	sig := bls.Sign(blsKey.PrivateKey(), digest)
	rootMsgID1 := common.HexToHash("0xaaaa")

	rootTx1 := &parentchain.ParentChainTx{
		Type:      parentchain.TxTypeSubmitStateRoot,
		PubKey:    pubKey[:],
		Epoch:     epoch,
		StateRoot: stateRoot,
		Cert:      sig[:],
		MsgID:     rootMsgID1,
	}

	block1 := &pb.ExecutableBlock{
		BlockNumber:        1,
		GlobalExecIndex:    1,
		CommitTimestampMs:  uint64(time.Now().UnixMilli()),
		Transactions: []*pb.TransactionExe{
			createExecutableTx(t, rootTx1),
		},
	}
	bp.processBlock(block1)
	assert.Error(t, txResults[rootMsgID1])
	assert.Contains(t, txResults[rootMsgID1].Error(), "unknown cluster")

	// Now register cluster
	depMsgID := common.HexToHash("0xdddd")
	depTx := &parentchain.ParentChainTx{
		Type:      parentchain.TxTypeDepositToFloat,
		PubKey:    pubKey[:],
		ClusterID: 101,
		Sender:    common.HexToAddress("0xaaaa"),
		Target:    common.HexToAddress("0xbbbb"),
		Amount:    big.NewInt(500),
		MsgID:     depMsgID,
	}

	// Case 2: Invalid signature
	rootMsgID2 := common.HexToHash("0xbbbb")
	otherKey := bls.GenerateKeyPair()
	badSig := bls.Sign(otherKey.PrivateKey(), digest)
	rootTx2 := &parentchain.ParentChainTx{
		Type:      parentchain.TxTypeSubmitStateRoot,
		PubKey:    pubKey[:],
		Epoch:     epoch,
		StateRoot: stateRoot,
		Cert:      badSig[:],
		MsgID:     rootMsgID2,
	}

	block2 := &pb.ExecutableBlock{
		BlockNumber:        2,
		GlobalExecIndex:    2,
		CommitTimestampMs:  uint64(time.Now().UnixMilli()),
		Transactions: []*pb.TransactionExe{
			createExecutableTx(t, depTx),
			createExecutableTx(t, rootTx2),
		},
	}
	bp.processBlock(block2)
	assert.NoError(t, txResults[depMsgID])
	assert.Error(t, txResults[rootMsgID2])
	assert.Contains(t, txResults[rootMsgID2].Error(), "invalid signature")
}
