package command

const (
	//General
	InitConnection = "InitConnection"
	Ping           = "Ping"

	GetStats       = "GetStats"
	Stats          = "Stats"
	ChangeLogLevel = "ChangeLogLevel"

	// Send messages
	ReadTransaction              = "ReadTransaction"
	SendTransaction              = "SendTransaction"
	SendTransactionWithDeviceKey = "SendTransactionWithDeviceKey"
	SendTransactions             = "SendTransactions"
	GetAccountState              = "GetAccountState"
	SubscribeToAddress           = "SubscribeToAddress"
	GetStakeState                = "GetStakeState"
	GetSmartContractData         = "GetSmartContractData"
	GetNonce                     = "GetNonce"

	GetDeviceKey = "GetDeviceKey"

	// Receive message
	Nonce             = "Nonce"
	AccountState      = "AccountState"
	StakeState        = "StakeState"
	Receipt           = "Receipt"
	TransactionError  = "TransactionError"
	EventLogs         = "EventLogs"
	QueryLogs         = "QueryLogs"
	SmartContractData = "SmartContractData"
	DeviceKey         = "DeviceKey"

	ServerBusy = "ServerBusy"

	// RPC TCP Commands - gửi lên RPC server qua TCP
	RpcNetVersion            = "NetVersion"             // net_version
	RpcEthChainId            = "eth_chainId"            // eth_chainId
	RpcEthSendRawTransaction = "eth_sendRawTransaction" // eth_sendRawTransaction

	// TCP-direct commands (RPC Server → TCP → Chain, không dùng HTTP)
	TcpGetChainId = "tcp_getChainId"

	// RPC TCP Response/Event - nhận từ RPC server
	RpcResponse = "RpcResponse"
	RpcEvent    = "RpcEvent" // eth_subscription event push từ server

	// Chain-direct commands — gửi thẳng lên chain, không qua RPC proxy
	// Dùng header ID để match request/response
	GetChainId            = "GetChainId"
	ChainId               = "ChainId"
	GetTransactionReceipt = "GetTransactionReceipt"
	TransactionReceipt    = "TransactionReceipt"
	GetBlockNumber        = "GetBlockNumber"
	BlockNumber           = "BlockNumber"

	GetLogs = "GetLogs"
	Logs    = "Logs"

	GetTransactionByHash = "GetTransactionByHash"
	TransactionByHash    = "TransactionByHash"

	TransactionSuccess = "TransactionSuccess"
)
