# Why Not Built-in Agent Sandboxes?

Claude Code and Codex both have built-in sandboxes. Why not just use those?

## Built-in Sandboxes Are the Dilemma, Not the Solution

The README describes the core dilemma: grant full permissions (fast but unsafe)
or lock them down (safe but constant approvals). Built-in agent sandboxes don't
solve this — they **are** this dilemma.

Every agent CLI ships exactly two modes:

**Codex:** `workspace-write` (restricted) vs `danger-full-access` (unrestricted)

**Claude Code:** `default` permission mode (approval prompts) vs `--dangerously-skip-permissions` (all checks bypassed)

In both cases:

**If you lock down permissions** (Codex `workspace-write` / Claude Code `default`):
- Common dev actions trigger approval prompts — the agent keeps asking for
  permission instead of working
- Codex: `.git` is force-remounted read-only, so `git commit` / `git push` fail
- Codex: network is all-or-nothing — full host network or no network at all

**If you open up permissions** (Codex `danger-full-access` / Claude Code `--dangerously-skip-permissions`):
- The sandbox is effectively off
- The agent can read/write anything on the host
- You are back to "one bad command away from disaster"

No combination of settings escapes this.

## Side-by-Side Comparison

Agents Sandbox moves the isolation boundary from **per-command** to
**per-session**: the entire development environment runs inside an isolated
Docker container. The agent gets full, unrestricted permissions inside.

| | Restricted mode | Unrestricted mode | Agents Sandbox |
|---|---|---|---|
| *Codex flag* | *`workspace-write`* | *`danger-full-access`* | |
| *Claude Code flag* | *`default`* | *`--dangerously-skip-permissions`* | |
| `git commit` / `git push` | Blocked or needs approval | Works | Works |
| Install deps, run tests, build | Partial — may trigger approval | Works | Works |
| Network access | All (host exposed) or nothing | All (host exposed) | Internet yes, host blocked |
| Approval prompts | Frequent | None | None |
| Host filesystem | Full-disk readable | Fully read-writable | Invisible — only declared mounts |
| Other projects on machine | Readable | Readable and writable | Invisible |
| Credential exposure | Inherits host environment | Inherits host environment | Only explicit projections (SSH agent, gh auth) |
| Host safety | Partial | None | Full — host is untouched |
| Blast radius of a bad command | Limited writes, but host readable | Entire host machine | Only the disposable sandbox |

Restricted mode is safe but can't do the job. Unrestricted mode can do the job
but isn't safe. Agents Sandbox does both — full capability with full isolation.
