package common

const (
	InitConnection                  = "InitConnection"
	GetSmartContractStorage         = "GetSmartContractStorage"
	GetSmartContractStorageResponse = "GetSmartContractStorageResponse"

	GetSmartContractCode         = "GetSmartContractCode"
	GetSmartContractCodeResponse = "GetSmartContractCodeResponse"
	GetSCStorageDBData           = "GetSCStorageDBData"

	GetNodeStateRoot             = "GetNodeStateRoot"
	GetAccountState              = "GetAccountState"
	GetAccountStateWithIdRequest = "GetAccountStateWithIdRequest"
	AccountState                 = "AccountState"
	AccountStateWithIdRequest    = "AccountStateWithIdRequest"

	CancelNodePendingState = "CancelNodePendingState"

	// syncs
	GetNodeSyncData = "GetNodeSyncData"
	NodeSyncData    = "NodeSyncData"

	NodeSyncDataFromNode      = "NodeSyncDataFromNode"
	NodeSyncDataFromValidator = "NodeSyncDataFromValidator"

	GetDeviceKey = "GetDeviceKey"
	DeviceKey    = "DeviceKey"
	Response     = "Response"

	// Monitor
	MonitorData = "MonitorData"

	ServerBusy = "ServerBusy"
	GetLogs    = "GetLogs"
	Logs       = "Logs"

	GetTransactionReceipt = "GetTransactionReceipt"
	TransactionReceipt    = "TransactionReceipt"

	GetTransactionByHash = "GetTransactionByHash"
	TransactionByHash    = "TransactionByHash"

	GetChainId = "GetChainId"
	ChainId    = "ChainId"

	GetNonce = "GetNonce"
	Nonce    = "Nonce"
)
