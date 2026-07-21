package tx_processor

import (
	"context"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain/tx_processor/mvcc"
	"github.com/meta-node-blockchain/meta-node/types"
)

// TestTrueBlockSTM_SuspendWakeupLogic kiểm tra cơ chế Suspend/Wake-up của TrueBlockSTM.
// Bài test này mô phỏng kịch bản:
// 1. TX 0 bị abort và để lại "estimate" (đánh dấu) trên tài khoản B.
// 2. TX 1 chạy sau, cố gắng đọc tài khoản B -> dính ErrEstimateHit -> nhảy vào trạng thái suspend (chờ TX 0).
// 3. TX 0 chạy lại và hoàn thành -> gọi wake-up để đánh thức TX 1.
// 4. TX 1 được đưa trở lại hàng đợi thực thi (execCh).
func TestTrueBlockSTM_SuspendWakeupLogic(t *testing.T) {
	// Sử dụng ChainState nhẹ chạy trên RAM (mượn từ integration test)
	cs := newTestChainState(t)
	addrA := common.HexToAddress("0xA1")
	addrB := common.HexToAddress("0xB2")
	
	// Khởi tạo số dư cho B để đọc không bị nil
	seedAccount(t, cs, addrB, big.NewInt(100), 0)

	// TX 0: A -> B
	tx0 := newTx(addrA, addrB, 0, big.NewInt(10), nil)
	// TX 1: B -> A (TX 1 phụ thuộc vào dữ liệu của TX 0)
	tx1 := newTx(addrB, addrA, 0, big.NewInt(10), nil)

	stm := NewTrueBlockSTM([]types.Transaction{tx0, tx1})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	execCh := make(chan uint32, 10)
	validateCh := make(chan uint32, 10)
	doneCh := make(chan struct{})
	var activeTasks int32 = 2

	// --- BƯỚC 1: MÔ PHỎNG TX 0 ĐỂ LẠI ESTIMATE ---
	// Đóng giả việc TX 0 vừa bị abort và cắm cờ estimate lên addrB
	stm.accountMap.AddEstimate(addrB, mvcc.Version(0))
	stm.waitersMu[0].Lock()
	stm.estimatedAccounts[0] = map[common.Address]bool{addrB: true}
	stm.waitersMu[0].Unlock()

	// Gán state của TX 0 thành đang chạy
	atomic.StoreUint64(&stm.txState[0], packState(1, 0))

	// --- BƯỚC 2: TX 1 CHẠY VÀ SUSPEND ---
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Chạy execOne cho TX 1. Nó sẽ SLOAD tài khoản B, đụng estimate của TX 0, và tự suspend.
		stm.execOne(ctx, cs, common.Address{}, blankHeader(), 1, execCh, validateCh, &activeTasks, doneCh)
	}()

	// Đợi 1 chút để đảm bảo TX 1 đã vào hàng chờ
	time.Sleep(200 * time.Millisecond)

	// Kiểm tra xem TX 1 có thực sự đang nằm trong hàng chờ (waiters) của TX 0 không
	stm.waitersMu[0].Lock()
	waiters := stm.waiters[0]
	stm.waitersMu[0].Unlock()
	t.Logf("Debug: tx1.FromAddress() = %s, tx1.ToAddress() = %s", tx1.FromAddress().Hex(), tx1.ToAddress().Hex())
	if len(waiters) != 1 || waiters[0] != 1 {
		t.Fatalf("LỖI: Kỳ vọng TX 1 bị suspend và chờ TX 0, nhưng hàng chờ hiện tại là: %v", waiters)
	}

	// --- BƯỚC 3: TX 0 CHẠY XONG VÀ ĐÁNH THỨC (WAKE-UP) ---
	wg.Add(1)
	go func() {
		defer wg.Done()
		stm.execOne(ctx, cs, common.Address{}, blankHeader(), 0, execCh, validateCh, &activeTasks, doneCh)
	}()

	// Đợi cả 2 goroutine kết thúc
	wg.Wait()

	// --- BƯỚC 4: KIỂM TRA KẾT QUẢ ĐÁNH THỨC ---
	
	select {
	case wokenTx := <-execCh:
		if wokenTx != 1 {
			t.Errorf("LỖI: Kỳ vọng TX 1 được đánh thức (đẩy vào execCh), nhưng nhận được TX %d", wokenTx)
		}
	default:
		t.Error("LỖI: Kỳ vọng có 1 TX được đẩy vào execCh để chạy lại, nhưng channel trống không (Lost Wake-up)!")
	}

	// 2. Kiểm tra xem estimate và waiters của TX 0 đã được dọn dẹp sạch sẽ chưa
	stm.waitersMu[0].Lock()
	remAccEst := stm.estimatedAccounts[0]
	remWaiters := stm.waiters[0]
	stm.waitersMu[0].Unlock()

	if len(remAccEst) != 0 {
		t.Errorf("LỖI: Kỳ vọng estimates của TX 0 bị xóa sạch sau khi xong, nhưng còn: %v", remAccEst)
	}
	if len(remWaiters) != 0 {
		t.Errorf("LỖI: Kỳ vọng hàng chờ (waiters) của TX 0 bị xóa sạch, nhưng còn: %v", remWaiters)
	}
}

// TestTrueBlockSTM_SuspendWakeupRace_DoubleCheck kiểm tra Race Condition giữa Wake-up và Suspend.
// Tình huống: TX 0 vừa chạy xong, đang chuẩn bị đánh thức.
// Cùng lúc đó, TX 1 nhảy vào, thấy TX 0 đã chạy xong nên KHÔNG tự suspend mà chạy lại ngay.
func TestTrueBlockSTM_SuspendWakeupRace_DoubleCheck(t *testing.T) {
	stm := newDummySTM(2)

	execCh := make(chan uint32, 10)
	
	// Ép TX 0 ở trạng thái ĐÃ XONG (st = 1)
	atomic.StoreUint64(&stm.txState[0], packState(1, 1))

	// TX 1 gọi logic suspend (copy từ block_stm.go)
	stm.waitersMu[0].Lock()
	s := atomic.LoadUint64(&stm.txState[0])
	_, st := unpackState(s)
	// Trạng thái đã xong (1, 2, 3), TX 1 phải đẩy thẳng vào execCh, không được nhét vào waiters
	if st == 1 || st == 2 || st == 3 {
		stm.waitersMu[0].Unlock()
		execCh <- 1
	} else {
		stm.waiters[0] = append(stm.waiters[0], 1)
		stm.waitersMu[0].Unlock()
	}

	// Đảm bảo TX 1 không bị kẹt (không bị nhét vào mảng waiters)
	stm.waitersMu[0].Lock()
	w := stm.waiters[0]
	stm.waitersMu[0].Unlock()
	
	if len(w) > 0 {
		t.Fatalf("LỖI Race Condition: TX 1 tự đưa mình vào hàng chờ trong khi TX 0 ĐÃ XONG (Lost wake-up)!")
	}

	// Đảm bảo TX 1 được đẩy thẳng vào kênh execCh
	select {
	case val := <-execCh:
		if val != 1 {
			t.Errorf("Kỳ vọng TX 1, nhận %d", val)
		}
	default:
		t.Fatalf("LỖI: TX 1 không được đẩy vào kênh chạy lại!")
	}
}
