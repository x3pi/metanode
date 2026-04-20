// File path: github.com/meta-node-blockchain/meta-node/types/errors.go
package types

import "errors"

var (
	// ErrInsufficientBalance cho biết số dư không đủ để thực hiện giao dịch.
	ErrInsufficientBalance = errors.New("insufficient balance for transaction")

	// ErrInvalidCommissionRate cho biết tỷ lệ hoa hồng không hợp lệ.
	ErrInvalidCommissionRate = errors.New("invalid commission rate")

	// ErrValidatorNotFound cho biết validator không tồn tại.
	ErrValidatorNotFound = errors.New("validator not found")
)
