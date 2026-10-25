# Tech-debt audit synthesis, 2026-10-25

Inputs: 42 slice reports (`raw/*.md`) and 791 adversarially verified findings (`verified/*.json`). One finding was refuted (`llm-core-providers-24`). Severity counts below use the verifier's adjusted severity, which is lower than the originals in many cases (for example `agent-misc-root-1` went from high to medium). No tests were run and nothing in the repo was edited except this file.

Finding ids are written as `slice-N`, for example `agent-jobs-6`.

## 1. Executive summary

### Overall health

The code behaves. Crash-safety, locking and idempotence reasoning is real, and the protocol catalog to generated TS pipeline (`appwire/protocol.go` to docs and `types.gen.ts`) is healthy. Verifiers found no widespread correctness rot. The debt is structural and comes from one pattern repeated everywhere: every review finding or new case got another guard, wrapper, seam or paragraph, and nothing was folded back into a smaller design. Production Go is about 100k+ lines in `package agent` alone, tests are 2 to 3 times production size, and a third of many files is comment text.

Verified totals: 54 high, 318 medium, 418 low.

### The eight biggest themes

1. **Production code that only tests use.** Legacy APIs, dead branches, `//nolint:unused` retentions, `ForTest` exports and injected seams. Tests then pin the dead code and give false coverage. `createShell` is the worst case: the only production definition is `agent/jobs.go:991` and it has ~198 test call sites (the slice report says 238) while production starts shells another way.
2. **Low-level helpers reinvented per package.** Atomic write, directory-fsync predicate, XDG state root, path containment, truncation, pointer clone, backoff, strict JSON decode. Copies have already drifted into real defects.
3. **Hand-mirrored structures across layers.** One 50-field delegate status is copied through four struct types and five mappers, and two mappers drop `LatestIntent`. The same shape appears for `SessionConfig`/`ConfigSnapshot`, `LiveEntry`, launchconfig options, registry `Caps`, and Go to TS enums and error codes.
4. **God files and god functions.** `agent/job_watch.go` 6,564 lines, `agent/session_tools_worktree.go` 3,798, `stores/threads.ts` 8,990, `server/appwire_runtime.go` 3,074, `runMain` 775 lines, `serve` 1,597 lines, `ConversationScreenView` 3,600 lines. 116 functions exceed 150 lines.
5. **Clients re-deriving what the shared client should own.** Attention class (about 8 copies), stuck/quiet policy (180s on web vs 600s on phone), a dozen duration formatters, two outbox adapters that already drifted, a phone conversation store hand-copying reducer internals.
6. **Test-suite debt.** About 38k lines of coverage-chasing files repo-wide, "fuzz" targets that ignore their input, 900+ error-string assertions that `testing.md` bans, dozens of hand-rolled fakes and fixtures despite shared kits, and 11k to 20k line test files.
7. **Narration comments and stale docs.** Round numbers, issue ids, spec section cites (178 in `llm/registry` alone), and `file:line` anchors that already point at the wrong code. `testing.md` (1,973 lines) reads as a changelog.
8. **Compat code with no skew policy.** Repo is about eight months old and many "legacy" paths protect formats that lived days to weeks. Removal needs Jesse's approval each time. A single written policy would let most of it go in one decision.

### Rough size of the cleanup (lines of code, order of magnitude)

| Bucket | Lines |
|---|---|
| Production deletions (dead, test-only, legacy, duplicated) | 25k to 35k removed |
| Production refactors (splits and extractions, roughly neutral, net negative after dedupe) | 40k to 60k moved, 10k to 15k net removed |
| New shared helpers (execsupport/helpers, a few test kits) | 2k to 4k added |
| Test deletions (coverage waves, dead-code tests, mockup tests, duplicated fixtures) | 60k to 90k removed |
| Test migrations onto existing kits | 20k to 30k touched |
| Docs and assets (plans corpus, ~70 MB PNG, comment sweeps) | mostly deletions, comment sweep ~15k lines edited |

These are estimates from the slice reports, not measurements.

## 2. Defects found (fix these first, red-test-first)

These are behavior bugs or drift with consequences. They need TDD, not just a refactor.

| Defect | Finding | Evidence recorded by the slice |
|---|---|---|
| Per-package timing ratchet is disarmed: committed `testing-budget.json` has no `"metric"` key | `build-gates-scripts-1` | Spot check: `grep -c '"metric"' testing-budget.json` returns 0 |
| Delegate status mappers drop `LatestIntent` | `appwire-server-2`, `x-go-ts-protocol-4` | Two hand converters omit the field |
| Cache validator key whitelists drifted from generated wire types, likely invalidating cached transcripts for working delegates | `appwire-ts-core-2` | Hand-listed keys vs `types.gen.ts` |
| `plugin.ResolveCommand` iterates a map, so ambiguous commands resolve nondeterministically | `agent-subpackages-8` | Skill resolution reports ambiguity, plugin does not |
| `10_000` timeout sentinel overrides an explicit timeout | `agent-subpackages-12` | `session_init.go` |
| Byte-slice truncation splits UTF-8 runes in summarizer prompts and other model-facing text | `agent-internal-state-4`, `x-go-duplicate-functions-5` | About 10 of 15 truncators cut bytes |
| 11 atomic writers use a predictable `.tmp` name that `llm/registry/write.go` itself calls a symlink-plant risk | `x-go-duplicate-functions-1` | Drifting durability across ~30 writers |
| Vertex model listing ignores the listing timeout; Chat reasoning precedence differs stream vs non-stream; Chat does not read local image paths | `llm-core-providers-7`, `-16`, `-8` | Drift between four protocol packages |
| `hostreg.SameRegistration` omits `PresenceEpoch`; four sshconn sites re-spell the compare | `hub-internal-small-12`, `hub-sshconn-6` | |
| `TranscriptDisplayPostApplyError` does not carry `ErrWriteApplied` | `hub-hubcore-5` | Latent bug candidate |
| `appitempaging.cloneThreadItem` shares four pointer fields that appwire's clone copies | `internal-app-layer-16` | |
| Remote `/doc/image` by path ignores the artifacts root; Settings shows a hardcoded 30s spawn timeout despite configurable `spawn_timeout` | `hub-root-rest-8`, `-4` | |
| Four position comparators, one ignores `Sub` | `hub-hostops-appsource-4` | |
| Two live-compaction scripts source a nonexistent path | `build-gates-scripts-9` | |
| Credential-material merge implemented three times (security relevant) | `llm-core-providers-11` | |
| `llm/authenticators.go:92` branches on a warning-text prefix | `llm-registry-3` | Prose used as a protocol |
| Bearer parsing: `httpguard` and `hubedge` have opposite empty-token semantics | `hub-internal-small-18`, `x-go-duplicate-functions-18` | |
| Likely write-only phone ActivityStore can throw and block reads | `native-tests-legacy-mobile-2` | |
| `ErrSSHAuth` doc contradicts code | `hub-sshconn-11` | |

## 3. Cross-cutting themes

Each theme lists the root cause, member findings, the canonical fix, and a guardrail. Guardrails prefer existing tooling. I am skeptical of adding more meta-tests; the repo already has heavy audit-test sprawl (`build-gates-scripts-3`, `-4`, `-5`, `-8`).

### T1. Production surface that only tests reach

**Root cause.** Coverage floors and "inject a fault" tests reward adding a seam or keeping a legacy entry point. `//nolint:unused` makes the linter stop noticing. Once 200 tests call a legacy path, deleting it looks expensive.

**Members.**
- Legacy APIs kept by tests: `agent-delegates-1` (22 test files), `agent-session-core-5`, `agent-session-core-6` (FollowUp), `agent-jobs-6` (`createShell`), `agent-jobs-7`, `x-dead-code-2`, `x-dead-code-3` and `agent-internal-state-7` (path-based snapshot readers), `llm-core-providers-1`, `-2`, `-3`, `-4`, `-19` and `x-dead-code-4` (StreamGenerate has no non-test caller; spot check confirmed only `llm/stream_generate.go:135` and `llm/generate_object.go:117` plus docs), `hub-sshconn-1` and `x-dead-code-7`/`x-compat-legacy-4` (port-based probe ladder), `hub-hostops-appsource-5`, `-7`, `-16`, `hub-hubcore-7`, `hub-root-rest-5`, `hub-app-rest-5`, `-6`, `appwire-server-3`, `-4`, `-5`, `agent-misc-root-6`, `-7`, `-8`, `agent-subpackages-6`, `-9`, `-15`, `internal-app-layer-3`, `-10`, `-11`, `x-dead-code-1` (75 functions, ~600 lines), `x-dead-code-6`, `x-dead-code-8`.
- `//nolint:unused` retention: `agent-delegates-2`, `x-compat-legacy-11`, `x-error-handling-slop-9`, `agent-session-tools-2`. Spot check: 15 non-test `nolint:unused` lines.
- Seams and `testing.Testing()` forks in production: `agent-session-core-12`, `agent-session-features-7`, `agent-session-tools-3`, `agent-delegates-14`, `agent-jobs-5`, `agent-misc-root-17`, `agent-execenv-sandbox-11`, `-22`, `llm-registry-10`, `hub-app-host-5`, `-16`, `hub-app-rest-8`, `hub-root-rest-12` (~44 seams), `x-error-handling-slop-5` (104 mutable func vars), `-6`, `-7`, `-10`, `internal-misc-cmds-10` (~75 plugin seams), `web-stores-3` (41 `ForTests` exports), `x-dead-code-11`.
- Test harness shipped in production: `agent-execenv-sandbox-18`, `agent-internal-tools-15`, `hub-hostops-appsource-15`, `web-stores-20`, `web-dev-and-tests-1`, `-2` (~2,750 lines of mockup pages for shipped designs), `x-dead-code-12`.

**Canonical fix.** Three moves, in order.
1. Migrate the test fixtures to the production path first (for example, tests that call `createShell` should start shells through the real start path). Reducing coverage is worse than failing tests, so tests move, they do not just vanish.
2. Delete the then-unreferenced production code.
3. For seams that remain: Go `ForTest` helpers go to `export_test.go`; mutable `var x func` seams stay only where a behavior test needs a failure that cannot be produced with a real fault (full disk, EXDEV). Fault-injection for "cannot happen" branches goes away with the branch.

**Guardrail.**
- Check whether `nolintlint` is enabled in `.golangci.yml` (the visible enable list through `unconvert` did not show it). Enable it with `require-specific: true` and `require-explanation: true`, then ban `unused` in nolint comments by a one-time sweep. This fits existing tooling.
- Add an AGENTS.md rule: "A production symbol needs a production caller. A test seam needs a behavior test that cannot reach the failure otherwise."
- No new audit test.

### T2. Low-level helpers reinvented per package (Go)

**Root cause.** No agreed shared home reachable from every module. `fsdurability` lives under `cmd/evener-hub/internal/`, so `server`, `agent` and `auth` cannot import it (spot checked). Each author, seeing no importable helper, wrote a private copy. Lint has no `dupl` guard.

**Members and counts.**
- Atomic write: `x-go-duplicate-functions-1` (~30 writers), `internal-misc-cmds-1` (7 variants), `x-error-handling-slop-12`, `hub-app-host-9`, `hub-hostops-appsource-10`, `hub-internal-small-15`, `appwire-server-13`, `llm-registry-12`.
- Sync-unsupported errno predicate: `x-go-duplicate-functions-2`, `agent-misc-root-5`, `hub-internal-small-1`. Spot check: 7 non-test definitions (`server/thread_clear_journal.go:155`, `auth/openai/storage.go:229`, `agent/session_client_mutation_persist.go:423`, `agent/internal/worktree/sync_unix.go:36`, `cmd/evener-hub/app_host_manage.go:1054`, `fsdurability.go:20`, `hubcore/state_file.go:127`).
- XDG state root: `agent-misc-root-4` (7 copies, 4 fallbacks), `agent-subpackages-4`, `internal-misc-cmds-2`, `llm-registry-12`, `appwire-server-9`.
- Session on-disk layout and transcript path: `agent-misc-root-11` (~16 sites in agent, ~12 outside), `agent-subpackages-2` (~25), `hub-root-rest-20`, `agent-internal-state-13`.
- Whole-transcript read loop: `agent-misc-root-3`, `agent-subpackages-3`, `internal-app-layer-9`.
- Path containment (13 helpers, argument order flips): `x-go-duplicate-functions-6`, `agent-session-tools-10`, `agent-execenv-sandbox-7`, `hub-internal-small-9`. Symlink-resolve: `x-go-duplicate-functions-7`, `agent-execenv-sandbox-8`. Git pointer parsing: `agent-execenv-sandbox-9`.
- Truncation and whitespace collapse: `x-go-duplicate-functions-5`, `-14`, `agent-misc-root-12`, `agent-subpackages-5`, `agent-internal-tools-13`, `agent-internal-tools-19`.
- Pointer and JSON clone: `x-go-duplicate-functions-10`, `hub-app-rest-16`, `x-go-ts-protocol-6`, `agent-internal-tools-12`, `hub-internal-small-4`, `appwire-server-9`, `internal-app-layer-14`. Spot check found 5 copies named like `clonePtr`/`cloneIntPtr`; the reports count 12 to 19 including differently named ones.
- Smaller: backoff `x-go-duplicate-functions-3` + `agent-session-features-1`; sleep `-4`; tilde `-8`, `hub-internal-small-10`; strict JSON `-9`; collection helpers `-11`; env slices `-16`; random id `-17`; bearer `-18`; flock `agent-internal-tools-10`; bounded line reader `agent-internal-tools-8`; jsonschema compile `agent-internal-tools-11`; bucketref `agent-internal-state-17`; hash fingerprints `x-error-handling-slop-13`; sysmsg `agent-internal-tools-14`, `agent-misc-root-13`.

**Canonical fix.** One small shared module as the home. `execsupport` is already required by root, `agent` and `llm` (`x-go-duplicate-functions` report), so put the durable-write, sync predicate, path-contains, and rune-truncate functions there, or in `envvars` for env/XDG. Move the existing `fsdurability` implementation there and leave the hub importing it. Prefer adopting packages that already exist before writing new ones: `agent/internal/runetrim`, `linecap`, `sysmsg`, `mcphttp`, `sessionlog` are each "partly adopted" (`agent-internal-tools` summary). Adoption is cheaper than design.

Add tests before changing semantics. Path containment, truncation, strict JSON and signal-0 liveness (`x-go-duplicate-functions-15`, opposite EPERM handling) differ at edges, so each consolidation needs a characterization test that records which variant wins and why.

**Guardrail.**
- `golangci-lint` `dupl` on non-test files with a high threshold, or `forbidigo` for `os.Rename(` outside the shared writer. Both are cheap, but run them once to see noise before committing to them. Use `issues.new-from-rev` so existing copies do not block while new ones do.
- AGENTS.md line: "Before writing a path, fsync, truncate or clone helper, grep for the shared one."

### T3. Hand-mirrored structures across layers

**Root cause.** Each layer (agent, server, appwire, hub, TS, native) has its own type, and no mechanism ties them. Comments say "keep in sync" or "must not drift". Adding one field means 5 to 8 edits, and one gets missed.

**Members.**
- Delegate status: `appwire-server-2` (high), `agent-misc-root-2` (high), `internal-misc-cmds-12`, `x-go-ts-protocol-4`, `-5`, `internal-app-layer-14`, `hub-hostops-appsource-11` and `hub-hubcore-2` (LiveEntry/ProbeResult, seven places).
- Config: `agent-session-core-13`, `agent-subpackages-13`, `hub-internal-small-2`, `-3`, `-4`, `-5` (launchconfig ~8 parallel lists), `hub-root-rest-3` (30 retention knobs through 6 layers), `hub-hubcore-10`.
- Registry: `llm-registry-6` (every Caps/Transport/Provider field in 5 to 7 places), `-7`, `-8`, `-14`.
- Store/record kinds: `hub-app-host-1` (nine "preserve unowned names" functions), `hub-app-host-2` (~38 sites per kind), `hub-hostops-appsource-8`.
- Go to TS: `x-go-ts-protocol-1`, `-2`, `-3`, `-8`, `-10`, `-11`, `-12`, `agent-subpackages-16`, `appwire-ts-core-2`, `-3`, `x-go-ts-protocol-7`, `appwire-ts-state-testing-8`.
- Fingerprints: `hub-hubcore-6` (rosterFingerprint ~290 lines of `h.Write`).
- Events: `agent-subpackages-11` (kind registered twice and `emit(kind, data)` 252 times), `agent-jobs-14`.

**Canonical fix.** Extend what already works. The TS generator (`internal/appwirets`) already emits types and has a drift test; give it an enum and constant registry so error codes, status and item-type literals, protocol version and size limits flow from Go. For Go to Go mirrors, pick the lowest layer both sides already import and make it the one struct (`agent.DetailedStatus` to `server.DetailedStatus` to appwire is the case with the most findings: pick one type, delete the converters). For field-by-field enumeration (Caps, launchconfig, rosterFingerprint), use a single table or reflect-based walk plus one guard test, which `rootFingerprint` already demonstrates.

**Guardrail.** Existing generated-output freshness in `make lint` covers TS once the generator emits enums. For Go struct mirrors, the pattern from `rootFingerprint` (a test that fails when a field is added without being handled) is already accepted; reuse it, do not invent a new framework. A review rule: "a new field that must be listed in more than two places is a design bug."

### T4. God files and god functions

**Root cause.** Files are organized by history (where the feature started) not by responsibility. Test seams and review follow-ups accrete inside the nearest big function.

**Members (size evidence spot checked where noted).**
- Go: `agent/job_watch.go` 6,564 (`agent-jobs-1`, spot checked), `session_tools_worktree.go` 3,798 (`agent-session-tools-12`, spot checked), `session_tools_jobs.go` (`-11`), `session_tools.go` (`-7`), `agent-session-core-1`/`-2`/`-4` (NewSession/Restore duplication, 1,250-line turn loop), `agent-session-core-8` (274 fields, 22 mutexes), `agent-session-features-2`, `-8`, `-9`, `agent-delegates-13`, `agent-internal-state-3`, `hub-app-host-10`, `-14`, `hub-app-rest-2` (1,829-line closure), `hub-app-rest-12`/`-13`, `hub-hubcore-1` (834-line tree builder), `hub-hubcore-8`, `hub-sshconn-3`, `-4`, `hub-root-rest-1`, `internal-misc-cmds-3` (`serve` 1,597 lines), `internal-app-layer-4`, `appwire-server-12`, `x-go-ts-protocol-14` (`types.go` 5,898), `server/appwire_runtime.go` 3,074 (spot checked), `agent-execenv-sandbox-1`, `llm-registry-1`, `agent-internal-tools-1` (`explain.go` 3,638), `agent-subpackages-1`, `x-error-handling-slop-1`, `-2`.
- TS: `web-stores-1` (`threads.ts` 8,990, spot checked), `web-stores-16`, `web-panes-a-2`, `-3`, `-4`, `web-panes-b-1`, `web-shell-widgets-2`, `appwire-ts-core-1`, `native-session-board-1`, `native-rest-1`, `native-tests-legacy-mobile-3`.
- Package level: `agent-misc-root-1` (agent is a 105k-line package), `hub-root-rest-10` (56k-line `main` package).

**Canonical fix.** Do it in this order, because each step makes the next one smaller.
1. Delete dead code (T1). It removes thousands of lines from the same files.
2. Dedupe constructors and skeletons inside the file: `NewSession` and `Restore` into one builder (`agent-session-core-1`), the seven client-mutation handlers into one skeleton (`-3`), the fire-a-watch funnel (`agent-jobs-2`), teardown (`agent-jobs-3`), the delegate "untrack and signal" snippet copied ~14 times (`agent-delegates-4`).
3. Split by responsibility within the package, as pure file moves with no logic change.
4. Only then consider extracting packages, and only leaf ones: hostresources, memorypages, envsnapshot, transcriptview, artifactread from `agent` (`agent-misc-root-1`), and hubconfig, navigation, hostmanage from hub `main` (`hub-root-rest-10`). Keep `Session`, retirement and the app_* handlers in place.

**Guardrail.** `revive` rules or `gocognit`/`funlen` with `issues.new-from-rev` so only new or touched functions are held to a limit. Avoid a hard repo-wide number; there are 116 existing offenders.

### T5. Clients re-deriving what the shared client should own

**Root cause.** `AGENTS.md` and the product guide say the shared client owns shared derivations, but web, native and `mobile/` each hand-copied the rule to unblock themselves and left a "keep in sync" comment.

**Members.** `x-client-duplication-1` (attention class, ~8 copies), `-2` (stuck policy: web 180s, phone 600s, package constant redeclared), `-3` (a dozen duration formatters that disagree at rounding boundaries) with `web-panes-a-7`, `appwire-ts-core-4`, `native-session-board-5`, `native-rest-18`; `-4` and `native-tests-legacy-mobile-4` (phone store hand-copies reducer internals), `-7` (two outbox adapters with drifted transition rules) with `native-rest-5`, `-6`; `-8` (composer verb mapping in seven places), `-9`, `-10`, `-11`, `-12`, `-13` (backoff six times), `-16`; `web-panes-a-5`, `-8`, `-10`, `-15`; `web-panes-b-3`, `-9`, `-12`; `web-shell-widgets-6`; `native-session-board-4`, `-6`, `-15`, `-16`; `native-rest-17`, `-22`; `web-stores-7`, `-8`, `-11`, `-12`, `-21`; `appwire-ts-state-testing-4`, `-8`, `-22`.

**Canonical fix.** Move each derivation into `appwire-client/typescript` once, with unit tests there, and delete the web and native copies. Start with the ones that have already disagreed (stuck policy, outbox retention, duration rounding) because those are user-visible. The client package has precedent: the send/queue gate, disclosure and ask-dock store. Note this needs Jesse to decide whether web and phone should keep different stuck thresholds on purpose (see section 5).

**Guardrail.** Biome cannot catch semantic duplicates. The cheap guard is the existing `lint-package-imports` pattern plus a PR checklist line: "does this derive state the package already derives?" Do not write another scanner.

### T6. Test-suite debt (coverage chasing, copy oracles, helper sprawl)

**Root cause.** A coverage floor (agent at 94.7, not a required gate per `build-gates-scripts-19`) pushed volume. Shared kits exist (`agenttest`, `testkit`, `appwiretest`, `hubtestenv`, `FakeClient`, `appwire-client/testing`) but adoption stalled, and the copy-oracle ratchet only flags tests named like "shows X".

**Members.**
- Coverage waves: `go-tests-agent-5` (211 sprint-labelled files, 42k lines), `agent-delegates-19` (4,013 lines), `agent-jobs-18`, `agent-internal-tools-5` (4.2k), `hub-root-rest-11` (40 `cov_*` files, spot checked as 40 under `cmd/evener-hub`), `hub-app-rest-15` (39 files), `go-tests-rest-2`, `-3` (121 file:line cites, some already wrong), `go-tests-agent-6`, `-7`, `agent-session-core-17`, `agent-session-features-15`, `agent-session-tools-15`, `hub-hubcore-19`, `hub-sshconn-18`, `hub-hostops-appsource-14`, `hub-internal-small-7`, `internal-misc-cmds-9`, `agent-internal-tools-7`, `build-gates-scripts-19`.
- Copy-as-oracle: `go-tests-agent-4` (543 assertions in 133 files), `go-tests-rest-14` (371 in 142 files), `agent-misc-root-18`, `agent-internal-state-12` (92), `agent-internal-tools-6`, `llm-registry-3` (~53), `hub-internal-small-8` (28), `hub-sshconn-18` (50+), `agent-delegates-19` (30), `hub-root-rest-13`, `web-dev-and-tests-7`, `web-shell-widgets-16`, `web-stores-15`, `web-panes-a` (~150), `native-rest-12`, `native-tests-legacy-mobile-10`, `appwire-ts-core-11`, `appwire-ts-state-testing-14`, `internal-misc-cmds-19`, `appwire-server-17`.
- Helper sprawl: `go-tests-agent-1`, `-2`, `-3`, `-8`, `-9`, `-10`, `-13`, `-15`; `go-tests-rest-1` (~54 provider fakes), `-5` (16 scripted server loops), `-6`, `-7`, `-8`, `-9`, `-11`, `-13`; TS: `web-dev-and-tests-3` (88 `connectFakeClient`), `-4` (~85 files), `-5`, `-6`, `x-go-ts-protocol-15`, `appwire-ts-state-testing-9`, `-10`, `-11`, `-17`; `native-tests-legacy-mobile-8` (92 files re-mock `expo-symbols`), `-9` (~60 hand-rolled clients), `-11`.
- Oversized files: `web-dev-and-tests-8` (27 files over 2,000 lines, `Spawn.test.tsx` 11,489), `go-tests-rest-12` (`app_rpc_test.go` 13,451), `native-tests-legacy-mobile-5` (20,457), `-12`, `web-panes-b-13`, `native-rest-13`, `go-tests-agent-11`.
- Tests of mocks: `appwire-ts-state-testing-13`, `web-dev-and-tests-9`, `native-tests-legacy-mobile-7`, `-1`.

**Canonical fix.** Policy first (Jesse): is the coverage floor required? If it is a ratchet nobody gates, the coverage-wave files and the `cov_*` matrices (about 38k lines) can be deleted in bulk after one before/after coverage check, with real behavior tests left. Then migrate to existing kits: `NewSession` to `testkit` (884 raw calls), hand-rolled adapters to the scripted adapter, `connectFakeClient` and thread fixtures to `appwire-client/testing`. Add new shared packages only when 10 or more copies exist and no home exists (git fixtures, wait helpers, provider-adapter fakes for `llm`).

**Guardrail.** Extend the existing copy-oracle inventory ratchet to include error-string assertions and per-assertion cases, instead of adding a new tool. Delete the doc-pinning and Makefile-text audit tests (`build-gates-scripts-3`, `-5`) rather than keeping them as a second meta layer. AGENTS.md rule: "No test file named for a sprint, round, pass or percentage."

### T7. Narration comments and stale docs

**Root cause.** LLM authors explain the review round that produced the code, and no rule says the explanation belongs in the commit message. Dated specs were cited by section number, and the specs later moved or died.

**Members.** Comments: `agent-session-tools-13` (49 hits), `agent-session-core-11` (a third of 19k lines), `agent-session-features-14`, `agent-delegates-20`, `agent-jobs-19`, `agent-misc-root-25`, `agent-internal-state-11`, `agent-internal-tools-17`, `agent-subpackages-1`, `-24`, `agent-execenv-sandbox-12`, `llm-registry-16`, `-17` (178 cites to a dated spec), `hub-app-host-17`, `hub-app-rest-17`, `hub-root-rest-19`, `hub-hubcore-20`, `hub-sshconn-5`, `hub-hostops-appsource-12`, `hub-internal-small-19`, `appwire-server-16`, `internal-misc-cmds-18`, `go-tests-agent-12`, `go-tests-rest-4`; TS: `web-panes-a-9` (117 refs to missing docs), `web-panes-b-2`, `web-shell-widgets-9`, `web-stores-18`, `appwire-ts-core-7` (17 pointers to deleted `renderer.js`), `appwire-ts-state-testing-16`, `native-session-board-10`, `native-rest-11`, `web-dev-and-tests-13` (271 "kata" refs). Docs: `x-docs-1` through `-11`, `agent-jobs-17`, `hub-app-host-13`, `hub-sshconn-17`, `agent-internal-tools-18`, `llm-core-providers-20`, `native-tests-legacy-mobile-6`, `x-compat-legacy-15`, `-16`, `build-gates-scripts-14`, `-15`.

**Canonical fix.** Delete narration while touching each file for other reasons, never as a bulk reformat PR on its own (comment-only churn rotates blame and trips reviewers). Replace "spec section N" cites with the evergreen doc under `docs/product/`, or drop them. Remove `file:line` anchors from evergreen docs (use symbol names). Archive or delete unreferenced plan corpus and the ~70 MB PNG evidence (`x-docs-1`, `-2`) after one grep for references.

**Guardrail.** One AGENTS.md rule: "Comments state the invariant. History, review rounds, issue ids and spec section numbers go in the commit message." Add it to the roborev review instructions so the reviewer flags new violations. A backticked-path check in the existing `docs-check` (`x-docs-11`) is acceptable since that tool already exists; a new regex lint for comments is not worth it.

### T8. Compat and legacy code with no skew policy

**Root cause.** No written version-skew contract, so each author added a local fallback with no removal condition. Rule #1 in `CLAUDE.md` says backward compat needs approval, but compat was added before the rule was noticed or labelled something else ("retained", "older hub").

**Members.** `x-compat-legacy-1` (46 "older hub" fallbacks), `-2` and `appwire-ts-core-13` (combined update/apply path), `-3` (transcript-display dual-write), `-5` and `hub-app-host-8` (pre-union adapters), `-6`, `hub-app-host-6`, `-7`, `hub-hostops-appsource-9`, `-7` (retired vocabulary), `-7`/`internal-misc-cmds-6` (serf migration), `agent-misc-root-16` (671-line MEMORY.md migration), `internal-misc-cmds-5` and `x-compat-legacy-13` (plugin store migrations), `internal-app-layer-1` and `x-compat-legacy-12` (legacy transcript projection, still running beside the identity format written since 2026-09-26), `native-rest-8`, `native-session-board-8`, `x-compat-legacy-14` (persisted client-state eras), `hub-internal-small-16`, `agent-internal-state-10` (680-line checkpoint parser with three legacy formats), `-18`, `agent-delegates-17`, `web-panes-a-17`, `web-shell-widgets-14`, `-18`, `appwire-server-8`, `-10`, `x-go-duplicate-functions-12`.

**Canonical fix.** Jesse writes the skew policy once (supported hub/client window, state migration window). Every item above then gets a mechanical keep/delete answer. Nothing here should be deleted without that approval. Dead-in-production compat (sshconn ladder, pre-union adapters, nolint:unused retentions) is the exception: it has no users, so it belongs in T1.

**Guardrail.** A "Compatibility" section in `docs/product/README.md` (policy, window, where removal conditions are recorded). New compat code must name its removal condition in the PR description.

## 4. Per-area scorecard

Health is my judgment from the reports (1 worst, 5 best). Counts are non-refuted findings by verifier-adjusted severity (high/medium/low).

| Slice | Health | H/M/L | Top issue |
|---|---|---|---|
| agent-session-core | 2 | 4/8/5 | `NewSession`/`Restore` copy, 1,250-line turn loop, dead legacy API kept by tests |
| agent-session-features | 3 | 2/6/7 | Seven backoff timers, 484-line `drainJobTreeWith` |
| agent-session-tools | 3 | 0/5/10 | No shared tool-argument decoder; 3.8k-line worktree file |
| agent-delegates | 3 | 1/6/13 | Dead legacy spawn path kept by 22 test files; 14-fold untrack snippet |
| agent-jobs | 2 | 2/7/11 | 6,564-line `job_watch.go`; production-dead `createShell` |
| agent-misc-root | 2 | 2/11/15 | God package; delegate status copied 4x; transcript read loop 6x |
| agent-internal-state | 3 | 0/9/11 | Pipeline copied 5x in contextmgr; four journals re-solve torn tails |
| agent-internal-tools | 3 | 1/6/14 | 3.6k-line hand-rolled schema evaluator in `explain.go` |
| agent-execenv-sandbox | 3 | 0/11/12 | 3.6k-line `local.go`; 3-arm dispatch for every file op |
| agent-subpackages | 3 | 0/11/13 | Layout literals in 25 places; one bad plugin file drops the whole plugin |
| llm-core-providers | 3 | 2/5/16 | Dead streaming API; four decoders copy finish/error code |
| llm-registry | 3 | 1/6/11 | Credential diagnosis as prose parsed by prefix; replay copied 3x |
| hub-app-host | 3 | 3/9/7 | ~38 sites per record kind; nine near-identical preserve functions |
| hub-app-rest | 2 | 1/11/7 | 1,829-line relay closure bag; correlation layer |
| hub-root-rest | 2 | 3/13/6 | 775-line `runMain`; hub.toml as config and database; 40 cov files |
| hub-hubcore | 3 | 2/8/11 | 834-line tree builder; live fact copied through 7 places |
| hub-sshconn | 2 | 2/7/9 | Superseded port-based path kept for tests; 351-line `ensureOnce` |
| hub-hostops-appsource | 3 | 2/8/7 | Remote capability probe nobody reads; ~55 forwarders |
| hub-internal-small | 4 | 0/8/14 | launchconfig parallel lists; reinvented small predicates |
| appwire-server | 3 | 1/7/9 | DetailedStatus mirror drops a field; 3k-line runtime file |
| internal-app-layer | 3 | 1/7/11 | Legacy projection path beside identity format |
| internal-misc-cmds | 3 | 2/9/11 | 7 atomic-write variants; 1,597-line `serve` |
| appwire-ts-core | 4 | 1/4/11 | Cache key whitelists drifted from wire types; 5k-line reducer |
| appwire-ts-state-testing | 4 | 0/6/16 | 946-line credentials closure; helper re-invention |
| web-stores | 2 | 2/8/11 | 8,990-line `threads.ts`; parallel hydration paths; dead fixture route |
| web-panes-a | 3 | 3/4/11 | Composer submit protocol duplicated; 1.6k-line components |
| web-panes-b | 2 | 1/9/10 | 2,980-line `SpawnForm` that re-implements existing seams |
| web-shell-widgets | 4 | 0/9/12 | Rail and AppShell mega-files; four unused widgets |
| native-session-board | 3 | 1/7/9 | 1,994-line BoardScreen; hand-copied derivations |
| native-rest | 3 | 2/6/14 | 3,600-line screen function; imports web source by relative path |
| native-tests-legacy-mobile | 3 | 3/6/6 | Write-only activity store that can block reads; 20k-line test |
| web-dev-and-tests | 3 | 2/5/7 | Fixtures re-declared in ~85 files; mockups for shipped designs |
| go-tests-agent | 3 | 2/7/6 | 543 phrase assertions; 60 hand-rolled fakes; coverage waves |
| go-tests-rest | 3 | 1/11/3 | 54 provider fakes; 16 pasted server loops |
| build-gates-scripts | 2 | 1/7/13 | Timing ratchet disarmed; ~5.5k lines of text-pinning audits |
| x-client-duplication | 3 | 0/9/8 | Attention class and stuck policy re-derived |
| x-go-duplicate-functions | 2 | 1/5/14 | ~30 atomic writers, no importable home |
| x-go-ts-protocol | 4 | 1/5/9 | Delegate model copied 4x; enums emit as `string` |
| x-dead-code | 3 | 0/7/9 | 75 functions only tests reference |
| x-compat-legacy | 3 | 0/12/5 | No skew contract |
| x-error-handling-slop | 3 | 1/6/10 | 116 functions over 150 lines; 104 mutable func seams |
| x-docs | 3 | 0/7/4 | Historical corpus and PNG weight; testing.md as changelog |

Totals: 54 high, 318 medium, 418 low, 1 refuted.

## 5. Conflicts and reconciliations

1. **Where the atomic-write and sync-predicate home lives.** `hub-app-host-9`, `hub-hostops-appsource-10` and `hub-internal-small-15` want to use hub `fsdurability`; `x-go-duplicate-functions-1`/`-2` point out `server`, `agent` and `auth` cannot import it (it sits under `cmd/evener-hub/internal`). Resolution: one home in `execsupport` (or whichever shared module `go list` shows every consumer already requires), hub `fsdurability` becomes the same code or a thin import. One function with durability always on; the cases that intentionally skip fsync (scratch, caches) call something plainly named for that. Several writers are weaker today (`internal-misc-cmds-1`), so the migration changes durability for them. Treat as a behavior change with a test per caller group, not a pure refactor.
2. **Where session file layout and transcript reading live.** `agent-subpackages-2`, `agent-misc-root-11`, `hub-root-rest-20` and `agent-internal-state-13` each propose their own helper. Resolution: one package that both agent and hub already import (candidate: `agent/schema`, which `agent-subpackages-14` says already mixes wire types with a locking filesystem engine, so also check the order of operations with that finding). One path builder and one `ReadTranscript(header, entries, limits)` that takes the limits; the six loops differ on limits and errors (`agent-misc-root-3`), so they become parameters.
3. **Root `frontmatter` module.** `agent-internal-tools-9`, `agent-misc-root-15`, `internal-misc-cmds-8`, `x-dead-code-5` agree it is a duplicate with one test importer. `agent-subpackages-18` and `agent-misc-root-15` also want to move YAML recovery and the memory frontmatter editors beside `Split`/`Parse`. Resolution: delete the root module (and its `testing-budget.json` row at line 96) after moving the one `internal/bundled/bundled_test.go` check onto a path that can import `agent/internal/frontmatter` or stop depending on it. Then do the editor move in the agent package. Do the delete first.
4. **Delete dead code vs "never reduce coverage".** `agent-delegates-1`, `agent-jobs-6`, `agent-session-core-5`, `-6` propose deletion; hundreds of tests use these. Resolution: migrate tests to the real path, then delete. For FollowUp (`agent-session-core-6`) Jesse decides first, because the drain ladder depends on it.
5. **`cov_*` pruning severity.** `hub-root-rest-11` rates the 40 files high, `hub-app-rest-15` medium, `go-tests-rest-2` low/partial (it argues they execute real code). Same files. Resolution: one decision about the coverage floor, then one deletion PR with a coverage before/after report. Do not delete piecemeal.
6. **Splitting `package agent`.** `agent-misc-root-1` wants leaf packages extracted; most per-file slices want internal splits (`job_watch.go`, session tools). Resolution: file splits inside the package first (pure moves), extracted leaf packages second, and nothing that touches `Session`. The two compose; they just need an order.
7. **Mobile core.** `native-rest-3`, `native-tests-legacy-mobile-1`, `x-client-duplication-5`: fold `mobile/src` into the app tree. `x-client-duplication-4` and `-6`: shared logic belongs in `appwire-client`. Resolution: delete the unread activity store first (`native-tests-legacy-mobile-2`, `x-client-duplication-6`); move reducer-derived logic into `appwire-client`; fold what remains of `mobile/` into `mobile-native/src`, breaking the cycle by moving `projectedRows` and `transcriptPresentation` along with it.
8. **New test-support packages.** `go-tests-rest` proposes six (`llmtest`, `appwiretest`, `testwait`, `gittest`, `testenv`, `providertest`). `go-tests-agent` says agent already has kits that nobody uses. Resolution: adopt existing first (`agenttest`, `testkit`, `appwire/appwiretest`, `hubtestenv`). Create only `gittest` and an `llm` adapter fake if copy counts stay above ten after adoption.
9. **Test seams: delete vs keep.** Slices disagree between deleting, keeping and moving to `_test`. Resolution: the T1 rule above, applied uniformly.
10. **DetailedStatus fix direction.** `appwire-server-2`, `x-go-ts-protocol-5`, `internal-misc-cmds-12` each name a different canonical type. Pick by import graph (lowest layer both import), generate the TS from it, and delete the other copies. Do it as one PR so the dropped `LatestIntent` is fixed once.
11. **Comment sweeps vs behavior-preserving PRs.** Many slices want comment cleanup. Resolution: edit comments only in files already being changed, so reviews stay readable.
12. **"Fail closed" items vs the product guide.** `hub-app-rest-9` (endpoint-fingerprint guard defaults to fail-closed, ~900 lines), `agent-subpackages-10` (one malformed plugin file discards the whole plugin), `agent-delegates-17` (legacy delegate state refuses startup instead of recovering). AGENTS.md says recovery over lockdown. These are product choices for Jesse, not refactors. Do not simplify the code around them before the decision.

### Decisions only Jesse can make (collected)

- Coverage floor policy (T6, conflict 5).
- Compat/skew policy and each deletion under it (T8): serf migration, MEMORY.md migration, plugin-store migrations, legacy transcript projection, hub.hosts.json sidecar, retired TOML decoders, native draft eras, `launchconfig` back-compat.
- Delete `FollowUp`, and queue-authority consolidation (`agent-session-core-6`, `-9`).
- Whether web and phone should keep different stuck thresholds (`x-client-duplication-2`).
- `explain.go` slimming changes model-facing coaching text (`agent-internal-tools-1`).
- Endpoint fingerprint guard policy, malformed plugin recovery, legacy delegate state recovery (conflict 12).
- Whether the demo hub fixture (~10k lines) stays (`native-tests-legacy-mobile-13`).
- Whether the fuzz toolkit, docs-table generator and timing ratchet stay (`build-gates-scripts-1`, `-6`, `-16`).

## 6. Things that look like debt but should stay

- **The refuted finding.** `llm-core-providers-24` (model-specific XML tool-argument rescue in the chat protocol package) was refuted. It is deliberate provider-quirk handling.
- **Crash-safe two-phase commit engine in hub app-host** (`hub-app-host` summary). It is large and heavily commented because the recovery paths are real. Dedupe the per-kind plumbing, but leave the protocol and the recovery paths alone.
- **Lock-protected delegate controller design** (`agent-delegates` summary: "needs no rewrite"). Do the `untrack and signal` helper extraction; leave the claim protocol.
- **Watch-restore semantics (R03) and activity read scope.** `agent-jobs` found nothing that needs to change; preserve both in any refactor.
- **Generated protocol pipeline.** `appwire/protocol.go` to docs and `types.gen.ts`, with its drift test, is the model the other mirrors should copy.
- **Small leaf packages and the widget library.** `sysmsg`, `lineend`, `toolname`, `clock`, `mcphttp`, `cheapmodel`, `runetrim` are tidy; `appoverlay`, `apphistory`, `appitempaging` hold together; the widget barrel and `requireClass` are fine. The `requireClass` boilerplate (`web-shell-widgets-10`, `web-panes-a-18`) is verbose but is the enforcement point for CSS-module realm checks; low priority.
- **Error handling.** Zero empty `if err` blocks; most of ~1,550 `_ =` discards are cleanup or CLI prints (`x-error-handling-slop` summary). Only the 15 `_ = reg.Register(...)` calls and the state-bearing silent discards (`-3`, `-16`) are worth fixing. The 566 unchecked `fmt.Fprint*` calls (`-17`) are noise; leave them.
- **Make targets and env vars** checked clean (`x-dead-code`). Env-var names are in sync.
- **`mobile/` is live.** It is the shared headless core of the phone app, not legacy. The debt is its location, cycle and dead activity store, not its existence.
- **Test infrastructure discipline.** Act/console/ended-body guards, only 7 `vi.mock` calls in the frontend, recorded Go wire fixtures. Do not touch these while cleaning test sprawl.
- **Real concurrency and durability reasoning in comments.** Not all of the 20 to 35% comment density is narration. Keep comments that state an invariant or a lock order (for example the `Session` lock contract) and delete the history around them.

## 7. Suggested order of work

Each step is a PR (or a small stack), CI-verified, no local full test runs.

0. **Decisions** from the Jesse list above (coverage floor, skew policy, FollowUp, stuck thresholds).
1. **Defects** from section 2, each red-test-first, in small PRs. Start with `testing-budget.json` (the ratchet is off), the `LatestIntent` drop, and cache validator keys.
2. **Dead-code deletions**, test fixtures migrated first: `createShell`, legacy spawn, llm streaming half, sshconn ladder, `nolint:unused` set, mockup pages, root `frontmatter`, write-only activity store, unused widgets, hubapi client. Several thousand lines each and low risk.
3. **Shared Go helpers**: durable write, sync predicate, XDG root, session layout and transcript reader, path-contains, truncation. Characterization tests before each.
4. **Mirror collapses**: DetailedStatus, SessionConfig/ConfigSnapshot, LiveEntry, TS enum registry in the generator, shared-client derivations (attention class, stuck policy, durations, outbox).
5. **Skeleton dedupe then file splits** for the god files listed in T4, pure moves for the split commits.
6. **Test-suite consolidation**: coverage-wave deletion (after step 0), kit adoption, copy-oracle fixes, oversized file splits.
7. **Docs and comments**: archive corpus and PNGs, evergreen-doc anchor fixes, AGENTS.md rules, comment cleanup as files are touched.

## 8. Spot checks made for this synthesis

- 7 non-test copies of the sync-unsupported predicate (list under T2): confirmed.
- `fsdurability` lives at `cmd/evener-hub/internal/fsdurability/` and cannot be imported from `server`, `agent` or `auth`: confirmed by path.
- `createShell` has one production definition (`agent/jobs.go:991`) and ~198 call sites in `agent/*_test.go`; the report says 238, so treat the number as "about 200".
- `StreamGenerate`/`StreamGenerateObject` appear only at their definitions plus docs: confirmed no non-test caller.
- File sizes: `job_watch.go` 6,564; `session_tools_worktree.go` 3,798; `threads.ts` 8,990; `server/appwire_runtime.go` 3,074: confirmed.
- 40 `cov_*_test.go` files in `cmd/evener-hub`: confirmed.
- 15 `//nolint:unused` lines in non-test Go: confirmed.
- `testing-budget.json` has zero `"metric"` keys: confirmed, consistent with `build-gates-scripts-1`. The file also lists `primeradiant.com/evener/frontmatter` at line 96.
- Not re-verified by me: individual line counts for comment density, the 543 and 371 phrase-assertion counts, and the "~70 MB unreferenced PNG" figure. I rely on the slice reports and their verifier notes.
