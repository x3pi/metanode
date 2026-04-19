package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"path/filepath"

	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	runtime_debug "runtime/debug"
	"runtime/pprof"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/devicekey"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	"github.com/meta-node-blockchain/meta-node/pkg/mvm"
)

var (
	defaultConfigPath = flag.String("config", "config.json", "Config path")
	logLevel          = flag.Int("log-level", logger.FLAG_INFO, "Log level")
	debug             = flag.Bool("debug", false, "Debug mode")
	// THÊM FLAG SSH-KEY TẠI ĐÂY
	sshKeyPath = flag.String("ssh-key", "", "Path to SSH private key")
	pprofAddr  = flag.String("pprof-addr", "localhost:6060", "Địa chỉ bind pprof (để trống để tắt)")

	// Tool flags
	toolRegisterValidator = flag.String("tool-register-validator", "", "Path to config.json for validator registration tool. If set, runs the tool and exits.")
	toolGetAddress        = flag.String("tool-get-address", "", "Hex private key to calculate address. If set, prints address and exits.")
)

var (
	BuildTime     string
	EnvDecryptKey string
	EnvFirstKey   string
)
var logCleaner *loggerfile.LogCleaner

func main() {
	// Thêm panic recovery để log khi panic
	defer func() {
		if r := recover(); r != nil {
			logger.Error("[FATAL] ===== PANIC DETECTED =====")
			logger.Error("[FATAL] Panic value: %v", r)
			logger.Error("[FATAL] Stack trace:\n%s", runtime_debug.Stack())
			logger.Error("[FATAL] ===== END PANIC =====")
			logger.SyncFileLog() // Force flush ra disk trước khi exit
			os.Exit(1)
		}

	// PERFORMANCE OPTIMIZATION: Cleanup TrieDB connection pool on shutdown
	logger.Info("[PERF] Cleaning up TrieDB connection pool on shutdown...")
	// Note: closeTrieDBPool() is called from processor package
}()

// CRITICAL FIX: Ignore SIGPIPE. Rust's tokio assumes SIGPIPE is ignored by the process.
// Because Rust is loaded via CGo, it didn't run its standard OS init hook to ignore SIGPIPE.
// When Tokio writes to a closed socket, it triggers a SIGPIPE, killing the entire process instantly.
signal.Ignore(syscall.SIGPIPE)

	// KHỞI TẠO CỜ LỆNH
	// Gọi Parse() sau khi đã định nghĩa TẤT CẢ các flag
	flag.Parse()

	// --- BẮT ĐẦU PROFILING NẾU LÀ MASTER 0 ---
	if *pprofAddr == "0.0.0.0:6061" || *pprofAddr == "localhost:6060" || os.Getenv("PPROF_PROFILE") == "1" {
		f, err := os.Create("startup_cpu.prof")
		if err == nil {
			pprof.StartCPUProfile(f)
			go func() {
				time.Sleep(25 * time.Second)
				pprof.StopCPUProfile()
				f.Close()
				logger.Info("✅ CPU profile saved to startup_cpu.prof")
			}()
			logger.Info("🚀 Bắt đầu ghi CPU Profile ra file startup_cpu.prof (25s)")
		} else {
			logger.Error("❌ Lỗi tạo file profile: %v", err)
		}
	}
	// ----------------------------------------

	if *toolRegisterValidator != "" {
		runRegisterValidator(*toolRegisterValidator)
		os.Exit(0)
	}

	if *toolGetAddress != "" {
		runGetAddress(*toolGetAddress)
		os.Exit(0)
	}

	// Log thông tin process
	logger.Info("[MAIN] ===== Khởi động process =====")
	logger.Info("[MAIN] PID: %d", os.Getpid())
	logger.Info("[MAIN] UID: %d, GID: %d", os.Getuid(), os.Getgid())
	logger.Info("[MAIN] PPID (Parent PID): %d", os.Getppid())

	// PERFORMANCE OPTIMIZATION: Increase GOMAXPROCS for better CPU utilization
	// Use more CPU cores for concurrent processing
	numCPU := runtime.NumCPU()
	maxProcs := numCPU // Use 1x CPU cores, since hyperthreading is usually enough, and 104 is big enough

	oldMaxProcs := runtime.GOMAXPROCS(maxProcs)
	logger.Info("[PERF] Increased GOMAXPROCS from %d to %d (NumCPU: %d)", oldMaxProcs, maxProcs, numCPU)

	// PERFORMANCE OPTIMIZATION: Optimize GC settings for high-throughput applications
	// Increase GC target percentage to reduce GC frequency (default is 100)
	oldGCPercent := runtime_debug.SetGCPercent(800) // 800% more headroom before GC
	logger.Info("[PERF] Adjusted GC target from %d%% to 800%% heap growth", oldGCPercent)

	// TPS OPT: Set soft memory limit to prevent OOM and bound GC pause time.
	// Without this, GOGC=800 can let heap grow to 50+ GB before GC triggers,
	// causing multi-second GC pauses that stall block processing.
	// 8GB is ~5% of 157GB server RAM — ample for blockchain state + caches.
	runtime_debug.SetMemoryLimit(8 << 30) // 8GB soft limit
	logger.Info("[PERF] Set GOMEMLIMIT=8GB (soft memory limit for GC pacing)")

	// Log resource limits
	logResourceLimits()

	// Log file descriptors đang sử dụng
	logFileDescriptors()

	// Log cgroup limits nếu có
	logCgroupLimits()

	// Log memory stats ban đầu
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	logger.Info("[MAIN] Memory ban đầu - Alloc: %d KB, Sys: %d KB, NumGC: %d",
		m.Alloc/1024, m.Sys/1024, m.NumGC)

	// Log goroutines ban đầu
	logger.Info("[MAIN] Goroutines ban đầu: %d", runtime.NumGoroutine())

	// Gọi hàm kiểm tra device key SAU KHI đã parse flag
	// Truyền giá trị của flag sshKeyPath vào
	if err := initializeDeviceKey(*sshKeyPath); err != nil {
		logger.Error("[FATAL] Device key initialization failed: %v", err)
		logger.SyncFileLog()
		os.Exit(1)
	}

	if *debug {
		startDebugServer(*pprofAddr)
	}

	// Khởi chạy ứng dụng
	app, err := NewApp(*defaultConfigPath, *logLevel)

	if err != nil {
		logger.Error("[FATAL] Failed to create app: %v", err)
		logger.SyncFileLog()
		os.Exit(1)
	}

	// Thêm panic recovery cho goroutine app.Run
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("[FATAL] ===== PANIC trong app.Run() =====")
				logger.Error("[FATAL] Panic value: %v", r)
				logger.Error("[FATAL] Stack trace:\n%s", runtime_debug.Stack())
				logger.Error("[FATAL] ===== END PANIC =====")
				logger.SyncFileLog()
				os.Exit(1)
			}
		}()
		if err := app.Run(); err != nil {
			// Do not hard-kill the process if it's just shutting down and the context was canceled
			if err.Error() == "context canceled" || strings.Contains(err.Error(), "canceled") {
				logger.Info("✅ [MAIN] app.Run() exited gracefully (context canceled).")
				return
			}
			logger.Error("[FATAL] app.Run() error: %v", err)
			logger.SyncFileLog()
			os.Exit(1)
		}
	}()

	logDir := app.config.LogPath
	loggerfile.SetGlobalLogDir(logDir)

	initializeLogCleaner(logDir, app.config.EpochsToKeep)
	if _, err := logger.EnableFileLog("App.log"); err != nil {
		logger.Error("enable file log failed: %v", err)
		os.Exit(1)
	}
	// Tắt console — chỉ ghi vào epoch file log (tránh trùng lặp với shell redirect)
	logger.SetConsoleOutputEnabled(false)

	// Redirect C++ cout/cerr sang file riêng, tách theo process
	// Derive tên process từ config file, ví dụ:
	//   config-master.json       → master
	//   config-sub-write.json    → sub-write
	//   config-master-node1.json → master-node1
	//   config-sub-node1.json    → sub-node1
	mvmProcessName := filepath.Base(*defaultConfigPath)
	mvmProcessName = strings.TrimSuffix(mvmProcessName, filepath.Ext(mvmProcessName))
	mvmProcessName = strings.TrimPrefix(mvmProcessName, "config-")
	mvmProcessName = strings.TrimPrefix(mvmProcessName, "config")
	if mvmProcessName == "" {
		mvmProcessName = "default"
	}
	mvm.InitMVMCppLog("", mvmProcessName)
	defer mvm.CloseMVMCppLog()
	defer mvm.CloseMVMFileLogger()

	// Redirect stderr vào file log
	// Đảm bảo unrecovered panic và runtime fatal error được ghi vào file
	if err := logger.RedirectStderrToFile(); err != nil {
		logger.Warn("⚠️ [LOG] Không thể redirect stderr: %v", err)
	}

	// Khởi động memory monitoring
	// go startMemoryMonitoring()

	// Khởi động goroutine leak monitoring
	// go startGoroutineLeakMonitoring()
	startRPCServer(app)
	handleExitSignals(app)

}

// Sửa lại hàm để nhận tham số sshKeyPath
func initializeDeviceKey(sshKeyPath string) error {
	// Truyền tham số này xuống hàm CalculateUUID
	return devicekey.CalculateUUID(BuildTime, EnvDecryptKey, EnvFirstKey, sshKeyPath)
}

// ... các hàm khác giữ nguyên ...
func initializeLogCleaner(logDir string, epochsToKeepPtr *int) {
	logCleaner = loggerfile.NewLogCleaner(logDir)
	loggerfile.SetGlobalLogCleaner(logCleaner) // Đăng ký global để snapshot_init dùng lại

	// Xác định epochsToKeep:
	// - nil (không set trong JSON) → mặc định 3
	// - 0 → archive mode, giữ tất cả
	// - N → giữ N epoch gần nhất
	var epochsToKeep int
	if epochsToKeepPtr != nil {
		epochsToKeep = *epochsToKeepPtr
	} else {
		epochsToKeep = 3
	}

	logCleaner.SetMaxEpochsToKeep(epochsToKeep)

	if logCleaner.IsCleanupDisabled() {
		logger.Info("🔒 [LOG] Chế độ archive (epochs_to_keep=0): giữ tất cả logs")
		return
	}

	// Dọn dẹp log epoch cũ khi khởi động
	if err := logCleaner.CleanOldEpochLogs(); err != nil {
		logger.Error("Lỗi khi dọn dẹp logs khởi động: %v", err)
	} else {
		logger.Info("🧹 [LOG] Đã dọn dẹp logs cũ khi khởi động")
	}
	// Khởi động periodic cleanup
	logCleaner.StartPeriodicCleanup()
	logger.Info("🧹 [LOG] Đã khởi động periodic cleanup (giữ %d epoch gần nhất)", epochsToKeep)
}

// func handleDeviceKeyError(err error) {
// 	if BuildTime == "" {
// 		exitTimer := time.NewTimer(30 * time.Hour)
// 		go func() {
// 			<-exitTimer.C
// 			logger.Info("30 hours elapsed. Exiting...")
// 			os.Exit(0)
// 		}()
// 	} else {
// 		panic(err)
// 	}
// }

func startDebugServer(addr string) {
	if addr == "" {
		logger.Info("Pprof server không khởi động vì --pprof-addr rỗng")
		return
	}

	go func() {
		listener, err := net.Listen("tcp", addr)
		if err != nil {
			logger.Error("Không thể khởi động pprof server: ", err)
			return
		}
		logger.Info("Pprof server đang chạy tại: ", listener.Addr().String())
		if err := http.Serve(listener, nil); err != nil {
			logger.Error("Pprof server error: ", err)
		}
	}()
}

func handleExitSignals(app *App) {
	sigs := make(chan os.Signal, 1)
	done := make(chan struct{})
	// Thêm SIGHUP để xử lý tmux kill-session (gửi SIGHUP thay vì SIGTERM)
	// Nếu không xử lý SIGHUP, LevelDB sẽ không được Close() → mất dữ liệu khi restart
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGABRT, syscall.SIGHUP)

	go func() {
		sig := <-sigs
		logger.Info("[SIGNAL] ===== Nhận signal: %v =====", sig)
		logger.Info("[SIGNAL] Signal name: %s", sig.String())
		logger.Info("[SIGNAL] PID: %d", os.Getpid())

		// Log memory stats trước khi dừng
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		logger.Info("[SIGNAL] Memory stats - Alloc: %d KB, Sys: %d KB, HeapAlloc: %d KB, HeapSys: %d KB, NumGC: %d",
			m.Alloc/1024, m.Sys/1024, m.HeapAlloc/1024, m.HeapSys/1024, m.NumGC)

		// Log goroutines
		logger.Info("[SIGNAL] Số lượng goroutines: %d", runtime.NumGoroutine())

		// Log stack trace của tất cả goroutines
		buf := make([]byte, 1024*1024)
		n := runtime.Stack(buf, true)
		logger.Info("[SIGNAL] Stack trace của tất cả goroutines:\n%s", buf[:n])

		logger.Info("[SIGNAL] Đang dừng app...")
		app.Stop()
		logger.Info("[SIGNAL] App đã dừng")
		logger.SyncFileLog()
		close(done)
	}()

	<-done
	logger.Info("[SIGNAL] Process đang thoát...")
	logger.SyncFileLog()
}

func startRPCServer(app *App) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Error("[FATAL] ===== PANIC trong RPC server =====")
				logger.Error("[FATAL] Panic value: %v", r)
				logger.Error("[FATAL] Stack trace:\n%s", runtime_debug.Stack())
				logger.Error("[FATAL] ===== END PANIC =====")
				logger.SyncFileLog()
			}
		}()
		mux := NewServer(app)
		logger.Info("RPC server đang chạy tại port: ", app.config.RpcPort)
		server := &http.Server{
			Addr:              app.config.RpcPort,
			Handler:           mux,
			ReadTimeout:       rpcServerReadTimeout,
			ReadHeaderTimeout: rpcServerReadHeaderTimeout,
			WriteTimeout:      rpcServerWriteTimeout,
			IdleTimeout:       rpcServerIdleTimeout,
		}
		if app.config.TlsCert != "" && app.config.TlsKey != "" {
			logger.Info("Starting HTTPS server...")
			if err := server.ListenAndServeTLS(app.config.TlsCert, app.config.TlsKey); err != nil && err != http.ErrServerClosed {
				logger.Error("[FATAL] Không thể khởi động HTTPS server: %v", err)
				logger.SyncFileLog()
				os.Exit(1)
			}
		} else {
			if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("[FATAL] Không thể khởi động HTTP server: %v", err)
				logger.SyncFileLog()
				os.Exit(1)
			}
		}
	}()
}

// startMemoryMonitoring bắt đầu monitoring memory định kỳ
func startMemoryMonitoring() {
	ticker := time.NewTicker(30 * time.Second) // Log mỗi 30 giây
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)

			// Log memory stats
			logger.Info("[MEMORY] ===== Memory Stats =====")
			logger.Info("[MEMORY] Alloc: %d KB (%.2f MB)", m.Alloc/1024, float64(m.Alloc)/(1024*1024))
			logger.Info("[MEMORY] TotalAlloc: %d KB (%.2f MB)", m.TotalAlloc/1024, float64(m.TotalAlloc)/(1024*1024))
			logger.Info("[MEMORY] Sys: %d KB (%.2f MB)", m.Sys/1024, float64(m.Sys)/(1024*1024))
			logger.Info("[MEMORY] HeapAlloc: %d KB (%.2f MB)", m.HeapAlloc/1024, float64(m.HeapAlloc)/(1024*1024))
			logger.Info("[MEMORY] HeapSys: %d KB (%.2f MB)", m.HeapSys/1024, float64(m.HeapSys)/(1024*1024))
			logger.Info("[MEMORY] HeapIdle: %d KB (%.2f MB)", m.HeapIdle/1024, float64(m.HeapIdle)/(1024*1024))
			logger.Info("[MEMORY] HeapInuse: %d KB (%.2f MB)", m.HeapInuse/1024, float64(m.HeapInuse)/(1024*1024))
			logger.Info("[MEMORY] NumGC: %d", m.NumGC)
			logger.Info("[MEMORY] NumGoroutine: %d", runtime.NumGoroutine())

			// Log file descriptors
			fdCount := countOpenFileDescriptors()
			logger.Info("[MEMORY] Open file descriptors: %d", fdCount)

			// Phân tích file descriptors
			if fdCount > 0 {
				fdTypes := analyzeFileDescriptors()
				if len(fdTypes) > 0 {
					logger.Info("[MEMORY] File descriptor types: %v", fdTypes)
				}
			}

			// Cảnh báo nếu file descriptors quá nhiều
			if fdCount > 500 {
				logger.Warn("[MEMORY] ⚠️ CẢNH BÁO: File descriptors quá nhiều (%d), có thể có leak!", fdCount)
			}

			logger.Info("[MEMORY] ===== End Memory Stats =====")

			// Cảnh báo nếu memory quá cao
			if m.HeapAlloc > 2*1024*1024*1024 { // > 2GB
				logger.Warn("[MEMORY] ⚠️ CẢNH BÁO: HeapAlloc > 2GB: %.2f MB", float64(m.HeapAlloc)/(1024*1024))
			}
		}
	}
}

// startGoroutineLeakMonitoring theo dõi goroutine leaks
func startGoroutineLeakMonitoring() {
	ticker := time.NewTicker(10 * time.Second) // Log mỗi 10 giây
	defer ticker.Stop()

	lastGoroutineCount := runtime.NumGoroutine()
	lastCheckTime := time.Now()

	for {
		select {
		case <-ticker.C:
			currentGoroutineCount := runtime.NumGoroutine()
			elapsed := time.Since(lastCheckTime)

			if currentGoroutineCount > lastGoroutineCount {
				increaseRate := float64(currentGoroutineCount-lastGoroutineCount) / elapsed.Seconds()
				logger.Warn("[GOROUTINE] ⚠️ Goroutines tăng: %d -> %d (tăng %d trong %.1fs, tốc độ: %.2f/s)",
					lastGoroutineCount, currentGoroutineCount,
					currentGoroutineCount-lastGoroutineCount,
					elapsed.Seconds(), increaseRate)

				// Cảnh báo nếu tăng quá nhanh
				if increaseRate > 3 {
					logger.Warn("[GOROUTINE] 🚨 CẢNH BÁO: Goroutines đang tăng nhanh (%.2f/s)! Có thể có goroutine leak!", increaseRate)

					// Log stack trace của một số goroutines để debug
					if increaseRate > 5 {
						logger.Info("[GOROUTINE] 🔍 Đang phân tích goroutines...")
						analyzeGoroutines()
					}
				}
			} else if currentGoroutineCount < lastGoroutineCount {
				logger.Info("[GOROUTINE] ✅ Goroutines giảm: %d -> %d (giảm %d trong %.1fs)",
					lastGoroutineCount, currentGoroutineCount,
					lastGoroutineCount-currentGoroutineCount,
					elapsed.Seconds())
			}

			// Cảnh báo nếu số lượng goroutines quá cao
			if currentGoroutineCount > 1000 {
				logger.Warn("[GOROUTINE] ⚠️ Số lượng goroutines cao: %d (nên < 500 cho hệ thống nhỏ)", currentGoroutineCount)
			}
			if currentGoroutineCount > 10000 {
				logger.Warn("[GOROUTINE] 🚨 CẢNH BÁO: Số lượng goroutines rất cao: %d", currentGoroutineCount)
			}

			lastGoroutineCount = currentGoroutineCount
			lastCheckTime = time.Now()
		}
	}
}

// analyzeGoroutines phân tích các goroutines đang chạy
func analyzeGoroutines() {
	buf := make([]byte, 512*1024) // Tăng buffer lên 512KB để lấy đủ stack trace
	n := runtime.Stack(buf, true)
	stackTrace := string(buf[:n])

	// Đếm tổng số goroutines từ stack trace
	goroutineCount := strings.Count(stackTrace, "goroutine ")

	// Phân tích từng goroutine riêng biệt
	goroutineBlocks := strings.Split(stackTrace, "\ngoroutine ")
	if len(goroutineBlocks) == 0 {
		return
	}

	// Đếm các loại goroutines (mỗi goroutine chỉ được đếm một lần cho mỗi loại)
	goroutineTypes := map[string]int{
		"select":           0,
		"chan":             0,
		"network":          0,
		"readLoop":         0,
		"writeLoop":        0,
		"HandleConnection": 0,
		"ReadRequest":      0,
		"worker":           0,
		"ticker":           0,
		"libp2p":           0,
		"pubsub":           0,
		"gossipsub":        0,
		"quic":             0,
		"database":         0,
		"rocksdb":          0,
		"leveldb":          0,
		"stateCommitter":   0,
		"createBlockBatch": 0,
		"TxsProcessor":     0,
		"ProcessorPool":    0,
		"other":            0,
	}

	// Phân tích từng goroutine
	for i, block := range goroutineBlocks {
		if i == 0 && !strings.HasPrefix(block, "goroutine ") {
			continue // Bỏ qua phần đầu không phải goroutine
		}

		lowerBlock := strings.ToLower(block)

		// Phân loại goroutine này (mỗi goroutine chỉ được phân loại một lần)
		classified := false

		if strings.Contains(lowerBlock, "libp2p") || strings.Contains(lowerBlock, "go-libp2p") {
			goroutineTypes["libp2p"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "gossipsub") {
			goroutineTypes["gossipsub"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "pubsub") {
			goroutineTypes["pubsub"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "quic") {
			goroutineTypes["quic"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "rocksdb") || strings.Contains(lowerBlock, "rocks") {
			goroutineTypes["rocksdb"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "leveldb") {
			goroutineTypes["leveldb"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "database") || strings.Contains(lowerBlock, "/db") {
			goroutineTypes["database"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "readloop") {
			goroutineTypes["readLoop"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "writeloop") {
			goroutineTypes["writeLoop"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "handleconnection") {
			goroutineTypes["HandleConnection"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "readrequest") {
			goroutineTypes["ReadRequest"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "statecommitter") {
			goroutineTypes["stateCommitter"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "createblockbatch") {
			goroutineTypes["createBlockBatch"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "txsprocessor") {
			goroutineTypes["TxsProcessor"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "processorpool") {
			goroutineTypes["ProcessorPool"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "worker") {
			goroutineTypes["worker"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "ticker") {
			goroutineTypes["ticker"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "select") {
			goroutineTypes["select"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "chan") {
			goroutineTypes["chan"]++
			classified = true
		}
		if strings.Contains(lowerBlock, "network") {
			goroutineTypes["network"]++
			classified = true
		}

		if !classified {
			goroutineTypes["other"]++
		}
	}

	logger.Info("[GOROUTINE] ===== Phân tích chi tiết goroutines =====")
	logger.Info("[GOROUTINE] Tổng số goroutines: %d", goroutineCount)
	logger.Info("[GOROUTINE] Phân loại (mỗi goroutine chỉ đếm 1 lần):")

	// Sắp xếp theo số lượng giảm dần
	type countPair struct {
		typ   string
		count int
	}
	var pairs []countPair
	for typ, count := range goroutineTypes {
		if count > 0 {
			pairs = append(pairs, countPair{typ, count})
		}
	}

	// Sort by count descending
	for i := 0; i < len(pairs)-1; i++ {
		for j := i + 1; j < len(pairs); j++ {
			if pairs[i].count < pairs[j].count {
				pairs[i], pairs[j] = pairs[j], pairs[i]
			}
		}
	}

	for _, p := range pairs {
		logger.Info("[GOROUTINE]   - %s: %d", p.typ, p.count)
	}

	// Tính tổng đã phân loại
	totalClassified := 0
	for _, count := range goroutineTypes {
		totalClassified += count
	}

	logger.Info("[GOROUTINE] Tổng đã phân loại: %d / %d", totalClassified, goroutineCount)
	logger.Info("[GOROUTINE] ===== End Phân tích =====")
}

// logResourceLimits log các resource limits của process
func logResourceLimits() {
	var rlim syscall.Rlimit

	limits := map[string]int{
		"RLIMIT_CPU":    syscall.RLIMIT_CPU,
		"RLIMIT_FSIZE":  syscall.RLIMIT_FSIZE,
		"RLIMIT_DATA":   syscall.RLIMIT_DATA,
		"RLIMIT_STACK":  syscall.RLIMIT_STACK,
		"RLIMIT_CORE":   syscall.RLIMIT_CORE,
		"RLIMIT_NOFILE": syscall.RLIMIT_NOFILE,
		"RLIMIT_AS":     syscall.RLIMIT_AS,
	}

	logger.Info("[RESOURCE] ===== Resource Limits =====")
	for name, limit := range limits {
		err := syscall.Getrlimit(limit, &rlim)
		if err == nil {
			soft := rlim.Cur
			hard := rlim.Max
			if soft == ^uint64(0) {
				soft = 0 // Unlimited
			}
			if hard == ^uint64(0) {
				hard = 0 // Unlimited
			}

			// Log với đơn vị phù hợp
			if limit == syscall.RLIMIT_NOFILE {
				logger.Info("[RESOURCE] %s: soft=%d, hard=%d", name, soft, hard)
			} else if limit == syscall.RLIMIT_AS || limit == syscall.RLIMIT_DATA || limit == syscall.RLIMIT_STACK {
				logger.Info("[RESOURCE] %s: soft=%d KB (%.2f MB), hard=%d KB (%.2f MB)",
					name, soft/1024, float64(soft)/(1024*1024), hard/1024, float64(hard)/(1024*1024))
			} else {
				logger.Info("[RESOURCE] %s: soft=%d, hard=%d", name, soft, hard)
			}

			// Cảnh báo nếu limit thấp
			if limit == syscall.RLIMIT_NOFILE && soft < 10000 {
				logger.Warn("[RESOURCE] ⚠️ CẢNH BÁO: RLIMIT_NOFILE quá thấp (%d), có thể gây vấn đề với nhiều connections", soft)
			}
			if limit == syscall.RLIMIT_AS && soft > 0 && soft < 1024*1024*1024 { // < 1GB
				logger.Warn("[RESOURCE] ⚠️ CẢNH BÁO: RLIMIT_AS quá thấp (%.2f MB)", float64(soft)/(1024*1024))
			}
		}
	}
	logger.Info("[RESOURCE] ===== End Resource Limits =====")
}

// logFileDescriptors log số lượng file descriptors đang mở
func logFileDescriptors() {
	fdCount := countOpenFileDescriptors()
	logger.Info("[FD] Số lượng file descriptors đang mở: %d", fdCount)

	// Kiểm tra limit
	var rlim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlim); err == nil {
		soft := rlim.Cur
		if soft == ^uint64(0) {
			soft = 0
		}
		if soft > 0 {
			usagePercent := float64(fdCount) / float64(soft) * 100
			logger.Info("[FD] Giới hạn: %d, Đang sử dụng: %.2f%%", soft, usagePercent)
			if usagePercent > 80 {
				logger.Warn("[FD] ⚠️ CẢNH BÁO: File descriptors đang sử dụng > 80%% giới hạn!")
			}
		}
	}
}

// countOpenFileDescriptors đếm số lượng file descriptors đang mở
func countOpenFileDescriptors() int {
	pid := os.Getpid()
	fdDir := fmt.Sprintf("/proc/%d/fd", pid)

	files, err := ioutil.ReadDir(fdDir)
	if err != nil {
		// Không thể đọc /proc, thử cách khác
		return -1
	}

	return len(files)
}

// analyzeFileDescriptors phân tích các loại file descriptors
func analyzeFileDescriptors() map[string]int {
	pid := os.Getpid()
	fdDir := fmt.Sprintf("/proc/%d/fd", pid)

	files, err := ioutil.ReadDir(fdDir)
	if err != nil {
		return nil
	}

	types := make(map[string]int)
	for _, file := range files {
		fdPath := fmt.Sprintf("%s/%s", fdDir, file.Name())
		linkTarget, err := os.Readlink(fdPath)
		if err != nil {
			continue
		}

		// Phân loại file descriptors
		if strings.HasPrefix(linkTarget, "socket:") {
			types["socket"]++
		} else if strings.HasPrefix(linkTarget, "pipe:") {
			types["pipe"]++
		} else if strings.HasPrefix(linkTarget, "/") {
			if strings.Contains(linkTarget, ".db") || strings.Contains(linkTarget, "data/") {
				types["database"]++
			} else if strings.Contains(linkTarget, ".log") {
				types["log"]++
			} else {
				types["file"]++
			}
		} else {
			types["other"]++
		}
	}

	return types
}

// logCgroupLimits log cgroup limits nếu có
func logCgroupLimits() {
	pid := os.Getpid()
	cgroupPaths := []string{
		fmt.Sprintf("/proc/%d/cgroup", pid),
	}

	for _, path := range cgroupPaths {
		if data, err := ioutil.ReadFile(path); err == nil {
			lines := strings.Split(string(data), "\n")
			for _, line := range lines {
				if strings.Contains(line, "memory") || strings.Contains(line, "cpu") {
					logger.Info("[CGROUP] %s", strings.TrimSpace(line))

					// Tìm memory limit
					parts := strings.Split(line, ":")
					if len(parts) >= 3 {
						cgroupPath := parts[2]
						if cgroupPath != "/" {
							// Kiểm tra memory limit
							memoryLimitPath := fmt.Sprintf("/sys/fs/cgroup/memory%s/memory.limit_in_bytes", cgroupPath)
							if data, err := ioutil.ReadFile(memoryLimitPath); err == nil {
								limit := strings.TrimSpace(string(data))
								if limit != "max" && limit != "" {
									if limitInt, err := strconv.ParseUint(limit, 10, 64); err == nil {
										logger.Info("[CGROUP] Memory limit: %d bytes (%.2f MB)",
											limitInt, float64(limitInt)/(1024*1024))
									}
								}
							}
						}
					}
				}
			}
		}
	}
}
