package client_context

import (
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/config"
	client_types "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/client-tcp/types"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

type ClientContext struct {
	// config
	Config  *config.ClientConfig
	KeyPair *bls.KeyPair

	// network
	ConnectionsManager network.ConnectionsManager
	MessageSender      network.MessageSender
	SocketServer       network.SocketServer
	Handler            client_types.ClientHandler
}
