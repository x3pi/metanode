package state

import (
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ══════════════════════════════════════════════════════════════════════════════
// UpdateStateFields tests — CRUD, serialization roundtrips
// ══════════════════════════════════════════════════════════════════════════════

var testUSFAddr = e_common.HexToAddress("0x5555555555555555555555555555555555555555")

// ---------- Constructor ----------

func TestNewUpdateStateFields(t *testing.T) {
	usf := NewUpdateStateFields(testUSFAddr)
	assert.Equal(t, testUSFAddr, usf.Address())
	assert.Empty(t, usf.Fields())
}

// ---------- AddField ----------

func TestUpdateStateFields_AddField(t *testing.T) {
	usf := NewUpdateStateFields(testUSFAddr)
	usf.AddField(pb.UPDATE_STATE_FIELD_ADD_BALANCE, []byte("100"))
	usf.AddField(pb.UPDATE_STATE_FIELD_CODE_HASH, []byte{0, 0, 0, 5})

	fields := usf.Fields()
	assert.Equal(t, 2, len(fields))
	assert.Equal(t, pb.UPDATE_STATE_FIELD_ADD_BALANCE, fields[0].Field())
	assert.Equal(t, []byte("100"), fields[0].Value())
}

// ---------- UpdateField ----------

func TestUpdateField_GetterSetter(t *testing.T) {
	uf := NewUpdateField(pb.UPDATE_STATE_FIELD_SUB_BALANCE, []byte("val"))
	assert.Equal(t, pb.UPDATE_STATE_FIELD_SUB_BALANCE, uf.Field())
	assert.Equal(t, []byte("val"), uf.Value())
}

// ---------- Marshal / Unmarshal roundtrip ----------

func TestUpdateStateFields_MarshalUnmarshal(t *testing.T) {
	usf := NewUpdateStateFields(testUSFAddr)
	usf.AddField(pb.UPDATE_STATE_FIELD_ADD_BALANCE, []byte("1000"))
	usf.AddField(pb.UPDATE_STATE_FIELD_STORAGE_ROOT, []byte{0, 0, 0, 1})

	data, err := usf.Marshal()
	require.NoError(t, err)

	usf2 := NewUpdateStateFields(e_common.Address{})
	err = usf2.Unmarshal(data)
	require.NoError(t, err)

	assert.Equal(t, usf.Address(), usf2.Address())
	assert.Equal(t, len(usf.Fields()), len(usf2.Fields()))
	assert.Equal(t, usf.Fields()[0].Field(), usf2.Fields()[0].Field())
	assert.Equal(t, usf.Fields()[0].Value(), usf2.Fields()[0].Value())
}

// ---------- String ----------

func TestUpdateStateFields_String(t *testing.T) {
	usf := NewUpdateStateFields(testUSFAddr)
	usf.AddField(pb.UPDATE_STATE_FIELD_ADD_BALANCE, []byte("42"))
	s := usf.String()
	assert.Contains(t, s, "UpdateStateFields")
	assert.Contains(t, s, testUSFAddr.Hex())
}

// ---------- MarshalUpdateStateFieldsListWithBlockNumber ----------

func TestMarshalUnmarshal_UpdateStateFieldsListWithBlockNumber(t *testing.T) {
	usf1 := NewUpdateStateFields(testUSFAddr)
	usf1.AddField(pb.UPDATE_STATE_FIELD_ADD_BALANCE, []byte("500"))

	addr2 := e_common.HexToAddress("0x6666")
	usf2 := NewUpdateStateFields(addr2)
	usf2.AddField(pb.UPDATE_STATE_FIELD_LOGS_HASH, []byte{0, 0, 0, 3})

	list := []types.UpdateStateFields{usf1, usf2}
	blockNum := uint64(42)

	data, err := MarshalUpdateStateFieldsListWithBlockNumber(list, blockNum)
	require.NoError(t, err)

	decoded, bn, err := UnmarshalUpdateStateFieldsListWithBlockNumber(data)
	require.NoError(t, err)
	assert.Equal(t, blockNum, bn)
	assert.Equal(t, 2, len(decoded))
}
