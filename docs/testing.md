# Testing

## Test Reliability Policy

The default test suite must be deterministic. Running `make test` or
`go test ./...` must not depend on provider credentials, model availability,
network access, quota, current model behavior, wall-clock timing outside the
process, or ambient developer machine state.

Use this boundary when adding or fixing tests:

- If the test verifies Serf plumbing, use a scripted provider at the LLM
  boundary and exercise the real Serf code below it. Examples: CLI flag/config
  wiring, appwire RPC, daemon input queues, session loops, tool execution,
  transcript writes, event emission, goal continuation routing, hook dispatch,
  and prompt composition.
- If the test verifies model behavior, keep it live. Examples: whether a
  specific model chooses a tool from a natural-language instruction, follows an
  output contract, supports a provider feature, honors a live API wire shape, or
  behaves well across multi-turn goal prompts.
- Live tests must be explicitly opt-in with a `SERF_*_E2E=1` or
  `SERF_LIVE_TESTS=1` style environment variable in addition to the provider
  credential. A provider key by itself must never make the default suite issue
  live requests.
- Do not use sleeps, polling races, or large string snapshots to prove behavior
  when a structured event, state field, file result, or fake transport script can
  prove the same contract.
- Do not mock Serf internals to make a test pass. Keep the fake boundary at an
  external dependency: LLM provider, network server, filesystem root, clock, or
  process launcher.

When a test needs a model, name that as the behavior under test and keep it out
of the default suite. When the model is only a way to drive Serf, replace it with
a scripted `llm.ProviderAdapter` response and assert the Serf side effects.

## Post-Merge Gate

Run the post-merge gate serially from the repository root:

```sh
make lint
make build
ROOT_FULL=1 make test
```

`ROOT_FULL=1` makes the protected first wave run the full root Go suite instead
of its ordinary `-short` form. `make test` owns the remaining Go module,
script self-test, and frontend streams; the self-test stream starts only after
the protected root wave and runs alongside wave two. The standalone
`go test ./...` gate is therefore duplicate coverage and must not be appended
to this stack. Ordinary `make test` remains the default local command and keeps
the root wave in short mode unless `ROOT_FULL=1` is explicitly set.

## Proving a Type Survives a Round Trip

When two code paths must agree about a struct — a decoder and a
projector, a live path and a reload path — a hand-written fixture proves
only that today's fields survive. A field added next month passes,
because nothing in the test knows it exists.

Build the fixture by walking the **type** with reflection instead. Fill
every field with a distinguishable value, decode the same bytes through
both paths, and report divergent fields by name.

`cmd/serf-hub/app_threadread_decode_fidelity_test.go` is the worked
example. It catches a field added tomorrow with no test edit and no new
fuzz seed — verified by adding a synthetic field and watching the test
name it unprompted.

The gotchas in the fixture builder are all in the leaves: `time.Time`
needs whole seconds, `json.RawMessage` must contain valid JSON, floats
must be integral to survive widening through `any`, and an unhandled
`reflect.Kind` must fail loudly rather than skip — a builder that
silently skips a kind is a test that silently stops covering it.

Two corollaries:

- **Always prove it by mutation.** Route one path through a struct that
  omits a field and confirm the test names that field. A drift test that
  has never failed is a decoration.
- **Delete the round-trip test when the second path dies.** Once a
  mirror type is gone, both sides of its round-trip test are the same
  `json.Unmarshal` and it cannot fail. Keeping it leaves a comment
  claiming coverage that no longer exists.

## The Two Browser Guards, and Why There Are Two

jsdom evaluates no cascade and reports zero for every box, so an entire class
of frontend defect is structurally invisible to `vitest`. Two checks in
`cmd/serf-hub/frontend` cover it, and the split matters:

- **`npm run layoutguard`** measures HAND-AUTHORED markup against the real
  `tokens.css` and component stylesheets, in headless Chrome. Cheap — static
  files, no build. Right for "does this CSS rule still hold its box".
- **`npm run overflowguard`** renders the REAL Session pane through the REAL
  reducer and asserts nothing inside it scrolls sideways, at four widths.

The second exists because the first could not have caught the bug that
prompted it. Hand-authored markup freezes whatever was current when the case
was written, so restoring the old glyph would have left the guard green while
the app broke. Both are manual pre-merge checks, not wired into `make lint`.

Three traps, each of which produced a false-green here. They are listed in the
order they were found, because each was hiding the next.

**A fixture must use REGISTERED types.** The overflow harness seeded item types
`"thinking"` and `"notification"`. Neither is registered — the set is
`agentMessage / reasoning / steering / systemMessage / userMessage / warning`
(`grep registerItemRenderer`) — so both fell through to `RawItemView`, the
debug fallback. The guard measured a debug renderer for two of five items and
reported PASS. **An unregistered type does not throw; it silently renders
something else.**

**`scrollWidth > clientWidth` is not "scrollable".** It is true for any element
whose content exceeds its box, *including* one deliberately clipping with
`text-overflow: ellipsis` — the recommended fix for overflow, flagged as an
instance of it. Only computed `overflow-x: auto|scroll` puts a scrollbar under
a reader's finger. Which is also the CSS fact behind the original bug worth
knowing on its own: **`overflow-y: auto` with no `overflow-x` declared computes
`overflow-x` to `auto`, not `visible`** — so every such element is silently a
horizontal scroll container too.

**`display` on a `<details>` replaces the UA's skipped-contents mechanism.**
`display: flex` on a `<details>` keeps its non-summary children laid out while
COLLAPSED — a collapsed disclosure leaks a sliver of its body. Scope such rules
to `.details[open]`.

Fixing trap one immediately exposed trap two, which was hiding trap three, a
real regression in code committed minutes earlier. A guard is only worth its
run time once it has been mutation-tested: break the thing on purpose and
confirm it fails, naming the right element.

Three more things about writing a layoutguard case, each learned by watching a
case pass when it should not have (katas hk8v, edhz):

- **A state a page script cannot reach needs `CSS.forcePseudoState`.** There is
  no way to synthesize a trusted hover, and a programmatic `.focus()` does
  **not** match `:focus-visible` — measured, the element stayed unmatched at
  opacity 0. A case declares `forcePseudoStates` in its `case.json` and the
  runner pins them before measuring. This proves the *cascade* applies the rule;
  whether Chrome's own heuristic calls a given focus "visible" is Chrome's
  contract, not ours. Put each state on its own copy of the markup in one
  harness so a single measurement covers all of them with a resting control.
- **Switch transitions off in the harness.** `getComputedStyle` reads the value
  at that instant, so a measure taken right after a state changes reads the
  *start* of a 120ms opacity ramp, not where the cascade settles. Waiting for
  `transitionend` instead hangs forever in exactly the regression the case
  exists to catch — no rule, so no transition, so no event.
- **A fixture can make a declaration unfalsifiable.** An `<img>` with no height
  still gets one from its intrinsic aspect ratio, so a *square* test image makes
  `height: 100%` redundant: deleting the declaration left the case green. The
  fixture is now 8x4. Include `styles/global.css` in any case that measures
  boxes — `box-sizing: border-box` lives there, and it is the difference between
  an 80px and an 82px tile.

Geometry also cannot see `object-fit`: dropping `object-fit: cover` leaves every
box identical and only changes how pixels are scaled inside the image box. Say
so in the case rather than letting a pass imply coverage it does not have.

## A Single `tmux capture-pane` Can Lie

`tmux capture-pane` returns tmux's OWN terminal-grid state, not a snapshot of
what the program last rendered. tmux updates that grid incrementally as bytes
arrive from the pty; `cmd/serf-tui` writes each frame through bubbletea's
default renderer as one unsynchronized ANSI byte stream — bubbletea v1.3.10
has no terminal synchronized-output-mode support at all (grep `standard_renderer.go`
for `2026`/`SyncUpdate`; nothing), and a single frame commonly runs several
KB, well past any platform's atomic-pipe-write guarantee. Under load (rapid
re-renders, CPU contention — kata nxq6's report: "shortly after a turn
started and notifications were arriving"), `capture-pane` can land while tmux
is still mid-write and read a pane that is blank above the last few lines,
with those last few lines already showing current content.

nxq6 investigated the alternative — a real partial-repaint bug, some update
path writing the composer without repainting the frame above it — first, and
ruled it out two ways before touching tmux at all:

- `hubModel.View()` composes the full frame synchronously from model state on
  every call (`cmd/serf-tui/hub_model.go`, `sessionView()` in
  `cmd/serf-tui/hub_session_view.go`); there is no code path that writes only
  the composer, and the session breadcrumb (`topBar`) is provably non-empty
  for any reachable state, so `tuiprim.AppShell.View()` cannot legitimately
  drop it while keeping the footer.
- The kata's own report is inconsistent with a `View()` bug on logical
  grounds: `View()` is a pure function of model state, so an inert keypress
  cannot change its return value — yet "any key... brought back... content
  that had not changed" is exactly what a render/transport-path bug looks
  like from the outside, and exactly what a `View()` bug cannot produce.

`cmd/serf-tui/hub_partial_repaint_nxq6_test.go` drives realistic notification
bursts through `hubModel.Update` directly (no terminal involved) and checks
that invariant after every step, both as a fixed scenario and as a fuzz
target; a mutation test (temporarily dropping `topBar` from `AppShell.View()`)
confirmed the check fails the way it should before the mutation was reverted.

**The fix**: never trust a lone `Capture()` (or a lone `capture-pane`) for a
negative assertion ("X is absent"). `WaitFor` in
`cmd/serf-tui/tmux_e2e_test.go` already retries until its wanted substrings
appear, which self-corrects for POSITIVE assertions the same way a capture
race self-heals — but the screen it returns is only guaranteed to contain
what it waited for, not to be a complete frame, so a
`strings.Contains(screen, unwanted)` check against that same screen can still
land mid-render. Use `(*tmuxTUI).CaptureStable()` instead: it polls until two
consecutive captures match. A torn frame cannot do that — it converges within
milliseconds — while a pane that is genuinely still changing (an active
stream) legitimately does not, so `CaptureStable` keeps polling rather than
settling on a stable-but-wrong frame. `TestTUITmuxE2E_CaptureStableDuringStream`
exercises it under a live rapid-notification burst.

For a check scripted OUTSIDE this harness — an agent driving `tmux
capture-pane` directly to verify TUI behavior, the scripted-verification
workflow this hazard cost real time in during e79v's verification (kata
nxq6's motivating example) — the same fix applies without the Go helper:

```sh
prev=$(tmux capture-pane -p -t "$SESSION")
sleep 0.02
cur=$(tmux capture-pane -p -t "$SESSION")
while [ "$prev" != "$cur" ]; do
  prev=$cur
  sleep 0.02
  cur=$(tmux capture-pane -p -t "$SESSION")
done
# $cur is now safe to grep for an absence.
```

A live repro under CPU-loaded, multi-session, wide-pane bursts (~7,000
captures across several attempts) never caught the pattern directly — tmux
usually wins this race on a healthy machine, which is consistent with the
above rather than a counter-example to it. Treat the mechanism as
evidence-backed, not directly observed.

## A Test That Never Runs

A test that does not execute is worse than a missing test: it reports the
coverage without providing it, and the suite stays green either way. Two shapes
in this repo produce one, and neither announces itself.

**Registered `check*` functions.** Several packages drive their behavioral
contracts through a fuzz entry point that replays one check selected by the fuzz
input — `FuzzFSPathsBehaviorProgram` and friends in `cmd/serf-hub/internal/`
(`fspaths`, `hostlock`, `hubedge`, `codexlaunch`, `launchconfig`). A
`check*(t *testing.T)` function in those packages runs **only** if it appears in
its `checks := []func(*testing.T){…}` seed table. Write one, forget the table
entry, and `go test` passes without ever calling it.

The reachability proxy is `golangci-lint run ./path/to/pkg/`, whose `unused`
linter reports the unregistered check as a dead function:

```
paths_test.go:411:6: func checkSanitizeDirPrefix_PreservesLoneTrailingDot is unused (unused)
```

`go vet` does **not** catch this — verified by unregistering a real check and
running both. Run the linter after adding a check, and state which table each
new check is registered in when handing work off.

**A shell selftest that redirects `TMPDIR`.** macOS `mktemp -t` resolves
against the per-user temp path (`confstr(_CS_DARWIN_USER_TEMP_DIR)`) and
ignores `TMPDIR`, so a selftest that sets `TMPDIR=$scratch` to observe a
script's temp handling observes nothing: the script writes to the real temp
dir, six assertions in run-module-lint-selftest.sh were unfalsifiable, and
every run littered the machine (found during kata cqne). Fake the `mktemp`
binary on `PATH` instead of faking the environment.

**A stylesheet assertion that matches its own comment.** A test that greps CSS
text (`expect(css).toContain("flex: none")`) will match the declaration quoted
in a doc comment above the rule. One of these passed with its implementation
deleted. Strip comments before matching:

```ts
const css = readFileSync(…, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
```

The general rule: **prove a new test can fail.** Break the thing it covers,
watch it go red, then put it back. A test you have only ever seen pass has not
been tested. Two corollaries, both from real incidents here:

- "No tests" is not "tests passed". A broken file makes vitest print
  `Tests no tests` next to a transform error; a grep for `Tests ` reads that as
  benign. Check exit codes, never a grep of piped output.
- Assert the mechanism, not a side effect a broken implementation also produces.
  An "onAdd called once" assertion passed with validation entirely removed,
  because committing the add unmounted the panel either way. Asserting the
  validate call itself distinguishes them.
## Real `git` in Worktree Tests

`git` is an external dependency like the LLM provider, and the same boundary rule
applies: keep it real when git's own behavior is the thing under test, and script
it when git is only a way to reach a Serf decision.

This matters more here than the rule alone suggests. A real-git worktree test
averages **~1.2s and ~14 `git` subprocesses**; the same test on the scripted
boundary runs in **~0.04s**. The `agent` package's real-git tests once accounted
for **48% of its total test-work** (303s of 630s) while being 9% of its tests —
almost all of it process spawn, not assertions.

### Which harness

Use the **real-git** harness (`newWorktreeRepo` and friends, in
`agent/session_tools_worktree_create_test.go`) when the assertion depends on
something only git can produce:

- real registry effects — `worktree add/remove/lock/unlock/prune` actually
  landing, `.git` pointer file contents, deregistration
- real ancestry or patch-equivalence — `merge-base --is-ancestor`, `git cherry`,
  merged/unmerged/adopted outcomes over real commits. The scripted model refuses
  these outright, so a converted test fails loudly rather than reading as merged.
- dirtiness from a *modified or staged tracked file*, real ahead-counts, and
  git's own refusal to remove a dirty worktree without `--force`. The scripted
  model derives dirtiness from untracked files it can see on disk, so an
  untracked-file assertion may stay scripted; it refuses to answer `rev-list
  --count` at all (like the ancestry verdicts above), so an ahead-count
  assertion belongs on real git.
- the real `--porcelain` output shape, including flags like `prunable` and a
  reasonless `worktree lock`
- git's ref rules — e.g. that it rejects the branch name `HEAD`
- symlink canonicalization against git's canonical registry path
- `ResolveMainRepoRoot`'s structural walk and its git-binary fallback
- concurrency that relies on git's own index/ref locking to serialize

Use the **scripted** boundary (`scriptedLaneRepo` in
`agent/worktree_scripted_lane_test.go`, or `scriptedWorktreeSession` in
`agent/session_tools_worktree_scripted_test.go`) when the subject is Serf's own
behavior:

- which validation or refusal rung fires, and its error text
- what event or warning was emitted, and how many times
- what Serf wrote to its own state — sidecars, jobstore records, disposed marks,
  gate flags, `SessionMeta`
- control flow — budget expiry, ordering, retries, "declined to touch"
- argument validation that returns before any git call

Both harnesses keep sidecars and `.git` pointer files as real files on disk. Only
the `git` subprocess boundary is replaced, via
`SessionConfig.testOnly.worktreeGitRunner`.

### The failure mode to avoid

`scriptedWorktreeGit` is a *semantic model* of git, not git. If you script a
behavior the model does not really implement, the test passes while proving
nothing.

A concrete example that was live in this repo: the model's
`check-ref-format --branch` arm hardcoded a rejection of `HEAD`, so a test whose
entire purpose was "real git rejects the reserved name HEAD" would have passed
against the fake regardless of git's actual rules. That hardcoding was removed
and the test stays on real git.

So: the model's unknown-argv arms return an `unsupported argv` error **on
purpose**. If a converted test reaches a command the model does not implement, it
fails loudly rather than silently passing. When that happens, either model the
command honestly or leave the test on real git — never stub the specific answer
the assertion is looking for.

`merge-base --is-ancestor` and `cherry` refuse for the same reason even though
the model recognizes them: their answer is a verdict over a commit graph the
model does not have, so any answer would be that stub.

### Adding a worktree test

Default to the scripted boundary. Reach for real git only when you can name the
git behavior the assertion depends on, and say so in the test's comment so the
next reader does not "optimize" it onto the fake.

## Seeding Hub Fixtures

A hub fixture's session and project identifiers are encodings, not names: a
session id is a 22-character base62 UUIDv7 payload, and a project directory is
`<readable>-<10 base62>`. A hand-written placeholder that looks plausible is
rejected, and `PastIndex.Rebuild` then leaves the seeded session out of the
index — so the fixture is invisible rather than wrong, and the test failure
points nowhere near the id.

Mint them with `cmd/serf-hub/internal/hubtest` instead of writing them out:

```go
sessionID := hubtest.SessionID(t)            // e.g. 02wMz5Txv1C3Hut0M8GCeB
projectDir := hubtest.ProjectID(t, "alpha")  // alpha-0123456789
```

When the fixture has a real checkout on disk, use `identifier.ResolveProject`
instead — the hub cross-checks a project's id against its working directory,
and only the resolved id matches.

Rebuild now names what it refused to index and why, so a seeding mistake that
does slip through shows up on the hub's stderr:

```
[hub] past index: skipped /…/projects/alpha-0123456789/sessions/placeholder.meta.json: invalid session id (want a 22-character base62 UUIDv7 payload): invalid UUID payload
```

## A Disposable Hub Needs Its Own HOME

The hub is a host singleton guarded by an exclusive flock on
`$HOME/.serf/hub.lock`, and that path is deliberately not overridable
(kata av1j): the lock, the run dir, the state root, and the auth token
all derive from `os.UserHomeDir()`, so they only stay coherent when they
move **together**. The blessed way to run a second, disposable hub — an
e2e harness, a scratch verification hub — is a fresh HOME:

```sh
HOME=$(mktemp -d) ./serf-hub -addr 127.0.0.1:0 -serf ./serf
```

Never point a test hub at the real HOME "just for a quick check": if the
real hub happens to be running you collide (the flock error names the
lock file it lost); if it happens to be **stopped**, the test hub
silently claims the real `~/.serf` and state root — the dangerous case,
because nothing fails. A lock-path-only override was considered and
rejected for exactly that reason: it would unbundle the singleton guard
from the state it guards.

## A Live Run Uses the Machine's Build Cache

None of the live-test commands below carries a `GOCACHE=` prefix, and adding
one is a regression. A per-command cache under `/tmp` lands on the same
volume as the checkout — the volume `scripts/setup-gocache.sh` exists to move
the build cache OFF, which has filled to 100% twice mid-fleet-run (kata
98x9); one stray `/tmp/serf-gocache` grew from 1.3G to 2.9G in an hour. There
is nothing to isolate, either: the build cache is content-addressed, so a
live run and the default suite cannot corrupt each other's entries. And when
the configured cache volume stalls, `scripts/disk-reclaim.sh --check` — which
`run-module-tests.sh` runs before every test run — names the stall instead of
letting the run hang (kata r07s).

## MCP Server E2E

The MCP manager has opt-in live tests against `npx -y
@modelcontextprotocol/server-everything`. They are intentionally not part of the
default suite because they depend on an ambient Node/npm toolchain and may fetch
or use cached packages outside the repository.

Run:

```sh
SERF_MCP_E2E=1 go test ./agent/internal/mcp -run 'TestRealMCP_' -count=1 -v
```

`SERF_LIVE_TESTS=1` also enables these tests with the other live test suites.

## Environment Variable Tests

Supported runtime environment variables are defined in the `envvars` package
and documented in `docs/environment.md`. Production code, help text, and test
helpers should use those rows instead of hard-coded env names. The default test
suite includes an audit that fails when a supported env var is used as a raw Go
string outside `envvars`.

When adding a runtime env var:

- Add one `envvars.Var` row.
- Use the row's `Name`, `Getenv`, `LookupEnv`, `Trimmed`, or `Assignment`
  helper at call sites.
- Document it in `docs/environment.md`.
- Keep live-test opt-in gates explicit; a provider credential alone must not
  make a default test issue network requests.

## OpenAI Codex Backend E2E

The OpenAI adapter has opt-in live tests for the ChatGPT/Codex Responses backend.
They are intentionally not part of normal CI because they require stored OpenAI
OAuth credentials and make live requests to `https://chatgpt.com/backend-api/codex/responses`.

Run:

```sh
SERF_OPENAI_CODEX_E2E=1 go test ./llm/providers/openai -run 'TestAdapter_E2E_Codex' -count=1 -v
```

Optional model override:

```sh
SERF_OPENAI_CODEX_E2E=1 SERF_OPENAI_CODEX_E2E_MODEL=gpt-5.4 go test ./llm/providers/openai -run 'TestAdapter_E2E_Codex' -count=1 -v
```

Prerequisites:

- `serf openai login` has completed and stored OAuth credentials.
- The active account can use the Codex backend.
- Network access to `chatgpt.com` is available.

The suite currently checks:

- OpenAI env resolution uses the stored OAuth/Codex transport.
- Requests hit `/backend-api/codex/responses`.
- Codex session metadata fields can be sent:
  - `prompt_cache_key`
  - `session-id`
  - `thread-id`
  - `x-client-request-id`
  - `client_metadata` installation ID
- Reasoning requests ask for `reasoning.encrypted_content`.
- Tool-call replay with preserved assistant messages still works.
- Selected public Responses API controls are accepted or explicitly reported as unsupported by the Codex backend.

Observed live result on 2026-05-21:

- The Codex backend accepted the transport/session metadata path.
- The Codex backend accepted explicit `store:false`.
- The Codex backend rejected these public Responses parameters:
  - `safety_identifier`
  - `prompt_cache_retention`
  - `truncation`
  - `max_tool_calls`
  - `background`
- The Codex backend rejected `service_tier:auto` with `Unsupported service_tier: auto`.
- For low-effort `gpt-5.4` prompts tested, responses contained `reasoning.effort` in the raw response but did not include an output `reasoning.encrypted_content` item. The adapter still supports encrypted reasoning round-trip when the backend returns that item, covered by unit tests.

If sandboxed DNS/network blocks the live run, rerun with command escalation for network access.

## Anthropic Messages API E2E

The Anthropic adapter has opt-in live tests for the Anthropic Messages API. They
are intentionally not part of normal CI because they require `ANTHROPIC_API_KEY`
and make live requests to `https://api.anthropic.com/v1/messages`.

Run:

```sh
SERF_ANTHROPIC_E2E=1 go test ./llm/providers/anthropic -run 'TestAdapter_E2E_Anthropic' -count=1 -v
```

Optional model override:

```sh
SERF_ANTHROPIC_E2E=1 SERF_ANTHROPIC_E2E_MODEL=claude-sonnet-4-5-20250929 go test ./llm/providers/anthropic -run 'TestAdapter_E2E_Anthropic' -count=1 -v
```

Prerequisites:

- `ANTHROPIC_API_KEY` is set.
- The active account can use the selected model.
- Network access to `api.anthropic.com` is available.

The suite currently checks:

- Requests hit `/v1/messages`.
- `service_tier: "standard_only"` is serialized and accepted.
- Automatic prompt caching request shape remains enabled through top-level `cache_control`.
- Extended thinking requests work when the selected model emits thinking blocks; returned thinking is replayed into the next request.
- Tool use and tool-result replay work across turns.

Docs-backed behaviors covered by unit tests and this live suite:

- Anthropic documents `service_tier` values `auto` and `standard_only`, and reports the assigned tier in `usage.service_tier`.
- Anthropic documents automatic prompt caching through top-level `cache_control`.
- Anthropic documents `thinking` and `redacted_thinking` blocks, including signatures/data that must be preserved when round-tripping tool-use conversations.

Observed live result on 2026-05-21:

- `service_tier: "standard_only"` was accepted.
- The live transport/service-tier/cache-shape test passed against `api.anthropic.com`.
- The live extended-thinking replay test passed against the default e2e model.
- The live tool-use/tool-result replay test passed against the default e2e model.
- Short prompts may not report cache write/read activity because they do not cross cache thresholds; the test logs this instead of failing.
- Some prompts/models may not emit visible thinking blocks even when reasoning is requested; the test logs this instead of failing. Unit tests cover thinking/signature and redacted-thinking round-trip shapes.

If sandboxed DNS/network blocks the live run, rerun with command escalation for network access.
