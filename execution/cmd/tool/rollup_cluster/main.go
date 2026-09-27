// rollup-cluster — operator tool for a `consensus_mode="raft"` cluster (plan C4).
//
// Every command that changes something supports --dry-run (prints the steps, changes nothing) and REFUSES to run
// when a safety condition does not hold (target not caught up, quorum would be lost, state not this chain's...).
//
//	rollup-cluster -config cluster.json check
//	rollup-cluster -config cluster.json transfer-leader --to n1 [--dry-run]
//	rollup-cluster -config cluster.json add-replica --id n3 --raft-addr host:7103 --admin-addr host:7203 [--dry-run]
//	rollup-cluster -config cluster.json remove-replica --id n2 [--transfer-first] [--dry-run]
//	rollup-cluster prepare-replica --from-data /path/of/stopped/peer/data --to-data /path/new/data --from-admin host:7201 [--dry-run]
//
// cluster.json: {"secret_file": "path/to/raft_forward.key", "members": [{"id":"n0","raft_addr":"h:7100","admin_addr":"h:7200"}, ...]}
//
// Adding a replica (the state is NOT streamed over the network; Raft snapshots only carry counters):
//  1. stop one healthy peer, `prepare-replica` copies its data directory (skipping the peer's Raft directory), restart the peer;
//  2. start the new replica with raft.join_existing_chain=true (empty Raft state, copied DB), same sequencer key and secret;
//  3. `add-replica` adds it as a NON-voter, waits until it caught up, checks its tip hash equals the leader's, then promotes it.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
)

type clusterFile struct {
	SecretFile string            `json:"secret_file"`
	Members    []raftfeed.Member `json:"members"`
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	global := flag.NewFlagSet("rollup-cluster", flag.ContinueOnError)
	cfgPath := global.String("config", "cluster.json", "cluster description (secret file + members)")
	if err := global.Parse(args); err != nil {
		return 2
	}
	rest := global.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "commands: check | transfer-leader | add-replica | remove-replica | prepare-replica")
		return 2
	}
	cmd, cargs := rest[0], rest[1:]
	if cmd == "prepare-replica" {
		return prepareReplica(cargs)
	}

	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		return fail(err)
	}
	var cf clusterFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		return fail(fmt.Errorf("%s: %w", *cfgPath, err))
	}
	secret, err := os.ReadFile(cf.SecretFile)
	if err != nil {
		return fail(fmt.Errorf("secret_file: %w", err))
	}
	cl := &raftfeed.AdminClient{Secret: secret}

	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "print the steps, change nothing")
	switch cmd {
	case "check":
		if err := fs.Parse(cargs); err != nil {
			return 2
		}
		rep := cl.Check(cf.Members)
		out, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(out))
		if len(rep.Problems) > 0 {
			return 3
		}
		return 0
	case "transfer-leader":
		to := fs.String("to", "", "member id that becomes the leader")
		if err := fs.Parse(cargs); err != nil {
			return 2
		}
		return report(cl.TransferLeader(cf.Members, *to, *dry))
	case "add-replica":
		id := fs.String("id", "", "new member id")
		raftAddr := fs.String("raft-addr", "", "new member's Raft address host:port")
		adminAddr := fs.String("admin-addr", "", "new member's internal endpoint host:port")
		wait := fs.Duration("catch-up-wait", 120*time.Second, "how long to wait for the new replica to catch up")
		if err := fs.Parse(cargs); err != nil {
			return 2
		}
		if *id == "" || *raftAddr == "" || *adminAddr == "" {
			return fail(errors.New("--id, --raft-addr and --admin-addr are required"))
		}
		cl.CatchUpWait = *wait
		return report(cl.AddReplica(cf.Members, raftfeed.Member{ID: *id, RaftAddr: *raftAddr, AdminAddr: *adminAddr}, *dry))
	case "remove-replica":
		id := fs.String("id", "", "member id to remove")
		tf := fs.Bool("transfer-first", false, "if it is the leader, move leadership away first")
		if err := fs.Parse(cargs); err != nil {
			return 2
		}
		return report(cl.RemoveReplica(cf.Members, *id, *tf, *dry))
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
	return 2
}

func report(steps []string, err error) int {
	if len(steps) > 0 {
		fmt.Print("steps:\n", raftfeed.FormatSteps(steps))
	}
	if err != nil {
		return fail(err)
	}
	fmt.Println("ok")
	return 0
}

func fail(err error) int {
	fmt.Fprintln(os.Stderr, "ERROR:", err)
	return 1
}

// prepareReplica copies the state directory of a STOPPED peer for a new replica. It refuses when the source is
// still answering on its internal endpoint (copying a running NOMT/Pebble store yields a corrupt copy), or when the
// destination is not empty. The peer's Raft directory is never copied: the new replica has its own identity.
func prepareReplica(args []string) int {
	fs := flag.NewFlagSet("prepare-replica", flag.ContinueOnError)
	from := fs.String("from-data", "", "databases.root_path of the STOPPED source peer")
	to := fs.String("to-data", "", "databases.root_path of the new replica (must not exist or be empty)")
	fromAdmin := fs.String("from-admin", "", "the source peer's internal endpoint host:port; must NOT answer")
	secretFile := fs.String("secret-file", "", "shared HMAC secret (to probe --from-admin)")
	dry := fs.Bool("dry-run", false, "print what would be copied")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *from == "" || *to == "" || *fromAdmin == "" || *secretFile == "" {
		return fail(errors.New("--from-data, --to-data, --from-admin and --secret-file are required"))
	}
	secret, err := os.ReadFile(*secretFile)
	if err != nil {
		return fail(err)
	}
	if _, err := (&raftfeed.AdminClient{Secret: secret, HTTP: raftfeed.ShortHTTPClient()}).Status(*fromAdmin); err == nil {
		return fail(fmt.Errorf("refused: the source peer still answers at %s — stop it first (copying a running store corrupts the copy)", *fromAdmin))
	}
	if st, err := os.Stat(*from); err != nil || !st.IsDir() {
		return fail(fmt.Errorf("refused: source %s is not a directory", *from))
	}
	if entries, err := os.ReadDir(*to); err == nil && len(entries) > 0 {
		return fail(fmt.Errorf("refused: destination %s is not empty", *to))
	}
	fmt.Printf("steps:\n  1. copy %s -> %s (cp -a --reflink=auto)\n  2. start the new replica with raft.join_existing_chain=true and its own raft.data_dir\n  3. rollup-cluster add-replica ...\n", *from, *to)
	if *dry {
		return 0
	}
	if err := os.MkdirAll(*to, 0o755); err != nil {
		return fail(err)
	}
	out, err := exec.Command("cp", "-a", "--reflink=auto", strings.TrimRight(*from, "/")+"/.", strings.TrimRight(*to, "/")+"/").CombinedOutput()
	if err != nil {
		return fail(fmt.Errorf("copy failed: %v: %s", err, out))
	}
	fmt.Println("ok: state copied; now start the new replica with join_existing_chain=true")
	return 0
}
