# k3sc changelog

## Changelog rules

These rules govern `docs/changelog.md`. Follow them exactly.

- Each day has exactly one `## YYYY-MM-DD` heading and exactly one table below
  it.
- Every daily table has exactly three columns: `System`, `Item`, and
  `Done when`.
- Start every Item with `[x]`. No preceding hyphen. Changelog rows are
  completed work only.
- Never combine completed items into one changelog row. Never replace them with
  a summary paragraph, grouped bullet, status paragraph, or count.
- When recording a completed todo row, copy its System into `System`, its
  completed instruction into `Item`, and its completion proof into `Done when`.
- Verify the exact result before adding the changelog row.
- Leave unfinished work in the todo. Do not add partial work to the changelog.
- Put new days above older days. Add rows to the existing table when that date
  already exists. Never create a second heading or second table for the same
  day.
- Use plain terms. Do not use vague descriptions, invented language, or design
  essays.

## 2026-10-22

| System | Item | Done when |
|---|---|---|
| cmd/cargo_lock.go | [x] Let Cargo build test and run targets once | Removed the redundant preliminary build while preserving the shared lock, manifest discovery, arguments and exit status. go test ./..., go vet ./... and local CLI build passed. Installed Windows CLI ran LOTJ's existing production catalog check with one Cargo invocation and no preliminary build; cached compile0.52s, test0.70s. Previous CLI executable retained; no Kubernetes rollout. |

## 2026-10-21

| System | Item | Done when |
|---|---|---|
| go.mod, cmd/tls_test.go | [x] Require Go 1.27.3 and rebuild the local Windows and Linux artifacts | go test ./... and go vet ./... pass; K3SC_TLS_CHECK=1 go test ./cmd -run '^TestCertificateDistrust$' -v rejects direct and cross-signed compromised chains with a valid HTTPS control. go version -m reports go1.27.3 for k3sc.exe and image/k3sc; k3sc --help passes. Cluster rollout remains an open todo because WSL reports HCS_E_CONNECTION_TIMEOUT. |

