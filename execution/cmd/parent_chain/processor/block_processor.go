package processor

import (
	"log"
	"time"
	
	"github.com/ethereum/go-ethereum/common"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/executor"
	"google.golang.org/protobuf/proto"
)

type BlockProcessor struct {
	store      parentchain.Store
	queue      chan *pb.ExecutableBlock
	quit       chan struct{}
	onTxResult func(msgID common.Hash, err error)
}

func NewBlockProcessor(store parentchain.Store, onTxResult func(msgID common.Hash, err error)) *BlockProcessor {
	return &BlockProcessor{
		store:      store,
		queue:      make(chan *pb.ExecutableBlock, 100),
		quit:       make(chan struct{}),
		onTxResult: onTxResult,
	}
}

func (bp *BlockProcessor) GetQueue() chan *pb.ExecutableBlock {
	return bp.queue
}

func (bp *BlockProcessor) GetStateRoot() string {
	// TODO: implement actual state root if needed
	return "0000000000000000000000000000000000000000000000000000000000000000"
}

func (bp *BlockProcessor) Start() {
	go bp.loop()
}

func (bp *BlockProcessor) Stop() {
	close(bp.quit)
}

func (bp *BlockProcessor) loop() {
	authQueue := executor.GetAuthoritativeBlockQueue()
	for {
		select {
		case <-bp.quit:
			return
		case block := <-bp.queue:
			bp.processBlock(block)
		case req, ok := <-authQueue:
			if !ok {
				log.Println("Parent Chain: Authoritative block queue closed")
				return
			}
			bp.processBlock(req.Block)
			if req.ResponseCh != nil {
				req.ResponseCh <- &pb.ExecuteBlockResponse{Success: true}
			}
		}
	}
}

func (bp *BlockProcessor) processBlock(block *pb.ExecutableBlock) {
	log.Printf("Parent Chain: Processing block %d (GEI %d) with %d txs", block.BlockNumber, block.GlobalExecIndex, len(block.Transactions))
	
	blockTime := block.CommitTimestampMs / 1000
	if blockTime == 0 {
		blockTime = uint64(time.Now().Unix())
	}

	for i, txExe := range block.Transactions {
		var pbTx pb.Transaction
		if err := proto.Unmarshal(txExe.Digest, &pbTx); err != nil {
			log.Printf("Parent Chain: failed to unmarshal pb.Transaction %d in block %d: %v", i, block.BlockNumber, err)
			continue
		}
		tx, err := parentchain.UnmarshalParentChainTx(pbTx.Data)
		if err != nil {
			log.Printf("Parent Chain: failed to unmarshal tx %d in block %d: %v", i, block.BlockNumber, err)
			continue
		}

		var sig cm.Sign
		if len(tx.Cert) > 0 {
			sig = cm.Sign(tx.Cert)
		}
		var pubKey cm.PublicKey
		if len(tx.PubKey) > 0 {
			copy(pubKey[:], tx.PubKey)
		}

		clusterID := tx.ClusterID
		if clusterID == 0 {
			clusterID = tx.ChainID
		}

		switch tx.Type {
		case parentchain.TxTypeDepositToFloat:
			err = parentchain.DepositToFloat(bp.store, pubKey, clusterID, tx.Sender, tx.Target, tx.Amount, tx.MsgID, blockTime)
		case parentchain.TxTypeTransferFloat:
			var toPubKey cm.PublicKey
			if len(tx.ToPubKey) > 0 {
				copy(toPubKey[:], tx.ToPubKey)
			}
			_, err = parentchain.TransferFloat(
				bp.store, pubKey, toPubKey, clusterID, tx.Sender, tx.Target,
				tx.Amount, tx.Fee, tx.Payload, tx.Nonce, sig, tx.IsRefund, 0, blockTime,
			)
		case parentchain.TxTypeMarkClaimed:
			err = parentchain.MarkClaimed(bp.store, tx.MsgID, tx.Outcome, sig)
		case parentchain.TxTypeReclaimFloat:
			err = parentchain.ReclaimFloat(bp.store, tx.MsgID, sig, blockTime, 60) // 60s timeout for reclaim by default
		case parentchain.TxTypeRegisterAccount:
			err = parentchain.RegisterAccount(bp.store, tx.UserAddress, pubKey, tx.UserSig, sig)
		default:
			log.Printf("Parent Chain: unknown tx type %s", tx.Type)
		}

		if err != nil {
			log.Printf("Parent Chain: tx %d failed: %v", i, err)
		}
		if bp.onTxResult != nil {
			bp.onTxResult(tx.MsgID, err)
		}
	}

	// Tell the FFI that the block has been processed
	storage.UpdateLastBlockNumber(block.BlockNumber)
	storage.UpdateLastAssignedBlockNumber(block.BlockNumber)
}
