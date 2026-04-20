package storage

import (
	"encoding/binary"
	fmt "fmt"

	ethCommon "github.com/ethereum/go-ethereum/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
	"google.golang.org/protobuf/proto"
)

type NotificationStorage struct {
	db *leveldb.DB
}

const (
	// Key structures:
	// Counter: notif_counter:<address> → uint64 (số thông báo hiện tại)
	NOTI_COUNTER_PREFIX = "notif_counter"
	// Data:    notif:<address>:<reversed_id> → Notification protobuf
	NOTI_DATA_PREFIX = "notif"
)

func NewNotificationStorage(db *leveldb.DB) *NotificationStorage {
	return &NotificationStorage{db: db}
}

// getCounterKey trả về key để lưu counter
func (ns *NotificationStorage) getCounterKey(address ethCommon.Address) []byte {
	return []byte(fmt.Sprintf("%s:%s", NOTI_COUNTER_PREFIX, address.Hex()))
}

// getNotificationKey trả về key để lưu notification
func (ns *NotificationStorage) getNotificationKey(address ethCommon.Address, reversedID uint64) []byte {
	return []byte(fmt.Sprintf("%s:%s:%020d", NOTI_DATA_PREFIX, address.Hex(), reversedID))
}
func (ns *NotificationStorage) getNextID(address ethCommon.Address) (uint64, error) {
	counterKey := ns.getCounterKey(address)
	// Đọc counter hiện tại
	data, err := ns.db.Get(counterKey, nil)
	if err != nil && err != leveldb.ErrNotFound {
		return 0, fmt.Errorf("failed to get counter: %w", err)
	}
	var currentID uint64 = 0
	if err == nil && len(data) == 8 {
		currentID = binary.BigEndian.Uint64(data)
	}
	nextID := currentID + 1
	newCounterData := make([]byte, 8)
	binary.BigEndian.PutUint64(newCounterData, nextID)
	if err := ns.db.Put(counterKey, newCounterData, nil); err != nil {
		return 0, fmt.Errorf("failed to update counter: %w", err)
	}

	return nextID, nil
}
func (ns *NotificationStorage) SaveNotification(notif *pb.Notification) error {
	address := ethCommon.BytesToAddress(notif.AccountAddress)
	// Lấy ID mới (tự động tăng)
	nextID, err := ns.getNextID(address)
	if err != nil {
		return err
	}
	notif.Id = fmt.Sprintf("%d", nextID)
	// Reversed ID để sort từ mới → cũ
	// ID càng lớn → reversedID càng nhỏ → xuất hiện đầu tiên khi iterate
	reversedID := 9999999999 - nextID
	key := ns.getNotificationKey(address, reversedID)
	data, err := proto.Marshal(notif)
	if err != nil {
		return fmt.Errorf("failed to marshal notification: %w", err)
	}
	return ns.db.Put(key, data, nil)
}
func (ns *NotificationStorage) GetNotifications(
	address ethCommon.Address,
	page, pageSize int,
) ([]*pb.Notification, int, error) {
	if page < 0 {
		page = 0
	}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 20
	}
	totalCount, err := ns.GetTotalCount(address)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to get total count: %w", err)
	}
	total := int(totalCount)
	// Nếu không có notification
	if total == 0 {
		return []*pb.Notification{}, 0, nil
	}
	startItemIndex := page * pageSize
	if startItemIndex >= total {
		return []*pb.Notification{}, total, nil
	}
	// Tính reversed ID bắt đầu
	startReversedID := (9999999999 - totalCount) + uint64(startItemIndex)
	startKey := ns.getNotificationKey(address, startReversedID)
	//
	prefix := []byte(fmt.Sprintf("%s:%s:", NOTI_DATA_PREFIX, address.Hex()))
	iter := ns.db.NewIterator(&util.Range{Start: startKey}, nil)
	defer iter.Release()
	var notifications []*pb.Notification
	count := 0
	for iter.Next() && count < pageSize {
		if !hasPrefix(iter.Key(), prefix) {
			break
		}
		notif := &pb.Notification{}
		if err := proto.Unmarshal(iter.Value(), notif); err != nil {
			return nil, 0, fmt.Errorf("failed to unmarshal notification: %w", err)
		}
		notifications = append(notifications, notif)
		count++
	}

	if err := iter.Error(); err != nil {
		return nil, 0, err
	}
	return notifications, total, nil
}
func (ns *NotificationStorage) GetTotalCount(address ethCommon.Address) (uint64, error) {
	counterKey := ns.getCounterKey(address)

	data, err := ns.db.Get(counterKey, nil)
	if err != nil {
		if err == leveldb.ErrNotFound {
			return 0, nil
		}
		return 0, err
	}

	if len(data) != 8 {
		return 0, nil
	}

	return binary.BigEndian.Uint64(data), nil
}

func hasPrefix(key, prefix []byte) bool {
	if len(key) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if key[i] != prefix[i] {
			return false
		}
	}
	return true
}
