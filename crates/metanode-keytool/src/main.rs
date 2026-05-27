use anyhow::Result;
use clap::Parser;
use metanode_keytool::{Commands, run_keytool};

/// Metanode keytool — generate and inspect cryptographic keys for validators.
#[derive(Parser)]
#[command(name = "metanode-keytool", version, about, long_about = None)]
struct Cli {
    #[command(subcommand)]
    command: Commands,
}

fn main() -> Result<()> {
    let cli = Cli::parse();
    run_keytool(cli.command)
}
