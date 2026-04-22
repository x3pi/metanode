// pathdetector/detector.go
package pathdetector

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
)

// PathType định nghĩa kiểu dữ liệu cho loại đường dẫn.
type PathType string

const (
	// URL là loại dành cho các liên kết web.
	URL PathType = "URL"
	// FilePath là loại dành cho đường dẫn tệp trên ổ cứng.
	FilePath PathType = "File Path"
	// Unknown là loại không xác định.
	Unknown PathType = "Unknown"
)

// isWindowsPath kiểm tra xem một chuỗi có phải là đường dẫn kiểu Windows hay không.
// Nó nhận diện các đường dẫn như "C:\Users\Test" hoặc "\\server\share".
func isWindowsPath(path string) bool {
	// Sử dụng regular expression để kiểm tra ký tự ổ đĩa (ví dụ: C:)
	// hoặc đường dẫn UNC (ví dụ: \\server)
	match, _ := regexp.MatchString(`^[a-zA-Z]:\\`, path)
	isUnc, _ := regexp.MatchString(`^\\\\[^\\]`, path)
	return match || isUnc
}

// DetectPathType phân tích một chuỗi và xác định nó là URL, File Path hay Unknown.
func DetectPathType(input string) PathType {
	// 1. Kiểm tra xem có phải là URL không
	// Cố gắng phân tích chuỗi như một URL.
	// URL cần có scheme (http, https, ftp, ...) và host.
	parsedURL, err := url.Parse(input)
	if err == nil && parsedURL.Scheme != "" && parsedURL.Host != "" {
		// Kiểm tra các scheme phổ biến
		switch parsedURL.Scheme {
		case "http", "https", "ftp", "sftp", "file":
			return URL
		}
	}

	// 2. Kiểm tra địa chỉ IP:port (ví dụ: 0.0.0.0:8881)
	// Regex để nhận diện địa chỉ IP:port
	ipPortRegex := regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}:\d+$`)
	if ipPortRegex.MatchString(input) {
		return URL
	}

	// 3. Kiểm tra xem có phải là File Path không
	// Kiểm tra đường dẫn tuyệt đối cho Unix-like systems (bắt đầu bằng "/")
	if strings.HasPrefix(input, "/") {
		return FilePath
	}

	// Kiểm tra đường dẫn tuyệt đối cho Windows
	if isWindowsPath(input) {
		return FilePath
	}

	// Sử dụng filepath.IsAbs để kiểm tra đường dẫn tuyệt đối theo OS hiện tại
	if filepath.IsAbs(input) {
		return FilePath
	}

	// Kiểm tra các dấu hiệu của đường dẫn tương đối
	if strings.HasPrefix(input, "./") || strings.HasPrefix(input, "../") || strings.HasPrefix(input, `.\`) || strings.HasPrefix(input, `..\`) {
		return FilePath
	}

	// 4. Nếu không phải cả hai, trả về Unknown
	return Unknown
}
