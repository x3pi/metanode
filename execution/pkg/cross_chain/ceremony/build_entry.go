package ceremony

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

// ZeroHash is the placeholder used for a fresh genesis alloc entry's
// last_hash/device_key fields, matching the convention already used in
// execution/cmd/simple_chain/genesis-main.json.
const ZeroHash = "0x0000000000000000000000000000000000000000000000000000000000000000"

// keytoolKeyFile mirrors the on-disk format written by
// `metanode keytool generate validator` (Rust, crates/metanode-keytool/src/lib.rs)
// for authority_key.json / protocol_key.json / network_key.json.
type keytoolKeyFile struct {
	PrivateKeyHex   string `json:"private_key_hex"`
	PublicKeyBase64 string `json:"public_key_base64"`
}

// keytoolEthKeyFile mirrors eth_key.json.
type keytoolEthKeyFile struct {
	EthPrivateKey string `json:"ETH_PRIVATE_KEY"`
	EthAddress    string `json:"ETH_ADDRESS"`
}

// BuildFoundingEntryParams are the operator-supplied inputs for one validator.
type BuildFoundingEntryParams struct {
	KeysDir           string // directory holding authority_key.json / protocol_key.json / network_key.json / eth_key.json
	ChainID           uint64
	ChainName         string
	Hostname          string
	IP                string
	P2PPort           int
	PrimaryPort       int
	WorkerPort        int
	Description       string
	Website           string
	Image             string
	StakeWei          string // decimal wei string
	MinSelfDelegation string // decimal wei string
	Commission        uint64
	BalanceWei        string // decimal wei string
}

// BuildFoundingEntry reads local key material and derives a FoundingEntry.
// No private key ever appears in the returned value: only the min-pk (blst,
// G1) execution BLS key is derived, and only its PUBLIC key + a
// Proof-of-Possession signature are kept.
func BuildFoundingEntry(p BuildFoundingEntryParams) (FoundingEntry, error) {
	var fe FoundingEntry

	if p.KeysDir == "" {
		return fe, fmt.Errorf("KeysDir is required")
	}
	if p.ChainID == 0 {
		return fe, fmt.Errorf("ChainID is required and must be non-zero")
	}
	if p.ChainName == "" {
		return fe, fmt.Errorf("ChainName is required")
	}
	if p.Hostname == "" {
		return fe, fmt.Errorf("Hostname is required")
	}

	authorityKey, err := loadKeytoolKeyFile(filepath.Join(p.KeysDir, "authority_key.json"))
	if err != nil {
		return fe, fmt.Errorf("reading authority_key.json: %w", err)
	}
	protocolKeyPub, err := loadEd25519PublicKeyBase64(filepath.Join(p.KeysDir, "protocol_key.json"))
	if err != nil {
		return fe, fmt.Errorf("reading protocol_key.json: %w", err)
	}
	networkKeyPub, err := loadEd25519PublicKeyBase64(filepath.Join(p.KeysDir, "network_key.json"))
	if err != nil {
		return fe, fmt.Errorf("reading network_key.json: %w", err)
	}
	ethKey, err := loadEthKeyFile(filepath.Join(p.KeysDir, "eth_key.json"))
	if err != nil {
		return fe, fmt.Errorf("reading eth_key.json: %w", err)
	}

	// Derive the min-pk (blst, G1) execution-layer BLS key from the SAME
	// 32-byte secret scalar authority_key.json holds. This is the missing
	// link between `metanode keytool` (which only writes fastcrypto min-sig
	// pubkeys) and cross_chain.ValidatorEntry (which uses execution/pkg/bls,
	// min-pk). See execution/pkg/bls/bls.go:46 GenerateKeyPairFromSecretKey.
	privKey, pubKey, _ := bls.GenerateKeyPairFromSecretKey(authorityKey.PrivateKeyHex)
	if len(privKey.Bytes()) == 0 {
		return fe, fmt.Errorf("failed to derive min-pk BLS key from authority_key.json private_key_hex — is the file corrupt?")
	}

	popSig := cross_chain.PopSign(privKey, pubKey)

	crossChainStake, err := NormalizeStakeToCommitteeUnits(p.StakeWei)
	if err != nil {
		return fe, fmt.Errorf("StakeWei: %w", err)
	}

	entry := cross_chain.ValidatorEntry{
		PubkeyBLS:    pubKey.Bytes(),
		Stake:        crossChainStake,
		PopSignature: popSig.Bytes(),
	}
	// Fail closed: never emit a founding_entry.json that would not itself
	// pass validation later at merge time (D2/assemble_root_anchor).
	if err := cross_chain.ValidateCommitteeEntry(entry); err != nil {
		return fe, fmt.Errorf("self-check failed, refusing to build a broken entry: %w", err)
	}

	desc := p.Description
	if desc == "" {
		desc = fmt.Sprintf("Validator %s", p.Hostname)
	}
	ethAddr := strings.ToLower(ethKey.EthAddress)

	stakeWei := p.StakeWei
	minSelf := p.MinSelfDelegation
	if minSelf == "" {
		minSelf = "1000000000000000000"
	}
	balance := p.BalanceWei
	if balance == "" {
		balance = "0"
	}

	fe = FoundingEntry{
		SchemaVersion: SchemaVersion,
		ChainID:       p.ChainID,
		ChainName:     p.ChainName,
		GenesisValidator: GenesisValidatorEntry{
			Address:                    ethAddr,
			Hostname:                   p.Hostname,
			Description:                desc,
			Website:                    p.Website,
			Image:                      p.Image,
			CommissionRate:             p.Commission,
			MinSelfDelegation:          minSelf,
			AccumulatedRewardsPerShare: "0",
			DelegatorStakes: []DelegatorStake{
				{Address: ethAddr, Amount: stakeWei},
			},
			TotalStakedAmount: stakeWei,
			NetworkKey:        networkKeyPub,
			AuthorityKey:      authorityKey.PublicKeyBase64,
			ProtocolKey:       protocolKeyPub,
			PrimaryAddress:    fmt.Sprintf("%s:%d", p.IP, p.PrimaryPort),
			WorkerAddress:     fmt.Sprintf("%s:%d", p.IP, p.WorkerPort),
			P2PAddress:        fmt.Sprintf("/ip4/%s/tcp/%d", p.IP, p.P2PPort),
		},
		GenesisAlloc: GenesisAllocEntry{
			Address:        ethAddr,
			Balance:        balance,
			PendingBalance: "0",
			LastHash:       ZeroHash,
			DeviceKey:      ZeroHash,
			PublicKeyBls:   "0x" + hex.EncodeToString(pubKey.Bytes()),
			AccountType:    0,
		},
		CrossChain: CrossChainEntry{
			PubkeyBLS:    "0x" + hex.EncodeToString(pubKey.Bytes()),
			PopSignature: "0x" + hex.EncodeToString(popSig.Bytes()),
			Stake:        crossChainStake,
		},
	}
	return fe, nil
}

func loadKeytoolKeyFile(path string) (keytoolKeyFile, error) {
	var f keytoolKeyFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("parsing %s: %w", path, err)
	}
	if f.PrivateKeyHex == "" || f.PublicKeyBase64 == "" {
		return f, fmt.Errorf("%s: missing private_key_hex/public_key_base64", path)
	}
	return f, nil
}

// loadEd25519PublicKeyBase64 recovers a protocol_key.json/network_key.json's
// public key as base64, tolerating BOTH on-disk shapes that exist in this
// codebase for the same file:
//   - the original `metanode keytool` output: JSON {private_key_hex, public_key_base64}
//   - deploy/systemd/gen_validator_entry.py's in-place rewrite (its
//     rewrite_key_as_base64, ~line 125-135): the file becomes a single raw
//     base64 string encoding priv(32B) || pub(32B), because that is the
//     shape the Rust node's node.toml key files must be in at runtime.
//
// This lets founding_entry work whether an operator runs it directly against
// `metanode keytool` output, or after gen_validator_entry.py has already
// rewritten these two files in place (e.g. via its --founding-entry flag).
func loadEd25519PublicKeyBase64(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var f keytoolKeyFile
	if err := json.Unmarshal(raw, &f); err == nil && f.PublicKeyBase64 != "" {
		return f.PublicKeyBase64, nil
	}

	// Fall back to the rewritten raw-base64 shape.
	combined, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return "", fmt.Errorf("%s: not valid JSON {private_key_hex,public_key_base64} nor a raw base64 priv||pub string: %w", path, err)
	}
	if len(combined) != 64 {
		return "", fmt.Errorf("%s: decoded to %d bytes, expected 64 (32B priv || 32B pub)", path, len(combined))
	}
	return base64.StdEncoding.EncodeToString(combined[32:]), nil
}

func loadEthKeyFile(path string) (keytoolEthKeyFile, error) {
	var f keytoolEthKeyFile
	raw, err := os.ReadFile(path)
	if err != nil {
		return f, err
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return f, fmt.Errorf("parsing %s: %w", path, err)
	}
	if f.EthAddress == "" {
		return f, fmt.Errorf("%s: missing ETH_ADDRESS", path)
	}
	return f, nil
}

// NormalizeStakeToCommitteeUnits mirrors the wei->committee-unit scaling
// already applied on the read path
// (execution/executor/unix_socket_handler_validators.go:452-458): stake =
// total_staked_amount / 1e18. cross_chain.ValidatorEntry.Stake is a uint64
// small-integer "voting weight" (see genesis-main.json's
// total_stake/quorum_threshold, order of a few thousand), NOT a wei amount —
// a raw wei value would overflow uint64 for any realistic stake.
func NormalizeStakeToCommitteeUnits(weiDecimal string) (uint64, error) {
	wei, ok := new(big.Int).SetString(weiDecimal, 10)
	if !ok {
		return 0, fmt.Errorf("invalid decimal amount %q", weiDecimal)
	}
	if wei.Sign() < 0 {
		return 0, fmt.Errorf("stake cannot be negative")
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	units := new(big.Int).Div(wei, divisor)
	if !units.IsUint64() {
		return 0, fmt.Errorf("stake %s is too large after /1e18 normalization to fit a uint64 committee weight", weiDecimal)
	}
	u := units.Uint64()
	if u == 0 {
		return 0, fmt.Errorf("stake %s normalizes to 0 committee weight (must be >= 1e18 wei)", weiDecimal)
	}
	return u, nil
}
