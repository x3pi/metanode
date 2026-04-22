package state

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/proto"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

var (
	ErrorInvalidSubPendingAmount      = errors.New("invalid sub pending amount")
	ErrorInvalidSubStakingAmount      = errors.New("invalid sub staking amount")
	ErrorInvalidSubBalanceAmount      = errors.New("invalid sub balance amount")
	ErrorInvalidSubTotalBalanceAmount = errors.New("invalid sub total balance amount")

	ErrorStakeStateNotFound = errors.New("stake info not found")
)

type AccountState struct {
	address            common.Address
	lastHash           common.Hash
	balance            *big.Int
	pendingBalance     *big.Int
	deviceKey          common.Hash
	smartContractState types.SmartContractState
	nonce              uint64
	publicKeyBls       []byte
	accountType        pb.ACCOUNT_TYPE
}

func NewAccountState(address common.Address) types.AccountState {
	return &AccountState{
		address:        address,
		balance:        big.NewInt(0),
		pendingBalance: big.NewInt(0),
		nonce:          0,
	}
}

// general
func (as *AccountState) Proto() *pb.AccountState {
	nonceBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(nonceBytes, as.nonce)
	pbAs := &pb.AccountState{
		Address:        as.address.Bytes(),
		LastHash:       as.lastHash.Bytes(),
		Balance:        as.balance.Bytes(),
		PendingBalance: as.pendingBalance.Bytes(),
		DeviceKey:      as.deviceKey.Bytes(),
		Nonce:          nonceBytes,
		PublicKeyBls:   as.publicKeyBls,
		AccountType:    as.accountType,
	}
	if as.smartContractState != nil {
		pbAs.SmartContractState = as.smartContractState.Proto()
	}
	return pbAs
}

func (as *AccountState) FromProto(pbData *pb.AccountState) {
	as.address = common.BytesToAddress(pbData.Address)
	as.lastHash = common.BytesToHash(pbData.LastHash)
	as.balance = big.NewInt(0).SetBytes(pbData.Balance)
	as.pendingBalance = big.NewInt(0).SetBytes(pbData.PendingBalance)
	as.deviceKey = common.BytesToHash(pbData.DeviceKey)
	if pbData.SmartContractState != nil {
		as.smartContractState = &SmartContractState{}
		as.smartContractState.FromProto(pbData.SmartContractState)
	}
	if len(pbData.Nonce) == 8 { // Check for valid length
		as.nonce = binary.BigEndian.Uint64(pbData.Nonce)
	} else {
		as.nonce = 0 // Or any default value
	}
	as.publicKeyBls = pbData.PublicKeyBls
	as.accountType = pbData.AccountType
}

func (as *AccountState) Marshal() ([]byte, error) {
	return proto.Marshal(as.Proto())
}

func (as *AccountState) Unmarshal(b []byte) error {
	asProto := &pb.AccountState{}
	err := proto.Unmarshal(b, asProto)
	if err != nil {
		return err
	}
	as.FromProto(asProto)
	return nil
}

func (as *AccountState) Copy() types.AccountState {
	copyAs := &AccountState{}
	copy(copyAs.address[:], as.address[:])
	copy(copyAs.lastHash[:], as.lastHash[:])
	copyAs.balance = big.NewInt(0).Set(as.balance)
	copyAs.pendingBalance = big.NewInt(0).Set(as.pendingBalance)
	copy(copyAs.deviceKey[:], as.deviceKey[:])
	if as.smartContractState != nil {
		copyAs.smartContractState = as.smartContractState.Copy()
	}
	copyAs.nonce = as.nonce
	copy(copyAs.publicKeyBls[:], as.publicKeyBls[:])
	copyAs.accountType = as.accountType
	return copyAs
}

func (as *AccountState) String() string {
	jsonAccountState := &JsonAccountState{}
	jsonAccountState.FromAccountState(as)
	b, _ := json.MarshalIndent(jsonAccountState, "", " ")
	return string(b)
}

// getter
func (as *AccountState) Address() common.Address {
	return as.address
}

func (as *AccountState) PublicKeyBls() []byte {
	return as.publicKeyBls
}

func (as *AccountState) AccountType() pb.ACCOUNT_TYPE {
	return as.accountType
}

func (as *AccountState) Balance() *big.Int {
	return as.balance
}

func (as *AccountState) PendingBalance() *big.Int {
	return as.pendingBalance
}

func (as *AccountState) TotalBalance() *big.Int {
	return big.NewInt(0).Add(
		as.Balance(),
		as.PendingBalance(),
	)
}

func (as *AccountState) LastHash() common.Hash {
	return as.lastHash
}

func (as *AccountState) SmartContractState() types.SmartContractState {
	return as.smartContractState
}

func (as *AccountState) DeviceKey() common.Hash {
	return as.deviceKey
}

// setter
func (as *AccountState) SetBalance(newBalance *big.Int) {
	as.balance = newBalance
}

func (as *AccountState) SetNewDeviceKey(newDeviceKey common.Hash) {
	as.deviceKey = newDeviceKey
}

func (as *AccountState) SetLastHash(newLastHash common.Hash) {
	as.lastHash = newLastHash
}

func (as *AccountState) SetSmartContractState(smState types.SmartContractState) {
	as.smartContractState = smState
}

func (as *AccountState) AddPendingBalance(amount *big.Int) {
	as.pendingBalance = big.NewInt(0).Add(as.pendingBalance, amount)
}

func (as *AccountState) SubPendingBalance(amount *big.Int) error {
	pendingBalance := as.PendingBalance()
	if amount.Cmp(pendingBalance) > 0 {
		return ErrorInvalidSubPendingAmount
	}
	as.pendingBalance = big.NewInt(0).Sub(pendingBalance, amount)
	return nil
}

func (as *AccountState) SubBalance(amount *big.Int) error {
	balance := as.Balance()
	if amount.Cmp(balance) > 0 {
		return ErrorInvalidSubBalanceAmount
	}
	as.balance = big.NewInt(0).Sub(balance, amount)
	return nil
}

func (as *AccountState) SubTotalBalance(amount *big.Int) error {
	totalBalance := big.NewInt(0).Add(as.PendingBalance(), as.Balance())
	if amount.Cmp(totalBalance) > 0 {
		return ErrorInvalidSubBalanceAmount
	}
	as.pendingBalance = big.NewInt(0)
	as.balance = big.NewInt(0).Sub(totalBalance, amount)
	return nil
}

func (as *AccountState) AddBalance(amount *big.Int) {
	as.balance = big.NewInt(0).Add(as.balance, amount)
}

func (as *AccountState) GetOrCreateSmartContractState() types.SmartContractState {
	scState := as.SmartContractState()
	if scState == nil {
		scState = NewEmptySmartContractState()
	}
	as.SetSmartContractState(scState)
	return scState
}

func (as *AccountState) SetCodeHash(hash common.Hash) {
	scState := as.GetOrCreateSmartContractState()
	scState.SetCodeHash(hash)
}

func (as *AccountState) SetStorageAddress(storageAddress common.Address) {
	scState := as.GetOrCreateSmartContractState()
	scState.SetStorageAddress(storageAddress)
}

func (as *AccountState) SetStorageRoot(hash common.Hash) {
	scState := as.GetOrCreateSmartContractState()
	scState.SetStorageRoot(hash)
}

func (as *AccountState) SetCreatorPublicKey(creatorPublicKey p_common.PublicKey) {
	scState := as.GetOrCreateSmartContractState()
	scState.SetCreatorPublicKey(creatorPublicKey)
}

func (as *AccountState) AddLogHash(hash common.Hash) {
	scState := as.GetOrCreateSmartContractState()
	scState.SetLogsHash(crypto.Keccak256Hash(append(scState.LogsHash().Bytes(), hash.Bytes()...)))
}

func (as *AccountState) SetPendingBalance(newBalance *big.Int) {
	as.pendingBalance = newBalance
}

type JsonAccountState struct {
	Address            string                  `json:"address"`
	Balance            string                  `json:"balance"`
	PendingBalance     string                  `json:"pending_balance"`
	LastHash           string                  `json:"last_hash"`
	DeviceKey          string                  `json:"device_key"`
	SmartContractState *JsonSmartContractState `json:"smart_contract_state"`
	Nonce              uint64                  `json:"nonce"`
	PublicKeyBls       string                  `json:"publicKeyBls"`
	AccountType        int32                   `json:"accountType"`
}

func (j *JsonAccountState) ToAccountState() *AccountState {
	as := &AccountState{}
	as.address = common.HexToAddress(j.Address)
	as.balance = new(big.Int)
	as.balance.SetString(j.Balance, 10)
	as.pendingBalance = new(big.Int)
	as.pendingBalance.SetString(j.PendingBalance, 10)
	as.lastHash = common.HexToHash(j.LastHash)
	as.deviceKey = common.HexToHash(j.DeviceKey)
	if j.SmartContractState != nil {
		as.smartContractState = j.SmartContractState.ToSmartContractState()
	}
	as.nonce = j.Nonce
	as.publicKeyBls = common.FromHex(j.PublicKeyBls)
	as.accountType = pb.ACCOUNT_TYPE(j.AccountType)
	return as
}

func (j *JsonAccountState) FromAccountState(as *AccountState) {
	j.Address = as.address.Hex()
	j.Balance = as.balance.String()
	j.PendingBalance = as.pendingBalance.String()
	j.LastHash = as.lastHash.Hex()
	j.DeviceKey = as.deviceKey.Hex()
	if as.smartContractState != nil {
		j.SmartContractState = &JsonSmartContractState{}
		j.SmartContractState.FromSmartContractState(as.smartContractState)
	}
	j.Nonce = as.Nonce()
	j.PublicKeyBls = hex.EncodeToString(as.publicKeyBls)
	j.AccountType = int32(as.accountType)
}

func MarshalSCStatesWithBlockNumber(
	states map[common.Address]types.SmartContractState,
	blockNumber uint64,
) ([]byte, error) {
	statesProto := make(map[string]*pb.SmartContractState, len(states))

	for i, state := range states {
		statesProto[hex.EncodeToString(i.Bytes())] = state.Proto()
	}
	return proto.Marshal(&pb.SCStatesWithBlockNumber{
		SCStates:    statesProto,
		BlockNumber: blockNumber,
	})
}

func UnmarshalSCStatesWithBlockNumber(
	b []byte,
) (
	map[common.Address]types.SmartContractState,
	uint64,
	error,
) {
	sswb := &pb.SCStatesWithBlockNumber{}
	err := proto.Unmarshal(b, sswb)
	if err != nil {
		return nil, 0, err
	}
	states := make(map[common.Address]types.SmartContractState, len(sswb.SCStates))
	for i, ssProto := range sswb.SCStates {
		states[common.HexToAddress(i)] = &SmartContractState{}
		states[common.HexToAddress(i)].FromProto(ssProto)
	}
	return states, sswb.BlockNumber, nil
}

func MarshalGetAccountStateWithIdRequest(
	address common.Address,
	id string,
) ([]byte, error) {
	request := &pb.GetAccountStateWithIdRequest{
		Address: address.Bytes(),
		Id:      id,
	}
	return proto.Marshal(request)
}

func UnmarshalGetAccountStateWithIdRequest(
	b []byte,
) (
	common.Address,
	string,
	error,
) {
	request := &pb.GetAccountStateWithIdRequest{}
	err := proto.Unmarshal(b, request)
	if err != nil {
		return common.Address{}, "", err
	}
	return common.BytesToAddress(request.Address), request.Id, nil
}

func MarshalAccountStateWithIdRequest(
	as types.AccountState,
	id string,
) ([]byte, error) {
	request := &pb.AccountStateWithIdRequest{
		AccountState: as.Proto(),
		Id:           id,
	}
	return proto.Marshal(request)
}

func UnmarshalAccountStateWithIdRequest(
	b []byte,
) (
	types.AccountState,
	string,
	error,
) {
	request := &pb.AccountStateWithIdRequest{}
	err := proto.Unmarshal(b, request)
	if err != nil {
		return nil, "", err
	}
	as := &AccountState{}
	as.FromProto(request.AccountState)
	return as, request.Id, nil
}

func (as *AccountState) Nonce() uint64 {
	return as.nonce
}

func (as *AccountState) SetNonce(newNonce uint64) {
	as.nonce = newNonce
}

// plus one nonce
func (as *AccountState) PlusOneNonce() {
	as.nonce++
}

func (as *AccountState) SetPublicKeyBls(publicKeyBls []byte) error {
	if len(publicKeyBls) != 48 {
		logger.Error("Invalid publicKeyBls length. Expected 48 bytes, got", len(publicKeyBls))
		return errors.New("invalid publicKeyBls length")
	}
	if len(as.publicKeyBls) == 0 {
		as.publicKeyBls = publicKeyBls
	} else {
		return errors.New("publicKeyBls is already set")
	}
	return nil
}

func (as *AccountState) SetAccountType(accountTypeNew pb.ACCOUNT_TYPE) error {
	isValid := false
	for _, accountType := range []pb.ACCOUNT_TYPE{pb.ACCOUNT_TYPE_READ_WRITE_STRICT, pb.ACCOUNT_TYPE_REGULAR_ACCOUNT} {
		if accountType == accountTypeNew {
			isValid = true
			break
		}
	}

	if isValid {
		as.accountType = accountTypeNew
		return nil // Thêm return nil khi hợp lệ
	} else {
		logger.Error("Invalid account type:", accountTypeNew)
		return errors.New("invalid account type") // Thêm return error khi không hợp lệ
	}
}
