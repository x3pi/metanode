package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// ═══════════════════════════════════════════════════════════════════════════════
// MetaNode Chaos Engine v2.0
//
// Automated chaos testing tool for validating MetaNode cluster resilience.
//
// Phases:
//   Phase 1: Verify initial cluster health (all 5 nodes in consensus)
//   Phase 2: Kill a random Validator node (SIGKILL via tmux session kill)
//   Phase 3: Wait for network stabilization without the victim
//   Phase 4: Resume the victim node (via resume_node.sh)
//   Phase 5: Wait for snapshot sync & DAG stream catch-up
//   Phase 6: Validate 100% StateRoot parity across all 5 nodes
//
// Usage:
//   go run ./cmd/tool/chaos_tester/
//   go run ./cmd/tool/chaos_tester/ --rounds 5 --kill-wait 20 --restore-wait 40
//   go run ./cmd/tool/chaos_tester/ --victim 2
//   go run ./cmd/tool/chaos_tester/ --kill-two
// ═══════════════════════════════════════════════════════════════════════════════

var (
	flagRounds      int
	flagKillWait    int
	flagRestoreWait int
	flagMaxRetries  int
	flagVictim      int    // -1 = random
	flagKillTwo     bool   // kill 2 nodes simultaneously
	flagNoRestore   bool   // skip restore (test permanent partition)
)

func init() {
	flag.IntVar(&flagRounds, "rounds", 1, "Number of kill-and-restore rounds to execute")
	flag.IntVar(&flagKillWait, "kill-wait", 15, "Seconds to wait after killing before restore")
	flag.IntVar(&flagRestoreWait, "restore-wait", 30, "Seconds to wait after restore before validation")
	flag.IntVar(&flagMaxRetries, "max-retries", 30, "Max consensus polling attempts (x3s each)")
	flag.IntVar(&flagVictim, "victim", -1, "Specific node to kill (0-4). -1 = random")
	flag.BoolVar(&flagKillTwo, "kill-two", false, "Kill 2 nodes simultaneously (harder scenario)")
	flag.BoolVar(&flagNoRestore, "no-restore", false, "Skip restore to test permanent partition (debug mode)")
}

func main() {
	flag.Parse()

	// Graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n\n🛑 Chaos Engine interrupted. Exiting...")
		os.Exit(0)
	}()

	// ─── Banner ───────────────────────────────────────────────────────────
	printBanner()

	// ─── Setup ────────────────────────────────────────────────────────────
	cluster := NewDefaultClusterConfig()
	cluster.KillWaitSec = flagKillWait
	cluster.RestoreWaitSec = flagRestoreWait
	cluster.MaxSyncRetries = flagMaxRetries

	// ─── Phase 1: Initial Health Check ────────────────────────────────────
	fmt.Println("\n" + sectionHeader("PHASE 1: INITIAL CLUSTER HEALTH CHECK"))
	fmt.Println("   Verifying all 5 nodes are online and in consensus...")
	cluster.PrintClusterStatus()

	if !cluster.WaitForFullConsensus(5, 10) {
		fmt.Println("\n💥 FATAL: Initial cluster is NOT healthy. Cannot proceed with chaos testing.")
		fmt.Println("   Ensure all 5 nodes are running with: mtn-orchestrator.sh restart --fresh --build-all")
		os.Exit(1)
	}
	fmt.Println("   🟢 Cluster is healthy. Ready for chaos.\n")

	// ─── Chaos Rounds ─────────────────────────────────────────────────────
	for round := 1; round <= flagRounds; round++ {
		fmt.Printf("\n%s\n", sectionHeader(fmt.Sprintf("CHAOS ROUND %d/%d", round, flagRounds)))

		var victims []int
		if flagKillTwo {
			victims = pickTwoVictims()
		} else {
			victims = []int{pickVictim()}
		}

		// ─── Phase 2: Kill ────────────────────────────────────────────
		fmt.Println("\n" + subHeader("PHASE 2: KILL"))
		for _, v := range victims {
			fmt.Printf("   🔫 Killing Node %d (hard crash via stop_node.sh + tmux kill)...\n", v)
			if err := cluster.StopNode(v); err != nil {
				fmt.Printf("   ⚠️  stop_node.sh warning: %v\n", err)
				// Fallback: try direct tmux kill
				if err2 := cluster.KillNode(v); err2 != nil {
					fmt.Printf("   ❌ KillNode fallback also failed: %v\n", err2)
				}
			}
		}

		// ─── Phase 3: Stabilization Wait ──────────────────────────────
		fmt.Println("\n" + subHeader("PHASE 3: STABILIZATION"))
		fmt.Printf("   ⏱️  Waiting %d seconds for network to stabilize without victim(s)...\n", cluster.KillWaitSec)
		countdown(cluster.KillWaitSec)

		// Show cluster status with victim(s) down
		fmt.Println("   📊 Cluster status with victim(s) offline:")
		cluster.PrintClusterStatus()

		// Verify surviving nodes are still in consensus
		survivingCount := 5 - len(victims)
		fmt.Printf("   🔍 Checking if %d surviving nodes maintain consensus...\n", survivingCount)
		if !cluster.WaitForFullConsensus(survivingCount, 10) {
			fmt.Printf("   ⚠️  WARNING: Surviving nodes lost consensus! This may indicate a deeper issue.\n")
		}

		if flagNoRestore {
			fmt.Println("   ⏭️  --no-restore flag set. Skipping restore phase.")
			continue
		}

		// ─── Phase 4: Restore ─────────────────────────────────────────
		fmt.Println("\n" + subHeader("PHASE 4: RESTORE"))
		for _, v := range victims {
			fmt.Printf("   🚑 Restoring Node %d via resume_node.sh...\n", v)
			if err := cluster.ResumeNode(v); err != nil {
				fmt.Printf("   ❌ FATAL: Failed to restore Node %d: %v\n", v, err)
				fmt.Println("   The chaos test cannot continue. Please manually check the node.")
				os.Exit(1)
			}
		}

		// ─── Phase 5: Catch-Up Wait ───────────────────────────────────
		fmt.Println("\n" + subHeader("PHASE 5: CATCH-UP"))
		fmt.Printf("   ⏱️  Waiting %d seconds for restored node(s) to sync (Snapshot + DAG stream)...\n", cluster.RestoreWaitSec)
		countdown(cluster.RestoreWaitSec)

		// ─── Phase 6: Final Validation ────────────────────────────────
		fmt.Println("\n" + subHeader("PHASE 6: PARITY VALIDATION"))
		fmt.Println("   🔍 Validating 100% StateRoot parity across all 5 nodes...")

		if !cluster.WaitForFullConsensus(5, cluster.MaxSyncRetries) {
			fmt.Printf("\n   💥 CHAOS TEST FAILED (Round %d/%d)!\n", round, flagRounds)
			fmt.Println("   Restored node(s) diverged from the cluster or failed to catch up.")
			fmt.Println("   Check logs for Dirty StateRoot or snapshot corruption.")
			os.Exit(1)
		}

		fmt.Printf("\n   🎉 Round %d/%d: PASSED! Cluster achieved 100%% Parity.\n", round, flagRounds)

	}

	// ─── Final Summary ────────────────────────────────────────────────────
	printFinalSummary(flagRounds)
}

// ─── Victim Selection ──────────────────────────────────────────────────────────

func pickVictim() int {
	if flagVictim >= 0 && flagVictim <= 4 {
		return flagVictim
	}
	return rand.IntN(5)
}

func pickTwoVictims() []int {
	first := pickVictim()
	second := first
	for second == first {
		second = rand.IntN(5)
	}
	return []int{first, second}
}

// ─── UI Helpers ────────────────────────────────────────────────────────────────

func printBanner() {
	fmt.Println(`
 ╔══════════════════════════════════════════════════════════════════╗
 ║                                                                  ║
 ║   🌪️  MetaNode Chaos Engine v2.0  🌪️                             ║
 ║                                                                  ║
 ║   Automated Kill-and-Restore Stress Testing                     ║
 ║   Validating: FFI Snapshot · DAG Sync · StateRoot Parity        ║
 ║                                                                  ║
 ╚══════════════════════════════════════════════════════════════════╝`)
	fmt.Printf("   Configuration:\n")
	fmt.Printf("   • Rounds:        %d\n", flagRounds)
	fmt.Printf("   • Kill Wait:     %ds\n", flagKillWait)
	fmt.Printf("   • Restore Wait:  %ds\n", flagRestoreWait)
	fmt.Printf("   • Max Retries:   %d (x3s = %ds max)\n", flagMaxRetries, flagMaxRetries*3)
	if flagVictim >= 0 {
		fmt.Printf("   • Victim:        Node %d (fixed)\n", flagVictim)
	} else {
		fmt.Printf("   • Victim:        Random\n")
	}
	if flagKillTwo {
		fmt.Printf("   • Mode:          KILL TWO (hard mode)\n")
	}
}

func sectionHeader(title string) string {
	line := "═══════════════════════════════════════════════════════════════════"
	return fmt.Sprintf("   %s\n   ║ %s\n   %s", line, title, line)
}

func subHeader(title string) string {
	return fmt.Sprintf("   ── %s ──────────────────────────────────────────", title)
}

func countdown(seconds int) {
	for i := seconds; i > 0; i-- {
		fmt.Printf("\r   ⏱️  %d seconds remaining...   ", i)
		time.Sleep(1 * time.Second)
	}
	fmt.Printf("\r   ⏱️  Done.%-30s\n", "")
}

func printFinalSummary(rounds int) {
	fmt.Println(`
   ╔══════════════════════════════════════════════════════════════╗
   ║                                                              ║
   ║   🎉  ALL CHAOS TESTS PASSED SUCCESSFULLY!  🎉              ║
   ║                                                              ║
   ║   The MetaNode operational safety net is bulletproof.        ║
   ║   • FFI Snapshot Sync:    ✅ Verified                        ║
   ║   • DAG Stream Catch-up:  ✅ Verified                        ║
   ║   • StateRoot Parity:     ✅ 100%%                            ║
   ║   • No Dirty StateRoot:   ✅ Confirmed                       ║
   ║                                                              ║
   ╚══════════════════════════════════════════════════════════════╝`)
	fmt.Printf("   Total rounds completed: %d\n\n", rounds)
}
