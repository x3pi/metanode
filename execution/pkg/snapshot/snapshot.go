package snapshot

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func Backup(sourceDir, destDir, fileName string) error {
	// Kiểm tra xem thư mục đích có tồn tại không, nếu không thì tạo mới.
	if _, err := os.Stat(destDir); os.IsNotExist(err) {
		if err := os.MkdirAll(destDir, 0755); err != nil {
			return fmt.Errorf("không thể tạo thư mục đích: %w", err)
		}
	}

	// Tạo đường dẫn đầy đủ cho tệp tin đích.
	destFile := filepath.Join(destDir, fileName)

	// Mở tệp tin đích để ghi.
	dest, err := os.Create(destFile)
	if err != nil {
		return fmt.Errorf("không thể tạo tệp tin đích: %w", err)
	}
	defer dest.Close()

	// Tạo một luồng gzip.
	gz := gzip.NewWriter(dest)
	defer gz.Close()

	// Tạo một luồng tar.
	tw := tar.NewWriter(gz)
	defer tw.Close()

	// Duyệt qua thư mục nguồn.
	err = filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Tạo một header cho tệp tin hoặc thư mục.
		header, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}

		// Sửa đường dẫn trong header để giữ cấu trúc thư mục gốc.
		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		header.Name = relPath
		header.ModTime = info.ModTime()

		// Ghi header vào luồng tar.
		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		// Nếu là tệp tin, ghi nội dung vào luồng tar.
		if !info.IsDir() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			if _, err := io.Copy(tw, file); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("lỗi khi duyệt thư mục nguồn: %w", err)
	}

	return nil
}

const maxSnapshotTarFileBytes = 2 * 1024 * 1024 * 1024 // 2 GiB per extracted file

// Restore khôi phục một thư mục từ một tệp tin tar.gz.
func Restore(sourceFile, destDir string) error {
	cleanDest, err := filepath.Abs(destDir)
	if err != nil {
		return fmt.Errorf("không thể chuẩn hóa thư mục đích: %w", err)
	}
	if err := os.MkdirAll(cleanDest, 0750); err != nil {
		return fmt.Errorf("không thể tạo thư mục đích: %w", err)
	}

	// Mở tệp tin nguồn để đọc.
	source, err := os.Open(sourceFile)
	if err != nil {
		return fmt.Errorf("không thể mở tệp tin nguồn: %w", err)
	}
	defer source.Close()

	// Tạo một luồng gzip.
	gz, err := gzip.NewReader(source)
	if err != nil {
		return fmt.Errorf("không thể tạo luồng gzip: %w", err)
	}
	defer gz.Close()

	// Tạo một luồng tar.
	tr := tar.NewReader(gz)

	// Duyệt qua các tệp tin và thư mục trong tệp tin tar.gz.
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("lỗi khi đọc tệp tin tar: %w", err)
		}

		if header.Name == "" || filepath.IsAbs(header.Name) || strings.Contains(header.Name, "..") {
			return fmt.Errorf("đường dẫn tar không hợp lệ (path traversal): %s", header.Name)
		}
		path := filepath.Clean(filepath.Join(cleanDest, header.Name))
		if path != cleanDest && !strings.HasPrefix(path, cleanDest+string(os.PathSeparator)) {
			return fmt.Errorf("đường dẫn tar vượt ngoài thư mục đích: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0750); err != nil {
				return err
			}
			continue
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxSnapshotTarFileBytes {
				return fmt.Errorf("kích thước tệp tin tar vượt quá giới hạn: %s (%d bytes)", header.Name, header.Size)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
				return err
			}
			safeMode := os.FileMode(header.Mode) & 0755
			if safeMode == 0 {
				safeMode = 0600
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_TRUNC, safeMode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, io.LimitReader(tr, maxSnapshotTarFileBytes))
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if err := os.Chtimes(path, header.ModTime, header.ModTime); err != nil {
				return err
			}
		default:
			return fmt.Errorf("loại tệp tin tar không được phép (type=%d): %s", header.Typeflag, header.Name)
		}
	}

	return nil
}
