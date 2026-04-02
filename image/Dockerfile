FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# system deps
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates git jq iproute2 && \
    rm -rf /var/lib/apt/lists/*

# node.js 22 LTS
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*

# claude code CLI + codex CLI
RUN npm install -g @anthropic-ai/claude-code@latest @openai/codex@latest

# github CLI
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    -o /usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=amd64 signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
    > /etc/apt/sources.list.d/github-cli.list && \
    apt-get update && apt-get install -y gh && \
    rm -rf /var/lib/apt/lists/*

# kubectl
RUN curl -fsSL "https://dl.k8s.io/release/$(curl -sL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" \
    -o /usr/local/bin/kubectl && \
    chmod +x /usr/local/bin/kubectl

# k3sc binary (pre-built for linux)
COPY k3sc /usr/local/bin/k3sc
RUN chmod +x /usr/local/bin/k3sc

# entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# non-root user (claude refuses --dangerously-skip-permissions as root)
RUN useradd -m -s /bin/bash claude && \
    mkdir -p /workspaces /home/claude/.codex/sessions /opt/gh-wrapper && \
    chown -R claude:claude /workspaces /home/claude/.codex /opt/gh-wrapper
COPY --chown=claude:claude claude-config/ /home/claude/.claude/

USER claude
WORKDIR /workspaces

ENTRYPOINT ["/entrypoint.sh"]
