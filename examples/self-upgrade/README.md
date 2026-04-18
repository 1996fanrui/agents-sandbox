# Self-Upgrade Example

The sandbox uses an `unless-stopped` restart policy: killing the main process
causes the container to restart automatically, picking up whatever is now
installed. This makes **install new version + kill main process** a sufficient
upgrade primitive — no external orchestration needed.

## How it works

```
Main process (sandbox command):  http-server@14.0.0   ← passive, just serves
Agent        (exec into sandbox): upgrade script       ← decides to upgrade
```

The upgrade flow:

1. Sandbox starts with `http-server@14.0.0` as the main process
2. Agent installs `http-server@14.1.1` and kills the main process
3. Container restarts automatically with the new version running

The main process is completely passive — it has no knowledge of the upgrade.
The agent is the sole decision-maker.

The example script also reads the version before and after to make the
transition visible in the output — that is for demonstration only, not part
of the upgrade flow itself.

## Run

```bash
uv run --directory sdk/python python examples/self-upgrade/main.py
```

Expected output:

```
Sandbox <id> created, waiting for service to start ...
[agent] version before upgrade: v14.0.0
[agent] upgrading http-server 14.0.0 -> 14.1.1 and restarting ...
[agent] waiting for container restart ...
[agent] version after upgrade:  v14.1.1
Sandbox <id> deleted.
```
