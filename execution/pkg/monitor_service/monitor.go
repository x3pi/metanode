package monitor_service

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	p_common "github.com/meta-node-blockchain/meta-node/pkg/common"
	"github.com/meta-node-blockchain/meta-node/pkg/logger"
	"github.com/meta-node-blockchain/meta-node/pkg/network"
	"github.com/meta-node-blockchain/meta-node/pkg/script"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
)

var (
	MEMORY_USED     = `free | grep Mem | awk '{printf "%.0f", $3/$2 * 100.0}'`
	DISK_USED       = `df -h / | awk 'NR==2 {print $5}'`
	CPU_USED        = `top -bn1 | grep 'Cpu(s)' | awk '{printf "%.0f", $2 + $4}'`
	OUTPUT_LOG_SIZE = `du -b output.log`
	ERROR_LOG_SIZE  = `du -b error.log`
	SERVICE_UPTIME  = `systemctl show %s -p ActiveEnterTimestamp | cut -d'=' -f2 | xargs -I {} date -d "{}" +%%s`
)

type SystemInfo struct {
	IP            string
	ServiceName   string
	ServiceUptime int64
	MemoryUsed    float64
	DiskUsed      float64
	CPUUsed       float64
	OutputLogSize int64
	ErrorLogSize  int64
	ErrorString   []string
}
type MonitorService struct {
	sync.RWMutex

	messageSender t_network.MessageSender
	monitorConn   t_network.Connection
	serviceName   string
	delayTime     time.Duration
}

func NewMonitorService(
	messageSender t_network.MessageSender,
	monitorAddress string,
	dnsLink string,
	serviceName string,
	delayTime time.Duration,
) *MonitorService {
	monitorConn := network.NewConnection(
		common.HexToAddress(monitorAddress),
		"",
		nil,
	)

	return &MonitorService{
		messageSender: messageSender,
		monitorConn:   monitorConn,
		serviceName:   serviceName,
		delayTime:     delayTime,
	}
}

func (m *MonitorService) Run() {
	for {
		go func() {
			start := time.Now()
			systemInfo := SystemInfo{
				ServiceName: m.serviceName,
			}

			output, err := script.ExecuteScript(fmt.Sprintf(SERVICE_UPTIME, systemInfo.ServiceName), []string{})
			if err != nil {
				systemInfo.ErrorString = append(systemInfo.ErrorString, output, err.Error())
			}

			systemInfo.ServiceUptime, _ = strconv.ParseInt(strings.TrimSpace(output), 10, 64)

			output, err = script.ExecuteScript(MEMORY_USED, []string{})
			if err != nil {
				systemInfo.ErrorString = append(systemInfo.ErrorString, output, err.Error())
			}
			systemInfo.MemoryUsed, _ = strconv.ParseFloat(strings.TrimSpace(output), 64)

			output, err = script.ExecuteScript(MEMORY_USED, []string{})
			if err != nil {
				systemInfo.ErrorString = append(systemInfo.ErrorString, output, err.Error())
			}
			systemInfo.MemoryUsed, _ = strconv.ParseFloat(strings.TrimSpace(output), 64)

			output, err = script.ExecuteScript(CPU_USED, []string{})
			if err != nil {
				systemInfo.ErrorString = append(systemInfo.ErrorString, output, err.Error())
			}
			systemInfo.CPUUsed, _ = strconv.ParseFloat(strings.TrimSpace(output), 64)

			output, err = script.ExecuteScript(DISK_USED, []string{})
			if err != nil {
				systemInfo.ErrorString = append(systemInfo.ErrorString, output, err.Error())
			}
			systemInfo.DiskUsed, _ = strconv.ParseFloat(strings.Replace(strings.TrimSpace(output), "%", "", -1), 64)

			data, err := json.Marshal(systemInfo)
			if err != nil {
				logger.Warn("Error marshaling system info", err)
				return
			}

			// New conn when send monitor data
			conn := m.monitorConn.Clone()
			err = conn.Connect()
			if err != nil {
				logger.Error("Not connect with monitor server")
				return
			}

			defer conn.Disconnect()
			err = m.messageSender.SendBytes(conn, p_common.MonitorData, data)
			if err != nil {
				logger.Error("Send msg error ", err)
			}
			logger.DebugP(systemInfo, time.Since(start))
		}()
		<-time.After(m.delayTime * time.Second)
	}
}
