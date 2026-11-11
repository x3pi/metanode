package main

import (
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/meta-node-blockchain/meta-node/pkg/config"
	"github.com/meta-node-blockchain/meta-node/pkg/rollup/raftfeed"
)

func renderAnsibleJinjaTemplate(t *testing.T, templatePath string, vars map[string]interface{}) string {
	t.Helper()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed; skipping Jinja2 template render check")
	}
	checkCmd := exec.Command("python3", "-c", "import jinja2")
	if err := checkCmd.Run(); err != nil {
		t.Skip("python3 jinja2 not installed; skipping Jinja2 template render check")
	}

	varsJSON, err := json.Marshal(vars)
	require.NoError(t, err)

	pyScript := `
import json, sys, jinja2

tpl_path = sys.argv[1]
ctx = json.loads(sys.argv[2])

def to_bool(val):
    if isinstance(val, bool):
        return val
    if isinstance(val, (int, float)):
        return val != 0
    return str(val).strip().lower() in ("true", "yes", "1", "on")

env = jinja2.Environment(keep_trailing_newline=True)
env.filters["bool"] = to_bool
env.filters["to_json"] = lambda v: json.dumps(v)

with open(tpl_path, "r", encoding="utf-8") as f:
    tpl = env.from_string(f.read())

sys.stdout.write(tpl.render(**ctx))
`
	cmd := exec.Command("python3", "-c", pyScript, templatePath, string(varsJSON))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "failed to render %s:\n%s", templatePath, string(out))
	return string(out)
}

func TestPartB_AnsibleTemplates_LowRamVPSAndHighRamServer_ValidAndSafe(t *testing.T) {
	templatesDir := filepath.Clean(filepath.Join("..", "..", "..", "deploy", "ansible_clusters", "roles", "exec_cluster", "templates"))
	execConfigTpl := filepath.Join(templatesDir, "exec_config.json.j2")
	systemdTpl := filepath.Join(templatesDir, "metanode-exec-cluster.service.j2")
	logrotateTpl := filepath.Join(templatesDir, "exec_cluster.logrotate.j2")

	for _, p := range []string{execConfigTpl, systemdTpl, logrotateTpl} {
		_, err := os.Stat(p)
		require.NoError(t, err, "template file must exist: %s", p)
	}

	tmpDir := t.TempDir()
	secretPath := filepath.Join(tmpDir, "raft_secret.key")
	secretBytes := make([]byte, 32)
	_, err := rand.Read(secretBytes)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(secretPath, secretBytes, 0o600))

	raftPeers := []map[string]string{
		{"id": "pri_r1", "address": "61.28.233.146:7110", "forward_address": "61.28.233.146:7210"},
		{"id": "pri_r2", "address": "116.118.92.254:7110", "forward_address": "116.118.92.254:7210"},
		{"id": "pri_r3", "address": "49.213.71.191:7110", "forward_address": "49.213.71.191:7210"},
		{"id": "pri_r4", "address": "61.28.238.209:7113", "forward_address": "61.28.238.209:7213"},
		{"id": "pri_r5", "address": "61.28.238.209:7114", "forward_address": "61.28.238.209:7214"},
	}

	t.Run("LowRamVPS_pri_1a_Profile", func(t *testing.T) {
		config.ResetConfigForTesting()
		t.Cleanup(config.ResetConfigForTesting)

		vars := map[string]interface{}{
			"metanode_env":             "devnet",
			"node_env":                 "devnet",
			"inventory_hostname":       "pri_1a",
			"ansible_user":             "stackops",
			"ansible_host":             "61.28.233.146",
			"cluster_id":               1,
			"node_id":                  "pri_r1",
			"node_data_dir":            "/opt/metanode/pri_cloud",
			"install_dir":              "/opt/metanode",
			"bin_dir":                  "/opt/metanode/bin",
			"base_log_dir":             "/var/log/metanode",
			"rpc_port":                 8750,
			"p2p_port":                 4200,
			"raft_port":                7110,
			"forward_port":             7210,
			"raft_bootstrap":           true,
			"raft_secret_file":         secretPath,
			"raft_peers":               raftPeers,
			"private_key":              "3c89b21f980145be702a0b7713d298ea547849b291a8e97f480371db68e64c23",
			"address":                  "0x5a075aC7e35CD05E331eaD878d52CbfBE5f8d732",
			"bls_priv":                 "3c89b21f980145be702a0b7713d298ea547849b291a8e97f480371db68e64c23",
			"state_backend":            "nomt",
			"go_mem_limit_gb":          1,
			"go_gc_percent":            100,
			"pebble_cache_size_mb":     128,
			"pebble_mem_table_size_mb": 32,
			"db_parallelism":           4,
			"nomt_commit_concurrency":  4,
			"nomt_page_cache_mb":       64,
			"nomt_leaf_cache_mb":       64,
			"is_explorer":              true,
			"is_explorer_read_only":    false,
			"explorer_queue_size":      2048,
			"explorer_worker_count":    8,
			"log_console_output":       false,
			"epochs_to_keep":           3,
			"pruning_mode":             "archive",
			"systemd_memory_high":      "3200M",
		}

		renderedJSON := renderAnsibleJinjaTemplate(t, execConfigTpl, vars)
		cfgFile := filepath.Join(t.TempDir(), "config_pri_1a.json")
		require.NoError(t, os.WriteFile(cfgFile, []byte(renderedJSON), 0o600))

		cfg, lErr := config.LoadConfig(cfgFile)
		require.NoError(t, lErr, "rendered pri_1a config.json must load cleanly:\n%s", renderedJSON)
		require.NoError(t, raftfeed.ValidateConfig(cfg), "rendered pri_1a config must pass raftfeed.ValidateConfig")

		assert.Equal(t, "nomt", cfg.StateBackend)
		assert.Equal(t, 1, cfg.GoMemLimitGB)
		assert.Equal(t, 100, cfg.GoGCPercent)
		assert.Equal(t, 128, cfg.Databases.PebbleCacheSizeMB)
		assert.Equal(t, 32, cfg.Databases.PebbleMemTableSizeMB)
		assert.Equal(t, 4, cfg.Databases.Parallelism)
		assert.Equal(t, 4, cfg.NomtCommitConcurrency)
		assert.Equal(t, 64, cfg.NomtPageCacheMB)
		assert.Equal(t, 64, cfg.NomtLeafCacheMB)
		assert.True(t, cfg.IsExplorer)
		assert.False(t, cfg.ExplorerReadOnlyEnabled())
		assert.Equal(t, 2048, cfg.ExplorerQueueSize)
		assert.Equal(t, 8, cfg.ExplorerWorkerCount)
		assert.False(t, cfg.Log.ConsoleOutput)
		require.NotNil(t, cfg.EpochsToKeep)
		assert.Equal(t, 3, *cfg.EpochsToKeep)
		assert.Equal(t, "archive", cfg.Pruning.Mode, "DB pruning must be disabled (archive mode)")

		renderedService := renderAnsibleJinjaTemplate(t, systemdTpl, vars)
		assert.Contains(t, renderedService, "MemoryHigh=3200M")
		assert.NotContains(t, renderedService, "MemoryMax=", "MemoryMax must be omitted so systemd does not prematurely OOM-kill before Swap")

		renderedLogrotate := renderAnsibleJinjaTemplate(t, logrotateTpl, vars)
		assert.Contains(t, renderedLogrotate, "su stackops stackops")
		assert.Contains(t, renderedLogrotate, "rotate 3")
		assert.NotContains(t, renderedLogrotate, "compress")
		assert.NotContains(t, renderedLogrotate, "/logs/*.log")
	})

	t.Run("HighRamServer_pri_1d_DefaultProfile", func(t *testing.T) {
		config.ResetConfigForTesting()
		t.Cleanup(config.ResetConfigForTesting)

		// pri_1d defines NO memory/cache overrides in inventory.cloud.yml -> inherits template defaults
		vars := map[string]interface{}{
			"metanode_env":       "devnet",
			"node_env":           "devnet",
			"inventory_hostname": "pri_1d",
			"ansible_host":       "61.28.238.209",
			"cluster_id":         1,
			"node_id":            "pri_r4",
			"node_data_dir":      "/opt/metanode/pri_cloud_r4",
			"install_dir":        "/opt/metanode",
			"bin_dir":            "/opt/metanode/bin",
			"base_log_dir":       "/var/log/metanode",
			"rpc_port":           8750,
			"p2p_port":           4203,
			"raft_port":          7113,
			"forward_port":       7213,
			"raft_bootstrap":     false,
			"raft_secret_file":   secretPath,
			"raft_peers":         raftPeers,
			"private_key":        "3c89b21f980145be702a0b7713d298ea547849b291a8e97f480371db68e64c23",
			"address":            "0x5a075aC7e35CD05E331eaD878d52CbfBE5f8d732",
			"bls_priv":           "3c89b21f980145be702a0b7713d298ea547849b291a8e97f480371db68e64c23",
		}

		renderedJSON := renderAnsibleJinjaTemplate(t, execConfigTpl, vars)
		cfgFile := filepath.Join(t.TempDir(), "config_pri_1d.json")
		require.NoError(t, os.WriteFile(cfgFile, []byte(renderedJSON), 0o600))

		cfg, lErr := config.LoadConfig(cfgFile)
		require.NoError(t, lErr, "rendered pri_1d config.json must load cleanly:\n%s", renderedJSON)
		require.NoError(t, raftfeed.ValidateConfig(cfg))

		assert.Equal(t, "nomt", cfg.StateBackend)
		assert.Equal(t, 8, cfg.GoMemLimitGB)
		assert.Equal(t, 800, cfg.GoGCPercent)
		assert.Equal(t, 4096, cfg.Databases.PebbleCacheSizeMB)
		assert.Equal(t, 256, cfg.Databases.PebbleMemTableSizeMB)
		assert.Equal(t, 4, cfg.NomtCommitConcurrency)
		assert.Equal(t, 128, cfg.NomtPageCacheMB)
		assert.Equal(t, 128, cfg.NomtLeafCacheMB)
		assert.True(t, cfg.IsExplorer)
		assert.False(t, cfg.ExplorerReadOnlyEnabled())
		assert.Equal(t, 65536, cfg.ExplorerQueueSize)
		assert.Equal(t, 32, cfg.ExplorerWorkerCount)
		assert.True(t, cfg.Log.ConsoleOutput)
		assert.Equal(t, "archive", cfg.Pruning.Mode, "High-RAM server must also default to archive mode (zero DB pruning)")

		renderedService := renderAnsibleJinjaTemplate(t, systemdTpl, vars)
		assert.Contains(t, renderedService, "User=abc")
		assert.NotContains(t, renderedService, "MemoryHigh=")
		assert.NotContains(t, renderedService, "MemoryMax=")

		renderedLogrotate := renderAnsibleJinjaTemplate(t, logrotateTpl, vars)
		assert.Contains(t, renderedLogrotate, "su abc abc", "omitted ansible_user must fallback to abc")
	})
}
