package network

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsTxSubmissionCommand(t *testing.T) {
	require.True(t, isTxSubmissionCommand("SendRawTransaction"))
	require.True(t, isTxSubmissionCommand("SendRawTransactions"))
	for _, c := range []string{"InitConnection", "GetNonce", "ReadTransaction", "Ping", ""} {
		require.False(t, isTxSubmissionCommand(c), c)
	}
}

func TestTxAdmissionGate_SetAndClear(t *testing.T) {
	t.Cleanup(func() { SetTxAdmissionGate(nil) })
	require.Nil(t, loadTxAdmissionGate())

	called := false
	SetTxAdmissionGate(func(ctx context.Context) error { called = true; return nil })
	g := loadTxAdmissionGate()
	require.NotNil(t, g)
	require.NoError(t, g(context.Background()))
	require.True(t, called)

	SetTxAdmissionGate(nil)
	require.Nil(t, loadTxAdmissionGate())
}
