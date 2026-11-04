package network

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	nt "github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

// waitOrFail fails the test instead of hanging the whole package when a signal never arrives.
func waitOrFail(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestAuditInitMustFinishBeforeNextRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseInit := func() { releaseOnce.Do(func() { close(release) }) }
	started := make(chan struct{})
	observed := make(chan bool, 1)
	var initialized atomic.Bool
	h := NewHandler(map[string]func(nt.Request) error{
		"InitConnection":      func(nt.Request) error { close(started); <-release; initialized.Store(true); return nil },
		"SendRawTransactions": func(nt.Request) error { observed <- initialized.Load(); return nil },
	}, nil)
	cfg := smallIngestConfig()
	s := &SocketServer{ctx: ctx, cancelFunc: cancel, config: cfg, handler: h, connectionsManager: NewConnectionsManager(), requestChan: make(chan nt.Request, 8), initChan: make(chan nt.Request, 8)}
	s.startWorkerPool()
	s.startInitWorkerPool()
	client, peer := net.Pipe()
	c, err := ConnectionFromTcpConnection(peer, cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.HandleConnection(c) }()
	defer func() {
		// a failed assertion must not leave the handler blocked, or the cleanup below would hang the package
		releaseInit()
		cancel()
		client.Close()
		c.Disconnect()
		<-done
		s.wg.Wait()
	}()

	writeCommand(t, client, "InitConnection")
	waitOrFail(t, started, "InitConnection to start")
	writeCommand(t, client, "SendRawTransactions")

	// Phase 1: Verify SendRawTransactions does NOT execute while InitConnection is still blocked
	select {
	case ready := <-observed:
		t.Fatalf("SendRawTransactions executed prematurely while InitConnection was still blocked (ready=%v)", ready)
	case <-time.After(50 * time.Millisecond):
		// Expected: handler blocked waiting for gate
	}

	// Phase 2: Unblock InitConnection and verify SendRawTransactions executes immediately
	releaseInit()
	select {
	case ready := <-observed:
		if !ready {
			t.Fatal("SendRawTransactions executed with initialized == false")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("SendRawTransactions did not execute after InitConnection completed")
	}
}

func TestReviewFailedInitMustNotAdmitNextRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := make(chan struct{}, 1)
	h := NewHandler(map[string]func(nt.Request) error{
		"InitConnection":      func(nt.Request) error { return errors.New("malformed handshake") },
		"SendRawTransactions": func(nt.Request) error { observed <- struct{}{}; return nil },
	}, nil)
	cfg := smallIngestConfig()
	s := &SocketServer{ctx: ctx, cancelFunc: cancel, config: cfg, handler: h, connectionsManager: NewConnectionsManager(), requestChan: make(chan nt.Request, 8), initChan: make(chan nt.Request, 8)}
	s.startWorkerPool()
	s.startInitWorkerPool()
	client, peer := net.Pipe()
	c, err := ConnectionFromTcpConnection(peer, cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.HandleConnection(c) }()
	defer func() {
		cancel()
		client.Close()
		c.Disconnect()
		<-done
		s.wg.Wait()
	}()

	writeCommand(t, client, "InitConnection")
	_ = client.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = client.Write(testFrame(t, "SendRawTransactions", nil))

	select {
	case <-observed:
		t.Fatal("transaction handler ran after InitConnection returned an error")
	case <-time.After(100 * time.Millisecond):
		// Expected: transaction handler is rejected / connection torn down
	}
}

func TestReviewInitServerShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	h := NewHandler(map[string]func(nt.Request) error{
		"InitConnection": func(nt.Request) error { close(started); <-release; return nil },
	}, nil)
	cfg := smallIngestConfig()
	s := &SocketServer{ctx: ctx, cancelFunc: cancel, config: cfg, handler: h, connectionsManager: NewConnectionsManager(), requestChan: make(chan nt.Request, 8), initChan: make(chan nt.Request, 8)}
	s.startWorkerPool()
	s.startInitWorkerPool()
	client, peer := net.Pipe()
	c, err := ConnectionFromTcpConnection(peer, cfg)
	if err != nil {
		t.Fatal(err)
	}
	handleDone := make(chan error, 1)
	go func() { handleDone <- s.HandleConnection(c) }()
	defer func() {
		close(release)
		client.Close()
		c.Disconnect()
		s.wg.Wait()
	}()

	writeCommand(t, client, "InitConnection")
	waitOrFail(t, started, "InitConnection to start")

	// Cancel context while init is waiting
	cancel()

	select {
	case err := <-handleDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("HandleConnection hung on server shutdown while waiting for InitConnection")
	}
}

type auditWriteFailure struct {
	net.Conn
	failed chan struct{}
	once   sync.Once
}

func (c *auditWriteFailure) Write([]byte) (int, error) {
	c.once.Do(func() { close(c.failed) })
	return 0, errors.New("injected write failure")
}

func TestAuditWriterFailureMustDisconnect(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	broken := &auditWriteFailure{Conn: peer, failed: make(chan struct{})}
	cfg := smallIngestConfig()
	cfg.AcceptedConnectionIdleTimeout = time.Hour
	c, err := ConnectionFromTcpConnection(broken, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Disconnect()
	msg := NewMessage(&pb.Message{Header: &pb.Header{Command: "Receipt"}})
	if err := c.SendMessage(msg); err != nil {
		t.Fatal(err)
	}
	waitOrFail(t, broken.failed, "the injected write failure")
	time.Sleep(30 * time.Millisecond)
	if c.IsConnect() {
		if err := c.SendMessage(msg); err == nil {
			t.Fatal("writer exited after I/O failure, but connection stays live and accepts another undeliverable Receipt")
		}
	}
}

func TestAuditInitConnectionIsolatedFromGeneralWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	generalBlock := make(chan struct{})
	var generalBlockedCount atomic.Int32
	initDone := make(chan struct{})

	h := NewHandler(map[string]func(nt.Request) error{
		"BlockGeneral": func(nt.Request) error {
			generalBlockedCount.Add(1)
			<-generalBlock
			return nil
		},
		"InitConnection": func(nt.Request) error {
			close(initDone)
			return nil
		},
	}, nil)

	cfg := smallIngestConfig()
	cfg.HandlerWorkerPoolSize = 2 // 2 general workers
	cfg.InitWorkerPoolSize = 2    // 2 dedicated init workers
	cfg.RequestChanSize = 2

	s := &SocketServer{
		ctx:                ctx,
		cancelFunc:         cancel,
		config:             cfg,
		handler:            h,
		connectionsManager: NewConnectionsManager(),
		requestChan:        make(chan nt.Request, cfg.RequestChanSize),
		initChan:           make(chan nt.Request, 8),
	}
	s.startWorkerPool()
	s.startInitWorkerPool()

	defer func() {
		close(generalBlock)
		cancel()
		s.wg.Wait()
	}()

	// Push 2 requests to saturate the 2 general workers
	reqMsg := NewMessage(&pb.Message{Header: &pb.Header{Command: "BlockGeneral"}})
	s.requestChan <- NewRequest(nil, reqMsg)
	s.requestChan <- NewRequest(nil, reqMsg)

	// Wait until both general workers have picked up the requests and are blocked
	for i := 0; i < 50; i++ {
		if generalBlockedCount.Load() == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if generalBlockedCount.Load() != 2 {
		t.Fatalf("expected 2 general workers blocked, got %d", generalBlockedCount.Load())
	}

	// Also saturate requestChan completely
	s.requestChan <- NewRequest(nil, reqMsg)
	s.requestChan <- NewRequest(nil, reqMsg)

	// Now connect a new client and send InitConnection
	client, peer := net.Pipe()
	conn, err := ConnectionFromTcpConnection(peer, cfg)
	if err != nil {
		t.Fatal(err)
	}
	handleDone := make(chan error, 1)
	go func() {
		handleDone <- s.HandleConnection(conn)
	}()
	defer func() {
		client.Close()
		conn.Disconnect()
		<-handleDone
	}()

	writeCommand(t, client, "InitConnection")

	// InitConnection must finish promptly because it runs on dedicated initWorker, not blocked by general workers or full requestChan
	waitOrFail(t, initDone, "InitConnection to complete while general workers and requestChan are saturated")
}

func TestAuditStopConcurrentWithInitPush(t *testing.T) {
	bls.Init()
	keyPair := bls.NewKeyPair(common.FromHex("372e9d6411071707a7e7ba76a51c7907a6c799f0cb972df1671e582d649caabf"))
	cfg := smallIngestConfig()
	cfg.InitWorkerPoolSize = 2
	cfg.InitChanSize = 32

	h := NewHandler(map[string]func(nt.Request) error{
		"InitConnection": func(nt.Request) error {
			time.Sleep(1 * time.Millisecond)
			return nil
		},
	}, nil)

	server, err := NewSocketServer(cfg, keyPair, NewConnectionsManager(), h, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	s := server.(*SocketServer)
	s.startWorkerPool()
	s.startInitWorkerPool()

	var stopWG sync.WaitGroup
	producerCount := 8
	stopWG.Add(producerCount)

	for i := 0; i < producerCount; i++ {
		go func() {
			defer stopWG.Done()
			for j := 0; j < 20; j++ {
				client, peer := net.Pipe()
				conn, errConn := ConnectionFromTcpConnection(peer, cfg)
				if errConn != nil {
					_ = client.Close()
					return
				}
				done := make(chan struct{})
				go func() {
					_ = s.HandleConnection(conn)
					close(done)
				}()

				body, _ := proto.Marshal(&pb.Message{Header: &pb.Header{Command: "InitConnection"}})
				length := make([]byte, 8)
				binary.LittleEndian.PutUint64(length, uint64(len(body)))
				_ = client.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
				_, _ = client.Write(append(length, body...))

				select {
				case <-done:
				case <-time.After(50 * time.Millisecond):
					_ = client.Close()
					<-done
				}
				_ = client.Close()
			}
		}()
	}

	time.Sleep(5 * time.Millisecond)
	s.Stop()
	stopWG.Wait()
}

func TestAuditConfigZeroValuesNormalized(t *testing.T) {
	bls.Init()
	keyPair := bls.NewKeyPair(common.FromHex("372e9d6411071707a7e7ba76a51c7907a6c799f0cb972df1671e582d649caabf"))
	rawCfg := &Config{
		InitWorkerPoolSize:  0,
		InitChanSize:        0,
		RequestChanSize:     0,
		MaxConnections:      0,
		MaxConnectionsPerIP: 0,
	}

	server, err := NewSocketServer(rawCfg, keyPair, NewConnectionsManager(), NewHandler(nil, nil), "1.0.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Caller's struct must NOT be mutated
	if rawCfg.InitWorkerPoolSize != 0 || rawCfg.InitChanSize != 0 {
		t.Fatalf("caller's config was mutated: %+v", rawCfg)
	}

	s := server.(*SocketServer)
	if s.config.InitWorkerPoolSize != 2 {
		t.Fatalf("expected InitWorkerPoolSize 2, got %d", s.config.InitWorkerPoolSize)
	}
	if s.config.InitChanSize != 256 {
		t.Fatalf("expected InitChanSize 256, got %d", s.config.InitChanSize)
	}
	if cap(s.initChan) != 256 {
		t.Fatalf("expected initChan capacity 256, got %d", cap(s.initChan))
	}
	if s.config.RequestChanSize != 500000 {
		t.Fatalf("expected RequestChanSize 500000, got %d", s.config.RequestChanSize)
	}
}

func TestAuditReconnectStormInitConnections(t *testing.T) {
	bls.Init()
	keyPair := bls.NewKeyPair(common.FromHex("372e9d6411071707a7e7ba76a51c7907a6c799f0cb972df1671e582d649caabf"))
	cfg := DefaultConfig()
	cfg.InitWorkerPoolSize = 2
	cfg.InitChanSize = 256
	cfg.MaxConnections = 1000
	cfg.MaxConnectionsPerIP = 1000

	var processedCount atomic.Int32
	routes := map[string]func(nt.Request) error{
		"InitConnection": func(nt.Request) error {
			processedCount.Add(1)
			return nil
		},
	}
	server, err := NewSocketServer(cfg, keyPair, NewConnectionsManager(), NewHandler(routes, nil), "1.0.0")
	if err != nil {
		t.Fatal(err)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	go func() { _ = server.Listen(addr) }()
	defer server.Stop()

	// Wait for server to accept connections
	for i := 0; i < 40; i++ {
		time.Sleep(20 * time.Millisecond)
		if c, err := net.Dial("tcp", addr); err == nil {
			_ = c.Close()
			break
		}
	}

	totalConns := 256
	var wg sync.WaitGroup
	startBarrier := make(chan struct{})
	var failedCount atomic.Int32

	for i := 0; i < totalConns; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
			if err != nil {
				failedCount.Add(1)
				return
			}
			defer conn.Close()

			<-startBarrier

			writeCommand(t, conn, "InitConnection")
			time.Sleep(100 * time.Millisecond)
		}()
	}

	close(startBarrier)
	wg.Wait()

	if failedCount.Load() > 0 {
		t.Fatalf("%d connections failed to dial", failedCount.Load())
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if processedCount.Load() >= int32(totalConns) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if processedCount.Load() < int32(totalConns) {
		t.Fatalf("expected at least %d processed InitConnection, got %d", totalConns, processedCount.Load())
	}
}

func TestReadOnlyCommandsBypassInitGate(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chainIdObserved := make(chan struct{}, 1)
	accountStateObserved := make(chan struct{}, 1)

	h := NewHandler(map[string]func(nt.Request) error{
		"GetChainId": func(nt.Request) error {
			chainIdObserved <- struct{}{}
			return nil
		},
		"GetAccountState": func(nt.Request) error {
			accountStateObserved <- struct{}{}
			return nil
		},
	}, nil)

	cfg := smallIngestConfig()
	s := &SocketServer{
		ctx:                ctx,
		cancelFunc:         cancel,
		config:             cfg,
		handler:            h,
		connectionsManager: NewConnectionsManager(),
		requestChan:        make(chan nt.Request, 8),
		initChan:           make(chan nt.Request, 8),
	}
	s.startWorkerPool()
	s.startInitWorkerPool()

	client, peer := net.Pipe()
	c, err := ConnectionFromTcpConnection(peer, cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = s.HandleConnection(c)
	}()
	defer func() {
		cancel()
		client.Close()
		c.Disconnect()
		<-done
		s.wg.Wait()
	}()

	// Gửi GetChainId mà KHÔNG gửi InitConnection trước
	writeCommand(t, client, "GetChainId")
	select {
	case <-chainIdObserved:
		// Thành công ngay lập tức mà không phải chờ 30s timeout hay InitConnection
	case <-time.After(200 * time.Millisecond):
		t.Fatal("GetChainId was blocked waiting for InitConnection")
	}

	// Gửi GetAccountState mà KHÔNG gửi InitConnection trước
	writeCommand(t, client, "GetAccountState")
	select {
	case <-accountStateObserved:
		// Thành công ngay lập tức
	case <-time.After(200 * time.Millisecond):
		t.Fatal("GetAccountState was blocked waiting for InitConnection")
	}
}
