#!/usr/bin/env bash
set -euo pipefail

ISSUE_NUMBER="${ISSUE_NUMBER:?ISSUE_NUMBER env var required}"
AGENT_SLOT="${AGENT_SLOT:?AGENT_SLOT env var required}"
SLOT_LETTER="${SLOT_LETTER:?SLOT_LETTER env var required}"
REPO_URL="${REPO_URL:-https://github.com/abix-/endless.git}"

AGENT_FAMILY="${AGENT_FAMILY:-claude}"
AGENT_ID="${AGENT_FAMILY}-${SLOT_LETTER}"
REPO_NAME=$(basename "${REPO_URL}" .git)
WORKSPACE="/workspaces/${REPO_NAME}-${AGENT_ID}"

JOB_KIND="${JOB_KIND:-issue}"

# skip cargo setup for non-code jobs
if [ "$JOB_KIND" != "timberbot" ]; then
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
fi
install -d -m 700 "${HOME}/.codex" 2>/dev/null || true
if [ ! -d "${HOME}/.codex" ] || [ ! -w "${HOME}/.codex" ]; then
    echo "[entrypoint] ERROR: ${HOME}/.codex must be writable for auth bootstrap"
    ls -ld "${HOME}" "${HOME}/.codex" 2>/dev/null || true
    exit 1
fi

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

# materialize Codex auth state from secret-backed env if present
if [ -n "${CODEX_AUTH_JSON:-}" ]; then
    if ! printf '%s' "${CODEX_AUTH_JSON}" > "${HOME}/.codex/auth.json"; then
        echo "[entrypoint] ERROR: failed to write ${HOME}/.codex/auth.json"
        ls -ld "${HOME}" "${HOME}/.codex" 2>/dev/null || true
        exit 1
    fi
    chmod 600 "${HOME}/.codex/auth.json"
fi

# GITHUB_TOKEN env var is auto-detected by gh CLI -- no explicit login needed
if [ "$JOB_KIND" != "timberbot" ]; then
    if [ -z "${GITHUB_TOKEN:-}" ]; then
        echo "[entrypoint] ERROR: GITHUB_TOKEN env var is required"
        exit 1
    fi
    echo "[entrypoint] verifying gh auth..."
    gh auth status 2>&1 || true
fi

AGENT_FAMILY="${AGENT_FAMILY:-claude}"
echo "[entrypoint] agent family: ${AGENT_FAMILY}"

PR_NUMBER="${PR_NUMBER:-0}"
echo "[entrypoint] job_kind=${JOB_KIND} pr=${PR_NUMBER}"

# build prompt based on job kind
if [ "$JOB_KIND" = "timberbot" ]; then
    TIMBERBOT_GOAL="${TIMBERBOT_GOAL:-play the game}"
    TIMBERBOT_ROUNDS="${TIMBERBOT_ROUNDS:-5}"
    # add timberbot to PATH and resolve game host for WSL2->Windows
    export PATH="/timberbot:${PATH}"
    TIMBERBOT_HOST="${TIMBERBOT_HOST:-$(ip route show default 2>/dev/null | awk '/default/ {print $3}')}"
    export TIMBERBOT_HOST
    echo "[entrypoint] timberbot host=${TIMBERBOT_HOST} rounds=${TIMBERBOT_ROUNDS} goal=${TIMBERBOT_GOAL}"
    SKILL_PROMPT="Skip /obey. Go straight to /timberbot.
Run /timberbot ${TIMBERBOT_ROUNDS} times. Use --host=${TIMBERBOT_HOST} on every timberbot.py call.
Goal: ${TIMBERBOT_GOAL}
After completing ${TIMBERBOT_ROUNDS} rounds, exit cleanly."
elif [ "$JOB_KIND" = "review" ]; then
    SKILL_PROMPT="/review ${REPO_NAME} ${PR_NUMBER}"
else
    SKILL_PROMPT="/issue ${REPO_NAME} ${ISSUE_NUMBER}"
fi

if [ "$AGENT_FAMILY" = "codex" ]; then
    if [ -z "${OPENAI_API_KEY:-}" ] && [ ! -s "${HOME}/.codex/auth.json" ]; then
        echo "[entrypoint] ERROR: Codex auth is required via OPENAI_API_KEY or CODEX_AUTH_JSON"
        exit 1
    fi

    echo "[entrypoint] verifying codex auth..."
    codex --version 2>&1 || true
    codex login status 2>&1 || true

    echo "[entrypoint] launching codex for ${REPO_NAME}#${ISSUE_NUMBER} (${JOB_KIND})..."
    codex exec --dangerously-bypass-approvals-and-sandbox "/obey
${SKILL_PROMPT}" 2>&1
    EXIT_CODE=$?
else
    if [ -z "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
        echo "[entrypoint] ERROR: CLAUDE_CODE_OAUTH_TOKEN env var is required for Claude agents"
        exit 1
    fi

    echo "[entrypoint] verifying claude auth..."
    claude --version 2>&1 || true

    echo "[entrypoint] launching claude for ${REPO_NAME}#${ISSUE_NUMBER} (${JOB_KIND})..."
    claude --dangerously-skip-permissions -p "/obey
${SKILL_PROMPT}" \
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
fi

echo "[entrypoint] ${AGENT_FAMILY} exited with code ${EXIT_CODE}"
exit ${EXIT_CODE}
