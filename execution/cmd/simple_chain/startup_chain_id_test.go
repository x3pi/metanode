package main

import (
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/metrics"
	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

type mockParentClientForStatus struct {
	parentchain.Client
	status parentchain.ChainStatus
	err    error
}

func (m *mockParentClientForStatus) GetStatus() (parentchain.ChainStatus, error) {
	return m.status, m.err
}

func TestVerifyParentChainID_Scenarios(t *testing.T) {
	t.Run("nil client succeeds", func(t *testing.T) {
		err := verifyParentChainID(nil, 991)
		assert.NoError(t, err)
		assert.Equal(t, float64(0), testutil.ToFloat64(metrics.ParentChainIDMismatch))
	})

	t.Run("matching chain ID succeeds", func(t *testing.T) {
		client := &mockParentClientForStatus{
			status: parentchain.ChainStatus{ChainID: 991},
		}
		err := verifyParentChainID(client, 991)
		assert.NoError(t, err)
		assert.Equal(t, float64(0), testutil.ToFloat64(metrics.ParentChainIDMismatch))
	})

	t.Run("mismatched chain ID returns clear error", func(t *testing.T) {
		client := &mockParentClientForStatus{
			status: parentchain.ChainStatus{ChainID: 992},
		}
		err := verifyParentChainID(client, 991)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "parent chain ID mismatch: parent reports 992, local config has 991")
		assert.Equal(t, float64(1), testutil.ToFloat64(metrics.ParentChainIDMismatch))
	})

	t.Run("unreachable parent logs warning but does not fail", func(t *testing.T) {
		client := &mockParentClientForStatus{
			err: errors.New("connection refused"),
		}
		err := verifyParentChainID(client, 991)
		assert.NoError(t, err)
	})

	t.Run("legacy parent with zero chain ID succeeds", func(t *testing.T) {
		client := &mockParentClientForStatus{
			status: parentchain.ChainStatus{ChainID: 0},
		}
		err := verifyParentChainID(client, 991)
		assert.NoError(t, err)
	})
}
