// protocol_fingerprint proves that the validators of a chain run by the same rules.
//
// Every rule that decides how a block executes is read from each node's own config.json. This tool
//
//	-nodes FILE   asks every node for its fingerprint (RPC mtn_protocolFingerprint) and names the settings that differ;
//	              exit 0 = all agree, 1 = settings differ, 2 = some node could not be asked.
//	-config FILE  computes the same report offline from a config.json (add -genesis to get the same fingerprint as the
//	              running node, which also folds in chain ID, attestation interval and epoch length from genesis);
//	              with a genesis that declares a protocol section it runs the startup check and exits 1 on a difference.
//	-snippet      prints the genesis "protocol" section for that config, to paste into genesis.json before its digest is
//	              published (do not edit the genesis of a chain whose digest is already published).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/protocolparams"
)

type nodeResult struct {
	Report protocolparams.Report
	Err    error
}

func main() {
	cfgPath := flag.String("config", "", "offline: node config.json")
	genesisPath := flag.String("genesis", "", "offline: genesis.json (optional)")
	snippet := flag.Bool("snippet", false, "offline: print the genesis protocol section for -config")
	nodesPath := flag.String("nodes", "", `online: JSON {"name":"http://rpc"} or {"nodes":{"name":"http://rpc"}}`)
	timeout := flag.Duration("timeout", 5*time.Second, "online: per-node RPC timeout")
	flag.Parse()

	switch {
	case *nodesPath != "":
		nodes, err := loadNodes(*nodesPath)
		exitOn(err)
		text, code := compare(queryAll(nodes, *timeout))
		fmt.Print(text)
		os.Exit(code)
	case *cfgPath != "":
		os.Exit(offline(*cfgPath, *genesisPath, *snippet))
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "protocol_fingerprint:", err)
		os.Exit(2)
	}
}

func offline(cfgPath, genesisPath string, snippet bool) int {
	raw, err := os.ReadFile(cfgPath)
	exitOn(err)
	var cfg config.SimpleChainConfig
	exitOn(json.Unmarshal(raw, &cfg))
	var g *config.GenesisData
	if genesisPath != "" {
		g, err = config.LoadGenesisData(genesisPath)
		exitOn(err)
		cfg.ChainId = g.Config.ChainId // the node does the same at startup
	}
	if snippet {
		var gc *config.GenesisConfig
		if g != nil {
			gc = &g.Config
		}
		out, _ := json.MarshalIndent(map[string]interface{}{"protocol": protocolparams.FromConfig(&cfg, gc).Section()}, "", "  ")
		fmt.Println(string(out))
		return 0
	}
	report, err := protocolparams.VerifyAgainstGenesis(&cfg, g)
	out, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(out))
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nSTARTUP CHECK WOULD FAIL:\n"+err.Error())
		return 1
	}
	if g != nil && !report.GenesisSection {
		fmt.Fprintln(os.Stderr, "\nnote: this genesis declares no protocol section, so there is nothing to cross-check")
	}
	return 0
}

func loadNodes(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wrapped struct {
		Nodes map[string]string `json:"nodes"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && len(wrapped.Nodes) > 0 {
		return wrapped.Nodes, nil
	}
	var flat map[string]string
	if err := json.Unmarshal(raw, &flat); err != nil || len(flat) == 0 {
		return nil, fmt.Errorf("%s: want {\"name\":\"url\"} or {\"nodes\":{...}}", path)
	}
	return flat, nil
}

func queryAll(nodes map[string]string, timeout time.Duration) map[string]nodeResult {
	out := make(map[string]nodeResult, len(nodes))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, url := range nodes {
		wg.Add(1)
		go func(name, url string) {
			defer wg.Done()
			r, err := queryOne(url, timeout)
			mu.Lock()
			out[name] = nodeResult{Report: r, Err: err}
			mu.Unlock()
		}(name, url)
	}
	wg.Wait()
	return out
}

func queryOne(url string, timeout time.Duration) (protocolparams.Report, error) {
	var zero protocolparams.Report
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"mtn_protocolFingerprint","params":[]}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return zero, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return zero, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return zero, err
	}
	var rpc struct {
		Result *protocolparams.Report `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return zero, fmt.Errorf("not a JSON-RPC answer: %w", err)
	}
	if rpc.Error != nil {
		return zero, fmt.Errorf("rpc error: %s (node too old to report a fingerprint?)", rpc.Error.Message)
	}
	if rpc.Result == nil || rpc.Result.Fingerprint == "" {
		return zero, fmt.Errorf("empty fingerprint in answer")
	}
	return *rpc.Result, nil
}

// compare names the nodes that disagree with the majority and the settings in which they differ.
// Exit code: 0 all agree, 1 settings differ, 2 no difference seen but some node could not be asked.
func compare(results map[string]nodeResult) (string, int) {
	var names, failed []string
	groups := map[string][]string{}
	for name, r := range results {
		names = append(names, name)
		if r.Err != nil {
			failed = append(failed, name)
			continue
		}
		groups[r.Report.Fingerprint] = append(groups[r.Report.Fingerprint], name)
	}
	sort.Strings(names)
	sort.Strings(failed)

	var b strings.Builder
	for _, n := range names {
		if r := results[n]; r.Err != nil {
			fmt.Fprintf(&b, "%-14s ERROR  %v\n", n, r.Err)
		} else {
			fmt.Fprintf(&b, "%-14s %s  genesis_section=%v\n", n, r.Report.Fingerprint[:16], r.Report.GenesisSection)
		}
	}

	// majority = largest group; ties go to the smallest fingerprint so the verdict is deterministic
	var majority string
	for fp, members := range groups {
		sort.Strings(members)
		if majority == "" || len(members) > len(groups[majority]) || (len(members) == len(groups[majority]) && fp < majority) {
			majority = fp
		}
	}
	code := 0
	if len(groups) > 1 {
		code = 1
		ref := results[groups[majority][0]].Report
		fmt.Fprintf(&b, "\nSETTINGS DIFFER. Reference (%d node(s): %s) has fingerprint %s\n", len(groups[majority]), strings.Join(groups[majority], ", "), majority[:16])
		var fps []string
		for fp := range groups {
			if fp != majority {
				fps = append(fps, fp)
			}
		}
		sort.Strings(fps)
		for _, fp := range fps {
			rep := results[groups[fp][0]].Report
			fmt.Fprintf(&b, "\n  %s have fingerprint %s; differing settings:\n", strings.Join(groups[fp], ", "), fp[:16])
			for _, field := range protocolparams.DiffFields(ref, rep) {
				rv, _ := ref.Params.Value(field)
				ov, _ := rep.Params.Value(field)
				fmt.Fprintf(&b, "    %-48s reference=%v   this node=%v\n", field, rv, ov)
			}
		}
	}
	if len(failed) > 0 {
		if code == 0 {
			code = 2
		}
		fmt.Fprintf(&b, "\n%d node(s) could not be asked: %s\n", len(failed), strings.Join(failed, ", "))
	}
	if code == 0 {
		fmt.Fprintf(&b, "\nAll %d node(s) agree: %s\n", len(names), majority)
	}
	return b.String(), code
}
