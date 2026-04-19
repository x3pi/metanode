package common

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

const (
	MAX_VALIDATOR = 101
	// Gas cost defaults (can be overridden via chain config in future)
	TRANSFER_GAS_COST     = 20000
	OPEN_CHANNEL_GAS_COST = 20000000
	PUNISH_GAS_COST       = 10000

	BLOCK_GAS_LIMIT                       = 10000000000
	OFF_CHAIN_GAS_LIMIT                   = 1000000000
	BASE_FEE_INCREASE_GAS_USE_THRESH_HOLD = 5000000000
	MINIMUM_BASE_FEE                      = 1000000
	BASE_FEE_CHANGE_PERCENTAGE            = 12.5
	MAX_GASS_FEE                          = 5000000

	MAX_GROUP_GAS    = 9999999999999999999
	MAX_TOTAL_GAS    = 9999999999999999999
	MAX_GROUP_TIME   = 9999999999999999999 // Millisecond giây
	MAX_TOTAL_TIME   = 9999999999999999999 // Millisecond giây
	MIN_TX_TIME      = 1                   // Millisecond giây
	MAX_TIME_PENDING = 5                   // phút

	//Wallet Select
	ACCOUNT_SETTING_ADDRESS_SELECT = "account"

	StartFileUpload   = "START_FILE_UPLOAD"
	FileChunkTransfer = "FILE_CHUNK_TRANSFER"
	EndFileUploadCmd  = "END_FILE_UPLOAD"

	SyncFileRequest  = "SYNC_FILE_REQUEST"
	SyncFileResponse = "SYNC_FILE_RESPONSE" // Mặc dù không phải là một "lệnh" mà là một "phản hồi", việc định nghĩa nó có thể hữu ích

	// === HẰNG SỐ MỚI ===
	RequestDirectory = "REQUEST_DIRECTORY"

	// === CROSS-CHAIN TX TYPES ===
	// Dùng trong proto.Type (field 16) để phân biệt giai đoạn xử lý batchSubmit.
	// 0 = TX bình thường (standard)
	// 100 = CC_SIG_ACK: chưa đủ 2/3 embassy vote, master chỉ tăng nonce
	// 101 = CC_EXECUTE: đủ 2/3 vote, master sẽ execute CrossChainHandler
	TX_TYPE_CC_SIG_ACK uint64 = 100
	TX_TYPE_CC_EXECUTE uint64 = 101
)

var (
	VALIDATOR_STAKE_POOL_ADDRESS    = common.Address{}
	ADDRESS_FOR_UPDATE_TYPE_ACCOUNT = common.HexToAddress("0x0000000000000000000000000000000000000000")
	SLASH_VALIDATOR_AMOUNT          = uint256.NewInt(0).SetBytes(common.FromHex("8ac7230489e80000"))
	MINIMUM_VALIDATOR_STAKE_AMOUNT  = uint256.NewInt(0)
	MINIMUM_OPEN_ACCOUNT_AMOUNT     = uint256.NewInt(0).SetBytes(common.FromHex("2386F26FC10000"))

	// Validator/Staking contract address - 0x1001
	VALIDATOR_CONTRACT_ADDRESS = common.HexToAddress("0x0000000000000000000000000000000000001001")
	// use global
	CROSS_CHAIN_CONTRACT_ADDRESS = common.HexToAddress("0x00000000000000000000000000000000B429C0B2")
)

type ServiceType string

const (
	ServiceTypeWrite    ServiceType = "SUB-WRITE"
	ServiceTypeMaster   ServiceType = "MASTER"
	ServiceTypeReadonly ServiceType = "SUB-READ"
)

type FolderBackup int

const (
	BlockDataTopic               = "block_data_topic"
	TransactionsFromSubTopic     = "tx_from_sub_topic"
	ReadTransactionsFromSubTopic = "read_tx_from_sub_topic"
	StateAttestationTopic        = "state_attestation_topic" // Phase 2: attestation exchange for fork detection
	FreeFeeRequestProtocol       = "/mtn/free-fee-request/1.0.0"
)

const (
	IDENTIFIER_STAKE   = "module:stake"
	IDENTIFIER_STORAGE = "module:storage"
)
