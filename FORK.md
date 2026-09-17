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
   lives on `feat/*` branches.
5. **Out of scope for now:** the Kaggle TPU provider and its time budget.
