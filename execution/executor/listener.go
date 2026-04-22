package executor

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	// Protobuf generated types for IPC protocol
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"

	"google.golang.org/protobuf/proto"
)

var bufferPool = sync.Pool{
	New: func() interface{} {
		// Initial allocation: 1MB (typical protobuf blocks fit within this; grows if needed)
		b := make([]byte, 1024*1024)
		return &b
	},
}

func getBuffer(size int) []byte {
	b := bufferPool.Get().(*[]byte)
	if cap(*b) < size {
		// Pool buffer too small — allocate a new one
		newBuf := make([]byte, size*2)
		return newBuf[:size]
	}
	return (*b)[:size]
}

func putBuffer(b []byte) {
	// Don't recycle buffers >10MB to prevent virtual memory bloat
	if cap(b) > 10*1024*1024 {
		return
	}
	bufferPool.Put(&b)
}

// Listener is a reusable component that listens for CommittedEpochData on a Unix/TCP socket.
type Listener struct {
	socketPath string
	listener   net.Listener
	dataChan   chan *pb.ExecutableBlock // Channel to deliver decoded blocks to consumers
	wg         sync.WaitGroup
	quit       chan struct{}
	sem        chan struct{} // Semaphore to limit concurrent connections
}

// NewListener creates and initializes a new Listener instance.
// socketPath specifies the Unix socket path or TCP address to listen on.
func NewListener(socketPath string) *Listener {
	// socketPath := fmt.Sprintf("/tmp/executor%d.sock", socketID)
	return &Listener{
		socketPath: socketPath,
		// Buffered channel to prevent blocking sender goroutine when consumer is busy
		// CRITICAL: Increase buffer size to prevent blocking when Go Master is processing blocks
		// If dataChan is full, handleConnection will block and Rust cannot send more blocks
		// Buffer size 50000 should be enough to handle bursts of blocks while Go Master processes them
		dataChan: make(chan *pb.ExecutableBlock, 50000),
		quit:     make(chan struct{}),
		// IPC-6: Limit concurrent connections. Increased from 50 to 100 to handle
		// burst recovery during epoch transitions and snapshot restores.
		sem: make(chan struct{}, 100),
	}
}

// Start begins listening for connections on the configured socket.
// This is a non-blocking call.
func (l *Listener) Start() error {
	// Use new socket abstraction layer
	socketConfig := NewSocketConfig(l.socketPath)

	listener, err := socketConfig.Listen()
	if err != nil {
		return fmt.Errorf("cannot start listener: %w", err)
	}
	l.listener = listener

	log.Printf("Module Listener listening on: %s (type: %s)", socketConfig, socketConfig.Network())

	l.wg.Add(1)
	go l.acceptConnections() // Run goroutine to accept incoming connections

	return nil
}

// Stop gracefully closes the listener and waits for all goroutines to finish.
func (l *Listener) Stop() {
	close(l.quit) // Signal stop
	if l.listener != nil {
		l.listener.Close() // Close listener to break the accept loop
	}
	l.wg.Wait()       // Wait for handler goroutines to finish
	close(l.dataChan) // Close data channel
	log.Println("Module Listener stopped.")
}

func (l *Listener) DataChannel() <-chan *pb.ExecutableBlock {
	return l.dataChan
}

// acceptConnections accepts incoming connections and handles each in a new goroutine.
func (l *Listener) acceptConnections() {
	defer l.wg.Done()
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			// Check if the error is because the listener was closed
			select {
			case <-l.quit:
				return // Exit on stop signal
			default:
				log.Printf("Error accepting connection: %v", err)
				continue
			}
		}

		// Try to acquire semaphore
		select {
		case l.sem <- struct{}{}:
			// Acquired
			// Handle each connection in a separate goroutine to avoid blocking accept loop
			go l.handleConnection(conn)
		default:
			// Limit reached
			log.Printf("⚠️ [LISTENER] Rejecting connection from %s: max connections limit (100) reached", conn.RemoteAddr())
			conn.Close()
		}
	}
}

// handleConnection reads, decodes data from a connection and sends it via dataChan.
func (l *Listener) handleConnection(conn net.Conn) {
	defer func() {
		<-l.sem // Release semaphore
		conn.Close()
	}()

	log.Printf("🔌 [LISTENER] Accepted new connection from %s (active: %d)", conn.RemoteAddr(), len(l.sem))

	// Optimize connection buffers
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
		tcpConn.SetKeepAlive(true)
		_ = tcpConn.SetReadBuffer(32 * 1024 * 1024)
		_ = tcpConn.SetWriteBuffer(32 * 1024 * 1024)
	}
	if unixConn, ok := conn.(*net.UnixConn); ok {
		_ = unixConn.SetReadBuffer(32 * 1024 * 1024)
		_ = unixConn.SetWriteBuffer(32 * 1024 * 1024)
	}

	reader := bufio.NewReaderSize(conn, 4*1024*1024)

	// STABILITY FIX (G-H1): Reuse a single timer instead of allocating one per block.
	// At high throughput (4K+ blocks/s), time.NewTimer per block creates massive
	// GC pressure and runtime timer goroutine churn.
	sendTimer := time.NewTimer(300 * time.Second)
	if !sendTimer.Stop() {
		<-sendTimer.C
	}

	for {
		// Set read deadline to cleanup inactive connections
		// Assuming blocks are sent frequently, 2 minutes is generous
		conn.SetReadDeadline(time.Now().Add(2 * time.Minute))

		// Read message length (Uvarint encoding)
		msgLen, err := binary.ReadUvarint(reader)
		if err != nil {
			if err != io.EOF {
				// Don't log timeout errors as errors, just info
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					log.Printf("🔌 [LISTENER] Connection timed out (idle) from %s", conn.RemoteAddr())
				} else {
					log.Printf("❌ [LISTENER] Error reading message length: %v", err)
				}
			} else {
				log.Printf("🔌 [LISTENER] Connection closed by client (EOF)")
			}
			return // End this goroutine on error or client disconnect
		}

		// Reset deadline for body read
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))

		// Read message body into recycled buffer
		buf := getBuffer(int(msgLen))
		if _, err := io.ReadFull(reader, buf); err != nil {
			log.Printf("❌ [LISTENER] Error reading message body (%d bytes): %v", msgLen, err)
			putBuffer(buf)
			return
		}

		// Decode buffer via Protobuf
		var epochData pb.ExecutableBlock
		if err := proto.Unmarshal(buf, &epochData); err != nil {
			log.Printf("❌ [LISTENER] Error unmarshaling Protobuf (%d bytes): %v. Dump: %x", msgLen, err, buf[:min(len(buf), 50)])
			putBuffer(buf)
			continue // Continue loop to read next message
		}

		// Return buffer to pool after Unmarshal (epochData has copied the needed data)
		putBuffer(buf)

		// CC-1: Unified batch_id
		batchID := fmt.Sprintf("E%dC%dG%d", epochData.GetEpoch(), epochData.GetCommitIndex(), epochData.GetGlobalExecIndex())

		if len(epochData.Transactions) > 0 || epochData.GetGlobalExecIndex()%100 == 0 {
			log.Printf("[batch_id=%s] 📥 [LISTENER] Received block: txs=%d, size=%d bytes",
				batchID, len(epochData.Transactions), msgLen)
			log.Printf("🔥 [PROFILING] GoMaster: Received block from Rust UDS at UnixMilli: %d (txs=%d, G=%d)", 
				time.Now().UnixMilli(), len(epochData.Transactions), epochData.GetGlobalExecIndex())
		}

		// T2-2: Queue fill monitoring (backpressure is handled by blocking channel send below)
		// Previously this used time.Sleep for adaptive backpressure, but the 60s-timeout
		// blocking send already provides correct natural backpressure. Sleep delays
		// were stacking on top of the channel block, adding unnecessary latency.
		chLen := len(l.dataChan)
		const dataChanCap = 50000
		if chLen > dataChanCap*8/10 { // Only log above 80% fill
			log.Printf("⚠️ [QUEUE-MONITOR] Queue %d%% full (%d/%d) — natural backpressure active via blocking send",
				chLen*100/dataChanCap, chLen, dataChanCap)
		}

		// CRITICAL: Send data through channel - MUST use blocking send!
		// G-H1 FIX: Reuse sendTimer instead of allocating a new timer per block.
		sendTimer.Reset(300 * time.Second)
		select {
		case l.dataChan <- &epochData:
			// Successfully queued — drain the timer if it hasn't fired
			if !sendTimer.Stop() {
				select {
				case <-sendTimer.C:
				default:
				}
			}
		case <-sendTimer.C:
			// Emergency: after 300s timeout, log critical error and drop connection
			log.Printf("🚨 [LISTENER CRITICAL] dataChan blocked for 300s! Dropping conn. GEI=%d epoch=%d txs=%d commit=%d — BLOCK LOST! Investigate Go processing stall.",
				epochData.GetGlobalExecIndex(), epochData.GetEpoch(), len(epochData.Transactions), epochData.GetCommitIndex())
			return // Goroutine exits, releasing l.sem and closing conn
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
