package parentchain

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

const (
	ParentChainID uint64 = 990
)

var (
	ParentChainGatewayAddress = common.HexToAddress("0x0000000000000000000000000000000000001003")

	ErrInvalidChainID     = errors.New("parentchain: invalid chain ID, expected 990")
	ErrInvalidToAddress   = errors.New("parentchain: invalid ToAddress, must be gateway contract")
	ErrWrongNonce         = errors.New("parentchain: sender nonce mismatch")
	ErrInvalidTxSignature = errors.New("parentchain: invalid transaction signature")
	ErrUnknownMethod      = errors.New("parentchain: unknown method selector")
	ErrUnauthorizedSender = errors.New("parentchain: unauthorized sender")
	ErrMalformedCallData  = errors.New("parentchain: malformed calldata")
)

// Method selectors for Parent Chain transactions
const (
	MethodDepositToFloat  byte = 0x01
	MethodTransferFloat   byte = 0x02
	MethodMarkClaimed     byte = 0x03
	MethodReclaimFloat    byte = 0x04
	MethodRegisterAccount byte = 0x05
	MethodSubmitStateRoot byte = 0x06
	MethodRegisterCluster byte = 0x07
)

// ─── CALLDATA ENCODING & DECODING ──────────────────────────────────────────

func EncodeDepositToFloatCallData(destKey cm.PublicKey, destClusterID uint64, sender, target common.Address, amount *big.Int, msgID common.Hash) []byte {
	buf := make([]byte, 1+48+8+20+20+32+32)
	buf[0] = MethodDepositToFloat
	copy(buf[1:49], destKey[:])
	binary.BigEndian.PutUint64(buf[49:57], destClusterID)
	copy(buf[57:77], sender.Bytes())
	copy(buf[77:97], target.Bytes())
	copy(buf[97:129], padTo32(amount))
	copy(buf[129:161], msgID.Bytes())
	return buf
}

func DecodeDepositToFloatCallData(data []byte) (destKey cm.PublicKey, destClusterID uint64, sender, target common.Address, amount *big.Int, msgID common.Hash, err error) {
	if len(data) < 1+48+8+20+20+32+32 {
		return destKey, 0, sender, target, nil, msgID, ErrMalformedCallData
	}
	copy(destKey[:], data[1:49])
	destClusterID = binary.BigEndian.Uint64(data[49:57])
	sender = common.BytesToAddress(data[57:77])
	target = common.BytesToAddress(data[77:97])
	amount = new(big.Int).SetBytes(data[97:129])
	msgID = common.BytesToHash(data[129:161])
	return destKey, destClusterID, sender, target, amount, msgID, nil
}

func EncodeTransferFloatCallData(toKey cm.PublicKey, destClusterID uint64, sender, target common.Address, value, fee *big.Int, nonce uint64, cert cm.Sign, isRefund bool, payload []byte) []byte {
	buf := make([]byte, 1+48+8+20+20+32+32+8+96+1+4+len(payload))
	buf[0] = MethodTransferFloat
	copy(buf[1:49], toKey[:])
	binary.BigEndian.PutUint64(buf[49:57], destClusterID)
	copy(buf[57:77], sender.Bytes())
	copy(buf[77:97], target.Bytes())
	copy(buf[97:129], padTo32(value))
	copy(buf[129:161], padTo32(fee))
	binary.BigEndian.PutUint64(buf[161:169], nonce)
	copy(buf[169:265], cert.Bytes())
	if isRefund {
		buf[265] = 1
	}
	binary.BigEndian.PutUint32(buf[266:270], uint32(len(payload)))
	copy(buf[270:], payload)
	return buf
}

func DecodeTransferFloatCallData(data []byte) (toKey cm.PublicKey, destClusterID uint64, sender, target common.Address, value, fee *big.Int, nonce uint64, cert cm.Sign, isRefund bool, payload []byte, err error) {
	if len(data) < 270 {
		return toKey, 0, sender, target, nil, nil, 0, cert, false, nil, ErrMalformedCallData
	}
	copy(toKey[:], data[1:49])
	destClusterID = binary.BigEndian.Uint64(data[49:57])
	sender = common.BytesToAddress(data[57:77])
	target = common.BytesToAddress(data[77:97])
	value = new(big.Int).SetBytes(data[97:129])
	fee = new(big.Int).SetBytes(data[129:161])
	nonce = binary.BigEndian.Uint64(data[161:169])
	cert = cm.SignFromBytes(data[169:265])
	isRefund = data[265] == 1
	pLen := binary.BigEndian.Uint32(data[266:270])
	if uint32(len(data)-270) < pLen {
		return toKey, 0, sender, target, nil, nil, 0, cert, false, nil, ErrMalformedCallData
	}
	payload = make([]byte, pLen)
	copy(payload, data[270:270+pLen])
	return toKey, destClusterID, sender, target, value, fee, nonce, cert, isRefund, payload, nil
}

func EncodeMarkClaimedCallData(messageID common.Hash, outcome FloatOutcome, cert cm.Sign) []byte {
	buf := make([]byte, 1+32+1+96)
	buf[0] = MethodMarkClaimed
	copy(buf[1:33], messageID.Bytes())
	buf[33] = byte(outcome)
	copy(buf[34:130], cert.Bytes())
	return buf
}

func DecodeMarkClaimedCallData(data []byte) (messageID common.Hash, outcome FloatOutcome, cert cm.Sign, err error) {
	if len(data) < 130 {
		return messageID, 0, cert, ErrMalformedCallData
	}
	messageID = common.BytesToHash(data[1:33])
	outcome = FloatOutcome(data[33])
	cert = cm.SignFromBytes(data[34:130])
	return messageID, outcome, cert, nil
}

func EncodeReclaimFloatCallData(messageID common.Hash, cert cm.Sign) []byte {
	buf := make([]byte, 1+32+96)
	buf[0] = MethodReclaimFloat
	copy(buf[1:33], messageID.Bytes())
	copy(buf[33:129], cert.Bytes())
	return buf
}

func DecodeReclaimFloatCallData(data []byte) (messageID common.Hash, cert cm.Sign, err error) {
	if len(data) < 129 {
		return messageID, cert, ErrMalformedCallData
	}
	messageID = common.BytesToHash(data[1:33])
	cert = cm.SignFromBytes(data[33:129])
	return messageID, cert, nil
}

func EncodeRegisterAccountCallData(userAddress common.Address, floatIdentityKey cm.PublicKey, userSig []byte) []byte {
	buf := make([]byte, 1+20+48+4+len(userSig))
	buf[0] = MethodRegisterAccount
	copy(buf[1:21], userAddress.Bytes())
	copy(buf[21:69], floatIdentityKey[:])
	binary.BigEndian.PutUint32(buf[69:73], uint32(len(userSig)))
	copy(buf[73:], userSig)
	return buf
}

func DecodeRegisterAccountCallData(data []byte) (userAddress common.Address, floatIdentityKey cm.PublicKey, userSig []byte, err error) {
	if len(data) < 73 {
		return userAddress, floatIdentityKey, nil, ErrMalformedCallData
	}
	userAddress = common.BytesToAddress(data[1:21])
	copy(floatIdentityKey[:], data[21:69])
	sigLen := binary.BigEndian.Uint32(data[69:73])
	if uint32(len(data)-73) < sigLen {
		return userAddress, floatIdentityKey, nil, ErrMalformedCallData
	}
	userSig = make([]byte, sigLen)
	copy(userSig, data[73:73+sigLen])
	return userAddress, floatIdentityKey, userSig, nil
}

func EncodeSubmitStateRootCallData(clusterPubKey cm.PublicKey, epoch uint64, stateRoot common.Hash, cert cm.Sign) []byte {
	buf := make([]byte, 1+48+8+32+96)
	buf[0] = MethodSubmitStateRoot
	copy(buf[1:49], clusterPubKey[:])
	binary.BigEndian.PutUint64(buf[49:57], epoch)
	copy(buf[57:89], stateRoot.Bytes())
	copy(buf[89:185], cert.Bytes())
	return buf
}

func DecodeSubmitStateRootCallData(data []byte) (clusterPubKey cm.PublicKey, epoch uint64, stateRoot common.Hash, cert cm.Sign, err error) {
	if len(data) < 185 {
		return clusterPubKey, 0, stateRoot, cert, ErrMalformedCallData
	}
	copy(clusterPubKey[:], data[1:49])
	epoch = binary.BigEndian.Uint64(data[49:57])
	stateRoot = common.BytesToHash(data[57:89])
	cert = cm.SignFromBytes(data[89:185])
	return clusterPubKey, epoch, stateRoot, cert, nil
}

func EncodeRegisterClusterCallData(clusterPubKey cm.PublicKey, clusterID uint64) []byte {
	buf := make([]byte, 1+48+8)
	buf[0] = MethodRegisterCluster
	copy(buf[1:49], clusterPubKey[:])
	binary.BigEndian.PutUint64(buf[49:57], clusterID)
	return buf
}

func DecodeRegisterClusterCallData(data []byte) (clusterPubKey cm.PublicKey, clusterID uint64, err error) {
	if len(data) < 57 {
		return clusterPubKey, 0, ErrMalformedCallData
	}
	copy(clusterPubKey[:], data[1:49])
	clusterID = binary.BigEndian.Uint64(data[49:57])
	return clusterPubKey, clusterID, nil
}

// ─── TRANSACTION HASH & SIGNING ──────────────────────────────────────────

var txHashPool = sync.Pool{
	New: func() interface{} {
		return &pb.TransactionHashData{}
	},
}

// ComputeTxHash computes the deterministic canonical hash of a transaction.
func ComputeTxHash(tx *pb.Transaction) common.Hash {
	if tx == nil {
		return common.Hash{}
	}
	hd := txHashPool.Get().(*pb.TransactionHashData)
	defer func() {
		proto.Reset(hd)
		txHashPool.Put(hd)
	}()

	hd.FromAddress = tx.FromAddress
	hd.ToAddress = tx.ToAddress
	hd.Amount = tx.Amount
	hd.MaxGas = tx.MaxGas
	hd.MaxGasPrice = tx.MaxGasPrice
	hd.MaxTimeUse = tx.MaxTimeUse
	hd.Data = tx.Data
	hd.Type = tx.Type
	hd.LastDeviceKey = tx.LastDeviceKey
	hd.NewDeviceKey = tx.NewDeviceKey
	hd.Nonce = tx.Nonce
	hd.ChainID = tx.ChainID
	hd.R = tx.R
	hd.S = tx.S
	hd.V = tx.V
	hd.GasTipCap = tx.GasTipCap
	hd.GasFeeCap = tx.GasFeeCap
	hd.AccessList = tx.AccessList

	raw, err := proto.MarshalOptions{Deterministic: true}.Marshal(hd)
	if err != nil {
		return common.Hash{}
	}
	return crypto.Keccak256Hash(raw)
}

// BuildAndSignBLSTx creates a pb.Transaction signed with the cluster's BLS key.
func BuildAndSignBLSTx(privKey cm.PrivateKey, pubKey cm.PublicKey, to common.Address, nonce uint64, callData []byte) (*pb.Transaction, error) {
	addr := common.BytesToAddress(crypto.Keccak256(pubKey[:])[12:])
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], nonce)

	tx := &pb.Transaction{
		FromAddress:   addr.Bytes(),
		ToAddress:     to.Bytes(),
		Nonce:         nonceBytes[:],
		Data:          callData,
		ChainID:       ParentChainID,
		LastDeviceKey: pubKey[:], // 48-byte BLS public key
	}

	txHash := ComputeTxHash(tx)
	sig := bls.Sign(privKey, txHash[:])
	tx.Sign = sig.Bytes()
	return tx, nil
}

// VerifyTxSignature verifies the signature (BLS or ECDSA) of a transaction.
func VerifyTxSignature(tx *pb.Transaction, store Store) (cm.PublicKey, error) {
	if tx == nil || len(tx.FromAddress) != 20 {
		return cm.PublicKey{}, ErrInvalidTxSignature
	}
	sender := common.BytesToAddress(tx.FromAddress)
	txHash := ComputeTxHash(tx)

	// Case 1: BLS signature (96 bytes)
	if len(tx.Sign) == 96 {
		var senderKey cm.PublicKey
		if len(tx.LastDeviceKey) == 48 {
			copy(senderKey[:], tx.LastDeviceKey)
			derived := common.BytesToAddress(crypto.Keccak256(senderKey[:])[12:])
			if derived != sender {
				return cm.PublicKey{}, fmt.Errorf("%w: LastDeviceKey address mismatch", ErrInvalidTxSignature)
			}
		} else {
			// Look up in AccountRegistry or ChainRegistry
			regKey, found, err := store.GetAccountRegistry(sender)
			if err != nil || !found {
				return cm.PublicKey{}, fmt.Errorf("%w: unknown BLS sender", ErrInvalidTxSignature)
			}
			senderKey = regKey
		}

		if !bls.VerifySign(senderKey, cm.SignFromBytes(tx.Sign), txHash[:]) {
			return cm.PublicKey{}, fmt.Errorf("%w: BLS verification failed", ErrInvalidTxSignature)
		}
		return senderKey, nil
	}

	// Case 2: ECDSA Ethereum signature (R, S, V)
	if len(tx.R) > 0 && len(tx.S) > 0 {
		var sig [65]byte
		copy(sig[0:32], padTo32(new(big.Int).SetBytes(tx.R)))
		copy(sig[32:64], padTo32(new(big.Int).SetBytes(tx.S)))
		if len(tx.V) > 0 {
			v := tx.V[0]
			if v >= 27 {
				v -= 27
			}
			sig[64] = v
		}

		pubKey, err := crypto.SigToPub(txHash[:], sig[:])
		if err != nil {
			return cm.PublicKey{}, fmt.Errorf("%w: ECDSA recover failed: %v", ErrInvalidTxSignature, err)
		}
		recoveredAddr := crypto.PubkeyToAddress(*pubKey)
		if recoveredAddr != sender {
			return cm.PublicKey{}, fmt.Errorf("%w: ECDSA address mismatch", ErrInvalidTxSignature)
		}
		return cm.PublicKey{}, nil
	}

	return cm.PublicKey{}, fmt.Errorf("%w: no signature present", ErrInvalidTxSignature)
}

// ─── EXECUTE TRANSACTION ──────────────────────────────────────────────────

// ExecuteTx validates signature, nonce, and chain ID, advances nonce, and executes the call.
func ExecuteTx(store Store, tx *pb.Transaction, blockTime uint64) (*Receipt, error) {
	if tx == nil {
		return nil, errors.New("parentchain: nil transaction")
	}

	txHash := ComputeTxHash(tx)

	// 1. Validate ChainID
	if tx.ChainID != ParentChainID {
		return &Receipt{
			TxHash:    txHash,
			Status:    0,
			ErrorCode: 101, // ErrInvalidChainID
		}, ErrInvalidChainID
	}

	// 2. Validate ToAddress
	toAddr := common.BytesToAddress(tx.ToAddress)
	if toAddr != ParentChainGatewayAddress {
		return &Receipt{
			TxHash:    txHash,
			Status:    0,
			ErrorCode: 102, // ErrInvalidToAddress
		}, ErrInvalidToAddress
	}

	// 3. Validate Nonce
	sender := common.BytesToAddress(tx.FromAddress)
	expectedNonce, err := store.GetNonce(sender)
	if err != nil {
		return nil, err
	}
	var txNonce uint64
	if len(tx.Nonce) >= 8 {
		txNonce = binary.BigEndian.Uint64(tx.Nonce[:8])
	} else if len(tx.Nonce) > 0 {
		txNonce = new(big.Int).SetBytes(tx.Nonce).Uint64()
	}
	if txNonce != expectedNonce {
		return &Receipt{
			TxHash:    txHash,
			Status:    0,
			ErrorCode: 103, // ErrWrongNonce
		}, fmt.Errorf("%w: expected %d, got %d", ErrWrongNonce, expectedNonce, txNonce)
	}

	// 4. Validate Signature
	senderKey, err := VerifyTxSignature(tx, store)
	if err != nil {
		return &Receipt{
			TxHash:    txHash,
			Status:    0,
			ErrorCode: 104, // ErrInvalidTxSignature
		}, err
	}

	// Advance sender nonce immediately after valid signature check
	if err := store.SetNonce(sender, expectedNonce+1); err != nil {
		return nil, err
	}

	// 5. Dispatch Method
	if len(tx.Data) == 0 {
		return &Receipt{
			TxHash:    txHash,
			Status:    1,
			ErrorCode: 0,
		}, nil
	}

	method := tx.Data[0]
	var events [][]byte

	switch method {
	case MethodDepositToFloat:
		destKey, destClusterID, argSender, target, amount, msgID, err := DecodeDepositToFloatCallData(tx.Data)
		if err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 201}, err
		}
		if err := DepositToFloat(store, destKey, destClusterID, argSender, target, amount, msgID, blockTime); err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 202}, err
		}
		events = append(events, msgID.Bytes())

	case MethodTransferFloat:
		toKey, destClusterID, argSender, target, value, fee, seq, cert, isRefund, payload, err := DecodeTransferFloatCallData(tx.Data)
		if err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 203}, err
		}
		// Sender must match fromKey
		if senderKey == (cm.PublicKey{}) {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 204}, ErrUnauthorizedSender
		}
		fromKey := senderKey
		msgID, err := TransferFloat(store, fromKey, toKey, destClusterID, argSender, target, value, fee, payload, seq, cert, isRefund, 20, blockTime)
		if err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 205}, err
		}
		events = append(events, msgID.Bytes())

	case MethodMarkClaimed:
		msgID, outcome, cert, err := DecodeMarkClaimedCallData(tx.Data)
		if err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 206}, err
		}
		rec, found, err := store.GetTransferRecord(msgID)
		if err != nil || !found {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 207}, ErrFloatUnknownMessage
		}
		// Sender must be destKey
		if senderKey != rec.DestKey {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 208}, ErrUnauthorizedSender
		}
		if err := MarkClaimed(store, msgID, outcome, cert); err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 209}, err
		}
		events = append(events, msgID.Bytes())

	case MethodReclaimFloat:
		msgID, cert, err := DecodeReclaimFloatCallData(tx.Data)
		if err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 210}, err
		}
		rec, found, err := store.GetTransferRecord(msgID)
		if err != nil || !found {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 211}, ErrFloatUnknownMessage
		}
		if rec.SourceKey == nil || senderKey != *rec.SourceKey {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 212}, ErrUnauthorizedSender
		}
		if err := ReclaimFloat(store, msgID, cert, blockTime, 86400); err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 213}, err
		}
		events = append(events, msgID.Bytes())

	case MethodRegisterAccount:
		userAddress, floatIdentityKey, userSig, err := DecodeRegisterAccountCallData(tx.Data)
		if err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 214}, err
		}
		clusterSig := cm.SignFromBytes(tx.Sign)
		if err := RegisterAccount(store, userAddress, floatIdentityKey, userSig, clusterSig); err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 215}, err
		}
		events = append(events, userAddress.Bytes())

	case MethodSubmitStateRoot:
		clusterPubKey, epoch, stateRoot, cert, err := DecodeSubmitStateRootCallData(tx.Data)
		if err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 216}, err
		}
		if senderKey != clusterPubKey {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 217}, ErrUnauthorizedSender
		}
		if err := SubmitStateRoot(store, clusterPubKey, epoch, stateRoot, cert); err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 218}, err
		}
		events = append(events, stateRoot.Bytes())

	case MethodRegisterCluster:
		clusterPubKey, clusterID, err := DecodeRegisterClusterCallData(tx.Data)
		if err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 219}, err
		}
		clusterHash := crypto.Keccak256Hash(clusterPubKey[:])
		if err := store.SetChainRegistry(clusterHash, ChainRegistryEntry{
			FloatIdentityKey:     clusterPubKey,
			ClusterIDDescriptive: clusterID,
			ChainIDDescriptive:   clusterID,
		}); err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 220}, err
		}
		// Also register the derived address in AccountRegistry
		derivedAddr := common.BytesToAddress(crypto.Keccak256(clusterPubKey[:])[12:])
		_ = store.SetAccountRegistry(derivedAddr, clusterPubKey)

	default:
		return &Receipt{
			TxHash:    txHash,
			Status:    0,
			ErrorCode: 299, // ErrUnknownMethod
		}, ErrUnknownMethod
	}

	return &Receipt{
		TxHash:    txHash,
		Status:    1,
		ErrorCode: 0,
		Events:    events,
	}, nil
}

// ─── LEGACY SUPPORT ────────────────────────────────────────────────────────

type TxType string

const (
	TxTypeDepositToFloat  TxType = "DepositToFloat"
	TxTypeTransferFloat   TxType = "TransferFloat"
	TxTypeMarkClaimed     TxType = "MarkClaimed"
	TxTypeReclaimFloat    TxType = "ReclaimFloat"
	TxTypeRegisterAccount TxType = "RegisterAccount"
	TxTypeSubmitStateRoot TxType = "SubmitStateRoot"
	TxTypeRegisterCluster TxType = "RegisterCluster"
)

type ParentChainTx struct {
	Type        TxType         `json:"type"`
	MsgID       common.Hash    `json:"msgID,omitempty"`
	Cert        []byte         `json:"cert,omitempty"`
	PubKey      []byte         `json:"pubKey,omitempty"`
	ClusterID   uint64         `json:"clusterID,omitempty"`
	ChainID     uint64         `json:"chainID,omitempty"`
	Amount      *big.Int       `json:"amount,omitempty"`
	ToPubKey    []byte         `json:"toPubKey,omitempty"`
	Sender      common.Address `json:"sender,omitempty"`
	Target      common.Address `json:"target,omitempty"`
	Payload     []byte         `json:"payload,omitempty"`
	IsRefund    bool           `json:"isRefund,omitempty"`
	Nonce       uint64         `json:"nonce,omitempty"`
	Fee         *big.Int       `json:"fee,omitempty"`
	Outcome     FloatOutcome   `json:"outcome,omitempty"`
	UserAddress common.Address `json:"userAddress,omitempty"`
	UserSig     []byte         `json:"userSig,omitempty"`
	Epoch       uint64         `json:"epoch,omitempty"`
	StateRoot   common.Hash    `json:"stateRoot,omitempty"`
}

func (tx *ParentChainTx) Marshal() ([]byte, error) {
	return json.Marshal(tx)
}

func UnmarshalParentChainTx(data []byte) (*ParentChainTx, error) {
	var tx ParentChainTx
	err := json.Unmarshal(data, &tx)
	if err != nil {
		return nil, err
	}
	return &tx, nil
}
