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

## Tests

```bash
./scripts/run_test.sh
```
