// gen_recovery_committee prints the []cross_chain.ValidatorEntry JSON for a given list of BLS
// secret hexes (real pkg/bls min-pk scalars, e.g. the same "authority" keys ansible's local_build
// role already generates for a chain's own validators -- deploy/systemd/node-N_keys/
// authority_key.json). The output is ready to paste directly into
// cross_chain.recovery_committee_json config (see pkg/config/config.go and
// pkg/blockchain/tx_processor/gateway_handler.go's applyRecoveryCommitteeConfig).
//
// RecoveryCommittee (2026-09-04, replacing the deleted GovernanceEngine's propose/vote/execute
// gate for actions no chain can self-authorize -- declareChainDeadWithCert/unregisterChainWithCert/
// updateCommitteeWithRecoveryCert, see GatewayEngine.RecoveryCommittee's own doc comment) is meant
// to be a small, FIXED, config-set committee, deliberately NOT grown by RegisterChainViaStake.
// This tool exists because building the config value requires real BLS pubkey derivation + a real
// Proof-of-Possession signature (cross_chain.PopSign) -- neither of which any of this repo's
// Python devnet-generation scripts can safely reimplement themselves; this reuses the exact same
// pkg/bls/pkg/cross_chain code paths the rest of the codebase already trusts.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/meta-node-blockchain/meta-node/pkg/bls"
	"github.com/meta-node-blockchain/meta-node/pkg/cross_chain"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: gen_recovery_committee <secret_hex_1> [secret_hex_2] ...")
		os.Exit(1)
	}
	var committee []cross_chain.ValidatorEntry
	for _, secretHex := range os.Args[1:] {
		priv, pub, _ := bls.GenerateKeyPairFromSecretKey(secretHex)
		pop := cross_chain.PopSign(priv, pub)
		committee = append(committee, cross_chain.ValidatorEntry{
			PubkeyBLS:    pub.Bytes(),
			Stake:        1000,
			PopSignature: pop.Bytes(),
		})
	}
	out, err := json.Marshal(committee)
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		os.Exit(1)
	}
	fmt.Println(string(out))
}
