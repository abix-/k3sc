use anyhow::Result;
use std::path::Path;
use std::process::Command;

fn run_cmd(desc: &str, cmd: &str) -> Result<()> {
    println!("=== {desc} ===");
    println!("  $ {cmd}");
    let status = Command::new("sh").arg("-c").arg(cmd).status()?;
    if !status.success() {
        anyhow::bail!("command failed with {status}");
    }
    Ok(())
}

pub async fn run() -> Result<()> {
    let repo_root = Path::new(env!("CARGO_MANIFEST_DIR"));
    let image_dir = repo_root.join("image");
    let manifests = repo_root.join("manifests");

    let nerdctl = "sudo nerdctl --address /run/k3s/containerd/containerd.sock --namespace k8s.io";
    let kubectl = "sudo k3s kubectl";

    run_cmd(
        "building claude-agent image",
        &format!("{nerdctl} build -t claude-agent:latest {}", image_dir.display()),
    )?;

    run_cmd(
        "applying namespace",
        &format!("{kubectl} apply -f {}/namespace.yaml", manifests.display()),
    )?;

    // apply all PVCs
    for entry in std::fs::read_dir(&manifests)? {
        let path = entry?.path();
        if path.file_name().map(|n| n.to_string_lossy().starts_with("pvc-")).unwrap_or(false) {
            run_cmd(
                &format!("applying {}", path.file_name().unwrap().to_string_lossy()),
                &format!("{kubectl} apply -f {}", path.display()),
            )?;
        }
    }

    run_cmd(
        "creating configmap",
        &format!(
            "{kubectl} create configmap dispatcher-scripts -n claude-agents \
             --from-file=job-template.yaml={}/job-template.yaml \
             --dry-run=client -o yaml | {kubectl} apply -f -",
            manifests.display()
        ),
    )?;

    run_cmd(
        "applying dispatcher cronjob + RBAC",
        &format!("{kubectl} apply -f {}/dispatcher-cronjob.yaml", manifests.display()),
    )?;

    println!("\n=== deployment complete ===\n");
    Ok(())
}
