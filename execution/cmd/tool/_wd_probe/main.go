// Scratch probe (not committed): exercises the REAL pkg/utils watchdog under a supervisor.
//
//	MODE=stuck   : starts StartWatchdogFatal and never stops it (simulates a hung block-execution phase)
//	MODE=healthy : starts it, stops it after 500 ms, then idles (watchdog must NOT fire)
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/utils"
)

func main() {
	fmt.Printf("probe start pid=%d mode=%s abort_secs=%q\n", os.Getpid(), os.Getenv("MODE"), os.Getenv("METANODE_EXEC_STUCK_ABORT_SECS"))
	stop := utils.StartWatchdogFatal("PROBE", 500*time.Millisecond, func() string { return "probe-context" })
	if os.Getenv("MODE") == "healthy" {
		time.Sleep(500 * time.Millisecond)
		stop()
		fmt.Println("probe: phase finished, watchdog stopped; idling")
	}
	select {}
}
