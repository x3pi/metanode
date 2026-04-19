package state

import (
	"fmt"

	e_common "github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
)

type UpdateField struct {
	field pb.UPDATE_STATE_FIELD
	value []byte
}

func NewUpdateField(field pb.UPDATE_STATE_FIELD, value []byte) *UpdateField {
	return &UpdateField{
		field: field,
		value: value,
	}
}

func (uf *UpdateField) Field() pb.UPDATE_STATE_FIELD {
	return uf.field
}

func (uf *UpdateField) Value() []byte {
	return uf.value
}

func updateFieldsFromProto(pbFields []*pb.UpdateStateField) []types.UpdateField {
	fields := make([]types.UpdateField, len(pbFields))
	for i, pbField := range pbFields {
		fields[i] = &UpdateField{
			field: pbField.Field,
			value: pbField.Value,
		}
	}
	return fields
}

func updateFieldsToProto(fields []types.UpdateField) []*pb.UpdateStateField {
	pbFields := make([]*pb.UpdateStateField, len(fields))
	for i, field := range fields {
		pbFields[i] = &pb.UpdateStateField{
			Field: field.Field(),
			Value: field.Value(),
		}
	}
	return pbFields
}

type UpdateStateFields struct {
	address e_common.Address
	fields  []types.UpdateField
}

func NewUpdateStateFields(address e_common.Address) types.UpdateStateFields {
	return &UpdateStateFields{
		address: address,
		fields:  make([]types.UpdateField, 0),
	}
}

func (usf *UpdateStateFields) AddField(field pb.UPDATE_STATE_FIELD, value []byte) {
	usf.fields = append(usf.fields, &UpdateField{
		field: field,
		value: value,
	})
}

func (usf *UpdateStateFields) Address() e_common.Address {
	return usf.address
}

func (usf *UpdateStateFields) Fields() []types.UpdateField {
	return usf.fields
}

func (usf *UpdateStateFields) Unmarshal(data []byte) error {
	pbData := &pb.UpdateStateFields{}
	err := proto.Unmarshal(data, pbData)
	if err != nil {
		return err
	}
	usf.FromProto(pbData)
	return nil
}

func (usf *UpdateStateFields) Marshal() ([]byte, error) {
	return proto.MarshalOptions{Deterministic: true}.Marshal(usf.Proto())
}

func (usf *UpdateStateFields) String() string {
	str := fmt.Sprintf("UpdateStateFields{Address: %s, Fields: [", usf.address.Hex())
	for i, v := range usf.fields {
		if i > 0 {
			str += ", "
		}
		str += fmt.Sprintf(`{Field: %v, Value: %v}`, v.Field().String(), fmt.Sprintf("%x", v.Value()))
	}
	str += "]}"
	return str
}

func (usf *UpdateStateFields) Proto() protoreflect.ProtoMessage {
	return &pb.UpdateStateFields{
		Address: usf.address.Bytes(),
		Fields:  updateFieldsToProto(usf.fields),
	}
}

func (usf *UpdateStateFields) FromProto(pbData protoreflect.ProtoMessage) {
	pbFields := pbData.(*pb.UpdateStateFields)
	usf.address = e_common.BytesToAddress(pbFields.Address)
	usf.fields = updateFieldsFromProto(pbFields.Fields)
}

func MarshalUpdateStateFieldsListWithBlockNumber(
	usf []types.UpdateStateFields,
	blockNumber uint64,
) ([]byte, error) {
	usfPb := make([]*pb.UpdateStateFields, len(usf))
	for i, fields := range usf {
		usfPb[i] = fields.Proto().(*pb.UpdateStateFields)
	}
	pbData := &pb.UpdateStateFieldsListWithBlockNumber{
		Fields:      usfPb,
		BlockNumber: blockNumber,
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(pbData)
}

func UnmarshalUpdateStateFieldsListWithBlockNumber(
	data []byte,
) ([]types.UpdateStateFields, uint64, error) {
	pbData := &pb.UpdateStateFieldsListWithBlockNumber{}
	err := proto.Unmarshal(data, pbData)
	if err != nil {
		return nil, 0, err
	}
	usf := make([]types.UpdateStateFields, len(pbData.Fields))
	for i, pbFields := range pbData.Fields {
		usf[i] = NewUpdateStateFields(e_common.Address{})
		usf[i].FromProto(pbFields)
	}
	return usf, pbData.BlockNumber, nil
}
