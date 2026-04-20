package storage

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	ethCommon "github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/ldb_storage"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/syndtr/goleveldb/leveldb"
	"google.golang.org/protobuf/proto"
)

// Key prefixes for LevelDB
const (
	PREFIX_ADMIN_INDEX = "adm:"
	PREFIX_ADMIN_DATA  = "admd:"
	PREFIX_ADMIN_COUNT = "admc:count"

	PREFIX_AUTHORIZED_WALLET_INDEX = "aw:"
	PREFIX_AUTHORIZED_WALLET_DATA  = "awd:"
	PREFIX_AUTHORIZED_WALLET_COUNT = "awc:count"

	PREFIX_CONTRACT_FREE_GAS_INDEX = "cfg:"
	PREFIX_CONTRACT_FREE_GAS_DATA  = "cfgd:"
	PREFIX_CONTRACT_FREE_GAS_COUNT = "cfgc:count"

	CONTRACT_ID_PADDING_LENGTH = 10
)

// Re-export proto types for callers
type ContractFreeGasData = pb.ContractFreeGasData
type AuthorizedWalletData = pb.AuthorizedWalletData
type AdminData = pb.AdminData

// ==================== STORAGE ====================

type ContractFreeGasStorage struct {
	ldb *ldb_storage.LevelDBStorage
	mu  sync.RWMutex
}

func NewContractFreeGasStorage(ldb *ldb_storage.LevelDBStorage) *ContractFreeGasStorage {
	return &ContractFreeGasStorage{ldb: ldb}
}

// ==================== GENERIC HELPERS ====================

func padEntityID(id uint64) string {
	s := strconv.FormatUint(id, 10)
	if len(s) >= CONTRACT_ID_PADDING_LENGTH {
		return s
	}
	return strings.Repeat("0", CONTRACT_ID_PADDING_LENGTH-len(s)) + s
}

func unpadEntityID(s string) (uint64, error) {
	return strconv.ParseUint(s, 10, 64)
}

// genericGetCount reads a uint64 count from a single LevelDB key.
func (s *ContractFreeGasStorage) genericGetCount(countKey string) (uint64, error) {
	value, err := s.ldb.Get([]byte(countKey))
	if err != nil {
		if err == leveldb.ErrNotFound {
			return 0, nil
		}
		return 0, err
	}
	return binary.BigEndian.Uint64(value), nil
}

// putCount encodes a uint64 count into a batch.
func putCount(batch *leveldb.Batch, countKey string, count uint64) {
	v := make([]byte, 8)
	binary.BigEndian.PutUint64(v, count)
	batch.Put([]byte(countKey), v)
}

// genericGetReverseIndex looks up the paddedID stored at <indexPrefix><address.Hex()>.
func (s *ContractFreeGasStorage) genericGetReverseIndex(indexPrefix string, addr ethCommon.Address) (paddedID string, exists bool, err error) {
	key := indexPrefix + addr.Hex()
	value, err := s.ldb.Get([]byte(key))
	if err != nil {
		if err == leveldb.ErrNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	return string(value), true, nil
}

// genericAdd saves a new proto message into <dataPrefix><paddedID>, updates reverse index and count.
// addrHex is the canonical hex of the entity address (used for the index key).
func (s *ContractFreeGasStorage) genericAdd(
	dataPrefix, indexPrefix, countKey string,
	addrHex string,
	msg proto.Message,
) error {
	count, err := s.genericGetCount(countKey)
	if err != nil {
		return fmt.Errorf("failed to get count: %w", err)
	}
	newID := count + 1
	paddedID := padEntityID(newID)

	data, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal: %w", err)
	}

	batch := new(leveldb.Batch)
	batch.Put([]byte(dataPrefix+paddedID), data)
	batch.Put([]byte(indexPrefix+addrHex), []byte(paddedID))
	putCount(batch, countKey, newID)

	if err := s.ldb.WriteBatch(batch, nil); err != nil {
		return fmt.Errorf("failed to write batch: %w", err)
	}
	return nil
}

// genericRemove removes an entity by address using the swap-delete strategy.
// getLastAddr extracts the entity's address from its serialised proto bytes.
func (s *ContractFreeGasStorage) genericRemove(
	dataPrefix, indexPrefix, countKey string,
	addr ethCommon.Address,
	getLastAddr func([]byte) (ethCommon.Address, error),
) error {
	paddedID, exists, err := s.genericGetReverseIndex(indexPrefix, addr)
	if err != nil {
		return fmt.Errorf("failed to get reverse index: %w", err)
	}
	if !exists {
		return fmt.Errorf("%s not found", addr.Hex())
	}

	foundID, err := unpadEntityID(paddedID)
	if err != nil {
		return fmt.Errorf("failed to parse paddedID: %w", err)
	}

	count, err := s.genericGetCount(countKey)
	if err != nil {
		return fmt.Errorf("failed to get count: %w", err)
	}

	batch := new(leveldb.Batch)
	batch.Delete([]byte(indexPrefix + addr.Hex()))

	if foundID == count {
		// Removing the last entry — no swap needed
		batch.Delete([]byte(dataPrefix + paddedID))
	} else {
		// Swap last entry into the vacated slot to keep IDs contiguous
		lastPaddedID := padEntityID(count)
		lastDataKey := dataPrefix + lastPaddedID
		lastBytes, err := s.ldb.Get([]byte(lastDataKey))
		if err != nil {
			return fmt.Errorf("failed to get last entry: %w", err)
		}

		lastAddr, err := getLastAddr(lastBytes)
		if err != nil {
			return fmt.Errorf("failed to extract address from last entry: %w", err)
		}

		batch.Put([]byte(dataPrefix+paddedID), lastBytes)
		batch.Put([]byte(indexPrefix+lastAddr.Hex()), []byte(paddedID))
		batch.Delete([]byte(lastDataKey))
	}

	putCount(batch, countKey, count-1)

	if err := s.ldb.WriteBatch(batch, nil); err != nil {
		return fmt.Errorf("failed to write batch: %w", err)
	}
	return nil
}

// genericScan iterates IDs [1..count] and calls unmarshal on each raw value.
// Returns items in the [startID, endID] window (inclusive, 1-based).
func genericScan[T proto.Message](
	s *ContractFreeGasStorage,
	dataPrefix string,
	count uint64,
	startID, endID uint64,
	newT func() T,
) ([]T, error) {
	result := make([]T, 0, int(endID-startID+1))
	for id := startID; id <= endID; id++ {
		value, err := s.ldb.Get([]byte(dataPrefix + padEntityID(id)))
		if err != nil {
			if err == leveldb.ErrNotFound {
				continue
			}
			return nil, fmt.Errorf("failed to get entry at ID %d: %w", id, err)
		}
		msg := newT()
		if err := proto.Unmarshal(value, msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal entry at ID %d: %w", id, err)
		}
		result = append(result, msg)
	}
	return result, nil
}

// paginateRange computes zero-based (startID, endID) for page/pageSize against count.
// Returns (0,0,false) when the page is out of range.
func paginateRange(page, pageSize int, count uint64) (startID, endID uint64, ok bool) {
	startID = uint64(page*pageSize + 1)
	endID = uint64((page + 1) * pageSize)
	if startID > count {
		return 0, 0, false
	}
	if endID > count {
		endID = count
	}
	return startID, endID, true
}

// ==================== CONTRACT FREE GAS ====================

func (s *ContractFreeGasStorage) AddContract(contractAddress, addedBy ethCommon.Address) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists, err := s.genericGetReverseIndex(PREFIX_CONTRACT_FREE_GAS_INDEX, contractAddress); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("contract %s already exists", contractAddress.Hex())
	}

	msg := &pb.ContractFreeGasData{
		ContractAddress: contractAddress.Bytes(),
		AddedAt:         time.Now().Unix(),
		AddedBy:         addedBy.Bytes(),
	}
	return s.genericAdd(
		PREFIX_CONTRACT_FREE_GAS_DATA,
		PREFIX_CONTRACT_FREE_GAS_INDEX,
		PREFIX_CONTRACT_FREE_GAS_COUNT,
		contractAddress.Hex(), msg,
	)
}

func (s *ContractFreeGasStorage) RemoveContract(contractAddress ethCommon.Address) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.genericRemove(
		PREFIX_CONTRACT_FREE_GAS_DATA,
		PREFIX_CONTRACT_FREE_GAS_INDEX,
		PREFIX_CONTRACT_FREE_GAS_COUNT,
		contractAddress,
		func(b []byte) (ethCommon.Address, error) {
			d := &pb.ContractFreeGasData{}
			if err := proto.Unmarshal(b, d); err != nil {
				return ethCommon.Address{}, err
			}
			return ethCommon.BytesToAddress(d.ContractAddress), nil
		},
	)
}

func (s *ContractFreeGasStorage) HasContract(contractAddress ethCommon.Address) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists, err := s.genericGetReverseIndex(PREFIX_CONTRACT_FREE_GAS_INDEX, contractAddress)
	return exists, err
}

func (s *ContractFreeGasStorage) GetContract(contractAddress ethCommon.Address) (*pb.ContractFreeGasData, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	paddedID, exists, err := s.genericGetReverseIndex(PREFIX_CONTRACT_FREE_GAS_INDEX, contractAddress)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("contract %s not found", contractAddress.Hex())
	}

	value, err := s.ldb.Get([]byte(PREFIX_CONTRACT_FREE_GAS_DATA + paddedID))
	if err != nil {
		return nil, fmt.Errorf("failed to get contract data: %w", err)
	}
	d := &pb.ContractFreeGasData{}
	if err := proto.Unmarshal(value, d); err != nil {
		return nil, fmt.Errorf("failed to unmarshal contract data: %w", err)
	}
	return d, nil
}

func (s *ContractFreeGasStorage) GetContracts(page, pageSize int) ([]*pb.ContractFreeGasData, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count, err := s.genericGetCount(PREFIX_CONTRACT_FREE_GAS_COUNT)
	if err != nil {
		return nil, 0, err
	}
	total := int(count)

	startID, endID, ok := paginateRange(page, pageSize, count)
	if !ok {
		return []*pb.ContractFreeGasData{}, total, nil
	}

	items, err := genericScan(s, PREFIX_CONTRACT_FREE_GAS_DATA, count, startID, endID, func() *pb.ContractFreeGasData { return &pb.ContractFreeGasData{} })
	return items, total, err
}

// GetContractsByAdder returns only contracts where added_by == adderAddress.
// Performs a full scan (O(n)) — acceptable because the free gas list is small.
func (s *ContractFreeGasStorage) GetContractsByAdder(adderAddress ethCommon.Address, page, pageSize int) ([]*pb.ContractFreeGasData, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count, err := s.genericGetCount(PREFIX_CONTRACT_FREE_GAS_COUNT)
	if err != nil {
		return nil, 0, err
	}

	// Collect matching entries first
	var matched []*pb.ContractFreeGasData
	for id := uint64(1); id <= count; id++ {
		value, err := s.ldb.Get([]byte(PREFIX_CONTRACT_FREE_GAS_DATA + padEntityID(id)))
		if err != nil {
			if err == leveldb.ErrNotFound {
				continue
			}
			return nil, 0, fmt.Errorf("failed to get contract at ID %d: %w", id, err)
		}
		d := &pb.ContractFreeGasData{}
		if err := proto.Unmarshal(value, d); err != nil {
			return nil, 0, fmt.Errorf("failed to unmarshal at ID %d: %w", id, err)
		}
		if ethCommon.BytesToAddress(d.AddedBy) == adderAddress {
			matched = append(matched, d)
		}
	}

	total := len(matched)
	start := page * pageSize
	if start >= total {
		return []*pb.ContractFreeGasData{}, total, nil
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return matched[start:end], total, nil
}

func (s *ContractFreeGasStorage) GetTotalCount() (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.genericGetCount(PREFIX_CONTRACT_FREE_GAS_COUNT)
}

// ==================== AUTHORIZED WALLETS ====================

func (s *ContractFreeGasStorage) AddWallet(walletAddress, addedBy ethCommon.Address) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists, err := s.genericGetReverseIndex(PREFIX_AUTHORIZED_WALLET_INDEX, walletAddress); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("wallet %s already exists", walletAddress.Hex())
	}

	msg := &pb.AuthorizedWalletData{
		WalletAddress: walletAddress.Bytes(),
		AddedAt:       time.Now().Unix(),
		AddedBy:       addedBy.Bytes(),
	}
	return s.genericAdd(
		PREFIX_AUTHORIZED_WALLET_DATA,
		PREFIX_AUTHORIZED_WALLET_INDEX,
		PREFIX_AUTHORIZED_WALLET_COUNT,
		walletAddress.Hex(), msg,
	)
}

func (s *ContractFreeGasStorage) RemoveWallet(walletAddress ethCommon.Address) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.genericRemove(
		PREFIX_AUTHORIZED_WALLET_DATA,
		PREFIX_AUTHORIZED_WALLET_INDEX,
		PREFIX_AUTHORIZED_WALLET_COUNT,
		walletAddress,
		func(b []byte) (ethCommon.Address, error) {
			d := &pb.AuthorizedWalletData{}
			if err := proto.Unmarshal(b, d); err != nil {
				return ethCommon.Address{}, err
			}
			return ethCommon.BytesToAddress(d.WalletAddress), nil
		},
	)
}

func (s *ContractFreeGasStorage) IsAuthorized(walletAddress ethCommon.Address) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists, err := s.genericGetReverseIndex(PREFIX_AUTHORIZED_WALLET_INDEX, walletAddress)
	return exists, err
}

func (s *ContractFreeGasStorage) GetWallets(page, pageSize int) ([]*pb.AuthorizedWalletData, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count, err := s.genericGetCount(PREFIX_AUTHORIZED_WALLET_COUNT)
	if err != nil {
		return nil, 0, err
	}
	total := int(count)

	startID, endID, ok := paginateRange(page, pageSize, count)
	if !ok {
		return []*pb.AuthorizedWalletData{}, total, nil
	}

	items, err := genericScan(s, PREFIX_AUTHORIZED_WALLET_DATA, count, startID, endID, func() *pb.AuthorizedWalletData { return &pb.AuthorizedWalletData{} })
	return items, total, err
}

// ==================== ADMIN LIST ====================

func (s *ContractFreeGasStorage) AddAdmin(adminAddress, addedBy ethCommon.Address) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists, err := s.genericGetReverseIndex(PREFIX_ADMIN_INDEX, adminAddress); err != nil {
		return err
	} else if exists {
		return fmt.Errorf("admin %s already exists", adminAddress.Hex())
	}

	msg := &pb.AdminData{
		AdminAddress: adminAddress.Bytes(),
		AddedAt:      time.Now().Unix(),
		AddedBy:      addedBy.Bytes(),
	}
	return s.genericAdd(
		PREFIX_ADMIN_DATA,
		PREFIX_ADMIN_INDEX,
		PREFIX_ADMIN_COUNT,
		adminAddress.Hex(), msg,
	)
}

func (s *ContractFreeGasStorage) RemoveAdmin(adminAddress ethCommon.Address) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.genericRemove(
		PREFIX_ADMIN_DATA,
		PREFIX_ADMIN_INDEX,
		PREFIX_ADMIN_COUNT,
		adminAddress,
		func(b []byte) (ethCommon.Address, error) {
			d := &pb.AdminData{}
			if err := proto.Unmarshal(b, d); err != nil {
				return ethCommon.Address{}, err
			}
			return ethCommon.BytesToAddress(d.AdminAddress), nil
		},
	)
}

func (s *ContractFreeGasStorage) IsAdmin(adminAddress ethCommon.Address) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, exists, err := s.genericGetReverseIndex(PREFIX_ADMIN_INDEX, adminAddress)
	return exists, err
}

func (s *ContractFreeGasStorage) GetAdmins(page, pageSize int) ([]*pb.AdminData, int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count, err := s.genericGetCount(PREFIX_ADMIN_COUNT)
	if err != nil {
		return nil, 0, err
	}
	total := int(count)

	startID, endID, ok := paginateRange(page, pageSize, count)
	if !ok {
		return []*pb.AdminData{}, total, nil
	}

	items, err := genericScan(s, PREFIX_ADMIN_DATA, count, startID, endID, func() *pb.AdminData { return &pb.AdminData{} })
	return items, total, err
}
