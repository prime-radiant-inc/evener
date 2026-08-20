# Agentic Test Scenarios

These are end-to-end test cards written for an AI agent (Claude, Codex,
etc.) to execute. They are **not** Playwright/Selenium/expect scripts.
The reader is expected to use general tools — a browser, a terminal,
file readers, log inspectors — and adapt when the surface changes a
little. The intent is high-level enough that a small UI shuffle
shouldn't invalidate the test, but precise enough that two agents
running the same scenario should arrive at the same verdict.

## What goes in a scenario

Each `.md` file under `test/scenarios/` is one scenario. The shape is
loosely:

- **What this covers** — one or two lines linking the kata IDs / commits
  the test exercises. If something else broke this, it should be
  caught here.
- **Pre-state** — what needs to be true before the agent starts.
- **Steps** — narrative actions described by intent, with concrete
  commands or tool calls. Reference real file paths, real UI labels.
  Avoid brittle selectors (`#nav > li:nth-child(3)`); prefer labels
  the user sees.
- **Expected** — what the agent should observe at each step. Spell
  out the falsification condition: "if you see X instead, the test
  fails."
- **Cleanup** — what to restore so reruns are hermetic.
- **Sharp edges** — known footguns, timing dependencies, ordering
  caveats noted during recording.

If a scenario is genuinely simple, half of these can be one line each.
Don't pad.

## Citing a contract

Cite a place in a file by a name that lives **inside** the file, never
by a line number. A line number is invalidated by any edit to the file,
silently: the citation still parses, still looks precise, and now points
at an unrelated line.

- **Prose (`.md`)** — the doc path in backticks, followed immediately by
  the quoted section heading and/or the quoted phrase you are leaning
  on: ``` `docs/job-control.md` "Nested jobs" ("the shell owner's
  durable record is authoritative over a forwarded copy") ```. Quotes
  may be separated by punctuation and the word `and`; the run ends at
  the first other word, so ordinary prose quotes later in the
  paragraph are not anchors.
- **Code (`.go`)** — the file path with the symbol appended after a
  `#`: `` `agent/tree_counter.go#defaultMaxConcurrentDelegateTurns` ``.
  That is the spelling `scripts/fuzz/run-fuzz.sh` already uses for the same
  idea. The symbol may be a func or method, a type, a package-level or
  grouped `const`/`var`, a struct field, or an interface method. The
  path may be abbreviated to the part that reads well
  (`internal/hubcore/tree.go`) as long as the suffix is findable.

Citation discipline is by convention: every quoted anchor must appear in
the doc it names, every cited source path must resolve, and every
`#symbol` must be declared. (The audits that enforced this were retired
in the 2026-08 tooling standardization; the rules still stand — a
renamed heading, a moved package, or a renamed function should be caught
in review instead of leaving a citation that points at nothing.)

Cite Go code with a symbol anchor (`#Symbol` after the path), never with
a line number — a line number is invalidated silently by any edit above
it. (The one-shot migration tool that swept the corpus onto symbol
anchors is retired; see git history for `scenario-cite-migrate.sh`.)

## How to run

Most scenarios assume:
- a `evener-hub` you built and started yourself, on an isolated `$HOME`
  and a free port. Never Jesse's real hub or his port `9180`. See
  "Setup checklist" in `docs/developing-evener/agentic-testing.md` for the exact recipe —
  a card that says nothing about the hub inherits that default.
- the auth token at `$HOME/.evener/auth-token` (that isolated `$HOME`,
  never Jesse's real one)
- the `superpowers-chrome:browsing` skill available for browser steps
- `tmux` and the Bash tool available for CLI steps
- `~/go/bin/kata` for issue lookups

When a scenario needs different state (e.g. no daemon running, OAuth
signed out), it says so in **Pre-state** and provides commands to
reach that state.

**See `docs/developing-evener/agentic-testing.md`** for practical patterns and recipes:
hermetic workdirs, the AGENTS.md pacing trick for keeping a turn in
`processing`, the synchronous-vs-async DOM assertion shape for
optimistic-rendering scenarios, tmux form-fill conventions
(`BTab` / `C-u` / `-l`), stderr-probe debugging when an assertion
fails, the rebuild matrix across daemon / hub / web / TUI, and the
over-specification trap (when a scenario describes a path that
production gating prevents).

## Filing failures

When a scenario fails, file a kata via `~/go/bin/kata create <title>
--label bug --body "scenario <id> failed: <observation>"`. Link the
scenario file path in the body.

## Scenario index

See individual files. Names are loosely `<area>-<short-slug>.md`.
