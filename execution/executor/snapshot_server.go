package executor

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

// SnapshotServer phục vụ tải snapshot qua HTTP
type SnapshotServer struct {
	snapshotDir string
	port        int
	bindAddr    string
	manager     *SnapshotManager
}

// NewSnapshotServer tạo instance mới
func NewSnapshotServer(snapshotDir string, port int, bindAddr string, manager *SnapshotManager) *SnapshotServer {
	if port <= 0 {
		port = 8700
	}
	if bindAddr == "" {
		bindAddr = "0.0.0.0"
	}
	return &SnapshotServer{
		snapshotDir: snapshotDir,
		port:        port,
		bindAddr:    bindAddr,
		manager:     manager,
	}
}

// Start khởi động HTTP server
func (ss *SnapshotServer) Start() error {
	mux := http.NewServeMux()

	// Index page — liệt kê snapshots
	mux.HandleFunc("/", ss.handleIndex)

	// API endpoint — trả về JSON metadata
	mux.HandleFunc("/api/snapshots", ss.handleAPISnapshots)

	// Phục vụ file tĩnh từ thư mục snapshot
	// http.FileServer tự động hỗ trợ:
	// - Range requests (resume download)
	// - Content-Type detection
	// - Directory listing
	// - If-Modified-Since caching
	fileServer := http.FileServer(http.Dir(ss.snapshotDir))
	mux.Handle("/files/", http.StripPrefix("/files/", fileServer))

	addr := fmt.Sprintf("%s:%d", ss.bindAddr, ss.port)

	logger.Info("📥 [SNAPSHOT SERVER] Starting HTTP server...")
	logger.Info("╔═══════════════════════════════════════════════════════════╗")
	logger.Info("║       SNAPSHOT DOWNLOAD SERVER (Go)                      ║")
	logger.Info("╚═══════════════════════════════════════════════════════════╝")
	logger.Info("📂 Serving snapshots from: %s", ss.snapshotDir)
	logger.Info("🌐 Server: http://%s", addr)
	logger.Info("📋 Index page: http://%s/", addr)
	logger.Info("📦 Download:   http://%s/files/<snapshot_name>/", addr)
	logger.Info("📡 API:        http://%s/api/snapshots", addr)
	logger.Info("")
	logger.Info("💡 Features:")
	logger.Info("   ✅ HTTP Range requests (resume on failure)")
	logger.Info("   ✅ Streaming (no memory limit)")
	logger.Info("   ✅ Concurrent connections")
	logger.Info("")
	logger.Info("📥 Download examples:")
	logger.Info("   wget -c -r -np http://%s/files/snap_epoch_1_block_100/", addr)
	logger.Info("   aria2c -x 16 -s 16 -c http://%s/files/snap_epoch_1_block_100/blocks/000001.ldb", addr)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// Không set ReadTimeout/WriteTimeout vì cần hỗ trợ tải file lớn
	}

	return server.ListenAndServe()
}

// handleIndex hiển thị trang index HTML
func (ss *SnapshotServer) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	snapshots, err := ss.manager.ListSnapshots()
	if err != nil {
		http.Error(w, "Failed to list snapshots", http.StatusInternalServerError)
		return
	}

	// Tính total size cho mỗi snapshot
	type SnapshotInfo struct {
		SnapshotMetadata
		TotalSize    int64
		TotalSizeStr string
		FileCount    int
	}

	var snapshotInfos []SnapshotInfo
	for _, snap := range snapshots {
		snapPath := filepath.Join(ss.snapshotDir, snap.SnapshotName)
		totalSize, fileCount := getDirSizeAndCount(snapPath)
		snapshotInfos = append(snapshotInfos, SnapshotInfo{
			SnapshotMetadata: snap,
			TotalSize:        totalSize,
			TotalSizeStr:     humanReadableSize(totalSize),
			FileCount:        fileCount,
		})
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	tmpl := template.Must(template.New("index").Parse(indexHTML))
	tmpl.Execute(w, struct {
		Snapshots  []SnapshotInfo
		ServerAddr string
	}{
		Snapshots:  snapshotInfos,
		ServerAddr: r.Host,
	})
}

// handleAPISnapshots trả về JSON metadata
func (ss *SnapshotServer) handleAPISnapshots(w http.ResponseWriter, r *http.Request) {
	snapshots, err := ss.manager.ListSnapshots()
	if err != nil {
		http.Error(w, `{"error":"Failed to list snapshots"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshots)
}

// StartSnapshotServer khởi động snapshot HTTP server trong goroutine riêng
func StartSnapshotServer(snapshotDir string, port int, manager *SnapshotManager) {
	server := NewSnapshotServer(snapshotDir, port, "0.0.0.0", manager)
	go func() {
		if err := server.Start(); err != nil {
			logger.Error("📥 [SNAPSHOT SERVER] HTTP server error: %v", err)
		}
	}()
}

// ============================================================================
// Helper functions
// ============================================================================

func getDirSizeAndCount(path string) (int64, int) {
	var totalSize int64
	var fileCount int
	filepath.Walk(path, func(_ string, info os.FileInfo, _ error) error {
		if info != nil && !info.IsDir() {
			totalSize += info.Size()
			fileCount++
		}
		return nil
	})
	return totalSize, fileCount
}

func humanReadableSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// ============================================================================
// HTML Template
// ============================================================================

const indexHTML = `<!DOCTYPE html>
<html>
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Blockchain Snapshot Server</title>
    <link rel="preconnect" href="https://fonts.googleapis.com">
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
    <style>
        :root {
            --bg-primary: #0f0f1a;
            --bg-card: #161625;
            --bg-code: #1e1e32;
            --border: #2a2a45;
            --accent: #00d4ff;
            --accent-glow: rgba(0, 212, 255, 0.15);
            --red: #e94560;
            --red-hover: #ff6b81;
            --green: #00e676;
            --text: #e0e0ee;
            --text-dim: #8888aa;
            --radius: 12px;
        }
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: 'Inter', 'Segoe UI', sans-serif;
            background: var(--bg-primary);
            color: var(--text);
            padding: 32px;
            max-width: 1100px;
            margin: 0 auto;
            line-height: 1.6;
        }
        .header {
            display: flex;
            align-items: center;
            gap: 16px;
            border-bottom: 2px solid var(--accent);
            padding-bottom: 16px;
            margin-bottom: 32px;
        }
        .header h1 {
            font-size: 28px;
            font-weight: 700;
            color: var(--accent);
            letter-spacing: -0.5px;
        }
        .badge {
            background: var(--accent-glow);
            color: var(--accent);
            font-size: 12px;
            font-weight: 600;
            padding: 4px 10px;
            border-radius: 20px;
            border: 1px solid rgba(0,212,255,0.3);
        }

        /* Snapshot Card */
        .snapshot {
            background: var(--bg-card);
            border: 1px solid var(--border);
            border-radius: var(--radius);
            padding: 24px;
            margin-bottom: 20px;
            transition: border-color 0.2s, box-shadow 0.2s;
        }
        .snapshot:hover {
            border-color: var(--accent);
            box-shadow: 0 0 20px var(--accent-glow);
        }
        .snap-header {
            display: flex;
            align-items: center;
            justify-content: space-between;
            flex-wrap: wrap;
            gap: 12px;
            margin-bottom: 12px;
        }
        .snap-header h2 {
            font-size: 20px;
            font-weight: 600;
            color: var(--red);
        }
        .snap-size {
            font-size: 14px;
            font-weight: 500;
            color: var(--green);
            background: rgba(0,230,118,0.1);
            padding: 4px 12px;
            border-radius: 20px;
        }
        .meta-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
            gap: 8px 20px;
            margin-bottom: 16px;
        }
        .meta-item {
            font-size: 13px;
            color: var(--text-dim);
        }
        .meta-item strong {
            color: var(--accent);
            font-weight: 500;
        }
        .btn-row {
            display: flex;
            flex-wrap: wrap;
            gap: 8px;
            margin-bottom: 4px;
        }
        .btn {
            display: inline-flex;
            align-items: center;
            gap: 6px;
            padding: 8px 16px;
            border-radius: 8px;
            text-decoration: none;
            font-size: 13px;
            font-weight: 500;
            cursor: pointer;
            border: none;
            transition: all 0.15s;
        }
        .btn-primary {
            background: var(--red);
            color: white;
        }
        .btn-primary:hover { background: var(--red-hover); }
        .btn-outline {
            background: transparent;
            color: var(--accent);
            border: 1px solid var(--border);
        }
        .btn-outline:hover {
            border-color: var(--accent);
            background: var(--accent-glow);
        }

        /* Download Instructions (per-snapshot collapsible) */
        .dl-toggle {
            width: 100%;
            text-align: left;
            background: rgba(0,212,255,0.06);
            border: 1px solid var(--border);
            color: var(--accent);
            padding: 10px 16px;
            border-radius: 8px;
            font-size: 14px;
            font-weight: 500;
            cursor: pointer;
            margin-top: 12px;
            transition: all 0.15s;
            font-family: 'Inter', sans-serif;
        }
        .dl-toggle:hover {
            background: var(--accent-glow);
            border-color: var(--accent);
        }
        .dl-toggle .arrow { transition: transform 0.2s; display: inline-block; }
        .dl-toggle.open .arrow { transform: rotate(90deg); }
        .dl-section {
            display: none;
            margin-top: 12px;
            padding: 16px;
            background: var(--bg-code);
            border: 1px solid var(--border);
            border-radius: 8px;
        }
        .dl-section.open { display: block; animation: fadeIn 0.2s; }
        @keyframes fadeIn { from { opacity: 0; transform: translateY(-4px); } to { opacity: 1; transform: translateY(0); } }

        .cmd-group { margin-bottom: 14px; }
        .cmd-group:last-child { margin-bottom: 0; }
        .cmd-label {
            font-size: 12px;
            font-weight: 600;
            color: var(--accent);
            margin-bottom: 6px;
            text-transform: uppercase;
            letter-spacing: 0.5px;
        }
        .cmd-desc {
            font-size: 12px;
            color: var(--text-dim);
            margin-bottom: 4px;
        }
        .cmd-box {
            display: flex;
            align-items: stretch;
            background: var(--bg-primary);
            border: 1px solid var(--border);
            border-radius: 6px;
            overflow: hidden;
        }
        .cmd-box code {
            flex: 1;
            padding: 10px 14px;
            font-family: 'JetBrains Mono', monospace;
            font-size: 12.5px;
            color: var(--text);
            background: transparent;
            white-space: pre-wrap;
            word-break: break-all;
            line-height: 1.5;
            border: none;
            border-radius: 0;
        }
        .copy-btn {
            display: flex;
            align-items: center;
            justify-content: center;
            width: 44px;
            min-width: 44px;
            background: rgba(0,212,255,0.08);
            border: none;
            border-left: 1px solid var(--border);
            color: var(--text-dim);
            cursor: pointer;
            font-size: 15px;
            transition: all 0.15s;
        }
        .copy-btn:hover { background: var(--accent-glow); color: var(--accent); }
        .copy-btn.copied { color: var(--green) !important; }

        .divider {
            border: none;
            border-top: 1px solid var(--border);
            margin: 12px 0;
        }

        /* Footer */
        .api-section {
            background: var(--bg-card);
            border: 1px solid var(--border);
            border-radius: var(--radius);
            padding: 20px 24px;
            margin-top: 28px;
        }
        .api-section h3 {
            color: var(--accent);
            font-size: 16px;
            margin-bottom: 12px;
        }

        .empty {
            color: var(--text-dim);
            font-style: italic;
            padding: 40px;
            text-align: center;
            font-size: 15px;
        }
    </style>
</head>
<body>
    <div class="header">
        <h1>📸 Blockchain Snapshot Server</h1>
        {{if .Snapshots}}<span class="badge">{{len .Snapshots}} snapshot{{if gt (len .Snapshots) 1}}s{{end}}</span>{{end}}
    </div>

    {{if .Snapshots}}
    {{range .Snapshots}}
    <div class="snapshot">
        <div class="snap-header">
            <h2>📦 {{.SnapshotName}}</h2>
            <span class="snap-size">{{.TotalSizeStr}} · {{.FileCount}} files</span>
        </div>

        <div class="meta-grid">
            <div class="meta-item"><strong>Epoch</strong> {{.Epoch}}</div>
            <div class="meta-item"><strong>Block</strong> {{.BlockNumber}}</div>
            <div class="meta-item"><strong>Boundary</strong> {{.BoundaryBlock}}</div>
            <div class="meta-item"><strong>Created</strong> {{.CreatedAt}}</div>
        </div>

        <div class="btn-row">
            <a class="btn btn-primary" href="/files/{{.SnapshotName}}/">📂 Browse Files</a>
            <a class="btn btn-outline" href="/files/{{.SnapshotName}}/metadata.json">📋 Metadata</a>
        </div>

        <button class="dl-toggle" onclick="toggleDL(this)">
            <span class="arrow">▶</span> 📥 Download Instructions — copy &amp; paste
        </button>
        <div class="dl-section">
            <div class="cmd-group">
                <div class="cmd-label">📦 wget — tải toàn bộ snapshot (recursive, resume)</div>
                <div class="cmd-desc">Tải tất cả files, tự resume nếu bị ngắt</div>
                <div class="cmd-box">
                    <code>wget -c -r -np -nH --cut-dirs=2 http://{{$.ServerAddr}}/files/{{.SnapshotName}}/ -P ./restored_data</code>
                    <button class="copy-btn" onclick="copyCmd(this)" title="Copy">📋</button>
                </div>
            </div>

            <hr class="divider">

            <div class="cmd-group">
                <div class="cmd-label">🔄 rsync — đồng bộ nhanh (nếu có SSH)</div>
                <div class="cmd-desc">Nhanh hơn wget cho lần tải lại, chỉ sync file thay đổi</div>
                <div class="cmd-box">
                    <code>rsync -avz --progress USER@SERVER_IP:SNAPSHOT_DIR/{{.SnapshotName}}/ ./restored_data/</code>
                    <button class="copy-btn" onclick="copyCmd(this)" title="Copy">📋</button>
                </div>
            </div>

            <hr class="divider">

            <div class="cmd-group">
                <div class="cmd-label">⚡ aria2c — tải đa luồng (nhanh nhất)</div>
                <div class="cmd-desc">16 kết nối song song, phù hợp file lớn</div>
                <div class="cmd-box">
                    <code>aria2c -x 16 -s 16 -c -d ./restored_data http://{{$.ServerAddr}}/files/{{.SnapshotName}}/metadata.json</code>
                    <button class="copy-btn" onclick="copyCmd(this)" title="Copy">📋</button>
                </div>
            </div>

            <hr class="divider">

            <div class="cmd-group">
                <div class="cmd-label">🌀 curl — tải từng file</div>
                <div class="cmd-desc">Tải 1 file cụ thể, hỗ trợ resume</div>
                <div class="cmd-box">
                    <code>curl -C - -o metadata.json http://{{$.ServerAddr}}/files/{{.SnapshotName}}/metadata.json</code>
                    <button class="copy-btn" onclick="copyCmd(this)" title="Copy">📋</button>
                </div>
            </div>

            <hr class="divider">

            <div class="cmd-group">
                <div class="cmd-label">🛠️ Script tự động — phục hồi node từ snapshot</div>
                <div class="cmd-desc">Stop node → tải snapshot → restart</div>
                <div class="cmd-box">
                    <code># 1. Stop node
pkill -f simple_chain; pkill -f metanode

# 2. Backup dữ liệu cũ (tuỳ chọn)
mv ./sample/simple/data-write/data ./sample/simple/data-write/data.bak

# 3. Tải snapshot
wget -c -r -np -nH --cut-dirs=2 http://{{$.ServerAddr}}/files/{{.SnapshotName}}/ -P ./sample/simple/data-write/data

# 4. Restart node
./run.sh</code>
                    <button class="copy-btn" onclick="copyCmd(this)" title="Copy">📋</button>
                </div>
            </div>
        </div>
    </div>
    {{end}}
    {{else}}
    <p class="empty">⏳ No snapshots available yet. Snapshots are created automatically after epoch transitions.</p>
    {{end}}

    <div class="api-section">
        <h3>📡 API Endpoint</h3>
        <div class="cmd-box" style="margin-top:8px">
            <code>curl http://{{.ServerAddr}}/api/snapshots</code>
            <button class="copy-btn" onclick="copyCmd(this)" title="Copy">📋</button>
        </div>
    </div>

    <script>
    function toggleDL(btn) {
        btn.classList.toggle('open');
        const section = btn.nextElementSibling;
        section.classList.toggle('open');
    }
    function copyCmd(btn) {
        const code = btn.parentElement.querySelector('code');
        navigator.clipboard.writeText(code.textContent.trim()).then(() => {
            btn.classList.add('copied');
            btn.textContent = '✅';
            setTimeout(() => { btn.classList.remove('copied'); btn.textContent = '📋'; }, 1500);
        });
    }
    </script>
</body>
</html>`
