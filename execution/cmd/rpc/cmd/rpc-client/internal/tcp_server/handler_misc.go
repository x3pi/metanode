package tcp_server

import (
	"encoding/binary"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/handlers"
	pkgCommon "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
)

// handleChainIdDirect - lấy chainId qua TCP trực tiếp
func (srv *RpcTcpServer) handleChainIdDirect(request t_network.Request) error {
	conn := request.Connection()
	msg := request.Message()
	if msg == nil {
		return nil
	}

	chainId := srv.AppCtx.Cfg.ChainId
	body := make([]byte, 8)
	binary.BigEndian.PutUint64(body, chainId.Uint64())

	respMsg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command:   pkgCommon.ChainId,
			Version:   msg.Version(),
			ToAddress: conn.Address().Bytes(),
			ID:        msg.ID(),
		},
		Body: body,
	})
	logger.Info("✅ TCP GetChainId: sent ChainId for reqID=%s chainId=%d", msg.ID(), chainId.Uint64())
	return conn.SendMessage(respMsg)
}
func (srv *RpcTcpServer) handleGetAccountState(request t_network.Request) error {
	conn := request.Connection()
	msg := request.Message()
	if msg == nil {
		return nil
	}
	body := msg.Body()
	if len(body) != common.AddressLength {
		return srv.sendRpcResponse(conn, msg.ID(), nil, &pb.RpcError{
			Code:    -32602,
			Message: "Invalid params: GetAccountState body must be 20-byte address",
		})
	}
	if srv.AppCtx == nil || srv.AppCtx.ChainPool == nil {
		return srv.sendRpcResponse(conn, msg.ID(), nil, &pb.RpcError{
			Code:    -32603,
			Message: "Chain connection pool is not available",
		})
	}
	chainClient, err := srv.AppCtx.ChainPool.Get()
	if err != nil {
		return srv.sendRpcResponse(conn, msg.ID(), nil, &pb.RpcError{
			Code:    -32603,
			Message: "Failed to get chain connection: " + err.Error(),
		})
	}
	asBytes, err := chainClient.GetAccountState(body, 30*time.Second)
	if err != nil {
		return srv.sendRpcResponse(conn, msg.ID(), nil, &pb.RpcError{
			Code:    -32603,
			Message: "GetAccountState TCP error: " + err.Error(),
		})
	}

	// Trả về đúng command TCP cũ để iOS có thể dùng lại handler hiện tại.
	respMsg := network.NewMessage(&pb.Message{
		Header: &pb.Header{
			Command:   pkgCommon.AccountState,
			Version:   msg.Version(),
			ToAddress: conn.Address().Bytes(),
			ID:        msg.ID(),
		},
		Body: asBytes,
	})
	logger.Info("✅ TCP GetAccountState: sent AccountState for reqID=%s (%d bytes)", msg.ID(), len(asBytes))
	return conn.SendMessage(respMsg)
}

// handleRegisterBlsKey - xử lý giống HTTP handler
func (srv *RpcTcpServer) handleRegisterBlsKey(request t_network.Request) error {
	conn := request.Connection()
	msgID := request.Message().ID()

	params := parseParamsRaw(request.Message().Body())
	if len(params) == 0 {
		return srv.sendRpcResponse(conn, msgID, nil, &pb.RpcError{
			Code:    -32602,
			Message: "Invalid params for rpc_registerBlsKeyWithSignature",
		})
	}

	logger.Info("🔄 TCP rpc_registerBlsKeyWithSignature from %s", conn.RemoteAddrSafe())
	httpResp := handlers.HandleRpcRegisterBlsKeyWithSignatureRaw(srv.AppCtx, params[0], msgID)
	resp := httpRespToTcpResp(httpResp, msgID)

	if resp.Error == nil {
		logger.Info("✅ TCP rpc_registerBlsKeyWithSignature result: %s", string(resp.Result))
	}
	return srv.sendTcpResponse(conn, resp)
}
