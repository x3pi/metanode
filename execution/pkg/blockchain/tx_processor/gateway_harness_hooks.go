//go:build !c0spike

package tx_processor

import (
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/types"
)

// Production build: the C0 spike harness hooks are no-ops, so the Gateway handler behaves exactly as
// it did before they existed (see gateway_handler_c0spike.go for the harness implementation).

func applyHarnessInitialRegistries(*cross_chain.GatewayEngine) {}

func harnessGatewayAccountFallback() types.AccountState { return nil }
