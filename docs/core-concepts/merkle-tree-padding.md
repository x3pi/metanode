# Merkle Tree Padding - Hướng dẫn chi tiết

## 📋 Mục lục
1. [Merkle Tree là gì?](#merkle-tree-là-gì)
2. [Cây Merkle thường (Non-Padded)](#cây-merkle-thường-non-padded)
3. [Cây Merkle Padding](#cây-merkle-padding)
4. [So sánh Padding vs Non-Padding](#so-sánh-padding-vs-non-padding)
5. [Implementation trong Project](#implementation-trong-project)
6. [Ví dụ cụ thể](#ví-dụ-cụ-thể)

---

## Merkle Tree là gì?

**Merkle Tree** (hay Hash Tree) là một cấu trúc dữ liệu dạng cây nhị phân, trong đó:
- Mỗi **lá (leaf)** chứa hash của một chunk dữ liệu
- Mỗi **node cha** chứa hash kết hợp từ 2 node con
- **Root** (gốc) là hash cuối cùng đại diện cho toàn bộ dữ liệu

### Mục đích sử dụng
- ✅ Xác thực tính toàn vẹn dữ liệu
- ✅ Chứng minh một chunk thuộc về file mà không cần gửi toàn bộ file
- ✅ Phát hiện dữ liệu bị sửa đổi
- ✅ Tối ưu băng thông trong hệ thống phân tán

---

## Cây Merkle thường (Non-Padded)

### Đặc điểm
- Số lượng lá = số lượng chunk thực tế
- Nếu số lá **không phải lũy thừa của 2**, cây sẽ **không cân bằng**
- Node cuối cùng lẻ có thể được xử lý theo nhiều cách:
  - Duplicate (nhân đôi)
  - Hash với chính nó
  - Promote lên cấp trên

### Ví dụ: File có 5 chunks

```
Chunks: [C0, C1, C2, C3, C4]

                    Root
                   /    \
                H01      H234
               /  \      /   \
             H0   H1   H23   H4  ← C4 lẻ, không có sibling
            /  \  / \  / \
           C0 C1 C2 C3 C4
```

### Vấn đề
1. **Logic phức tạp**: Xử lý trường hợp lẻ khác nhau ở mỗi level
2. **Khó đồng bộ**: Client và server phải implement logic giống hệt nhau
3. **Khó tính toán index**: Công thức `index >> level` không hoạt động đúng

---

## Cây Merkle Padding

### Đặc điểm
- Số lượng lá **luôn là lũy thừa của 2** (1, 2, 4, 8, 16, 32, ...)
- Các lá thiếu được **đệm (pad)** bằng `emptyHash`
- Cây **luôn cân bằng hoàn toàn**
- Logic xác minh **đơn giản và nhất quán**

### Công thức Padding
```go
// Tính số lá cần thiết (lũy thừa của 2)
nextPowerOfTwo := 2^(ceil(log2(numChunks)))

Ví dụ:
- 1 chunk  → 1 lá
- 2 chunks → 2 lá
- 3 chunks → 4 lá (pad thêm 1)
- 5 chunks → 8 lá (pad thêm 3)
- 9 chunks → 16 lá (pad thêm 7)
```

### Ví dụ: File có 5 chunks với Padding

```
Chunks thực: [C0, C1, C2, C3, C4]
Chunks sau padding: [C0, C1, C2, C3, C4, E, E, E]  ← E = emptyHash

                        Root
                       /    \
                    H0123    H4567
                   /    \    /    \
                H01    H23  H45   H67
               /  \   /  \  / \   / \
              H0  H1 H2  H3 H4 E  E  E
              ↓   ↓  ↓   ↓  ↓  ↓  ↓  ↓
             C0  C1 C2  C3 C4  E  E  E
```

**Lưu ý**: `E` là hash của chuỗi rỗng:
```go
emptyHash := sha256.Sum256([]byte{})
// = e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
```

---

## So sánh Padding vs Non-Padding

| Tiêu chí | Non-Padded Tree | Padded Tree |
|----------|----------------|-------------|
| **Số lá** | = Số chunk thực tế | = Lũy thừa của 2 gần nhất |
| **Cấu trúc cây** | Có thể không cân bằng | Luôn cân bằng hoàn toàn |
| **Độ phức tạp logic** | Cao (xử lý trường hợp lẻ) | Thấp (logic đơn giản) |
| **Tính index** | Phức tạp | Đơn giản: `index >> level` |
| **Proof size** | Nhỏ hơn một chút | Lớn hơn 1 hash ở một số level |
| **Tính đồng bộ** | Khó (dễ sai khác) | Dễ (luôn nhất quán) |
| **Storage overhead** | Không | Minimal (chỉ lưu emptyHash) |

---

## Implementation trong Project

### 1. Build Merkle Tree với Padding

```go
func buildMerkleTreePadded(chunks [][]byte) ([][]byte, []byte) {
    numLeaves := len(chunks)
    if numLeaves == 0 {
        emptyHash := sha256.Sum256(nil)
        return nil, emptyHash[:]
    }
    
    // Bước 1: Tính số lá cần thiết (lũy thừa của 2)
    nextPowerOfTwo := calculateNextPowerOfTwo(numLeaves)
    leaves := make([][]byte, nextPowerOfTwo)
    
    // Bước 2: Hash các chunk thực
    for i := 0; i < numLeaves; i++ {
        hash := sha256.Sum256(chunks[i])
        leaves[i] = hash[:]
    }
    
    // Bước 3: Đệm các lá còn lại bằng emptyHash
    emptyHash := sha256.Sum256([]byte{})
    for i := numLeaves; i < nextPowerOfTwo; i++ {
        leaves[i] = emptyHash[:]
    }
    
    // Bước 4: Xây dựng cây từ dưới lên
    treeLevel := leaves
    for len(treeLevel) > 1 {
        var nextLevel [][]byte
        for i := 0; i < len(treeLevel); i += 2 {
            h := sha256.New()
            h.Write(treeLevel[i])   // Node trái
            h.Write(treeLevel[i+1]) // Node phải
            nextLevel = append(nextLevel, h.Sum(nil))
        }
        treeLevel = nextLevel
    }
    
    return leaves, treeLevel[0] // Trả về lá và root
}
```

### 2. Tạo Merkle Proof

```go
func getMerkleProofPadded(paddedLeaves [][]byte, chunkIndex int) [][32]byte {
    var proof [][32]byte
    treeLevel := paddedLeaves
    currentIndex := chunkIndex
    
    for len(treeLevel) > 1 {
        var nextLevel [][]byte
        for i := 0; i < len(treeLevel); i += 2 {
            var siblingIndex int
            if currentIndex == i {
                siblingIndex = i + 1 // Sibling bên phải
            } else if currentIndex == i+1 {
                siblingIndex = i     // Sibling bên trái
            } else {
                // Không liên quan đến chunk hiện tại
                h := sha256.New()
                h.Write(treeLevel[i])
                h.Write(treeLevel[i+1])
                nextLevel = append(nextLevel, h.Sum(nil))
                continue
            }
            
            // Lấy sibling hash để làm proof
            var sibling32 [32]byte
            copy(sibling32[:], treeLevel[siblingIndex])
            proof = append(proof, sibling32)
            
            // Tính hash cha
            h := sha256.New()
            h.Write(treeLevel[i])   // Trái
            h.Write(treeLevel[i+1]) // Phải
            nextLevel = append(nextLevel, h.Sum(nil))
        }
        treeLevel = nextLevel
        currentIndex = currentIndex / 2 // Di chuyển lên level cha
    }
    
    return proof
}
```

### 3. Xác minh Merkle Proof (Server-side)

```go
// File: file_handler.go
func verifyMerkleProof(
    chunkData []byte,
    chunkIndex *big.Int,
    merkleProofHashes [][32]byte,
    merkleRoot [32]byte,
) error {
    // Bước 1: Hash chunk data
    leafHash32 := sha256.Sum256(chunkData)
    computedHash := leafHash32[:]
    
    // Bước 2: Duyệt qua từng level của proof
    for level := 0; level < len(merkleProofHashes); level++ {
        siblingHash := merkleProofHashes[level]
        hash := sha256.New()
        
        // Tính vị trí của node ở level hiện tại
        levelIndex := chunkIndex.Uint64() >> uint(level)
        
        if levelIndex%2 == 0 {
            // Node hiện tại là con TRÁI → sibling ở PHẢI
            hash.Write(computedHash)
            hash.Write(siblingHash[:])
        } else {
            // Node hiện tại là con PHẢI → sibling ở TRÁI
            hash.Write(siblingHash[:])
            hash.Write(computedHash)
        }
        
        computedHash = hash.Sum(nil)
    }
    
    // Bước 3: So sánh với merkle root
    if !bytes.Equal(computedHash, merkleRoot[:]) {
        return fmt.Errorf("merkle proof không hợp lệ")
    }
    
    return nil
}
```

---

## Ví dụ cụ thể

### Scenario: Upload file 500KB với chunk size 100KB

#### Thông tin file
```
File size: 500KB
Chunk size: 100KB
Số chunks: 5
Chunks: [C0, C1, C2, C3, C4]
```

#### Bước 1: Padding
```
Số chunks thực: 5
Lũy thừa của 2 gần nhất: 8
Cần pad thêm: 3 chunks
```

#### Bước 2: Xây dựng cây

```
Level 3 (Root):                    Root
                                   /    \
Level 2:                        H0123    H4567
                               /    \    /    \
Level 1:                    H01    H23  H45   H67
                           /  \   /  \  / \   / \
Level 0 (Leaves):        H0  H1 H2  H3 H4 E  E  E
                         ↓   ↓  ↓   ↓  ↓  ↓  ↓  ↓
Chunk Data:             C0  C1 C2  C3 C4  ∅  ∅  ∅
Index:                   0   1  2   3  4  5  6  7
```

#### Bước 3: Tạo Proof cho Chunk 2

**Chunk Index = 2**

| Level | Index tại level | Sibling Index | Sibling Hash | Position |
|-------|----------------|---------------|--------------|----------|
| 0     | 2              | 3             | H3           | Right    |
| 1     | 1              | 0             | H01          | Left     |
| 2     | 0              | 1             | H4567        | Right    |

**Merkle Proof = [H3, H01, H4567]**

#### Bước 4: Xác minh Proof

```
Start: computedHash = H2 = SHA256(C2)

Level 0: index=2, levelIndex = 2 >> 0 = 2 (chẵn → trái)
  computedHash = SHA256(H2 + H3) = H23 ✓

Level 1: index=2, levelIndex = 2 >> 1 = 1 (lẻ → phải)
  computedHash = SHA256(H01 + H23) = H0123 ✓

Level 2: index=2, levelIndex = 2 >> 2 = 0 (chẵn → trái)
  computedHash = SHA256(H0123 + H4567) = Root ✓

So sánh: computedHash == merkleRoot? ✅ YES → Valid!
```

---

## Lợi ích của Padding trong Project

### 1. Logic đơn giản và nhất quán
```go
// Tính vị trí node ở mỗi level
levelIndex := chunkIndex >> level

// Xác định trái/phải
if levelIndex % 2 == 0 {
    // Trái: hash(current + sibling)
} else {
    // Phải: hash(sibling + current)
}
```

### 2. Đồng bộ giữa Client và Server
- Client (Go): `buildMerkleTreePadded()`
- Server (Go): `verifyMerkleProof()`
- Cả hai dùng cùng logic → **không bao giờ sai khác**

### 3. Dễ debug
- Số lá luôn là 2^n → dễ visualize
- Index mapping rõ ràng
- Không có trường hợp đặc biệt

### 4. Tương thích Smart Contract
```solidity
// Contract có thể verify proof đơn giản
for (uint i = 0; i < proof.length; i++) {
    uint levelIndex = chunkIndex >> i;
    if (levelIndex % 2 == 0) {
        computedHash = keccak256(abi.encodePacked(computedHash, proof[i]));
    } else {
        computedHash = keccak256(abi.encodePacked(proof[i], computedHash));
    }
}
```

---

## Kết luận

### Khi nào dùng Padding?
✅ Hệ thống phân tán với nhiều nodes xác minh  
✅ Cần logic đơn giản, ít bug  
✅ Tích hợp với Smart Contract  
✅ Dữ liệu lớn, số chunks không cố định  

### Khi nào dùng Non-Padding?
❌ Rất ít khi nên dùng  
⚠️ Chỉ khi tối ưu storage cực kỳ quan trọng  
⚠️ Hệ thống đơn giản, 1 bên verify  

### Best Practice
> **Luôn dùng Merkle Tree Padding** cho hệ thống blockchain/distributed storage để đảm bảo tính nhất quán và giảm thiểu lỗi logic.

---

## Tài liệu tham khảo

- [Wikipedia - Merkle Tree](https://en.wikipedia.org/wiki/Merkle_tree)
- [Ethereum Documentation - Merkle Proofs](https://ethereum.org/en/developers/tutorials/merkle-proofs-for-offline-data-integrity/)
- Project implementation:
  - `client_go_rpc/main.go`: Client-side implementation
  - `cmd/simple_chain/processor/contract_processor/file_handler.go`: Server-side verification

---

**Tác giả**: MetaNode Blockchain Team  
**Ngày cập nhật**: 2025-10-31
