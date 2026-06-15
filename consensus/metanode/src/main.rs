// Copyright (c) MetaNode Team
// SPDX-License-Identifier: Apache-2.0

use anyhow::Result;
use clap::{Parser, Subcommand};
use std::path::PathBuf;
use tracing::info;

use metanode::config::NodeConfig;
use metanode::node::startup::{InitializedNode, StartupConfig};

#[derive(Parser)]
#[command(name = "metanode")]
#[command(about = "MetaNode Consensus Engine - Multi-node consensus based on Sui Mysticeti")]
struct Cli {
    #[command(subcommand)]
    command: Commands,
}

#[derive(Subcommand)]
enum Commands {
    /// Start a consensus node
    Start {
        /// Path to node configuration file
        #[arg(short, long, default_value = "config/node.toml")]
        config: PathBuf,
    },
    /// Generate node configuration files for multiple nodes
    Generate {
        /// Number of nodes to generate
        #[arg(short, long, default_value = "4")]
        nodes: usize,
        /// Output directory for config files
        #[arg(short, long, default_value = "config")]
        output: PathBuf,
    },
    /// CLI keytool for validator key generation and inspection
    #[command(subcommand)]
    Keytool(metanode_keytool::Commands),
}

#[tokio::main]
async fn main() -> Result<()> {
    let cli = Cli::parse();

    match cli.command {
        Commands::Start { config } => {
            let node_config = NodeConfig::load(&config)?;

            // Initialize tracing from config file
            let log_config = node_config.log.clone().unwrap_or_default();
            let level = log_config.level.clone();

            // Create registry for metrics early so we can pass it to telemetry
            let registry = prometheus::Registry::new();

            let mut telemetry_config = telemetry_subscribers::TelemetryConfig::new()
                .with_log_level(&level)
                .with_prom_registry(&registry);

            if log_config.format == "json" {
                telemetry_config = telemetry_config.with_json();
            }

            if log_config.enable_otlp_tracing {
                std::env::set_var("TRACE_FILTER", "info");
                if let Some(endpoint) = &log_config.otlp_endpoint {
                    std::env::set_var("OTLP_ENDPOINT", endpoint);
                }
            }

            let _guards = telemetry_config.with_env().init();

            info!("Starting MetaNode Consensus Engine...");
            info!("Loading configuration from: {:?}", config);
            info!("Node ID: {}", node_config.node_id);
            info!("Network address: {}", node_config.network_address);

            // Initialize and start the node
            let startup_config = StartupConfig::new(node_config, registry, None);
            let initialized_node = InitializedNode::initialize(startup_config).await?;

            // Run the main event loop
            initialized_node.run_main_loop().await?;
        }
        Commands::Generate { nodes, output } => {
            // Default logger for CLI commands
            tracing_subscriber::fmt()
                .with_env_filter(
                    tracing_subscriber::EnvFilter::try_from_default_env()
                        .unwrap_or_else(|_| "metanode=info".into()),
                )
                .init();

            info!("Generating configuration for {} nodes...", nodes);
            NodeConfig::generate_multiple(nodes, &output).await?;
            info!("Configuration files generated in: {:?}", output);
        }
        Commands::Keytool(cmd) => {
            // Default logger for CLI commands
            tracing_subscriber::fmt()
                .with_env_filter(
                    tracing_subscriber::EnvFilter::try_from_default_env()
                        .unwrap_or_else(|_| "metanode=info".into()),
                )
                .init();

            metanode_keytool::run_keytool(cmd)?;
        }
    }

    Ok(())
}
