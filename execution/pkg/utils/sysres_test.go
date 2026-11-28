package utils

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMeminfoTotalMB(t *testing.T) {
	sample := `MemTotal:        3981236 kB
MemFree:          412000 kB
MemAvailable:    1820000 kB
`
	assert.Equal(t, uint64(3981236/1024), parseMeminfoTotalMB(sample))
	assert.Equal(t, uint64(0), parseMeminfoTotalMB("MemFree: 1000 kB\n"))
	assert.Equal(t, uint64(0), parseMeminfoTotalMB(""))
}

func TestParseCgroupLimitBytes(t *testing.T) {
	_, ok := parseCgroupLimitBytes("max\n")
	assert.False(t, ok)

	_, ok = parseCgroupLimitBytes("9223372036854771712\n") // cgroup v1 unlimited
	assert.False(t, ok)

	val, ok := parseCgroupLimitBytes("3355443200\n") // 3200 MiB
	require.True(t, ok)
	assert.Equal(t, uint64(3355443200), val)
}

func TestResolveEffectiveMemoryMB_SystemdCgroupV2Hierarchy(t *testing.T) {
	tmp := t.TempDir()
	meminfoPath := filepath.Join(tmp, "meminfo")
	procCgroupPath := filepath.Join(tmp, "cgroup")
	cgRoot := filepath.Join(tmp, "sys_fs_cgroup")

	// Host has 16 GB physical RAM, service cgroup sets MemoryMax=3200M, parent slice sets 6400M.
	require.NoError(t, os.WriteFile(meminfoPath, []byte("MemTotal:       16777216 kB\n"), 0o600))
	require.NoError(t, os.WriteFile(procCgroupPath, []byte("0::/system.slice/metanode-exec.service\n"), 0o600))

	serviceDir := filepath.Join(cgRoot, "system.slice", "metanode-exec.service")
	require.NoError(t, os.MkdirAll(serviceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cgRoot, "system.slice", "memory.max"), []byte("6710886400\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(serviceDir, "memory.max"), []byte("3355443200\n"), 0o600))

	got := resolveEffectiveMemoryMB(meminfoPath, procCgroupPath, cgRoot)
	assert.Equal(t, uint64(3200), got, "must use systemd slice cgroup memory.max (3200 MB) instead of host MemTotal")

	// When service sets memory.max to "max", parent slice limit still bounds it.
	require.NoError(t, os.WriteFile(filepath.Join(serviceDir, "memory.max"), []byte("max\n"), 0o600))
	got = resolveEffectiveMemoryMB(meminfoPath, procCgroupPath, cgRoot)
	assert.Equal(t, uint64(6400), got, "must walk parent slice cgroup limits when leaf is 'max'")

	// When both leaf and parent are "max", falls back to MemTotal.
	require.NoError(t, os.WriteFile(filepath.Join(cgRoot, "system.slice", "memory.max"), []byte("max\n"), 0o600))
	got = resolveEffectiveMemoryMB(meminfoPath, procCgroupPath, cgRoot)
	assert.Equal(t, uint64(16384), got)
}

func TestResolveEffectiveMemoryMB_CgroupV1Fallback(t *testing.T) {
	tmp := t.TempDir()
	meminfoPath := filepath.Join(tmp, "meminfo")
	procCgroupPath := filepath.Join(tmp, "cgroup")
	cgRoot := filepath.Join(tmp, "sys_fs_cgroup")

	require.NoError(t, os.WriteFile(meminfoPath, []byte("MemTotal:       67108864 kB\n"), 0o600))
	require.NoError(t, os.WriteFile(procCgroupPath, []byte("8:memory:/docker/abc123\n"), 0o600))

	memDir := filepath.Join(cgRoot, "memory", "docker", "abc123")
	require.NoError(t, os.MkdirAll(memDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(memDir, "memory.limit_in_bytes"), []byte("4294967296\n"), 0o600))

	got := resolveEffectiveMemoryMB(meminfoPath, procCgroupPath, cgRoot)
	assert.Equal(t, uint64(4096), got)
}

func TestClassifyAndResolveResourceDefaults(t *testing.T) {
	t.Run("TinyHost_4GB_2CPU", func(t *testing.T) {
		d := ResolveResourceDefaults(3900, 2, "")
		assert.Equal(t, ProfileTiny, d.Profile)
		assert.True(t, d.IsSmallHost)
		assert.Equal(t, 1024, d.GoMemLimitMB)
		assert.Equal(t, 1, d.GoMemLimitGB)
		assert.Equal(t, 100, d.GoGCPercent)
		assert.Equal(t, 128, d.PebbleCacheSizeMB)
		assert.Equal(t, 16, d.PebbleMemTableSizeMB)
		assert.Equal(t, 64, d.NomtPageCacheMB)
		assert.Equal(t, 64, d.NomtLeafCacheMB)
		assert.Equal(t, 100_000, d.TrieNodeCacheEntries)
		assert.Equal(t, 200_000, d.SignatureCacheEntries)
		assert.Equal(t, 2048, d.ExplorerQueueSize)
		assert.Equal(t, 4, d.ExplorerWorkerCount)
		assert.Equal(t, 4, d.EthCallConcurrency)
	})

	t.Run("TinyHost_1CPU_ExplorerWorkerAtLeast1", func(t *testing.T) {
		d := ResolveResourceDefaults(3200, 1, "")
		assert.Equal(t, ProfileTiny, d.Profile)
		assert.Equal(t, 2, d.ExplorerWorkerCount)
		assert.Equal(t, 1024, d.GoMemLimitMB)
	})

	t.Run("SmallHost_8GB_4CPU", func(t *testing.T) {
		d := ResolveResourceDefaults(8*1024, 4, "")
		assert.Equal(t, ProfileSmall, d.Profile)
		assert.False(t, d.IsSmallHost)
		assert.Equal(t, 2457, d.GoMemLimitMB)
		assert.Equal(t, 200, d.GoGCPercent)
		assert.Equal(t, 512, d.PebbleCacheSizeMB)
		assert.Equal(t, 64, d.PebbleMemTableSizeMB)
		assert.Equal(t, 128, d.NomtPageCacheMB)
		assert.Equal(t, 128, d.NomtLeafCacheMB)
		assert.Equal(t, 500_000, d.TrieNodeCacheEntries)
		assert.Equal(t, 500_000, d.SignatureCacheEntries)
		assert.Equal(t, 8192, d.ExplorerQueueSize)
		assert.Equal(t, 4, d.ExplorerWorkerCount)
		assert.Equal(t, 8, d.EthCallConcurrency)
	})

	t.Run("MediumHost_32GB_16CPU", func(t *testing.T) {
		d := ResolveResourceDefaults(32*1024, 16, "")
		assert.Equal(t, ProfileMedium, d.Profile)
		assert.Equal(t, 8192, d.GoMemLimitMB) // capped at 8GB
		assert.Equal(t, 400, d.GoGCPercent)
		assert.Equal(t, 2048, d.PebbleCacheSizeMB)
		assert.Equal(t, 128, d.PebbleMemTableSizeMB)
		assert.Equal(t, 1_000_000, d.TrieNodeCacheEntries)
		assert.Equal(t, 1_000_000, d.SignatureCacheEntries)
		assert.Equal(t, 32768, d.ExplorerQueueSize)
		assert.Equal(t, 16, d.ExplorerWorkerCount)
		assert.Equal(t, 32, d.EthCallConcurrency)
	})

	t.Run("LargeHost_157GB_104CPU_MatchesLegacyDefaults", func(t *testing.T) {
		d := ResolveResourceDefaults(157*1024, 104, "")
		assert.Equal(t, ProfileLarge, d.Profile)
		assert.Equal(t, 8192, d.GoMemLimitMB)
		assert.Equal(t, 8, d.GoMemLimitGB)
		assert.Equal(t, 800, d.GoGCPercent)
		assert.Equal(t, 4096, d.PebbleCacheSizeMB)
		assert.Equal(t, 256, d.PebbleMemTableSizeMB)
		assert.Equal(t, 128, d.NomtPageCacheMB)
		assert.Equal(t, 128, d.NomtLeafCacheMB)
		assert.Equal(t, 2_000_000, d.TrieNodeCacheEntries)
		assert.Equal(t, 2_500_000, d.SignatureCacheEntries)
		assert.Equal(t, 65536, d.ExplorerQueueSize)
		assert.Equal(t, 32, d.ExplorerWorkerCount)
		assert.Equal(t, 100, d.EthCallConcurrency) // legacy off-chain limiter on large hosts
	})

	t.Run("ExplicitProfileOverrideBeatsDetectedRAM", func(t *testing.T) {
		d := ResolveResourceDefaults(157*1024, 32, "tiny")
		assert.Equal(t, ProfileTiny, d.Profile)
		assert.Equal(t, 128, d.PebbleCacheSizeMB)
		assert.Equal(t, 16, d.PebbleMemTableSizeMB)
		assert.Equal(t, 2048, d.ExplorerQueueSize)
		assert.Equal(t, 4, d.ExplorerWorkerCount)
		assert.Equal(t, 2048, d.GoMemLimitMB) // capped: 25% of 157 GB must not exceed the tiny ceiling

		dLarge := ResolveResourceDefaults(3200, 2, "LARGE")
		assert.Equal(t, ProfileLarge, dLarge.Profile)
		assert.Equal(t, 4096, dLarge.PebbleCacheSizeMB)
		assert.Equal(t, 800, dLarge.GoGCPercent)
	})
}

func TestParseResourceProfile(t *testing.T) {
	for _, valid := range []string{"", "tiny", "SMALL", " Medium ", "large"} {
		_, err := ParseResourceProfile(valid)
		assert.NoError(t, err)
	}
	_, err := ParseResourceProfile("huge")
	assert.Error(t, err)
}

func TestClassifyResourceProfile_BoundariesLeaveHeadroom(t *testing.T) {
	// MemTotal reported by the kernel is a few percent below the nominal RAM size.
	assert.Equal(t, ProfileLarge, ClassifyResourceProfile(62*1024, ""), "64 GB machine (~62 GiB MemTotal) must stay large")
	assert.Equal(t, ProfileMedium, ClassifyResourceProfile(15*1024+600, ""), "16 GB machine (~15.6 GiB) must be medium")
	assert.Equal(t, ProfileSmall, ClassifyResourceProfile(5800, ""), "6 GB machine (~5.8 GiB) must be small")
	assert.Equal(t, ProfileTiny, ClassifyResourceProfile(3917, ""), "4 GB VPS must be tiny")
	assert.Equal(t, ProfileTiny, ClassifyResourceProfile(3200, ""), "MemoryMax=3200M must be tiny")
}

func TestResolveResourceDefaults_SmallOverrideOnBigHostIsCapped(t *testing.T) {
	d := ResolveResourceDefaults(193*1024, 72, "small")
	assert.Equal(t, ProfileSmall, d.Profile)
	assert.Equal(t, 4096, d.GoMemLimitMB)
	assert.LessOrEqual(t, d.GoMemLimitMB, ResolveResourceDefaults(193*1024, 72, "large").GoMemLimitMB)
}
