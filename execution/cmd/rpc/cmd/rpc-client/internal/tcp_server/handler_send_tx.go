package tcp_server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	ethCommon "github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	app_handlers "github.com/meta-node-blockchain/meta-node/cmd/rpc-client/handlers"
	"github.com/meta-node-blockchain/meta-node/pkg/account_handler"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/connection_manager/connection_client"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	robothandler "github.com/meta-node-blockchain/meta-node/pkg/robot_handler"
	mt_types "github.com/meta-node-blockchain/meta-node/types"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

// handleHttpSendRawTransaction - Xử lý http_sendRawTransaction qua HTTP thay vì TCP
func (srv *RpcTcpServer) handleHttpSendRawTransaction(request t_network.Request) error {
	conn := request.Connection()
	msgID := request.Message().ID()

	// Parse body to get the raw tx hex
	var params []string
	body := request.Message().Body()
	if len(body) > 0 {
		_ = json.Unmarshal(body, &params)
	}

	if len(params) == 0 || params[0] == "" {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32602,
			Message: "Invalid params: missing raw transaction hex",
		})
	}
	rawTxHex := params[0]
	httpResult := app_handlers.ProcessSendRawTransaction(srv.AppCtx, rawTxHex, msgID)
	return srv.sendTcpResponse(conn, httpRespToTcpResp(httpResult, msgID))
}

// handleSendRawTransaction - build BLS key, convert Ethereum tx → MetaTx
// Gửi TX qua TCP trực tiếp và chờ receipt/error
// Chỉ nhận proto TcpSendTxRequest (TCP-only handler)
func (srv *RpcTcpServer) handleSendRawTransaction(request t_network.Request) error {
	conn := request.Connection()
	msgID := request.Message().ID()
	body := request.Message().Body()

	// Parse proto TcpSendTxRequest
	tcpReq := &pb.TcpSendTxRequest{}
	if err := proto.Unmarshal(body, tcpReq); err != nil || len(tcpReq.RawTx) == 0 {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32602,
			Message: "Invalid params: failed to parse TcpSendTxRequest",
		})
	}
	rawTxBytes := tcpReq.RawTx

	// Decode Ethereum TX
	ethTx := new(e_types.Transaction)
	if err := ethTx.UnmarshalBinary(rawTxBytes); err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "Failed to unmarshal Ethereum transaction: " + err.Error(),
		})
	}

	chainClient, err := srv.AppCtx.ChainPool.Get()
	if err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "Failed to get chain connection: " + err.Error(),
		})
	}

	return srv.handleSendRawTransactionTCP(conn, msgID, rawTxBytes, ethTx, chainClient)
}

// handleSendRawTransactionTCP gửi TX qua chain TCP connection, chờ TransactionSuccess/Error.
// Nhận raw bytes trực tiếp (không cần hex decode lại)
func (srv *RpcTcpServer) handleSendRawTransactionTCP(conn t_network.Connection, msgID string, rawTxBytes []byte, ethTx *e_types.Transaction, chainClient *connection_client.ConnectionClient) error {
	// Tạo rawTxHex cho interceptor handlers (cần hex string)
	rawTxHex := "0x" + hex.EncodeToString(rawTxBytes)
	// Lưu original ETH tx hash trước khi build BLS transaction
	ethTxHash := ethTx.Hash()
	var (
		bTx       []byte
		tx        mt_types.Transaction
		releaseTx func()
		buildErr  error
	)

	// Retrieve fromAddress for PKS check
	signer := e_types.LatestSignerForChainID(srv.AppCtx.ClientRpc.ChainId)
	fromAddr, err := e_types.Sender(signer, ethTx)
	if err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{Code: -32603, Message: "Failed to get sender: " + err.Error()})
	}

	exists, err := srv.AppCtx.PKS.HasPrivateKey(fromAddr)
	if err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{Code: -32603, Message: "error checking private key store: " + err.Error()})
	}

	// topUpFunc: đưa giao dịch chuyển native coin vào hàng chờ owner (tuần tự) để tránh nonce conflict
	ownerAddr := ethCommon.HexToAddress(srv.AppCtx.Cfg.OwnerRpcAddress)
	topUpFunc := func(toAddress ethCommon.Address) error {
		ah, err := account_handler.GetAccountHandler(srv.AppCtx)
		if err != nil {
			return fmt.Errorf("get account handler error: %w", err)
		}
		result := ah.SendOwnerTransfer(ownerAddr, toAddress, srv.AppCtx.Cfg.ExtraAmount)
		return result.Err
	}

	if !exists {
		bTx, tx, releaseTx, buildErr = srv.AppCtx.ClientRpc.BuildTransactionWithDeviceKeyFromEthTxTCP(
			ethTx, srv.AppCtx.TcpCfg, srv.AppCtx.Cfg, srv.AppCtx.LdbContractFreeGas, false, chainClient, topUpFunc,
		)
	} else {
		senderPkString, _ := srv.AppCtx.PKS.GetPrivateKey(fromAddr)
		keyPair := bls.NewKeyPair(ethCommon.FromHex(senderPkString))
		bTx, tx, releaseTx, buildErr = srv.AppCtx.ClientRpc.BuildTransactionWithDeviceKeyFromEthTxAndBlsPrivateKeyTCP(
			ethTx, srv.AppCtx.TcpCfg, srv.AppCtx.Cfg, srv.AppCtx.LdbContractFreeGas, keyPair.PrivateKey(), chainClient, topUpFunc,
		)
	}
	txReleased := false
	releaseTxOnce := func() {
		if txReleased {
			return
		}
		txReleased = true
		if releaseTx != nil {
			releaseTx()
		}
	}
	defer releaseTxOnce()
	if buildErr != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "Failed to build transaction: " + buildErr.Error(),
		})
	}
	if tx != nil {
		// 1. Account Interceptor
		if tx.ToAddress() == ethCommon.HexToAddress(srv.AppCtx.Cfg.ContractsInterceptor[0]) {
			accountHandler, err := account_handler.GetAccountHandler(srv.AppCtx)
			if err != nil {
				return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{Code: -32603, Message: "Failed to get account handler: " + err.Error()})
			}
			handled, result, err := accountHandler.HandleAccountTransaction(context.Background(), tx, rawTxHex)
			if handled {
				releaseTxOnce()
				if err != nil {
					logger.Error("Account handler transaction error: %v", err)
					return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{Code: -32603, Message: "Account handler transaction error: " + err.Error()})
				}
				finalHash := tx.Hash()
				if result != nil {
					if txHashStr, ok := result.(string); ok && txHashStr != "" {
						finalHash = ethCommon.HexToHash(txHashStr)
					}
				}
				hashResp := &pb.TcpHashParam{Hash: finalHash.Bytes()}
				resultBytes, _ := proto.Marshal(hashResp)
				return srv.sendRpcResponse(conn, msgID, resultBytes, nil)
			} else if !handled && err == nil {
				return srv.sendNormalTCPTransaction(conn, msgID, bTx, ethTxHash, chainClient)
			}
			return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{Code: -32603, Message: "Method not found in ABI account"})
		}

		// 2. Robot Interceptor
		if tx.ToAddress() == ethCommon.HexToAddress(srv.AppCtx.Cfg.ContractsInterceptor[1]) {
			robotHandler, err := robothandler.GetRobotHandler(srv.AppCtx)
			if err != nil {
				logger.Error("❌ [sendRawTransaction] Failed to get robot handler: %v", err)
				return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{Code: -32603, Message: "Failed to get robot handler: " + err.Error()})
			}
			handled, result, err := robotHandler.HandleRobotTransaction(context.Background(), tx, rawTxHex)
			if handled {
				releaseTxOnce()
				if err != nil {
					logger.Error("❌ [sendRawTransaction] Robot handler transaction error: %v", err)
					return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{Code: -32603, Message: "Robot handler transaction error: " + err.Error()})
				}
				var finalHash ethCommon.Hash
				if result != nil {
					if txHashStr, ok := result.(string); ok && txHashStr != "" {
						finalHash = ethCommon.HexToHash(txHashStr)
					}
				} else {
					finalHash = tx.Hash()
				}
				hashResp := &pb.TcpHashParam{Hash: finalHash.Bytes()}
				resultBytes, _ := proto.Marshal(hashResp)
				return srv.sendRpcResponse(conn, msgID, resultBytes, nil)
			} else if !handled && err == nil {
				return srv.sendNormalTCPTransaction(conn, msgID, bTx, ethTxHash, chainClient)
			}
			return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{Code: -32603, Message: "Method not found in ABI robot"})
		}
		// 3. Normal Transaction (Dùng TCP)
		return srv.sendNormalTCPTransaction(conn, msgID, bTx, ethTxHash, chainClient)

	} else {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "Null transaction returned after build",
		})
	}
}

// Helper: gửi bình thường nếu không bị intercept
// SendTransactionWithDeviceKeyAndWaitReceipt chờ receipt từ chain (proto Receipt).
// Nếu receipt timeout → fallback trả txHash.
// Nếu TX là chuyển tiền (Amount > 0) → forward receipt cho toAddress.
func (srv *RpcTcpServer) sendNormalTCPTransaction(conn t_network.Connection, msgID string, bTx []byte, ethTxHash ethCommon.Hash, chainClient *connection_client.ConnectionClient) error {
	responseBytes, err := chainClient.SendTransactionWithDeviceKeyAndWaitReceipt(bTx, 60*time.Second)
	if err != nil {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32603,
			Message: "SendTransactionWithDeviceKeyAndWaitReceipt error: " + err.Error(),
		})
	}
	// Detect if response is a receipt (proto-encoded, >32 bytes) or txHash (32 bytes)
	// Receipt bytes (proto Receipt) — return directly to sender
	logger.Info("✅ TCP eth_sendRawTransaction: got RECEIPT (%d bytes)", len(responseBytes))
	// Forward receipt cho toAddress nếu TX là chuyển tiền
	go srv.forwardReceiptToRecipient(responseBytes)
	return srv.sendRpcResponse(conn, msgID, responseBytes, nil)
}

// forwardReceiptToRecipient parse receipt, nếu TX là chuyển tiền (Amount > 0)
// thì tìm toAddress trong clientConnections và gửi receipt.
func (srv *RpcTcpServer) forwardReceiptToRecipient(receiptBytes []byte) {
	rcpt := &pb.Receipt{}
	if err := proto.Unmarshal(receiptBytes, rcpt); err != nil {
		return
	}
	// Check: có Amount và > 0 → là chuyển tiền
	if len(rcpt.Amount) == 0 {
		return
	}
	// Amount là big.Int bytes, check nếu tất cả = 0 thì bỏ qua
	allZero := true
	for _, b := range rcpt.Amount {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return
	}
	// Lấy toAddress
	toAddr := ethCommon.BytesToAddress(rcpt.ToAddress)
	if toAddr == (ethCommon.Address{}) {
		return
	}
	// Tìm connection của người nhận
	recipientConn := srv.GetClientConnection(toAddr)
	if recipientConn == nil {
		logger.Warn("⚠️ Failed to forward receipt to %s: connection not found", toAddr.Hex())
		return
	}
	// Gửi receipt cho người nhận qua command "Receipt"
	if err := srv.messageSender.SendBytes(recipientConn, "Receipt", receiptBytes); err != nil {
		logger.Warn("⚠️ Failed to forward receipt to %s: %v", toAddr.Hex(), err)
	}
}
