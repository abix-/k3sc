use chrono::{DateTime, Utc};

#[derive(Debug, Clone)]
pub struct AgentPod {
    pub name: String,
    pub issue: u32,
    pub slot: u8,
    pub phase: PodPhase,
    pub started: Option<DateTime<Utc>>,
    pub finished: Option<DateTime<Utc>>,
    pub log_tail: String,
}

#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord)]
pub enum PodPhase {
    Running,
    Pending,
    Succeeded,
    Failed,
    Unknown,
}

impl PodPhase {
    pub fn from_str(s: &str) -> Self {
        match s {
            "Running" => Self::Running,
            "Pending" => Self::Pending,
            "Succeeded" => Self::Succeeded,
            "Failed" => Self::Failed,
            _ => Self::Unknown,
        }
    }

    pub fn display(&self) -> &str {
        match self {
            Self::Running => "Running",
            Self::Pending => "Pending",
            Self::Succeeded => "Completed",
            Self::Failed => "Failed",
            Self::Unknown => "Unknown",
        }
    }
}

#[derive(Debug, Clone)]
pub struct Issue {
    pub number: u32,
    pub title: String,
    pub state: String,
    pub owner: String,
}

pub const NAMESPACE: &str = "claude-agents";
pub const REPO_OWNER: &str = "abix-";
pub const REPO_NAME: &str = "endless";
pub const SLOT_OFFSET: u8 = 5; // k8s slot 1 = claude-6
