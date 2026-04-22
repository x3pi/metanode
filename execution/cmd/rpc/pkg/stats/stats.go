package stats

import (
	"fmt"
	"runtime"
	"time"

	pb "github.com/meta-node-blockchain/meta-node/pkg/proto"
	"github.com/meta-node-blockchain/meta-node/pkg/storage"
	"github.com/meta-node-blockchain/meta-node/types/network"
	"google.golang.org/protobuf/proto"
)

type Stats struct {
	PbStats *pb.Stats
}

func GetStats(
	startTime time.Time,
	levelDbs []*storage.LevelDB,
	connectionManager network.ConnectionsManager) *Stats {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	pbStats := &pb.Stats{
		TotalMemory:   memStats.TotalAlloc,
		HeapMemory:    memStats.HeapAlloc,
		NumGoroutines: int32(runtime.NumGoroutine()),
		Uptime:        uint64(time.Since(startTime).Seconds()),
		Network:       connectionManager.Stats(),
	}
	pbStats.DB = make([]*pb.LevelDBStats, len(levelDbs))
	for i, v := range levelDbs {
		pbStats.DB[i] = v.Stats()
	}
	stats := &Stats{
		PbStats: pbStats,
	}
	return stats
}

func (s *Stats) String() string {
	str := fmt.Sprintf(`
Total memory: %v
Heap memory: %v			
Num go routines: %v
Uptime: %v
Network: 
`,
		s.PbStats.TotalMemory,
		s.PbStats.HeapMemory,
		s.PbStats.NumGoroutines,
		s.PbStats.Uptime,
	)
	for i, v := range s.PbStats.Network.TotalConnectionByType {
		str += fmt.Sprintf("\t%v: %v\n", i, v)
	}
	str += "DB:"
	for _, v := range s.PbStats.DB {
		str += fmt.Sprintf(`	Path %v:
		LevelSizes: %v
		LevelTablesCounts: %v
		LevelRead: %v
		LevelWrite: %v
		LevelDurations: %v
		MemComp: %v
		Level0Comp: %v
		NonLevel0Comp: %v
		SeekComp: %v
		AliveSnapshots: %v
		AliveIterators: %v
		IOWrite: %v
		IORead: %v
		BlockCacheSize: %v
		OpenedTablesCount: %v
`,
			v.Path,
			v.LevelSizes,
			v.LevelTablesCounts,
			v.LevelRead,
			v.LevelWrite,
			v.LevelDurations,
			v.MemComp,
			v.Level0Comp,
			v.NonLevel0Comp,
			v.SeekComp,
			v.AliveSnapshots,
			v.AliveIterators,
			v.IOWrite,
			v.IORead,
			v.BlockCacheSize,
			v.OpenedTablesCount,
		)
	}
	return str
}

func (s *Stats) Unmarshal(b []byte) error {
	s.PbStats = &pb.Stats{}
	return proto.Unmarshal(b, s.PbStats)
}

func (s *Stats) Marshal() ([]byte, error) {
	return proto.Marshal(s.PbStats)
}
