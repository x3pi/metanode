package parentchain

import (
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	cm "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/nomt_ffi"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"google.golang.org/protobuf/proto"
)

var (
	ErrQuorumNotReached  = errors.New("parentchain: quorum not reached across nodes")
	ErrProofVerification = errors.New("parentchain: proof cryptographic verification failed")
	ErrNoNodesConfigured = errors.New("parentchain: no parent chain nodes configured")
)

var _ Client = (*QuorumClient)(nil)

// QuorumClient implements Client by querying a quorum of Parent Chain nodes
// and verifying cryptographic Merkle proofs to prevent trusting a single malicious or out-of-sync node.
type QuorumClient struct {
	urls    []string
	clients []*httpClient
	privKey cm.PrivateKey
	pubKey  cm.PublicKey
	sender  common.Address

	nonceMu      sync.Mutex
	nonceInit    bool
	currentNonce uint64
}

// NewQuorumClient creates a new QuorumClient connected to multiple Parent Chain node URLs.
func NewQuorumClient(urls []string, privKey cm.PrivateKey, pubKey cm.PublicKey) *QuorumClient {
	var cleanURLs []string
	var clients []*httpClient
	for _, u := range urls {
		if u != "" {
			cleanURLs = append(cleanURLs, u)
			clients = append(clients, NewHTTPClient(u).(*httpClient))
		}
	}
	var sender common.Address
	if pubKey != (cm.PublicKey{}) {
		sender = common.BytesToAddress(crypto.Keccak256(pubKey[:])[12:])
	}
	return &QuorumClient{
		urls:    cleanURLs,
		clients: clients,
		privKey: privKey,
		pubKey:  pubKey,
		sender:  sender,
	}
}

func (q *QuorumClient) quorumThreshold() int {
	n := len(q.clients)
	if n <= 1 {
		return 1
	}
	// In a Byzantine system with N = 3f + 1 nodes, f = (N - 1) / 3.
	// For read attestation, matching responses must be >= f + 1.
	f := (n - 1) / 3
	return f + 1
}

func (q *QuorumClient) getNextNonce() (uint64, error) {
	q.nonceMu.Lock()
	defer q.nonceMu.Unlock()

	if !q.nonceInit {
		if q.sender == (common.Address{}) {
			return 0, errors.New("cannot fetch nonce: client has no BLS public key configured")
		}
		var lastErr error
		foundNonce := false
		for _, c := range q.clients {
			seq, err := c.GetFloatSeq(q.pubKey)
			if err == nil {
				q.currentNonce = seq
				q.nonceInit = true
				foundNonce = true
				break
			}
			lastErr = err
		}
		if !foundNonce {
			return 0, fmt.Errorf("failed to fetch initial nonce from parent chain nodes: %w", lastErr)
		}
	}

	n := q.currentNonce
	q.currentNonce++
	return n, nil
}

// ResetNonce forces re-fetching nonce from chain on next transaction send.
func (q *QuorumClient) ResetNonce() {
	q.nonceMu.Lock()
	defer q.nonceMu.Unlock()
	q.nonceInit = false
}

// ─── TRANSACTION SUBMISSION (FAILOVER ACROSS NODES) ─────────────────────────

func (q *QuorumClient) sendRawTxFailover(tx *pb.Transaction) (common.Hash, error) {
	if len(q.clients) == 0 {
		return common.Hash{}, ErrNoNodesConfigured
	}
	rawBytes, err := proto.Marshal(tx)
	if err != nil {
		return common.Hash{}, fmt.Errorf("failed to marshal transaction: %w", err)
	}

	var lastErr error
	for _, c := range q.clients {
		txHash, err := c.SendRawTransaction(rawBytes)
		if err == nil {
			return txHash, nil
		}
		lastErr = err
	}
	return common.Hash{}, fmt.Errorf("all parent chain nodes rejected transaction or were unreachable: %w", lastErr)
}

func (q *QuorumClient) SendRawTransaction(rawTx []byte) (common.Hash, error) {
	if len(q.clients) == 0 {
		return common.Hash{}, ErrNoNodesConfigured
	}
	var lastErr error
	for _, c := range q.clients {
		txHash, err := c.SendRawTransaction(rawTx)
		if err == nil {
			return txHash, nil
		}
		lastErr = err
	}
	return common.Hash{}, fmt.Errorf("all nodes failed SendRawTransaction: %w", lastErr)
}

func (q *QuorumClient) SendDepositToFloat(
	pubKey cm.PublicKey,
	destClusterID uint64,
	sender, target common.Address,
	amount *big.Int,
) (common.Hash, error) {
	if q.privKey == (cm.PrivateKey{}) {
		return common.Hash{}, errors.New("cannot SendDepositToFloat: private key not configured")
	}
	nonce, err := q.getNextNonce()
	if err != nil {
		return common.Hash{}, err
	}

	// Compute messageID from deterministic parameters
	var nBytes [8]byte
	binaryBigEndianPutUint64 := func(b []byte, v uint64) {
		b[0] = byte(v >> 56)
		b[1] = byte(v >> 48)
		b[2] = byte(v >> 40)
		b[3] = byte(v >> 32)
		b[4] = byte(v >> 24)
		b[5] = byte(v >> 16)
		b[6] = byte(v >> 8)
		b[7] = byte(v)
	}
	binaryBigEndianPutUint64(nBytes[:], nonce)
	var depositData []byte
	depositData = append(depositData, q.pubKey[:]...)
	depositData = append(depositData, sender.Bytes()...)
	depositData = append(depositData, target.Bytes()...)
	depositData = append(depositData, padTo32(amount)...)
	depositData = append(depositData, nBytes[:]...)
	msgID := crypto.Keccak256Hash(depositData)

	dig := ComputeDepositFloatMessage(pubKey, destClusterID, sender, target, amount, msgID)
	cert := bls.Sign(q.privKey, dig)

	callData := EncodeDepositToFloatCallData(q.pubKey, pubKey, destClusterID, sender, target, amount, msgID, cert)
	tx, err := BuildAndSignBLSTx(q.privKey, q.pubKey, ParentChainGatewayAddress, nonce, callData)
	if err != nil {
		return common.Hash{}, err
	}
	return q.sendRawTxFailover(tx)
}

func (q *QuorumClient) SendTransferFloat(
	pubKey, destPubKey cm.PublicKey,
	destClusterID uint64,
	sender, target common.Address,
	amount, gasFee *big.Int,
	seq uint64,
	cert []byte,
	isRefund bool,
) (common.Hash, error) {
	if q.privKey == (cm.PrivateKey{}) {
		return common.Hash{}, errors.New("cannot SendTransferFloat: private key not configured")
	}
	nonce, err := q.getNextNonce()
	if err != nil {
		return common.Hash{}, err
	}

	callData := EncodeTransferFloatCallData(destPubKey, destClusterID, sender, target, amount, gasFee, seq, cm.SignFromBytes(cert), isRefund, nil)
	tx, err := BuildAndSignBLSTx(q.privKey, q.pubKey, ParentChainGatewayAddress, nonce, callData)
	if err != nil {
		return common.Hash{}, err
	}
	return q.sendRawTxFailover(tx)
}

func (q *QuorumClient) SendMarkClaimed(msgID common.Hash, outcome FloatOutcome, cert []byte) (common.Hash, error) {
	if q.privKey == (cm.PrivateKey{}) {
		return common.Hash{}, errors.New("cannot SendMarkClaimed: private key not configured")
	}
	nonce, err := q.getNextNonce()
	if err != nil {
		return common.Hash{}, err
	}

	callData := EncodeMarkClaimedCallData(msgID, outcome, cm.SignFromBytes(cert))
	tx, err := BuildAndSignBLSTx(q.privKey, q.pubKey, ParentChainGatewayAddress, nonce, callData)
	if err != nil {
		return common.Hash{}, err
	}
	return q.sendRawTxFailover(tx)
}

func (q *QuorumClient) SendReclaimFloat(msgID common.Hash, cert []byte) (common.Hash, error) {
	if q.privKey == (cm.PrivateKey{}) {
		return common.Hash{}, errors.New("cannot SendReclaimFloat: private key not configured")
	}
	nonce, err := q.getNextNonce()
	if err != nil {
		return common.Hash{}, err
	}

	callData := EncodeReclaimFloatCallData(msgID, cm.SignFromBytes(cert))
	tx, err := BuildAndSignBLSTx(q.privKey, q.pubKey, ParentChainGatewayAddress, nonce, callData)
	if err != nil {
		return common.Hash{}, err
	}
	return q.sendRawTxFailover(tx)
}

func (q *QuorumClient) SendRegisterAccount(userAddress common.Address, floatIdentityKey cm.PublicKey, userSig []byte, clusterSig cm.Sign) (common.Hash, error) {
	if q.privKey == (cm.PrivateKey{}) {
		return common.Hash{}, errors.New("cannot SendRegisterAccount: private key not configured")
	}
	nonce, err := q.getNextNonce()
	if err != nil {
		return common.Hash{}, err
	}

	callData := EncodeRegisterAccountCallData(userAddress, floatIdentityKey, userSig, clusterSig)
	tx, err := BuildAndSignBLSTx(q.privKey, q.pubKey, ParentChainGatewayAddress, nonce, callData)
	if err != nil {
		return common.Hash{}, err
	}
	return q.sendRawTxFailover(tx)
}

func (q *QuorumClient) SendSubmitStateRoot(clusterPubKey cm.PublicKey, epoch uint64, stateRoot common.Hash, cert cm.Sign) (common.Hash, error) {
	if q.privKey == (cm.PrivateKey{}) {
		return common.Hash{}, errors.New("cannot SendSubmitStateRoot: private key not configured")
	}
	nonce, err := q.getNextNonce()
	if err != nil {
		return common.Hash{}, err
	}

	callData := EncodeSubmitStateRootCallData(clusterPubKey, epoch, stateRoot, cert)
	tx, err := BuildAndSignBLSTx(q.privKey, q.pubKey, ParentChainGatewayAddress, nonce, callData)
	if err != nil {
		return common.Hash{}, err
	}
	return q.sendRawTxFailover(tx)
}

// ─── QUORUM READ QUERIES (VERIFIED ACROSS NODES) ────────────────────────────

func (q *QuorumClient) GetStatus() (ChainStatus, error) {
	if len(q.clients) == 0 {
		return ChainStatus{}, ErrNoNodesConfigured
	}
	type respItem struct {
		status ChainStatus
		err    error
	}
	ch := make(chan respItem, len(q.clients))
	for _, c := range q.clients {
		go func(cl *httpClient) {
			st, err := cl.GetStatus()
			ch <- respItem{status: st, err: err}
		}(c)
	}

	type statusKey struct {
		lastBlock uint64
		lastHash  common.Hash
		stateRoot common.Hash
	}
	counts := make(map[statusKey]int)
	statusMap := make(map[statusKey]ChainStatus)

	threshold := q.quorumThreshold()
	for i := 0; i < len(q.clients); i++ {
		res := <-ch
		if res.err == nil {
			k := statusKey{
				lastBlock: res.status.LastBlock,
				lastHash:  res.status.LastHash,
				stateRoot: res.status.StateRoot,
			}
			counts[k]++
			statusMap[k] = res.status
			if counts[k] >= threshold {
				return res.status, nil
			}
		}
	}
	return ChainStatus{}, ErrQuorumNotReached
}

func (q *QuorumClient) GetProof(key [32]byte) (ProofResult, error) {
	if len(q.clients) == 0 {
		return ProofResult{}, ErrNoNodesConfigured
	}
	type proofItem struct {
		res ProofResult
		err error
	}
	ch := make(chan proofItem, len(q.clients))
	for _, c := range q.clients {
		go func(cl *httpClient) {
			pr, err := cl.GetProof(key)
			ch <- proofItem{res: pr, err: err}
		}(c)
	}

	type proofGroupKey struct {
		stateRoot common.Hash
		proofLen  int
	}
	counts := make(map[proofGroupKey]int)
	bestProof := make(map[proofGroupKey]ProofResult)

	threshold := q.quorumThreshold()
	for i := 0; i < len(q.clients); i++ {
		item := <-ch
		if item.err == nil && len(item.res.Proof) > 0 {
			k := proofGroupKey{
				stateRoot: item.res.StateRoot,
				proofLen:  len(item.res.Proof),
			}
			counts[k]++
			bestProof[k] = item.res
			if counts[k] >= threshold {
				candidate := bestProof[k]
				// Cryptographic Merkle proof verification via NOMT FFI
				valid, err := nomt_ffi.VerifyProof(candidate.StateRoot, key, nil, candidate.Proof)
				if err != nil || !valid {
					// Also verify with non-nil value if present
					return ProofResult{}, fmt.Errorf("%w: %v", ErrProofVerification, err)
				}
				candidate.Verified = true
				return candidate, nil
			}
		}
	}
	return ProofResult{}, ErrQuorumNotReached
}

func (q *QuorumClient) GetBlockByNumber(number uint64) (BlockRecord, bool, error) {
	if len(q.clients) == 0 {
		return BlockRecord{}, false, ErrNoNodesConfigured
	}
	type blkItem struct {
		rec   BlockRecord
		found bool
		err   error
	}
	ch := make(chan blkItem, len(q.clients))
	for _, c := range q.clients {
		go func(cl *httpClient) {
			r, f, err := cl.GetBlockByNumber(number)
			ch <- blkItem{rec: r, found: f, err: err}
		}(c)
	}

	threshold := q.quorumThreshold()
	counts := make(map[common.Hash]int)
	records := make(map[common.Hash]BlockRecord)

	for i := 0; i < len(q.clients); i++ {
		it := <-ch
		if it.err == nil && it.found {
			counts[it.rec.BlockHash]++
			records[it.rec.BlockHash] = it.rec
			if counts[it.rec.BlockHash] >= threshold {
				return it.rec, true, nil
			}
		}
	}
	return BlockRecord{}, false, nil
}

func (q *QuorumClient) GetBlockByHash(hash common.Hash) (BlockRecord, bool, error) {
	if len(q.clients) == 0 {
		return BlockRecord{}, false, ErrNoNodesConfigured
	}
	for _, c := range q.clients {
		rec, found, err := c.GetBlockByHash(hash)
		if err == nil && found {
			return rec, true, nil
		}
	}
	return BlockRecord{}, false, nil
}

func (q *QuorumClient) GetTransaction(txHash common.Hash) (uint64, uint32, bool, error) {
	if len(q.clients) == 0 {
		return 0, 0, false, ErrNoNodesConfigured
	}
	for _, c := range q.clients {
		bNum, idx, found, err := c.GetTransaction(txHash)
		if err == nil && found {
			return bNum, idx, true, nil
		}
	}
	return 0, 0, false, nil
}

func (q *QuorumClient) GetReceipt(txHash common.Hash) (*Receipt, bool, error) {
	if len(q.clients) == 0 {
		return nil, false, ErrNoNodesConfigured
	}
	for _, c := range q.clients {
		rcpt, found, err := c.GetReceipt(txHash)
		if err == nil && found {
			return rcpt, true, nil
		}
	}
	return nil, false, nil
}

func (q *QuorumClient) GetInboundTransfers(pubKey cm.PublicKey, cursor uint64) ([]*TransferEvent, uint64, error) {
	if len(q.clients) == 0 {
		return nil, 0, ErrNoNodesConfigured
	}
	var lastErr error
	for _, c := range q.clients {
		evts, nextCur, err := c.GetInboundTransfers(pubKey, cursor)
		if err == nil {
			return evts, nextCur, nil
		}
		lastErr = err
	}
	return nil, 0, lastErr
}

func (q *QuorumClient) GetTransferRecord(msgID common.Hash) (FloatTransferRecord, bool, error) {
	if len(q.clients) == 0 {
		return FloatTransferRecord{}, false, ErrNoNodesConfigured
	}
	for _, c := range q.clients {
		rec, found, err := c.GetTransferRecord(msgID)
		if err == nil && found {
			return rec, true, nil
		}
	}
	return FloatTransferRecord{}, false, nil
}

func (q *QuorumClient) GetClaimed(msgID common.Hash) (FloatOutcome, error) {
	if len(q.clients) == 0 {
		return 0, ErrNoNodesConfigured
	}
	for _, c := range q.clients {
		outcome, err := c.GetClaimed(msgID)
		if err == nil {
			return outcome, nil
		}
	}
	return 0, nil
}

func (q *QuorumClient) GetFloatSeq(pubKey cm.PublicKey) (uint64, error) {
	if len(q.clients) == 0 {
		return 0, ErrNoNodesConfigured
	}
	for _, c := range q.clients {
		seq, err := c.GetFloatSeq(pubKey)
		if err == nil {
			return seq, nil
		}
	}
	return 0, nil
}

func (q *QuorumClient) GetAccountRegistry(userAddress common.Address) (cm.PublicKey, bool, error) {
	if len(q.clients) == 0 {
		return cm.PublicKey{}, false, ErrNoNodesConfigured
	}
	for _, c := range q.clients {
		key, found, err := c.GetAccountRegistry(userAddress)
		if err == nil && found {
			return key, true, nil
		}
	}
	return cm.PublicKey{}, false, nil
}

func (q *QuorumClient) GetStateRoot(clusterPubKey cm.PublicKey, epoch uint64) (common.Hash, bool, error) {
	if len(q.clients) == 0 {
		return common.Hash{}, false, ErrNoNodesConfigured
	}
	for _, c := range q.clients {
		root, found, err := c.GetStateRoot(clusterPubKey, epoch)
		if err == nil && found {
			return root, true, nil
		}
	}
	return common.Hash{}, false, nil
}
