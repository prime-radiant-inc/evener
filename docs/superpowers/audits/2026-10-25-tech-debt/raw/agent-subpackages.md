# Tech-debt audit: agent-subpackages

Slice: `agent/doctor`, `agent/skill`, `agent/plugin`, `agent/schema`, `agent/transcript`, `agent/events`, `agent/provider`, `agent/task`, `agent/diagnostic`, `agent/envctx`, `agent/mcpconfig`, `agent/command`, `agent/mcpprobe`, `agent/provenance`, `agent/activitybound`, `agent/searchresult`, `agent/argrepair` (non-test).

Worktree: `.worktrees/debt-audit` (origin/main). Read-only audit; no tests were run.

## Overview and inventory

| package | src files | src LOC | test LOC | notes |
|---|---|---|---|---|
| doctor | 14 | 5255 | 12547 | `audit.go` 1238, `apilog.go` 950, `transcript.go` 468 |
| skill | 17 | 2630 | 4140 | `builtin_skills.go` 1039 + lease/owner platform files ~520 |
| schema | 15 | 2157 | 3103 | `snapshot.go` 893 (persistence), `turn.go` 508 |
| transcript | 9 | 2074 | 4768 | `transcript.go` 1212 (Writer) |
| events | 5 | 1408 | 1070 | `payloads.go` 981 |
| plugin | 7 | 1482 | 4332 | `plugin.go` 483, `hooks.go` 428 |
| provider | 4 | 918 | 1970 | `profile.go` 644 |
| task | 1 | 916 | 2398 | `task_store.go` |
| diagnostic | 2 | 486 | 840 | |
| envctx | 6 | 405 | 417 | |
| mcpconfig | 1 | 372 | 1458 | |
| command | 1 | 265 | 982 | |
| mcpprobe | 1 | 185 | 596 | |
| provenance | 1 | 152 | 541 | |
| activitybound | 1 | 120 | 133 | |
| searchresult | 1 | 36 | 49 | |
| argrepair | 1 | 33 | 0 | thin re-export |

Overall health: the slice is mostly well-factored leaf packages, with solid tests (test LOC exceeds source in nearly every package). The debt concentrates in four patterns.

1. Review-round archaeology. `doctor.RunAudit` is a 400-line function whose comments narrate "round 9 finding 2" style fixes. The `skill` bundled-cache has 45 fix commits behind it.
2. Production API that only tests call. `skill` (legacy map API), `plugin` (LoadAll, LoadSettings, hook wrappers), `transcript.Writer` (Append/AppendDurable/AppendSynced/AppendBatch/OpenWriter), `events.ToStreamEvent`, `task.TaskStore.Progress`.
3. Shared helpers that were never extracted. Session file layout literals (about 25 sites), XDG state-home resolution (6 sites), whole-transcript read loops (8+ sites), rune truncation (6+ variants), root-to-cwd project walk (3 sites).
4. Mirrored structs and tables kept in sync by hand. `SessionConfig` and `schema.ConfigSnapshot` (34 fields, three hand copies), the Go and TS hub-failure keyword lists, and `emit(kind, data)` where kind is derivable from data.

Findings are ordered roughly by value (impact over cost).

---

## agent-subpackages-1: `doctor.RunAudit` is a 400-line function narrated by review rounds (high)

Locations: `agent/doctor/audit.go:742-1143` (RunAudit), `:1168-1203` helpers, constant doc at `:1121-1139`.

Evidence:
- `RunAudit` spans lines 742 to about 1143 (about 400 lines) and keeps five parallel maps keyed by finding signature: `findingsBySignature`, `checkBySignature`, `doctorRefsBySig`, `nonReproBySig`, `sessionsBySig`, plus `signatureOrder`.
- `grep -ci "round [0-9]+ finding|finding [0-9])" agent/doctor/audit.go` gives 32 hits. Examples: `// Round 12 finding 1: a bare sid in SessionRefs (agentSel) is colliding...` (line ~872), `(round 9 finding 1: the sweep previously used followSelector ...)` (line ~815), `(round 14 finding 2 superseded round 10 finding 4's separate command-side truncation disclosure)` (const doc at ~1126).
- The comment on the `evidenceSessionRefCap` constant is itself 14 lines of finding-number history. A reader has to reconstruct the rule from the rounds.
- The inner loop per session does five jobs inline: locate, build three different selector forms (`sel`, `agentSel`, `doctorSel`), probe for ambiguity via a second `Locate`, enumerate all buckets for a canonical-name collision, evaluate checks, then update the five maps.
- Fail-closed wording ("Fail closed: treat the sid as colliding ... An honest refusal beats a possibly wrong handle", line ~889) when bucket enumeration fails. AGENTS.md asks for recovery over lockdown; this branch silently drops evidence refs.

Impact: the most review-churned and least readable code in the slice. Any new runbook field or ref policy means editing a 400-line closure-free function. History comments describe past defects, not the current rule, so they rot.

Fix plan:
1. Pin current behavior first: the existing 68 round-referencing lines in `audit_test.go` already cover it; add no new behavior.
2. Extract `sessionRefPolicy` (a small struct built per session from `Paths`): methods `AgentRef()`, `DoctorRef()`, `Reproducible()`, `CollidesWithCanonical()`. This replaces the three selector locals and two `Locate` probes.
3. Extract `findingAccumulator` (one struct holding the five maps, with `observe(check, session)` and `finalize() []Finding`). `RunAudit` becomes: resolve refs, for each session load metrics and `observe`, then `finalize`.
4. Replace all "round N finding M" comments with one comment per rule stating the rule (ref cap shared between reproducible and non-reproducible refs; DoctorCommand never carries a `#` comment because cmd.exe lacks it; bare sid dropped from SessionRefs when it resolves differently on the agent side).
5. Decide with Jesse whether the bucket-enumeration-failure branch should keep the ref (recovery) and attach a warning instead of dropping it.

Size: about 400 lines moved, about 120 net removed (comments plus the repeated per-sig map plumbing). Risk: medium; output strings (Description, DoctorCommand) are asserted by tests, so refactor must keep them byte-identical. Docs: none required (no product change); update the `doctoring-evener` bundled reference only if the Description grammar changes.

---

## agent-subpackages-2: On-disk session file layout is spelled out in about 25 places (medium)

Locations: `agent/doctor/locate.go:168-181` (`pathsFor`), `:218`; `agent/doctor/turnids.go:141,164`; `agent/schema/snapshot.go:343,640,685,859-862`; `agent/session.go:2775,2783`; `agent/transcript_lookup.go:438`; `agent/fork.go:198,294`; `agent/delegate_tree_start.go:160,349,651`; `agent/delegate_tree_attention.go:963`; `agent/session_init.go:601,990,1403`; `agent/jobs_activity_past.go:324`; `agent/session_lifecycle.go:738`; `agent/delegate_runtime.go:3487,3502`; `agent/aside.go:75-79`; `agent/apilog_read.go:144`; `llm/apilog.go:113,146`; `cmd/evener/serve.go:1368`; `tools/tool-fluency/cmd/evener-fluency/main.go:1392,1561`; `cmd/evener-doctor/reconstruct.go:159`.

Evidence:
- `grep '\.transcript\.jsonl"'` returns at least 22 non-test hits; `.meta.json"` and `.api.jsonl"` add about a dozen more.
- `const sessionsSubdir = "sessions"` is declared twice: `agent/session.go:2775` and `agent/schema/snapshot.go:343`. `doctor/locate.go:169` and `:218` and `turnids.go:141` use the bare literal.
- `agent/doctor/doctor.go:1-9` claims "There is no second implementation of the on-disk format to drift", while `pathsFor` re-derives transcript, api-log, meta, jobs, delegates, and mutations paths with string literals.
- `schema` already owns `SessionDir`/`SessionArtifactsDir` (`snapshot.go:348,362`), so a canonical home exists but covers only the directory.

Impact: a layout change (or a Windows-reserved-name fix like the one already in `ValidateSessionID`) must be made at every site; the doctor, which promises to be type-coupled to the real format, is not.

Fix plan:
1. Add to `agent/schema` (next to `SessionDir`): `TranscriptPath(stateDir, id)`, `APILogPath`, `MetaPath`, `JobsPath`, `DelegatesPath`, plus exported suffix constants for the glob and trim-suffix cases.
2. Replace the declarations in `agent/session.go` and `schema/snapshot.go` with the single const.
3. Migrate callers mechanically in this order: doctor (including `pathsFor`), schema internals, agent, then `llm/apilog.go`, cmds, and tools. `llm` cannot import `agent/schema`, so either leave `llm/apilog.go` taking a path from its caller or put the suffix constants in a leaf package both can import.
4. A single test asserts each helper's shape against `SaveSessionMeta`/`NewWriter` actual outputs (writer creates file, helper finds it).

Size: about 80 lines added, about 60 call sites touched (each a one-line change). Risk: low. Docs: `docs/product/subsystems.md` row for session state if it lists the layout.

---

## agent-subpackages-3: No canonical whole-transcript reader; 8+ copies of the header-then-entries loop (medium)

Locations: `agent/doctor/transcript.go:34-78`; `agent/transcript_read.go:82-140`; `agent/fork.go:205-262`; `agent/session_attention.go:80-135`; `agent/transcript_render.go:335,381,404`; `cmd/evener-doctor/reconstruct.go:194-207`; `internal/apptranscript/apptranscript.go:1359-1370`; `internal/transcriptindex/index.go:1081-1104`; `cmd/evener-hub/image_serve.go:157-174`; `server/thread_history.go:806-819`; `cmd/evener-transcript-v2-upgrade/main.go:209-298`.

Evidence: every copy does `bufio.NewReaderSize(f, 64*1024)`, loops `transcript.ReadLine(reader, max)`, `bytes.TrimSpace`, skips blanks, `DecodeHeader` for the first line, `DecodeEntry` for the rest. They differ only in error wrapping and what they retain:
- `doctor/transcript.go` wraps with `read transcript line %d: %w`.
- `transcript_read.go` wraps with a sentinel (`wrapTranscriptCorrupt`) and tracks `Skipped`.
- `fork.go` returns `errors.New("parent transcript is empty")`.
- `session_attention.go` also tracks `consumed` offset.
`agent/transcript/transcript.go` already has `ReadHeader` (line 221) but no entry iterator, so every reader reinvented the second half.

Impact: a format change (the repo already did a v2 upgrade) or a new quarantine policy needs eleven edits; error behavior silently differs (one copy aborts on unknown fields, another skips).

Fix plan:
1. Add `transcript.Scan(r io.Reader, maxLineBytes int, fn func(line []byte, header *Header, entry *Entry) error) (ScanResult, error)` or a `ForEachRecord` iterator in `agent/transcript` returning `{Header, Entries, Skipped, Consumed}` with typed errors (`*ScanError{Phase, Line, Err}`).
2. Migrate in order: doctor, fork, transcript_read, session_attention (these are in-process and tested), then the cmd and internal readers.
3. Keep per-caller error sentinel mapping at the call site via `errors.As` on `ScanError`.
4. Tests: one table test for the iterator (partial tail, blank lines, oversized line, header-only, bad header), existing caller tests guard behavior.

Size: about 90 lines added, about 400 removed. Risk: medium (offset accounting in `session_attention.go` and the torn-tail rule must be preserved exactly). Docs: the `transcript` package doc.

---

## agent-subpackages-4: XDG state-home resolution reimplemented in 6 places (medium)

Locations: `agent/doctor/statedir.go:21-37`; `agent/runtime_dir.go:73-83` (`xdgStateHome`); `cmdutil/statedir.go:30-52` (`StateRootFromLookup`) and `:63-80` (`userHomeDirFromLookup`); `rendezvous/rendezvous.go:53`; `appwire/frame_recorder.go:95`; `auth/openai/storage.go:100`; `cmdutil/legacymigration.go:88`.

Evidence:
- `doctor.ResolveStateBase` is `flag > EVENER_STATE_DIR > XDG_STATE_HOME > $HOME/.local/state`, with `os.TempDir()` as the home fallback.
- `agent.xdgStateHome` is the same chain without the flag/EVENER_STATE_DIR arms, same `os.TempDir()` fallback.
- `cmdutil.StateRootFromLookup` is the same again but falls back to `"."` rather than `os.TempDir()` when home is unresolvable, and appends `evener`. `cmdutil/statedir.go:21` comments that it must be "the one canonical chain" (#1012). Two fallbacks for an unresolvable home (`os.TempDir()` vs `.`) are drift already in place.
- The doctor comment at `statedir.go:17-19` ("Note EVENER_STATE_HOME does not exist - it was never read by evener") is a history note about a wrong guess.

Impact: the home-unresolvable path puts state in two different places depending on the binary; the doctor can look in a different state root than the daemon writes.

Fix plan:
1. Make `envvars/userdirs` (already a leaf, already imported by cmdutil, plugin, mcpconfig) own `StateHome(lookup, userHomeDir)`. It returns `$XDG_STATE_HOME` or `<home>/.local/state`, with one documented fallback.
2. `cmdutil.StateRootFromLookup`, `agent.xdgStateHome`, and `doctor.ResolveStateBase` call it (doctor keeps its two extra precedence arms in front).
3. Move rendezvous, frame_recorder, and openai storage onto it as follow-ups.
4. Decide the single fallback for unresolvable home with Jesse (recommend `os.TempDir()` since `.` makes state depend on cwd).

Size: about 40 lines added, about 70 removed. Risk: low-medium (the fallback choice changes behavior only when home is unresolvable). Docs: update `envvars.XDGStateHome` summary if the fallback text changes.

---

## agent-subpackages-5: Rune truncation, "oneLine" and whitespace collapse reimplemented 8+ times (medium)

Locations: `agent/activitybound/activitybound.go:49-76` (Truncate); `agent/doctor/transcript.go:459-468` (Truncate, oneLine); `appwire/delegate_roster.go:33-48` (truncateRosterText); `agent/transcript_render.go:1966-1978` (truncRunes, oneLine); `agent/transcript_util.go:10-31` (firstLineClamp); `agent/jobs_activity.go:109-111` (truncateActivityText); `agent/internal/tool/breaker.go:554,591` (collapseWhitespace, TruncateRunes); `agent/memory_page.go:219` (collapseWhitespace); `cmd/evener-hub/navigation_projection.go:1932-1955`; `agent/internal/runetrim` (the existing partial helper).

Evidence:
- `activitybound.Truncate` and `appwire.truncateRosterText` are the same algorithm (walk runes to the cap, `cutAt` at `max-1`, append `"…"`), 25 lines each, copy-pasted.
- `agent/jobs_activity.go:109`: `func truncateActivityText(s string, maxRunes int) string { return activitybound.Truncate(s, maxRunes) }` is a pure pass-through wrapper.
- `transcript_render.go:1974` says "Mirrors doctor.oneLine" in a comment: a known copy.
- `TruncateRunes` (breaker.go) cuts without an ellipsis, `truncRunes` cuts with one, `firstLineClamp` allocates `[]rune(firstLine)` of the whole input, which the `activitybound.Truncate` doc explicitly calls out as the thing to avoid.
- `doctor.Truncate` is exported only so `tools/tool-fluency/.../review.go:180` can call it.

Impact: bounding behavior (ellipsis, rune vs byte, invalid UTF-8) varies by call site and the memory-bound care taken in one copy is absent in others.

Fix plan:
1. Put one `Truncate(s, maxRunes) string` (the allocation-free version from `activitybound`) and one `OneLine(s)` in a shared leaf (extend `agent/internal/runetrim`, or a new `internal/text`, whichever has no import cycle for `appwire` and `cmd/evener-hub`; `appwire` cannot import `agent/internal`, so the home must be a root-level `internal/` or an existing public leaf).
2. Delete `truncateActivityText`, `truncRunes`, `truncateRosterText`, doctor `Truncate`/`oneLine`, `agent.oneLine`, `collapseWhitespace` duplicates, and point callers at the shared helper. Keep `TruncateRunes`' no-ellipsis variant under a different, explicit name.
3. Test: move `activitybound`'s truncation tests to the shared helper; keep one thin test in each remaining caller only if its cap constant matters.

Size: about 60 added, about 150 removed, 20 call sites. Risk: low. Docs: none.

---

## agent-subpackages-6: `skill` ships a second, production-dead resolution API (medium-high)

Locations: `agent/skill/skills.go:36-141` (`DiscoverSkills`, `LoadSkillBody`, `CatalogEntries`, `ResolveSkill`, `ResolveSkillContent`), `agent/skill/builtin_skills.go:223-250` (`ExtractEmbeddedSkills`, `EmbeddedSkills`), `agent/skill/builtin_skills.go:98-207` (process metadata cache that feeds only `EmbeddedSkills`), `agent/skill/discovery.go:55-66` (`Catalog.ResolveSkillContent`).

Evidence: a grep for non-test callers outside `agent/skill`:
- `DiscoverSkills`, `LoadSkillBody`, `CatalogEntries`, `ResolveSkill`, map-based `ResolveSkillContent`, `ExtractEmbeddedSkills`, `EmbeddedSkills`: zero production callers. Test files reference them (about 75 uses: `skills_test.go`, `load_test.go`, `cov_s5_skills_test.go`, `program_fuzz_test.go`, `agent/skills_test.go`, ...).
- `Catalog.ResolveSkillContent` has no production caller either; its doc says "for existing role preloads" and "retain their existing empty-body behavior" (a compat promise nobody uses) and carries a `//nolint:nilerr` justification.
- The only production use of the legacy file is `skill.ScanSkillsDir` from `plugin/plugin.go:132` and `builtin_skills.go:189,316`, plus the `SkillMeta` type.
- The `ResolveSkill` rules (map, "uniquely matching plugin suffix") duplicate `Catalog.Resolve` (discovery.go:72) with a different return shape and a different ambiguity result (silent not-found vs typed `ResolutionError`).
- `embeddedProcessSkills` (builtin_skills.go:171-207) holds a retry loop whose own comment says the re-check "is unreachable ... It is kept anyway".

Impact: two sources of truth for how a skill name resolves (one tested heavily, one used); the dead one pins the embedded-metadata cache (`embeddedSkillsCache.skills`, `cacheDirComplete`, `cloneSkillMetaMap`, `processSkillsMeta`) into the 1039-line cache file.

Fix plan:
1. Delete the dead exports and their tests (tests of dead code are not coverage); keep the `Catalog`-based tests, which already cover discovery, resolution and loading.
2. Narrow `ScanSkillsDir` to an unexported helper or make `plugin.discoverPluginSkills` use `Discover` with `PluginSource` (it already exists for that purpose).
3. Drop `embeddedSkillsCache.skills`, `processSkillsMeta`, `embeddedProcessSkills`, `cloneSkillMetaMap`, and the unreachable retry loop; the cache only needs to return a directory.
4. Move the `unavailable`/`nilerr` empty-body promise out of the code (nothing calls it).

Size: about 250 lines of source and about 600 lines of tests removed. Risk: low (compile-checked deletion). Docs: `docs/skills.md` if it mentions the legacy discovery.

---

## agent-subpackages-7: Bundled-skills cache is about 1.6k lines of cross-process machinery for 124 KB of markdown (medium)

Locations: `agent/skill/builtin_skills.go` (1039 lines), `lease.go` (134), `lease_unix.go` (89), `lease_windows.go` (116), `lease_other.go` (10), `owner_unix.go` (81), `owner_windows.go` (54), `owner_other.go` (31), `base_root_*.go` (34).

Evidence:
- `du -sh internal/bundled/skills` is 124K across 14 files (doctoring-evener plus gardening-memory).
- `git log --oneline -- agent/skill/builtin_skills.go | wc -l` is 45. Recent subjects: "fix(skills): never recurse through cache links and reparse points", "fail closed without a verified cache root", "durable fallback copies and true exclusive leases", "revalidate the process copy and re-digest after claiming", "refactor(skills): delete the fallback tier and keep one process copy".
- It guards against "a hostile occupant of a shared cache name" (lines 27-31), checks ancestor-directory ownership and sticky bits, runs `flock`/`LockFileEx` leases to protect reapers, stages, publishes by digest, reaps staging and retained copies, and keeps a second process-lifetime extraction as fallback.
- Test code for the package is 4140 lines, largely for this cache.
- The reason a filesystem copy exists at all is that sessions read `SkillFile` paths (comment at line 71: "sessions read the SkillFile paths inside it").

Impact: very large maintenance drag per user-visible feature; each fix has produced another path. Most of the weight defends a multi-user shared-temp threat model for content that is public and immutable.

Fix plan (needs a decision from Jesse before any code):
1. Option A (simplest): extract once per process into a private `os.MkdirTemp` directory (mode 0700, owned by the caller by construction) and revalidate by re-stat only. Drops digest/publish/lease/reap/ownership code (roughly 1200 lines).
2. Option B: serve bundled skills from the embedded FS through the file-reading tool so no path is needed. Larger, touches read tool and sandbox roots.
3. Whichever is chosen, keep the one product contract: bundled skills survive an unusable temp root (recovery over lockdown).

Size: roughly -1200 source and -2500 test lines for option A. Risk: medium (sandboxed sessions read this path; verify with the sandbox read-roots test). Docs: the skills guide, `docs/product/subsystems.md` skill row.

---

## agent-subpackages-8: Command resolution is nondeterministic where skill resolution reports ambiguity (medium)

Locations: `agent/plugin/commands.go:126-137` (`ResolveCommand`), `agent/session_slash_command.go:59`; compare `agent/skill/discovery.go:72-94` (`Catalog.Resolve`) and `agent/skill/skills.go:105-125`.

Evidence: `ResolveCommand` falls back to "the unqualified suffix of each key" and its own doc says an unqualified name matching more than one plugin's command "resolves to whichever is found first in map iteration order". The skill resolver sorts candidates and returns `ResolutionError{Kind: "ambiguous", Candidates}`. The same user typing `/review` with two plugins that define it can get a different command on each run.

Impact: real bug class (nondeterministic behavior), and a third resolver variant beside the two skill ones.

Fix plan (TDD, behavior change):
1. Red test: two plugins with the same bare command; resolve 200 times; assert the same result or an ambiguity error.
2. Return `(Command, error)` with an ambiguity error listing qualified names; `session_slash_command.go` surfaces it as a user-visible slash result instead of guessing.
3. Share the "exact match, else single qualified-suffix match, else ambiguous" rule with `skill.Catalog.Resolve` via a small generic helper (`resolveQualified[T any](entries map[string]T, name string)`), placed in `agent/skill` or a leaf.

Size: about 40 added, about 30 removed. Risk: low. Docs: `docs/skills.md` (slash commands section) to state the ambiguity rule.

---

## agent-subpackages-9: `plugin` exports test-only API and duplicates manifest probing three times (medium)

Locations: `agent/plugin/plugin.go:230-290` (Load), `:296-322` (ManifestName), `:326-344` (LoadAll), `:365-405` (LoadAllFailSoft); `agent/plugin/skill_sources.go:11-37` (SkillSources); `agent/plugin/hooks.go:209-217` (parsePluginHooks), `:420-428` (discoverPluginHooks); `agent/plugin/settings.go:13-39` (LoadSettings).

Evidence:
- `plugin.LoadAll` and `plugin.LoadSettings`: zero non-test callers (callers are `agent/plugin_real_test.go`, `plugin_e2e_test.go`, `plugin_integration_*_test.go`, `plugin_settings_test.go`). `LoadSettings` reads `.claude/<plugin>.local.md`, a feature no production path consumes.
- `parsePluginHooks` and `discoverPluginHooks` are documented "thin wrapper ... for callers that do not need the diagnostics" and have callers only in `hooks_test.go`, `discover_test.go`, `loader_program_fuzz_test.go`.
- `Load` and `ManifestName` both do: `EvalSymlinks`, `pluginAbs`, stat `.claude-plugin/plugin.json`, fall back to `.codex-plugin/plugin.json`, read, `ParseManifest` (about 20 duplicated lines). A third batch loop in `SkillSources` repeats `EvalSymlinks`+`Abs` yet again and repeats first-manifest-wins like `LoadAllFailSoft`.
- Three "seen name -> dir" loops: `LoadAll`, `LoadAllFailSoft`, `SkillSources`.
- `internal/plugins/manifest_fallback.go:24` documents that it mirrors the same `.claude-plugin` first then `.codex-plugin` rule.

Impact: dead production surface that tests pin; the flavor-probing rule lives in at least three files.

Fix plan:
1. Add `resolveManifest(dir) (resolvedDir, path, flavor string, Manifest, error)` and make `Load`, `ManifestName`, and `SkillSources` call it.
2. Add one `selectUnique(dirs, nameOf)` used by `LoadAllFailSoft` and `SkillSources` for first-wins plus skipped/diagnostic bookkeeping.
3. Delete `LoadAll`, `LoadSettings`/`Settings`, `parsePluginHooks`, `discoverPluginHooks`; move the affected tests to `LoadAllFailSoft` and `parsePluginHooksDiag` (keep what is behavior, drop what only checks the wrapper).
4. Check `internal/plugins/manifest_fallback.go` can call `resolveManifest` through an exported `plugin.ManifestPath(dir)`.

Size: about 60 added, about 190 removed. Risk: low. Docs: none (no product change); `docs/skills.md` if it mentions `.local.md` settings.

---

## agent-subpackages-10: One malformed plugin file discards the whole plugin's agents, commands, hooks and MCP (medium, product-relevant)

Locations: `agent/plugin/plugin.go:230-290` (Load returns on first component error), `agent/plugin/plugin.go:365-405` (LoadAllFailSoft skips whole plugin), `agent/plugin/commands.go:85-120`, `agent/plugin/agents.go:196-224`, `agent/skill/description.go:17-115` (skills do recover).

Evidence:
- `discoverPluginCommands`/`discoverPluginAgents` return on the first unreadable or unparsable `.md` file (`parsing command file %q: %w`), and `Load` wraps it `in plugin at %q` and returns `Instance{}`.
- `LoadAllFailSoft` then records `SkippedPlugin` and drops the plugin entirely. Skills still load through `SkillSources`, which deliberately uses manifests alone.
- Skills with the same class of defect (`description: foo: bar`) are recovered with a diagnostic by `parseSkillDocument`; commands and agents use bare `frontmatter.Parse` with no recovery.
- AGENTS.md: "Prefer ... continued use of healthy parts of the product."

Impact: one typo in one command file in a third-party plugin silently removes every other command, agent, hook and MCP server of that plugin, with only a skip warning.

Fix plan (TDD, behavior change; discuss with Jesse first because it is a product choice):
1. Red tests: plugin with one good and one bad command file loads the good command and returns a warning; same for agents, hooks file, and MCP file.
2. Make component discovery collect `[]events.WarningData`-style diagnostics per file (the `MCPConfigWarnings` field already does this for MCP) and keep going.
3. Apply `parseSkillDocument`-style recovery for the description colon case through the shared `frontmatter` package so commands and agents get it too (see finding 18).
4. Update `docs/product/subsystems.md` plugin row (recovery ownership) and `docs/skills.md`.

Size: about 120 lines changed. Risk: medium (new warning surface in hub catalog).

---

## agent-subpackages-11: `events` double-registers kind and payload; `emit(kind, data)` repeats it 252 times (medium)

Locations: `agent/events/eventdata.go:15-40` (`New` derives Kind from payload), `:42-104` (62 `eventKind()` methods), `:106-163` (50 `_ EventData = X{}` assertions); `agent/events/events.go:197-226` (`ToStreamEvent`); `agent/session_events.go:231-247,253-263,378-390`; `agent/diagnostics.go:72-100`; pointer arms in `agent/job_watch.go` (7), `session_events.go` (2), `diagnostics.go` (2), `summary_diagnostics.go` (1).

Evidence:
- The `EventData` doc says Kind is derived from the payload "so Kind and payload can never disagree". Yet `Session.emit(kind events.EventKind, data events.EventData)` takes both, 252 call sites read `s.emit(events.EventX, events.XData{...})`, and `sendEventContext` calls `events.New(data)` which ignores `kind`. `kind` is then used only for `enrichDiagnosticData(kind, data)` and `kind == events.EventWarning`. A mismatched kind would enrich one payload type and tag another.
- The 50-line `_ EventData = X{}` block repeats what the `eventKind` methods and `New(data EventData)` callers already enforce at compile time.
- 12 `case *events.XData:` arms exist, but no producer emits a pointer payload (`grep 'emit([a-zA-Z.]*, &events\.'` is empty); the value-receiver methods let pointers satisfy the sealed interface, which is how the dead arms arose.
- `SessionEvent.ToStreamEvent` has callers only in `agent/sdk_alignment_test.go`; `events` imports `llm` solely for it.

Impact: every emit site carries redundant text, mismatches are silently possible, and consumers write two-arm type switches.

Fix plan:
1. `emit(data events.EventData)`, `emitWithProvenance(data, p)`, `sendEvent(data, p)`; derive kind via an exported `events.KindOf(data)` (or a method on the interface used by the agent package). Mechanical rewrite with `gofmt -r`/sed over 252 sites.
2. Delete the 12 pointer arms and the sealed-set value-only guarantee note; add an `events` test that no pointer payload satisfies a documented "value-only" check, or switch methods to pointer receivers on a few types if pointers are genuinely wanted.
3. Delete `ToStreamEvent` and its tests, and the `llm` import.
4. Delete the `_ EventData` block (keep one test enumerating all payloads via `New` if exhaustiveness is wanted).

Size: about 250 call-site lines edited, about 150 lines removed. Risk: low-medium (mechanical, compile-checked). Docs: `agent/events` package doc.

---

## agent-subpackages-12: Constant-true capability methods, and a magic-number timeout override (medium)

Locations: `agent/provider/profile.go:256-265`; `agent/session_init.go:356-359`; `agent/session_config.go:966`; `agent/session_stream.go:170`; `agent/session_tool_round.go:134`; `agent/session_model_call.go:421,468,1277`.

Evidence:
- `func (p *Profile) SupportsParallelToolCalls() bool { return true }`, `SupportsStreaming() bool { return true }`, `DefaultCommandTimeoutMS() int { return 120_000 }`. Docs: "Every protocol the agent drives does." Production branches still test them (`if profile.SupportsStreaming() {` at `session_stream.go:170` wraps a 100-line streaming path; `session_tool_round.go:134`).
- `session_init.go:357`: `if profileTimeout := profile.DefaultCommandTimeoutMS(); profileTimeout > 0 && cfg.DefaultCommandTimeoutMS == 10_000 { cfg.DefaultCommandTimeoutMS = profileTimeout }`. `applyDefaults` sets the default to the literal `10_000` (`session_config.go:967`), so the code detects "user did not set it" by comparing to the default value. A user who explicitly sets `default_command_timeout_ms` to 10000 gets 120000 instead. That is a latent bug caused by a sentinel instead of a presence check.

Impact: a correctness hazard plus dead capability plumbing that implies per-provider variance that does not exist.

Fix plan:
1. Red test: `SessionConfig{DefaultCommandTimeoutMS: 10_000}` explicitly set stays 10000 after init.
2. Make the default itself 120_000 in `applyDefaults` (one number in one place) and delete `Profile.DefaultCommandTimeoutMS` and the override block.
3. Delete `SupportsParallelToolCalls` and `SupportsStreaming`; inline the always-taken branches. If the non-streaming `Complete` path (line 268) remains only as an unavailable-stream fallback, keep that fallback explicit.
4. Remove the corresponding profile tests (`profile_test.go:43,56,66,750,1238`, `profile_overrides_test.go:236,245`, `provider_covtest_test.go:10`, fuzz lines 71-74).

Size: about 70 lines removed, 10 added. Risk: low. Docs: `docs/skills.md`/config reference if `default_command_timeout_ms` default is documented as 10s.

---

## agent-subpackages-13: `SessionConfig` and `schema.ConfigSnapshot` are mirrored field-for-field with hand converters and a hand `Clone` (medium)

Locations: `agent/schema/config_snapshot.go:13-70`; `agent/session_config.go:1001-1090` (`toSnapshot`, `configFromSnapshot`).

Evidence: `ConfigSnapshot` has 34 fields; `toSnapshot` and `configFromSnapshot` are each a 34-line literal copying every field (`MemoryProjectID: c.MemoryProjectID`, ...). `Clone` then hand-deep-copies 9 of them (slices, map, two `*bool`). The comment says "The field set mirrors schema.ConfigSnapshot exactly; the converter round-trip test guards against any field being dropped".

Impact: adding one persisted setting needs edits in four places (SessionConfig, ConfigSnapshot, two converters) plus `Clone` for reference types; a missed `Clone` arm silently aliases state between sessions.

Fix plan:
1. Embed `schema.ConfigSnapshot` in `SessionConfig` (`json:",inline"` behavior via anonymous struct embedding), so the persisted fields live once. Engine-only fields stay on `SessionConfig`.
2. Delete `toSnapshot`/`configFromSnapshot`; callers use `cfg.ConfigSnapshot` and `SessionConfig{ConfigSnapshot: s}`.
3. Replace the hand `Clone` with `reflect`-free per-type clones guarded by a reflection test that fails when a new slice/map/pointer field is not deep-copied (walk the struct type; assert distinct backing arrays after `Clone`).

Size: about 70 lines removed, 30 added; touches every `cfg.Field` access only if field promotion is not used (it is). Risk: medium (JSON tags and `omitempty` behavior on the embedded struct must remain; the existing round-trip test covers it).

---

## agent-subpackages-14: `schema` mixes wire types with a locking filesystem persistence engine; `SessionMeta` has 44 fields (medium)

Locations: `agent/schema/snapshot.go:1-893`, `agent/schema/snapshot_flock_*.go`, `snapshot_meta_open_*.go`, `boot_generation.go`; `SessionMeta` at `snapshot.go:141-286`.

Evidence:
- `snapshot.go` is 893 lines and contains: session-ID validation (Windows reserved names), a 64-stripe in-process write lock, a cross-process flock, tombstones (`TombstoneSessionMeta`, `UntombstoneSessionMeta`), `O_NOFOLLOW` component walks (`metaComponentWalk`, `readMetaFile`, `wrapperSourceIsOS`), and directory listing. The package name and `turn.go`/`skill_lifecycle.go` suggest "pure types".
- `SessionMeta` has 44 JSON fields across 145 lines of struct; many carry paragraph comments about specs ("S4", "S1d", "native worktree tools spec §7") and "metas written before the field existed" compat notes.
- Both `SaveSessionMeta` and `SaveSessionMetaWithFS`, `LoadSessionMeta` and `LoadSessionMetaWithFS`, `ListSessionMetas` and `ListSessionMetasWithErrors` exist as non-FS/FS pairs.

Impact: every package that wants a type (even `doctor`, `events`) pulls in the persistence engine; the meta struct is a catch-all where unrelated subsystems append fields.

Fix plan (architecture; discuss before doing):
1. Move persistence to `agent/sessionmeta` (store: `Save`, `Load`, `List`, `Tombstone`, locks) and leave only `SessionMeta` and its sub-structs in `schema`.
2. Group `SessionMeta` fields into embedded sub-structs by owner (`WorktreeState`, `ScratchState`, `JobTreeState`, `NameState`) with JSON inlining, so the wire format is unchanged but ownership is visible.
3. Collapse the non-FS/FS pairs to the FS form with a package-level default `afero.OsFs` wrapper.

Size: about 900 lines moved, no net deletion; value is ownership. Risk: medium (import churn across about 60 files). Docs: `docs/product/subsystems.md` session-state row.

---

## agent-subpackages-15: `transcript.Writer` exposes four append doors and extra openers that only tests use (low-medium)

Locations: `agent/transcript/transcript.go:590-660` (`Append`, `AppendDurable`, `AppendSynced`, `AppendBatch`), `:1069-1100` (`OpenWriter`, `OpenWriterForSessionWithFS`).

Evidence: production writes all go through `Writer.Record(turn, RecordOptions{Door, Place})` (`agent/session.go:2507`, `session_attention.go:422,625,1933,1980`). Callers of the wrapper doors outside the package, excluding tests: `Append` 0, `AppendDurable` 0, `AppendSynced` 0, `Writer.AppendBatch` 0, `OpenWriter` 0, `OpenWriterForSessionWithFS` 0. Test callers: `AppendDurable` 79, `AppendBatch` 86, `AppendSynced` 13, `OpenWriter` 7, `OpenWriterForSessionWithFS` 17. The `AppendBatch` doc still says "No caller until A2's fold; kept here as the exported entry to the one write primitive" (a stale history comment). The 30-line contract comment lives on `Append`, an API nobody in production calls.

Impact: the contract is documented on the wrong function; a reader looking for how to write sees four doors, and only `Record` is real.

Fix plan:
1. Move the contract comment to `Record`/`Door`.
2. Move the convenience wrappers into `agent/transcript/transcripttest` (`transcripttest.Append(w, turn)` etc.) or an `export_test.go` for in-package tests; update tests mechanically.
3. Delete the stale "A2's fold" comment and the unused openers.

Size: about 80 lines moved, about 40 removed, about 240 test lines touched mechanically. Risk: low.

---

## agent-subpackages-16: Hub-failure classification keyword list is duplicated across Go and TypeScript, held together by a parity test (medium)

Locations: `agent/diagnostic/diagnostic.go:263-300` (`HubFailureKeywords`, `isHubFailure`), `:309-338` (`isProviderFailure` keyword fallbacks); `cmd/evener-hub/frontend/src/panes/session/transcript/turnFailure.ts:26-38` (`RECONNECT_KEYWORDS`); `cmd/evener-hub/diagnostic_keyword_parity_test.go:29-60`.

Evidence: the Go doc says the web client "runs the same vocabulary over the same messages ... and the two lists had already drifted apart in both directions by the time anyone noticed. They are held together now by TestHubFailureKeywordsMatchWebClient". `FromError` already prefers structured categories (`llm.ConfigurationError`, `llm.KindQuotaExceeded`), then falls back to lowercase substring matching on `err.Error()`. AGENTS.md: shared client owns shared derivations.

Impact: classification by English substrings is fragile and duplicated; the TS side re-derives what the agent could stamp on the event.

Fix plan:
1. Add a typed field on `events.ErrorData` (for example `Recovery` with values `retry`, `reconnect`) stamped at the emitter, derived once in `diagnostic.Info`.
2. `appwire` carries it to the client; `turnFailure.ts` reads it and deletes `RECONNECT_KEYWORDS`.
3. Delete the parity test once the TS list is gone; keep keyword fallback in Go only for legacy persisted messages if Jesse approves the compat (otherwise remove).

Size: about 60 lines added, about 90 removed across Go and TS. Risk: medium (appwire schema change, generated TS types; touches the shared client). Docs: appwire protocol doc, `docs/product` friction note if relevant.

---

## agent-subpackages-17: `doctor.classifyAPIErrorClass` re-states `llm.Classify`'s status table (low)

Locations: `agent/doctor/apilog.go:449-477`; `llm/classify.go:57-106` (cases at 85-90).

Evidence: both have `case 400, 401, 403, 404, 413, 422:` permanent and `case 408, 429, 500, 502, 503, 504:` retryable, plus the quota special case. The doctor copy exists because it works from logged status codes, not an `error`, and its comment says it "mirrors Classify's own conservative fallback".

Impact: a new status code added to `llm.Classify` will not change the doctor's verdict.

Fix plan: add `llm.ClassifyStatus(status int) (ErrorClass, bool)` and have both call it; doctor keeps only its string-class mapping.

Size: about 15 added, about 12 removed. Risk: low.

---

## agent-subpackages-18: Frontmatter recovery lives only in `skill`, and is about 150 lines of YAML paranoia (low-medium)

Locations: `agent/skill/description.go:17-150`, `agent/skill/metadata.go:14-72` (`recoverMissingOpeningDelimiter`, `looksLikeFrontmatterStart`), `agent/plugin/commands.go:39`, `agent/plugin/agents.go:46`, `agent/internal/frontmatter/frontmatter.go:72`.

Evidence: `parseSkillDocument` re-parses to a `yaml.Node`, checks key uniqueness through aliases (`recoveryMappingKeysUnique`), re-quotes one scalar via `json.Marshal`, and verifies EOF and no extra documents. Only skills use it; `ParseCommand`/`ParseAgent` and the memory pages call `frontmatter.Parse` directly. `Parse` returns `(Descriptor, []Diagnostic, error)` where diagnostics and error describe the same failure, and callers discard one of the two (`discovery.go` `d, diagnostics, _ := Parse(...)`; `parseSkillFile` drops diagnostics).

Impact: valuable behavior (recovery) trapped in one consumer, written defensively enough to be hard to audit.

Fix plan:
1. Move `recoverMissingOpeningDelimiter` and the unquoted-description recovery into `agent/internal/frontmatter` as `ParseRecovering(raw) (Document, []Recovery, error)`.
2. Skills, commands, agents use it (this is the shared piece finding 10 needs).
3. Simplify: a single "quote the one top-level `description:` scalar and retry" path with a smaller guard list; drop `recoveryMappingKeysUnique` by letting yaml.v3 strict-decode reject duplicate keys.
4. Make `Parse` return diagnostics only, with `Unavailable` conveying failure, or return only an error type that carries the diagnostics.

Size: about 150 moved, about 60 removed. Risk: low-medium (fuzz tests exist: `fuzz_ap_skills_test.go`, `program_fuzz_test.go`).

---

## agent-subpackages-19: Root-to-cwd project walk and the user-skills gate are copy-pasted (low)

Locations: `agent/skill/discovery.go:229-249` (`projectSkillDirs`), `agent/plugin/evenerwide.go:51-70`, `agent/project_docs.go:70-77`; `agent/mcpconfig/config.go:292,306-314`; gate `envvars.EVENERNoUserSkills.Getenv() != "1"` at `agent/session_init.go:1709`, `agent/mcpconfig/config.go:292`, `agent/plugin/evenerwide.go:48`, and `agent/sandbox/probe.go:347`.

Evidence: three sites do `cwd := strings.TrimSpace(env.WorkingDirectory())`, `EvalSymlinks`, `root := cwd; if gr := execenv.GitRootOrEmpty(env, cwd); gr != "" { root = gr }`, then `execenv.DirsFromRootToCwd(root, cwd)`. Four sites compare the env var to the literal `"1"`.

Impact: small; but each new "per-project config" feature copies it again (this is how `.evener/commands` and `.agents/skills` got there).

Fix plan: add `execenv.ProjectDirChain(env) []string` (empty on nil env or blank cwd) and `envvars.EVENERNoUserSkills.Enabled()` (or a `userdirs.UserSourcesDisabled()`); replace the sites.

Size: about 25 added, about 40 removed. Risk: low.

---

## agent-subpackages-20: Package-level mutable seams in production code, used by a single test (low)

Locations: `agent/plugin/plugin.go:20-29` (8 vars: `pluginAbs`, `pluginReadFile`, `pluginReadDir`, `pluginStat`, `pluginDiscoverAgents`, `pluginDiscoverCommands`, `pluginDiscoverHooks`, `pluginDiscoverMCP`); `agent/doctor/jobs.go:139` (`delegateJournalRead = func(path string) {}` no-op hook called from production at `:234`); `agent/doctor/transcript.go:20`; `agent/doctor/statedir.go:10`; `agent/doctor/locate.go:15`; `agent/plugin/evenerwide.go:17`; `agent/skill/builtin_skills.go:76,80,88`; `agent/task/task_store.go` `beforeMutationPublicationWaitHook`.

Evidence: the plugin vars are swapped only in `agent/plugin/loader_program_fuzz_test.go:591-645`. `delegateJournalRead` is a no-op function executed in production solely so a test can count reads. The doc on the task hook says "a test that installs it must not run in parallel".

Impact: forces serial tests, adds indirection to every file operation in `Load`, and invites more seams.

Fix plan: replace with an `fs.FS`/`afero.Fs` parameter or a small options struct where an injection is truly needed (the repo already uses `afero.Fs` in `schema` and `task`); for `delegateJournalRead`, count reads via the existing `delegatestore` call or a counting `afero.Fs`. Delete the four `pluginDiscover*` seams: the fuzz test can inject faults through the filesystem.

Size: about 60 lines removed, tests adjusted. Risk: low-medium (fuzz harness changes).

---

## agent-subpackages-21: `diagnostic`, `doctor` and `skill` tests and docs contain stringly-typed categories (low)

Locations: `agent/skill/metadata.go:85-93` (`Diagnostic.Category` strings: `"invalid_metadata"`, `"unreadable_source"`, `"collision"`, `"missing_frontmatter_delimiter"`, `"unquoted_description"`, `"allowed_tools_not_enforced"`, `"unsupported_control"`, `"invalid_control"`, `"invalid_frontmatter"`); `agent/mcpprobe/mcpprobe.go:67-73` (`Status` strings `"available"/"unreachable"/"missing"`); `agent/doctor/health.go:36-47` (`class` strings).

Evidence: categories are repeated as literals in `metadata.go`, `discovery.go`, `skill_sources.go` (`"unreadable_source"`, `"collision"`).

Impact: typos compile; consumers cannot enumerate valid categories.

Fix plan: define `type DiagnosticCategory string` with exported consts in `skill`, and `type Status string` in `mcpprobe`; mechanical replace.

Size: about 40 lines. Risk: low.

---

## agent-subpackages-22: Doctor presentation helpers duplicated and legacy-state reporting carried (low)

Locations: `agent/doctor/mutations.go:322-333` (`plural`), `agent/doctor/turnids.go:210-215` (`verb`), `agent/doctor/watches.go:365-371` (`dash`), `agent/doctor/jobs.go:398-412` (`jobTime`, `jobTimePtr`), `agent/doctor/health.go:347-366` (`renderCounts`, `sortedKeys`), `agent/doctor/jobs.go:276-308` (`legacyDelegateFailures`).

Evidence:
- Small formatting helpers each defined once per file; `dash` and the `"-"` fallbacks recur in `jobs.go`, `watches.go`, `health.go`. `sortedKeys` is `slices.Sorted(maps.Keys(m))` (the module already uses `slices` elsewhere, e.g. `discovery.go`).
- `legacyDelegateFailures` reports retired state `legacy_delegate_state` / `legacy_delegate_watch_state`. The agent refuses such state at runtime (`agent/delegate_legacy_state.go:22`), so the doctor repeats a compat detector for a state the runtime no longer accepts. AGENTS rule: no new backward compat without approval.

Impact: low; clutter plus an unreviewed compat path.

Fix plan: collect the formatting helpers into `doctor/render.go`, replace `sortedKeys` with `slices.Sorted(maps.Keys)`; ask Jesse whether `legacyDelegateFailures` stays (it does explain a failure to a user holding old state) or goes with the runtime's refusal.

Size: about 40 lines moved/removed. Risk: low.

---

## agent-subpackages-23: `envctx` stores rendered prose as state and parses it back (low)

Locations: `agent/envctx/envctx.go:18-24` (`Pressure` fields hold rendered lines such as `"load pressure: 12.3 (4 cores)"`), `:56-101` (`ReplayBlock`), `:116-157` (`RenderDiff`), `:103-114` (`pressureValue`).

Evidence: the render format and its parser are separate hand-maintained halves; `Pressure.Load` is the entire rendered line, so `ReplayBlock` stores `line` verbatim (`pressureValue` returns `line`, not the value) and RenderDiff compares rendered strings. A change to any label (`"git branch: "`, `"(not in a git repository)"`) needs both halves plus the persisted `session meta` values.

Impact: low today (tests exist), but model-visible text doubles as persisted state, so a wording tweak invalidates resumed sessions' silence guarantee.

Fix plan: make `Pressure` hold structured values (`LoadAvg float, Cores int; DiskFrac`) and render in one place; keep `ReplayBlock` only if the crash-window repair truly needs text parsing, else journal the structured snapshot beside the block. Needs Jesse's decision (the spec doc `2026-08-06-environment-context-design.md` ties text to the cache-safety goal).

Size: about 60 lines changed. Risk: medium (persisted format). Defer unless the area is next touched.

---

## agent-subpackages-24: Small unused or narrating items (low)

Locations: `agent/task/task_store.go:554-561` (`Progress`; test-only callers `store_crud_fuzz_test.go:248`, `store_fault_program_fuzz_test.go:124`), `:545` (`Summary` method has no non-test caller; production uses package-level `Summarize`), `:236-251` (`SetClock`/`SetFs` used by tests only); `agent/mcpprobe/mcpprobe.go:2-4` ("It replaces the orphaned cmd/evener-hub/internal/mcpstatus ..."); `agent/doctor/statedir.go:17-19`; `agent/events/events.go` (`kata v3dv`, `kata 7vmd` references in doc comments); `agent/argrepair/argrepair.go:1-33` (pure re-export, `RepairJSON` drops the second return).

Evidence: see quotes above. `argrepair` has 5 production callers (`cmd/evener-hub/output_images.go:43`, `internal/apptranscript/*`) and no tests of its own, so it is a legitimate visibility shim; flagged only as one more hand-written pass-through.

Impact: minimal; history comments belong in commit messages per CLAUDE.md.

Fix plan: delete `Progress`, `Summary` and test-only accessors where tests can use `Summarize(store.View())`; drop history sentences from the three doc comments; leave `argrepair` alone.

Size: about 40 lines removed. Risk: low.

---

## Recommended fix sequence for this slice

Order is by safety and by what unblocks later work.

1. Deletions with compile-checked safety (low risk, large payoff): findings 6, 9 (dead half), 15, 24, and the `ToStreamEvent` / pointer-arm / `_ EventData` parts of 11, plus the constant methods in 12. One PR each for `skill`, `plugin`, `transcript`, so reviews stay focused.
2. Behavior bug fixes with red tests: finding 12 (timeout sentinel), finding 8 (nondeterministic command resolution). Small PRs.
3. Shared-helper extractions (mechanical, many call sites): finding 2 (layout helpers), finding 4 (state home), finding 5 (truncation), finding 19 (project dir chain), finding 3 (transcript scanner). Do 2 and 4 first; they touch the doctor, which makes finding 1 easier.
4. `RunAudit` restructure (finding 1) after layout helpers land, since `pathsFor` and selector logic shrink first.
5. `emit` signature cleanup (finding 11 main part) as one mechanical PR when the agent package has few in-flight branches.
6. `SessionConfig`/`ConfigSnapshot` embedding (finding 13), then the `schema` persistence split (finding 14) as a separate architecture PR agreed with Jesse.
7. Product-affecting items to discuss first: finding 10 (per-file plugin fail-soft plus shared recovery, with 18), finding 7 (bundled skills cache simplification: option A vs B), finding 16 (typed reconnect hint replacing duplicated keyword lists), finding 22 (legacy doctor detector), finding 23.
8. Low-value cleanups (17, 20, 21) opportunistically when files are touched.

Every behavior-changing item (8, 10, 12, 16) is red-test-first; all others are behavior-preserving refactors guarded by the existing suites, run in CI rather than locally.
