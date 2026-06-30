package vm_processor

import (
	"context"

	"github.com/ethereum/go-ethereum/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	"github.com/meta-node-blockchain/meta-node/pkg/tee_revm_ffi"
	"github.com/meta-node-blockchain/meta-node/types"
)

// TeeResultToExecuteResult converts TeeStateDiff to ExecuteSCResult.
func (vmP *VmProcessor) TeeResultToExecuteResult(
	ctx context.Context,
	transaction types.Transaction,
	teeRs *tee_revm_ffi.TeeStateDiff,
) (types.ExecuteSCResult, error) {
	status := pb.RECEIPT_STATUS_TRANSACTION_ERROR
	if teeRs.Success {
		status = pb.RECEIPT_STATUS_RETURNED
	}
	rs := smart_contract.NewExecuteSCResult(
		transaction.Hash(), status, pb.EXCEPTION_NONE, nil,
		teeRs.GasUsed, common.Hash{}, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, nil,
	)
	return rs, nil
}

// UpdateStateDB is a stub for TEE REVM state updates.
func (vmP *VmProcessor) UpdateStateDB(
	ctx context.Context,
	tx types.Transaction,
	teeRs *tee_revm_ffi.TeeStateDiff,
	mvmId common.Address,
	isFree bool,
	isLeader bool,
) (bool, error) {
	// TODO: Implement actual state updates from TeeStateDiff's WriteKeys
	return false, nil
}
