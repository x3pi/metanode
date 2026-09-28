package rollup

import (
	"log"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

type StateProvider interface {
	GetLatestEpoch() (uint64, common.Hash)
}

type CheckpointWorker struct {
	client      parentchain.Client
	stateProv   StateProvider
	blsKeyPair  *bls.KeyPair
	destPubKey  cm.PublicKey
	interval    time.Duration

	lastEpoch uint64
	wakeCh    chan struct{}
	quitCh    chan struct{}
	wg        sync.WaitGroup
}

func NewCheckpointWorker(client parentchain.Client, stateProv StateProvider, blsKeyPair *bls.KeyPair, destPubKey cm.PublicKey, interval time.Duration) *CheckpointWorker {
	return &CheckpointWorker{
		client:     client,
		stateProv:  stateProv,
		blsKeyPair: blsKeyPair,
		destPubKey: destPubKey,
		interval:   interval,
		wakeCh:     make(chan struct{}, 1),
		quitCh:     make(chan struct{}),
	}
}

func (w *CheckpointWorker) Start() {
	w.wg.Add(1)
	go w.loop()
}

func (w *CheckpointWorker) Stop() {
	close(w.quitCh)
	w.wg.Wait()
}

func (w *CheckpointWorker) Wakeup() {
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
}

func (w *CheckpointWorker) loop() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.quitCh:
			return
		case <-w.wakeCh:
			w.checkAndSubmit()
		case <-ticker.C:
			w.checkAndSubmit()
		}
	}
}

func (w *CheckpointWorker) checkAndSubmit() {
	epoch, stateRoot := w.stateProv.GetLatestEpoch()
	// Skip if epoch is 0 (uninitialized) or we've already submitted it
	if epoch == 0 || epoch <= w.lastEpoch {
		return
	}

	digest := parentchain.ComputeSubmitStateRootMessage(w.blsKeyPair.PublicKey(), epoch, stateRoot)
	cert := bls.Sign(w.blsKeyPair.PrivateKey(), digest)

	_, err := w.client.SendSubmitStateRoot(w.blsKeyPair.PublicKey(), epoch, stateRoot, cert)
	if err != nil {
		// Log transient errors and retry on next tick
		log.Printf("CheckpointWorker: failed to submit state root for epoch %d: %v", epoch, err)
		return
	}

	log.Printf("CheckpointWorker: successfully submitted state root for epoch %d (root: %s)", epoch, stateRoot.Hex())
	w.lastEpoch = epoch
}
