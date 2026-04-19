package command

const (
	// Supervisor commands
	VerifyTransaction  = "VerifyTransaction"
	VerificationResult = "VerificationResult"
	Ping               = "Ping"
	InitConnection     = "InitConnection"

	// Chain commands (reused from simple_chain)
	GetBlockNumber               = "GetBlockNumber"
	BlockNumber                  = "BlockNumber"
	GetTransactionsByBlockNumber = "GetTransactionsByBlockNumber"
	TransactionsByBlockNumber    = "TransactionsByBlockNumber"
)
