package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/keyvault"
)

func TestConfigModeEncryptsEverySecretAndNothingElse(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	os.WriteFile(pw, []byte("tool-test-password\n"), 0o600)
	in := filepath.Join(dir, "config.json")
	os.WriteFile(in, []byte(`{"private_key":"aa11","address":"0x1","chainId":991,"Databases":{"BLSPrivateKey":"bb22","RootPath":"/d"},
	  "cross_chain":{"root_anchor_submitter_private_key_hex":"cc33"},"gateway_bls_key":"","securepassword":"pp"}`), 0o600)
	out := filepath.Join(dir, "config.enc.json")
	var so, se bytes.Buffer
	if code := run([]string{"config", "-in", in, "-out", out, "-password-file", pw, "-require"}, nil, &so, &se); code != 0 {
		t.Fatalf("exit %d: %s", code, se.String())
	}
	raw, _ := os.ReadFile(out)
	if bytes.Contains(raw, []byte("aa11")) || bytes.Contains(raw, []byte("bb22")) || bytes.Contains(raw, []byte("cc33")) {
		t.Fatalf("a clear-text secret is still in the output: %s", raw)
	}
	var m map[string]interface{}
	json.Unmarshal(raw, &m)
	if !strings.HasPrefix(m["private_key"].(string), "enc:v1:") || m["address"] != "0x1" || m["chainId"].(float64) != 991 ||
		m["gateway_bls_key"] != "" || m["require_encrypted_keys"] != true || m["key_password_file"] != pw {
		t.Fatalf("unexpected output: %v", m)
	}
	if v, _ := keyvault.Decrypt(m["Databases"].(map[string]interface{})["BLSPrivateKey"].(string), "tool-test-password"); v != "bb22" {
		t.Fatalf("nested secret did not round-trip: %q", v)
	}
	// refuses to overwrite
	if code := run([]string{"config", "-in", in, "-out", out, "-password-file", pw}, nil, &so, &se); code == 0 {
		t.Fatal("overwrote an existing output file")
	}
}

func TestEncryptDecryptViaStdin(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw")
	os.WriteFile(pw, []byte("tool-test-password"), 0o600)
	var enc, se bytes.Buffer
	if code := run([]string{"encrypt", "-password-file", pw}, strings.NewReader("the-secret\n"), &enc, &se); code != 0 {
		t.Fatal(se.String())
	}
	var dec bytes.Buffer
	if code := run([]string{"decrypt", "-password-file", pw}, &enc, &dec, &se); code != 0 || strings.TrimSpace(dec.String()) != "the-secret" {
		t.Fatalf("%d %q %s", code, dec.String(), se.String())
	}
}
