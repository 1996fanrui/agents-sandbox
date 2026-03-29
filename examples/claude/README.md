# Claude Example

Run Claude Code inside an AgentsSandbox container. Two variants are provided: inline Python parameters and declarative YAML config.

Prerequisites:

- the daemon is already running
- the host machine has valid Claude authentication (`~/.claude` credentials — the sandbox inherits them via the `claude` builtin tool)

## Inline parameters

```bash
uv run --directory sdk/python python ../../examples/claude/main.py
```

## YAML config

```bash
uv run --directory sdk/python python ../../examples/claude/main_yaml.py
```

Expected stdout shape:

```json
{"reasoning":"One plus one equals two.","result":2}
```
