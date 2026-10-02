package parentchain

import (
	"encoding/binary"
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

// ─── CALLDATA ENCODING & DECODING (PROTOBUF) ──────────────────────────────

func EncodeDepositToFloatCallData(sourceKey, destKey cm.PublicKey, destClusterID uint64, sender, target common.Address, amount *big.Int, msgID common.Hash, cert cm.Sign) []byte {
	var amtBytes []byte
	if amount != nil {
		amtBytes = amount.Bytes()
	}
	cd := &pb.ParentChainCallData{
		Method: pb.ParentChainMethod_METHOD_DEPOSIT_TO_FLOAT,
		Args: &pb.ParentChainCallData_DepositToFloat{
			DepositToFloat: &pb.DepositToFloatArgs{
				SourceKey:     sourceKey[:],
				DestKey:       destKey[:],
				DestClusterId: destClusterID,
				Sender:        sender.Bytes(),
				Target:        target.Bytes(),
				Amount:        amtBytes,
				MsgId:         msgID.Bytes(),
				Cert:          cert.Bytes(),
			},
		},
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(cd)
	return b
}

func DecodeDepositToFloatCallData(data []byte) (sourceKey, destKey cm.PublicKey, destClusterID uint64, sender, target common.Address, amount *big.Int, msgID common.Hash, cert cm.Sign, err error) {
	var cd pb.ParentChainCallData
	if err := proto.Unmarshal(data, &cd); err != nil {
		return sourceKey, destKey, 0, sender, target, nil, msgID, cert, ErrMalformedCallData
	}
	args := cd.GetDepositToFloat()
	if args == nil || len(args.DestKey) != 48 || len(args.Sender) != 20 || len(args.Target) != 20 || len(args.MsgId) != 32 {
		return sourceKey, destKey, 0, sender, target, nil, msgID, cert, ErrMalformedCallData
	}
	if len(args.SourceKey) == 48 {
		copy(sourceKey[:], args.SourceKey)
	}
	copy(destKey[:], args.DestKey)
	if len(args.Cert) == 96 {
		copy(cert[:], args.Cert)
	}
	var amt *big.Int
	if len(args.Amount) > 0 {
		amt = new(big.Int).SetBytes(args.Amount)
	} else {
		amt = big.NewInt(0)
	}
	return sourceKey, destKey, args.DestClusterId, common.BytesToAddress(args.Sender), common.BytesToAddress(args.Target), amt, common.BytesToHash(args.MsgId), cert, nil
}

func EncodeTransferFloatCallData(toKey cm.PublicKey, destClusterID uint64, sender, target common.Address, value, fee *big.Int, nonce uint64, cert cm.Sign, isRefund bool, payload []byte) []byte {
	var valBytes, feeBytes []byte
	if value != nil {
		valBytes = value.Bytes()
	}
	if fee != nil {
		feeBytes = fee.Bytes()
	}
	cd := &pb.ParentChainCallData{
		Method: pb.ParentChainMethod_METHOD_TRANSFER_FLOAT,
		Args: &pb.ParentChainCallData_TransferFloat{
			TransferFloat: &pb.TransferFloatArgs{
				DestKey:       toKey[:],
				DestClusterId: destClusterID,
				Sender:        sender.Bytes(),
				Target:        target.Bytes(),
				Amount:        valBytes,
				GasFee:        feeBytes,
				Payload:       payload,
				Seq:           nonce,
				Cert:          cert.Bytes(),
				IsRefund:      isRefund,
			},
		},
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(cd)
	return b
}

func DecodeTransferFloatCallData(data []byte) (toKey cm.PublicKey, destClusterID uint64, sender, target common.Address, value, fee *big.Int, nonce uint64, cert cm.Sign, isRefund bool, payload []byte, err error) {
	var cd pb.ParentChainCallData
	if err := proto.Unmarshal(data, &cd); err != nil {
		return toKey, 0, sender, target, nil, nil, 0, cert, false, nil, ErrMalformedCallData
	}
	args := cd.GetTransferFloat()
	if args == nil || len(args.DestKey) != 48 || len(args.Sender) != 20 || len(args.Target) != 20 || len(args.Cert) != 96 {
		return toKey, 0, sender, target, nil, nil, 0, cert, false, nil, ErrMalformedCallData
	}
	copy(toKey[:], args.DestKey)
	var val, feeVal *big.Int
	if len(args.Amount) > 0 {
		val = new(big.Int).SetBytes(args.Amount)
	} else {
		val = big.NewInt(0)
	}
	if len(args.GasFee) > 0 {
		feeVal = new(big.Int).SetBytes(args.GasFee)
	} else {
		feeVal = big.NewInt(0)
	}
	return toKey, args.DestClusterId, common.BytesToAddress(args.Sender), common.BytesToAddress(args.Target), val, feeVal, args.Seq, cm.SignFromBytes(args.Cert), args.IsRefund, args.Payload, nil
}

func EncodeMarkClaimedCallData(messageID common.Hash, outcome FloatOutcome, cert cm.Sign) []byte {
	cd := &pb.ParentChainCallData{
		Method: pb.ParentChainMethod_METHOD_MARK_CLAIMED,
		Args: &pb.ParentChainCallData_MarkClaimed{
			MarkClaimed: &pb.MarkClaimedArgs{
				MsgId:   messageID.Bytes(),
				Outcome: uint32(outcome),
				Cert:    cert.Bytes(),
			},
		},
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(cd)
	return b
}

func DecodeMarkClaimedCallData(data []byte) (messageID common.Hash, outcome FloatOutcome, cert cm.Sign, err error) {
	var cd pb.ParentChainCallData
	if err := proto.Unmarshal(data, &cd); err != nil {
		return messageID, 0, cert, ErrMalformedCallData
	}
	args := cd.GetMarkClaimed()
	if args == nil || len(args.MsgId) != 32 || len(args.Cert) != 96 {
		return messageID, 0, cert, ErrMalformedCallData
	}
	return common.BytesToHash(args.MsgId), FloatOutcome(args.Outcome), cm.SignFromBytes(args.Cert), nil
}

func EncodeReclaimFloatCallData(messageID common.Hash, cert cm.Sign) []byte {
	cd := &pb.ParentChainCallData{
		Method: pb.ParentChainMethod_METHOD_RECLAIM_FLOAT,
		Args: &pb.ParentChainCallData_ReclaimFloat{
			ReclaimFloat: &pb.ReclaimFloatArgs{
				MsgId: messageID.Bytes(),
				Cert:  cert.Bytes(),
			},
		},
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(cd)
	return b
}

func DecodeReclaimFloatCallData(data []byte) (messageID common.Hash, cert cm.Sign, err error) {
	var cd pb.ParentChainCallData
	if err := proto.Unmarshal(data, &cd); err != nil {
		return messageID, cert, ErrMalformedCallData
	}
	args := cd.GetReclaimFloat()
	if args == nil || len(args.MsgId) != 32 || len(args.Cert) != 96 {
		return messageID, cert, ErrMalformedCallData
	}
	return common.BytesToHash(args.MsgId), cm.SignFromBytes(args.Cert), nil
}

func EncodeRegisterAccountCallData(userAddress common.Address, floatIdentityKey cm.PublicKey, userSig []byte, clusterSig cm.Sign) []byte {
	cd := &pb.ParentChainCallData{
		Method: pb.ParentChainMethod_METHOD_REGISTER_ACCOUNT,
		Args: &pb.ParentChainCallData_RegisterAccount{
			RegisterAccount: &pb.RegisterAccountArgs{
				UserAddress:      userAddress.Bytes(),
				FloatIdentityKey: floatIdentityKey[:],
				UserSig:          userSig,
				ClusterSig:       clusterSig.Bytes(),
			},
		},
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(cd)
	return b
}

func DecodeRegisterAccountCallData(data []byte) (userAddress common.Address, floatIdentityKey cm.PublicKey, userSig []byte, clusterSig cm.Sign, err error) {
	var cd pb.ParentChainCallData
	if err := proto.Unmarshal(data, &cd); err != nil {
		return userAddress, floatIdentityKey, nil, clusterSig, ErrMalformedCallData
	}
	args := cd.GetRegisterAccount()
	if args == nil || len(args.UserAddress) != 20 || len(args.FloatIdentityKey) != 48 {
		return userAddress, floatIdentityKey, nil, clusterSig, ErrMalformedCallData
	}
	copy(floatIdentityKey[:], args.FloatIdentityKey)
	if len(args.ClusterSig) == 96 {
		clusterSig = cm.SignFromBytes(args.ClusterSig)
	}
	return common.BytesToAddress(args.UserAddress), floatIdentityKey, args.UserSig, clusterSig, nil
}

func EncodeSubmitStateRootCallData(clusterPubKey cm.PublicKey, epoch uint64, stateRoot common.Hash, cert cm.Sign) []byte {
	cd := &pb.ParentChainCallData{
		Method: pb.ParentChainMethod_METHOD_SUBMIT_STATE_ROOT,
		Args: &pb.ParentChainCallData_SubmitStateRoot{
			SubmitStateRoot: &pb.SubmitStateRootArgs{
				ClusterKey: clusterPubKey[:],
				Epoch:      epoch,
				StateRoot:  stateRoot.Bytes(),
				Cert:       cert.Bytes(),
			},
		},
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(cd)
	return b
}

func DecodeSubmitStateRootCallData(data []byte) (clusterPubKey cm.PublicKey, epoch uint64, stateRoot common.Hash, cert cm.Sign, err error) {
	var cd pb.ParentChainCallData
	if err := proto.Unmarshal(data, &cd); err != nil {
		return clusterPubKey, 0, stateRoot, cert, ErrMalformedCallData
	}
	args := cd.GetSubmitStateRoot()
	if args == nil || len(args.ClusterKey) != 48 || len(args.StateRoot) != 32 || len(args.Cert) != 96 {
		return clusterPubKey, 0, stateRoot, cert, ErrMalformedCallData
	}
	copy(clusterPubKey[:], args.ClusterKey)
	return clusterPubKey, args.Epoch, common.BytesToHash(args.StateRoot), cm.SignFromBytes(args.Cert), nil
}

func EncodeRegisterClusterCallData(clusterPubKey cm.PublicKey, clusterID uint64) []byte {
	cd := &pb.ParentChainCallData{
		Method: pb.ParentChainMethod_METHOD_REGISTER_CLUSTER,
		Args: &pb.ParentChainCallData_RegisterCluster{
			RegisterCluster: &pb.RegisterClusterArgs{
				ClusterKey: clusterPubKey[:],
				ClusterId:  clusterID,
			},
		},
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(cd)
	return b
}

func DecodeRegisterClusterCallData(data []byte) (clusterPubKey cm.PublicKey, clusterID uint64, err error) {
	var cd pb.ParentChainCallData
	if err := proto.Unmarshal(data, &cd); err != nil {
		return clusterPubKey, 0, ErrMalformedCallData
	}
	args := cd.GetRegisterCluster()
	if args == nil || len(args.ClusterKey) != 48 {
		return clusterPubKey, 0, ErrMalformedCallData
	}
	copy(clusterPubKey[:], args.ClusterKey)
	return clusterPubKey, args.ClusterId, nil
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
// Note: LastDeviceKey is intentionally NOT used for carrying the public key (fix H12).
func BuildAndSignBLSTx(privKey cm.PrivateKey, pubKey cm.PublicKey, to common.Address, nonce uint64, callData []byte) (*pb.Transaction, error) {
	addr := common.BytesToAddress(crypto.Keccak256(pubKey[:])[12:])
	var nonceBytes [8]byte
	binary.BigEndian.PutUint64(nonceBytes[:], nonce)

	tx := &pb.Transaction{
		FromAddress: addr.Bytes(),
		ToAddress:   to.Bytes(),
		Nonce:       nonceBytes[:],
		Data:        callData,
		ChainID:     ParentChainID,
	}

	txHash := ComputeTxHash(tx)
	sig := bls.Sign(privKey, txHash[:])
	tx.Sign = sig.Bytes()
	return tx, nil
}

// VerifyTxSignature verifies that tx is signed over its transaction hash (ComputeTxHash, which covers the
// nonce and chain id) by the sender named in FromAddress, using BLS (96-byte Sign); every sender on this chain
// is a BLS identity. It returns the sender's BLS public key, which is resolved from the
// AccountRegistry/ChainRegistry, or, for a registerCluster/registerAccount bootstrap transaction, from the key
// carried in its own CallData (H12). There is no unsigned or alternate-digest mode: every transaction is
// signed over its hash and carries a sequential nonce.
func VerifyTxSignature(tx *pb.Transaction, store Store) (cm.PublicKey, error) {
	if tx == nil || len(tx.FromAddress) != 20 {
		return cm.PublicKey{}, ErrInvalidTxSignature
	}
	sender := common.BytesToAddress(tx.FromAddress)
	txHash := ComputeTxHash(tx)

	if len(tx.Sign) == 96 {
		var senderKey cm.PublicKey
		regKey, found, err := store.GetAccountRegistry(sender)
		if err == nil && found {
			senderKey = regKey
		} else {
			// A registration transaction provides the sender key in its own CallData.
			var cd pb.ParentChainCallData
			if protoErr := proto.Unmarshal(tx.Data, &cd); protoErr == nil {
				switch cd.Method {
				case pb.ParentChainMethod_METHOD_REGISTER_CLUSTER:
					if args := cd.GetRegisterCluster(); args != nil && len(args.ClusterKey) == 48 &&
						common.BytesToAddress(crypto.Keccak256(args.ClusterKey)[12:]) == sender {
						copy(senderKey[:], args.ClusterKey)
					}
				case pb.ParentChainMethod_METHOD_REGISTER_ACCOUNT:
					if args := cd.GetRegisterAccount(); args != nil && len(args.FloatIdentityKey) == 48 &&
						common.BytesToAddress(crypto.Keccak256(args.FloatIdentityKey)[12:]) == sender {
						copy(senderKey[:], args.FloatIdentityKey)
					}
				}
			}
		}
		if senderKey == (cm.PublicKey{}) {
			return cm.PublicKey{}, fmt.Errorf("%w: unknown BLS sender", ErrInvalidTxSignature)
		}
		if !bls.VerifySign(senderKey, cm.SignFromBytes(tx.Sign), txHash[:]) {
			return cm.PublicKey{}, fmt.Errorf("%w: BLS verification failed", ErrInvalidTxSignature)
		}
		return senderKey, nil
	}

	return cm.PublicKey{}, fmt.Errorf("%w: no signature present", ErrInvalidTxSignature)
}

// ─── EXECUTE TRANSACTION ──────────────────────────────────────────────────

// TxNonceValue decodes tx.Nonce (big-endian, first 8 bytes) into a uint64.
func TxNonceValue(tx *pb.Transaction) uint64 {
	if len(tx.Nonce) >= 8 {
		return binary.BigEndian.Uint64(tx.Nonce[:8])
	}
	if len(tx.Nonce) > 0 {
		return new(big.Int).SetBytes(tx.Nonce).Uint64()
	}
	return 0
}

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

	// 3. Validate Signature (over the transaction hash)
	senderKey, err := VerifyTxSignature(tx, store)
	if err != nil {
		return &Receipt{
			TxHash:    txHash,
			Status:    0,
			ErrorCode: 104, // ErrInvalidTxSignature
		}, err
	}

	// 4. Validate and advance the sequential nonce (T-U14 & H7)
	sender := common.BytesToAddress(tx.FromAddress)
	expectedNonce, err := store.GetNonce(sender)
	if err != nil {
		return nil, err
	}
	txNonce := TxNonceValue(tx)
	if txNonce != expectedNonce {
		return &Receipt{
			TxHash:    txHash,
			Status:    0,
			ErrorCode: 103, // ErrWrongNonce
		}, fmt.Errorf("%w: expected %d, got %d", ErrWrongNonce, expectedNonce, txNonce)
	}
	if err := store.SetNonce(sender, expectedNonce+1); err != nil {
		return nil, err
	}

	// 5. Dispatch Method with state isolation for handler execution (H7)
	if len(tx.Data) == 0 {
		return &Receipt{
			TxHash:    txHash,
			Status:    1,
			ErrorCode: 0,
		}, nil
	}

	snap, isSnapshotter := store.(Snapshotter)
	if isSnapshotter {
		snap.Push()
	}

	rcpt, err := dispatchTxMethod(store, tx, senderKey, txHash, blockTime)
	if err != nil {
		if isSnapshotter {
			snap.Drop() // Revert any partial writes made by the handler (H7)
		}
		return rcpt, err
	}
	if isSnapshotter {
		snap.Merge()
	}
	return rcpt, nil
}

func dispatchTxMethod(store Store, tx *pb.Transaction, senderKey cm.PublicKey, txHash common.Hash, blockTime uint64) (*Receipt, error) {
	var cd pb.ParentChainCallData
	if err := proto.Unmarshal(tx.Data, &cd); err != nil {
		return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 200}, ErrMalformedCallData
	}

	var events [][]byte

	switch cd.Method {
	case pb.ParentChainMethod_METHOD_DEPOSIT_TO_FLOAT:
		sourceKey, destKey, destClusterID, argSender, target, amount, msgID, cert, err := DecodeDepositToFloatCallData(tx.Data)
		if err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 201}, err
		}
		events = append(events, msgID.Bytes())
		if err := DepositToFloat(store, sourceKey, destKey, destClusterID, argSender, target, amount, msgID, cert, blockTime); err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 202, Events: events}, err
		}

	case pb.ParentChainMethod_METHOD_TRANSFER_FLOAT:
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

	case pb.ParentChainMethod_METHOD_MARK_CLAIMED:
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

	case pb.ParentChainMethod_METHOD_RECLAIM_FLOAT:
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

	case pb.ParentChainMethod_METHOD_REGISTER_ACCOUNT:
		userAddress, floatIdentityKey, userSig, clusterSig, err := DecodeRegisterAccountCallData(tx.Data)
		if err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 214}, err
		}
		regDigest := ComputeRegisterAccountMessage(userAddress, floatIdentityKey)
		events = append(events, crypto.Keccak256(regDigest))
		events = append(events, userAddress.Bytes())
		if err := RegisterAccount(store, userAddress, floatIdentityKey, userSig, clusterSig); err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 215, Events: events}, err
		}

	case pb.ParentChainMethod_METHOD_SUBMIT_STATE_ROOT:
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
		subDigest := ComputeSubmitStateRootMessage(clusterPubKey, epoch, stateRoot)
		events = append(events, crypto.Keccak256(append(subDigest, cert.Bytes()...)))
		events = append(events, stateRoot.Bytes())

	case pb.ParentChainMethod_METHOD_REGISTER_CLUSTER:
		clusterPubKey, clusterID, err := DecodeRegisterClusterCallData(tx.Data)
		if err != nil {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 219}, err
		}
		if !clusterRegistrationAllowed(clusterPubKey) {
			return &Receipt{TxHash: txHash, Status: 0, ErrorCode: 221}, ErrClusterNotAuthorized
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
