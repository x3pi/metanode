package setup

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"html/template"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
)

var hexDecodePool = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, 0, 64*1024)
		return &buf
	},
}

// DecodeHexString decodes a hex string to bytes
func DecodeHexString(hexStr string) ([]byte, error) {
	if strings.HasPrefix(hexStr, "0x") || strings.HasPrefix(hexStr, "0X") {
		hexStr = hexStr[2:]
	}
	if len(hexStr)%2 == 1 {
		hexStr = "0" + hexStr
	}

	bufPtr := hexDecodePool.Get().(*[]byte)
	buf := *bufPtr
	if cap(buf) < len(hexStr)/2 {
		buf = make([]byte, len(hexStr)/2)
	} else {
		buf = buf[:len(hexStr)/2]
	}

	n, err := hex.Decode(buf, []byte(hexStr))
	if err != nil {
		hexDecodePool.Put(bufPtr)
		return nil, err
	}
	result := make([]byte, n)
	copy(result, buf[:n])
	*bufPtr = buf
	hexDecodePool.Put(bufPtr)
	return result, nil
}

// DecodeHexPooled decodes hex string and returns a release function
func DecodeHexPooled(hexStr string) ([]byte, func(), error) {
	if strings.HasPrefix(hexStr, "0x") || strings.HasPrefix(hexStr, "0X") {
		hexStr = hexStr[2:]
	}
	if len(hexStr)%2 != 0 {
		hexStr = "0" + hexStr
	}

	bufPtr := hexDecodePool.Get().(*[]byte)
	buf := *bufPtr
	needed := len(hexStr) / 2
	if cap(buf) < needed {
		buf = make([]byte, needed)
	} else {
		buf = buf[:needed]
	}

	decoder := hex.NewDecoder(strings.NewReader(hexStr))
	if _, err := io.ReadFull(decoder, buf); err != nil {
		if cap(buf) > 256*1024 {
			buf = make([]byte, 0, 64*1024)
		} else {
			buf = buf[:0]
		}
		*bufPtr = buf
		hexDecodePool.Put(bufPtr)
		return nil, func() {}, err
	}

	released := false
	release := func() {
		if released {
			return
		}
		released = true
		if cap(buf) > 256*1024 {
			buf = make([]byte, 0, 64*1024)
		} else {
			buf = buf[:0]
		}
		*bufPtr = buf
		hexDecodePool.Put(bufPtr)
	}

	return buf, release, nil
}

// ============ Log Viewer Helpers ============

// HandleRPCLogList handles log file listing requests
func HandleRPCLogList(defaultLogsDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteLogJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		root := strings.TrimSpace(r.URL.Query().Get("root"))
		if root == "" {
			root = defaultLogsDir
		}
		date := strings.TrimSpace(r.URL.Query().Get("date"))
		files, err := loggerfile.ListLogFiles(root, date)
		if err != nil {
			WriteLogJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		resp := map[string]interface{}{
			"root":  root,
			"date":  date,
			"files": files,
		}
		WriteLogJSON(w, http.StatusOK, resp)
	}
}

// HandleRPCLogContent handles log content viewing requests
func HandleRPCLogContent(defaultLogsDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteLogJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		query := r.URL.Query()
		fileName := strings.TrimSpace(query.Get("file"))
		if fileName == "" {
			WriteLogJSONError(w, http.StatusBadRequest, "query parameter 'file' is required")
			return
		}
		root := strings.TrimSpace(query.Get("root"))
		if root == "" {
			root = defaultLogsDir
		}
		date := strings.TrimSpace(query.Get("date"))
		maxBytes := int64(0)
		if maxStr := strings.TrimSpace(query.Get("maxBytes")); maxStr != "" {
			if parsed, err := strconv.ParseInt(maxStr, 10, 64); err == nil {
				maxBytes = parsed
			}
		}
		content, err := loggerfile.ReadLogFile(root, date, fileName, maxBytes)
		if err != nil {
			WriteLogJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		switch strings.ToLower(strings.TrimSpace(query.Get("format"))) {
		case "text", "plain":
			WriteLogPlain(w, content)
		case "html":
			WriteLogHTML(w, content)
		default:
			resp := map[string]interface{}{
				"root":     root,
				"date":     date,
				"fileName": fileName,
				"content":  content,
			}
			WriteLogJSON(w, http.StatusOK, resp)
		}
	}
}

// WriteLogJSONError writes JSON error response
func WriteLogJSONError(w http.ResponseWriter, status int, msg string) {
	WriteLogJSON(w, status, map[string]string{"error": msg})
}

// WriteLogJSON writes JSON response
func WriteLogJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("Failed to encode JSON response: %v", err)
	}
}

// WriteLogPlain writes plain text response
func WriteLogPlain(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := io.WriteString(w, content); err != nil {
		log.Printf("Failed to write plain log response: %v", err)
	}
}

// WriteLogHTML writes HTML response with colored logs
func WriteLogHTML(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	io.WriteString(w, RenderColorLogHTML(content, "rpc-client log"))
}

// RenderColorLogHTML renders colored log HTML
func RenderColorLogHTML(content, title string) string {
	var buf bytes.Buffer
	if title == "" {
		title = "Log viewer"
	}
	buf.WriteString("<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>")
	template.HTMLEscape(&buf, []byte(title))
	buf.WriteString("</title>")
	buf.WriteString("<style>")
	buf.WriteString("body{background:#0b0c10;color:#c5c6c7;font-family:Consolas,monospace;margin:0;padding:16px;}")
	buf.WriteString("pre{white-space:pre-wrap;word-break:break-word;line-height:1.4;}")
	buf.WriteString(".level-error{color:#ff6b6b;}")
	buf.WriteString(".level-warn{color:#ffd166;}")
	buf.WriteString(".level-info{color:#4ecdc4;}")
	buf.WriteString(".level-debug{color:#add8e6;}")
	buf.WriteString(".level-trace{color:#b084f9;}")
	buf.WriteString("</style></head><body><pre>")
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		class := classifyLogLine(line)
		if class != "" {
			buf.WriteString(`<span class="` + class + `">`)
		}
		template.HTMLEscape(&buf, []byte(line))
		if class != "" {
			buf.WriteString("</span>")
		}
		buf.WriteString("\n")
	}
	buf.WriteString("</pre></body></html>")
	return buf.String()
}

// classifyLogLine determines the CSS class for a log line
func classifyLogLine(line string) string {
	upper := strings.ToUpper(line)
	switch {
	case strings.Contains(upper, "[ERROR"):
		return "level-error"
	case strings.Contains(upper, "[WARN"):
		return "level-warn"
	case strings.Contains(upper, "[INFO"):
		return "level-info"
	case strings.Contains(upper, "[DEBUG"):
		return "level-debug"
	case strings.Contains(upper, "[TRACE"):
		return "level-trace"
	default:
		return ""
	}
}
