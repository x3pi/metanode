package types

import (
	"sync"

	"github.com/meta-node-blockchain/meta-node/types/network"
)

type ClientHandler interface {
	HandleRequest(request network.Request) (err error)
	SetPendingRpcRequests(pending *sync.Map)
	SetPendingChainRequests(pending *sync.Map)
	RegisterEventCallback(subID string, cb func([]byte))
	RemoveEventCallback(subID string)
	RegisterReceiptCallback(cb func([]byte))
}
