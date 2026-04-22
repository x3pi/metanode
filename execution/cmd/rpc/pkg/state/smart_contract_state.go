package state

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

type SmartContractState struct {
	createPublicKey p_common.PublicKey
	storageAddress  common.Address
	codeHash        common.Hash
	storageRoot     common.Hash
	mapFullDbHash   common.Hash
	simpleDbHash    common.Hash
	logsHash        common.Hash
	trieDatabaseMap map[string][]byte
}

// methods for trieDatabaseMap
func (ss *SmartContractState) GetTrieDatabaseMapValue(key string) []byte {
	return ss.trieDatabaseMap[key]
}

func (ss *SmartContractState) SetTrieDatabaseMapValue(key string, value []byte) {
	if ss.trieDatabaseMap == nil {
		ss.trieDatabaseMap = make(map[string][]byte)
	}
	ss.trieDatabaseMap[key] = value
}

func (ss *SmartContractState) DeleteTrieDatabaseMapValue(key string) {
	delete(ss.trieDatabaseMap, key)
}

func NewSmartContractState(
	createPublicKey p_common.PublicKey,
	storageAddress common.Address,
	codeHash common.Hash,
	storageRoot common.Hash,
	logsHash common.Hash,
) types.SmartContractState {
	return &SmartContractState{
		createPublicKey: createPublicKey,
		storageAddress:  storageAddress,
		codeHash:        codeHash,
		storageRoot:     storageRoot,
		logsHash:        logsHash,
	}
}

func NewEmptySmartContractState() types.SmartContractState {
	return &SmartContractState{}
}

// general
func (ss *SmartContractState) Proto() *pb.SmartContractState {
	return &pb.SmartContractState{
		CreatorPublicKey: ss.createPublicKey.Bytes(),
		StorageAddress:   ss.storageAddress.Bytes(),
		CodeHash:         ss.codeHash.Bytes(),
		StorageRoot:      ss.storageRoot.Bytes(),
		LogsHash:         ss.logsHash.Bytes(),
		MapFullDbHash:    ss.mapFullDbHash.Bytes(),
		SimpleDbHash:     ss.simpleDbHash.Bytes(),
		TrieDatabaseMap:  ss.trieDatabaseMap,
	}
}

func (ss *SmartContractState) Marshal() ([]byte, error) {
	return proto.Marshal(ss.Proto())
}

func (ss *SmartContractState) FromProto(pbData *pb.SmartContractState) {
	ss.createPublicKey = p_common.PubkeyFromBytes(pbData.CreatorPublicKey)
	ss.storageAddress = common.BytesToAddress(pbData.StorageAddress)
	ss.codeHash = common.BytesToHash(pbData.CodeHash)
	ss.storageRoot = common.BytesToHash(pbData.StorageRoot)
	ss.logsHash = common.BytesToHash(pbData.LogsHash)
	ss.mapFullDbHash = common.BytesToHash(pbData.MapFullDbHash)
	ss.simpleDbHash = common.BytesToHash(pbData.SimpleDbHash)
	ss.trieDatabaseMap = pbData.TrieDatabaseMap
}

func (ss *SmartContractState) Unmarshal(b []byte) error {
	ssProto := &pb.SmartContractState{}
	err := proto.Unmarshal(b, ssProto)
	if err != nil {
		return err
	}
	ss.FromProto(ssProto)
	return nil
}

func (ss *SmartContractState) String() string {
	jsonSmartContractState := &JsonSmartContractState{}
	jsonSmartContractState.FromSmartContractState(ss)
	b, _ := json.MarshalIndent(jsonSmartContractState, "", " ")
	return string(b)
}

func (ss *SmartContractState) CreatorPublicKey() p_common.PublicKey {
	return ss.createPublicKey
}

func (ss *SmartContractState) CreatorAddress() common.Address {
	return p_common.AddressFromPubkey(ss.CreatorPublicKey())
}

func (ss *SmartContractState) StorageAddress() common.Address {
	return ss.storageAddress
}

func (ss *SmartContractState) CodeHash() common.Hash {
	return ss.codeHash
}

func (ss *SmartContractState) StorageRoot() common.Hash {
	return ss.storageRoot
}

func (ss *SmartContractState) LogsHash() common.Hash {
	return ss.logsHash
}

func (ss *SmartContractState) MapFullDbHash() common.Hash {
	return ss.mapFullDbHash
}

func (ss *SmartContractState) SimpleDbHash() common.Hash {
	return ss.simpleDbHash
}

// setter
func (ss *SmartContractState) SetCreatorPublicKey(pk p_common.PublicKey) {
	ss.createPublicKey = pk
}

func (ss *SmartContractState) SetStorageAddress(address common.Address) {
	ss.storageAddress = address
}

func (ss *SmartContractState) SetCodeHash(hash common.Hash) {
	ss.codeHash = hash
}

func (ss *SmartContractState) SetStorageRoot(hash common.Hash) {
	ss.storageRoot = hash
}

func (ss *SmartContractState) SetLogsHash(hash common.Hash) {
	ss.logsHash = hash
}

func (ss *SmartContractState) SetMapFullDbHash(hash common.Hash) {
	ss.mapFullDbHash = hash
}

func (ss *SmartContractState) SetSimpleDbHash(hash common.Hash) {
	ss.simpleDbHash = hash
}

func (ss *SmartContractState) Copy() types.SmartContractState {
	cpSs := &SmartContractState{
		createPublicKey: ss.createPublicKey,
		storageAddress:  ss.storageAddress,
		codeHash:        ss.codeHash,
		storageRoot:     ss.storageRoot,
		logsHash:        ss.logsHash,
		mapFullDbHash:   ss.mapFullDbHash,
		simpleDbHash:    ss.simpleDbHash,
		trieDatabaseMap: make(map[string][]byte),
	}
	for k, v := range ss.trieDatabaseMap {
		cpSs.trieDatabaseMap[k] = v
	}
	return cpSs
}

type JsonSmartContractState struct {
	CreatorPublicKey string            `json:"creator_public_key"`
	StorageAddress   string            `json:"storage_address"`
	CodeHash         string            `json:"code_hash"`
	StorageRoot      string            `json:"storage_root"`
	LogsHash         string            `json:"logs_hash"`
	MapFullDbHash    string            `json:"full_db_hash"`
	TrieDatabaseMap  map[string][]byte `json:"trie_database_map,omitempty"`
}

func (jss *JsonSmartContractState) FromSmartContractState(ss types.SmartContractState) {
	jss.CreatorPublicKey = ss.CreatorPublicKey().String()
	jss.StorageAddress = ss.StorageAddress().String()
	jss.CodeHash = ss.CodeHash().String()
	jss.StorageRoot = ss.StorageRoot().String()
	jss.LogsHash = ss.LogsHash().String()
	jss.MapFullDbHash = ss.MapFullDbHash().String()
	jss.TrieDatabaseMap = ss.TrieDatabaseMap()
}

func (jss *JsonSmartContractState) ToSmartContractState() types.SmartContractState {
	createPublicKey := p_common.PubkeyFromBytes(common.FromHex(jss.CreatorPublicKey))
	storageAddress := common.HexToAddress(jss.StorageAddress)
	codeHash := common.HexToHash(jss.CodeHash)
	storageRoot := common.HexToHash(jss.StorageRoot)
	logsHash := common.HexToHash(jss.LogsHash)
	MapFullDbHash := common.HexToHash(jss.MapFullDbHash)

	smartContractState := NewSmartContractState(createPublicKey, storageAddress, codeHash, storageRoot, logsHash)
	smartContractState.SetTrieDatabaseMap(jss.TrieDatabaseMap)
	smartContractState.SetMapFullDbHash(MapFullDbHash)

	return smartContractState
}

func (ss *SmartContractState) TrieDatabaseMap() map[string][]byte {
	return ss.trieDatabaseMap
}

func (ss *SmartContractState) SetTrieDatabaseMap(trieDatabaseMap map[string][]byte) {
	ss.trieDatabaseMap = trieDatabaseMap
}
