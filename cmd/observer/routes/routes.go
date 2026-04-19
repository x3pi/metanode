package routes

import (
	"github.com/meta-node-blockchain/meta-node/cmd/observer/processor"
	t_network "github.com/meta-node-blockchain/meta-node/types/network"
)

// ProcessInitConnection handles connection initialization

func InitRoutes(
	routes map[string]func(t_network.Request) error,
	limits map[string]int,
	supervisorProcessor *processor.SupervisorProcessor,
) {
	// Register the verify transaction route
	// routes[command.VerifyTransaction] = supervisorProcessor.ProcessVerifyTransaction
	// routes[pkg_com.SendContractCrossChain] = supervisorProcessor.SendContractCrossChain
}
