# stay-go

**Declarative system configuration for Linux.** Describe the packages, files, users, services, and scripts you want — stay-go figures out what needs to change and shows you the plan before touching anything.

---

## Why stay-go?

Most configuration tools are either too complex (Ansible, NixOS) or too limited (shell scripts). stay-go sits in the middle: a single YAML file that captures your desired system state, with an explicit diff-then-apply loop and no server required.

- **Shows a plan first** — every run diffs config against the live system and asks before acting
- **Idempotent** — running twice produces no additional changes
- **Dependency-aware** — resources declare deps on each other; ordering and skip propagation are automatic
- **Multi-distro** — auto-detects paru, yay, pacman, apt-get, dnf, zypper, apk, xbps
- **Layered config** — split config across `default.yaml`, per-host, and per-user files

---

## Install

```sh
go install github.com/rayzorben/stay-go/cmd/stay-go@latest
```

Or build from source:

```sh
git clone https://github.com/rayzorben/stay-go
cd stay-go
make install
```

---

## Quick start

Create a `config/` directory with a `default.yaml`:

```yaml
packages:
  - neovim
  - git
  - fish

users:
  - username: alice
    shell: /bin/fish
    groups: [wheel, docker]

services:
  - docker
```

Run it:

```sh
stay-go
```

stay-go shows a plan and waits for confirmation:

```
Packages
  [+] neovim
  [+] git
  [+] fish

Users
  [+] alice

Services
  [+] docker

3 to add, 1 to add, 1 to add.  Apply? [Y/n]
```

---

## Configuration

### Packages

```yaml
packages:
  - neovim
  - git

  # Inline service dependency — enables docker after the package is installed
  - package: docker
    services: [docker]

  # Force-remove a package
  - "!vim"
```

### Files

Copy files, write inline content, or create symlinks. Sources can be local paths, git repos, or URLs.

```yaml
files:
  # Copy a local file
  - source: ${config_root}/files/gitconfig
    target: ~/.gitconfig

  # Inline content
  - content: |
      [main]
      shift = oneshot(shift)
    target: /etc/keyd/default.conf
    sudo: true

  # Symlink
  - source: ${config_root}/files/nvim
    target: ~/.config/nvim
    symlink: true

  # From a URL
  - source: https://example.com/font.ttf
    target: ~/.local/share/fonts/font.ttf
```

### Services

```yaml
services:
  - docker

  # User service, enable without starting
  - service: syncthing
    user: true
    now: false
    depends:
      - packages: [syncthing]
```

### Scripts & Commands

Scripts are tracked by file content — they re-run automatically when the file changes.

```yaml
scripts:
  - script: ${config_root}/scripts/install-theme.sh
    sudo: true
    depends:
      - packages: [git]
      - services: [sddm]
      - folders: ["!/usr/share/sddm/themes/my-theme"]  # skip if already installed

commands:
  # Inline shell command, tracked by content hash
  - name: refresh font cache
    command: fc-cache -f
    depends:
      - packages: [fontconfig]
```

### Users & Groups

```yaml
groups:
  - docker
  - wheel

users:
  - username: alice
    name: Alice Smith
    shell: /bin/fish
    home: ~/alice
    groups: [wheel, docker]
```

### Distrobox

Declare Distrobox containers with their own package lists and setup commands:

```yaml
distrobox:
  - name: dev
    image: docker.io/cachyos/cachyos:latest
    exports: [code, firefox]
    packages:
      - nodejs
      - python
    commands:
      - name: setup dev tools
        command: npm install -g typescript
```

### Variables & Layered Config

Variables are declared once and referenced as `${var_name}` anywhere in the config.
`${config_root}`, `${env:VAR}`, `$(command)`, and `~` are always available.

```yaml
variables:
  dotfiles: ${config_root}/dotfiles
  fonts_dir: /usr/share/fonts/TTF

files:
  - source: ${dotfiles}/nvim
    target: ~/.config/nvim
    symlink: true
```

Split config across multiple files — the most specific layer wins:

```
config/
  default.yaml          # applied everywhere
  hosts/mymachine.yaml  # applied on mymachine only
  users/alice.yaml      # applied when running as alice
```

### Secrets

Secrets are stored in the `secrets:` block as plaintext initially. On the first run, stay-go prompts for a password, encrypts each value in-place inside the YAML file, and caches the password in the OS keyring for subsequent runs.

```yaml
secrets:
  tailscale_auth_key: tskey-auth-abc123          # plaintext — encrypted on first run

  api_keys:                                       # nested form — referenced as ${secrets.api_keys.github}
    github: ghp_abc123
```

After the first run the file is rewritten automatically:

```yaml
secrets:
  _verify: !encrypted sn+HxIu...                 # sentinel — verifies the correct password
  tailscale_auth_key: !encrypted Zm9v...          # ciphertext replaces the plaintext

  api_keys:
    github: !encrypted dGVzdA...
```

Decrypted values are available as `${secrets.name}` (or `${secrets.group.name}` for nested keys) anywhere in the config at execute time. They are **never shown in plan output**.

```yaml
files:
  - content: ${secrets.tailscale_auth_key}
    target: /etc/tailscale/authkey
    sudo: true
    mode: "0600"
```

**Encryption:** Argon2id key derivation + AES-256-GCM authenticated encryption. Each value gets a unique random salt and nonce, so encrypting the same plaintext twice produces different ciphertexts. The `_verify` sentinel lets stay-go detect a wrong password before touching anything.

---

## Flags

```
stay-go [flags]

  -c, --config string   path to config directory (default "config")
      --state  string   path to state file (default ~/.local/share/stay-go/state.json)
  -d, --debug           stream command output to the terminal
  -n, --dry-run         show plan without executing
  -y, --yes             auto-confirm without prompting
  -S, --skipped         show skipped items in the plan
  -u, --update [target] upgrade tracked resources to their latest versions
      --show [scope]    print tracked state and exit (all, packages, users, …)
      --version         print version and exit
```

---

## Updating

`--update` refreshes what is already installed without changing your desired
config — the same plan → confirm → execute flow, but every row is an upgrade:

```sh
stay-go --update                      # everything: system packages, containers,
                                      # compose projects, flatpaks, distroboxes
stay-go --update=containers           # one resource type
stay-go --update=containers/frigate   # one item (also: containers.frigate, or
                                      # just "frigate" if unambiguous)
stay-go --update=flatpak,compose/immich   # comma-separate multiple targets
```

What each resource does on update:

| Resource | Update behaviour |
|---|---|
| `packages` | Full system upgrade via the detected manager (`paru -Syu`, `apt-get upgrade`, …) |
| `containers` | Pull the configured image; recreate the container only if the image changed |
| `compose` | Render the project, `compose pull`, then `up -d` (recreates changed services) |
| `flatpak` | `flatpak update` per app, plus one pass for runtimes |
| `distrobox` | `distrobox upgrade` (the box's own package manager, inside the box) |

Pin anything with `lock: true` on its entry — locked items are skipped by bulk
updates and shown in the plan with the reason:

```yaml
containers:
  frigate:
    image: ghcr.io/blakeblackshear/frigate:stable-rocm
    lock: true      # skipped by --update / --update=containers

packages:
  - name: linux
    lock: true      # held back via --ignore/--exclude where the manager supports it
```

Explicitly naming a locked item (`--update=containers/frigate`) overrides the
lock — you said it, you meant it. Compose projects and containers whose files
stay-go renders internally (secrets substituted into a private cache dir) are
updated the same way, so there is no need to hunt down the rendered files to
run `docker compose pull` by hand.

---

## Resource types

| Resource | What it manages |
|---|---|
| `packages` | System packages via the detected package manager |
| `files` | Files, symlinks, inline content, git repos, URLs |
| `users` | System users (shell, home, group membership) |
| `groups` | System groups |
| `services` | systemd system and user services |
| `scripts` | Shell scripts tracked by file content hash |
| `commands` | Inline shell commands tracked by content hash |
| `containers` | Docker / Podman containers |
| `compose` | docker-compose projects (rendered with secrets, `compose up -d`) |
| `distrobox` | Distrobox containers with in-box packages and commands |
| `flatpak` | Flatpak applications |
| `json` | Specific values inside existing JSON files |
| `secrets` | Age-encrypted secrets available to other resources |

---

## License

MIT
