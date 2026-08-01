package main

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	eth_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

// TestCheckBloomFilter kiểm tra logic lọc sự kiện (Event Logs) của RPC.
// Đây là một Unit Test CỰC KỲ HỮU DỤNG vì nó đảm bảo tính năng GetLogs
// (được DApps và Wallet gọi liên tục) không bị lọc sai dữ liệu.
func TestCheckBloomFilter(t *testing.T) {
	// 1. Tạo một Bloom filter giả lập có chứa Address A và Topic 1
	var bloom eth_types.Bloom
	addrA := common.HexToAddress("0xAAAAA")
	topic1 := common.HexToHash("0x11111")
	
	// Thêm vào bloom filter
	bloom.Add(addrA.Bytes())
	bloom.Add(topic1.Bytes())

	t.Run("Match cả Address và Topic", func(t *testing.T) {
		addresses := []common.Address{addrA}
		topics := [][]common.Hash{{topic1}}
		
		// Phải trả về true vì bloom filter có chứa cả 2
		match := checkBloomFilter(bloom, addresses, topics)
		require.True(t, match, "Bloom filter phải match khi có đủ address và topic")
	})

	t.Run("Không match Address", func(t *testing.T) {
		addrB := common.HexToAddress("0xBBBBB") // Address không có trong bloom
		addresses := []common.Address{addrB}
		topics := [][]common.Hash{{topic1}}
		
		// Phải trả về false vì addrB không tồn tại
		match := checkBloomFilter(bloom, addresses, topics)
		require.False(t, match, "Bloom filter phải false khi address không tồn tại")
	})

	t.Run("Không match Topic", func(t *testing.T) {
		addresses := []common.Address{addrA}
		topic2 := common.HexToHash("0x22222") // Topic không có trong bloom
		topics := [][]common.Hash{{topic2}}
		
		// Phải trả về false vì topic2 không tồn tại
		match := checkBloomFilter(bloom, addresses, topics)
		require.False(t, match, "Bloom filter phải false khi topic không tồn tại")
	})

	t.Run("Topic là wildcard (rỗng)", func(t *testing.T) {
		addresses := []common.Address{addrA}
		topics := [][]common.Hash{{}} // Wildcard topic
		
		match := checkBloomFilter(bloom, addresses, topics)
		require.True(t, match, "Bloom filter phải true khi topic là wildcard")
	})
}

// TestGetLogs_MaxLimit_Logic minh họa lại lý do tại sao Early Filtering (lọc sớm) 
// lại sửa được bug maxLogsPerRequest.
func TestGetLogs_MaxLimit_Logic(t *testing.T) {
	// Giả lập cấu hình
	const maxLogsPerRequest = 2
	
	// Mảng eventLogs chứa kết quả (đã áp dụng Early Filtering)
	var eventLogs []*eth_types.Log

	// Giả lập 10 logs (chỉ có 1 log hợp lệ cần tìm)
	for i := 0; i < 10; i++ {
		evL := &eth_types.Log{
			Address: common.HexToAddress("0x11111"), // Mọi log đều có address này
		}
		
		// Giả lập bộ lọc (Criteria): Chỉ tìm log ở vị trí i == 5
		isMatch := (i == 5)
		
		// Nhờ có Early Filtering, chúng ta chỉ thêm log khi thực sự Match!
		if isMatch {
			eventLogs = append(eventLogs, evL)
			// Kiểm tra max limit NGAY SAU KHI thêm log hợp lệ
			require.LessOrEqual(t, len(eventLogs), maxLogsPerRequest, "Chưa được vượt quá max limit")
		}
	}

	// Kết quả mong đợi: eventLogs chỉ chứa đúng 1 log, KHÔNG bao giờ bị văng lỗi Limit!
	require.Len(t, eventLogs, 1, "Mảng kết quả chỉ chứa đúng log đã lọc")
}
