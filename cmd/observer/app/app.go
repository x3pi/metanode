package app

import (
	c_config "github.com/meta-node-blockchain/meta-node/cmd/observer/client-tcp/config"
)

type App struct {
	// Configurations
	Cfg *c_config.ClientConfig
}

func New(cfg *c_config.ClientConfig) (*App, error) {
	return &App{
		Cfg: cfg,
	}, nil
}
