# stay-go — Developer Guide

stay-go is a declarative system configuration manager for Linux. You describe the desired state of packages, users, groups, services, and scripts in a YAML file; stay-go diffs that against the live system and a persisted state file, then presents a plan of changes for approval before executing.

---

## Project Goals

- **Idempotent** — running twice produces no additional changes
- **Transparent** — always shows an explicit plan before acting
- **Robust** — one resource's failure does not affect unrelated resources
- **Multi-distro** — supports Arch, Debian/Ubuntu, Fedora/RHEL, openSUSE, Alpine, Void
- **Dependency-aware** — resources declare deps on each other; ordering and skip propagation are automatic
- **Auditable** — state is a plain JSON file; every managed node has a content hash

---

## Architecture

```
cmd/stay-go/main.go          — CLI entry point, flag parsing, resource registration
internal/
  config/                    — YAML loading, node IDs, deterministic hashing, variable resolution
    config.go                — Config struct, entry types, LoadAll (merges layers + resolves vars)
    vars.go                  — Variable resolution: ${var_name}, ~ expansion, iterative passes
  state/                     — JSON state persistence (~/.local/share/stay-go/state.json)
  executor/                  — os/exec wrapper (capture/stream stdout+stderr, sudo, env);
                               records every command in the run log
  runlog/                    — per-run log file (~/.local/share/stay-go/logs/), redaction, retention
  engine/
    resource.go              — Resource interface (ISP: Knowledger+Planner+NodeExecutor)
                               + shared helpers: DetermineAction, StateRemovals
                               + PlanNode (includes NeedsSudo for pre-auth)
    engine.go                — Orchestration: gather → plan → markSkips → sort → confirm → execute
                               preSudo: runs "sudo -v" once before execute loop if any node needs it
    graph.go                 — Dependency DAG, Kahn's topo sort, markSkips
    display.go               — ANSI plan display, confirmation prompt, --show output
  resource/
    packages/                — Package management resource
      packages.go            — Resource struct, New, Type, ensureManager
      knowledge.go           — GatherKnowledge (runs package manager list command)
      plan.go                — BuildPlan (uses DetermineAction + StateRemovals)
      execute.go             — Execute (install / remove via package manager)
      managers.go            — PackageManager table, Detect, parser helpers
    groups/                  — System group resource
      groups.go              — Resource struct, New, Type, /etc/group helpers
      knowledge.go           — GatherKnowledge (reads /etc/group natively)
      plan.go                — BuildPlan
      execute.go             — Execute (groupadd / groupdel)
    users/                   — System user resource
      users.go               — Resource struct, New, Type, /etc/passwd + /etc/group helpers
      knowledge.go           — GatherKnowledge (reads /etc/passwd natively)
      plan.go                — BuildPlan (group membership diff; auto-deps on groups)
      execute.go             — Execute (useradd / usermod / userdel / gpasswd -d)
    services/                — Systemd service resource
      services.go            — Resource struct, New, Type, systemctl helpers
      knowledge.go           — GatherKnowledge (systemctl is-enabled per service)
      plan.go                — BuildPlan
      execute.go             — Execute (systemctl enable/restart/disable)
    scripts/                 — Shell script resource
      scripts.go             — Resource struct, New, Type, nodeID helper
      knowledge.go           — GatherKnowledge (all configured scripts reported as present)
      plan.go                — BuildPlan (hashes file content; evaluates folder conditions)
      execute.go             — Execute (bash <script>; REMOVE only clears state)
```

### Import Layering (no circular imports)

```
runlog    ← stdlib only
executor  ← runlog
config    ← yaml.v3 only
state     ← stdlib only
engine    ← executor + config + state + runlog   (defines Resource interface + PlanNode)
resource/* ← engine + config + state + executor
cmd/stay-go ← all of the above
```

---

## Core Flow

```
1. GatherKnowledge  — all resources in parallel (sync.WaitGroup)
                      each returns []KnowledgeEntry{ID string}
                      engine merges into combined map[nodeID]bool

2. BuildPlan        — sequential, registration order
                      (packages → groups → users → services → scripts)
                      each resource calls engine.DetermineAction() (DRY)
                      and engine.StateRemovals() for REMOVE detection (DRY)
                      services/scripts attach DependsOn from the depends: config field
                      scripts also evaluate folder conditions at plan time

3. markSkips        — iterative propagation: if a dep's action is REMOVE or SKIP
                      → mark dependent as SKIP with all failing reasons (transitive)

4. topoSort         — Kahn's algorithm; REMOVE nodes use reversed dep edges
                      (remove dependents before dependencies)

5. DisplayPlan      — ANSI grouped output: [+] ADD [=] TRACK [-] REMOVE [~] UPDATE [!] SKIP

6. Confirm          — Y/n prompt; --dry-run skips this step

7. preSudo          — if any active node has NeedsSudo=true, run "sudo -v" once
                      to cache credentials before the execute loop

8. Execute          — process nodes in topo order:
                      TRACK/ADOPT/LEVEL → state.Set (no command)
                      ADD/UPDATE/REMOVE → resource.Execute → state.Set or Delete
                      if a dep's Execute fails → dependent becomes SKIP at runtime

9. SaveState        — atomic write (tmp + rename)
```

Every step is recorded in the run log (see **Run Log** below).

---

## Action Types

| Action | Condition | Effect |
|--------|-----------|--------|
| TRACK  | In knowledge (+ matches config or first-time) | Add to state; no command |
| ADD    | In config, not in knowledge | Install/create/run |
| UPDATE | In state with different hash, or a command's `check:` exits non-zero | Modify existing / re-run |
| UPGRADE | --update mode only (tracked entry) | Refresh to latest version; state untouched |
| REMOVE | In state, not in config | Uninstall/delete (scripts: state only) |
| SKIP   | Dependency REMOVE/SKIP/FAIL, folder condition not met, or `lock: true` in a bulk --update | Log only |

---

## Update Mode (--update)

`stay-go --update[=targets]` refreshes tracked items to their latest versions
without changing desired state. It is a separate pipeline (`engine.RunUpdate`
in `engine/update.go`) that reuses markSkips → topoSort → DisplayPlan →
Confirm → execute:

1. Every resource implementing the optional `engine.Updater` interface
   (`BuildUpdatePlan(ctx, st) ([]*PlanNode, error)`) returns ActionUpgrade
   nodes for its tracked entries. Not-yet-applied entries come back as SKIP.
2. Targets filter the nodes: none/`all` → everything; a resource type
   (`containers`) → bulk for that type; a node (`containers/frigate`,
   `containers.frigate`, or bare unambiguous `frigate`) → just that item.
3. `lock: true` on an entry sets `PlanNode.Locked`; bulk updates turn locked
   nodes into SKIP, but an explicit node target overrides the lock.
4. The secrets resource's plan is included and `addSecretBarrierDeps` wired,
   so compose/container upgrades see plaintext secrets at execute time.
5. UPGRADE Execute never calls `st.Set`/`st.Delete` — an upgrade does not
   change the desired config, so state hashes stay stable.

Per-resource behaviour:

| Resource | Updater implementation |
|----------|------------------------|
| packages | Single ephemeral `packages/__upgrade__` node: `UpgradeCmd` from the manager table (full system upgrade; per-package upgrades are deliberately unsupported). Locked packages → `IgnoreFmt` flag (`--ignore=%s` pacman family, `--exclude=%s` dnf/yum); managers without one get a plan note. |
| containers | Per container: pull image, compare image IDs, stop+rm+run only if changed |
| compose | Per project: render (secrets substituted), `compose pull`, `up -d --remove-orphans` |
| flatpak | Per app: `flatpak update --user <app>`; plus one `flatpak/runtimes` node |
| distrobox | Per box: `distrobox upgrade <name>` (in-box package manager) |

`lock: true` is excluded from every config hash, so toggling it never triggers
an UPDATE in normal runs.

---

## Dependency System

Declared in config under any resource's `depends` key:

```yaml
services:
  - service: docker
    depends:
      - packages: [docker]   # "packages/docker" must be TRACK/ADD/UPDATE

scripts:
  - script: ${config_root}/scripts/install-theme.sh
    sudo: true
    depends:
      - packages: [git]
      - services: [sddm]
      - folders: ["!/usr/share/sddm/themes/my-theme"]  # skip if folder exists
```

The engine converts resource deps to `DependsOn: ["packages/docker"]` on the PlanNode.
`markSkips` enforces it at plan time, collecting **all** failing deps into the skip reason.
Runtime failure propagation re-checks at execution time.

### Folder Conditions

Folder conditions are evaluated at `BuildPlan` time — they are not plan nodes:

| Syntax | Meaning |
|--------|---------|
| `folders: ["/path"]`  | Skip if `/path` does **not** exist |
| `folders: ["!/path"]` | Skip if `/path` **does** exist |

Multiple conditions in the same `folders:` list are all evaluated; any failure skips the node.

---

## Variable System

Variables are defined in the config YAML under `variables:` and referenced as `${var_name}`.

```yaml
variables:
  dotfiles: ${config_root}/dotfiles

users:
  - name: rayben
    home: ~/rayben
```

**Implicit variables** (always available):
- `${config_root}` — absolute path to the config directory
- `~` — current user's home directory (expanded everywhere inline)

**Resolution**: iterative passes replace `${key}` tokens until the result stabilises
(max 15 passes), supporting transitive references like `${a}` → `${b}` → `/real/path`.

Variables are resolved once in `LoadAll` and applied to all string fields before
any resource sees the config. Higher-specificity layers override lower ones.

Also resolved universally (never by individual resources):
- `${env:NAME}` — value of an environment variable
- `$(command)` — stdout of a shell command, run at load time
- `${secrets.x}` — see below

`$(...)` and `~` are expanded in **every** string field, including
`commands[].command`/`check`/`rollback`: a `$(...)` meant for the shell runs
once at load time (with shell variables like `$t` empty) and is replaced by its
output. In command strings use a pipe (`cmd | grep -qx value`), backticks, or a
script file instead. Unknown `${name}` tokens are left for the shell.

---

## Substitution Pipeline & Secrets

All substitution is centralised in the `config` package (`config/resolve.go`,
`config/vars.go`, `config/secrets.go`). **Resources never deal with `${var}`,
`${env:}`, `$(cmd)`, `~`, or `${secrets.x}`** — by the time a resource sees the
`Config`, every string field is fully resolved.

Pipeline:
1. **`LoadAll`** — `ApplyVars` resolves `${var}/${env:}/$(cmd)/~` in every string
   field (reflection walk, skips `yaml:"-"`); then `ResolveSecretsToCiphertext`
   replaces every `${secrets.x}` token with the secret's **ciphertext**. After
   this the `Config` holds no secret tokens and no plaintext — safe to display
   and to hash (a secret rotation changes the ciphertext → triggers UPDATE).
2. **Execute phase** — the engine registers the `secrets` resource first and
   makes every other (non-REMOVE) node depend on the secrets nodes
   (`addSecretBarrierDeps`). As the secrets resource decrypts each secret it
   calls `config.SubstituteSecret`, replacing that ciphertext with the plaintext
   throughout the `Config`. So every later resource's `Execute` sees plaintext.

Content loaded from **outside** the `Config` tree (a file a resource reads at
execute time — e.g. the `compose` resource's project files) cannot be
pre-resolved; such resources call **`config.RenderExternal`** (plaintext, at
execute) or **`config.RenderExternalForHash`** (ciphertext, at plan) — the only
substitution API a resource ever touches.

A struct field tagged **`secrets:"-"`** is excluded from secret substitution
(still gets `${var}` resolution) — used where a `${secrets.x}` token is a
structural marker rather than an inline value (`FileEntry.Source`) or a value
serialised verbatim for a separate stay-go run (`DistroboxEntry.Packages/Commands`).

In `command:`/`rollback:` strings, `${secrets.x}` and `${var}` substitute
literally (no shell-quoting) — quote them in the YAML if a value may contain
whitespace or shell metacharacters, exactly as you would any shell variable.

---

## Scripts Resource

Scripts are shell scripts managed by stay-go. The hash is computed from:
- The script path and `sudo:` flag (from config)
- The **file content** of the script (changes trigger re-run)

```yaml
scripts:
  - script: ${config_root}/scripts/configure-zoxide.sh
    depends:
      - packages: [zoxide, fzf]

  - script: ${config_root}/scripts/install-theme.sh
    sudo: true
    depends:
      - packages: [git]
      - services: [sddm]
      - folders: ["!/usr/share/sddm/themes/my-theme"]
```

Actions:
- **ADD** — first run; script has never been executed under stay-go
- **UPDATE** — script file or config changed; re-run
- **REMOVE** — removed from config; clears state only (no system undo)
- **SKIP** — file not found, folder condition not met, or dep failed

---

## Command Checks

A `commands` entry may carry `check:` — a read-only shell probe run unprivileged
at plan time on every run (including `--dry-run`) once the command is applied.
Exit 0 keeps it TRACK; non-zero plans an UPDATE (re-run), described with the
check's first stdout line. `check` is excluded from the hash. Used for commands
that converge drifting state, e.g. `dropbox-exclude-regenerable` on cachygram.

---

## Run Log

Every run that can plan or change anything writes one plain-text log file to
`<state dir>/logs/` (`--log-dir` overrides; default
`~/.local/share/stay-go/logs/`). Files are named
`YYYYMMDD-HHMMSS.mmm-<pid>.log`, mode 0600; `latest.log` symlinks the newest
and only the last `runlog.DefaultKeep` (100) are kept. `--show` and
`--guest-knowledge` are not logged.

Contents, all timestamped:
- header: version, host, user, pid, args, cwd, config and state paths
- every command the executor runs: `cmd#N $ argv` (shell-quoted), caller env,
  then `cmd#N exit=… <duration> err=…` with captured stdout/stderr (each stream
  capped at 64 KiB, head + tail kept). The id pairs start and end lines, which
  interleave during parallel knowledge gathering.
- the plan (every non-TRACK node with reason, declared deps, notes; barrier
  deps on secrets are omitted), confirmation outcome, each node's
  start / ok / FAILED / SKIP with duration and error, and a summary listing
  every failed node
- fatal errors and panics (with stack), and `run finished: exit=N`

Writes are unbuffered so a crash keeps everything up to that point. Escape
sequences are stripped. The secrets resource reports each plaintext value via
its `Redact` hook as it is decrypted, and the log replaces it with
`«redacted»` in everything written afterwards. `*runlog.Log` methods are
nil-safe, so `Executor.Log` / `engine.Options.Log` may be left nil.

On failure, main prints the log path after the final report.

---

## Sudo Pre-Authentication

If any active plan node has `NeedsSudo: true` (packages with sudo manager, groups,
users, system services, or scripts with `sudo: true`), the engine runs `sudo -v`
once before the execute loop. This caches credentials so all subsequent sudo
invocations proceed without prompting.

---

## State File

Location: `~/.local/share/stay-go/state.json`

```json
{
  "hostname": "my-machine",
  "updated_at": "2026-01-01T00:00:00Z",
  "nodes": {
    "packages/neovim": {
      "hash": "a1b2c3d4",
      "tracked_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:00:00Z"
    },
    "scripts//home/user/config/scripts/foo.sh": {
      "hash": "b2c3d4e5",
      "tracked_at": "2026-01-01T00:00:00Z",
      "updated_at": "2026-01-01T00:00:00Z"
    }
  }
}
```

The `hash` is a SHA-256 fingerprint of the node's config entry (plus file content for scripts).
Saved atomically (`.tmp` + rename) to prevent corruption.

---

## Adding a New Resource

1. Create `internal/resource/<name>/` with four files:
   - `<name>.go` — `Resource` struct, `New`, `Type() string`
   - `knowledge.go` — `GatherKnowledge` (return `[]engine.KnowledgeEntry`)
   - `plan.go` — `BuildPlan` (use `engine.DetermineAction` + `engine.StateRemovals`; set `NeedsSudo` on nodes that require privilege escalation)
   - `execute.go` — `Execute` (call `st.Set` or `st.Delete` on success)

2. Register in `cmd/stay-go/main.go`:
   ```go
   eng.Register(myresource.New(cfg, exec))
   ```

3. Add to `resourceLabel` and `canonicalOrder` in `engine/display.go`.

4. Add the YAML section to `config.go` and the `Config` struct.

5. (Optional) implement `engine.Updater` (`update.go` — `BuildUpdatePlan` +
   an ActionUpgrade branch in `Execute`) if the resource's items can be
   refreshed to a latest version; add a `lock:` field to the entry and set
   `PlanNode.Locked` from it. Keep `lock` out of the config hash.

The engine handles all orchestration — no engine changes required.

---

## Package Manager Support

Auto-detected in priority order:

| Manager | Distro | NeedsSudo | List command | Upgrade command |
|---------|--------|-----------|--------------|-----------------|
| paru | Arch (AUR) | no | `pacman -Qq` | `paru -Syu --noconfirm` |
| yay | Arch (AUR) | no | `pacman -Qq` | `yay -Syu --noconfirm` |
| pacman | Arch | yes | `pacman -Qq` | `pacman -Syu --noconfirm` |
| apt-get | Debian/Ubuntu | yes | `dpkg-query -f '${Package}\n' -W` | `apt-get upgrade -y` |
| dnf | Fedora/RHEL | yes | `rpm -qa --queryformat '%{NAME}\n'` | `dnf upgrade -y` |
| yum | CentOS/old RHEL | yes | `rpm -qa --queryformat '%{NAME}\n'` | `yum update -y` |
| zypper | openSUSE | yes | `rpm -qa --queryformat '%{NAME}\n'` | `zypper update -y` |
| apk | Alpine | yes | `apk info` | `apk upgrade` |
| xbps-install | Void | yes | `xbps-query -l` (custom parser) | `xbps-install -yu` |

**Removals never take configured packages.** `pacman -Rs`, `apt-get remove
--autoremove` and `apk del` also drop dependencies nothing needs any more —
including a configured package that was first pulled in as a dependency (so
it is still marked as one). Before every REMOVE, `protectConfigured` marks all
still-configured installed packages explicit via the manager's
`MarkExplicitCmd` (`<helper> -D --asexplicit` after `pacman -Qq` resolves
provides names, `apt-mark manual`, `apk add`). dnf instead removes with
`--setopt=clean_requirements_on_remove=False`; yum, zypper and xbps-remove
never remove unrequested dependencies by default. If marking fails, the
removal is aborted.

---

## CLI Flags

```
stay-go [flags]

  -c, --config string     path to config directory (default "config")
      --state  string     path to state file (default ~/.local/share/stay-go/state.json)
      --log-dir string    per-run log directory (default <state dir>/logs)
  -d, --debug             stream all command stdout/stderr to terminal
  -n, --dry-run           show plan without executing
  -u, --update [targets]  upgrade tracked resources to latest; bare = all,
                          =<resource> for a type, =<resource>/<name> for one item
                          (overrides lock: true). Value must be attached with "="
                          (bool-style flag, same as --show).
      --show [scope]      print tracked state and exit; scope: all (default),
                          packages, groups, users, services, scripts, variables

stay-go [-c dir] secret update <key> [value] [--all]
                          re-encrypt an existing secret with a new value in place
                          (cmd/stay-go/secret.go → secrets.Manager.SetInFile);
                          value prompted hidden when omitted, or read from stdin
```

---

## Code Conventions

- **SOLID**: Single Responsibility (one file per concern per resource), Open/Closed (add resources without modifying engine), Interface Segregation (Knowledger + Planner + NodeExecutor compose Resource)
- **DRY**: `engine.DetermineAction` and `engine.StateRemovals` are shared across all resources
- **Error wrapping**: always `fmt.Errorf("context: %w", err)`
- **No global state**: all config/state/exec passed explicitly
- **Atomic writes**: state file always written via tmp+rename
- **Non-interactive by default**: stdin → /dev/null; package managers get `-y`/`--noconfirm`
- **Interactive commands**: `commands` entries run with `AllowInteractive`. Without `sudo` they get a full PTY (live output, keystrokes forwarded). With `sudo` a new session would break sudo's per-terminal credential cache, so only stdout/stderr go through a PTY while stdin and the controlling terminal stay real; output is captured quietly and, if the process is silent for 10s (`executor.stallTimeout`), the captured tail (usually a prompt) is shown and output streams live from then on
- **Debug mode**: `--debug` streams stdout/stderr via `io.MultiWriter` (capture + terminal)
- **NeedsSudo**: set on PlanNode by resources that require privilege escalation; engine pre-auths once
- **Tests**: unit tests for config, state, engine graph logic, and package manager parsers; run with `make test`

---

## Development

```bash
make build      # compile to bin/stay-go
make dry-run    # show plan without executing
make debug      # run with full command output
make test       # run unit tests
make cover      # test coverage report
make install    # install to /usr/local/bin
```
