use anyhow::Result;
use chrono::{DateTime, Utc};
use chrono_tz::America::New_York;

use crate::github;
use crate::k8s;
use crate::types::{AgentPod, Issue, PodPhase, SLOT_OFFSET};

pub fn fmt_time(dt: DateTime<Utc>) -> String {
    let est = dt.with_timezone(&New_York);
    est.format("%-I:%M %p EST").to_string()
}

pub fn fmt_duration(start: DateTime<Utc>, end: Option<DateTime<Utc>>) -> String {
    let end = end.unwrap_or_else(Utc::now);
    let secs = (end - start).num_seconds().max(0);
    format!("{}m {:02}s", secs / 60, secs % 60)
}

struct Dashboard {
    node_name: String,
    node_version: String,
    pods: Vec<AgentPod>,
    issues: Vec<Issue>,
}

async fn gather() -> Result<Dashboard> {
    let client = k8s::new_client().await?;

    // parallel fetch
    let (node_info, mut pods, issues) = tokio::try_join!(
        k8s::get_node_info(&client),
        k8s::get_agent_pods(&client),
        github::get_workflow_issues(),
    )?;

    // fetch log tails in parallel
    let mut handles = Vec::new();
    for pod in &pods {
        let c = client.clone();
        let name = pod.name.clone();
        handles.push(tokio::spawn(async move {
            k8s::get_pod_log_tail(&c, &name, 20).await.unwrap_or_default()
        }));
    }
    for (i, handle) in handles.into_iter().enumerate() {
        pods[i].log_tail = handle.await.unwrap_or_default();
    }

    // sort: running first, then by start time
    pods.sort_by(|a, b| a.phase.cmp(&b.phase).then(a.started.cmp(&b.started)));

    Ok(Dashboard {
        node_name: node_info.0,
        node_version: node_info.1,
        pods,
        issues,
    })
}

fn print_dashboard(d: &Dashboard) {
    println!("=== CLUSTER ===");
    println!("Node: {} Ready {}", d.node_name, d.node_version);
    println!();

    let running = d.pods.iter().filter(|p| p.phase == PodPhase::Running).count();
    let completed = d.pods.iter().filter(|p| p.phase == PodPhase::Succeeded).count();
    let failed = d.pods.iter().filter(|p| p.phase == PodPhase::Failed).count();

    println!("=== AGENTS ({running} running, {completed} completed, {failed} failed) ===");
    if d.pods.is_empty() {
        println!("  (no agent pods)");
    } else {
        println!(
            "{:<7} {:<10} {:<11} {:<16} {:<10} Last Output",
            "Issue", "Agent", "Status", "Started", "Duration"
        );
        for pod in &d.pods {
            let agent = format!("claude-{}", pod.slot as u16 + SLOT_OFFSET as u16);
            let started = pod.started.map(fmt_time).unwrap_or_default();
            let duration = pod.started.map(|s| fmt_duration(s, pod.finished)).unwrap_or_default();
            let tail = if pod.log_tail.len() > 50 {
                format!("{}...", &pod.log_tail[..47])
            } else {
                pod.log_tail.clone()
            };
            println!(
                "#{:<6} {:<10} {:<11} {:<16} {:<10} {}",
                pod.issue,
                agent,
                pod.phase.display(),
                started,
                duration,
                tail
            );
        }
    }
    println!();

    println!("=== GITHUB ISSUES ===");
    if d.issues.is_empty() {
        println!("  (no issues with workflow labels)");
    } else {
        println!("{:<7} {:<14} {:<10} Title", "Issue", "State", "Owner");
        for i in &d.issues {
            println!("#{:<6} {:<14} {:<10} {}", i.number, i.state, i.owner, i.title);
        }
    }
    println!();
}

pub async fn run(once: bool) -> Result<()> {
    if once {
        let d = gather().await?;
        print_dashboard(&d);
        return Ok(());
    }

    // TUI mode with ratatui
    run_tui().await
}

async fn run_tui() -> Result<()> {
    use crossterm::event::{self, Event, KeyCode, KeyEventKind};
    use crossterm::terminal::{disable_raw_mode, enable_raw_mode, EnterAlternateScreen, LeaveAlternateScreen};
    use crossterm::execute;
    use ratatui::prelude::*;
    use ratatui::widgets::*;
    use std::io;
    use std::time::Duration;

    enable_raw_mode()?;
    let mut stdout = io::stdout();
    execute!(stdout, EnterAlternateScreen)?;
    let backend = CrosstermBackend::new(stdout);
    let mut terminal = Terminal::new(backend)?;

    let mut dashboard = gather().await?;
    let mut last_refresh = std::time::Instant::now();

    loop {
        // refresh every 5 seconds
        if last_refresh.elapsed() > Duration::from_secs(5) {
            if let Ok(d) = gather().await {
                dashboard = d;
            }
            last_refresh = std::time::Instant::now();
        }

        terminal.draw(|f| {
            let chunks = Layout::default()
                .direction(Direction::Vertical)
                .constraints([
                    Constraint::Length(3),  // cluster
                    Constraint::Min(8),     // agents
                    Constraint::Length(12), // issues
                    Constraint::Length(1),  // help
                ])
                .split(f.area());

            // cluster
            let running = dashboard.pods.iter().filter(|p| p.phase == PodPhase::Running).count();
            let completed = dashboard.pods.iter().filter(|p| p.phase == PodPhase::Succeeded).count();
            let cluster = Paragraph::new(format!(
                " Node: {} {}  |  Agents: {} running, {} completed",
                dashboard.node_name, dashboard.node_version, running, completed
            ))
            .block(Block::default().borders(Borders::ALL).title(" Cluster "));
            f.render_widget(cluster, chunks[0]);

            // agents table
            let header = Row::new(["Issue", "Agent", "Status", "Started", "Duration", "Last Output"])
                .style(Style::default().bold());
            let rows: Vec<Row> = dashboard.pods.iter().map(|pod| {
                let agent = format!("claude-{}", pod.slot as u16 + SLOT_OFFSET as u16);
                let started = pod.started.map(fmt_time).unwrap_or_default();
                let duration = pod.started.map(|s| fmt_duration(s, pod.finished)).unwrap_or_default();
                let tail = if pod.log_tail.len() > 50 {
                    format!("{}...", &pod.log_tail[..47])
                } else {
                    pod.log_tail.clone()
                };
                let style = match pod.phase {
                    PodPhase::Running => Style::default().fg(Color::Green),
                    PodPhase::Failed => Style::default().fg(Color::Red),
                    _ => Style::default().fg(Color::DarkGray),
                };
                Row::new([
                    format!("#{}", pod.issue),
                    agent,
                    pod.phase.display().to_string(),
                    started,
                    duration,
                    tail,
                ]).style(style)
            }).collect();
            let table = Table::new(rows, [
                Constraint::Length(7),
                Constraint::Length(10),
                Constraint::Length(11),
                Constraint::Length(16),
                Constraint::Length(10),
                Constraint::Fill(1),
            ])
            .header(header)
            .block(Block::default().borders(Borders::ALL).title(" Agents "));
            f.render_widget(table, chunks[1]);

            // issues
            let issue_header = Row::new(["Issue", "State", "Owner", "Title"])
                .style(Style::default().bold());
            let issue_rows: Vec<Row> = dashboard.issues.iter().map(|i| {
                let style = match i.state.as_str() {
                    "claimed" => Style::default().fg(Color::Yellow),
                    "needs-human" => Style::default().fg(Color::Magenta),
                    "needs-review" => Style::default().fg(Color::Cyan),
                    "ready" => Style::default().fg(Color::Green),
                    _ => Style::default(),
                };
                Row::new([
                    format!("#{}", i.number),
                    i.state.clone(),
                    i.owner.clone(),
                    i.title.clone(),
                ]).style(style)
            }).collect();
            let issue_table = Table::new(issue_rows, [
                Constraint::Length(7),
                Constraint::Length(14),
                Constraint::Length(10),
                Constraint::Fill(1),
            ])
            .header(issue_header)
            .block(Block::default().borders(Borders::ALL).title(" GitHub Issues "));
            f.render_widget(issue_table, chunks[2]);

            // help bar
            let help = Paragraph::new(" q: quit  |  refreshes every 5s")
                .style(Style::default().fg(Color::DarkGray));
            f.render_widget(help, chunks[3]);
        })?;

        // handle input
        if event::poll(Duration::from_millis(250))? {
            if let Event::Key(key) = event::read()? {
                if key.kind == KeyEventKind::Press && key.code == KeyCode::Char('q') {
                    break;
                }
            }
        }
    }

    disable_raw_mode()?;
    execute!(terminal.backend_mut(), LeaveAlternateScreen)?;
    Ok(())
}
