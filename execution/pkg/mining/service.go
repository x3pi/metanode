// service/service.go
package mining

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto" // Import crypto for PubkeyToAddress in NewMiningService
	"github.com/meta-node-blockchain/meta-node/pkg/goxapian"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// Khai báo các hằng số cho JobStatus và JobType
const (
	// Job statuses
	JobStatusNew       = "new"
	JobStatusCompleted = "completed"

	// Job types
	JobTypeValidateBlock = "validate_block"
	JobTypeVideoAds      = "video_ads"

	// Transaction history prefixes
	TxHistoryPrefix = "T"
)

// Job định nghĩa cấu trúc dữ liệu cho một công việc.
type Job struct {
	DocID       uint64  `json:"docId"`
	JobID       string  `json:"jobId"`
	Creator     string  `json:"creator"`
	Assignee    string  `json:"assignee"`
	JobType     string  `json:"job_type"`
	Status      string  `json:"status"`
	Data        string  `json:"data"` // Có thể là block number hoặc link video
	Reward      float64 `json:"reward"`
	CreatedAt   int64   `json:"created_at"`
	CompletedAt int64   `json:"completed_at"`
	TxHash      string  `json:"txHash,omitempty"` // Thêm trường này để lưu hash giao dịch
}

// TransactionRecord định nghĩa cấu trúc dữ liệu cho một bản ghi giao dịch lịch sử.
type TransactionRecord struct {
	TxID      string  `json:"txId"` // Unique ID for the transaction record, e.g., txHash
	JobID     string  `json:"jobId"`
	Sender    string  `json:"sender"`
	Recipient string  `json:"recipient"`
	Amount    float64 `json:"amount"`
	Timestamp int64   `json:"timestamp"`
	Status    string  `json:"status"` // e.g., "pending", "confirmed", "failed"
}

// MiningService quản lý việc đánh chỉ mục và tìm kiếm dữ liệu công việc (jobs).
type MiningService struct {
	db        *goxapian.Database
	qp        *goxapian.QueryParser
	mu        sync.Mutex
	nextJobID uint64

	// Thêm các biến để quản lý việc gửi giao dịch
	rewardSenderPrivateKey string
	rewardSenderAddress    common.Address
	txBroadcaster          TransactionBroadcaster // Interface để gửi giao dịch
}

// TransactionBroadcaster là một interface để trừu tượng hóa việc gửi giao dịch
// Hàm này sẽ trả về hash giao dịch Ethereum.
type TransactionBroadcaster interface {
	SendRewardTransaction(from common.Address, to common.Address, amount float64, privateKey string) (common.Hash, error)
}

// NewMiningService khởi tạo dịch vụ quản lý job mới.
// Cập nhật hàm NewMiningService để nhận private key và txBroadcaster.
func NewMiningService(dbPath string, senderPrivateKey string, txBroadcaster TransactionBroadcaster) (*MiningService, error) {
	if err := os.MkdirAll(dbPath, 0755); err != nil {
		return nil, fmt.Errorf("could not create job database directory: %v", err)
	}

	db, err := goxapian.NewWritableDatabase(dbPath)
	if err != nil {
		return nil, fmt.Errorf("could not open job xapian database: %v", err)
	}
	qp := goxapian.NewQueryParser()
	qp.SetDatabase(db)
	qp.SetDefaultOp(goxapian.QueryOpOr)

	// Thêm các tiền tố cho tìm kiếm job
	qp.AddPrefix("jobid", "Q")
	qp.AddPrefix("status", "S")
	qp.AddPrefix("assignee", "A")
	qp.AddPrefix("txid", TxHistoryPrefix) // Thêm tiền tố cho lịch sử giao dịch
	qp.AddPrefix("sender", "R")           // Thêm tiền tố cho sender (Rewarder)
	qp.AddPrefix("recipient", "P")        // Thêm tiền tố cho recipient (Paid)

	// Khởi tạo seed cho số ngẫu nhiên
	rand.Seed(time.Now().UnixNano())

	// Lấy địa chỉ từ private key
	// Dùng go-ethereum/crypto để derive địa chỉ từ private key
	privKeyEth, err := crypto.HexToECDSA(strings.TrimPrefix(senderPrivateKey, "0x"))
	if err != nil {
		return nil, fmt.Errorf("invalid sender private key: %w", err)
	}
	rewardSenderAddr := crypto.PubkeyToAddress(privKeyEth.PublicKey)

	service := &MiningService{
		db: db,
		qp: qp,
		// Khởi tạo ID tiếp theo dựa trên số lượng tài liệu đã có trong DB
		nextJobID:              uint64(db.GetDocCount() + 1),
		rewardSenderPrivateKey: senderPrivateKey,
		rewardSenderAddress:    rewardSenderAddr,
		txBroadcaster:          txBroadcaster,
	}

	return service, nil
}

// GetOrAssignJob lấy job có sẵn hoặc gán job mới cho một địa chỉ.
func (s *MiningService) GetOrAssignJob(address common.Address, lastBlockNumber uint64) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	addrLower := strings.ToLower(address.String())

	// 1. Tìm kiếm job có sẵn với trạng thái "new" của địa chỉ này
	queryStr := fmt.Sprintf("assignee:%s AND status:%s", addrLower, JobStatusNew)
	existingJobs, _, err := s._searchAndParseJobs(queryStr, 1)
	if err != nil {
		return nil, fmt.Errorf("error checking for existing job: %w", err)
	}

	if len(existingJobs) > 0 {
		logger.Info("Found existing '%s' job %s for address %s", JobStatusNew, existingJobs[0].JobID, address)
		return existingJobs[0], nil
	}

	// 2. Nếu không có job nào, tạo một job mới
	logger.Info("No '%s' job found for %s, creating a new one.", JobStatusNew, address)
	jobID := s.nextJobID
	s.nextJobID++

	newJob := &Job{
		DocID:     jobID,
		JobID:     fmt.Sprintf("job%d", jobID),
		Creator:   "system",
		Assignee:  addrLower,
		Status:    JobStatusNew,
		Reward:    0.01, // Đặt phần thưởng mặc định (ví dụ: 0.01 ETH)
		CreatedAt: time.Now().Unix(),
	}

	// Gán loại công việc và dữ liệu ngẫu nhiên
	if rand.Intn(2) == 0 { // 50% là validate_block
		newJob.JobType = JobTypeValidateBlock
		// Sửa đổi ở đây: Lưu lastBlockNumber dưới dạng hex string
		newJob.Data = fmt.Sprintf("0x%x", lastBlockNumber)
	} else { // 50% là video_ads
		newJob.JobType = JobTypeVideoAds
		videoLinks := []string{
			"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
			"https://www.youtube.com/watch?v=xvFZjo5PgG0",
			"https://www.youtube.com/watch?v=3JZ_D3ELwOQ",
			"https://www.youtube.com/watch?v=L_LUpnjgPso",
		}
		newJob.Data = videoLinks[rand.Intn(len(videoLinks))]
	}

	// 3. Lưu job mới vào database
	if err := s._indexJob(newJob); err != nil {
		s.nextJobID-- // Hoàn tác jobID nếu lưu thất bại
		return nil, fmt.Errorf("failed to index new job: %w", err)
	}

	logger.Info("Created and assigned new job %s (Type: %s, Data: %s) to %s", newJob.JobID, newJob.JobType, newJob.Data, address)
	return newJob, nil
}

// CompleteJob hoàn thành một công việc và xử lý chuyển tiền thưởng.
func (s *MiningService) CompleteJob(jobID string, assigneeAddress string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	job, err := s.GetJobByID(jobID)
	if err != nil {
		return nil, err // GetJobByID đã bao gồm thông báo lỗi "not found"
	}

	// 2. Xác thực
	lowerAssignee := strings.ToLower(assigneeAddress)
	if !strings.EqualFold(job.Assignee, lowerAssignee) {
		return nil, fmt.Errorf("job %s is not assigned to address %s", jobID, assigneeAddress)
	}
	if job.Status != JobStatusNew {
		return nil, fmt.Errorf("job %s is not in '%s' status (current: %s)", jobID, JobStatusNew, job.Status)
	}

	// 3. Xử lý chuyển tiền thưởng
	recipientAddr := common.HexToAddress(job.Assignee) // Địa chỉ người nhận là người được gán job
	rewardAmount := job.Reward

	if s.txBroadcaster == nil {
		return nil, errors.New("transaction broadcaster is not configured")
	}

	logger.Info("Attempting to send %f reward from %s to %s for job %s", rewardAmount, s.rewardSenderAddress.Hex(), recipientAddr.Hex(), jobID)
	txHash, txErr := s.txBroadcaster.SendRewardTransaction(
		s.rewardSenderAddress,
		recipientAddr,
		rewardAmount,
		s.rewardSenderPrivateKey,
	)

	if txErr != nil {
		// Log lỗi nhưng không chặn việc đánh dấu job là completed nếu muốn xử lý lại sau
		logger.Warn("Warning: Failed to send reward transaction for job %s to %s: %v", jobID, recipientAddr.Hex(), txErr)
		// Quyết định: Bạn có thể chọn trả về lỗi ở đây HOẶC tiếp tục đánh dấu job là completed
		// và để hệ thống khác (ví dụ: một worker background) thử lại việc chuyển tiền.
		// Trong ví dụ này, chúng ta sẽ trả về lỗi để đảm bảo tiền được chuyển thành công trước khi job được đánh dấu hoàn thành.
		return nil, fmt.Errorf("failed to send reward for job %s: %w", jobID, txErr)
	}

	// 4. Cập nhật trạng thái Job và lưu hash giao dịch
	job.Status = JobStatusCompleted
	job.CompletedAt = time.Now().Unix()
	job.TxHash = txHash.Hex() // Lưu hash giao dịch

	// 5. Lưu lại job đã cập nhật
	if err := s._indexJob(job); err != nil {
		// Nếu không thể index job sau khi gửi TX, cần một cơ chế phục hồi
		logger.Error("CRITICAL ERROR: Failed to update job %s after successful reward TX %s. Manual intervention may be required!", jobID, txHash.Hex())
		return nil, fmt.Errorf("failed to update job %s after transaction: %w", jobID, err)
	}

	// 6. Lưu lịch sử giao dịch
	txRecord := &TransactionRecord{
		TxID:      txHash.Hex(),
		JobID:     job.JobID,
		Sender:    s.rewardSenderAddress.Hex(),
		Recipient: recipientAddr.Hex(),
		Amount:    rewardAmount,
		Timestamp: time.Now().Unix(),
		Status:    "confirmed", // Hoặc "pending" nếu bạn muốn chờ xác nhận on-chain
	}
	if err := s._indexTransactionRecord(txRecord); err != nil {
		// Log lỗi nhưng không chặn CompleteJob nếu việc index lịch sử không quá quan trọng bằng việc chuyển tiền
		logger.Warn("Warning: Failed to index transaction record for job %s, tx %s: %v", jobID, txHash.Hex(), err)
	}

	logger.Info("Job %s completed by %s. Reward transaction: %s", job.JobID, assigneeAddress, txHash.Hex())
	return job, nil
}

// GetJobByID tìm và trả về thông tin của một job dựa vào ID.
func (s *MiningService) GetJobByID(jobID string) (*Job, error) {
	// Không cần lock/unlock ở đây vì các hàm gọi nó (public) đã có lock
	queryStr := fmt.Sprintf("jobid:%s", jobID)
	jobs, _, err := s._searchAndParseJobs(queryStr, 1)
	if err != nil {
		return nil, fmt.Errorf("error searching for job %s: %w", jobID, err)
	}
	if len(jobs) == 0 {
		return nil, fmt.Errorf("job with ID %s not found", jobID)
	}
	return jobs[0], nil
}

// GetTransactionRecordByTxID tìm và trả về lịch sử giao dịch dựa vào TxID.
func (s *MiningService) GetTransactionRecordByTxID(txID string) (*TransactionRecord, error) {
	queryStr := fmt.Sprintf("txid:%s", txID)
	records, _, err := s._searchAndParseTransactionRecords(queryStr, 1)
	if err != nil {
		return nil, fmt.Errorf("error searching for transaction record %s: %w", txID, err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("transaction record with ID %s not found", txID)
	}
	return records[0], nil
}

// SearchTransactionHistory tìm kiếm và trả về lịch sử giao dịch theo query, có phân trang.
// Đây là hàm public mà MtnAPI sẽ gọi.
func (s *MiningService) SearchTransactionHistory(queryStr string, offset, limit int) ([]*TransactionRecord, uint, error) {
	s.mu.Lock() // Bảo vệ truy cập database
	defer s.mu.Unlock()
	// Gọi hàm nội bộ để thực hiện tìm kiếm và parse
	return s._searchAndParseTransactionRecords(queryStr, limit)
}

// _indexJob là hàm nội bộ để đánh chỉ mục (hoặc cập nhật) một công việc.
func (s *MiningService) _indexJob(job *Job) error {
	jobJSON, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("could not marshal job to JSON: %w", err)
	}

	doc := goxapian.NewDocument()
	defer doc.Close()

	doc.SetData(string(jobJSON))
	doc.AddTerm("S" + job.Status)
	doc.AddTerm("A" + strings.ToLower(job.Assignee))

	uniqueTerm := "Q" + job.JobID // Sử dụng "Q" cho JobID
	doc.AddTerm(uniqueTerm)

	s.db.ReplaceDocumentByTerm(uniqueTerm, doc)
	s.db.Commit()
	return nil
}

// _indexTransactionRecord là hàm nội bộ để đánh chỉ mục một bản ghi giao dịch lịch sử.
func (s *MiningService) _indexTransactionRecord(record *TransactionRecord) error {
	recordJSON, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("could not marshal transaction record to JSON: %w", err)
	}

	doc := goxapian.NewDocument()
	defer doc.Close()

	doc.SetData(string(recordJSON))
	doc.AddTerm(TxHistoryPrefix + record.TxID)           // Sử dụng TxHistoryPrefix cho TxID
	doc.AddTerm("jobid" + record.JobID)                  // Có thể thêm term để tìm giao dịch theo JobID
	doc.AddTerm("R" + strings.ToLower(record.Sender))    // Thêm term cho Sender
	doc.AddTerm("P" + strings.ToLower(record.Recipient)) // Thêm term cho Recipient

	s.db.ReplaceDocumentByTerm(TxHistoryPrefix+record.TxID, doc) // Replace dựa trên TxID
	s.db.Commit()
	return nil
}

// _searchAndParseJobs là hàm nội bộ để tìm kiếm và chuyển đổi kết quả thành []*Job.
func (s *MiningService) _searchAndParseJobs(queryStr string, limit int) ([]*Job, uint, error) {
	query := s.qp.ParseQuery(queryStr, goxapian.FeatureBoolean, goxapian.FeatureBooleanAnyCase)
	if query == nil {
		return nil, 0, fmt.Errorf("error parsing job query: '%s'", queryStr)
	}
	defer query.Close()

	enquire := s.db.Enquire()
	defer enquire.Close()
	enquire.SetQuery(query)

	mset := enquire.GetMSet(0, uint(limit))
	if mset == nil {
		return nil, 0, errors.New("error getting MSet for jobs")
	}
	defer mset.Close()

	totalResults := mset.GetMatchesEstimated()
	var jobs []*Job
	for i := 0; i < mset.GetSize(); i++ {
		doc := mset.GetDocument(uint(i))
		if doc == nil {
			continue
		}
		defer doc.Close()

		var job Job
		if err := json.Unmarshal([]byte(doc.GetData()), &job); err != nil {
			logger.Warn("Warning: could not unmarshal job JSON: %v", err)
			continue
		}
		jobs = append(jobs, &job)
	}

	return jobs, totalResults, nil
}

// _searchAndParseTransactionRecords là hàm nội bộ để tìm kiếm và chuyển đổi kết quả thành []*TransactionRecord.
func (s *MiningService) _searchAndParseTransactionRecords(queryStr string, limit int) ([]*TransactionRecord, uint, error) {
	query := s.qp.ParseQuery(queryStr, goxapian.FeatureBoolean, goxapian.FeatureBooleanAnyCase)
	if query == nil {
		return nil, 0, fmt.Errorf("error parsing transaction record query: '%s'", queryStr)
	}
	defer query.Close()

	enquire := s.db.Enquire()
	defer enquire.Close()
	enquire.SetQuery(query)

	mset := enquire.GetMSet(0, uint(limit))
	if mset == nil {
		return nil, 0, errors.New("error getting MSet for transaction records")
	}
	defer mset.Close()

	totalResults := mset.GetMatchesEstimated()
	var records []*TransactionRecord
	for i := 0; i < mset.GetSize(); i++ {
		doc := mset.GetDocument(uint(i))
		if doc == nil {
			continue
		}
		defer doc.Close()

		var record TransactionRecord
		if err := json.Unmarshal([]byte(doc.GetData()), &record); err != nil {
			logger.Warn("Warning: could not unmarshal transaction record JSON: %v", err)
			continue
		}
		records = append(records, &record)
	}

	return records, totalResults, nil
}

// Close đóng database và các tài nguyên liên quan.
func (s *MiningService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.qp != nil {
		s.qp.Close()
	}
	if s.db != nil {
		s.db.Close()
	}
	return nil
}
