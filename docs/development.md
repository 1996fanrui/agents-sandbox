# Development Guide

## Prerequisites

- Go toolchain
- Docker daemon
- systemd (Linux, for running agboxd as a user service)

## Build

Build both binaries:

```bash
go build ./cmd/...
# outputs: .build/agboxd  .build/agbox
```

## Running the Daemon

Build from source, install to `~/.local/bin/`, and start the systemd user service (Linux) or launchd agent (macOS):

```bash
./scripts/install_local.sh
```

Service management:

```bash
systemctl --user status agboxd
systemctl --user restart agboxd
journalctl --user -u agboxd -f
```

After code changes, re-run `./scripts/install_local.sh` to rebuild and restart.

## Proto Generation

Go and Python bindings are generated from `api/proto/service.proto` using pinned tool versions:

| Tool | Version |
|------|---------|
| protoc | v6.31.1 (release tag v31.1) |
| protoc-gen-go | v1.36.11 |
| protoc-gen-go-grpc | v1.6.1 |
| grpcio-tools | from `sdk/python` dev dependencies |

Regenerate bindings:

```bash
bash scripts/generate_proto.sh
```

The script downloads and caches protoc in `.local/protoc/` (project-local, git-ignored) and installs Go plugins in `.local/go-bin/`. CI runs `scripts/lints/check_proto_consistency.sh` automatically through `run_test.sh lint` to ensure checked-in bindings stay in sync with the proto source.

## Tests

```bash
./scripts/run_test.sh
```
