package utils

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// ResourceProfileName identifies a memory/cache sizing tier for a node.
// All fields derived from a profile are node-local (caches, GC, explorer queues,
// off-chain concurrency) and never affect EVM execution or consensus state roots.
type ResourceProfileName string

const (
	ProfileTiny   ResourceProfileName = "tiny"
	ProfileSmall  ResourceProfileName = "small"
	ProfileMedium ResourceProfileName = "medium"
	ProfileLarge  ResourceProfileName = "large"

	smallHostThresholdMB = 8 * 1024
	// Tier boundaries leave headroom below the nominal size: MemTotal of a 64 GB / 16 GB / 6 GB
	// machine is reported 1-4% lower (kernel/firmware reservations), which must not demote it a tier.
	tinyMaxMemMB   = 5632      // < 5.5 GiB  -> tiny   (4 GB VPS, or a service capped by MemoryMax)
	smallMaxMemMB  = 15 * 1024 // < 15 GiB   -> small  (8 GB class)
	mediumMaxMemMB = 60 * 1024 // < 60 GiB   -> medium (16-32 GB class); >= 60 GiB -> large

	// Upper bounds so an explicit tiny/small override on a big host never yields a larger
	// Go heap limit than the medium/large tiers.
	tinyMaxGoMemLimitMB  = 2048
	smallMaxGoMemLimitMB = 4096

	// legacyLargeEthCallConcurrency keeps the pre-profile off-chain limiter size on large hosts.
	legacyLargeEthCallConcurrency = 100
)

// ResourceDefaults holds resolved memory and cache defaults for a single node.
type ResourceDefaults struct {
	Profile               ResourceProfileName
	EffectiveMemMB        uint64
	NumCPU                int
	IsSmallHost           bool
	GoMemLimitMB          int
	GoMemLimitGB          int
	GoGCPercent           int
	PebbleCacheSizeMB     int
	PebbleMemTableSizeMB  int
	NomtPageCacheMB       int
	NomtLeafCacheMB       int
	TrieNodeCacheEntries  int
	SignatureCacheEntries int
	ExplorerQueueSize     int
	ExplorerWorkerCount   int
	EthCallConcurrency    int
}

var (
	activeProfileMu   sync.RWMutex
	activeProfileName string
)

// SetConfiguredResourceProfile records the active node's configured resource_profile
// (empty string = auto-detect from cgroup/RAM) so packages without a config dependency
// (pkg/trie, tx_processor) size their internal caches consistently.
func SetConfiguredResourceProfile(profile string) {
	activeProfileMu.Lock()
	activeProfileName = strings.ToLower(strings.TrimSpace(profile))
	activeProfileMu.Unlock()
}

// GetConfiguredResourceProfile returns the currently configured resource_profile override.
func GetConfiguredResourceProfile() string {
	activeProfileMu.RLock()
	p := activeProfileName
	activeProfileMu.RUnlock()
	return p
}

// ActiveResourceDefaults resolves the active node's resource defaults from the
// configured profile (or META_RESOURCE_PROFILE env var) and effective host/cgroup memory.
func ActiveResourceDefaults() ResourceDefaults {
	override := GetConfiguredResourceProfile()
	if env := strings.TrimSpace(os.Getenv("META_RESOURCE_PROFILE")); env != "" {
		override = env
	}
	return ResolveResourceDefaults(EffectiveMemoryMB(), runtime.NumCPU(), override)
}

// EffectiveMemoryMB returns the smaller of physical MemTotal (/proc/meminfo) and any
// finite cgroup memory limit (v2 memory.max walking /proc/self/cgroup to root, or v1
// memory.limit_in_bytes). Returns 0 only if neither source can be read.
func EffectiveMemoryMB() uint64 {
	// Host RAM and the service cgroup limit are fixed for the process lifetime; read them once so
	// hot paths (per-signature cache cap) do not pay several /proc + cgroup file reads per call.
	effectiveMemOnce.Do(func() {
		effectiveMemMB = resolveEffectiveMemoryMB("/proc/meminfo", "/proc/self/cgroup", "/sys/fs/cgroup")
	})
	return effectiveMemMB
}

var (
	effectiveMemOnce sync.Once
	effectiveMemMB   uint64
)

// IsSmallHost reports whether the effective memory available to this process is under 8 GB.
func IsSmallHost() bool {
	memMB := EffectiveMemoryMB()
	return memMB > 0 && memMB < smallHostThresholdMB
}

// ParseResourceProfile validates and normalizes a resource_profile string.
// Empty string is valid and means auto-detect.
func ParseResourceProfile(raw string) (ResourceProfileName, error) {
	norm := strings.ToLower(strings.TrimSpace(raw))
	switch ResourceProfileName(norm) {
	case "", ProfileTiny, ProfileSmall, ProfileMedium, ProfileLarge:
		return ResourceProfileName(norm), nil
	default:
		return "", fmt.Errorf("invalid resource_profile %q: must be one of tiny, small, medium, large", raw)
	}
}

// ClassifyResourceProfile picks a profile tier from explicit override or effective memory in MiB.
// If override is empty and memMB == 0 (unable to detect), defaults to ProfileLarge for backward compatibility.
func ClassifyResourceProfile(memMB uint64, override string) ResourceProfileName {
	if parsed, err := ParseResourceProfile(override); err == nil && parsed != "" {
		return parsed
	}
	if memMB == 0 {
		return ProfileLarge
	}
	switch {
	case memMB < tinyMaxMemMB:
		return ProfileTiny
	case memMB < smallMaxMemMB:
		return ProfileSmall
	case memMB < mediumMaxMemMB:
		return ProfileMedium
	default:
		return ProfileLarge
	}
}

// ResolveResourceDefaults computes node-local cache/GC/worker defaults for the given
// memory size (MiB), CPU count, and optional profile override.
func ResolveResourceDefaults(memMB uint64, numCPU int, override string) ResourceDefaults {
	if numCPU <= 0 {
		numCPU = 1
	}
	profile := ClassifyResourceProfile(memMB, override)

	ethCallConc := 2 * numCPU
	if ethCallConc < 4 {
		ethCallConc = 4
	}
	if ethCallConc > 64 {
		ethCallConc = 64
	}

	d := ResourceDefaults{
		Profile:            profile,
		EffectiveMemMB:     memMB,
		NumCPU:             numCPU,
		IsSmallHost:        memMB > 0 && memMB < smallHostThresholdMB,
		EthCallConcurrency: ethCallConc,
	}

	// If explicit profile override is used without memMB (e.g. unit tests), provide a representative baseline.
	calcMemMB := memMB
	switch profile {
	case ProfileTiny:
		if calcMemMB == 0 {
			calcMemMB = 4 * 1024
		}
		goMemMB := int(calcMemMB * 25 / 100)
		if goMemMB > tinyMaxGoMemLimitMB {
			goMemMB = tinyMaxGoMemLimitMB
		}
		if goMemMB < 1024 {
			goMemMB = 1024
		}
		// Explorer indexing is mostly Xapian I/O; 2 workers per CPU (max 4) keeps the 2048-slot
		// queue draining so index jobs are not dropped after the 50ms enqueue wait.
		expWorkers := 2 * numCPU
		if expWorkers > 4 {
			expWorkers = 4
		}
		d.GoMemLimitMB = goMemMB
		d.GoMemLimitGB = maxInt(1, goMemMB/1024)
		d.GoGCPercent = 100
		d.PebbleCacheSizeMB = 128
		d.PebbleMemTableSizeMB = 16
		d.NomtPageCacheMB = 64
		d.NomtLeafCacheMB = 64
		d.TrieNodeCacheEntries = 100_000
		d.SignatureCacheEntries = 200_000
		d.ExplorerQueueSize = 2048
		d.ExplorerWorkerCount = expWorkers

	case ProfileSmall:
		if calcMemMB == 0 {
			calcMemMB = 8 * 1024
		}
		goMemMB := int(calcMemMB * 30 / 100)
		if goMemMB > smallMaxGoMemLimitMB {
			goMemMB = smallMaxGoMemLimitMB
		}
		if goMemMB < 1536 {
			goMemMB = 1536
		}
		d.GoMemLimitMB = goMemMB
		d.GoMemLimitGB = maxInt(1, (goMemMB+512)/1024)
		d.GoGCPercent = 200
		d.PebbleCacheSizeMB = 512
		d.PebbleMemTableSizeMB = 64
		d.NomtPageCacheMB = 128
		d.NomtLeafCacheMB = 128
		d.TrieNodeCacheEntries = 500_000
		d.SignatureCacheEntries = 500_000
		d.ExplorerQueueSize = 8192
		d.ExplorerWorkerCount = 4

	case ProfileMedium:
		if calcMemMB == 0 {
			calcMemMB = 32 * 1024
		}
		goMemMB := int(calcMemMB * 35 / 100)
		if goMemMB > 8192 {
			goMemMB = 8192
		}
		if goMemMB < 2048 {
			goMemMB = 2048
		}
		d.GoMemLimitMB = goMemMB
		d.GoMemLimitGB = maxInt(1, (goMemMB+512)/1024)
		d.GoGCPercent = 400
		d.PebbleCacheSizeMB = 2048
		d.PebbleMemTableSizeMB = 128
		d.NomtPageCacheMB = 128
		d.NomtLeafCacheMB = 128
		d.TrieNodeCacheEntries = 1_000_000
		d.SignatureCacheEntries = 1_000_000
		d.ExplorerQueueSize = 32768
		d.ExplorerWorkerCount = 16

	default: // ProfileLarge — identical to legacy high-RAM defaults
		d.Profile = ProfileLarge
		d.GoMemLimitMB = 8192
		d.GoMemLimitGB = 8
		d.GoGCPercent = 800
		d.PebbleCacheSizeMB = 4096
		d.PebbleMemTableSizeMB = 256
		d.NomtPageCacheMB = 128
		d.NomtLeafCacheMB = 128
		d.TrieNodeCacheEntries = 2_000_000
		d.SignatureCacheEntries = 2_500_000
		d.ExplorerQueueSize = 65536
		d.ExplorerWorkerCount = 32
		d.EthCallConcurrency = legacyLargeEthCallConcurrency
	}

	return d
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func resolveEffectiveMemoryMB(meminfoPath, procCgroupPath, cgroupRoot string) uint64 {
	memTotalMB := readMeminfoTotalMB(meminfoPath)
	cgroupLimitMB := readCgroupMemoryLimitMB(procCgroupPath, cgroupRoot)

	if memTotalMB == 0 {
		return cgroupLimitMB
	}
	if cgroupLimitMB == 0 || memTotalMB < cgroupLimitMB {
		return memTotalMB
	}
	return cgroupLimitMB
}

func readMeminfoTotalMB(path string) uint64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return parseMeminfoTotalMB(string(raw))
}

func parseMeminfoTotalMB(content string) uint64 {
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return kb / 1024
	}
	return 0
}

func readCgroupMemoryLimitMB(procCgroupPath, cgroupRoot string) uint64 {
	raw, err := os.ReadFile(procCgroupPath)
	if err != nil {
		return 0
	}
	paths := parseProcCgroupPaths(string(raw))

	var minBytes uint64 = math.MaxUint64
	found := false

	checkFile := func(p string) {
		b, err := os.ReadFile(p)
		if err != nil {
			return
		}
		if limit, ok := parseCgroupLimitBytes(string(b)); ok && limit > 0 {
			if limit < minBytes {
				minBytes = limit
				found = true
			}
		}
	}

	// Walk cgroup v2 unified hierarchy ("0::/system.slice/foo.service" -> root).
	v2Rel := paths.v2Path
	if v2Rel != "" {
		rel := strings.TrimPrefix(v2Rel, "/")
		for {
			dir := cgroupRoot
			if rel != "" {
				dir = filepath.Join(cgroupRoot, rel)
			}
			checkFile(filepath.Join(dir, "memory.max"))
			if rel == "" || rel == "." {
				break
			}
			parent := filepath.Dir(rel)
			if parent == rel {
				break
			}
			if parent == "." {
				rel = ""
			} else {
				rel = parent
			}
		}
	} else {
		checkFile(filepath.Join(cgroupRoot, "memory.max"))
	}

	// Fallback / also honor cgroup v1 memory controller if present.
	v1Rel := paths.v1MemPath
	v1Base := filepath.Join(cgroupRoot, "memory")
	if v1Rel != "" {
		rel := strings.TrimPrefix(v1Rel, "/")
		for {
			dir := v1Base
			if rel != "" {
				dir = filepath.Join(v1Base, rel)
			}
			checkFile(filepath.Join(dir, "memory.limit_in_bytes"))
			if rel == "" || rel == "." {
				break
			}
			parent := filepath.Dir(rel)
			if parent == rel {
				break
			}
			if parent == "." {
				rel = ""
			} else {
				rel = parent
			}
		}
	} else {
		checkFile(filepath.Join(v1Base, "memory.limit_in_bytes"))
	}

	if !found {
		return 0
	}
	return minBytes / (1024 * 1024)
}

type parsedCgroupPaths struct {
	v2Path    string
	v1MemPath string
}

func parseProcCgroupPaths(content string) parsedCgroupPaths {
	var out parsedCgroupPaths
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		controllers, path := parts[1], parts[2]
		if controllers == "" {
			out.v2Path = path
			continue
		}
		for _, ctrl := range strings.Split(controllers, ",") {
			if ctrl == "memory" {
				out.v1MemPath = path
			}
		}
	}
	return out
}

func parseCgroupLimitBytes(raw string) (uint64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "max" {
		return 0, false
	}
	val, err := strconv.ParseUint(s, 10, 64)
	if err != nil || val == 0 {
		return 0, false
	}
	// Cgroup v1 reports ~math.MaxInt64 (pages aligned, > 1<<50 ~ 1 PiB) when unlimited.
	const unlimitedThreshold = uint64(1) << 50
	if val >= unlimitedThreshold {
		return 0, false
	}
	return val, true
}
