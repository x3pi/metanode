package processor

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types/network"
)

type countingConnManager struct {
	*MockConnectionsManager
	addCalled int
	lastConn  network.Connection
	lastAddr  common.Address
}

func (c *countingConnManager) AddConnectionWithAddress(conn network.Connection, addr common.Address, replace bool, cType string) {
	c.addCalled++
	c.lastConn = conn
	c.lastAddr = addr
	c.MockConnectionsManager.AddConnectionWithAddress(conn, addr, replace, cType)
}

func TestProcessInitConnection_DisconnectedConnBailsOut(t *testing.T) {
	cm := &countingConnManager{MockConnectionsManager: NewMockConnectionsManager()}
	p := NewConnectionProcessor(cm)

	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	conn := NewMockConnection(addr)

	// Simulate connection already disconnected (e.g. after 30s timeout in HandleConnection)
	require.NoError(t, conn.Disconnect())
	assert.False(t, conn.IsConnect())

	initData := &pb.InitConnection{
		Address: addr.Bytes(),
		Type:    p_common.CLIENT_CONNECTION_TYPE,
		Replace: false,
	}
	body, err := proto.Marshal(initData)
	require.NoError(t, err)

	msg := &mockInitMessage{
		MockMessage: NewMockMessage(p_common.InitConnection, body),
		initData:    initData,
	}
	req := NewMockRequest(conn, msg)

	err = p.ProcessInitConnection(req)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection đã disconnect trước khi add vào manager")
	assert.Equal(t, 0, cm.addCalled, "zombie connection must not be added to connectionsManager")
}

func TestProcessInitConnection_ConnectedConnSucceeds(t *testing.T) {
	cm := &countingConnManager{MockConnectionsManager: NewMockConnectionsManager()}
	p := NewConnectionProcessor(cm)

	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	conn := NewMockConnection(addr)
	assert.True(t, conn.IsConnect())

	initData := &pb.InitConnection{
		Address: addr.Bytes(),
		Type:    p_common.CLIENT_CONNECTION_TYPE,
		Replace: false,
	}
	body, err := proto.Marshal(initData)
	require.NoError(t, err)

	msg := &mockInitMessage{
		MockMessage: NewMockMessage(p_common.InitConnection, body),
		initData:    initData,
	}
	req := NewMockRequest(conn, msg)

	err = p.ProcessInitConnection(req)
	require.NoError(t, err)
	assert.Equal(t, 1, cm.addCalled)
	assert.Equal(t, addr, cm.lastAddr)
}

type mockInitMessage struct {
	*MockMessage
	initData *pb.InitConnection
}

func (m *mockInitMessage) Unmarshal(pm proto.Message) error {
	if target, ok := pm.(*pb.InitConnection); ok {
		proto.Reset(target)
		proto.Merge(target, m.initData)
		return nil
	}
	return m.MockMessage.Unmarshal(pm)
}

