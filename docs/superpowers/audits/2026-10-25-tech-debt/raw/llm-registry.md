# Tech-debt audit: llm-registry (llm/registry/**)

Audited 2026-10-25 on the `debt-audit` worktree (origin/main). Read-only; no tests run.

## Overview and inventory

`llm/registry` is the data-driven provider registry: models.dev snapshot + curated overlay + user `providers.toml` merged into a `Resolved` record per `instance/model`. It is part of S15/S16 in `docs/product/subsystems.md`; the evergreen reference is `docs/llm-providers.md` and `llm/README.md`.

| Item | Number |
| --- | --- |
| Non-test Go | ~7,400 lines in 17 files (+ 2 small `internal/` commands, 110 + ~60 lines) |
| Largest files | `resolve.go` 1709, `instances.go` 1304, `load.go` 1263, `schema.go` 497, `modelsdev.go` 395 |
| Overlay data | `data/providers_overlay.toml` 653 lines, 67 `[providers.*]` tables |
| Embedded snapshot | `data/models.dev.json.gz` (binary) + `testdata/models.dev.sample.json` 74,936 lines |
| Test Go | 10,507 lines in 27 files; `instances_test.go` 2172, `resolve_test.go` 1609, `load_test.go` 1058 |
| Comment density | `resolve.go` 480/1709 (28%), `instances.go` 476/1304 (36%), `load.go` 179/1263 |
| `§` spec citations in non-test code | 178, all into the dated `docs/superpowers/specs/2026-08-28-provider-registry-design.md` |
| Longest functions | `resolveLayersModeLive` 242 lines, `AuthFingerprint` 173, `derive` 145, `credentialWithAuth` 135, `Load` 127 |
| Public `Resolve*` entry points | 11 (`Resolve`, `ResolveFallback`, `ResolveLiveCandidate`, `ResolveInstance`, `ResolveInstancePresence`, `ResolveInstanceListing`, `ResolveInstanceTransport`, `ResolveInstanceModelFacts`, `ResolveGateCredential`, `ResolveGateCredentialCandidate`, `ResolveCatalog`) |
| Commits touching the dir | 119 total, 67 since 2026-09-01 |

Overall: behaviorally careful and heavily tested, but the code has accreted. The cost shows up as (a) three or four hand-copied replays/predicates that comments explicitly say "must not drift", (b) one 240-line resolver driven by three flags, (c) every capability/transport field enumerated by hand in 5-7 places, and (d) credential diagnosis carried as English strings that another module parses by prefix.

---

## Findings

### llm-registry-1 (high) resolveLayersModeLive is a 242-line flag-driven god function; 11 public Resolve entry points sit on top of it

Locations: `llm/registry/resolve.go:746-987` (`resolveLayersModeLive`), `:735` (`resolveLayersMode`), `:691-720` (`resolveOn`, `resolveOnMode`), `:275-318` (`Resolve*`), `:348-446` (`resolveInstanceMode`), `:561-634` (`ResolveInstance{Listing,Transport,ModelFacts}`), `instances.go:713-778` (gate pair), `:793-838` (`resolveDefaultRow`, `listingTransport`, `listingProtocol`).

Evidence:
- Signature is `(rec, ref, warnings, depth resolveDepth, includeLive, allowCodexCandidate bool)`. Two wrappers exist only to fix flags: `resolveLayersMode` (`true,false`), `resolveOn` (`true,false`).
- The body does alias seeding (lines 766-845), layer replay with hand-interleaved snapshot/overlay/live/config extras (850-905), disabled-lockstep fixup, transport build, early return for `resolveTransport` depth, web-search gating, credentials, derive, and 5 distinct warning appends, in one function with 12 local state variables (`aliasLockstep`, `aliasDisabled`, `aliasDefault`, `aliasTargetProtocol`, `aliasControlsRecorded`, `canonicalRowID`, `seenTag`, `liveApplied`, ...).
- `allowCodexCandidate` and `ErrCodexModelNotListed` (`:739`, `:747`) put an OpenAI-Codex-specific allowlist inside the generic resolver.
- Doc comments are mis-attached: the `resolveLayers replays one reference...` paragraph (`:713-721`) now sits on top of `resolveDepth`, and `resolveAliasTarget`'s doc (`:995-1002`) is glued to `aliasTargetRow`. `resolveOn` has none.
- Callers pick among near-identical entry points: `llm/client.go:496` uses `ResolveLiveCandidate`, `:601` `ResolveFallback`; `cmd/evener-hub/spawn.go:885-887` retries `ResolveGateCredential` with `...Candidate` on `ErrCodexModelNotListed`.

Impact: every new resolve behavior adds a flag or a wrapper; the function cannot be unit-tested by stage; reviewers cannot hold it in their head (it is the file where roborev finding churn clusters: 67 commits in 8 weeks).

Fix plan (behavior-preserving, existing resolve/golden tests are the guard):
1. Introduce `type resolveOpts struct{ depth resolveDepth; includeLive, codexCandidate bool }`; replace the 3 positional params and delete `resolveLayersMode`, `resolveOn`, `resolveOnMode` wrappers (keep one `resolveRow(rec, ref, warnings, opts)`).
2. Split the body into named stages in the same file: `seedFromAliasTarget` (returns a small `aliasSeed` struct holding lockstep/default/protocol/controls), `replayLayers` (see llm-registry-2), `finishRow` (transport, web-search gate, credentials, derive, warnings). No behavior change.
3. Fix the mis-attached doc comments while moving.
4. Public surface: keep `Resolve`; fold `ResolveFallback`/`ResolveLiveCandidate` into `ResolveWith(ref, ResolveMode)` only if a second caller appears (YAGNI otherwise); this audit only asks to reduce the private flag tower.

Size: ~250 lines moved, net -40 (wrappers and duplicated warning-building). Risk: medium; the golden and `resolve_test.go` suites are exactly the guard, run in CI.

### llm-registry-2 (high) The layer-order replay is hand-copied three times and each copy says "must not drift"

Locations: `resolve.go:850-905` (`resolveLayersModeLive`: `layerOrder`, `seenTag`, `applyExtrasBefore`, `applyConfigExtras`), `resolve.go:1376-1458` (`modelDisabled`: second `layerOrder := map[string]int{LayerSnapshot: 0, ...}` at `:1415`, `applyTop`), `instances.go:259-285` (`canonicalRow`: third replay minus the extras).

Evidence: `modelDisabled`'s own comment: "Mirror resolveOn's interleave: missing tags replay at their true layer positions ... so a curated overlay Disabled value cannot overwrite user config here either, and FindModel agrees with Resolve." `orderedGlobKeys` (`:1073`) was already extracted "so the two replays cannot disagree about matching order" but the interleave itself, the more intricate part, is still copied. `canonicalRow` omits the missing-tag extras entirely, so it is a third, slightly different order.

Impact: a change to layer precedence (e.g. adding a layer) must be made identically in three places; `FindModel`/`InstanceModels` (modelDisabled) versus `Resolve` can disagree silently, which is user-visible (a model listed as enabled that fails Resolve with `ErrModelDisabled`).

Fix plan: extract one `replayLayers(rec, ref, altID, visit func(step layerStep))` where `layerStep` is one of {topGlobs(tag), providerCaps(layer), layerGlobs(layer), exactRow(layer), live, configExtras}. `resolveLayersModeLive` applies caps/row scalars in the visitor, `modelDisabled` only reads `.Disabled`, `canonicalRow` filters to curated layers. Add a table test that feeds the same synthetic layer set to all three and asserts `modelDisabled == BoolValue(Resolve.Model.Disabled)` (TDD: write the cross-check first, expect green, then refactor).

Size: ~-120 lines net. Risk: medium (precedence is the core invariant); cross-check test is the safety net.

### llm-registry-3 (high) Credential diagnosis is English prose; llm parses it by prefix and tests assert copy

Locations: producer `instances.go:968-1109` (`credentialWithAuth`), `resolve.go:1294-1325` (`expandCredentialHeaders`), consumer `llm/authenticators.go:84-105` (`missingCredential`), tests `instances_test.go:129,287,816,953,985,1010,1169,1196,1238,1410,1420,1459,2146`, `schema_test.go:206-440`.

Evidence:
- `llm/authenticators.go:92`: `if strings.HasPrefix(w, "no credential")`, and a second loop matches `fmt.Sprintf("credential header %q:", authHeader)` case-insensitively against `res.Warnings`. Control flow in another module depends on registry warning wording; its own comment admits "the resolve path suppresses that warning when the auth header's own expression failed ... so the second look recognizes the auth header's failure wording".
- ~53 test assertions are `strings.Contains(strings.Join(res.Warnings, ";"), "<phrase>")` (e.g. `instances_test.go:816` asserts the full sentence `no credential (command expression failed: command exited with status 1: session expired)`). AGENTS.md "Copy Is Not a Test Oracle" forbids this.
- `Resolved.Warnings` is `[]string`, so there is no field a test or caller can assert on.

Impact: rewording a hint breaks credential error routing in `llm` and dozens of tests; the suppress/second-look dance exists only because the structure is missing.

Fix plan: add a typed reason to `Credential`: `Problem CredentialProblem` (enum: `None`, `Unset`, `Empty`, `SchemeWordOnly`, `CommandFailed`, `NoRecord`, `NoADC`), plus the existing `AuthoredLayer`. `credentialWithAuth` sets it once; warning text is rendered from it in one `func (c Credential) Warning() string`. `missingCredential` switches on `res.Credential.Problem`. Migrate tests to assert `Credential.Problem`/`Source`. Needs Jesse's sign-off on the new exported type (it changes the API surface; no back-compat shim: delete the prefix matching in the same PR).

Size: ~+60 / -120 incl. test edits. Risk: medium; hub UI strings must keep rendering the same text (keep rendering function identical, and test the enum not the text).

### llm-registry-4 (high) "Is this credential slot usable" is decided in four places

Locations: `instances.go:1031-1050` (auth header: unresolved / empty / noMaterial), `instances.go:1059-1087` (api_key: missing / empty / schemeWordDefault, same triple), `resolve.go:1303-1324`, expansion test at `:1315` (`expandCredentialHeaders`, same triple again for non-auth headers, written as an if/else-if chain), `credential_header_names.go:30-55` (`credentialHeaderNames`, the presence-mode mirror), `instances.go:610`, `:618`, `:657` (`AuthFingerprint` re-derives "inert" for slots and headers).

Evidence: `credentialHeaderNames`' doc: "A header counts as present under the same rules expandCredentialHeaders applies". `AuthFingerprint` comment: "hash under the same drop rules expandCredentialHeaders applies". Each site calls `expandEnv` then tests `len(missing)==0 && e != "" && !schemeWordDefault(v)` in slightly different shapes.

Impact: adding a fourth "no material" reason (it has already grown from empty -> unresolved -> scheme word) means touching four places; fingerprint drift would silently over- or under-invalidate cached live rows.

Fix plan: one `classifySlot(raw string, presence bool) slotState{Present, CommandBorne, Unset(missing), Empty, SchemeWordOnly}` in `env.go` (or new `credslot.go`). `credentialWithAuth`, `expandCredentialHeaders`, `credentialHeaderNames`, `AuthFingerprint` each switch on it. Pairs naturally with llm-registry-3 (the enum is the same vocabulary). Existing tests (`TestCredentialHeader*`, `TestAuthFingerprint*`) are the guard.

Size: -150 lines. Risk: medium-low.

### llm-registry-5 (medium) instances.go and resolve.go are grab-bags; AuthFingerprint and the first-party-endpoint gate are separable subsystems

Locations: `instances.go:526-699` (`AuthFingerprint`, 173 lines), `:178-420` (`firstPartyEndpoint`, `canonicalRow`, `canonicalVarLookup`, `hostRuleAuthorityVars`, `hostRuleInputAdmissible`, `authorityVars`, `sameEndpointURL`; the first-party doc comment alone is ~60 lines), `instances.go:111-160` (`ProviderRenameLeavesInstance`), `:1285-1304` (`StrayOAuthRecords`); `resolve.go:27-120` (chat-id filter, Vertex family table, dated-suffix regex), `:129-200` (live listing store).

Evidence: `instances.go` holds six unrelated concerns under a name that says "instances" (instance listing, credential resolution, fingerprinting, web-search endpoint gate, rename predicate, OAuth stray scan). `resolve.go` is 1709 lines and holds live-listing state, model-id vocabulary, alias machinery, disabled replay, and model-id listing.

Impact: navigation and review cost; the two biggest files change in nearly every provider PR so they conflict constantly.

Fix plan: pure file moves, no code changes, in this order: `fingerprint.go` (AuthFingerprint), `endpointgate.go` (firstPartyEndpoint family + gateWebSearch), `credential.go` (credential, credentialWithAuth, authorizationMode, schemeWordDefault, authHeaderKey), `live.go` (liveListing, ApplyLive, SnapshotLive, LiveModels, liveRow), `aliases.go` (alias target/verdict functions), `modelids.go` (IsChatModelID, StripDatedSuffix, vertex families). Do after llm-registry-1/2/4 so the moves do not fight the refactors; or first, as a zero-risk move, if Jesse prefers.

Size: ~0 net, ~2000 lines moved. Risk: low.

### llm-registry-6 (medium) Every Caps / Transport / Provider field is enumerated by hand in 5-7 places

Locations (Caps, 47 fields, `types.go:106-166`):
- `load.go:573-620` `cloneCaps` (45 hand-written lines, one `clonePointer` per field)
- `resolve.go:1044-1070` `seedFromAlias` (12-field subset)
- `resolve.go:132-146` `liveFacts` (another 13-field subset)
- `merge.go:19-47` `mergeCaps` (reflection + two name-string maps `wholesaleFields`/`keyWiseFields`)
- `write.go:117-138` `capsInto` (reflection over `toml` tags)
- `schema.go:360-400` `validateCaps` (hand list of 9 vocab checks)
Transport (12 fields): `types.go:70`, `schema.go:90` (`transportSchema` + `isZero` 4 lines of `&&`), `merge.go:51-74`, `write.go:104-116`, `load.go:514-522` and `:565-570`, `resolve.go:11-16` `clearProtocolTransport` (`load.go:668`).
Provider: `types.go:41`, `schema.go:110` (`providerSchema`), `schema.go:263-271` (construction), `load.go:700-770` (`fold`), `write.go:63-85` (`providerTable`), `load.go:535-551` (`cloneProviderView`).

Evidence: mixed strategies (reflection in 2 places, hand-listing in 4) means a new cap (the file shows recent additions: `ResponsesLite`, `EffortParameterUnsupported`, `DefaultEffort`) silently misses `cloneCaps` or `liveFacts` unless a test happens to cover it.

Impact: aliasing bugs (a missed `clonePointer` makes layer data shared) and silently dropped fields; adding a field costs 5 edits.

Fix plan: (1) replace `cloneCaps` with a reflection deep-copy next to `mergeCaps` (they already use reflection over the same struct), or generate by `slices.Clone`/`clonePointer` via one generic `cloneField`; (2) add one test that fills every `Caps` field via reflection and asserts clone/merge/capsInto each round-trip all of them (the real guard, TDD: write it first); (3) keep `seedFromAlias`/`liveFacts` as explicit allow-lists but build them from a `var aliasInheritedCaps = []string{...}` of field names consumed by the same reflection helper, so "what an alias inherits" is data in one spot.

Size: -90 lines, +40 test. Risk: low-medium.

### llm-registry-7 (medium) write.go re-implements the schema in reverse; schema structs and domain structs are a second parallel hierarchy

Locations: `write.go:44-160` (`MarshalConfig`, `providerTable`, `modelTables`, `transportInto`, `capsInto`, `costTable`, `setString`, `setStringMap`), `schema.go:90-170` (`fileSchema`, `transportSchema`, `providerSchema`, `modelSchema` and their `.transport()` / `.model()` / `isZero`).

Evidence: the TOML key names for providers/models/transports appear three times: struct tags in `schema.go`, `setString(t, "wire_id", ...)` literals in `write.go`, and construction code in `parseLayer` (`schema.go:263-271`). `Transport(ts)` is a type conversion that only compiles while the two structs stay field-identical. `write.go` builds `map[string]any` documents by hand to get omit-empty behavior, and the comment on `MarshalConfig` has to explain the one case (`api_key_env = []`) the hand-rolled path preserves.

Impact: a new provider key must be added to schema struct, parse constructor, writer, fold, and view clone; the round-trip test (`ParseConfig(MarshalConfig(l))`) is the only guard.

Fix plan: encode with the schema structs (`toml:",omitempty"` tags; pointer fields already distinguish unset) and delete `providerTable/modelTables/transportInto/capsInto/costTable/setString*` (~120 lines). Keep the explicit-empty `api_key_env` via a pointer-to-slice or a custom marshal for that one field. Guard with the existing `write_test.go` round-trips plus a fuzz-ish test over every field. Needs a spike to confirm BurntSushi/toml's omit semantics on embedded `Caps` (same embedding the decoder already relies on).

Size: -130 lines. Risk: medium (serialization drift would rewrite users' files; the re-parse in `WriteConfigFile` catches unloadable output but not changed bytes).

### llm-registry-8 (medium) Alias-target parsing ("provider/id") is implemented four times with different acceptance rules

Locations: `load.go:896-910` (`aliasTarget`, load-time), `resolve.go:1003-1028` (`aliasTargetRow`), `resolve.go:1534-1585` (`recordMayDisableSeen` re-parses `AliasOf` inline at `:1561`), `resolve.go:121-127` (`ParseRef`, same first-slash split), plus `aliasTargetRef` `:1516` and `aliasTargetVerdict` `:1461` which each call `aliasTargetRow` again.

Evidence: `aliasTargetRow`'s comment says it applies "the alias-target acceptance rules both resolve paths share" yet `aliasTarget` (load) accepts an alias row as target (rejecting later at `validateRecord` `:870`) and falls to `r.curated` directly, while `aliasTargetRow` uses `recordFor` first then `curated`. `recordMayDisableSeen` ignores same-provider targets by a third rule (`target == rec` continue) and reimplements glob rejection (`isGlob(m.AliasOf[:i]) || isGlob(id)`).

Impact: three subtly different definitions of "a valid alias target"; a dangling alias can validate at load but fail at runtime or vice versa.

Fix plan: one `func (r *Registry) aliasTarget(rec, aliasOf) (target *record, id string, row Model, err error)` that returns *why* it failed (glob, unknown provider, no such row, itself an alias); `validateRecord`, `aliasTargetRow`, `recordMayDisable` all call it. Delete the other two. Tests: existing `TestAliasTarget_*`, `disabled_test.go`.

Size: -60 lines. Risk: low-medium.

### llm-registry-9 (medium) Two gate functions are copy-paste of each other; the caller retries one with the other

Locations: `instances.go:713-749` (`ResolveGateCredential`) and `:751-778` (`ResolveGateCredentialCandidate`); caller `cmd/evener-hub/spawn.go:884-891`.

Evidence: the two bodies differ by one call: `resolveLayersMode(..., resolveTransport)` versus `resolveLayersModeLive(..., resolveTransport, true, true)`. The 6-line instance/model normalization, the disabled check, and the final `credential` call are duplicated verbatim. The only caller does `res, err := r.ResolveGateCredential(...); if errors.Is(err, ErrCodexModelNotListed) { return r.ResolveGateCredentialCandidate(...) }`.

Impact: any gate fix (e.g. the model-prefix stripping) must be applied twice; the pair exists solely to serve one caller's retry.

Fix plan: make a single `ResolveGateCredential(instance, model)` that performs the candidate fallback internally (error-driven, exactly what spawn.go does), delete `...Candidate` and the spawn.go wrapper `resolveGateCredentialForSpawn`. TDD: add a registry-level test for "Codex id the bundled roster lacks resolves transport via glob auth override" (spawn.go's current behavior), then collapse.

Size: -45 lines (registry) -8 (hub). Risk: low.

### llm-registry-10 (medium) Production code branches on testing.Testing() and exports test seams

Locations: `load.go:17,1234` (`import "testing"`; `case testing.Testing(): offline = o.fetcher == nil`), exported options `WithSnapshot`, `WithOverlay`, `WithFetcher`, `WithLog`, `WaitRefresh`, `RefreshStarted` (`load.go:96,110,118,122,1216-1226`).

Evidence: no non-test caller of `WithFetcher`, `WithLog`, `WithOverlay`, `WithSnapshot`, `WaitRefresh` (grep outside `llm/registry` and its tests: only `registry.Refresh` itself uses `WithSnapshot`); comments say "(tests)". The `testing` package is linked into the production binary and the default offline/online behavior of `Load` differs under `go test`, which means tests do not run the production default path.

Impact: hidden behavior fork (a test that forgets `WithOffline` is online in prod, offline under test); production API surface padded with test-only knobs.

Fix plan: make the default always "refresh unless offline", and have test helpers pass `WithOffline(true)` (one `loadForTest` helper already exists in `load_test.go`); remove `testing` import; move the test-only options into an `export_test.go`-style file or keep only `WithOffline` + `WithFetcher` if the hub ever injects one. Check CI-time impact: any package test calling `registry.Load` without options would need offline; `cmdutil`/`agent` tests may rely on the testing fork, so grep `registry.Load(`/`EmbeddedRegistry` callers first and set `EVENER_OFFLINE=1` in their `TestMain` (see `main_test.go` in llm).

Size: ~-30 / +20. Risk: medium (could silently turn network on in some package's tests; run in CI).

### llm-registry-11 (medium) Dead and test-only exported API

Locations: `derive.go:213-230` (`ReasoningEnabled`, `RequestEffortCapable`), `prune.go:233` (`getPath`), `prune.go:69` (`TemperaturePath`), `prune.go:146` (`ValidateFields`), `prune.go:136` (`Baseline`), `schema.go:485` (`ThinkingFormats`), `credential_json.go:18` (`AllowedCredentialJSONTypes`), `modelsdev.go:200` (`FromModelsDev`), `snapshot.go:25` (`ParseMeta`), `overlay.go` (`ParseOverlay`, `EmbeddedOverlay`).

Evidence (repo-wide grep, non-comment):
- `RequestEffortCapable`: zero references anywhere other than its definition (no test either). `ReasoningEnabled` has one external caller (`agent/session_tools.go:446`).
- `getPath`: only `prune_test.go:103`; defined in production code.
- `Baseline`, `ThinkingFormats`: used only by `llm` tests.
- `TemperaturePath`, `ValidateFields`, `ParseMeta`, `AllowedCredentialJSONTypes`: referenced only inside the package (plus its tests); exported but no outside consumer.
- `ParseOverlay`/`EmbeddedOverlay`/`FromModelsDev`: used only by `internal/snapshotreport` (inside the module's own tooling) and tests.

Impact: surface that must be kept compatible and documented for no consumer; `RequestEffortCapable` is a speculative helper with a 6-line rationale comment.

Fix plan: delete `RequestEffortCapable` and `getPath` (move `getPath` into `prune_test.go`); unexport `TemperaturePath`, `ValidateFields`, `ParseMeta`, `AllowedCredentialJSONTypes`; leave `FromModelsDev/ParseOverlay` (tool consumers). `make lint-internal` must stay green.

Size: -40 lines. Risk: low.

### llm-registry-12 (medium) Reinvented helpers: state/config root resolution and atomic file writes

Locations: `load.go:188-216` (`defaultConfigRoot`, `defaultStateRoot`), `refresh.go:48-66` (`writeAtomic`), `write.go:225-262` (inline temp+rename).

Evidence:
- `defaultStateRoot`'s own comment: "mirrors cmdutil.DefaultStateRoot". The canonical chain is `cmdutil/statedir.go:26-50` (`StateRootFromLookup`, handles Windows/Plan 9 home via `userHomeDirFromLookup`); the registry copy only reads `HOME`, so on Windows it resolves `./.local/state/evener`. `llm` cannot import `cmdutil` (it imports `agent`), but `envvars/userdirs` is already a dependency (`load.go:19`) and already hosts `ConfigRoot`/`DefaultConfigRoot`.
- Atomic write: `refresh.go:48` (no fsync, no chmod), `write.go` (random temp, chmod 0644, no fsync), plus 5 more named helpers in the repo (`internal/plugins/atomic.go:32` with fsync of file and dir, `internal/transcriptindex/index.go:1294`, `cmd/evener-dev/agentshards.go:750`, `cmd/evener-hub/internal/launchconfig/io.go:53`, `agent/execenv/securepath_fdops_unix.go:916`). Different durability guarantees in each.

Impact: Windows/home-resolution drift for the registry cache dir; durability guarantees differ for no stated reason (a crash can leave a zero-length `models.dev.json`, which `readCache` then treats as corrupt and re-fetches, so it self-heals, but `providers.toml` is user data).

Fix plan: (a) move `StateRootFromLookup` into `envvars/userdirs` (leaf module, no `agent` import) and have both `cmdutil` and the registry call it; delete `defaultStateRoot`/`defaultConfigRoot` (use `userdirs.DefaultConfigRoot` with injected lookup). (b) Create one `fsutil.WriteFileAtomic` (module already shared by llm via `primeradiant.com/evener/...`; pick the leaf module `execsupport` or a new tiny `atomicfile` package) based on `internal/plugins/atomic.go`, and migrate the registry's two writers first; other repos' sites are covered by their slices. Keep the symlink-resolution wrapper in `WriteConfigFile`.

Size: -50 in registry, +60 shared. Risk: low. Cross-cutting.

### llm-registry-13 (medium) Model and provider facts hard-coded in Go in a "data-driven" registry

Locations: `resolve.go:43-71` (`vertexGlobalOnly` lists `claude-opus-4-7`, `claude-opus-5`, `claude-sonnet-5`, `claude-fable-5`, `claude-mythos`, `gemini-3`), `resolve.go:739-750` (Codex allowlist in the core resolver), `modelsdev.go:289-310` (`switch providerID { case "amazon-bedrock" ... case "google-vertex" }` row-hiding), `modelsdev.go:120-125` (`hiddenNPM` list of 10 vendor packages), `resolve.go:23` (`nonChatPatterns` incl. `"image"`, `"audio"`, `"sora"`).

Evidence: the package doc says "data-driven provider registry" and the overlay exists for curated facts, yet the Vertex regional limitation list must be edited in Go every time Anthropic or Google ships a model (the list already needed `claude-opus-4-8`, `claude-fable-5`, `claude-mythos`), and `IsVertexGlobalOnly` is exported for `llm/providers/google/protocol_transport.go`.

Impact: each new model generation is a code change plus release for a fact that belongs in `providers_overlay.toml` next to the Vertex rows.

Fix plan (needs Jesse's design call): add a curated overlay field (e.g. `regions = ["global","us","eu"]` on the Vertex glob rows, `claude-opus-5*`, `gemini-3*`) and derive `IsVertexGlobalOnly` from the resolved row; delete `vertexGlobalOnly` and `familyCovers`. Leave the models.dev converter tables (that is the one place allowed to know models.dev's schema).

Size: -60 Go, +15 TOML. Risk: medium (new schema key; update `docs/llm-providers.md`).

### llm-registry-14 (low-medium) Overlay copy-paste: Codex rows repeat a 13-line block three times

Locations: `data/providers_overlay.toml:249-295` (`gpt-6-astra`, `gpt-6-luna`, `gpt-6.1-sol`), with the inline table `body = { "reasoning.context" = "all_turns", "text.verbosity" = "low", parallel_tool_calls = false }` 4 times (counted with grep, including the existing `gpt-5.6*` glob at `:239`), `context_window = 272000` 5 times, `responses_lite/thinking_always_on/reasoning_summary/sampling=false/tools/structured_output/input_modalities` repeated per row.

Impact: adding a Codex model is a copy-paste that can silently diverge (e.g. `image_detail = "omit"` on the 5.6 glob vs `"original"` on the 6.x rows is either deliberate or drift, with nothing marking which).

Fix plan: one `"gpt-6*"` glob carrying the shared lite shaping; per-row deltas only (`default_effort`, `max_output_tokens`). Verify with the golden tests (`testdata/golden/openai-codex-*.json`); add the `openai-codex` rows to the golden set if missing. Document the 5.6 vs 6.x `image_detail` difference in a one-line comment or unify.

Size: -40 TOML lines. Risk: low.

### llm-registry-15 (low-medium) derive() is a 145-line function with narrative comments and bypasses Caps helpers

Locations: `derive.go:35-181`, `types.go` `ReasoningDisabled`, `derive.go:36` (`reasoningOff := c.Reasoning != nil && !*c.Reasoning`).

Evidence: `derive` re-implements `Caps.ReasoningDisabled()` and `ReasoningEnabled()` inline, and contains six numbered phases (effort pass-through, junk cap, surface fallback, google shape, anthropic shape, adaptive defaults) with a 25-line comment block at `:84-103` for one `if`. `deriveInput` has 7 fields to carry alias state from the resolver (see llm-registry-1).

Impact: each new model-family quirk grows one function; the Google shape block's correctness depends on `prov["ReasoningControls"] == provAlias` string equality.

Fix plan: split into `deriveEffort`, `deriveSurface`, `deriveGoogleShape`, `deriveAnthropicShape`, called in order from `derive`; use `c.ReasoningDisabled()`; trim the comment to rules, move the "why" to the commit. Guard: `reasoning_default_test.go`, golden tests.

Size: ~0 net, ~-30 comment lines. Risk: low.

### llm-registry-16 (low) Comment bloat, history narration, and orphaned doc comments

Locations: `instances.go:111-160` (50-line comment above a 40-line function), `:178-232` (`firstPartyEndpoint` 60-line doc incl. test names `TestResolve_WebSearchEndpointGate`, `:225`; `TestCuratedBaseURLsCarryNoQueryOrFragment`, `:389`), `instances.go:991` ("roborev F2"), `prune.go:94` ("issue #834"), `prune.go:121` ("the round-5 probe on a work/my-model row"), `write.go:208-210` ("issue #1040 ... Jesse's ruling, 2026-09-07"), `resolve.go:713-729` and `:995-1002` (doc comments attached to the wrong declaration), `load.go:918-940` (`varLookupWith` 22-line doc).

Evidence: comment ratio 28-36% in the two largest files. Many comments narrate who found a bug and when; global rules say history goes in commit messages. Several describe call graphs that code changes will falsify.

Impact: noise that hides the non-obvious "why"; comments that cite tests by name rot on rename.

Fix plan: mechanical pass after the structural PRs (1,2,4,5): keep invariants and the one-sentence why, drop review/issue/round/date references, drop test-name citations, fix the two mis-attached docs. No code changes.

Size: ~-400 comment lines. Risk: none.

### llm-registry-17 (low) Code cites a dated spec by section number 178 times; evergreen docs are elsewhere

Locations: `types.go:1-5` ("Design: docs/superpowers/specs/2026-08-28-provider-registry-design.md"), 178 `§` references across non-test files (`grep -c '§'`), e.g. `schema.go:11`, `resolve.go:11`, `instances.go:18`.

Evidence: AGENTS.md says to keep product docs evergreen and dated narratives out of references; `docs/llm-providers.md` and `llm/README.md` are the evergreen references and have different numbering. When the spec is edited or superseded the `§` numbers point nowhere.

Impact: doc-drift risk; a reader cannot tell whether `§10.1` refers to the spec or current behavior.

Fix plan: replace section numbers with the `docs/llm-providers.md` anchor names where one exists (credential-resolution-order, providerstoml, ...) and delete the rest; add an "invariants" section to `llm/README.md` for the credential-presence rules (no command execution on hub paths) that the code currently defends by citation. Do with llm-registry-16.

Size: ~178 comment edits. Risk: none.

### llm-registry-18 (low) Small local duplications and bypassed helpers

Locations:
- `BoolValue(rec.head.Implicit)` exists (`types.go`), but the nil-check-and-deref is hand-written 4 times: `instances.go:119`, `:1126`, `:1168`, `:1183`.
- `recordGlobPinsAuth` (`resolve.go:507-536`) has two copies of the same `switch m.Transport.Auth` loop over `r.topGlobs` and `rec.head.Models`.
- `templateURL` var-loop copied in `modelsdev.go:226-233` (`convertProvider`) and `:359-365` (`convertModelOverride`), both doing `mergeStringMap(x, map[string]string{v: v})` per variable.
- `CheckCredentialHeaderValue` (`env.go:127-188`) emits the same two refusal messages three times each (`:164/:175/:184`, `:167/:178`) in the `PieceRef` and `PieceCommand` arms.
- `schema.go:222-225` vs `:255-258`: the glob-row "protocol/preset not allowed" checks copied for top-level and provider-scoped rows; `validateProvider` and `validateModel` repeat the same headers loop.
- `schema.go:71` `effortLevels` restates `llm.ReasoningEffortVocabulary` (documented, pinned by `llm/registry_shape_test`).
- `cloneTransport`/`cloneModel` (`load.go:514-531`) copy `Body` with `maps.Copy` (shallow) while `cloneTransportView` (`:565`) deep-copies via `cloneAnyMap`; two clone families for the same type.

Impact: each is small, but they are exactly the sites reviewers keep patching in pairs; the shallow `Body` copy is a latent aliasing bug if a nested body table is ever mutated.

Fix plan: single-purpose cleanups, each its own tiny commit: use `BoolValue`; extract `neverSendsAPIKey(auth)`; extract `varsEnvFromTemplate(api)`; extract `checkSchemeAdjacency(schemeGlued, schemeControl)`; extract `validateGlobRow(ms, where)` and `validateHeaders(h, where)`; replace both clone families with the deep-copy variant (llm-registry-6 covers the shared mechanism).

Size: -80 lines. Risk: low.

---

## Recommended fix sequence

1. llm-registry-11 and llm-registry-18 first: deletions and one-line helper substitutions, no design decisions, shrink the surface before touching internals.
2. llm-registry-9 (collapse the gate pair): small, has a clear caller, removes `ErrCodexModelNotListed` retry from the hub.
3. llm-registry-8 (single alias-target function), then llm-registry-2 (single layer replay), each behind a new cross-check test written first. These two unlock the resolver split.
4. llm-registry-1 (resolver stages and options struct) on top of 2 and 8.
5. llm-registry-4 then llm-registry-3 (slot classification, then typed `Credential.Problem` replacing prefix-parsing in `llm/authenticators.go`; discuss the exported type with Jesse before starting).
6. llm-registry-6 (Caps field enumeration test + reflection clone) and llm-registry-7 (write through schema structs; spike first).
7. llm-registry-10 (remove `testing.Testing()` fork; set offline in test mains), llm-registry-12 (shared state-root and atomic-write helpers; coordinate with other slices that own the other six writers).
8. llm-registry-5 (pure file split) after the refactors so moves do not conflict; llm-registry-15 (split `derive`).
9. llm-registry-13 and llm-registry-14 (move Vertex regional facts and Codex row duplication into overlay data; needs a design call and `docs/llm-providers.md` update).
10. llm-registry-16 and llm-registry-17 last: comment and spec-citation cleanup plus `llm/README.md` / `docs/llm-providers.md` updates, so the pass rewrites the final code rather than code about to move.

Docs to update in the same changes: `docs/llm-providers.md` (credential resolution, overlay schema if llm-registry-13 lands), `llm/README.md`, and the S15/S16 rows in `docs/product/subsystems.md` only if ownership of the credential-diagnosis contract (llm-registry-3) or fingerprint file moves change the linked sources.
