use anyhow::Result;
use octocrab::Octocrab;

use crate::types::{Issue, REPO_NAME, REPO_OWNER};

pub async fn get_issues_by_label(label: &str) -> Result<Vec<Issue>> {
    let token = std::env::var("GITHUB_TOKEN")
        .or_else(|_| std::env::var("GH_TOKEN"))
        .unwrap_or_default();

    let octocrab = if token.is_empty() {
        Octocrab::default()
    } else {
        Octocrab::builder()
            .personal_token(token)
            .build()?
    };

    let page = octocrab
        .issues(REPO_OWNER, REPO_NAME)
        .list()
        .labels(&[label.to_string()])
        .state(octocrab::params::State::Open)
        .per_page(50)
        .send()
        .await?;

    let mut issues = Vec::new();
    for item in page {
        let labels: Vec<String> = item.labels.iter().map(|l| l.name.clone()).collect();
        let state = if labels.contains(&"claimed".to_string()) {
            "claimed"
        } else if labels.contains(&"needs-human".to_string()) {
            "needs-human"
        } else if labels.contains(&"needs-review".to_string()) {
            "needs-review"
        } else if labels.contains(&"ready".to_string()) {
            "ready"
        } else {
            ""
        };
        let owner = labels
            .iter()
            .find(|l| l.starts_with("claude-") || l.starts_with("codex-"))
            .cloned()
            .unwrap_or_default();

        issues.push(Issue {
            number: item.number as u32,
            title: item.title.chars().take(60).collect(),
            state: state.to_string(),
            owner,
        });
    }
    issues.sort_by_key(|i| i.number);
    Ok(issues)
}

pub async fn get_workflow_issues() -> Result<Vec<Issue>> {
    let mut all = Vec::new();
    for label in ["needs-review", "needs-human", "claimed", "ready"] {
        let mut issues = get_issues_by_label(label).await.unwrap_or_default();
        // dedup by number
        issues.retain(|i| !all.iter().any(|a: &Issue| a.number == i.number));
        all.extend(issues);
    }
    all.sort_by_key(|i| i.number);
    Ok(all)
}

pub async fn get_eligible_issues() -> Result<Vec<Issue>> {
    // needs-review first (sorted ascending), then ready
    let mut review = get_issues_by_label("needs-review").await.unwrap_or_default();
    let ready = get_issues_by_label("ready").await.unwrap_or_default();

    review.sort_by_key(|i| i.number);
    let mut result = review;
    for i in ready {
        if !result.iter().any(|r| r.number == i.number) {
            result.push(i);
        }
    }
    Ok(result)
}
