package tx_processor

import (
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cross_chain.velocity_limit_disabled in the node config is copied into the gateway engine state on load, and not
// switched back off by a later load without the setting (it lives in the state, so every validator agrees).
func TestApplySecurityBondConfig_SeedsVelocityLimitDisabled(t *testing.T) {
	prev := config.ConfigApp
	t.Cleanup(func() { config.ConfigApp = prev })

	config.ConfigApp = &config.SimpleChainConfig{}
	engine := cross_chain.NewGatewayEngine(102, nil, nil)
	require.NoError(t, applySecurityBondConfig(engine))
	assert.False(t, engine.VelocityLimitDisabled, "unset in the config: the 20%/24h cap stays on")

	config.ConfigApp.CrossChain.VelocityLimitDisabled = true
	require.NoError(t, applySecurityBondConfig(engine))
	assert.True(t, engine.VelocityLimitDisabled, "set in the config: the cap is switched off in the engine state")

	config.ConfigApp.CrossChain.VelocityLimitDisabled = false
	require.NoError(t, applySecurityBondConfig(engine))
	assert.True(t, engine.VelocityLimitDisabled, "a later load without the setting does not switch the cap back on")
}
