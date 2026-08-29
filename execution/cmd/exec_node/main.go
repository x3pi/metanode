package main

import (
	"flag"
	"fmt"
	"log"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
)

var (
	defaultConfigPath = flag.String("config", "config.json", "Config path")
	logLevel          = flag.Int("log-level", logger.FLAG_INFO, "Log level")
	debug             = flag.Bool("debug", false, "Debug mode")
)

var logCleaner *loggerfile.LogCleaner

func main() {
	// KHỞI TẠO CỜ LỆNH
	// Gọi Parse() sau khi đã định nghĩa TẤT CẢ các flag
	flag.Parse()

	// if *debug {
	// 	startDebugServer()
	// }

	// Khởi chạy ứng dụng
	app, err := NewApp(*defaultConfigPath, *logLevel)
	if err != nil {
		log.Fatalf("Failed to create app: %v", err)
	}

	go func() {
		if err := app.Run(); err != nil {
			logger.Fatal(fmt.Sprintf("App.Run() failed: %v", err))
		}
	}()

	loggerfile.SetGlobalLogDir(app.config.LogPath)

	initializeLogCleaner(app.config.LogPath)
	handleExitSignals(app)
}

func initializeLogCleaner(logDir string) {
	logCleaner = loggerfile.NewLogCleaner(logDir)
	if err := logCleaner.CleanOldEpochLogs(); err != nil {
		logger.Error(fmt.Sprintf("Lỗi khi xóa logs khởi động: %v", err))
	} else {
		logger.Info("Đã xóa logs khi khởi động thành công")
	}
	logCleaner.StartPeriodicCleanup()
	logger.Info("Đã khởi động lịch trình xóa logs hàng ngày lúc 12h")
}

func handleExitSignals(app *App) {
	sigs := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigs
		app.Stop()
		close(done)
	}()

	<-done
}
