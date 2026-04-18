FROM ghcr.io/agents-sandbox/coding-runtime:latest

# Extra CLI tools useful in a long-lived dev sandbox.
RUN apt-get update && apt-get install -y --no-install-recommends \
        vim less tree jq procps \
        iproute2 net-tools htop tmux \
        zsh fzf \
    && rm -rf /var/lib/apt/lists/*

# Atuin: SQLite-backed shell history with fuzzy search. Install the static
# binary system-wide so every user picks it up from PATH.
ARG TARGETARCH
RUN set -eux; \
    case "${TARGETARCH}" in \
        amd64) atuin_arch=x86_64-unknown-linux-musl ;; \
        arm64) atuin_arch=aarch64-unknown-linux-musl ;; \
        *) echo "unsupported arch ${TARGETARCH}"; exit 1 ;; \
    esac; \
    atuin_url="https://github.com/atuinsh/atuin/releases/latest/download/atuin-${atuin_arch}.tar.gz"; \
    curl -fsSL "${atuin_url}" -o /tmp/atuin.tgz; \
    tar -xzf /tmp/atuin.tgz -C /tmp; \
    mv /tmp/atuin-${atuin_arch}/atuin /usr/local/bin/atuin; \
    chmod +x /usr/local/bin/atuin; \
    rm -rf /tmp/atuin.tgz /tmp/atuin-${atuin_arch}

# oh-my-zsh + plugins, seeded into /etc/skel. The runtime entrypoint creates
# the agbox user with `useradd -m`, which copies /etc/skel/* into its home.
RUN git clone --depth=1 https://github.com/ohmyzsh/ohmyzsh.git /etc/skel/.oh-my-zsh \
    && git clone --depth=1 https://github.com/zsh-users/zsh-autosuggestions \
        /etc/skel/.oh-my-zsh/custom/plugins/zsh-autosuggestions \
    && git clone --depth=1 https://github.com/zsh-users/zsh-syntax-highlighting \
        /etc/skel/.oh-my-zsh/custom/plugins/zsh-syntax-highlighting \
    && cp /etc/skel/.oh-my-zsh/templates/zshrc.zsh-template /etc/skel/.zshrc \
    && sed -i 's|^plugins=.*|plugins=(git docker zsh-autosuggestions zsh-syntax-highlighting)|' \
        /etc/skel/.zshrc \
    && printf '\n# atuin: replace Ctrl-R and Up-arrow with the full-screen search UI\neval "$(atuin init zsh)"\n' \
        >> /etc/skel/.zshrc

# Agent CLIs on top of claude-code / codex already present in the base image.
RUN npm install -g opencode-ai @getpaseo/cli

# Seed a quickstart README into new user homes via /etc/skel.
COPY skel/quickstart /etc/skel/quickstart

# Override the base entrypoint to create agbox with zsh as its default shell
# and to sync oh-my-zsh assets into an existing agbox home (volume-reused case).
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
ENTRYPOINT ["entrypoint.sh"]
CMD ["/bin/zsh"]
