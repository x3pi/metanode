package main

import (
	"encoding/hex"
	"fmt"
	"golang.org/x/crypto/sha3"
)

func keccak256(signature string) string {
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte(signature))
	return hex.EncodeToString(hash.Sum(nil)[:4])
}

func main() {
	sigs := []string{
		"runStep1_Setup()",
		"runStep1_Reset()",
		"runStep2_ReadBack()",
		"runStep3_UpdateDoc()",
		"runStep4_IndexMore()",
		"runStep5a_Search(string)",
		"runStep5b_QuerySearch()",
		"runStep6_SearchRange()",
		"runStep7_DeleteAndCommit()",
		"runAllInOne()",
		"increment()",
		"decrement()",
		"setValue(uint256)",
		"increaseValue(uint256)",
		"getValue()",
		"initDb()",
		"insertDoc(uint256,string)",
		"readDoc(uint256)",
		"getDocId(uint256)",
	}
	for _, sig := range sigs {
		fmt.Printf("%s -> 0x%s\n", sig, keccak256(sig))
	}
}
