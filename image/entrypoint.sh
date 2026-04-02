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

# cargo setup: only if rust toolchain is present in the image
if [ "$JOB_KIND" != "timberbot" ] && [ -d "/usr/local/cargo/bin" ]; then
    export CARGO_TARGET_DIR="/cargo-target"
    export CARGO_HOME="/cargo-home"
    if [ ! -f "${CARGO_HOME}/bin/cargo" ]; then
        echo "[entrypoint] seeding CARGO_HOME from image..."
        cp -a /usr/local/cargo/bin "${CARGO_HOME}/"
        cp -a /usr/local/cargo/env* "${CARGO_HOME}/" 2>/dev/null || true
        mkdir -p "${CARGO_HOME}/registry" "${CARGO_HOME}/git"
    fi
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

# install gh CLI wrapper from SAFETY_GH_ALLOWED config
# format: "auth:status issue:view,list,edit pr:create,view,list,diff,checks"
# wrapper goes to /opt/gh-wrapper/gh (owned by claude, prepended to PATH)
{
    cat << 'GHWRAPPER_HEAD'
#!/bin/bash
CMD="${1:-}"
SUB="${2:-}"
case "$CMD" in
GHWRAPPER_HEAD
    for rule in ${SAFETY_GH_ALLOWED}; do
        cmd="${rule%%:*}"
        subs="${rule#*:}"
        # convert comma-separated subs to pipe-separated for case pattern
        pattern=$(echo "$subs" | sed 's/,/|/g')
        cat << GHWRAPPER_RULE
    ${cmd})
        case "\$SUB" in
            ${pattern}) exec /usr/bin/gh "\$@" ;;
            *) echo "ERROR: gh ${cmd} \$SUB is not allowed. Permitted: ${subs}" >&2; exit 1 ;;
        esac
        ;;
GHWRAPPER_RULE
    done
    cat << 'GHWRAPPER_TAIL'
    *)
        echo "ERROR: gh $CMD is not allowed." >&2
        exit 1
        ;;
esac
GHWRAPPER_TAIL
} > /opt/gh-wrapper/gh
chmod +x /opt/gh-wrapper/gh
export PATH="/opt/gh-wrapper:${PATH}"

# trust all workspaces (PVC may have been created by different uid)
git config --global --add safe.directory '*'

# configure git to use token for HTTPS auth (credential helper reads env at runtime)
if [ -n "${GITHUB_TOKEN:-}" ]; then
    git config --global credential.helper '!f() { echo "username=x-access-token"; echo "password=${GH_TOKEN:-${GITHUB_TOKEN}}"; }; f'
fi

echo "[entrypoint] agent=${AGENT_ID} repo=${REPO_NAME} issue=${ISSUE_NUMBER}"

# set up workspace: clone once, fetch on reuse
if [ ! -d "${WORKSPACE}/.git" ]; then
    echo "[entrypoint] cloning repo into ${WORKSPACE}..."
    git clone "${REPO_URL}" "${WORKSPACE}"
else
    # workspace ownership is handled by pod securityContext.fsGroup
    echo "[entrypoint] workspace exists, fetching..."
    git -C "${WORKSPACE}" fetch origin
fi

cd "${WORKSPACE}"

# safety config from env (set by job template from k3sc config)
SAFETY_BRANCH_PATTERN="${SAFETY_BRANCH_PATTERN:-issue-{N}}"
SAFETY_GH_ALLOWED="${SAFETY_GH_ALLOWED:-auth:status issue:view,list,edit pr:create,view,list,diff,checks repo:view,list,clone}"
SAFETY_BLOCKED_COMMIT_WORDS="${SAFETY_BLOCKED_COMMIT_WORDS:-fixes,closes,resolves}"

# branch setup: ensure agent starts on the correct branch (not main)
if [ "$JOB_KIND" = "issue" ] || [ "$JOB_KIND" = "review" ]; then
    BRANCH=$(echo "${SAFETY_BRANCH_PATTERN}" | sed "s/{N}/${ISSUE_NUMBER}/g")
    if git ls-remote --heads origin "${BRANCH}" | grep -q "${BRANCH}"; then
        echo "[entrypoint] checking out existing branch ${BRANCH}"
        git checkout "${BRANCH}" 2>/dev/null || git checkout -b "${BRANCH}" "origin/${BRANCH}"
        git pull --rebase origin main 2>/dev/null || true
    else
        echo "[entrypoint] creating branch ${BRANCH} from origin/main"
        git fetch origin
        git checkout -b "${BRANCH}" origin/main
    fi
    echo "[entrypoint] on branch: $(git branch --show-current)"
fi

# install pre-push hook: only allow pushing to the configured branch
mkdir -p "${WORKSPACE}/.git/hooks"
ALLOWED_BRANCH=$(echo "${SAFETY_BRANCH_PATTERN}" | sed "s/{N}/${ISSUE_NUMBER}/g")
cat > "${WORKSPACE}/.git/hooks/pre-push" << HOOK
#!/bin/bash
ALLOWED_BRANCH="${ALLOWED_BRANCH}"
while read local_ref local_sha remote_ref remote_sha; do
    branch=\$(echo "\$remote_ref" | sed 's|refs/heads/||')
    if [ "\$branch" != "\$ALLOWED_BRANCH" ]; then
        echo "ERROR: push to '\$branch' blocked. Only '\$ALLOWED_BRANCH' is allowed."
        exit 1
    fi
done
exit 0
HOOK
chmod +x "${WORKSPACE}/.git/hooks/pre-push"

# install commit-msg hook: block configured keywords
BLOCKED_PATTERN=$(echo "${SAFETY_BLOCKED_COMMIT_WORDS}" | sed 's/,/|/g')
cat > "${WORKSPACE}/.git/hooks/commit-msg" << HOOK
#!/bin/bash
if grep -qiE "(${BLOCKED_PATTERN})\s+#[0-9]+" "\$1"; then
    echo "ERROR: commit messages must not use ${SAFETY_BLOCKED_COMMIT_WORDS} #N (auto-closes issues)."
    echo "Use 'ref #N' or 'for #N' instead."
    exit 1
fi
exit 0
HOOK
chmod +x "${WORKSPACE}/.git/hooks/commit-msg"

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
    # detect GHE host from REPO_URL and configure gh CLI
    REPO_HOST=$(echo "${REPO_URL}" | sed -n 's|https://\([^/]*\)/.*|\1|p')
    if [ -n "${REPO_HOST}" ] && [ "${REPO_HOST}" != "github.com" ]; then
        export GH_ENTERPRISE_TOKEN="${GITHUB_TOKEN}"
        export GH_HOST="${REPO_HOST}"
        export GH_TOKEN="${GITHUB_TOKEN}"
        unset GITHUB_TOKEN
        echo "[entrypoint] GHE host: ${REPO_HOST}"
    fi
    echo "[entrypoint] verifying gh auth..."
    gh auth status --hostname "${REPO_HOST:-github.com}" 2>&1 || true
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
    # Auth priority: Bedrock > API key > OAuth
    if [ "${CLAUDE_CODE_USE_BEDROCK:-}" = "1" ] && [ -n "${AWS_ACCESS_KEY_ID:-}" ]; then
        echo "[entrypoint] claude auth: AWS Bedrock (region=${AWS_REGION:-us-east-1})"
    elif [ -n "${ANTHROPIC_API_KEY:-}" ]; then
        echo "[entrypoint] claude auth: API key"
    elif [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ]; then
        echo "[entrypoint] claude auth: OAuth (subscription)"
    else
        echo "[entrypoint] ERROR: no Claude auth found (need CLAUDE_CODE_USE_BEDROCK+AWS creds, ANTHROPIC_API_KEY, or CLAUDE_CODE_OAUTH_TOKEN)"
        exit 1
    fi

    echo "[entrypoint] verifying claude auth..."
    claude --version 2>&1 || true

    echo "[entrypoint] launching claude for ${REPO_NAME}#${ISSUE_NUMBER} (${JOB_KIND})..."
    MODEL_FLAG=""
    if [ -n "${CLAUDE_MODEL:-}" ]; then
        MODEL_FLAG="--model ${CLAUDE_MODEL}"
    fi
    claude --dangerously-skip-permissions ${MODEL_FLAG} -p "/obey
${SKILL_PROMPT}" \
        --output-format stream-json --verbose --include-partial-messages 2>&1 | \
        while IFS= read -r line || [ -n "$line" ]; do
            if parsed=$(printf '%s\n' "$line" | jq -rj 'if .type == "stream_event" and .event.delta.type? == "text_delta" then .event.delta.text
             elif .type == "assistant" then (.message.content[]? | select(.type=="tool_use") | "\n[tool] " + .name + (if .name == "Bash" then ": " + (.input.command // "" | split("\n")[0] | .[:120]) elif .name == "Read" then ": " + (.input.file_path // "") elif .name == "Edit" then ": " + (.input.file_path // "") elif .name == "Grep" then ": " + (.input.pattern // "") elif .name == "Write" then ": " + (.input.file_path // "") else "" end) + "\n")
             elif .type == "tool_result" then (.message.content[]? | select(.type=="text") | .text | split("\n") | if length > 20 then (.[0:10] | join("\n")) + "\n...[" + (length | tostring) + " lines]\n" + (.[-5:] | join("\n")) else join("\n") end | . + "\n")
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

echo ""
# collect usage stats from claude's JSONL files
collect_usage() {
    local claude_dir="${HOME}/.claude"
    local projects_dir="${claude_dir}/projects"
    if [ ! -d "$projects_dir" ]; then
        return
    fi
    # find all .jsonl usage files, parse with jq, sum token counts
    find "$projects_dir" -name '*.jsonl' -type f 2>/dev/null | while IFS= read -r f; do cat "$f"; done | \
        jq -sc '
            [.[] | select(.message != null and .message.usage != null)] |
            {
                input_tokens: (map(.message.usage.input_tokens // 0) | add // 0),
                output_tokens: (map(.message.usage.output_tokens // 0) | add // 0),
                cache_creation_tokens: (map(.message.usage.cache_creation_input_tokens // 0) | add // 0),
                cache_read_tokens: (map(.message.usage.cache_read_input_tokens // 0) | add // 0),
                models: ([.[].message.model // empty] | unique),
                entries: length
            } |
            .total_tokens = .input_tokens + .output_tokens + .cache_creation_tokens + .cache_read_tokens |
            .total_input = .input_tokens + .cache_creation_tokens + .cache_read_tokens |
            .cache_hit_rate = (if .total_input > 0 then ((.cache_read_tokens * 1000 / .total_input) | round / 1000) else 0 end) |
            .output_ratio = (if .total_tokens > 0 then ((.output_tokens * 1000 / .total_tokens) | round / 1000) else 0 end)
        ' 2>/dev/null | {
            read -r usage_json
            if [ -n "$usage_json" ]; then
                echo "[usage] $usage_json"
            fi
        }
}
collect_usage

echo "[entrypoint] ${AGENT_FAMILY} exited with code ${EXIT_CODE}"
exit ${EXIT_CODE}
