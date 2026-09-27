// encrypt_secret — keep node secrets out of config.json in clear text (see pkg/keyvault).
//
//	encrypt_secret encrypt -password-file pw            # secret on stdin  -> "enc:v1:..." on stdout
//	encrypt_secret decrypt -password-file pw            # envelope on stdin -> secret on stdout (to verify)
//	encrypt_secret config  -in config.json -out config.enc.json -password-file pw [-require]
//
// `config` encrypts every clear-text secret field of config.json (private_key, Databases.BLSPrivateKey,
// gateway_bls_key, reward_sender_private_key, securepassword, master_password, app_pepper, pk_admin_file_storage,
// bls_admin_storage, cross_chain.root_anchor_submitter_private_key_hex), sets key_password_file to the given file,
// and with -require also require_encrypted_keys=true. The node then needs the password at startup, from
// META_KEY_PASSWORD or the file (mode 0600), never from config.json. Secrets are read from stdin, not from
// arguments, so they stay out of shell history and the process list.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/meta-node-blockchain/meta-node/pkg/keyvault"
)

var secretPaths = []string{
	"private_key", "reward_sender_private_key", "securepassword", "pk_admin_file_storage", "bls_admin_storage",
	"gateway_bls_key", "master_password", "app_pepper", "Databases.BLSPrivateKey",
	"cross_chain.root_anchor_submitter_private_key_hex",
}

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "commands: encrypt | decrypt | config")
		return 2
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	pwFile := fs.String("password-file", "", "file with the password (mode 0600); default: META_KEY_PASSWORD")
	in := fs.String("in", "", "config.json to read (config)")
	out := fs.String("out", "", "file to write (config); must not exist")
	req := fs.Bool("require", false, "also set require_encrypted_keys=true (config)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	pw, err := keyvault.LoadPassword(*pwFile)
	if err != nil {
		return fail(stderr, err)
	}
	switch args[0] {
	case "encrypt":
		b, _ := io.ReadAll(stdin)
		s := strings.TrimRight(string(b), "\r\n")
		if s == "" {
			return fail(stderr, errors.New("empty secret on stdin"))
		}
		e, err := keyvault.Encrypt(s, pw)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintln(stdout, e)
	case "decrypt":
		b, _ := io.ReadAll(stdin)
		s, err := keyvault.Decrypt(strings.TrimSpace(string(b)), pw)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintln(stdout, s)
	case "config":
		if *in == "" || *out == "" {
			return fail(stderr, errors.New("-in and -out are required"))
		}
		if _, err := os.Stat(*out); err == nil {
			return fail(stderr, fmt.Errorf("%s already exists: refusing to overwrite", *out))
		}
		n, err := encryptConfig(*in, *out, pw, *pwFile, *req)
		if err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "encrypted %d secret field(s) -> %s\n", n, *out)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		return 2
	}
	return 0
}

func fail(w io.Writer, err error) int {
	fmt.Fprintln(w, "ERROR:", err)
	return 1
}

func encryptConfig(inPath, outPath, pw, pwFile string, require bool) (int, error) {
	raw, err := os.ReadFile(inPath)
	if err != nil {
		return 0, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root map[string]interface{}
	if err := dec.Decode(&root); err != nil {
		return 0, fmt.Errorf("%s: %w", inPath, err)
	}
	n := 0
	for _, p := range secretPaths {
		parent, key := root, p
		if i := strings.Index(p, "."); i >= 0 {
			m, ok := root[p[:i]].(map[string]interface{})
			if !ok {
				continue
			}
			parent, key = m, p[i+1:]
		}
		v, ok := parent[key].(string)
		if !ok || v == "" || keyvault.IsEncrypted(v) {
			continue
		}
		e, err := keyvault.Encrypt(v, pw)
		if err != nil {
			return 0, err
		}
		parent[key] = e
		n++
	}
	if pwFile != "" {
		abs, _ := filepath.Abs(pwFile)
		root["key_password_file"] = abs
	}
	if require {
		root["require_encrypted_keys"] = true
	}
	b, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return 0, err
	}
	return n, os.WriteFile(outPath, b, 0o600)
}
