package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func loadConfigFreshForTest(t *testing.T, rawJSON string) *SimpleChainConfig {
	t.Helper()
	ResetConfigForTesting()
	t.Cleanup(ResetConfigForTesting)

	cfgPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(cfgPath, []byte(rawJSON), 0o600))

	cfg, err := LoadConfig(cfgPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	return cfg
}

func TestExplorerReadOnlyEnabled_BackwardCompatibleAndExplicit(t *testing.T) {
	var nilCfg *SimpleChainConfig
	assert.False(t, nilCfg.ExplorerReadOnlyEnabled(), "nil config must report false")

	t.Run("omitted is_explorer_read_only defaults to false when is_explorer=true", func(t *testing.T) {
		cfg := loadConfigFreshForTest(t, `{
			"is_explorer": true,
			"explorer_db_path": "/tmp/explorer",
			"explorer_read_only_db_path": "/tmp/explorer-ro"
		}`)
		assert.True(t, cfg.IsExplorer)
		assert.Nil(t, cfg.IsExplorerReadOnly)
		assert.False(t, cfg.ExplorerReadOnlyEnabled())
	})

	t.Run("explicit is_explorer_read_only=false disables secondary Xapian index while keeping primary explorer", func(t *testing.T) {
		cfg := loadConfigFreshForTest(t, `{
			"is_explorer": true,
			"is_explorer_read_only": false,
			"explorer_queue_size": 2048,
			"explorer_worker_count": 4
		}`)
		assert.True(t, cfg.IsExplorer)
		require.NotNil(t, cfg.IsExplorerReadOnly)
		assert.False(t, *cfg.IsExplorerReadOnly)
		assert.False(t, cfg.ExplorerReadOnlyEnabled())
		assert.Equal(t, 2048, cfg.ExplorerQueueSize)
		assert.Equal(t, 4, cfg.ExplorerWorkerCount)
	})

	t.Run("explicit is_explorer_read_only=true keeps secondary Xapian index enabled", func(t *testing.T) {
		cfg := loadConfigFreshForTest(t, `{
			"is_explorer": true,
			"is_explorer_read_only": true
		}`)
		assert.True(t, cfg.IsExplorer)
		require.NotNil(t, cfg.IsExplorerReadOnly)
		assert.True(t, *cfg.IsExplorerReadOnly)
		assert.True(t, cfg.ExplorerReadOnlyEnabled())
	})

	t.Run("is_explorer=false forces ExplorerReadOnlyEnabled=false even if is_explorer_read_only=true", func(t *testing.T) {
		cfg := loadConfigFreshForTest(t, `{
			"is_explorer": false,
			"is_explorer_read_only": true
		}`)
		assert.False(t, cfg.IsExplorer)
		assert.False(t, cfg.ExplorerReadOnlyEnabled())
	})
}

func TestEpochsToKeep_DecoupledFromChainPruning(t *testing.T) {
	t.Run("low_ram_vps config keeps 128-epoch chain pruning when epochs_to_keep=2 is set for log cleaner", func(t *testing.T) {
		cfg := loadConfigFreshForTest(t, `{
			"state_backend": "nomt",
			"epochs_to_keep": 2,
			"pruning": {
				"mode": "full",
				"epochs_to_keep": 128,
				"prune_interval_blocks": 100
			},
			"Databases": {
				"pebble_cache_size_mb": 128,
				"pebble_mem_table_size_mb": 32
			}
		}`)
		require.NotNil(t, cfg.EpochsToKeep)
		assert.Equal(t, 2, *cfg.EpochsToKeep, "log cleaner retention must stay 2 days/epochs")
		assert.Equal(t, "full", cfg.Pruning.Mode)
		assert.Equal(t, 128, cfg.Pruning.EpochsToKeep, "chain DB pruning must NOT be dragged down to 2 epochs")
		assert.Equal(t, 100, cfg.Pruning.PruneIntervalBlocks)
		assert.Equal(t, 128, cfg.Databases.PebbleCacheSizeMB)
		assert.Equal(t, 32, cfg.Databases.PebbleMemTableSizeMB)
	})

	t.Run("archive pruning mode stays archive even when epochs_to_keep=2", func(t *testing.T) {
		cfg := loadConfigFreshForTest(t, `{
			"epochs_to_keep": 2,
			"pruning": {
				"mode": "archive",
				"epochs_to_keep": 128,
				"prune_interval_blocks": 100
			}
		}`)
		assert.Equal(t, "archive", cfg.Pruning.Mode)
	})
}
