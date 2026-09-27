package processor

import (
	"log"
	
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

type BlockProcessor struct {
	store parentchain.Store
	queue chan *pb.ExecutableBlock
	quit  chan struct{}
}

func NewBlockProcessor(store parentchain.Store) *BlockProcessor {
	return &BlockProcessor{
		store: store,
		queue: make(chan *pb.ExecutableBlock, 100),
		quit:  make(chan struct{}),
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
	for {
		select {
		case <-bp.quit:
			return
		case block := <-bp.queue:
			bp.processBlock(block)
		}
	}
}

func (bp *BlockProcessor) processBlock(block *pb.ExecutableBlock) {
	log.Printf("Parent Chain: Processing block %d (GEI %d) with %d txs", block.BlockNumber, block.GlobalExecIndex, len(block.Transactions))
	
	// Parent chain logic goes here:
	// Iterate through block.Transactions, parse as Parent Chain native TXs, and apply via bp.store functions.
	// Since this is a new chain type, the exact tx parsing depends on the RPC design.
}
