package main

import (
	"crypto/tls"
	"errors"
	"flag"
	"log"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/app"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/config"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/internal/proxy"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/internal/tcp_server"
	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/setup"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
)

var defaultLogsDir string

func main() {
	log.SetOutput(os.Stdout)
	var configPath string
	var tcpConfigPath string
	flag.StringVar(&defaultLogsDir, "logs-root", "./logs", "Root directory to store rpc-client logs (YYYY/MM/DD)")
	flag.StringVar(&configPath, "config", "config-rpc.json", "Path to the RPC configuration file")
	flag.StringVar(&tcpConfigPath, "tcp-config", "config-client-tcp.json", "Path to the TCP client configuration file")
	flag.Parse()

	if err := setup.SetupLogging(defaultLogsDir); err != nil {
		log.Fatalf("FATAL: Failed to setup logging: %v", err)
	}
	cfg, tcpCfg, err := config.Load(configPath, tcpConfigPath)
	if err != nil {
		log.Fatalf("FATAL: Failed to load configuration: %v", err)
	}
	go func() {
		logger.Info("Starting pprof server on localhost:6060")
		logger.Error(http.ListenAndServe("localhost:6060", nil))
	}()
	appCtx, err := app.New(cfg, tcpCfg)
	if err != nil {
		logger.Error("Failed to initialize application context: %v", err)
		log.Printf("FATAL: Application context initialization failed: %v", err)
		log.Printf("HINT: Nếu LevelDB bị corrupted, hãy xóa thư mục database và chạy lại:")
		log.Printf("     rm -rf %s %s", cfg.LdbBlsWalletsPath, cfg.LdbNotificationPath)
		log.Fatalf("FATAL: Application context initialization failed: %v", err)
	}
	// Initialize proxy
	rpcProxy, err := proxy.New(appCtx)
	if err != nil {
		logger.Error("Failed to initialize proxy: %v", err)
		log.Fatalf("FATAL: Proxy initialization failed: %v", err)
	}
	defer func() {
		if err := rpcProxy.Close(); err != nil {
			logger.Error("Error closing proxy resources: %v", err)
		}
	}()
	logger.Info("RPC Reverse Proxy initialized successfully")

	// Initialize TCP RPC Server
	tcpRpcServer, err := tcp_server.New(appCtx)
	if err != nil {
		logger.Error("Failed to initialize TCP RPC server: %v", err)
		log.Fatalf("FATAL: TCP RPC server initialization failed: %v", err)
	}
	defer tcpRpcServer.Stop()

	httpServer := setupHTTPServer(rpcProxy, cfg, defaultLogsDir)
	// TLS setup
	var tlsConfig *tls.Config
	useTLS := cfg.CertFile != "" && cfg.KeyFile != ""
	if useTLS {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			log.Fatalf("FATAL: Failed to load TLS certificate/key: %v", err)
		}
		tlsConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
		logger.Info("TLS enabled using cert: %s, key: %s", cfg.CertFile, cfg.KeyFile)
	}

	var wg sync.WaitGroup
	serverRunning := false

	if cfg.ServerPort != "" {
		serverRunning = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("Starting HTTP server on port %s", cfg.ServerPort)
			if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("FATAL: HTTP server failed: %v", err)
			}
		}()
	}

	if useTLS && cfg.HTTPSPort != "" {
		serverRunning = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			httpsServer := &http.Server{
				Addr:              cfg.HTTPSPort,
				Handler:           httpServer.Handler,
				TLSConfig:         tlsConfig,
				ReadTimeout:       120 * time.Second,
				WriteTimeout:      120 * time.Second,
				IdleTimeout:       360 * time.Second,
				MaxHeaderBytes:    1 << 20, // 1MB
				ReadHeaderTimeout: 10 * time.Second,
			}
			logger.Info("Starting HTTPS server on port %s", cfg.HTTPSPort)
			if err := httpsServer.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Fatalf("FATAL: HTTPS server failed: %v", err)
			}
		}()
	}

	// Start TCP RPC Server
	if cfg.TcpServerPort != "" {
		serverRunning = true
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.Info("Starting TCP RPC server on port %s", cfg.TcpServerPort)
			if err := tcpRpcServer.ListenAndServe(cfg.TcpServerPort); err != nil {
				log.Printf("TCP RPC server error: %v", err)
			}
		}()
	}

	if !serverRunning {
		log.Fatalf("FATAL: No HTTP or HTTPS server configured to run. Exiting.")
	}

	logger.Info("RPC Reverse Proxy started successfully.")
	wg.Wait()
	logger.Info("All servers have shut down. Exiting.")
}
func setupHTTPServer(rpcProxy *proxy.RpcReverseProxy, cfg *config.Config, logsDir string) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/debug/logs/list", setup.HandleRPCLogList(logsDir))
	mux.HandleFunc("/debug/logs/content", setup.HandleRPCLogContent(logsDir))
	distPath := "./dist"
	mux.Handle("/register-bls-key/", spaHandler(distPath, "/register-bls-key/"))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if websocket.IsWebSocketUpgrade(r) {
			var finalTargetURL string
			// Interceptor WebSocket endpoint - có interceptor, chặn lại
			if strings.HasPrefix(r.URL.Path, "/interceptor") {
				finalTargetURL = rpcProxy.AppCtx.ClientRpc.UrlWS
				logger.Debug("Routing WebSocket upgrade for %s to INTERCEPTOR target %s", r.URL.Path, finalTargetURL)
				if finalTargetURL == "" {
					logger.Error("WebSocket upgrade requested for %s, but target URL is empty", r.URL.Path)
					http.Error(w, "Target WebSocket endpoint is not configured", http.StatusServiceUnavailable)
					return
				}
				rpcProxy.ServeWebSocketWithInterceptor(w, r, finalTargetURL)
				return
			}
			// Readonly WebSocket endpoint
			if strings.HasPrefix(r.URL.Path, "/readonly") {
				if rpcProxy.ReadonlyWSSServerURL != "" {
					targetBaseURL, _ := url.Parse(rpcProxy.ReadonlyWSSServerURL)
					targetHost := targetBaseURL.Scheme + "://" + targetBaseURL.Host
					pathForTarget := strings.TrimPrefix(r.URL.Path, "/readonly")
					finalTargetURL = targetHost + pathForTarget
					logger.Debug("Routing WebSocket upgrade for %s to READONLY target %s", r.URL.Path, finalTargetURL)
				} else {
					logger.Error("Readonly WebSocket upgrade requested for %s, but no readonly_wss_server_url configured", r.URL.Path)
					http.Error(w, "Readonly WebSocket endpoint is not configured", http.StatusServiceUnavailable)
					return
				}
			} else {
				// Default WebSocket endpoint - KHÔNG có interceptor, forward trực tiếp lên chain
				finalTargetURL = rpcProxy.AppCtx.ClientRpc.UrlWS
				logger.Debug("Routing WebSocket upgrade for %s to DEFAULT (no interceptor) target %s", r.URL.Path, finalTargetURL)
			}

			if finalTargetURL == "" {
				logger.Error("WebSocket upgrade requested for %s, but target URL is empty", r.URL.Path)
				http.Error(w, "Target WebSocket endpoint is not configured", http.StatusServiceUnavailable)
				return
			}
			// Default route: không có interceptor, forward trực tiếp
			rpcProxy.ServeWebSocketWithoutInterceptor(w, r, finalTargetURL)
			return
		}

		// HTTP Readonly endpoint
		if strings.HasPrefix(r.URL.Path, "/readonly") {
			if rpcProxy.ReadonlyReverseProxy != nil {
				logger.Debug("Forwarding HTTP request to READONLY target: %s", r.URL.Path)
				r.URL.Path = strings.TrimPrefix(r.URL.Path, "/readonly")
				if !strings.HasPrefix(r.URL.Path, "/") {
					r.URL.Path = "/" + r.URL.Path
				}
				rpcProxy.ReadonlyReverseProxy.ServeHTTP(w, r)
			} else {
				logger.Error("Received HTTP request for /readonly but readonly proxy is not configured")
				http.Error(w, "Readonly endpoint is not configured", http.StatusNotImplemented)
			}
			return
		}
		// Default: Forward to main RPC proxy
		rpcProxy.ServeHTTP(w, r)
	})
	handler := setup.CORSMiddleware(mux)
	handler = setup.LoggingMiddleware(handler)

	return &http.Server{
		Addr:              cfg.ServerPort,
		Handler:           handler,
		ReadTimeout:       300 * time.Second,
		WriteTimeout:      300 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB
		ReadHeaderTimeout: 10 * time.Second,
	}
}

// spaHandler serves static files với SPA routing fallback
func spaHandler(staticPath string, prefix string) http.Handler {
	return http.StripPrefix(prefix, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(staticPath, r.URL.Path)
		fileInfo, err := os.Stat(path)
		// If file exists and is not a directory, serve it
		if err == nil && !fileInfo.IsDir() {
			http.ServeFile(w, r, path)
			return
		}
		// If file doesn't exist or is a directory, serve index.html (SPA fallback)
		indexPath := filepath.Join(staticPath, "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			http.ServeFile(w, r, indexPath)
			return
		}

		// If index.html doesn't exist, return 404
		http.NotFound(w, r)
	}))
}
