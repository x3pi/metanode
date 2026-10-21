package mvm

import (
	"testing"

	e_common "github.com/ethereum/go-ethereum/common"
)

// The Xapian extension callbacks run inside cgo on behalf of one MVM instance. If that instance is already gone (an
// execution that shared its id finished and cleared it), the callback must fail the one call, not dereference nil and
// kill the process.
func TestXapianExtensionCallbacks_MissingInstanceIsAnErrorNotAPanic(t *testing.T) {
	gone := e_common.HexToAddress("0xFD00000000000000000000000000000000dead01")
	id := e_common.HexToAddress("0x1111111111111111111111111111111111111111")
	if GetMVMApi(gone) != nil {
		t.Fatal("test id unexpectedly owns an instance")
	}
	calls := map[string]func() error{
		"getOrCreateSimpleDb": func() error { _, err := getOrCreateSimpleDb("db", gone, id); return err },
		"deleteDb":            func() error { _, err := deleteDb("db", gone, id); return err },
		"getSimpledb":         func() error { _, err := getSimpledb("db", "k", gone, id); return err },
		"setSimpledb":         func() error { _, err := setSimpledb("db", "k", "v", gone, id); return err },
		"getNextKeys":         func() error { _, err := getNextKeys("db", "k", 10, gone, id); return err },
		"getAllSimpledb":      func() error { _, err := getAllSimpledb("db", gone, id); return err },
		"searchByValue":       func() error { _, err := searchByValue("db", "k", gone, id); return err },
	}
	for name, call := range calls {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s panicked for a missing MVM instance: %v", name, r)
				}
			}()
			if err := call(); err == nil {
				t.Errorf("%s returned no error for a missing MVM instance", name)
			}
		}()
	}
}
