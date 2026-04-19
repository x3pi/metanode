package logger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
)

const (
	FLAG_DEBUGP   = 0
	FLAG_TELEGRAM = 6
	FLAG_TRACE    = 5
	FLAG_DEBUG    = 4
	FLAG_INFO     = 3
	FLAG_WARN     = 2
	FLAG_ERROR    = 1

	Reset  = "\033[0m"
	Red    = "\033[31m"
	Green  = "\033[32m"
	Yellow = "\033[33m"
	Blue   = "\033[34m"
	Purple = "\033[35m"
	Cyan   = "\033[36m"
	Gray   = "\033[37m"
	White  = "\033[97m"
)

type LoggerConfig struct {
	Flag             int
	Identifier       string
	TelegramToken    string
	TelegramChatId   int
	TelegramThreadId uint
	Outputs          []*os.File
}

type Logger struct {
	Config *LoggerConfig
}

var config = &LoggerConfig{
	Flag:             FLAG_INFO,
	Outputs:          []*os.File{os.Stdout},
	TelegramChatId:   0,
	TelegramToken:    "",
	TelegramThreadId: 0, // Default
	// TelegramThreadId: 6010, // Devnet
	// TelegramThreadId: 6759, // Testnet
	// TelegramThreadId: 1, // worknet
}

var logger = &Logger{
	Config: config,
}

var (
	fileLoggerMu            sync.Mutex
	fileLoggerInstance      *loggerfile.FileLogger
	fileLoggerFileName      string
	fileLoggerCurrentEpoch  uint64
	fileLoggerSizeCheckStop chan struct{}
	consoleOutputEnabled    = true
)

func SetConfig(newConfig *LoggerConfig) {
	if newConfig == nil {
		return
	}

	config = newConfig
	setOutputsUnsafe(config.Outputs)
	logger.Config.Flag = config.Flag
	logger.Config.Identifier = config.Identifier
	logger.Config.TelegramChatId = config.TelegramChatId
	logger.Config.TelegramToken = config.TelegramToken
	logger.Config.TelegramThreadId = config.TelegramThreadId

	fileLoggerMu.Lock()
	defer fileLoggerMu.Unlock()

	if fileLoggerInstance != nil {
		attachFileLoggerOutputLocked()
	}
}

func SetOutputs(outputs []*os.File) {
	setOutputsUnsafe(outputs)

	fileLoggerMu.Lock()
	defer fileLoggerMu.Unlock()

	if fileLoggerInstance != nil {
		attachFileLoggerOutputLocked()
	}
}

func SetFlag(flag int) {
	config.Flag = flag
}

func SetTelegramInfo(token string, chatId int) {
	config.TelegramToken = token
	config.TelegramChatId = chatId
	config.TelegramThreadId = 0
}

func SetTelegramGroupInfo(token string, chatId int, threadId uint) {
	config.TelegramToken = token
	config.TelegramChatId = chatId
	config.TelegramThreadId = threadId
}

func SetIdentifier(identifier string) {
	config.Identifier = identifier
}

// SetConsoleOutputEnabled bật/tắt việc log ra stdout.
// Khi tắt, logger chỉ ghi vào các output còn lại (ví dụ file).
func SetConsoleOutputEnabled(enabled bool) {
	consoleOutputEnabled = enabled
	setOutputsUnsafe(config.Outputs)
}

func DebugP(message interface{}, a ...interface{}) {
	if config.Flag < FLAG_DEBUGP {
		return
	}
	colored, plain := getLogBuffers(Purple, "DEBUG_P", message, a)
	logger.writeToOutputsSplit(colored, plain)
}

func Trace(message interface{}, a ...interface{}) {
	if config.Flag < FLAG_TRACE {
		return
	}
	colored, plain := getLogBuffers(Blue, "TRACE", message, a)
	logger.writeToOutputsSplit(colored, plain)
}

func Debug(message interface{}, a ...interface{}) {
	if config.Flag < FLAG_DEBUG {
		return
	}
	colored, plain := getLogBuffers(Cyan, "DEBUG", message, a)
	logger.writeToOutputsSplit(colored, plain)
}

func Info(message interface{}, a ...interface{}) {
	if config.Flag < FLAG_INFO {
		return
	}
	colored, plain := getLogBuffers(Green, "INFO", message, a)
	logger.writeToOutputsSplit(colored, plain)
}

func Warn(message interface{}, a ...interface{}) {
	if config.Flag < FLAG_WARN {
		return
	}
	colored, plain := getLogBuffers(Yellow, "WARN", message, a)
	logger.writeToOutputsSplit(colored, plain)
}

func Error(message interface{}, a ...interface{}) {
	if config.Flag < FLAG_ERROR {
		return
	}
	_, plain := getLogBuffers("", "ERROR", message, a)
	if config.TelegramToken != "" && config.TelegramChatId != 0 {
		sendToTelegram(plain)
	}
	colored, _ := getLogBuffers(Red, "ERROR", message, a)
	logger.writeToOutputsSplit(colored, plain)
	// Force sync to disk — đảm bảo Error logs không bị mất khi crash
	syncFileOutputs()
}

// Fatal logs a fatal error message and terminates the program with os.Exit(1).
// Use this instead of panic() for clean shutdown without stack trace dump.
// Logs are synced to disk before exit to prevent log loss.
func Fatal(message interface{}, a ...interface{}) {
	_, plain := getLogBuffers("", "FATAL", message, a)
	if config.TelegramToken != "" && config.TelegramChatId != 0 {
		sendToTelegram(plain)
	}
	colored, _ := getLogBuffers(Red, "FATAL", message, a)
	logger.writeToOutputsSplit(colored, plain)
	// Force sync to disk — đảm bảo Fatal logs không bị mất
	syncFileOutputs()
	os.Exit(1)
}

func Telegram(message interface{}, a ...interface{}) {
	if config.Flag < FLAG_TELEGRAM {
		return
	}
	_, plain := getLogBuffers("", "TELE", message, a)
	sendToTelegram(plain) // Gửi plain text, không ANSI codes
}

func sendToTelegram(messageContent []byte) {
	var jsonPayload bytes.Buffer
	jsonPayload.WriteString(`{"chat_id": "`)
	jsonPayload.WriteString(strconv.Itoa(config.TelegramChatId))
	jsonPayload.WriteString(`", `)

	if config.TelegramThreadId > 0 {
		jsonPayload.WriteString(`"message_thread_id": "`)
		jsonPayload.WriteString(strconv.FormatUint(uint64(config.TelegramThreadId), 10))
		jsonPayload.WriteString(`", `)
	}

	jsonPayload.WriteString(`"text": `)
	// Use json.Marshal to correctly escape the message content for JSON.
	// This will also add the surrounding quotes to the message string.
	escapedMessage, err := json.Marshal(string(messageContent))
	if err != nil {
		// Fallback or log error if marshalling fails, though unlikely for a string.
		// For simplicity, writing the raw string (less safe for complex content).
		// A more robust solution would log this marshalling error.
		fmt.Printf("Error marshalling telegram message content: %v. Sending raw.\n", err)
		jsonPayload.WriteString(`"`)
		jsonPayload.Write(messageContent) // Might be problematic if content has unescaped quotes
		jsonPayload.WriteString(`"`)
	} else {
		jsonPayload.Write(escapedMessage)
	}

	jsonPayload.WriteString(`}`)

	apiUrl := "https://api.telegram.org/bot" + config.TelegramToken + "/sendMessage"
	resp, err := http.Post(
		apiUrl,
		"application/json",
		bytes.NewBuffer(jsonPayload.Bytes()),
	)

	if err != nil {
		// Use your logger here if it's safe and won't cause a loop,
		// otherwise fmt.Println for critical network errors.
		fmt.Printf("Error sending message to Telegram: %v\n", err)
		if resp != nil {
			resp.Body.Close()
		}
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		var body bytes.Buffer
		_, readErr := body.ReadFrom(resp.Body)
		responseText := body.String()
		if readErr != nil {
			responseText = "could not read error response body"
		}
		// Use your logger here if safe.
		fmt.Printf("Error response from Telegram API: %s, Body: %s\n", resp.Status, responseText)
	}
}

// getLogBuffers tạo 2 buffer: colored (cho terminal) và plain (cho file)
func getLogBuffers(color string, prefix string, message interface{}, a []interface{}) (colored []byte, plain []byte) {
	// Tạo plain buffer trước (không ANSI codes)
	plain = buildLogLine("", prefix, message, a)
	if color == "" {
		return plain, plain
	}
	// Tạo colored buffer cho terminal
	colored = buildLogLine(color, prefix, message, a)
	return colored, plain
}

// buildLogLine tạo một dòng log hoàn chỉnh
func buildLogLine(color string, prefix string, message interface{}, a []interface{}) []byte {
	var buffer bytes.Buffer
	var contentBuffer bytes.Buffer

	// Add color if specified
	if color != "" {
		buffer.WriteString(color)
	}

	// Add Identifier if specified
	if config.Identifier != "" {
		buffer.WriteString("[")
		buffer.WriteString(config.Identifier)
		buffer.WriteString("]")
	}

	// Add log level prefix and timestamp
	buffer.WriteString("[")
	buffer.WriteString(prefix)
	buffer.WriteString("][")
	buffer.WriteString(time.Now().Format(time.Stamp))
	buffer.WriteString("] ")

	// Content formatting
	if formatStr, ok := message.(string); ok && len(a) > 0 {
		contentBuffer.WriteString(fmt.Sprintf(formatStr, a...))
	} else {
		contentBuffer.WriteString(fmt.Sprintf("%v", message))
		for _, v_item := range a {
			contentBuffer.WriteString("\n")
			contentBuffer.WriteString(fmt.Sprintf("%v", v_item))
		}
	}

	contentBytes := contentBuffer.Bytes()
	if len(contentBytes) > 0 && contentBytes[len(contentBytes)-1] != '\n' {
		buffer.WriteString(" ")
	}

	buffer.Write(contentBytes)

	// Add color reset if color was used
	if color != "" && color != Reset {
		buffer.WriteString(Reset)
	}

	buffer.WriteString("\n")
	return buffer.Bytes()
}

// writeMu protects concurrent writes to outputs to prevent deadlock
var writeMu sync.Mutex

// writeToOutputsSplit ghi colored ra terminal, plain ra file
// Phân biệt output là terminal (stdout) hay file dựa vào pointer
func (l *Logger) writeToOutputsSplit(colored []byte, plain []byte) {
	writeMu.Lock()
	defer writeMu.Unlock()
	outputs := l.Config.Outputs
	for _, out := range outputs {
		if out == nil {
			continue
		}
		if out == os.Stdout || out == os.Stderr {
			// Terminal output → colored (có ANSI codes)
			out.Write(colored)
		} else {
			// File output → plain (không ANSI codes)
			out.Write(plain)
		}
	}
}

// syncFileOutputs force sync file outputs ra disk
// Gọi sau Error/Fatal để đảm bảo log không mất khi crash
func syncFileOutputs() {
	writeMu.Lock()
	defer writeMu.Unlock()
	for _, out := range config.Outputs {
		if out == nil || out == os.Stdout || out == os.Stderr {
			continue
		}
		out.Sync()
	}
}

// SyncFileLog force flush tất cả log file ra disk
// Gọi trước os.Exit() hoặc khi panic để đảm bảo không mất log
func SyncFileLog() {
	syncFileOutputs()
}

// RedirectStderrToFile redirect stderr của Go runtime vào log file
// Đảm bảo unrecovered panic và runtime fatal error được ghi vào file
// Gọi SAU EnableFileLog()
func RedirectStderrToFile() error {
	fileLoggerMu.Lock()
	defer fileLoggerMu.Unlock()

	if fileLoggerInstance == nil {
		return fmt.Errorf("file logger not enabled yet")
	}

	logFile := fileLoggerInstance.File()
	if logFile == nil {
		return fmt.Errorf("file logger has no file")
	}

	// Redirect stderr (fd 2) đến file log
	// Go runtime sẽ in panic/fatal vào stderr → giờ sẽ vào file
	err := syscall.Dup2(int(logFile.Fd()), int(os.Stderr.Fd()))
	if err != nil {
		return fmt.Errorf("failed to redirect stderr: %w", err)
	}

	log.Printf("📝 [LOG] Stderr redirected to log file — runtime panics will be captured")
	return nil
}

// EnableFileLog bật ghi log vào file theo epoch (logs/epoch_N/filename).
// Hàm sẽ trả về *loggerfile.FileLogger để caller tùy chọn dùng thêm các API chuyên biệt.
func EnableFileLog(fileName string) (*loggerfile.FileLogger, error) {
	fileLoggerMu.Lock()
	defer fileLoggerMu.Unlock()

	trimmed := strings.TrimSpace(fileName)
	if trimmed == "" {
		if fileLoggerFileName != "" {
			trimmed = fileLoggerFileName
		} else {
			trimmed = "app.log"
		}
	}

	if fileLoggerInstance != nil {
		stopSizeCheckLocked()
		oldLogger := fileLoggerInstance
		oldFile := oldLogger.File()
		fileLoggerInstance = nil
		oldLogger.Close()
		removeOutputLocked(oldFile)
	}

	newFileLogger, err := loggerfile.NewFileLogger(trimmed)
	if err != nil {
		return nil, err
	}

	fileLoggerInstance = newFileLogger
	fileLoggerFileName = trimmed
	fileLoggerCurrentEpoch = loggerfile.GetGlobalEpoch()
	attachFileLoggerOutputLocked()
	startSizeCheckLocked()

	return fileLoggerInstance, nil
}

// EnableDailyFileLog giữ backward compatibility — gọi EnableFileLog
func EnableDailyFileLog(fileName string) (*loggerfile.FileLogger, error) {
	return EnableFileLog(fileName)
}

// RotateToEpoch xoay log sang epoch mới
// Gọi khi epoch transition xảy ra
// Tự động tạo thư mục epoch mới và đóng file cũ
func RotateToEpoch(newEpoch uint64) {
	fileLoggerMu.Lock()
	defer fileLoggerMu.Unlock()

	if fileLoggerInstance == nil {
		return
	}

	// Cập nhật epoch trong loggerfile
	loggerfile.SetGlobalEpoch(newEpoch)

	if newEpoch == fileLoggerCurrentEpoch {
		return // Epoch chưa thay đổi
	}

	// Tạo logger mới cho epoch mới
	newLogger, err := loggerfile.NewFileLogger(fileLoggerFileName)
	if err != nil {
		fmt.Printf("failed to rotate log to epoch %d: %v\n", newEpoch, err)
		return
	}

	oldLogger := fileLoggerInstance
	var oldFile *os.File
	if oldLogger != nil {
		oldFile = oldLogger.File()
	}

	fileLoggerInstance = newLogger
	fileLoggerCurrentEpoch = newEpoch
	attachFileLoggerOutputLocked()

	if oldLogger != nil {
		removeOutputLocked(oldFile)
		oldLogger.Close()
	}

	fmt.Printf("📝 [LOG] Rotated to epoch_%d (file %s)\n", newEpoch, fileLoggerFileName)
}

// CloseFileLog đóng file logger (nếu đang bật) và loại khỏi outputs.
func CloseFileLog() {
	fileLoggerMu.Lock()
	defer fileLoggerMu.Unlock()

	if fileLoggerInstance == nil {
		return
	}

	stopSizeCheckLocked()

	fileLoggerInstance.Close()
	removeOutputLocked(fileLoggerInstance.File())
	fileLoggerInstance = nil
}

func attachFileLoggerOutputLocked() {
	if fileLoggerInstance == nil {
		return
	}

	file := fileLoggerInstance.File()
	if file == nil {
		return
	}

	outputs := append([]*os.File(nil), config.Outputs...)
	outputs = append(outputs, file)
	setOutputsUnsafe(outputs)
}

func removeOutputLocked(target *os.File) {
	if target == nil {
		return
	}

	filtered := make([]*os.File, 0, len(config.Outputs))
	for _, out := range config.Outputs {
		if out == nil || out == target {
			continue
		}
		filtered = append(filtered, out)
	}
	setOutputsUnsafe(filtered)
}

func dedupeOutputs(outputs []*os.File) []*os.File {
	seen := make(map[*os.File]struct{})
	deduped := make([]*os.File, 0, len(outputs))
	for _, out := range outputs {
		if out == nil {
			continue
		}
		if _, exists := seen[out]; exists {
			continue
		}
		seen[out] = struct{}{}
		deduped = append(deduped, out)
	}
	return deduped
}

func setOutputsUnsafe(outputs []*os.File) {
	config.Outputs = enforceConsolePreference(dedupeOutputs(outputs))
	logger.Config.Outputs = config.Outputs
	syncStdLoggerOutputs()
}

func enforceConsolePreference(outputs []*os.File) []*os.File {
	if consoleOutputEnabled {
		hasStdout := false
		for _, out := range outputs {
			if out == os.Stdout {
				hasStdout = true
				break
			}
		}
		if !hasStdout {
			outputs = append(outputs, os.Stdout)
		}
		return outputs
	}

	filtered := make([]*os.File, 0, len(outputs))
	for _, out := range outputs {
		if out == os.Stdout {
			continue
		}
		filtered = append(filtered, out)
	}
	return filtered
}

func syncStdLoggerOutputs() {
	writers := make([]io.Writer, 0, len(config.Outputs))
	for _, out := range config.Outputs {
		if out != nil {
			writers = append(writers, out)
		}
	}

	if len(writers) == 0 {
		log.SetOutput(io.Discard)
		return
	}
	if len(writers) == 1 {
		log.SetOutput(writers[0])
		return
	}
	log.SetOutput(io.MultiWriter(writers...))
}

// Epoch-based rotation được trigger bởi RotateToEpoch()
// Size-based rotation được trigger bởi startSizeCheckLocked()

// ============================================================================
// Size-based log rotation — check mỗi 30s, rotate khi vượt 50MB
// ============================================================================

// startSizeCheckLocked khởi động ticker kiểm tra kích thước file log
// Phải gọi khi đã giữ fileLoggerMu
func startSizeCheckLocked() {
	stopSizeCheckLocked()
	fileLoggerSizeCheckStop = make(chan struct{})
	stopChan := fileLoggerSizeCheckStop

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				checkAndRotateBySize()
			case <-stopChan:
				return
			}
		}
	}()
}

// stopSizeCheckLocked dừng ticker kiểm tra kích thước
// Phải gọi khi đã giữ fileLoggerMu
func stopSizeCheckLocked() {
	if fileLoggerSizeCheckStop != nil {
		close(fileLoggerSizeCheckStop)
		fileLoggerSizeCheckStop = nil
	}
}

// checkAndRotateBySize kiểm tra kích thước file log hiện tại
// Nếu vượt quá giới hạn, rotate files rồi tạo mới
// Rotation: App.log → App.log.1 → App.log.2 → App.log.3 (xóa .3 nếu có)
func checkAndRotateBySize() {
	fileLoggerMu.Lock()
	defer fileLoggerMu.Unlock()

	if fileLoggerInstance == nil {
		return
	}

	// Lấy *os.File cũ
	oldFile := fileLoggerInstance.File()
	if oldFile == nil {
		return
	}

	// Kiểm tra kích thước
	info, err := oldFile.Stat()
	if err != nil {
		return
	}

	if info.Size() < loggerfile.DefaultMaxLogFileSize {
		return // Chưa cần rotate
	}

	// Tính đường dẫn file log hiện tại
	logDir := loggerfile.GetGlobalLogDir()
	epochDir := fmt.Sprintf("epoch_%d", loggerfile.GetGlobalEpoch())
	currentPath := filepath.Join(logDir, epochDir, fileLoggerFileName)

	// Đóng file hiện tại trước khi rename
	removeOutputLocked(oldFile)
	fileLoggerInstance.Close()

	// Rotate files: .3 xóa, .2→.3, .1→.2, current→.1
	maxOldFiles := loggerfile.DefaultMaxLogFiles
	for i := maxOldFiles; i >= 1; i-- {
		oldPath := fmt.Sprintf("%s.%d", currentPath, i)
		if i == maxOldFiles {
			os.Remove(oldPath)
		} else {
			newPath := fmt.Sprintf("%s.%d", currentPath, i+1)
			os.Rename(oldPath, newPath)
		}
	}
	os.Rename(currentPath, fmt.Sprintf("%s.1", currentPath))

	// Tạo FileLogger mới (file mới, size=0)
	newLogger, err := loggerfile.NewFileLogger(fileLoggerFileName)
	if err != nil {
		fmt.Printf("🔄 [LOG-ROTATE] Error creating new logger after rotation: %v\n", err)
		fileLoggerInstance = nil
		return
	}

	fileLoggerInstance = newLogger
	attachFileLoggerOutputLocked()

	fmt.Printf("🔄 [LOG-ROTATE] Rotated %s (exceeded %dMB, keeping %d old files)\n",
		fileLoggerFileName, loggerfile.DefaultMaxLogFileSize/(1024*1024), maxOldFiles)
}
