package tx_processor

import "testing"

// The parent-chain gateway and rollup system handlers are process-wide singletons behind a sync.Once, so the first test to
// call Init* wins for the whole process: a second iteration (-count=2) or a different test order saw the first test's mock
// dispatcher and failed. Tests install their own instance for their duration instead.

func installParentChainGatewayHandlerForTest(t testing.TB, d CrossChainTransferDispatcher) {
	t.Helper()
	prev := parentChainGatewayHandlerInstance
	parentChainGatewayHandlerInstance = &ParentChainGatewayHandler{dispatcher: d}
	t.Cleanup(func() { parentChainGatewayHandlerInstance = prev })
}

func installRollupSystemHandlerForTest(t testing.TB, d RollupSystemEventDispatcher, regHandler AccountRegistryApplier) {
	t.Helper()
	prev := rollupSystemHandlerInstance
	rollupSystemHandlerInstance = &RollupSystemHandler{dispatcher: d, registryHandler: regHandler}
	t.Cleanup(func() { rollupSystemHandlerInstance = prev })
}
