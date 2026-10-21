package network

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	nt "github.com/meta-node-blockchain/meta-node/types/network"
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
	s := &SocketServer{ctx: ctx, cancelFunc: cancel, config: cfg, handler: h, connectionsManager: NewConnectionsManager(), requestChan: make(chan nt.Request, 8)}
	s.startWorkerPool()
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
	s := &SocketServer{ctx: ctx, cancelFunc: cancel, config: cfg, handler: h, connectionsManager: NewConnectionsManager(), requestChan: make(chan nt.Request, 8)}
	s.startWorkerPool()
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
	s := &SocketServer{ctx: ctx, cancelFunc: cancel, config: cfg, handler: h, connectionsManager: NewConnectionsManager(), requestChan: make(chan nt.Request, 8)}
	s.startWorkerPool()
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
