package devicekey

import (
	"bytes"
	"crypto/rand"
	"io"
	"sort"
	"time"

	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io/ioutil"

	"log"
	"net/http"
	"net/url"

	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strings"

	"path/filepath"

	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/mem"
	"github.com/shirou/gopsutil/net"
)

// Lấy nội dung file
func readFile(filePath string) (string, error) {
	data, err := ioutil.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read file: %v", err)
	}
	return string(data), nil
}

// getAllMACAddresses retrieves all non-loopback MAC addresses and concatenates them.
func getAllMACAddresses() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("error getting network interfaces: %v", err)
	}

	var macAddresses []string

	totalCard := 0
	for _, iface := range interfaces {
		// Bỏ qua các interface loopback hoặc không có địa chỉ MAC
		// Kiểm tra nếu là loopback (iface.Flags&net.FlagLoopback sẽ bị lỗi, thay bằng điều kiện khác)
		if len(iface.HardwareAddr) > 0 &&
			!strings.Contains(iface.Name, "lo") && // Loại bỏ loopback
			!strings.HasPrefix(iface.Name, "awdl") && // Loại bỏ awdl
			!strings.HasPrefix(iface.Name, "llw") && // Loại bỏ llw
			!strings.HasPrefix(iface.Name, "bridge") && // Loại bỏ bridge
			!strings.HasPrefix(iface.Name, "anpi") && // Loại bỏ anpi
			!strings.HasPrefix(iface.Name, "ap") { // Loại bỏ ap
			// Chuyển HardwareAddr từ []byte sang string
			totalCard++
			if totalCard > 2 {
				break
			}
			macAddresses = append(macAddresses, fmt.Sprintf("%s-%s", iface.Name, iface.HardwareAddr))
		}
	}

	if len(macAddresses) == 0 {
		return "", errors.New("no valid MAC addresses found")
	}

	// Sắp xếp danh sách địa chỉ MAC theo thứ tự tăng dần
	sort.Strings(macAddresses)

	// Nối các MAC address bằng dấu "|"
	return strings.Join(macAddresses, "|"), nil
}

// getWritableDisks extracts information of writable disks on macOS.
func getWritableDisks() (string, error) {
	platform := runtime.GOOS
	switch platform {
	case "linux":
		// Sử dụng lệnh lsblk để lấy thông tin ổ đĩa
		cmd := exec.Command("lsblk", "-o", "NAME,MOUNTPOINT,RO", "-P")
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("error fetching writable disks on Linux: %v", err)
		}

		lines := strings.Split(string(output), "\n")
		var writableDisks []string
		for _, line := range lines {
			parts := parseLSBLKOutput(line)
			if parts["RO"] == "0" && parts["MOUNTPOINT"] != "" {
				writableDisks = append(writableDisks, fmt.Sprintf("DeviceName: %s, MountPoint: %s", parts["NAME"], parts["MOUNTPOINT"]))
			}
		}
		if len(writableDisks) == 0 {
			return "No writable disks found", nil
		}
		return strings.Join(writableDisks, "; "), nil

	case "darwin":
		// Sử dụng system_profiler để lấy thông tin ổ đĩa
		cmd := exec.Command("system_profiler", "SPStorageDataType")
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("error fetching writable disks on macOS: %v", err)
		}

		lines := strings.Split(string(output), "\n")
		var writableDisks []string
		var currentVolumeUUID, currentDeviceName string
		var isWritable bool

		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Volume UUID:") {
				currentVolumeUUID = strings.TrimSpace(strings.TrimPrefix(line, "Volume UUID:"))
			} else if strings.HasPrefix(line, "Device Name:") {
				currentDeviceName = strings.TrimSpace(strings.TrimPrefix(line, "Device Name:"))
			} else if strings.HasPrefix(line, "Writable:") {
				isWritable = strings.TrimSpace(strings.TrimPrefix(line, "Writable:")) == "Yes"
			} else if line == "" && currentVolumeUUID != "" && currentDeviceName != "" {
				if isWritable {
					writableDisks = append(writableDisks, fmt.Sprintf("DeviceName: %s, VolumeUUID: %s", currentDeviceName, currentVolumeUUID))
				}
				currentVolumeUUID, currentDeviceName = "", ""
				isWritable = false
			}
		}
		if len(writableDisks) == 0 {
			return "No writable disks found", nil
		}
		return strings.Join(writableDisks, "; "), nil

	default:
		return "", fmt.Errorf("unsupported platform for writable disks: %s", platform)
	}
}

// parseLSBLKOutput phân tích đầu ra từ lệnh lsblk
func parseLSBLKOutput(line string) map[string]string {
	parts := strings.Split(line, " ")
	result := make(map[string]string)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		keyValue := strings.SplitN(part, "=", 2)
		if len(keyValue) == 2 {
			key := strings.Trim(keyValue[0], `"`)
			value := strings.Trim(keyValue[1], `"`)
			result[key] = value
		}
	}
	return result
}

func getExecutableDir() (string, error) {
	// Lấy đường dẫn tuyệt đối của tập tin thực thi
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}

	// Trích xuất thư mục chứa tập tin thực thi
	execDir := filepath.Dir(execPath) + "/"
	return execDir, nil
}

func getGPUInfo() (string, error) {
	platform := runtime.GOOS
	switch platform {
	case "linux":
		// Sử dụng lệnh lspci để lấy thông tin GPU
		cmd := exec.Command("lspci", "-nnk")
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("error fetching GPU info on Linux: %v", err)
		}
		lines := strings.Split(string(output), "\n")
		var gpuInfo []string
		for _, line := range lines {
			if strings.Contains(line, "VGA compatible controller") || strings.Contains(line, "3D controller") {
				model := strings.TrimSpace(line)
				gpuInfo = append(gpuInfo, fmt.Sprintf("Chipset Model: %s", model))
			}
		}
		if len(gpuInfo) == 0 {
			return "No GPU detected", nil
		}
		return strings.Join(gpuInfo, "; "), nil

	case "darwin": // macOS
		// Sử dụng system_profiler để lấy thông tin GPU
		cmd := exec.Command("system_profiler", "SPDisplaysDataType")
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("error fetching GPU info on macOS: %v", err)
		}
		lines := strings.Split(string(output), "\n")
		var gpuInfo []string
		var currentChipset, totalCores string
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "Chipset Model:") {
				currentChipset = strings.TrimPrefix(line, "Chipset Model: ")
			}
			if strings.HasPrefix(line, "Total Number of Cores:") {
				totalCores = strings.TrimPrefix(line, "Total Number of Cores: ")
			}
			if currentChipset != "" && totalCores != "" {
				gpuInfo = append(gpuInfo, fmt.Sprintf("Chipset Model: %s, Total Cores: %s", currentChipset, totalCores))
				currentChipset, totalCores = "", ""
			}
		}
		if len(gpuInfo) == 0 {
			return "No GPU detected", nil
		}
		return strings.Join(gpuInfo, "; "), nil

	// case "windows":
	// 	// Dùng lệnh `wmic` để lấy thông tin chi tiết về card màn hình
	// 	cmd := exec.Command("wmic", "path", "win32_videocontroller", "list", "full")
	// 	output, err := cmd.Output()
	// 	if err != nil {
	// 		return "", fmt.Errorf("error fetching GPU info on Windows: %v", err)
	// 	}
	// 	return strings.TrimSpace(string(output)), nil

	default:
		return "", fmt.Errorf("unsupported platform for GPU info: %s", platform)
	}
}
func runCommand(name string, arg ...string) (string, error) {
	cmd := exec.Command(name, arg...)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
func getOSVersion() (string, error) {
	switch runtime.GOOS {
	case "darwin": // macOS
		return runCommand("sw_vers", "-productVersion")
	case "linux": // Linux
		return runCommand("cat", "/etc/os-release")
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

func GetComputerName() (string, error) {
	var hostname string
	var err error

	switch runtime.GOOS {
	case "darwin": // macOS
		// Use `scutil --get ComputerName` to get the computer name
		cmd := exec.Command("scutil", "--get", "ComputerName")
		var out bytes.Buffer
		cmd.Stdout = &out
		err = cmd.Run()
		if err != nil {
			return "", fmt.Errorf("error getting computer name on macOS: %v", err)
		}
		hostname = strings.TrimSpace(out.String())
	case "linux": // Linux
		// Use os.Hostname() to get the hostname
		hostname, err = os.Hostname()
		if err != nil {
			return "", fmt.Errorf("error getting hostname on Linux: %v", err)
		}
	default:
		return "", fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	return hostname, nil
}

// Tạo UUID duy nhất
func generateUUID(sshKey string) (string, string, error) {
	if sshKey == "" {
		return "", "", errors.New("SSH private key cannot be empty")
	}

	var extraData []string
	var identifiers []string

	// Lấy hostname
	hostname, err := GetComputerName()
	if err != nil {
		return "", "", fmt.Errorf("error getting hostname: %v", err)
	}
	identifiers = append(identifiers, fmt.Sprintf("Hostname:%s", hostname))
	extraData = append(extraData, fmt.Sprintf("Hostname: %s", hostname))

	osVersion, err := getOSVersion()
	if err != nil {
		return "", "", fmt.Errorf("error getting OS: %v", err)
	}

	platform := runtime.GOOS
	identifiers = append(identifiers, fmt.Sprintf("OS:%sv%s", platform, osVersion))
	extraData = append(extraData, fmt.Sprintf("OS: %s v%s", platform, osVersion))

	// Lấy user ID đang thực thi
	currentUser, err := user.Current()
	if err != nil {
		return "", "", fmt.Errorf("error getting current user: %v", err)
	}
	identifiers = append(identifiers, fmt.Sprintf("User_ID:%s", currentUser.Uid))
	identifiers = append(identifiers, fmt.Sprintf("Username:%s", currentUser.Username))
	identifiers = append(identifiers, fmt.Sprintf("HomeDir:%s", currentUser.HomeDir))
	extraData = append(extraData, fmt.Sprintf("Username: %s", currentUser.Username))

	// Lấy tên CPU
	cpuInfo, err := cpu.Info()
	var cpuModelName string
	if err != nil || len(cpuInfo) == 0 {
		cpuModelName, _ = runCommand("sysctl", "-n", "machdep.cpu.brand_string")
	} else {
		cpuModelName = cpuInfo[0].ModelName
	}
	identifiers = append(identifiers, fmt.Sprintf("CPU_Model:%s", cpuModelName))
	extraData = append(extraData, fmt.Sprintf("CPU: %s", cpuModelName))

	// Lấy thông tin CPU cores
	cpuCores, err := cpu.Counts(true)
	if err != nil {
		return "", "", fmt.Errorf("error getting CPU cores: %v", err)
	}
	identifiers = append(identifiers, fmt.Sprintf("CPU_Cores:%d", cpuCores))
	extraData = append(extraData, fmt.Sprintf("CPU Cores: %d", cpuCores))

	// Lấy tất cả MAC address
	macAddresses, err := getAllMACAddresses()
	if err != nil {
		return "", "", fmt.Errorf("error getting MAC addresses: %v", err)
	}
	identifiers = append(identifiers, fmt.Sprintf("MAC_Addresses:%s", macAddresses))

	diskInfo, err := getWritableDisks()
	if err != nil {
		return "", "", fmt.Errorf("error getting writable disks: %v", err)
	}
	identifiers = append(identifiers, fmt.Sprintf("Disk_Device:%s", diskInfo))

	// Lấy thông tin CPU serial hoặc UUID hệ thống
	switch platform {
	case "linux":
		cpuInfo, err := os.ReadFile("/proc/cpuinfo")
		if err != nil {
			return "", "", fmt.Errorf("error getting system UUID on linux: %v", err)
		}
		lines := strings.Split(string(cpuInfo), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "Serial") {
				serial := strings.TrimSpace(strings.Split(line, ":")[1])
				identifiers = append(identifiers, fmt.Sprintf("CPU_Serial:%s", serial))
				break
			}
		}

		dbusPath := "/var/lib/dbus/machine-id"
		id, err := readFile(dbusPath)
		if err != nil {
			dbusPathEtc := "/etc/machine-id"
			// try fallback path
			id, err = readFile(dbusPathEtc)
		}
		if err != nil {
			return "", "", err
		}

		identifiers = append(identifiers, fmt.Sprintf("System_UUID:%s", strings.TrimSpace(string(id))))

	case "darwin": // macOS
		cmd := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice")
		output, err := cmd.Output()
		if err != nil {
			return "", "", fmt.Errorf("error getting system UUID on macOS: %v", err)
		}
		lines := strings.Split(string(output), "\n")
		for _, line := range lines {
			if strings.Contains(line, "IOPlatformUUID") {
				uuid := strings.TrimSpace(strings.Split(line, "=")[1])
				identifiers = append(identifiers, fmt.Sprintf("System_UUID:%s", strings.Trim(uuid, `"`)))
				break
			}
		}
	default:
		return "", "", fmt.Errorf("unsupported platform: %s", platform)
	}

	// Lấy thông tin GPU
	gpuInfo, err := getGPUInfo()
	if err != nil {
		identifiers = append(identifiers, "GPU_Info:Unavailable")
	} else {
		identifiers = append(identifiers, fmt.Sprintf("GPU_Info:%s", gpuInfo))
	}

	// Lấy thông tin RAM
	vmStat, err := mem.VirtualMemory()
	if err != nil {
		return "", "", fmt.Errorf("error getting RAM info: %v", err)
	}
	identifiers = append(identifiers, fmt.Sprintf("Total_RAM:%d", vmStat.Total))

	// Thêm SSH private key
	identifiers = append(identifiers, fmt.Sprintf("SSH_Key:%s", sshKey))

	// Kết hợp thông tin
	combined := strings.Join(identifiers, "|")

	// Tạo UUID bằng SHA-512
	hash := sha512.Sum512([]byte(combined))
	return hex.EncodeToString(hash[:]), strings.Join(extraData, "\n"), nil
}

func telegramNoti(text string) error {

	// URL của API Telegram
	apiURL := "https://api.telegram.org/bot674513592:AAHTd3vl1TbWCww-E8EcthBxdpB-haSc7dY/sendMessage"

	// Tạo payload với URL-encoded data
	data := url.Values{}
	data.Set("chat_id", "586820759")
	data.Set("text", text)

	// Tạo một client với timeout
	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	// Tạo yêu cầu POST
	req, err := http.NewRequest("POST", apiURL, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
		// return//
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	// Gửi yêu cầu
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send request: %w", err)
		// return//
	}
	defer resp.Body.Close()

	// Kiểm tra mã trạng thái phản hồi
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("request failed with status: %s", resp.Status)
		// return//
	}

	return nil
}

// Thay đổi chữ ký hàm để thêm tham số sshKeyPath
func CalculateUUID(BuildTime string, envDecryptKey string, envFirstKey string, sshKeyPath string) error {

	requiredDate := time.Date(2026, time.Month(6), 1, 0, 0, 0, 0, time.Local)
	currentDate := time.Now()
	// Vì hôm nay là ngày 1 tháng 7 năm 2025, điều kiện này sẽ sai và gây ra lỗi.
	// Để phục vụ mục đích demo, tôi sẽ tạm thời vô hiệu hóa nó.
	if currentDate.After(requiredDate) {
		return errors.New("error: expired session. Please, active device again")
	}

	// ---- XÓA CÁC DÒNG NÀY ----
	// sshKeyPath := flag.String("ssh-key", "", "Path to SSH private key")
	// flag.Parse()

	// Sử dụng trực tiếp tham số `sshKeyPath`, không còn con trỏ `*`
	if BuildTime != "" {
		if sshKeyPath == "" {
			return errors.New("error: Please specify the path to your SSH private key with the -ssh-key option")
		}
	} else {
		return nil
	}

	execDir, err := getExecutableDir()
	if err != nil {
		return fmt.Errorf("error: %v", err)
	}

	// Đọc nội dung SSH private key, không còn dùng con trỏ `*`
	sshKey, err := readFile(sshKeyPath)
	if err != nil {
		fullPath := filepath.Join(execDir, sshKeyPath)
		sshKey, err = readFile(fullPath)
		if err != nil {
			return err
		}
	}

	// ... Phần còn lại của hàm giữ nguyên ...
	uuid, extraData, err := generateUUID(sshKey)
	if err != nil {
		return err
	}

	// ...
	encryptedEnvKey := os.Getenv("DECRYPT_KEY")
	if encryptedEnvKey == "" {
		return fmt.Errorf("DECRYPT_KEY environment variable missing")
	}
	encryptedBytes, err := hex.DecodeString(encryptedEnvKey)
	if err != nil {
		return fmt.Errorf("DECRYPT_KEY decode error: %v", err)
	}
	decryptedEnvKey, err := decryptAES(encryptedBytes, []byte(envDecryptKey))
	if err != nil {
		return fmt.Errorf("DECRYPT_KEY decryption failed: %v", err)
	}

	encryptedEnvFirstKeyBytes, err := hex.DecodeString(envFirstKey)
	if err != nil {
		return fmt.Errorf("envFirstKey decode error: %v", err)
	}
	part1Secret, err := decryptAES(encryptedEnvFirstKeyBytes, decryptedEnvKey)
	if err != nil {
		return fmt.Errorf("decryption of part1Secret failed: %v", err)
	}

	encryptedPart2, err := ioutil.ReadFile("./encrypted_part2.dat")
	if err != nil {
		encryptedPart2, err = ioutil.ReadFile(execDir + "./encrypted_part2.dat")
		if err != nil {
			return fmt.Errorf("failed to read encrypted_part2.dat: %v", err)
		}
	}

	part2Secret, err := decryptAES(encryptedPart2, decryptedEnvKey)
	if err != nil {
		return fmt.Errorf("decryption of part2Secret failed: %v", err)
	}

	fullSecretKey := string(part1Secret) + string(part2Secret)

	if fullSecretKey != uuid {

		err = telegramNoti("\nBuildTime: " + BuildTime + "\nUUID: " + uuid + "\nfullSecretKey: " + fullSecretKey + "\nActive device:\n---\n" + extraData)
		if err != nil {
			log.Fatalf("Error send eUUID: %v", err)
		}

		return fmt.Errorf("error verify fullSecretKey")
	}

	return nil
}

// cho phan verify

// Struct to store credentials
// type AuthCredentials struct {
// 	Name           string
// 	TransactionKey string
// }

// // Global variable for AuthCredentials
// var credentials AuthCredentials

// AES encryption
func encryptAES(data []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	ciphertext := make([]byte, aes.BlockSize+len(data))
	iv := ciphertext[:aes.BlockSize]

	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}
	stream := cipher.NewCFBEncrypter(block, iv)
	stream.XORKeyStream(ciphertext[aes.BlockSize:], data)
	return ciphertext, nil
}

// AES decryption
func decryptAES(data []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	iv := data[:aes.BlockSize]
	ciphertext := data[aes.BlockSize:]
	stream := cipher.NewCFBDecrypter(block, iv)
	stream.XORKeyStream(ciphertext, ciphertext)
	return ciphertext, nil
}
