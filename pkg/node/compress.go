package node

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time" // Đảm bảo import time

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	// "github.com/google/uuid" // Bỏ comment nếu muốn dùng UUID thay cho timestamp
)

// --- Cấu trúc và Hàm Helper (Giữ nguyên) ---

type CompressionProfile struct {
	Name      string
	Arguments []string
	MinRAMMB  uint64
	MinCores  int
	IsDefault bool
}

func DetermineBestProfile() (*CompressionProfile, error) {
	cpuCount := runtime.NumCPU()
	mmtArg := "-mmt=off"
	if cpuCount >= 2 {
		mmtArg = "-mmt=on"
	}
	return &CompressionProfile{
		Name:      "Default-DynamicMT",
		Arguments: []string{"-mx=5", mmtArg},
		IsDefault: true,
	}, nil
}

// executeExternalCommand thực thi lệnh bên ngoài.
// Sửa đổi để chấp nhận context và log tốt hơn.
func executeExternalCommand(ctx context.Context, commandString string) (string, error) {
	if commandString == "" {
		return "", fmt.Errorf("chuỗi lệnh trống")
	}

	// Sử dụng "sh -c" và CommandContext để hỗ trợ hủy bỏ
	cmd := exec.CommandContext(ctx, "sh", "-c", commandString)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	logger.Info("(Snapshot) Đang thực thi lệnh shell: ", commandString)
	err := cmd.Run() // Chạy lệnh shell

	stdoutStr := strings.TrimSpace(stdout.String())
	stderrStr := strings.TrimSpace(stderr.String())

	logMsg := fmt.Sprintf("(Snapshot) Lệnh shell '%s'", commandString)
	if stdoutStr != "" {
		logMsg += fmt.Sprintf("\n  stdout: %s", stdoutStr)
	}
	if stderrStr != "" {
		logMsg += fmt.Sprintf("\n  stderr: %s", stderrStr)
	}

	// Kiểm tra lỗi context trước lỗi command
	if ctx.Err() != nil {
		logger.Error(logMsg) // Log output ngay cả khi context bị hủy
		logger.Error("(Snapshot) Context bị hủy trong khi thực thi lệnh shell.")
		return stdoutStr, fmt.Errorf("context bị hủy: %w (stderr: %s)", ctx.Err(), stderrStr)
	}

	if err != nil {
		logger.Error(logMsg) // Log output khi có lỗi
		return stdoutStr, fmt.Errorf("lỗi thực thi lệnh shell '%s': %w\nstderr: %s", commandString, err, stderrStr)
	}

	if stdoutStr != "" || stderrStr != "" {
		logger.Info(logMsg)
	} else {
		logger.Debug("(Snapshot) Thực thi lệnh shell thành công (không có output).")
	}

	return stdoutStr, nil
}

// run7zCompress thực thi lệnh 7z với các tham số đã cho. (Giữ nguyên)
func run7zCompress(ctx context.Context, sevenZArgs []string, description string) error {
	sevenZPath, err := exec.LookPath("7z")
	if err != nil {
		logger.Error("Lệnh '7z' không tìm thấy trong PATH hệ thống. Hãy đảm bảo 7zip đã được cài đặt.")
		return fmt.Errorf("lệnh '7z' không tìm thấy trong PATH hệ thống: %w", err)
	}

	cmd := exec.CommandContext(ctx, sevenZPath, sevenZArgs...)
	logger.Debug(fmt.Sprintf("(7z) Đang thực thi [%s]: %s", description, cmd.String()))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	stdoutStr := stdout.String()
	stderrStr := stderr.String()
	if len(stdoutStr) > 0 {
		logger.Debug("(7z) stdout:", stdoutStr)
	}
	if len(stderrStr) > 0 {
		if err != nil {
			logger.Error("(7z) stderr:", stderrStr)
		} else {
			logger.Debug("(7z) stderr:", stderrStr)
		}
	}

	if err != nil {
		logger.Error(fmt.Sprintf("(7z) Lệnh nén/giải nén thất bại [%s]", description))
		return fmt.Errorf("lỗi thực thi 7z (%s): %w\nstderr: %s", strings.Join(sevenZArgs, " "), err, stderrStr)
	}
	logger.Info(fmt.Sprintf("(7z) Thực thi lệnh thành công [%s]", description))
	return nil
}

// findGeneratedParts tìm các file đã tạo bởi 7z. (Giữ nguyên)
func findGeneratedParts(outputPathPrefix string, isSplit bool) ([]string, error) {
	var pattern string
	if isSplit {
		pattern = outputPathPrefix + ".*"
	} else {
		pattern = outputPathPrefix
	}

	generatedParts, globErr := filepath.Glob(pattern)
	if globErr != nil {
		logger.Error(fmt.Sprintf("Lỗi tìm file đã tạo với pattern '%s': %v", pattern, globErr))
		return nil, fmt.Errorf("lỗi tìm file part sau khi nén: %w", globErr)
	}

	if len(generatedParts) == 0 {
		if !isSplit {
			if _, statErr := os.Stat(outputPathPrefix); statErr == nil {
				return []string{outputPathPrefix}, nil
			}
		}
		logger.Error(fmt.Sprintf("Không tìm thấy file nén/part nào khớp pattern '%s'.", pattern))
		return nil, fmt.Errorf("không tìm thấy file nén/part nào khớp '%s' sau khi nén", pattern)
	}
	return generatedParts, nil
}

// --- Hàm Nén/Giải Nén Gốc (Giữ nguyên để tương thích nếu cần) ---
// Có thể giữ lại các hàm gốc hoặc xóa đi nếu không còn dùng đến

// compressFolderAndSplitInternal (Giữ nguyên)
func compressFolderAndSplitInternal(ctx context.Context, sourceDir, outputDir, baseArchiveName string, splitSizeMB int, snapshotUsed bool) ([]string, error) {
	logSource := sourceDir
	if snapshotUsed {
		logSource = fmt.Sprintf("%s (từ snapshot)", sourceDir)
	}
	cleanBaseName := filepath.Base(baseArchiveName)
	if !strings.HasSuffix(cleanBaseName, ".7z") {
		cleanBaseName += ".7z"
	}
	outputPathPrefix := filepath.Join(outputDir, cleanBaseName)

	logger.Info(fmt.Sprintf("Chuẩn bị nén '%s' vào '%s', bắt đầu với '%s', chia nhỏ: %d MB", logSource, outputDir, outputPathPrefix, splitSizeMB))

	info, err := os.Stat(sourceDir)
	if err != nil {
		return nil, fmt.Errorf("lỗi kiểm tra thư mục nguồn '%s': %w", sourceDir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("đường dẫn nguồn '%s' không phải là thư mục", sourceDir)
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("lỗi tạo thư mục output '%s': %w", outputDir, err)
	}

	patternToRemove := outputPathPrefix + ".*"
	existingParts, _ := filepath.Glob(patternToRemove)
	if splitSizeMB <= 0 {
		if _, err := os.Stat(outputPathPrefix); err == nil {
			existingParts = append(existingParts, outputPathPrefix)
		}
	}
	for _, f := range existingParts {
		logger.Debug(fmt.Sprintf("Đang xóa file/part nén cũ: %s", f))
		if err := os.Remove(f); err != nil {
			logger.Warn(fmt.Sprintf("Không thể xóa file cũ %s: %v", f, err))
		}
	}

	profile, err := DetermineBestProfile()
	var compressionArgs []string
	if err != nil {
		logger.Warn("Không thể xác định cấu hình nén tốt nhất, dùng mặc định (-mx=5 -mmt=on):", err)
		compressionArgs = []string{"-mx=5", "-mmt=on"}
	} else {
		compressionArgs = profile.Arguments
		logger.Info("Sử dụng cấu hình nén được xác định tự động:", profile.Name, profile.Arguments)
	}

	args := []string{"a", "-y"}
	args = append(args, compressionArgs...)

	isSplit := false
	if splitSizeMB > 0 {
		isSplit = true
		volumeSize := strconv.Itoa(splitSizeMB) + "m"
		args = append(args, "-v"+volumeSize)
		logger.Info(fmt.Sprintf("Chia archive thành các phần dung lượng: %s", volumeSize))
	}

	args = append(args, outputPathPrefix, sourceDir)

	err = run7zCompress(ctx, args, fmt.Sprintf("CompressFolderAndSplit for %s", sourceDir))
	if err != nil {
		return nil, err
	}

	generatedParts, err := findGeneratedParts(outputPathPrefix, isSplit)
	if err != nil {
		return nil, err
	}

	logger.Info(fmt.Sprintf("Đã tạo %d file/part nén tại '%s'", len(generatedParts), outputDir))
	for _, part := range generatedParts {
		logger.Debug(fmt.Sprintf("- Part: %s", part))
	}

	return generatedParts, nil
}

// compressFolderInternal (Giữ nguyên)
func compressFolderInternal(ctx context.Context, sourceDir, outputPath string, snapshotUsed bool) error {
	logSource := sourceDir
	if snapshotUsed {
		logSource = fmt.Sprintf("%s (từ snapshot)", sourceDir)
	}
	logger.Info(fmt.Sprintf("Chuẩn bị nén '%s' vào '%s'", logSource, outputPath))

	info, err := os.Stat(sourceDir)
	if err != nil {
		return fmt.Errorf("lỗi kiểm tra thư mục nguồn '%s': %w", sourceDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("đường dẫn nguồn '%s' không phải là thư mục", sourceDir)
	}

	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("lỗi tạo thư mục output '%s': %w", outputDir, err)
	}

	if _, err := os.Stat(outputPath); err == nil {
		logger.Debug("Đang xóa file nén cũ:", outputPath)
		if errRem := os.Remove(outputPath); errRem != nil {
			logger.Warn("Không thể xóa file nén cũ:", errRem)
		}
	}

	profile, err := DetermineBestProfile()
	var compressionArgs []string
	if err != nil {
		logger.Warn("Không thể xác định cấu hình nén tốt nhất, dùng mặc định (-mx=5 -mmt=on):", err)
		compressionArgs = []string{"-mx=5", "-mmt=on"}
	} else {
		compressionArgs = profile.Arguments
		logger.Info("Sử dụng cấu hình nén được xác định tự động:", profile.Name, profile.Arguments)
	}

	args := []string{"a", "-y"}
	args = append(args, compressionArgs...)
	args = append(args, outputPath, sourceDir)

	return run7zCompress(ctx, args, fmt.Sprintf("CompressFolder for %s", sourceDir))
}

// --- Các hàm giải nén (Giữ nguyên) ---

func DecompressFolder(compressedFilePath, outputDir string) error {
	logger.Info(fmt.Sprintf("Giải nén archive '%s' vào thư mục '%s' bằng 7z", compressedFilePath, outputDir))
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("lỗi khi tạo thư mục giải nén '%s': %w", outputDir, err)
	}
	if _, err := os.Stat(compressedFilePath); os.IsNotExist(err) {
		return fmt.Errorf("file nén nguồn '%s' không tồn tại", compressedFilePath)
	}
	args := []string{"x", compressedFilePath, "-o" + outputDir, "-y"}
	return run7zCompress(context.Background(), args, fmt.Sprintf("DecompressFolder for %s", compressedFilePath))
}

func DecompressFile(compressedFilePath, outputDir string) error {
	logger.Info(fmt.Sprintf("Giải nén archive '%s' (có thể là file đơn) vào thư mục '%s' bằng 7z", compressedFilePath, outputDir))
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("lỗi khi tạo thư mục giải nén '%s': %w", outputDir, err)
	}
	if _, err := os.Stat(compressedFilePath); os.IsNotExist(err) {
		return fmt.Errorf("file nén nguồn '%s' không tồn tại", compressedFilePath)
	}
	args := []string{"x", compressedFilePath, "-o" + outputDir, "-y"}
	return run7zCompress(context.Background(), args, fmt.Sprintf("DecompressFile for %s", compressedFilePath))
}

// --- HÀM MỚI CÓ LOGIC SNAPSHOT ĐÃ SỬA ĐỔI ---

// replacePlaceholders thay thế các placeholder trong chuỗi lệnh mẫu.
func replacePlaceholders(template string, replacements map[string]string) string {
	command := template
	logger.Debug(fmt.Sprintf("--- replacePlaceholders --- START ---"))
	logger.Debug(fmt.Sprintf("  Template IN: [%s]", template))
	logger.Debug(fmt.Sprintf("  Replacements map: %v", replacements)) // Log the whole map

	for placeholder, value := range replacements {
		// Log before attempting replacement
		logger.Debug(fmt.Sprintf("  Attempting replace: placeholder='%s', value='%s'", placeholder, value))

		originalCommand := command // Store original for comparison
		command = strings.ReplaceAll(command, placeholder, value)

		// Log after replacement attempt and check if anything changed
		if command != originalCommand {
			logger.Debug(fmt.Sprintf("    REPLACED! command is now: [%s]", command))
		} else {
			logger.Debug(fmt.Sprintf("    NO CHANGE. Placeholder '%s' not found or value identical.", placeholder))
		}
	}
	logger.Debug(fmt.Sprintf("  Command OUT: [%s]", command))
	logger.Debug(fmt.Sprintf("--- replacePlaceholders --- END ---"))
	return command
}

// CompressFolderAndSplitWithOptionalSnapshot attempts to create rsync snapshot before compressing.
// Sử dụng tên snapshot duy nhất cho mỗi lần gọi.
func CompressFolderAndSplitWithOptionalSnapshot(ctx context.Context, sourceDir, outputDir, baseArchiveName string, splitSizeMB int) ([]string, error) {
	logger.Info("Gọi hàm nén (chia nhỏ) với tùy chọn snapshot...")

	// --- Đọc cấu hình Snapshot từ Environment Variables ---
	snapshotEnabled := strings.ToLower(os.Getenv("COMPRESS_ENABLE_SNAPSHOT")) == "true"
	createCmdTemplate := os.Getenv("COMPRESS_SNAPSHOT_CREATE_CMD")
	mountPoint := os.Getenv("COMPRESS_SNAPSHOT_MOUNT_POINT")
	cleanupCmdTemplate := os.Getenv("COMPRESS_SNAPSHOT_CLEANUP_CMD")

	snapshotNameBase := os.Getenv("SNAPSHOT_NAME_BASE")
	rsyncSrc := os.Getenv("RSYNC_MASTER_DATA_SRC_ABS")

	currentSourceDir := sourceDir
	snapshotUsed := false
	var cleanupCmdFinal string
	var snapshotCleanupNeeded bool = false

	if snapshotEnabled {
		logger.Info("Snapshot (rsync) được bật qua biến môi trường.")

		if createCmdTemplate == "" || cleanupCmdTemplate == "" || snapshotNameBase == "" || mountPoint == "" {
			logger.Error("Snapshot được bật, nhưng thiếu cấu hình (CREATE_CMD, CLEANUP_CMD, SNAPSHOT_NAME_BASE, MOUNT_POINT). Bỏ qua snapshot.")
		} else {
			uniqueSuffix := fmt.Sprintf("%d", time.Now().UnixNano())
			snapshotNameUnique := snapshotNameBase + "_" + uniqueSuffix

			replacements := map[string]string{
				"$SNAPSHOT_NAME":             snapshotNameUnique,
				"$MOUNT_POINT":               mountPoint,
				"$SNAPSHOT_MOUNT_POINT":      mountPoint,
				"$RSYNC_MASTER_DATA_SRC_ABS": rsyncSrc,
			}

			createCmdFinal := replacePlaceholders(createCmdTemplate, replacements)
			cleanupCmdFinal = replacePlaceholders(cleanupCmdTemplate, replacements)

			defer func() {
				if snapshotCleanupNeeded {
					logger.Info("(Snapshot) Executing deferred cleanup command: ", cleanupCmdFinal)
					cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 2*time.Minute)
					defer cancelCleanup()
					_, cleanupErr := executeExternalCommand(cleanupCtx, cleanupCmdFinal)
					if cleanupErr != nil {
						logger.Error("!!! LỖI CLEANUP SNAPSHOT !!! Cần cleanup thủ công. Lỗi:", cleanupErr)
					} else {
						logger.Info("(Snapshot) Thực thi lệnh cleanup thành công.")
					}
				}
			}()

			logger.Info("(Snapshot) Executing rsync create command: ", createCmdFinal)
			_, createErr := executeExternalCommand(ctx, createCmdFinal)
			if createErr != nil {
				if ctx.Err() != nil {
					return nil, fmt.Errorf("context bị hủy khi tạo rsync snapshot: %w", ctx.Err())
				}
				logger.Error("(Snapshot) Tạo rsync snapshot thất bại, sẽ nén thư mục gốc.", createErr)
			} else {
				logger.Info("(Snapshot) Rsync snapshot tạo thành công.")
				snapshotCleanupNeeded = true

				// Kiểm tra mount point (rsync target)
				if _, err := os.Stat(mountPoint); err == nil {
					info, err := os.Stat(mountPoint)
					if err != nil {
						logger.Error("(Snapshot) Không thể truy cập rsync target:", mountPoint, err)
						return nil, fmt.Errorf("không thể truy cập rsync target '%s': %w", mountPoint, err)
					}
					if !info.IsDir() {
						logger.Error("(Snapshot) Rsync target không phải thư mục:", mountPoint)
						return nil, fmt.Errorf("rsync target '%s' không phải thư mục", mountPoint)
					}
					currentSourceDir = mountPoint
					snapshotUsed = true
					logger.Info("(Snapshot) Nén sẽ được thực hiện trên rsync target:", currentSourceDir)
				} else {
					logger.Error("(Snapshot) Rsync target không truy cập được:", mountPoint, err)
					return nil, fmt.Errorf("rsync target '%s' không truy cập được: %w", mountPoint, err)
				}
			}
		}
	} else {
		logger.Info("Snapshot không được bật. Sẽ nén thư mục gốc.")
	}

	return compressFolderAndSplitInternal(ctx, currentSourceDir, outputDir, baseArchiveName, splitSizeMB, snapshotUsed)
}

// CompressFolderWithOptionalSnapshot attempts to create rsync snapshot before compressing a folder to a single archive.
func CompressFolderWithOptionalSnapshot(ctx context.Context, sourceDir, outputPath string) error {
	logger.Info("Gọi hàm nén thư mục (file đơn) với tùy chọn snapshot...")

	snapshotEnabled := strings.ToLower(os.Getenv("COMPRESS_ENABLE_SNAPSHOT")) == "true"
	createCmdTemplate := os.Getenv("COMPRESS_SNAPSHOT_CREATE_CMD")
	mountPoint := os.Getenv("COMPRESS_SNAPSHOT_MOUNT_POINT")
	cleanupCmdTemplate := os.Getenv("COMPRESS_SNAPSHOT_CLEANUP_CMD")

	snapshotNameBase := os.Getenv("SNAPSHOT_NAME_BASE")
	rsyncSrc := os.Getenv("RSYNC_MASTER_DATA_SRC_ABS")

	currentSourceDir := sourceDir
	snapshotUsed := false
	var cleanupCmdFinal string
	var snapshotCleanupNeeded bool = false

	if snapshotEnabled {
		logger.Info("Snapshot (rsync) được bật qua biến môi trường.")
		if createCmdTemplate == "" || cleanupCmdTemplate == "" || snapshotNameBase == "" || mountPoint == "" {
			logger.Error("Snapshot được bật, nhưng thiếu cấu hình. Bỏ qua snapshot.")
		} else {
			uniqueSuffix := fmt.Sprintf("%d", time.Now().UnixNano())
			snapshotNameUnique := snapshotNameBase + "_" + uniqueSuffix

			replacements := map[string]string{
				"$SNAPSHOT_NAME":             snapshotNameUnique,
				"$MOUNT_POINT":               mountPoint,
				"$SNAPSHOT_MOUNT_POINT":      mountPoint,
				"$RSYNC_MASTER_DATA_SRC_ABS": rsyncSrc,
			}

			createCmdFinal := replacePlaceholders(createCmdTemplate, replacements)
			cleanupCmdFinal = replacePlaceholders(cleanupCmdTemplate, replacements)

			defer func() {
				if snapshotCleanupNeeded {
					logger.Info("(Snapshot) Executing deferred cleanup command: ", cleanupCmdFinal)
					cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 2*time.Minute)
					defer cancelCleanup()
					_, cleanupErr := executeExternalCommand(cleanupCtx, cleanupCmdFinal)
					if cleanupErr != nil {
						logger.Error("!!! LỖI CLEANUP SNAPSHOT !!! Cần cleanup thủ công. Lỗi:", cleanupErr)
					} else {
						logger.Info("(Snapshot) Thực thi lệnh cleanup thành công.")
					}
				}
			}()

			logger.Info("(Snapshot) Executing rsync create command: ", createCmdFinal)
			_, createErr := executeExternalCommand(ctx, createCmdFinal)
			if createErr != nil {
				if ctx.Err() != nil {
					return fmt.Errorf("context bị hủy khi tạo rsync snapshot: %w", ctx.Err())
				}
				logger.Error("(Snapshot) Tạo rsync snapshot thất bại, sẽ nén thư mục gốc.", createErr)
			} else {
				logger.Info("(Snapshot) Rsync snapshot tạo thành công.")
				snapshotCleanupNeeded = true

				if _, err := os.Stat(mountPoint); err == nil {
					info, err := os.Stat(mountPoint)
					if err != nil {
						return fmt.Errorf("không thể truy cập rsync target '%s': %w", mountPoint, err)
					}
					if !info.IsDir() {
						return fmt.Errorf("rsync target '%s' không phải thư mục", mountPoint)
					}
					currentSourceDir = mountPoint
					snapshotUsed = true
					logger.Info("(Snapshot) Nén sẽ được thực hiện trên rsync target:", currentSourceDir)
				} else {
					logger.Error("(Snapshot) Rsync target không truy cập được:", mountPoint, err)
					return fmt.Errorf("rsync target '%s' không truy cập được: %w", mountPoint, err)
				}
			}
		}
	} else {
		logger.Info("Snapshot không được bật. Sẽ nén thư mục gốc.")
	}

	return compressFolderInternal(ctx, currentSourceDir, outputPath, snapshotUsed)
}
