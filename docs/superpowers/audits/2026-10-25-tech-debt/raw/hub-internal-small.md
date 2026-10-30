# Audit: hub-internal-small

Scope: `cmd/evener-hub/internal/{launchconfig,hostreg,daemonprocess,fspaths,hostlock,hubedge,hubtestenv,hubtest,httpsec,fsdurability}` and `hubapi/` (non-test).
Worktree: `.worktrees/debt-audit` at a6c0b9c561 (origin/main). Read-only audit; no tests were run.

## Inventory

| Package | non-test LOC | test LOC | Notes |
|---|---|---|---|
| launchconfig | 1,658 (13 files) | 4,116 | layered launch config: merge, schema, wire, args, env, trust |
| hostreg | 1,014 (1 file) | 1,320 | host registry + identity counters; 426 of 1,014 lines are comments (42%) |
| daemonprocess | 844 | 855 | verified force-stop; linux/darwin/other backends |
| fspaths | 393 | 1,481 | path canonicalize/contain/autocomplete/validate |
| hostlock | ~330 | ~1,000 | flock + storage-locality check, per-OS files |
| hubedge | 310 | 763 | capability-URL auth |
| hubtestenv | 251 | 129 | test env redirect |
| hubtest | 158 | 105 | fixture helpers |
| httpsec | 48 | 92 | one CSP middleware |
| fsdurability | 24 | 0 | one predicate |
| hubapi/ | 836 (7 files) | 466 | REST types, refs, attention ranking, navigation wire, Client |

Overall: the code is generally careful and well-tested. The debt is concentrated in three places: (1) `launchconfig`, where every launch option is hand-written into 8 parallel per-field lists; (2) small predicates and helpers that were reinvented repeatedly across the repo instead of shared (sync-unsupported, path containment, tilde expansion, atomic write, ref parsing); (3) dead or coverage-driven code (hubapi.Client, test-only seams, history comments).

Module note: `auth/`, `agent/`, `llm/`, `envvars/`, `identifier/` etc. are separate Go modules. That explains some copies (they cannot import `cmd/evener-hub/internal`), but not the copies inside the root module.

---

## Findings

### hub-internal-small-1 (medium) The "filesystem cannot sync" predicate exists in 6 copies; the "canonical" one is unreachable and 3 wrappers add nothing

Locations:
- `cmd/evener-hub/internal/fsdurability/fsdurability.go:20` (canonical, 24 lines)
- copies: `server/thread_clear_journal.go:155`, `auth/openai/storage.go:229`, `agent/session_client_mutation_persist.go:423`, `agent/internal/worktree/sync_unix.go:36`, `internal/hubendpoint/runtime.go:203` (inline)
- pass-through wrappers around the canonical one: `cmd/evener-hub/app_host_manage.go:1054` (`hubTOMLSyncUnsupported`), `cmd/evener-hub/internal/hubcore/state_file.go:127` (`deletionSyncUnsupported`)

Evidence: all five copies are byte-for-byte `errors.Is(err, ENOSYS) || ENOTSUP || EINVAL`. The comment at app_host_manage.go:1051 says hubcore and server "keep matching package-private copies ... in their own modules", but `server/` and `internal/hubendpoint` are in the root module and can import a root-level package. The doc comments in each copy say "mirrors the tolerance..." which is the tell for copy-paste.

Impact: any change to the tolerated errno set (e.g. adding `EOPNOTSUPP`) must be made in 6 places; drift is silent.

Fix plan: move `fsdurability` to root `internal/fsdurability` (git mv; update imports in app_host_manage.go, app_host_running.go, hostops/store.go, hubcore/state_file.go). Make `server/thread_clear_journal.go` and `internal/hubendpoint/runtime.go` call it. Delete `hubTOMLSyncUnsupported` and `deletionSyncUnsupported` and call the package directly. For `auth/` and `agent/` (separate modules) leave a decision for Jesse: either accept two copies with a comment pointing at the canonical one, or put the predicate in an existing leaf module (`execsupport`/`invariant`); do not create a new module without approval. Existing tests per copy (e.g. `app_host_manage_round15_test.go:175`, `session_client_mutation_persist_covtest_test.go:346`) collapse into the one fsdurability test.

Size: net about -50 LOC. Risk: low (pure predicate). Docs: none beyond the comment at app_host_manage.go:1051 which gets deleted.

---

### hub-internal-small-2 (high) Adding one launch option means editing 8 parallel per-field lists in launchconfig

Locations: `types.go:16` (Layer struct, 36 fields), `schema.go:90` (hand-written LaunchOption rows), `merge.go:211-435` (one if-block per field, ~225 lines), `args.go:12-128` (one if per field), `wire.go:6-98` (FromWire and ToWire, 34 field lines each), `runtime_defaults.go:228,276` (two per-field switches), `resolver.go:239,279` (path-field lists, twice), plus `appwire.LaunchConfigLayer` and the frontend.

Evidence: merge.go repeats this 20+ times with only names changed:
```go
if l.Model != "" { eff.Model = l.Model; prov["model"] = name; nonEmpty = true }
if l.MaxRounds != nil { v := *l.MaxRounds; eff.MaxRounds = &v; prov["max_rounds"] = name; nonEmpty = true }
```
The toml tag, `LaunchOption.Field`, the provenance key, and the ToArgs flag are the same fact written four times. `LaunchOption` already carries Field/WireField/Kind/Layers/Builtin defaults, but nothing derives from it except `ApplyRuntimeDefaults` partially. A bug here has already happened in the shape: merge.go has special cases (app_replay_size global only, system prompt mode) buried in a flat list so a new field copy-pasted from the wrong neighbor silently loses provenance.

Impact: biggest maintenance drag in the slice. Every launch option PR touches ~8 files; omissions produce silent bugs (field saved but not merged, merged but not passed to argv, no provenance).

Fix plan (behavior-preserving, test-first; the existing 4,100 lines of tests plus `wire_test.go`/`args_test.go` pin behavior):
1. Add a reflection-based completeness test first: every `Layer` field has a schema row (or is in an explicit exclusion list) and a ToArgs mapping; this fails today only if something is already missing and guards the refactor.
2. Introduce small generic helpers, no framework: `mergeScalar[T comparable](dst *T, src T, field string, ...)`, `mergePtr[T any](dst **T, src *T, ...)`, `mergeList`. Convert merge.go block by block; the special cases (app_replay_size, system prompt mode/append, env, mcps, model_fallbacks) stay explicit.
3. Collapse FromWire/ToWire with the helpers in finding 4.
4. Keep ToArgs explicit (flag names are agent CLI contract) but drive the simple `string != ""` and `*int` cases from a table of `{flag, getter}`.
Do not build a full reflect-driven registry; Jesse's simplicity-over-mechanism rule says stop at helpers unless the completeness test shows drift.

Size: roughly -250 LOC net across merge.go/wire.go/args.go. Risk: medium (provenance keys and diagnostics are user-visible; keep tests green per step). Docs: none (internal); if the field-addition checklist exists in docs/developing-evener, add the one-line "touch points" list.

---

### hub-internal-small-3 (medium) runtime_defaults.go re-implements the schema it claims is the single source of truth

Location: `runtime_defaults.go:200-248` (ApplyEnvDefaults) and `:250-358` (ApplyRuntimeDefaults).

Evidence: doc comment at :261 says "The schema is the single source of truth for the defaults - this function applies them, it does not duplicate them", yet the function is a 14-case switch on `opt.WireField` where each case is the same 4 lines ("if effective field empty and opt default set, assign, record provenance"); 3 more cases in the env function. Adding a builtin default to the schema still requires adding a switch case here or it is silently ignored. Also every call site passes `LaunchOptionSchema()` (app_launch.go:125,209,263; app_threadlifecycle.go:342) and `getenv`; the `schema` parameter exists only so tests can pass `schemaForTest()` (runtime_defaults_test.go:16), which is a hand-copied second schema that will not notice production changes.

Impact: drift between schema and applier, plus a test schema that can go stale.

Fix plan: give `LaunchOption` a small setter table keyed by `Field`: `func(*Layer) (isSet bool, setString func(string), ...)`, or simpler, two helper funcs `fillString(&eff.X, opt.BuiltinDefault)` and `fillInt/fillBool` and keep the switch as `case "agent": fillString(&e.Agent, ...)` one-liners. Drop the `schema` parameter (use `LaunchOptionSchema()` internally) and rewrite the tests to use the real schema (assert on behavior: "unset fields get the schema's default and provenance `builtin`"). Handled together with finding 2 step 2.

Size: -90 LOC. Risk: low-medium. Docs: none.

---

### hub-internal-small-4 (medium) wire.go: FromWire/ToWire are mirror images, plus three copy helpers that all do `v := *p; return &v`

Location: `wire.go:6-98`, helpers `:129-164`; `merge.go:473` (`cloneStringSlicePtr`).

Evidence: two 34-line struct literals differ only in direction. `copyIntPtr`, `copyBoolPtr` are identical bodies for different types; `copyStringSlicePtr`, `derefStringSlicePtr`, `cloneStringSlicePtr` (merge.go:473) are three near-identical slice-pointer clones, the latter living in another file. merge.go also inlines the pointer copy 8 times (`v := *l.X; eff.X = &v`).

Fix plan: one generic `func clonePtr[T any](p *T) *T` in a new `clone.go` (or at the top of wire.go), delete `copyIntPtr`/`copyBoolPtr`/`cloneStringSlicePtr`/`copyStringSlicePtr`, keep `derefStringSlicePtr` only if the wire really differs (nil vs empty semantics, documented). Use `clonePtr` in merge.go. Wire conversion stays explicit (appwire types are generated and the explicit literal is a compile-time completeness check, which is worth keeping).

Size: -40 LOC. Risk: low. Docs: none.

---

### hub-internal-small-5 (low) resolver.go validates path fields with two parallel function pairs and the same field list twice

Location: `resolver.go:216-249` (`validateAndExpandRepoLayer`: `expand`/`expandOne`) and `:253-289` (`validateAbsolutePaths`: `check`/`checkOne`).

Evidence: both enumerate the same 9 path fields (`skills_dirs, plugin_dirs, mcp_configs, system_prompt_file, system_prompt_append_file, system_prompt_append, trace_file, cpu_profile, export_atif_path`). The schema already records which fields are paths (`PathKind` in schema.go:90+), a third list of the same fact. `validateAbsolutePaths` takes `diags *[]Diagnostic` and nil-checks it at both call sites' closures, though both callers pass non-nil.

Fix plan: one `mapPathFields(layer Layer, f func(field, v string) (string, bool))` applying a per-value function over the 9 fields; the two validators become 10-line closures. Remove the dead `diags != nil` branches. Optionally assert in a test that the list equals the set of schema options with `PathKind != ""`.

Size: -35 LOC. Risk: low.

---

### hub-internal-small-6 (low) Pass-through wrappers and a dead function in launchconfig

Locations: `trust.go:534-557`, `merge.go:178-190`, `io.go:399`, `resolver.go:146`.

Evidence:
- `TrustHashSet(t)` just calls `trustHashes(t)` (trust.go:550 vs :536); only one non-test caller (app_launch.go:245).
- `HashInSet(hash, set)` is `slices.Contains` (trust.go:555); one caller (app_launch.go:247).
- `IsCredentialEnvKey` is a one-line call to `isCredentialEnvKey` (merge.go:190); one external caller (app_launch.go:193).
- `tomlDecode` (io.go:401) wraps `toml.Decode(string(data), out)`; its comment says "exposed for use by callers", it is package-private, and `loadMetaFS` (io.go:393) and `canonicalHashTOML` (trust.go:523) call `toml.Decode` directly anyway.
- `loadRepoLayer(cwd, stateRoot)` (resolver.go:146) has no production caller; the only reference is `resolver_test.go:368`. The real path is `loadRepoLayerFS`.
- `ValidateRepoRelativePath`/`ValidateAbsolutePath` are exported but have no caller outside the package.

Fix plan: delete the wrappers (rename `trustHashes` to `TrustHashSet`, inline `slices.Contains` at the call site, export once), delete `loadRepoLayer` and point the test at `loadRepoLayerFS` with an `afero.NewMemMapFs()`, unexport the two validators. Delete `tomlDecode` or use it consistently.

Size: -40 LOC. Risk: low.

---

### hub-internal-small-7 (medium) Coverage-driven injection seams in production signatures

Locations: `launchconfig/paths.go:57,61` (`validateRepoRelativePath(..., relPath func)`), `launchconfig/trust.go:19,23` (`canonicalHashTOML(..., encode func)`), `launchconfig/resolver.go:187,193` (`decodeTrustedRepoLayer(..., decode func)`), `fspaths/paths.go:17-39` (`pathOps` struct with `evalSymlinks`/`stat`), `hubedge/auth_token.go:81,84` (`loadOrCreateAuthToken(root, random io.Reader, rename func)`), `daemonprocess/process_linux.go:47-51` (`exitedWith(poll)`).

Evidence: production passes exactly one value everywhere (`filepath.Rel`, a closure around `toml.NewEncoder(w).Encode(l)`, `tomlDecode`, `osPathOps`, `rand.Reader`+`os.Rename`, `unix.Poll`). The only callers of the injecting variants are `launchconfig/coverage_test.go:113,123,133`, `fspaths/paths_test.go:320`, `hubedge/auth_token_test.go:23,32`, `daemonprocess/process_linux_test.go:47`. These make stdlib functions that cannot fail in practice return errors to reach error branches (`filepath.Rel` failing, `toml.Encode` failing on a Layer). The file name `coverage_test.go` states the intent. This tests mocked behavior, which the repo rules forbid, and costs signature noise in 6 production functions.

Fix plan: where the error branch is unreachable with real inputs, delete the branch and the seam (`validateRepoRelativePath` rel error, `canonicalHashTOML` encode error, `decodeTrustedRepoLayer` decode param). Where a failure is reachable (token write, rename), test with a real fault: read-only directory or a path whose parent is a file, using the real function. Keep `exitedWith(poll)` only if EINTR retry is not testable otherwise (it is the one legitimately OS-boundary seam; leave it). Delete `coverage_test.go` and `merge_coverage_test.go` after migrating any case that exercises real behavior.

Size: -200 LOC mostly tests. Risk: low; coverage % may dip on unreachable branches, which is the honest number.

---

### hub-internal-small-8 (medium) Tests assert on error/diagnostic copy

Locations (28 `strings.Contains` on messages in this slice): `launchconfig/io_test.go:136,151,168,183,202,220,236,252,271,289` (e.g. `"launchconfig: read "+path`), `launchconfig/merge_test.go:124,170,250` (`"no effect"`, `"unknown sandbox mode"`, `"UI supports one append source"`), `launchconfig/coverage_test.go:97,116`, `hubedge/auth_token_test.go:432,439,456,473,491,507,645` (`"mkdir"`, `"read"`, `"Unauthorized"`), `hostlock/hostlock_test.go:52,88,106,129`, `hubapi/client_test.go:122` (`"400"`).

Evidence: AGENTS.md "Never assert on phrasing" (testing.md "Copy Is Not a Test Oracle"). The io_test cases pin the exact wrapper prefix (`launchconfig: rename X.tmp -> X`) which no caller parses.

Fix plan: assert `errors.Is`/`errors.As` on the underlying cause (the tests already inject sentinels), and for diagnostics assert `Layer` and `Field` (merge_test already filters on those) without the Message clause. For hubedge, assert status code and absence of `Set-Cookie`. For hostlock, assert `errors.Is(err, syscall.EWOULDBLOCK)`-class or a typed error. Where a stable distinguishing signal is genuinely missing, add a typed error rather than keep the string match.

Size: ~60 LOC touched. Risk: low.

---

### hub-internal-small-9 (medium) At least 10 hand-rolled "is path inside root" helpers, 3 of which this slice owns

Locations: `fspaths/paths.go:124` (`withinRoot`), `launchconfig/paths.go:475-489` (`validateRepoRelativePath` using Rel + `..` prefix), `launchconfig/resolver_fuzz_test.go:172` (`withinRoot` test copy), `hubtestenv/hubtestenv.go:246-250` (`ContainedIn`, Rel-based), plus elsewhere: `agent/session_tools_worktree.go:1685 pathWithin`, `agent/sandbox/session_scratch.go:213 pathWithin`, `agent/execenv/local.go:3491 pathWithinRoot`, `agent/session_notes.go:705`, `internal/plugins/marketplaces.go:1459 pathWithinDir`, `cmd/evener-fuzzregistry/main.go:466`, `tools/tool-fluency/.../review.go:299`.

Evidence: two techniques (prefix-with-separator in fspaths vs `filepath.Rel` + `".."`prefix in launchconfig and hubtestenv) with different edge handling (`rel == "."` is "inside" in `withinRoot` but "not inside" in `ContainedIn`: hubtestenv.go:250 `rel != "."`). Similarly three implementations of "resolve symlinks through deepest existing ancestor": `hubtestenv.canonicalizeExisting` (:211), `agent/sandbox/resolve.go:1014 resolveExistingPrefix`, and `fspaths.canonicalizeDir` (strict, different semantics).

Impact: security-relevant boundary logic with divergent semantics in a dozen places.

Fix plan: for the in-slice copies, have `launchconfig.validateRepoRelativePath` and the fuzz-test copy call `fspaths.Within` (export `withinRoot` as `Within`, document `target == root` is inside) and delete the Rel-based variant. `hubtestenv.ContainedIn` keeps its strictness but is renamed `StrictlyInside` and built on the shared helper. Cross-module copies (agent/) are a separate decision for Jesse; list them in the cross-cutting report.

Size: -40 LOC in slice. Risk: medium (security boundary; the fspaths and launchconfig tests plus fuzzers already pin traversal cases). Run the existing fuzz seeds in CI.

---

### hub-internal-small-10 (low) Tilde expansion reinvented 3 times in the hub plus llm.ExpandTilde; and the output-file path-kind spelling forks

Locations: `fspaths/app_paths.go:35` and `:156`, `cmd/evener-hub/app_dirs.go:22`, `llm/media_utils.go:37` (`ExpandTilde`, different rules: `~` alone is not expanded, uses os.UserHomeDir not envvars.Home). Kind spelling: `fspaths/app_paths.go:169` accepts `"output-file"` while `launchconfig/schema.go:36` declares `LaunchPathOutputFile = "outputFile"`; `appwire-client/typescript/launchSchema.ts:20-25` carries a shim mapping one to the other.

Evidence: each hub copy is `strings.HasPrefix(p, "~/") || p == "~"` then `filepath.Join(envvars.Home.Getenv(), strings.TrimPrefix(p, "~"))`. `ValidateLaunchPath` (app_paths.go:150-218) also repeats the "is a directory" and "not executable" branches across `command`, `file`, `executable` cases.

Fix plan: add `fspaths.ExpandHome(p string) string` (the hub rule: env-based home, bare `~` included), use it in the three hub sites. Decide the output-file spelling: make the Go validator accept the schema spelling `outputFile` and delete the TS shim (behavior change, TDD; coordinate with appwire-client owner and note in the shared-client docs). Fold the duplicate switch branches in `ValidateLaunchPath` with a `rejectDir`/`needExec` helper.

Size: -45 LOC Go, -6 TS. Risk: low-medium for the wire spelling (client + hub change together).

---

### hub-internal-small-11 (medium) hostreg.go: the identity-counter raise logic is hand-repeated 11 times; the file is 42% comment

Location: `hostreg/hostreg.go` lines 325, 347, 412, 512, 515, 579, 590, 670, 673, 956, 959 (`if x > r.gen { r.gen = x }` / `if p > r.presence[name] { r.presence[name] = p }`).

Evidence: five code paths each re-derive "mint generation, incarnation, presence; raise counters": `seedLocked` (:317), `Stamp` (:382), `insertLocked` (:502), `Update` (:539, a 60-line function with a 3-way switch), `RebaseBootCollision` (:933), `SeedHighWater` (:663). `RebaseBootCollision` (:944-960) re-implements `mintIdentityLocked` (:600) inline (next generation, next presence, mint id, raise counters). 426 of 1,014 lines are comments, many narrating spec sections ("spec 08 section 1", cited 21 times) and restating the code.

Impact: this is the registry that guards attach identity; each new path must remember all the raise-only rules, and a missed one lowers a durable high-water mark.

Fix plan: add `func (r *Registry) raiseLocked(name string, id Identity)` doing the two raise-only updates, and make `RebaseBootCollision` call `mintIdentityLocked`. Call `raiseLocked` from the five sites. Cut comments that restate code to the invariant they protect (one paragraph per type); move the spec narrative into `docs/product/` (subsystems.md already owns host registry responsibilities) rather than the source. Existing `incarnation_test.go`/`update_test.go` (643 lines) pin behavior.

Size: -120 LOC (code -35, comments -85). Risk: low-medium; do it test-first, no behavior change.

---

### hub-internal-small-12 (medium) `hostreg.SameRegistration` omits PresenceEpoch, so four call sites re-spell the comparison

Locations: `hostreg/hostreg.go:846` (definition), `sshconn/deploy.go:1121`, `:1240`, `:1255`, `sshconn/selected_restart.go:189`.

Evidence: deploy.go:1255 `!hostreg.SameRegistration(current, host) || current.PresenceEpoch != host.PresenceEpoch`; deploy.go:1121 hand-writes `Equal && Generation && IncarnationID && PresenceEpoch`; selected_restart.go:189 does the same hand-written four-way check. The doc on SameRegistration (hostreg.go:834-845) argues all three of content/generation/incarnation are needed, but presence epoch, which spec 08 calls part of the guarded-mutation identity (hostreg.go:105-113 on the Host fields), is not part of it.

Impact: the next identity check someone writes will pick one spelling or the other; omission of PresenceEpoch is a latent fence hole.

Fix plan: include `PresenceEpoch` in `SameRegistration` (verify against `app_host_*` callers at app_host_ops.go:313, deploy.go:283, :498 by running their tests in CI), then replace the four hand-written comparisons with it. Tests: add a failing case first (same content/generation/incarnation, different epoch must not match).

Size: -12 LOC, +1 test. Risk: medium (behavior change at fence points; intended). Docs: update hostreg section in docs/product/subsystems.md if it enumerates the identity triple.

---

### hub-internal-small-13 (medium) hubapi.Client, HTTPError and URL are dead in production; StateWord has no Go caller

Locations: `hubapi/client.go:12-82`, `hubapi/attention.go:70-92` (`StateWord`).

Evidence: `grep hubapi.NewClient` outside `hubapi/` returns nothing; the only users are `hubapi/client_test.go` and `hubapi/refs_fuzz_test.go:91`. `HTTPError` (client.go:20-32) is never constructed: `Client.get` returns `fmt.Errorf("hub returned %d", ...)` at client.go:184, so `StatusCode()` and `ErrorResponse` decoding are unreachable (and the test at client_test.go:122 asserts on the string "400"). `StateWord` is called only by `attention_test.go:74` and the fuzz file; the display word "Question waiting" lives in the frontend (`RailRow.test.tsx:553`). Its doc says "used by clients" but no client calls it. History: commit 0e4a9f406d "Remove unused session REST surface" left this behind.

Fix plan: delete `client.go`, `client_test.go`, the Client-based fuzz target, `HTTPError`; delete `StateWord` and `NeedsResponse` if no external user remains (only one external ref to NeedsResponse: confirm before delete). Keep `ErrorResponse`, `HealthResponse`, `MobileAPIVersion`, `HealthCapabilities` (used by web_api.go and sshconn/version.go:1190). Update `docs/product/subsystems.md` if hubapi is listed as a client.

Size: -250 LOC incl. tests. Risk: low. Needs Jesse's nod only because it deletes a public-looking package surface; grep shows no importers in this repo.

---

### hub-internal-small-14 (medium) Two Ref types and two ref parsers with drifted grammar; stale "does not import appwire" doc

Locations: `hubapi/refs.go:69-111` vs `appwire/refs.go:19-47`; `hubapi/types.go:35-38`.

Evidence: `hubapi.Ref{HostID, SessionID}` / `ParseRef` and `appwire.Ref{SourceID, ThreadID}` / `ParseRef` both format and parse `"<source>:<id>"` with the same `String()`. Grammar differs: hubapi uses `^[A-Za-z0-9._~:-]+$` (permits extra `:` in the session id, refs.go:69) while appwire uses `[A-Za-z0-9._~-]+` per part (appwire/refs.go:10), and `..` is rejected only in the id in both. appwire.ParseRef has 82 non-test callers, hubapi.ParseRef 16 (web_api_tree.go:1288,1301; app_activity.go:38; app_session_seen.go:48; navigation_projection.go:779). navigation_projection.go:779-795 round-trips a bare id through `LocalRef(id).String()` then `ParseRef`. The literal `"local"` is also hostreg.ReservedName (hostreg.go:27) and hubapi.LocalRef (refs.go:80). types.go:35-38 says hubapi "deliberately does not import appwire", but navigation.go:7 imports it, and `AttentionSummary` there is a field-identical twin of `appwire.AttentionSummary` (appwire/attention.go:31).

Fix plan: make `hubapi.Ref` a thin alias/method set over `appwire.Ref` (or delete it and use appwire.Ref with `LocalRef` as a constructor in appwire), settle the grammar on the appwire one, and make `hubapi.AttentionSummary` a type alias of `appwire.AttentionSummary` (drops the `//nolint:tagliatelle`). Fix the stale comment. Tests: add a table test that a session id containing `:` is rejected/accepted consistently (decide which; confirm with Jesse because a remote id containing `:` would change behavior).

Size: -60 LOC. Risk: medium (ref grammar change touches 16 call sites).

---

### hub-internal-small-15 (medium) Atomic file write implemented ad hoc in launchconfig and hubedge, weaker than the hub's other store writers

Locations: `launchconfig/io.go:53-93` (`writeAtomicFS`), `hubedge/auth_token.go:106-113`; compare `hubcore/state_file.go:35` (`writeStateFileAtomic`), `hostops/store.go:1127,1255`, `app_host_running.go:259,292`, `internal/plugins/atomic.go:32`, `internal/transcriptindex/index.go:1294`.

Evidence: launchconfig writes a fixed `path + ".tmp"` (two concurrent saves collide and one truncates the other's temp), fsyncs the file but never the directory; hubedge writes the token with `os.WriteFile(tmp)` with no fsync at all, then renames, so a crash can leave an empty token file that `LoadOrCreateAuthToken` then treats as "regenerate" (empty trimmed content falls through at :94) and rotates a persisted capability URL. The hub's other stores use `TempFile` + file fsync + rename + dir fsync tolerating `fsdurability.SyncUnsupported`.

Fix plan: extract one `atomicfile.Write(fs afero.Fs, path string, mode, data, opts)` in root `internal/` from `writeStateFileAtomic` (it already has before/after rename hooks for faults), move launchconfig (`SaveLayer`, `SaveMeta`) and hubedge to it, and delete `writeAtomicFS`. Keep `recoveryStore`'s distinct contract out of it as hubcore already documents. TDD: write a test that two concurrent `SaveLayer` calls to the same path both succeed and leave one complete file (fails today).

Size: -90 LOC in slice, +60 shared. Risk: medium; do hubcore's own call sites in a second PR.

---

### hub-internal-small-16 (low) Backward-compat paths in launchconfig that need a decision

Locations: `merge.go:376-389` (legacy `system_prompt_append` list, truncated to the first entry with a diagnostic), `resolver.go:111-141` (`LegacyProject` read-only fallback to `<root>/projects/<id>/launch.toml`, with a "migrate" diagnostic), `paths.go:434`.

Evidence: both carry comments calling the old shape "legacy". `Layer.SystemPromptAppend` is still a toml field (`types.go:42`) and is expanded/validated in resolver.go:244,284 solely for the legacy path. AGENTS/CLAUDE rule: no backward compatibility without Jesse's approval; these predate or ignore that rule.

Fix plan: ask Jesse whether the legacy project layer and `system_prompt_append` can be dropped (they need a one-time migration or deletion). If yes: delete the fallback branch, `LegacyProject` path, `SystemPromptAppend` field and its handling in merge/args/wire/resolver (also removes 3 of the parallel list entries in finding 2), update tests. Not a code change until decided.

Size: -90 LOC if removed. Risk: medium (users with old config lose it silently unless migrated). Escalate as a question.

---

### hub-internal-small-17 (low) hostlock/daemonprocess keep stub backends for platforms the hub never ships, and two seams for one thing

Locations: `hostlock/hostlock_other.go:10-28`, `hostlock/storage_other.go:10`, `daemonprocess/process_other.go:8-13`, `hostlock/marker_darwin.go:8` vs `hostlock_other.go:24` (identical no-op `lockMarker`), `hostlock/storage.go:27,32,77-80` (package var `filesystemInspector` AND a per-call `inspect` parameter), `daemonprocess/process_{linux,darwin,other}.go` (three identical `NewController()` one-liners).

Evidence: hostlock_other.go:16-19 says "evener-hub never ships here". `CheckStorage(root, inspect)` takes an injected inspector, main.go:179,314 passes `deps.inspectFilesystem` which is nil in production, and a test-only global var (`storage.go:26` "replaced only by serial OS-boundary package tests") does the same job.

Fix plan: collapse to `//go:build !linux && !darwin` single stub file per package returning an unsupported error (or drop non-unix build support if go.mod/CI never builds it; confirm with `.tool-versions`/CI matrix). Remove the global `filesystemInspector` var and keep the parameter (the parameter is the one seam used by endpoint.go). Move `NewController` to process.go with a package-level `nativeBind`.

Size: -40 LOC. Risk: low.

---

### hub-internal-small-18 (low) Three small HTTP-security packages with duplicated bearer parsing and opposite empty-token semantics

Locations: `httpsec/httpsec.go` (one 25-line middleware), `hubedge/auth_token.go:121-137` (`tokenFromRequest`, `tokensEqual`), `internal/httpguard/httpguard.go:70-80` (`HubTokenAuthorized`), `cmd/evener-hub/app_device_pairing.go:39` (hand-set `nosniff`).

Evidence: both hubedge and httpguard parse `Authorization: Bearer ` with `strings.HasPrefix` + `TrimSpace(TrimPrefix)` + `subtle.ConstantTimeCompare`. They disagree on an empty expected token: httpguard returns true ("authorizes every request", :71), hubedge `tokensEqual` returns false for empty (:133) and only `AuthGuard` special-cases `token == ""` to bypass (:168). hubedge's package doc calls itself the "sibling of httpsec", so the split is by intent, not by shared code. `app_device_pairing.go:39` sets `X-Content-Type-Options` outside `CSPMiddleware`.

Fix plan: export `httpguard.BearerToken(r *http.Request) string` and `httpguard.TokensEqual`, use them from hubedge; keep each package's empty-token policy but document it at the shared helper. Optionally fold httpsec into hubedge's package since it has one consumer (`web.go:348`).

Size: -25 LOC. Risk: low-medium (auth code; cover with existing hubedge tests).

---

### hub-internal-small-19 (low) History and ruling narration in comments

Locations: `httpsec/httpsec.go:7-26` (kata 1pgw and "tracked separately" in a doc comment), `hubedge/auth_token.go:181-186` ("JESSE'S RULING (2026-09-01) ... no backward compatibility") and `:35-36`, `hubedge/auth_token_test.go:168,282`, `hostreg/hostreg.go` (21 "spec 08 / component 05" references; "Previously ..." style text), `hubapi/attention.go:1-6` ("Previously AttentionRank and rollupRank were duplicated in hubcore, and a client carried a third copy"), `fsdurability.go`-style history in `daemonprocess/process.go:88,235` (#3339, #3383).

Evidence: user rule: history belongs in commit messages. These comments describe how code used to be or who decided, not what it does.

Fix plan: rewrite each to the current-behavior sentence; keep issue numbers only where they justify an order-dependent read (daemonprocess linux read-order comment is load-bearing, keep that one). No code change.

Size: -70 comment lines. Risk: none.

---

### hub-internal-small-20 (low) hubapi navigation Validate duplicates owner checks and repeats string kinds

Location: `hubapi/navigation_delta.go:5-8` (consts), `NavigationSnapshot.Validate` (container owner checks) vs `NavigationDelta.Validate` (`resource_root`/`entity` checks repeated), kinds `"resource_root"`, `"entity"`, `"session"` as 21 raw literals across hubapi and cmd/evener-hub.

Evidence: Snapshot.Validate's owner check is a single-line compound boolean mixing `&&`/`||` without parentheses (`container.Owner.Kind == "resource_root" && container.Owner.EntityKey != "" || container.Owner.Kind == "entity" && (...)`), followed by a second statement re-checking the kind; Delta.Validate re-implements a subset. No constants for the kinds.

Fix plan: add `const ownerResourceRoot, ownerEntity` and `func (o NavigationContainerOwner) validate() error`; call it from both. Add a table test for owner validity first.

Size: -20 LOC. Risk: low.

---

### hub-internal-small-21 (low) daemonprocess: `Controller` interface and `controller{bind}` struct wrap a function; platform offsets are magic numbers

Location: `daemonprocess/process.go:120-121,146-148,171-188`, `process_darwin.go:80,121,144,145,208-224` (offsets `[16:]`, `[32:]`, `[120:]`, `[128:]`, `size 56/136/176`, flag masks `2`, `0x4000`).

Evidence: `Controller` is a one-method interface; its only production implementation is `controller{bind}` which holds a function pointer set per platform; all hub tests fake it with `forceStopControllerFunc` (cmd/evener-hub tests), so the interface is justified at the consumer side, but the struct plus `nativeBind` var plus `NewController` x3 is three layers for one call. The darwin file parses `proc_info` structs with raw byte offsets and no named constants (partially commented with `// SZOMB`), unlike the linux file which names `linuxPFExiting`.

Fix plan: replace `controller{bind}`/`nativeBind` with a package `Open(Target)` function per platform behind one `var bind = ...`, keep the `Controller` interface for the hub's fake. Name the darwin offsets (`bsdInfoStartSecOffset`, etc.) as consts next to `darwinProcInfo`. Low priority; the daemon-kill code is correct and heavily tested, so do not touch behavior.

Size: net -15 LOC. Risk: low (cannot run darwin tests locally; rely on CI).

---

### hub-internal-small-22 (low) hubtestenv and the stale-cache pinning duplicate cmd/evener's TestMain; test helper packages are non-test packages

Location: `hubtestenv/hubtestenv.go:83-98` (comment: "exactly as cmd/evener's TestMain does"), `hubtestenv/hubtestenv.go:20` (imports `agent/sandbox/sandboxtest` from a non-test package), `hubtest/capability_seams.go:17` (imports `server` from a non-test package).

Evidence: `GOCACHE/GOPATH/GOMODCACHE` pinning is copied in `gatemodulerunner_test.go`, `install_test.go`, `agent/session_capabilities_test.go`, `agent/memory_eval_test.go` (GOMODCACHE appears in gatemodulerunner_test.go, install_test.go and several agent tests). Non-test packages `hubtestenv` and `hubtest` ship in `go build ./...` and import test-only helper/production server packages; no production binary imports them (only `_test.go` files do), so this is hygiene, not a bug.

Fix plan: once per-module test helpers exist, extract the Go-cache pin to the shared place `agent/sandbox/sandboxtest` already offers (it exports `RedirectHostTemp`/`Redirected`). Low value; only do when touching these TestMains.

Size: -30 LOC. Risk: low.

---

## Recommended fix sequence

1. Finding 13 (delete dead hubapi.Client/HTTPError/StateWord) and finding 6 (launchconfig wrappers and dead `loadRepoLayer`): pure deletions, no risk, shrink the surface before refactors.
2. Finding 1 (fsdurability to root internal, delete wrappers, fix server and hubendpoint copies).
3. Finding 8 (stop asserting copy) and finding 7 (remove coverage-only seams): do these before the launchconfig refactor so the tests that guard it are behavior tests.
4. Finding 2 with 3, 4, 5 as one PR series on launchconfig: completeness test first, then `clonePtr`, merge helpers, runtime_defaults one-liners, path-field mapper. Finding 16 decision from Jesse first so the legacy fields are deleted rather than refactored.
5. Finding 12 (SameRegistration includes PresenceEpoch) TDD, then finding 11 (hostreg counter helper, comment cut).
6. Finding 14 (unify Ref/AttentionSummary with appwire) after confirming the `:` grammar choice with Jesse; finding 9 (shared containment helper) and 10 (ExpandHome, output-file spelling) together as the "path helpers" PR.
7. Finding 15 (shared atomic write) as its own PR with the concurrent-save regression test; then 18, 17, 20, 21, 22, 19 as low-priority cleanup, comments (19) folded into whichever PR touches the file.
