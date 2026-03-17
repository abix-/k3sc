use anyhow::Result;

use crate::k8s;
use crate::types::SLOT_OFFSET;
use crate::top::fmt_time;

pub async fn run(issue: Option<u32>, follow: bool) -> Result<()> {
    let client = k8s::new_client().await?;

    if let Some(issue) = issue {
        let pod_name = k8s::find_pod_for_issue(&client, issue).await?;
        let Some(pod_name) = pod_name else {
            println!("No pods found for issue #{issue}");
            return Ok(());
        };

        if follow {
            println!("Following logs for issue #{issue} (pod {pod_name})...");
            k8s::follow_log(&client, &pod_name).await?;
        } else {
            let log = k8s::get_full_log(&client, &pod_name).await?;
            print!("{log}");
        }
    } else {
        // summary
        let mut pods = k8s::get_agent_pods(&client).await?;
        pods.sort_by(|a, b| a.phase.cmp(&b.phase).then(a.started.cmp(&b.started)));

        if pods.is_empty() {
            println!("No agent pods found.");
            return Ok(());
        }

        println!(
            "{:<7} {:<10} {:<11} {:<16} Last Output",
            "Issue", "Agent", "Status", "Started"
        );
        for pod in &pods {
            let agent = format!("claude-{}", pod.slot as u16 + SLOT_OFFSET as u16);
            let started = pod.started.map(fmt_time).unwrap_or_default();
            let tail = k8s::get_pod_log_tail(&client, &pod.name, 20).await.unwrap_or_default();
            println!(
                "#{:<6} {:<10} {:<11} {:<16} {}",
                pod.issue, agent, pod.phase.display(), started, tail
            );
        }
    }
    Ok(())
}
