# Scenario index

One-line summaries. Files are loosely grouped; the names start with
the area they exercise.

## CLI surface

- `cli-help.md` — `--help` prints proper usage across serf-tui +
  serf subcommands (kata `hsmm`).
- `cli-sibling-binary.md` — serf-tui finds serf-hub next to itself
  via `os.Executable` + `EvalSymlinks` (kata `a4w6`).

## Authentication

- `auth-oauth-wins.md` — stored OAuth state takes precedence over
  `OPENAI_API_KEY` env var, daemon-side and hub-UI-side.
- `auth-device-autodetect.md` — `serf openai login` auto-picks
  device vs browser flow based on env / platform (katas `4f93712`,
  `8b7762c`).

## Spawn form (`/new`)

- `spawn-stale-model-cleared.md` — retired models in localStorage
  are detected against the harness list and cleared with an inline
  notice; sweep also drops sibling per-project blobs (katas `bvfh`,
  `hnvv`).
- `spawn-empty-prompt-blocked.md` — empty/whitespace prompt shows
  inline error, no spawn POST (kata `xj9j`).
- `spawn-picker-enter-noop.md` — pressing Enter inside the model
  picker search selects first match and prevents form submit (kata
  `t13x`).

## Session workspace

- `state-stuck-processing-display.md` — past session whose last
  turn errored shows `ended`/`error` not `processing` (kata `r6y9`).
- `reconnect-auto-resume.md` — killing the daemon and sending a
  new turn transparently spawns a fresh daemon and replays (katas
  `e465`, `t65c`, `ws5f`, `xcas`).
- `meta-flush-on-completion.md` — `meta.json` `turn_count` tracks
  committed exchanges across happy + error exits (katas `3tgv`,
  `ztne`, `wnfz`).

## Transcript / debug

- `transcript-endpoint-url.md` — api_call entries record
  `response.endpoint_url` (typed) + `response.raw.endpoint_url`
  (legacy) across all adapters (katas `v5pm`, `dyph`).

## Regression sweep (older surfaces)

- `credentials-page-displays-sources.md` — `/credentials` shows
  correct effective source per provider with env/file shadow
  badges.
- `index-sidebar-lists-projects.md` — hub home page sidebar
  enumerates projects + sessions.
- `search-finds-content-across-sessions.md` — `⌘K` overlay
  searches transcripts.

## End-to-end software development

These are the lightweight "does serf actually build software"
smoke tests. They spawn a real session against a real model,
use a run-specific `mktemp -d` for hermeticity, and verify the
output by running it.

- `dev-hello-script.md` — agent writes hello.py + runs it. Most
  basic write+exec loop. Verified live against `openai/gpt-5.5`.
- `dev-fix-broken-script.md` — agent reads, edits, and re-runs a
  syntactically-broken Python script. Exercises the
  read→edit→exec→verify flow that most real serf use looks like.

## Coverage gaps (worth writing)

- `reconnect-button-source-hub.md` — when a real source=hub
  diagnostic appears (different from the auto-resume case), the
  Reconnect & retry button shows + calls startTurn. Requires
  setting up a daemon-down state that produces a source=hub UI
  diagnostic, which is awkward — see kata `96pr` for why current
  legacy diagnostics stay classified as serf.
- `auth-device-poll-concurrent.md` — kata `24p1` (concurrent
  OAuth detection during device-code poll). Requires two
  parallel logins; need scripted CLI driver.
- `cli-device-code-flow.md` — full device-code login round trip
  with browser side. Requires browser action against
  `auth.openai.com` outside the hub. Worth scripting but separate
  from the hub surface.
- `tui-workspace-navigation.md` — serf-tui keyboard navigation
  + status display. Requires tmux fixturing.
- `compact-and-shutdown.md` — workspace compact + shutdown
  actions hit their RPC handlers cleanly. Worth a smoke test.

## Open katas surfaced while writing scenarios

- `96pr` — legacy diagnostics with stored source=serf never get
  reclassified (sharp edge).
- `6bdb` — serf-hub doesn't find sibling serf binary either
  (same shape as a4w6, sharp edge).

Both filed via `kata create`; not blocking.
