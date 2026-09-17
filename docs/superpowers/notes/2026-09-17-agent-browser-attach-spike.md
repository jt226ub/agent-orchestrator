# Spike record: agent-browser 0.33.1 attach mode against headless Chromium

Run 2026-09-17 on this workstation (x86_64, Google Chrome stable 150.0.7871.128,
`--headless=new`). Bookworm-image-level confirmation (Debian `chromium` package)
remains part of Task 10's in-image smoke; everything here used the closest local
binary.

## Verdict: GO. Env-only attach works exactly as the Electron bridge implies.

## What was verified

1. Pinned binary: `agent-browser-linux-x64` v0.33.1, sha256
   `6e04d06605c4ca62da36e3263086e0f7ceae808b55508de2c3958d4b7fe430aa` (matches
   `prepare-agent-browser.mjs`).
2. Headless Chrome with `--remote-debugging-address=127.0.0.1
   --remote-debugging-port=9222`: `/json/version` returns
   `webSocketDebuggerUrl: ws://127.0.0.1:9222/devtools/browser/<uuid>`.
3. **`AGENT_BROWSER_CDP` accepts the browser-level ws URL directly.** With the
   full env contract (SESSION/NAMESPACE/CONFIG/CONTENT_BOUNDARIES=1/
   MAX_OUTPUT=50000/IDLE_TIMEOUT_MS=300000/AUTO_CONNECT=0):
   - `stream disable` exits 0 ("Streaming disabled").
   - `open https://example.com --json` succeeds.
   - `snapshot --interactive --compact --json` succeeds.
   - `screenshot <path> --json` writes a real PNG (780x437).
   - `tab list --json` and `get url --json` succeed.
4. **Two namespaces attach to the same Chromium concurrently** (`spike` and
   `spike2` in sequence over one browser). Independent namespace state worked;
   this is exactly the VM model (engine + future screencast supervisor).
5. **`--remote-debugging-port=0` DOES write
   `<user-data-dir>/DevToolsActivePort`** containing `<port>\n<path>` (observed:
   `40785` + `/devtools/browser/<uuid>`), and the endpoint derived from it
   (`ws://127.0.0.1:40785/devtools/browser/...`) served a full command
   round-trip. The pre-picked-ephemeral-port fallback in the plan is NOT
   needed; the primary DevToolsActivePort approach stands.
6. Memory, blank page, one tab, Chrome stable: summed RSS across 14 processes
   is 1253 MiB but double-counts shared pages; honest **PSS is ~330 MiB across
   11 processes**. Spec's 250-450 MiB estimate holds for the headless engine
   (this excludes the agent-browser daemon, which is short-lived per command).

## Envelope shape (confirms `parseAgentBrowserJSON` assumptions)

```json
{"_boundary":{"nonce":"...","origin":"https://example.com/"},
 "data":{...},"error":null,"success":true}
```

- `_boundary` rides at the ENVELOPE top level, not inside `data`.
- `data.snapshot` is the compact text; `data.refs` is an **object keyed by
  ref id** (`{"e1":{"role":"heading","name":"Example Domain"}}`), never an
  array. The Go act-matcher port must consume the map (matching
  `parseSnapshotEntries` in `browser-act-matcher.ts`, which also rejects
  arrays).
- `data.tabs` from `tab list` IS an array:
  `[{tabId, title, url, active, label, type}]` (`label` may be JSON null).
- Every response's `data` carries a `lifecycle` object (launch/reuse/restore
  state). The Electron host strips it for `snapshot` (keeps `text`/`refs`)
  and passes it through on other verbs; the cloud service mirrors that split.

## Deviations and notes for executors

- Local test binary was `google-chrome-stable`, not Debian `chromium`; the
  `--headless=new` flag was accepted as-is.
- `stream disable` output was "Streaming disabled" here; the already-disabled
  tolerance path ("Streaming is not enabled for this session") was not
  triggered in this run but the exact marker string is pinned from source.
- RSS-sum via `ps -C` undercounts process names longer than 15 chars
  (`google-chrome-st`); use `pgrep -f` + `/proc/<pid>/status` or
  `smaps_rollup` (PSS) as this note did.
