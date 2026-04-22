package network

import (
	"errors"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/tcp-rpc/client-tcp/command"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/receipt"
	"github.com/meta-node-blockchain/meta-node/pkg/state"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

type TpsHandler struct {
	accountStateChan chan types.AccountState
	receiptChan      chan types.Receipt
}

func NewTpsHandler(
	accountStateChan chan types.AccountState,
	receiptChan chan types.Receipt,
) *TpsHandler {
	return &TpsHandler{
		accountStateChan: accountStateChan,
		receiptChan:      receiptChan,
	}
}

func (h *TpsHandler) HandleRequest(r network.Request) error {
	start := time.Now()
	logger.Trace("Handling command " + r.Message().Command())
	defer logger.Trace(
		"Handled command " + r.Message().Command() + "took " + time.Since(start).String(),
	)
	switch r.Message().Command() {
	case command.InitConnection:
		return h.handleInitConnection(r)
	case command.AccountState:
		return h.handleAccountState(r)
	case command.Nonce:
		return h.handleNonce(r)
	case command.Receipt:
		return h.handleReceipt(r)
	}

	return errors.New("command not found: " + r.Message().Command())
}

func (h *TpsHandler) handleInitConnection(request network.Request) (err error) {
	conn := request.Connection()
	initData := &pb.InitConnection{}
	err = request.Message().Unmarshal(initData)
	if err != nil {
		return err
	}
	address := common.BytesToAddress(initData.Address)
	logger.Debug(fmt.Sprintf(
		"init connection from %v type %v", address, initData.Type,
	))
	conn.Init(address, initData.Type)
	return nil
}

func (h *TpsHandler) handleAccountState(request network.Request) (err error) {
	accountState := &state.AccountState{}
	err = accountState.Unmarshal(request.Message().Body())
	if err != nil {
		return err
	}
	// logger.Debug(fmt.Sprintf("Receive Account state: \n%v", accountState))
	h.accountStateChan <- accountState
	return nil
}

func (h *TpsHandler) handleNonce(request network.Request) (err error) {
	accountState := &state.AccountState{}
	err = accountState.Unmarshal(request.Message().Body())
	if err != nil {
		return err
	}
	// logger.Debug(fmt.Sprintf("Receive Account state: \n%v", accountState))
	h.accountStateChan <- accountState
	return nil
}

func (h *TpsHandler) handleReceipt(request network.Request) (err error) {
	receipt := &receipt.Receipt{}
	err = receipt.Unmarshal(request.Message().Body())
	if err != nil {
		return err
	}
	logger.Debug(fmt.Sprintf("Receive receipt: %v", receipt))

	return nil
}
