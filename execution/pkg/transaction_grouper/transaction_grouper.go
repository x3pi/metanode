package transaction_grouper

import (
	"github.com/ethereum/go-ethereum/crypto"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/types"
)

// trannsactionGrouper used to group transactions by prefix of from and to address
type TransactionGrouper struct {
	groups [16][]types.Transaction
	prefix []byte
}

func NewTransactionGrouper(
	prefix []byte,
) *TransactionGrouper {
	return &TransactionGrouper{
		groups: [16][]types.Transaction{},
		prefix: prefix,
	}
}

func (t *TransactionGrouper) AddFromTransactions(transactions []types.Transaction) {
	for _, v := range transactions {
		t.AddFromTransaction(v)
	}
}

func (t *TransactionGrouper) AddFromTransaction(transaction types.Transaction) {
	address := transaction.FromAddress()
	nibbles := p_common.KeybytesToHex(crypto.Keccak256(address.Bytes()))
	// remove prefix
	nibbles = nibbles[len(t.prefix):]
	// add to group
	t.groups[nibbles[0]] = append(t.groups[nibbles[0]], transaction)
}

func (t *TransactionGrouper) AddToTransactions(transactions []types.Transaction) {
	for _, v := range transactions {
		t.AddToTransaction(v)
	}
}

func (t *TransactionGrouper) AddToTransaction(transaction types.Transaction) {
	address := transaction.ToAddress()
	nibbles := p_common.KeybytesToHex(crypto.Keccak256(address.Bytes()))
	// remove prefix
	nibbles = nibbles[len(t.prefix):]
	// add to group
	t.groups[nibbles[0]] = append(t.groups[nibbles[0]], transaction)
}

func (t *TransactionGrouper) GetTransactionsGroups() [16][]types.Transaction {
	return t.groups
}

func (t *TransactionGrouper) HaveTransactionGroupsCount() int {
	count := 0
	for _, v := range t.groups {
		if len(v) > 0 {
			count++
		}
	}
	return count
}

func (t *TransactionGrouper) Clear() {
	t.groups = [16][]types.Transaction{}
}
