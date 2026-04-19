package processor

import (
	"context"
	"sync"
	"time"

	c_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/pkg/connection_manager"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
)

// CrossChainCache stores cross-chain transaction information to prevent duplicates
type CrossChainCache struct {
	mu          sync.Mutex
	Connections sync.Map    // map[t_network.Connection]bool - true=processed, false=waiting
	Result      interface{} // Flexible: *pb_cross.ContractCrossChainResponse, *pb_cross.TransferResponse, etc.
	Timestamp   time.Time
}

// SupervisorProcessor handles verification requests
type SupervisorProcessor struct {
	config            *c_config.ClientConfig
	messageSender     t_network.MessageSender
	crossChainCache   sync.Map // map[txHash]*CrossChainCache - cache for cross-chain transactions
	ctx               context.Context
	cancel            context.CancelFunc
	connectionManager *connection_manager.ConnectionManager
}

// NewSupervisorProcessor creates a new supervisor processor
func NewSupervisorProcessor(
	cfg *c_config.ClientConfig,
	messageSender t_network.MessageSender,
) *SupervisorProcessor {
	ctx, cancel := context.WithCancel(context.Background())

	processor := &SupervisorProcessor{
		config:        cfg,
		messageSender: messageSender,
		ctx:           ctx,
		cancel:        cancel,
	}
	// Start cleanup goroutine to remove old cache entries every minute
	go processor.cleanupCacheRoutine()
	return processor
}

func (p *SupervisorProcessor) SetConnectionManager(cm *connection_manager.ConnectionManager) {
	p.connectionManager = cm
}
func (p *SupervisorProcessor) GetConnectionManager() *connection_manager.ConnectionManager {
	return p.connectionManager
}
func (p *SupervisorProcessor) GetDefaultCfg() *c_config.ClientConfig {
	return p.config
}

// cleanupCacheRoutine runs every minute to clean up old cache entries
func (p *SupervisorProcessor) cleanupCacheRoutine() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			logger.Info("Stopping cache cleanup routine")
			return
		case <-ticker.C:
			now := time.Now()
			deletedCount := 0
			p.crossChainCache.Range(func(key, value interface{}) bool {
				txHash := key.(string)
				cache := value.(*CrossChainCache)

				cache.mu.Lock()
				age := now.Sub(cache.Timestamp)
				cache.mu.Unlock()
				// Delete entries older than 1 minute
				if age > 1*time.Minute {
					p.crossChainCache.Delete(txHash)
					deletedCount++
					logger.Debug("Cleaned up cache entry for txHash: %s (age: %v)", txHash, age)
				}
				return true
			})

			if deletedCount > 0 {
				logger.Info("Cache cleanup completed: removed %d entries", deletedCount)
			}
		}
	}
}

// Stop gracefully stops the processor and disconnects all clients
func (p *SupervisorProcessor) Stop() {
	p.cancel()
}
