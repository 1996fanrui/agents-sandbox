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

agboxd runs as a systemd user service. The service file uses
`scripts/agboxd_start.sh`, which builds and starts the daemon automatically.

```bash
# Start / stop / restart
systemctl --user start agboxd
systemctl --user stop agboxd
systemctl --user restart agboxd

# Status and logs
systemctl --user status agboxd
journalctl --user -u agboxd -f
```

## Rebuilding and Redeploying

One-liner to rebuild agboxd and restart the service:

```bash
go build -o .build/agboxd ./cmd/agboxd && systemctl --user restart agboxd
```

agbox (CLI) is stateless — just rebuild and use it directly:

```bash
go build -o .build/agbox ./cmd/agbox
```

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
