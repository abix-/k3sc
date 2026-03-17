use anyhow::Result;
use chrono::Utc;
use futures::{AsyncBufReadExt, StreamExt};
use k8s_openapi::api::batch::v1::Job;
use k8s_openapi::api::core::v1::{Node, Pod};
use kube::api::{Api, ListParams, LogParams, PostParams};
use kube::Client;

use crate::types::{AgentPod, PodPhase, NAMESPACE};

pub async fn new_client() -> Result<Client> {
    Ok(Client::try_default().await?)
}

pub async fn get_agent_pods(client: &Client) -> Result<Vec<AgentPod>> {
    let pods: Api<Pod> = Api::namespaced(client.clone(), NAMESPACE);
    let lp = ListParams::default().labels("app=claude-agent");
    let list = pods.list(&lp).await?;

    let mut result = Vec::new();
    for pod in list {
        let meta = &pod.metadata;
        let labels = meta.labels.as_ref();
        let status = pod.status.as_ref();

        let issue: u32 = labels
            .and_then(|l| l.get("issue-number"))
            .and_then(|v| v.parse().ok())
            .unwrap_or(0);
        let slot: u8 = labels
            .and_then(|l| l.get("agent-slot"))
            .and_then(|v| v.parse().ok())
            .unwrap_or(0);
        let phase = status
            .and_then(|s| s.phase.as_deref())
            .map(PodPhase::from_str)
            .unwrap_or(PodPhase::Unknown);
        let started = status.and_then(|s| s.start_time.as_ref()).map(|t| t.0);
        let finished = status
            .and_then(|s| s.container_statuses.as_ref())
            .and_then(|cs| cs.first())
            .and_then(|c| c.state.as_ref())
            .and_then(|s| s.terminated.as_ref())
            .and_then(|t| t.finished_at.as_ref())
            .map(|t| t.0);

        result.push(AgentPod {
            name: meta.name.clone().unwrap_or_default(),
            issue,
            slot,
            phase,
            started,
            finished,
            log_tail: String::new(),
        });
    }
    Ok(result)
}

pub async fn get_active_slots(client: &Client) -> Result<Vec<u8>> {
    let jobs: Api<Job> = Api::namespaced(client.clone(), NAMESPACE);
    let lp = ListParams::default().labels("app=claude-agent");
    let list = jobs.list(&lp).await?;

    let mut slots = Vec::new();
    for job in list {
        let active = job.status.as_ref().and_then(|s| s.active).unwrap_or(0);
        if active > 0 {
            if let Some(slot) = job
                .metadata
                .labels
                .as_ref()
                .and_then(|l| l.get("agent-slot"))
                .and_then(|v| v.parse::<u8>().ok())
            {
                slots.push(slot);
            }
        }
    }
    Ok(slots)
}

pub async fn get_pod_log_tail(client: &Client, pod_name: &str, lines: i64) -> Result<String> {
    let pods: Api<Pod> = Api::namespaced(client.clone(), NAMESPACE);
    let lp = LogParams {
        tail_lines: Some(lines),
        ..Default::default()
    };
    let log = pods.logs(pod_name, &lp).await.unwrap_or_default();

    // filter to last meaningful line
    let meaningful: Vec<&str> = log
        .lines()
        .filter(|l| {
            let t = l.trim();
            !t.is_empty()
                && !t.starts_with("[entrypoint]")
                && !t.starts_with("[tool]")
                && !t.starts_with("[result]")
                && !t.ends_with("/10")
        })
        .collect();

    Ok(meaningful.last().map(|s| {
        let s = *s;
        if s.len() > 80 { format!("{}...", &s[..77]) } else { s.to_string() }
    }).unwrap_or_default())
}

pub async fn get_full_log(client: &Client, pod_name: &str) -> Result<String> {
    let pods: Api<Pod> = Api::namespaced(client.clone(), NAMESPACE);
    let lp = LogParams::default();
    Ok(pods.logs(pod_name, &lp).await.unwrap_or_default())
}

pub async fn follow_log(client: &Client, pod_name: &str) -> Result<()> {
    let pods: Api<Pod> = Api::namespaced(client.clone(), NAMESPACE);
    let lp = LogParams {
        follow: true,
        ..Default::default()
    };
    let mut stream = pods.log_stream(pod_name, &lp).await?.lines();
    while let Some(line) = stream.next().await {
        match line {
            Ok(l) => println!("{}", l),
            Err(_) => break,
        }
    }
    Ok(())
}

pub async fn find_pod_for_issue(client: &Client, issue: u32) -> Result<Option<String>> {
    let pods: Api<Pod> = Api::namespaced(client.clone(), NAMESPACE);
    let lp = ListParams::default().labels(&format!("issue-number={issue}"));
    let list = pods.list(&lp).await?;

    // return most recent pod
    let mut items: Vec<_> = list.into_iter().collect();
    items.sort_by(|a, b| {
        let ta = a.metadata.creation_timestamp.as_ref();
        let tb = b.metadata.creation_timestamp.as_ref();
        ta.cmp(&tb)
    });
    Ok(items.last().and_then(|p| p.metadata.name.clone()))
}

pub async fn create_job_from_template(
    client: &Client,
    template: &str,
    issue: u32,
    slot: u8,
) -> Result<String> {
    let timestamp = Utc::now().timestamp();
    let manifest = template
        .replace("__ISSUE_NUMBER__", &issue.to_string())
        .replace("__AGENT_SLOT__", &slot.to_string())
        .replace(
            &format!("name: \"claude-issue-{issue}\""),
            &format!("name: \"claude-issue-{issue}-{timestamp}\""),
        );

    let job: Job = serde_json::from_str(
        &serde_yaml::to_string(&serde_yaml::from_str::<serde_yaml::Value>(&manifest)?)?
            .replace("---\n", ""),
    )
    .map_err(|e| anyhow::anyhow!("bad job manifest: {e}"))?;

    let jobs: Api<Job> = Api::namespaced(client.clone(), NAMESPACE);
    let created = jobs.create(&PostParams::default(), &job).await?;
    Ok(created.metadata.name.unwrap_or_default())
}

pub async fn get_node_info(client: &Client) -> Result<(String, String)> {
    let nodes: Api<Node> = Api::all(client.clone());
    let list = nodes.list(&ListParams::default()).await?;
    if let Some(node) = list.into_iter().next() {
        let name = node.metadata.name.unwrap_or_default();
        let version = node
            .status
            .as_ref()
            .and_then(|s| s.node_info.as_ref())
            .map(|i| i.kubelet_version.clone())
            .unwrap_or_default();
        Ok((name, version))
    } else {
        Ok(("unknown".into(), "unknown".into()))
    }
}
