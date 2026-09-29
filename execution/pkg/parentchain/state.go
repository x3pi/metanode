package parentchain

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
)

var (
	ErrFloatInvalidAmount       = errors.New("float account: amount must be positive")
	ErrFloatInsufficientBalance = errors.New("float account: insufficient balance")
	ErrFloatAlreadyResolved     = errors.New("float account: messageID already resolved (claimed or reclaimed)")
	ErrFloatUnknownMessage      = errors.New("float account: unknown messageID")
	ErrFloatReclaimTooEarly     = errors.New("float account: reclaim timeout not reached yet")
	ErrFloatNotReclaimable      = errors.New("float account: this messageID cannot be reclaimed")
	ErrFloatVelocityExceeded    = errors.New("float account: transfer outflow velocity limit exceeded")
	ErrFloatWrongNonce          = errors.New("float account: wrong nonce")
	ErrInvalidSignature         = errors.New("float account: invalid signature")
	ErrAccountAlreadyRegistered = errors.New("float account: account already registered")
)

var (
	TransferFloatDomainTag   = []byte("TRANSFER_FLOAT_V1:")
	ReclaimFloatDomainTag    = []byte("RECLAIM_FLOAT_V1:")
	MarkClaimedDomainTag     = []byte("MARK_CLAIMED_FLOAT_V1:")
	RegisterAccountDomainTag = []byte("REGISTER_ACCOUNT_V1:")
)

// floatTotalSupplyKey is the reserved Store key holding the running total of every
// DepositToFloat ever processed. It uses the all-zero hash.
var floatTotalSupplyKey = common.Hash{}

func appendUint64BE(buf []byte, v uint64) []byte {
	var b [8]byte
	for i := 7; i >= 0; i-- {
		b[i] = byte(v)
		v >>= 8
	}
	return append(buf, b[:]...)
}

func padTo32(val *big.Int) []byte {
	b := val.Bytes()
	if len(b) >= 32 {
		return b
	}
	res := make([]byte, 32)
	copy(res[32-len(b):], b)
	return res
}

func ComputeTransferFloatMessage(fromKey, toKey cm.PublicKey, sender, target common.Address, value, fee *big.Int, payloadHash common.Hash, nonce uint64) []byte {
	var buf []byte
	buf = append(buf, TransferFloatDomainTag...)
	buf = append(buf, fromKey[:]...)
	buf = append(buf, toKey[:]...)
	buf = append(buf, sender.Bytes()...)
	buf = append(buf, target.Bytes()...)
	buf = append(buf, padTo32(value)...)
	
	if fee != nil {
		buf = append(buf, padTo32(fee)...)
	} else {
		buf = append(buf, padTo32(big.NewInt(0))...)
	}

	buf = append(buf, payloadHash.Bytes()...)
	buf = appendUint64BE(buf, nonce)
	return buf
}

func ComputeReclaimFloatMessage(messageID common.Hash, sourceKey cm.PublicKey) []byte {
	var buf []byte
	buf = append(buf, ReclaimFloatDomainTag...)
	buf = append(buf, messageID.Bytes()...)
	buf = append(buf, sourceKey[:]...)
	return buf
}

func ComputeMarkClaimedMessage(messageID common.Hash, outcome FloatOutcome) []byte {
	var buf []byte
	buf = append(buf, MarkClaimedDomainTag...)
	buf = append(buf, messageID.Bytes()...)
	buf = append(buf, byte(outcome))
	return buf
}

func ComputeRegisterAccountMessage(userAddress common.Address, floatIdentityKey cm.PublicKey) []byte {
	var buf []byte
	buf = append(buf, RegisterAccountDomainTag...)
	buf = append(buf, userAddress.Bytes()...)
	buf = append(buf, floatIdentityKey[:]...)
	return buf
}

// ensureChainRegistry lazy-creates a chain registry entry if it doesn't exist
func ensureChainRegistry(store Store, keyHash common.Hash, key cm.PublicKey, clusterIDDesc uint64) error {
	_, found, err := store.GetChainRegistry(keyHash)
	if err != nil {
		return err
	}
	if !found {
		return store.SetChainRegistry(keyHash, ChainRegistryEntry{
			FloatIdentityKey:     key,
			ClusterIDDescriptive: clusterIDDesc,
			ChainIDDescriptive:   clusterIDDesc,
		})
	}
	return nil
}

// DepositToFloat credits destKey's NodeFloatAccount by amount and records messageID.
func DepositToFloat(store Store, destKey cm.PublicKey, destClusterIDDesc uint64, sender, target common.Address, amount *big.Int, messageID common.Hash, blockTime uint64) error {
	if amount == nil || amount.Sign() <= 0 {
		return ErrFloatInvalidAmount
	}
	if _, found, err := store.GetTransferRecord(messageID); err != nil {
		return err
	} else if found {
		return fmt.Errorf("DepositToFloat: %w: %s", ErrFloatAlreadyResolved, messageID.Hex())
	}

	destHash := crypto.Keccak256Hash(destKey[:])

	// Lazy create dest entry
	if err := ensureChainRegistry(store, destHash, destKey, destClusterIDDesc); err != nil {
		return err
	}

	cur, err := store.GetFloat(destHash)
	if err != nil {
		return err
	}
	total, err := store.GetFloat(floatTotalSupplyKey)
	if err != nil {
		return err
	}
	newBal := new(big.Int).Add(cur, amount)
	newTotal := new(big.Int).Add(total, amount)

	if err := store.SetFloat(destHash, newBal); err != nil {
		return err
	}
	if err := store.SetFloat(floatTotalSupplyKey, newTotal); err != nil {
		return err
	}
	err = store.SetTransferRecord(messageID, FloatTransferRecord{
		SourceKey:            nil,
		DestKey:              destKey,
		Value:                new(big.Int).Set(amount),
		ConfirmedAtBlockTime: blockTime,
	})
	if err != nil {
		return err
	}
	
	return store.AppendInboundTransfer(destHash, &TransferEvent{
		MsgID:      messageID,
		DestPubKey: destKey,
		Sender:     sender,
		Target:     target,
		Amount:     amount,
		BlockTime:  blockTime,
	})
}

// TransferFloat atomically moves value from fromKey to toKey.
func TransferFloat(
	store Store,
	fromKey, toKey cm.PublicKey,
	destClusterIDDesc uint64,
	sender, target common.Address,
	value, fee *big.Int,
	payload []byte,
	nonce uint64,
	cert cm.Sign,
	isRefund bool,
	velocityLimitPercent uint64,
	blockTime uint64,
) (common.Hash, error) {
	if value == nil || value.Sign() <= 0 {
		return common.Hash{}, ErrFloatInvalidAmount
	}
	if fromKey == toKey {
		return common.Hash{}, errors.New("TransferFloat: source and destination key are the same")
	}

	fromHash := crypto.Keccak256Hash(fromKey[:])
	toHash := crypto.Keccak256Hash(toKey[:])

	// The source chain must exist (since it has funds), no lazy create for source.
	_, found, err := store.GetChainRegistry(fromHash)
	if err != nil {
		return common.Hash{}, err
	}
	if !found {
		return common.Hash{}, errors.New("TransferFloat: unknown source chain")
	}

	wantNonce, err := store.GetFloatSeq(fromHash)
	if err != nil {
		return common.Hash{}, err
	}
	if nonce != wantNonce {
		return common.Hash{}, fmt.Errorf("TransferFloat: %w: got %d, want %d", ErrFloatWrongNonce, nonce, wantNonce)
	}

	payloadHash := crypto.Keccak256Hash(payload)
	digest := ComputeTransferFloatMessage(fromKey, toKey, sender, target, value, fee, payloadHash, nonce)
	
	if !bls.VerifySign(fromKey, cert, digest) {
		return common.Hash{}, fmt.Errorf("TransferFloat: %w", ErrInvalidSignature)
	}

	fromBal, err := store.GetFloat(fromHash)
	if err != nil {
		return common.Hash{}, err
	}
	
	totalDeduction := new(big.Int).Set(value)
	if fee != nil && fee.Sign() > 0 {
		totalDeduction.Add(totalDeduction, fee)
	}

	if fromBal.Cmp(totalDeduction) < 0 {
		return common.Hash{}, fmt.Errorf("TransferFloat: %w (has %s, needs %s)", ErrFloatInsufficientBalance, fromBal.String(), totalDeduction.String())
	}

	var newVelocity FloatVelocityState
	if !isRefund {
		cur, err := store.GetVelocity(fromHash)
		if err != nil {
			return common.Hash{}, err
		}
		newVelocity, err = checkAndRecordFloatVelocity(cur, value, fromBal, blockTime, velocityLimitPercent)
		if err != nil {
			return common.Hash{}, fmt.Errorf("TransferFloat: %w", err)
		}
	}

	// MessageID is the hash of the signed digest
	messageID := crypto.Keccak256Hash(digest)
	if _, found, err := store.GetTransferRecord(messageID); err != nil {
		return common.Hash{}, err
	} else if found {
		return common.Hash{}, fmt.Errorf("TransferFloat: %w: %s", ErrFloatAlreadyResolved, messageID.Hex())
	}

	// Lazy create dest entry
	if err := ensureChainRegistry(store, toHash, toKey, destClusterIDDesc); err != nil {
		return common.Hash{}, err
	}

	toBal, err := store.GetFloat(toHash)
	if err != nil {
		return common.Hash{}, err
	}

	newFrom := new(big.Int).Sub(fromBal, totalDeduction)
	newTo := new(big.Int).Add(toBal, value)

	if err := store.SetFloat(fromHash, newFrom); err != nil {
		return common.Hash{}, err
	}
	if err := store.SetFloat(toHash, newTo); err != nil {
		return common.Hash{}, err
	}
	
	if fee != nil && fee.Sign() > 0 {
		total, err := store.GetFloat(floatTotalSupplyKey)
		if err != nil {
			return common.Hash{}, err
		}
		newTotal := new(big.Int).Sub(total, fee)
		if err := store.SetFloat(floatTotalSupplyKey, newTotal); err != nil {
			return common.Hash{}, err
		}
	}
	
	if !isRefund {
		if err := store.SetVelocity(fromHash, newVelocity); err != nil {
			return common.Hash{}, err
		}
	}
	fromKeyCopy := fromKey
	if err := store.SetTransferRecord(messageID, FloatTransferRecord{
		SourceKey:            &fromKeyCopy,
		DestKey:              toKey,
		Value:                new(big.Int).Set(value),
		ConfirmedAtBlockTime: blockTime,
	}); err != nil {
		return common.Hash{}, err
	}
	
	if err := store.AppendInboundTransfer(toHash, &TransferEvent{
		MsgID:        messageID,
		SourcePubKey: fromKey,
		DestPubKey:   toKey,
		SourceSeq:    nonce,
		Sender:       sender,
		Target:       target,
		Amount:       value,
		PayloadHash:  payloadHash,
		BlockTime:    blockTime,
		IsRefund:     isRefund,
	}); err != nil {
		return common.Hash{}, err
	}
	
	if err := store.SetFloatSeq(fromHash, nonce+1); err != nil {
		return common.Hash{}, err
	}
	return messageID, nil
}

// checkAndRecordFloatVelocity enforces the rolling-window outflow velocity limit.
func checkAndRecordFloatVelocity(st FloatVelocityState, amount, currentBalance *big.Int, blockTime, limitPercent uint64) (FloatVelocityState, error) {
	const windowSeconds = 24 * 60 * 60
	if limitPercent == 0 {
		limitPercent = 20
	}

	windowStart := st.WindowStart
	baseAlloc := st.WindowBaseAlloc
	spent := st.Spent
	if spent == nil {
		spent = big.NewInt(0)
	}
	if baseAlloc == nil || blockTime < windowStart || blockTime-windowStart >= windowSeconds {
		windowStart = blockTime
		baseAlloc = new(big.Int).Set(currentBalance)
		spent = big.NewInt(0)
	}

	newSpent := new(big.Int).Add(spent, amount)
	limit := new(big.Int).Div(new(big.Int).Mul(baseAlloc, big.NewInt(int64(limitPercent))), big.NewInt(100))
	if newSpent.Cmp(limit) > 0 {
		return st, fmt.Errorf("%w: would spend %s within the current window (limit %s, %d%% of window-start balance %s)",
			ErrFloatVelocityExceeded, newSpent.String(), limit.String(), limitPercent, baseAlloc.String())
	}
	return FloatVelocityState{WindowStart: windowStart, WindowBaseAlloc: baseAlloc, Spent: newSpent}, nil
}

// MarkClaimed lets messageID's DESTINATION chain resolve it exactly once.
func MarkClaimed(store Store, messageID common.Hash, outcome FloatOutcome, cert cm.Sign) error {
	if outcome != FloatOutcomeCredited && outcome != FloatOutcomeRefund {
		return fmt.Errorf("MarkClaimed: outcome must be Credited or Refund, got %d", outcome)
	}
	rec, found, err := store.GetTransferRecord(messageID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("MarkClaimed: %w: %s", ErrFloatUnknownMessage, messageID.Hex())
	}
	cur, err := store.GetClaimed(messageID)
	if err != nil {
		return err
	}
	if cur != FloatOutcomeNone {
		return fmt.Errorf("MarkClaimed: %w: %s", ErrFloatAlreadyResolved, messageID.Hex())
	}
	
	digest := ComputeMarkClaimedMessage(messageID, outcome)
	if !bls.VerifySign(rec.DestKey, cert, digest) {
		return fmt.Errorf("MarkClaimed: %w", ErrInvalidSignature)
	}
	return store.SetClaimed(messageID, outcome)
}

// ReclaimFloat lets messageID's SOURCE chain take the value back once reclaimTimeoutSeconds have passed.
func ReclaimFloat(store Store, messageID common.Hash, cert cm.Sign, blockTime, reclaimTimeoutSeconds uint64) error {
	rec, found, err := store.GetTransferRecord(messageID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("ReclaimFloat: %w: %s", ErrFloatUnknownMessage, messageID.Hex())
	}
	if rec.SourceKey == nil {
		return fmt.Errorf("ReclaimFloat: %w: %s is a direct deposit", ErrFloatNotReclaimable, messageID.Hex())
	}
	cur, err := store.GetClaimed(messageID)
	if err != nil {
		return err
	}
	if cur != FloatOutcomeNone {
		return fmt.Errorf("ReclaimFloat: %w: %s", ErrFloatAlreadyResolved, messageID.Hex())
	}
	if reclaimTimeoutSeconds == 0 || blockTime < rec.ConfirmedAtBlockTime || blockTime-rec.ConfirmedAtBlockTime < reclaimTimeoutSeconds {
		return ErrFloatReclaimTooEarly
	}
	
	digest := ComputeReclaimFloatMessage(messageID, *rec.SourceKey)
	if !bls.VerifySign(*rec.SourceKey, cert, digest) {
		return fmt.Errorf("ReclaimFloat: %w", ErrInvalidSignature)
	}

	destHash := crypto.Keccak256Hash(rec.DestKey[:])
	srcHash := crypto.Keccak256Hash(rec.SourceKey[:])

	destBal, err := store.GetFloat(destHash)
	if err != nil {
		return err
	}
	if destBal.Cmp(rec.Value) < 0 {
		return fmt.Errorf("ReclaimFloat: dest balance %s is below the %s being reclaimed for %s", destBal.String(), rec.Value.String(), messageID.Hex())
	}
	srcBal, err := store.GetFloat(srcHash)
	if err != nil {
		return err
	}
	newDest := new(big.Int).Sub(destBal, rec.Value)
	newSrc := new(big.Int).Add(srcBal, rec.Value)

	if err := store.SetClaimed(messageID, FloatOutcomeReclaimed); err != nil {
		return err
	}
	if err := store.SetFloat(destHash, newDest); err != nil {
		return err
	}
	return store.SetFloat(srcHash, newSrc)
}

// RegisterAccount allows a user to register to a float account with signatures from both.
func RegisterAccount(store Store, userAddress common.Address, floatIdentityKey cm.PublicKey, userSig []byte, clusterSig cm.Sign) error {
	_, found, err := store.GetAccountRegistry(userAddress)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("RegisterAccount: %w", ErrAccountAlreadyRegistered)
	}

	digest := ComputeRegisterAccountMessage(userAddress, floatIdentityKey)

	// Verify the cluster's BLS signature first
	if !bls.VerifySign(floatIdentityKey, clusterSig, digest) {
		return fmt.Errorf("RegisterAccount: cluster %w", ErrInvalidSignature)
	}

	// Check if this is a self-registration by the Exec Node itself.
	// The Exec Node's address is derived directly from its BLS public key.
	hash := crypto.Keccak256(floatIdentityKey.Bytes())
	blsDerivedAddress := common.BytesToAddress(hash[12:])
	
	if userAddress == blsDerivedAddress {
		// It's a self-registration, the cluster BLS signature is sufficient.
	} else {
		// Normal user registration requires a valid ECDSA signature.
		userHash := crypto.Keccak256Hash(digest)
		pubKey, err := crypto.SigToPub(userHash.Bytes(), userSig)
		if err != nil {
			return fmt.Errorf("RegisterAccount: invalid user signature: %w", err)
		}
		recoveredAddr := crypto.PubkeyToAddress(*pubKey)
		if recoveredAddr != userAddress {
			return fmt.Errorf("RegisterAccount: user signature address mismatch: got %s, want %s", recoveredAddr.Hex(), userAddress.Hex())
		}
	}

	return store.SetAccountRegistry(userAddress, floatIdentityKey)
}

// CheckFloatSupplyInvariant verifies that the sum of all NodeFloatAccounts equals the total deposited.
func CheckFloatSupplyInvariant(store Store) error {
	keys, err := store.GetAllChainRegistryKeys()
	if err != nil {
		return err
	}
	
	sum := big.NewInt(0)
	for _, keyHash := range keys {
		bal, err := store.GetFloat(keyHash)
		if err != nil {
			return fmt.Errorf("CheckFloatSupplyInvariant: key %s: %w", keyHash.Hex(), err)
		}
		if bal.Sign() < 0 {
			return fmt.Errorf("CheckFloatSupplyInvariant: key %s balance is negative: %s", keyHash.Hex(), bal.String())
		}
		sum.Add(sum, bal)
	}
	
	total, err := store.GetFloat(floatTotalSupplyKey)
	if err != nil {
		return err
	}
	if sum.Cmp(total) != 0 {
		return fmt.Errorf("CheckFloatSupplyInvariant: sum of every NodeFloatAccount (%s) != total ever deposited (%s)", sum.String(), total.String())
	}
	return nil
}

func ComputeSubmitStateRootMessage(clusterPubKey cm.PublicKey, epoch uint64, stateRoot common.Hash) []byte {
	var epochBytes [8]byte
	binary.BigEndian.PutUint64(epochBytes[:], epoch)
	data := append(clusterPubKey[:], epochBytes[:]...)
	data = append(data, stateRoot.Bytes()...)
	return data
}

func SubmitStateRoot(store Store, clusterPubKey cm.PublicKey, epoch uint64, stateRoot common.Hash, cert cm.Sign) error {
	clusterHash := crypto.Keccak256Hash(clusterPubKey[:])
	
	// Check if cluster exists
	_, found, err := store.GetChainRegistry(clusterHash)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("SubmitStateRoot: unknown cluster")
	}

	digest := ComputeSubmitStateRootMessage(clusterPubKey, epoch, stateRoot)
	if !bls.VerifySign(clusterPubKey, cert, digest) {
		return fmt.Errorf("SubmitStateRoot: %w", ErrInvalidSignature)
	}

	// Verify we are not submitting a root for an older epoch than what we have?
	// The implementation can just accept whatever epoch as long as it's signed.
	// But it might be good to prevent overwriting with older roots.
	// We just overwrite the epoch's state root.
	
	_, found, err = store.GetStateRoot(clusterHash, epoch)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("SubmitStateRoot: state root for epoch %d already submitted", epoch)
	}

	return store.SetStateRoot(clusterHash, epoch, stateRoot)
}
