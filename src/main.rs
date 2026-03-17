mod deploy;
mod dispatch;
mod github;
mod k8s;
mod logs;
mod top;
mod types;

use clap::{Parser, Subcommand};

#[derive(Parser)]
#[command(name = "k3s-claude", about = "k3s Claude agent management")]
struct Cli {
    #[command(subcommand)]
    command: Commands,
}

#[derive(Subcommand)]
enum Commands {
    /// Find eligible GitHub issues and create k8s Jobs
    Dispatch,
    /// Dashboard of agent pods, GitHub issues, and cluster health
    Top {
        /// Print once and exit (no TUI)
        #[arg(long)]
        once: bool,
    },
    /// View agent pod logs
    Logs {
        /// Issue number (omit for summary)
        issue: Option<u32>,
        /// Follow log output
        #[arg(short, long)]
        follow: bool,
    },
    /// Build image and deploy manifests to k3s
    Deploy,
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    let cli = Cli::parse();
    match cli.command {
        Commands::Dispatch => dispatch::run().await,
        Commands::Top { once } => top::run(once).await,
        Commands::Logs { issue, follow } => logs::run(issue, follow).await,
        Commands::Deploy => deploy::run().await,
    }
}
