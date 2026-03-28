FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

# system deps
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates git build-essential pkg-config \
    libssl-dev unzip jq python3 python3-pip && \
    rm -rf /var/lib/apt/lists/*

# python deps for timberbot
RUN pip3 install --break-system-packages requests toons

# node.js 22 LTS
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - && \
    apt-get install -y nodejs && \
    rm -rf /var/lib/apt/lists/*

# claude code CLI + codex CLI
RUN npm install -g @anthropic-ai/claude-code@2.1.80 @openai/codex@0.116.0

# github CLI
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    -o /usr/share/keyrings/githubcli-archive-keyring.gpg && \
    echo "deb [arch=amd64 signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
    > /etc/apt/sources.list.d/github-cli.list && \
    apt-get update && apt-get install -y gh && \
    rm -rf /var/lib/apt/lists/*

# kubectl (for dispatcher pod)
RUN curl -fsSL "https://dl.k8s.io/release/$(curl -sL https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl" \
    -o /usr/local/bin/kubectl && \
    chmod +x /usr/local/bin/kubectl

# rust toolchain (as non-root user later)
ENV RUSTUP_HOME=/usr/local/rustup
ENV CARGO_HOME=/usr/local/cargo
ENV PATH="/usr/local/cargo/bin:${PATH}"
RUN curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | \
    sh -s -- -y --default-toolchain stable --profile minimal && \
    rustup component add clippy rustfmt

# k3sc binary (pre-built for linux, includes cargo-lock subcommand)
COPY k3sc /usr/local/bin/k3sc
RUN chmod +x /usr/local/bin/k3sc

# entrypoint
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# claude config is copied below for non-root user

# non-root user (claude refuses --dangerously-skip-permissions as root)
RUN apt-get update && apt-get install -y --no-install-recommends sudo && \
    rm -rf /var/lib/apt/lists/* && \
    useradd -m -s /bin/bash claude && \
    echo "claude ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/claude && \
    mkdir -p /workspaces /cargo-target /cargo-home /home/claude/.codex/sessions && \
    chown -R claude:claude /workspaces /cargo-target /cargo-home /home/claude/.codex
COPY --chown=claude:claude claude-config/ /home/claude/.claude/

USER claude
WORKDIR /workspaces

ENTRYPOINT ["/entrypoint.sh"]
