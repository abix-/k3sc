use anyhow::Result;
use chrono::Utc;
use chrono_tz::America::New_York;

use crate::github;
use crate::k8s;

pub async fn run() -> Result<String> {
    let mut log = Vec::new();

    let max_slots: u8 = std::env::var("MAX_SLOTS")
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(3);
    let template_path = std::env::var("JOB_TEMPLATE")
        .unwrap_or_else(|_| "/etc/dispatcher/job-template.yaml".into());

    let now = Utc::now().with_timezone(&New_York).format("%Y-%m-%d %H:%M:%S %Z");
    log.push(format!("[dispatcher] {now} starting scan"));

    let eligible = github::get_eligible_issues().await?;
    if eligible.is_empty() {
        log.push("[dispatcher] no eligible issues found".into());
        let output = log.join("\n");
        println!("{output}");
        return Ok(output);
    }

    let nums: Vec<String> = eligible.iter().map(|i| i.number.to_string()).collect();
    log.push(format!("[dispatcher] eligible issues: {}", nums.join(" ")));

    let client = k8s::new_client().await?;
    let mut active_slots = k8s::get_active_slots(&client).await?;
    log.push(format!(
        "[dispatcher] active jobs: {}, slots in use: {}",
        active_slots.len(),
        active_slots.iter().map(|s| s.to_string()).collect::<Vec<_>>().join(" ")
    ));

    let template = std::fs::read_to_string(&template_path)
        .map_err(|e| anyhow::anyhow!("cannot read template {template_path}: {e}"))?;

    let mut created = 0u32;
    for issue in &eligible {
        if active_slots.len() >= max_slots as usize {
            log.push(format!("[dispatcher] at max capacity ({max_slots}), stopping"));
            break;
        }

        let slot = (1..=max_slots).find(|s| !active_slots.contains(s));
        let Some(slot) = slot else {
            log.push("[dispatcher] no free slots available".into());
            break;
        };

        log.push(format!(
            "[dispatcher] creating job for issue {} in slot {slot}",
            issue.number
        ));
        match k8s::create_job_from_template(&client, &template, issue.number, slot).await {
            Ok(name) => log.push(format!("  job.batch/{name} created")),
            Err(e) => log.push(format!("  ERROR: {e}")),
        }

        active_slots.push(slot);
        created += 1;
    }

    let now = Utc::now().with_timezone(&New_York).format("%Y-%m-%d %H:%M:%S %Z");
    log.push(format!("[dispatcher] {now} scan complete -- created {created} jobs"));

    let output = log.join("\n");
    println!("{output}");
    Ok(output)
}
