// filter.go — Pure filter matching functions for eth_getLogs and eth_getFilterLogs.
// Extracted from block_processor_logs.go for independent testability.
package rpcquery

import "github.com/ethereum/go-ethereum/common"

// MatchAddress returns true if the log address matches the filter.
// An empty filter matches all addresses.
func MatchAddress(filter []common.Address, addr common.Address) bool {
	if len(filter) == 0 {
		return true
	}
	for _, a := range filter {
		if a == addr {
			return true
		}
	}
	return false
}

// MatchTopics returns true if the log topics match the filter criteria.
// Each element in filter is an OR group: the log must match at least one hash
// in each group. An empty group acts as a wildcard (matches any topic).
// An empty filter matches all topics.
func MatchTopics(filter [][]common.Hash, logTopics []common.Hash) bool {
	for i, group := range filter {
		if len(group) == 0 {
			continue // empty group = wildcard
		}
		if i >= len(logTopics) {
			return false // log has fewer topics than filter requires
		}
		matched := false
		for _, h := range group {
			if h == logTopics[i] {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
