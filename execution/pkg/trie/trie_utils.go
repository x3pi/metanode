package trie

import (
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"os"

	e_common "github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/pkg/trie/node"
)

// ExportTrie xuất Trie vào file.
func ExportTrie(t *MerklePatriciaTrie, filePath string) error {
	data := TrieData{
		RootHash:  t.Hash(),
		KeyValues: make(map[string][]byte),
	}

	// Lấy tất cả key-value từ Trie
	allData, err := t.GetAll()
	if err != nil {
		return fmt.Errorf("không thể lấy tất cả dữ liệu từ Trie: %v", err)
	}

	data.KeyValues = allData

	// Mở file để ghi
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("không thể tạo file: %v", err)
	}
	defer file.Close()

	// Mã hóa và ghi dữ liệu vào file
	encoder := gob.NewEncoder(file)
	if err := encoder.Encode(data); err != nil {
		return fmt.Errorf("lỗi mã hóa dữ liệu vào file: %v", err)
	}

	fmt.Printf("✅ Đã xuất Trie vào file: %s\n", filePath)
	return nil
}

// ImportTrie khôi phục Trie từ file.
func ImportTrie(filePath string) (*MerklePatriciaTrie, error) {
	// Mở file để đọc
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("không thể mở file: %v", err)
	}
	defer file.Close()

	// Giải mã dữ liệu từ file
	var data TrieData
	decoder := gob.NewDecoder(file)
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("lỗi giải mã dữ liệu từ file: %v", err)
	}

	// Tạo một MerklePatriciaTrie mới với cơ sở dữ liệu memory
	// Use empty root: we rebuild the trie from scratch via Update() below.
	db := storage.NewMemoryDb()
	t, err := New(e_common.Hash{}, db, true)
	if err != nil {
		return nil, fmt.Errorf("không thể tạo Trie mới: %v", err)
	}

	// Cập nhật dữ liệu vào Trie
	for key, value := range data.KeyValues {
		err := t.Update([]byte(key), value)
		if err != nil {
			return nil, fmt.Errorf("lỗi cập nhật key %s: %v", key, err)
		}
	}

	fmt.Printf("✅ Đã nhập Trie từ file: %s\n", filePath)
	return t, nil
}
func (t *MerklePatriciaTrie) GetStorageKeys() []e_common.Hash {
	rootHashNode, _ := t.root.Cache()
	root := e_common.BytesToHash(rootHashNode)
	// copy to new trie with root hash
	trie := &MerklePatriciaTrie{
		reader: t.reader,
		isHash: t.isHash,
		tracer: newTracer(),
	}
	if root != (e_common.Hash{}) && root != EmptyRootHash {
		rootnode, err := trie.resolveAndTrack(root[:], nil)
		if err != nil {
			logger.Error("error when resolve and track root node", err)
			return nil
		}
		trie.root = rootnode
	}
	// not yet process nodes: which nodes can have hash node in it
	unprocessedNodes := []node.Node{trie.root}
	storageKeys := []e_common.Hash{root}
	for len(unprocessedNodes) > 0 {
		uNode := unprocessedNodes[0]
		unprocessedNodes = unprocessedNodes[1:]
		switch n := uNode.(type) {
		case *node.ShortNode:
			switch n.Val.(type) {
			case node.HashNode:
				storageKeys = append(storageKeys, e_common.BytesToHash(n.Val.(node.HashNode)))
				rn, err := t.resolveAndTrack(n.Val.(node.HashNode), nil)
				if err != nil {
					logger.Error("error when resolve and track short node", err)
				}
				n.Val = rn
			default:
			}
			unprocessedNodes = append(unprocessedNodes, n.Val)
		case *node.FullNode:
			for i, child := range n.Children {
				if child != nil {
					switch child.(type) {
					case node.HashNode:
						storageKeys = append(storageKeys, e_common.BytesToHash(child.(node.HashNode)))
						var err error
						n.Children[i], err = t.resolveAndTrack(child.(node.HashNode), nil)
						if err != nil {
							hash, _ := n.Cache()
							logger.Error("error when resolve and track full node", err, n, hex.EncodeToString(hash))
							logger.DebugP("index", i)
						}
					default:
					}
					unprocessedNodes = append(unprocessedNodes, n.Children[i])
				}
			}
		case node.HashNode:
			logger.Warn("String function revice hashNode:", n)
			storageKeys = append(storageKeys, e_common.BytesToHash(n))
			rn, err := t.resolveAndTrack(n, nil) // nil prefix because we dont need it to able to solve hash node
			if err != nil {
				logger.Error("error when resolve and track hash node", err)
			}
			unprocessedNodes = append(unprocessedNodes, rn)
		case node.ValueNode:
		}
	}
	return storageKeys
}

func (t *MerklePatriciaTrie) String() string {
	rootHashNode, _ := t.root.Cache()
	root := e_common.BytesToHash(rootHashNode)
	// copy to new trie with root hash
	trie := &MerklePatriciaTrie{
		reader: t.reader,
		isHash: t.isHash,
		tracer: newTracer(),
	}

	if root != (e_common.Hash{}) && root != EmptyRootHash {
		rootnode, err := trie.resolveAndTrack(root[:], nil)
		if err != nil {
			logger.Error("error when resolve and track root node", err)
			return ""
		}
		trie.root = rootnode
	}
	// not yet process nodes: which nodes can have hash node in it
	unprocessedNodes := []node.Node{trie.root}
	for len(unprocessedNodes) > 0 {
		uNode := unprocessedNodes[0]
		unprocessedNodes = unprocessedNodes[1:]
		switch n := uNode.(type) {
		case *node.ShortNode:
			switch n.Val.(type) {
			case node.HashNode:
				rn, err := t.resolveAndTrack(n.Val.(node.HashNode), nil)
				if err != nil {
					logger.Error("error when resolve and track short node", err)
					continue
				}
				n.Val = rn
			default:
			}
			unprocessedNodes = append(unprocessedNodes, n.Val)
		case *node.FullNode:
			for i, child := range n.Children {
				if child != nil {
					switch child.(type) {
					case node.HashNode:
						var err error
						n.Children[i], err = t.resolveAndTrack(child.(node.HashNode), nil)
						if err != nil {
							hash, _ := n.Cache()
							logger.Error("error when resolve and track full node", err, n, hex.EncodeToString(hash))
							logger.DebugP("index", i)
						}
					default:
					}
					unprocessedNodes = append(unprocessedNodes, n.Children[i])
				}
			}
		case node.HashNode:
			logger.Warn("String function revice hashNode:", n)
			rn, err := t.resolveAndTrack(n, nil) // nil prefix because we dont need it to able to solve hash node
			if err != nil {
				logger.Error("error when resolve and track hash node", err)
			}
			unprocessedNodes = append(unprocessedNodes, rn)

		case node.ValueNode:
			logger.Debug("value node:", hex.EncodeToString(n))
		}
	}
	return trie.root.FString("==>")
}

func GetRootHash(
	data map[string][]byte,
) (e_common.Hash, error) {
	// create mem storage
	memStorage := storage.NewMemoryDb()
	trie, err := New(e_common.Hash{}, memStorage, false)
	if err != nil {
		logger.Error("error when create new trie", err)
		return e_common.Hash{}, err
	}
	for k, v := range data {
		trie.Update(e_common.FromHex(k), v)
	}
	hash := trie.Hash()
	memStorage = nil
	trie = nil
	return hash, nil
}

func (t *MerklePatriciaTrie) GetKeyValue() (map[string][]byte, error) {
	if t.committed {
		return nil, ErrCommitted
	}

	data := make(map[string][]byte)
	err := t.getKeyValue(t.root, nil, data)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (t *MerklePatriciaTrie) getKeyValue(
	origNode node.Node,
	prefix []byte,
	data map[string][]byte,
) error {
	switch n := (origNode).(type) {
	case nil:
		return nil
	case node.ValueNode:
		// Key is the path from the root to this node
		key := append(prefix, node.KeybytesToHex(crypto.Keccak256(prefix))...)
		data[hex.EncodeToString(key)] = n
		return nil
	case *node.ShortNode:
		// Key is the path from the root to this node
		key := append(prefix, n.Key...)
		// Recursively traverse the value node
		err := t.getKeyValue(n.Val, key, data)
		if err != nil {
			fmt.Print("🟡")
			return err
		}
		return nil
	case *node.FullNode:
		for i, child := range n.Children {
			if child != nil {
				// Recursively traverse each child node
				err := t.getKeyValue(child, append(prefix, byte(i)), data)
				if err != nil {
					fmt.Print("🟢")
					return err
				}
			}
		}
		return nil
	case node.HashNode:
		// Resolve the hash node and recursively traverse it
		child, err := t.resolveAndTrack(n, prefix)
		if err != nil {
			fmt.Print("🔴")
			return err
		}
		return t.getKeyValue(child, prefix, data)
	default:
		return fmt.Errorf("%T: invalid node: %v", origNode, origNode)
	}
}
func (t *MerklePatriciaTrie) GetAll() (map[string][]byte, error) {
	if t.committed {
		return nil, ErrCommitted
	}

	data := make(map[string][]byte)
	err := t.getAll(t.root, nil, data)
	if err != nil {
		return nil, err
	}

	return data, nil
}

func (t *MerklePatriciaTrie) getAll(
	origNode node.Node,
	prefix []byte,
	data map[string][]byte,
) error {
	switch n := (origNode).(type) {
	case nil:
		return nil
	case node.ValueNode:
		key := hex.EncodeToString(append(prefix, node.KeybytesToHex(crypto.Keccak256(prefix))...))
		data[key] = n
		return nil
	case *node.ShortNode:
		key := append(prefix, n.Key...)
		err := t.getAll(n.Val, key, data)
		if err != nil {
			return err
		}
		return nil
	case *node.FullNode:
		for i, child := range n.Children {
			if child != nil {
				err := t.getAll(child, append(prefix, byte(i)), data)
				if err != nil {
					return err
				}
			}
		}
		return nil
	case node.HashNode:
		child, err := t.resolveAndTrack(n, prefix)
		if err != nil {
			return err
		}
		return t.getAll(child, prefix, data)
	default:
		return fmt.Errorf("%T: invalid node: %v", origNode, origNode)
	}
}

// Count trả về tổng số cặp key-value trong trie.
func (t *MerklePatriciaTrie) Count() (int, error) {
	if t.committed {
		return 0, ErrCommitted
	}

	count := 0
	// Sử dụng một con trỏ tới count để hàm đệ quy có thể cập nhật nó
	err := t.countRecursive(t.root, nil, &count)
	if err != nil {
		return 0, fmt.Errorf("lỗi khi đếm phần tử: %w", err)
	}

	return count, nil
}

// countRecursive là hàm đệ quy giúp đếm số lượng phần tử.
func (t *MerklePatriciaTrie) countRecursive(
	origNode node.Node,
	prefix []byte,
	count *int, // Sử dụng con trỏ để cập nhật giá trị count
) error {
	switch n := (origNode).(type) {
	case nil:
		return nil // Nút rỗng, không làm gì cả
	case node.ValueNode:
		*count++ // Gặp nút giá trị (lá), tăng bộ đếm
		return nil
	case *node.ShortNode:
		// Tiếp tục duyệt xuống nút con (Val) với prefix được cập nhật
		key := append(prefix, n.Key...)
		return t.countRecursive(n.Val, key, count)
	case *node.FullNode:
		// Duyệt qua tất cả các nút con không rỗng
		for i, child := range n.Children {
			if child != nil {
				err := t.countRecursive(child, append(prefix, byte(i)), count)
				if err != nil {
					return err // Nếu có lỗi ở nhánh con, trả về lỗi
				}
			}
		}
		return nil
	case node.HashNode:
		// Giải quyết nút hash và tiếp tục duyệt
		child, err := t.resolveAndTrack(n, prefix)
		if err != nil {
			logger.Error("Lỗi khi resolve hash node trong lúc đếm:", err, "Prefix:", hex.EncodeToString(prefix))
			return fmt.Errorf("không thể resolve hash node %s: %w", hex.EncodeToString(n), err)
		}
		return t.countRecursive(child, prefix, count)
	default:
		// Trường hợp không mong muốn
		return fmt.Errorf("%T: kiểu node không hợp lệ khi đếm: %v", origNode, origNode)
	}
}
