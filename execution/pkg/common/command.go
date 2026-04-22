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

	// Monitor
	MonitorData = "MonitorData"

	Ping = "Ping"

	Response = "Response"

	ServerBusy = "ServerBusy"
	// Cross chain
	VerifyTransaction            = "VerifyTransaction"
	ContractCrossChainResponse   = "ContractCrossChainResponse"
	ContractCrossChainRequest    = "ContractCrossChainRequest"
	CrossClusterTransferAck      = "CrossClusterTransferAck"
	GetTransactionsByBlockNumber = "GetTransactionsByBlockNumber"
	TransactionsByBlockNumber    = "TransactionsByBlockNumber"
	// contract cross chain
	SendContractCrossChain         = "SendContractCrossChain"
	SendContractCrossChainResponse = "SendContractCrossChainResponse"
)
