package main

import (
	"fmt"
	"os"
	runtime_debug "runtime/debug"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
)

// FatalExit dumps critical execution state to crash.log and cleanly halts the process.
func FatalExit(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)

	// Safely retrieve critical metrics (could be 0 if not initialized yet)
	gei := storage.GetLastGlobalExecIndex()
	commitIdx := storage.GetLastHandledCommitIndex()
	epoch := storage.GetLastHandledCommitEpoch()

	dump := fmt.Sprintf(`
=========================================================
FATAL CRASH DUMP - %s
=========================================================
Reason: %s

[System State]
GEI: %d
Last Commit Index: %d
Epoch: %d

[Stack Trace]
%s
=========================================================
`, time.Now().Format(time.RFC3339), msg, gei, commitIdx, epoch, string(runtime_debug.Stack()))

	logger.Error("🚨 [FATAL EXIT] %s\n%s", msg, dump)

	// Write the structured dump to crash.log
	if err := os.WriteFile("crash.log", []byte(dump), 0644); err != nil {
		logger.Error("⚠️ Failed to write crash.log: %v", err)
	}

	// Always terminate with 1 as this is unrecoverable
	os.Exit(1)
}
