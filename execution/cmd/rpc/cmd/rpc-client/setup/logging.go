package setup

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
)

// SetupLogging initializes file and console logging system
func SetupLogging(logsDir string) error {
	// 1. Create logs directory if not exists
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return fmt.Errorf("failed to create logs directory %s: %w", logsDir, err)
	}

	// 2. Set global log directory for loggerfile package
	loggerfile.SetGlobalLogDir(logsDir)

	// Log to stdout initially (before file logging is ready)
	log.Printf("Log files will be stored under: %s", logsDir)

	// 3. Enable daily file logging (creates YYYY/MM/DD subdirectories)
	// Example: ./logs/2025/11/17/rpc-client.log
	logFile, err := logger.EnableDailyFileLog("rpc-client.log")
	if err != nil {
		return fmt.Errorf("failed to enable daily file logging: %w", err)
	}

	// 4. Disable console output (only write to file for production)
	logger.SetConsoleOutputEnabled(false)

	// 5. Log initialization success
	logger.Info("Logging system initialized successfully")
	logger.Info("Current log file: %s", logFile)
	logger.Info("Log format: YYYY/MM/DD/rpc-client.log")

	// 6. Setup log rotation monitoring (optional)
	go MonitorLogRotation(logsDir)

	return nil
}

// MonitorLogRotation checks and reports log rotation events
func MonitorLogRotation(logsDir string) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	lastDate := time.Now().Format("2006/01/02")
	for range ticker.C {
		currentDate := time.Now().Format("2006/01/02")
		if currentDate != lastDate {
			logger.Info("Log rotation detected: Date changed from %s to %s", lastDate, currentDate)
			lastDate = currentDate
			// Log current log file path
			currentLogPath := filepath.Join(logsDir, currentDate, "rpc-client.log")
			logger.Info("Now writing to: %s", currentLogPath)
		}
	}
}
