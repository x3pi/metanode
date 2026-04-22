// Copyright 2015 The go-ethereum Authors
// This file is part of the go-ethereum library.
//
// The go-ethereum library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The go-ethereum library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see <http://www.gnu.org/licenses/>.

// Package filters implements an ethereum filtering system for block,
// transactions and log events.
package filters

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/rpc"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

var (
	errInvalidTopic           = errors.New("invalid topic(s)")
	errInvalidBlockRange      = errors.New("invalid block range params")
	errPendingLogsUnsupported = errors.New("pending logs are not supported")
	errExceedMaxTopics        = errors.New("exceed max topics")
)

const maxSubTopics = 1000

type EventHeader struct {
	ParentHash  common.Hash      `json:"parentHash"       gencodec:"required"`
	Hash        common.Hash      `json:"hash"       gencodec:"required"`
	UncleHash   common.Hash      `json:"sha3Uncles"       gencodec:"required"`
	Coinbase    common.Address   `json:"miner"`
	Root        common.Hash      `json:"stateRoot"        gencodec:"required"`
	TxHash      common.Hash      `json:"transactionsRoot" gencodec:"required"`
	ReceiptHash common.Hash      `json:"receiptsRoot"     gencodec:"required"`
	Bloom       types.Bloom      `json:"logsBloom"        gencodec:"required"`
	Difficulty  *big.Int         `json:"difficulty"       gencodec:"required"`
	Number      *big.Int         `json:"number"           gencodec:"required"`
	GasLimit    uint64           `json:"gasLimit"         gencodec:"required"`
	GasUsed     uint64           `json:"gasUsed"          gencodec:"required"`
	Time        uint64           `json:"timestamp"        gencodec:"required"`
	Extra       []byte           `json:"extraData"        gencodec:"required"`
	MixDigest   common.Hash      `json:"mixHash"`
	Nonce       types.BlockNonce `json:"nonce"`

	// BaseFee was added by EIP-1559 and is ignored in legacy headers.
	BaseFee *big.Int `json:"baseFeePerGas" rlp:"optional"`

	// WithdrawalsHash was added by EIP-4895 and is ignored in legacy headers.
	WithdrawalsHash *common.Hash `json:"withdrawalsRoot" rlp:"optional"`

	// BlobGasUsed was added by EIP-4844 and is ignored in legacy headers.
	BlobGasUsed *uint64 `json:"blobGasUsed" rlp:"optional"`

	// ExcessBlobGas was added by EIP-4844 and is ignored in legacy headers.
	ExcessBlobGas *uint64 `json:"excessBlobGas" rlp:"optional"`

	// ParentBeaconRoot was added by EIP-4788 and is ignored in legacy headers.
	ParentBeaconRoot *common.Hash `json:"parentBeaconBlockRoot" rlp:"optional"`

	// RequestsHash was added by EIP-7685 and is ignored in legacy headers.
	RequestsHash *common.Hash `json:"requestsRoot" rlp:"optional"`
}

type ChainEvent struct {
	Header *EventHeader
}

// Config represents the configuration of the filter system.
type Config struct {
	LogCacheSize int           // maximum number of cached blocks (default: 32)
	Timeout      time.Duration // how long filters stay active (default: 5min)
}

func (cfg Config) withDefaults() Config {
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.LogCacheSize == 0 {
		cfg.LogCacheSize = 32
	}
	return cfg
}

type FilterCriteria ethereum.FilterQuery

// UnmarshalJSON sets *args fields with given data.
func (args *FilterCriteria) UnmarshalJSON(data []byte) error {
	type input struct {
		BlockHash *common.Hash     `json:"blockHash"`
		FromBlock *rpc.BlockNumber `json:"fromBlock"`
		ToBlock   *rpc.BlockNumber `json:"toBlock"`
		Addresses interface{}      `json:"address"`
		Topics    []interface{}    `json:"topics"`
	}

	var raw input
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	if raw.BlockHash != nil {
		if raw.FromBlock != nil || raw.ToBlock != nil {
			// BlockHash is mutually exclusive with FromBlock/ToBlock criteria
			return errors.New("cannot specify both BlockHash and FromBlock/ToBlock, choose one or the other")
		}
		args.BlockHash = raw.BlockHash
	} else {
		if raw.FromBlock != nil {
			args.FromBlock = big.NewInt(raw.FromBlock.Int64())
		}

		if raw.ToBlock != nil {
			args.ToBlock = big.NewInt(raw.ToBlock.Int64())
		}
	}

	args.Addresses = []common.Address{}

	if raw.Addresses != nil {
		// raw.Address can contain a single address or an array of addresses
		switch rawAddr := raw.Addresses.(type) {
		case []interface{}:
			for i, addr := range rawAddr {
				if strAddr, ok := addr.(string); ok {
					addr, err := decodeAddress(strAddr)
					if err != nil {
						return fmt.Errorf("invalid address at index %d: %v", i, err)
					}
					args.Addresses = append(args.Addresses, addr)
				} else {
					return fmt.Errorf("non-string address at index %d", i)
				}
			}
		case string:
			addr, err := decodeAddress(rawAddr)
			if err != nil {
				return fmt.Errorf("invalid address: %v", err)
			}
			args.Addresses = []common.Address{addr}
		default:
			return errors.New("invalid addresses in query")
		}
	}
	if len(raw.Topics) > maxTopics {
		return errExceedMaxTopics
	}

	// topics is an array consisting of strings and/or arrays of strings.
	// JSON null values are converted to common.Hash{} and ignored by the filter manager.
	if len(raw.Topics) > 0 {
		args.Topics = make([][]common.Hash, len(raw.Topics))
		for i, t := range raw.Topics {
			switch topic := t.(type) {
			case nil:
				// ignore topic when matching logs

			case string:
				// match specific topic
				top, err := decodeTopic(topic)
				if err != nil {
					return err
				}
				args.Topics[i] = []common.Hash{top}

			case []interface{}:
				// or case e.g. [null, "topic0", "topic1"]
				if len(topic) > maxSubTopics {
					return errExceedMaxTopics
				}
				for _, rawTopic := range topic {
					if rawTopic == nil {
						// null component, match all
						args.Topics[i] = nil
						break
					}
					if topic, ok := rawTopic.(string); ok {
						parsed, err := decodeTopic(topic)
						if err != nil {
							return err
						}
						args.Topics[i] = append(args.Topics[i], parsed)
					} else {
						return errInvalidTopic
					}
				}
			default:
				return errInvalidTopic
			}
		}
	}

	return nil
}

// The maximum number of topic criteria allowed, vm.LOG4 - vm.LOG0
const maxTopics = 4

// The maximum number of allowed topics within a topic criteria
const MaxSubTopics = 1000

// FilterSystem holds resources shared by all filters.
type FilterSystem struct {
	cfg *Config
}

// NewFilterSystem creates a filter system.
func NewFilterSystem(config Config) *FilterSystem {
	config = config.withDefaults()
	return &FilterSystem{
		cfg: &config,
	}
}

// Type determines the kind of filter and is used to put the filter in to
// the correct bucket when added.
type Type byte

const (
	// UnknownSubscription indicates an unknown subscription type
	UnknownSubscription Type = iota
	// LogsSubscription queries for new or removed (chain reorg) logs
	LogsSubscription
	// PendingTransactionsSubscription queries for pending transactions entering
	// the pending state
	PendingTransactionsSubscription
	// BlocksSubscription queries hashes for blocks that are imported
	BlocksSubscription
	// LastIndexSubscription keeps track of the last index
	LastIndexSubscription
)

const (
	// txChanSize is the size of channel listening to NewTxsEvent.
	// The number is referenced from the size of tx pool.
	txChanSize = 4096
	// rmLogsChanSize is the size of channel listening to RemovedLogsEvent.
	rmLogsChanSize = 10
	// logsChanSize is the size of channel listening to LogsEvent.
	logsChanSize = 10
	// chainEvChanSize is the size of channel listening to ChainEvent.
	chainEvChanSize = 10
)

type subscription struct {
	id        rpc.ID
	typ       Type
	logsCrit  FilterCriteria
	created   time.Time
	logs      chan []*types.Log
	txs       chan []mt_types.Transaction
	headers   chan *EventHeader
	installed chan struct{} // closed when the filter is installed
	err       chan error    // closed when the filter is uninstalled
}

// EventSystem creates subscriptions, processes events and broadcasts them to the
// subscription which match the subscription criteria.
type EventSystem struct {
	// Subscriptions
	TxsFeed    *event.Feed        // Subscription for new transaction event
	LogsFeed   *event.Feed        // Subscription for new log event
	RmLogsFeed *event.Feed        // Subscription for removed log event
	ChainFeed  *event.Feed        // Subscription for new chain event
	txsSub     event.Subscription // Subscription for new transaction event
	logsSub    event.Subscription // Subscription for new log event
	rmLogsSub  event.Subscription // Subscription for removed log event
	chainSub   event.Subscription // Subscription for new chain event

	// Channels
	install   chan *subscription         // install filter for event notification
	uninstall chan *subscription         // remove filter for event notification
	txsCh     chan NewTxsEvent           // Channel to receive new transactions event
	logsCh    chan []*types.Log          // Channel to receive new log event
	rmLogsCh  chan core.RemovedLogsEvent // Channel to receive removed log event
	chainCh   chan ChainEvent            // Channel to receive new chain event
	statusCh  chan interface{}           // Channel to receive status updates

}

// NewEventSystem creates a new manager that listens for event on the given mux,
// parses and filters them. It uses the all map to retrieve filter changes. The
// work loop holds its own index that is used to forward events to filters.
//
// The returned manager has a loop that needs to be stopped with the Stop function
// or by stopping the given mux.
func NewEventSystem() *EventSystem {

	m := &EventSystem{
		TxsFeed:    &event.Feed{},
		LogsFeed:   &event.Feed{},
		RmLogsFeed: &event.Feed{},
		ChainFeed:  &event.Feed{},
		install:    make(chan *subscription),
		uninstall:  make(chan *subscription),
		txsCh:      make(chan NewTxsEvent, txChanSize),
		logsCh:     make(chan []*types.Log, logsChanSize),
		rmLogsCh:   make(chan core.RemovedLogsEvent, rmLogsChanSize),
		chainCh:    make(chan ChainEvent, chainEvChanSize),
		statusCh:   make(chan interface{}), // Channel for status updates

	}
	m.txsSub = m.TxsFeed.Subscribe(m.txsCh)
	m.logsSub = m.LogsFeed.Subscribe(m.logsCh)
	m.rmLogsSub = m.RmLogsFeed.Subscribe(m.rmLogsCh)
	m.chainSub = m.ChainFeed.Subscribe(m.chainCh)
	// Make sure none of the subscriptions are empty
	if m.txsSub == nil || m.logsSub == nil || m.rmLogsSub == nil || m.chainSub == nil {
		log.Crit("Subscribe for event system failed")
	}

	go m.eventLoop()
	return m
}

// SendStatus sends a status update to the status channel.
func (es *EventSystem) SendStatus(status interface{}) {
	es.statusCh <- status
}

// SubscribeStatus subscribes to status updates.
func (es *EventSystem) SubscribeStatus() <-chan interface{} {
	ch := make(chan interface{})
	go func() {
		defer close(ch)
		for status := range es.statusCh {
			ch <- status
		}
	}()
	return ch
}

// Subscription is created when the client registers itself for a particular event.
type Subscription struct {
	ID        rpc.ID
	f         *subscription
	es        *EventSystem
	unsubOnce sync.Once
}

// Err returns a channel that is closed when unsubscribed.
func (sub *Subscription) Err() <-chan error {
	return sub.f.err
}

// Unsubscribe uninstalls the subscription from the event broadcast loop.
func (sub *Subscription) Unsubscribe() {
	sub.unsubOnce.Do(func() {
	uninstallLoop:
		for {
			// write uninstall request and consume logs/hashes. This prevents
			// the eventLoop broadcast method to deadlock when writing to the
			// filter event channel while the subscription loop is waiting for
			// this method to return (and thus not reading these events).
			select {
			case sub.es.uninstall <- sub.f:
				break uninstallLoop
			case <-sub.f.logs:
			case <-sub.f.txs:
			case <-sub.f.headers:
			}
		}

		// wait for filter to be uninstalled in work loop before returning
		// this ensures that the manager won't use the event channel which
		// will probably be closed by the client asap after this method returns.
		<-sub.Err()
	})
}

// subscribe installs the subscription in the event broadcast loop.
func (es *EventSystem) subscribe(sub *subscription) *Subscription {
	es.install <- sub
	<-sub.installed
	return &Subscription{ID: sub.id, f: sub, es: es}
}

// SubscribeNewHeads creates a subscription that writes the header of a block that is
// imported in the chain.
func (es *EventSystem) SubscribeNewHeads(headers chan *EventHeader) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       BlocksSubscription,
		created:   time.Now(),
		logs:      make(chan []*types.Log),
		txs:       make(chan []mt_types.Transaction),
		headers:   headers,
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

// SubscribePendingTxs creates a subscription that writes transactions for
// transactions that enter the transaction pool.
func (es *EventSystem) SubscribePendingTxs(txs chan []mt_types.Transaction) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       PendingTransactionsSubscription,
		created:   time.Now(),
		logs:      make(chan []*types.Log),
		txs:       txs,
		headers:   make(chan *EventHeader),
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

type filterIndex map[Type]map[rpc.ID]*subscription
type NewTxsEvent struct{ Txs []mt_types.Transaction }

func (es *EventSystem) handleLogs(filters filterIndex, ev []*types.Log) {
	if len(ev) == 0 {
		return
	}
	for _, f := range filters[LogsSubscription] {
		matchedLogs := FilterLogs(ev, f.logsCrit.FromBlock, f.logsCrit.ToBlock, f.logsCrit.Addresses, f.logsCrit.Topics)
		if len(matchedLogs) > 0 {
			f.logs <- matchedLogs
		}
	}
}

func (es *EventSystem) handleTxsEvent(filters filterIndex, ev NewTxsEvent) {
	for _, f := range filters[PendingTransactionsSubscription] {
		f.txs <- ev.Txs
	}
}

func (es *EventSystem) handleChainEvent(filters filterIndex, ev ChainEvent) {
	for _, f := range filters[BlocksSubscription] {
		f.headers <- ev.Header
	}
}

// SubscribeLogs creates a subscription that will write all logs matching the
// given criteria to the given logs channel. Default value for the from and to
// block is "latest". If the fromBlock > toBlock an error is returned.
func (es *EventSystem) SubscribeLogs(crit FilterCriteria, logs chan []*types.Log) (*Subscription, error) {
	if len(crit.Topics) > maxTopics {
		return nil, errExceedMaxTopics
	}
	var from, to rpc.BlockNumber
	if crit.FromBlock == nil {
		from = rpc.LatestBlockNumber
	} else {
		from = rpc.BlockNumber(crit.FromBlock.Int64())
	}
	if crit.ToBlock == nil {
		to = rpc.LatestBlockNumber
	} else {
		to = rpc.BlockNumber(crit.ToBlock.Int64())
	}

	// Pending logs are not supported anymore.
	if from == rpc.PendingBlockNumber || to == rpc.PendingBlockNumber {
		return nil, errPendingLogsUnsupported
	}

	// only interested in new mined logs
	if from == rpc.LatestBlockNumber && to == rpc.LatestBlockNumber {
		return es.subscribeLogs(crit, logs), nil
	}
	// only interested in mined logs within a specific block range
	if from >= 0 && to >= 0 && to >= from {
		return es.subscribeLogs(crit, logs), nil
	}
	// interested in logs from a specific block number to new mined blocks
	if from >= 0 && to == rpc.LatestBlockNumber {
		return es.subscribeLogs(crit, logs), nil
	}
	return nil, errInvalidBlockRange
}

// subscribeLogs creates a subscription that will write all logs matching the
// given criteria to the given logs channel.
func (es *EventSystem) subscribeLogs(crit FilterCriteria, logs chan []*types.Log) *Subscription {
	sub := &subscription{
		id:        rpc.NewID(),
		typ:       LogsSubscription,
		logsCrit:  crit,
		created:   time.Now(),
		logs:      logs,
		txs:       make(chan []mt_types.Transaction),
		headers:   make(chan *EventHeader),
		installed: make(chan struct{}),
		err:       make(chan error),
	}
	return es.subscribe(sub)
}

// eventLoop (un)installs filters and processes mux events.
func (es *EventSystem) eventLoop() {
	// ticker := time.NewTicker(5 * time.Second)
	// defer ticker.Stop()
	// go func() {
	// 	for range ticker.C {
	// 		log.Info("5 seconds passed")
	// 		header := &types.Header{
	// 			ParentHash:  common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
	// 			UncleHash:   common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
	// 			Coinbase:    common.HexToAddress("0x0000000000000000000000000000000000000000"),
	// 			Root:        common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
	// 			TxHash:      common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
	// 			ReceiptHash: common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
	// 			Bloom:       types.Bloom{},
	// 			Difficulty:  big.NewInt(1),
	// 			Number:      big.NewInt(1), // Số thứ tự khối
	// 			GasLimit:    uint64(1000000),
	// 			GasUsed:     uint64(0),
	// 			Time:        uint64(time.Now().Unix()),
	// 			Extra:       []byte{},
	// 			MixDigest:   common.HexToHash("0x0000000000000000000000000000000000000000000000000000000000000000"),
	// 			Nonce:       types.BlockNonce{},
	// 		}
	// 		ev := core.ChainEvent{Header: header} // Thay thế nil bằng header phù hợp
	// 		es.ChainFeed.Send(ev)
	// 	}
	// }()
	// Ensure all subscriptions get cleaned up
	defer func() {
		es.txsSub.Unsubscribe()
		es.logsSub.Unsubscribe()
		es.rmLogsSub.Unsubscribe()
		es.chainSub.Unsubscribe()
	}()

	index := make(filterIndex)
	for i := UnknownSubscription; i < LastIndexSubscription; i++ {
		index[i] = make(map[rpc.ID]*subscription)
	}

	for {
		select {
		case status := <-es.statusCh:
			// Handle status updates
			log.Info("Status update received:", status)
		case ev := <-es.txsCh:
			es.handleTxsEvent(index, ev)
		case ev := <-es.chainCh:
			es.handleChainEvent(index, ev)
		case ev := <-es.logsCh:
			es.handleLogs(index, ev)
		case f := <-es.install:
			index[f.typ][f.id] = f
			close(f.installed)

		case f := <-es.uninstall:
			delete(index[f.typ], f.id)
			close(f.err)

		// System stopped
		case <-es.txsSub.Err():
			return
		case <-es.logsSub.Err():
			return
		case <-es.rmLogsSub.Err():
			return
		case <-es.chainSub.Err():
			return
		}
	}
}

func decodeAddress(s string) (common.Address, error) {
	b, err := hexutil.Decode(s)
	if err == nil && len(b) != common.AddressLength {
		err = fmt.Errorf("hex has invalid length %d after decoding; expected %d for address", len(b), common.AddressLength)
	}
	return common.BytesToAddress(b), err
}

func decodeTopic(s string) (common.Hash, error) {
	b, err := hexutil.Decode(s)
	if err == nil && len(b) != common.HashLength {
		err = fmt.Errorf("hex has invalid length %d after decoding; expected %d for topic", len(b), common.HashLength)
	}
	return common.BytesToHash(b), err
}
