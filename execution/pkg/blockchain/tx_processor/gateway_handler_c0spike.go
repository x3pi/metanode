//go:build c0spike

package tx_processor

import (
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"

	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/types"
)

// Test-harness support for the C0 spike (cmd/simple_chain/c0_spike.go). Compiled only with
// -tags c0spike; the production build uses the no-op versions in gateway_harness_hooks.go.

var (
	harnessRegistries   = make(map[uint64]cross_chain.ChainRegistry)
	harnessRegistriesMu sync.RWMutex
)

// RegisterInitialChain makes loadGatewayEngine add reg to every engine it returns (when absent).
func RegisterInitialChain(reg cross_chain.ChainRegistry) {
	harnessRegistriesMu.Lock()
	defer harnessRegistriesMu.Unlock()
	harnessRegistries[reg.ChainID] = reg
}

func applyHarnessInitialRegistries(engine *cross_chain.GatewayEngine) {
	harnessRegistriesMu.RLock()
	defer harnessRegistriesMu.RUnlock()
	if len(harnessRegistries) == 0 {
		return
	}
	if engine.ChainRegistry == nil {
		engine.ChainRegistry = make(map[uint64]cross_chain.ChainRegistry)
	}
	for id, reg := range harnessRegistries {
		if _, exists := engine.ChainRegistry[id]; !exists {
			engine.ChainRegistry[id] = reg
		}
	}
}

func harnessGatewayAccountFallback() types.AccountState {
	return state.NewAccountState(mt_common.GATEWAY_CONTRACT_ADDRESS)
}

// GetABI returns the parsed Gateway ABI.
func (h *GatewayHandler) GetABI() *abi.ABI {
	return &h.abi
}

// LoadGatewayEngine exposes loadGatewayEngine to the spike.
func LoadGatewayEngine(chainState *blockchain.ChainState) (*cross_chain.GatewayEngine, error) {
	return loadGatewayEngine(chainState)
}

// SaveGatewayEngine exposes saveGatewayEngine to the spike.
func SaveGatewayEngine(chainState *blockchain.ChainState, engine *cross_chain.GatewayEngine) error {
	return saveGatewayEngine(chainState, engine)
}
