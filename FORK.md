# This fork

`jt226ub/agent-orchestrator` is a fork of `Untrivial-ai/agent-orchestrator` that adds, in
order: Antigravity plan-quota capacity, role profiles, workflow templates, and the Drive
contract as the first layer of every session's rules. The design and the order of work are
in the LLM Drive Skill repository, `modules/ao-fork/DESIGN.md` (decision D22 there).

## Standing rules (the user's, 2026-09-17)

1. **Mimic upstream.** Every addition follows the structure and approach of the nearest
   upstream feature — the same package boundaries (`domain` → `ports` → `service` →
   `httpd/controllers` → CLI mirror DTOs → renderer), the same naming, the same test style
   (table tests beside the code, fakes over network), the same generated-artifact discipline
   (`npm run api`, `npm run sqlc`, commit `openapi.yaml` and `schema.ts` with the Go). Read
   the upstream feature first; write ours the way theirs is written.
2. **Contribution-ready.** Each addition is built as if it were a PR to upstream:
   `AGENTS.md` and `CONTRIBUTING.md` govern (surgical changes, conventional commits, one
   issue per PR, CI commands run locally before handing over, intentional omissions
   explained). Anything that could plausibly be accepted upstream is proposed there first
   on the issue or Discord, as their process asks, before we carry it only here.
3. **Prior art before code.** Before building a feature, search upstream issues, PRs
   (merged and closed, with the reasons) and forks for the same work, and follow the
   accepted shape or the stated reason for rejection.
4. **Upstream stays the engine.** No rewrites of the daemon, the Kanban or the adapters;
   additions are new files or small named seams so `git rebase upstream/main` stays cheap.
   `upstream` is the remote for `Untrivial-ai/agent-orchestrator`; `main` tracks it, work
   lives on `feat/*` branches. `fork/main` is `main` plus every landed `feat/*` branch
   merged in order; a branch that needs an earlier one starts from `fork/main` and its PR
   targets `fork/main`, so each PR's diff stays one feature.
5. **Out of scope for now:** the Kaggle TPU provider and its time budget.

## Using the fork (from 2026-09-21)

The fork is the user's day-to-day Agent Orchestrator: it replaced the stock app and the
LLM Drive Skill sidecar (skill repo decisions D28, D29).

**What `fork/main` carries**, upstream `main` plus, in order: PR #1 Antigravity plan
capacity; #2 role profiles; #3 workflow templates; #4 New Task profile chip; #5 profile
quota admission; #6 Antigravity account management; #7 Claude Code subscription and usage;
#8 default profiles and editable rules files in Settings; #9 the daemon drops an inherited
`CLAUDE_CODE_CHILD_SESSION` marker at boot; #10 project saves and template binds accept
default profile names; #11 a profile's permission mode beats a parent orchestrator's inherited
mode (request > profile > parent conversation > project default); #12 `ao session cleanup`
leaves a worktree that a live session still uses; #13 `ao session tail` reads a session's
terminal scrollback; #14 the pty-host queues keystrokes so a multi-line `ao send` to a TUI
session is delivered whole (before it, everything after the first kilobyte could be lost
while the agent was still consuming the paste); #16 worker turn signals: a daemon-authored
`[AO] Worker <id> ... finished its turn` message to the owning orchestrator (queued while it is
busy), `ao session wait`, `no_signal` in `ao session ls`, orchestrator-spawned workers default
to chat mode when the agent has a Chat driver, and plain-text `ao session tail`; #17 an agy Stop hook with
`fullyIdle:false` (the agent parked behind a background command) keeps the session active instead of
reading as idle. The design and decisions live in the LLM Drive
Skill repository, `modules/ao-fork/DESIGN.md` and `DECISIONS.md` (D22–D31).

**Installed versus merged.** The installed build (2026-09-21, late evening) carries PRs #1–#17, the
whole of `fork/main`. The turn-end notice was verified live: a worker spawned with the orchestrator's
`AO_SESSION_ID` reached its chat as an automation message the second the worker's turn ended. Remember for later rebuilds that PR #14 lives in the `ao pty-host`
process each terminal session keeps alive across daemon restarts: a session started before a
rebuild keeps the old host until it is killed and restored (`ao session kill` then
`ao session restore`). Known rough edges: `ao session wait` on a worker spawned seconds ago can accept the seed
idle before the agent's first hook arrives (the session view does not expose `firstSignalAt`;
exposing it is the follow-up), and the notice quotes a worker's last message only for harnesses
whose Stop hook carries it (Claude Code does, Agy does not).

**The installed build.** `frontend/out/Agent Orchestrator-darwin-arm64/Agent Orchestrator.app`,
built from `fork/main` with `npm run package` (which runs `build:daemon`, `build:tmux`,
`browser-runtime:prepare` and `build:acp-runtime` first), copied to `/Applications/Agent
Orchestrator.app`. The stock 0.13.0 app it replaced is kept at
`~/.ao/staging/Agent Orchestrator (stock 0.13.0).app`. The fork's `ao` CLI (the same binary
the app bundles) is at `~/.local/bin/ao`. The build is unsigned; it runs because it was
built locally, not downloaded. Auto-update stays off (`~/.ao/update-settings.json`), so
nothing replaces the build behind the user's back.

**Data.** The app uses the default data dir `~/.ao/data`: `ao.db`, worktrees, prompts, and
the daemon-wide texts the fork adds — `rules/contract.md` (the Drive contract),
`rules/orchestrator.md` and `rules/worker.md` (the role layers), `rules/<profile>.md`, and
`profiles.json` (default profiles: `orchestrator`, `flash-coder`, `pro-expert`,
`deepseek-expert`). Edit them in Settings → Profiles, or through
`PUT /api/v1/settings/rules/{name}` and `PUT /api/v1/settings/profiles`. Every session's
standing rules are layered contract → role file → project rules → profile file → template
plan. `deepseek-expert` needs `ANTHROPIC_AUTH_TOKEN` added to its environment before it can
run; the key is deliberately not stored by the tooling.

**To rebuild after a change.** Two quirks of this machine first: with Node 26 the
packager's `extract-zip` exits silently mid-extraction (no bundle, exit 0), so the
Electron packaging step runs under the Node 22 that `build:acp-runtime` downloads; and the
tmux source build cannot run from a path with a space (`Coding Projects`), so the tmux
cache under `.cache/bundled-tmux/` was seeded once with the stock app's identical
tmux 3.5a and is reused by every later build (delete it only if the pinned versions in
`scripts/build-tmux.mjs` change).

```bash
cd "/Volumes/External Data/Coding Projects/agent-orchestrator"
git checkout fork/main && git pull --ff-only
cd frontend && npm run prepackage
PATH="$PWD/resources/acp-runtime/node/bin:$PATH" npx electron-forge package
osascript -e 'quit app "Agent Orchestrator"'
rm -rf "/Applications/Agent Orchestrator.app"
cp -R "out/Agent Orchestrator-darwin-arm64/Agent Orchestrator.app" /Applications/
cp daemon/ao ~/.local/bin/ao
open -a "Agent Orchestrator"
```

Launch the app from Finder or `open`, never from inside a Claude Code session's shell: a
daemon started there inherits that session's environment (PR #9 strips the one marker that
mattered, but the rest still leaks).

**To go back to the stock app:** quit, move the backup from `~/.ao/staging` back to
`/Applications`, and keep `~/.ao/data` aside (the fork's migrations 0148–0149 are unknown to
stock).

**Open items:** a second Google account for a live Antigravity account switch; the Codex
setup as a `codex-*` profile; the DeepSeek token; rebasing `fork/main` on upstream `main`
(step 9 of the design note); the Kaggle TPU provider stays out of scope.
