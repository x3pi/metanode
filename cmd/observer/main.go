package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	client_tcp "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp"
	tcp_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/listener"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/processor"
	"github.com/meta-node-blockchain/meta-node/cmd/observer/routes"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/connection_manager"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/loggerfile"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
)

type SupervisorApp struct {
	config             *tcp_config.ClientConfig
	processor          *processor.SupervisorProcessor
	connectionsManager t_network.ConnectionsManager
	messageSender      t_network.MessageSender
	socketServer       t_network.SocketServer
	keyPair            *bls.KeyPair
	ctx                context.Context
	cancel             context.CancelFunc
}

func NewSupervisorApp(cfg *tcp_config.ClientConfig) (*SupervisorApp, error) {
	app := &SupervisorApp{
		config: cfg,
	}
	app.ctx, app.cancel = context.WithCancel(context.Background())
	bls.Init()
	app.keyPair = bls.GenerateKeyPair()
	app.connectionsManager = network.NewConnectionsManager()
	app.messageSender = network.NewMessageSender("1.0.0")
	app.processor = processor.NewSupervisorProcessor(cfg, app.messageSender)
	r, limits := app.initRoutes()

	handler := network.NewHandler(r, limits)
	socketServer, err := network.NewSocketServer(
		nil,
		app.keyPair,
		app.connectionsManager,
		handler,
		"SUPERVISOR",
		"1.0.0",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create socket server: %w", err)
	}
	customConnectionManager := connection_manager.NewConnectionManager(socketServer, app.messageSender)
	app.processor.SetConnectionManager(customConnectionManager)

	socketServer.SetContext(app.ctx, app.cancel)
	app.socketServer = socketServer

	return app, nil
}

func (app *SupervisorApp) initRoutes() (map[string]func(t_network.Request) error, map[string]int) {
	r := make(map[string]func(t_network.Request) error)
	limits := make(map[string]int)
	routes.InitRoutes(r, limits, app.processor)
	logger.Info("Registered %d routes", len(r))
	return r, limits
}

func (app *SupervisorApp) Run() error {
	logger.Info("Starting Supervisor service on %s", app.config.ConnectionAddress())
	go func() {
		if err := app.socketServer.Listen(app.config.ConnectionAddress()); err != nil {
			logger.Error("Socket server error: %v", err)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan
	logger.Info("Received shutdown signal")
	return nil
}

func (app *SupervisorApp) Stop() {
	logger.Info("Stopping Supervisor service...")
	if app.processor != nil {
		app.processor.Stop()
	}
	if app.socketServer != nil {
		app.socketServer.Stop()
	}
	if app.cancel != nil {
		app.cancel()
	}
	logger.Info("Supervisor service stopped")
}

func main() {
	configPath := flag.String("config", "config-client-tcp.json", "Path to configuration file")
	logDirFlag := flag.String("log-dir", "", "Override log directory path")
	logNameFlag := flag.String("log-name", "Supervisor.log", "Log file name")
	logLevel := flag.Int("log-level", logger.FLAG_INFO, "Log level")
	flag.Parse()

	logger.SetConfig(&logger.LoggerConfig{
		Flag:    *logLevel,
		Outputs: []*os.File{os.Stdout},
	})

	cfgRaw, err := tcp_config.LoadConfig(*configPath)
	if err != nil {
		logger.Error("Failed to load config: %v", err)
		os.Exit(1)
	}
	cfg := cfgRaw.(*tcp_config.ClientConfig)

	app, err := NewSupervisorApp(cfg)
	if err != nil {
		logger.Error("Failed to create supervisor app: %v", err)
		os.Exit(1)
	}

	// Setup log file
	logDir := cfg.LogPath
	if *logDirFlag != "" {
		logDir = *logDirFlag
	}
	if logDir != "" {
		if _, err := os.Stat(logDir); os.IsNotExist(err) {
			if err = os.MkdirAll(logDir, 0755); err != nil {
				fmt.Printf("Failed to create log directory '%s': %v\n", logDir, err)
				os.Exit(1)
			}
		}
		loggerfile.SetGlobalLogDir(logDir)
		todayDir := time.Now().Format("2006/01/02")
		logFilePath := filepath.Join(logDir, todayDir, *logNameFlag)
		fmt.Printf("Log file will be: %s\n", logFilePath)
		if _, err := logger.EnableDailyFileLog(*logNameFlag); err != nil {
			logger.Error("Failed to enable file logging: %v", err)
			os.Exit(1)
		}
		logger.SetConsoleOutputEnabled(false)
	}

	// ─────────────────────────────────────────────────────────────────────────
	// New design: 1 client duy nhất từ config chính (private_key + parent_address)
	// Dùng connection_manager để kết nối từng remote chain và scan GetLogs.
	// Không còn tạo client riêng cho từng remote_chain.
	// ─────────────────────────────────────────────────────────────────────────
	logger.Info("Creating single embassy client from main config (address=%s, chain=%s)",
		cfg.ParentAddress, cfg.ParentConnectionAddress)

	localClient, err := client_tcp.NewClient(cfg)
	if err != nil {
		logger.Error("Failed to create embassy client: %v", err)
		os.Exit(1)
	}

	// Tạo 1 scanner dùng connection_manager để quét logs từ các remote chains
	scanner := listener.NewCrossChainScanner(
		localClient,
		cfg,
		app.processor,
	)
	scanner.Start()

	logger.Info("Starting Supervisor with config: %s", *configPath)
	fmt.Println("Server is running")
	defer app.Stop()
	if err := app.Run(); err != nil {
		logger.Error("Application error: %v", err)
		os.Exit(1)
	}
}
