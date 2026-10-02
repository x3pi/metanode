package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"

	"github.com/meta-node-blockchain/meta-node/pkg/parentchain"
)

// ClusterSpec holds cluster BLS public key and its genesis path.
type ClusterSpec struct {
	BLSPublicKey string
	GenesisPath  string
}

// ClusterFloatResult contains calculated supply for a cluster.
type ClusterFloatResult struct {
	BLSPublicKey string   `json:"bls_public_key"`
	Balance      string   `json:"balance"`
	AccountCount int      `json:"account_count"`
	TotalBig     *big.Int `json:"-"`
}

func main() {
	var (
		genesisPath       string
		blsPubKey         string
		clusterSpecs      multiClusterFlag
		format            string
		outFile           string
		patchParentGen    string
	)

	flag.StringVar(&genesisPath, "genesis", "", "Path to execution cluster genesis.json")
	flag.StringVar(&blsPubKey, "bls-pubkey", "", "Cluster BLS public key (48-byte hex or base64)")
	flag.Var(&clusterSpecs, "cluster", "Cluster spec in format 'BLS_PUBKEY=GENESIS_PATH' (can be specified multiple times)")
	flag.StringVar(&format, "format", "json", "Output format: json | yaml | text")
	flag.StringVar(&outFile, "out", "", "Output file path (default stdout)")
	flag.StringVar(&patchParentGen, "patch-parent-genesis", "", "Optional path to parent_genesis.json to update float_accounts in-place")
	flag.Parse()

	var specs []ClusterSpec
	if genesisPath != "" || blsPubKey != "" {
		if genesisPath == "" || blsPubKey == "" {
			fmt.Fprintln(os.Stderr, "Error: both -genesis and -bls-pubkey must be specified together")
			os.Exit(1)
		}
		specs = append(specs, ClusterSpec{
			BLSPublicKey: strings.TrimSpace(blsPubKey),
			GenesisPath:  strings.TrimSpace(genesisPath),
		})
	}
	for _, cs := range clusterSpecs {
		specs = append(specs, cs)
	}

	if len(specs) == 0 {
		fmt.Fprintln(os.Stderr, "Usage of gen_float_accounts:")
		fmt.Fprintln(os.Stderr, "  gen_float_accounts -genesis <path> -bls-pubkey <pubkey> [-format json|yaml|text] [-patch-parent-genesis <path>]")
		fmt.Fprintln(os.Stderr, "  gen_float_accounts -cluster <pubkey1>=<genesis1.json> -cluster <pubkey2>=<genesis2.json> ...")
		os.Exit(1)
	}

	results, err := ProcessClusters(specs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error processing clusters: %v\n", err)
		os.Exit(1)
	}

	// Format output
	var outContent string
	switch strings.ToLower(format) {
	case "yaml":
		outContent = FormatYAML(results)
	case "text":
		outContent = FormatText(results)
	case "json":
		outContent, err = FormatJSON(results)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error formatting JSON: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown format %q (allowed: json, yaml, text)\n", format)
		os.Exit(1)
	}

	if patchParentGen != "" {
		if err := PatchParentGenesis(patchParentGen, results); err != nil {
			fmt.Fprintf(os.Stderr, "Error patching parent genesis %s: %v\n", patchParentGen, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "✅ Successfully patched float_accounts in %s with %d cluster(s)\n", patchParentGen, len(results))
	}

	if outFile != "" {
		if err := os.WriteFile(outFile, []byte(outContent), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing output to %s: %v\n", outFile, err)
			os.Exit(1)
		}
	} else {
		fmt.Print(outContent)
	}
}

// ProcessClusters calculates the total allocation for each cluster spec.
func ProcessClusters(specs []ClusterSpec) ([]ClusterFloatResult, error) {
	results := make([]ClusterFloatResult, 0, len(specs))
	seenKeys := make(map[string]bool)

	for i, spec := range specs {
		normKey, err := ValidateAndNormalizeBLSPublicKey(spec.BLSPublicKey)
		if err != nil {
			return nil, fmt.Errorf("cluster[%d]: invalid BLS public key: %w", i, err)
		}
		if seenKeys[normKey] {
			return nil, fmt.Errorf("cluster[%d]: duplicate BLS public key %s", i, normKey)
		}
		seenKeys[normKey] = true

		sum, count, err := SumGenesisAlloc(spec.GenesisPath)
		if err != nil {
			return nil, fmt.Errorf("cluster[%d] (%s): failed to sum genesis alloc: %w", i, spec.GenesisPath, err)
		}

		results = append(results, ClusterFloatResult{
			BLSPublicKey: normKey,
			Balance:      sum.String(),
			AccountCount: count,
			TotalBig:     sum,
		})
	}

	return results, nil
}

// SumGenesisAlloc parses an execution cluster genesis.json and sums balance + pending_balance of all alloc entries.
func SumGenesisAlloc(path string) (*big.Int, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot read file: %w", err)
	}

	var root struct {
		Alloc []struct {
			Address        string `json:"address"`
			Balance        string `json:"balance"`
			PendingBalance string `json:"pending_balance"`
		} `json:"alloc"`
	}

	if err := json.Unmarshal(data, &root); err != nil {
		return nil, 0, fmt.Errorf("cannot parse JSON: %w", err)
	}

	total := new(big.Int)
	for idx, entry := range root.Alloc {
		balStr := strings.TrimSpace(entry.Balance)
		if balStr != "" && balStr != "0" {
			bal, ok := new(big.Int).SetString(balStr, 10)
			if !ok {
				return nil, 0, fmt.Errorf("alloc[%d] (%s): invalid balance decimal integer %q", idx, entry.Address, entry.Balance)
			}
			if bal.Sign() < 0 {
				return nil, 0, fmt.Errorf("alloc[%d] (%s): negative balance %s", idx, entry.Address, bal.String())
			}
			total.Add(total, bal)
		}

		pendStr := strings.TrimSpace(entry.PendingBalance)
		if pendStr != "" && pendStr != "0" {
			pend, ok := new(big.Int).SetString(pendStr, 10)
			if !ok {
				return nil, 0, fmt.Errorf("alloc[%d] (%s): invalid pending_balance decimal integer %q", idx, entry.Address, entry.PendingBalance)
			}
			if pend.Sign() < 0 {
				return nil, 0, fmt.Errorf("alloc[%d] (%s): negative pending_balance %s", idx, entry.Address, pend.String())
			}
			total.Add(total, pend)
		}
	}

	return total, len(root.Alloc), nil
}

// ValidateAndNormalizeBLSPublicKey validates that the key decodes to exactly 48 bytes and returns 0x-hex format.
func ValidateAndNormalizeBLSPublicKey(s string) (string, error) {
	s = strings.TrimSpace(s)
	var raw []byte
	var err error

	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		raw, err = hex.DecodeString(s[2:])
	} else if len(s)%2 == 0 {
		raw, err = hex.DecodeString(s)
		if err != nil {
			raw, err = base64.StdEncoding.DecodeString(s)
		}
	} else {
		raw, err = base64.StdEncoding.DecodeString(s)
	}

	if err != nil {
		return "", fmt.Errorf("key is neither valid hex nor valid base64: %w", err)
	}

	if len(raw) != 48 {
		return "", fmt.Errorf("expected 48-byte BLS public key, got %d bytes", len(raw))
	}

	return "0x" + hex.EncodeToString(raw), nil
}

// FormatJSON produces JSON representation suitable for parent_genesis.json.
func FormatJSON(results []ClusterFloatResult) (string, error) {
	floatAccounts := make([]parentchain.GenesisFloatAccount, len(results))
	for i, r := range results {
		floatAccounts[i] = parentchain.GenesisFloatAccount{
			BLSPublicKey: r.BLSPublicKey,
			Balance:      r.Balance,
		}
	}

	wrapped := struct {
		FloatAccounts []parentchain.GenesisFloatAccount `json:"float_accounts"`
	}{
		FloatAccounts: floatAccounts,
	}

	bytes, err := json.MarshalIndent(wrapped, "", "  ")
	if err != nil {
		return "", err
	}
	return string(bytes) + "\n", nil
}

// FormatYAML produces YAML snippet for ansible inventory.yml under parent_float_accounts.
func FormatYAML(results []ClusterFloatResult) string {
	var sb strings.Builder
	sb.WriteString("parent_float_accounts:\n")
	for _, r := range results {
		sb.WriteString(fmt.Sprintf("  - bls_public_key: \"%s\"\n", r.BLSPublicKey))
		sb.WriteString(fmt.Sprintf("    balance: \"%s\"\n", r.Balance))
	}
	return sb.String()
}

// FormatText produces human-readable diagnostic report.
func FormatText(results []ClusterFloatResult) string {
	var sb strings.Builder
	sb.WriteString("=== Float Accounts Summary for Parent Chain Genesis ===\n")
	grandTotal := new(big.Int)
	for i, r := range results {
		sb.WriteString(fmt.Sprintf("Cluster %d:\n", i+1))
		sb.WriteString(fmt.Sprintf("  BLS Public Key : %s\n", r.BLSPublicKey))
		sb.WriteString(fmt.Sprintf("  Accounts Count : %d\n", r.AccountCount))
		sb.WriteString(fmt.Sprintf("  Float Balance  : %s base units\n", r.Balance))
		grandTotal.Add(grandTotal, r.TotalBig)
	}
	sb.WriteString("-------------------------------------------------------\n")
	sb.WriteString(fmt.Sprintf("Grand Total Supply: %s base units (%d cluster(s))\n", grandTotal.String(), len(results)))
	return sb.String()
}

// PatchParentGenesis reads parent_genesis.json, updates float_accounts, and writes back atomically.
func PatchParentGenesis(parentGenPath string, results []ClusterFloatResult) error {
	data, err := os.ReadFile(parentGenPath)
	if err != nil {
		return fmt.Errorf("read parent genesis: %w", err)
	}

	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("unmarshal parent genesis: %w", err)
	}

	floatAccounts := make([]map[string]string, len(results))
	for i, r := range results {
		floatAccounts[i] = map[string]string{
			"bls_public_key": r.BLSPublicKey,
			"balance":      r.Balance,
		}
	}
	root["float_accounts"] = floatAccounts

	updatedBytes, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal updated parent genesis: %w", err)
	}

	tmpFile := parentGenPath + ".tmp"
	if err := os.WriteFile(tmpFile, append(updatedBytes, '\n'), 0644); err != nil {
		return fmt.Errorf("write tmp file: %w", err)
	}

	if err := os.Rename(tmpFile, parentGenPath); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("atomic rename: %w", err)
	}

	return nil
}

type multiClusterFlag []ClusterSpec

func (m *multiClusterFlag) String() string {
	return fmt.Sprintf("%v", []ClusterSpec(*m))
}

func (m *multiClusterFlag) Set(val string) error {
	parts := strings.SplitN(val, "=", 2)
	if len(parts) != 2 {
		return fmt.Errorf("cluster spec must be in format 'BLS_PUBKEY=GENESIS_PATH', got %q", val)
	}
	*m = append(*m, ClusterSpec{
		BLSPublicKey: strings.TrimSpace(parts[0]),
		GenesisPath:  strings.TrimSpace(parts[1]),
	})
	return nil
}
