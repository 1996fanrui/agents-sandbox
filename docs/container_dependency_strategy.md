# Container Dependency Strategy

This document defines how `agents-sandbox` handles projections, dependencies, permissions, and network isolation.

The goal is a portable Docker-first runtime with a strict default security posture and no hidden product-specific branches.

## Core Rules

- Each sandbox gets its own dedicated Docker network.
- The primary container and all declared dependencies attach only to that sandbox network.
- Host network, shared bridge reuse, and Docker socket exposure to runtime containers are not supported.
- Only explicitly declared filesystem inputs may enter the sandbox.
- Invalid or unsafe runtime inputs must fail fast. The daemon must not silently widen mounts or fall back to weaker isolation.

## Filesystem Ingress Classes

The public northbound surface supports three distinct filesystem ingress classes:

| Class | Purpose | Examples | Default Behavior |
|-------|---------|----------|------------------|
| `mounts` | Bind explicit host paths into the sandbox | project tree, host config file, shared data directory | Disabled unless the caller explicitly declares each mount |
| `copies` | Copy explicit host files or trees into the sandbox | source snapshot, seed config, fixture data | Disabled unless the caller explicitly declares each copy |
| `builtin_resources` | Request daemon-defined resource shortcuts | `.claude`, `.codex`, `.agents`, `gh-auth`, `ssh-agent`, `uv`, `npm`, `apt` | Disabled unless the caller explicitly requests each resource |

Legacy `workspace`, `cache_projections`, and `tooling_projections` request fields still exist for protocol compatibility and daemon internals, but they are not the preferred public SDK surface.

## Built-in Tooling Projections

The imported session/auth runtime uses `/home/agbox` as its effective `HOME`, so the default container targets below are aligned with that path.

| Capability ID | Default Host Source | Default Container Target | Mode |
|---------------|---------------------|--------------------------|------|
| `.claude` | `~/.claude` | `/home/agbox/.claude` | read-write |
| `.codex` | `~/.codex` | `/home/agbox/.codex` | read-write |
| `.agents` | `~/.agents` | `/home/agbox/.agents` | read-write |
| `gh-auth` | `~/.config/gh` | `/home/agbox/.config/gh` | read-only |
| `ssh-agent` | `SSH_AUTH_SOCK` from the host environment | `/ssh-agent` | socket forwarding |
| `uv` | `~/.cache/uv` | `/home/agbox/.cache/uv` | read-write |
| `npm` | `~/.npm` | `/home/agbox/.npm` | read-write |
| `apt` | `~/.cache/agents-sandbox-apt` | `/var/cache/apt/archives` | read-write |

These are daemon-defined capabilities. Requests may select from this set but may not replace them with arbitrary host paths.

The minimal base runtime image asset is maintained under `images/base-runtime/`, and the HOME-aligned coding runtime image asset is maintained under `images/coding-runtime/`.

## Symlink Handling

The runtime applies different rules to different ingress modes.

### Generic mounts

`mounts` keep the original host tree shape:

- the mount source itself must be a real file or directory, not a symlink
- if the requested mount cannot be provided safely, the daemon fails fast
- the daemon does not silently rewrite a mount into a copy

### Generic copies

`copies` copy content into daemon-owned state before exposing it in the sandbox:

- the copy source itself must be a real file or directory, not a symlink
- exclude patterns are applied while populating the copied tree
- project-external or unreadable symlink targets are rejected instead of being auto-imported

### Built-in resources

`builtin_resources` resolve to daemon-defined host paths and targets:

- regular directories use bind mounts when safe
- directory trees that contain escaping symlinks fall back to daemon-owned shadow copies when supported
- socket resources such as `ssh-agent` are forwarded only when the host path is a real Unix socket

The daemon exposes the resolved result through `ResolvedProjectionHandle`, including whether the resource uses `bind` or `shadow_copy` and whether write-back remains enabled.

## Dependency Model

Dependencies are declared explicitly through `DependencySpec`.

| Field Area | Required Semantics |
|------------|--------------------|
| Identity | Each dependency has a stable dependency name inside the sandbox |
| Image and env | Defined explicitly by the caller or profile |
| Network alias | Scoped to the sandbox's dedicated network |
| Health contract | The daemon waits for declared readiness conditions before reporting the sandbox ready |
| Lifecycle ownership | Dependencies are created, stopped, and deleted with the sandbox |
| Init hooks | Dependency-owned init hooks may run inside the primary container after the dependency is ready |

Dependencies are generic runtime features. Product-specific config formats that map into these fields stay outside this repository.

## Startup Strategy

```mermaid
flowchart TB
    A[Create dedicated network] --> B[Materialize filesystem inputs and built-in resources]
    B --> C[Create dependency containers]
    B --> D[Create primary container]
    C --> E[Start dependencies when prerequisites are ready]
    D --> F[Start primary container]
    E --> G[Wait for dependency health conditions]
    F --> H[Run dependency-owned init hooks inside the primary container]
    G --> I[Emit dependency-ready events]
    H --> J[Emit sandbox ready event]
    I --> J
```

Startup rules:

- Dependencies may start in parallel once the required network and projections are ready.
- Parallel startup is a performance optimization only; it must not weaken isolation or readiness checks.
- Init hooks run only after their owner dependency is ready and the primary container is running.
- A failing dependency or failing init hook fails the whole materialization path and triggers cleanup of newly created runtime resources.

## Permissions and Runtime User Model

The runtime must execute under a non-root user inside the sandbox.

Required rules:

- Images may be built as root, but runtime command execution must happen as a non-root sandbox user.
- Bind-mounted writable paths must remain writable to that runtime user.
- The daemon must not rely on root-only behavior for normal exec, lifecycle, or dependency orchestration.

## Cleanup and Ownership

`agents-sandbox` owns cleanup for runtime resources in its namespace:

- primary containers
- dependency containers
- dedicated networks
- runtime-owned shadow-copy trees
- runtime-owned event and artifact files

The daemon must not require an external product database snapshot to decide whether a dependency or network belongs to a live sandbox. Ownership must be derivable from runtime state plus namespaced labels.
