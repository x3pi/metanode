package transaction

import (
	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
)

type TransactionError struct {
	Code        int64
	Description string
}

var (
	InvalidTransactionHash              = &TransactionError{1, "invalid transaction hash"}
	InvalidNewDeviceKey                 = &TransactionError{2, "invalid new device key"}
	NotMatchLastHash                    = &TransactionError{3, "not match last hash"}
	InvalidLastDeviceKey                = &TransactionError{4, "invalid last device key"}
	InvalidAmount                       = &TransactionError{5, "invalid amount"}
	InvalidPendingUse                   = &TransactionError{6, "invalid pending use"}
	InvalidDeploySmartContractToAccount = &TransactionError{
		7,
		"invalid deploy smart contract to account",
	}
	InvalidCallSmartContractToAccount = &TransactionError{
		8,
		"invalid call smart contract to  non-existent smart account",
	}
	InvalidCallSmartContractData     = &TransactionError{9, "invalid call smart contract data"}
	InvalidStakeAddress              = &TransactionError{10, "invalid stake address"}
	InvalidUnstakeAddress            = &TransactionError{11, "invalid unstake address"}
	InvalidUnstakeAmount             = &TransactionError{12, "invalid unstake amount"}
	InvalidMaxGas                    = &TransactionError{13, "invalid max gas"}
	InvalidMaxGasPrice               = &TransactionError{14, "invalid max gas price"}
	InvalidCommissionSign            = &TransactionError{15, "invalid commission sign"}
	NotEnoughBalanceForCommissionFee = &TransactionError{
		16,
		"smart contract not enough balance for commission fee",
	}
	InvalidOpenChannelToAccount = &TransactionError{17, "invalid open channel to account"}
	InvalidSign                 = &TransactionError{18, "invalid sign"}
	InvalidCommitAddress        = &TransactionError{19, "invalid commit address"}
	InvalidOpenAccountAmount    = &TransactionError{20, "invalid open account amount"}
	IsNotCreator                = &TransactionError{21, "is not creator"}

	//
	NotEnoughVerifyMinerToVerifyTransation = &TransactionError{
		22,
		"not enough verify miner to verify transaction",
	}
	VerifyTransactionSignTimedOut = &TransactionError{
		23,
		"verify transaction sign timed out",
	}
	InvalidCodeStorage            = &TransactionError{24, "invalid code storage"}
	InvalidCallNonExitAccount     = &TransactionError{25, "invalid call smart contract to non-existent account"}
	InvalidNonce                  = &TransactionError{26, "invalid nonce"}
	InvalidMinTimeUse             = &TransactionError{27, "invalid min time use "}
	TimeoutPending                = &TransactionError{28, "timeout pending"}
	CannotInitializeBLS           = &TransactionError{29, "cannot initialize BLS"} // New error code
	InvalidData                   = &TransactionError{30, "invalid data"}
	AddressMismatch               = &TransactionError{31, "address mismatch for add bls public key"}
	RequiresTwoSignatures         = &TransactionError{32, "transaction requires authentication from 2 signatures"}
	InvalidTransaction            = &TransactionError{33, "invalid transaction"}
	InvalidChainId                = &TransactionError{34, "invalid chain id"}
	InvalidDataInputLengthForTx0  = &TransactionError{35, "invalid data input length for tx0"}
	InvalidBLSSignatureForTx0     = &TransactionError{36, "invalid BLS signature for tx0"}
	FailedToRecoverPubkeyForTx0   = &TransactionError{37, "failed to recover pubkey for tx0"}
	InvalidAddressMatchForTx0     = &TransactionError{38, "invalid address match for tx0"}
	NonceZeroButPublicKeyNotEmpty = &TransactionError{39, "nonce is 0 but public key is not empty"}
	InvalidDeployData             = &TransactionError{40, "invalid deploy data"}
	InvalidCallData               = &TransactionError{41, "invalid call data"}
	InvalidBlockData              = &TransactionError{42, "invalid block data"}
	PublicKeyExists               = &TransactionError{43, "Public key BLS already exists"}
	InvalidSignSecp               = &TransactionError{44, "invalid sign SECP"}

	// Các mã lỗi EXCEPTION mới được thêm vào
	ErrOutOfGas                 = &TransactionError{45, "out of gas"}                 // EXCEPTION_ERR_OUT_OF_GAS = 0
	ErrCodeStoreOutOfGas        = &TransactionError{46, "code store out of gas"}      // EXCEPTION_ERR_CODE_STORE_OUT_OF_GAS = 1
	ErrDepth                    = &TransactionError{47, "depth"}                      // EXCEPTION_ERR_DEPTH = 2
	ErrInsufficientBalance      = &TransactionError{48, "insufficient balance"}       // EXCEPTION_ERR_INSUFFICIENT_BALANCE = 3
	ErrContractAddressCollision = &TransactionError{49, "contract address collision"} // EXCEPTION_ERR_CONTRACT_ADDRESS_COLLISION = 4
	ErrExecutionReverted        = &TransactionError{50, "execution reverted"}         // EXCEPTION_ERR_EXECUTION_REVERTED = 5
	ErrMaxCodeSizeExceeded      = &TransactionError{51, "max code size exceeded"}     // EXCEPTION_ERR_MAX_CODE_SIZE_EXCEEDED = 6
	ErrInvalidJump              = &TransactionError{52, "invalid jump"}               // EXCEPTION_ERR_INVALID_JUMP = 7
	ErrWriteProtection          = &TransactionError{53, "write protection"}           // EXCEPTION_ERR_WRITE_PROTECTION = 8
	ErrReturnDataOutOfBounds    = &TransactionError{54, "return data out of bounds"}  // EXCEPTION_ERR_RETURN_DATA_OUT_OF_BOUNDS = 9
	ErrGasUintOverflow          = &TransactionError{55, "gas uint overflow"}          // EXCEPTION_ERR_GAS_UINT_OVERFLOW = 10
	ErrInvalidCode              = &TransactionError{56, "invalid code"}               // EXCEPTION_ERR_INVALID_CODE = 11
	ErrNonceUintOverflow        = &TransactionError{57, "nonce uint overflow"}        // EXCEPTION_ERR_NONCE_UINT_OVERFLOW = 12
	ErrOutOfBounds              = &TransactionError{58, "out of bounds"}              // EXCEPTION_ERR_OUT_OF_BOUNDS = 13
	ErrOverflow                 = &TransactionError{59, "overflow"}                   // EXCEPTION_ERR_OVERFLOW = 14
	ErrAddressNotInRelated      = &TransactionError{60, "address not in related"}     // EXCEPTION_ERR_ADDRESS_NOT_IN_RELATED = 15
	ErrNone                     = &TransactionError{61, "none"}
	InvalidMaxFee               = &TransactionError{62, "invalid max fee"}
	NodeSyncingError            = &TransactionError{63, "node is syncing and not ready yet"}
	NonceConflictError          = &TransactionError{64, "transaction rejected: nonce conflict detected"}
	GetStateError               = &TransactionError{65, "error: Get state"}
	VerifyTransactionError      = &TransactionError{66, "verify transaction failed"}
	AddToPoolError              = &TransactionError{67, "failed to add transaction to pool"}
	UploadChunkError            = &TransactionError{68, "failed to upload chunk"}
	AccountNotRegistered        = &TransactionError{69, "account not registered on parent chain"}
	UnauthorizedSystemSender    = &TransactionError{70, "unauthorized sender for a rollup system event"}

	// Ethereum native ingress & envelope admission errors
	ErrDecodeRawEth           = &TransactionError{71, "failed to decode raw Ethereum transaction envelope"}
	ErrPreEIP155              = &TransactionError{72, "pre-EIP-155 unprotected transactions are not allowed"}
	ErrMalleableSignature     = &TransactionError{73, "malleable signature: s exceeds curve order / 2 (EIP-2)"}
	ErrSenderRecovery         = &TransactionError{74, "failed to recover sender address from signature"}
	ErrExceedsMaxEnvelopeSize = &TransactionError{75, "transaction envelope exceeds maximum allowed size"}
	ErrExceedsMaxBatchSize    = &TransactionError{76, "batch contains too many transactions"}
	ErrAlreadyKnown           = &TransactionError{77, "transaction already known in mempool or blockchain"}
	ErrNonceTooLow            = &TransactionError{78, "nonce too low"}
	ErrNonceTooHigh           = &TransactionError{79, "nonce too high"}
	ErrInsufficientFunds      = &TransactionError{80, "insufficient funds for gas * price + value"}
	ErrReplacementUnderpriced = &TransactionError{81, "replacement transaction underpriced"}
	ErrIntrinsicGasTooLow     = &TransactionError{82, "intrinsic gas too low"}
	ErrExceedsBlockGasLimit   = &TransactionError{83, "exceeds block gas limit"}
	ErrInvalidSender          = &TransactionError{84, "invalid sender"}
	ErrTxTypeNotSupported     = &TransactionError{85, "transaction type not supported"}
	ErrMaxInitCodeSizeExceeded = &TransactionError{86, "max initcode size exceeded"}
	ErrGasLimitReached        = &TransactionError{87, "gas limit reached"}
	ErrEnvelopeBindingMismatch = &TransactionError{88, "transaction fields do not match raw envelope"}
	ErrInvalidEnvelope         = &TransactionError{89, "invalid raw envelope bytes"}
)

var CodeToError = map[int64]*TransactionError{
	1:  InvalidTransactionHash,
	2:  InvalidNewDeviceKey,
	3:  NotMatchLastHash,
	4:  InvalidLastDeviceKey,
	5:  InvalidAmount,
	6:  InvalidPendingUse,
	7:  InvalidDeploySmartContractToAccount,
	8:  InvalidCallSmartContractToAccount,
	9:  InvalidCallSmartContractData,
	10: InvalidStakeAddress,
	11: InvalidUnstakeAddress,
	12: InvalidUnstakeAmount,
	13: InvalidMaxGas,
	14: InvalidMaxGasPrice,
	15: InvalidCommissionSign,
	16: NotEnoughBalanceForCommissionFee,
	17: InvalidOpenChannelToAccount,
	18: InvalidSign,
	19: InvalidCommitAddress,
	20: InvalidOpenAccountAmount,
	21: IsNotCreator,
	22: NotEnoughVerifyMinerToVerifyTransation,
	23: VerifyTransactionSignTimedOut,
	24: InvalidCodeStorage,
	25: InvalidCallNonExitAccount,
	26: InvalidNonce,
	27: InvalidMinTimeUse,
	28: TimeoutPending,
	29: CannotInitializeBLS,
	30: InvalidData,
	31: AddressMismatch,
	32: RequiresTwoSignatures,
	33: InvalidTransaction,
	34: InvalidChainId,
	35: InvalidDataInputLengthForTx0,
	36: InvalidBLSSignatureForTx0,
	37: FailedToRecoverPubkeyForTx0,
	38: InvalidAddressMatchForTx0,
	39: NonceZeroButPublicKeyNotEmpty,
	40: InvalidDeployData,
	41: InvalidCallData,
	42: InvalidBlockData,
	43: PublicKeyExists,
	44: InvalidSignSecp,

	// Ánh xạ cho các mã lỗi EXCEPTION mới
	45: ErrOutOfGas,
	46: ErrCodeStoreOutOfGas,
	47: ErrDepth,
	48: ErrInsufficientBalance,
	49: ErrContractAddressCollision,
	50: ErrExecutionReverted,
	51: ErrMaxCodeSizeExceeded,
	52: ErrInvalidJump,
	53: ErrWriteProtection,
	54: ErrReturnDataOutOfBounds,
	55: ErrGasUintOverflow,
	56: ErrInvalidCode,
	57: ErrNonceUintOverflow,
	58: ErrOutOfBounds,
	59: ErrOverflow,
	60: ErrAddressNotInRelated,
	61: ErrNone,
	62: InvalidMaxFee,
	63: NodeSyncingError,
	64: NonceConflictError,
	65: GetStateError,
	66: VerifyTransactionError,
	67: AddToPoolError,

	// upload chunk
	68: UploadChunkError,

	// account registration gate
	69: AccountNotRegistered,
	70: UnauthorizedSystemSender,

	// Ethereum native ingress & envelope admission errors
	71: ErrDecodeRawEth,
	72: ErrPreEIP155,
	73: ErrMalleableSignature,
	74: ErrSenderRecovery,
	75: ErrExceedsMaxEnvelopeSize,
	76: ErrExceedsMaxBatchSize,
	77: ErrAlreadyKnown,
	78: ErrNonceTooLow,
	79: ErrNonceTooHigh,
	80: ErrInsufficientFunds,
	81: ErrReplacementUnderpriced,
	82: ErrIntrinsicGasTooLow,
	83: ErrExceedsBlockGasLimit,
	84: ErrInvalidSender,
	85: ErrTxTypeNotSupported,
	86: ErrMaxInitCodeSizeExceeded,
	87: ErrGasLimitReached,
	88: ErrEnvelopeBindingMismatch,
	89: ErrInvalidEnvelope,
}

// DescriptionToError provides reverse lookup from error description to TransactionError.
// Used by the batch TX processing path to recover actual error codes from wrapped errors.
var DescriptionToError = map[string]*TransactionError{}

func init() {
	for _, err := range CodeToError {
		if err != nil {
			DescriptionToError[err.Description] = err
		}
	}
}

func (te *TransactionError) Error() string {
	if te == nil {
		return ""
	}
	return te.Description
}

func (te *TransactionError) Is(target error) bool {
	if te == nil {
		return target == nil
	}
	t, ok := target.(*TransactionError)
	if !ok {
		return false
	}
	if te.Code == t.Code {
		return true
	}
	// Semantic aliases
	if (te.Code == InvalidNonce.Code && t.Code == ErrNonceTooLow.Code) ||
		(te.Code == ErrNonceTooLow.Code && t.Code == InvalidNonce.Code) {
		return true
	}
	if isFundsError(te.Code) && isFundsError(t.Code) {
		return true
	}
	if isSenderError(te.Code) && isSenderError(t.Code) {
		return true
	}
	if (te.Code == ErrIntrinsicGasTooLow.Code && t.Code == InvalidMaxGas.Code) ||
		(te.Code == InvalidMaxGas.Code && t.Code == ErrIntrinsicGasTooLow.Code) {
		return true
	}
	if (te.Code == ErrMaxInitCodeSizeExceeded.Code && t.Code == ErrMaxCodeSizeExceeded.Code) ||
		(te.Code == ErrMaxCodeSizeExceeded.Code && t.Code == ErrMaxInitCodeSizeExceeded.Code) {
		return true
	}
	return false
}

func isFundsError(code int64) bool {
	return code == ErrInsufficientFunds.Code || code == ErrInsufficientBalance.Code || code == InvalidMaxFee.Code || code == InvalidAmount.Code
}

func isSenderError(code int64) bool {
	return code == ErrInvalidSender.Code || code == InvalidSign.Code || code == InvalidSignSecp.Code || code == ErrSenderRecovery.Code
}

func (te *TransactionError) Proto() *pb.TransactionError {
	return &pb.TransactionError{
		Code:        te.Code,
		Description: te.Description,
	}
}

func (te *TransactionError) FromProto(pbData *pb.TransactionError) {
	te.Code = pbData.Code
	te.Description = pbData.Description
}

func (transactionErr *TransactionError) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(transactionErr.Proto())
}

func (transactionErr *TransactionError) Unmarshal(data []byte) error {
	pbData := &pb.TransactionError{}
	if err := proto.Unmarshal(data, pbData); err != nil {
		return err
	}
	transactionErr.FromProto(pbData)
	return nil
}

type TransactionHashWithErrorCode struct {
	transactionHash common.Hash
	errorCode       int64
}

type TransactionHashWithError struct {
	hash        common.Hash
	errorCode   int64
	description string
	output      []byte
}

func (te *TransactionHashWithError) Proto() *pb.TransactionHashWithError {
	return &pb.TransactionHashWithError{
		Hash:        te.hash[:],
		Code:        te.errorCode,
		Description: te.description,
		Output:      te.output,
	}
}

func NewTransactionHashWithErrorCode(
	transactionHash common.Hash,
	errorCode int64,
) *TransactionHashWithErrorCode {
	return &TransactionHashWithErrorCode{
		transactionHash: transactionHash,
		errorCode:       errorCode,
	}
}

func NewTransactionHashWithError(
	transactionHash common.Hash,
	errorCode int64,
	description string,
	output []byte,

) *TransactionHashWithError {
	return &TransactionHashWithError{
		hash:        transactionHash,
		errorCode:   errorCode,
		description: description,
		output:      output,
	}
}

func (te *TransactionHashWithErrorCode) Proto() *pb.TransactionHashWithErrorCode {
	return &pb.TransactionHashWithErrorCode{
		TransactionHash: te.transactionHash[:],
		Code:            te.errorCode,
	}
}

func (te *TransactionHashWithErrorCode) FromProto(
	pbData *pb.TransactionHashWithErrorCode,
) {
	te.transactionHash = common.BytesToHash(pbData.TransactionHash)
	te.errorCode = pbData.Code
}

func (transactionErr *TransactionHashWithErrorCode) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(transactionErr.Proto())
}

func (transactionErr *TransactionHashWithErrorCode) Unmarshal(data []byte) error {
	pbData := &pb.TransactionHashWithErrorCode{}
	if err := proto.Unmarshal(data, pbData); err != nil {
		return err
	}

	transactionErr.FromProto(pbData)
	return nil
}

// MapProtoExceptionToTransactionError ánh xạ một proto.EXCEPTION sang TransactionError tương ứng.
func MapProtoExceptionToTransactionError(exception pb.EXCEPTION) *TransactionError {
	switch exception {
	case pb.EXCEPTION_ERR_OUT_OF_GAS:
		return ErrOutOfGas // Mã 45
	case pb.EXCEPTION_ERR_CODE_STORE_OUT_OF_GAS:
		return ErrCodeStoreOutOfGas // Mã 46
	case pb.EXCEPTION_ERR_DEPTH:
		return ErrDepth // Mã 47
	case pb.EXCEPTION_ERR_INSUFFICIENT_BALANCE:
		return ErrInsufficientBalance // Mã 48
	case pb.EXCEPTION_ERR_CONTRACT_ADDRESS_COLLISION:
		return ErrContractAddressCollision // Mã 49
	case pb.EXCEPTION_ERR_EXECUTION_REVERTED:
		return ErrExecutionReverted // Mã 50
	case pb.EXCEPTION_ERR_MAX_CODE_SIZE_EXCEEDED:
		return ErrMaxCodeSizeExceeded // Mã 51
	case pb.EXCEPTION_ERR_INVALID_JUMP:
		return ErrInvalidJump // Mã 52
	case pb.EXCEPTION_ERR_WRITE_PROTECTION:
		return ErrWriteProtection // Mã 53
	case pb.EXCEPTION_ERR_RETURN_DATA_OUT_OF_BOUNDS:
		return ErrReturnDataOutOfBounds // Mã 54
	case pb.EXCEPTION_ERR_GAS_UINT_OVERFLOW:
		return ErrGasUintOverflow // Mã 55
	case pb.EXCEPTION_ERR_INVALID_CODE:
		return ErrInvalidCode // Mã 56
	case pb.EXCEPTION_ERR_NONCE_UINT_OVERFLOW:
		return ErrNonceUintOverflow // Mã 57
	case pb.EXCEPTION_ERR_OUT_OF_BOUNDS:
		return ErrOutOfBounds // Mã 58
	case pb.EXCEPTION_ERR_OVERFLOW:
		return ErrOverflow // Mã 59
	case pb.EXCEPTION_ERR_ADDRESS_NOT_IN_RELATED:
		return ErrAddressNotInRelated // Mã 60
	case pb.EXCEPTION_NONE:
		return ErrNone // Mã 61
	default:
		// Trả về một lỗi chung hoặc nil nếu không có ánh xạ nào được tìm thấy
		// Ở đây, chúng ta có thể trả về một TransactionError chung cho "unknown exception"
		// hoặc bạn có thể định nghĩa một lỗi cụ thể cho trường hợp này.
		// Ví dụ:
		// return &TransactionError{-2, "unknown or unmapped exception"}
		return nil // Hoặc một lỗi mặc định phù hợp
	}
}

func (te *TransactionHashWithError) FromProto(pbData *pb.TransactionHashWithError) {
	te.hash = common.BytesToHash(pbData.Hash)
	te.errorCode = pbData.Code
	te.description = pbData.Description
	te.output = pbData.Output

}

func (transactionErr *TransactionHashWithError) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(transactionErr.Proto())
}

func (transactionErr *TransactionHashWithError) Unmarshal(data []byte) error {
	pbData := &pb.TransactionHashWithError{}
	if err := proto.Unmarshal(data, pbData); err != nil {
		return err
	}

	transactionErr.FromProto(pbData)
	return nil
}
