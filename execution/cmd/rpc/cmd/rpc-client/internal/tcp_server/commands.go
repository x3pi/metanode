package tcp_server

// TCP RPC Server command constants
// Client gửi command qua TCP, server xử lý và trả response
const (
	// RPC Commands - Client gửi lên server
	CmdNetVersion = "NetVersion" // Lấy chain ID (net_version)
	CmdEthChainId = "EthChainId" // Lấy chain ID hex (eth_chainId)
	CmdEthCall    = "EthCall"    // eth_call

	// RPC Response - Server trả về cho client
	CmdRpcResponse = "RpcResponse" // Response cho bất kỳ RPC request nào

	// RPC Event - Server push event về client (subscription)
	CmdRpcEvent = "RpcEvent" // eth_subscription event push
)
