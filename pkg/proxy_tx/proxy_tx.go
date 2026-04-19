package proxy_tx

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/types"
)

// ProxyCallData overrides the Input() method of a base CallData.
type ProxyCallData struct {
	types.CallData
	input []byte
}

func (c *ProxyCallData) Input() []byte {
	return c.input
}

// ProxyTransaction wraps a base Transaction and selectively overrides fields.
// Useful for cross-chain/system-level VM execution where from/to/amount/gas
// must differ from the original on-chain transaction.
//
// Fields are lowercase (unexported) to avoid conflict with interface method names.
// Use New() constructor to build from outside this package.
type ProxyTransaction struct {
	types.Transaction
	from      common.Address
	to        common.Address
	amount    *big.Int
	maxGas    uint64
	gasPrice  uint64 // 0 = free gas
	inputData []byte
}

// New creates a ProxyTransaction overriding the specified fields of base tx.
// Set gasPrice = 0 to grant free gas for this execution.
func New(
	base types.Transaction,
	from common.Address,
	to common.Address,
	amount *big.Int,
	maxGas uint64,
	gasPrice uint64,
	inputData []byte,
) *ProxyTransaction {
	return &ProxyTransaction{
		Transaction: base,
		from:        from,
		to:          to,
		amount:      amount,
		maxGas:      maxGas,
		gasPrice:    gasPrice,
		inputData:   inputData,
	}
}

func (f *ProxyTransaction) FromAddress() common.Address { return f.from }
func (f *ProxyTransaction) ToAddress() common.Address   { return f.to }
func (f *ProxyTransaction) Amount() *big.Int            { return f.amount }
func (f *ProxyTransaction) MaxGas() uint64              { return f.maxGas }
func (f *ProxyTransaction) MaxGasPrice() uint64         { return f.gasPrice }
func (f *ProxyTransaction) CallData() types.CallData    { return &ProxyCallData{input: f.inputData} }
func (f *ProxyTransaction) IsCallContract() bool        { return len(f.inputData) > 0 }
func (f *ProxyTransaction) IsDeployContract() bool      { return false }
func (f *ProxyTransaction) IsRegularTransaction() bool  { return false }
