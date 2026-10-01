package processor

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/executor"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

const maxTxPerBatch = 2000

// TxBatcher collects ParentChainTxs from RPC, batches them, and submits them to Rust consensus.
type TxBatcher struct {
	txChan      chan *parentchain.ParentChainTx
	protoTxChan chan *pb.Transaction
	stop        chan struct{}
}

func NewTxBatcher(queueSize int) *TxBatcher {
	return &TxBatcher{
		txChan:      make(chan *parentchain.ParentChainTx, queueSize),
		protoTxChan: make(chan *pb.Transaction, queueSize),
		stop:        make(chan struct{}),
	}
}

// Chan returns the channel HTTPServer writes accepted txs into directly.
func (tb *TxBatcher) Chan() chan *parentchain.ParentChainTx {
	return tb.txChan
}

// ProtoChan returns the channel for protobuf raw transactions.
func (tb *TxBatcher) ProtoChan() chan *pb.Transaction {
	return tb.protoTxChan
}

func (tb *TxBatcher) SubmitTx(tx *parentchain.ParentChainTx) error {
	select {
	case tb.txChan <- tx:
		return nil
	default:
		return fmt.Errorf("parent chain tx queue is full")
	}
}

func (tb *TxBatcher) SubmitProtoTx(tx *pb.Transaction) error {
	select {
	case tb.protoTxChan <- tx:
		return nil
	default:
		return fmt.Errorf("parent chain proto tx queue is full")
	}
}

func (tb *TxBatcher) Start() {
	go tb.batchingLoop()
}

func (tb *TxBatcher) Stop() {
	close(tb.stop)
}

func (tb *TxBatcher) batchingLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var pending []*pb.Transaction

	for {
		select {
		case <-tb.stop:
			return
		case tx := <-tb.txChan:
			pbTx := convertParentChainTxToProto(tx)
			pending = append(pending, pbTx)
		drainLoop:
			for len(pending) < maxTxPerBatch {
				select {
				case extra := <-tb.txChan:
					pending = append(pending, convertParentChainTxToProto(extra))
				case extraProto := <-tb.protoTxChan:
					pending = append(pending, extraProto)
				default:
					break drainLoop
				}
			}
			tb.submitBatch(pending)
			pending = nil
		case ptx := <-tb.protoTxChan:
			pending = append(pending, ptx)
		drainProtoLoop:
			for len(pending) < maxTxPerBatch {
				select {
				case extraProto := <-tb.protoTxChan:
					pending = append(pending, extraProto)
				case extra := <-tb.txChan:
					pending = append(pending, convertParentChainTxToProto(extra))
				default:
					break drainProtoLoop
				}
			}
			tb.submitBatch(pending)
			pending = nil
		case <-ticker.C:
			if len(pending) > 0 {
				tb.submitBatch(pending)
				pending = nil
			}
		}
	}
}

func convertParentChainTxToProto(tx *parentchain.ParentChainTx) *pb.Transaction {
	var callData []byte
	var senderAddr []byte
	var senderKey []byte
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], tx.Nonce)

	switch tx.Type {
	case parentchain.TxTypeSubmitStateRoot:
		var pubKey cm.PublicKey
		copy(pubKey[:], tx.PubKey)
		callData = parentchain.EncodeSubmitStateRootCallData(pubKey, tx.Epoch, tx.StateRoot, cm.SignFromBytes(tx.Cert))
		senderKey = tx.PubKey
		senderAddr = crypto.Keccak256(tx.PubKey)[12:]

	case parentchain.TxTypeRegisterCluster:
		var pubKey cm.PublicKey
		copy(pubKey[:], tx.PubKey)
		callData = parentchain.EncodeRegisterClusterCallData(pubKey, tx.ClusterID)
		senderKey = tx.PubKey
		senderAddr = crypto.Keccak256(tx.PubKey)[12:]

	case parentchain.TxTypeRegisterAccount:
		var floatKey cm.PublicKey
		copy(floatKey[:], tx.PubKey)
		callData = parentchain.EncodeRegisterAccountCallData(tx.UserAddress, floatKey, tx.UserSig)
		senderKey = tx.PubKey
		senderAddr = crypto.Keccak256(tx.PubKey)[12:]

	case parentchain.TxTypeDepositToFloat:
		var destKey cm.PublicKey
		copy(destKey[:], tx.PubKey)
		callData = parentchain.EncodeDepositToFloatCallData(destKey, tx.ClusterID, tx.Sender, tx.Target, tx.Amount, tx.MsgID)
		senderAddr = tx.Sender.Bytes()

	case parentchain.TxTypeTransferFloat:
		var toKey cm.PublicKey
		copy(toKey[:], tx.ToPubKey)
		callData = parentchain.EncodeTransferFloatCallData(toKey, tx.ClusterID, tx.Sender, tx.Target, tx.Amount, tx.Fee, tx.Nonce, cm.SignFromBytes(tx.Cert), tx.IsRefund, tx.Payload)
		senderKey = tx.PubKey
		senderAddr = tx.Sender.Bytes()

	case parentchain.TxTypeMarkClaimed:
		callData = parentchain.EncodeMarkClaimedCallData(tx.MsgID, tx.Outcome, cm.SignFromBytes(tx.Cert))
		senderAddr = tx.Sender.Bytes()

	case parentchain.TxTypeReclaimFloat:
		callData = parentchain.EncodeReclaimFloatCallData(tx.MsgID, cm.SignFromBytes(tx.Cert))
		senderAddr = tx.Sender.Bytes()
	}

	if len(callData) > 0 {
		return &pb.Transaction{
			FromAddress:   senderAddr,
			ToAddress:     parentchain.ParentChainGatewayAddress.Bytes(),
			Nonce:         nonceBytes[:],
			Data:          callData,
			ChainID:       parentchain.ParentChainID,
			LastDeviceKey: senderKey,
			Sign:          tx.Cert,
		}
	}

	raw, _ := json.Marshal(tx)
	return &pb.Transaction{
		Data:      raw,
		ChainID:   parentchain.ParentChainID,
		ToAddress: parentchain.ParentChainGatewayAddress.Bytes(),
	}
}

func (tb *TxBatcher) submitBatch(txs []*pb.Transaction) {
	if len(txs) == 0 {
		return
	}

	var batch pb.Transactions
	batch.Transactions = txs

	batchBytes, err := proto.Marshal(&batch)
	if err != nil {
		log.Printf("Failed to marshal pb.Transactions: %v", err)
		return
	}

	// Submit to Rust consensus core
	success := executor.SubmitTransactionBatch(batchBytes)
	if !success {
		log.Printf("Warning: SubmitTransactionBatch returned false for %d txs", len(batch.Transactions))
	}
}
