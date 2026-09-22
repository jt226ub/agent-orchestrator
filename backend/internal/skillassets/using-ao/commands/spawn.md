# ao spawn

Spawn a worker agent session in a registered project, or a standalone workspace session that is not tied to a project.
Standalone sessions run in an AO-managed directory. Register a project first with `ao project add` for project-scoped sessions.

## Syntax

```
ao spawn [flags]
```

## Flags

| Flag | Meaning | Default / Required |
|---|---|---|
| `--branch string` | Branch for the session worktree | `ao/<session-id>/root` |
| `--claim-pr string` | Immediately claim an existing PR for the spawned session | - |
| `--harness string` | Agent harness to use (see list below) | Project `worker.agent`; required if the project has none |
| `--issue string` | Issue id to associate with the session | - |
| `--profile string` | Role profile from the project's `profiles` map: its harness, agent config, environment and rules file apply to this session; `--agent` and `--model` still override it | Project `worker.profile` / `orchestrator.profile` when set |
| `--name string` | Display name shown in the sidebar (max 100 characters) | Required |
| `--mode string` | Session interface: `chat` or `tui`. A worker spawned from an orchestrator session defaults to `chat` when its agent has a Chat driver (only a chat session can be steered mid-turn); otherwise the daemon default applies | Daemon default (`tui`) |
| `--no-takeover` | Refuse if another active session owns the claimed PR (requires `--claim-pr`) | - |
| `--project string` | Project id to spawn the session in | Optional when `--standalone` is used; defaults to `AO_PROJECT_ID` or the current repo's registered project |
| `--standalone` | Spawn a projectless worker session in an AO-managed directory | Disabled when `--project` is set |
| `--prompt string` | Initial prompt for the agent | - |

`--agent` is an alias for `--harness`.

Available harnesses: `claude-code`, `codex`, `aider`, `opencode`, `grok`, `droid`, `amp`, `agy`, `crush`, `cursor`, `qwen`, `copilot`, `goose`, `auggie`, `continue`, `devin`, `cline`, `kimi`, `kiro`, `kilocode`, `vibe`, `pi`, `autohand`.

## Examples

```bash
# Spawn a worker for issue 142 in the agent-orchestrator project
ao spawn --project agent-orchestrator --issue 142 --name "fix-session-leak" --prompt "Fix the session leak described in issue 142. Branch off upstream/main."
```

```bash
# Spawn a worker and immediately claim an open PR
ao spawn --project agent-orchestrator --name "review-pr-88" --claim-pr 88 --harness claude-code
```
