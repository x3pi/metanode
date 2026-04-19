package shard_storage

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ShardStorage là cấu trúc lưu trữ dữ liệu phân mảnh
type ShardStorage struct {
	maxBlocksPerShard int
	shardDir          string
	lineByte          int
}

// NewShardStorage tạo một đối tượng ShardStorage mới
func NewShardStorage(maxBlocksPerShard int, shardDir string, lineByte int) (*ShardStorage, error) {
	// Kiểm tra tính hợp lệ của tham số
	if maxBlocksPerShard <= 0 || lineByte <= 0 {
		return nil, fmt.Errorf("invalid parameters: maxBlocksPerShard and lineByte must be positive")
	}
	// Tạo thư mục shards nếu chưa tồn tại
	if err := os.MkdirAll(shardDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create shard directory: %w", err)
	}

	return &ShardStorage{
		maxBlocksPerShard: maxBlocksPerShard,
		shardDir:          shardDir,
		lineByte:          lineByte,
	}, nil
}

// getShardFileName lấy tên file shard dựa trên số thứ tự shard
func (ss *ShardStorage) getShardFileName(shardIndex int) string {
	return fmt.Sprintf("%s/shard_%d.txt", ss.shardDir, shardIndex)
}

// createShardFileIfNeeded tạo file shard nếu chưa tồn tại, xử lý lỗi tốt hơn
func (ss *ShardStorage) createShardFileIfNeeded(shardFile string) error {
	if _, err := os.Stat(shardFile); os.IsNotExist(err) {
		file, err := os.Create(shardFile)
		if err != nil {
			return fmt.Errorf("failed to create shard file: %w", err)
		}
		defer file.Close() // Đóng file sau khi sử dụng
	}
	return nil
}

func (ss *ShardStorage) ensureLinesExist(file *os.File, lineIndex int) error {
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	fileSize := fileInfo.Size()
	// Thay đổi ở đây: Sử dụng phép chia int64
	neededLines := lineIndex - int(fileSize/(int64(ss.lineByte+1))) + 1

	if neededLines > 0 {
		_, err = file.Seek(fileSize, 0)
		if err != nil {
			return fmt.Errorf("failed to seek to end of file: %w", err)
		}
		for i := 0; i < neededLines; i++ {
			_, err = file.WriteString(strings.Repeat(" ", ss.lineByte) + "\n")
			if err != nil {
				return fmt.Errorf("failed to write empty line: %w", err)
			}
		}
	}
	return nil
}

// updateLine cập nhật một dòng trong file shard
func (ss *ShardStorage) updateLine(shardFile string, lineIndex int, newLine string) error {
	file, err := os.OpenFile(shardFile, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("failed to open shard file: %w", err)
	}
	defer file.Close()

	// Kiểm tra và tạo các dòng trống nếu cần thiết
	if err := ss.ensureLinesExist(file, lineIndex); err != nil {
		return fmt.Errorf("failed to ensure lines exist: %w", err)
	}

	// Viết dòng mới vào file
	_, err = file.Seek(int64(lineIndex*(ss.lineByte+1)), 0)
	if err != nil {
		return fmt.Errorf("failed to seek to line: %w", err)
	}
	_, err = file.WriteString(newLine + "\n")
	if err != nil {
		return fmt.Errorf("failed to write line: %w", err)
	}

	return nil
}

// getLine đọc dòng thứ n của file
func (ss *ShardStorage) getLine(shardFile string, lineIndex int) (string, error) {
	file, err := os.Open(shardFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to open shard file: %w", err)
	}
	defer file.Close()

	// Kiểm tra file rỗng
	fileInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to get file info: %w", err)
	}
	if fileInfo.Size() == 0 {
		return "", nil
	}
	lineByteOffset := int64(lineIndex) * int64(ss.lineByte+1) // Sửa đổi dòng này
	if lineByteOffset >= fileInfo.Size() {
		return "", fmt.Errorf("line number out of range")
	}

	_, err = file.Seek(lineByteOffset, 0)
	if err != nil {
		return "", fmt.Errorf("failed to seek to line: %w", err)
	}

	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
		return scanner.Text(), nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading line: %w", err)
	}
	return "", nil // Dòng không tồn tại
}

// saveBlock lưu block vào file shard, đảm bảo dòng cuối cùng khớp. Sử dụng updateLine để cập nhật.
func (ss *ShardStorage) SetIndexValue(blockNumber int, blockHash string) error {
	shardIndex := (blockNumber - 1) / ss.maxBlocksPerShard
	lineIndex := (blockNumber - 1) % ss.maxBlocksPerShard
	shardFile := ss.getShardFileName(shardIndex)

	// Kiểm tra nếu file shard chưa tồn tại và tạo nếu cần
	if err := ss.createShardFileIfNeeded(shardFile); err != nil {
		return fmt.Errorf("failed to create or access shard file: %w", err)
	}

	// Sử dụng updateLine để ghi block vào file
	err := ss.updateLine(shardFile, lineIndex, blockHash)
	if err != nil {
		return fmt.Errorf("failed to update line: %w", err)
	}

	return nil
}

// findBlockHashByBlockNumber tìm blockHash dựa trên blockNumber
func (ss *ShardStorage) FindValueByIndex(blockNumber int) (string, error) {
	shardIndex := (blockNumber - 1) / ss.maxBlocksPerShard
	lineIndex := (blockNumber - 1) % ss.maxBlocksPerShard
	shardFile := ss.getShardFileName(shardIndex)

	blockHash, err := ss.getLine(shardFile, lineIndex)
	if err != nil {
		return "", fmt.Errorf("error finding block hash: %w", err)
	}

	return blockHash, nil
}

// findLastShardFile tìm file shard cuối cùng trong thư mục
func (ss *ShardStorage) FindLastShardFile() (string, error) {
	files, err := os.ReadDir(ss.shardDir)
	if err != nil {
		return "", fmt.Errorf("failed to read shard directory: %w", err)
	}

	shardFiles := make([]int, 0) // Thay đổi: Lưu trữ số thứ tự shard dưới dạng số nguyên
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "shard_") && strings.HasSuffix(file.Name(), ".txt") {
			shardNumStr := strings.TrimPrefix(strings.TrimSuffix(file.Name(), ".txt"), "shard_")
			shardNum, err := strconv.Atoi(shardNumStr) // Chuyển đổi chuỗi thành số nguyên
			if err != nil {
				continue // Bỏ qua các file không hợp lệ
			}
			shardFiles = append(shardFiles, shardNum)
		}
	}

	if len(shardFiles) == 0 {
		return "", nil // Không có file shard nào
	}

	// Sắp xếp các file shard theo số thứ tự
	sort.Ints(shardFiles) // Thay đổi: Sắp xếp mảng số nguyên
	lastShardNum := shardFiles[len(shardFiles)-1]
	return filepath.Join(ss.shardDir, fmt.Sprintf("shard_%d.txt", lastShardNum)), nil // Thay đổi: Sử dụng số thứ tự để tạo tên file
}

// CountTotalKeyValuePairs đếm tổng số cặp khóa-giá trị trong tất cả các shard.
func (ss *ShardStorage) CountTotalKeyValuePairs() (int, error) {
	files, err := os.ReadDir(ss.shardDir)
	if err != nil {
		return 0, fmt.Errorf("failed to read shard directory: %w", err)
	}

	totalPairs := 0
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "shard_") && strings.HasSuffix(file.Name(), ".txt") {
			filePath := filepath.Join(ss.shardDir, file.Name())
			file, err := os.Open(filePath)
			if err != nil {
				return 0, fmt.Errorf("failed to open shard file %s: %w", filePath, err)
			}
			defer file.Close()

			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				if scanner.Text() != "" { // Kiểm tra xem dòng có rỗng hay không
					totalPairs++
				}
			}
			if err := scanner.Err(); err != nil {
				return 0, fmt.Errorf("error reading shard file %s: %w", filePath, err)
			}
		}
	}
	return totalPairs, nil
}

// GetLastValue trả về giá trị (blockHash) cuối cùng được lưu trữ trong kho lưu trữ phân mảnh.
func (ss *ShardStorage) GetLastValue() (string, error) {
	lastShardFile, err := ss.FindLastShardFile()
	if err != nil {
		return "", fmt.Errorf("failed to find last shard file: %w", err)
	}

	if lastShardFile == "" {
		return "", nil // Không có dữ liệu nào
	}

	file, err := os.Open(lastShardFile)
	if err != nil {
		return "", fmt.Errorf("failed to open last shard file: %w", err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to get file info: %w", err)
	}

	if fileInfo.Size() == 0 {
		return "", nil // File rỗng
	}

	// Tìm dòng cuối cùng
	_, err = file.Seek(int64(-(ss.lineByte + 1)), io.SeekEnd) // Di chuyển con trỏ đến trước dòng cuối cùng
	if err != nil {
		return "", fmt.Errorf("failed to seek to last line: %w", err)
	}

	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
		return scanner.Text(), nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("error reading last line: %w", err)
	}

	return "", nil // Không tìm thấy dòng cuối cùng
}

// BackupAllShardFiles sao lưu tất cả các tệp shard vào một thư mục đã chỉ định.
func (ss *ShardStorage) BackupAllShardFiles() (string, error) {
	currentTime := time.Now().Format("20060102150405")                // Định dạng YYYYMMDDHHMMSS
	backupDir := ss.shardDir + fmt.Sprintf("/backup_%s", currentTime) // Tạo thư mục sao lưu nếu chưa tồn tại.
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create backup directory: %w", err)
	}

	files, err := os.ReadDir(ss.shardDir)
	if err != nil {
		return "", fmt.Errorf("failed to read shard directory: %w", err)
	}

	for _, file := range files {
		if strings.HasPrefix(file.Name(), "shard_") && strings.HasSuffix(file.Name(), ".txt") {
			sourcePath := filepath.Join(ss.shardDir, file.Name())
			destinationPath := filepath.Join(backupDir, file.Name())
			// Sao chép tệp.
			if err := copyFile(sourcePath, destinationPath); err != nil {
				return "", fmt.Errorf("failed to copy shard file %s: %w", sourcePath, err)
			}
		}
	}
	return currentTime, nil // Trả về đường dẫn thư mục sao lưu
}

// copyFile sao chép một tệp từ nguồn đến đích.
func copyFile(source, destination string) error {
	sourceFile, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer sourceFile.Close()

	destinationFile, err := os.Create(destination)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer destinationFile.Close()

	_, err = io.Copy(destinationFile, sourceFile)
	if err != nil {
		return fmt.Errorf("failed to copy file content: %w", err)
	}

	return nil
}

// backupIndex trạng thái của một index key-value trước khi xóa
func (ss *ShardStorage) backupIndex(blockNumber int) (string, error) {
	shardIndex := (blockNumber - 1) / ss.maxBlocksPerShard
	lineIndex := (blockNumber - 1) % ss.maxBlocksPerShard
	shardFile := ss.getShardFileName(shardIndex)

	blockHash, err := ss.getLine(shardFile, lineIndex)
	if err != nil {
		return "", fmt.Errorf("error getting block hash: %w", err)
	}
	return blockHash, nil
}

// deleteUnusedShards xóa các shard không cần thiết sau khi khôi phục về blockNumber
func (ss *ShardStorage) deleteUnusedShards(blockNumber int) error {
	lastShardIndex := (blockNumber - 1) / ss.maxBlocksPerShard
	files, err := os.ReadDir(ss.shardDir)
	if err != nil {
		return fmt.Errorf("failed to read shard directory: %w", err)
	}

	for _, file := range files {
		if strings.HasPrefix(file.Name(), "shard_") && strings.HasSuffix(file.Name(), ".txt") {
			shardNumStr := strings.TrimPrefix(strings.TrimSuffix(file.Name(), ".txt"), "shard_")
			shardNum, err := strconv.Atoi(shardNumStr)
			if err != nil {
				continue
			}
			if shardNum > lastShardIndex {
				filePath := filepath.Join(ss.shardDir, file.Name())
				if err := os.Remove(filePath); err != nil {
					return fmt.Errorf("failed to remove shard file %s: %w", filePath, err)
				}
			}
		}
	}
	return nil
}

func (ss *ShardStorage) BackToBlock(blockNumber int) (string, error) {
	shardIndex := (blockNumber) / ss.maxBlocksPerShard
	lineIndex := (blockNumber) % ss.maxBlocksPerShard
	shardFile := ss.getShardFileName(shardIndex)

	backupHash, err := ss.backupIndex(blockNumber)
	if err != nil {
		return "", fmt.Errorf("failed to backup index: %w", err)
	}

	file, err := os.OpenFile(shardFile, os.O_RDWR, 0644)
	if err != nil {
		return "", fmt.Errorf("failed to open shard file: %w", err)
	}
	defer file.Close()

	// Xóa các dòng từ lineIndex đến cuối file
	fileInfo, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("failed to get file info: %w", err)
	}

	// Chỉ xóa nếu có dữ liệu cần xóa
	if int64(lineIndex*(ss.lineByte+1)) < fileInfo.Size() {
		_, err = file.Seek(int64(lineIndex*(ss.lineByte+1)), 0)
		if err != nil {
			return "", fmt.Errorf("failed to seek to line: %w", err)
		}
		err = file.Truncate(int64(lineIndex * (ss.lineByte + 1)))
		if err != nil {
			return "", fmt.Errorf("failed to truncate file: %w", err)
		}
	}
	// Xóa các shard không cần thiết sau khi truncate file.
	if err := ss.deleteUnusedShards(blockNumber); err != nil {
		return "", fmt.Errorf("failed to delete unused shards: %w", err)
	}
	return backupHash, nil
}

// RestoreFromBackup khôi phục dữ liệu từ một bản sao lưu cụ thể dựa trên số đuôi.
func (ss *ShardStorage) RestoreFromBackup(backupSuffix string) error {
	// Tìm thư mục sao lưu dựa trên số đuôi.
	backupDir, err := ss.findBackupDir(backupSuffix)
	if err != nil {
		return err
	}

	// Kiểm tra xem thư mục sao lưu tồn tại hay không.
	if _, err := os.Stat(backupDir); os.IsNotExist(err) {
		return fmt.Errorf("thư mục sao lưu không tồn tại: %w", err)
	}

	// Duyệt qua tất cả các tệp trong thư mục sao lưu.
	err = filepath.Walk(backupDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Bỏ qua thư mục hiện tại.
		if info.IsDir() && path == backupDir {
			return nil
		}

		// Tạo đường dẫn tương đối so với thư mục sao lưu.
		relativePath, err := filepath.Rel(backupDir, path)
		if err != nil {
			return err
		}

		// Chỉ xử lý các tệp shard
		if !strings.HasPrefix(relativePath, "shard_") || !strings.HasSuffix(relativePath, ".txt") {
			return nil
		}

		// Tạo đường dẫn đầy đủ trong thư mục shard.
		destinationPath := filepath.Join(ss.shardDir, relativePath)

		// Tạo thư mục cha nếu cần thiết.
		if err := os.MkdirAll(filepath.Dir(destinationPath), 0755); err != nil {
			return err
		}

		// Sao chép tệp.
		sourceFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer sourceFile.Close()

		destinationFile, err := os.Create(destinationPath)
		if err != nil {
			return err
		}
		defer destinationFile.Close()

		_, err = io.Copy(destinationFile, sourceFile)
		return err
	})

	if err != nil {
		return fmt.Errorf("lỗi khi khôi phục từ bản sao lưu: %w", err)
	}

	return nil
}

// findBackupDir tìm thư mục sao lưu dựa trên số đuôi.
func (ss *ShardStorage) findBackupDir(backupSuffix string) (string, error) {
	// Biểu thức chính quy để tìm kiếm thư mục sao lưu.
	re := regexp.MustCompile(`backup_\d{14}`)
	files, err := os.ReadDir(ss.shardDir)
	if err != nil {
		return "", fmt.Errorf("không thể đọc thư mục shard: %w", err)
	}

	for _, file := range files {
		if file.IsDir() {
			matches := re.FindStringSubmatch(file.Name())
			if len(matches) > 0 && strings.HasSuffix(matches[0], backupSuffix) {
				return filepath.Join(ss.shardDir, file.Name()), nil
			}
		}
	}

	return "", fmt.Errorf("không tìm thấy thư mục sao lưu với số đuôi '%s'", backupSuffix)
}

// listBackupIDs liệt kê tất cả các ID bản sao lưu và trả về ID cuối cùng.
func (ss *ShardStorage) ListBackupIDs() (string, []string, error) {
	files, err := os.ReadDir(ss.shardDir)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read shard directory: %w", err)
	}

	backupIDs := make([]string, 0)
	var lastBackupID string

	for _, file := range files {
		if file.IsDir() && strings.HasPrefix(file.Name(), "backup_") {
			backupID := strings.TrimPrefix(file.Name(), "backup_")
			backupIDs = append(backupIDs, backupID)
			if lastBackupID == "" || backupID > lastBackupID {
				lastBackupID = backupID
			}
		}
	}

	return lastBackupID, backupIDs, nil
}
