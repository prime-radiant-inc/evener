# Decisions on the cleanup roadmap

Jesse's answers to the questions in [ROADMAP.md](ROADMAP.md) ("Decisions that are Jesse's"), all dated 2026-10-25. Each entry gives the answer and its effect on the roadmap. Where a decision changes a roadmap entry, the entry has been edited to match.

## Q1. Version skew and state migration

Jesse's words: "we don't want to fuck over users who dont upgrade all at once or frequently. but the hub should always be as new as or newer than the daemons." On the two boundaries where versions can skew either way: "phone and hub tolerate remote hub being older."

Rules that follow:

| Boundary | Must tolerate | May drop |
|---|---|---|
| Hub and its daemons | Hub works with any older daemon | Daemon-side fallbacks for an older hub (the hub is always the same version or newer) |
| Phone and hub | Phone works with an older hub | Hub-side fallbacks for an older phone app |
| Local hub and remote hub over SSH | Local hub works with an older remote hub | Remote-side fallbacks for a newer local hub |
| On-disk state | Migrations stay, because users skip versions | Nothing on a time window alone |

How it applies to the gated compat entries:
- The "older hub" fallbacks (W1-59, x-compat-legacy-1): sort each by direction. A fallback in phone code or local-hub code for an older hub stays. A fallback in daemon code for an older hub goes.
- State migrations stay. This covers plugin-store migrations (W1-61), the serf-era and MEMORY.md migrations (W1-62), the legacy transcript projection and checkpoint formats (W1-63), and persisted client-state eras (W1-64). The allowed work there is deduplication and simplification that keeps every migration path working.
- Direction decides every compat entry. Keep the phone and local-hub fallbacks for an older (remote) hub, including the combined update/apply path. Delete only the directions in the "May drop" column: code that only serves a newer peer, an older phone app, or a daemon facing an older hub.
- Compat code with no production producer at all (dead in every direction) is T1 dead code, not compat, and can be deleted.

## Q2. Coverage floor

Drop it as a protected gate. Prune the coverage-wave and hub `cov_*` files after one before/after coverage report from CI, and keep the real behavior tests. Remove the matching rows in `scripts/fuzz/fuzz-targets.txt`, `scripts/coverage/fuzzcov-ignore.txt`, `scripts/coverage/coverage-floors.txt` and `testing-budget.json` in the same PR as the files. Unblocks W1-54.

## Q4. Shared durable writer

Home it in a new package in the `execsupport` module (`auth` takes the dependency). It fsyncs the file and its directory by default. Rebuildable caches use a variant named for skipping sync. Measure hot callers before switching them (see GAPS.md section 3, item 1). Unblocks W1-01..W1-03.

## Q10(a). Design mockup harnesses

They are final. Delete them (W0-05).

## Q12. AGENTS.md contributor rules

Approved with the roadmap's five-rule wording, plus the typo and path fixes (W0-01).

## Q17. Slash-command ambiguity

When two plugins define the same bare command, report it as ambiguous with a sentinel error, as skills already do, instead of picking one at random (W0-18).

## Q3. `llm` is a public SDK

Keep the `StreamGenerate` family and the multi-round tool loop. W1-27 becomes dedupe only, with no deletion of either. Any other entry that proposed deleting them follows the same rule.

## Q5. HOME-less state root

The shared state-root resolver returns a clear error when `HOME` and `XDG_STATE_HOME` are both unset. No `os.TempDir()` fallback and no `"."` fallback. Callers surface the error. Applies to W1-04 (the last commit, which removes the per-caller fallbacks).

## Q6. One stuck rule

One stuck rule, shared from `appwire-client`: 10 minutes, and a session is never "stuck" while its delegates are actively running. Web's short quiet hint (`describeLiveness` 20s) may stay as a separate, softer state that is not called "stuck". Unblocks W1-39.

## Q7. Attention precedence

In the shared attention rule, restart-needed or ended wins over `approval_pending`. Unblocks W1-38 phase 2.

## Q8. `FollowUp`

Delete `Session.FollowUp`, `popFollowUp`, the `runFollowUp` drain rung and its retirement check, and repoint the tests at real input paths. Consolidating the three queue representations stays a design discussion and does not start in W1-67. Unblocks W1-67.

## Q9. Experimental context strategies

Delete all four: `obs-mask`, `checkpoint-pred`, `memory-crystals`, `recursive-distill`. Unblocks W1-66.

## Q10(b-d). UI dead weight

Delete the four unused widgets (W1-47). Delete the write-only phone `ActivityStore` after the rehydrate-trigger check (W1-43). Keep the ~10k-line demo hub fixture (`native-tests-legacy-mobile-13`); no entry deletes it.

## Q11. Gate tooling

Delete none of it. The timing ratchet, the prose and Makefile audit tests, the fuzz mutation toolkit, `maketargetsdoc` and `gatesurface` all stay. Fix what is broken: rebaseline the timing ratchet so the per-package ratios enforce again. That needs a `metric` key in `testing-budget.json`, baselined from a CI-class measurement, not from this workstation. Any entry that deletes these tools (W1-56 and others) becomes keep-and-fix.

## Q13. Test-support packages (orchestrator default, not Jesse's call)

Adopt the existing test kits first (W1-51). Create `llm/llmtest` or `gittest` only if more than ten copies remain afterwards. Jesse can overrule.

## Q14. Remote image-by-path

Remote image-by-path falls back to the artifacts root like local doc serving does, through one shared resolver for the doc and image routes. New roadmap entry W1-68 in the hub lane.

## Q15. Recovery over lockdown

All three switch to recovery (W1-65):
- (a) A plugin with a malformed command file loads its healthy commands, skips the bad file, and emits a diagnostic naming it.
- (b) Legacy delegate state is archived or ignored with a diagnostic, and startup proceeds instead of refusing.
- (c) When the endpoint-fingerprint key cannot be computed, discard the pending login or pairing flow and automatically restart a fresh flow against the current endpoint. Never complete unchecked. The three verifiers are also deduplicated.

## Q16. Ref grammar

Reject `:` in session ids. Keep one strict `ParseRef` (appwire's grammar) and have the other callers use it (W1-36).

## Q18. `explain.go` coaching text

Leave the coaching text as is. W2-09 only removes narration comments and dead code; the example-synthesis family stays.

## Q19. Plan and spec corpus

Delete plans whose PR merged and that nobody has touched in 30 days. Keep specs. Going forward a plan is deleted in the PR that completes it (W1-58).

## G7. `afero`

Phase it out. New code and the shared durable writer use the real `os` package, and tests use `t.TempDir()`. Callers migrate as their files are touched, and `afero` leaves `go.mod` after the last one. Affects W1-01 (no FS interface needed) and W1-02/W1-03 (migrating callers drop `afero` as they move).

## Reviewer corrections (roborev), applied to the roadmap

- Migration entries (W1-40(c), W1-62, W1-63, W1-64 persisted-state parts) allow only simplification that keeps every migration path and its behavior coverage: the serf-era migration, the MEMORY.md migration, the legacy transcript projection and checkpoint formats, persisted client-state eras.
- W1-64 and W1-59 classify each fallback by version direction, as in Q1.
- GAPS.md G3 names the real producers of the fuzz corpus.

## Q12, second tier. Error strings in the copy-oracle ratchet

Yes. The ratchet also counts tests that assert on error-message text, with an allowlist of today's sites so the count only goes down. W1-50 burns the allowlist down area by area. Unblocks W1-49.

## Merge rule for Wave 1

Final merges are serialized through a shared lock. The lock holder merges `origin/main`, pushes, waits for green CI on that head, then admin squash merges. No admin merge of a branch that is behind main.

## Q20. `hub.toml` as machine database, and the host retention knobs

- End state: `hub.toml` holds only `[[hosts]]` and scalar operator config. Every machine-managed record (`host_records`, `generations`, `mutation_receipts`, `tombstones`, staged and pruned receipts, teardown remnants and attempts) moves into the `hostops` store. `pending_store_sync` and `mirror_commits` and the two-file commit protocol go away. Existing files that carry these sections are migrated once on startup, and per Q1 that migration stays. A design record comes first, then several PRs with recovery tests (W2-13).
- Retention knobs: nobody tunes the undocumented ones. Keep `host_probe_timeout`, `host_min_free_space_bytes` and `host_tombstone_retention`. Fold the rest into one internal defaults value that the pruning code reads. A `hub.toml` that still sets a removed key starts normally with a warning naming the key (recovery, per AGENTS.md).

## `preDispatchRefusalError` not-accepted branch (hub-app-rest-5)

Delete it, along with its stated invariant, its wrap sites and the parity test that reaches it only with a synthetic error. Nothing produces that error today. Routed to W1-32.

## Wave 2 (2026-10-26)

Design records for every Wave 2 entry are in [designs/](designs/), one file per entry.

Jesse's rules for Wave 2:
- Autonomy: the orchestrator does the design and implementation, behavior changes included. It follows the roadmap, this file and the product docs, and asks Jesse only about true product calls.
- `agent` is internal. Move and rename exported symbols freely, update every caller, and add no forwarding aliases. `llm` stays public (Q3).
- `@evener/appwire-client` is consumed only in this repo. Its exports can change when the web and native apps change in the same PR.
- W2-18: fold `mobile/src` into `mobile-native/src`.
- W2-13: when the `hostops` store cannot open, keep serving the `[[hosts]]` in `hub.toml` so healthy hosts keep working. Quarantine the bad store, rebuild what can be rebuilt, and refuse only host add, remove and edit until it reopens. Retry automatically.
- W2-08: keep the ripgrep arm. Replace the gitignore cost estimator with one simple, very high cap. A hostile `.gitignore` is not a real threat model.
- W2-17: the Live next/previous shortcut follows the needs-you rule, so a dialog in another window does not block it.

Orchestrator defaults for the other questions in the design records (Jesse can overrule any of them):
- Each record's stated recommendation applies, unless listed below.
- W2-13: warn about a removed `host_*` key on every boot while it is present. Keep a one-time copy of the original `hub.toml` before the migration rewrites it. A host mutation writes the store intent first, then `hub.toml`. In `WebConfig`, delete only the knob fields.
- W2-12: the local resolver returns Invalid for an undecodable `thread/read` body, matching the hub. `urls/remove` becomes retry-safe if it is idempotent, proved red-first.
- W2-18: `ConversationScreenView` becomes one screen composed from hooks, with no navigation change. The two `confirmShutDown` prompts become one with an optional title.
- W2-17: the palette help lists rebound chords for the app-global rows. `ResourceState.normalized` becomes required once the compat path is gone.
- W2-10: no split of the hub `main` package for now. Revisit after W2-13. `serve --help` may take the unified wording.
- W2-03: `agent/memorypages` is a new package, with no alias for the old name; `tools/prompt-eval/memory-lab` is updated in the same PR. Skip the envsnapshot extraction.
- W2-14: internal revision and lifecycle helpers leave the public subpaths.
- W2-16: split into independent PRs (Spawn, transcript scroll, small cleanups). Defer ProviderConnection and Composer.
- W2-19: test-file splits go first as pure moves. Split the named files plus the agent test files over 2,000 lines. The testing guide gets a soft 2,500-line cap, with no lint gate.
- W2-05: the quiet-watch registry becomes a field on the owner Session, matching today's per-session behavior.
- Behavior changes these records surfaced but don't need get filed separately and are not done here: `job_watch clear` on descendant watches (W2-02), checkpointed carry for the session-log strategy (W2-04), and the lenient shell/model_list clamps (W2-06).

## G1. `tools/prompt-eval/memory-lab`

It was a one-off experiment. Delete it from the tree (history keeps it), about -9,000 lines. W2-03 then has no memory-lab caller to update.

## Wave 3

The engineering items in [GAPS.md](GAPS.md) run after Wave 2, with the same autonomy: fuzz corpus (G3), the committed bundle (G4), npm version skew (G5), `go.mod` (G6), logging (M1), concurrency and cancellation hygiene (M2), config sprawl (M3), dependencies (M4), and the test-orchestration overlap (M5).

## Wave 1 follow-ups (2026-10-27)

Jesse's answers to six open Wave 1 questions. Each applies to the named roadmap entry.

### W1-60. Host store sidecar
Delete the legacy `hub.hosts.json` sidecar migration and its write-poisoning. No tagged release ever wrote that sidecar. A corrupt main store still refuses writes.

### W1-63. Transcript display settings
Drop the dual write of transcript display settings to the legacy prefs keys.

### W1-41b. Scoped host read
Rewrite the debounced scoped host read behind `usePluginPreview` and `useScopedHostRead` as a framework-free store in the `appwire-client` package, with a thin hook in each app. The package stays React-free.

### W1-58. Plan corpus
Delete all of the roughly 225 superpowers plans older than 30 days that were kept.

### W1-02. Hub store writers
Keep afero in the hub stores (`hubcore` `writeStateFileAtomic` and its stores, `hostops` `saveFS` and `writeQuarantineFile`) along with their fault-injection tests. W1-02 is closed as done.

### W1-37. Thought duration
`formatThoughtDuration` switches to the shared `formatDurationMs`. Visible changes: 0 ms shows `1ms` instead of `0ms`, and 9,999 ms shows `10s` instead of `10.0s`.

## Still open

Nothing.
