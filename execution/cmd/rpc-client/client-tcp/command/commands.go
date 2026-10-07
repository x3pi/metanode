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
	SendTransactions             = "SendTransactions"
	SendRawTransaction           = "SendRawTransaction"
	SendRawTransactions          = "SendRawTransactions"
	GetAccountState              = "GetAccountState"
	SubscribeToAddress           = "SubscribeToAddress"
	GetStakeState                = "GetStakeState"
	GetSmartContractData         = "GetSmartContractData"
	GetNonce                     = "GetNonce"

	GetDeviceKey = "GetDeviceKey"

	// Receive message
	Nonce              = "Nonce"
	AccountState       = "AccountState"
	StakeState         = "StakeState"
	Receipt            = "Receipt"
	TransactionError   = "TransactionError"
	EventLogs          = "EventLogs"
	QueryLogs          = "QueryLogs"
	SmartContractData  = "SmartContractData"
	DeviceKey          = "DeviceKey"
	TransactionSuccess = "TransactionSuccess"

	ServerBusy = "ServerBusy"
)
