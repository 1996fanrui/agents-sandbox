# Container Dependency Strategy

This document defines how `agents-sandbox` materializes everything inside and around the sandbox container before it becomes READY: filesystem ingress, built-in resources, services, permissions, network isolation, and cleanup.

## Core Rules

- Each sandbox gets its own dedicated Docker network. Host network, shared bridge reuse, and Docker socket exposure are not supported.
- Only explicitly declared filesystem inputs may enter the sandbox. Invalid or unsafe inputs must fail fast.
- Runtime orchestration uses structured Docker Engine API calls through the daemon's shared runtime client instead of Docker CLI subprocesses. This keeps interactions on typed API surfaces and removes text-parsing dependencies.

## Filesystem Ingress Classes

The public surface supports three distinct ingress classes. `mounts`, `copies`, and `builtin_tools` are separate concepts because they have different security and lifecycle behavior: a bind mount keeps a live host path, a copy materializes daemon-owned content, and a built-in resource is a daemon-defined shortcut with its own validation and resolution rules.

| Class | Purpose | Default |
|-------|---------|---------|
| `mounts` | Bind explicit host paths into the sandbox | Disabled unless caller declares each mount |
| `copies` | Copy host files/trees into daemon-owned state | Disabled unless caller declares each copy |
| `builtin_tools` | Daemon-defined resource shortcuts | Disabled unless caller requests each resource |

Mount and copy must not be implicitly converted into each other. Mounts and copies require absolute container targets and real host file or directory sources. Conflicting targets must fail fast.

## Built-in Tooling Projections

The imported session/auth runtime uses `/home/agbox` as its effective `HOME`, so the default container targets are aligned with that path.

Tools are the user-facing names passed in `builtin_tools`. Each tool resolves to one or more named mounts; multiple tools may share a mount and the daemon deduplicates by mount ID before materializing.

| Tool | Resolved Mounts (Host Source → Container Target, Mode) |
|------|--------------------------------------------------------|
| `claude` | `~/.claude` → `/home/agbox/.claude` (rw), `~/.claude.json` → `/home/agbox/.claude.json` (rw), `$XDG_RUNTIME_DIR/pulse/native` → `/pulse-audio` (socket forwarding, when host socket exists) |
| `codex` | `~/.codex` → `/home/agbox/.codex` (rw), `~/.agents` → `/home/agbox/.agents` (rw) |
| `git` | `SSH_AUTH_SOCK` → `/ssh-agent` (socket forwarding), `~/.config/gh` → `/home/agbox/.config/gh` (read-only), `~/.ssh/known_hosts` → `/home/agbox/.ssh/known_hosts` (read-write) |
| `uv` | `~/.cache/uv` → `/home/agbox/.cache/uv` (rw), `~/.local/share/uv` → `/home/agbox/.local/share/uv` (rw) |
| `npm` | `~/.npm` → `/home/agbox/.npm` (read-write) |
| `apt` | `~/.cache/agents-sandbox-apt` → `/var/cache/apt/archives` (read-write) |
| `opencode` | `~/.config/opencode` → `/home/agbox/.config/opencode` (rw), `~/.local/share/opencode` → `/home/agbox/.local/share/opencode` (rw) |

Notes:
- `codex` mounts both `~/.codex` and `~/.agents`; `~/.agents` is the shared agents state directory.
- `git` bundles SSH agent forwarding, GitHub CLI auth, and SSH known-hosts; requesting `git` is equivalent to requesting all three.
- `uv` mounts both the package cache and the data directory holding Python interpreters and globally installed tools.
- `claude` includes optional PulseAudio socket forwarding for voice support; the mount is silently skipped when the host socket does not exist.

These are daemon-defined capabilities. Callers may select from this set but may not replace them with arbitrary host paths. The minimal base runtime image asset is under `images/base-runtime/`; the HOME-aligned coding runtime image is under `images/coding-runtime/`.

When an imported runtime image needs host-backed authentication material, `HOST_UID` and `HOST_GID` let the entrypoint create or reuse a non-root runtime user whose file ownership matches the host identity. The container `HOME` must match the built-in resource target path (`/home/agbox`).

## Copy Exclude Patterns and Symlink Handling

### Generic mounts

- The mount source must be a real file, directory, or Unix socket, not a symlink.
- If the mount cannot be provided safely, the daemon fails fast.
- The daemon does not silently rewrite a mount into a copy.

### Generic copies

- The copy source must be a real file or directory, not a symlink.
- Copies are injected into the container via Docker's `CopyToContainer` API (tar stream) between container create and start — no host-side shadow directories are needed.
- `exclude_patterns` are applied while building the tar stream (see [Declarative YAML Config](declarative_yaml_config.md) for the YAML field reference).
- Project-internal symlinks are preserved as symlinks in the tar archive.
- Project-external or unreadable symlink targets are rejected instead of being auto-imported.

### Built-in resources

- Regular directories use bind mounts when safe.
- Directory trees with escaping symlinks are bind-mounted directly from the host path.
- Socket resources such as `ssh-agent` and `pulse-audio` are forwarded only when the host path is a real Unix socket.

## Companion Container Model

For companion container field definitions and usage scenarios, see [Companion Container Guide](companion_container_guide.md).

## Startup Strategy

```mermaid
flowchart TB
    A[Create dedicated network] --> B[Materialize filesystem inputs and built-in resources]
    B --> S[Create companion containers]
    B --> D[Create primary container]
    S --> P[Start all companion containers in parallel with primary]
    D --> F[Start primary container]
    P --> Q[Emit COMPANION_CONTAINER_READY or COMPANION_CONTAINER_FAILED per companion]
    P --> H[Run post_start_on_primary hooks after companion healthy]
    F --> R[Emit sandbox ready event]
```

Startup rules:
- All companion containers start concurrently with the primary container and do not block sandbox READY.
- `post_start_on_primary` requires `healthcheck` to be defined on the companion container.
- Companion container startup, health inspection, and hook execution must stay on structured Docker API paths.

## Permissions and Runtime User Model

The runtime must execute under a non-root user inside the sandbox. Bind-mounted writable paths must remain writable to that runtime user. The daemon must not rely on root-only behavior for normal exec, lifecycle, or companion container orchestration. Exception: on Linux, `CAP_NET_ADMIN` is required for nftables-based sandbox network isolation (see [Isolation and Security](isolation_and_security.md)).

## Cleanup and Ownership

`agents-sandbox` owns cleanup for resources carrying the `io.github.1996fanrui.agents-sandbox.*` label namespace: primary containers, companion containers, dedicated networks, and event/artifact files.

Docker objects without these labels are never inspected, stopped, or removed by the daemon. Ownership must be derivable from runtime state plus namespaced labels without requiring an external product database snapshot. Cleanup continues on daemon-owned contexts rather than request-scoped cancellation.

## Architectural Exception: agent commands

The rule that all Docker access goes through the daemon's structured runtime client has one deliberate exception: the CLI agent commands in interactive mode.

These commands — the per-type top-level entries (`agbox claude`, `agbox codex`, `agbox openclaw`, `agbox paseo`) and the custom-command entry (`agbox agent --command "..."`) — create a sandbox via gRPC, wait for it to become READY, then — depending on the session mode — either attach directly or delegate to the daemon's exec model:

- **Interactive mode** (default): Calls `docker exec -it` directly from the CLI process to attach an interactive TTY session into the primary container. On exit, the sandbox is deleted via gRPC.
- **Long-running mode** (`--mode long-running`): Creates the sandbox with the service process as the container primary command (under tini). The CLI waits for sandbox READY and detaches. Does not use `CreateExec` for the service process. On exit, the sandbox is not deleted and must be managed manually.

Two agent definition surfaces are supported:
- **Pre-registered tool:** `agbox claude`, `agbox codex`, `agbox openclaw`, `agbox paseo` — each is its own top-level command that uses the built-in command and builtin-tool defaults from the agent tool registry. The old `agbox agent <type>` form has been removed.
- **Custom command:** `agbox agent --command "aider --yes" --workspace /path/to/project` — the only remaining use of `agbox agent`; the user provides the full command and specifies the workspace directory.

**Why the interactive-mode exception is necessary:**

The daemon's exec model is designed for non-interactive batch execution. Adding interactive TTY support at the daemon protocol layer would require gRPC bidirectional streaming plus in-daemon PTY management — significant complexity with little benefit beyond these commands. Calling `docker exec -it` directly from the CLI is simpler, keeps the daemon out of the TTY path, and is equivalent to what a user would do manually.

**Known constraint:** The CLI's `docker exec` call (interactive mode only) and the daemon's Docker Engine API calls must target the same Docker daemon. If `DOCKER_HOST` or `DOCKER_CONTEXT` differs between the environment where `agboxd` was started and the shell running the agent command, the exec may land on the wrong target. This is rarely a problem when `agboxd` runs as a user process sharing the shell environment.

**Scope:** The direct Docker access exception is strictly limited to the agent commands in interactive mode. Long-running mode and all other CLI commands use the daemon's gRPC API exclusively.
