package relayer_daemon

import (
	"context"
	"math/big"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain/rootanchor"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// gasPriceCacheEntry caches a chain's last-resolved gas price for a short TTL, so a tight burst
// of sends to the same chain (e.g. RelayBatch's several attest/claim calls within one tick)
// doesn't issue a fresh eth_gasPrice RPC for every single transaction.
type gasPriceCacheEntry struct {
	price     *big.Int
	fetchedAt time.Time
}

// resolveGasPrice returns the gas price sendToChain should use for chainID.
//
// PRODUCTION-READINESS FIX (2026-09-05): before this, every transaction RelayerDaemon submitted
// used a hardcoded `big.NewInt(1_000_000_000)` (1 Gwei) with zero fee-market awareness at all --
// found during a review of the Relayer architecture for production readiness. On any chain whose
// real fee market rises meaningfully above 1 Gwei, this hardcoded value would leave relayed
// transactions sitting unmined indefinitely (a stuck cross-chain message, indistinguishable from a
// dead relayer without deep RPC inspection); on a quiet chain it silently overpays.
//
// Resolution order: (1) reuse a still-fresh cached price for this chain if one exists: (2)
// otherwise query the chain's own eth_gasPrice; (3) on any query failure or a non-positive result,
// fall back to config.FallbackGasPriceWei (defaulting to the old hardcoded 1 Gwei constant, so the
// failure-mode behavior is unchanged from before this fix); (4) apply config.GasPriceBumpPercent
// if set (>100) to bias towards faster inclusion; (5) clamp to config.MaxGasPriceWei if set, as a
// hard safety ceiling against a compromised/buggy RPC endpoint suggesting an absurd price and
// draining the relayer's balance on a single transaction.
func (d *RelayerDaemon) resolveGasPrice(ctx context.Context, chainID uint64, client *rootanchor.Client) *big.Int {
	ttl := d.config.GasPriceCacheTTL
	if ttl <= 0 {
		ttl = 5 * time.Second
	}

	d.gasPriceCacheMu.Lock()
	if entry, ok := d.gasPriceCache[chainID]; ok && time.Since(entry.fetchedAt) < ttl {
		d.gasPriceCacheMu.Unlock()
		return entry.price
	}
	d.gasPriceCacheMu.Unlock()

	fallback := d.config.FallbackGasPriceWei
	if fallback == nil || fallback.Sign() <= 0 {
		fallback = big.NewInt(1_000_000_000) // 1 Gwei -- matches the old hardcoded constant exactly
	}

	price, err := client.SuggestGasPrice(ctx)
	if err != nil || price == nil || price.Sign() <= 0 {
		logger.Warn("⚠️ [RELAYER DAEMON] eth_gasPrice query failed for chain %d, using fallback %s wei: %v", chainID, fallback.String(), err)
		price = new(big.Int).Set(fallback)
	} else if bump := d.config.GasPriceBumpPercent; bump > 100 {
		price = new(big.Int).Div(new(big.Int).Mul(price, new(big.Int).SetUint64(bump)), big.NewInt(100))
	}

	if cap := d.config.MaxGasPriceWei; cap != nil && cap.Sign() > 0 && price.Cmp(cap) > 0 {
		logger.Warn("⚠️ [RELAYER DAEMON] resolved gas price %s wei for chain %d exceeds configured cap, clamping to %s wei", price.String(), chainID, cap.String())
		price = new(big.Int).Set(cap)
	}

	d.gasPriceCacheMu.Lock()
	d.gasPriceCache[chainID] = gasPriceCacheEntry{price: price, fetchedAt: time.Now()}
	d.gasPriceCacheMu.Unlock()

	priceF, _ := new(big.Float).SetInt(price).Float64()
	relayGasPriceWei.WithLabelValues(chainIDLabel(chainID)).Set(priceF)
	return price
}
