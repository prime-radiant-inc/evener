# iPhone redesign, Phase 7: why lines on rows and remote task progress (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Every task carries its code, so every task's implementer is Sonnet; the reviewer checks it against this plan and the code as it is on main.

**Goal:** A Board row says why it is there. A row asking a question names it (S1b), a Failed row says what failed (S1c), a Finished row opens with the agent's last words (S1d), and a session on another host shows its task line like a local one (S13b).

**Architecture:**
- **One excerpt rule (PR 7).** `appwire.Excerpt` turns any text into one short line: whitespace and control characters collapse to single spaces, invalid UTF-8 is repaired, and text past its bound is cut at a word break with an ellipsis. The daemon cuts every why value with it before the value leaves the session; the hub cuts again on projection, and its schema accepts exactly what the cut yields.
- **S1b (PRs 7-8).** The agent keeps each pending question's option labels. The thread envelope samples the first pending question and the ask flag in one call, so `EvenerThread.pendingQuestion` is present exactly when `askPending` is. The hub carries it to a `question` record on rows, named only while the row's ask flag is set.
- **S1c (PR 9).** The agent summarizes the failed turn it rests on (the classifier's title and the structured cause, never the message) under the lock and the condition `RestingWireState` uses. Thread snapshots carry the summary only while the status is systemError. Rows carry a `failure` record; a crashed daemon's rows carry the hub's own `crashed` cause.
- **S1d (PRs 10-11).** The session records the opening of each agent message as it writes it (an assistant text part, or a delivered communicate message) and persists it in `SessionMeta.LastMessage`. It rides the envelope's meta facet. Rows carry `last_message`: a live session's from its probe, an ended session's from its meta, a subagent row none.
- **S13b (PR 13).** The remote hub's list rows carry `tasks`, and the controller reads them, the way PR 5 (#2513) carried questions and approvals.

**Tech Stack:** Go 1.27 workspace (root module and `agent/`), AppWire over WebSocket (`ProtocolVersion` `"evener-appwire-v5"`), TypeScript 6 in `appwire-client/typescript` tested by vitest from `cmd/evener-hub/frontend`.

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`: 7.1 (the Board's example rows), 7.2 (row anatomy: the why line and the last line), 7.3 (the long-press preview), 8.4 (the question dock), 13.1 (the Failed, Question and Finished why lines), 13.2 (subagent failures never mark a coordinator's row), 13.4 (what notification payloads carry), 17 and 18 (S1, S13). The server plan `docs/superpowers/plans/2026-09-26-iphone-redesign-server-additions.md` holds the design-level sections this plan replaces and the PR map (these are its PRs 7 to 11 and 13). Jesse's answers: `.superpowers/research/2026-09-26-jesse-answers.md`, answer 12 in particular. Written and dry-run against main at `1c9cb464a`, after S1a (#2526), S13a (#2502), S2b (#2513), S4 (#2566, #2584), S3 (#2570, #2587) and S5 (#2582, #2593) merged, and replayed onto main at `8d671c559` (see Self-review).

## Global Constraints

- **Wire.** Every change is additive and stays on `ProtocolVersion = "evener-appwire-v5"` (`appwire/types.go:25`); the navigation read stays `representationVersion: 2`. Every new field is an optional key (`omitempty`), absent unless it carries a fact (Jesse's standing rule: no flag day for an additive wire change).
- **Never add a `FeatureSet` key.** The TypeScript `initialize` decoder refuses unknown feature keys (`appwire-client/typescript/client.ts:146-193`). A client learns a new row field from its presence.
- **Casing.** `hubapi` navigation JSON is snake_case (`last_message`, `cause_kind`); `appwire` JSON is camelCase (`pendingQuestion`, `lastMessage`). The tagliatelle lint enforces both (`.golangci.yml`, and `.golangci-appwire.yml` for `server/`).
- **A new navigation summary field moves together.** In one commit: the `hubapi` struct field, `projectShallow`, a validity predicate in `cmd/evener-hub/navigation_schema.go` that the projector also calls (so a value the schema refuses is dropped from its row and never fails the resource, as `navigationTaskProgress` does), or for a plain string a schema check the projector's cut always satisfies (Task 11.2), `cloneNavigationSummary` for a pointer field, the codec's `SESSION_KEYS` (plus a nested `valueRecordKeys` for a record) and its check in `sessionValue` (`appwire-client/typescript/state/navigation/codec.ts:164-197`, `:310`), the shared fixture `cmd/evener-hub/testdata/navigation/value-records.json`, and `make generate`. `TestNavigationValueRecordFixtureNamesEveryWireField` (`cmd/evener-hub/navigation_value_records_test.go:24`) and the codec's "codec keeps every field the hub's value records carry" (`codec.test.ts:1349`) fail when one of them is missing.
- **Every string is bounded twice.** A why string is cut with `appwire.Excerpt` on the daemon and again on projection. The hub schema accepts it only when `appwire.Excerpt(value, bound) == value`: one trimmed line of valid UTF-8 within the bound. The codec bounds its length. Identities (`cause_kind`, `provider`) are bounded as S1a's `approval_tool` is: `truncateNavigationBytes` in the projector and `navigationSchemaIdentity` in the schema.
- **No secrets and no tool output on rows** (rulings 6, 12 and 20).
- **The TUI rule.** A change to what `internal/appprojector` emits needs a `cmd/evener-tui` case in the same PR. No PR here changes the projector or adds a notification: the new fields ride thread snapshots and navigation rows, and of these thread fields the TUI reads only `askPending` and `pendingEscalations` (`cmd/evener-tui/hub_types.go:244`, `:305-306`). So no PR needs a TUI case; one that finds itself editing `internal/appprojector` adds one.
- **Clones and fingerprints.** A new pointer or slice on `ProbeResult`, `LiveEntry`, `TreeNode` or a summary is deep-copied in `CloneLiveEntry` (`hubcore/roster.go:198`, which `liveEntryFromProbe` and `cloneNavigationLiveEntries` call), `cloneTreeNodesContext` (`hubcore/tree.go:114`), `cloneNavigationSummary` (`navigation_projection.go:2169`) and `appwire`'s `cloneEvenerThread` (`appwire/clone.go:90`), as it applies. A `LiveEntry` field that changes a row is hashed in `rosterFingerprint` (`roster.go:422`), or a change the status does not also move never invalidates navigation.
- **hubcore tests** are `fuzzScenario*` functions registered in `FuzzHubcoreScenarios` (`hubcore/scenarios_fuzz_test.go:8`), each placed right after its nearest alphabetical neighbour. Run them with `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`. An unregistered one never runs; `golangci-lint run ./cmd/evener-hub/internal/hubcore/` reports it unused.
- **Generated files.** After any `appwire` or `hubapi` type change, including a doc comment: `make generate`, then `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1` and `make lint-generated`. Commit `appwire-client/typescript/types.gen.ts` (in this plan `docs/appwire-protocol.md` never changes; commit it if it does).
- **Go floors,** per module touched (root, and `agent/` for PRs 7, 9 and 10): `go vet ./...`, `go vet -tags evenerfuzz ./...` and `GOOS=windows go vet -tags evenerfuzz ./...`. Format with `$(go env GOROOT)/bin/gofmt`. Run the pinned `golangci-lint run ./<package>/` on each touched Go package, and for `server/` also `golangci-lint run --config .golangci-appwire.yml ./server/...` (`make/linting.mk:125`).
- **TypeScript floors.** Biome only from `cmd/evener-hub/frontend`: `npx biome check --write <paths>` on touched files under `../../../appwire-client/typescript`. Never run Biome in `mobile-native` or from the repo root, and never `npm ci` through a symlinked `node_modules`.
- **Secret scan.** PRs 8, 9 and 11 change the fixture: run `make secret-scan`.
- **Targeted tests only.** Run each task's own tests and the gates it names. CI runs the full matrix; never run `make test-web`, `make test-native` or `make lint` locally.
- **Tests are deterministic.** Scripted providers at the LLM boundary, no sleeps, every agent test `t.Parallel()`, and every new test shown failing before its code lands.
- **Line numbers.** Outside the PR sections they are on main at `1c9cb464a`. In a PR's section they are on the tree that PR starts from: that main plus the PRs its Depends on column names, merged in the order 7, 8, 9, 10, 11 (PR 13 cites main alone). A merge from another lane moves them a few lines, so every step also names its anchor; find it by the name.
- **Size.** Measured on the dry run, production lines (tests, fixture and generated output excluded): PR 7 170, PR 8 139, PR 9 266, PR 10 78, PR 11 62, PR 13 15. Landing follows the roadmap: a regular PR, CI green at the head, the RoboRev comment read, /simplify run and its fixes pushed, then an admin squash merge.
- **Copy.** Wire names use the codebase's words (`cause_kind`, `provider`). What a row says belongs to the phone and web lanes and follows spec section 5; the handoff section below gives the facts.

## Rulings

Decisions the spec and the server plan leave open, with the reason for each.

**S1, every part**

1. **One excerpt rule, in `appwire`, used by the daemon and the hub.** `Excerpt(text, maxRunes)` turns each run of whitespace or control characters into one space, trims the ends, repairs invalid UTF-8, and cuts text past its bound to at most `maxRunes` runes ending in "…", at the last word break in the kept text's second half. The daemon cuts before a value leaves the session, so a thread snapshot never carries a paragraph; the hub cuts again on projection, so an older daemon or a remote host cannot widen a row; the schema accepts a value only when `Excerpt(value, bound) == value`. One function in the module both sides import means the two cuts can never disagree, and the fixed-point check says exactly what the schema allows. It reads only as much text as the excerpt needs, so a long message costs no more than a short one.
2. **The bounds.** Question text 200 runes; option labels 80 runes each, at most 5; failure title 80 runes; last message 200 runes. Spec 18 asks for "about 200 characters" of the last message (S1), and spec 7.2 gives a why line two lines (about 90 to 100 characters on a phone at 15pt), so 200 leaves the long-press preview room without shipping a paragraph. Five is `ask_user`'s own ceiling on options (`agent/internal/tool/definitions.go:1013-1017`), and a label names one choice in a few words. A failure title is a headline from a fixed vocabulary (`agent/diagnostic/diagnostic.go`). The constants live beside `Excerpt` in `appwire/excerpt.go`, and the hub's projection and schema use them directly.
3. **Every S1 value rides the thread envelope.** It is present on `thread/read` and `thread/list` alike and, like `lastTurnEndedAt`, snapshot-only: no notification carries it. The hub's five-second probe keeps it on `LiveEntry`, `BuildTree` puts it on `TreeNode` through one closure the three builders share, and `projectShallow` puts it on the summary. A remote host's sessions carry it on the remote hub's `thread/list` rows (`LocalDaemonEntry`, `threadFromEntry`), which the controller reads in `appThreadTreeEntries`: the pattern PR 5 (#2513) set.
4. **The codec checks lengths; the hub schema also checks the shape.** The codec holds each string to its bound and to being non-empty; the hub schema also refuses a line break, a leading or trailing space and invalid UTF-8 (the `Excerpt` fixed point). The hub's self-check stays stricter than the codec, as PR 1 set it: it validates what the hub produces.
5. **No TUI case.** See Global Constraints.
6. **Privacy.** Nothing a row carries is a secret or a tool's output. S1b carries the agent's `ask_user` question and option labels: words the agent wrote to the user, which the question dock shows anyway (spec 8.4). S1c carries no free text from the failure (ruling 12). S1d carries only the agent's own message text (ruling 20).

**S1b, the first pending question**

7. **The row names the first question of the pending set.** `askPending[0]` is the first question of the earliest pending call; `count` is `len(askPending)`, which can exceed four because several `ask_user` calls may share a round (`definitions.go:993`). The text is the question itself and leaves out its display header: spec 13.1's why line is "Asks: <first question>", and spec 7.1's example row reads "Question · keep or drop the implied options?".
8. **The flag and the question are one read.** `ThreadEnvelopeSource.AskPending() bool` becomes `PendingQuestion() *appwire.PendingQuestion`, and the envelope's `AskPending` is the question's presence, so the ask facet stays what `server/thread_envelope.go` requires: "The grouping follows what one call to the source returns, so a facet can never be half-written." `agent.EnvelopeSampling` swaps `HasPendingAsk` for `PendingQuestion` the same way; `HasPendingAsk` stays on `Session` for `cmd/evener/serve.go:1733`. The server's test stub keeps its `askPending` flag, which now reports a question with no text, so the tests that only set the flag keep their meaning.
9. **A row names the question only while its ask flag is set.** `pendingQuestionFor` answers only when `LiveEntry.PendingAsk` is true, as `firstApprovalFor` does for the approval detail (RoboRev's Medium on #2526). An empty label is left out; a question with no text left after the cut, or with no question counted, is dropped from the row, and the row keeps `ask_pending` and the phone's fallback.
10. **The fingerprint hashes the question.** A question answered and another asked between two probes holds `Status` at awaiting and `PendingAsk` at true, and moves only the text.

**S1c, the failure summary**

11. **The summary is the failed turn the session rests on.** `restingFailureLocked` reads state, the pending asks and the history under one hold of `s.mu`, the condition `RestingWireState` publishes systemError on (`agent/session_state.go:94-102`), and returns the TurnFailure the history ends in (`historyTurnFailure`, which `historyEndsInTurnFailure` becomes). So the summary exists exactly while the daemon reports Failed, and a restored session, which rebuilds all three from its transcript, summarizes the failure the live one did. A failure recorded with no diagnostic (a legacy entry) has no summary; the row still reads Failed.
12. **It carries the title and the structured cause, never the message.** A turn failure's message is the raw error text (`agent/session_events.go:292-314`), and a provider's error body can quote what the provider echoed. The llm package strips credential material only from API logs (`llm.SanitizeErrorForAPILog`, `llm/api_attempt_sanitize.go:283`), never from the transcript's copy, and a row goes further than the transcript: the Board, its on-device cache, and phase 2's notifications. The structured cause is enough for the spec's example: provider and status give "codex-jesse-fsck.com sign-in expired (401)" (spec 7.1). The message stays one tap away in the transcript's error row. For a failure evener raised itself (an exhausted retry budget, a skill that could not route) the title is a generic "Evener error"; that is the cost of the rule, and the phone says what it can.
13. **A facet the checkpoints sample, gated by the status.** `facetTurnFailure` joins `facetAll` and no single-event row. A failed turn always ends with TURN_ENDED, which samples every facet (`server/thread_envelope.go:188-203`: "the failed-turn return path emits ONLY this event"), and the snapshot shows the summary only while the status it is published beside is systemError. The next turn moves the status first, so a summary sampled before that turn started is never read.
14. **The hub names a crash itself.** A crash-retained entry (`Crashed`) keeps the last snapshot its daemon reported (`roster.go:834-843`), including any summary, but its process is gone. Its rows carry the hub's own cause, `cause_kind: "crashed"` (`hubapi.NavigationFailureCrashed`), so the phone can say the session stopped rather than repeat a stale reason. A remote hub skips crashed entries in its list (`localDaemonEntriesFromRoster`), so `crashed` is always the controller's own word.
15. **A coordinator's row names only its own failure** (Jesse's answer 12: "the coordinator row should only be red if the coordinator failed"). The summary comes from the coordinator's own history, and `failureFor` reads only the session's own live entry, so a failed subagent never lends its failure to the row; the in-process aliases a hub lists carry none.
16. **The fingerprint hashes the summary.** A turn retried and failed again between two probes holds `Status` at systemError and moves only the reason.

**S1d, the last agent message**

17. **The last agent message is the last `agentMessage` the transcript shows.** That is an assistant response's last text part that says something (`internal/apptranscript/apptranscript.go:604-615` projects each text part as its own agent message) or a delivered communicate message (`internal/appprojector/appwire_projection.go:638-660`). The session records it as it writes each one: inside `appendAssistantTurn`'s append, under `s.mu`, and in `deliverCommunicate` just before it announces the message, past its refusals. Tracking it at the write means compaction can never fold it away, which a scan of history at read time could not promise. A preamble the model writes mid-turn is an agent message too; the Finished row shows the excerpt only after the turn ends, by which time the turn's own last message has replaced it.
18. **It persists in `SessionMeta.LastMessage` with the next meta save.** processInput's exit save writes it right after the turn ends; retirement and `Close` save it too, as they do `LastTurnEndedAt` (S4 ruling 11). Restore seeds it, so a restarted daemon's Finished row still says what it finished with.
19. **It rides the meta facet.** No new `ThreadEnvelopeSource` method: `SessionMeta()` already carries it. It can move mid-turn and lag there until TURN_ENDED re-samples every facet, which is exactly when a row starts to show it.
20. **What the excerpt carries, against how the transcript redacts.** The transcript's projection shows an assistant response's text parts verbatim as `agentMessage` items, its reasoning as separate `reasoning` items, encrypted reasoning only as "[redacted thinking]" (`apptranscript.go:627-634`), and tool calls and their output as `commandExecution` items. It scrubs nothing from message text (ruling 12). So the excerpt carries only the opening of the agent's own message, the same words the transcript shows every client at every detail level, Chat included, and never reasoning, redacted reasoning, a tool's arguments or output, or a failure's text. `lastAgentText` reads only `ContentText` parts, and `deliverCommunicate` only the delivered message. An agent that echoes a secret in its own message has put it in the transcript already; the row shows no client anything it could not read at the Chat level. Phase 2's notifications must not put `last_message` in a payload without the message-preview opt-in (spec 13.4).
21. **Rows carry it for top-level sessions, live and ended.** A live session's row takes its daemon's value, which outranks the meta the past index may still hold; an ended session's takes its meta's (the past index holds each full `SessionMeta` in memory, `hubcore/past.go:26-30`, so no disk read); a subagent row carries none. This resolves the server plan's byte-budget question without the fitter: a project resource holds at most 150 top-level rows (three tiers of `maxNavigationSectionRows = 50`, `navigation_projection.go:24`, `:1078-1080`), and 150 excerpts of at most 200 runes cost at most about 120 KB of the 2 MiB response cap (`maxNavigationResponseBytes`), about 30 KB for ASCII text. Subagent rows would cost up to 2,000 excerpts no row shows. The long-press preview (spec 7.3) then shows the excerpt for an ended session too.
22. **Markdown stays as the agent wrote it.** The excerpt keeps the message's own markup on one line ("## Summary Three layouts…"). How to show it is the client's: the phone strips leading heading, list and quote markers. Stripping on the daemon would throw away what a client might render.
23. **A remote host's rows carry it both ways.** `appThreadTreeEntries` sets it on the live entry and on the synthesized meta, so an online host's live row and an offline host's last-known row (which is kept but is not live, `web_api_tree.go:384-400`) both keep it.
24. **The fingerprint hashes it.** It can land while the status and the turn end hold still.

**S13b, remote task progress**

25. **Remote rows carry tasks through the remote hub's list rows.** `LocalDaemonEntry.Tasks` carries the roster's `LiveEntry.Tasks`, `threadFromEntry` puts a copy on the row, aliases carry none, and `appThreadTreeEntries` reads it into the controller's entry. `tasksFor` already reads the live entry, so the tree needs no change, and the remote cache compares whole rows (`hubcore/remotecache.go:130`), so a task change invalidates navigation with no fingerprint change. Ended sessions stay without a task line, as the server plan settled: reading persisted task state costs a disk read per session (`app_threadread.go:945-965`).

**Landing**

26. **PR 7 lands first, and the hub PRs land in order.** Every S1 PR uses its excerpt rule, so PRs 8, 9 and 10 start from main once PR 7 is on it. PRs 8, 9 and 11 each add a field to the same structs, literals, codec lists and fixture record beside the one before, so they land in that order rather than trade merge conflicts in aligned blocks. PR 9 stays one PR, daemon and hub together, as the server plan numbered it: 266 production lines is well inside the budget.

## Questions for Jesse

None. The spec settles every product behavior here. The closest calls, made as rulings with their reasons: a Failed row's reason carries no error text (ruling 12), and ended sessions' rows carry their last message for the long-press preview (ruling 21).

## Review Focus

1. **A row names a stale or wrong question.** A question answered and another asked between two probes leaves the status and the ask flag where they were, and a question left on an entry whose flag is clear must not reach a row. Pinned by `fuzzScenarioRoster_FingerprintMovesWhenOnlyTheQuestionMoves` and `fuzzScenarioBuildTree_EveryRowNamesThePendingQuestion` (Task 8.1), and `TestThreadSnapshotsCarryThePendingQuestion` (Task 7.3: the flag and the question are one read).
2. **A secret or a tool's output reaches a row.** The failure summary carries no message text; the excerpt carries only the agent's own words. Pinned by `TestRestingFailure_SummarizesTheFailedTurnUntilTheNextTurn` (Task 9.1: the exact wire JSON, without the provider's "sign-in rejected"), `TestNavigationRowsCarryTheFailure` (Task 9.4), `TestLastAgentText_IsTheLastTextPart` (Task 10.1: reasoning, redacted reasoning and a tool call's arguments are never the text) and `TestLastMessage_RecordsTheOpeningOfTheLastAgentMessage` (Task 10.1).
3. **A long or multi-line value breaks a row, or the whole resource.** Pinned by `TestExcerptNeverExceedsItsBound` (Task 7.1: every excerpt is one bounded line and its own excerpt, so a value the projector cut always passes the schema), `TestBoundedPendingQuestionCutsTheQuestionToTheWireBounds` (Task 7.2), `TestNavigationRowsDropAQuestionTheSchemaRefuses` and `TestNavigationSchemaBoundsTheQuestion` (Task 8.2), `TestNavigationRowsDropAFailureWithNothingToSay` and `TestNavigationSchemaBoundsTheFailure` (Task 9.4), `TestNavigationRowsCarryTheLastMessage` and `TestNavigationSchemaBoundsTheLastMessage` (Task 11.2), and each codec's refuse table.
4. **A coordinator's row shows a subagent's failure or words** (Jesse's answer 12). Pinned by `fuzzScenarioBuildTree_ACoordinatorsRowNeverShowsASubagentsFailure` (Task 9.3), `TestLocalDaemonEntriesFromRosterCarryTheFailureOnlyOnTheRoot` (Task 9.5), `fuzzScenarioBuildTree_RowsCarryTheirOwnSessionsLastMessage` (Task 11.1) and `TestLocalDaemonEntriesFromRosterCarryTheLastMessageOnlyOnTheRoot` (Task 11.3).
5. **A row's reason is lost on a daemon restart, or outlives what it describes.** The failure summary shows only while the status is systemError, clears with the next turn, gives way to a pending question and survives a restore; the last message survives a restore and reaches the meta file. Pinned by `TestRestingFailure_*` (Task 9.1), `TestThreadSnapshotsCarryTheRestingFailureOnlyWhileFailed` (Task 9.2), `fuzzScenarioBuildTree_EveryFailedRowSaysWhy` (Task 9.3: a working session's stale summary reaches no row), `TestPendingQuestion_SurvivesRestore` (Task 7.2), and `TestLastMessage_SurvivesRestore` and `TestLastMessage_RecordsTheOpeningOfTheLastAgentMessage` (Task 10.1).

---

## PRs and lanes

| PR | Item | Tasks | Production lines (dry run) | Depends on | Lane |
|---|---|---|---|---|---|
| 7 | S1b daemon: the excerpt rule and the first pending question | 7.1-7.3 | 170 | none | daemon |
| 8 | S1b hub: rows name the pending question | 8.1-8.3 | 139 | PR 7 | hub |
| 9 | S1c: the failure summary, daemon and hub | 9.1-9.5 | 266 | PR 7; Tasks 9.3-9.5 also PR 8 | daemon, then hub |
| 10 | S1d daemon: the last message, kept and persisted | 10.1-10.2 | 78 | PR 7 | daemon |
| 11 | S1d hub: rows carry the last message | 11.1-11.3 | 62 | PRs 9 and 10 | hub |
| 13 | S13b: remote rows carry task progress | 13.1 | 15 | none | hub |

- **Merge order.** PR 7 first. The hub lane then lands PR 8, PR 9 and PR 11 in that order, each branching from main once the one before it has merged, because each adds its fields beside the ones the one before added. PR 9's daemon tasks (9.1 and 9.2) need only PR 7 and can start beside PR 8; the branch merges main once PR 8 is on it, before Task 9.3. PR 10 needs only PR 7 and runs in the daemon lane beside PRs 8 and 9; PR 11 waits for it too. PR 13 names only lines already on main and can merge at any point. Each of these paths was dry-run: PR 9's daemon tasks and PR 10 on main with PR 7 alone, PR 13 on main alone.
- **Where the lanes collide.** The daemon halves (PRs 7, 9, 10) each touch `appwire/types.go` (`EvenerThread`), `appwire/excerpt.go`, `server/thread_envelope.go` and `server/appwire_runtime.go`; PRs 7 and 9 also touch `appwire/clone.go` and the `ThreadEnvelopeSource` implementations and test stubs. The hub halves (PRs 8, 9, 11) each touch `hubcore/roster.go`, `prober.go`, `tree.go`, `hubapi/navigation.go`, `navigation_projection.go`, `navigation_schema.go`, the codec, the fixture, `internal/appsource/local_daemon.go`, `app_rpc.go` and `web_api_tree.go`; PR 13 touches the last three and `hubapi/navigation.go`. Every collision is an addition on an adjacent line. The code below was dry-run in the order 7, 8, 9, 10, 11, 13, and each anchor names a line on main or a line a PR in its Depends on column added; the anchors that can meet a PR still in flight (Task 9.2's probe stub, Task 10.1's constant, PR 13's `LastTurnEndedAt` lines) say where to go either way. When a PR merges main after a neighbour landed, the merge keeps both sides; re-run `make generate` whenever `types.gen.ts` conflicts.
- **The TestFlight checkpoint is met.** The server plan's rule that a PR adding a navigation summary field waits for a TestFlight build containing the tolerant codec (#2473) held for PRs 17 and 19 (#2584, #2587), which merged behind it; the iOS TestFlight workflow has run successfully on main since (for example run 36314680692 on 2026-09-27).
- The server plan's PRs 1 to 6, 12 and 14 to 19 are on main, and this plan is written against them.

## Phone lane handoff

This lane changes `mobile-native/` only through the shared package. When each hub PR is on the hub the phone talks to, the phone switches off its fallback in `mobile-native/src/board/attention.ts` (`REASONS` and `whyLine`, `:207-221`, and `lastLine`, `:271-283`, on main at `8d671c559`; the Board lane edits this file, so find them by name) as follows. Every field is optional: a row without it keeps today's fallback.

**S1b (after PR 8).**
- A `question` row's why line is "Question · " then `row.question.text` (spec 7.1), up to two lines. Without `row.question` (an older hub, or a question the hub dropped) keep `REASONS.question`.
- The long-press preview can list `row.question.options`, and say "Question 1 of N" from `row.question.count` when it is above one.
- A row carries `question` only while it carries `ask_pending`.

**S1c (after PR 9).**
- A `failed` row's reason comes from `row.failure`:
  - `cause_kind: "crashed"`: the session's process stopped while the hub still listed it.
  - `cause_kind: "provider"` with `status` 401 or 403: "<provider> sign-in expired (<status>)", spec 7.1's example.
  - another `provider` failure: the provider, the title and the status.
  - otherwise the title ("Evener error", "Usage limit reached").
- Without `row.failure`, keep `REASONS.failed`.
- `failure` always describes the coordinator's own failed turn, never a subagent's (answer 12). It never carries the error text; the session's transcript has it.

**S1d (after PR 11).**
- A Finished row (unseen) gets a why line: `row.last_message` in the reading serif, 15/21, ink-mid, up to two lines, without quotation marks (spec 7.2). Strip leading heading, list and quote markers and show emphasis as plain text: the excerpt keeps the agent's markup (ruling 22). A row without it has no why line, as today.
- The long-press preview shows `last_message` for any row that carries it, ended sessions included.
- A phase 2 notification carries `last_message` only with the message-preview opt-in (spec 13.4).

**S13b (after PR 13).**
- A live row from another host now carries `tasks` like a local row, so the task line needs no host case.

**Known limits.**
- A daemon started before PR 7, 9 or 10 sends none of these until it restarts on a new build; its rows keep their fallbacks.
- A Failed row whose failure evener raised itself (a retry budget, a skill route) reads the generic title "Evener error" (ruling 12).
- A Working row's `last_message` can be its previous turn's, since the meta facet re-samples at turn ends (ruling 19); the Board shows a Working row's current activity instead.

---

## PR 7: the excerpt rule and the first pending question, S1b daemon (Tasks 7.1-7.3)

**Branch:** `git fetch origin && git switch -c claude/s1b-pending-question-daemon origin/main`

**What it adds.** The one-line excerpt rule every S1 PR uses, the option labels the agent now keeps on each pending question, and `EvenerThread.pendingQuestion` on thread snapshots, sampled in the same read as `askPending`. Nothing reads the question yet; PR 8 carries it to rows.

### Task 7.1: The excerpt rule

**Implementer:** Sonnet.

**Files:**
- Create: `appwire/excerpt.go`
- Create: `appwire/excerpt_test.go`

**Interfaces:**
- Produces: `appwire.Excerpt(text string, maxRunes int) string`; the constants `appwire.MaxQuestionTextRunes = 200`, `MaxQuestionOptionRunes = 80`, `MaxQuestionOptions = 5`. PR 9 adds `MaxFailureTitleRunes` and PR 10 `MaxMessageExcerptRunes` to the same block.

- [ ] **Step 1: Write the failing tests**

`appwire/excerpt_test.go`:

```go
package appwire

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Every run of whitespace or control characters, line breaks included,
// becomes one space and the ends are trimmed: a row's why line is one line
// (S1).
func TestExcerptIsOneLine(t *testing.T) {
	got := Excerpt("  Keep or drop\n\nthe implied\toptions?\r\n\x00 ", 200)
	if want := "Keep or drop the implied options?"; got != want {
		t.Fatalf("Excerpt = %q, want %q", got, want)
	}
	if got := Excerpt(" \n\t ", 200); got != "" {
		t.Fatalf("whitespace alone excerpted to %q, want empty", got)
	}
}

// Text at the bound is kept whole; longer text is cut to the bound, ending in
// an ellipsis at the last word break in its second half.
func TestExcerptCutsLongTextAtAWordBreak(t *testing.T) {
	exact := strings.Repeat("a", 20)
	if got := Excerpt(exact, 20); got != exact {
		t.Fatalf("text at the bound = %q, want it whole", got)
	}
	for _, tc := range []struct {
		text string
		max  int
		want string
	}{
		{"the quick brown fox jumps", 12, "the quick…"},
		// The rune past the kept text is a space, so the kept text already
		// ends on a whole word.
		{"hello world again", 12, "hello world…"},
		// No word break in the second half: the word is cut.
		{"a " + strings.Repeat("b", 30), 10, "a bbbbbbb…"},
		{"supercalifragilistic", 8, "superca…"},
		{"ab", 1, "…"},
	} {
		if got := Excerpt(tc.text, tc.max); got != tc.want {
			t.Errorf("Excerpt(%q, %d) = %q, want %q", tc.text, tc.max, got, tc.want)
		}
	}
}

// Invalid UTF-8 is repaired, so the wire never carries a malformed string.
func TestExcerptRepairsInvalidUTF8(t *testing.T) {
	if got, want := Excerpt("ok\xffgo", 200), "ok�go"; got != want {
		t.Fatalf("Excerpt = %q, want %q", got, want)
	}
}

func TestExcerptOfNothingIsEmpty(t *testing.T) {
	if got := Excerpt("anything", 0); got != "" {
		t.Fatalf("Excerpt with no room = %q, want empty", got)
	}
}

// Whatever it is given, an excerpt is valid UTF-8, one line with no leading or
// trailing space, never longer than its bound, and its own excerpt: the hub
// schema accepts a value only when Excerpt leaves it unchanged, so a value the
// projector cut always passes.
func TestExcerptNeverExceedsItsBound(t *testing.T) {
	inputs := []string{
		"",
		"short",
		strings.Repeat("word ", 400),
		strings.Repeat("😀", 300),
		"line one\nline two\n\nline three",
		"\xff\xfe" + strings.Repeat("x y ", 100),
		"para\u2028graph\u00a0with\x00control\u0085runes " + strings.Repeat("é ", 30),
		strings.Repeat("a", 1<<20),
	}
	for _, text := range inputs {
		for maxRunes := 1; maxRunes <= 40; maxRunes++ {
			got := Excerpt(text, maxRunes)
			if !utf8.ValidString(got) || strings.ContainsAny(got, "\r\n\t") ||
				got != strings.TrimSpace(got) || utf8.RuneCountInString(got) > maxRunes ||
				Excerpt(got, maxRunes) != got {
				t.Fatalf("Excerpt(%.20q…, %d) = %q, want one trimmed valid line of at most %d runes that is its own excerpt", text, maxRunes, got, maxRunes)
			}
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./appwire -run 'TestExcerpt' -count=1`
Expected: FAIL to compile (`Excerpt` undefined).

- [ ] **Step 3: Implement**

`appwire/excerpt.go`:

```go
package appwire

import "unicode"

// The bounds a session row's why text travels under (S1). The daemon cuts
// each value with Excerpt before it leaves the session, and the hub's
// navigation projection and schema hold rows to the same numbers.
const (
	// MaxQuestionTextRunes bounds the text of a session's first pending
	// question, a Needs you row's why line ("Question · keep or drop the
	// implied options?"): two lines on a phone, with room left for the
	// long-press preview.
	MaxQuestionTextRunes = 200
	// MaxQuestionOptionRunes bounds one option label. An ask_user label is a
	// choice, not a sentence.
	MaxQuestionOptionRunes = 80
	// MaxQuestionOptions is ask_user's own ceiling on options per question.
	MaxQuestionOptions = 5
)

// Excerpt is text as one short line, the form every row why text takes on the
// wire (S1). Each run of whitespace or control characters, line breaks
// included, becomes one space; the ends are trimmed; invalid UTF-8 becomes
// U+FFFD. Text longer than maxRunes is cut to at most maxRunes runes ending in
// "…", at the last word break in the kept text's second half when there is
// one. It reads only as much of text as the excerpt needs, so a long message
// costs no more than a short one.
func Excerpt(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	line := make([]rune, 0, min(len(text), maxRunes+1))
	space := false
	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			space = len(line) > 0
			continue
		}
		if space {
			line = append(line, ' ')
			space = false
		}
		line = append(line, r)
		if len(line) > maxRunes {
			return cutExcerpt(line, maxRunes)
		}
	}
	return string(line)
}

// cutExcerpt ends a line that runs past maxRunes: it keeps maxRunes-1 runes
// and an ellipsis. When the rune after the kept text is not a space, the kept
// text ends mid-word, so it backs up to the last word break in its second
// half; a single word longer than that is cut where it stands.
func cutExcerpt(line []rune, maxRunes int) string {
	keep := line[:maxRunes-1]
	if line[maxRunes-1] != ' ' {
		for i := len(keep) - 1; i >= len(keep)/2; i-- {
			if keep[i] == ' ' {
				keep = keep[:i]
				break
			}
		}
	}
	return string(keep) + "…"
}
```

Run `$(go env GOROOT)/bin/gofmt -w appwire/excerpt.go appwire/excerpt_test.go`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./appwire -run 'TestExcerpt' -count=1`
Expected: PASS. `TestExcerptNeverExceedsItsBound` feeds a 1 MiB string to every bound from 1 to 40; the function stops reading after `maxRunes+1` runes.

- [ ] **Step 5: Commit**

```bash
git add appwire/excerpt.go appwire/excerpt_test.go
git commit -m "feat(appwire): one excerpt rule for a row's why text"
```

### Task 7.2: The session names its first pending question

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire/types.go` (the `PendingQuestion` type after `SubagentTally`, `:955-959`)
- Modify: `appwire/excerpt.go` (`BoundedPendingQuestion` after the constants)
- Modify: `appwire/excerpt_test.go`
- Modify: `agent/session_tools_ask.go` (`askQuestion` `:33-36`; `PendingQuestion` after `HasPendingAsk` `:69-71`; `parseAskQuestions`' label loop `:278-300`; the `appwire` import)
- Create: `agent/session_pending_question_test.go`

**Interfaces:**
- Consumes: `appwire.Excerpt` and the constants (Task 7.1).
- Produces:
  - `appwire.PendingQuestion{Question string; Options []string; Count int}`, JSON `question`, `options,omitempty`, `count`.
  - `appwire.BoundedPendingQuestion(text string, labels []string, count int) PendingQuestion`, which PR 8's projector calls too.
  - `askQuestion.Options []string` (labels, in call order), filled by `parseAskQuestions`, so the live `ask_user` call and restore's `questionsFromAskCalls` both keep them.
  - `(*agent.Session).PendingQuestion() *appwire.PendingQuestion`: nil while no question waits.

`appwire.PendingQuestion` is not reachable from the catalog until Task 7.3 puts it on `EvenerThread`, so `make generate` has nothing to emit here.

- [ ] **Step 1: Write the failing tests**

`agent/session_pending_question_test.go`:

```go
package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// The first question of the pending ask names the Needs you row (S1b): its
// text, its option labels in the order the call listed them, and how many
// questions wait in all. A session with nothing pending reports none, and the
// answer clears it.
func TestPendingQuestion_NamesTheFirstQuestionOfThePendingAsk(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})
	if got := sess.PendingQuestion(); got != nil {
		t.Fatalf("a session with no ask reports %+v", got)
	}
	for _, call := range []llm.ToolCallData{askUserCall("c1", askUserArgsValid()), askUserCall("c2", askUserArgsTwoQuestions())} {
		if res := sess.reg.ExecuteCall(context.Background(), sess.env, call); res.IsError {
			t.Fatalf("ask_user %s errored: %s", call.ID, res.Output)
		}
	}
	want := &appwire.PendingQuestion{Question: "Which datastore for the ingest path?", Options: []string{"Postgres", "SQLite"}, Count: 3}
	if got := sess.PendingQuestion(); !reflect.DeepEqual(got, want) {
		t.Fatalf("PendingQuestion = %+v, want %+v", got, want)
	}
	sess.clearAskPending()
	if got := sess.PendingQuestion(); got != nil {
		t.Fatalf("after the answer PendingQuestion = %+v, want none", got)
	}
}

// A question leaves the session cut to the wire's bounds
// (appwire.BoundedPendingQuestion): a long, many-line question is one line.
func TestPendingQuestion_LeavesTheSessionBounded(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})
	sess.mu.Lock()
	sess.askPending = []askQuestion{{Question: "Keep or drop\nthe implied options?\n\n" + strings.Repeat("Fourteen descriptions mention flags. ", 20)}}
	sess.mu.Unlock()
	got := sess.PendingQuestion()
	if got == nil || !strings.HasPrefix(got.Question, "Keep or drop the implied options? Fourteen") ||
		utf8.RuneCountInString(got.Question) > appwire.MaxQuestionTextRunes {
		t.Fatalf("PendingQuestion = %+v, want one line cut to %d runes", got, appwire.MaxQuestionTextRunes)
	}
}

// A restored session names the same question the live one did: restore
// rebuilds the pending set through the parse the live call used (S1b).
func TestPendingQuestion_SurvivesRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask := askUserCall("ask1", askUserArgsValid())
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return toolCallResponse(ask) },
	}})
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	want := &appwire.PendingQuestion{Question: "Which datastore for the ingest path?", Options: []string{"Postgres", "SQLite"}, Count: 1}
	if got := sess.PendingQuestion(); !reflect.DeepEqual(got, want) {
		t.Fatalf("live PendingQuestion = %+v, want %+v", got, want)
	}
	meta := sess.Meta()
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.PendingQuestion(); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored PendingQuestion = %+v, want %+v", got, want)
	}
}
```

In `appwire/excerpt_test.go`, add `"slices"` to the imports and append:

```go
// A question leaves the session, and reaches a row, as one bounded line with
// at most five one-line labels, none of them empty (S1b).
func TestBoundedPendingQuestionCutsTheQuestionToTheWireBounds(t *testing.T) {
	got := BoundedPendingQuestion(
		"Keep or drop\nthe implied options? "+strings.Repeat("Fourteen descriptions mention flags. ", 20),
		[]string{"Drop them", " \n ", strings.Repeat("x", 200), "B", "C", "D", "E"},
		2,
	)
	if !strings.HasPrefix(got.Question, "Keep or drop the implied options? Fourteen") || !strings.HasSuffix(got.Question, "…") ||
		utf8.RuneCountInString(got.Question) > MaxQuestionTextRunes {
		t.Fatalf("question = %q, want one line cut to %d runes", got.Question, MaxQuestionTextRunes)
	}
	want := []string{"Drop them", strings.Repeat("x", MaxQuestionOptionRunes-1) + "…", "B", "C", "D"}
	if !slices.Equal(got.Options, want) {
		t.Fatalf("options = %q, want %q", got.Options, want)
	}
	if got.Count != 2 {
		t.Fatalf("count = %d, want the count it was given", got.Count)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd agent && go test . -run 'TestPendingQuestion' -count=1`, then from the repo root `go test ./appwire -run 'TestBounded' -count=1`
Expected: FAIL to compile (`appwire.PendingQuestion`, `sess.PendingQuestion` and `BoundedPendingQuestion` undefined).

- [ ] **Step 3: Implement**

`appwire/types.go`, after the `SubagentTally` type:

```go
// PendingQuestion is the first question of a session's pending ask (S1b): what
// its Needs you row says ("Question · keep or drop the implied options?") and
// the option labels a long-press preview lists. The daemon cuts Question and
// each label to one line (Excerpt, at MaxQuestionTextRunes and
// MaxQuestionOptionRunes) and sends at most MaxQuestionOptions labels. Count
// is how many questions the pending ask holds, so a client can say "Question
// 1 of 2".
type PendingQuestion struct {
	Question string   `json:"question"`
	Options  []string `json:"options,omitempty"`
	Count    int      `json:"count"`
}
```

`appwire/excerpt.go`, after the constant block:

```go
// BoundedPendingQuestion is a pending question cut to the wire's bounds: its
// text as one line of at most MaxQuestionTextRunes, and at most
// MaxQuestionOptions of its labels, each one line of at most
// MaxQuestionOptionRunes, leaving out a label with no text. The daemon applies
// it before a question leaves the session, and the hub again before a row
// carries one, so a remote host or an older daemon cannot widen a row.
func BoundedPendingQuestion(text string, labels []string, count int) PendingQuestion {
	question := PendingQuestion{Question: Excerpt(text, MaxQuestionTextRunes), Count: count}
	for _, label := range labels {
		if len(question.Options) == MaxQuestionOptions {
			break
		}
		if label = Excerpt(label, MaxQuestionOptionRunes); label != "" {
			question.Options = append(question.Options, label)
		}
	}
	return question
}
```

`agent/session_tools_ask.go`: add `"primeradiant.com/evener/appwire"` to the imports after `agent/schema`. Replace `askQuestion` and its comment with:

```go
// askQuestion is one question posted by an ask_user call, recorded in the
// session's per-turn pending set (spec §5.1) so a round-boundary check can
// tell whether the round just posted question(s), and so the session's row can
// name its first pending question (PendingQuestion, S1b). The transcript
// remains the durable, renderable record of the questions and their options
// (spec §5.1, §6); this struct carries the question and its option labels,
// never the options' details.
type askQuestion struct {
	Header   string
	Question string
	// Options are the question's option labels, in the order the call
	// listed them.
	Options []string
}
```

After `HasPendingAsk`:

```go
// PendingQuestion is the first question of the session's pending ask, which
// its Needs you row names (S1b), cut to the wire's bounds; nil while no
// question waits. It reads the pending set in one hold of s.mu, so the
// envelope's AskPending, which is its presence, can never disagree with it.
func (s *Session) PendingQuestion() *appwire.PendingQuestion {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.askPending) == 0 {
		return nil
	}
	first := s.askPending[0]
	question := appwire.BoundedPendingQuestion(first.Question, first.Options, len(s.askPending))
	return &question
}
```

In `parseAskQuestions`, keep each label as the loop checks it: add `labels := make([]string, 0, len(opts))` right after `opts, _ := qm["options"].([]any)`, `labels = append(labels, label)` right after `labelsSeen[label] = true`, and `Options:  labels,` after `Question: fmt.Sprint(qm["question"]),` in the `askQuestion` literal.

Run `$(go env GOROOT)/bin/gofmt -w` on the five touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `cd agent && go test . -run 'TestPendingQuestion|TestParseAskQuestions|TestAskUser' -count=1 && go test -tags evenerfuzz . -run '^FuzzStatefulSessionToolsProgram$' -count=1`, then `go test ./appwire -run 'TestExcerpt|TestBounded' -count=1`
Expected: PASS. The fuzz seeds compare a restored pending set with the live parse by `reflect.DeepEqual` (`session_tools_misc_contract_fuzz_test.go:67-104`), so they now pin the labels through restore too.

- [ ] **Step 5: Commit**

```bash
git add appwire/types.go appwire/excerpt.go appwire/excerpt_test.go agent/session_tools_ask.go agent/session_pending_question_test.go
git commit -m "feat(agent): a session keeps its questions' labels and names its first pending question"
```

### Task 7.3: Thread snapshots carry the pending question

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire/types.go` (`EvenerThread.PendingQuestion` after `AskPending`, `:898`)
- Modify: `appwire/clone.go` (`ClonePendingQuestion` before `CloneTaskAggregate` `:130`; one line in `cloneEvenerThread` `:90-104`)
- Modify: `appwire/clone_test.go`
- Modify: `agent/session_envelope_sampling.go` (`HasPendingAsk() bool` becomes `PendingQuestion() *appwire.PendingQuestion`, `:70`)
- Modify: `server/thread_envelope.go` (`threadEnvelope` `:83`; `ThreadEnvelopeSource` `:130`; `refreshFacets`' ask facet `:376-378`; `assign`'s ask facet `:510-512`)
- Modify: `server/appwire_runtime.go` (`appThreadWithDiagnosticsLocked`, `:2517` and `:2563`)
- Modify: `cmd/evener/serve.go` (`liveThreadEnvelopeSource.AskPending` becomes `PendingQuestion`, `:2322-2324`)
- Modify the five other `ThreadEnvelopeSource` implementations: `server/thread_envelope_test_helpers_test.go` (`:34`, `:56`), `server/askpending_live_notification_test.go` (`:102-104`), `server/thread_envelope_test.go` (`:748`), `cmd/evener-hub/app_threadread_tasks_test.go` (`:205`, `:213-215`), `cmd/evener-hub/internal/hubcore/prober_wire_test.go` (`:141`); and `cmd/evener/serve_residual_fuzz_test.go` (`:143`)
- Create: `server/thread_envelope_question_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`

**Interfaces:**
- Consumes: `Session.PendingQuestion` and `appwire.PendingQuestion` (Task 7.2).
- Produces:
  - `EvenerThread.PendingQuestion *PendingQuestion` (`pendingQuestion,omitempty`), on `thread/read` and `thread/list`; TypeScript `PendingQuestion` and `EvenerThread.pendingQuestion?: PendingQuestion`.
  - `appwire.ClonePendingQuestion(*PendingQuestion) *PendingQuestion` (PR 8 uses it).
  - `server.ThreadEnvelopeSource.PendingQuestion() *appwire.PendingQuestion` replaces `AskPending() bool`; `agent.EnvelopeSampling.PendingQuestion()` replaces `HasPendingAsk()`.

The envelope reads the question and derives the flag from it in one call (ruling 8). `agent/session_envelope_sampling_test.go` reads the interface's method set, so the new method is checked against every forbidden lock with no test edit; it takes only `s.mu`.

- [ ] **Step 1: Write the failing tests**

`server/thread_envelope_question_test.go`:

```go
package server

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// A session's thread snapshots carry the first question of its pending ask
// beside AskPending (S1b). Both come from one read of the pending set, so the
// flag is the question's presence, and the answer clears both.
func TestThreadSnapshotsCarryThePendingQuestion(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	question := &appwire.PendingQuestion{Question: "Which datastore for the ingest path?", Options: []string{"Postgres", "SQLite"}, Count: 2}
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{question: question})
	listed := func() appwire.EvenerThread {
		t.Helper()
		list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{StatusOnly: true})
		if err != nil {
			t.Fatalf("thread/list: %v", err)
		}
		return list.Data[0].Evener
	}
	if got := listed(); !got.AskPending || !reflect.DeepEqual(got.PendingQuestion, question) {
		t.Fatalf("listed ask = %v with question %+v, want the pending question %+v", got.AskPending, got.PendingQuestion, question)
	}
	if got := readThreadOverWire(t, srv, "local:root").Evener; !got.AskPending || !reflect.DeepEqual(got.PendingQuestion, question) {
		t.Fatalf("read ask = %v with question %+v, want the pending question %+v", got.AskPending, got.PendingQuestion, question)
	}

	// The answer empties the pending set, and the user input that carries it
	// re-samples the ask facet.
	src.question = nil
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventUserInput, SessionID: "root", Data: events.UserInputData{Text: "Postgres"}}, nil)
	got := listed()
	if got.AskPending || got.PendingQuestion != nil {
		t.Fatalf("after the answer ask = %v with question %+v, want neither", got.AskPending, got.PendingQuestion)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pendingQuestion") {
		t.Fatalf("a session with no pending ask carries pendingQuestion: %s", raw)
	}
}
```

Append to `appwire/clone_test.go`:

```go
func TestCloneThreadOwnsThePendingQuestion(t *testing.T) {
	original := Thread{Evener: EvenerThread{PendingQuestion: &PendingQuestion{Question: "Which datastore?", Options: []string{"Postgres", "SQLite"}, Count: 2}}}
	clone := CloneThread(original)
	if !reflect.DeepEqual(clone, original) {
		t.Fatal("clone changed values while copying")
	}
	clone.Evener.PendingQuestion.Options[0] = "changed"
	clone.Evener.PendingQuestion.Count = 9
	if original.Evener.PendingQuestion.Options[0] != "Postgres" || original.Evener.PendingQuestion.Count != 2 {
		t.Fatalf("the pending question was changed through its clone: %+v", original.Evener.PendingQuestion)
	}
	if ClonePendingQuestion(nil) != nil {
		t.Fatal("a nil question cloned to a non-nil one")
	}
}
```

In `server/thread_envelope_test_helpers_test.go`, give `stubThreadEnvelopeSource` a field `question *appwire.PendingQuestion` after `askPending`, and replace its `AskPending` method with:

```go
// PendingQuestion reports question when a test set one. A test that sets only
// askPending gets a question with no text, which is still a pending ask: the
// envelope's AskPending is the question's presence.
func (s *stubThreadEnvelopeSource) PendingQuestion() *appwire.PendingQuestion {
	if s.question != nil {
		return s.question
	}
	if s.askPending {
		return &appwire.PendingQuestion{Count: 1}
	}
	return nil
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./server -run 'TestThreadSnapshotsCarryThePendingQuestion' -count=1` and `go test ./appwire -run 'TestCloneThreadOwnsThePendingQuestion' -count=1`
Expected: FAIL to compile (`EvenerThread.PendingQuestion` and `ClonePendingQuestion` undefined).

- [ ] **Step 3: Implement**

`appwire/types.go`, after `EvenerThread.AskPending`:

```go
	// PendingQuestion is the first question of the session's pending ask and
	// how many the ask holds (S1b). The daemon reads both from one sample of
	// the pending set, so it is present exactly when AskPending is true; it is
	// absent from an older daemon. Snapshot-only: thread/status/changed
	// carries AskPending, and nothing carries the question's text.
	PendingQuestion *PendingQuestion `json:"pendingQuestion,omitempty"`
```

`appwire/clone.go`: in `cloneEvenerThread`, after `e.Subagents = clonePointer(e.Subagents)`, add `e.PendingQuestion = ClonePendingQuestion(e.PendingQuestion)`; and before `CloneTaskAggregate`:

```go
// ClonePendingQuestion returns a copy of value whose option labels are its
// own; nil stays nil.
func ClonePendingQuestion(value *PendingQuestion) *PendingQuestion {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Options = append([]string(nil), value.Options...)
	return &clone
}
```

`agent/session_envelope_sampling.go`: in `EnvelopeSampling`, replace `HasPendingAsk() bool` with `PendingQuestion() *appwire.PendingQuestion`.

`server/thread_envelope.go`:
- In `threadEnvelope`, replace the line `AskPending            bool` with:

```go
	// AskPending and PendingQuestion are one facet sampled from one read:
	// AskPending is PendingQuestion's presence (S1b).
	AskPending            bool
	PendingQuestion       *appwire.PendingQuestion
```

- In `ThreadEnvelopeSource`, replace `AskPending() bool` with:

```go
	// PendingQuestion is the first question of the session's pending ask, nil
	// while none waits. The envelope's AskPending is its presence.
	PendingQuestion() *appwire.PendingQuestion
```

- In `refreshFacets`, replace the ask facet's body:

```go
	if facets&facetAsk != 0 {
		// One call answers both: a question waits exactly when the session
		// names one, so the flag and the question cannot disagree.
		next.PendingQuestion = src.PendingQuestion()
		next.AskPending = next.PendingQuestion != nil
	}
```

- In `assign`'s ask facet, after `e.AskPending = next.AskPending`, add `e.PendingQuestion = next.PendingQuestion`.

`server/appwire_runtime.go`, in `appThreadWithDiagnosticsLocked`: after `askPending := envelope.AskPending` add `pendingQuestion := envelope.PendingQuestion`, and after `AskPending:            askPending,` in the `EvenerThread` literal add `PendingQuestion:       pendingQuestion,`. The envelope owns the pointer and never mutates it in place, so the snapshot hands it out as it does `PendingEscalations`.

`cmd/evener/serve.go`, replace `liveThreadEnvelopeSource.AskPending` with:

```go
func (l liveThreadEnvelopeSource) PendingQuestion() *appwire.PendingQuestion {
	return l.session().PendingQuestion()
}
```

The other implementations, method for method:
- `server/askpending_live_notification_test.go`, `sessionAskPendingEnvelopeSource`: replace `AskPending` with

```go
func (s *sessionAskPendingEnvelopeSource) PendingQuestion() *appwire.PendingQuestion {
	return s.sess.PendingQuestion()
}
```

- `server/thread_envelope_test.go`, `countingThreadEnvelopeSource`: replace the `AskPending` one-liner with

```go
func (c *countingThreadEnvelopeSource) PendingQuestion() *appwire.PendingQuestion {
	c.hit()
	return nil
}
```

- `cmd/evener-hub/app_threadread_tasks_test.go`, `sessionTaskEnvelopeSource`: delete the `AskPending` one-liner and add, after `PendingEscalations`,

```go
func (s sessionTaskEnvelopeSource) PendingQuestion() *appwire.PendingQuestion {
	return nil
}
```

- `cmd/evener-hub/internal/hubcore/prober_wire_test.go`, `wireProbeEnvelopeSource`: replace the `AskPending` one-liner with

```go
func (s wireProbeEnvelopeSource) PendingQuestion() *appwire.PendingQuestion {
	if s.askPending {
		return &appwire.PendingQuestion{Count: 1}
	}
	return nil
}
```

- `cmd/evener/serve_residual_fuzz_test.go`: `_ = s.envelopeSource.AskPending()` becomes `_ = s.envelopeSource.PendingQuestion()`.

Writing the replacement methods on several lines where the old ones were aligned one-liners keeps gofmt from realigning their neighbours. Run `$(go env GOROOT)/bin/gofmt -w` on every touched Go file, then `make generate`.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./server -count=1`, `go test ./appwire -count=1`, `cd agent && go test . -run 'TestEnvelopeSampling|TestEverySamplingRelevantMutexIsClassified|TestPendingQuestion' -count=1`, `go test ./cmd/evener -run 'Notes|Envelope|Ask' -count=1`, `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$|TestStatusProber' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`
Expected: PASS, every existing ask test unchanged (`TestStatusChangeCarriesAskPending*`, `TestAskUserLiveStatusFrameCarriesAskPending`); `types.gen.ts` gains `PendingQuestion` and `pendingQuestion?: PendingQuestion` on `EvenerThread`.

Gates, in the root module and in `agent/`: `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...`; `golangci-lint run ./appwire/ ./cmd/evener/ ./cmd/evener-hub/internal/hubcore/ ./server/`, `golangci-lint run --config .golangci-appwire.yml ./server/...`, `cd agent && golangci-lint run .`; `make lint-generated`.

- [ ] **Step 5: Commit and open PR 7**

```bash
git add appwire/types.go appwire/clone.go appwire/clone_test.go agent/session_envelope_sampling.go server/thread_envelope.go server/appwire_runtime.go server/thread_envelope_question_test.go server/thread_envelope_test_helpers_test.go server/askpending_live_notification_test.go server/thread_envelope_test.go cmd/evener/serve.go cmd/evener/serve_residual_fuzz_test.go cmd/evener-hub/app_threadread_tasks_test.go cmd/evener-hub/internal/hubcore/prober_wire_test.go appwire-client/typescript/types.gen.ts
git commit -m "feat(server): thread snapshots carry the first pending question beside askPending"
```

Title "feat(server): the excerpt rule and the first pending question (S1b daemon, phase 7 PR 7)". The body says every S1 value is cut by `appwire.Excerpt` before it leaves the daemon, the question and the ask flag come from one read, nothing reads the question until PR 8, and no projector change means no TUI case.

---

## PR 8: rows name the pending question, S1b hub (Tasks 8.1-8.3)

**Branch:** `git fetch origin && git switch -c claude/s1b-pending-question-rows origin/main`. PR 7 is on main.

**What it adds.** The hub keeps each live root's first pending question from its probe, every row of an asking session names it, a remote host's rows carry theirs, and navigation rows gain `question`. The phone's fallback (`REASONS.question`, "waiting for your answer") retires as the handoff section says.

### Task 8.1: Live entries and tree rows carry the question

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/hubcore/roster.go` (`LiveEntry.PendingQuestion` after `PendingEscalations` `:41`; `ProbeResult.PendingQuestion` after its `PendingEscalations`; `CloneLiveEntry` `:198`; `rosterFingerprint`, after the escalation cards `:465-469`; `liveEntryFromProbe` `:1387`; `ReadSpawnedThread`'s result literal `:1468-1478`)
- Modify: `cmd/evener-hub/internal/hubcore/prober.go` (`Probe`'s result literal `:152-175`)
- Modify: `cmd/evener-hub/internal/hubcore/tree.go` (`TreeNode.Question` after `ApprovalTarget` `:446`; a `pendingQuestionFor` closure after `firstApprovalFor` `:1064-1069`; `buildNode` `:1251-1300`; the live-only leaf `:1527-1550`; the NeedsYou node `:1639-1669`; `cloneTreeNodesContext` `:114-134`)
- Modify: `cmd/evener-hub/internal/hubcore/prober_wire_test.go` (`wireProbeEnvelopeSource` gains `question`)
- Create: `cmd/evener-hub/internal/hubcore/pending_question_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go` (register the five scenarios)

**Interfaces:**
- Consumes: `EvenerThread.PendingQuestion`, `appwire.ClonePendingQuestion` (PR 7).
- Produces: `ProbeResult.PendingQuestion`, `LiveEntry.PendingQuestion` and `TreeNode.Question`, all `*appwire.PendingQuestion`.

- [ ] **Step 1: Write the failing scenarios**

In `prober_wire_test.go`, add `question *appwire.PendingQuestion` to `wireProbeEnvelopeSource` after `askPending`, and make its `PendingQuestion` method report it first:

```go
func (s wireProbeEnvelopeSource) PendingQuestion() *appwire.PendingQuestion {
	if s.question != nil {
		return s.question
	}
	if s.askPending {
		return &appwire.PendingQuestion{Count: 1}
	}
	return nil
}
```

`cmd/evener-hub/internal/hubcore/pending_question_test.go`:

```go
package hubcore

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

func testPendingQuestion() *appwire.PendingQuestion {
	return &appwire.PendingQuestion{Question: "Keep or drop the implied options?", Options: []string{"Drop them", "Keep them"}, Count: 2}
}

// fuzzScenarioStatusProber_KeepsThePendingQuestion: the probe keeps the listed
// root's first pending question beside its ask flag (S1b).
func fuzzScenarioStatusProber_KeepsThePendingQuestion(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_question",
		state:     appwire.ThreadStatusAwaiting,
		source:    wireProbeEnvelopeSource{question: testPendingQuestion()},
	})
	got := prober.Probe(entry)
	if !got.OK || !got.PendingAsk || !reflect.DeepEqual(got.PendingQuestion, testPendingQuestion()) {
		t.Fatalf("probe = %+v, want the pending question %+v", got, testPendingQuestion())
	}
}

// fuzzScenarioRoster_FingerprintMovesWhenOnlyTheQuestionMoves: a question
// answered and another asked between two probes leaves Status awaiting and
// PendingAsk set on both, and moves only the question the row names (S1b).
func fuzzScenarioRoster_FingerprintMovesWhenOnlyTheQuestionMoves(t *testing.T) {
	entry := func(question *appwire.PendingQuestion) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusAwaiting, PendingAsk: true, PendingQuestion: question}}
	}
	base := testPendingQuestion()
	text, options, count := testPendingQuestion(), testPendingQuestion(), testPendingQuestion()
	text.Question = "Which datastore for the ingest path?"
	options.Options = []string{"Drop them", "Keep them", "Ask me per tool"}
	count.Count = 3
	for name, moved := range map[string]*appwire.PendingQuestion{"text": text, "options": options, "count": count, "absent": nil} {
		if rosterFingerprint(entry(base)) == rosterFingerprint(entry(moved)) {
			t.Errorf("the roster fingerprint held when only the question's %s moved", name)
		}
	}
}

// fuzzScenarioRoster_EntriesOwnTheirPendingQuestion: an entry never aliases the
// probe's question, and a clone never aliases the entry's.
func fuzzScenarioRoster_EntriesOwnTheirPendingQuestion(t *testing.T) {
	question := testPendingQuestion()
	fromProbe := liveEntryFromProbe(rendezvous.Entry{PID: 1, SessionID: "01A"}, ProbeResult{SessionID: "01A", OK: true, PendingAsk: true, PendingQuestion: question})
	question.Options[0] = "changed by the probe"
	if fromProbe.PendingQuestion.Options[0] != "Drop them" {
		t.Fatal("liveEntryFromProbe aliased the probe's option labels")
	}
	clone := CloneLiveEntry(fromProbe)
	clone.PendingQuestion.Options[0] = "changed by the clone"
	if fromProbe.PendingQuestion.Options[0] != "Drop them" {
		t.Fatal("CloneLiveEntry aliased the entry's option labels")
	}
}

// fuzzScenarioBuildTree_EveryRowNamesThePendingQuestion: an asking session's
// NeedsYou, Live and project rows and a meta-less live leaf name its first
// pending question from one closure, each row with its own copy; an entry that
// carries a question while its ask flag is clear names none on any row, as the
// approval detail does (S1b).
func fuzzScenarioBuildTree_EveryRowNamesThePendingQuestion(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01ASKING", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01STALE", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "01ASKING", Status: appwire.ThreadStatusAwaiting, PendingAsk: true, PendingQuestion: testPendingQuestion()},
		{PID: 2, SessionID: "01NOMETA", Status: appwire.ThreadStatusAwaiting, PendingAsk: true, PendingQuestion: testPendingQuestion()},
		{PID: 3, SessionID: "01STALE", Status: appwire.ThreadStatusAwaiting, PendingQuestion: testPendingQuestion()},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01ASKING")
	leaf, inLeaf, _, _ := liveAndProjectRowsFor(tree, "01NOMETA")
	var needsYou TreeNode
	for _, row := range tree.NeedsYou {
		if row.ID == "01ASKING" {
			needsYou = row
		}
	}
	if !inLive || !inProject || !inLeaf || needsYou.ID == "" {
		t.Fatalf("rows missing: live=%v project=%v leaf=%v needs-you=%+v", inLive, inProject, inLeaf, tree.NeedsYou)
	}
	rows := map[string]TreeNode{"NeedsYou": needsYou, "Live": liveRow, "project": projectRow, "live-only leaf": leaf}
	for name, row := range rows {
		if !reflect.DeepEqual(row.Question, testPendingQuestion()) {
			t.Fatalf("%s row question = %+v, want %+v", name, row.Question, testPendingQuestion())
		}
	}
	liveRow.Question.Options[0] = "changed on one row"
	if projectRow.Question.Options[0] != "Drop them" || needsYou.Question.Options[0] != "Drop them" {
		t.Fatal("two rows of one session share one question")
	}
	for _, row := range tree.NeedsYou {
		if row.ID == "01STALE" && row.Question != nil {
			t.Fatalf("a session whose ask flag is clear names the question %+v", row.Question)
		}
	}
	if _, _, stale, found := liveAndProjectRowsFor(tree, "01STALE"); !found || stale.Question != nil {
		t.Fatalf("stale session's project row = %+v (found %v), want no question", stale.Question, found)
	}
}

// fuzzScenarioBuildTree_DeadParentClearsItsSubagentsQuestion: a row clamped to
// ended under a dead parent asks nothing, so it names no question.
func fuzzScenarioBuildTree_DeadParentClearsItsSubagentsQuestion(t *testing.T) {
	child := staleSubagentOfDeadParent(t, LiveEntry{PID: 9, SessionID: "01STALESUB", Status: appwire.ThreadStatusAwaiting, PendingAsk: true, PendingQuestion: testPendingQuestion()})
	if child.State != "ended" || child.AskPending || child.Question != nil {
		t.Fatalf("stale subagent = state %q, ask %v, question %+v; want ended with no question", child.State, child.AskPending, child.Question)
	}
}
```

Register the five in `FuzzHubcoreScenarios`, each after its nearest alphabetical neighbour: `fuzzScenarioBuildTree_DeadParentClearsItsSubagentsQuestion` after `..._DeadParentClearsItsSubagentsApproval`, `fuzzScenarioBuildTree_EveryRowNamesThePendingQuestion` after `..._EveryRowCarriesTheTurnEndedTime`, `fuzzScenarioRoster_EntriesOwnTheirPendingQuestion` after `..._EntriesOwnTheirActivity`, `fuzzScenarioRoster_FingerprintMovesWhenOnlyTheQuestionMoves` after `..._FingerprintIgnoresActivity`, and `fuzzScenarioStatusProber_KeepsThePendingQuestion` after `..._KeepsTheLastTurnEndedTime`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`
Expected: FAIL to compile (`PendingQuestion` is not a field of `LiveEntry`; `Question` is not a field of `TreeNode`).

- [ ] **Step 3: Implement**

`roster.go`, in `LiveEntry` after `PendingEscalations`:

```go
	// PendingQuestion is the first question of the daemon's pending ask
	// (S1b), from the same root row as PendingAsk; nil while none waits and
	// from a daemon that predates it. A row names it only while PendingAsk is
	// set, and rosterFingerprint hashes it: a question answered and another
	// asked between two probes leaves PendingAsk and Status where they were.
	PendingQuestion *appwire.PendingQuestion
```

in `ProbeResult` after `PendingEscalations`:

```go
	// PendingQuestion mirrors LiveEntry.PendingQuestion: the first pending
	// question from the same root row as PendingAsk (S1b).
	PendingQuestion *appwire.PendingQuestion
```

In `CloneLiveEntry`, after the `PendingEscalations` copy: `out.PendingQuestion = appwire.ClonePendingQuestion(in.PendingQuestion)`. In `liveEntryFromProbe`, after `PendingEscalations:    result.PendingEscalations,`: `PendingQuestion:       result.PendingQuestion,` (it clones through `CloneLiveEntry`). In `ReadSpawnedThread`'s `ProbeResult` literal, after `PendingEscalations: root.Evener.PendingEscalations,`: `PendingQuestion:    root.Evener.PendingQuestion,`.

In `rosterFingerprint`, right after the escalation cards' loop and the `_, _ = h.Write([]byte{0})` that follows it:

```go
		// A row names the first pending question, and a question answered and
		// another asked between two probes holds the ask flag and the status
		// still (S1b).
		if question := bySess[id].PendingQuestion; question != nil {
			_, _ = h.Write([]byte(question.Question))
			_, _ = h.Write([]byte{0})
			for _, label := range question.Options {
				_, _ = h.Write([]byte(label))
				_, _ = h.Write([]byte{0})
			}
			_, _ = h.Write([]byte(strconv.Itoa(question.Count)))
		}
		_, _ = h.Write([]byte{0})
```

`prober.go`, in `Probe`'s result literal after `PendingEscalations:    root.Evener.PendingEscalations,`: `PendingQuestion:       root.Evener.PendingQuestion,`.

`tree.go`, in `TreeNode` after `ApprovalTarget string`:

```go
	// Question is the first question of the session's pending ask
	// (LiveEntry.PendingQuestion, S1b). Like the approval detail, every
	// builder sets it from one closure and only while AskPending is set, so a
	// row always names the question it says is pending.
	Question *appwire.PendingQuestion
```

After the `firstApprovalFor` closure:

```go
	// pendingQuestionFor resolves the first pending question for a session ID
	// from the same live map, for the reason firstApprovalFor does: every
	// builder names the same question, and only while the entry's ask flag is
	// set. Each row gets its own copy.
	pendingQuestionFor := func(id string) *appwire.PendingQuestion {
		if entry := liveMap[id]; entry.PendingAsk {
			return appwire.ClonePendingQuestion(entry.PendingQuestion)
		}
		return nil
	}
```

In `buildNode`: `question := pendingQuestionFor(m.ID)` after `approval := firstApprovalFor(m.ID)`; `question = nil` in the `parentDead` branch after `approval = appwire.SandboxEscalationRequested{}`; and `Question:        question,` after `ApprovalTarget:  approval.DeniedPath,` in the literal. The live-only leaf and the NeedsYou node: `Question:        pendingQuestionFor(le.SessionID),` after their `ApprovalTarget:  approval.DeniedPath,`. In `cloneTreeNodesContext`, after the `Tasks` copy: `out[index].Question = appwire.ClonePendingQuestion(node.Question)`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub/internal/hubcore -count=1`, `golangci-lint run ./cmd/evener-hub/internal/hubcore/`
Expected: PASS, and no unused scenario.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/internal/hubcore/roster.go cmd/evener-hub/internal/hubcore/prober.go cmd/evener-hub/internal/hubcore/tree.go cmd/evener-hub/internal/hubcore/prober_wire_test.go cmd/evener-hub/internal/hubcore/pending_question_test.go cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go
git commit -m "feat(hub): live entries and rows carry the first pending question"
```

### Task 8.2: Rows carry `question`

**Implementer:** Sonnet.

**Files:**
- Modify: `hubapi/navigation.go` (`NavigationQuestion` after `NavigationSubagentTally` `:241-246`; `NavigationSessionSummary.Question` after `ApprovalTarget` `:276`)
- Modify: `cmd/evener-hub/navigation_projection.go` (`projectShallow` `:1811-1850`; `navigationQuestion` after `navigationTaskProgress`; `cloneNavigationSummary` `:2169`)
- Modify: `cmd/evener-hub/navigation_schema.go` (the `appwire` import; `navigationSessionValueValid`'s final return `:447-448`; `navigationQuestionValid` after it)
- Modify: `appwire-client/typescript/state/navigation/codec.ts` (`QUESTION_KEYS`; `SESSION_KEYS` `:164-197`; `questionValue`; `sessionValue` `:310`)
- Modify: `appwire-client/typescript/state/navigation/codec.test.ts`
- Modify: `cmd/evener-hub/testdata/navigation/value-records.json`
- Create: `cmd/evener-hub/navigation_question_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`

**Interfaces:**
- Consumes: `TreeNode.Question` (Task 8.1); `appwire.BoundedPendingQuestion`, `appwire.Excerpt` and the question bounds (PR 7).
- Produces: `hubapi.NavigationQuestion{Text string; Options []string; Count int}` (JSON `text`, `options,omitempty`, `count`); `NavigationSessionSummary.Question *NavigationQuestion` (`question,omitempty`); `navigationQuestionValid(hubapi.NavigationQuestion) bool`; TypeScript `NavigationQuestion` and `NavigationSessionSummary.question?: NavigationQuestion`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/navigation_question_test.go` (it reuses `liveTaskRows` from `navigation_task_progress_test.go`, which projects tree rows onto the Live section and passes them through the hub schema, and `navigationSchemaSession` from `navigation_schema_test.go`):

```go
package hub

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// A row asking a question names it: its text, the option labels and how many
// questions wait (S1b). The hub re-cuts what a daemon sent to the wire's
// bounds, so a remote host or an older daemon cannot widen a row, and a row
// with no question carries no key.
func TestNavigationRowsCarryThePendingQuestion(t *testing.T) {
	rows := liveTaskRows(t, []hubcore.TreeNode{
		{ID: "session-asking", Title: "asking", Kind: "session", State: "awaiting", AskPending: true, Question: &appwire.PendingQuestion{
			Question: "Keep or drop the implied options?", Options: []string{"Drop them", "Keep them"}, Count: 2,
		}},
		{ID: "session-wide", Title: "wide", Kind: "session", State: "awaiting", AskPending: true, Question: &appwire.PendingQuestion{
			Question: "Line one\nline two " + strings.Repeat("word ", 100), Options: []string{"A", "", "B", "C", "D", "E", "F"}, Count: 1,
		}},
		{ID: "session-working", Title: "working", Kind: "session", State: "active"},
	})

	want := &hubapi.NavigationQuestion{Text: "Keep or drop the implied options?", Options: []string{"Drop them", "Keep them"}, Count: 2}
	if got := rows["session-asking"].Question; !reflect.DeepEqual(got, want) {
		t.Fatalf("asking row question = %+v, want %+v", got, want)
	}
	if got, want := string(navigationSummaryJSONFields(t, rows["session-asking"])["question"]), `{"text":"Keep or drop the implied options?","options":["Drop them","Keep them"],"count":2}`; got != want {
		t.Errorf("asking row question on the wire = %s, want %s", got, want)
	}
	wide := rows["session-wide"].Question
	if wide == nil || strings.ContainsAny(wide.Text, "\r\n") || utf8.RuneCountInString(wide.Text) > appwire.MaxQuestionTextRunes ||
		!reflect.DeepEqual(wide.Options, []string{"A", "B", "C", "D", "E"}) {
		t.Fatalf("wide row question = %+v, want one bounded line and five non-empty labels", wide)
	}
	if question, carried := navigationSummaryJSONFields(t, rows["session-working"])["question"]; carried {
		t.Fatalf("a row with no question carries %s", question)
	}
}

// A question the schema would refuse, which only a malformed daemon answer can
// carry, is dropped from its row instead of failing the whole resource; the
// row keeps its ask flag.
func TestNavigationRowsDropAQuestionTheSchemaRefuses(t *testing.T) {
	rows := liveTaskRows(t, []hubcore.TreeNode{
		{ID: "session-blank", Title: "blank", Kind: "session", State: "awaiting", AskPending: true, Question: &appwire.PendingQuestion{Question: " \n ", Count: 1}},
		{ID: "session-uncounted", Title: "uncounted", Kind: "session", State: "awaiting", AskPending: true, Question: &appwire.PendingQuestion{Question: "Which?"}},
	})
	for _, id := range []string{"session-blank", "session-uncounted"} {
		row, listed := rows[id]
		if !listed || !row.AskPending || row.Question != nil {
			t.Errorf("%s = %+v (listed %v), want the asking row listed without a question", id, row.Question, listed)
		}
	}
}

// The hub schema refuses a question the codec would refuse, and text the
// projector's cut never yields.
func TestNavigationSchemaBoundsTheQuestion(t *testing.T) {
	session := navigationSchemaSession("local:schema-session", "schema-session")
	session.Question = &hubapi.NavigationQuestion{
		Text:    strings.Repeat("é", appwire.MaxQuestionTextRunes),
		Options: []string{strings.Repeat("x", appwire.MaxQuestionOptionRunes), "B", "C", "D", "E"},
		Count:   1,
	}
	if !navigationSessionValueValid(session) {
		t.Fatal("a question at every bound was refused")
	}
	for name, question := range map[string]hubapi.NavigationQuestion{
		"empty text":                  {Count: 1},
		"text past its bound":         {Text: strings.Repeat("é", appwire.MaxQuestionTextRunes+1), Count: 1},
		"a line break":                {Text: "one\ntwo", Count: 1},
		"a leading space":             {Text: " Which?", Count: 1},
		"invalid UTF-8":               {Text: "ok\xff", Count: 1},
		"no question counted":         {Text: "Which?"},
		"six options":                 {Text: "Which?", Options: []string{"A", "B", "C", "D", "E", "F"}, Count: 1},
		"an empty option":             {Text: "Which?", Options: []string{""}, Count: 1},
		"an option past its bound":    {Text: "Which?", Options: []string{strings.Repeat("x", appwire.MaxQuestionOptionRunes+1)}, Count: 1},
		"an option with a line break": {Text: "Which?", Options: []string{"a\nb"}, Count: 1},
	} {
		session.Question = &question
		if navigationSessionValueValid(session) {
			t.Errorf("a question with %s was accepted", name)
		}
	}
}

// A cloned summary owns its question: a row handed to one reader cannot change
// another's.
func TestCloneNavigationSummaryOwnsTheQuestion(t *testing.T) {
	original := hubapi.NavigationSessionSummary{Question: &hubapi.NavigationQuestion{Text: "Which?", Options: []string{"A", "B"}, Count: 1}}
	clone := cloneNavigationSummary(original)
	original.Question.Options[0] = "changed"
	original.Question.Count = 9
	if clone.Question == nil || clone.Question.Options[0] != "A" || clone.Question.Count != 1 {
		t.Fatalf("clone question = %+v, want its own copy", clone.Question)
	}
}
```

In `value-records.json`, after `"approval_target": "/home/me/sites/docs/index.md",` in the session record:

```json
    "question": { "text": "Keep or drop the implied options?", "options": ["Drop them", "Keep them"], "count": 2 },
```

In `codec.test.ts`, before the comment `// cmd/evener-hub/navigation_value_records_test.go keeps this fixture naming` that opens "codec keeps every field the hub's value records carry":

```ts
// A live row asking a question names it (S1b): a nested value record the hub
// carries only while the row's ask flag is set. The codec keeps it, drops a
// key inside it that it does not know, and holds it to the hub schema's
// bounds (navigation_schema.go navigationQuestionValid).
test("codec keeps a row's pending question and drops keys inside it that it does not know", () => {
  const question = { text: "Keep or drop the implied options?", options: ["Drop them", "Keep them"], count: 2 };
  const rows = materializeSnapshot(
    key,
    decodedSnapshot(key, snapshotWithSessionField("question", { ...question, future_question_key: futureValue })),
  ).sessions as Array<Record<string, unknown>>;
  expect(rows[0]?.question).toEqual(question);
  const onTheBounds = {
    text: "😀".repeat(200),
    options: ["😀".repeat(80), "b", "c", "d", "e"],
    count: Number.MAX_SAFE_INTEGER,
  };
  expect(decodedSnapshot(key, snapshotWithSessionField("question", onTheBounds)).snapshot.entities[0]?.value).toEqual({
    ...sessionValue("local:session"),
    question: onTheBounds,
  });
});

test.each([
  ["null", null],
  ["a string in place of the record", "Keep or drop?"],
  ["a missing text", { count: 1 }],
  ["an empty text", { text: "", count: 1 }],
  ["an over-long text", { text: "t".repeat(201), count: 1 }],
  ["a missing count", { text: "Which?" }],
  ["no question counted", { text: "Which?", count: 0 }],
  ["a fractional count", { text: "Which?", count: 1.5 }],
  ["six options", { text: "Which?", options: ["a", "b", "c", "d", "e", "f"], count: 1 }],
  ["an empty option", { text: "Which?", options: [""], count: 1 }],
  ["an over-long option", { text: "Which?", options: ["o".repeat(81)], count: 1 }],
  ["a non-string option", { text: "Which?", options: [7], count: 1 }],
  ["options that are not a list", { text: "Which?", options: "a", count: 1 }],
] as const)("codec refuses a pending question with %s", (_name, question) => {
  expectContentFreeRejection(key, snapshotWithSessionField("question", question));
});
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestNavigationRowsCarryThePendingQuestion|TestNavigationRowsDropAQuestionTheSchemaRefuses|TestNavigationSchemaBoundsTheQuestion|TestCloneNavigationSummaryOwnsTheQuestion|TestNavigationValueRecordFixtureNamesEveryWireField' -count=1`
Expected: FAIL to compile (`hubapi.NavigationQuestion` undefined).

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation/codec.test.ts`
Expected: FAIL: "codec keeps every field the hub's value records carry" and the new "keeps" test (the codec drops `question`), and every "refuses" case (an unknown key is dropped, not refused).

- [ ] **Step 3: Implement**

`hubapi/navigation.go`, after `NavigationSubagentTally`:

```go
// NavigationQuestion is the first question of a live session's pending ask
// (S1b): a Needs you row's why line ("Question · keep or drop the implied
// options?"), the option labels a long-press preview lists, and how many
// questions the ask holds. Text and each label are one line, cut to the
// wire's bounds (appwire.BoundedPendingQuestion).
type NavigationQuestion struct {
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
	Count   int      `json:"count"`
}
```

In `NavigationSessionSummary`, replace the `Dormant` line that follows `ApprovalTarget` with these lines (gofmt aligns the pair):

```go
	// Question is the first question of the session's pending ask (S1b). It
	// is present only on a row that carries AskPending and whose daemon named
	// the question.
	Question *NavigationQuestion `json:"question,omitempty"`
	Dormant  bool                `json:"dormant,omitempty"`
```

`navigation_projection.go`: in `projectShallow`'s literal, after `ApprovalTarget: ...,` add `Question:            navigationQuestion(node.Question),`; after `navigationTaskProgress`:

```go
// navigationQuestion is a row's pending question on the wire: re-cut to the
// wire's bounds (appwire.BoundedPendingQuestion), so a remote host or an older
// daemon cannot widen a row, and dropped when the schema would refuse it (no
// text left, or no question counted), the way navigationTaskProgress drops bad
// progress rather than fail the resource.
func navigationQuestion(question *appwire.PendingQuestion) *hubapi.NavigationQuestion {
	if question == nil {
		return nil
	}
	bounded := appwire.BoundedPendingQuestion(question.Question, question.Options, question.Count)
	wire := hubapi.NavigationQuestion{Text: bounded.Question, Options: bounded.Options, Count: bounded.Count}
	if !navigationQuestionValid(wire) {
		return nil
	}
	return &wire
}
```

and in `cloneNavigationSummary`, after `clone.Subagents = clonePointer(summary.Subagents)`:

```go
	if summary.Question != nil {
		question := *summary.Question
		question.Options = append([]string(nil), summary.Question.Options...)
		clone.Question = &question
	}
```

`navigation_schema.go`: add `"primeradiant.com/evener/appwire"` to the imports, end `navigationSessionValueValid` with

```go
	return (value.Tasks == nil || navigationTaskProgressValid(*value.Tasks)) &&
		(value.Subagents == nil || navigationSubagentTallyValid(*value.Subagents)) &&
		(value.Question == nil || navigationQuestionValid(*value.Question))
}
```

and add after it:

```go
// navigationQuestionValid mirrors the web codec's questionValue: text that is
// not empty, at least one question counted, and at most five labels, none of
// them empty. The hub also holds each text to be an excerpt at its bound
// (appwire.Excerpt leaves it unchanged: valid UTF-8, one trimmed line, within
// the bound), which the projector's cut always yields. The projector drops a
// question this refuses rather than failing the whole resource.
func navigationQuestionValid(question hubapi.NavigationQuestion) bool {
	if question.Text == "" || appwire.Excerpt(question.Text, appwire.MaxQuestionTextRunes) != question.Text ||
		question.Count < 1 || !navigationIntCount(question.Count) || len(question.Options) > appwire.MaxQuestionOptions {
		return false
	}
	for _, label := range question.Options {
		if label == "" || appwire.Excerpt(label, appwire.MaxQuestionOptionRunes) != label {
			return false
		}
	}
	return true
}
```

`codec.ts`: after `SUBAGENT_TALLY_KEYS`, add `const QUESTION_KEYS = valueRecordKeys(["text", "count"], ["options"]);`; add `"question"` to `SESSION_KEYS`' optional list after `"approval_target"` and `question: QUESTION_KEYS,` to its nested map after `subagents`; before `function sessionValue`:

```ts
// Mirrors navigationQuestionValid's bounds: text of 1 to 200 characters, at
// least one question counted, and at most five labels of 1 to 80 characters.
const questionValue = (value: unknown): boolean =>
  knownKeys(value, QUESTION_KEYS) &&
  boundedString(value.text, 200) &&
  value.text !== "" &&
  count(value.count) &&
  (value.count as number) >= 1 &&
  optional(
    value.options,
    (item) =>
      Array.isArray(item) && item.length <= 5 && item.every((label) => boundedString(label, 80) && label !== ""),
  );
```

and in `sessionValue`, after the `approval_target` line: `optional(value.question, questionValue) &&`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched Go files, `make generate`, and `cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/state/navigation/codec.ts ../../../appwire-client/typescript/state/navigation/codec.test.ts`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub -run 'TestNavigation|TestCloneNavigationSummary' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`, `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation && npm run typecheck`
Expected: PASS; `types.gen.ts` gains `NavigationQuestion` and `question?: NavigationQuestion` on `NavigationSessionSummary`.

- [ ] **Step 5: Commit**

```bash
git add hubapi/navigation.go cmd/evener-hub/navigation_projection.go cmd/evener-hub/navigation_schema.go cmd/evener-hub/navigation_question_test.go cmd/evener-hub/testdata/navigation/value-records.json appwire-client/typescript/state/navigation/codec.ts appwire-client/typescript/state/navigation/codec.test.ts appwire-client/typescript/types.gen.ts
git commit -m "feat(hub): navigation rows name the first pending question"
```

### Task 8.3: A remote host's rows carry their question

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go` (`LocalDaemonEntry.PendingQuestion` after `PendingEscalations`; `threadFromEntry`'s `Evener` literal `:1150-1156`)
- Create: `cmd/evener-hub/internal/appsource/local_daemon_question_test.go`
- Modify: `cmd/evener-hub/app_rpc.go` (`localDaemonEntriesFromRoster` `:116-171`)
- Modify: `cmd/evener-hub/web_api_tree.go` (`appThreadTreeEntries`' `LiveEntry` literal `:950-963`)
- Create: `cmd/evener-hub/remote_question_test.go`

**Interfaces:**
- Consumes: `LiveEntry.PendingQuestion` (Task 8.1); `NavigationSessionSummary.Question` (Task 8.2).
- Produces: `appsource.LocalDaemonEntry.PendingQuestion *appwire.PendingQuestion`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/internal/appsource/local_daemon_question_test.go`:

```go
package appsource

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller carries each asking root's
// first pending question on its row, and no key on a row with none (S1b).
func TestLocalDaemonSourceListCarriesTheRootsPendingQuestion(t *testing.T) {
	question := &appwire.PendingQuestion{Question: "Keep or drop the implied options?", Options: []string{"Drop them", "Keep them"}, Count: 1}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/asking", ThreadID: "th_asking", SessionID: "sess_asking"}, Status: appwire.ThreadStatusAwaiting, PendingAsk: true, PendingQuestion: question},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/idle", ThreadID: "th_idle", SessionID: "sess_idle"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]*appwire.PendingQuestion{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread.Evener.PendingQuestion
	}
	if !reflect.DeepEqual(byID["th_asking"], question) || byID["th_idle"] != nil {
		t.Fatalf("questions = %+v, want th_asking's %+v and none on th_idle", byID, question)
	}
	byID["th_asking"].Options[0] = "changed"
	if question.Options[0] != "Drop them" {
		t.Fatal("a listed row aliases the roster's question")
	}
}
```

`cmd/evener-hub/remote_question_test.go`:

```go
package hub

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows carry a root's pending question and never lend it to
// the root's in-process subagent aliases: a subagent never asks the user.
func TestLocalDaemonEntriesFromRosterCarryTheQuestionOnlyOnTheRoot(t *testing.T) {
	question := &appwire.PendingQuestion{Question: "Which datastore?", Options: []string{"Postgres", "SQLite"}, Count: 1}
	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{{
		Entry:     rendezvous.Entry{ThreadID: "sess_root", SessionID: "sess_root"},
		SessionID: "sess_root", Status: appwire.ThreadStatusAwaiting, PendingAsk: true,
		RunningSubagentIDs: []string{"sess_child"}, PendingQuestion: question,
	}})
	if len(entries) != 2 || !reflect.DeepEqual(entries[0].PendingQuestion, question) || entries[1].PendingQuestion != nil {
		t.Fatalf("entries = %+v, want the root's question %+v and none on its alias", entries, question)
	}
}

// A controller reads a remote host's pending question off its list row, so
// the remote row names it exactly like a local one (S1b).
func TestNavigationRemoteRowCarriesItsPendingQuestion(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID: "asking", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusAwaiting},
		Evener: appwire.EvenerThread{AskPending: true, PendingQuestion: &appwire.PendingQuestion{Question: "Which datastore?", Options: []string{"Postgres", "SQLite"}, Count: 2}},
	}})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})

	captured, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	projection, err := buildNavigationProjection(captured.Inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	want := &hubapi.NavigationQuestion{Text: "Which datastore?", Options: []string{"Postgres", "SQLite"}, Count: 2}
	if row := navigationProjectedSummary(t, projection, "host-a:asking"); !row.AskPending || !reflect.DeepEqual(row.Question, want) {
		t.Fatalf("remote row = ask %v, question %+v; want %+v", row.AskPending, row.Question, want)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/appsource -run 'TestLocalDaemonSourceListCarriesTheRootsPendingQuestion' -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRosterCarryTheQuestionOnlyOnTheRoot|TestNavigationRemoteRowCarriesItsPendingQuestion' -count=1`
Expected: FAIL to compile (`PendingQuestion` is not a field of `LocalDaemonEntry`); once it exists, the remote row test fails with no question.

- [ ] **Step 3: Implement**

`local_daemon.go`, in `LocalDaemonEntry` after `PendingEscalations`:

```go
	// PendingQuestion mirrors hubcore.LiveEntry.PendingQuestion: the root's
	// first pending question (S1b). threadFromEntry carries it into
	// appwire.EvenerThread.PendingQuestion, so a controller hub listing this
	// hub's sessions names the question as a local probe would. A read-only
	// alias has none.
	PendingQuestion *appwire.PendingQuestion
```

and in `threadFromEntry`'s `Evener` literal, after `AskPending:         item.PendingAsk,`: `PendingQuestion:    appwire.ClonePendingQuestion(item.PendingQuestion),`.

`app_rpc.go`, in `localDaemonEntriesFromRoster`: `PendingQuestion:    item.PendingQuestion,` after `PendingEscalations: item.PendingEscalations,` in the root entry's literal, and `child.PendingQuestion = nil` after `child.PendingEscalations = nil` among the alias's clears (a subagent never asks the user, the comment above them says).

`web_api_tree.go`, in `appThreadTreeEntries`' `LiveEntry` literal after `PendingEscalations: thread.Evener.PendingEscalations,`: `PendingQuestion:    appwire.ClonePendingQuestion(thread.Evener.PendingQuestion),`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./cmd/evener-hub/internal/appsource -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRoster|TestNavigationRemote|TestNavigationOffline' -count=1`
Expected: PASS.

Gates: `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...`, `golangci-lint run ./cmd/evener-hub/ ./cmd/evener-hub/internal/appsource/ ./cmd/evener-hub/internal/hubcore/ ./hubapi/`, `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`, `make lint-generated`, `make secret-scan`.

- [ ] **Step 5: Commit and open PR 8**

```bash
git add cmd/evener-hub/internal/appsource/local_daemon.go cmd/evener-hub/internal/appsource/local_daemon_question_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/web_api_tree.go cmd/evener-hub/remote_question_test.go
git commit -m "feat(hub): remote hosts' rows carry their pending question"
```

Title "feat(hub): rows name the first pending question (S1b hub, phase 7 PR 8)". The body names the fallback it retires ("waiting for your answer"), says the row names the question only while `ask_pending` is set, the hub re-cuts it to the wire's bounds, remote rows carry theirs, and points the phone lane at the handoff section of this plan.

---

## PR 9: the failure summary, S1c daemon and hub (Tasks 9.1-9.5)

**Branch:** `git fetch origin && git switch -c claude/s1c-failure-summary origin/main`, once PR 7 is on main. Tasks 9.1 and 9.2 need only PR 7 and can start beside PR 8; one anchor in Task 9.2 names a PR 8 line and says where to go without it. Tasks 9.3 to 9.5 add each field beside the one PR 8 added (`PendingQuestion`, `pendingQuestionFor`, `NavigationQuestion`, `QUESTION_KEYS`), so before Task 9.3 PR 8 must be on main: `git fetch origin && git merge origin/main`, then `make generate` if `types.gen.ts` conflicted.

**What it adds.** A session resting on a failed turn summarizes it (the failure's title and structured cause, never its message), thread snapshots carry the summary while the status is systemError, and Failed rows carry `failure`; a crashed daemon's rows carry the hub's own `crashed` cause, and a remote host's rows carry theirs. The phone's fallback (`REASONS.failed`, "open the session to see what went wrong") retires as the handoff section says.

### Task 9.1: The session summarizes the failed turn it rests on

**Implementer:** Sonnet.

**Files:**
- Modify: `agent/session_state.go` (`RestingWireState` and its comment `:79-102`; `historyEndsInTurnFailure` `:104-123` becomes `historyTurnFailure`, beside two new methods)
- Modify: `agent/session_turn_failure_state_test.go` (`TestHistoryEndsInTurnFailure`'s call, `:44-45`)
- Modify: `appwire/excerpt.go` (`MaxFailureTitleRunes`)
- Modify: `appwire/types.go` (`ThreadFailure` after `SubagentTally`, before `PendingQuestion`)
- Create: `agent/session_resting_failure_test.go`

**Interfaces:**
- Consumes: `appwire.Excerpt` (PR 7); `appwire.DiagnosticCause` (`appwire/types.go:1549`); `schema.TurnFailureInfo` (`agent/schema/turn.go:260`).
- Produces: `appwire.ThreadFailure{Title string; Cause *DiagnosticCause}` (JSON `title,omitempty`, `cause,omitempty`); `appwire.MaxFailureTitleRunes = 80`; `(*agent.Session).RestingFailure() *appwire.ThreadFailure`; `restingFailureLocked() (schema.Turn, bool)` and `historyTurnFailure([]schema.Turn) (schema.Turn, bool)` in package `agent`.

- [ ] **Step 1: Write the failing tests**

`agent/session_resting_failure_test.go` (it reuses `failingThenRecoveringSession`, `failedCarrierWithPendingQuestion` and `restoreClosedSession` from `session_turn_failure_state_test.go`; the first fails its first model call with `llm.ErrorFromHTTPStatus("openai", 403, "sign-in rejected", nil, nil)`):

```go
package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// failureJSON is a failure summary as the wire carries it.
func failureJSON(t *testing.T, failure *appwire.ThreadFailure) string {
	t.Helper()
	raw, err := json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A session resting on a failed turn summarizes it for its row (S1c): the
// failure's headline and its structured cause, and never its message ("sign-in
// rejected" here), which can quote a provider's error body. The next turn to
// start clears it.
func TestRestingFailure_SummarizesTheFailedTurnUntilTheNextTurn(t *testing.T) {
	t.Parallel()
	sess := failingThenRecoveringSession(t, t.TempDir())
	defer sess.Close()
	if got := sess.RestingFailure(); got != nil {
		t.Fatalf("a session that has not failed rests on %s", failureJSON(t, got))
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err == nil {
		t.Fatal("first turn succeeded, want the scripted provider failure")
	}
	want := `{"title":"Provider error","cause":{"kind":"provider","provider":"openai","model":"test-model","status":403}}`
	if got := failureJSON(t, sess.RestingFailure()); got != want {
		t.Fatalf("RestingFailure = %s, want %s", got, want)
	}
	if _, err := sess.ProcessInput(ctx, "second", nil); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := sess.RestingFailure(); got != nil {
		t.Fatalf("after the next clean turn RestingFailure = %s, want none", failureJSON(t, got))
	}
}

// A question still pending outranks the failure: the session reads awaiting on
// the wire, and its row names the question, not the failure (S1c).
func TestRestingFailure_NoneWhileAQuestionWaits(t *testing.T) {
	t.Parallel()
	sess := failedCarrierWithPendingQuestion(t, t.TempDir(), SessionConfig{})
	defer sess.Close()
	if got := sess.RestingFailure(); got != nil {
		t.Fatalf("RestingFailure with a pending question = %s, want none", failureJSON(t, got))
	}
}

// A daemon restarted after a failed turn summarizes the failure the live
// session did: restore rebuilds the history the summary reads (S1c).
func TestRestingFailure_SurvivesRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := failingThenRecoveringSession(t, dir)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err == nil {
		t.Fatal("first turn succeeded, want the scripted provider failure")
	}
	live := sess.RestingFailure()
	if live == nil {
		t.Fatal("the live session summarizes no failure")
	}
	restored, _ := restoreClosedSession(t, sess, dir, "test-model")
	if got, want := failureJSON(t, restored.RestingFailure()), failureJSON(t, live); got != want {
		t.Fatalf("restored RestingFailure = %s, want the live %s", got, want)
	}
}

// A failure recorded with no diagnostic (a legacy entry) still reads Failed on
// the wire, and has nothing to summarize.
func TestRestingFailure_NoneForAFailureWithNoDiagnostic(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	sess.mu.Lock()
	sess.history = append(sess.history, schema.NewTurn(schema.TurnUserInput, llm.User("go")), schema.NewTurn(schema.TurnFailure, llm.System("it broke")))
	sess.mu.Unlock()
	if got := sess.RestingWireState(); got != appwire.ThreadStatusSystemError {
		t.Fatalf("RestingWireState = %q, want %q", got, appwire.ThreadStatusSystemError)
	}
	if got := sess.RestingFailure(); got != nil {
		t.Fatalf("RestingFailure = %s, want none: the failure recorded no diagnostic", failureJSON(t, got))
	}
}
```

In `session_turn_failure_state_test.go`, `TestHistoryEndsInTurnFailure` keeps its cases and calls the renamed helper:

```go
			if _, got := historyTurnFailure(c.history); got != c.want {
				t.Fatalf("historyTurnFailure found a failure = %v, want %v", got, c.want)
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd agent && go test . -run 'TestRestingFailure|TestHistoryEndsInTurnFailure' -count=1`
Expected: FAIL to compile (`RestingFailure`, `historyTurnFailure` and `appwire.ThreadFailure` undefined).

- [ ] **Step 3: Implement**

`appwire/excerpt.go`, at the end of the constant block:

```go
	// MaxFailureTitleRunes bounds a failure's headline, a Failed row's why
	// line ("Provider error", "Usage limit reached").
	MaxFailureTitleRunes = 80
```

`appwire/types.go`, after the `SubagentTally` type:

```go
// ThreadFailure summarizes the failed turn a session rests on (S1c): the
// failure's headline (the diagnostic classifier's title, such as "Provider
// error" or "Usage limit reached", cut to one line of MaxFailureTitleRunes)
// and its structured cause. It never carries the failure's message, which can
// quote a provider's error body; the transcript's error row has that.
type ThreadFailure struct {
	Title string           `json:"title,omitempty"`
	Cause *DiagnosticCause `json:"cause,omitempty"`
}
```

`agent/session_state.go`: in `RestingWireState`'s comment, `(historyEndsInTurnFailure)` becomes `(restingFailureLocked)`. Replace `RestingWireState`'s body and the whole `historyEndsInTurnFailure` function (comment included) with:

```go
func (s *Session) RestingWireState() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, failed := s.restingFailureLocked(); failed {
		return appwire.ThreadStatusSystemError
	}
	return string(s.state)
}

// RestingFailure summarizes the failed turn the session rests on for its row's
// why line (S1c): the failure's headline and its structured cause, never its
// message, which can quote a provider's error body. It reads what
// RestingWireState reads, under the same one hold, so it is present only while
// the session publishes systemError, and a restored session summarizes the
// failure the live one did. It is nil when the session rests on no failed
// turn, or when the failure recorded no diagnostic (a legacy entry): the row
// still reads Failed, and there is nothing more to say.
func (s *Session) RestingFailure() *appwire.ThreadFailure {
	s.mu.Lock()
	defer s.mu.Unlock()
	turn, failed := s.restingFailureLocked()
	if !failed || turn.Error == nil {
		return nil
	}
	failure := &appwire.ThreadFailure{Title: appwire.Excerpt(turn.Error.Title, appwire.MaxFailureTitleRunes)}
	if cause := turn.Error.Cause; cause != nil {
		failure.Cause = &appwire.DiagnosticCause{Kind: cause.Kind, Provider: cause.Provider, Model: cause.Model, Status: cause.Status}
	}
	if failure.Title == "" && failure.Cause == nil {
		return nil
	}
	return failure
}

// restingFailureLocked is the recorded turn failure the session rests on: it is
// idle, or awaiting with no pending question, and its history ends in the
// failure (historyTurnFailure). The caller holds s.mu.
func (s *Session) restingFailureLocked() (schema.Turn, bool) {
	resting := s.state == SessionIdle || s.state == SessionAwaiting
	if !resting || len(s.askPending) != 0 {
		return schema.Turn{}, false
	}
	return historyTurnFailure(s.history)
}

// historyTurnFailure returns the recorded turn failure the history ends in,
// when its last turn-bearing record is one (a TurnFailure, written by
// emitTurnFailure and emitSteeringCarrierTurnFailure): the session's last turn
// failed and no turn has started since. A turn-bearing record is one a turn
// writes as it runs: user input, steering (the interrupt marker included), an
// assistant response, tool results. Bookkeeping records (TurnSystem,
// TurnCheckpoint, TurnSummary, TurnModelSwitch, TurnHookCompleted,
// TurnEnvironment, TurnNotesContext, TurnAttentionResolution) are skipped, so
// a model switch or a hook line after the failure leaves the session failed.
func historyTurnFailure(history []schema.Turn) (schema.Turn, bool) {
	for i := range slices.Backward(history) {
		switch history[i].Kind {
		case schema.TurnFailure:
			return history[i], true
		case schema.TurnUserInput, schema.TurnSteering, schema.TurnAssistant, schema.TurnTool, schema.TurnToolResults:
			return schema.Turn{}, false
		}
	}
	return schema.Turn{}, false
}
```

Run `$(go env GOROOT)/bin/gofmt -w` on the five touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `cd agent && go test . -run 'TestRestingFailure|TestHistoryEndsInTurnFailure|TestWireState|TestRestore_FailedTurn' -count=1`
Expected: PASS: the new tests, and every failed-turn state test from #2514 unchanged (`RestingWireState` reads the same condition through `restingFailureLocked`). The exact summary is `{"title":"Provider error","cause":{"kind":"provider","provider":"openai","model":"test-model","status":403}}`; `diagnostic.FromError` titles a provider `llm.Error` "Provider error" (`agent/diagnostic/diagnostic.go:77-80`, `providerFailure` `:162-168`).

- [ ] **Step 5: Commit**

```bash
git add agent/session_state.go agent/session_turn_failure_state_test.go agent/session_resting_failure_test.go appwire/excerpt.go appwire/types.go
git commit -m "feat(agent): a session summarizes the failed turn it rests on"
```

### Task 9.2: Thread snapshots carry the summary while the status is systemError

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire/types.go` (`EvenerThread.Failure` after `PendingQuestion`)
- Modify: `appwire/clone.go` (`CloneThreadFailure` after `ClonePendingQuestion`; one line in `cloneEvenerThread`)
- Modify: `appwire/clone_test.go`
- Modify: `agent/session_envelope_sampling.go` (`RestingFailure()` after `PendingQuestion()`)
- Modify: `server/thread_envelope.go` (`threadEnvelope.Failure`; `ThreadEnvelopeSource.RestingFailure`; `facetTurnFailure` and `facetAll` `:151-170`; `refreshFacets`; `assign`)
- Modify: `server/appwire_runtime.go` (`appThreadWithDiagnosticsLocked` `:2481-2574`)
- Modify: `cmd/evener/serve.go` (`liveThreadEnvelopeSource.RestingFailure` after `PendingQuestion`)
- Modify the other implementations: `server/thread_envelope_test_helpers_test.go`, `server/thread_envelope_test.go`, `cmd/evener-hub/app_threadread_tasks_test.go`, `cmd/evener-hub/internal/hubcore/prober_wire_test.go`, `cmd/evener/serve_residual_fuzz_test.go`. `sessionAskPendingEnvelopeSource` (`server/askpending_live_notification_test.go`) embeds the server stub and needs no edit.
- Create: `server/thread_envelope_failure_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`

**Interfaces:**
- Consumes: `Session.RestingFailure`, `appwire.ThreadFailure` (Task 9.1).
- Produces: `EvenerThread.Failure *ThreadFailure` (`failure,omitempty`), on `thread/read` and `thread/list` while the status is systemError; `appwire.CloneThreadFailure`; `ThreadEnvelopeSource.RestingFailure() *appwire.ThreadFailure`; `EnvelopeSampling.RestingFailure()`; TypeScript `ThreadFailure` and `EvenerThread.failure?: ThreadFailure`.

- [ ] **Step 1: Write the failing tests**

`server/thread_envelope_failure_test.go`:

```go
package server

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// The root row and thread/read carry the failure the session rests on while
// its status is systemError, and never otherwise (S1c). The status is the gate:
// the next turn moves the status first, so a summary sampled before that turn
// started cannot outlive the failure. TURN_ENDED re-samples the summary,
// because it re-samples every facet, so a second failure replaces the first.
func TestThreadSnapshotsCarryTheRestingFailureOnlyWhileFailed(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	first := &appwire.ThreadFailure{Title: "Provider error", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Status: 401}}
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{failure: first})
	listed := func() *appwire.ThreadFailure {
		t.Helper()
		list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{StatusOnly: true})
		if err != nil {
			t.Fatalf("thread/list: %v", err)
		}
		return list.Data[0].Evener.Failure
	}

	srv.SetState(appwire.ThreadStatusIdle)
	if got := listed(); got != nil {
		t.Fatalf("an idle session's row carries the failure %+v", got)
	}
	srv.SetState(appwire.ThreadStatusSystemError)
	if got := listed(); !reflect.DeepEqual(got, first) {
		t.Fatalf("a failed session's row carries %+v, want %+v", got, first)
	}
	if got := readThreadOverWire(t, srv, "local:root").Evener.Failure; !reflect.DeepEqual(got, first) {
		t.Fatalf("a failed session's thread/read carries %+v, want %+v", got, first)
	}

	second := &appwire.ThreadFailure{Title: "Usage limit reached", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Status: 429}}
	src.failure = second
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "root", Data: events.TurnEndedData{TurnDurationMS: 1_000}}, nil)
	if got := listed(); !reflect.DeepEqual(got, second) {
		t.Fatalf("after TURN_ENDED the row carries %+v, want %+v", got, second)
	}

	srv.SetState(appwire.ThreadStatusActive)
	if got := listed(); got != nil {
		t.Fatalf("a working session's row carries the failure %+v", got)
	}
}
```

Append to `appwire/clone_test.go`:

```go
func TestCloneThreadOwnsTheFailure(t *testing.T) {
	original := Thread{Evener: EvenerThread{Failure: &ThreadFailure{Title: "Provider error", Cause: &DiagnosticCause{Kind: "provider", Status: 401}}}}
	clone := CloneThread(original)
	if !reflect.DeepEqual(clone, original) {
		t.Fatal("clone changed values while copying")
	}
	clone.Evener.Failure.Title = "changed"
	clone.Evener.Failure.Cause.Status = 500
	if original.Evener.Failure.Title != "Provider error" || original.Evener.Failure.Cause.Status != 401 {
		t.Fatalf("the failure was changed through its clone: %+v", original.Evener.Failure)
	}
	if CloneThreadFailure(nil) != nil {
		t.Fatal("a nil failure cloned to a non-nil one")
	}
}
```

In `server/thread_envelope_test_helpers_test.go`, give `stubThreadEnvelopeSource` a field `failure *appwire.ThreadFailure` after `question`, and add before its `PendingEscalations` method:

```go
func (s *stubThreadEnvelopeSource) RestingFailure() *appwire.ThreadFailure { return s.failure }
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./server -run 'TestThreadSnapshotsCarryTheRestingFailureOnlyWhileFailed' -count=1` and `go test ./appwire -run 'TestCloneThreadOwnsTheFailure' -count=1`
Expected: FAIL to compile (`EvenerThread.Failure` and `CloneThreadFailure` undefined).

- [ ] **Step 3: Implement**

`appwire/types.go`, after `EvenerThread.PendingQuestion`:

```go
	// Failure summarizes the failed turn the session rests on (S1c). It is
	// present only while the thread's status is systemError, and absent from
	// an older daemon and for a failure that recorded no diagnostic.
	// Snapshot-only: no notification carries it.
	Failure *ThreadFailure `json:"failure,omitempty"`
```

`appwire/clone.go`: in `cloneEvenerThread`, after the `PendingQuestion` line, `e.Failure = CloneThreadFailure(e.Failure)`; after `ClonePendingQuestion`:

```go
// CloneThreadFailure returns a copy of value whose cause is its own; nil stays
// nil.
func CloneThreadFailure(value *ThreadFailure) *ThreadFailure {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Cause = clonePointer(value.Cause)
	return &clone
}
```

`agent/session_envelope_sampling.go`: add `RestingFailure() *appwire.ThreadFailure` after `PendingQuestion() *appwire.PendingQuestion`. It takes only `s.mu`, which `session_envelope_sampling_test.go` proves with no edit.

`server/thread_envelope.go`:
- In `threadEnvelope`, after `PendingQuestion`, add the field below; gofmt then aligns the ask pair on its own, since the new comment starts a new alignment section:

```go
	// Failure summarizes the failed turn the session rests on (S1c). The
	// snapshot shows it only while the status is systemError.
	Failure               *appwire.ThreadFailure
```

- In `ThreadEnvelopeSource`, after `PendingQuestion() *appwire.PendingQuestion`:

```go
	// RestingFailure summarizes the failed turn the session rests on, nil
	// when it rests on none.
	RestingFailure() *appwire.ThreadFailure
```

- In the facet constants, after `facetMeta`:

```go
	// facetTurnFailure is the summary of the failed turn the session rests on
	// (S1c). No row of facetsByEvent names it on its own: a failed turn ends
	// with TURN_ENDED, which samples every facet (the failed-turn path emits
	// nothing else), and a snapshot shows the summary only while the status
	// is systemError, so one left over once the next turn starts is never read.
	facetTurnFailure
```

  and add `| facetTurnFailure` at the end of `facetAll`.
- In `refreshFacets`, after the ask facet:

```go
	if facets&facetTurnFailure != 0 {
		next.Failure = src.RestingFailure()
	}
```

- In `assign`, after the ask facet:

```go
	if facets&facetTurnFailure != 0 {
		e.Failure = next.Failure
	}
```

`server/appwire_runtime.go`, in `appThreadWithDiagnosticsLocked`: after `pendingEscalations := envelope.PendingEscalations`:

```go
	statusType := appStatus(status.State, processing, turnReserved)
	// A failure summary describes the failed turn the session rests on, so it
	// shows only while the status says so: the next turn moves the status
	// before any facet re-samples the summary (S1c).
	var failure *appwire.ThreadFailure
	if statusType == appwire.ThreadStatusSystemError {
		failure = envelope.Failure
	}
```

then `Status:        appwire.ThreadStatus{Type: statusType},` in place of the inline `appStatus(...)` call, and `Failure:               failure,` after `PendingQuestion:       pendingQuestion,` in the `EvenerThread` literal.

`cmd/evener/serve.go`, after `liveThreadEnvelopeSource.PendingQuestion`:

```go
func (l liveThreadEnvelopeSource) RestingFailure() *appwire.ThreadFailure {
	return l.session().RestingFailure()
}
```

The other implementations:
- `server/thread_envelope_test.go`, after `countingThreadEnvelopeSource.PendingQuestion`:

```go
func (c *countingThreadEnvelopeSource) RestingFailure() *appwire.ThreadFailure {
	c.hit()
	return nil
}
```

- `cmd/evener-hub/app_threadread_tasks_test.go`, after `sessionTaskEnvelopeSource.PendingQuestion`:

```go
func (s sessionTaskEnvelopeSource) RestingFailure() *appwire.ThreadFailure {
	return nil
}
```

- `cmd/evener-hub/internal/hubcore/prober_wire_test.go`: a field `failure *appwire.ThreadFailure` after `question` in `wireProbeEnvelopeSource` (PR 8 adds `question`; without PR 8 on main, put `failure` after `askPending`), and after its `PendingQuestion` method: `func (s wireProbeEnvelopeSource) RestingFailure() *appwire.ThreadFailure { return s.failure }`.
- `cmd/evener/serve_residual_fuzz_test.go`: `_ = s.envelopeSource.RestingFailure()` after the `PendingQuestion()` line.

Run `$(go env GOROOT)/bin/gofmt -w` on every touched Go file, then `make generate`.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./server -count=1`, `go test ./appwire -count=1`, `cd agent && go test . -run 'TestEnvelopeSampling|TestEverySamplingRelevantMutexIsClassified' -count=1`, `go test ./cmd/evener -run 'Notes|Envelope|Ask' -count=1`, `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$|TestStatusProber' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`
Expected: PASS; `types.gen.ts` gains `ThreadFailure` and `failure?: ThreadFailure` on `EvenerThread`.

Gates, in the root module and in `agent/`: `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...`; `golangci-lint run ./appwire/ ./cmd/evener/ ./cmd/evener-hub/ ./cmd/evener-hub/internal/hubcore/ ./server/`, `golangci-lint run --config .golangci-appwire.yml ./server/...`, `cd agent && golangci-lint run .`; `make lint-generated`.

- [ ] **Step 5: Commit**

```bash
git add appwire/types.go appwire/clone.go appwire/clone_test.go agent/session_envelope_sampling.go server/thread_envelope.go server/appwire_runtime.go server/thread_envelope_failure_test.go server/thread_envelope_test_helpers_test.go server/thread_envelope_test.go cmd/evener/serve.go cmd/evener/serve_residual_fuzz_test.go cmd/evener-hub/app_threadread_tasks_test.go cmd/evener-hub/internal/hubcore/prober_wire_test.go appwire-client/typescript/types.gen.ts
git commit -m "feat(server): thread snapshots carry the failure summary while the session is failed"
```

### Task 9.3: Live entries and tree rows carry the failure

**Implementer:** Sonnet.

**Files:**
- Modify: `hubapi/navigation.go` (`NavigationFailureCrashed` before `NavigationQuestion`)
- Modify: `cmd/evener-hub/internal/hubcore/roster.go` (`LiveEntry.Failure` after `PendingQuestion`; `ProbeResult.Failure` after its `PendingQuestion`; `CloneLiveEntry`; `rosterFingerprint`, after the question's block; `liveEntryFromProbe`; `ReadSpawnedThread`)
- Modify: `cmd/evener-hub/internal/hubcore/prober.go` (`Probe`'s result literal)
- Modify: `cmd/evener-hub/internal/hubcore/tree.go` (`TreeNode.Failure` after `Question`; a `failureFor` closure after `pendingQuestionFor`; `buildNode`; the live-only leaf; the NeedsYou node; `cloneTreeNodesContext`)
- Create: `cmd/evener-hub/internal/hubcore/resting_failure_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go` (register the six scenarios)

**Interfaces:**
- Consumes: `EvenerThread.Failure`, `appwire.CloneThreadFailure` (Task 9.2); `wireProbeEnvelopeSource.failure` (Task 9.2).
- Produces: `hubapi.NavigationFailureCrashed = "crashed"`; `ProbeResult.Failure`, `LiveEntry.Failure` and `TreeNode.Failure`, all `*appwire.ThreadFailure`.

- [ ] **Step 1: Write the failing scenarios**

`cmd/evener-hub/internal/hubcore/resting_failure_test.go`:

```go
package hubcore

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

func testRestingFailure() *appwire.ThreadFailure {
	return &appwire.ThreadFailure{Title: "Provider error", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Status: 401}}
}

// fuzzScenarioStatusProber_KeepsTheRestingFailure: the probe keeps the listed
// root's failure summary, which says why a Failed row failed (S1c).
func fuzzScenarioStatusProber_KeepsTheRestingFailure(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_failed",
		state:     appwire.ThreadStatusSystemError,
		source:    wireProbeEnvelopeSource{failure: testRestingFailure()},
	})
	if got := prober.Probe(entry); !got.OK || !reflect.DeepEqual(got.Failure, testRestingFailure()) {
		t.Fatalf("probe = %+v, want the failure %+v", got, testRestingFailure())
	}
}

// fuzzScenarioRoster_FingerprintMovesWhenOnlyTheFailureMoves: a retried turn
// that fails again between two probes leaves Status systemError on both and
// moves only the failure the row names (S1c).
func fuzzScenarioRoster_FingerprintMovesWhenOnlyTheFailureMoves(t *testing.T) {
	entry := func(failure *appwire.ThreadFailure) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusSystemError, Failure: failure}}
	}
	title, kind, provider, status := testRestingFailure(), testRestingFailure(), testRestingFailure(), testRestingFailure()
	title.Title = "Usage limit reached"
	kind.Cause.Kind = "transcript_failed_closed"
	provider.Cause.Provider = "lunaroute"
	status.Cause.Status = 429
	for name, moved := range map[string]*appwire.ThreadFailure{"title": title, "cause kind": kind, "provider": provider, "status": status, "absence": nil} {
		if rosterFingerprint(entry(testRestingFailure())) == rosterFingerprint(entry(moved)) {
			t.Errorf("the roster fingerprint held when only the failure's %s moved", name)
		}
	}
}

// fuzzScenarioRoster_EntriesOwnTheirFailure: an entry never aliases the probe's
// failure, and a clone never aliases the entry's.
func fuzzScenarioRoster_EntriesOwnTheirFailure(t *testing.T) {
	failure := testRestingFailure()
	fromProbe := liveEntryFromProbe(rendezvous.Entry{PID: 1, SessionID: "01A"}, ProbeResult{SessionID: "01A", OK: true, Failure: failure})
	failure.Cause.Status = 500
	if fromProbe.Failure.Cause.Status != 401 {
		t.Fatal("liveEntryFromProbe aliased the probe's failure")
	}
	clone := CloneLiveEntry(fromProbe)
	clone.Failure.Cause.Status = 503
	if fromProbe.Failure.Cause.Status != 401 {
		t.Fatal("CloneLiveEntry aliased the entry's failure")
	}
}

// fuzzScenarioBuildTree_EveryFailedRowSaysWhy: a failed session's NeedsYou,
// Live and project rows and a meta-less live leaf name its failure from one
// closure, each with its own copy; a crash-retained entry's rows say it
// crashed, whatever its daemon last reported; a session that is not failed
// names none, whatever its entry still carries (S1c).
func fuzzScenarioBuildTree_EveryFailedRowSaysWhy(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01FAILED", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01CRASHED", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01RETRIED", CreatedAt: now, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "01FAILED", Status: appwire.ThreadStatusSystemError, Failure: testRestingFailure()},
		{PID: 2, SessionID: "01NOMETA", Status: appwire.ThreadStatusSystemError, Failure: testRestingFailure()},
		{PID: 3, SessionID: "01CRASHED", Status: "errored", Crashed: true, Failure: testRestingFailure()},
		{PID: 4, SessionID: "01RETRIED", Status: appwire.ThreadStatusActive, Failure: testRestingFailure()},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	needsYou := map[string]TreeNode{}
	for _, row := range tree.NeedsYou {
		needsYou[row.ID] = row
	}
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01FAILED")
	leaf, inLeaf, _, _ := liveAndProjectRowsFor(tree, "01NOMETA")
	if !inLive || !inProject || !inLeaf {
		t.Fatalf("rows missing: live=%v project=%v leaf=%v", inLive, inProject, inLeaf)
	}
	for name, row := range map[string]TreeNode{"NeedsYou": needsYou["01FAILED"], "Live": liveRow, "project": projectRow, "live-only leaf": leaf} {
		if !reflect.DeepEqual(row.Failure, testRestingFailure()) {
			t.Fatalf("%s row failure = %+v, want %+v", name, row.Failure, testRestingFailure())
		}
	}
	liveRow.Failure.Cause.Status = 500
	if projectRow.Failure.Cause.Status != 401 {
		t.Fatal("two rows of one session share one failure")
	}
	crashed := &appwire.ThreadFailure{Cause: &appwire.DiagnosticCause{Kind: hubapi.NavigationFailureCrashed}}
	if got := needsYou["01CRASHED"].Failure; !reflect.DeepEqual(got, crashed) {
		t.Fatalf("crashed NeedsYou row failure = %+v, want the hub's own crashed cause", got)
	}
	if _, _, retried, found := liveAndProjectRowsFor(tree, "01RETRIED"); !found || retried.Failure != nil {
		t.Fatalf("working session's row = %+v (found %v), want no failure", retried.Failure, found)
	}
}

// fuzzScenarioBuildTree_ACoordinatorsRowNeverShowsASubagentsFailure: a
// coordinator's row is red only when the coordinator itself failed, so it
// never names a subagent's failure (Jesse's answer 12). A failed subagent's
// row names none either: the hub has no live entry for an in-process
// subagent, and its failure shows in the session's Subagents list.
func fuzzScenarioBuildTree_ACoordinatorsRowNeverShowsASubagentsFailure(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01ROOT", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01CHILD", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ParentSessionID: "01ROOT", IsSubagent: true, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{{
		PID: 1, SessionID: "01ROOT", Status: appwire.ThreadStatusActive,
		RunningSubagentIDs: []string{"01CHILD"}, RunningSubagentStates: map[string]string{"01CHILD": appwire.ThreadStatusSystemError},
	}}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01ROOT")
	if !inLive || !inProject || liveRow.Failure != nil || projectRow.Failure != nil {
		t.Fatalf("root rows = Live %+v (%v), project %+v (%v); want both found with no failure", liveRow.Failure, inLive, projectRow.Failure, inProject)
	}
	if len(liveRow.Children) != 1 || liveRow.Children[0].Failure != nil {
		t.Fatalf("children = %+v, want the one subagent row with no failure", liveRow.Children)
	}
}

// fuzzScenarioBuildTree_DeadParentClearsItsSubagentsFailure: a row clamped to
// ended under a dead parent is not Failed, so it names no failure.
func fuzzScenarioBuildTree_DeadParentClearsItsSubagentsFailure(t *testing.T) {
	child := staleSubagentOfDeadParent(t, LiveEntry{PID: 9, SessionID: "01STALESUB", Status: appwire.ThreadStatusSystemError, Failure: testRestingFailure()})
	if child.State != "ended" || child.Failure != nil {
		t.Fatalf("stale subagent = state %q, failure %+v; want ended with no failure", child.State, child.Failure)
	}
}
```

Register the six in `FuzzHubcoreScenarios`, each after its nearest alphabetical neighbour: `fuzzScenarioBuildTree_ACoordinatorsRowNeverShowsASubagentsFailure` after `fuzzScenarioBuildTreeSessionTiersAndProjectOrder`, `fuzzScenarioBuildTree_DeadParentClearsItsSubagentsFailure` after `..._DeadParentClearsItsSubagentsApproval`, `fuzzScenarioBuildTree_EveryFailedRowSaysWhy` after `..._DoesNotClusterLiveRepeatedTitles`, `fuzzScenarioRoster_EntriesOwnTheirFailure` after `..._EntriesOwnTheirActivity`, `fuzzScenarioRoster_FingerprintMovesWhenOnlyTheFailureMoves` after `..._FingerprintIgnoresActivity`, and `fuzzScenarioStatusProber_KeepsTheRestingFailure` after `..._KeepsThePendingQuestion`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`
Expected: FAIL to compile (`Failure` is not a field of `LiveEntry`; `hubapi.NavigationFailureCrashed` undefined).

- [ ] **Step 3: Implement**

`hubapi/navigation.go`, before `NavigationQuestion`:

```go
// NavigationFailureCrashed is the cause_kind of a row whose daemon's process
// exited while the hub still lists it. Nothing is left to report why, so the
// hub says so itself (S1c).
const NavigationFailureCrashed = "crashed"
```

`roster.go`, in `LiveEntry` after `PendingQuestion`:

```go
	// Failure summarizes the failed turn the session rests on (S1c), from the
	// same root row as Status; the daemon sends it only while that status is
	// systemError. rosterFingerprint hashes it: a turn retried and failed
	// again between two probes leaves Status where it was.
	Failure *appwire.ThreadFailure
```

in `ProbeResult` after `PendingQuestion`:

```go
	// Failure mirrors LiveEntry.Failure: the failure summary from the same
	// root row as Status (S1c).
	Failure *appwire.ThreadFailure
```

`CloneLiveEntry`: `out.Failure = appwire.CloneThreadFailure(in.Failure)` after the `PendingQuestion` line. `liveEntryFromProbe`: `Failure:               result.Failure,` after `PendingQuestion`. `ReadSpawnedThread`: `Failure:            root.Evener.Failure,` after its `PendingQuestion`. In `rosterFingerprint`, after the question's block and its closing `_, _ = h.Write([]byte{0})`:

```go
		// A Failed row says why, and a turn retried and failed again between
		// two probes holds the status still (S1c).
		if failure := bySess[id].Failure; failure != nil {
			_, _ = h.Write([]byte(failure.Title))
			_, _ = h.Write([]byte{0})
			if cause := failure.Cause; cause != nil {
				for _, field := range []string{cause.Kind, cause.Provider, cause.Model, strconv.Itoa(cause.Status)} {
					_, _ = h.Write([]byte(field))
					_, _ = h.Write([]byte{0})
				}
			}
		}
		_, _ = h.Write([]byte{0})
```

`prober.go`: `Failure:               root.Evener.Failure,` after `PendingQuestion` in `Probe`'s literal.

`tree.go`, in `TreeNode` after `Question`:

```go
	// Failure says why a Failed row failed (LiveEntry.Failure, S1c): its
	// daemon's summary, or the hub's own crashed cause for a crash-retained
	// entry. Every builder sets it from one closure, and only on a row whose
	// session is failed itself: a coordinator's row never names a subagent's
	// failure.
	Failure *appwire.ThreadFailure
```

After `pendingQuestionFor`:

```go
	// failureFor resolves why a session failed from the same live map, so its
	// rows agree (S1c). A crash-retained entry's daemon is gone, so its rows
	// say it crashed whatever it last reported; an errored entry names the
	// summary its daemon reported; any other session names none, whatever its
	// entry still carries. Each row gets its own copy.
	failureFor := func(id string) *appwire.ThreadFailure {
		entry := liveMap[id]
		if entry.Crashed {
			return &appwire.ThreadFailure{Cause: &appwire.DiagnosticCause{Kind: hubapi.NavigationFailureCrashed}}
		}
		if NormalizeState(entry.Status) != "errored" {
			return nil
		}
		return appwire.CloneThreadFailure(entry.Failure)
	}
```

In `buildNode`: `failure := failureFor(m.ID)` after `question := pendingQuestionFor(m.ID)`; `failure = nil` in the `parentDead` branch after `question = nil`; `Failure:         failure,` after `Question:        question,` in the literal. The live-only leaf and the NeedsYou node: `Failure:         failureFor(le.SessionID),` after their `Question` lines. In `cloneTreeNodesContext`, after the `Question` copy: `out[index].Failure = appwire.CloneThreadFailure(node.Failure)`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub/internal/hubcore -count=1`, `golangci-lint run ./cmd/evener-hub/internal/hubcore/ ./hubapi/`
Expected: PASS, and no unused scenario.

- [ ] **Step 5: Commit**

```bash
git add hubapi/navigation.go cmd/evener-hub/internal/hubcore/roster.go cmd/evener-hub/internal/hubcore/prober.go cmd/evener-hub/internal/hubcore/tree.go cmd/evener-hub/internal/hubcore/resting_failure_test.go cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go
git commit -m "feat(hub): live entries and Failed rows carry why they failed"
```

### Task 9.4: Rows carry `failure`

**Implementer:** Sonnet.

**Files:**
- Modify: `hubapi/navigation.go` (`NavigationFailure` before `NavigationQuestion`; `NavigationSessionSummary.Failure` after `Question`)
- Modify: `cmd/evener-hub/navigation_projection.go` (`projectShallow`; `navigationFailure` after `navigationQuestion`; `cloneNavigationSummary`; `clonePointer`'s comment)
- Modify: `cmd/evener-hub/navigation_schema.go` (`navigationSessionValueValid`'s final return; `navigationFailureValid` after it)
- Modify: `appwire-client/typescript/state/navigation/codec.ts` (`FAILURE_KEYS`; `SESSION_KEYS`; `failureValue`; `sessionValue`)
- Modify: `appwire-client/typescript/state/navigation/codec.test.ts`
- Modify: `cmd/evener-hub/testdata/navigation/value-records.json`
- Create: `cmd/evener-hub/navigation_failure_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`

**Interfaces:**
- Consumes: `TreeNode.Failure` (Task 9.3); `appwire.Excerpt`, `appwire.MaxFailureTitleRunes`.
- Produces: `hubapi.NavigationFailure{Title, CauseKind, Provider string; Status int}` (JSON `title`, `cause_kind`, `provider`, `status`, each `omitempty`); `NavigationSessionSummary.Failure *NavigationFailure` (`failure,omitempty`); `navigationFailureValid(hubapi.NavigationFailure) bool`; TypeScript `NavigationFailure` and `NavigationSessionSummary.failure?: NavigationFailure`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/navigation_failure_test.go`:

```go
package hub

import (
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// A Failed row says why (S1c): the failure's headline and its cause's kind,
// provider and HTTP status, the title re-cut to the wire's bound. A crashed
// daemon's row names the hub's own crashed cause, and any other row carries
// no key. The summary has no message to give, so none reaches a row.
func TestNavigationRowsCarryTheFailure(t *testing.T) {
	rows := liveTaskRows(t, []hubcore.TreeNode{
		{ID: "session-failed", Title: "failed", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{
			Title: "Provider error", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Model: "gpt-5.6", Status: 401},
		}},
		{ID: "session-crashed", Title: "crashed", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{
			Cause: &appwire.DiagnosticCause{Kind: hubapi.NavigationFailureCrashed},
		}},
		{ID: "session-wide", Title: "wide", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{Title: "Provider\nerror " + strings.Repeat("x", 200)}},
		{ID: "session-idle", Title: "idle", Kind: "session", State: "idle"},
	})
	for id, want := range map[string]string{
		"session-failed":  `{"title":"Provider error","cause_kind":"provider","provider":"codex-jesse-fsck.com","status":401}`,
		"session-crashed": `{"cause_kind":"crashed"}`,
	} {
		if got := string(navigationSummaryJSONFields(t, rows[id])["failure"]); got != want {
			t.Errorf("%s failure on the wire = %s, want %s", id, got, want)
		}
	}
	wide := rows["session-wide"].Failure
	if wide == nil || strings.ContainsAny(wide.Title, "\r\n") || utf8.RuneCountInString(wide.Title) > appwire.MaxFailureTitleRunes {
		t.Fatalf("wide failure = %+v, want one line of at most %d runes", wide, appwire.MaxFailureTitleRunes)
	}
	if failure, carried := navigationSummaryJSONFields(t, rows["session-idle"])["failure"]; carried {
		t.Fatalf("a row that is not failed carries %s", failure)
	}
}

// A summary with nothing to say (no title, and no cause kind), which only a
// malformed daemon answer can carry, is dropped from its row instead of
// failing the whole resource; the row stays Failed.
func TestNavigationRowsDropAFailureWithNothingToSay(t *testing.T) {
	rows := liveTaskRows(t, []hubcore.TreeNode{
		{ID: "session-blank", Title: "blank", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{Title: " \n "}},
		{ID: "session-kindless", Title: "kindless", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{Cause: &appwire.DiagnosticCause{Provider: "openai", Status: 500}}},
	})
	for _, id := range []string{"session-blank", "session-kindless"} {
		row, listed := rows[id]
		if !listed || row.State != "errored" || row.Failure != nil {
			t.Errorf("%s = %+v (listed %v), want the Failed row listed without a failure", id, row.Failure, listed)
		}
	}
}

// The hub schema refuses a failure the codec would refuse, and a title the
// projector's cut never yields.
func TestNavigationSchemaBoundsTheFailure(t *testing.T) {
	session := navigationSchemaSession("local:schema-session", "schema-session")
	session.Failure = &hubapi.NavigationFailure{
		Title:     strings.Repeat("é", appwire.MaxFailureTitleRunes),
		CauseKind: "provider",
		Provider:  strings.Repeat("p", maxNavigationIdentityBytes),
		Status:    401,
	}
	if !navigationSessionValueValid(session) {
		t.Fatal("a failure at every bound was refused")
	}
	for name, failure := range map[string]hubapi.NavigationFailure{
		"nothing to say":                     {},
		"a status alone":                     {Status: 500},
		"a title past its bound":             {Title: strings.Repeat("é", appwire.MaxFailureTitleRunes+1)},
		"a title with a line break":          {Title: "Provider\nerror"},
		"a provider past the identity bound": {CauseKind: "provider", Provider: strings.Repeat("p", maxNavigationIdentityBytes+1)},
		"invalid UTF-8 in the cause kind":    {CauseKind: "prov\xffider"},
		"a negative status":                  {CauseKind: "provider", Status: -1},
	} {
		session.Failure = &failure
		if navigationSessionValueValid(session) {
			t.Errorf("a failure with %s was accepted", name)
		}
	}
}

// A cloned summary owns its failure.
func TestCloneNavigationSummaryOwnsTheFailure(t *testing.T) {
	original := hubapi.NavigationSessionSummary{Failure: &hubapi.NavigationFailure{Title: "Provider error", CauseKind: "provider", Status: 401}}
	clone := cloneNavigationSummary(original)
	original.Failure.Status = 500
	if clone.Failure == nil || clone.Failure.Status != 401 {
		t.Fatalf("clone failure = %+v, want its own copy", clone.Failure)
	}
}
```

In `value-records.json`, after the `question` record in the session:

```json
    "failure": { "title": "Provider error", "cause_kind": "provider", "provider": "codex-jesse-fsck.com", "status": 401 },
```

In `codec.test.ts`, after PR 8's `test.each` "codec refuses a pending question with %s", before the comment `// cmd/evener-hub/navigation_value_records_test.go keeps this fixture naming`:

```ts
// A Failed row says why (S1c): a nested value record of the failure's title and
// its cause's kind, provider and status. The codec keeps it, drops a key
// inside it that it does not know, and holds it to the hub schema's bounds
// (navigation_schema.go navigationFailureValid).
test("codec keeps a row's failure summary and drops keys inside it that it does not know", () => {
  const failure = { title: "Provider error", cause_kind: "provider", provider: "codex-jesse-fsck.com", status: 401 };
  const rows = materializeSnapshot(
    key,
    decodedSnapshot(key, snapshotWithSessionField("failure", { ...failure, future_failure_key: futureValue })),
  ).sessions as Array<Record<string, unknown>>;
  expect(rows[0]?.failure).toEqual(failure);
  for (const kept of [
    { cause_kind: "crashed" },
    {
      title: "😀".repeat(80),
      cause_kind: "k".repeat(1024),
      provider: "p".repeat(1024),
      status: Number.MAX_SAFE_INTEGER,
    },
  ]) {
    expect(decodedSnapshot(key, snapshotWithSessionField("failure", kept)).snapshot.entities[0]?.value).toEqual({
      ...sessionValue("local:session"),
      failure: kept,
    });
  }
});

test.each([
  ["null", null],
  ["a string in place of the record", "Provider error"],
  ["nothing to say", {}],
  ["a status alone", { status: 500 }],
  ["an empty title", { title: "" }],
  ["an over-long title", { title: "t".repeat(81) }],
  ["an empty cause kind", { cause_kind: "" }],
  ["an over-long provider", { cause_kind: "provider", provider: "p".repeat(1025) }],
  ["a negative status", { cause_kind: "provider", status: -1 }],
  ["a string status", { cause_kind: "provider", status: "401" }],
] as const)("codec refuses a failure summary with %s", (_name, failure) => {
  expectContentFreeRejection(key, snapshotWithSessionField("failure", failure));
});
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestNavigationRowsCarryTheFailure|TestNavigationRowsDropAFailureWithNothingToSay|TestNavigationSchemaBoundsTheFailure|TestCloneNavigationSummaryOwnsTheFailure|TestNavigationValueRecordFixtureNamesEveryWireField' -count=1`
Expected: FAIL to compile (`hubapi.NavigationFailure` undefined).

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation/codec.test.ts`
Expected: FAIL: the fixture test and the new "keeps" test (the codec drops `failure`), and every "refuses" case.

- [ ] **Step 3: Implement**

`hubapi/navigation.go`, before `NavigationQuestion`:

```go
// NavigationFailure says why a Failed row failed (S1c): the failure's headline
// ("Provider error", "Usage limit reached") and its cause's kind ("provider",
// "transcript_failed_closed", or the hub's own NavigationFailureCrashed), the
// provider it came from and its HTTP status, from which a client composes the
// row's why line ("codex-jesse-fsck.com sign-in expired (401)"). It never
// carries the failure's message, which can quote a provider's error body.
type NavigationFailure struct {
	Title     string `json:"title,omitempty"`
	CauseKind string `json:"cause_kind,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Status    int    `json:"status,omitempty"`
}
```

In `NavigationSessionSummary`, replace the two lines `Question *NavigationQuestion ...` and `Dormant  bool ...` with:

```go
	Question *NavigationQuestion `json:"question,omitempty"`
	// Failure says why the session failed (S1c). It is present only on a
	// Failed row whose daemon summarized the failure, or whose daemon
	// crashed.
	Failure *NavigationFailure `json:"failure,omitempty"`
	Dormant bool               `json:"dormant,omitempty"`
```

`navigation_projection.go`: in `projectShallow`'s literal, after `Question: ...`, add `Failure:             navigationFailure(node.Failure),`; after `navigationQuestion`:

```go
// navigationFailure is a Failed row's why on the wire (S1c): the failure's
// headline re-cut to the wire's bound, and its cause's kind, provider and HTTP
// status, identities cut to the identity bound. It is dropped when the schema
// would refuse it (nothing left to say), the way navigationTaskProgress drops
// bad progress rather than fail the resource.
func navigationFailure(failure *appwire.ThreadFailure) *hubapi.NavigationFailure {
	if failure == nil {
		return nil
	}
	wire := hubapi.NavigationFailure{Title: appwire.Excerpt(failure.Title, appwire.MaxFailureTitleRunes)}
	if cause := failure.Cause; cause != nil {
		wire.CauseKind = truncateNavigationBytes(cause.Kind, maxNavigationIdentityBytes)
		wire.Provider = truncateNavigationBytes(cause.Provider, maxNavigationIdentityBytes)
		wire.Status = cause.Status
	}
	if !navigationFailureValid(wire) {
		return nil
	}
	return &wire
}
```

In `cloneNavigationSummary`, after `clone.Subagents = clonePointer(summary.Subagents)`: `clone.Failure = clonePointer(summary.Failure)`. Replace `clonePointer`'s comment with:

```go
// clonePointer returns a pointer to a shallow copy of *value; nil stays nil.
// It is for the summaries' pointers to values (a time, the task progress, the
// subagent tally, the failure), so the copy shares nothing that can change.
```

`navigation_schema.go`: end `navigationSessionValueValid` with

```go
	return (value.Tasks == nil || navigationTaskProgressValid(*value.Tasks)) &&
		(value.Subagents == nil || navigationSubagentTallyValid(*value.Subagents)) &&
		(value.Question == nil || navigationQuestionValid(*value.Question)) &&
		(value.Failure == nil || navigationFailureValid(*value.Failure))
}
```

and add after it:

```go
// navigationFailureValid mirrors the web codec's failureValue: something to
// say (a title or a cause kind), cause identities within the identity bound,
// and a safe non-negative status. The hub also holds the title to be an
// excerpt at its bound, which the projector's cut always yields. The projector
// drops a failure this refuses rather than failing the whole resource.
func navigationFailureValid(failure hubapi.NavigationFailure) bool {
	return (failure.Title != "" || failure.CauseKind != "") &&
		appwire.Excerpt(failure.Title, appwire.MaxFailureTitleRunes) == failure.Title &&
		navigationSchemaIdentity(failure.CauseKind, true) && navigationSchemaIdentity(failure.Provider, true) &&
		navigationIntCount(failure.Status)
}
```

`codec.ts`: after `QUESTION_KEYS`, `const FAILURE_KEYS = valueRecordKeys([], ["title", "cause_kind", "provider", "status"]);`; add `"failure"` to `SESSION_KEYS`' optional list after `"question"` and `failure: FAILURE_KEYS,` to its nested map after `question`; after `questionValue`:

```ts
// Mirrors navigationFailureValid: a title of 1 to 80 characters or a cause kind
// (at least one), cause identities, and a safe non-negative status.
const failureValue = (value: unknown): boolean =>
  knownKeys(value, FAILURE_KEYS) &&
  (value.title !== undefined || value.cause_kind !== undefined) &&
  optional(value.title, (item) => boundedString(item, 80) && item !== "") &&
  optional(value.cause_kind, (item) => identity(item)) &&
  optional(value.provider, (item) => identity(item)) &&
  optional(value.status, count);
```

and in `sessionValue`, after the `question` line: `optional(value.failure, failureValue) &&`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched Go files, `make generate`, and `cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/state/navigation/codec.ts ../../../appwire-client/typescript/state/navigation/codec.test.ts`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub -run 'TestNavigation|TestCloneNavigationSummary' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`, `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation && npm run typecheck`
Expected: PASS; `types.gen.ts` gains `NavigationFailure` and `failure?: NavigationFailure`.

- [ ] **Step 5: Commit**

```bash
git add hubapi/navigation.go cmd/evener-hub/navigation_projection.go cmd/evener-hub/navigation_schema.go cmd/evener-hub/navigation_failure_test.go cmd/evener-hub/testdata/navigation/value-records.json appwire-client/typescript/state/navigation/codec.ts appwire-client/typescript/state/navigation/codec.test.ts appwire-client/typescript/types.gen.ts
git commit -m "feat(hub): Failed rows say why they failed"
```

### Task 9.5: A remote host's rows carry their failure

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go` (`LocalDaemonEntry.Failure` after `PendingQuestion`; `threadFromEntry`)
- Create: `cmd/evener-hub/internal/appsource/local_daemon_failure_test.go`
- Modify: `cmd/evener-hub/app_rpc.go` (`localDaemonEntriesFromRoster`)
- Modify: `cmd/evener-hub/web_api_tree.go` (`appThreadTreeEntries`)
- Create: `cmd/evener-hub/remote_failure_test.go`

**Interfaces:**
- Consumes: `LiveEntry.Failure` (Task 9.3); `NavigationSessionSummary.Failure` (Task 9.4).
- Produces: `appsource.LocalDaemonEntry.Failure *appwire.ThreadFailure`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/internal/appsource/local_daemon_failure_test.go`:

```go
package appsource

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller carries a failed root's
// failure summary on its row, and no key on a healthy row (S1c).
func TestLocalDaemonSourceListCarriesTheRootsFailure(t *testing.T) {
	failure := &appwire.ThreadFailure{Title: "Provider error", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Status: 401}}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/failed", ThreadID: "th_failed", SessionID: "sess_failed"}, Status: appwire.ThreadStatusSystemError, Failure: failure},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/idle", ThreadID: "th_idle", SessionID: "sess_idle"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]*appwire.ThreadFailure{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread.Evener.Failure
	}
	if !reflect.DeepEqual(byID["th_failed"], failure) || byID["th_idle"] != nil {
		t.Fatalf("failures = %+v, want th_failed's %+v and none on th_idle", byID, failure)
	}
	byID["th_failed"].Cause.Status = 500
	if failure.Cause.Status != 401 {
		t.Fatal("a listed row aliases the roster's failure")
	}
}
```

`cmd/evener-hub/remote_failure_test.go`:

```go
package hub

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows carry a failed root's summary and never lend it to
// the root's in-process subagent aliases: a coordinator's failure is not its
// subagent's.
func TestLocalDaemonEntriesFromRosterCarryTheFailureOnlyOnTheRoot(t *testing.T) {
	failure := &appwire.ThreadFailure{Title: "Provider error", Cause: &appwire.DiagnosticCause{Kind: "provider", Status: 401}}
	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{{
		Entry:     rendezvous.Entry{ThreadID: "sess_root", SessionID: "sess_root"},
		SessionID: "sess_root", Status: appwire.ThreadStatusSystemError,
		RunningSubagentIDs: []string{"sess_child"}, Failure: failure,
	}})
	if len(entries) != 2 || !reflect.DeepEqual(entries[0].Failure, failure) || entries[1].Failure != nil {
		t.Fatalf("entries = %+v, want the root's failure %+v and none on its alias", entries, failure)
	}
}

// A controller reads a remote host's failure summary off its list row, so the
// remote Failed row says why exactly like a local one (S1c).
func TestNavigationRemoteRowCarriesItsFailure(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID: "failed", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusSystemError},
		Evener: appwire.EvenerThread{Failure: &appwire.ThreadFailure{Title: "Usage limit reached", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Status: 429}}},
	}})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})

	captured, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	projection, err := buildNavigationProjection(captured.Inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	want := &hubapi.NavigationFailure{Title: "Usage limit reached", CauseKind: "provider", Provider: "codex-jesse-fsck.com", Status: 429}
	if row := navigationProjectedSummary(t, projection, "host-a:failed"); row.State != "errored" || !reflect.DeepEqual(row.Failure, want) {
		t.Fatalf("remote row = state %q, failure %+v; want errored with %+v", row.State, row.Failure, want)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/appsource -run 'TestLocalDaemonSourceListCarriesTheRootsFailure' -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRosterCarryTheFailureOnlyOnTheRoot|TestNavigationRemoteRowCarriesItsFailure' -count=1`
Expected: FAIL to compile (`Failure` is not a field of `LocalDaemonEntry`); once it exists, the remote row test fails with no failure.

- [ ] **Step 3: Implement**

`local_daemon.go`, in `LocalDaemonEntry` after `PendingQuestion`:

```go
	// Failure mirrors hubcore.LiveEntry.Failure: the summary of the failed turn
	// the root rests on (S1c). threadFromEntry carries it into
	// appwire.EvenerThread.Failure, so a controller hub says why a remote
	// session failed. A read-only alias has none.
	Failure *appwire.ThreadFailure
```

and in `threadFromEntry`'s `Evener` literal after the `PendingQuestion` line: `Failure:            appwire.CloneThreadFailure(item.Failure),`. The remote hub's daemon sends a summary only while its status is systemError, and the entry takes both from one probe, so the row needs no gate of its own.

`app_rpc.go`, in `localDaemonEntriesFromRoster`: `Failure:            item.Failure,` after `PendingQuestion:    item.PendingQuestion,`, and among the alias's clears, after `child.PendingQuestion = nil`:

```go
			// A coordinator's failure is its own, never its subagent's.
			child.Failure = nil
```

`web_api_tree.go`, in `appThreadTreeEntries`' `LiveEntry` literal after the `PendingQuestion` line: `Failure:            appwire.CloneThreadFailure(thread.Evener.Failure),`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./cmd/evener-hub/internal/appsource -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRoster|TestNavigationRemote|TestNavigationOffline' -count=1`
Expected: PASS.

Gates: `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...` (root module and `agent/`), `golangci-lint run ./cmd/evener-hub/ ./cmd/evener-hub/internal/appsource/ ./cmd/evener-hub/internal/hubcore/ ./hubapi/`, `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`, `make lint-generated`, `make secret-scan`.

- [ ] **Step 5: Commit and open PR 9**

```bash
git add cmd/evener-hub/internal/appsource/local_daemon.go cmd/evener-hub/internal/appsource/local_daemon_failure_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/web_api_tree.go cmd/evener-hub/remote_failure_test.go
git commit -m "feat(hub): remote hosts' rows carry their failure summary"
```

Title "feat(hub): Failed rows say why (S1c, phase 7 PR 9)". The body names the fallback it retires ("open the session to see what went wrong"), says the summary is the classifier's title and the structured cause and never the error text (ruling 12, and why), that it shows only while the status is systemError, that a crashed daemon's rows carry the hub's own `crashed` cause, and that a coordinator's row never names a subagent's failure (Jesse's answer 12).

---

## PR 10: the last agent message, S1d daemon (Tasks 10.1-10.2)

**Branch:** `git fetch origin && git switch -c claude/s1d-last-message-daemon origin/main`, once PR 7 is on main. It needs neither PR 8 nor PR 9 (dry-run on main with PR 7 alone): its one anchor that can meet a PR 9 line says where to go either way.

**What it adds.** The session records the opening of each agent message it writes, persists it in `SessionMeta.LastMessage`, and thread snapshots carry it as `EvenerThread.lastMessage`. Nothing reads it yet; PR 11 carries it to rows.

### Task 10.1: The session keeps the opening of its last agent message

**Implementer:** Sonnet.

**Files:**
- Create: `agent/session_last_message.go`
- Modify: `appwire/excerpt.go` (`MaxMessageExcerptRunes`, at the end of the constant block)
- Modify: `agent/schema/snapshot.go` (`SessionMeta.LastMessage` after `LastTurnEndedAt`, `:249`)
- Modify: `agent/session.go` (the `lastMessage` field after `lastTurnEndedAt`, `:412`; `appendAssistantTurn`'s append callback, `:2472`)
- Modify: `agent/session_execution.go` (`deliverCommunicate`, `:310-336`)
- Modify: `agent/session_init.go` (`RestoreSessionFromMetaWithConfig`'s literal, `:1135`)
- Modify: `agent/session_state.go` (`metaWithNotes`, after `LastTurnEndedAt: s.lastTurnEndedAt,`)
- Create: `agent/session_last_message_test.go`, `agent/schema/snapshot_last_message_test.go`

**Interfaces:**
- Consumes: `appwire.Excerpt` (PR 7).
- Produces: `appwire.MaxMessageExcerptRunes = 200`; `schema.SessionMeta.LastMessage string` (JSON `last_message,omitempty`), which `(*agent.Session).Meta()` fills; `lastAgentText(llm.Message) string` and `(*Session).noteAgentMessageLocked(string)` in package `agent`.

- [ ] **Step 1: Write the failing tests**

`agent/session_last_message_test.go` (`newSession`, `withConfig`, `withSteps`, `communicateResponse` and `fakeAdapter` are the package's existing test helpers):

```go
package agent

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// An assistant response's agent message is its last text part: the transcript
// shows each text part as its own agent message, the last one last. Reasoning,
// redacted reasoning and tool call arguments are never part of it.
func TestLastAgentText_IsTheLastTextPart(t *testing.T) {
	t.Parallel()
	message := llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "private reasoning"}},
		{Kind: llm.ContentText, Text: "Looking at the tests."},
		{Kind: llm.ContentRedThinking, Thinking: &llm.ThinkingData{Redacted: true}},
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "shell", Arguments: []byte(`{"command":"cat secrets.env"}`)}},
		{Kind: llm.ContentText, Text: "Three layouts are ready."},
		{Kind: llm.ContentText, Text: "  \n "},
	}}
	if got := lastAgentText(message); got != "Three layouts are ready." {
		t.Fatalf("lastAgentText = %q, want the last text part", got)
	}
	thinking := llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "only reasoning"}}}}
	if got := lastAgentText(thinking); got != "" {
		t.Fatalf("a response with no text reports %q", got)
	}
}

// A turn's last agent message leaves its opening on the session as one line cut
// to the wire's bound, and the turn's end persists it in the meta, so an ended
// session and a restarted daemon still have it (S1d). The response's own text
// before the delivered message, and its reasoning, are not what it ended with.
func TestLastMessage_RecordsTheOpeningOfTheLastAgentMessage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	report := "Three layouts are ready for review.\n\nI recommend B: " + strings.Repeat("it keeps the project first. ", 20)
	sess := newSession(t, withConfig(SessionConfig{StateDir: dir}), withSteps(
		func(llm.Request) llm.Response {
			response := communicateResponse(true, report)
			response.Message.Content = append([]llm.ContentPart{
				{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "private reasoning"}},
				{Kind: llm.ContentText, Text: "Writing the report now."},
			}, response.Message.Content...)
			return response
		},
	))
	if got := sess.Meta().LastMessage; got != "" {
		t.Fatalf("a session that has written nothing reports %q", got)
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "mock up three layouts", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	want := appwire.Excerpt(report, appwire.MaxMessageExcerptRunes)
	if !strings.HasPrefix(want, "Three layouts are ready for review. I recommend B:") || utf8.RuneCountInString(want) > appwire.MaxMessageExcerptRunes {
		t.Fatalf("the fixture's excerpt %q is not one bounded line", want)
	}
	if got := sess.Meta().LastMessage; got != want {
		t.Fatalf("LastMessage = %q, want %q", got, want)
	}
	saved, err := schema.LoadSessionMeta(dir, sess.ID())
	if err != nil || saved.LastMessage != want {
		t.Fatalf("saved LastMessage = %q (%v), want %q", saved.LastMessage, err, want)
	}
}

// A restored session keeps the opening of its last agent message, so its
// Finished row still says what it finished with after a daemon restart (S1d).
func TestLastMessage_SurvivesRestore(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	meta := schema.SessionMeta{
		ID:          "01RESTORELASTMESSAGE0001",
		ProfileID:   "openai",
		Model:       "gpt-5.2",
		CreatedAt:   time.Date(2026, 9, 27, 11, 0, 0, 0, time.UTC),
		LastMessage: "Three layouts are ready for review.",
	}
	sess, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, "")
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()
	if got := sess.Meta().LastMessage; got != meta.LastMessage {
		t.Fatalf("restored LastMessage = %q, want %q", got, meta.LastMessage)
	}
}
```

`agent/schema/snapshot_last_message_test.go`:

```go
package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

// LastMessage persists as last_message and is absent until the session has
// written an agent message, so an older meta and a silent session read the
// same.
func TestSessionMeta_LastMessageRoundTrip(t *testing.T) {
	raw, err := json.Marshal(SessionMeta{ID: "01X", LastMessage: "Three layouts are ready for review."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"last_message":"Three layouts are ready for review."`) {
		t.Fatalf("meta = %s, want last_message", raw)
	}
	var back SessionMeta
	if err := json.Unmarshal(raw, &back); err != nil || back.LastMessage != "Three layouts are ready for review." {
		t.Fatalf("round trip = %q (%v)", back.LastMessage, err)
	}
	empty, err := json.Marshal(SessionMeta{ID: "01X"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "last_message") {
		t.Fatalf("a meta with no message carries the key: %s", empty)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `cd agent && go test . ./schema -run 'TestLastAgentText|TestLastMessage|TestSessionMeta_LastMessageRoundTrip' -count=1`
Expected: FAIL to compile (`lastAgentText`, `appwire.MaxMessageExcerptRunes` and `SessionMeta.LastMessage` undefined).

- [ ] **Step 3: Implement**

`appwire/excerpt.go`, at the end of the constant block (after `MaxFailureTitleRunes` when PR 9 is on main, otherwise after `MaxQuestionOptions`):

```go
	// MaxMessageExcerptRunes bounds the opening of a session's last agent
	// message, a Finished row's why line (spec 18, S1: "about 200 characters").
	MaxMessageExcerptRunes = 200
```

`agent/schema/snapshot.go`, in `SessionMeta` after `LastTurnEndedAt`:

```go
	// LastMessage is the opening of the session's last agent message (S1d):
	// one line cut to the wire's bound, recorded whenever the session writes
	// an agent message and persisted with the next meta save, so an ended
	// session's row and a restarted daemon still have it. Empty until the
	// session has written one, and on metas written before the field existed.
	LastMessage string `json:"last_message,omitempty"`
```

`agent/session_last_message.go`:

```go
package agent

import (
	"slices"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// lastAgentText is the text of the agent message an assistant response records
// last: its last text part that says something, the one the transcript shows
// last (internal/apptranscript projects each text part as its own agentMessage
// item). Reasoning, redacted reasoning and tool calls are not agent messages,
// so none of them reaches a row.
func lastAgentText(message llm.Message) string {
	for _, part := range slices.Backward(message.Content) {
		if part.Kind == llm.ContentText && strings.TrimSpace(part.Text) != "" {
			return part.Text
		}
	}
	return ""
}

// noteAgentMessageLocked records the opening of an agent message the session
// just wrote to its transcript, which its Finished row shows (S1d): one line
// cut to the wire's bound. A message with no text leaves the last one
// standing. The caller holds s.mu.
func (s *Session) noteAgentMessageLocked(text string) {
	if excerpt := appwire.Excerpt(text, appwire.MaxMessageExcerptRunes); excerpt != "" {
		s.lastMessage = excerpt
	}
}
```

`agent/session.go`, in the `Session` struct after the `lastTurnEndedAt` field:

```go
	lastMessage                   string    // the opening of the last agent message the session wrote (S1d, noteAgentMessageLocked); seeded from SessionMeta on restore, mapped out via Meta(). Guarded by mu.
```

In `appendAssistantTurn`, the third argument to `s.appendTurnAfterTranscriptWrite` is the callback that appends the turn to history; `appendTurnAfterTranscriptWriteLocked` runs it under `s.mu` once the transcript write is recorded. After its `s.roundID = ""` line:

```go
			s.noteAgentMessageLocked(lastAgentText(t.Message))
```

`agent/session_execution.go`, in `deliverCommunicate`, right before `s.emit(events.EventCommunicate, data)` (past the refusals, so a message the session refused is never noted):

```go
	s.mu.Lock()
	s.noteAgentMessageLocked(data.Message)
	s.mu.Unlock()
```

`agent/session_init.go`, in `RestoreSessionFromMetaWithConfig`'s `Session` literal after `lastTurnEndedAt:          meta.LastTurnEndedAt,`:

```go
		lastMessage:              meta.LastMessage,
```

`agent/session_state.go`, in `metaWithNotes`' literal after `LastTurnEndedAt:          s.lastTurnEndedAt,`:

```go
		LastMessage:              s.lastMessage,
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `cd agent && go test . ./schema -run 'TestLastAgentText|TestLastMessage|TestSessionMeta|TestCommunicate|TestRestoreSessionFromMeta|TestMeta' -count=1`, then `go test ./appwire -count=1`
Expected: PASS. The recorded excerpt is the delivered report's opening, "Three layouts are ready for review. I recommend B: …" on one line, not the response's earlier "Writing the report now." and never its reasoning; the saved meta carries it.

- [ ] **Step 5: Commit**

```bash
git add appwire/excerpt.go agent/schema/snapshot.go agent/schema/snapshot_last_message_test.go agent/session_last_message.go agent/session_last_message_test.go agent/session.go agent/session_execution.go agent/session_init.go agent/session_state.go
git commit -m "feat(agent): a session keeps the opening of its last agent message"
```

### Task 10.2: Thread snapshots carry the last message

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire/types.go` (`EvenerThread.LastMessage` after `LastTurnEndedAt`, `:936`)
- Modify: `server/thread_envelope.go` (the facetMeta fields and their comment, `:94-104`; the `facetMeta` branch of `refreshFacets`; `assign`)
- Modify: `server/appwire_runtime.go` (`appThreadWithDiagnosticsLocked`, `:2524` and `:2571`)
- Create: `server/thread_envelope_last_message_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`

**Interfaces:**
- Consumes: `SessionMeta.LastMessage` (Task 10.1), through `ThreadEnvelopeSource.SessionMeta()`, which `liveThreadEnvelopeSource` answers with `Session.Meta()` (`cmd/evener/serve.go`). No new source method.
- Produces: `EvenerThread.LastMessage string` (`lastMessage,omitempty`) on `thread/read` and `thread/list`; TypeScript `EvenerThread.lastMessage?: string`.

- [ ] **Step 1: Write the failing test**

`server/thread_envelope_last_message_test.go`:

```go
package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// The thread snapshot carries the opening of the session's last agent message,
// sampled from its meta; TURN_ENDED re-samples it, because it re-samples every
// facet (S1d). A session that has written no message carries no key.
func TestThreadSnapshotsCarryTheLastMessage(t *testing.T) {
	srv := NewServer(ServerConfig{})
	srv.SetAppIdentity("local", "root")
	src := publishEnvelope(srv, &stubThreadEnvelopeSource{meta: schema.SessionMeta{ID: "root"}})
	listed := func() appwire.EvenerThread {
		t.Helper()
		list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{StatusOnly: true})
		if err != nil {
			t.Fatalf("thread/list: %v", err)
		}
		return list.Data[0].Evener
	}
	raw, err := json.Marshal(listed())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "lastMessage") {
		t.Fatalf("a session that has written nothing carries lastMessage: %s", raw)
	}

	const message = "Three layouts are ready for review. I recommend B."
	src.meta.LastMessage = message
	BridgeEvent(srv, events.SessionEvent{Kind: events.EventTurnEnded, SessionID: "root", Data: events.TurnEndedData{TurnDurationMS: 1_000}}, nil)
	if got := listed().LastMessage; got != message {
		t.Fatalf("listed lastMessage = %q, want %q", got, message)
	}
	if got := readThreadOverWire(t, srv, "local:root").Evener.LastMessage; got != message {
		t.Fatalf("read lastMessage = %q, want %q", got, message)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./server -run 'TestThreadSnapshotsCarryTheLastMessage' -count=1`
Expected: FAIL to compile (`EvenerThread.LastMessage` undefined).

- [ ] **Step 3: Implement**

`appwire/types.go`, in `EvenerThread` after `LastTurnEndedAt`:

```go
	// LastMessage is the opening of the session's last agent message (S1d):
	// one line of at most MaxMessageExcerptRunes, the agent's own words only,
	// never its reasoning or a tool's output. Absent until the session has
	// written a message, and from an older daemon. Snapshot-only: no
	// notification carries it.
	LastMessage string `json:"lastMessage,omitempty"`
```

`server/thread_envelope.go`: in `threadEnvelope`, replace the comment that opens "Name, Preview and LastTurnEndedAt are the facetMeta fields" and the three fields under it with:

```go
	// Name, Preview, LastTurnEndedAt and LastMessage are the facetMeta fields
	// appThread reads out of schema.SessionMeta. Storing them rather than the
	// whole struct is deliberate: SessionMeta has roughly a dozen other fields
	// (turn counts, pinned notes, worktree paths) that change constantly and
	// silently, and storing them would create a dozen values this envelope
	// claims to keep current and does not. LastTurnEndedAt (Unix ms, 0 before
	// any turn has ended) is current by construction: only a turn ending moves
	// it, and TURN_ENDED re-samples every facet, facetMeta included.
	// LastMessage can move mid-turn and lag until the turn ends, which is when
	// a row shows it (S1d): a Finished row is a session whose turn ended.
	Name            string
	Preview         string
	LastTurnEndedAt int64
	LastMessage     string
```

In `refreshFacets`, inside `if facets&facetMeta != 0 {` (within the `facetMeta|facetGoal` block), after the `if !meta.LastTurnEndedAt.IsZero() { ... }` statement:

```go
			next.LastMessage = meta.LastMessage
```

In `assign`, after `e.LastTurnEndedAt = next.LastTurnEndedAt`:

```go
		e.LastMessage = next.LastMessage
```

`server/appwire_runtime.go`, in `appThreadWithDiagnosticsLocked`: after `lastTurnEndedAt := envelope.LastTurnEndedAt` add `lastMessage := envelope.LastMessage`, and after `LastTurnEndedAt:       lastTurnEndedAt,` in the `EvenerThread` literal add `LastMessage:           lastMessage,`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files, then `make generate`.

- [ ] **Step 4: Run it to verify it passes, then the gates**

Run: `go test ./server -count=1`, `go test ./cmd/evener -run 'Notes|Envelope' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`
Expected: PASS; `types.gen.ts` gains `lastMessage?: string` on `EvenerThread`.

Gates, in the root module and in `agent/`: `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...`; `golangci-lint run ./appwire/ ./server/`, `golangci-lint run --config .golangci-appwire.yml ./server/...`, `cd agent && golangci-lint run . ./schema/`; `make lint-generated`.

- [ ] **Step 5: Commit and open PR 10**

```bash
git add appwire/types.go server/thread_envelope.go server/appwire_runtime.go server/thread_envelope_last_message_test.go appwire-client/typescript/types.gen.ts
git commit -m "feat(server): thread snapshots carry the session's last message"
```

Title "feat(agent): sessions keep the opening of their last agent message (S1d daemon, phase 7 PR 10)". The body says what the excerpt carries and never carries (ruling 20: the agent's own message text, never reasoning, a tool's arguments or output, or a failure's text), that it persists in the meta and survives a restart, that it rides the meta facet, that nothing reads it until PR 11, and that no projector change means no TUI case.

---

## PR 11: rows carry the last message, S1d hub (Tasks 11.1-11.3)

**Branch:** `git fetch origin && git switch -c claude/s1d-last-message-hub origin/main`, once PRs 9 and 10 are on main. It reads PR 10's `EvenerThread.LastMessage` and `SessionMeta.LastMessage`, and adds each navigation field beside the one PR 9 added (`failure`).

**What it adds.** Rows carry `last_message`: a top-level session's opening of its last agent message, from its daemon while it is live and from its meta once it has ended; a subagent row carries none. A remote host's rows carry theirs, online or offline. A Finished row gains a why line on the phone, which it has none of today.

### Task 11.1: Live entries and tree rows carry the last message

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/hubcore/roster.go` (`LiveEntry.LastMessage` after `LastTurnEndedAt`, `:118`; `ProbeResult.LastMessage` after its `LastTurnEndedAt`, `:166`; the end of `rosterFingerprint`'s per-session loop, `:631`; `liveEntryFromProbe`, `:1432`; `ReadSpawnedThread`, `:1521`)
- Modify: `cmd/evener-hub/internal/hubcore/prober.go` (`Probe`'s result literal, `:174`)
- Modify: `cmd/evener-hub/internal/hubcore/tree.go` (`TreeNode.LastMessage` after `TurnEndedAt`, `:488`; a `lastMessageFor` closure before `turnEndedAtFor`, `:1132`; `buildNode`, `:1343`; the live-only leaf, `:1596`; the NeedsYou node, `:1704`)
- Create: `cmd/evener-hub/internal/hubcore/last_message_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go` (register the three scenarios)

**Interfaces:**
- Consumes: `EvenerThread.LastMessage` and `SessionMeta.LastMessage` (PR 10); `wireProbeEnvelopeSource.meta` (on main).
- Produces: `ProbeResult.LastMessage`, `LiveEntry.LastMessage` and `TreeNode.LastMessage`, all `string`. A string needs no clone.

- [ ] **Step 1: Write the failing scenarios**

`cmd/evener-hub/internal/hubcore/last_message_test.go`:

```go
package hubcore

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// fuzzScenarioStatusProber_KeepsTheLastMessage: the probe keeps the opening of
// the listed root's last agent message, a Finished row's why line (S1d).
func fuzzScenarioStatusProber_KeepsTheLastMessage(t *testing.T) {
	const message = "Three layouts are ready for review. I recommend B."
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_finished",
		state:     appwire.ThreadStatusIdle,
		source:    wireProbeEnvelopeSource{meta: schema.SessionMeta{ID: "th_finished", LastMessage: message}},
	})
	if got := prober.Probe(entry); !got.OK || got.LastMessage != message {
		t.Fatalf("probe = %+v, want the last message %q", got, message)
	}
}

// fuzzScenarioRoster_FingerprintMovesWhenOnlyTheLastMessageMoves: a row shows
// the opening of its session's last agent message, which can move while the
// status and the turn end hold still (S1d).
func fuzzScenarioRoster_FingerprintMovesWhenOnlyTheLastMessageMoves(t *testing.T) {
	ended := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	entry := func(message string) map[string]LiveEntry {
		return map[string]LiveEntry{"01A": {SessionID: "01A", Status: appwire.ThreadStatusIdle, LastTurnEndedAt: ended, LastMessage: message}}
	}
	if rosterFingerprint(entry("Three layouts are ready.")) == rosterFingerprint(entry("Two layouts are ready.")) {
		t.Fatal("the roster fingerprint held when only the last message moved")
	}
}

// fuzzScenarioBuildTree_RowsCarryTheirOwnSessionsLastMessage: a live session's
// rows carry its daemon's last message, which outranks the meta the past index
// may still hold; an ended session's rows carry its meta's; a meta-less live
// leaf carries its entry's; and a subagent row carries none, so a coordinator's
// row says what the coordinator said (S1d).
func fuzzScenarioBuildTree_RowsCarryTheirOwnSessionsLastMessage(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	metas := []schema.SessionMeta{
		{ID: "01LIVE", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastMessage: "An older message.", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01ENDED", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, LastMessage: "The ended session's last words.", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
		{ID: "01CHILD", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, ParentSessionID: "01LIVE", IsSubagent: true, LastMessage: "The subagent's report.", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}},
	}
	live := []LiveEntry{
		{PID: 1, SessionID: "01LIVE", Status: appwire.ThreadStatusAwaiting, RunningSubagentIDs: []string{"01CHILD"}, LastMessage: "Three layouts are ready."},
		{PID: 2, SessionID: "01NOMETA", Status: appwire.ThreadStatusAwaiting, LastMessage: "A meta-less session's words."},
	}
	tree := BuildTreeAt(metas, live, map[ArchiveKey]bool{}, now)
	liveRow, inLive, projectRow, inProject := liveAndProjectRowsFor(tree, "01LIVE")
	if !inLive || !inProject || liveRow.LastMessage != "Three layouts are ready." || projectRow.LastMessage != "Three layouts are ready." {
		t.Fatalf("Live row %q (%v), project row %q (%v): both must carry the daemon's message", liveRow.LastMessage, inLive, projectRow.LastMessage, inProject)
	}
	for _, row := range tree.NeedsYou {
		if row.ID == "01LIVE" && row.LastMessage != "Three layouts are ready." {
			t.Fatalf("NeedsYou row carries %q, want the daemon's message", row.LastMessage)
		}
	}
	if leaf, found, _, _ := liveAndProjectRowsFor(tree, "01NOMETA"); !found || leaf.LastMessage != "A meta-less session's words." {
		t.Fatalf("meta-less leaf = %q (found %v), want its entry's message", leaf.LastMessage, found)
	}
	if _, _, ended, found := liveAndProjectRowsFor(tree, "01ENDED"); !found || ended.LastMessage != "The ended session's last words." {
		t.Fatalf("ended row = %q (found %v), want its meta's message", ended.LastMessage, found)
	}
	if len(liveRow.Children) != 1 || liveRow.Children[0].LastMessage != "" {
		t.Fatalf("children = %+v, want the one subagent row with no message", liveRow.Children)
	}
}
```

Register the three in `FuzzHubcoreScenarios`, each after its nearest alphabetical neighbour: `fuzzScenarioBuildTree_RowsCarryTheirOwnSessionsLastMessage` after `fuzzScenarioBuildTree_RootRowsCarryTheTreesSubagentTally`, `fuzzScenarioRoster_FingerprintMovesWhenOnlyTheLastMessageMoves` after `fuzzScenarioRoster_FingerprintMovesWhenOnlyTheFailureMoves`, and `fuzzScenarioStatusProber_KeepsTheLastMessage` after `fuzzScenarioStatusProber_DecodesRunningSubagentStates`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`
Expected: FAIL to compile (`LastMessage` is not a field of `ProbeResult`, `LiveEntry` or `TreeNode`).

- [ ] **Step 3: Implement**

`roster.go`, in `LiveEntry` after `LastTurnEndedAt`:

```go
	// LastMessage is the opening of the session's last agent message, from its
	// probe (S1d): a Finished row's why line. rosterFingerprint hashes it: a
	// message can land while the status and the turn end hold still.
	LastMessage string
```

in `ProbeResult` after `LastTurnEndedAt`:

```go
	// LastMessage mirrors LiveEntry.LastMessage: the opening of the listed
	// root's last agent message (S1d), empty from a daemon that predates it.
	LastMessage string
```

In `rosterFingerprint`, after the per-session loop's last statement, `_, _ = h.Write([]byte(strconv.FormatInt(UnixMilliseconds(bySess[id].LastTurnEndedAt), 10)))`:

```go
		_, _ = h.Write([]byte{0})
		// A Finished row shows its last message, which can land while the
		// status and the turn end hold still (S1d).
		_, _ = h.Write([]byte(bySess[id].LastMessage))
```

`liveEntryFromProbe`: `LastMessage:           result.LastMessage,` after `LastTurnEndedAt:       result.LastTurnEndedAt,`. `ReadSpawnedThread`: `LastMessage:        root.Evener.LastMessage,` after `Failure:            root.Evener.Failure,`.

`prober.go`: `LastMessage:           root.Evener.LastMessage,` after `LastTurnEndedAt:       UnixMilliTime(root.Evener.LastTurnEndedAt),` in `Probe`'s literal.

`tree.go`, in `TreeNode` after `TurnEndedAt`:

```go
	// LastMessage is the opening of the session's last agent message (S1d): a
	// live session's from its probe, an ended one's from its meta. Every
	// builder sets it from one closure; subagent rows have none.
	LastMessage string
```

Before `turnEndedAtFor` (after `subagentsFor`):

```go
	// lastMessageFor resolves the opening of a session's last agent message
	// (S1d): a live session's from its daemon's probe, which outranks the meta
	// the past index may still hold, and an ended one's from that meta, so
	// every row of one session agrees. A subagent row carries none: a
	// coordinator's row says what the coordinator said, and a tree of 500
	// subagents would spend the response's byte budget on excerpts no row
	// shows.
	lastMessageFor := func(id, kind string) string {
		if kind == "subagent" {
			return ""
		}
		if entry, live := liveMap[id]; live {
			return entry.LastMessage
		}
		return metaMap[id].LastMessage
	}
```

In `buildNode`'s literal, after `TurnEndedAt:     turnEndedAt,`: `LastMessage:     lastMessageFor(m.ID, kind),`. In the live-only leaf's and the NeedsYou node's literals, after `TurnEndedAt:     turnEndedAtFor(le.SessionID),`: `LastMessage:     lastMessageFor(le.SessionID, "session"),` (both are `Kind: "session"`).

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub/internal/hubcore -count=1`, `golangci-lint run ./cmd/evener-hub/internal/hubcore/`
Expected: PASS, and no unused scenario.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/internal/hubcore/roster.go cmd/evener-hub/internal/hubcore/prober.go cmd/evener-hub/internal/hubcore/tree.go cmd/evener-hub/internal/hubcore/last_message_test.go cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go
git commit -m "feat(hub): live entries and top-level rows carry the last message"
```

### Task 11.2: Rows carry `last_message`

**Implementer:** Sonnet.

**Files:**
- Modify: `hubapi/navigation.go` (`NavigationSessionSummary.LastMessage` after `Failure`, `:313-314`)
- Modify: `cmd/evener-hub/navigation_projection.go` (`projectShallow`, `:1839`)
- Modify: `cmd/evener-hub/navigation_schema.go` (`navigationSessionValueValid`, `:427`)
- Modify: `appwire-client/typescript/state/navigation/codec.ts` (`SESSION_KEYS`, `:178`; `sessionValue`, `:362`)
- Modify: `appwire-client/typescript/state/navigation/codec.test.ts`
- Modify: `cmd/evener-hub/testdata/navigation/value-records.json`
- Create: `cmd/evener-hub/navigation_last_message_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`

**Interfaces:**
- Consumes: `TreeNode.LastMessage` (Task 11.1); `appwire.Excerpt`, `appwire.MaxMessageExcerptRunes`.
- Produces: `NavigationSessionSummary.LastMessage string` (`last_message,omitempty`); TypeScript `NavigationSessionSummary.last_message?: string`.

A plain string needs no predicate of its own: the schema holds it to `appwire.Excerpt(value, bound) == value`, and the projector's cut is `appwire.Excerpt`, which Task 7.1's `TestExcerptNeverExceedsItsBound` holds to be its own excerpt, so a projected row always passes. `ApprovalTarget`'s rune cut works the same way.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/navigation_last_message_test.go`:

```go
package hub

import (
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// A row carries the opening of its session's last agent message (S1d): a
// Finished row's why line. The hub re-cuts what a daemon or a meta held to one
// line at the wire's bound, and a row with no message carries no key.
func TestNavigationRowsCarryTheLastMessage(t *testing.T) {
	rows := liveTaskRows(t, []hubcore.TreeNode{
		{ID: "session-finished", Title: "finished", Kind: "session", State: "idle", LastMessage: "Three layouts are ready for review. I recommend B."},
		{ID: "session-wide", Title: "wide", Kind: "session", State: "idle", LastMessage: "## Summary\n\n" + strings.Repeat("It keeps the project first. ", 20)},
		{ID: "session-silent", Title: "silent", Kind: "session", State: "idle"},
	})
	if got, want := string(navigationSummaryJSONFields(t, rows["session-finished"])["last_message"]), `"Three layouts are ready for review. I recommend B."`; got != want {
		t.Fatalf("finished row last_message on the wire = %s, want %s", got, want)
	}
	wide := rows["session-wide"].LastMessage
	if !strings.HasPrefix(wide, "## Summary It keeps the project first.") || strings.ContainsAny(wide, "\r\n") ||
		utf8.RuneCountInString(wide) > appwire.MaxMessageExcerptRunes {
		t.Fatalf("wide row last message = %q, want one line cut to %d runes", wide, appwire.MaxMessageExcerptRunes)
	}
	if message, carried := navigationSummaryJSONFields(t, rows["session-silent"])["last_message"]; carried {
		t.Fatalf("a row with no message carries %s", message)
	}
}

// The hub schema holds a row's last message to the excerpt the projector's cut
// yields: one trimmed line of valid UTF-8 within the bound.
func TestNavigationSchemaBoundsTheLastMessage(t *testing.T) {
	session := navigationSchemaSession("local:schema-session", "schema-session")
	session.LastMessage = strings.Repeat("é", appwire.MaxMessageExcerptRunes)
	if !navigationSessionValueValid(session) {
		t.Fatal("a last message at its bound was refused")
	}
	for name, message := range map[string]string{
		"past its bound":  strings.Repeat("é", appwire.MaxMessageExcerptRunes+1),
		"a line break":    "Three layouts\nare ready.",
		"a leading space": " Three layouts are ready.",
		"invalid UTF-8":   "ready\xff",
	} {
		session.LastMessage = message
		if navigationSessionValueValid(session) {
			t.Errorf("a last message with %s was accepted", name)
		}
	}
}
```

In `value-records.json`, after the `failure` record in the session:

```json
    "last_message": "Three layouts are ready for review. I recommend B.",
```

In `codec.test.ts`, after PR 9's `test.each` "codec refuses a failure summary with %s":

```ts
// A row carries the opening of its session's last agent message (S1d). The
// codec keeps one within the hub schema's bound and refuses anything else.
test("codec keeps a row's last message within its bound and refuses one past it", () => {
  const onTheBound = "😀".repeat(200);
  expect(
    decodedSnapshot(key, snapshotWithSessionField("last_message", onTheBound)).snapshot.entities[0]?.value,
  ).toEqual({ ...sessionValue("local:session"), last_message: onTheBound });
  for (const malformed of ["", "t".repeat(201), 7, ["Three layouts are ready."]]) {
    expectContentFreeRejection(key, snapshotWithSessionField("last_message", malformed));
  }
});
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestNavigationRowsCarryTheLastMessage|TestNavigationSchemaBoundsTheLastMessage|TestNavigationValueRecordFixtureNamesEveryWireField' -count=1`
Expected: FAIL to compile (`LastMessage` is not a field of `NavigationSessionSummary`).

Run: `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation/codec.test.ts`
Expected: FAIL: the new test (the codec drops `last_message`, so nothing is refused) and the fixture test.

- [ ] **Step 3: Implement**

`hubapi/navigation.go`, in `NavigationSessionSummary`: replace the `Dormant` line after `Failure` with these lines (gofmt aligns the pair):

```go
	// LastMessage is the opening of the session's last agent message (S1d):
	// a Finished row's why line and the long-press preview's excerpt, one
	// line of at most appwire.MaxMessageExcerptRunes. It is the agent's own
	// words, never its reasoning or a tool's output. A live session's comes
	// from its daemon and an ended one's from its meta; subagent rows carry
	// none.
	LastMessage string `json:"last_message,omitempty"`
	Dormant     bool   `json:"dormant,omitempty"`
```

`navigation_projection.go`, in `projectShallow`'s literal after `Failure: ...`: `LastMessage:         appwire.Excerpt(node.LastMessage, appwire.MaxMessageExcerptRunes),`.

`navigation_schema.go`, in `navigationSessionValueValid`'s refusal condition after `utf8.RuneCountInString(value.ApprovalTarget) > maxNavigationLabelRunes ||`:

```go
		appwire.Excerpt(value.LastMessage, appwire.MaxMessageExcerptRunes) != value.LastMessage ||
```

`codec.ts`: add `"last_message",` to `SESSION_KEYS`' optional list after `"failure",`, and in `sessionValue` after the `failure` line:

```ts
    optional(value.last_message, (item) => boundedString(item, 200) && item !== "") &&
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched Go files, `make generate`, and `cd cmd/evener-hub/frontend && npx biome check --write ../../../appwire-client/typescript/state/navigation/codec.ts ../../../appwire-client/typescript/state/navigation/codec.test.ts`.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub -run 'TestNavigation|TestCloneNavigationSummary' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`, `cd cmd/evener-hub/frontend && npx vitest run ../../../appwire-client/typescript/state/navigation && npm run typecheck`
Expected: PASS; `types.gen.ts` gains `last_message?: string` on `NavigationSessionSummary`.

- [ ] **Step 5: Commit**

```bash
git add hubapi/navigation.go cmd/evener-hub/navigation_projection.go cmd/evener-hub/navigation_schema.go cmd/evener-hub/navigation_last_message_test.go cmd/evener-hub/testdata/navigation/value-records.json appwire-client/typescript/state/navigation/codec.ts appwire-client/typescript/state/navigation/codec.test.ts appwire-client/typescript/types.gen.ts
git commit -m "feat(hub): rows carry the opening of their session's last message"
```

### Task 11.3: A remote host's rows carry their last message

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go` (`LocalDaemonEntry.LastMessage` after `LastTurnEndedAt`, `:117`; `threadFromEntry`, `:1172`)
- Create: `cmd/evener-hub/internal/appsource/local_daemon_last_message_test.go`
- Modify: `cmd/evener-hub/app_rpc.go` (`localDaemonEntriesFromRoster`, `:137` and `:172`)
- Modify: `cmd/evener-hub/web_api_tree.go` (`appThreadTreeEntries`, `:933` and `:974`)
- Create: `cmd/evener-hub/remote_last_message_test.go`

**Interfaces:**
- Consumes: `LiveEntry.LastMessage` (Task 11.1); `NavigationSessionSummary.LastMessage` (Task 11.2).
- Produces: `appsource.LocalDaemonEntry.LastMessage string`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/internal/appsource/local_daemon_last_message_test.go`:

```go
package appsource

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller carries each root's last
// message on its row, and no key on a row with none (S1d).
func TestLocalDaemonSourceListCarriesTheRootsLastMessage(t *testing.T) {
	const message = "Three layouts are ready for review."
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/finished", ThreadID: "th_finished", SessionID: "sess_finished"}, Status: appwire.ThreadStatusIdle, LastMessage: message},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/fresh", ThreadID: "th_fresh", SessionID: "sess_fresh"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]string{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread.Evener.LastMessage
	}
	if byID["th_finished"] != message || byID["th_fresh"] != "" {
		t.Fatalf("last messages = %q, want th_finished's %q and none on th_fresh", byID, message)
	}
}
```

`cmd/evener-hub/remote_last_message_test.go`:

```go
package hub

import (
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows carry a root's last message and never lend it to the
// root's in-process subagent aliases: a coordinator's words are its own.
func TestLocalDaemonEntriesFromRosterCarryTheLastMessageOnlyOnTheRoot(t *testing.T) {
	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{{
		Entry:     rendezvous.Entry{ThreadID: "sess_root", SessionID: "sess_root"},
		SessionID: "sess_root", Status: appwire.ThreadStatusIdle,
		RunningSubagentIDs: []string{"sess_child"}, LastMessage: "Three layouts are ready.",
	}})
	if len(entries) != 2 || entries[0].LastMessage != "Three layouts are ready." || entries[1].LastMessage != "" {
		t.Fatalf("entries = %+v, want the root's message and none on its alias", entries)
	}
}

// A controller reads a remote host's last message off its list row, so the
// remote row carries it like a local one: an online host's live row from the
// row as its live entry, and an offline host's last-known row, which is not
// live, from the row as its meta (S1d).
func TestNavigationRemoteRowsCarryTheirLastMessage(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{
		{ID: "online-thread", Source: "remote-online", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, Evener: appwire.EvenerThread{LastMessage: "The online host's words."}},
		{ID: "offline-thread", Source: "remote-offline", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle}, Evener: appwire.EvenerThread{LastMessage: "The offline host's words."}},
	})
	registry := appsource.NewRegistry()
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "remote-online"}, online: true})
	registry.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "remote-offline"}, online: false})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})
	web.sources = registry

	captured, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	projection, err := buildNavigationProjection(captured.Inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	for ref, want := range map[string]string{
		"remote-online:online-thread":   "The online host's words.",
		"remote-offline:offline-thread": "The offline host's words.",
	} {
		if row := navigationProjectedSummary(t, projection, ref); row.LastMessage != want {
			t.Errorf("%s last message = %q (live %v), want %q", ref, row.LastMessage, row.Live, want)
		}
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/appsource -run 'TestLocalDaemonSourceListCarriesTheRootsLastMessage' -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRosterCarryTheLastMessageOnlyOnTheRoot|TestNavigationRemoteRowsCarryTheirLastMessage' -count=1`
Expected: FAIL to compile (`LastMessage` is not a field of `LocalDaemonEntry`); once it exists, the remote rows test fails with no message on either row.

- [ ] **Step 3: Implement**

`local_daemon.go`, in `LocalDaemonEntry` after `LastTurnEndedAt`:

```go
	// LastMessage mirrors hubcore.LiveEntry.LastMessage: the opening of the
	// root's last agent message (S1d). threadFromEntry carries it into
	// appwire.EvenerThread.LastMessage. A read-only alias has none.
	LastMessage string
```

and in `threadFromEntry`'s `Evener` literal after `LastTurnEndedAt:    item.LastTurnEndedAt,`: `LastMessage:        item.LastMessage,`.

`app_rpc.go`, in `localDaemonEntriesFromRoster`: `LastMessage:        item.LastMessage,` after `LastTurnEndedAt:    hubcore.UnixMilliseconds(item.LastTurnEndedAt),`, and among the alias's clears, after `child.LastTurnEndedAt = 0 // the root's turn is not the child's`:

```go
			child.LastMessage = ""    // nor are the root's words
```

`web_api_tree.go`, in `appThreadTreeEntries`: in the `schema.SessionMeta` literal after `IsSubagent:      thread.Evener.Kind == "subagent",`:

```go
		// The row's last message reaches the tree through the meta too, so an
		// offline host's last-known row, which is not live, keeps it (S1d).
		LastMessage: thread.Evener.LastMessage,
```

and after `entry.LastTurnEndedAt = hubcore.UnixMilliTime(thread.Evener.LastTurnEndedAt)`:

```go
	entry.LastMessage = thread.Evener.LastMessage
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./cmd/evener-hub/internal/appsource -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRoster|TestNavigationRemote|TestNavigationOffline' -count=1`
Expected: PASS.

Gates: `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...` (root module), `golangci-lint run ./cmd/evener-hub/ ./cmd/evener-hub/internal/appsource/ ./cmd/evener-hub/internal/hubcore/ ./hubapi/`, `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`, `make lint-generated`, `make secret-scan`.

- [ ] **Step 5: Commit and open PR 11**

```bash
git add cmd/evener-hub/internal/appsource/local_daemon.go cmd/evener-hub/internal/appsource/local_daemon_last_message_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/web_api_tree.go cmd/evener-hub/remote_last_message_test.go
git commit -m "feat(hub): remote hosts' rows carry their last message"
```

Title "feat(hub): Finished rows open with the agent's last words (S1d hub, phase 7 PR 11)". The body says rows carry `last_message` for top-level sessions, live and ended, and none for subagents (ruling 21, with the byte arithmetic), that it is the agent's own words (ruling 20), that remote rows carry it online and offline, and that a phase 2 notification may carry it only with the message-preview opt-in.

---

## PR 13: remote rows carry task progress, S13b (Task 13.1)

**Branch:** `git fetch origin && git switch -c claude/s13b-remote-task-progress origin/main`. It depends on no PR here: every anchor below is a line on main at `1c9cb464a` (dry-run there alone). If PR 11 merged first, its `LastMessage` line sits beside each `LastTurnEndedAt` anchor; put the `Tasks` line after either one.

**What it adds.** A controller hub's rows for a live session on another host carry `tasks`, so the Board's task line needs no host case. `tasksFor` already reads the live entry, so the tree needs no change, and the remote cache compares whole rows (`hubcore/remotecache.go:130`), so a task change invalidates navigation with no fingerprint change (ruling 25).

### Task 13.1: A remote host's rows carry their task progress

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/appsource/local_daemon.go` (`LocalDaemonEntry.Tasks` after `LastTurnEndedAt`, `:106`; `threadFromEntry`, `:1159`)
- Modify: `cmd/evener-hub/app_rpc.go` (`localDaemonEntriesFromRoster`, `:135` and `:167`)
- Modify: `cmd/evener-hub/web_api_tree.go` (`appThreadTreeEntries`, `:972`)
- Modify: `hubapi/navigation.go` (the `Tasks` doc comment in `NavigationSessionSummary`, `:321-325`)
- Create: `cmd/evener-hub/internal/appsource/local_daemon_tasks_test.go`, `cmd/evener-hub/remote_tasks_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`

**Interfaces:**
- Consumes: `hubcore.LiveEntry.Tasks` (`roster.go:92`, from the probe); `appwire.CloneTaskAggregate`; `tasksFor` (`hubcore/tree.go:1075`), unchanged.
- Produces: `appsource.LocalDaemonEntry.Tasks *appwire.TaskAggregate`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/internal/appsource/local_daemon_tasks_test.go`:

```go
package appsource

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
)

// A hub listing its own sessions to a controller carries each root's task-list
// progress on its row, as its own copy, and no key on a row whose daemon
// cannot read its tasks (S13b).
func TestLocalDaemonSourceListCarriesTheRootsTasks(t *testing.T) {
	tasks := &appwire.TaskAggregate{Total: 7, Done: 3, Remaining: 4, Current: &appwire.TaskSummary{ID: 4, Description: "Fix the settle/drain race"}}
	source := NewLocalDaemonSourceWithEntries("local", func() []LocalDaemonEntry {
		return []LocalDaemonEntry{
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/planned", ThreadID: "th_planned", SessionID: "sess_planned"}, Status: appwire.ThreadStatusActive, Tasks: tasks},
			{Entry: rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: "ws://127.0.0.1/unknown", ThreadID: "th_unknown", SessionID: "sess_unknown"}, Status: appwire.ThreadStatusIdle},
		}
	}, nil)
	resp, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	byID := map[string]*appwire.TaskAggregate{}
	for _, thread := range resp.Data {
		byID[thread.ID] = thread.Evener.Tasks
	}
	if !reflect.DeepEqual(byID["th_planned"], tasks) || byID["th_unknown"] != nil {
		t.Fatalf("tasks = %+v, want th_planned's %+v and none on th_unknown", byID, tasks)
	}
	byID["th_planned"].Current.Description = "changed"
	if tasks.Current.Description != "Fix the settle/drain race" {
		t.Fatal("a listed row aliases the roster's task progress")
	}
}
```

`cmd/evener-hub/remote_tasks_test.go`:

```go
package hub

import (
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

// The hub's own list rows carry a root's task progress and never lend it to
// the root's in-process subagent aliases: the task list is the root's.
func TestLocalDaemonEntriesFromRosterCarryTasksOnlyOnTheRoot(t *testing.T) {
	tasks := &appwire.TaskAggregate{Total: 2, Done: 1, Remaining: 1}
	entries := localDaemonEntriesFromRoster([]hubcore.LiveEntry{{
		Entry:     rendezvous.Entry{ThreadID: "sess_root", SessionID: "sess_root"},
		SessionID: "sess_root", Status: appwire.ThreadStatusActive,
		RunningSubagentIDs: []string{"sess_child"}, Tasks: tasks,
	}})
	if len(entries) != 2 || !reflect.DeepEqual(entries[0].Tasks, tasks) || entries[1].Tasks != nil {
		t.Fatalf("entries = %+v, want the root's tasks %+v and none on its alias", entries, tasks)
	}
}

// A controller reads a remote host's task progress off its list row, so the
// remote live row carries its task line like a local one (S13b).
func TestNavigationRemoteLiveRowCarriesItsTaskProgress(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{{
		ID: "planned", Source: "host-a", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener: appwire.EvenerThread{Tasks: &appwire.TaskAggregate{Total: 7, Done: 3, Remaining: 4, Current: &appwire.TaskSummary{ID: 4, Description: "Fix the settle/drain race"}}},
	}})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})

	captured, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	projection, err := buildNavigationProjection(captured.Inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}
	want := &hubapi.NavigationTaskProgress{Total: 7, Done: 3, CurrentID: 4, Current: "Fix the settle/drain race"}
	if row := navigationProjectedSummary(t, projection, "host-a:planned"); !reflect.DeepEqual(row.Tasks, want) {
		t.Fatalf("remote row tasks = %+v, want %+v", row.Tasks, want)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/appsource -run 'TestLocalDaemonSourceListCarriesTheRootsTasks' -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRosterCarryTasksOnlyOnTheRoot|TestNavigationRemoteLiveRowCarriesItsTaskProgress' -count=1`
Expected: FAIL to compile (`Tasks` is not a field of `LocalDaemonEntry`); once it exists, the remote row test fails with no task progress.

- [ ] **Step 3: Implement**

`local_daemon.go`, in `LocalDaemonEntry` after `LastTurnEndedAt`:

```go
	// Tasks mirrors hubcore.LiveEntry.Tasks: the root's task-list progress
	// (S13b). threadFromEntry carries a copy into appwire.EvenerThread.Tasks,
	// so a controller hub shows a remote session's task line. nil means the
	// daemon cannot read its task state; a read-only alias has none.
	Tasks *appwire.TaskAggregate
```

and in `threadFromEntry`'s `Evener` literal after `LastTurnEndedAt:    item.LastTurnEndedAt,`: `Tasks:              appwire.CloneTaskAggregate(item.Tasks),`.

`app_rpc.go`, in `localDaemonEntriesFromRoster`: `Tasks:              item.Tasks,` after `LastTurnEndedAt:    hubcore.UnixMilliseconds(item.LastTurnEndedAt),`, and among the alias's clears, after `child.LastTurnEndedAt = 0 // the root's turn is not the child's`:

```go
			child.Tasks = nil         // nor is the root's task list
```

`web_api_tree.go`, in `appThreadTreeEntries` after `entry.LastTurnEndedAt = hubcore.UnixMilliTime(thread.Evener.LastTurnEndedAt)`:

```go
	// The remote hub's root row carries its session's task progress (S13b), so
	// the remote row shows its task line like a local one.
	entry.Tasks = appwire.CloneTaskAggregate(thread.Evener.Tasks)
```

`hubapi/navigation.go`: in `NavigationSessionSummary`, the `Tasks` doc comment's last two lines, "every session this hub has no live daemon entry for: ended sessions, / in-process children, and rows from other hosts.", become:

```go
	// every session with no live daemon entry: ended sessions and in-process
	// children. A live session on another host carries its host's (S13b).
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files, then `make generate`.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./cmd/evener-hub/internal/appsource -count=1`, `go test ./cmd/evener-hub -run 'TestLocalDaemonEntriesFromRoster|TestNavigationRemote|Tasks' -count=1`, `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`
Expected: PASS; `types.gen.ts` carries the new comment on `NavigationSessionSummary.tasks`.

Gates: `go vet ./...`, `go vet -tags evenerfuzz ./...`, `GOOS=windows go vet -tags evenerfuzz ./...` (root module), `golangci-lint run ./cmd/evener-hub/ ./cmd/evener-hub/internal/appsource/ ./hubapi/`, `make lint-generated`.

- [ ] **Step 5: Commit and open PR 13**

```bash
git add cmd/evener-hub/internal/appsource/local_daemon.go cmd/evener-hub/internal/appsource/local_daemon_tasks_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/web_api_tree.go cmd/evener-hub/remote_tasks_test.go hubapi/navigation.go appwire-client/typescript/types.gen.ts
git commit -m "feat(hub): remote hosts' rows carry task progress"
```

Title "feat(hub): remote rows carry task progress (S13b, phase 7 PR 13)". The body says a remote live row now carries `tasks` like a local one, aliases carry none, ended sessions stay without a task line (the server plan's ruling: a disk read per session), and the remote cache's whole-row comparison invalidates navigation with no fingerprint change.

---

## Self-review

- **Spec coverage.** S1b: spec 7.1's "Question · keep or drop the implied options?", 7.2's two-line Needs you why line, 7.3's long-press preview (the option labels, "Question 1 of N"), 8.4's ask dock (the same words) and 13.1's question why line: PRs 7 and 8. S1c: 7.1's "Failed · codex-jesse-fsck.com sign-in expired (401)", 13.1's "Failed: <error summary>", and 13.2's rule that a subagent's failure never marks the coordinator's row: PR 9. S1d: 7.2's Finished why line, 18's "about 200 characters", 7.3's preview and 13.4's payload rule: PRs 10 and 11. S13b: 7.2's task line on every live row: PR 13. Spec 18's fallbacks: every field is optional, and the phone lane handoff names the fallback each one retires. The documents-and-artifacts part of S1 stays blocked on the shared-artifacts work, as the server plan says.
- **Checked by running.** Every task is its own commit on a scratch branch from main at `1c9cb464a` (Tasks 7.1 to 13.1, then the excerpt's fixed-point check and a comment fix), and each commit builds and vets on its own. The chain was then replayed onto main at `8d671c559`, whose six newer commits touch no file this plan edits, and at its tip these passed: `go build`; `go vet`, with `evenerfuzz` and for Windows, in both modules; the agent tests the tasks name, with the ask-tools fuzz program; the full `appwire`, `server`, hubcore and appsource suites; the `cmd/evener` envelope tests and the hub's navigation, remote, task and thread-read tests; `TestGeneratedFileCurrent`; `make generate` leaving no diff; golangci-lint on every touched package, with the appwire config on `server/`; the navigation codec's vitest suite and `npm run typecheck`; `make lint-generated`; and `make secret-scan`. Three orders the Depends on column allows were dry-run separately: PR 9's daemon tasks and PR 10 on main with PR 7 alone, and PR 13 on main alone.
- **Not run.** `make test-api-package` (the package qualification), `make test-web`, `make test-web-browser` and `make test-native`: nothing here changes web or phone code, and the codec's own suite ran. The whole `agent` suite: targeted runs only; on this Mac, unmodified main already fails `TestRollbackFreshDelegateWorktreeUsesCarriedProjectMetadataDir` (`/var` against `/private/var`), which no PR here touches. Linux: every run above is macOS, and CI's Ubuntu runners have caught tests that pass locally before.
- **Placeholders.** None. Each task carries its code or the exact line to change, and each test is written out.
- **Names.** One spelling across tasks: `appwire.Excerpt` and its five bounds; `BoundedPendingQuestion`, `PendingQuestion`, `ClonePendingQuestion`; `ThreadFailure`, `CloneThreadFailure`, `RestingFailure`, `restingFailureLocked`, `historyTurnFailure`, `facetTurnFailure`; `NavigationQuestion`, `NavigationFailure`, `NavigationFailureCrashed`; `navigationQuestion`, `navigationFailure`, `navigationQuestionValid`, `navigationFailureValid`; `pendingQuestionFor`, `failureFor`, `lastMessageFor`; `lastAgentText`, `noteAgentMessageLocked`; `LastMessage` in Go, `lastMessage` on AppWire, `last_message` on rows and in the meta file. Every test the plan names exists on the dry run.
- **Review Focus.** Each line names the tests that pin it, in the task that owns the code; all of them pass on the dry run.
- **Departures from the server plan's sketches.** S1b: no `header` (the why line is the question, spec 13.1); bounds of 200, 80 and 5 in place of 512 and 200 (ruling 2); `count` uncapped (ruling 7); the flag and the text from one source call in place of a new method sampled on the ask carriers (ruling 8). S1c: no `message` (ruling 12); `status` added; the cause flattened on rows (`cause_kind`, `provider`, `status`); a title bound of 80. S1d: a plain string with no `at`, since the row's `turn_ended_at` dates it (ruling 19); recorded as each message is written, where the sketch read the loop's turn-local `lastText` (ruling 17); top-level rows only, live and ended, which settles the byte budget without the fitter (ruling 21).
