package config

import (
	"encoding/json"
	"fmt"
	"os"
)

// ClusterConfig represents configuration for a single cluster/chain
type ClusterConfig struct {
	NationID          int    `json:"nation_id"`
	ConnectionAddress string `json:"connection_address"`
}

// SupervisorConfig represents the main configuration
type SupervisorConfig struct {
	ConnectionAddress string                   `json:"connection_address"`
	KnownClusters     map[string]ClusterConfig `json:"known_clusters"`
	PKAddmin          string                   `json:"pk_addmin"`
	AddrAddmin        string                   `json:"addr_addmin"`
	LogPath           string                   `json:"log_path"`
	NationID          uint64                   `json:"nation_id"`
}

// LoadConfig loads the configuration from a JSON file
func LoadConfig(configPath string) (*SupervisorConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config SupervisorConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}

	return &config, nil
}
