package tx_processor

import (
	"math/big"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/meta-node-blockchain/meta-node/pkg/blockchain"
	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/types"
	"github.com/meta-node-blockchain/meta-node/pkg/smart_contract"
	mt_common "github.com/meta-node-blockchain/meta-node/pkg/common"
)

// mergeMvmResults combines all individual parallel execution results into one final delta
func mergeMvmResults(results []types.ExecuteSCResult, coinbase common.Address) types.ExecuteSCResult {
	mapAddBalance := make(map[string][]byte)
	mapSubBalance := make(map[string][]byte)
	mapNonce := make(map[string][]byte)
	mapCodeHash := make(map[string][]byte)
	mapCodeChange := make(map[string][]byte)
	mapStorageRoot := make(map[string][]byte)
	mapStorageAddress := make(map[string]common.Address)
	mapCreatorPubkey := make(map[string][]byte)
	mapStorageAddressTouchedAddresses := make(map[common.Address][]common.Address)
	mapNativeSmartContractUpdateStorage := make(map[common.Address][][2][]byte)
	mapFullDbLogs := make(map[string][]byte)
	mapStorageChange := make(map[string]map[string][]byte)
	mapPublicKeyBls := make(map[string][]byte)
	mapAccountType := make(map[string]uint8)
	mapNewDeviceKey := make(map[string][]byte)

	for _, res := range results {
		mergeMapBytesAdd(mapAddBalance, res.MapAddBalance())
		mergeMapBytesAdd(mapSubBalance, res.MapSubBalance())
		
		for k, v := range res.MapNonce() { mapNonce[k] = v }
		for k, v := range res.MapCodeHash() { mapCodeHash[k] = v }
		for k, v := range res.MapCodeChange() { mapCodeChange[k] = v }
		for k, v := range res.MapStorageRoot() { mapStorageRoot[k] = v }
		for k, v := range res.MapStorageAddress() { mapStorageAddress[k] = v }
		for k, v := range res.MapCreatorPubkey() { mapCreatorPubkey[k] = v }
		for k, v := range res.MapPublicKeyBls() { mapPublicKeyBls[k] = v }
		for k, v := range res.MapAccountType() { mapAccountType[k] = v }
		for k, v := range res.MapNewDeviceKey() { mapNewDeviceKey[k] = v }
		for k, v := range res.MapFullDbLogs() { mapFullDbLogs[k] = v }
		
		for k, v := range res.MapStorageAddressTouchedAddresses() {
			mapStorageAddressTouchedAddresses[k] = append(mapStorageAddressTouchedAddresses[k], v...)
		}
		
		for k, v := range res.MapNativeSmartContractUpdateStorage() {
			mapNativeSmartContractUpdateStorage[k] = append(mapNativeSmartContractUpdateStorage[k], v...)
		}

		for k, v := range res.MapStorageChange() {
			if mapStorageChange[k] == nil {
				mapStorageChange[k] = make(map[string][]byte)
			}
			for k2, v2 := range v {
				mapStorageChange[k][k2] = v2
			}
		}
	}

	merged := smart_contract.NewExecuteSCResult(
		common.Hash{}, pb.RECEIPT_STATUS_RETURNED, pb.EXCEPTION_NONE, nil, 0, common.Hash{},
		mapAddBalance, mapSubBalance, mapNonce, mapCodeHash, mapStorageRoot,
		mapStorageAddress, mapCreatorPubkey, mapStorageAddressTouchedAddresses, mapNativeSmartContractUpdateStorage, nil,
	)
	
	merged.SetMapStorageChange(mapStorageChange)
	merged.SetMapCodeChange(mapCodeChange)
	merged.SetMapPublicKeyBls(mapPublicKeyBls)
	merged.SetMapAccountType(mapAccountType)
	merged.SetMapNewDeviceKey(mapNewDeviceKey)
	merged.SetMapFullDbLogs(mapFullDbLogs)

	return merged
}

func mergeMapBytesAdd(dest map[string][]byte, src map[string][]byte) {
	for k, v := range src {
		if existing, ok := dest[k]; ok {
			total := new(big.Int).SetBytes(existing)
			total.Add(total, new(big.Int).SetBytes(v))
			dest[k] = total.Bytes()
		} else {
			dest[k] = v
		}
	}
}

// applyMergedExecuteResult applies a merged ExecuteSCResult directly to the global database.
func applyMergedExecuteResult(
	chainState *blockchain.ChainState,
	exRs types.ExecuteSCResult,
) error {
	accDB := chainState.GetAccountStateDB()
	scDB := chainState.GetSmartContractDB()

	// 🔒 [DOUBLE-BALANCE-FIX]
	// We DO NOT apply MapAddBalance and MapSubBalance here!
	// In Block-STM, balances are applied inline to `localAccountDB` in `tx_executor.go` 
	// to ensure sequential state visibility. Those changes become `DirtyAccounts` 
	// and are persisted to the global DB via `validationCache.FlushToGlobal()`.
	// Applying them again here would cause a Double-Balance Bug!

	// --- Nonce ---
	if len(exRs.MapNonce()) > 0 {
		sortedNonceAddrs := make([]string, 0, len(exRs.MapNonce()))
		for addr := range exRs.MapNonce() {
			sortedNonceAddrs = append(sortedNonceAddrs, addr)
		}
		sort.Strings(sortedNonceAddrs)
		for _, address := range sortedNonceAddrs {
			nonceBytes := exRs.MapNonce()[address]
			fmtAddress := common.HexToAddress(address)
			newNonce := big.NewInt(0).SetBytes(nonceBytes).Uint64()

			// Apply directly
			accDB.SetNonce(fmtAddress, newNonce)
		}
	}

	// --- Code Hash ---
	if len(exRs.MapCodeHash()) > 0 {
		sortedAddrs := make([]string, 0, len(exRs.MapCodeHash()))
		for addr := range exRs.MapCodeHash() {
			sortedAddrs = append(sortedAddrs, addr)
		}
		sort.Strings(sortedAddrs)
		
		for _, address := range sortedAddrs {
			codeHashBytes := exRs.MapCodeHash()[address]
			fmtAddress := common.HexToAddress(address)
			
			// Try to get creator and storage from maps if available
			var creatorKey []byte
			var storageAddr common.Address
			if mapCreator := exRs.MapCreatorPubkey(); mapCreator != nil {
				creatorKey = mapCreator[address]
			}
			if mapStorage := exRs.MapStorageAddress(); mapStorage != nil {
				storageAddr = mapStorage[address]
			}
			
			asState, _ := accDB.AccountState(fmtAddress)
			if asState != nil {
				asState.SetCreatorPublicKey(mt_common.PubkeyFromBytes(creatorKey))
				asState.SetStorageAddress(storageAddr)
				asState.SetCodeHash(common.BytesToHash(codeHashBytes))
				accDB.SetState(asState)
			}
			
			if mapCodeChange := exRs.MapCodeChange(); mapCodeChange != nil {
				if code, ok := mapCodeChange[address]; ok {
					scDB.SetCode(fmtAddress, common.BytesToHash(codeHashBytes), code)
				}
			}
		}
	}

	// --- Storage Root ---
	if len(exRs.MapStorageRoot()) > 0 {
		sortedAddrs := make([]string, 0, len(exRs.MapStorageRoot()))
		for addr := range exRs.MapStorageRoot() {
			sortedAddrs = append(sortedAddrs, addr)
		}
		sort.Strings(sortedAddrs)
		
		for _, address := range sortedAddrs {
			storageRoot := exRs.MapStorageRoot()[address]
			fmtAddress := common.HexToAddress(address)
			
			asState, _ := accDB.AccountState(fmtAddress)
			if asState != nil {
				asState.SetStorageRoot(common.BytesToHash(storageRoot))
				accDB.SetState(asState)
			}
		}
	}

	// --- Storage Change (Key-Value pairs) ---
	if len(exRs.MapStorageChange()) > 0 {
		sortedAddrs := make([]string, 0, len(exRs.MapStorageChange()))
		for addr := range exRs.MapStorageChange() {
			sortedAddrs = append(sortedAddrs, addr)
		}
		sort.Strings(sortedAddrs)
		for _, address := range sortedAddrs {
			fmtAddress := common.HexToAddress(address)
			changes := exRs.MapStorageChange()[address]
			
			for keyHex, valueBytes := range changes {
				key := common.HexToHash(keyHex)
				scDB.SetStorageValue(fmtAddress, key.Bytes(), valueBytes)
			}
		}
	}

	// --- BLS Keys ---
	if len(exRs.MapPublicKeyBls()) > 0 {
		for addrHex, blsKey := range exRs.MapPublicKeyBls() {
			asState, _ := accDB.AccountState(common.HexToAddress(addrHex))
			if asState != nil {
				asState.SetPublicKeyBls(blsKey)
				accDB.SetState(asState)
			}
		}
	}
	
	// --- Account Types ---
	if len(exRs.MapAccountType()) > 0 {
		for addrHex, accType := range exRs.MapAccountType() {
			asState, _ := accDB.AccountState(common.HexToAddress(addrHex))
			if asState != nil {
				asState.SetAccountType(pb.ACCOUNT_TYPE(accType))
				accDB.SetState(asState)
			}
		}
	}

	// --- New Device Keys ---
	if len(exRs.MapNewDeviceKey()) > 0 {
		for addrHex, key := range exRs.MapNewDeviceKey() {
			asState, _ := accDB.AccountState(common.HexToAddress(addrHex))
			if asState != nil {
				asState.SetNewDeviceKey(common.BytesToHash(key))
				accDB.SetState(asState)
			}
		}
	}

	return nil
}
