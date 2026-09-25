package types

import "github.com/meta-node-blockchain/meta-node/types/network"

type ClientHandler interface {
	HandleRequest(request network.Request) (err error)
}
