package config

import (
	"os"
	"testing"

	"github.com/meta-node-blockchain/meta-node/pkg/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig_ResourceProfilePrecedenceAndDefaults(t *testing.T) {
	t.Run("ExplicitFieldsAlwaysWinOverTinyProfile", func(t *testing.T) {
		t.Setenv("META_RESOURCE_PROFILE", "")
		cfg := loadConfigFreshForTest(t, `{
			"resource_profile": "tiny",
			"go_mem_limit_gb": 3,
			"go_gc_percent": 350,
			"nomt_page_cache_mb": 96,
			"nomt_leaf_cache_mb": 80,
			"explorer_queue_size": 4096,
			"explorer_worker_count": 6,
			"Databases": {
				"pebble_cache_size_mb": 256,
				"pebble_mem_table_size_mb": 32
			}
		}`)
		assert.Equal(t, "tiny", cfg.ResourceProfile)
		assert.Equal(t, 3, cfg.GoMemLimitGB)
		assert.Equal(t, 3*1024, cfg.EffectiveGoMemLimitMB())
		assert.Equal(t, 350, cfg.GoGCPercent)
		assert.Equal(t, 96, cfg.NomtPageCacheMB)
		assert.Equal(t, 80, cfg.NomtLeafCacheMB)
		assert.Equal(t, 4096, cfg.ExplorerQueueSize)
		assert.Equal(t, 6, cfg.ExplorerWorkerCount)
		assert.Equal(t, 256, cfg.Databases.PebbleCacheSizeMB)
		assert.Equal(t, 32, cfg.Databases.PebbleMemTableSizeMB)
	})

	t.Run("MetaResourceProfileEnvOverridesConfigJSON", func(t *testing.T) {
		t.Setenv("META_RESOURCE_PROFILE", "small")
		cfg := loadConfigFreshForTest(t, `{
			"resource_profile": "large"
		}`)
		assert.Equal(t, "small", cfg.ResourceProfile)
		assert.Equal(t, "small", utils.GetConfiguredResourceProfile())
		assert.Equal(t, 200, cfg.GoGCPercent)
		assert.Equal(t, 512, cfg.Databases.PebbleCacheSizeMB)
		assert.Equal(t, 64, cfg.Databases.PebbleMemTableSizeMB)
		assert.Equal(t, 128, cfg.NomtPageCacheMB)
		assert.Equal(t, 128, cfg.NomtLeafCacheMB)
		assert.Equal(t, 8192, cfg.ExplorerQueueSize)
		assert.Equal(t, 4, cfg.ExplorerWorkerCount)
	})

	t.Run("ZeroFieldsResolveFromTinyProfile", func(t *testing.T) {
		t.Setenv("META_RESOURCE_PROFILE", "")
		cfg := loadConfigFreshForTest(t, `{
			"resource_profile": "tiny",
			"explorer_queue_size": 0,
			"explorer_worker_count": 0,
			"go_mem_limit_gb": 0,
			"go_gc_percent": 0,
			"nomt_page_cache_mb": 0,
			"nomt_leaf_cache_mb": 0,
			"Databases": {
				"pebble_cache_size_mb": 0,
				"pebble_mem_table_size_mb": 0
			}
		}`)
		assert.Equal(t, "tiny", cfg.ResourceProfile)
		assert.Equal(t, 100, cfg.GoGCPercent)
		assert.Equal(t, 128, cfg.Databases.PebbleCacheSizeMB)
		assert.Equal(t, 16, cfg.Databases.PebbleMemTableSizeMB)
		assert.Equal(t, 64, cfg.NomtPageCacheMB)
		assert.Equal(t, 64, cfg.NomtLeafCacheMB)
		assert.Equal(t, 2048, cfg.ExplorerQueueSize)
		assert.GreaterOrEqual(t, cfg.ExplorerWorkerCount, 1)
		assert.LessOrEqual(t, cfg.ExplorerWorkerCount, 2)
		assert.Equal(t, 100_000, utils.ActiveResourceDefaults().TrieNodeCacheEntries)
		assert.Equal(t, 200_000, utils.ActiveResourceDefaults().SignatureCacheEntries)
	})

	t.Run("PreciseGoMemLimitMBOverridesZeroGB", func(t *testing.T) {
		t.Setenv("META_RESOURCE_PROFILE", "")
		cfg := loadConfigFreshForTest(t, `{
			"resource_profile": "small",
			"go_mem_limit_mb": 2400
		}`)
		assert.Equal(t, 2400, cfg.EffectiveGoMemLimitMB())
		assert.Equal(t, 2, cfg.GoMemLimitGB)
	})

	t.Run("InvalidResourceProfileFailsClosed", func(t *testing.T) {
		t.Setenv("META_RESOURCE_PROFILE", "")
		ResetConfigForTesting()
		t.Cleanup(ResetConfigForTesting)

		tmp := t.TempDir()
		p := tmp + "/bad.json"
		require.NoError(t, os.WriteFile(p, []byte(`{"resource_profile":"enormous"}`), 0o600))
		_, err := LoadConfig(p)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid resource_profile")
	})
}
