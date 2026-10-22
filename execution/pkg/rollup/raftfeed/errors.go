package raftfeed

import "errors"

var (
	// ErrTxTooLarge is returned when a transaction individually exceeds max_batch_bytes.
	ErrTxTooLarge = errors.New("batch contains transaction exceeding max_batch_bytes")

	// ErrEmptyBatch is returned when a batch contains no transactions.
	ErrEmptyBatch = errors.New("empty batch")

	// errTxTooLarge is an internal alias for backward compatibility within the package.
	errTxTooLarge = ErrTxTooLarge
)
