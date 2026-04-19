package command

const (
	InitConnection = "InitConnection"
	InitMaster     = "InitMaster"
	Ping           = "Ping"

	// transaction
	TransactionError   = "TransactionError"
	TransactionSuccess = "TransactionSuccess"
	ReadTransaction  = "ReadTransaction"
	EstimateGas      = "EstimateGas"

	SendTransaction                 = "SendTransaction"
	SendTransactions                = "SendTransactions"
	SendProcessedVirtualTransaction = "SendProcessedVirtualTransaction"

	SendTransactionWithDeviceKey  = "SendTransactionWithDeviceKey"
	TxWithDeviceKeyFromMainMaster = "TxWithDeviceKeyFromMainMaster"
	BlockFromMainMaster           = "BlockFromMainMaster"
	BlockDataFromMainMaster       = "BlockDataFromMainMaster"

	EventLogs = "EventLogs"

	Receipt = "Receipt"

	// state
	GetAccountState = "GetAccountState"
	GetNonce        = "GetNonce"
	Nonce           = "Nonce"

	GetDeviceKey       = "GetDeviceKey"
	DeviceKey          = "DeviceKey"
	AccountState       = "AccountState"
	GetBlockNumber     = "GetBlockNumber"
	GetLastBlockHeader = "GetLastBlockHeader"
	BlockNumber        = "BlockNumber"
	LastBlockNumber    = "LastBlockNumber"

	SubscribeToAddress         = "SubscribeToAddress"
	GetBlockByNumberFromMaster = "GetBlockByNumberFromMaster"
	GetLastBlockNumber         = "GetLastBlockNumber"

	GetBlockDataByNumberFromMaster = "GetBlockDataByNumberFromMaster"
	DeviceKeyData                  = "DeviceKeyData"
	RemoteDeviceKeyDB              = "RemoteDeviceKeyDB"

	GetTransactionsByBlockNumber = "GetTransactionsByBlockNumber"
	TransactionsByBlockNumber    = "TransactionsByBlockNumber"

	GetBlockHeaderByBlockNumber = "GetBlockHeaderByBlockNumber"
	BlockHeaderByBlockNumber    = "BlockHeaderByBlockNumber"

	Job    = "Job"
	GetJob = "GetJob"

	SetCompleteJob = "SetCompleteJob"
	CompleteJob    = "CompleteJob"

	GetTxRewardHistoryByAddress = "GetTxRewardHistoryByAddress"
	TxRewardHistoryByAddress    = "TxRewardHistoryByAddress"

	GetTxRewardHistoryByJobID = "GetTxRewardHistoryByJobID"
	TxRewardHistoryByJobID    = "TxRewardHistoryByJobID"

	GetLogs = "GetLogs"
	Logs    = "Logs"

	GetTransactionReceipt = "GetTransactionReceipt"
	TransactionReceipt    = "TransactionReceipt"

	GetTransactionByHash = "GetTransactionByHash"
	TransactionByHash    = "TransactionByHash"

	GetChainId = "GetChainId"
	ChainId    = "ChainId"
)
