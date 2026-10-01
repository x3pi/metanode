package processor

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/executor"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"google.golang.org/protobuf/proto"
)

type BlockProcessor struct {
	committer    parentchain.BlockCommitter
	queue        chan *pb.ExecutableBlock
	quit         chan struct{}
	onTxResult   func(msgID common.Hash, err error)
	syncCallback func(fromBlock uint64)

	mu              sync.RWMutex
	lastBlockNumber uint64
	lastGEI         uint64
	lastStateRoot   common.Hash
	lastBlockHash   common.Hash
	forkDetected    bool
}

func NewBlockProcessor(committer parentchain.BlockCommitter, onTxResult func(msgID common.Hash, err error)) *BlockProcessor {
	bp := &BlockProcessor{
		committer:  committer,
		queue:      make(chan *pb.ExecutableBlock, 100),
		quit:       make(chan struct{}),
		onTxResult: onTxResult,
	}

	// Startup recovery: reload last applied progress from durable DB
	if committer != nil {
		prog, err := committer.LastApplied()
		if err == nil && prog.LastBlock > 0 {
			bp.lastBlockNumber = prog.LastBlock
			bp.lastGEI = prog.LastGEI
			bp.lastStateRoot = prog.LastStateRoot
			bp.lastBlockHash = prog.LastHash

			storage.UpdateLastBlockNumber(prog.LastBlock)
			storage.UpdateLastAssignedBlockNumber(prog.LastBlock)
			storage.UpdateLastGlobalExecIndex(prog.LastGEI)

			log.Printf("Parent Chain: Recovered at block #%d (GEI %d), stateRoot=%s",
				prog.LastBlock, prog.LastGEI, prog.LastStateRoot.Hex())
		}
	}

	// Register StateRootProvider hook for cgo_get_state_root FFI
	executor.SetStateRootProvider(bp.GetStateRoot)

	return bp
}

func (bp *BlockProcessor) SetSyncCallback(cb func(fromBlock uint64)) {
	bp.mu.Lock()
	defer bp.mu.Unlock()
	bp.syncCallback = cb
}

func (bp *BlockProcessor) GetQueue() chan *pb.ExecutableBlock {
	return bp.queue
}

func (bp *BlockProcessor) Committer() parentchain.BlockCommitter {
	return bp.committer
}

func (bp *BlockProcessor) GetStateRoot() string {
	bp.mu.RLock()
	defer bp.mu.RUnlock()
	return "0x" + hex.EncodeToString(bp.lastStateRoot.Bytes())
}

func (bp *BlockProcessor) LastBlockNumber() uint64 {
	bp.mu.RLock()
	defer bp.mu.RUnlock()
	return bp.lastBlockNumber
}

func (bp *BlockProcessor) LastBlockHash() common.Hash {
	bp.mu.RLock()
	defer bp.mu.RUnlock()
	return bp.lastBlockHash
}

func (bp *BlockProcessor) IsForkDetected() bool {
	bp.mu.RLock()
	defer bp.mu.RUnlock()
	return bp.forkDetected
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
			bp.ProcessBlock(block)
		case req, ok := <-authQueue:
			if !ok {
				log.Println("Parent Chain: Authoritative block queue closed")
				return
			}
			resp := bp.ProcessBlock(req.Block)
			if req.ResponseCh != nil {
				req.ResponseCh <- resp
			}
		}
	}
}

// ProcessBlock processes an ExecutableBlock deterministically.
func (bp *BlockProcessor) ProcessBlock(block *pb.ExecutableBlock) *pb.ExecuteBlockResponse {
	bp.mu.Lock()
	defer bp.mu.Unlock()

	// 1. Guard against fork
	if bp.forkDetected {
		log.Printf("Parent Chain: REFUSING TO PROCESS block #%d because fork was previously detected", block.BlockNumber)
		return &pb.ExecuteBlockResponse{
			BlockNumber: block.BlockNumber,
			Success:     false,
			Error:       "fork detected",
		}
	}

	// 2. Ignore block 0 (genesis boundary block in consensus)
	if block.BlockNumber == 0 {
		log.Printf("Parent Chain: Skipping block 0 (boundary block)")
		return &pb.ExecuteBlockResponse{
			BlockNumber:  0,
			ActualGei:    block.GlobalExecIndex,
			GeisConsumed: 1,
			Success:      true,
			StateRoot:    bp.lastStateRoot.Bytes(),
		}
	}

	log.Printf("Parent Chain: Processing block %d (GEI %d) with %d txs",
		block.BlockNumber, block.GlobalExecIndex, len(block.Transactions))

	// 3. Extract and deterministically order transaction bytes
	// Sort by (FromAddress ASC, Nonce ASC, TxHash ASC) so that multiple transactions from the
	// same account are executed in strict nonce order (0, 1, 2...) without nonce gaps/rejections.
	type txItem struct {
		from  common.Address
		nonce uint64
		hash  common.Hash
		raw   []byte
	}
	items := make([]txItem, 0, len(block.Transactions))
	for _, txExe := range block.Transactions {
		var pbTx pb.Transaction
		if err := proto.Unmarshal(txExe.Digest, &pbTx); err == nil {
			var n uint64
			if len(pbTx.Nonce) >= 8 {
				n = binary.BigEndian.Uint64(pbTx.Nonce[:8])
			} else if len(pbTx.Nonce) > 0 {
				n = new(big.Int).SetBytes(pbTx.Nonce).Uint64()
			}
			items = append(items, txItem{
				from:  common.BytesToAddress(pbTx.FromAddress),
				nonce: n,
				hash:  parentchain.ComputeTxHash(&pbTx),
				raw:   txExe.Digest,
			})
		} else {
			items = append(items, txItem{
				hash: crypto.Keccak256Hash(txExe.Digest),
				raw:  txExe.Digest,
			})
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		cmp := items[i].from.Cmp(items[j].from)
		if cmp != 0 {
			return cmp < 0
		}
		if items[i].nonce != items[j].nonce {
			return items[i].nonce < items[j].nonce
		}
		return bytes.Compare(items[i].hash.Bytes(), items[j].hash.Bytes()) < 0
	})

	var rawTxs [][]byte
	for _, it := range items {
		rawTxs = append(rawTxs, it.raw)
	}

	rawBlock, _ := proto.Marshal(block)

	in := parentchain.BlockInput{
		Number:        block.BlockNumber,
		Epoch:         block.Epoch,
		CommitIndex:   block.CommitIndex,
		GEI:           block.GlobalExecIndex,
		TimestampMs:   block.CommitTimestampMs,
		LeaderAddress: common.BytesToAddress(block.LeaderAddress),
		CommitDigest:  common.BytesToHash(block.CommitDigest),
		Txs:           rawTxs,
		RawBlock:      rawBlock,
	}

	// 4. Apply Block atomically
	blockTime := block.CommitTimestampMs / 1000
	res, err := bp.committer.ApplyBlock(in, func(store parentchain.Store, txIndex int, rawTx []byte) (*parentchain.Receipt, error) {
		var pbTx pb.Transaction
		if err := proto.Unmarshal(rawTx, &pbTx); err != nil {
			return nil, err
		}
		return parentchain.ExecuteTx(store, &pbTx, blockTime)
	})

	if err != nil {
		if errors.Is(err, parentchain.ErrBlockGap) {
			log.Printf("Parent Chain: Block GAP detected at block #%d (last was #%d)", block.BlockNumber, bp.lastBlockNumber)
			if bp.syncCallback != nil {
				bp.syncCallback(bp.lastBlockNumber + 1)
			}
			return &pb.ExecuteBlockResponse{
				BlockNumber: block.BlockNumber,
				Success:     false,
				Error:       fmt.Sprintf("block gap: %v", err),
			}
		}

		if errors.Is(err, parentchain.ErrBlockConflict) {
			log.Printf("🚨 Parent Chain: CRITICAL FORK DETECTED at block #%d! State conflict: %v", block.BlockNumber, err)
			bp.forkDetected = true
			parentchain.ParentChainForkDetected.Set(1)
			return &pb.ExecuteBlockResponse{
				BlockNumber: block.BlockNumber,
				Success:     false,
				Error:       fmt.Sprintf("critical block conflict: %v", err),
			}
		}

		log.Printf("Parent Chain: ApplyBlock failed for block #%d: %v", block.BlockNumber, err)
		return &pb.ExecuteBlockResponse{
			BlockNumber: block.BlockNumber,
			Success:     false,
			Error:       err.Error(),
		}
	}

	// 5. Update local state pointers on success
	bp.lastBlockNumber = res.Record.Header.Number
	bp.lastGEI = res.Record.Header.GEI
	bp.lastStateRoot = res.Record.Header.StateRoot
	bp.lastBlockHash = res.Record.BlockHash

	// Update Prometheus metrics (H9)
	parentchain.ParentChainLastBlock.Set(float64(res.Record.Header.Number))
	parentchain.ParentChainBlocksTotal.Inc()
	parentchain.ParentChainTxsTotal.Add(float64(len(res.Record.Receipts)))
	parentchain.ParentChainStateRoot.Reset()
	parentchain.ParentChainStateRoot.WithLabelValues(res.Record.Header.StateRoot.Hex()).Set(1)

	// Notify individual transaction execution outcomes
	if bp.onTxResult != nil {
		for i, rcpt := range res.Record.Receipts {
			var txErr error
			if rcpt.Status == 0 {
				if i < len(res.TxErrors) && res.TxErrors[i] != nil {
					txErr = res.TxErrors[i]
				} else {
					txErr = fmt.Errorf("receipt error code: %d", rcpt.ErrorCode)
				}
			}
			log.Printf("block_processor: receipt #%d txHash=%s status=%d errCode=%d events=%d err=%v",
				i, rcpt.TxHash.Hex()[:10], rcpt.Status, rcpt.ErrorCode, len(rcpt.Events), txErr)

			bp.onTxResult(rcpt.TxHash, txErr)
			for _, evt := range rcpt.Events {
				if len(evt) == 32 {
					bp.onTxResult(common.BytesToHash(evt), txErr)
				}
			}

			// If rcpt failed before events were populated, fallback to extracting msgID from rawTxs
			if len(rcpt.Events) == 0 && i < len(rawTxs) {
				var pbTx pb.Transaction
				if err := proto.Unmarshal(rawTxs[i], &pbTx); err == nil {
					var cd pb.ParentChainCallData
					if err := proto.Unmarshal(pbTx.Data, &cd); err == nil {
						switch cd.Method {
						case pb.ParentChainMethod_METHOD_DEPOSIT_TO_FLOAT:
							if args := cd.GetDepositToFloat(); args != nil && len(args.MsgId) == 32 {
								bp.onTxResult(common.BytesToHash(args.MsgId), txErr)
							}
						case pb.ParentChainMethod_METHOD_REGISTER_ACCOUNT:
							if args := cd.GetRegisterAccount(); args != nil && len(args.FloatIdentityKey) == 48 {
								var fKey cm.PublicKey
								copy(fKey[:], args.FloatIdentityKey)
								regDigest := parentchain.ComputeRegisterAccountMessage(common.BytesToAddress(args.UserAddress), fKey)
								bp.onTxResult(crypto.Keccak256Hash(regDigest), txErr)
							}
						case pb.ParentChainMethod_METHOD_SUBMIT_STATE_ROOT:
							if args := cd.GetSubmitStateRoot(); args != nil && len(args.ClusterKey) == 48 {
								var cKey cm.PublicKey
								copy(cKey[:], args.ClusterKey)
								digest := parentchain.ComputeSubmitStateRootMessage(cKey, args.Epoch, common.BytesToHash(args.StateRoot))
								bp.onTxResult(crypto.Keccak256Hash(append(digest, args.Cert...)), txErr)
							}
						case pb.ParentChainMethod_METHOD_MARK_CLAIMED:
							if args := cd.GetMarkClaimed(); args != nil && len(args.MsgId) == 32 {
								bp.onTxResult(common.BytesToHash(args.MsgId), txErr)
							}
						case pb.ParentChainMethod_METHOD_RECLAIM_FLOAT:
							if args := cd.GetReclaimFloat(); args != nil && len(args.MsgId) == 32 {
								bp.onTxResult(common.BytesToHash(args.MsgId), txErr)
							}
						}
					}
				}
			}
		}
	}

	log.Printf("Parent Chain: Block #%d applied successfully: txs=%d receipts=%d hash=%s stateRoot=%s",
		res.Record.Header.Number, len(block.Transactions), len(res.Record.Receipts), res.Record.BlockHash.Hex()[:18], res.Record.Header.StateRoot.Hex()[:18])

	return &pb.ExecuteBlockResponse{
		BlockNumber:  res.Record.Header.Number,
		ActualGei:    res.Record.Header.GEI,
		GeisConsumed: 1,
		Success:      true,
		StateRoot:    res.Record.Header.StateRoot.Bytes(),
	}
}
