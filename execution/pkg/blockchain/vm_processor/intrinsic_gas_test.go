package vm_processor

import (
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/stretchr/testify/assert"
)

func TestApplyIntrinsicGas_NilResult(t *testing.T) {
	// Should not panic on nil
	applyIntrinsicGas(nil, 21000, 100000, 0)
}

func TestApplyIntrinsicGas_MaxGasLessThanIntrinsic(t *testing.T) {
	res := &mvm.MVMExecuteResult{
		Status:  pb.RECEIPT_STATUS_RETURNED,
		GasUsed: 1000,
	}
	applyIntrinsicGas(res, 50000, 40000, 12500)
	assert.Equal(t, pb.RECEIPT_STATUS_TRANSACTION_ERROR, res.Status)
	assert.Equal(t, pb.EXCEPTION_ERR_OUT_OF_GAS, res.Exception)
	assert.Equal(t, uint64(40000), res.GasUsed)
	assert.Nil(t, res.Return)
}

func TestApplyIntrinsicGas_NoRefund(t *testing.T) {
	// PreRefund > 0, no refund
	res := &mvm.MVMExecuteResult{
		Status:       pb.RECEIPT_STATUS_RETURNED,
		GasPreRefund: 10000,
		GasRefund:    0,
		GasUsed:      10000,
	}
	applyIntrinsicGas(res, 21000, 100000, 0)
	// total = 10000 + 21000 = 31000, refund = 0 -> gasUsed = 31000
	assert.Equal(t, uint64(31000), res.GasUsed)
}

func TestApplyIntrinsicGas_AuthRefundCapped(t *testing.T) {
	// EIP-7702 test case:
	// intrinsic = 46000 (21000 + 25000)
	// VM pre-refund gas = 2723
	// auth refund = 12500
	// total = 2723 + 46000 = 48723
	// refund cap = 48723 / 5 = 9744
	// actual refund = 9744
	// gasUsed = 48723 - 9744 = 38979
	res := &mvm.MVMExecuteResult{
		Status:       pb.RECEIPT_STATUS_RETURNED,
		GasPreRefund: 2723,
		GasRefund:    0,
		GasUsed:      2723,
	}
	applyIntrinsicGas(res, 46000, 200000, 12500)
	assert.Equal(t, uint64(38979), res.GasUsed)
}

func TestApplyIntrinsicGas_AuthRefundUncapped(t *testing.T) {
	// If total gas is large enough, auth refund is fully granted:
	// intrinsic = 46000
	// VM pre-refund gas = 100000
	// total = 146000
	// cap = 146000 / 5 = 29200
	// auth refund = 12500 <= 29200
	// gasUsed = 146000 - 12500 = 133500
	res := &mvm.MVMExecuteResult{
		Status:       pb.RECEIPT_STATUS_RETURNED,
		GasPreRefund: 100000,
		GasRefund:    0,
		GasUsed:      100000,
	}
	applyIntrinsicGas(res, 46000, 200000, 12500)
	assert.Equal(t, uint64(133500), res.GasUsed)
}

func TestApplyIntrinsicGas_PooledVmRefundAndAuthRefund(t *testing.T) {
	// VM refund = 5000 (e.g. storage clear)
	// Auth refund = 12500
	// Total refund = 17500
	// intrinsic = 46000
	// VM pre-refund gas = 2723
	// total = 48723
	// cap = 48723 / 5 = 9744
	// refund capped at 9744
	// gasUsed = 48723 - 9744 = 38979
	res := &mvm.MVMExecuteResult{
		Status:       pb.RECEIPT_STATUS_RETURNED,
		GasPreRefund: 2723,
		GasRefund:    5000,
		GasUsed:      2723,
	}
	applyIntrinsicGas(res, 46000, 200000, 12500)
	assert.Equal(t, uint64(38979), res.GasUsed)
}

func TestApplyIntrinsicGas_ZeroGasPreRefundPath(t *testing.T) {
	// When GasPreRefund is 0 (e.g. VM didn't report it or zero execution gas),
	// total falls back to GasUsed + intrinsicGas.
	res := &mvm.MVMExecuteResult{
		Status:       pb.RECEIPT_STATUS_RETURNED,
		GasPreRefund: 0,
		GasRefund:    0,
		GasUsed:      3000,
	}
	applyIntrinsicGas(res, 46000, 200000, 12500)
	// total = 3000 + 46000 = 49000
	// cap = 49000 / 5 = 9800
	// refund = 9800
	// gasUsed = 49000 - 9800 = 39200
	assert.Equal(t, uint64(39200), res.GasUsed)
}
