package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"

	// Thay đổi đường dẫn này để khớp với cấu trúc dự án của bạn
	"github.com/meta-node-blockchain/meta-node/pkg/goxapian"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// --- CẤU TRÚC DỮ LIỆU & HẰNG SỐ ---

const (
	JobTypeHashMining = "hash_mining"
	JobTypeVideoAds   = "video_ads"
)

// Danh sách các link video mẫu
var videoLinks = []string{
	"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
	"https://www.youtube.com/watch?v=xvFZjo5PgG0",
	"https://www.youtube.com/watch?v=3JZ_D3ELwOQ",
	"https://www.youtube.com/watch?v=L_LUpnjgPso",
}

// Job struct được mở rộng để khớp với JobInfo trong Solidity
type Job struct {
	DocID       uint64  `json:"docId"`
	JobID       string  `json:"jobId"`
	Creator     string  `json:"creator"`
	Assignee    string  `json:"assignee"`
	JobType     string  `json:"job_type"`
	Status      string  `json:"status"`
	Data        string  `json:"data"`
	Reward      float64 `json:"reward"`
	CreatedAt   int64   `json:"created_at"`
	CompletedAt int64   `json:"completed_at"`
	Nonce       int     `json:"nonce"`
}

type SearchService struct {
	db     *goxapian.Database
	qp     *goxapian.QueryParser
	dbPath string
	mu     sync.Mutex
}

func NewSearchService(dbPath string) (*SearchService, error) {
	db, err := goxapian.NewWritableDatabase(dbPath)
	if err != nil {
		return nil, fmt.Errorf("không thể mở writable database: %v", err)
	}
	qp := goxapian.NewQueryParser()
	qp.SetDatabase(db)
	qp.SetStemmer("vietnamese")
	qp.SetDefaultOp(goxapian.QueryOpOr)

	qp.AddPrefix("status", "S")
	qp.AddPrefix("assignee", "A")
	qp.AddPrefix("jobid", "Q") // Sử dụng tiền tố cho JobID
	qp.AddPrefix("type", "T")  // Thêm tiền tố cho JobType

	return &SearchService{db: db, qp: qp, dbPath: dbPath}, nil
}

func (s *SearchService) Close() {
	s.qp.Close()
	s.db.Close()
}

// Hàm nội bộ để lập chỉ mục cho một Job
func (s *SearchService) _indexJob(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	jobJSON, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("không thể marshal job JSON: %v", err)
	}

	doc := goxapian.NewDocument()
	defer doc.Close()

	doc.SetData(string(jobJSON))

	// Lập chỉ mục cho nội dung của job.Data để tìm kiếm toàn văn
	contentTerms := strings.Fields(strings.ToLower(job.Data))
	for _, term := range contentTerms {
		doc.AddTerm(term)
	}

	doc.AddTerm("S" + job.Status)
	doc.AddTerm("A" + strings.ToLower(job.Assignee))
	doc.AddTerm("T" + job.JobType) // Lập chỉ mục cho loại job

	// Thêm term duy nhất để có thể thay thế document
	uniqueTerm := "Q" + job.JobID
	doc.AddTerm(uniqueTerm)

	s.db.ReplaceDocumentByTerm(uniqueTerm, doc)
	s.db.Commit()

	logger.Info("✔️ Đã index/update Job ID %s trong Xapian.", job.JobID)
	return nil
}

// Hàm tìm kiếm đã được nâng cấp
func (s *SearchService) _findAndParseJobs(queryStr string, maxitems uint, features ...goxapian.QueryParserFeature) ([]*Job, error) {
	logger.Info("Executing query: %s", queryStr)

	query := s.qp.ParseQuery(queryStr, features...)
	if query == nil {
		return nil, fmt.Errorf("lỗi parse query: '%s'", queryStr)
	}
	defer query.Close()

	enquire := s.db.Enquire()
	defer enquire.Close()
	enquire.SetQuery(query)
	mset := enquire.GetMSet(0, maxitems)

	if mset == nil || mset.GetSize() == 0 {
		return []*Job{}, nil
	}
	defer mset.Close()

	var jobs []*Job
	size := mset.GetSize()
	for i := 0; i < size; i++ {
		doc := mset.GetDocument(uint(i))
		if doc == nil {
			logger.Warn("Warning: không thể lấy document từ MSet tại index %d", i)
			continue
		}
		defer doc.Close()

		var job Job
		if err := json.Unmarshal([]byte(doc.GetData()), &job); err != nil {
			logger.Warn("Warning: không thể unmarshal job JSON: %v", err)
			continue
		}
		jobs = append(jobs, &job)
	}

	return jobs, nil
}

// --- API RPC ---
type MiningAPI struct {
	searcher       *SearchService
	nextJobID      int
	idMutex        sync.Mutex
	searchFeatures []goxapian.QueryParserFeature
}

func NewMiningAPI(searcher *SearchService) *MiningAPI {
	rand.Seed(time.Now().UnixNano()) // Khởi tạo seed cho số ngẫu nhiên
	initialDocCount := int(searcher.db.GetDocCount())
	features := []goxapian.QueryParserFeature{
		goxapian.FeatureBoolean,
		goxapian.FeatureBooleanAnyCase,
		goxapian.FeaturePhrase,
	}
	return &MiningAPI{
		searcher:       searcher,
		nextJobID:      initialDocCount + 1,
		searchFeatures: features,
	}
}

func (api *MiningAPI) getNextID() int {
	api.idMutex.Lock()
	defer api.idMutex.Unlock()
	id := api.nextJobID
	api.nextJobID++
	return id
}

// API cho phép truy vấn phức tạp
func (api *MiningAPI) SearchJobs(ctx context.Context, queryStr string) ([]*Job, error) {
	logger.Info("🔎 Yêu cầu tìm kiếm phức tạp: %s", queryStr)
	jobs, err := api.searcher._findAndParseJobs(queryStr, 100, api.searchFeatures...)
	if err != nil {
		logger.Error("❌ Lỗi khi thực hiện tìm kiếm: %v", err)
		return nil, err
	}
	logger.Info("✅ Tìm thấy %d kết quả.", len(jobs))
	return jobs, nil
}

func (api *MiningAPI) GetOrAssignJob(ctx context.Context, address string) (*Job, error) {
	logger.Info("✅ Yêu cầu getOrAssignJob từ: %s", address)
	addrLower := strings.ToLower(address)

	// Bước 1: Tìm xem người dùng đã có job nào được gán với trạng thái "new" chưa.
	assignedQuery := fmt.Sprintf("assignee:%s AND status:new", addrLower)
	existingJobs, err := api.searcher._findAndParseJobs(assignedQuery, 1, api.searchFeatures...)
	if err != nil {
		logger.Error("❌ Lỗi khi kiểm tra job đã có: %v", err)
		return nil, err
	}

	// Nếu tìm thấy một job, trả về nó ngay lập tức và kết thúc hàm.
	if len(existingJobs) > 0 {
		existingJob := existingJobs[0]
		logger.Warn("⚠️ Địa chỉ %s đã có Job ID %s. Trả về job hiện tại.", address, existingJob.JobID)
		return existingJob, nil
	}

	// Bước 2: Nếu không tìm thấy job nào, tạo một job hoàn toàn mới.
	logger.Info("✨ Không có job nào có sẵn cho người dùng, tạo job mới...")
	newJobId := api.getNextID()
	newJob := &Job{
		DocID:     uint64(newJobId),
		JobID:     fmt.Sprintf("auto-job-%d", newJobId),
		Creator:   "0xSERVER_ADDRESS", // Địa chỉ của server hoặc smart contract
		Assignee:  addrLower,
		Status:    "new",
		Reward:    1.0, // Phần thưởng mặc định
		CreatedAt: time.Now().Unix(),
	}

	// Gán loại công việc (JobType) một cách ngẫu nhiên.
	if rand.Intn(2) == 0 {
		newJob.JobType = JobTypeHashMining
		// Tạo dữ liệu ngẫu nhiên cho việc đào hash, tương thích với Keccak256
		hashData := crypto.Keccak256([]byte(fmt.Sprintf("%s-%d", newJob.JobID, time.Now().UnixNano())))
		newJob.Data = hex.EncodeToString(hashData)
		logger.Info("... Đã gán JobType: %s", JobTypeHashMining)
	} else {
		newJob.JobType = JobTypeVideoAds
		// Chọn một link video ngẫu nhiên từ danh sách đã định nghĩa
		newJob.Data = videoLinks[rand.Intn(len(videoLinks))]
		logger.Info("... Đã gán JobType: %s", JobTypeVideoAds)
	}

	// Lưu công việc mới vào cơ sở dữ liệu.
	if err := api.searcher._indexJob(newJob); err != nil {
		return nil, err
	}
	logger.Info("✨ Đã tạo và gán Job %s (Type: %s) mới cho %s", newJob.JobID, newJob.JobType, address)

	// Trả về công việc vừa được tạo.
	return newJob, nil
}

func (api *MiningAPI) CompleteJob(ctx context.Context, address string, jobID string, nonce int) (string, error) {
	logger.Info("✅ Yêu cầu completeJob cho Job ID %s từ %s", jobID, address)
	queryStr := fmt.Sprintf("jobid:%s", jobID)

	jobs, err := api.searcher._findAndParseJobs(queryStr, 1, api.searchFeatures...)
	if err != nil {
		return "", err
	}
	if len(jobs) == 0 {
		return "", fmt.Errorf("job ID %s không tồn tại", jobID)
	}

	job := jobs[0]
	if !strings.EqualFold(job.Assignee, address) {
		return "", fmt.Errorf("job không được gán cho địa chỉ này")
	}
	if job.Status != "new" {
		return fmt.Sprintf("Job %s không ở trạng thái 'new' (hiện tại: %s)", job.JobID, job.Status), nil
	}

	// *** LOGIC XÁC THỰC TƯƠNG THÍCH VỚI SOLIDITY ***
	if job.JobType == JobTypeHashMining {
		logger.Info("⚙️ Xác thực công việc HASH_MINING cho Job ID %s bằng Keccak256...", job.JobID)

		// 1. Giải mã hex string data về bytes
		dataBytes, err := hex.DecodeString(job.Data)
		if err != nil {
			return "", fmt.Errorf("lỗi giải mã job data: %v", err)
		}

		// 2. Chuyển nonce thành big.Int rồi thành 32-byte slice
		nonceBigInt := new(big.Int).SetInt64(int64(nonce))
		nonceBytes := common.LeftPadBytes(nonceBigInt.Bytes(), 32)

		// 3. Ghép nối data và nonce (tương đương abi.encodePacked)
		packedData := append(dataBytes, nonceBytes...)

		// 4. Băm bằng Keccak256
		hash := crypto.Keccak256(packedData)
		hashHex := hex.EncodeToString(hash)

		// 5. Kiểm tra 5 ký tự hex đầu tiên (2.5 bytes)
		if !strings.HasPrefix(hashHex, "00000") {
			logger.Warn("❌ Bằng chứng công việc không hợp lệ cho Job ID %s. Hash: %s", job.JobID, hashHex)
			return "", fmt.Errorf("bằng chứng công việc không hợp lệ: hash không có 5 số 0 đứng đầu")
		}
		logger.Info("✔️ Bằng chứng công việc hợp lệ! Hash: %s", hashHex)
	}

	job.Status = "completed"
	job.Nonce = nonce
	job.CompletedAt = time.Now().Unix()
	if err := api.searcher._indexJob(job); err != nil {
		return "", err
	}
	logger.Info("🎉 Job ID %s đã được hoàn thành bởi %s!", jobID, address)
	return fmt.Sprintf("Job %s đã hoàn thành thành công!", jobID), nil
}

func (api *MiningAPI) DumpDatabase(ctx context.Context) (string, error) {
	logger.Info("⚙️ Yêu cầu dump toàn bộ database...")
	api.searcher.mu.Lock()
	defer api.searcher.mu.Unlock()
	dumpData := api.searcher.db.DumpAllDocs()
	return dumpData, nil
}

// --- MAIN & SETUP ---
type Config struct {
	Port string `json:"port"`
}

func loadConfig(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("không thể mở tệp cấu hình %s: %w", path, err)
	}
	defer file.Close()
	var config Config
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("không thể giải mã tệp cấu hình %s: %w", path, err)
	}
	return &config, nil
}

func main() {
	config, err := loadConfig("config.json")
	if err != nil {
		logger.Error("❌ Lỗi khi tải cấu hình: %v", err)
		os.Exit(1)
	}

	dbPath := "./mining_jobs_db"
	// os.RemoveAll(dbPath) // Bỏ comment để xóa DB cũ mỗi khi khởi động

	searcher, err := NewSearchService(dbPath)
	if err != nil {
		logger.Error("❌ Không thể khởi tạo dịch vụ Xapian: %v", err)
		os.Exit(1)
	}
	defer searcher.Close()
	logger.Info("🚀 Dịch vụ tìm kiếm Xapian đã sẵn sàng tại: %s", dbPath)

	miningAPI := NewMiningAPI(searcher)
	server := rpc.NewServer()
	if err := server.RegisterName("mining", miningAPI); err != nil {
		logger.Error("❌ Không thể đăng ký API: %v", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.Handle("/", server)
	port := config.Port
	logger.Info("📡 Máy chủ RPC đang lắng nghe trên cổng %s...", port)

	httpServer := &http.Server{Addr: ":" + port, Handler: mux}

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("Lỗi máy chủ HTTP: %v", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info("...Đang tắt ứng dụng...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		logger.Error("Lỗi khi tắt máy chủ: %v", err)
		os.Exit(1)
	}
	logger.Info("Máy chủ đã tắt.")
}
