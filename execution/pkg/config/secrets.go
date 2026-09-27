package config

import (
	"fmt"
	"strings"

	"github.com/meta-node-blockchain/meta-node/pkg/keyvault"
)

// secretFields lists every secret of SimpleChainConfig (JSON name -> address of the field). Each may hold either
// a plain value or a keyvault envelope ("enc:v1:...").
func secretFields(c *SimpleChainConfig) map[string]*string {
	return map[string]*string{
		"private_key":               &c.PrivateKey,
		"reward_sender_private_key": &c.RewardSenderPrivateKey,
		"securepassword":            &c.Securepassword,
		"pk_admin_file_storage":     &c.PkAdminFileStorage,
		"bls_admin_storage":         &c.BlsAdminStorage,
		"gateway_bls_key":           &c.GatewayBLSKey,
		"master_password":           &c.MasterPassword,
		"app_pepper":                &c.AppPepper,
		"Databases.BLSPrivateKey":   &c.Databases.BLSPrivateKey,
		"cross_chain.root_anchor_submitter_private_key_hex": &c.CrossChain.RootAnchorSubmitterPrivateKeyHex,
	}
}

// resolveSecrets decrypts every keyvault envelope in place (once, at startup) and, when require_encrypted_keys is
// set, refuses to continue while any secret is still in clear text. It runs after the META_* environment
// overrides, so an override may itself be an envelope.
func resolveSecrets(c *SimpleChainConfig) error {
	fields := secretFields(c)
	var plain, enc []string
	for name, p := range fields {
		switch {
		case *p == "":
		case keyvault.IsEncrypted(*p):
			enc = append(enc, name)
		default:
			plain = append(plain, name)
		}
	}
	if c.RequireEncryptedKeys && len(plain) > 0 {
		return fmt.Errorf("require_encrypted_keys is set but these secrets are in clear text: %s (encrypt them with cmd/tool/encrypt_secret)", strings.Join(sortedStrings(plain), ", "))
	}
	if len(enc) == 0 {
		return nil
	}
	pw, err := keyvault.LoadPassword(c.KeyPasswordFile)
	if err != nil {
		return fmt.Errorf("encrypted secrets present (%s): %w", strings.Join(sortedStrings(enc), ", "), err)
	}
	for _, name := range enc {
		v, err := keyvault.Decrypt(*fields[name], pw)
		if err != nil {
			return fmt.Errorf("secret %s: %w", name, err)
		}
		*fields[name] = v
	}
	return nil
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
