// Package abi_helper cung cấp các helper dùng reflect để đọc field từ
// anonymous struct được go-ethereum ABI unpack trả về.
//
// Khi go-ethereum unpack một tuple/tuple[], nó tạo ra anonymous struct nội bộ
// có JSON tag gắn vào. Không thể type-assert trực tiếp vì Go so sánh toàn bộ
// type (kể cả JSON tags). Package này cung cấp cách đọc field an toàn qua reflect.
//
// Tất cả hàm đều có prefix Reflect để:
//   - Rõ ngữ nghĩa: cho biết hàm hoạt động thông qua reflection
//   - Tránh đặt tên trùng với kiểu dữ liệu (Field, Bool, Bytes dễ nhầm)
//   - Dễ search/grep trong codebase lớn
package abi_helper

import (
	"fmt"
	"math/big"
	"reflect"
	"strings"

	"github.com/ethereum/go-ethereum/common"
)

// ReflectField trả về reflect.Value của field có tên fieldName (case-insensitive).
// Hỗ trợ cả pointer-to-struct.
func ReflectField(v reflect.Value, fieldName string) (reflect.Value, error) {
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return reflect.Value{}, fmt.Errorf("abi_helper: expected struct, got %s", v.Kind())
	}
	f := v.FieldByNameFunc(func(name string) bool {
		return strings.EqualFold(name, fieldName)
	})
	if !f.IsValid() {
		return reflect.Value{}, fmt.Errorf("abi_helper: field %q not found in %s", fieldName, v.Type())
	}
	return f, nil
}

// ReflectUint8 đọc field kiểu uint8 (hoặc uint16/32/64/uint).
// Dùng cho các enum như EventKind, MessageType, v.v.
func ReflectUint8(v reflect.Value, fieldName string) (uint8, error) {
	f, err := ReflectField(v, fieldName)
	if err != nil {
		return 0, err
	}
	switch f.Kind() {
	case reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uint:
		return uint8(f.Uint()), nil
	default:
		return 0, fmt.Errorf("abi_helper: field %q has kind %s, expected uint", fieldName, f.Kind())
	}
}

// ReflectBigInt đọc field kiểu *big.Int. Nếu nil, trả về new(big.Int).
func ReflectBigInt(v reflect.Value, fieldName string) (*big.Int, error) {
	f, err := ReflectField(v, fieldName)
	if err != nil {
		return nil, err
	}
	if f.Kind() == reflect.Ptr && f.IsNil() {
		return new(big.Int), nil
	}
	b, ok := f.Interface().(*big.Int)
	if !ok {
		return nil, fmt.Errorf("abi_helper: field %q is not *big.Int (got %T)", fieldName, f.Interface())
	}
	if b == nil {
		return new(big.Int), nil
	}
	return b, nil
}

// ReflectAddress đọc field kiểu common.Address.
func ReflectAddress(v reflect.Value, fieldName string) (common.Address, error) {
	f, err := ReflectField(v, fieldName)
	if err != nil {
		return common.Address{}, err
	}
	a, ok := f.Interface().(common.Address)
	if !ok {
		return common.Address{}, fmt.Errorf("abi_helper: field %q is not common.Address (got %T)", fieldName, f.Interface())
	}
	return a, nil
}

// ReflectBytes đọc field kiểu []byte. Nếu nil, trả về []byte{}.
func ReflectBytes(v reflect.Value, fieldName string) ([]byte, error) {
	f, err := ReflectField(v, fieldName)
	if err != nil {
		return nil, err
	}
	if f.Kind() == reflect.Slice && f.IsNil() {
		return []byte{}, nil
	}
	b, ok := f.Interface().([]byte)
	if !ok {
		return nil, fmt.Errorf("abi_helper: field %q is not []byte (got %T)", fieldName, f.Interface())
	}
	return b, nil
}

// ReflectBytes32 đọc field kiểu [32]byte.
func ReflectBytes32(v reflect.Value, fieldName string) ([32]byte, error) {
	f, err := ReflectField(v, fieldName)
	if err != nil {
		return [32]byte{}, err
	}
	b, ok := f.Interface().([32]byte)
	if !ok {
		return [32]byte{}, fmt.Errorf("abi_helper: field %q is not [32]byte (got %T)", fieldName, f.Interface())
	}
	return b, nil
}

// ReflectBool đọc field kiểu bool.
func ReflectBool(v reflect.Value, fieldName string) (bool, error) {
	f, err := ReflectField(v, fieldName)
	if err != nil {
		return false, err
	}
	if f.Kind() != reflect.Bool {
		return false, fmt.Errorf("abi_helper: field %q has kind %s, expected bool", fieldName, f.Kind())
	}
	return f.Bool(), nil
}

// ReflectSlice kiểm tra v là slice và trả về reflect.Value + length.
// Dùng để validate kết quả ABI Unpack trước khi iterate.
func ReflectSlice(v interface{}) (reflect.Value, int, error) {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return reflect.Value{}, 0, fmt.Errorf("abi_helper: expected slice, got %T", v)
	}
	return rv, rv.Len(), nil
}

// ReflectIndex trả về phần tử thứ i trong slice (deref pointer nếu cần).
func ReflectIndex(slice reflect.Value, i int) reflect.Value {
	el := slice.Index(i)
	if el.Kind() == reflect.Ptr {
		el = el.Elem()
	}
	return el
}
