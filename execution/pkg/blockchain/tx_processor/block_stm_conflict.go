package tx_processor

import (
"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/grouptxns"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/utils"
)

type ConflictDetector struct {
writeToIndices map[string][]int
readToIndices  map[string][]int
groupIdxMap    map[int]int
conflictFreeAddr common.Address
}

func NewConflictDetector(groupsToExecute []int) *ConflictDetector {
groupIdxMap := make(map[int]int, len(groupsToExecute))
for i, realIdx := range groupsToExecute {
groupIdxMap[realIdx] = i
}
return &ConflictDetector{
writeToIndices: make(map[string][]int),
readToIndices:  make(map[string][]int),
groupIdxMap:    groupIdxMap,
conflictFreeAddr: utils.GetAddressSelector(mt_common.ACCOUNT_SETTING_ADDRESS_SELECT),
}
}

func (cd *ConflictDetector) RegisterAccess(addr common.Address, isWrite bool, realIdx int) {
if addr == cd.conflictFreeAddr {
return
}
key := addr.String()
idx := cd.groupIdxMap[realIdx]
if isWrite {
cd.writeToIndices[key] = append(cd.writeToIndices[key], idx)
} else {
cd.readToIndices[key] = append(cd.readToIndices[key], idx)
}
}

func (cd *ConflictDetector) RegisterStorageAccess(addr common.Address, kStr string, isWrite bool, realIdx int) {
if addr == cd.conflictFreeAddr {
return
}
key := addr.String() + kStr
idx := cd.groupIdxMap[realIdx]
if isWrite {
cd.writeToIndices[key] = append(cd.writeToIndices[key], idx)
} else {
cd.readToIndices[key] = append(cd.readToIndices[key], idx)
}
}

func (cd *ConflictDetector) BuildExecutionGroups(groupsToExecute []int, uf *grouptxns.UnionFind) [][]int {
// Union groups accessing same addresses (Write-Write and Read-Write ONLY)
for key, wIndices := range cd.writeToIndices {
rIndices := cd.readToIndices[key]

// All writers of this key conflict with each other
for i := 1; i < len(wIndices); i++ {
uf.Union(wIndices[0], wIndices[i])
}
// All readers of this key conflict with the writers
if len(wIndices) > 0 {
for _, rIdx := range rIndices {
uf.Union(wIndices[0], rIdx)
}
}
}

// Collect group indices into execution groups
rootToItems := make(map[int][]int)
for i, realIdx := range groupsToExecute {
root := uf.Find(i)
rootToItems[root] = append(rootToItems[root], realIdx)
}

var executionGroups [][]int
for _, groupRealIndices := range rootToItems {
executionGroups = append(executionGroups, groupRealIndices)
}

return executionGroups
}
