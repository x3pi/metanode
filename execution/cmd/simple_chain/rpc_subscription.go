package main

import (
	"context"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/meta-node-blockchain/meta-node/pkg/filters"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
)

// NewHeads send a notification each time a new (header) block is appended to the chain.
func (api *MetaAPI) NewHeads(ctx context.Context) (*rpc.Subscription, error) {
	notifier, supported := rpc.NotifierFromContext(ctx)
	if !supported {
		return &rpc.Subscription{}, rpc.ErrNotificationsUnsupported
	}

	rpcSub := notifier.CreateSubscription()

	go func() {
		headers := make(chan *filters.EventHeader)
		headersSub := api.events.SubscribeNewHeads(headers)
		defer headersSub.Unsubscribe()

		for {
			select {
			case h := <-headers:
				notifier.Notify(rpcSub.ID, h)
			case <-rpcSub.Err():
				return
			}
		}
	}()

	return rpcSub, nil
}

// NewPendingTransactions creates a subscription that is triggered each time a
// transaction enters the transaction pool. If fullTx is true the full tx is
// sent to the client, otherwise the hash is sent.
func (api *MetaAPI) NewPendingTransactions(ctx context.Context, fullTx *bool) (*rpc.Subscription, error) {
	notifier, supported := rpc.NotifierFromContext(ctx)
	if !supported {
		return &rpc.Subscription{}, rpc.ErrNotificationsUnsupported
	}

	rpcSub := notifier.CreateSubscription()

	go func() {
		txs := make(chan []mt_types.Transaction, 128)
		pendingTxSub := api.events.SubscribePendingTxs(txs)
		defer pendingTxSub.Unsubscribe()

		for {
			select {
			case txs := <-txs:
				for _, tx := range txs {
					notifier.Notify(rpcSub.ID, tx.Hash())
				}
			case <-rpcSub.Err():
				return
			}
		}
	}()

	return rpcSub, nil
}

// Logs creates a subscription that fires for all new log that match the given filter criteria.
func (api *MetaAPI) Logs(ctx context.Context, crit filters.FilterCriteria) (*rpc.Subscription, error) {
	notifier, supported := rpc.NotifierFromContext(ctx)
	if !supported {
		return &rpc.Subscription{}, rpc.ErrNotificationsUnsupported
	}

	var (
		rpcSub      = notifier.CreateSubscription()
		matchedLogs = make(chan []*types.Log)
	)

	logsSub, err := api.events.SubscribeLogs(crit, matchedLogs)
	if err != nil {
		return nil, err
	}

	go func() {
		defer logsSub.Unsubscribe()
		for {
			select {
			case logs := <-matchedLogs:
				for _, log := range logs {
					notifier.Notify(rpcSub.ID, &log)
				}
			case <-rpcSub.Err(): // client send an unsubscribe request
				return
			}
		}
	}()

	return rpcSub, nil
}

// Syncing provides information when this node starts synchronising with the Ethereum network and when it's finished.
func (api *MetaAPI) Syncing(ctx context.Context) (*rpc.Subscription, error) {
	notifier, supported := rpc.NotifierFromContext(ctx)
	if !supported {
		return &rpc.Subscription{}, rpc.ErrNotificationsUnsupported
	}

	rpcSub := notifier.CreateSubscription()

	go func() {
		// statuses := make(chan interface{})
		sub := api.events.SubscribeStatus()
		// defer sub.Unsubscribe()

		for {
			select {
			case status := <-sub:
				notifier.Notify(rpcSub.ID, status)
			case <-rpcSub.Err():
				return

			}
		}
	}()

	return rpcSub, nil
}
