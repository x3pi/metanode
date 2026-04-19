package snapshot

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// Restore khôi phục một thư mục từ một tệp tin tar.gz.
func Restore(sourceFile, destDir string) error {
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

		// Tạo thư mục nếu cần thiết.
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(filepath.Join(destDir, header.Name), 0755); err != nil {
				return err
			}
			continue
		}

		// Tạo tệp tin và ghi nội dung vào đó.
		path := filepath.Join(destDir, header.Name)
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, os.FileMode(header.Mode))
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err := io.Copy(file, tr); err != nil {
			return err
		}

		// Thiết lập thời gian sửa đổi cuối cùng cho tệp tin.
		if err := os.Chtimes(path, header.ModTime, header.ModTime); err != nil {
			return err
		}
	}

	return nil
}
