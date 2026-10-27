# Testing

## Test Reliability Policy

The default test suite must be deterministic. Running `make test` or
`go test ./...` must not depend on provider credentials, model availability,
network access, quota, current model behavior, wall-clock timing outside the
process, or ambient developer machine state.

Use this boundary when adding or fixing tests:

- If the test verifies Evener plumbing, use a scripted provider at the LLM
  boundary and exercise the real Evener code below it. Examples: CLI flag/config
  wiring, appwire RPC, daemon input queues, session loops, tool execution,
  transcript writes, event emission, goal continuation routing, hook dispatch,
  and prompt composition.
- If the test verifies model behavior, keep it live. Examples: whether a
  specific model chooses a tool from a natural-language instruction, follows an
  output contract, supports a provider feature, honors a live API wire shape, or
  behaves well across multi-turn goal prompts.
- Live tests must be explicitly opt-in with a `EVENER_*_E2E=1` or
  `EVENER_LIVE_TESTS=1` style environment variable in addition to the provider
  credential. A provider key by itself must never make the default suite issue
  live requests.
- Do not use sleeps, polling races, or large string snapshots to prove behavior
  when a structured event, state field, file result, or fake transport script can
  prove the same contract.
- Do not mock Evener internals to make a test pass. Keep the fake boundary at an
  external dependency: LLM provider, network server, filesystem root, clock, or
  process launcher.

When a test needs a model, name that as the behavior under test and keep it out
of the default suite. When the model is only a way to drive Evener, replace it with
a scripted `llm.ProviderAdapter` response and assert the Evener side effects.

### Prompt Prose Is Not a Test Oracle

Pin assembled/system prompts, prompt sections, model-facing tool and agent
descriptions, and natural-language steering, reminder, warning, or tool-result
prose only with a whole-output golden snapshot. `agenttest.CheckGolden` compares
the complete rendered text with a file under `testdata/`, and a wording change
regenerates the file with an `-update-prompt` flag so the diff is read in
review. `agent/testdata/memoryprompt` and `agent/testdata/messagingprompt` are
the pattern: one golden per session kind, rendered from the requests a real
session sends.

Never test prose by presence, absence, substring, regex match, token count, or
order of words or clauses. Those assertions pin fragments of copy, not behavior,
and they pass while the surrounding text drifts. If a test's only claim is that
the prose contains a sentence, delete it and let the golden cover the text.

Assert behavior structurally: typed template inputs, section identity and order,
schema properties and enums, capability metadata, route or branch selection,
structured event or data fields, provider request shape, or downstream side
effects. An opaque sentinel may prove that user or task data crosses a plumbing
boundary, but it must not encode or pin natural-language copy.

Exact text assertions are allowed only when the text is itself a machine or API
contract value, such as a JSON field name, enum token, error code, or CLI flag.
Prefer asserting such values through their structured representation.

### Copy Is Not a Test Oracle

The same rule holds for every human-readable string, not only prompts: UI copy,
labels, empty-state and usage text, toast and error messages, log lines, and Go
error strings. A test that passes because a string came out the same as it went
in proves nothing and breaks on every wording change. Never write:

- an assertion that rendered or returned text equals, contains, or matches a
  hardcoded phrase (`findByText("Use: name command [args...]")`,
  `strings.Contains(err.Error(), "not found")`);
- an assertion that output equals a constant imported from the code under test
  (`findByText(USAGE_MESSAGE)`): the test only re-reads the source, so it
  cannot fail for a behavioral reason;
- a test whose whole claim is that some copy exists.

Assert what the copy is for instead:

- **Frontend:** that the state changed. A refusal raises `role="alert"`, the
  list did not grow, the request was never sent, focus moved, a control became
  disabled, a callback fired with the right structured value. Locate elements
  by role, and by accessible name only to pick the right control, never as the
  thing being verified. Use `data-testid` when no role and name is stable.
- **Go:** that the error is the right error. Match a sentinel with
  `errors.Is`, a type with `errors.As`, or a structured field or code, and
  assert the side effects that did or did not happen.

When the text really is the contract (an enum token, a wire field, a CLI flag,
a file format), assert it through its structured form, as above. When it is
copy a person reads, review it in the diff and test the behavior around it.

Two audits enforce the rule against tests whose whole claim is that some copy
exists. Each fails on a test whose name says it shows, renders or says
something and whose every check compares output with production copy:

- `copyonly_audit_test.go` (part of `go test`) runs `internal/devtool/copyonly`
  over every tracked Go test. Its allowlist is
  `internal/devtool/copyonly/allowlist.tsv`.
- `cmd/evener-hub/frontend/src/dev/copyonly/copyonlyAudit.test.ts` (part of
  `make test-web`) reads every tracked TypeScript test in the frontend,
  `mobile-native` and `appwire-client/typescript` with the
  TypeScript compiler. Its allowlist is
  `cmd/evener-hub/frontend/src/dev/copyonly/allowlist.tsv`.

Both skip expected errors, counts, attribute and state reads, spy assertions,
role queries, machine-contract tokens, and text the test supplied itself. When
an audit flags your test, assert the behavior instead. If the copy must stay
the oracle (model-facing text whose conversion is deferred, or a false
positive), add a row to that language's allowlist: the file, the test name,
and the reason it is kept, tab-separated. Rows are keyed by file and test name,
not line. Each audit also fails on a row whose test is no longer flagged, so
remove the row when you convert, delete or rename the test.

A third audit covers Go tests that are not named for wording. It counts the
checks of an error's text against a phrase from production source
(`strings.Contains(err.Error(), "not found")`) anywhere in a test file. Its
allowlist, `internal/devtool/copyonly/errstring_allowlist.tsv`, has one row per
test file: the file and how many such checks it holds. A file may hold no more
than its row, and a file with no row may hold none, so a new test matches a
sentinel with `errors.Is`, a type with `errors.As`, or a code. The counts only
go down: when you convert checks in a file, lower its count (delete the row at
zero), because the audit also fails on a file that holds fewer than its row.

To find what is left to convert, `go run ./cmd/evener-dev/bin dev copy-oracle`
inventories every assertion whose oracle is production copy, per package and
category, using the same two scanners. `--hits FILE` writes each one as TSV,
`--copy-only FILE` lists the copy-only tests instead, and `--path PREFIX` and
`--only ts|go` narrow the scan. The TypeScript half runs under `node` with the
frontend's install, so run `make web-preflight` first, or pass `--only go`.
After converting a test, `tools/copy-oracle/mutate.sh` breaks the behavior it
now checks and confirms the test fails.

## Flakes and Timeouts

Two rules, both absolute.

**A sighted flake gets a root-cause fix on the spot.** Even one failure, even
under heavy load, even in code you don't own. "Pre-existing" and "another
session's area" are not exemptions; a deferred flake gets re-triaged at full cost
by every session that hits it, and it poisons every run's signal in between. The
fix must address the mechanism, not the symptom. Honest irreproducibility after
serious looped effort is reportable, but only with mechanism analysis, not a
shrug.

**Never widen or hardcode a timeout to absorb awaitable work.** A widened
deadline hides a guess that re-flakes as the suite grows, and it converts real
failures into timeout mysteries. The preference hierarchy:

1. Await the actual async completion: the import promise, event, callback,
   channel, or process exit.
2. Condition-watching (`findBy`/`waitFor`/poll-until) only where no awaitable
   completion exists; any ceiling there is a tripwire bound, never the
   mechanism.
3. Never fixed sleeps, fixed flush counts, or widened deadlines to paper over
   load-dependent work. Where pacing itself is under test, use the codebase's
   clock abstraction.

Every flake family root-caused here (a vite-cache digest, an auth stopwatch, a
composer projection refresh) was an awaitable completion nobody was awaiting.

## Agent Tests Run in Parallel by Default

A top-level Go test without `t.Parallel()` runs alone, one at a time, and a
mostly serial agent package dominates the `-race` lane. Start every agent test
with `t.Parallel()` unless it reaches process-wide state: the environment or
working directory, the default logger, a global test setter (`Set…ForTesting`),
or a package-level variable it changes (a seam, an override stack, a hook),
directly or through a helper. Such a test stays serial, and says why in a comment
when the reason is not visible in its own body (`Not parallel:
shortenCloseCascadeBudget pushes the package-wide budget`).

No lint enforces this: without type and pointer analysis a checker cannot prove a
test leaves shared state alone, and one that guesses would ask for `t.Parallel()`
on tests that race. The `-race` lane catches a parallel test that races on
memory; a parallel test that changes shared behavior (an override stack, a global
hook) shows up as a flake instead, so a change that makes tests parallel is
checked for exactly that before it merges.

## Destructive Operations and the Tooling Test Estate

Four standing rules for the shell tooling's tests.

**No recursive delete takes an argument a caller could clobber.** Scratch is
minted by `scratch_dir <var> <prefix>` and reclaimed by the no-argument
`scratch_rm` (scripts/lib/scratch-lib.sh); a delete that cannot be handed a path
cannot be handed the wrong one. A second sanctioned pattern serves the runners
that keep scratch on failure or reclaim it across runs: the directory is named
`<prefix>.$$`, the trap is armed before `mkdir` so a signal in the window
abandons nothing, and the next run reclaims leftovers whose pid suffix no longer
answers `kill -0` (scripts/lib/covscratch-lib.sh). Every remaining variable-fed
recursive delete in the repository's shell (scripts/ and the repo-root
install.sh) lives on `TestNoScriptFeedsVariableToRecursiveDelete`'s count-pinned
list with the reason it is allowed to exist. The list grows only with an
explicitly reviewed reason and shrinks as scripts convert or retire.

Two parts of the rule the audit cannot hold. Its scan is textual, so `find -exec
rm -rf {} +` and `xargs -I{} rm -rf {}` both read as deletes of the literal `{}`
placeholder rather than a variable, and slip through. A recursive delete piped a
path with no placeholder at all (the ordinary `| xargs rm -rf` shape, or a delete
split across a backslash-newline continuation) is refused outright: the predicate
cannot tell what it deletes. And the count pin stops a file gaining an *extra*
variable-fed delete and fails a listed file whose lines went away, but it cannot
see one blessed delete being swapped for a different one inside an already-listed
file. Read the deletes in any diff that touches a listed file.

**Hazards are banned by construction and enforced statically, never proven by
firing live weapons.** A test never contains a live destructive command, even one
asserted to be unreachable. To prove a guard stops before a delete, put a marker
line where the delete would be and assert the marker is never reached. Fixtures
aim at seeded decoy directories, never at `/`, `$HOME`, or anything a person
would miss. Where deletion itself is the behaviour under test, the delete targets
fixture-owned paths and sits on the audit's count-pinned list with that reason.

**Test depth caps at one meta-level.** A tool gets a direct test; the shared
libraries under it get one direct test file each; nothing tests the tests. A
library property is proven once, directly. That every consumer goes through the
library is a static audit's job, not a reason to re-run consumers under
sabotage.

**A test of dev tooling earns its keep by pinning outcomes of a tool the gate or
CI depends on.** Outcomes are exit codes, summaries, refusals, and file effects;
the argv a script hands a faked binary is an implementation restated, and
asserting on it is testing the mock. Faking the toolchain to test a script is
banned outright: a tool that can only be tested by faking `go`, `gh`, or its
sibling tools on PATH has outgrown shell, and the move is the port (the
dev-tooling spec's port-on-touch trigger), never a fixture harness built around
the fake. Feeding a data seam fixture input (the registry listing fuzz-bisect's
and fuzz-oracle-audit's real behavior stub while the git history, replays, and
verdicts stay real) is not a faked toolchain. Hand-run conveniences fail loudly
in front of whoever ran them and need no automated suite. Tooling that
accumulates real logic belongs in Go under `go test`; shell stays for glue.

## Gates that need more than a table row

Every gate that is a make target is documented in the generated target table of
the doc that owns its family: [Targets](#targets) below for the test family, and
[building.md](building.md), [linting.md](linting.md), [coverage.md](coverage.md)
and [fuzzing.md](fuzzing.md) for the rest. Those tables are generated from the
`##` annotations above each rule in `make/*.mk`, and `make lint` fails if a
committed table has drifted from them.

The gates below are written out here because a table row cannot carry them:
environment opt-ins, work this repository does not own, and `ROOT_FULL=1 make
test`, which is the `test` target under an environment override.
`scripts/web/web-preflight.sh` is listed so a reader hunting for it among the
gates is told where its row lives. Test assertions stay deterministic when the
live opt-ins are unset, and dependency installation, disk capacity, browser
availability, and CI tool setup are explicit prerequisites rather than
assumptions.

### `scripts/web/web-preflight.sh`

A setup prerequisite for the web, build, browser, and lint gates rather than a
gate in its own right: `make web-preflight` runs it directly, and `make
build-web`, `make test-web`, `make test-web-browser`, and `make lint` (through
`lint-biome`) all reach it.

What it proves, what it may run, and how it fails live in the `make
web-preflight` row of [building.md's target table](building.md#targets). That row
is generated from the annotation above the rule and gated for staleness by `make
lint`; a copy of those cells here would drift the first time the annotation
changed. Managing a shared install across worktrees belongs to the worktree and
frontend tooling, outside Evener.

### `ROOT_FULL=1 make test`

The full intended non-fuzz surface: the whole root suite plus every non-root
module plus the frontend. `ROOT_FULL=1` removes root's ordinary `-short` mode
while retaining the non-fuzz `Test`/`Example` name filter and the explicit
fuzz-owned exclusions, which is what preserves the complete non-fuzz
post-merge surface.

Run it locally pre-merge and post-merge; `make merge-approval-gate` runs it as
its third phase. The required CI equivalent is `ROOT_FULL=1 WEB=0 make test`,
because the web job owns `make test-web` and the deterministic Go job must not
duplicate it.

Requirements are the same as `make test`. `ROOT_FULL=1` enables no provider and
no fuzz search. Any root or module failure is nonzero, and live tests stay
explicitly skipped unless their own opt-in is set.

### Opt-in MCP and provider smoke tests

The maintained live selectors are MCP's `TestRealMCP_` in
`agent/internal/mcp` and `TestIntegration_` in the `llm` module root. Check that
each selector lists tests before opting in; an empty list proves nothing:

~~~sh
go list ./agent/internal/mcp
go test ./agent/internal/mcp -list 'TestRealMCP_'
go list ./llm
go test ./llm -list 'TestIntegration_'

# Run only with the corresponding tool or provider credentials and network:
EVENER_MCP_E2E=1 go test ./agent/internal/mcp -run 'TestRealMCP_' -count=1 -v
EVENER_LIVE_TESTS=1 go test ./llm -run 'TestIntegration_' -count=1 -v
~~~

MCP requires `npx` and may fetch `@modelcontextprotocol/server-everything`;
it accepts `EVENER_MCP_E2E=1` or `EVENER_LIVE_TESTS=1`. The `llm` smoke tests
require `EVENER_LIVE_TESTS=1`, network and at least one applicable credential:
`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `GEMINI_API_KEY`/`GOOGLE_API_KEY`, or
`MINIMAX_API_KEY` for text; image cases also accept `OPENROUTER_API_KEY`.
The tests run every configured provider, not just one selected by the command.
A key alone never enables live requests. Without the opt-in they skip; without
credentials an opted-in smoke run also skips rather than proving an API works.
See [MCP Server E2E](#mcp-server-e2e) and the declarations in
`llm/integration_smoke_test.go` for the individual cases.

### `EVENER_TMPDIR_PRIVDROP_E2E=1` — a host privilege-drop check

Not a provider suite. It proves that a child which becomes another uid can create
temp files in the session temp container `TMPDIR` names: a private `0700` scratch
exported as `TMPDIR` is unwritable for such a child. It needs a real
world-usable host temp, the external `mktemp`, and a `sudo` that may drop to
`nobody`, so it is an explicit opt-in and never runs in default CI. Without the
opt-in the property is still covered deterministically by the container and leaf
mode assertions in `agent/sandbox` and `agent/execenv`.

~~~sh
EVENER_TMPDIR_PRIVDROP_E2E=1 go test ./agent/sandbox -run TestSessionTmpPrivilegeDropE2E -count=1 -v
~~~

### Live multi-host checks (`EVENER_SSH_E2E*`)

These opt-in checks drive a throwaway loopback hub's own AppWire client against a
real remote host, or against a disposable Docker container for the container
checks. Each test's doc comment states its mechanism and what its assertions
prove; this section states what a person running one needs. They share one
contract:

- Every one needs `EVENER_SSH_E2E=1` and skips under `-short`. The host-backed
  ones also need `EVENER_SSH_E2E_HOST`, the host's ssh destination (an
  `ssh_config` alias or `user@host`). `EVENER_SSH_E2E_USER` optionally sets the
  entry's ssh user, and `EVENER_SSH_E2E_EVENER_PATH` overrides the host's evener
  path (default `~/.local/bin/evener`).
- The host must be disposable, reachable over non-interactive ssh (`ssh -T -o
  BatchMode=yes`), and a supported target (`linux/amd64` or `darwin/arm64`). Use
  an unmanaged host: the bootstrap selects evener hub launchd/systemd units by
  name, without matching the private config or port, so a loaded unit on a
  managed host can start or restart the regular hub.
- Every check except attach is its own opt-in because it writes to the host.
  Isolation differs by check:
  - Deploy, container, push and UI run a private hub from a run-unique directory
    they create on the host and remove when they finish. Deploy, and the
    container pipeline check against an external host, remove it only after
    proving shutdown: an unproved shutdown fails the check and leaves the
    directory. Push and UI stop the listener and remove the directory in separate
    cleanups, so an unproved shutdown does not retain it.
  - Deploy hashes the host's real install (`~/.local/bin/evener`) before and
    after and fails if it changed. Push and UI hash that install and the real
    `~/.config/evener/credentials.toml` and fail if either appeared or changed. A
    failed hash probe cannot establish absence.
  - The session check runs no private hub. It starts a session on the host's own
    hub, so it uses the host's real provider configuration, credentials and state
    root, and the session record survives the run. Only its working directory is
    run-unique.
- Where a check expects a host build already installed, install one matching
  the controller's build: a plain `go build` of `./cmd/evener/` (reporting `dev`),
  or both stamped with the same `-ldflags`. The deploy checks cross-compile the
  tree under test; they need a **clean** checkout (the build identity is stamped
  from `HEAD`; a dirty tree skips) and the embedded web UI built (`make
  build-web`). Each test's doc comment states its exact prerequisites.

| Opt-in (besides `EVENER_SSH_E2E=1`) | Test | What it proves |
| --- | --- | --- |
| none (also needs `EVENER_SSH_E2E_REMOTE_DIR`) | `TestHostAddAttachForwardedDiscoveryE2E` | Add, attach, and a spawn-form discovery call go through `evener/host/request` and are answered only by the attached remote hub. |
| `EVENER_SSH_E2E_DEPLOY=1` | `TestHostDeployNoEvenerE2E` | A host with no `evener` is given one by the controller (`-build-source` and `-deploy-binary`), attaches, and reports the controller's `buildinfo.Version()`; forwarded slash catalogs follow project switches without leaking entries. |
| `EVENER_SSH_E2E_DEPLOY=1` and `EVENER_SSH_E2E_CONTAINER=1` | `TestHostDeployPipelineContainerE2E` | The add, attach, plan, deploy, restart, operations wire path against a Docker container host: operation lifecycle, single-use confirmation token, host-side process replacement, and `launch-check` reporting the controller's build. |
| the same two | `TestHostCompatibleConnectContainerE2E` | Connecting a remote hub with a different build stamp and the same AppWire protocol deploys nothing and leaves the remote binary and process untouched. Never uses `EVENER_SSH_E2E_HOST`. |
| `EVENER_SSH_E2E_PUSH=1` | `TestHostPushCredentialsDisposableHostE2E` | The controller's credentials store reaches the host's own store through `evener/auth/apiKey/conditionalSet`. |
| `EVENER_SSH_E2E_UI=1` | `TestHostSettingsUIDisposableHostE2E` | Each host-scoped settings pane in the production web app renders the host's data, and the UI's writes (Save, Trust, credential push) land on the host. |
| `EVENER_SSH_E2E_SESSION=1` | `TestHostSpawnSessionE2E` | A session the controller asks for is spawned and served by the host's hub from the host's own configuration, shows in the controller's fleet, and stops in band through the controller. |

~~~sh
EVENER_SSH_E2E=1 EVENER_SSH_E2E_HOST=paradise-park \
  EVENER_SSH_E2E_REMOTE_DIR=/srv/remote-only \
  go test ./cmd/evener-hub/ -run TestHostAddAttachForwardedDiscoveryE2E -count=1 -v

EVENER_SSH_E2E=1 EVENER_SSH_E2E_HOST=paradise-park EVENER_SSH_E2E_DEPLOY=1 \
  go test ./cmd/evener-hub/ -run 'TestHostDeployNoEvenerE2E' -count=1 -v

EVENER_SSH_E2E=1 EVENER_SSH_E2E_DEPLOY=1 EVENER_SSH_E2E_CONTAINER=1 \
  go test ./cmd/evener-hub/ -run 'TestHostDeployPipelineContainerE2E' -count=1 -v

EVENER_SSH_E2E=1 EVENER_SSH_E2E_DEPLOY=1 EVENER_SSH_E2E_CONTAINER=1 \
  go test ./cmd/evener-hub -run '^TestHostCompatibleConnectContainerE2E$' -count=1 -v

EVENER_SSH_E2E=1 EVENER_SSH_E2E_HOST=paradise-park EVENER_SSH_E2E_PUSH=1 \
  go test ./cmd/evener-hub/ -run 'TestHostPushCredentialsDisposableHostE2E' -count=1 -v

EVENER_SSH_E2E=1 EVENER_SSH_E2E_HOST=paradise-park EVENER_SSH_E2E_UI=1 \
  go test ./cmd/evener-hub/ -run 'TestHostSettingsUIDisposableHostE2E' -count=1 -v

EVENER_SSH_E2E=1 EVENER_SSH_E2E_HOST=paradise-park EVENER_SSH_E2E_SESSION=1 \
  go test ./cmd/evener-hub/ -run 'TestHostSpawnSessionE2E' -count=1 -v
~~~

What each check needs beyond the shared contract:

- **Attach.** `EVENER_SSH_E2E_REMOTE_DIR` names a directory that exists on the host
  and not on the controller, holding at least one visible child entry. The check
  forwards `evener/paths/complete` for it and requires the host's entries back,
  then asks the controller the same prefix directly and requires none. A path both
  machines share (like `/opt/homebrew` when the controller is also a Mac) fails
  the direct half, and the controller's `$HOME` is exactly wrong. Verify it first:
  `ssh HOST '[ -d /srv/remote-only ] && ls -A /srv/remote-only'` and `test ! -e
  /srv/remote-only` locally. The host needs a matching build installed. On a fresh
  host create `~/.local/bin`, `scp` the build, and on macOS re-sign it ad hoc
  (`codesign --force --sign - ~/.local/bin/evener`) or the loader kills the
  unsigned arm64 copy.
- **Deploy.** The host's filesystem must be separate from the controller's;
  localhost cannot satisfy the host-only path checks or the direct-controller
  negative control. The check writes under the host's `/tmp` and binds loopback
  ports `19180`/`19181`.
- **Container.** Docker with a reachable local daemon (a `DOCKER_HOST` or docker
  context naming a non-local daemon skips, because sshd is published on the
  controller's `127.0.0.1`) and the `linux/amd64` target. The check caches the
  image `evener-e2e-ssh:local` (`docker rmi evener-e2e-ssh:local` removes it) and
  removes its container when the run finishes. A per-run `ssh` shim first on
  `PATH` hands the per-run `ssh_config` to exactly the container alias's
  invocations, because OpenSSH resolves its user config from the passwd-database
  home and not `$HOME`. `EVENER_SSH_E2E_HOST` redirects the pipeline check to an
  existing host. The compatible-connect check verifies its container's isolation
  (private process namespace, unprivileged, no host network/IPC, no mounts or
  host sockets) before Connect; a skip leaves the journey unverified, so also run
  the pipeline check from a clean checkout with no host override.
- **Push.** The host's private config lives in `evener-push-e2e-<run>` under its
  `HOME`, with `XDG_CONFIG_HOME` pointed into it: the hub config schema has no
  credential-path field, so a private `hub.toml` alone would redirect state but
  not the credential file. It launches a cross-compiled build of this checkout,
  since a host hub built from older main answers the conditional set with "method
  not found". `EVENER_SSH_E2E_PUSH_GUARD_DISPOSABLE=1` points the non-mutation
  guard at the disposable store so the push's own write trips it, showing the
  guard can fail.
- **UI.** The hub embeds `frontend/dist`, so the check runs `npm run build` in
  `cmd/evener-hub/frontend` and then builds the controller hub. It needs that
  directory's `node_modules` (it never runs `npm ci` or `npm install`), `node`,
  and Chrome. The disposable host hub listens on `127.0.0.1:19183`, and the
  controller runs on an isolated `HOME` seeded with different values so a pane
  rendering the controller's data fails. Chrome's profile must live somewhere
  short: its singleton socket path is capped at about 108 bytes, so the driver
  creates the profile under `/tmp`. Screenshots and `result.json` land in a
  directory under the test process's temp root, kept on failure and, on a pass,
  only with `EVENER_SSH_E2E_UI_KEEP_ARTIFACTS`; name a base outside that root with
  `EVENER_SSH_E2E_UI_ARTIFACT_DIR` to keep them past the run.
  `EVENER_SSH_E2E_UI_GUARD_DISPOSABLE=1` is the guard's own failure demonstration.
- **Session.** The host must carry a matching evener build at
  `EVENER_SSH_E2E_EVENER_PATH` and its own launch configuration must resolve a
  model (a `model` in its `launch.toml`, or its provider environment); a host that
  cannot resolve one refuses the spawn, which is the failure this check surfaces.
  No turn runs, but resolving the spawn makes the host enumerate its models
  through each configured provider's endpoint, so the host's credentials, network
  and quota must be healthy. The session runs in
  `$HOME/evener-session-e2e-<pid>-<timestamp>`. A session that could not be
  stopped, or a start that returned no usable ref, leaves that directory in place
  and the failure names where to look; the session record the host keeps in its
  state root always survives, since `thread/shutdown` stops the daemon without
  deleting the session.

### Live service coverage and host sandbox parity

~~~sh
EVENER_E2E_LIVE=1 scripts/coverage/e2e-cover.sh --merge-unit
EVENER_SEATBELT_LIVE=1 go test ./agent/sandbox/ -run TestSeatbeltLive -count=1 -v
~~~

These exercise real binaries and services, or the host Seatbelt backend, beyond
what deterministic unit coverage reaches. Both are manual and platform-specific,
and neither is required CI. The first needs provider and network services for the
live scenario scripts; the second needs macOS Seatbelt.

`EVENER_E2E_LIVE` is not a correctness gate, because the coverage script
intentionally continues past a failing scenario. A missing platform or service is
a limitation; a live scenario failure has to be read out of the script's output
rather than treated as coverage success.

### Launcher health checks, managed-service restart, and SDD/Kata semantics

Operational and external workflow, owned by the launcher, the worktree manager,
or the SDD/Kata tooling. None of it is a gate proof this repository owns (none
appears in its Makefile or workflows) and none belongs in Evener CI: do not add
these checks here, and do not write anything that silently implies Evener runs
them.

## Post-Merge Gate

Run the canonical local gate from the repository root:

~~~sh
make merge-approval-gate
~~~

For diagnosis and evidence, that target expands serially to:

~~~sh
make lint
make build
ROOT_FULL=1 make test
~~~

Live/e2e tests police their own requirements: the cmd/evener-hub `TestE2E_*`
family calls `internal/e2ecap`'s `RequireLoopbackBind`/`RequireProcessInspect`,
which probe the capability at test time and `t.Skip` with the probe's reason when
the host lacks it. On an unrestricted host nothing skips; on a sandboxed host the
infeasible tests skip individually instead of failing into a denied bind.

`ROOT_FULL=1` removes root's ordinary `-short` mode while retaining the non-fuzz
`Test`/`Example` name filter. The runner excludes the explicitly fuzz-owned
sanity functions; their deterministic replay, and all other fuzz coverage, is
owned and run by `make fuzz`. Ordinary `make test` keeps the root wave in short
mode unless `ROOT_FULL=1` is set.

CI runs the web check as two lanes on separate runners, web-unit (`make
test-web`) and web-browser (`make test-web-browser`), and the required `web` job
passes only when both do. The deterministic Go check runs the same way: `tests /
root` (`ROOT_FULL=1 WEB=0 make test TEST_SCOPE=root`) and `tests / nonroot`
(`TEST_SCOPE=nonroot`, every other module) on separate runners, with the required
`tests` job passing only when both do. `WEB=0` keeps frontend tests from being
duplicated.

`make test` runs three packages as cost-balanced shards: the agent module,
`cmd/evener-hub`, and `cmd/evener`. Its test runner invokes the development
tooling's `agent-shards`, `hub-shards`, and `cli-shards` commands
(`cmd/evener-dev/agentshards.go`; the gate's `ROOT_SHARDED` table). Each is one
binary of hundreds to thousands of mostly serial tests, so splitting it across
processes bounds its wall time. A shard's `-test.run` arrives through
`EVENER_SHARD_RUN_FILE`, which the package's TestMain applies with
`shardrun.ConfigureRunFile` and then unsets, so a test that re-execs its own
binary as a helper keeps its explicit `-test.run`. `AGENT_SHARDS=0`,
`HUB_SHARDS=0` and `CLI_SHARDS=0` fall back to one `go test`.
`HUB_SHARDS=elsewhere` and `CLI_SHARDS=elsewhere` leave that package out of the
run because another job runs it, and `ROOT_REST=0` skips the root `go test`
itself: the CI race lanes use them to run the hub on one runner and the rest of
the root module on another (scripts/lib/gate-root-shards.sh, `RACE_ROOT_PART`).

A TestMain that dropped the run-file call would still pass, with every shard
running the whole package; one that made the call before `flag.Parse` would let
any `-test.run` on the command line override the file. So each sharded package
pins the wiring with a one-line test,
`shardrun.RequireTestMainAppliesRunFile(t)`, which re-execs the binary with
`-test.run=^$` on the command line and a run file naming only that test, and
fails unless exactly that test ran. A newly sharded package adds the same test.

The `make test` runner gives every Go module and frontend stream a distinct
private `HOME` plus temporary and XDG roots beneath its per-run log directory.
For Go streams, the runner copies ambient GOENV settings into that owned root and
preserves configured or platform-default GOPATH/GOCACHE locations so reusable
module and build caches stay warm. Frontend commands disable Node's automatic
compile cache. After every child process exits successfully, a green run removes
the complete per-run directory and its process-lifetime scratch. This cleanup is
not a session-end policy: it does not delete scratch that a resumable live
application still owns. A failed or interrupted run retains the directory and
prints its path so the evidence remains. Standard reusable caches outside the
owned roots are audited separately rather than claimed as temporary cleanup.

That per-run directory lives in RAM when the host offers it: the runner mints it
under `/dev/shm` when that is a writable directory with at least
`GATE_SCRATCH_MIN_KB` (default 2GiB) free, and under the ambient `TMPDIR`
otherwise (scripts/lib/gate-scratch-root.sh). On a disk every `t.TempDir`, SQLite
fixture and atomic write pays real fsync latency. macOS has no `/dev/shm` and
stays on disk. Two consequences for test authors: a failed run's retained logs
occupy RAM until you delete them or reboot, and a test must not assume `TMPDIR`
is under `/tmp`: a fixture that needs a `/tmp` path creates one explicitly
(`tmpMainCheckout` in agent/sandbox). The agent sandbox's minimal `--dev`
replaces `/dev`, so it re-binds read roots under `/dev/shm` afterwards and masks
secrets there, the same way it treats `/tmp`; sandbox tests behave the same with
`TMPDIR` on disk or on `/dev/shm`. `XDG_RUNTIME_DIR` is not a usable alternative:
the sandbox masks `/run/user`.

Tests clean up after themselves without the runner, too: a direct `go test` must
leave nothing in the developer's temp dir or in `/tmp`. Sessions make that harder
than it looks. A closing session keeps its named scratch
(`evener-scratch-<root>/<session>`) until its root is archived, a session a test
kills leaves its disposable scratch for the crashed-scratch sweep's 24h reclaim
(`sandbox.SweepCrashedSessionScratch`), and a detached command's world-usable temp
container lives in `/tmp` or `/var/tmp`, which no `TMPDIR` moves. So a package
whose tests run sessions routes its TestMain through `agent/sandbox/sandboxtest`:
`Run`, or `RedirectHostTemp` and `Discard` in a TestMain that does more, point
`TMPDIR` and the container bases into one root and remove it when the run ends.
Self-exec helper children inherit that `TMPDIR`, so what they leave when they are
killed on purpose goes with it.

The container bases travel as `EVENER_HOST_TEMP_BASES`, so every `evener` and
`evener serve` a test starts inherits them too. That matters beyond leftovers:
each of those processes runs the crashed-scratch sweep at startup, so without the
variable a test reclaims other sessions' abandoned scratch from the developer's
real `/tmp` and `/var/tmp`. A TestMain that clears every product `EVENER_*`
variable after `RedirectHostTemp` keeps that one value
(`sandboxtest.Redirected`), and a test that builds a child environment from
scratch must pass it on. A test that sets the bases itself proves they are in
force before it mints or sweeps anything, so a regression fails the test instead
of reaching `/tmp`. A test that drives the crashed-scratch sweep itself confines
it to scratch it owns (its own `TMPDIR` and user cache dir, no container bases),
because the sweep deletes any aged, unleased scratch it can see, including
another process's.

A fixture built once and cached for the whole package run (a `sync.Once` repo, a
built binary) belongs in the package's own fixture root, never in
`os.MkdirTemp("", ...)`: tests point `TMPDIR` at their own `t.TempDir()`, so a
cache made under whichever `TMPDIR` the first caller had is deleted by that
test's cleanup, and every later user fails. In agent that root is
`sharedAgentTempRoot` (`packageFixtureTempDir` for a helper with a `t`).

The browser guards are deliberately not part of `make lint` or `make test`: those
default gates remain usable without Chrome, while CI still requires the
browser-specific gate in its web job.

### Frontend setup boundary

`make test-web` and `make test-web-browser` are deterministic after the frontend
dependencies are installed. Establishing that install may require npm/network
access or an existing compatible worktree install, so an unavailable setup is a
reported prerequisite failure rather than a deterministic test pass.

The production editorial-preview config refuses a symlinked `node_modules`: its
server boundary must not serve dependencies that concurrent lanes can write. Its
Node isolation tests do not skip in a worktree that uses the lockfile-matched
shared install. They copy the real frontend, AppWire sources, and dependency tree
into a private repository-shaped fixture, then run the config, HTTP routes, and
wrapper there. Never run `npm ci` through the shared symlink to make those tests
pass.

A fresh agent worktree has no frontend install. The cheap setup that stays inside
every preflight's rules is to symlink a lockfile-matched real install from
another tree: `cmp` this tree's `package-lock.json` against the install's first,
then `ln -s`. The frontend tree's `.gitignore` ignores the link, but
`mobile-native/.gitignore` uses `node_modules/`, which does not match a symlink:
that link shows as untracked in `git status`, so keep it out of commits by path,
not by ignore.

The web gate (`evener-dev dev web-checks`: typecheck, test, lint) writes nothing
under `cmd/evener-hub/frontend`, so it runs in a checkout whose frontend
directory denies writes. (`make test-web` as a whole is not read-only: its own
prerequisites write `./evener-dev` and may `npm ci`.) `vitest_run_args` selects
`--configLoader runner` (Vite's default loader writes a bundled config under
`node_modules/.vite-temp`), the plugin that restores `dist/PLACEHOLDER` applies to
builds only, and `webCheckSpec` points `EVENER_FRONTEND_VITE_CACHE_DIR` at each
check's private scratch root so Vitest's dependency cache never lands in the
checkout's `.vite-cache`. The other `npm test` phases match: `check-package-tests`
passes `--configLoader runner` to its own Vitest spawns, and the `node --test
scripts/*.test.mjs` phase loads its configs with the runner loader
(`viteDepCache.test.mjs`, `browserguard-vite-config.test.mjs`,
`distPlaceholderPlugin.test.mjs`, and `browserguard-vite.mjs`, which that phase
spawns; `editorial-preview.test.mjs` does the same and keeps its loads inside its
own temporary copy).

A focused run in a read-only lane needs those same two things by hand, with the
real config:
`EVENER_FRONTEND_VITE_CACHE_DIR=<absolute scratch path outside the checkout>
npx vitest run <file> --configLoader runner --maxWorkers=2`. The value has to be
absolute: a relative one resolves against the config root and lands back in the
checkout. Keep a test-file filter, keep at least two workers (the vmThreads
isolation floor explained below), and discard the scratch cache directory
afterward.

The frontend unit gate sizes Vitest from the machine's spare capacity through
`scripts/lib/load-aware-workers.sh`: worker count is the CPUs the process may
actually use (affinity- and cgroup-quota-aware, not the host's advertised count)
minus the 1-minute load average rounded up, clamped to at least one and at most
four. A checkout where the helper cannot be read falls back to a flat four. On
top of that, `vitest_run_args` (scripts/lib/gate-budgets.sh) and vite.config.ts's
default never hand Vitest fewer than two workers. That floor is a correctness
bound, not a tuning one, and it lives there rather than in the shared helper: see
the vmThreads note below. The four is a ceiling, not a fixed pool: Vitest's own
default pool is `os.availableParallelism()`, which oversubscribes a 10-core host
under the combined load of `make test`'s sibling Go streams and starves otherwise
causal IndexedDB mutation completions past their test deadlines. Both levers are
start-time readings of a lagging metric, not admission control: runs that begin
together can still stack. Neither widens a timeout or replaces an awaitable
completion with polling. The heavy local gates compete for CPU, so run `make
test-web`, `make vet` and `make lint` one at a time.

`LOAD_AWARE_LOAD1` replaces the measured load average for every budget the helper
sizes. The CI `tests` lanes set it to 0, because a fresh runner's own checkout
and cache restore are still in the one-minute average when the gate starts, which
would otherwise shrink every Go budget on a 4-core runner. Vitest file isolation
prevents worker-count or file assignment from sharing module stores, panes, or
mocks; per-file teardown is still required for timers, clients, and listeners.

The suite runs on Vitest's `vmThreads` pool (vite.config.ts): each file gets its
own VM context, module registry and jsdom window, but workers are reused, so
jsdom (~450ms to load) loads once per worker rather than once per file. Three
consequences for test authors, because the global object is the jsdom window
itself:

- `localStorage` is a getter-only window property, so install a storage stub
  with `installLocalStorage` (src/storageTestUtils.ts) rather than assigning
  `globalThis.localStorage`.
- `window.location` is non-configurable and cannot be stubbed; UI code reloads
  through `reloadPage()` (src/shell/pageReload.ts), which a test spies on.
- `instanceof` fails for objects built in the runner's realm, such as
  `vi.mock`'s wrapper errors; check the brand
  (`Object.prototype.toString.call(e) === "[object Error]"`) instead.

With a single worker, Vitest batches every file of a VM pool into one shared
context and isolation is gone, so the gate never sizes Vitest below two workers,
and src/testSetup.ts fails the second file that lands in a used context. Running
one file with `--maxWorkers=1` is fine. Workers are recycled at `vmMemoryLimit`
(512MB); VM contexts otherwise grow a worker's memory file after file.

A test that has to get past a real debounce or timer uses Vitest's fake timers
rather than waiting it out, with two pieces of wiring so Testing Library keeps
working: `vi.stubGlobal("jest", { advanceTimersByTime: vi.advanceTimersByTime })`,
because `waitFor` and `findBy*` only advance a faked clock when they find a
`jest` global, and `userEvent.setup({ advanceTimers: vi.advanceTimersByTime })`,
so typing delays advance it too. Fake only the timer functions (`toFake:
["setTimeout", "clearTimeout", "setInterval", "clearInterval"]`);
src/panes/spawn/Spawn.test.tsx is the worked example. To read a Tooltip from a
test that otherwise runs on real timers, use `hoverForTooltip`
(src/widgets/tooltip/tooltipTestUtils.ts): it crosses the show delay on a fake
clock scoped to the hover, so it needs none of that wiring, and it must be called
on real timers.

The frontend has no jest-dom (it is not in `package.json`), so matchers such as
`toHaveValue`, `toBeDisabled` or `toBeInTheDocument` fail `npm run typecheck`
even when vitest, which never typechecks, runs them green. Assert on the DOM
directly (`input.value`, `el.textContent`, `button.disabled`,
`document.activeElement`).

`await user.click(...)` returns once the event is dispatched, not once the
handler's async work finishes. An effect that sits behind an `await` has to be
awaited, never asserted right after the click. Work that goes through durable
mutation storage (a send, steer, queue, drain, Stop, queue edit, note save or
clear, and `threadsStore.forceStop`, which writes its cancellation durably before
the RPC) is awaited with `flushPendingTurnsProjectionForTests()`
(src/panes/session/composer/queue/testing/flushPendingTurnsProjection.ts), and
the test asserts directly afterwards. Every storage transaction registers with
the projection work tracker, so the flush returns once the whole chain has
settled, and the dispatched request has gone out by then. The flush waits only for
work registered with that tracker: if production code adds an awaited step outside
storage (say, a component awaits a store call before clearing its state), wrap
the whole operation in `trackProjectionWork` (src/stores/projectionWork.ts), or
the flush returns early. A `waitFor` on the request races that chain against a
1000ms ceiling, which a loaded host can outlast, and it stops as soon as the
request appears, with the receipt's settle and the refresh after it still to
come. A test that holds tracked work open on purpose (a held read or commit)
cannot flush until it releases it; it awaits the request at the fake's handler
instead. Other async effects are waited for with `waitFor` or a `findBy*` on the
result. Leaving any of this unawaited is worse than a flake in one test: the
store calls `requireClient()` when the RPC finally goes out, which by then can be
the next test's fake client, so the next test sees an extra call. For the same
reason a suite resets every global store it renders against, such as
`resetToastStoreForTests()`, in `beforeEach`; otherwise a toast from the previous
test can satisfy this test's assertion.

The connection store follows the same rule: reset it with
`resetConnectionStoreForTests()` (`stores/connection.ts`) rather than a
hand-written `setState`, and wire a ready fake with `connectReadyFakeClient()`
(`src/testing/connectionTestUtils.ts`) rather than a per-file
`connectFakeClient`.

### Frontend store-fixture contracts

Five contracts that repeatedly bite store-test authors, none of them visible
from the assertion that fails:

- A store fixture's `thread/read` handler must echo the request generation
  (`requestGeneration: params.requestGeneration ?? …`), or the response is
  thrown away: `readDisposition` (appwire-client/typescript/reducer.ts)
  discards a latest-window response whose `requestGeneration` is older than
  the held history's `issuedGeneration`, so a handler returning a static
  fixture with a stale default generation turns the read into a silent no-op
  — issued on the wire, never applied to the model. Assert on wire request
  counts when only issuance matters.
- `holdNextWriteTransaction(stores)`
  (src/stores/testing/stalledIndexedDB.ts) matches the next readwrite
  transaction opened over exactly that store set, not the cache method that
  opened it, so an earlier matching transaction can consume a broadly armed
  hold. Arm it immediately before the intended real `put` call
  in a call-through spy, and observe the method entry and the hold's
  `reached` boundary separately. The helper lets fake-indexeddb commit and
  withholds the completion event; it does not prove an active transaction
  lock blocks other writes, so diagnose a stalled successor from its actual
  pending operation.
- To test a boundary before commit, while the write can still abort, hook
  the request itself: wrap the object-store method in a call-through spy
  that either holds the request's `success` event with `holdIndexedDBEvent`
  or calls `this.transaction.abort()`. Match the call by `this.name` and the
  exact key the caller builds, not by call order, since production may add
  an earlier read. Worked example: src/stores/humanNoteDrafts.test.ts.
- fake-indexeddb throws a synchronous `DataError` from the store method
  itself for a put/add whose key cannot be derived (`buildRecordAddPut`
  throws before any request is created). Such a test proves method-fault
  handling only; request-error and rollback proofs need an actual failing
  request, its observed error and transaction abort, and a reopen check that
  earlier writes did not persist.
- Both the real client and `FakeClient` isolate notification-handler errors
  — a throwing subscriber must not abort dispatch — so an assertion inside
  a notification callback is swallowed instead of failing the test, and the
  failure surfaces later as an unrelated stall or timeout. Capture callback
  assertion errors outside the catching pipeline and assert them in the test
  body.

The console guard (src/testConsoleGuard.ts) fails a test on any unspied
`console.error`/`warn`/`log`/`info`/`debug` call, with a message that
names the call and the `vi.spyOn(console, …)` recipe. A diagnostic probe
that prints its
observations therefore reports a phantom failure: record probe observations
to a scratch file or spy on the console.

### Navigation fixtures

- A session summary in a v3 navigation graph may not nest other sessions:
  the TS codec (appwire-client/typescript/state/navigation/codec.ts) and Go
  schema both enforce flat session rows ("Session summaries are flat; the
  empty owned slot preserves graph structure", navigation_schema.go). A
  fixture that nests fails the TS codec as `invalid resource graph`, not as
  the behavior under test. Prove a recursive roster walk at the
  selector/helper level, and
  discovery through real stores with flat rows.
- The store's progress validator rejects an empty page only when it still
  reports data ("… returned no rows with remaining data",
  state/navigation/store.ts), and the codec accepts `sessions: [],
  remaining: 0, truncated: true`, so `remaining === 0` alone is not proof
  that a roster sweep completed. Absence proofs must check truncation; keep
  the ordinary non-truncated empty terminal page as a removal control.

### Whole-system residue contract

A `make build && make test` cycle leaves nothing outside its declared output and
reusable-cache roots (the built binaries, `cmd/evener-hub/frontend/dist` and
`node_modules`, and the Go build, npm and module caches) and no persistent
process, listener, Unix socket, or browser, Vite, Crashpad or Chrome profile
residue.

Scratch created by the build and default test streams is owned by the enclosing
test process: each stream receives private home, temporary, and XDG roots, and
the runner removes those roots only after its children exit. Browser profiles
belong to the browser-guard application lifecycle and are removed only after the
exact Chrome/Vite owners exit; failed cleanup retains that evidence. Continuation
state created by tests lives inside their process-owned state root, and no
resumable Evener session owns anything a green run removes. A disposable
container (Apple container or Docker) is a way to audit this contract by
snapshotting the filesystem and process table around a cold and a warm cycle, not
a local or CI gate dependency.

## The Native Gate Bundles the App

The native npm test command uses
`scripts/native/run-tests.sh` to size Vitest with the shared load-aware budget.
The pool is capped at four workers and shrinks to one when spare capacity is
exhausted. An unavailable machine probe falls back to four. Native uses isolated
forks, so it can run with one worker without the frontend VM pool's two-worker
floor. The budget is sampled when each command starts; it does not coordinate
concurrent gates or change per-test deadlines or file isolation.

`make test-native` starts by bundling the real iOS entry point (`make
test-native-bundle`, `scripts/native/test-native-bundle.sh`). Nothing else in the
repository runs Metro, and that is the whole reason the target exists.

The native app has three resolvers and only one of them ships. Vitest resolves
through Vite, `tsc` resolves through TypeScript's own resolver reading `tsconfig`,
and the app on a device resolves through `mobile-native/metro.config.js`: its
`watchFolders`, its `nodeModulesPaths`, and the `resolveRequest` hook that
redirects specifiers coming from the shared `mobile/` and frontend sources. A
regression in that third resolver (a stale alias, a dropped `watchFolders` entry,
a package that moved) is invisible to the first two and would otherwise fail
first on a device or in a TestFlight build.

So the gate bundles: `npx expo export --platform ios`, which drives Metro over
`index.ts` without a device, a simulator, or a running packager, and exits
non-zero naming the unresolved specifier and its import stack. It runs with
`--clear` and a private `HOME`, `TMPDIR` and XDG roots so the verdict is never
inherited from a warm Metro cache, and it runs ahead of the Vitest suites because
a bundle that does not build is the cheaper failure to read. iOS only: that is the
platform the app ships on, and the app tree carries no platform-specific source
files. A run that exits zero without writing an iOS bundle fails too.

A hung bundler is bounded by `timeout 900` where coreutils provides it (the ubuntu
runner, or a Mac with `gtimeout`) and by the CI step's own `timeout-minutes`
everywhere else.

## The Test Timing Ratchet

`make test-timing-budget` measures per-Go-package test wall time, plus one
aggregate for the frontend, against the checked-in `testing-budget.json`, so a
timing regression cannot silently erode the suite's runtime. It fails at 1.5x a
package's checked-in budget and warns at 1.1x, and it applies a per-test ceiling
segmented by test class. It reuses `gate-surface-lib.sh`, so it measures the same
surface `ROOT_FULL=1 make test` proves.

### Per-Test Ceilings Are Segmented by Test Class

A per-test ceiling checks a test's own wall time, independent of its package's
budget: one runaway test is a defect on its own. `testing-budget.json`'s
`perTestCeilingClasses` maps each class to a ceiling and to the tests that belong
to it, by test name (a subtest inherits the class of the test it is under). A
class ceiling is the union of the class's largest CI observations across two CI
runs plus 25%, rounded up to a multiple of five seconds, so a class holding a
48-second test is not held to a 3-second number and a member that doubles still
trips its ceiling.

| class | ceiling | what it covers |
| --- | --- | --- |
| delegate-attention & idle | 40s | delegate attention and idle lifecycles |
| deploy-host integration | 30s | host deploy and remote-host attach |
| session-activity | 30s | session-activity tree capture |
| retirement | 30s | daemon retirement lifecycles |
| eval harness | 20s | memory and prompt evals |
| plugin-git integration | 15s | plugin materialization over real git |
| live-stack E2E | 15s | hub end-to-end suite |

A wall ceiling on a test whose time tracks runner load measures the runner, not
the code, so such tests are deliberately unclassified: dev-tool compile and CLI
tests, install tests, large transcript fixtures, gate-runner integration, and
bounded-wait unit tests whose whole job is to sit out a wait. A parallel test
whose wall time tracks its package's parallel load
(`TestLoadSessionJobActivityTree_TuesdayFullyFoldsAndFullyPaginates`) stays out of
its topical class for the same reason. A class of one member is a smell, so the
web repo-audit test is split into one case per slice of the tracked TypeScript
test files and stays unclassified.

A test named in a class is compared **strictly**: a breach is nonzero under
`CHECK=1` in a CI-shaped environment. A test in **no** class keeps the flat
`perTestCeilingSeconds` (currently 3s; 2s when a budget file names none) and is
**warn-only in every mode**, `CHECK=1` included, so the unclassified backlog
cannot red the gate; give the test a class to enforce it. A test may be named in
exactly one class; a name matched by two classes is a config error that fails
the run. A malformed class (a spec that is not an object, a non-string or empty
`tests` list) fails naming the class and drops it, leaving its members on the
flat warn-only ceiling. A class
`ceilingSeconds` that is not a finite, float-convertible number fails in every
mode, naming the class and the field; a huge but finite value is a legitimate
ceiling that simply disables that class.

Enforcement of the package ratios is conditional. A local run only warns. A
package over 1.5x its budget is nonzero only under `CHECK=1` in a CI-shaped
environment (`$CI` set, or `--strict`). A `testing-budget.json` that is missing, or whose `packages` map is empty, is an
explicit warn state, so `CHECK=1` exits zero until `make test-rebaseline` lands a
measured baseline. A file that cannot be parsed, or whose top level, `packages`,
`perTestCeilingSeconds` or `perTestCeilingClasses` has the wrong type, or whose
package budget is not a finite number, is a broken config and fails in every
mode, naming the file and the entry.

The checked-in baseline carries the `"metric": "package-wall-seconds"` marker, so
per-package ratios, the `"web"` row included, are enforced under `CHECK=1` in CI.
Each number is the slowest wall time seen across recent green CI runs on main,
floored at 2s for Go packages so sub-second packages do not trip the ratio on
runner noise. A budget file without the marker downgrades every ratio failure to
a warning with its numbers; a full rebaseline on a CI-class host records the
marker, while a narrowed `--modules`/`--no-web` rebaseline never adds it and only
preserves one already stamped.

A broken measurement is not conditional. `go list` or `go test` exiting nonzero,
or a package `go list` reported that the `go test -json` stream never mentions, is
nonzero in every mode, and `--bless` refuses rather than record a budget from a
stream that may have stopped early. `--measured` takes an already-measured file,
so nothing vouches for the packages a bless would record, and `--bless` with it is
refused for that reason. A bless writes every package it measured and preserves
the rest of the file, so a narrowed run (`--modules`, `--no-web`, or a frontend
that is not checked out) refreshes part of the file instead of deleting the
entries it did not measure.

Enforcement reads the gate's own run, not a second one. When
`EVENER_GATE_TIMING_TSV` names a file, `make test`'s runner captures the ratchet's
rows from the tests it already runs (plain modules through `go test -json`, a
sharded package from its shards' own `-test.v` logs, the frontend from the vitest
JSON reporter) and writes them there on a green run. `make merge-approval-gate`
passes that file to `--check --measured`, so the budget is enforced there without
a second non-fuzz run; CI's two Go test lanes and its web lane each check the rows
their own scope produced. A run that writes no file fails the check rather than
passing silently.

## The Browser Guards

jsdom evaluates no cascade and reports zero for every box, so an entire class of
frontend defect is structurally invisible to `vitest`. The guards in
`cmd/evener-hub/frontend` cover different contracts:

- **`npm run layoutguard`** measures HAND-AUTHORED markup against the real
  `tokens.css` and component stylesheets, in headless Chrome. Cheap: static files,
  no build. Right for "does this CSS rule still hold its box".
- **`npm run overflowguard`** renders the REAL Session pane through the REAL
  reducer and asserts nothing inside it scrolls sideways, at four widths.
- **`npm run shellguard`** renders the production AppShell (rail + workspace) with
  a scripted tall sidebar tree and asserts the PAGE never grows taller than the
  viewport: the rail's own body is the scroll container, not the document. The
  Overview sweep also opens the Tasks tab over a scripted task list taller than any
  swept viewport and asserts the tab's own scroller owns that list while the
  document stays at the viewport height. The floating fixture waits for the
  restored workspace before opening Settings. A held lazy DockHost request
  verifies that it captures no pane ID before restoration, then proves real
  placement, overhang, native input and resize after the request resumes.
  Workspace restoration and Dockview stay real.
- **`npm run spawnguard`** renders the real Spawn pane through its staging and
  breakpoint path, asserting the responsive form remains usable at three widths.
- **`npm run transcriptscrollguard`** renders the real transcript through the real
  virtualization stack and drives NATIVE scroll events: the jump-to-latest pill
  must appear on a scroll away from the bottom, and clicking it must land at the
  true bottom of the settled geometry and stay there, including after content
  grows below the reader and after the scroll port itself shrinks (the pane header
  growing). Paged-open readiness waits through asynchronous cache/read admission
  until the actual scroll port mounts and its geometry settles; an explicitly
  deferred read exercises those pre-mount frames without a fixed delay or a wider
  readiness deadline. A pane that unmounts under any post-settle wait must report
  it by name ("unmounted during the scroll-away wait"), never as the harness's own
  mount error.

Layoutguard covers static geometry; the next three cover the Session pane, the
AppShell, and the Spawn pane, each with its own responsive layout and failure
modes; the last covers scroll behavior, which only exists inside a real
virtualization engine with real measurements. Overflowguard renders the real pane
because hand-authored markup freezes whatever was current when the case was
written, so restoring an old glyph would leave layoutguard green while the app
broke.

A new dev harness entry that mounts the app-router SPA itself (none of the
existing `src/dev/*-entry.tsx` guards does; they mount fixtures or shell
components directly) needs two things, or it presents as a dead document with no
error captured anywhere: reset the route before the app import, since a
router-driven shell renders NotFound from the harness path, so call
`window.history.replaceState(null, "", "/")`; and set a synchronous boot-marker
global before any top-level `await import("../main")`, because the `load` event
fires while that import's top-level await is still pending, and a boot check on a
global set after it re-navigates the live document forever. Order the entry:
synchronous install/setup, boot marker, `replaceState`, then the dynamic import,
and wait for the real evidence global separately, with its own timeout.

The full-stack skill guard is a different KIND of check:

- **`web-skillguard`** (`make test-web-browser`'s last step;
  `cmd/evener-hub/skill_composer_browser_test.go` driving
  `cmd/evener-hub/frontend/scripts/skillguard/run.mjs`, behind the `browserguard`
  build tag) proves canonical SKILL SELECTION through the real stack: the
  production web app served by a REAL hub (roster prober, past index, auth URL),
  driven in real Chrome by a Node CDP driver, against TWO REAL `evener serve`
  daemons compiled from `cmd/evener`'s own test binary. The only scripted piece
  sits at the external LLM provider adapter. The browser asserts the composer's
  chips, drafts, queue, steering, attachments, failed-activation retry, and
  offline outbox behavior through real DOM gestures; the Go test asserts what the
  daemons ACTUALLY received (provider request payloads, `<skill-context>`
  documents, durable transcripts, held-turn choreography through the fixture's
  control IPC). The guards above test the frontend against scripted clients; this
  one is the only place the frontend's skill contract is tested against the
  daemons and hub that must honor it. It needs the BUILT frontend (the hub serves
  the embedded dist): `make test-web-browser` builds it first via `build-web`, and
  the gate (`evener-dev dev web-browser-guards`, cmd/evener-dev/webbrowser.go)
  still builds one when the dist is missing rather than skipping.

These guards are owned by `make test-web-browser`, which is required by the CI web
job and remains separate from `make lint` and `make test` because it needs Chrome.

Three traps each produce a false green.

**A fixture must use REGISTERED types.** An overflow harness that seeded item
types `"thinking"` and `"notification"` measured a debug renderer for them and
reported PASS: neither is registered (the set is `agentMessage / reasoning /
steering / systemMessage / userMessage / warning`; `grep registerItemRenderer`),
so both fell through to `RawItemView`, the debug fallback. **An unregistered type
does not throw; it silently renders something else.**

**`scrollWidth > clientWidth` is not "scrollable".** It is true for any element
whose content exceeds its box, *including* one deliberately clipping with
`text-overflow: ellipsis`. Only computed `overflow-x: auto|scroll` puts a
scrollbar under a reader's finger. Also worth knowing on its own: **`overflow-y:
auto` with no `overflow-x` declared computes `overflow-x` to `auto`, not
`visible`**, so every such element is silently a horizontal scroll container too.

**`display` on a `<details>` replaces the UA's skipped-contents mechanism.**
`display: flex` on a `<details>` keeps its non-summary children laid out while
COLLAPSED, so a collapsed disclosure leaks a sliver of its body. Scope such rules
to `.details[open]`.

A guard is only worth its run time once it has been mutation-tested: break the
thing on purpose and confirm it fails, naming the right element. Commit or copy
your real work first, then revert the declaration, run the guard, confirm it fails
for the right reason, and restore the file with `git restore -- path/to/file`.
Never use `git checkout --` or `git restore` on a file that carries uncommitted
work you have not set aside: it discards everything in the file, mutation and real
edits alike.

Three more things about writing a layoutguard case:

- **A state a page script cannot reach needs `CSS.forcePseudoState`.** There is no
  way to synthesize a trusted hover, and a programmatic `.focus()` does **not**
  match `:focus-visible` (the element stays unmatched at opacity 0). A case
  declares `forcePseudoStates` in its `case.json` and the runner pins them before
  measuring. This proves the *cascade* applies the rule; whether Chrome's own
  heuristic calls a given focus "visible" is Chrome's contract, not ours. Put each
  state on its own copy of the markup in one harness so a single measurement
  covers all of them with a resting control.
- **Switch transitions off in the harness.** `getComputedStyle` reads the value at
  that instant, so a measure taken right after a state changes reads the *start*
  of a 120ms opacity ramp, not where the cascade settles. Waiting for
  `transitionend` instead hangs forever in exactly the regression the case exists
  to catch: no rule, so no transition, so no event.
- **A fixture can make a declaration unfalsifiable.** An `<img>` with no height
  still gets one from its intrinsic aspect ratio, so a *square* test image makes
  `height: 100%` redundant and deleting the declaration leaves the case green;
  use a non-square (8x4) image. Include `styles/global.css` in any case that
  measures boxes: `box-sizing: border-box` lives there, and it is the difference
  between an 80px and an 82px tile.

Geometry also cannot see `object-fit`: dropping `object-fit: cover` leaves every
box identical and only changes how pixels are scaled inside the image box. Say so
in the case rather than letting a pass imply coverage it does not have.

### What the browser gate owns, and how it cleans up

Make invokes each guard's Node entrypoint directly, so an interruption reaches the
process that owns cleanup rather than a wrapper that does not. Each guard receives
private process-home, temporary, and XDG roots and disables Node's automatic
compile cache. Chrome runs against a private profile with crash reporting disabled
and Crashpad state kept beneath that profile, and on a fresh Darwin profile it
uses a mock keychain so network startup cannot block on ambient credentials.

Cleanup first awaits Chrome's CDP `Browser.close`, then falls back through a
bounded TERM-to-KILL escalation when CDP fails or stays pending. On POSIX, Chrome
and Vite run in detached process groups and cleanup removes the profile only after
each exact group disappears; Win32 uses direct-child exit handling instead.

macOS Crashpad escapes Chrome's process group, so cleanup captures the handler
before shutdown and rescans afterward for only the handler carrying the canonical
random profile's exact database argument. That escaped helper is
observation-only: cleanup waits for that exact identity within a bounded grace
period, and on failure retains the private profile rather than signalling a
numeric PID the OS may already have reused.

Green output contains only the three verdicts and removes the owned root. Any
guard error, Vite failure, cleanup failure, or missing Chrome/Chromium is
nonzero; a failed guard's log is replayed, and a failed or interrupted root is
retained and named. An interruption waits for the cleanup it owns.

### Why there is no WebKit or Safari runner

Chrome/Chromium is required and there is no WebKit or Safari runner. That gap is a
deliberate ceiling: Playwright's WebKit cannot diverge `vh` from `dvh`, because no
browser-chrome emulation exists in its API, and the iOS-simulator plus
`safaridriver` route needs sudo enablement and WebDriver plumbing we chose not to
carry. WebKit/Safari is an explicit unsupported and manual gap, never a pass.

Dynamic-viewport enforcement is instead the CSS-text contract in layoutguard's
`mobile-shell-viewport-height` case plus the `AppShell.test.tsx` source contract;
true geometry verification stays manual and on-device.

## A Test That Never Runs

A test that does not execute is worse than a missing test: it reports the coverage
without providing it, and the suite stays green either way. Three shapes in this
repo produce one, and none announces itself.

**Registered `check*` functions.** Several packages drive their behavioral
contracts through a fuzz entry point that replays one check selected by the fuzz
input: `FuzzFSPathsBehaviorProgram` and friends in `cmd/evener-hub/internal/`
(`fspaths`, `hostlock`, `hubedge`, `launchconfig`). A `check*(t *testing.T)`
function in those packages runs **only** if it appears in its `checks :=
[]func(*testing.T){…}` seed table. Write one, forget the table entry, and `go
test` passes without ever calling it.

The reachability proxy is `golangci-lint run ./path/to/pkg/`, whose `unused`
linter reports the unregistered check as a dead function:

```
paths_test.go:411:6: func checkSanitizeDirPrefix_PreservesLoneTrailingDot is unused (unused)
```

`go vet` does **not** catch this. Run the linter after adding a check, and state
which table each new check is registered in when handing work off.

**A shell test that redirects `TMPDIR`.** macOS `mktemp -t` resolves against the
per-user temp path (`confstr(_CS_DARWIN_USER_TEMP_DIR)`) and ignores `TMPDIR`, so a
test that sets `TMPDIR=$scratch` to observe a script's temp handling observes
nothing: the script writes to the real temp dir and every run litters the
machine. Fake the `mktemp` binary on `PATH` instead of faking the environment.

**A stylesheet assertion that matches its own comment.** A test that greps CSS text
(`expect(css).toContain("flex: none")`) will match the declaration quoted in a doc
comment above the rule, and can pass with its implementation deleted. Read the
stylesheet with `readModuleCss(import.meta.url, "x.module.css")` from
`src/styles/cssBlock.ts`, which strips comments, and pick rules with
`topRuleBlock` / `mediaBlock` from the same file.

The general rule: **prove a new test can fail.** Break the thing it covers, watch
it go red, then put it back. A test you have only ever seen pass has not been
tested. Two corollaries:

- "No tests" is not "tests passed". A broken file makes vitest print `Tests no
  tests` next to a transform error; a grep for `Tests ` reads that as benign.
  Check exit codes, never a grep of piped output.
- Assert the mechanism, not a side effect a broken implementation also produces.
  An "onAdd called once" assertion passes with validation entirely removed if
  committing the add unmounts the panel either way. Asserting the validate call
  itself distinguishes them.

**A fixture whose content nothing reads.** `execenv.detectDocumentFormat` decides
a document's format on the FILENAME first (`.pdf` wins outright) and only sniffs
the five-byte `%PDF-` signature when the extension does not claim a PDF;
downstream, `tool.dispatchedResult` exempts `application/pdf` from raster decoding
entirely, so nothing past the environment looks at the bytes again. That
exemption is deliberate (a PDF is not something `llm.RasterMediaType` can decode)
and is stated as a contract next to the fixture, in
`session_tools_core_exact_fuzz_test.go`. That file is `//go:build evenerfuzz`, so
`make fuzz-seeds` reads it and `make test` does not; several untagged tests hold
the same rule.

So a document test named `*.pdf` cannot be byte-sensitive, whatever you put in it.
The only oracle that reads the content is the same path with an extension that
does NOT claim a PDF, run twice against different bytes
(`TestReadFile_RealPDF_DetectedByItsBytes`). And a passing case licenses a small
claim: five bytes are checked, so "handles a real PDF" means "accepts the
signature". A fixture is only honestly a PDF if something proved it parses:
`validPDFFixture` computes its cross-reference offsets from the bytes as they are
written and `TestValidPDFFixtureCrossReferencesResolve` resolves them back.
Grepping for `%PDF`/`xref`/`trailer`/`startxref` markers is not validation.

## A Subprocess Can Outlive Its Context

A context on `exec.CommandContext` bounds the child, not the pipes. Captured
stdout and stderr are read until EOF, and EOF waits for every process holding the
write end: a child that backgrounds anything (a wrapper script, a shell rc file, a
hook, ssh's ProxyCommand) hands it to a grandchild the context never kills, and
`Wait` lasts as long as that grandchild. So an Evener call that captures a child's
output must set `cmd.WaitDelay`, which caps how long exec waits for the pipes once
the context ends or the child exits, and passes the result through
`orphanpipe.ChildErr`, which reads `exec.ErrWaitDelay` after a successful exit as
the success it was.

Prove the bound with `execsupport/orphanpipe/orphanpipetest` rather than a
stopwatch. `New` stages the FIFOs, `WriteScript` writes the fake executable,
`Spawn` is the shell fragment that backgrounds a grandchild holding the script's
stdout and stderr, `AwaitStarted` waits until it holds them, and `Await` returns
the call's result while the grandchild still does, failing the test if the call
only returned once the grandchild was released.

## Prove a Wait with a Signal, Not a Window

"X does not happen while Y is held" is tempting to test by holding Y for a second
or two and checking that X did not happen. That window is a guess: on a loaded
host a broken implementation can take longer than the window to do the wrong
thing, so the test passes anyway. Give the code a test-only seam that fires at the
moment in question instead, nil in production, and let the test hold the work
until the seam says the code is waiting:
`SessionConfig.testOnly.closeAwaitingEnvWork` fires when a close blocks at its
environment-work join; the shard runner's `shardsConfig.slotWait` fires when it
holds a shard back for a free slot. A seam that fires just before the wait it
reports needs its own test that the wait really follows: see
`TestEnvWorkJoinWaitsAfterSignallingUntilItsBudgetEnds`, which spends the join's
budget from inside the seam and requires the warning only a parked join can
produce.

## Real `git` in Worktree Tests

`git` is an external dependency like the LLM provider, and the same boundary rule
applies: keep it real when git's own behavior is the thing under test, and script
it when git is only a way to reach an Evener decision.

A real-git worktree test costs roughly thirty times a scripted one (about a second
and a dozen `git` subprocesses against a few hundredths of a second), nearly all
of it process spawn rather than assertions, so the choice matters for suite time.

### Which harness

Use the **real-git** harness (`newWorktreeRepo` and friends, in
`agent/session_tools_worktree_create_test.go`) when the assertion depends on
something only git can produce:

- real registry effects: `worktree add/remove/lock/unlock/prune` actually landing,
  `.git` pointer file contents, deregistration
- real ancestry or patch-equivalence: `merge-base --is-ancestor`, `git cherry`,
  merged/unmerged/adopted outcomes over real commits. The scripted model refuses
  these outright, so a converted test fails loudly rather than reading as merged.
- dirtiness from a *modified or staged tracked file*, real ahead-counts, and git's
  own refusal to remove a dirty worktree without `--force`. The scripted model
  derives dirtiness from untracked files it can see on disk, so an untracked-file
  assertion may stay scripted; it refuses to answer `rev-list --count` at all
  (like the ancestry verdicts above), so an ahead-count assertion belongs on real
  git.
- the real `--porcelain` output shape, including flags like `prunable` and a
  reasonless `worktree lock`
- git's ref rules, e.g. that it rejects the branch name `HEAD`
- symlink canonicalization against git's canonical registry path
- `ResolveMainRepoRoot`'s structural walk and its git-binary fallback
- concurrency that relies on git's own index/ref locking to serialize

Use the **scripted** boundary (`scriptedLaneRepo` in
`agent/worktree_scripted_lane_test.go`, or `scriptedWorktreeSession` in
`agent/session_tools_worktree_scripted_test.go`) when the subject is Evener's own
behavior:

- which validation or refusal rung fires (asserted as a sentinel or code, never as
  error text)
- what event or warning was emitted, and how many times
- what Evener wrote to its own state: sidecars, jobstore records, disposed marks,
  gate flags, `SessionMeta`
- control flow: budget expiry, ordering, retries, "declined to touch"
- argument validation that returns before any git call

Both harnesses keep sidecars and `.git` pointer files as real files on disk. Only
the `git` subprocess boundary is replaced, via
`SessionConfig.testOnly.worktreeGitRunner`.

### The failure mode to avoid

`scriptedWorktreeGit` is a *semantic model* of git, not git. If you script a
behavior the model does not really implement, the test passes while proving
nothing. For example, a model arm for `check-ref-format --branch` that hardcoded a
rejection of `HEAD` would let a test whose entire purpose is "real git rejects the
reserved name HEAD" pass against the fake regardless of git's actual rules, so
that test stays on real git.

The model's unknown-argv arms return an `unsupported argv` error **on purpose**.
If a converted test reaches a command the model does not implement, it fails
loudly rather than silently passing. When that happens, either model the command
honestly or leave the test on real git; never stub the specific answer the
assertion is looking for. `merge-base --is-ancestor` and `cherry` refuse for the
same reason even though the model recognizes them: their answer is a verdict over
a commit graph the model does not have, so any answer would be that stub.

### Building a real repository

Build real-git fixtures with `gittest` (`gittest.Run`, `gittest.NewLinkedWorktree`)
instead of writing a local `runGit`. It runs git under `gittest.Env`: ambient
`GIT_DIR`, injected `GIT_CONFIG_*` and the developer's global config cannot reach
the fixture. Code in the `identifier` module cannot import it (that module depends
on nothing else in the repository) and keeps its own helper. A fixture builder
that wants to prove it never copies a repository while git maintenance is still
writing into it wraps its runner in `gittest.MaintenanceProbe`.

### Adding a worktree test

Default to the scripted boundary. Reach for real git only when you can name the
git behavior the assertion depends on, and say so in the test's comment so the
next reader does not "optimize" it onto the fake.

## Seeding Hub Fixtures

A hub fixture's session and project identifiers are encodings, not names: a
session id is a 22-character base62 UUIDv7 payload, and a project directory is
`<readable>-<10 base62>`. A hand-written placeholder that looks plausible is
rejected, and `PastIndex.Rebuild` then leaves the seeded session out of the
index, so the fixture is invisible rather than wrong, and the test failure points
nowhere near the id.

Mint them with `cmd/evener-hub/internal/hubtest` instead of writing them out:

```go
sessionID := hubtest.SessionID(t)            // e.g. 02wMz5Txv1C3Hut0M8GCeB
projectDir := hubtest.ProjectID(t, "alpha")  // alpha-0123456789
```

When the fixture has a real checkout on disk, use `identifier.ResolveProject`
instead: the hub cross-checks a project's id against its working directory, and
only the resolved id matches.

Rebuild names what it refused to index and why, so a seeding mistake that does
slip through shows up on the hub's stderr:

```
[hub] past index: skipped /…/projects/alpha-0123456789/sessions/placeholder.meta.json: invalid session id (want a 22-character base62 UUIDv7 payload): invalid UUID payload
```

## Driving Turns from a Go Fixture

A session runs one turn at a time. A `turn/start` during an active turn is
refused with `turn is already active`, so a fixture must wait for each turn to
finish before starting the next.

To wait on the turn lifecycle, call `thread/read` with `ThreadReadParams{Ref: ref,
Subscribe: true}` on the same daemon connection before `TurnStart`. Without
`Subscribe: true` the read returns a snapshot and subscribes to nothing, so
`Client.Notifications()` never carries that thread's `thread/status/changed` or
`history/updated`.

## A Disposable Hub Needs Its Own HOME

The hub is a host singleton guarded by an exclusive flock on
`$XDG_STATE_HOME/evener/hub.lock` (`$HOME/.local/state/evener/hub.lock` when
`XDG_STATE_HOME` is unset), and that path is deliberately not independently
overridable: the lock, the run dir, the state root, and the auth token all derive
from `userdirs.DefaultStateRoot()` (XDG_STATE_HOME, else `os.UserHomeDir()`, else
an error), so they only stay coherent when they move **together**. The blessed way
to run a second, disposable hub (an e2e harness, a scratch verification hub) is a
fresh HOME **plus** clearing every variable that can redirect evener away from it.
Both halves matter: `HOME=$(mktemp -d)` alone moves nothing when `XDG_STATE_HOME`
is exported (`userdirs.DefaultStateRoot` prefers it over `$HOME`), so the
"disposable" hub silently claims the real `$XDG_STATE_HOME/evener`; and
`EVENER_PROVIDERS_CONFIG`/`EVENER_CREDENTIALS_CONFIG` outrank
`$HOME/.config/evener`, so a stub `providers.toml` in the throwaway HOME is
overridden and the daemon calls the real provider. The recipe below routes through
`e2e_isolate_home`, the same helper the e2e harnesses use, so it cannot drift from
that list:

```bash
set -euo pipefail
run_dir=$(mktemp -d "${TMPDIR:-/tmp}/evener-disposable-hub.XXXXXX")
. scripts/lib/e2e-lib.sh
e2e_isolate_home "$run_dir"
./evener hub -addr 127.0.0.1:0 -evener ./evener
```

Never point a test hub at the real HOME "just for a quick check": if the real hub
happens to be running you collide (the flock error names the lock file it lost);
if it happens to be **stopped**, the test hub silently claims the real
`~/.local/state/evener`, the dangerous case, because nothing fails. A
lock-path-only override is rejected for exactly that reason: it would unbundle the
singleton guard from the state it guards.

## A Live Run Uses the Machine's Build Cache

None of the live-test commands carries a `GOCACHE=` prefix, and adding one is a
regression. A per-command cache under `/tmp` duplicates the machine's warm cache
on the checkout's volume, which can fill it. There is nothing to isolate, either:
the build cache is content-addressed, so a live run and the default suite cannot
corrupt each other's entries. A stalled cache volume surfaces as an ordinary hung
`go` command, as with any other unmounted-volume failure.

## MCP Server E2E

The MCP manager has opt-in live tests against `npx -y
@modelcontextprotocol/server-everything`. They are intentionally not part of the
default suite because they depend on an ambient Node/npm toolchain and may fetch
or use cached packages outside the repository.

Run:

```sh
EVENER_MCP_E2E=1 go test ./agent/internal/mcp -run 'TestRealMCP_' -count=1 -v
```

`EVENER_LIVE_TESTS=1` also enables these tests with the other live test suites.

## Environment Variable Tests

Supported runtime environment variables are defined in the `envvars` package and
documented in `docs/developing-evener/environment.md`. Production code, help text,
and test helpers should use those rows instead of hard-coded env names.

When adding a runtime env var:

- Add one `envvars.Var` row.
- Use the row's `Name`, `Getenv`, `LookupEnv`, `Trimmed`, or `Assignment` helper at
  call sites.
- Document it in `docs/developing-evener/environment.md`.
- Keep live-test opt-in gates explicit; a provider credential alone must not make a
  default test issue network requests.

## Targets

<!-- BEGIN GENERATED: make targets. Edit make/testing.mk, then run `make generate`. -->
| Command | Summary | What it proves | Trigger | Requires | Fails when |
| --- | --- | --- | --- | --- | --- |
| `make test-web` | The frontend's single gate entry point: typecheck, unit tests, then lint, run concurrently. | jsdom/unit-level frontend behavior, type safety, and source lint. | Local pre-merge; required CI web job. | The Go toolchain (the gate is the prebuilt evener-dev) and the installed Node dependencies; deterministic after those. Each check owns a private process home plus temporary/XDG roots and disables Node's compile cache; no real browser, provider, or network service. | Any of the three streams is nonzero; a missing or unhealthy frontend install fails preflight. |
| `make test-web-browser` | The real browser-only frontend guards (layoutguard, overflowguard, shellguard, spawnguard, transcriptscrollguard, retirementguard, mermaidguard) plus the full-stack `web-skillguard` (TestSkillComposerBrowser behind the `browserguard` tag) that jsdom cannot evaluate. | Headless Chrome evaluates real CSS geometry, the real Session reducer/tree, the real Spawn staging/breakpoint path, the real transcript scroll/jump-to-latest path, and the real selected-thread recovery contract when its daemon retires and is replaced; the skill guard additionally drives the production composer through a REAL hub and two REAL `evener serve` daemons with only the LLM provider scripted. | Required CI web job; local pre-merge on a Chrome-capable host. | Chrome/Chromium and the Go toolchain (the gate is the prebuilt evener-dev); each guard gets a private process home, temporary/XDG roots, and a private browser profile. No WebKit/Safari runner. retirementguard also needs the Go toolchain: its npm script runs the isolated TestRetirementBrowser fixture, which starts the Hub and drives the guard against it. The skill guard also needs the Go toolchain and the built frontend, which this target builds first (`build-web`); the gate still builds one when the dist is missing. | Any guard error, Vite failure, cleanup failure, or missing Chrome/Chromium is nonzero. |
| `make test-native` | The native iPhone app gate. | Metro bundles the real iOS entry point, the native Vitest suites plus strict native TypeScript compilation pass against the checked-in Expo/React Native sources, the `mobile-native/src` sources match the native Biome formatter config (`mobile-native/biome.jsonc`, the `npm run lint` step), and the hand-run scripts/*.mts tools still resolve their module graph under tsx. | Native CI; local pre-merge when native or shared mobile sources change. | Node 22.13+ and an already-installed mobile-native dependency tree; does not contact a hub or provider - script resolution is checked without loading anything, since every one of those scripts opens a socket the moment its body runs. | Bundling, native tests, shared-session tests, native typechecking, formatting, or script module resolution fail. |
| `make native-preflight` | Ensure the mobile-native dependency install is present, real, and lockfile-compatible before any native target runs. | mobile-native/node_modules exists as a real directory, matches package-lock.json, and holds an executable .bin/expo, so Metro bundles with the pinned Expo instead of whatever `npx` finds on PATH. | Setup prerequisite for the native gates. | Node 22.13+; never installs, refusing instead with the command to run. | node_modules is missing, is a symlink (the bundler resolves no module through one, whatever the lockfiles say), is older than package-lock.json, or has no executable .bin/expo; the message names `cd mobile-native && npm ci`. |
| `make test-native-bundle` | The native app's Metro bundling gate. | Metro resolves every specifier the real iOS entry point reaches — the app's own sources, the frontend sources its resolveRequest redirects, and the AppWire client wherever that package lives — and the export writes an iOS bundle. | Native CI (via make test-native); local pre-merge when native sources or metro.config.js change. | Node 22.13+ and an already-installed mobile-native dependency tree; no device, simulator, packager, hub, or provider. Runs with a private process home plus temporary and XDG roots and passes --clear, so the verdict never comes from a warm Metro cache. ~10s on a developer Mac, bounded by `timeout 900` where coreutils provides it (the ubuntu runner, or a Mac with gtimeout); without it the run is unbounded and the CI step's timeout-minutes is the backstop. | Metro cannot resolve a module, the export fails, or the export writes no iOS bundle. |
| `make check-podfile-lock` | Check that mobile-native/Podfile.lock locks exactly the iOS pods autolinking resolves. | every pod the Expo and React Native autolinking the generated Podfile runs would link, Expo's companion pods included, is in the lock's DEPENDENCIES under the same name and directory, and every autolinked pod the lock lists is still linked. | Native CI; local pre-merge when mobile-native/package.json, package-lock.json or Podfile.lock change. | Node 22.13+ and an already-installed, real (not symlinked) mobile-native dependency tree; no macOS, CocoaPods, Xcode or generated ios/ project. Expo's precompiled mode and extraPods are not handled. | an autolinked pod is not locked, or the lock lists an autolinked pod nothing links (a version change inside an already locked pod is not checked), or the script's own tests (check-podfile-lock.test.mjs) fail. |
| `make api-package-preflight` | Ensure the appwire-client/typescript dependency install is present and healthy before the qualification runner starts. | node_modules carries the pinned tsc and the ws the qualification runner imports, or is repaired with npm ci. | Setup prerequisite for test-api-package. | Node 22+; never runs npm ci through a symlinked node_modules. | node_modules is missing and npm ci fails, is a mismatched symlink, or lacks a working tsc / ws; on a real install the message names `cd appwire-client/typescript && npm ci`, and on a symlinked one it names the shared install instead. |
| `make test-api-package` | The independently consumable AppWire package qualification gate. | A packed package installs outside the checkout, exposes ESM and CommonJS runtime/type entry points, and executes its shipped read-only example against a scripted local WebSocket server. | Package CI; local pre-merge when protocol sources change. | Node 22+ and the protocol package's installed development dependencies (installed by api-package-preflight); qualification makes no external network requests. | Build, pack, outside-checkout install, runtime import/require, declaration checking, example protocol exchange or output validation fails. |
| `make test` | The default local test gate: Go modules (short mode) plus the frontend, run concurrently. | Root short-mode tests, other module tests, and frontend typecheck/Vitest/Biome all pass. | Local quick check; included by the merge gate. | Scripted/fake external boundaries for default tests; runs ZERO fuzz-family tests, even at reduced depth. WEB=0 skips the frontend stream. TEST_SCOPE picks the Go modules: all (default), root (the root module alone) or nonroot (every other module); CI runs root and nonroot on separate runners. | Any module, frontend stream, or setup failure is nonzero. |
| `make merge-approval-gate` | The canonical serial post-merge gate: lint, build, full tests, and native/package qualification. | make lint, make build, ROOT_FULL=1 make test, the timing budget checked against the rows that same test run produced, make test-native and make test-api-package all pass, in that order. | Local pre-merge/post-merge; CI keeps equivalent checks in separate named jobs. | Does not run fuzz search, race testing, provider calls, or browser guards; those have separate owners. The timing check reads the gate's own run (EVENER_GATE_TIMING_TSV), so no test executes twice; it is strict where the ratchet's own policy is (CI) and warn-only on a local run; a blessed-class per-test ceiling breach is nonzero there, while an unclassified test over the flat ceiling only warns (#4601). | The first failing phase stops the gate and returns nonzero; do not infer a verdict from partial logs. |
| `make test-race` | The permanent -race gate across every non-fuzz module. | Data races in the non-fuzz modules surface; frontend is intentionally not duplicated. | Required CI; local diagnostic. | A race-capable Go toolchain and more CPU/memory; WEB=0, AGENT_SHARDS=0 and AGENT_PARALLEL=6 to cap test concurrency under -race's ~10x slowdown, while cmd/evener-hub and cmd/evener stay sharded (12 hub shards, no cost survey: under -race the survey costs as much as the run). RACE_SCOPE defaults to all; CI uses the explicit root scope plus agent and nonagent on separate runners. The two new scopes derive from GO_MODULES; nonroot remains the local aggregate. RACE_ROOT_PART (root scope only) splits the root module across runners: all (default), hub (only cmd/evener-hub's shards), or rest (everything else in the root module). | Any race report, test failure, or setup failure is nonzero. |
| `make vet` | go vet across every non-fuzz workspace module. | go vet diagnostics for every module, independent of the tagged lint floors. | Required CI; local diagnostic. | Deterministic Go analysis; no provider calls. | Any module's vet failure is nonzero. |
| `make test-timing-budget` | Ratchet per-package test wall time against testing-budget.json. | A timing regression does not silently erode the suite's runtime wins — fail at 1.5x the checked-in budget, warn at 1.1x, plus a per-test ceiling segmented by test class: a test named in a class is compared strictly against that class's ceiling (a breach is nonzero under CHECK=1 in a CI-shaped environment), while a test in no class keeps the flat perTestCeilingSeconds and is warn-only in every mode, CHECK=1 included, so the unclassified backlog cannot red the gate (#4601). The checked-in budget carries the package-wall-seconds metric marker, so per-package ratios (frontend "web" included) are enforced; a budget file without the marker downgrades every ratio failure to a warning. | Local/on-demand. merge-approval-gate runs the check through --measured over the rows its own ROOT_FULL=1 make test run captured, so enforcing the budget no longer needs a second full test run (issue #144); invoked bare it still measures by running the suite itself. CHECK=1 enforces the ratios; bare invocation only measures and prints them, except for a broken measurement, which is nonzero either way. | Deterministic; no provider calls. Reuses gate-surface-lib.sh, so it measures the same surface ROOT_FULL=1 make test proves. | A broken measurement — go list or go test exiting nonzero, or a go list package with no terminal event in the stream — is nonzero in every mode, and --bless refuses it. A narrowed bless refreshes the packages it measured and preserves the rest of the file instead of deleting entries it did not measure; a full rebaseline also drops entries go list no longer reports. Under CHECK=1 in a CI-shaped environment a Go package over 1.5x its budget is nonzero once the budget file carries the wall-time metric marker; while the marker is absent those ratios warn, the "web" row among them. A blessed-class per-test ceiling breach is nonzero under CHECK=1; an unclassified test over the flat ceiling is reported and named but nonzero in no mode. A missing budget file, or one whose packages map is empty, always exits zero; a malformed budget file (a top-level value that is not an object, invalid JSON, a packages value that is present but not an object, a package budget value that is not a finite number, a perTestCeilingSeconds that is present but not a finite number, a perTestCeilingClasses that is present but not an object, or a class ceilingSeconds that is not a finite, float-convertible number including an integer too large to convert) is a broken config and is nonzero in every mode, naming the file and the entry (#4609, #4614, #4618, #4622, #4629). |

### Other targets

| Command | Summary |
| --- | --- |
| `make test-short` | Alias for `make test`. |
| `make test-rebaseline` | Reset testing-budget.json to what a clean-host run just measured. Run deliberately, on an otherwise idle box, and review the diff in the same commit as whatever change earned it — never to invent a baseline. Not part of any gate. |
<!-- END GENERATED -->
