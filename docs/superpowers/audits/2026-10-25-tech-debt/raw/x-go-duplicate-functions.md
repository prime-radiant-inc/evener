# x-go-duplicate-functions: cross-cutting semantic duplicate functions (Go)

Scope: semantic duplicates of small helpers across all Go modules. Method: catalog every
`func` signature (13,527 declarations in non-test Go, 6,146 unexported), grep by purpose
(truncation, atomic write, backoff, sleep, pid/process, env, path, JSON decode, clone, sort/dedup,
string utils, tilde/home), read the bodies, and cluster.

## Inventory

- 1,136 non-test Go files, about 391,800 lines, plus 3,376 test files. Nine Go modules in `go.work`:
  root, `agent`, `llm`, `auth`, `envvars`, `execsupport`, `fuzz`, `identifier`, `invariant`.
- Import visibility matters for fixes. Go's `internal/` rule is path based, so `agent/`, `llm/` and
  `execsupport/` (all under `primeradiant.com/evener/...`) may import root `internal/...` only if their
  `go.mod` requires the root module. `agent` and `llm` already do. `auth` requires only `envvars`.
  `cmd/evener-hub/internal/*` is importable only inside `cmd/evener-hub`.
- `execsupport` is the one leaf module every other module already requires (`agent`, `llm`, root). Its
  README calls it "the shared low-level helpers used by Evener's root, agent, and LLM modules". It is the
  natural canonical home for filesystem and path primitives. `auth` would need one new `require`.
- Lint has `errorlint`, `modernize`, `gocritic`, `revive` but no `dupl`, `forbidigo` or `depguard`, so
  nothing stops a new private copy of a helper.

Clusters found (2+ implementations): 19. Highest drift: atomic writes (about 30 copies), path containment
(13 copies, argument order flips between them), rune-safe truncation (15 copies, several byte-cutting).

Reviewed and healthy: env-var access (`envvars.Var`, one registry, no `os.Getenv` parse sprawl),
process-group signalling (`execsupport/procgroup`; `internal/devtool/procgroup` is a documented thin layer),
activity text bounding (`agent/activitybound` is the single statement for the activity projection),
`appwire/clone.go` deep clones of wire types (type-specific, not duplicates), `plugins.copyTree` vs
`copyTreeExact` (different, documented semantics: Git-metadata refusal vs exact mode/symlink copy).

---

## Finding 1: about 30 hand-rolled "temp file + rename" writers, with drifting durability and safety

Locations (each opened):
- Fixed `path+".tmp"` names, no exclusive create: `rendezvous/rendezvous.go:96`,
  `server/thread_clear_journal.go:107`, `agent/schema/boot_generation.go:71`,
  `agent/schema/snapshot.go:641`, `agent/session_queue_persist.go:71`, `agent/task/task_store.go:345`,
  `agent/internal/jobstore/output.go:794,906`, `cmd/evener-migrate/convert.go:242`,
  `cmd/evener-hub/internal/hubedge/auth_token.go:106`, `cmd/evener-hub/internal/launchconfig/io.go:57`,
  `internal/transcriptindex/index.go:1295`.
- Random temp name, no fsync: `llm/registry/refresh.go:48`, `cmd/evener-dev/agentshards.go:750`.
- Random temp, fsync, rename, directory fsync: `internal/plugins/atomic.go:32`,
  `llm/registry/write.go:248`, `cmd/evener-hub/app_host_manage.go:914` and `:2912`
  (two copies in one file), `cmd/evener-hub/internal/hostops/store.go:1101`, `quarantine.go:848`,
  `hubcore/state_file.go:35`, `hubcore/recovery_store.go:142`, `internal/credentials/store.go:204`,
  `auth/openai/storage.go:173`, `internal/hubendpoint/runtime.go:142`,
  `agent/session_client_mutation_persist.go:152`, `agent/internal/worktree/sidecar.go:206`,
  `cmd/evener-hub/app_rpc_agents_doc.go:96`, `cmd/evener-hub/app_instances.go:715`.
- Plus `agent/execenv/securepath_fdops_unix.go:916` (fd-relative `atomicWriteAt`),
  `tools/tool-fluency/.../version_manifest.go:168`, `cmd/evener-transcript-v2-upgrade/main.go:353`.
- Counting every non-test file that has both a temp-file creation and a `Rename(`: 34 files.

Evidence: `llm/registry/write.go:219-229` documents why a fixed name is wrong: "a planted symlink at a
predictable temp name would otherwise redirect the save into a file of the attacker's choosing, the same
class #1040 fixed for the target". `internal/credentials/store.go:188` repeats the same rule. Eleven other
writers (list above) still use `path+".tmp"`. The `app_host_manage.go` pair is a 45-line body copied with
only the error strings changed (`hub.toml write:` vs `write %s:`). Chmod handling varies by site: `0o600`
set via `tmp.Chmod` in some, `0o644` in others, absent in the rest.

Impact: any change to the durability recipe (fsync order, directory sync tolerance, mode, cleanup of the
temp file on failure) has to be found in 30 places. The security rule already applied in 8 of them is
silently missing in 11. Roughly 800 lines are the same sequence of write, sync, close, rename, cleanup.

Fix plan:
1. New `execsupport/fsatomic` (leaf, stdlib only): `WriteFile(path, data, perm)` and
   `Write(path, perm, func(io.Writer) error)`, with options for `Sync` (default on) and a directory-sync
   hook. Implementation: `os.CreateTemp` in the target directory, `Chmod(perm)`, write, `Sync`, close, rename,
   directory sync tolerant of `SyncUnsupported` (see finding 2), remove temp on any failure.
2. An afero variant lives next to it (`WriteFileFS(fs afero.Fs, ...)`) for the hub stores that inject
   `afero.Fs` (state_file, recovery_store, hostops, quarantine, credentials, launchconfig,
   client mutation, queue persist, task store). Keep the injected "before/after rename" hooks that
   `writeStateFileAtomic` exposes as options, because tests drive them.
3. Migrate one package per commit, strongest first (the 11 fixed-name sites, then the two `app_host_manage`
   copies, then the rest). Each keeps its own tests; add table tests in `fsatomic` for: temp removed on
   write/sync/rename failure, mode honored under a restrictive umask, concurrent writers never expose a
   partial file, a pre-planted symlink at `path+".tmp"` is not followed.
4. Do NOT migrate the deliberately special cases: `securepath_fdops_unix.go` (fd-anchored),
   `selfupdate` (staged binary with restore), `jobstore/output.go` (append-journal protocol), after reading
   each. Say in the package doc that they are exceptions.
5. Ask Jesse whether to add a root `*_audit_test.go` (the repo has several, e.g. `recursivedelete_audit_test.go`)
   that fails on `os.CreateTemp` followed by `Rename` outside `fsatomic`. That is a consequential choice, so
   it is not in the default scope.
6. Docs: `docs/architecture.md` mentions `execsupport`; add `fsatomic` to its list and to
   `execsupport/README.md`.

Size: net -600 to -700 lines (add about 150, remove about 800). Risk: medium. Behavior moves from
"truncate/rename without fsync" to "fsync" in the lightweight stores (queue persist, task store), which is
slower per write. Decide per store whether to pass `NoSync`. Auth module needs a `require execsupport`.

---

## Finding 2: seven copies of the "directory fsync unsupported" predicate, plus test-name wrappers

Locations:
- Canonical already exists: `cmd/evener-hub/internal/fsdurability/fsdurability.go:20`
  (`SyncUnsupported`: ENOSYS, ENOTSUP, EINVAL).
- Identical private copies: `server/thread_clear_journal.go:155` (`journalSyncUnsupported`),
  `auth/openai/storage.go:229` (`authSyncUnsupported`), `agent/session_client_mutation_persist.go:423`
  (`clientMutationSyncUnsupported`), `agent/internal/worktree/sync_unix.go:36` (`syncUnsupported`),
  inline in `internal/hubendpoint/runtime.go:203`.
- Pass-through wrappers over the canonical one: `cmd/evener-hub/internal/hubcore/state_file.go:127`
  (`deletionSyncUnsupported`, whose comment says it "is the name the deletion store's tests use"),
  `cmd/evener-hub/app_host_manage.go:1054` (`hubTOMLSyncUnsupported`).
- Whole directory-sync functions also duplicated: `agent/internal/worktree/sync_unix.go:19`,
  `hostops/store.go:1250` (`syncDirFS`), `internal/hubendpoint/runtime.go:196`, `internal/plugins/atomic.go:62-66`.

Evidence: all copies are the same three `errors.Is` lines. The comment at `app_host_manage.go:1048-1053`
says other modules "keep matching package-private copies" because the canonical one is under
`cmd/evener-hub/internal` and unreachable from `server`, `agent`, `auth`. That visibility is the root cause.

Impact: a new errno (for example ENOTTY on some FUSE mounts) must be added seven times. The two wrappers
exist only so tests can keep an old name.

Fix plan: move `SyncUnsupported` and `SyncDir(dir)` into `execsupport/fsatomic` (finding 1) so every module can
reach them; delete `fsdurability`, the five copies, and the two wrappers (rewrite the tests that name
them to call the canonical function, preserving the assertions). One commit per consumer.
Size: -80 lines. Risk: low.

---

## Finding 3: seven exponential-backoff "double and cap" implementations

Locations:
- `agent/session_fail_closed.go:133` (`durabilityRetryBackoff`) and `agent/job_shell.go:720`
  (`shellFinalizeBackoff`): byte-identical bodies (`for range attempt { delay *= 2; if delay >= max {return max} }`),
  differing only in constants.
- `cmd/evener-hub/app_relay.go:164` (`relayRetryBackoff.Next/Reset`).
- `cmd/evener-hub/app_host_admin.go:652` (`nextHostNotificationBackoff`).
- `cmd/evener-hub/internal/sshconn/manager.go:2876` (`nextBackoff`), the only one that saturates against
  overflow ("a negative delay fires the sleep timer immediately instead of backing off").
- `cmd/evener-hub/internal/appsource/relay_session.go:591-598` (inline loop, 100ms to 5s).
- `internal/plugins/locks.go:353` (inline, 200ms cap).
- `llm/retry_util.go:169` (`retryDelay`), a richer policy (jitter, Retry-After) that is legitimately separate.

Evidence: only the sshconn version guards overflow; the other five would overflow if given a large
base, and each re-derives the cap check differently (`>=` vs `>`, before vs after doubling).

Impact: small each, but five readers must re-verify the same 8 lines and the overflow behavior is
inconsistent.

Fix plan: add `execsupport/backoff` with `Next(delay, limit time.Duration) time.Duration` (the sshconn
saturating form) and `Nth(base, limit, attempt)`. Replace the six callers; keep `llm.retryDelay`.
Tests: move sshconn's overflow test to the new package, keep each caller's schedule tests (assert the
delay sequence, not messages).
Size: -50 lines. Risk: low.

---

## Finding 4: three context-aware sleep helpers

Locations: `llm/retry_util.go:17` (`DefaultSleep`), `auth/openai/device.go:301` (`ctxSleep`),
`cmd/evener-hub/internal/sshconn/manager.go:354` (`Options.waitSleep`, falls back to an inline timer/select).
Test stubs for the type are redefined as `noSleep`, `noSleepRetry`, `logicalGroupNoSleep`, `noNamerSleep`,
`noSleep` in five `_test.go` files.

Evidence: all three are `NewTimer`, `select` on `ctx.Done()` and `timer.C`. They differ on `d <= 0`:
`ctxSleep` still checks `ctx.Err()`, `DefaultSleep` returns nil without checking, `waitSleep` creates a
zero timer.

Fix plan: fold into `execsupport/backoff` as `Sleep(ctx, d)` with the `ctx.Err()`-checking semantics
(strictest), keep `llm.SleepFunc` as the injection type. Delete the other two.
Size: -35 lines. Risk: low; the zero-duration behavior change is observable only for cancelled contexts.

---

## Finding 5: 15 "truncate with ellipsis" helpers, several of which cut bytes mid-rune

Locations (rune-aware): `agent/activitybound/activitybound.go:49` (`Truncate`, the careful one),
`appwire/delegate_roster.go:33`, `agent/transcript_render.go:1966` (`truncRunes`),
`agent/transcript_util.go:10-26`, `agent/transcript_render.go:1095`, `agent/internal/hooks/hooks.go:565`,
`internal/apptranscript/apptranscript.go:412`, `llm/registry/credential_json.go:80` (`clipEcho`),
`cmd/evener-hub/navigation_projection.go:1932` (`truncateNavigationRunes`, `truncateNavigationBytes`),
`cmd/evener-hub/internal/hubcore/tree.go:558` (`truncateTitle`), `agent/internal/tool/breaker.go:591`
(`TruncateRunes`), `execsupport/valueexpr/evaluate.go:345` (`firstLine`, 200 runes),
`agent/jobs_activity.go:109` (`truncateActivityText`, a pure pass-through to `activitybound.Truncate`, 32 call sites).
Byte-based (can split a multi-byte rune): `agent/internal/contextmgr/fork_summarize.go:104`,
`agent/internal/contextmgr/context_manager.go:1092,1393,1434,2561,2614,2660`,
`agent/internal/contextmgr/strategy_session_log.go:204`, `agent/prompt_data.go:212`,
`cmd/evener/run.go:574`, `cmd/evener-hub/app_search.go:263`, `cmd/evener-hub/navigation_projection.go:1943`
(this one repairs the cut), `agent/doctor/transcript.go:459` (uses `runetrim.Cut`).
Byte-safe primitive: `agent/internal/runetrim/runetrim.go:49` (`Cut`), 19 non-test call sites, but it is under
`agent/internal` so `cmd/`, `appwire`, `internal/` cannot use it.

Evidence of drift: ellipsis is `"…"` in 11 places and `"..."` in 8; the cut keeps `limit` runes in
`truncRunes` but `limit-1` plus the ellipsis in `Truncate` and `truncateTitle`, so "the cap" means two
different things. Examples of the bug: `context_manager.go:2561` `return s[:limit] + "..."` on model-facing
history text; `cmd/evener/run.go:574` `args[:97] + "..."` on JSON that is routinely non-ASCII;
`app_search.go:263` `prompt[:maxLen-1]` on a user prompt title. A cut inside a rune emits invalid UTF-8 into
a prompt or a UI title.

Impact: real latent mojibake in model-facing text and UI strings; cap semantics inconsistent.

Fix plan: promote `activitybound.Truncate`'s algorithm and `runetrim.Cut` into one exported package reachable
by everything (`execsupport/runetrim` or `execsupport/text`): `Cut(s, maxBytes)`, `Truncate(s, maxRunes)`
(ellipsis counted inside the cap), `TrimPartial` helpers. Keep `activitybound` constants but delete its
`Truncate` copy and `jobs_activity.go`'s wrapper (call sites change from `truncateActivityText` to the
shared function). TDD for the byte-cutting sites: add a failing test per site with a multi-byte boundary
(for example a string of 70 `é` through the shell summary) before switching it. Decide once whether the
ellipsis is `…` and whether it counts toward the cap; note the choice in the package doc.
Size: -120 lines net, 25 call sites touched. Risk: medium (output text changes for the `...` sites;
tests that assert on those strings must be rewritten to assert length and validity, per the repo's
"copy is not a test oracle" rule).

---

## Finding 6: 13 "is path under directory" helpers with swapped argument orders

Locations: `agent/sandbox/gitdir.go:627` `pathUnder(p, dir)`, `agent/execenv/securepath.go:497` `pathUnder(p, dir)`
(comment: "kept local so execenv does not reach into an unexported helper"), `agent/sandbox/session_scratch.go:213`
`pathWithin(path, root)`, `agent/session_tools_worktree.go:1685` `pathWithin(path, dir)`,
`agent/execenv/local.go:3491` `pathWithinRoot(abs, root)`, `agent/execenv/project_resolver.go:302`
`pathContains(root, path, resolve)`, `cmd/evener-hub/internal/fspaths/paths.go:124` `withinRoot(root, target)`,
`internal/plugins/marketplaces.go:1449` `pathUnder(dir, path)` and `:1459` `pathWithinDir(dir, candidate)`,
`tools/tool-fluency/.../review.go:299` `pathWithin(dir, path) (bool, error)`, `cmd/evener-fuzzregistry/main.go:466`
`pathWithinDir(root, candidate)`, `agent/session_notes.go:705` `pathWithinNotesScope(abs, cwd)`,
`agent/sandbox/bwrap.go:405` `isUnderAnyRoot`. Plus 12 inline `HasPrefix(x, root+sep)` checks, for example
`agent/execenv/gitpath.go:150`, `securepath_browse.go:631`.

Evidence: two implementation families (`filepath.Rel` and `".."` prefix test vs `HasPrefix(root+sep)`) that
disagree on edge cases: the `HasPrefix` family is wrong for a root of `/` (`"//"` prefix) and for
non-cleaned inputs; `session_scratch.go:213` treats an empty root as "no" while the others treat it as `.`.
Argument order is `(path, dir)` in five and `(dir, path)` in five. In a security boundary (sandbox,
file tools), a swapped call compiles and silently inverts the answer.

Impact: high drift risk in the sandbox and file-tool containment code; every new caller picks one at random.

Fix plan: one `execsupport/pathutil.Within(root, path string) bool` (cleaned, `Rel`-based, `root == path`
allowed, empty root false) and `Under(root, path) (rel string, ok bool)` for callers that need the relative
remainder. Root-first ordering everywhere. Migrate sandbox and execenv first with a table test lifted from
the union of existing tests (root `/`, trailing slash, `..` sibling prefix like `/a/bc` vs `/a/b`, equal path,
empty root, Windows volume). The `HasPrefix` family gets a failing test for root `/` before replacement.
Size: -90 lines, about 35 call sites. Risk: medium (security-relevant; keep each existing test green and
add the edge table).

---

## Finding 7: symlink-resolution helpers: three byte-identical, five "deepest existing ancestor" variants

Identical `EvalSymlinks` with `Clean` fallback: `identifier/git.go:85` (`resolveClean`),
`agent/execenv/gitpath.go:275` (`resolveClean`, annotated "Compatibility wrapper retaining the old local
normalization seam"), `agent/session_tools_worktree.go:1816` (`canonicalOrClean`).

Deepest-existing-ancestor resolution, five shapes: `agent/sandbox/seatbelt.go:70` (`canonicalizeLongestPrefix`,
recursive, injectable `eval`), used via `agent/sandbox/resolve.go:1014`; `cmd/evener-hub/internal/hubtestenv/hubtestenv.go:211`
(`canonicalizeExisting`, iterative, fails closed on a dangling link); `agent/session_init.go:170`
(`resolveStatePathComponents`, component walk resolving `..` against the resolved parent);
`cmd/evener-hub/internal/hostops/store.go:354` (`canonicalStorePath`, component walk, refuses dangling links);
`agent/execenv` `resolveSymlinksBestEffort` (called at `local.go:3492`).
`EvalSymlinks` appears in 62 non-test files.

Evidence: the five variants differ exactly on the dangling-symlink case (kept, refused, or fail-closed) and on
`..` handling. Each comment explains why its local rule is right, but none says why it could not share the
walk with a policy flag.

Impact: for sandbox policy and store identity the answer must agree; today it depends on which helper a
caller used.

Fix plan: `execsupport/pathutil.ResolveClean(p)` replaces the three identical copies (delete the
"Compatibility wrapper" per the no-compat rule). Add `ResolveExisting(p, DanglingPolicy)` with
`Keep | Refuse` and migrate the four walkers in separate commits, each guarded by its existing tests plus a
dangling-link case. Keep the injectable `eval` seam only where a test needs a fake filesystem (seatbelt, which
runs the walk on Linux).
Size: -110 lines. Risk: medium (semantic differences must be preserved via the policy argument).

---

## Finding 8: tilde and home-directory handling written seven ways

Locations: `llm/media_utils.go:37` (`ExpandTilde`: only `~/`, trims whitespace, uses `os.UserHomeDir`),
three verbatim copies in the hub: `cmd/evener-hub/app_dirs.go:22`, `cmd/evener-hub/internal/fspaths/app_paths.go:35`
and `:156` (`strings.HasPrefix(p, "~/") || p == "~"` then `filepath.Join(envvars.Home.Getenv(), TrimPrefix(p, "~"))`),
`agent/sandbox/policy.go:264` (`resolveHomePath`: also joins bare relative to home), and the reverse direction:
`cmd/evener-hub/web_settings.go:34` (`tildeHome`, string-prefix with literal `"/"`) vs
`agent/project_docs.go:150` (`tildeCollapse`, `filepath.Rel`-based).

Evidence: hub copies accept bare `~` and read `$HOME` through `envvars.Home`; `llm.ExpandTilde` rejects bare `~`
and reads `os.UserHomeDir()`. `tildeHome` hardcodes `/` and would not match on Windows; `tildeCollapse` uses
`filepath.ToSlash`. Same intent, three different edge behaviors.

Impact: a path the UI accepts as `~` is rejected by the model-facing image loader; inconsistent display
in settings vs prompts.

Fix plan: `execsupport/pathutil.ExpandHome(p, home string)` and `CollapseHome(p, home string)` (home passed in so
callers keep their source: `envvars.Home` in the hub, `os.UserHomeDir` elsewhere). Replace the 7 sites. Test:
`~`, `~/x`, `~user/x` (unchanged), home itself, sibling prefix (`/home/jessie` vs `/home/jesse`).
Size: -45 lines. Risk: low.

---

## Finding 9: strict "decode one JSON value, reject unknown fields and trailing data" written 12+ times

Locations: `llm/apilog/codec.go:303`, `hubapi/navigation_delta.go:59`, `appwire/keybindings.go:158`,
`appwire/transcript_display.go:306`, `appwire/types.go:2366` (no trailing check, name `decodeStrictJSON`),
`agent/transcript/transcript.go:285` (also `decodeStrictJSON`), `agent/internal/delegatestore/store.go:328`,
`agent/session_client_mutation_persist.go:411` (`rejectTrailingClientMutationJSON`) plus `:67,:245`,
`agent/doctor/mutations.go:224`, `internal/hubendpoint/report.go:64`,
`cmd/evener-hub/internal/hubcore/state_file.go:108` (`decodeStateFileStrict`),
`cmd/evener-hub/internal/hubcore/recovery_store.go:77`, `cmd/evener-hub/internal/hostops/store.go:774,786,843,856,915`,
`custody.go:364`, `quarantine.go:285,767,827`. 33 `DisallowUnknownFields` call sites in non-test code.

Evidence: the core is the same 8 lines (`NewDecoder`, `DisallowUnknownFields`, `Decode(dst)`, `Decode(&extra)`,
compare to `io.EOF`). They differ in sentinel: `err != io.EOF` vs `!errors.Is(err, io.EOF)`; some return
`errors.New("multiple JSON values")`, others `trailing JSON value`, others wrap the trailing error. The
`appwire/types.go:2366` variant silently accepts trailing data while its siblings reject it.

Impact: two same-named `decodeStrictJSON` with different trailing-data behavior; a malformed file passes in one
reader and is quarantined by another.

Fix plan: `execsupport/jsonstrict.Decode(data []byte, dst any) error` returning a sentinel
`ErrTrailing` (use `errors.Is`) so callers wrap with their label. Replace the 12 verified sites; leave
`appwire/types.go:2366` on a documented `AllowTrailing` variant only if a test proves a caller needs it,
otherwise switch it to strict and add a test.
Size: -110 lines. Risk: low-medium (trailing-data acceptance in `appwire/types.go` must be checked per caller).

---

## Finding 10: JSON-value deep clone x9 and pointer clone x12

Deep clone of `any`/`map[string]any`: `llm/responses_continuation.go:294,305`, `llm/registry/load.go:630,641`,
`llm/providers/internal/openaichat/strict.go:73` (`deepCopyAny`), `agent/internal/jobstore/event_clone.go:55`
(`cloneJSONValue`), `agent/skill/metadata.go:305,316`, `agent/subagents.go:594` (`cloneMap` via JSON round-trip)
and `:609` (`cloneShallowMap` = `maps.Clone`), `agent/provider/profile_overrides.go:152` (`deepCopyJSONMap`, another
JSON round-trip), `fuzz/typegen/typegen.go:237` (`cloneSchema` = `maps.Clone`),
`llm/providers/anthropic/request.go:253` (`shallowCopyMap` = `maps.Clone`), `appwire/clone.go:176`.
Pointer clone: generic `clonePointer[T]` in three packages (`llm/registry/load.go:622`, `appwire/clone.go:125`,
`cmd/evener-hub/navigation_projection.go:1887`), generic `clonePtr[T]` (`agent/internal/jobstore/event_clone.go:32`),
and eight monomorphic copies with byte-identical bodies: `cloneServerBool` (`server/appwire_runtime.go:2858`),
`cloneBool` (`agent/status.go:569`), `cloneServeBool` (`cmd/evener/serve.go:2347`), `cloneHubBool`
(`cmd/evener-hub/app_threadread.go:1309`), `cloneBoolPointer` (`internal/appprojector/appwire_projection.go:573`),
`cloneAPILogInt` (`llm/api_attempt.go:643`), `copyBoolPtr` and `copyIntPtr` (`cmd/evener-hub/internal/launchconfig/wire.go:129,158`).

Evidence: the five recursive JSON clones all handle `map[string]any` and `[]any`; they differ in which extra
cases they support (`[]string`, `map[string]string`, `map[any]any`, `json.RawMessage`), so a value cloned by
one is aliased by another. Three of the "clones" are `maps.Clone` with a longer name. Go's `slices.Clone` and
`maps.Clone` plus one generic `ptr.Clone` cover the pointer family.

Impact: aliasing bugs when a nested type is added; 8 identical functions.

Fix plan: (a) one generic `Clone[T any](*T) *T` in a shared leaf (`execsupport/ptr`), delete the 12 copies;
(b) one `jsonclone.Value(any) any` that handles the union of cases, replacing the five recursive clones
(keep `appwire.cloneCodexErrorInfo`'s `json.RawMessage` case in the union); (c) delete the three trivial
`maps.Clone` wrappers. Tests: one table test for the union; existing clone tests continue to run.
Size: -170 lines. Risk: low.

---

## Finding 11: collection helpers that duplicate each other or the standard library

- `appendUnique`-style (all `slices.Contains` then `append`): `llm/providers/responses/request.go:212`,
  `agent/sandbox/gitdir.go:599`, `agent/doctor/audit.go:1192` (`appendUniqueString`),
  `cmd/evener-hub/internal/hubcore/favorite_authority.go:321`. Variants that also drop empties:
  `agent/subagents.go:363`, `llm/providers/internal/requestutil/gateway.go:200` (`DedupStrings`),
  `cmd/evener-hub/web_api_tree.go:1388` (`uniqueStrings`), `agent/sandbox/resolve.go:994`.
- `hasString` (`agent/subagents.go:359`) is a one-line wrapper of `slices.Contains`, 12 production call sites;
  `fuzz/schemagen/schema.go:134` `contains` the same.
- Sorted-keys: 11 near-identical "collect keys, `sort.Strings`" functions
  (`llm/headers.go:10`, `agent/session_stream.go:37`, `agent/doctor/audit.go:651`, `agent/doctor/health.go:359`,
  `fuzz/schemagen/schema.go:125`, `agent/jobs_activity.go:792,950`, `cmd/evener-hub/app_host_manage.go:3847`,
  `hubcore/tree.go:1895`) and three generic `sortedKeys[V]` (`llm/registry/load.go:392`,
  `llm/registry/internal/snapshotreport/main.go:103`, `app_host_manage.go:2984` named `sortedMapKeys`).
  `llm/providers/chatcompletions/rescue.go:328` already shows the one-liner: `slices.Sorted(maps.Keys(m))`.
  Repo-wide: 168 `sort.Strings` calls vs 26 `slices.Sorted(maps.Keys(...))`.
- `agent/internal/tool/apply_patch.go:599,606` `minInt` / `maxInt` shadow the builtins `min` / `max` (9 uses).

Fix plan: delete all of the above in favor of `slices.Contains`, `slices.Sorted(maps.Keys(m))`, `slices.Compact`
and builtins. Where empties are dropped, use a single `execsupport/text.UniqueNonEmpty`. No new abstraction
for the sort helpers. `modernize` is already enabled; check whether it can flag the loop form and, if so,
turn the rule on rather than adding a custom check.
Size: -120 lines, mechanical. Risk: low (each replacement is behavior-identical; `DedupStrings` and
`uniqueStrings` keep their empty-dropping).

---

## Finding 12: pass-through wrappers and "compatibility" shims

- `agent/execenv/gitpath.go:247,255,267,275`: three wrappers of `identifier.*` and `resolveClean`, each with the
  comment "Compatibility wrapper around the shared identifier helper". Each has 1 production caller.
- `pointerTarget` is byte-identical in `identifier/git.go:123` and `agent/execenv/project_resolver.go:206`
  (the second calls `identifier.ParseGitdirPointer` and repeats the join/clean).
- `refFor` x2: `agent/transcript_lookup.go:381` and `agent/doctor/locate.go:197`, both delegate to
  `bucketref.RefFor` (36 + 44 call sites).
- `openAIStateDirFromEnv` (`cmd/evener-hub/app_auth.go:1036`) calls `openAIStateDirFromEnvMap`
  (`openai_state_dir.go:21`) which calls `openAIStateDirFromLookup` (`:32`) which calls
  `cmdutil.StateRootFromLookup`: a four-deep chain with one real function at the bottom.
- `cmd/evener-hub/app_compact.go:12-18`: `shouldResumeAfterTurnStartError` -> `shouldResumeAfterSessionUnavailable`
  -> `isSessionUnavailableError`, two layers of rename.
- `cmd/evener-hub/host_routing_origin.go:36,42` wrap `appsource.WithHostRoutingOrigin` / `HostRoutingOrigin`;
  `roster.go:218,222` and `local_daemon.go:1344,1348` wrap `appwire.CloneEvenerJobs` / `Watches`;
  `web_api_tree.go:29` `projectKeyForStateDir` = `filepath.Base`; `hubTOMLSyncUnsupported` and
  `deletionSyncUnsupported` (finding 2).
- `agent/jobs_activity.go:109` `truncateActivityText`, `agent/subagents.go:359` `hasString` (findings 5, 11).
- A scripted scan (single-statement bodies whose arguments are forwarded unchanged) found 52 such unexported
  wrappers repo-wide; roughly half are legitimate test seams (`os.Open` wrappers split by build tag).

Impact: indirection that carries no meaning; the "Compatibility wrapper" comments contradict the repo rule of
no backward compatibility without approval.

Fix plan: inline each wrapper at its caller and delete it, one package per commit; update tests that call
the wrapper name. Leave build-tag seams alone. Keep `refFor` only if the two copies can share a single
agent-level name, otherwise call `bucketref.RefFor` directly. The `identifier` helpers become the only home
for gitdir-pointer parsing.
Size: -150 lines. Risk: low.

---

## Finding 13: `firstLocalNonEmpty` drifted from the canonical `envvars.FirstNonEmpty`

Locations: `envvars/firstnonempty.go:11` (trims whitespace; 58 call sites; doc says "the canonical implementation
shared across all evener modules"), `cmd/evener-hub/internal/appsource/local_daemon.go:1464`
(`firstLocalNonEmpty`, exact `!= ""` check, 22 call sites in one file). `llm/providers/internal/requestutil/numbers.go:21`
`FirstPositiveInt` is the integer analogue.

Evidence: `firstLocalNonEmpty(" ", id)` returns `" "`; `FirstNonEmpty(" ", id)` returns `id`. The Go standard
library `cmp.Or(a, b, c)` already does the exact-empty form (13 other uses in the tree).

Fix plan: replace the 22 calls with `cmp.Or` if exact semantics are intended, or `envvars.FirstNonEmpty` if
whitespace-only should count as empty (decide with a test on thread names that are whitespace). Delete the
function. Size: -10 lines. Risk: low.

---

## Finding 14: first-line, one-line and whitespace-collapse text helpers

- First line: `agent/transcript_util.go:10` (`firstLineClamp`), `agent/internal/hooks/hooks.go:558`,
  `agent/internal/tool/breaker.go:545` (`firstNonBlankLine`), `cmd/evener-hub/internal/sshconn/version.go:1642`,
  `agent/session_queue.go:677` (`firstQueueLine`), `execsupport/valueexpr/evaluate.go:345`.
- `oneLine`: `agent/transcript_render.go:1976` and `agent/doctor/transcript.go:466` (comment "Mirrors
  doctor.oneLine"), plus a third inline copy in `internal/apptranscript/apptranscript.go:413`
  ("the mirror lives locally; keep it in step with the hub's helper").
- `collapseWhitespace`: `agent/memory_page.go:219` (`strings.Fields` join, trims) and
  `agent/internal/tool/breaker.go:554` (rune loop, does NOT trim): same name, different results. The
  `strings.Join(strings.Fields(x), " ")` idiom is also inline at 8 more sites
  (`session_notes.go:229,254`, `session_namer.go:290`, `prompt_data.go:201`, `execenv/local.go:1936`,
  `apply_patch.go:615`, `session_tools_find.go:926`).
- Non-empty lines: `agent/transcript_render.go:1723` (`nonEmptyLines`, trims to test),
  `agent/internal/worktree/predicates.go:47` (`splitNonEmptyLines`, exact test), `agent/execenv/local.go:1460`.

Fix plan: put `FirstLine`, `OneLine`, `Collapse` into the shared text package from finding 5; delete the
two "mirrors" and replace the comments that tell people to keep copies in sync. Rename
`breaker.collapseWhitespace` to say it does not trim, or switch it to the shared one if the fingerprint
test still passes.
Size: -60 lines. Risk: low.

---

## Finding 15: process-liveness checks, three variants of the same signal-0 probe

Locations: `cmd/evener-hub/internal/hubcore/roster_unix.go:19` (`processAlive`: `err == nil || errors.Is(err, EPERM)`;
non-unix returns true), `internal/devtool/scratch/pid_unix.go:12` (`pidAlive`: `err == syscall.EPERM`, non-unix true),
`cmd/evener-dev/webgate_unix.go:8` (`groupAlive`: `syscall.Kill(-pgid, 0) == nil`, EPERM counts as dead, non-unix false).

Evidence: two have identical logic with `==` vs `errors.Is`; the third treats EPERM the opposite way. The
`pid_unix.go` comment explicitly discusses that EPERM must count as alive; `groupAlive` repeats the
question and answers it the other way (arguably fine for a group the dev tool itself created, but unstated).

Fix plan: `execsupport/procgroup.Alive(pid int) bool` and `GroupAlive(pgid int) bool` with the EPERM rule documented
once; the three callers switch. Non-unix platforms keep their documented fallback inside the shared package.
Size: -35 lines. Risk: low.

---

## Finding 16: environment-slice (`[]string{"K=V"}`) helpers reinvented

Locations: `cmd/evener-hub/spawn.go:1072` (`envLookup`, last wins), `:1082` (`envToMap`),
`agent/sandbox/env_floor.go:335` (`envVars`, same as `envToMap` but keeps entries without `=`),
`agent/sandbox/probe.go:384` (`envValueFold`, case-insensitive, last wins), `agent/internal/mcp/manager.go:827`
(`mergeEnvInto`), `cmd/evener-hub/internal/launchconfig/env.go:75` (`setEnv`, replaces in place),
and the overlay-extra-over-inherited loop written three times inside `agent/execenv/local.go:3546-3585`
(`filteredEnvWithSource` for `EnvPolicyAll`, `EnvPolicyCoreOnly`, and `filteredEnvFrom`) plus 8 other
`strings.Cut(kv, "=")` loops (`agent/sandbox/secret_env.go:26`, `env_floor.go:62,474,551,594`, `local.go:3551,3576,3607`,
`cmd/evener-dev/webgate.go:431`).

Impact: duplicate-key semantics (first vs last wins, replace vs append) differ, which matters when an
overlay is supposed to override inherited values. `mergeEnvInto` and the `EnvPolicyAll` branch of
`filteredEnvWithSource` are the same operation.

Fix plan: `execsupport/envlist` with `Lookup`, `Map`, `Overlay(base, extra)`, `Set`. Migrate `mergeEnvInto` and the
three overlay loops first (they carry the behavior risk), then the readers. Keep sandbox-specific scrubbing where it is.
Size: -80 lines. Risk: medium (duplicate-key semantics must be pinned by tests before switching).

---

## Finding 17: random hex/ID generation inlined nine times, with different failure policies

Locations: `agent/session_escalation.go:338`, `agent/session_queue.go:454`, `agent/internal/artifactstore/store.go:51`,
`cmd/evener-hub/navigation_service.go:280`, `cmd/evener-hub/main.go:92` (falls back to `pid-%d-%d`),
`cmd/evener-hub/app_host_running.go:301` (`probeNonce`), `cmd/evener-hub/internal/hubcore/past.go:165`
(falls back to `UnixNano`), `internal/transcriptindex/index.go:1397` (`randomName`), `cmd/evener-hub/internal/hostops/token.go:715`
(base64url, deliberately different), `app_instances.go:703,712`.

Evidence: eight are `rand.Read(buf)` followed by `hex.EncodeToString`; two swallow the error
(`_, _ = rand.Read`), three return it, two fall back to a time-based value. `crypto/rand.Read` does not return
an error on current Go, so the fallbacks and error paths are dead code.

Fix plan: `execsupport/randid.Hex(nBytes int) string` (panic-free, no error). Replace the hex sites; leave the token
and key generators. Drop the unreachable error branches. Size: -45 lines. Risk: low.

---

## Finding 18: bearer-token parsing in two places with opposite empty-token rules

Locations: `internal/httpguard/httpguard.go:70` (`HubTokenAuthorized`: empty expected token authorizes every
request; used by `server/server.go:586`) and `cmd/evener-hub/internal/hubedge/auth_token.go:121,132`
(`tokenFromRequest` / `tokensEqual`: empty presented or expected returns false; `AuthGuard` at `:164` separately
treats an empty token as "guard disabled").

Evidence: both parse `Authorization: Bearer <x>` with `TrimSpace(TrimPrefix(...))` and call
`subtle.ConstantTimeCompare`. `HubTokenAuthorized("", "")` is true, `tokensEqual("", "")` is false. Two more
`ConstantTimeCompare` calls exist in `auth/openai/manual.go:47` and `hostops/token.go:479` for non-bearer uses.

Impact: easy to apply the wrong convention when adding a third guard; the "empty means open" rule is
a testing escape hatch in one and a rule in the other.

Fix plan: `httpguard.BearerToken(header string) string` and `httpguard.TokenEqual(a, b string) bool` (empty is never
equal); `HubTokenAuthorized` and `AuthGuard` both use them and keep their own explicit "no token configured" branch.
Size: -15 lines. Risk: low. Security-adjacent, so keep both packages' existing auth tests.

---

## Finding 19: small filesystem predicates and mixed not-exist idioms

Locations: `fileExists` in `llm/registry/instances.go:70`, `agent/doctor/locate.go:226`, `cmd/evener-fuzz-harvest/emit.go:102`;
`isDir` in `agent/doctor/locate.go:221`, `cmd/evener-fuzz-harvest/main.go:183`; `directoryExists`
(`agent/session_tools_worktree.go:859`), `pathExists` (`agent/sandbox/bwrap.go:496`),
`worktreeGitEntryExists` (`agent/session_worktree_resume.go:237`), `isExecutable` (`internal/binresolve/sibling.go:99`).
Idiom mix for "file is absent": 106 `os.IsNotExist(err)` vs 159 `errors.Is(err, fs.ErrNotExist)`
across non-test code; `os.IsNotExist` does not unwrap `%w`-wrapped errors, so it is wrong after any
wrapping helper, and `errorlint` does not flag it.

Fix plan: standardize on `errors.Is(err, fs.ErrNotExist)` (mechanical rewrite of the 106, review each for
wrapped sources first), and collapse `fileExists`/`isDir` into `pathutil.IsFile` / `IsDir`. If lint can enforce
it, add a `forbidigo` rule for `os.IsNotExist` after the sweep (consequential: ask Jesse).
Size: net -30 lines plus a 106-site mechanical edit. Risk: low.

---

## Finding 20: human-readable size, age and "plural" formatting duplicated server side

Locations: `agent/job_output_digest.go:40` (`humanBytes`, 1024 base, one decimal), `cmd/evener-hub/web_settings.go:70`
(`fileSizeHuman`, integer KB/MB/GB), `cmd/evener-hub/web_settings.go:50` (`fileAgeHuman`, "created 3d ago"),
`cmd/evener-hub/internal/hubcore/tree.go:482` (`AgeString`, "3d"), `agent/failure_steering.go:343` (`humanElapsed`),
`agent/failure_steering.go:350` (`pluralizedUnit`), `agent/doctor/mutations.go:322` (`plural`).

Evidence: `appwire/types.go:4703,4728` ships the preformatted `BearerTokenAge` and index `Size` strings on
the wire ("Source: web_settings.go fileAgeHuman ..."). The product guide says the shared client owns shared
derivations; here the server formats display copy that every client must show verbatim.

Fix plan: send `modifiedAt` and `bytes` over the wire and let the shared TS client format (separate change
touching appwire, hub, TS client and web UI; discuss with Jesse because it alters the wire contract). Short
term, keep `humanBytes` only where the text is model-facing (`job_output_digest.go`), merge `plural` and
`pluralizedUnit`. Size: -50 Go lines, plus TS work. Risk: medium (wire change).

---

## Findings considered and dropped
- `plugins.copyTree` vs `copyTreeExact` (`internal/plugins/source.go:304`, `publication.go:325`): different, documented
  rules (Git-metadata skip and symlink refusal vs exact mode/symlink copy). Not duplicates.
- `internal/devtool/procgroup` vs `execsupport/procgroup`: documented layering (`Start`, `Stop`, `ExitCode` are new);
  only `Terminate` and `Kill` are one-line forwards. Too small to fix on its own.
- Repeated `hex.EncodeToString(sum[:])` after `sha256.Sum256` (39 sites): one-liners, no drift found.

## Recommended fix sequence

1. Create `execsupport` shared packages with no behavior change yet: `fsatomic` (findings 1, 2), `backoff`
   (3, 4), `pathutil` (6, 7, 8, 15), `jsonstrict` (9), `text` (5, 14). Land each as its own commit with its table tests,
   before any consumer moves. Update `execsupport/README.md` and `docs/architecture.md` in the same change.
2. Finding 6 (path containment) and finding 5 (byte-cutting truncations) first among the migrations: they hold
   the real correctness risk. Write the failing edge tests (root `/`, multi-byte boundary) before switching.
3. Finding 1 then 2: migrate the 11 fixed-name `.tmp` writers first, then the two `app_host_manage.go` copies,
   then the rest; delete `fsdurability` last. One package per commit.
4. Findings 3, 4, 8, 9, 15, 17, 18: independent small PRs, any order.
5. Findings 10, 11, 12, 13, 14, 19: mechanical deletions after the shared homes exist; batch by package to keep
   diffs reviewable.
6. Finding 16 (env slices) after 1 to 5, behind pinned duplicate-key tests. Finding 20 only after Jesse decides
   on the wire change.
7. Ask Jesse once, at the end, whether to add the optional guard rails (an audit test or `forbidigo` rules) so
   the private copies do not return.
