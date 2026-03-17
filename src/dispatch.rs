use anyhow::Result;
use chrono::Local;

use crate::github;
use crate::k8s;

pub async fn run() -> Result<()> {
    let max_slots: u8 = std::env::var("MAX_SLOTS")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(3);
    let template_path = std::env::var("JOB_TEMPLATE")
        .unwrap_or_else(|_| "/etc/dispatcher/job-template.yaml".into());

    let now = Local::now().format("%Y-%m-%d %H:%M:%S %Z");
    println!("[dispatcher] {now} starting scan");

    let eligible = github::get_eligible_issues().await?;
    if eligible.is_empty() {
        println!("[dispatcher] no eligible issues found");
        return Ok(());
    }

    let nums: Vec<String> = eligible.iter().map(|i| i.number.to_string()).collect();
    println!("[dispatcher] eligible issues: {}", nums.join(" "));

    let client = k8s::new_client().await?;
    let mut active_slots = k8s::get_active_slots(&client).await?;
    println!(
        "[dispatcher] active jobs: {}, slots in use: {}",
        active_slots.len(),
        active_slots.iter().map(|s| s.to_string()).collect::<Vec<_>>().join(" ")
    );

    let template = std::fs::read_to_string(&template_path)
        .map_err(|e| anyhow::anyhow!("cannot read template {template_path}: {e}"))?;

    let mut created = 0u32;
    for issue in &eligible {
        if active_slots.len() >= max_slots as usize {
            println!("[dispatcher] at max capacity ({max_slots}), stopping");
            break;
        }

        let slot = (1..=max_slots).find(|s| !active_slots.contains(s));
        let Some(slot) = slot else {
            println!("[dispatcher] no free slots available");
            break;
        };

        println!(
            "[dispatcher] creating job for issue {} in slot {slot}",
            issue.number
        );
        match k8s::create_job_from_template(&client, &template, issue.number, slot).await {
            Ok(name) => println!("  job.batch/{name} created"),
            Err(e) => println!("  ERROR: {e}"),
        }

        active_slots.push(slot);
        created += 1;
    }

    let now = Local::now().format("%Y-%m-%d %H:%M:%S %Z");
    println!("[dispatcher] {now} scan complete -- created {created} jobs");
    Ok(())
}
