#!/usr/bin/env bash
set -euo pipefail

ISSUE_NUMBER="${ISSUE_NUMBER:?ISSUE_NUMBER env var required}"
AGENT_SLOT="${AGENT_SLOT:?AGENT_SLOT env var required}"
SLOT_LETTER="${SLOT_LETTER:?SLOT_LETTER env var required}"
REPO_URL="${REPO_URL:-https://github.com/abix-/endless.git}"

AGENT_ID="claude-${SLOT_LETTER}"
REPO_NAME=$(basename "${REPO_URL}" .git)
WORKSPACE="/workspaces/${REPO_NAME}-claude-${SLOT_LETTER}"

export CARGO_TARGET_DIR="/cargo-target"
export CARGO_HOME="/cargo-home"
# seed cargo-home from image if empty (first run)
if [ ! -f "${CARGO_HOME}/bin/cargo" ]; then
    echo "[entrypoint] seeding CARGO_HOME from image..."
    cp -a /usr/local/cargo/bin "${CARGO_HOME}/"
    cp -a /usr/local/cargo/env* "${CARGO_HOME}/" 2>/dev/null || true
    mkdir -p "${CARGO_HOME}/registry" "${CARGO_HOME}/git"
fi
# seed rustup settings
if [ ! -d "${HOME}/.rustup" ]; then
    ln -sf /usr/local/rustup "${HOME}/.rustup"
fi
export PATH="${CARGO_HOME}/bin:${PATH}"

# trust all workspaces (PVC may have been created by different uid)
git config --global --add safe.directory '*'

echo "[entrypoint] agent=${AGENT_ID} repo=${REPO_NAME} issue=${ISSUE_NUMBER}"

# set up workspace: clone once, fetch on reuse
if [ ! -d "${WORKSPACE}/.git" ]; then
    echo "[entrypoint] cloning repo into ${WORKSPACE}..."
    git clone "${REPO_URL}" "${WORKSPACE}"
else
    # fix ownership if workspace was created by a different user (e.g. root)
    if [ ! -w "${WORKSPACE}/.git" ]; then
        echo "[entrypoint] fixing workspace ownership..."
        sudo chown -R "$(id -u):$(id -g)" "${WORKSPACE}" 2>/dev/null || true
    fi
    echo "[entrypoint] workspace exists, fetching..."
    git -C "${WORKSPACE}" fetch origin
fi

cd "${WORKSPACE}"

# configure git identity for this agent
git config user.name "${AGENT_ID}"
git config user.email "${AGENT_ID}@endless.dev"

# read github token from mounted file if env var not set
if [ -z "${GITHUB_TOKEN:-}" ] && [ -f /home/claude/.gh-token ]; then
    export GITHUB_TOKEN=$(cat /home/claude/.gh-token)
fi

# GITHUB_TOKEN env var is auto-detected by gh CLI -- no explicit login needed

# verify auth works
echo "[entrypoint] verifying gh auth..."
gh auth status 2>&1 || true

echo "[entrypoint] verifying claude auth..."
claude --version 2>&1 || true

echo "[entrypoint] launching claude for ${REPO_NAME}#${ISSUE_NUMBER}..."
claude --dangerously-skip-permissions -p "/issue ${REPO_NAME} ${ISSUE_NUMBER}" \
    --output-format stream-json --verbose --include-partial-messages 2>&1 | \
    while IFS= read -r line || [ -n "$line" ]; do
        if parsed=$(printf '%s\n' "$line" | jq -rj 'if .type == "stream_event" and .event.delta.type? == "text_delta" then .event.delta.text
         elif .type == "assistant" then (.message.content[]? | select(.type=="tool_use") | "\n[tool] " + .name + "\n")
         elif .type == "result" then ((.result? // "") | if . != "" then "\n" + . + "\n" else "" end) + "[result] exit\n"
         elif .type == "error" then ((.error.message? // .message? // tostring) + "\n")
         else empty end' 2>/dev/null); then
            printf '%s' "$parsed"
        elif [ -n "$line" ]; then
            printf '%s\n' "$line"
        fi
    done
EXIT_CODE=${PIPESTATUS[0]}
echo "[entrypoint] claude exited with code ${EXIT_CODE}"
exit ${EXIT_CODE}