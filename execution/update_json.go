package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
)

func main() {
	keysToRemove := []string{
		"AccountState", "Trie", "SmartContractCode", "SmartContractStorage",
		"Blocks", "Receipts", "TxsEth", "BlocksHash", "BackupDeviceKey",
		"TransactionBlockNumber", "TransactionState", "BlockHashToNumber",
		"Wallets", "Mapping", "Backup", "Stake", "XapianPath", "NodeType",
	}

	dirs := []string{
		"/home/abc/chain-n/metanode/execution/cmd/simple_chain",
		"/home/abc/chain-n/metanode/execution/cmd/exec_node",
	}

	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "config*.json"))
		if err != nil {
			fmt.Printf("Error globbing dir %s: %v\n", dir, err)
			continue
		}

		for _, file := range files {
			data, err := ioutil.ReadFile(file)
			if err != nil {
				fmt.Printf("Error reading %s: %v\n", file, err)
				continue
			}

			var config map[string]interface{}
			if err := json.Unmarshal(data, &config); err != nil {
				fmt.Printf("Error parsing %s: %v\n", file, err)
				continue
			}

			changed := false
			if databases, ok := config["Databases"].(map[string]interface{}); ok {
				for _, key := range keysToRemove {
					if _, exists := databases[key]; exists {
						delete(databases, key)
						changed = true
					}
				}
			}

			if changed {
				out, err := json.MarshalIndent(config, "", "    ")
				if err != nil {
					fmt.Printf("Error marshaling %s: %v\n", file, err)
					continue
				}
				if err := ioutil.WriteFile(file, out, 0644); err != nil {
					fmt.Printf("Error writing %s: %v\n", file, err)
				} else {
					fmt.Printf("Updated %s\n", file)
				}
			}
		}
	}
}
