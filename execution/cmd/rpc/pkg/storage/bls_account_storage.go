package storage

import (
	"encoding/binary"
	fmt "fmt"
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
	// Reverse index: ba:<address>:<blsPublicKey> -> paddedId
	// Dùng để tìm nhanh xem address có tồn tại không và ở vị trí nào
	PREFIX_BLS_ACCOUNT_INDEX = "ba:"
	PREFIX_PENDING_TX        = "pt:" // pending tx: pt:<address> -> PendingTransaction
	// Confirmed accounts with full data
	// bc:<blsPublicKey>:count -> total count (8 bytes)
	// bc:<blsPublicKey>:<paddedId> -> BlsAccountData (Protobuf)
	PREFIX_BLS_CONFIRMED = "bc:"
	// Unconfirmed accounts with full data
	// bu:<blsPublicKey>:count -> total count (8 bytes)
	// bu:<blsPublicKey>:<paddedId> -> BlsAccountData (Protobuf)
	PREFIX_BLS_UNCONFIRMED = "bu:"

	ID_PADDING_LENGTH = 10 // Hỗ trợ tối đa 10 tỷ accounts (0000000001 -> 9999999999)
)

type BlsAccountStorage struct {
	ldb           *ldb_storage.LevelDBStorage
	pendingMu     sync.RWMutex // Cho pending transactions
	unconfirmedMu sync.RWMutex // Cho unconfirmed accounts
	confirmedMu   sync.RWMutex // Cho confirmed accounts
}

func NewBlsAccountStorage(ldb *ldb_storage.LevelDBStorage) *BlsAccountStorage {
	return &BlsAccountStorage{ldb: ldb}
}

// ========== REVERSE INDEX OPERATIONS ==========
func getReverseIndexKey(address ethCommon.Address, blsPublicKey []byte) string {
	return PREFIX_BLS_ACCOUNT_INDEX + address.Hex() + ":" + ethCommon.Bytes2Hex(blsPublicKey)
}

// saveReverseIndex lưu reverse index: ba:<address>:<blsPublicKey> -> paddedId
func (s *BlsAccountStorage) saveReverseIndex(address ethCommon.Address, blsPublicKey []byte, paddedID string, isConfirmed bool) error {
	key := getReverseIndexKey(address, blsPublicKey)
	// Value format: <confirmed_flag>:<paddedId>
	// Example: "1:0000000001" (confirmed) or "0:0000000001" (unconfirmed)
	flag := "0"
	if isConfirmed {
		flag = "1"
	}
	value := flag + ":" + paddedID
	return s.ldb.Put([]byte(key), []byte(value))
}

func (s *BlsAccountStorage) getReverseIndex(address ethCommon.Address, blsPublicKey []byte) (paddedID string, isConfirmed bool, exists bool, err error) {
	key := getReverseIndexKey(address, blsPublicKey)
	value, err := s.ldb.Get([]byte(key))
	if err != nil {
		return "", false, false, nil // Not found
	}
	// Parse value: "0:0000000001" or "1:0000000001"
	parts := strings.Split(string(value), ":")
	if len(parts) != 2 {
		return "", false, false, fmt.Errorf("invalid reverse index format")
	}

	isConfirmed = parts[0] == "1"
	paddedID = parts[1]
	return paddedID, isConfirmed, true, nil
}

// deleteReverseIndex xóa reverse index
func (s *BlsAccountStorage) deleteReverseIndex(address ethCommon.Address, blsPublicKey []byte) error {
	key := getReverseIndexKey(address, blsPublicKey)
	return s.ldb.Delete([]byte(key))
}

// ========== PENDING TRANSACTION OPERATIONS ==========
func (s *BlsAccountStorage) SavePendingTransaction(tx *pb.PendingTransaction) error {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()

	key := PREFIX_PENDING_TX + ethCommon.BytesToAddress(tx.Address).Hex()
	value, err := proto.Marshal(tx)
	if err != nil {
		return fmt.Errorf("failed to marshal pending transaction: %w", err)
	}

	return s.ldb.Put([]byte(key), value)
}

func (s *BlsAccountStorage) GetPendingTransaction(address ethCommon.Address) (*pb.PendingTransaction, error) {
	s.pendingMu.RLock()
	defer s.pendingMu.RUnlock()

	key := PREFIX_PENDING_TX + address.Hex()
	value, err := s.ldb.Get([]byte(key))
	if err != nil {
		return nil, err
	}
	tx := &pb.PendingTransaction{}
	if err := proto.Unmarshal(value, tx); err != nil {
		return nil, fmt.Errorf("failed to unmarshal pending tx: %w", err)
	}
	return tx, nil
}

func (s *BlsAccountStorage) DeletePendingTransaction(address ethCommon.Address) error {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()

	key := PREFIX_PENDING_TX + address.Hex()
	return s.ldb.Delete([]byte(key))
}

// ========== HELPER FUNCTIONS ==========
func padID(id uint64) string {
	idStr := strconv.FormatUint(id, 10)
	if len(idStr) >= ID_PADDING_LENGTH {
		return idStr
	}
	return strings.Repeat("0", ID_PADDING_LENGTH-len(idStr)) + idStr
}

func unpadID(paddedID string) (uint64, error) {
	return strconv.ParseUint(paddedID, 10, 64)
}

// getCount lấy tổng số accounts cho một BLS public key
func (s *BlsAccountStorage) getCount(blsPublicKey []byte, isConfirmed bool) (uint64, error) {
	var prefix string
	if isConfirmed {
		prefix = PREFIX_BLS_CONFIRMED
	} else {
		prefix = PREFIX_BLS_UNCONFIRMED
	}

	key := prefix + ethCommon.Bytes2Hex(blsPublicKey) + ":count"
	value, err := s.ldb.Get([]byte(key))
	if err != nil {
		return 0, nil // Chưa có count thì return 0
	}

	return binary.BigEndian.Uint64(value), nil
}

// setCount lưu tổng số accounts
func (s *BlsAccountStorage) setCount(blsPublicKey []byte, isConfirmed bool, count uint64) error {
	var prefix string
	if isConfirmed {
		prefix = PREFIX_BLS_CONFIRMED
	} else {
		prefix = PREFIX_BLS_UNCONFIRMED
	}

	key := prefix + ethCommon.Bytes2Hex(blsPublicKey) + ":count"
	value := make([]byte, 8)
	binary.BigEndian.PutUint64(value, count)

	return s.ldb.Put([]byte(key), value)
}

// AddAccountToBlsPublicKey - thêm account với batch write (atomic)
func (s *BlsAccountStorage) AddAccountToBlsPublicKey(
	accountData *pb.BlsAccountData,
	isConfirmed bool,
) error {
	// Chọn mutex phù hợp
	if isConfirmed {
		s.confirmedMu.Lock()
		defer s.confirmedMu.Unlock()
	} else {
		s.unconfirmedMu.Lock()
		defer s.unconfirmedMu.Unlock()
	}

	// Kiểm tra xem address đã tồn tại chưa bằng reverse index (O(1))
	address := ethCommon.BytesToAddress(accountData.Address)
	blsPublicKey := accountData.BlsPublicKey
	_, _, exists, err := s.getReverseIndex(address, blsPublicKey)
	if err != nil {
		return fmt.Errorf("failed to check reverse index: %w", err)
	}

	if exists {
		return fmt.Errorf("address already exists")
	}

	// Gọi internal version không lock
	return s.addAccountToBlsPublicKeyNoLock(accountData, isConfirmed)
}

// addAccountToBlsPublicKeyNoLock - internal version không lock (dùng khi đã lock bên ngoài)
func (s *BlsAccountStorage) addAccountToBlsPublicKeyNoLock(
	accountData *pb.BlsAccountData,
	isConfirmed bool,
) error {
	var prefix string
	if isConfirmed {
		prefix = PREFIX_BLS_CONFIRMED
	} else {
		prefix = PREFIX_BLS_UNCONFIRMED
	}

	address := ethCommon.BytesToAddress(accountData.Address)
	blsPublicKey := accountData.BlsPublicKey
	blsKeyHex := ethCommon.Bytes2Hex(blsPublicKey)

	// Lấy count hiện tại
	count, err := s.getCount(blsPublicKey, isConfirmed)
	if err != nil {
		return fmt.Errorf("failed to get count: %w", err)
	}

	// Thêm account mới với ID = count + 1
	newID := count + 1
	paddedID := padID(newID)

	// Marshal account data
	accountValue, err := proto.Marshal(accountData)
	if err != nil {
		return fmt.Errorf("failed to marshal account data: %w", err)
	}

	// Prepare count value
	countValue := make([]byte, 8)
	binary.BigEndian.PutUint64(countValue, newID)

	// Prepare reverse index value
	reverseIndexFlag := "0"
	if isConfirmed {
		reverseIndexFlag = "1"
	}
	reverseIndexValue := reverseIndexFlag + ":" + paddedID

	// ========== BATCH WRITE (ATOMIC) ==========
	batch := new(leveldb.Batch)
	// 1. Write account data
	accountKey := prefix + blsKeyHex + ":" + paddedID
	batch.Put([]byte(accountKey), accountValue)
	// 2. Write reverse index
	reverseIndexKey := getReverseIndexKey(address, blsPublicKey)
	batch.Put([]byte(reverseIndexKey), []byte(reverseIndexValue))
	// 3. Update count
	countKey := prefix + ethCommon.Bytes2Hex(blsPublicKey) + ":count"
	batch.Put([]byte(countKey), countValue)

	// Write batch atomically
	if err := s.ldb.WriteBatch(batch, nil); err != nil {
		return fmt.Errorf("failed to write batch: %w", err)
	}
	return nil
}

// GetAccountsByBlsPublicKey - lấy accounts với pagination
func (s *BlsAccountStorage) GetAccountsByBlsPublicKey(blsPublicKey []byte, page, pageSize int, isConfirmed bool) ([]*pb.BlsAccountData, int, error) {
	// Chọn mutex phù hợp (RLock để cho phép nhiều reads đồng thời)
	if isConfirmed {
		s.confirmedMu.RLock()
		defer s.confirmedMu.RUnlock()
	} else {
		s.unconfirmedMu.RLock()
		defer s.unconfirmedMu.RUnlock()
	}
	var prefix string
	if isConfirmed {
		prefix = PREFIX_BLS_CONFIRMED
	} else {
		prefix = PREFIX_BLS_UNCONFIRMED
	}

	blsKeyHex := ethCommon.Bytes2Hex(blsPublicKey)

	// Lấy tổng số accounts
	count, err := s.getCount(blsPublicKey, isConfirmed)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get count: %w", err)
	}

	total := int(count)

	// Tính toán range để lấy
	// Page 0: ID 1-10, Page 1: ID 11-20, ...
	startID := uint64(page*pageSize + 1)
	endID := uint64((page + 1) * pageSize)

	if startID > count {
		return []*pb.BlsAccountData{}, total, nil
	}
	if endID > count {
		endID = count
	}

	// Lấy account data trong range
	accounts := make([]*pb.BlsAccountData, 0, int(endID-startID+1))

	for id := startID; id <= endID; id++ {
		key := prefix + blsKeyHex + ":" + padID(id)
		value, err := s.ldb.Get([]byte(key))
		if err != nil {
			continue // Skip nếu không tìm thấy
		}

		accountData := &pb.BlsAccountData{}
		if err := proto.Unmarshal(value, accountData); err != nil {
			continue // Skip nếu unmarshal lỗi
		}

		accounts = append(accounts, accountData)
	}

	return accounts, total, nil
}

// MarkAccountConfirmed - di chuyển từ unconfirmed → confirmed với batch write
func (s *BlsAccountStorage) MarkAccountConfirmed(
	address ethCommon.Address,
	confirmTxHash []byte,
	blsPublicKey []byte,
) error {
	// Lock cả 2 mutex vì sẽ modify cả unconfirmed và confirmed
	s.unconfirmedMu.Lock()
	defer s.unconfirmedMu.Unlock()
	s.confirmedMu.Lock()
	defer s.confirmedMu.Unlock()

	// Lấy paddedID từ reverse index (trong unconfirmed)
	paddedID, isConfirmed, exists, err := s.getReverseIndex(address, blsPublicKey)
	if err != nil {
		return fmt.Errorf("failed to get reverse index: %w", err)
	}

	if !exists {
		return fmt.Errorf("account not found in storage")
	}

	if isConfirmed {
		return fmt.Errorf("account already confirmed")
	}

	// Lấy account data từ unconfirmed storage
	unconfirmedKey := PREFIX_BLS_UNCONFIRMED + ethCommon.Bytes2Hex(blsPublicKey) + ":" + paddedID
	value, err := s.ldb.Get([]byte(unconfirmedKey))
	if err != nil {
		return fmt.Errorf("failed to get unconfirmed account data: %w", err)
	}

	accountData := &pb.BlsAccountData{}
	if err := proto.Unmarshal(value, accountData); err != nil {
		return fmt.Errorf("failed to unmarshal account data: %w", err)
	}

	// Update account data
	accountData.IsConfirmed = true
	accountData.ConfirmedAt = time.Now().Unix()
	accountData.ConfirmTxHash = confirmTxHash

	// Xóa khỏi unconfirmed (không dùng removeAccountFromList vì đã lock)
	if err := s.removeAccountFromListNoLock(blsPublicKey, address, false); err != nil {
		return fmt.Errorf("failed to remove from unconfirmed: %w", err)
	}

	// Thêm vào confirmed (không dùng AddAccountToBlsPublicKey vì đã lock)
	if err := s.addAccountToBlsPublicKeyNoLock(accountData, true); err != nil {
		return fmt.Errorf("failed to add to confirmed: %w", err)
	}

	return nil
}

// removeAccountFromList - xóa account khỏi list (with lock)
func (s *BlsAccountStorage) removeAccountFromList(
	blsPublicKey []byte,
	address ethCommon.Address,
	isConfirmed bool,
) error {
	// Chọn mutex phù hợp
	if isConfirmed {
		s.confirmedMu.Lock()
		defer s.confirmedMu.Unlock()
	} else {
		s.unconfirmedMu.Lock()
		defer s.unconfirmedMu.Unlock()
	}

	return s.removeAccountFromListNoLock(blsPublicKey, address, isConfirmed)
}

// removeAccountFromListNoLock - internal version không lock
func (s *BlsAccountStorage) removeAccountFromListNoLock(
	blsPublicKey []byte,
	address ethCommon.Address,
	isConfirmed bool,
) error {
	var prefix string
	if isConfirmed {
		prefix = PREFIX_BLS_CONFIRMED
	} else {
		prefix = PREFIX_BLS_UNCONFIRMED
	}

	blsKeyHex := ethCommon.Bytes2Hex(blsPublicKey)
	// Lấy paddedID từ reverse index (O(1))
	paddedID, indexConfirmed, exists, err := s.getReverseIndex(address, blsPublicKey)
	if err != nil {
		return fmt.Errorf("failed to get reverse index: %w", err)
	}

	if !exists {
		return fmt.Errorf("address not found in reverse index")
	}

	if indexConfirmed != isConfirmed {
		return fmt.Errorf("confirmation status mismatch")
	}

	// Parse paddedID to get foundID
	foundID, err := unpadID(paddedID)
	if err != nil {
		return fmt.Errorf("failed to parse paddedID: %w", err)
	}

	// Lấy count
	count, err := s.getCount(blsPublicKey, isConfirmed)
	if err != nil || count == 0 {
		return fmt.Errorf("no accounts found or error getting count: %w", err)
	}

	// ========== BATCH DELETE/SWAP (ATOMIC) ==========
	batch := new(leveldb.Batch)

	// 1. Xóa reverse index cho account hiện tại
	reverseIndexKey := getReverseIndexKey(address, blsPublicKey)
	batch.Delete([]byte(reverseIndexKey))

	// Strategy: swap với entry cuối rồi xóa entry cuối
	if foundID == count {
		// Nếu là entry cuối, chỉ cần xóa
		key := prefix + blsKeyHex + ":" + paddedID
		batch.Delete([]byte(key))
	} else {
		// Swap với entry cuối
		lastPaddedID := padID(count)
		lastKey := prefix + blsKeyHex + ":" + lastPaddedID
		lastValue, err := s.ldb.Get([]byte(lastKey))
		if err != nil {
			return fmt.Errorf("failed to get last entry: %w", err)
		}

		// Parse last account để update reverse index
		lastAccountData := &pb.BlsAccountData{}
		if err := proto.Unmarshal(lastValue, lastAccountData); err != nil {
			return fmt.Errorf("failed to unmarshal last account: %w", err)
		}

		// 2. Ghi last account vào vị trí foundID
		foundKey := prefix + blsKeyHex + ":" + paddedID
		batch.Put([]byte(foundKey), lastValue)

		// 3. Update reverse index cho last account (nó bây giờ ở vị trí foundID)
		lastAddress := ethCommon.BytesToAddress(lastAccountData.Address)
		lastReverseIndexKey := getReverseIndexKey(lastAddress, blsPublicKey)
		reverseIndexFlag := "0"
		if isConfirmed {
			reverseIndexFlag = "1"
		}
		reverseIndexValue := reverseIndexFlag + ":" + paddedID
		batch.Put([]byte(lastReverseIndexKey), []byte(reverseIndexValue))

		// 4. Xóa entry cuối
		batch.Delete([]byte(lastKey))
	}
	// 5. Giảm count
	countKey := prefix + ethCommon.Bytes2Hex(blsPublicKey) + ":count"
	countValue := make([]byte, 8)
	binary.BigEndian.PutUint64(countValue, count-1)
	batch.Put([]byte(countKey), countValue)

	// Write batch atomically
	if err := s.ldb.WriteBatch(batch, nil); err != nil {
		return fmt.Errorf("failed to write batch: %w", err)
	}

	return nil
}

// GetTotalAccountsCount lấy tổng số accounts của một BLS public key
func (s *BlsAccountStorage) GetTotalAccountsCount(blsPublicKey []byte, isConfirmed bool) (uint64, error) {
	return s.getCount(blsPublicKey, isConfirmed)
}

// GetAccountAddressByIndex lấy account data tại vị trí index (1-indexed)
func (s *BlsAccountStorage) GetAccountAddressByIndex(blsPublicKey []byte, index uint64, isConfirmed bool) (*pb.BlsAccountData, error) {
	var prefix string
	if isConfirmed {
		prefix = PREFIX_BLS_CONFIRMED
	} else {
		prefix = PREFIX_BLS_UNCONFIRMED
	}

	blsKeyHex := ethCommon.Bytes2Hex(blsPublicKey)
	key := prefix + blsKeyHex + ":" + padID(index)

	value, err := s.ldb.Get([]byte(key))
	if err != nil {
		return nil, fmt.Errorf("account not found at index %d: %w", index, err)
	}

	accountData := &pb.BlsAccountData{}
	if err := proto.Unmarshal(value, accountData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal account data: %w", err)
	}

	return accountData, nil
}

// BatchGetAccountAddresses lấy nhiều account data trong một range
func (s *BlsAccountStorage) BatchGetAccountAddresses(
	blsPublicKey []byte,
	startID, endID uint64,
	isConfirmed bool,
) ([]*pb.BlsAccountData, error) {
	var prefix string
	if isConfirmed {
		prefix = PREFIX_BLS_CONFIRMED
	} else {
		prefix = PREFIX_BLS_UNCONFIRMED
	}

	count, err := s.getCount(blsPublicKey, isConfirmed)
	if err != nil {
		return nil, fmt.Errorf("failed to get count: %w", err)
	}

	if endID > count {
		endID = count
	}

	if startID > endID || startID == 0 {
		return []*pb.BlsAccountData{}, nil
	}

	blsKeyHex := ethCommon.Bytes2Hex(blsPublicKey)
	accounts := make([]*pb.BlsAccountData, 0, int(endID-startID+1))

	for id := startID; id <= endID; id++ {
		key := prefix + blsKeyHex + ":" + padID(id)
		value, err := s.ldb.Get([]byte(key))
		if err != nil {
			continue // Skip missing entries
		}

		accountData := &pb.BlsAccountData{}
		if err := proto.Unmarshal(value, accountData); err != nil {
			continue
		}

		accounts = append(accounts, accountData)
	}

	return accounts, nil
}

// GetAccountByAddress lấy account data bằng address (sử dụng reverse index)
func (s *BlsAccountStorage) GetAccountByAddress(address ethCommon.Address, blsPublicKey []byte) (*pb.BlsAccountData, error) {
	paddedID, isConfirmed, exists, err := s.getReverseIndex(address, blsPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get reverse index: %w", err)
	}

	if !exists {
		return nil, fmt.Errorf("account not found")
	}

	var prefix string
	if isConfirmed {
		prefix = PREFIX_BLS_CONFIRMED
	} else {
		prefix = PREFIX_BLS_UNCONFIRMED
	}

	blsKeyHex := ethCommon.Bytes2Hex(blsPublicKey)
	key := prefix + blsKeyHex + ":" + paddedID

	value, err := s.ldb.Get([]byte(key))
	if err != nil {
		return nil, fmt.Errorf("failed to get account data: %w", err)
	}

	accountData := &pb.BlsAccountData{}
	if err := proto.Unmarshal(value, accountData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal account data: %w", err)
	}

	return accountData, nil
}
