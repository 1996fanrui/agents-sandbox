# Codex Example

Run Codex inside an AgentsSandbox container. Two variants are provided: inline Python parameters and declarative YAML config.

Prerequisites:

- the daemon is already running
- the host machine has valid Codex authentication (no `OPENAI_API_KEY` needed — the sandbox inherits the host's Codex credentials)

## Inline parameters

```bash
uv run --directory sdk/python python ../../examples/codex/main.py
```

## YAML config

```bash
uv run --directory sdk/python python ../../examples/codex/main_yaml.py
```

Expected stdout shape:

```json
{"reasoning":"One plus one equals two.","result":2}
```
