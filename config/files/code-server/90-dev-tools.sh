#!/usr/bin/with-contenv bash
# Runs as root on every start of the code-server container (linuxserver
# custom-cont-init.d). Installs what mudengine's gate needs: Node, the
# Electron runtime libraries and xvfb for the smokes, plus neovim and the
# claude CLI on the terminal's PATH. Each step is skipped when already done,
# so a restart is quick; a recreate starts from the bare image and redoes it.
set -euo pipefail

NODE_MAJOR=24

log() { echo "[dev-tools] $*"; }

export DEBIAN_FRONTEND=noninteractive

apt_packages=(
    ca-certificates curl gnupg xz-utils unzip git ripgrep fd-find
    build-essential python3
    # mudengine smoke and check:live run Electron under xvfb-run
    xvfb xauth dbus-x11
    libnss3 libnspr4 libatk1.0-0t64 libatk-bridge2.0-0t64 libcups2t64
    libdrm2 libgbm1 libgtk-3-0t64 libasound2t64 libxss1 libxtst6
    libxkbcommon0 libxcomposite1 libxdamage1 libxrandr2 libxfixes3
    libpango-1.0-0 libcairo2 libsecret-1-0 libnotify4 libxshmfence1
    libdbus-1-3 libuuid1 fonts-liberation fonts-dejavu-core
)

missing=()
for p in "${apt_packages[@]}"; do
    dpkg-query -W -f='${Status}' "$p" 2>/dev/null | grep -q "install ok installed" || missing+=("$p")
done
if ((${#missing[@]})); then
    log "apt: installing ${missing[*]}"
    apt-get update -qq
    apt-get install -y -qq --no-install-recommends "${missing[@]}"
fi

# Ubuntu's own nodejs is too old for mudengine (engines: node >= 20).
if ! command -v node >/dev/null || [[ "$(node -p 'process.versions.node.split(".")[0]')" != "$NODE_MAJOR" ]]; then
    log "node: installing ${NODE_MAJOR}.x from NodeSource"
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash -
    apt-get install -y -qq nodejs
fi

# Neovim from the upstream release; Ubuntu 24.04 ships 0.9.
if ! command -v nvim >/dev/null; then
    log "neovim: installing latest release to /opt/nvim"
    tmp="$(mktemp -d)"
    curl -fsSL -o "$tmp/nvim.tar.gz" \
        https://github.com/neovim/neovim/releases/latest/download/nvim-linux-x86_64.tar.gz
    rm -rf /opt/nvim
    mkdir -p /opt/nvim
    tar -xzf "$tmp/nvim.tar.gz" -C /opt/nvim --strip-components=1
    ln -sf /opt/nvim/bin/nvim /usr/local/bin/nvim
    rm -rf "$tmp"
fi

# The claude CLI lives in /config (persistent, updates itself); link it onto
# the image's PATH. The code-server extension carries its own copy.
if [[ ! -x /config/.local/bin/claude ]]; then
    log "claude: installing native CLI for abc"
    s6-setuidgid abc env HOME=/config bash -c 'curl -fsSL https://claude.ai/install.sh | bash'
fi
ln -sf /config/.local/bin/claude /usr/local/bin/claude

log "done: node $(node -v), $(nvim --version | head -1), xvfb-run $(command -v xvfb-run)"
