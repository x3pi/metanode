# QUIC Stream Architecture - Chi tiết hoạt động

## 📋 Mục lục
1. [Tổng quan về QUIC](#tổng-quan-về-quic)
2. [Kiến trúc Connection vs Stream](#kiến-trúc-connection-vs-stream)
3. [Flow hoạt động chi tiết](#flow-hoạt-động-chi-tiết)
4. [Client-side (Go)](#client-side-go)
5. [Server-side (Rust)](#server-side-rust)
6. [Length-Delimited Framing](#length-delimited-framing)
7. [Lifecycle của một Request](#lifecycle-của-một-request)
8. [Ví dụ thực tế](#ví-dụ-thực-tế)
9. [So sánh với TCP/HTTP](#so-sánh-với-tcphttp)

---

## Tổng quan về QUIC

### QUIC là gì?
**QUIC** (Quick UDP Internet Connections) là giao thức truyền tải được Google phát triển, chạy trên UDP nhưng cung cấp:
- ✅ **Multiplexing**: Nhiều stream song song trên 1 connection
- ✅ **0-RTT**: Kết nối nhanh hơn TCP
- ✅ **Đảm bảo thứ tự**: Trong từng stream riêng lẻ
- ✅ **Không bị Head-of-Line Blocking**: Stream này không ảnh hưởng stream khác

### Tại sao dùng QUIC?
```
HTTP/1.1 (TCP):
    Connection 1 → Request 1 → Response 1 → Request 2 → Response 2
    ❌ Phải chờ tuần tự

HTTP/2 (TCP):
    Connection 1 → [Stream 1, Stream 2, Stream 3] 
    ⚠️ Vẫn bị Head-of-Line Blocking ở TCP layer

QUIC (UDP):
    Connection 1 → [Stream 1, Stream 2, Stream 3] (độc lập hoàn toàn)
    ✅ Không blocking, packet loss ở stream 1 không ảnh hưởng stream 2
```

---

## Kiến trúc Connection vs Stream

### Mô hình phân cấp

```
┌─────────────────────────────────────────┐
│         QUIC Connection                 │  ← 1 kết nối TCP logic
│  (Giữa Client và Server)               │
│                                         │
│  ┌─────────────┐  ┌─────────────┐     │
│  │  Stream 1   │  │  Stream 2   │  ...│  ← Nhiều stream song song
│  │  (Request)  │  │  (Request)  │     │
│  └─────────────┘  └─────────────┘     │
└─────────────────────────────────────────┘

Mỗi Stream:
  - Có ID riêng
  - Send/Receive độc lập
  - Đóng mở không ảnh hưởng stream khác
  - Có thứ tự gói tin bảo đảm
```

### Trong code của project

#### Rust (Server)

```rust
// network/src/quic.rs

pub struct QuicConnection {
    connection: quinn::Connection,  // ← ĐẠI DIỆN CHO KẾT NỐI
}

pub struct QuicStreamHandler {
    framed: Framed<QuicStream, LengthDelimitedCodec>,  // ← ĐẠI DIỆN CHO 1 STREAM
}
```

**QuicConnection**:
- Đại diện cho **1 kết nối** giữa client và server
- Có thể chấp nhận **nhiều stream** liên tiếp
- Sống lâu, tái sử dụng cho nhiều request

**QuicStreamHandler**:
- Đại diện cho **1 stream** = **1 request**
- Chỉ xử lý **1 request/response**
- Sau khi gửi response → **ĐÓNG stream**
- Nhưng **Connection vẫn sống** để nhận stream mới

#### Go (Client)

```go
// processor/quic_client.go

type Connection = quic.Connection  // ← Kết nối QUIC

func SendChunkToRustServerQuic(conn quic.Connection, ...) error {
    stream, err := conn.OpenStreamSync(...)  // ← Mở 1 stream mới
    defer stream.Close()                     // ← Đóng stream sau khi xong
    
    // Gửi request
    writeFrameWithLength(stream, jsonData)
    
    // Đọc response
    responseData := readFrameWithLength(stream)
    
    // Stream tự động đóng (defer)
    return nil
}
```

**Quan trọng**:
- Client **MỞ stream mới** cho mỗi request
- Client **ĐÓNG stream** sau khi nhận response
- **Connection được tái sử dụng** cho request tiếp theo

---

## Flow hoạt động chi tiết

### Phase 1: Thiết lập Connection (1 lần)

```
Client (Go)                                Server (Rust)
    |                                           |
    |--- QUIC Handshake (TLS 1.3) ------------>|
    |<-- Connection Established ----------------|
    |                                           |
    | Connection ID: 0xABCD1234                 |
    | (Giữ mãi mãi, không đóng)                |
```

**Code Client**:
```go
// main.go
conn1, err := processor.CreateQuicConnection("localhost:7081")
// Kết nối này được giữ trong pool, dùng nhiều lần
```

**Code Server**:
```rust
// main.rs
let mut listener = transport.listen(addr).await?;

loop {
    let (connection, peer_addr) = listener.accept().await?;
    // 'connection' là QuicConnection, sẽ xử lý nhiều stream
    tokio::spawn(async move {
        server::handle_connection(connection, peer_addr, app).await
    });
}
```

### Phase 2: Gửi Request (Mỗi request = 1 stream mới)

```
Client                                          Server
  |                                                |
  |--- Open Stream 1 (ID: 0) -------------------->|
  |                                                |
  |--- [4-byte length][JSON request] ------------>|
  |                                                |
  |                         (Server xử lý logic)  |
  |                                                |
  |<-- [4-byte length][JSON response] ------------|
  |                                                |
  |<-- Stream 1 CLOSED (FIN) ----------------------|
  |                                                |
  | (Connection vẫn mở)                           |
  |                                                |
  |--- Open Stream 2 (ID: 2) -------------------->|  ← Request mới
  |--- [4-byte length][JSON request] ------------>|
  |<-- [4-byte length][JSON response] ------------|
  |<-- Stream 2 CLOSED (FIN) ----------------------|
  |                                                |
```

**Đặc điểm quan trọng**:
1. Mỗi request = **1 stream mới** (stream ID tăng dần: 0, 2, 4, ...)
2. Stream **chỉ tồn tại** trong thời gian xử lý 1 request
3. Stream **đóng ngay** sau khi gửi response
4. **Connection không đóng**, chờ stream tiếp theo

---

## Client-side (Go)

### 1. Tạo Connection (Pool)

```go
// file_handler.go (Server-side của blockchain)

const CONNECTION_POOL_SIZE = 10

type FileHandlerNoReceipt struct {
    connPool1  []quic.Connection  // Pool kết nối đến Server 1
    connPool2  []quic.Connection  // Pool kết nối đến Server 2
}

// Khởi tạo pool
func GetFileHandler(...) (*FileHandlerNoReceipt, error) {
    connPool1 := make([]quic.Connection, CONNECTION_POOL_SIZE)
    connPool2 := make([]quic.Connection, CONNECTION_POOL_SIZE)
    
    for i := 0; i < CONNECTION_POOL_SIZE; i++ {
        conn1, err := processor.CreateQuicConnection("localhost:7081")
        connPool1[i] = conn1
        
        conn2, err := processor.CreateQuicConnection("localhost:7082")
        connPool2[i] = conn2
    }
    
    // Các connection này được tái sử dụng nhiều lần
}
```

**Lưu ý**: 
- Tạo **10 connection** đến mỗi server
- Mỗi connection có thể xử lý **hàng nghìn stream**
- Connection được **tái sử dụng** cho nhiều request

### 2. Gửi Request qua Stream

```go
// quic_client.go

func SendChunkToRustServerQuic(
    conn quic.Connection,  // ← Connection đã tồn tại sẵn
    fileKey string,
    chunkIndex int,
    chunkData []byte,
    signature string,
) error {
    // ============================================
    // BƯỚC 1: MỞ STREAM MỚI
    // ============================================
    stream, err := conn.OpenStreamSync(context.Background())
    if err != nil {
        return fmt.Errorf("không thể mở stream: %v", err)
    }
    defer stream.Close()  // ← Đảm bảo đóng stream sau khi xong
    
    // ============================================
    // BƯỚC 2: TẠO REQUEST JSON
    // ============================================
    payload := models.UploadChunkPayload{
        FileKey:         fileKey,
        ChunkIndex:      chunkIndex,
        ChunkDataBase64: base64.StdEncoding.EncodeToString(chunkData),
        Signature:       signature,
    }
    command := models.Command{
        Command: "UploadChunk",
        Payload: payload,
    }
    
    jsonData, _ := json.Marshal(command)
    jsonData = append(jsonData, '\n')  // ← Thêm newline để Rust dễ parse
    
    // ============================================
    // BƯỚC 3: GỬI REQUEST VỚI LENGTH PREFIX
    // ============================================
    if err := writeFrameWithLength(stream, jsonData); err != nil {
        return err
    }
    
    // ============================================
    // BƯỚC 4: ĐỌC RESPONSE
    // ============================================
    responseData, err := readFrameWithLength(stream)
    if err != nil {
        return err
    }
    
    var response models.GenericResponse
    json.Unmarshal(responseData, &response)
    
    // ============================================
    // STREAM TỰ ĐỘNG ĐÓNG (defer stream.Close())
    // CONNECTION VẪN MỞ CHO REQUEST TIẾP THEO
    // ============================================
    
    return nil
}
```

### 3. Length-Delimited Protocol (Client)

```go
// writeFrameWithLength gửi data với 4-byte big-endian length prefix
func writeFrameWithLength(stream quic.Stream, data []byte) error {
    // Tính độ dài data
    length := uint32(len(data))
    lengthBuf := make([]byte, 4)
    binary.BigEndian.PutUint32(lengthBuf, length)
    
    // Gửi 4-byte length
    stream.Write(lengthBuf)  // [0x00, 0x00, 0x01, 0x2A] = 298 bytes
    
    // Gửi data thực
    stream.Write(data)       // [{"command":"UploadChunk",...}]
    
    return nil
}

// readFrameWithLength đọc data với 4-byte length prefix
func readFrameWithLength(stream quic.Stream) ([]byte, error) {
    // Đọc 4-byte length
    lengthBuf := make([]byte, 4)
    io.ReadFull(stream, lengthBuf)  // Block đến khi đọc đủ 4 bytes
    
    length := binary.BigEndian.Uint32(lengthBuf)
    
    // Đọc data (block đến khi đọc đủ 'length' bytes)
    data := make([]byte, length)
    io.ReadFull(stream, data)
    
    return data, nil
}
```

**Format trên wire**:
```
┌────────────┬─────────────────────────────────────┐
│ 4 bytes    │ N bytes                             │
│ (Length)   │ (Data)                              │
├────────────┼─────────────────────────────────────┤
│ 0x00000012 │ {"command":"test"}                  │
└────────────┴─────────────────────────────────────┘
     ↑                    ↑
  Big-Endian         JSON string
  18 bytes
```

---

## Server-side (Rust)

### 1. Accept Connection

```rust
// main.rs

#[tokio::main]
async fn main() {
    let transport = QuicTransport::new();
    let mut listener = transport.listen("127.0.0.1:7081".parse()?).await?;
    
    println!("🚀 QUIC server listening on 127.0.0.1:7081");
    
    // ============================================
    // VÒNG LẶP CHẤP NHẬN CONNECTION
    // ============================================
    loop {
        // Đợi kết nối mới từ client
        let (connection, peer_addr) = listener.accept().await?;
        // 'connection' = QuicConnection (có thể xử lý nhiều stream)
        
        let app_clone = app.clone();
        
        // Spawn task để xử lý connection này
        tokio::spawn(async move {
            println!("🚀 New connection from {}", peer_addr);
            
            // Hàm này sẽ chấp nhận NHIỀU stream liên tiếp
            server::handle_connection(connection, peer_addr, app_clone).await
        });
    }
}
```

### 2. Handle Connection (Accept Multiple Streams)

```rust
// server.rs

pub async fn handle_connection(
    mut connection: Box<dyn Connection>,
    peer: SocketAddr,
    app: Arc<App>,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    println!("[{}] 🔵 Connection accepted (Ready for streams)", peer);
    
    // ============================================
    // DOWNCAST đến QuicConnection
    // ============================================
    let quic_conn = connection
        .as_any_mut()
        .downcast_mut::<QuicConnection>()
        .ok_or("Failed to downcast")?;
    
    // ============================================
    // VÒNG LẶP CHẤP NHẬN STREAM
    // ============================================
    loop {
        // Đợi client mở stream mới
        let mut stream_handler = match quic_conn.accept_stream().await {
            Ok(handler) => {
                println!("[{}] ✅ New stream accepted", peer);
                handler
            }
            Err(e) => {
                // Kiểm tra lỗi đóng kết nối
                if let Some(io_err) = e.downcast_ref::<std::io::Error>() {
                    if io_err.kind() == std::io::ErrorKind::ConnectionAborted {
                        println!("[{}] 📭 Connection closed by client", peer);
                        break; // Thoát vòng lặp
                    }
                }
                eprintln!("[{}] ❌ Error accepting stream: {}", peer, e);
                break;
            }
        };
        
        // ============================================
        // XỬ LÝ 1 REQUEST TRÊN STREAM NÀY
        // ============================================
        handle_single_stream(&mut stream_handler, peer, &app).await?;
        
        // Stream đã đóng, quay lại đầu vòng lặp để chờ stream mới
    }
    
    println!("[{}] 🔵 Connection handler finished", peer);
    Ok(())
}
```

**Quan trọng**:
- `handle_connection` xử lý **1 Connection**
- Bên trong là **vòng lặp** chấp nhận **nhiều stream**
- Mỗi stream = 1 lần gọi `handle_single_stream`

### 3. Handle Single Stream

```rust
async fn handle_single_stream(
    stream_handler: &mut QuicStreamHandler,
    peer: SocketAddr,
    app: &Arc<App>,
) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    
    // ============================================
    // BƯỚC 1: ĐỌC REQUEST
    // ============================================
    let data = match stream_handler.recv().await? {
        Some(bytes) => bytes,
        None => {
            println!("[{}] ⚠️ Stream closed before data", peer);
            return Ok(());
        }
    };
    
    // Parse JSON (Client gửi kèm \n)
    let line = String::from_utf8_lossy(&data).trim().to_string();
    let command: Command = serde_json::from_str(&line)?;
    
    // ============================================
    // BƯỚC 2: XỬ LÝ COMMAND
    // ============================================
    match command {
        Command::UploadChunk { payload } => {
            println!("[{}] UploadChunk - chunk {}", peer, payload.chunk_index);
            
            // Xác thực signature
            verify_upload_chunk(&payload, app).await?;
            
            // Decode base64 → bytes
            let chunk_data = base64::decode(&payload.chunk_data_base64)?;
            
            // Lưu file vào disk
            let file_path = format!("storage/{}/{}", 
                payload.file_key, 
                payload.chunk_index
            );
            std::fs::write(&file_path, chunk_data)?;
            
            // ============================================
            // BƯỚC 3: GỬI RESPONSE
            // ============================================
            let response = GenericResponse {
                status: "SUCCESS".to_string(),
                message: "Chunk stored".to_string(),
            };
            
            let mut response_json = serde_json::to_vec(&response)?;
            response_json.push(b'\n');
            
            // ✅ GỬI RESPONSE VÀ ĐÓNG STREAM
            stream_handler.send(Bytes::from(response_json)).await?;
            
            println!("[{}] ✅ Response sent, stream closed", peer);
        }
        
        Command::DownloadChunkRequest { payload } => {
            // Tương tự...
        }
    }
    
    Ok(())
}
```

### 4. QuicStreamHandler (Rust)

```rust
// network/src/quic.rs

pub struct QuicStreamHandler {
    framed: Framed<QuicStream, LengthDelimitedCodec>,
    //      ↑        ↑          ↑
    //      |        |          └─ Tự động encode/decode length prefix
    //      |        └─ Wrapper cho SendStream + RecvStream
    //      └─ Helper từ tokio-util
}

impl QuicStreamHandler {
    // Đọc 1 frame (tự động đọc length prefix + data)
    pub async fn recv(&mut self) -> TransportResult<Option<Bytes>> {
        match self.framed.next().await {
            Some(Ok(bytes)) => Ok(Some(bytes.freeze())),
            Some(Err(e)) => Err(Box::new(e)),
            None => Ok(None),  // Stream đã đóng
        }
    }
    
    // Gửi 1 frame VÀ ĐÓNG STREAM
    pub async fn send(&mut self, data: Bytes) -> TransportResult<()> {
        self.framed.send(data).await?;      // Gửi data
        self.framed.close().await?;         // Đóng stream
        Ok(())
    }
}
```

**LengthDelimitedCodec**:
- Tự động thêm **4-byte length prefix** khi gửi
- Tự động đọc **4-byte length prefix** khi nhận
- Đảm bảo đọc đủ `length` bytes trước khi trả về
- **Tương thích 100%** với `writeFrameWithLength` của Go

---

## Length-Delimited Framing

### Tại sao cần Length Prefix?

**Vấn đề**: QUIC/TCP là **byte stream**, không có ranh giới message tự nhiên.

```
Không có length prefix:
    Stream: [{"cmd":"A"}{"cmd":"B"}{"cmd":"C"}]
            ↑
            Làm sao biết JSON đầu tiên kết thúc ở đâu?

Có length prefix:
    Stream: [0x0000000C]{"cmd":"A"}[0x0000000C]{"cmd":"B"}
            ↑          ↑           ↑
            12 bytes   JSON 1      12 bytes
```

### Format chi tiết

```
Packet structure:
┌──────────────┬────────────────────────────────────┐
│   4 bytes    │         N bytes                    │
│  (Big-Endian)│       (Payload)                    │
├──────────────┼────────────────────────────────────┤
│  0x00000020  │  {"command":"UploadChunk",...}     │
│      ↓       │                                     │
│   32 bytes   │  JSON data                          │
└──────────────┴────────────────────────────────────┘
```

### Implementation

**Go (Client)**:
```go
func writeFrameWithLength(stream quic.Stream, data []byte) error {
    length := uint32(len(data))
    lengthBuf := make([]byte, 4)
    binary.BigEndian.PutUint32(lengthBuf, length)
    
    stream.Write(lengthBuf)  // [0x00, 0x00, 0x00, 0x20]
    stream.Write(data)        // [{"command":...}]
    return nil
}
```

**Rust (Server)**:
```rust
use tokio_util::codec::LengthDelimitedCodec;

let codec = LengthDelimitedCodec::new();
let framed = Framed::new(stream, codec);

// Đọc (tự động parse length)
let data = framed.next().await?;

// Gửi (tự động thêm length)
framed.send(Bytes::from(response)).await?;
```

**Compatibility**:
- Go: `binary.BigEndian.PutUint32` → Big-Endian 4 bytes
- Rust: `LengthDelimitedCodec::new()` → Big-Endian 4 bytes (default)
- ✅ Hoàn toàn tương thích

---

## Lifecycle của một Request

### Ví dụ: Upload 1 chunk

```
┌─────────────────────────────────────────────────────────────────┐
│                    TIMELINE                                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  T=0ms    Client: conn.OpenStreamSync()                         │
│           ↓ Mở stream mới (ID: 42)                              │
│                                                                  │
│  T=1ms    Client: writeFrameWithLength(stream, jsonData)        │
│           ↓ Gửi [4-byte length][JSON]                           │
│           │                                                      │
│           ├──── Network (UDP packets) ────→                     │
│           │                                                      │
│  T=5ms    Server: quic_conn.accept_stream()                     │
│           ↓ Chấp nhận stream (ID: 42)                           │
│                                                                  │
│  T=6ms    Server: stream_handler.recv()                         │
│           ↓ Đọc [4-byte length] → Đọc JSON                      │
│                                                                  │
│  T=7ms    Server: Parse JSON → Command::UploadChunk             │
│                                                                  │
│  T=8ms    Server: verify_upload_chunk()                         │
│           ↓ Kiểm tra signature                                  │
│                                                                  │
│  T=10ms   Server: std::fs::write(chunk_path, data)              │
│           ↓ Ghi file vào disk                                   │
│                                                                  │
│  T=15ms   Server: stream_handler.send(response)                 │
│           ↓ Gửi [4-byte length][JSON response]                  │
│           ↓ ĐÓNG STREAM (FIN packet)                            │
│           │                                                      │
│           ├──── Network (UDP packets) ────→                     │
│           │                                                      │
│  T=20ms   Client: readFrameWithLength(stream)                   │
│           ↓ Nhận response                                        │
│                                                                  │
│  T=21ms   Client: stream.Close() (defer)                        │
│           ↓ Stream đóng hoàn toàn                               │
│                                                                  │
│  T=22ms   ✅ REQUEST HOÀN TẤT                                   │
│           Connection vẫn mở, sẵn sàng cho request tiếp theo     │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

### State của Connection và Stream

```
Time: T=0
    Connection: OPEN
    Streams: []

Time: T=1 (Client mở stream)
    Connection: OPEN
    Streams: [Stream 42 (OPEN)]

Time: T=15 (Server gửi response + đóng stream)
    Connection: OPEN
    Streams: [Stream 42 (HALF_CLOSED_SERVER)]
    
Time: T=21 (Client đóng stream)
    Connection: OPEN
    Streams: [Stream 42 (CLOSED)]
    
Time: T=22
    Connection: OPEN  ← VẪN MỞ!
    Streams: []       ← Stream đã bị cleanup

Time: T=100 (Client mở stream mới)
    Connection: OPEN
    Streams: [Stream 44 (OPEN)]  ← ID mới, tái sử dụng connection
```

---

## Ví dụ thực tế

### Scenario: Upload file 500KB (5 chunks x 100KB)

```
Client Pool:
    connPool1[0] = Connection to Server 1 (ID: 0xABCD)
    connPool1[1] = Connection to Server 1 (ID: 0xEF01)
    ...

Server 1:
    Listening on 127.0.0.1:7081
    Connection 0xABCD đang active
```

#### Upload Chunk 0 (Chẵn → Server 1)

```go
// file_handler.go
poolIndex := (0 / 2) % 10 = 0
conn := connPool1[0]  // Lấy connection từ pool

// Gửi chunk qua stream mới
SendChunkToRustServerQuic(conn, fileKey, 0, chunk0Data, signature)
```

**Trên wire**:
```
Client                                   Server 1
  |                                         |
  |--- Open Stream 0 on Conn 0xABCD -----→|
  |                                         |
  |--- [0x00000132][JSON UploadChunk] --->|
  |    Length: 306 bytes                   |
  |    Payload: {"command":"UploadChunk",  |
  |              "fileKey":"abc...",       |
  |              "chunkIndex":0,...}       |
  |                                         |
  |                    (Server lưu file)   |
  |                                         |
  |<-- [0x00000025][JSON SUCCESS] ---------|
  |    Length: 37 bytes                    |
  |    {"status":"SUCCESS",...}            |
  |                                         |
  |<-- Stream 0 CLOSED (FIN) --------------|
  |                                         |
  | Conn 0xABCD vẫn mở                     |
```

#### Upload Chunk 1 (Lẻ → Server 2)

```go
poolIndex := (1 / 2) % 10 = 0
conn := connPool2[0]  // Connection khác (đến Server 2)

SendChunkToRustServerQuic(conn, fileKey, 1, chunk1Data, signature)
```

#### Upload Chunk 2 (Chẵn → Server 1, TÁI SỬ DỤNG Connection)

```go
poolIndex := (2 / 2) % 10 = 1
conn := connPool1[1]  // Connection khác trong pool

// HOẶC nếu index = 0:
conn := connPool1[0]  // TÁI SỬ DỤNG connection cũ!

SendChunkToRustServerQuic(conn, fileKey, 2, chunk2Data, signature)
```

**Trên wire** (Cùng connection 0xABCD):
```
Client                                   Server 1
  |                                         |
  | (Conn 0xABCD vẫn đang mở)              |
  |                                         |
  |--- Open Stream 2 on Conn 0xABCD -----→|  ← Stream mới!
  |                                         |
  |--- [0x00000132][JSON chunk 2] -------->|
  |                                         |
  |<-- [0x00000025][JSON SUCCESS] ---------|
  |<-- Stream 2 CLOSED --------------------|
  |                                         |
  | Conn 0xABCD vẫn mở                     |
```

**Lợi ích**:
- Không cần handshake lại (đã có connection)
- Latency thấp hơn (0-RTT cho request tiếp theo)
- Giảm overhead CPU/memory

---

## So sánh với TCP/HTTP

### HTTP/1.1 (TCP)

```
Sequence:
    1. Client mở connection TCP (3-way handshake)
    2. Client gửi request
    3. Server trả response
    4. Client đóng connection (hoặc keep-alive)
    5. Request mới → Phải chờ response cũ xong

Vấn đề:
    ❌ Head-of-Line Blocking: Request 2 phải chờ Request 1
    ❌ Mỗi connection = 1 request đang xử lý
    ❌ Latency cao (3-way handshake mỗi lần)
```

### HTTP/2 (TCP)

```
Improvements:
    ✅ Multiplexing: Nhiều stream trên 1 connection
    ✅ Header compression
    
Vấn đề:
    ⚠️ Vẫn bị TCP Head-of-Line Blocking
       (Packet loss ở stream 1 → blocking toàn bộ connection)
    ⚠️ Handshake vẫn cần TLS 1.2 (2-RTT)
```

### QUIC (This Project)

```
Architecture:
    ✅ 1 Connection UDP = Nhiều stream độc lập
    ✅ 0-RTT handshake (cho connection cũ)
    ✅ Không bị Head-of-Line Blocking
    ✅ Stream level flow control
    ✅ Connection migration (IP thay đổi vẫn OK)

Example:
    Client mở 10 connection
    Mỗi connection xử lý 1000 stream/giây
    → 10,000 request/giây

Performance:
    TCP:  ~1000 req/s (với keep-alive)
    QUIC: ~10,000 req/s (với connection pool)
```

### Bảng so sánh

| Tính năng | TCP | HTTP/2 | QUIC (Project) |
|-----------|-----|--------|----------------|
| **Connection setup** | 3-way handshake | TLS 1.2 (2-RTT) | TLS 1.3 (1-RTT/0-RTT) |
| **Multiplexing** | ❌ Không | ✅ Có | ✅ Có |
| **Head-of-Line Blocking** | ❌ Nghiêm trọng | ⚠️ Ở TCP layer | ✅ Không có |
| **Stream isolation** | ❌ Không | ⚠️ Partial | ✅ Hoàn toàn |
| **Connection reuse** | ⚠️ Keep-alive | ✅ Có | ✅ Có + Pool |
| **Latency (request mới)** | ~50-100ms | ~20-50ms | ~1-5ms (0-RTT) |
| **Throughput** | ~1K req/s | ~5K req/s | ~10K req/s |

---

## Best Practices

### 1. Connection Pooling

```go
// ✅ TỐT: Tái sử dụng connection
const POOL_SIZE = 10
connPool := make([]quic.Connection, POOL_SIZE)

for i := 0; i < POOL_SIZE; i++ {
    connPool[i] = CreateQuicConnection("server:7081")
}

// Dùng round-robin hoặc hash
poolIndex := chunkIndex % POOL_SIZE
conn := connPool[poolIndex]
```

```go
// ❌ TỆ: Tạo connection mới mỗi request
for i := 0; i < 1000; i++ {
    conn := CreateQuicConnection("server:7081")  // Handshake mỗi lần!
    SendChunk(conn, ...)
    conn.Close()  // Lãng phí!
}
```

### 2. Stream Cleanup

```go
// ✅ TỐT: Dùng defer
stream, _ := conn.OpenStreamSync(ctx)
defer stream.Close()

// Gửi/nhận data...
```

```rust
// ✅ TỐT: stream_handler.send() tự động đóng
stream_handler.send(response).await?;
// Stream đã đóng, không cần cleanup
```

### 3. Error Handling

```go
// ✅ TỐT: Kiểm tra lỗi connection và retry
conn, err := CreateQuicConnection(addr)
if err != nil {
    // Thử lại hoặc lấy connection khác từ pool
    conn = getHealthyConnection(pool)
}
```

```rust
// ✅ TỐT: Phân biệt lỗi đóng connection bình thường
match quic_conn.accept_stream().await {
    Err(e) if is_connection_closed(&e) => {
        println!("Connection closed gracefully");
        break;
    }
    Err(e) => {
        eprintln!("Real error: {}", e);
    }
    Ok(stream) => { /* xử lý */ }
}
```

### 4. Monitoring

```rust
// ✅ TỐT: Log thời gian xử lý
let start = Instant::now();
handle_single_stream(&mut stream_handler, peer, &app).await?;
let duration = start.elapsed();
println!("[{}] Stream processed in {:?}", peer, duration);
```

---

## Performance Tuning

### QUIC Configuration (Rust)

```rust
// network/src/quic.rs

let mut transport_config = TransportConfig::default();

// Tăng số stream đồng thời
transport_config.max_concurrent_bidi_streams(VarInt::from_u32(100_000));

// Tăng receive buffer
const MAX_STREAM_WINDOW: u32 = 20 * 1024 * 1024;  // 20MB
const MAX_CONN_WINDOW: u32 = 40 * 1024 * 1024;    // 40MB
transport_config.stream_receive_window(VarInt::from_u32(MAX_STREAM_WINDOW));
transport_config.receive_window(VarInt::from_u32(MAX_CONN_WINDOW));

// Keep-alive để giữ connection
transport_config.keep_alive_interval(Some(Duration::from_secs(5)));
transport_config.max_idle_timeout(Some(Duration::from_secs(60).try_into()?));
```

### Go Client Tuning

```go
// Connection pool size dựa trên workload
const POOL_SIZE = 10  // Cho ~1000 req/s
const POOL_SIZE = 50  // Cho ~5000 req/s
const POOL_SIZE = 100 // Cho ~10000 req/s

// Timeout cho từng request
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

stream, err := conn.OpenStreamSync(ctx)
```

---

## Kết luận

### Điểm mạnh của kiến trúc này

1. **Connection Reuse**: 1 connection xử lý hàng nghìn request
2. **Stream Isolation**: Lỗi ở stream này không ảnh hưởng stream khác
3. **Low Latency**: 0-RTT cho request tiếp theo
4. **High Throughput**: 10,000+ request/s với pool size 10
5. **Simple Protocol**: Length-delimited JSON, dễ debug
6. **Type Safety**: Command pattern với Rust enums

### Khi nào nên dùng?

✅ **Phù hợp**:
- Hệ thống phân tán, nhiều request nhỏ
- Cần low latency (gaming, real-time)
- Upload/download file với nhiều chunk song song
- Microservices communication

❌ **Không phù hợp**:
- Chỉ có 1-2 request lớn
- Môi trường firewall chặn UDP
- Legacy system chỉ hỗ trợ TCP

---

## Tài liệu tham khảo

- [QUIC Protocol (RFC 9000)](https://www.rfc-editor.org/rfc/rfc9000.html)
- [quinn - Rust QUIC Implementation](https://github.com/quinn-rs/quinn)
- [quic-go - Go QUIC Implementation](https://github.com/quic-go/quic-go)
- [tokio-util LengthDelimitedCodec](https://docs.rs/tokio-util/latest/tokio_util/codec/length_delimited/index.html)

**Project files**:
- Client: `hybrid-storage-demo/client_go_rpc/processor/quic_client.go`
- Server: `hybrid-storage-demo/storage-rust/src/server.rs`
- Network: `hybrid-storage-demo/network/src/quic.rs`

---

**Tác giả**: MetaNode Blockchain Team  
**Ngày cập nhật**: 2025-10-31  
**Version**: 1.0
