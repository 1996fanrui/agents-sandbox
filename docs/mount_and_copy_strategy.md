# Mount And Copy Strategy

This document defines the final filesystem ingress model for `agents-sandbox`.

## Scope & Core Rules

The sandbox supports four distinct concepts:

- `mounts` are the generic host-to-container mount capability, multiple `mounts` entries are supported.
- `copies` are the generic copy capability, multiple `copies` entries are supported.
- `builtin_resources` are daemon-provided built-in resources, not substitutes for generic `mounts` or `copies`.
- `mount` and `copy` are distinct semantics and must not be implicitly converted into each other.

## Example

```python
create_sandbox(
    image=...,
    mounts=[
        MountSpec(source="/host/a", target="/work/a", writable=True),
        MountSpec(source="/host/b", target="/opt/data", writable=False),
    ],
    copies=[
        CopySpec(
            source="/host/proj1",
            target="/workspace/proj1",
            exclude_patterns=[".git", ".venv", "node_modules", "__pycache__"],
        ),
        CopySpec(
            source="/host/proj2",
            target="/workspace/proj2",
            exclude_patterns=[".git", ".pytest_cache"],
        ),
    ],
    builtin_resources=[...],
)
```

## Mounts

`mounts` support arbitrary caller-defined host paths.

Each mount entry must define:

- `source`
- `target`
- `writable`

Rules:

- `source` may be a file or directory.
- `target` must be an absolute container path.
- Conflicting targets must fail fast.
- Invalid or unsafe sources must fail fast.

## Copies

`copies` are the generic copy capability.

Each copy entry must define:

- `source`
- `target`
- `exclude_patterns`

Rules:

- `source` may be a file or directory.
- `target` must be an absolute container path.
- `exclude_patterns` define which files or directories should be skipped during the copy.
- Copied content becomes sandbox filesystem content rather than a live host bind mount.
- Multiple copy entries may target different container paths in the same sandbox.

## Built-In Resources

`builtin_resources` remain available for daemon-provided built-in resources.

These resources are resolved against a container `HOME` contract, not against an arbitrary mount target. In this repository the default runtime home for the coding runtime image asset is `/home/agbox`, so the built-in resource targets below are intentionally aligned with that path.

When an imported runtime image needs host-backed authentication material:

- `HOST_UID` and `HOST_GID` let the entrypoint create or reuse a non-root runtime user whose file ownership matches the host identity.
- The container `HOME` must match the built-in resource target path. If the image uses a different home directory, authentication and config discovery for tools such as `claude`, `codex`, `git`, or `gh` can fail even when the directory is mounted.
- Ordinary auth relies on `HOST_UID` and `HOST_GID` to keep ownership correct and on `HOME` alignment so the tools read the expected credential paths.
- The corresponding image asset is maintained under `images/coding-runtime/`; the minimal foundation image asset lives under `images/base-runtime/`.

| Key | Host Source | Container Target | Access |
| --- | --- | --- | --- |
| `.claude` | `~/.claude` | `/home/agbox/.claude` | read-write |
| `.codex` | `~/.codex` | `/home/agbox/.codex` | read-write |
| `.agents` | `~/.agents` | `/home/agbox/.agents` | read-write |
| `gh-auth` | `~/.config/gh` | `/home/agbox/.config/gh` | read-only |
| `ssh-agent` | host `SSH_AUTH_SOCK` socket | `/ssh-agent` | socket forwarding |
| `uv` | `~/.cache/uv` | `/home/agbox/.cache/uv` | read-write |
| `npm` | `~/.npm` | `/home/agbox/.npm` | read-write |
| `apt` | `~/.cache/agents-sandbox-apt` | `/var/cache/apt/archives` | read-write |

Rules:

- They are daemon-defined resource shortcuts.
- They must not be treated as the generic mount API.
- Their internal materialization is daemon-defined.
- Their container targets must stay aligned with the runtime image's effective `HOME` so host-authored config and credential files remain discoverable.

## Symlink Rules

Symlink handling must prioritize correctness.

### Mount Path

`mount` keeps the original host tree shape. If the requested mount cannot be provided safely, the daemon must fail fast.

### Copy Path

`copy` preserves project-internal symlinks as symlinks. It must not silently follow a project-external symlink, and it must not silently dereference a symlink into a regular file or directory.

| Scope | `mount` | `copy` |
| --- | --- | --- |
| Project-internal symlink | Keep the symlink as-is. | Keep the symlink. Rewrite absolute host paths to equivalent relative targets when needed. |
| Project-external symlink | May be unusable inside the container unless the caller separately materializes the external target. | Reject by default. Do not silently pull extra host content into the sandbox. |

## Product Position

The supported final model is:

- generic `mounts`
- generic `copies`
- built-in `builtin_resources`

`mount` remains `mount`, and `copy` remains `copy`.
