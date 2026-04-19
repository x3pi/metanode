package processor

import (
	"math/big"
	"net"
	"sync"

	e_common "github.com/ethereum/go-ethereum/common"
	e_types "github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

// ============================================================================
// MockConnection — implements network.Connection
// ============================================================================

type MockConnection struct {
	address    e_common.Address
	connected  bool
	connType   string
	remoteAddr string
	sentCount  int
}

func NewMockConnection(addr e_common.Address) *MockConnection {
	return &MockConnection{
		address:    addr,
		connected:  true,
		connType:   "test",
		remoteAddr: "127.0.0.1:9999",
	}
}

func (mc *MockConnection) Address() e_common.Address          { return mc.address }
func (mc *MockConnection) ConnectionAddress() (string, error) { return mc.remoteAddr, nil }
func (mc *MockConnection) TcpLocalAddr() net.Addr             { return nil }
func (mc *MockConnection) TcpRemoteAddr() net.Addr            { return nil }
func (mc *MockConnection) RequestChan() (chan network.Request, chan error) {
	return make(chan network.Request), make(chan error)
}
func (mc *MockConnection) Type() string           { return mc.connType }
func (mc *MockConnection) String() string         { return "mock-connection" }
func (mc *MockConnection) RemoteAddrSafe() string { return mc.remoteAddr }
func (mc *MockConnection) RemoteAddr() string     { return mc.remoteAddr }
func (mc *MockConnection) Init(addr e_common.Address, t string) {
	mc.address = addr
	mc.connType = t
}
func (mc *MockConnection) SetRealConnAddr(addr string)           { mc.remoteAddr = addr }
func (mc *MockConnection) SendMessage(msg network.Message) error { mc.sentCount++; return nil }
func (mc *MockConnection) SentCount() int                        { return mc.sentCount }
func (mc *MockConnection) Connect() error                        { mc.connected = true; return nil }
func (mc *MockConnection) Disconnect() error                     { mc.connected = false; return nil }
func (mc *MockConnection) IsConnect() bool                       { return mc.connected }
func (mc *MockConnection) ReadRequest()                          {}
func (mc *MockConnection) Clone() network.Connection             { return mc }

// ============================================================================
// MockMessage — implements network.Message
// ============================================================================

type MockMessage struct {
	body    []byte
	command string
}

func NewMockMessage(command string, body []byte) *MockMessage {
	return &MockMessage{command: command, body: body}
}

func (mm *MockMessage) Marshal() ([]byte, error)                        { return mm.body, nil }
func (mm *MockMessage) String() string                                  { return "mock-message" }
func (mm *MockMessage) Unmarshal(proto protoreflect.ProtoMessage) error { return nil }
func (mm *MockMessage) Command() string                                 { return mm.command }
func (mm *MockMessage) Body() []byte                                    { return mm.body }
func (mm *MockMessage) Pubkey() common.PublicKey                        { return common.PublicKey{} }
func (mm *MockMessage) Sign() common.Sign                               { return common.Sign{} }
func (mm *MockMessage) ToAddress() e_common.Address                     { return e_common.Address{} }
func (mm *MockMessage) ID() string                                      { return "mock-msg-id" }
func (mm *MockMessage) Version() string                                 { return "1.0" }

// ============================================================================
// MockRequest — implements network.Request
// ============================================================================

type MockRequest struct {
	conn network.Connection
	msg  network.Message
}

func NewMockRequest(conn network.Connection, msg network.Message) *MockRequest {
	return &MockRequest{conn: conn, msg: msg}
}

func (mr *MockRequest) Message() network.Message       { return mr.msg }
func (mr *MockRequest) Connection() network.Connection { return mr.conn }
func (mr *MockRequest) Reset(conn network.Connection, msg network.Message) {
	mr.conn = conn
	mr.msg = msg
}

// ============================================================================
// MockMessageSender — implements network.MessageSender
// Records all sent messages for assertions.
// ============================================================================

type SentMessage struct {
	Connection network.Connection
	Command    string
	Data       []byte
}

type MockMessageSender struct {
	mu   sync.Mutex
	Sent []SentMessage
}

func NewMockMessageSender() *MockMessageSender {
	return &MockMessageSender{Sent: make([]SentMessage, 0)}
}

func (ms *MockMessageSender) SendMessage(conn network.Connection, command string, pbMessage protoreflect.ProtoMessage) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.Sent = append(ms.Sent, SentMessage{Connection: conn, Command: command})
	return nil
}

func (ms *MockMessageSender) SendMessage2(conn network.Connection, command string, pbMessage protoreflect.ProtoMessage) error {
	return ms.SendMessage(conn, command, pbMessage)
}

func (ms *MockMessageSender) SendBytes(conn network.Connection, command string, b []byte) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	ms.Sent = append(ms.Sent, SentMessage{Connection: conn, Command: command, Data: b})
	return nil
}

func (ms *MockMessageSender) BroadcastMessage(conns map[e_common.Address]network.Connection, command string, marshaler network.Marshaler) error {
	return nil
}

func (ms *MockMessageSender) SentCount() int {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	return len(ms.Sent)
}

// ============================================================================
// MockConnectionsManager — implements network.ConnectionsManager
// ============================================================================

type MockConnectionsManager struct {
	connections map[int]map[e_common.Address]network.Connection
}

func NewMockConnectionsManager() *MockConnectionsManager {
	return &MockConnectionsManager{
		connections: make(map[int]map[e_common.Address]network.Connection),
	}
}

func (mcm *MockConnectionsManager) AddConnectionForType(cType int, conn network.Connection) {
	if mcm.connections[cType] == nil {
		mcm.connections[cType] = make(map[e_common.Address]network.Connection)
	}
	mcm.connections[cType][conn.Address()] = conn
}

func (mcm *MockConnectionsManager) ConnectionsByType(cType int) map[e_common.Address]network.Connection {
	return mcm.connections[cType]
}

func (mcm *MockConnectionsManager) ConnectionByTypeAndAddress(cType int, address e_common.Address) network.Connection {
	if conns, ok := mcm.connections[cType]; ok {
		return conns[address]
	}
	return nil
}

func (mcm *MockConnectionsManager) ConnectionsByTypeAndAddresses(cType int, addresses []e_common.Address) map[e_common.Address]network.Connection {
	result := make(map[e_common.Address]network.Connection)
	for _, addr := range addresses {
		if conn := mcm.ConnectionByTypeAndAddress(cType, addr); conn != nil {
			result[addr] = conn
		}
	}
	return result
}

func (mcm *MockConnectionsManager) FilterAddressAvailable(cType int, addresses map[e_common.Address]*uint256.Int) map[e_common.Address]*uint256.Int {
	return addresses
}

func (mcm *MockConnectionsManager) ParentConnection() network.Connection                    { return nil }
func (mcm *MockConnectionsManager) AddConnection(conn network.Connection, _ bool, _ string) {}
func (mcm *MockConnectionsManager) AddConnectionWithAddress(conn network.Connection, addr e_common.Address, _ bool, _ string) {
}
func (mcm *MockConnectionsManager) RemoveConnection(conn network.Connection)    {}
func (mcm *MockConnectionsManager) AddParentConnection(conn network.Connection) {}
func (mcm *MockConnectionsManager) Stats() *pb.NetworkStats                     { return &pb.NetworkStats{} }

// ============================================================================
// MockTransaction — minimal mock for types.Transaction interface
// Only implements methods used in PendingTransactionManager tests.
// ============================================================================

type MockTransaction struct {
	hash       e_common.Hash
	rHash      e_common.Hash
	fromAddr   e_common.Address
	toAddr     e_common.Address
	nonce      uint64
	amount     *big.Int
	data       []byte
	isReadOnly bool
	txType     uint64 // mirrors proto.Type (field 16) — used only in test mock
}

func NewMockTransaction(from e_common.Address, to e_common.Address, nonce uint64) *MockTransaction {
	h := e_common.BytesToHash([]byte{byte(nonce), from[0], from[1], to[0]})
	return &MockTransaction{
		hash:     h,
		rHash:    h,
		fromAddr: from,
		toAddr:   to,
		nonce:    nonce,
		amount:   big.NewInt(0),
		data:     []byte{},
	}
}

func (mt *MockTransaction) Hash() e_common.Hash           { return mt.hash }
func (mt *MockTransaction) RHash() e_common.Hash          { return mt.rHash }
func (mt *MockTransaction) NewDeviceKey() e_common.Hash   { return e_common.Hash{} }
func (mt *MockTransaction) LastDeviceKey() e_common.Hash  { return e_common.Hash{} }
func (mt *MockTransaction) FromAddress() e_common.Address { return mt.fromAddr }
func (mt *MockTransaction) ToAddress() e_common.Address   { return mt.toAddr }
func (mt *MockTransaction) Sign() common.Sign             { return common.Sign{} }
func (mt *MockTransaction) Amount() *big.Int              { return mt.amount }
func (mt *MockTransaction) BRelatedAddresses() [][]byte   { return nil }
func (mt *MockTransaction) RelatedAddresses() []e_common.Address {
	return []e_common.Address{mt.fromAddr, mt.toAddr}
}
func (mt *MockTransaction) Data() []byte                          { return mt.data }
func (mt *MockTransaction) Fee(_ uint64) *big.Int                 { return big.NewInt(0) }
func (mt *MockTransaction) MaxGas() uint64                        { return 21000 }
func (mt *MockTransaction) MaxGasPrice() uint64                   { return 1 }
func (mt *MockTransaction) MaxTimeUse() uint64                    { return 0 }
func (mt *MockTransaction) MaxFee() *big.Int                      { return big.NewInt(0) }
func (mt *MockTransaction) GasTipCap() *big.Int                   { return big.NewInt(0) }
func (mt *MockTransaction) GasFeeCap() *big.Int                   { return big.NewInt(0) }
func (mt *MockTransaction) GetNonce() uint64                      { return mt.nonce }
func (mt *MockTransaction) GetChainID() uint64                    { return 1 }
func (mt *MockTransaction) ClearCacheHash()                       {}
func (mt *MockTransaction) GetNonce32Bytes() []byte               { return make([]byte, 32) }
func (mt *MockTransaction) Marshal() ([]byte, error)              { return mt.data, nil }
func (mt *MockTransaction) Unmarshal(b []byte) error              { mt.data = b; return nil }
func (mt *MockTransaction) Proto() protoreflect.ProtoMessage      { return nil }
func (mt *MockTransaction) FromProto(_ protoreflect.ProtoMessage) {}
func (mt *MockTransaction) String() string                        { return mt.hash.Hex() }
func (mt *MockTransaction) SetSign(_ common.PrivateKey)           {}
func (mt *MockTransaction) SetSignBytes(_ []byte)                 {}
func (mt *MockTransaction) SetNonce(n uint64)                     { mt.nonce = n }
func (mt *MockTransaction) SetFromAddress(addr e_common.Address)  { mt.fromAddr = addr }
func (mt *MockTransaction) SetToAddress(addr e_common.Address)    { mt.toAddr = addr }
func (mt *MockTransaction) CopyTransaction() types.Transaction    { return mt }
func (mt *MockTransaction) SetIsDebug(_ bool)                     {}
func (mt *MockTransaction) GetIsDebug() bool                      { return false }
func (mt *MockTransaction) ValidEthSign() bool                    { return true }
func (mt *MockTransaction) UpdateRelatedAddresses(_ [][]byte)     {}
func (mt *MockTransaction) AddRelatedAddress(_ e_common.Address)  {}
func (mt *MockTransaction) UpdateDeriver(_, _ e_common.Hash)      {}
func (mt *MockTransaction) SetReadOnly(v bool)                    { mt.isReadOnly = v }
func (mt *MockTransaction) GetReadOnly() bool                     { return mt.isReadOnly }
func (mt *MockTransaction) SetType(t uint64)                      { mt.txType = t }
func (mt *MockTransaction) GetType() uint64                       { return mt.txType }
func (mt *MockTransaction) ValidTx0(_ types.AccountState, _ string) (bool, int64) {
	return true, 0
}
func (mt *MockTransaction) ValidChainID(_ uint64) bool        { return true }
func (mt *MockTransaction) ValidSign(_ common.PublicKey) bool { return true }
func (mt *MockTransaction) ValidDeviceKey(_ types.AccountState) bool {
	return true
}
func (mt *MockTransaction) ValidMaxGas() bool                     { return true }
func (mt *MockTransaction) ValidMaxGasPrice(_ uint64) bool        { return true }
func (mt *MockTransaction) ValidAmount(_ types.AccountState) bool { return true }
func (mt *MockTransaction) ValidMaxFee(_ types.AccountState) bool { return true }
func (mt *MockTransaction) ValidAmountSpend(_ types.AccountState, _ *big.Int) bool {
	return true
}
func (mt *MockTransaction) ValidPendingUse(_ types.AccountState) bool { return true }
func (mt *MockTransaction) ValidDeploySmartContractToAccount(_ types.AccountState) bool {
	return true
}
func (mt *MockTransaction) ValidCallSmartContractToAccount(_ types.AccountState) bool { return true }
func (mt *MockTransaction) ValidDeployData() bool                                     { return true }
func (mt *MockTransaction) ValidCallData() bool                                       { return true }
func (mt *MockTransaction) RawSignatureValues() (v, r, s *big.Int) {
	return big.NewInt(0), big.NewInt(0), big.NewInt(0)
}
func (mt *MockTransaction) SetSignatureValues(_, _, _, _ *big.Int) {}
func (mt *MockTransaction) IsDeployContract() bool                 { return false }
func (mt *MockTransaction) IsCallContract() bool                   { return false }
func (mt *MockTransaction) IsRegularTransaction() bool             { return true }
func (mt *MockTransaction) ToJSONString() string                   { return "{}" }
func (mt *MockTransaction) DeployData() types.DeployData {
	return nil
}
func (mt *MockTransaction) CallData() types.CallData               { return nil }
func (mt *MockTransaction) ToEthTransaction() *e_types.Transaction { return nil }
