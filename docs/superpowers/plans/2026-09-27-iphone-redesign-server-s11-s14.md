# iPhone redesign, Phase 7: hub notices and message search (Implementation Plan)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. Every task carries its code, so every task's implementer is Sonnet; the reviewer checks it against this plan and the code as it is on main.

**Goal:** The Board's notices come from the hub (S11a), a Codex sign-in the issuer refused shows up as needing a sign-in (S11b), and Board search finds words inside sessions' messages and offers an Archived scope (S14a, S14b).

**Architecture:**
- **S11a (PR 22).** The hub derives its notices from the answers the phone's fallback already reads: `evener/auth/list` rows that need a login, the navigation manifest's offline sources, and `evener/plugin/list` rows marked broken. Each notice names the live sessions it blocks when the hub can count them: for a sign-in, this hub's live sessions whose current model runs on the provider instance (the probe now keeps `Evener.Profile`); for a host, its sessions that were live when last reached. A new method, `evener/notices/list`, answers with the list, and a watcher re-derives it every five seconds and broadcasts `evener/notices/changed` with the new list when it changes.
- **S11b (PR 23).** The #2479 fix. When a Codex token refresh fails permanently, the auth package writes a small note beside the record naming the refused refresh token by its SHA-256. Status (the hub's `evener/auth/list` and the CLI's `evener openai status`) reports `needsLogin` while the record still holds that token. Saving a record (a login, or a refresh that worked) clears the note, and so does signing out. The note never rewrites the record itself, so a concurrent refresh by another process can never be undone. S11a's watcher then raises the sign-in notice.
- **S14a (PR 24).** A message-text index in its own SQLite file, `<hub state>/search.db`. It is FTS5 over the text of every user and agent message in this hub's format-2 transcripts, kept under the transcript key and position a thread read gives each item. It reads transcripts through the transcript read model (`internal/transcriptindex`), the same index thread reads use. On the past index's 60-second tick it re-reads only the transcripts whose size or modification time moved, and then only what changed since the snapshot it holds (`ChangedSince`). Deleting a session forgets its messages at once.
- **S14b (PR 25).** `evener/search` gains a scope (`all`, `live`, `archived`) that filters every group before the limit, an `archived` flag on every result computed by the rail's own rule, the `inSessions` group (each matching session with its match count and its newest three hits, each hit with a transcript key, a position and a one-line snippet whose matched words are marked), and the applied `scope` echoed as the capability signal. A live session is listed once, live, even when only its prompt matched.

**Tech Stack:** Go 1.27 workspace (root module and `agent/`), AppWire over WebSocket (`ProtocolVersion` `"evener-appwire-v6"`), SQLite through `modernc.org/sqlite` with FTS5, TypeScript 6 in `appwire-client/typescript` (generated types only).

**Spec:** `docs/superpowers/specs/2026-09-25-mobile-app-redesign-design.md`: 7.1 (notices), 7.4 (search), 7.5 (offline), 12 (Hub > Hosts and Providers), 13.3 (an alert when a notice appears), 17 and 18 (S11, S14). The server plan `docs/superpowers/plans/2026-09-26-iphone-redesign-server-additions.md` holds the design-level sections this plan replaces (PRs 22 to 25), its Global Constraints and Jesse's answers (answer 2 drops "expiring within a day"). Written and dry-run against main at `d18386ade` (see Self-review).

## What was measured

Everything below was read on main at `d18386ade` or measured on this machine (m4.local) on 2026-09-27. Transcript measurements read file sizes and counts, and a throwaway tool that printed only counts, byte totals and durations; no transcript content was printed or kept.

**Search today.**
- `hubSearch` (`cmd/evener-hub/app_search.go:14`) matches live roster entries by ID or title substring and past sessions through `PastIndex.Search` (`hubcore/past.go:581`). Past search is FTS5 over ID, name, original prompt and working directory (`past_sessions_fts`, `past.go:655`) merged with an in-memory substring scan, capped at 20.
- A live session's meta is in the past index too, so a live session whose prompt matches comes back a second time as an "ended" past result (there is no dedupe).
- Nothing indexes message text.
- `SearchParams` is decoded with plain `json.Unmarshal` (`internal/appserver/router.go:44`), so an older hub ignores an unknown `scope` key and answers as before. That is why the response echoes the scope it applied.

**The archive rule.** `classifySession` (`hubcore/tree.go:404`) makes an explicit session decision win and auto-archives past two weeks of inactivity. The Live band's filter (`tree.go:1597`) also drops a session whose project the owning source archived. Both are unexported.

**The transcript store.** `~/.local/state/evener/projects/*/sessions/`:

| Measure | Value |
|---|---|
| Transcripts | 1,105 files, 537 MB (median 0.18 MB, p90 0.76 MB, p99 3.7 MB, largest 55.7 MB) |
| Format 2 (readable by any reader) | 223 files, 149 MB; the other 882 are format 1, which every reader refuses (`transcript.ErrUnsupportedFormat`) |
| Message items in the format-2 files | 2,345 (426 user, 1,912 agent, 7 human steers), 2.27 MB of text: 1.5% of the transcript bytes |
| Items of any kind | 33,725 |

**Scanning per query is ruled out.**
- A lowercase byte scan of the 537 MB took 7.3 s.
- The canonical projection of the 149 MB of format-2 transcripts took 2.9 s.
- Both are far slower than a keystroke. The spec's Archived scope rules out bounding a scan to recent sessions.

**The index's cost.** An in-memory FTS5 build of the real message text, in the layout PR 24 uses (external-content table, `unicode61 remove_diacritics 0`), and the same text repeated 20 times to stand for a busier hub:

| | 1x (this machine) | 20x |
|---|---|---|
| Messages / text | 2,345 / 2.3 MB | 46,900 / 45 MB |
| Build | 0.2 s | 6.0 s |
| Database | 3.8 MB (1.7x text) | 73 MB (1.6x text) |
| Match and group by session: `settle*` | 0.08 ms | 0.4 ms |
| `set*` | 0.3 ms | 4.4 ms |
| `se*` | 1.3 ms | 24 ms |
| `a*` | 5 ms | 82 ms |

A one-letter prefix is the expensive query and matches nearly every session, which is why a message search needs a word of two characters.

**Reading through the transcript index.**
- Opening `transcriptindex` for all 223 format-2 transcripts and reading every item back took 8.3 s in all: 12.8 ms p50, 106 ms p99 and 491 ms max per build.
- It wrote 7.0 MB of sidecars. A thread read writes the same sidecar the first time it opens a session (`transcriptindex.DirFor`), and a daemon writes it for its own session.
- Peak memory was 55 MB.

**Freshness.** The past index rebuilds every 60 s (`cmd/evener-hub/main.go:764`), and the index refreshes on the same interval. A refresh stats every listed transcript. It re-reads only the ones that grew, and only their changes (`transcriptindex.ChangedSince`, `internal/transcriptindex/window.go:297`).

**Item keys.**
- On main a thread read's items come from the transcript read model (#2475), keyed `apptranscript-item-v2:<turn>:<entry ordinal>:<part>` (`internal/transcriptindex/key.go:33`), with `position {entry, item, sub}`.
- The phone's reader anchor already restores to `itemKey` or `itemPosition` (`mobile-native/src/readerPosition.ts`).
- Reading through the same index is what makes a hit's key the reader's key. `TestMessageSearchHitsAreTheItemsAThreadReadShows` pins it against `pastThreadReadResponse`.

**Notices today.**
- Sign-in: `evener/auth/list` (`app_auth.go:706`) reports `needsLogin` from `openAIStatusFromRecord` (`app_auth.go:1020`): after #2483, an expired access token with no refresh token.
- A refresh the issuer refuses returns `ErrLoginRequired` from `ResolveRuntimeCredentials` (`auth/openai/service.go:486`) and is recorded nowhere (#2479).
- The daemon's Codex transport turns that into an `llm.ConfigurationError` (`llm/providers/tokenauth/codex.go:57`). It carries no provider, so `providerCauseFromError` (`agent/diagnostics.go:83`) gives the failure no cause, and `diagnostic.FromError` (`agent/diagnostic/diagnostic.go:65`) titles it "Evener configuration error".
- So S1c's row failure summary cannot identify a refused sign-in. That is why S11 does not derive sign-in notices from failed rows, as the server plan's sketch proposed (ruling 2).
- Hosts: the manifest's `sources[].online` (`web_api_tree.go:1042`, `sourceOnline` `web.go:86`) is the SSH manager's attached state.
- The background refresh never dials a host (`web_api_tree.go:620`), so a host is offline until something attaches it, and an offline host keeps its last-known rows in the remote cache (`web_api_tree.go:399`).
- Plugins: `PluginEntry.Broken` is an install directory that fails validation (`internal/plugins/install.go:409`), with no reason and no per-session use on any row.
- Provider per session: the probe drops `Evener.Profile` (`hubcore/prober.go`). The daemon keeps it current across a model switch (`server/server.go:598`), while the rendezvous entry's `Provider` is the one the session started on.

## Global Constraints

- **Wire.** Every change is additive and stays on `ProtocolVersion = "evener-appwire-v6"`. New keys are optional (`omitempty`), and an old peer reads a missing key as no information. `evener/notices/changed` is a new notification, and the TS client hands any notification to its subscribers without validation (`appwire-client/typescript/client.ts`, the `msg.method` branch), so an older phone ignores it. `evener/notices/list` is a new method, and an older hub answers it with MethodNotFound, which is the phone's cue to keep its fallback. `SearchParams.scope` is ignored by an older hub, and `SearchResponse.scope` says whether it was applied.
- **Never add a `FeatureSet` key.** The TS `initialize` decoder refuses unknown feature keys.
- **Casing.** `appwire` JSON is camelCase (`affectedSessions`, `inSessions`, `hitCount`, `transcriptKey`). `auth/openai` JSON is snake_case (`refresh_token_sha256`). The tagliatelle lint enforces both (`.golangci.yml`).
- **Generated files.** After any `appwire` type or catalog change, doc comments included: run `make generate`, then `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`. Commit `appwire-client/typescript/types.gen.ts` and `docs/appwire-protocol.md`.
- **The TUI rule.** A new notification is dispatched by the TUI or listed in `notifyMethodsDeliberatelyIgnored` (`cmd/evener-tui/hub_notification_coverage_test.go`). A hub-wide notification is listed in `TestThreadNotificationsRequireAuthoritativeRoutingIdentity`'s `global` set (`appwire/protocol_test.go`). No PR changes `internal/appprojector`.
- **A new hub method** gets a catalog row (`appwire/protocol.go`), a row in `TestHostAdminAllowListMatchesCatalog`'s policy (`cmd/evener-hub/app_host_admin_test.go`), and an entry in `TestHubRPCRegistersExpectedHandlerSet`'s expected list (`cmd/evener-hub/app_rpc_test.go`).
- **hubcore tests.** Probe tests are `fuzzScenario*` functions registered alphabetically in `FuzzHubcoreScenarios` (`hubcore/scenarios_fuzz_test.go`). Run them with `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$' -count=1`. An unregistered one never runs, and `golangci-lint run ./cmd/evener-hub/internal/hubcore/` reports it unused. Store tests are plain `Test*` functions, as `archive_test.go` is.
- **Go floors,** per module touched (root; `auth/openai` is in the root module):
  - `go vet ./...`, `go vet -tags evenerfuzz ./...` and `GOOS=windows go vet -tags evenerfuzz ./...`;
  - format with `$(go env GOROOT)/bin/gofmt`;
  - run the pinned `golangci-lint` (2.13.1, `.tool-versions`) on each touched package.
- **TypeScript floor.** `cd cmd/evener-hub/frontend && npm run typecheck`, and `npx vitest run ../../../appwire-client/typescript/reducer.test.ts ../../../appwire-client/typescript/testing/fakeClient.test.ts`: both walk every generated method and notification name. Never run Biome from the repo root, and never `npm ci` through a symlinked `node_modules`.
- **Targeted tests only.** Run each task's own tests and the gates it names; CI runs the full matrix.
- **Deterministic tests.**
  - No network, no provider credentials, no sleeps.
  - The notice watcher takes its tick channel as a parameter, and its tests drive it with an unbuffered channel.
  - Every new test is shown failing before its code lands.
- **Privacy.** `search.db` holds message text and is created 0600, with its WAL file inheriting the mode. The refusal note never holds the refresh token. No PR logs message text.
- **Line numbers** are on main at `d18386ade`. A merge from another lane moves them a few lines, so every step also names its anchor: find it by the name.
- **Size.** Measured on the dry run, production lines (tests and generated output excluded): PR 22 321, PR 23 105, PR 24 about 600, PR 25 322. Landing follows the roadmap: a regular PR, CI green at the head, the RoboRev comment read, /simplify run and its fixes pushed, then an admin squash merge.

## Rulings

Decisions the spec and the server plan leave open, with the reason for each.

**S11, the notices**

1. **Three kinds, one list, ordered as the phone orders them.**
   - `signInRequired`, `hostOffline` and `pluginBroken`, sign-ins first, then hosts, then plugins; within a kind, by subject, and plugins in `evener/plugin/list`'s order.
   - "signInRequired", not the server plan's "signInExpired": a refused refresh is not an expiry.
   - A notice's `id` is `<kind>:<subject>` (plugins `<kind>:<plugin>@<marketplace>`), stable while the problem lasts, so the phone can alert on a new id (spec 13.3) and key rows by it.
   - The server plan's `label` is dropped: every label would equal the subject (the manifest labels a host by its ID).
2. **A sign-in notice comes from the credential state, not from failed rows.**
   - The notice exists exactly when that instance's `evener/auth/list` row says `needsLogin`, so the notice and the Providers screen its action opens always agree.
   - The server plan derived it from sessions resting Failed with an auth cause. A refused refresh fails a turn as an `llm.ConfigurationError` with no provider and no cause (see What was measured), so that derivation misses the case that matters most.
   - S11 therefore no longer depends on PR 9 (S1c).
3. **A sign-in notice counts the sessions that use the instance.**
   - The count is the live, uncrashed sessions on this hub whose current model runs on the provider instance.
   - They are the sessions the problem blocks: each fails at its next refresh. The Failed ones are among them.
   - The probe now keeps `Evener.Profile`, which the daemon keeps current across a model switch. The rendezvous entry's `Provider` goes stale.
   - See question 1.
4. **A host notice counts the host's sessions that were live when last reached.**
   - Those are the remote cache's rows for that source whose status is live, subagents left out: the rows a Board still shows for the host.
   - A host never reached since the hub started has none, and its notice carries no count.
5. **A plugin notice carries no count.** No row says which plugins a session runs. Spec 18 asks for counts on every kind, so this is question 5.
6. **An offline host means what the manifest says.** A host is offline when its source is not attached. A host the hub has not attached since it started is offline too, so its notice shows until someone connects it. The phone's fallback reads the same manifest, so this changes nothing the phone shows today. See question 2.
7. **A method and a notification that carries the list.**
   - The list is not on the navigation manifest: auth and plugin reads are file reads, and notices change on their own clock.
   - The notification carries the whole new list (a few rows) rather than being payload-free, so a phone updates without a round trip. `evener/notices/list` serves a client that connects later.
   - Both are hub-wide and name no thread.
8. **The hub watches for changes it did not cause.**
   - A watcher re-derives the notices every five seconds (the attention watcher's cadence) and announces a change once.
   - An access token that expires with no refresh token, a refusal a daemon noted, a host dropping and a plugin directory vanishing each have no event.
   - The read is cheap: `evener/auth/list` reads a few small files, and `evener/plugin/list` validates each install directory (14 on Jesse's hub).
   - A kind whose read fails keeps its last notices, for `evener/notices/list` and the watcher alike (both go through `hubNotices.read`), so a transient failure never reports a problem resolved and then new again.
   - When the set of providers needing a sign-in changes, the watcher first broadcasts the no-data `evener/auth/updated`, so a providers pane refreshes a state that changed with no write.
9. **This hub's own providers, hosts and plugins only.**
   - A remote host's sign-ins and plugins are the host's own: its own `evener/auth/list`, reachable through `evener/host/request`.
   - A failed session there already says why on its row.
   - See question 3.

**S11b, the refused refresh (#2479)**

10. **S11b is the #2479 fix.** #2479 is exactly the signal S11b needs, it is small, and nobody else has it open. PR 23 closes #2479.
11. **A note beside the record, never a rewrite of it.**
    - With refresh-token rotation, two processes refreshing one record race: the loser's old token is refused.
    - Marking the refusal on the record would need a read-check-write that can put the old token back over the winner's new one. A note naming the refused token by its SHA-256 cannot: once the record holds another token, the note no longer matches.
    - The note is `auth/<instance>.json.refresh-rejected`. It does not end in `.json`, so it can never be another instance's record.
12. **The note only reports.**
    - `ResolveRuntimeCredentials` still tries every refresh. A later refresh that works saves the record, and saving clears the note.
    - So a refusal the issuer takes back heals itself, and nothing changes how a turn behaves.
    - Signing out clears the note too.
13. **Both status readers read it:** the hub's `openAIStatusFromRecord` (signed in false, needs login true) and the CLI's `statusFromRecord` (needs login true).

**S14, message search**

14. **What a message is.** A `userMessage`, a `steering` item the user sent (`source: "user"`), and an `agentMessage`, which includes a delivered communicate message. Reasoning, tool calls and their output, and system notices are not messages (spec 7.4: "message text matches").
15. **Every format-2 transcript this hub holds, subagents included.** Search's Sessions group already lists subagent sessions, and a subagent opens as its own session (spec 9). See question 4.
16. **Hits are the reader's items.**
    - The index reads through `internal/transcriptindex`, so a hit carries the `transcriptKey` and `position` a thread read gives the same item, and the phone opens the session at it with its reader anchor.
    - The index opens its own handle per transcript and closes it: indexing a thousand transcripts must not evict the 64 handles readers hold (`transcriptindex.DefaultCacheCapacity`).
    - A legacy transcript is detected from its header first, so no empty sidecar is left beside it.
17. **An FTS5 index in its own file, refreshed incrementally.**
    - `search.db` sits beside `index.db`, whose header and write lock the archive, favorite, pin and seen stores share.
    - Its schema version lives in `PRAGMA user_version`; any other version is dropped and rebuilt, because the index is a cache.
    - It re-reads a transcript only when its size or modification time moved. With the same incarnation it applies `ChangedSince` (each changed item's row is dropped and written again); otherwise it replaces the session's rows.
    - A transcript it cannot read is recorded with no rows and not read again until it changes. A format-1 transcript is not a failure; any other failure is logged once.
    - A rewrite that keeps a transcript's size and modification time would go unseen until the file next grows. Transcripts only grow; a rewrite (the v2 upgrade) changes both.
18. **Freshness is one past-index interval (60 s).** A live session's newest words become searchable within a minute. The Sessions group's title and prompt matches are immediate, and Find in session (phase 3) searches what the phone has loaded.
19. **Matching.**
    - Every word must match as a prefix, letter case aside, with words split on anything but letters, digits and underscores. This is the title search's own rule (`ftsQuery`, now sharing `hubcore.SearchTokens`), so FTS5 syntax in a search is only words.
    - Diacritics are not folded, so the snippet's marks fall where the index matched.
    - A search with no word of at least two characters searches no messages: one letter prefixes nearly every word, and it is the most expensive query (measured above). The Sessions group still answers it.
20. **The In sessions group.**
    - At most 20 sessions: live ones first in the Live order, then ended ones newest first. Each carries its match count (`hitCount`) and its newest three hits.
    - A hit's snippet is the message as one line around its first match: 40 runes before it and 160 in all, cut at word breaks and marked with "…".
    - The snippet comes as parts (`{text, match}`), so a client needs no offsets in UTF-16 or runes.
    - The phone shows `match` parts bold (spec 16.1: "search hits are bold").
21. **Scopes.**
    - `all` is everything.
    - `live` is what the Board's Live section holds: live and not archived (spec 7.1).
    - `archived` is every session the rail files as archived, live or ended.
    - The scope filters before the limits, so an archived match is never crowded out by newer unarchived ones. An unknown scope is InvalidParams.
22. **One archive rule.**
    - `hubcore.SessionArchived` is the rail's rule: the owning source archived the project, or the session's own decision archives it, or, with no decision, two weeks without activity.
    - The Live band's filter now calls it, and search's `archived` flag reports it.
    - A past session's project is its state directory's name. Search passes the controller's own source: the roster lists only this hub's daemons, and remote rows never reach `hubSearch` (ruling 25).
23. **Each session once.** A live session is listed only in `live`, and a prompt match lists it there too (spec 7.4: "title and prompt matches"). This fixes today's duplicate, which also showed a working session as "ended".
24. **The capability signal is the echoed scope.** Every S14 hub echoes the applied `scope`, so a phone that sees none keeps its fallback: All and Live client-side, and no In sessions group.
25. **This hub's sessions only.** Search does not fan out to attached hosts today, and S14 keeps that. See question 3.

**Landing**

26. **The four PRs are independent,** each branching from main. PR 23 needs nothing from PR 22 to be correct, but the watcher that turns its note into a notice is PR 22's, so land PR 22 first. PRs 24 and 25 touch no file PRs 22 and 23 touch, except `appwire/types.go`, `appwire/protocol.go` and `types.gen.ts`, where the additions sit apart (re-run `make generate` on a conflict). PR 25 needs PR 24's `MessageSearch`.

## Questions for Jesse

Each has a recommendation; the plan is written to the recommendation, so none blocks work.

1. **What does a sign-in notice's count mean?** The plan counts this hub's live sessions whose current model runs on that provider instance: "codex-jesse-fsck.com sign-in expired · 3 sessions" means three sessions will fail on their next refresh. The alternative is counting only the sessions that already failed from it, which today the hub cannot identify (ruling 2). **Recommendation:** sessions that use it.
2. **Should a host the hub has not attached since it restarted raise a notice?** Today, after a hub restart, every configured host is offline until something connects it. The Board would show "paradise-park is offline" until you tap Details and Connect. The phone's current fallback already does this. **Recommendation:** keep it. The sessions there really are out of reach, and the notice leads to Connect. The alternative is to suppress notices for hosts never attached this run, and would need a new fact from the SSH manager.
3. **Should notices and message search reach other hosts?** Both cover this hub only, as today's search does. **Recommendation:** not in these PRs. A follow-up (call it S14c) could fan search out the way `evener/activity/read` does (`RemoteHubSource.ReadSessionActivity`). It must recompute each remote result's `archived` flag with this hub's decisions, since the controller holds the archive decisions for remote sessions. A notices fan-out would follow the same pattern. File both when you want them.
4. **Should message search cover subagent sessions?** It does (ruling 15), as the Sessions group already lists them. **Recommendation:** keep it. Finding which subagent edited a file is a real use. If results feel noisy, the phone can group a subagent hit under its coordinator later; the result would need a parent ref for that.

5. **Should a broken-plugin notice count sessions?** Spec 18 asks for affected counts on every notice. No row or probe says which plugins a session runs; the StatusOnly root row strips them. **Recommendation:** ship without a count (ruling 5). A plugin that fails validation mostly affects new sessions, since running ones loaded it at start. A count needs the daemon to report its enabled plugins on the root row, which is a small follow-up if you want it.

## Review Focus

1. **A hit that opens the wrong place.**
   - A hit whose key or position differs from the item the reader shows opens the session somewhere else, or nowhere.
   - Prelude items, tool calls between messages, continuation turns and new-format entries all move positions.
   - Pinned by `TestMessageSearchHitsAreTheItemsAThreadReadShows` (Task 24.2, against the hub's own thread read) and `TestMessageSearchKeepsUserAndAgentMessagesUnderTheirReadKeys` (Task 24.1, against the transcript index's window).
2. **A deleted session still found.** A session's words must leave search when the session is deleted, not a minute later.
   - Pinned by `TestDeletingASessionForgetsItsMessages` (Task 24.2) and `TestMessageSearchForgetsSessions` (Task 24.1: a session the past index stops listing is forgotten too).
3. **A notice that flaps.**
   - A transient read failure must not announce a sign-in resolved and then needed again.
   - A session count moving must not look like a new sign-in problem to a providers pane.
   - Pinned by `TestHubNoticesKeepAFailedKindsLastNotices` and `TestNoticeWatcherAnnouncesEachChangeOnce` (Task 22.2).
4. **A false "sign in" for a healthy login.**
   - A refresh that races another process's refresh, or a refusal followed by a refresh that worked, must not leave the instance asking for a sign-in.
   - Pinned by `TestRefreshRefusalNamesOnlyTheRefusedToken`, `TestRuntimeCredentialsRefreshClearsAnEarlierRefusal` and `TestSaveAuthClearsARefreshRefusal` (Task 23.1).
5. **A search that breaks or lies.**
   - FTS5 operators typed into the field must not error.
   - A long or multi-line message must still give a one-line snippet of bounded length.
   - An archived session must not be crowded out of the Archived scope by newer matches.
   - An explicitly unarchived session inside an archived project must count as archived, as the rail files it.
   - Pinned by `TestMessageSearchMatchesEveryWordAsAPrefix` (Task 24.1), `TestSearchSnippetCutsAroundTheFirstMatch` (Task 25.2), `TestHubSearchScopesByTheRailsArchiveRules` and `TestSessionArchivedFollowsTheRail` (Tasks 25.1 and 25.2).

---

## PRs and lanes

| PR | Item | Tasks | Production lines (dry run) | Depends on |
|---|---|---|---|---|
| 22 | S11a: the hub's notices | 22.1-22.3 | 321 | none |
| 23 | S11b: a refused refresh needs a sign-in (#2479) | 23.1-23.2 | 105 | none; land after 22 |
| 24 | S14a: the message index | 24.1-24.2 | about 600 | none |
| 25 | S14b: search scopes, archived flag and message hits | 25.1-25.2 | 322 | PR 24 |

- **Merge order.** PR 22, then PR 23. PR 24 can run beside them in its own lane, and PR 25 follows PR 24. Each branches from main once the PR it depends on has merged.
- **Where the lanes meet.** `appwire/types.go`, `appwire/protocol.go` and the generated files (PRs 22 and 25): the additions sit apart, so run `make generate` again when `types.gen.ts` conflicts. `cmd/evener-hub/main.go` (PRs 22 and 24): each adds one `startBackground` line beside the others.
- The server plan's PR map changes with this plan: PRs 22 and 23 no longer depend on PR 9, and PR 25 adds no host fan-out (question 3).

## Phone lane handoff

This lane changes `mobile-native/` only through the shared package's generated types. When each hub PR is on the hub the phone talks to, the phone switches off its fallback as follows. The fallback lives on main in `mobile-native/src/board/notices.ts`, `boardData.ts` (`readNoticeLists`) and `boardSearch.ts`, from #2634.

**S11a (after PR 22).**
- Call `evener/notices/list` when the Board binds a client. A MethodNotFound means an older hub: keep today's derivation.
- Replace the list with each `evener/notices/changed`'s `notices`.
- Stop polling `evener/plugin/list` for notices. Keep reading `evener/auth/list` only for the Providers screen.
- Sentences:
  - `signInRequired`: "<subject> sign-in expired" (spec 7.1), with " · N sessions" when `affectedSessions` is present. Action "Sign in" at provider `subject`.
  - `hostOffline`: "<subject> is offline", with " · N sessions". Action "Details" at host `subject`.
  - `pluginBroken`: "<subject> is broken", naming `marketplace` when two broken plugins share a name. Action "Plugins" at `subject` and `marketplace`.
- An in-app alert fires for a notice `id` the phone had not seen (spec 13.3), not for a count change.

**S11b (after PR 23).**
- A refused Codex refresh now reads `needsLogin: true, signedIn: false` on `evener/auth/list`, and the sign-in notice follows.
- "Expires in 3d" (phase 5's Providers row) is still not available: no refresh-token lifetime exists (Jesse's answer 2). The row shows no expiry.

**S14 (after PR 25).**
- Send `evener/search {query, scope}`.
- `response.scope` present: show the Archived chip. The Sessions group is `live` then `past`, already filtered and each session once. Show the In sessions group from `inSessions`: each row is a session (title, state mark, `archived`) with its first hit's snippet and "N matches" from `hitCount`.
- Tapping a hit opens the session with a reader anchor `{itemKey: hit.transcriptKey, itemPosition: hit.position}`, loading older pages until it resolves (phase 3's reader restore). Highlight the query's words in that item.
- Render snippet parts in order, `match` parts bold.
- `response.scope` absent: keep today's All and Live client-side, and no In sessions group.
- A query of one character returns no `inSessions` by design (ruling 19).

**Known limits.**
- A live session's newest message is searchable within a minute (ruling 18).
- Notices and message search cover this hub only (question 3).
- A turn that failed because the issuer refused a sign-in still reads "Evener configuration error" on its row (#2705).

---

## PR 22: the hub's notices, S11a (Tasks 22.1-22.3)

**Branch:** `git fetch origin && git switch -c claude/s11a-hub-notices origin/main`

**What it adds.** Probes keep each session's current provider instance; the hub derives its notices; `evener/notices/list` answers with them and `evener/notices/changed` announces each change. The phone's fallback retires as the handoff says.

### Task 22.1: Live entries carry the session's current provider instance

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/hubcore/roster.go` (`LiveEntry.Profile` after `LastTurnEndedAt`; `ProbeResult.Profile` after its `LastTurnEndedAt`; `liveEntryFromProbe`; `ReadSpawnedThread`'s result literal)
- Modify: `cmd/evener-hub/internal/hubcore/prober.go` (`Probe`'s result literal)
- Create: `cmd/evener-hub/internal/hubcore/profile_test.go`
- Modify: `cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go` (register the scenario after `fuzzScenarioStatusProber_KeepsThePendingQuestion`)

**Interfaces:**
- Produces: `LiveEntry.Profile string` and `ProbeResult.Profile string`, from the listed root's `Evener.Profile`. No row shows the profile, so `rosterFingerprint` does not hash it.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/internal/hubcore/profile_test.go`:

```go
package hubcore

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/rendezvous"
	"primeradiant.com/evener/server"
)

// fuzzScenarioStatusProber_KeepsTheRootsProfile: the probe keeps the provider
// instance the root's current model runs on, which a sign-in notice counts its
// sessions by (S11). The rendezvous entry's Provider is the one the session
// started on and goes stale after a model switch.
func fuzzScenarioStatusProber_KeepsTheRootsProfile(t *testing.T) {
	prober, entry := startProbeDaemon(t, probeDaemonConfig{
		sessionID: "th_profile",
		state:     appwire.ThreadStatusIdle,
		setup: func(srv *server.Server) {
			srv.UpdateSessionInfo("th_profile", "gpt-5.6", "codex-jesse-fsck.com")
		},
	})
	entry.Provider = "lunaroute"
	got := prober.Probe(entry)
	if !got.OK || got.Profile != "codex-jesse-fsck.com" {
		t.Fatalf("probe = %+v, want the profile codex-jesse-fsck.com", got)
	}
	if live := liveEntryFromProbe(entry, got); live.Profile != "codex-jesse-fsck.com" || live.Provider != "lunaroute" {
		t.Fatalf("live entry profile %q provider %q, want the probe's profile beside the entry's own provider", live.Profile, live.Provider)
	}
}

// A resumed session's first publication carries its profile too, so a sign-in
// notice counts it before the next scan.
func TestRosterReadSpawnedThreadPublishesTheProfile(t *testing.T) {
	r := NewRoster(t.TempDir(), nil)
	entry := rendezvous.Entry{
		PID: 1001, SourceID: "local", Protocol: appwire.ProtocolVersion,
		Endpoint: "ws://127.0.0.1:50001/rpc", ThreadID: "01SPAWNED", SessionID: "01SPAWNED",
	}
	if _, err := r.ReadSpawnedThread(t.Context(), entry, func(context.Context) (appwire.ThreadReadResponse, error) {
		return appwire.ThreadReadResponse{Thread: appwire.Thread{
			ID: "01SPAWNED", SessionID: "01SPAWNED",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener: appwire.EvenerThread{Profile: "codex-jesse-fsck.com"},
		}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	live, ok := r.Find("01SPAWNED")
	if !ok || live.Profile != "codex-jesse-fsck.com" {
		t.Fatalf("published entry = %+v, want the profile the read carried", live)
	}
}
```

In `scenarios_fuzz_test.go`, after `fuzzScenarioStatusProber_KeepsThePendingQuestion,` add `fuzzScenarioStatusProber_KeepsTheRootsProfile,`.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$|TestRosterReadSpawnedThreadPublishesTheProfile' -count=1`
Expected: FAIL to compile (`Profile` undefined on `ProbeResult` and `LiveEntry`).

- [ ] **Step 3: Implement**

`cmd/evener-hub/internal/hubcore/roster.go`:

```diff
--- a/cmd/evener-hub/internal/hubcore/roster.go
+++ b/cmd/evener-hub/internal/hubcore/roster.go
@@ -111,6 +111,12 @@ type LiveEntry struct {
 	// the hub's seen marker, so rosterFingerprint hashes it: a turn that starts
 	// and ends between two probes leaves Status unchanged and moves only this.
 	LastTurnEndedAt time.Time
+	// Profile is the provider instance the session's current model runs on,
+	// from its probe; the embedded Entry's Provider is the one it started on,
+	// and a model switch leaves that behind. A sign-in notice counts the
+	// sessions it blocks by it (S11). No row shows it, so rosterFingerprint
+	// leaves it out.
+	Profile string
 }
 
 // ProbeResult is the dynamic session state returned by a daemon liveness probe.
@@ -156,6 +162,9 @@ type ProbeResult struct {
 	// LastTurnEndedAt is when the listed root's last turn ended (S4); zero from
 	// a daemon that predates it, or before any turn has ended.
 	LastTurnEndedAt time.Time
+	// Profile mirrors LiveEntry.Profile: the provider instance of the root's
+	// current model.
+	Profile string
 	// ProtocolMismatch: the endpoint answered, but as a daemon this hub cannot
 	// talk to (restart required). Such an answer names no session of its own,
 	// so it does not vouch for the entry's PID the way a bound answer does.
@@ -1431,6 +1440,7 @@ func liveEntryFromProbe(e rendezvous.Entry, result ProbeResult) LiveEntry {
 		Activity:              result.Activity,
 		Subagents:             result.Subagents,
 		LastTurnEndedAt:       result.LastTurnEndedAt,
+		Profile:               result.Profile,
 	})
 }
 
@@ -1502,7 +1512,8 @@ func (r *Roster) ReadSpawnedThread(ctx context.Context, entry rendezvous.Entry,
 		// and every current daemon stamps its capability set on the thread
 		// projection this read answered from, so the caps beside the status
 		// are the daemon's own answer — not an approximation.
-		Capabilities: root.Evener.Capabilities, CapabilitiesKnown: true}
+		Capabilities: root.Evener.Capabilities, CapabilitiesKnown: true,
+		Profile: root.Evener.Profile}
 	if root.Evener.Diagnostics != nil {
 		result.RunningSubagentStates = make(map[string]string)
 		for _, delegate := range root.Evener.Diagnostics.Delegates {
```

`cmd/evener-hub/internal/hubcore/prober.go`:

```diff
--- a/cmd/evener-hub/internal/hubcore/prober.go
+++ b/cmd/evener-hub/internal/hubcore/prober.go
@@ -171,6 +171,7 @@ func (p *StatusProber) Probe(entry rendezvous.Entry) ProbeResult {
 		Activity:              root.Evener.Activity,
 		Subagents:             subagents,
 		LastTurnEndedAt:       UnixMilliTime(root.Evener.LastTurnEndedAt),
+		Profile:               root.Evener.Profile,
 		OK:                    true,
 	}
 }
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub/internal/hubcore -run '^FuzzHubcoreScenarios$|TestRosterReadSpawnedThread' -count=1`
Expected: PASS. Prove the scenario can fail: delete the `Profile:` line from `Probe`'s literal, watch `fuzzScenarioStatusProber_KeepsTheRootsProfile` fail with `Profile:` empty, and put it back.

Gates: `golangci-lint run ./cmd/evener-hub/internal/hubcore/`.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/internal/hubcore/roster.go cmd/evener-hub/internal/hubcore/prober.go cmd/evener-hub/internal/hubcore/profile_test.go cmd/evener-hub/internal/hubcore/scenarios_fuzz_test.go
git commit -m "feat(hub): live entries carry the session's current provider instance"
```

### Task 22.2: The hub derives its notices, and a watcher announces each change

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire/types.go` (`MethodEvenerNoticesList` after `MethodEvenerActivityRead`; `NotifyEvenerNoticesChanged` after `NotifyEvenerPluginUpdated`; the notice kinds, `HubNotice` and `NoticesListResponse` after `SessionActivity`)
- Modify: `cmd/evener-hub/web.go` (`sourceIsOnline`, split out of `sourceOnline`)
- Create: `cmd/evener-hub/app_notices.go`
- Create: `cmd/evener-hub/app_notices_test.go`

**Interfaces:**
- Consumes: `LiveEntry.Profile` (Task 22.1); `hubAuthController.List`, `hubPluginsController.ListPlugins`, `appsource.Registry.All`, `hubcore.RemoteThreadCache.Snapshot`, `appThreadTreeLive` (all on main).
- Produces:
  - `appwire.NoticeKindSignInRequired`, `NoticeKindHostOffline`, `NoticeKindPluginBroken`;
  - `appwire.HubNotice{ID, Kind, Subject, Marketplace, AffectedSessions}`;
  - `appwire.NoticesListResponse{Notices}`;
  - `appwire.MethodEvenerNoticesList = "evener/notices/list"` and `NotifyEvenerNoticesChanged = "evener/notices/changed"`;
  - `hubNotices{auth, plugins, sources, roster, remote}` with `derive(ctx) ([]appwire.HubNotice, map[string]bool)` and `read(ctx) []appwire.HubNotice`, which keeps a failed kind's last notices;
  - `registerNoticesHandler(server, notices)`;
  - `runNoticeWatcher(ctx, ticks, read, broadcaster)`;
  - `noticeInterval`;
  - `sourceIsOnline(appsource.Source) bool`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/app_notices_test.go` (the RPC round trip joins it in Task 22.3):

```go
package hub

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/plugins"
)

// The notices name what blocks sessions (S11, spec 7.1): a provider instance
// that needs signing in again, with this hub's live sessions whose current
// model runs on it; a host that is offline, with its sessions that were live
// when last reached; and a broken plugin, with no count. Sign-ins come first,
// then hosts, then plugins, as the phone lists them.
func TestHubNoticesNameWhatBlocksSessions(t *testing.T) {
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 1, SessionID: "s1", Status: appwire.ThreadStatusIdle, Profile: "codex-jesse-fsck.com"},
		hubcore.LiveEntry{PID: 2, SessionID: "s2", Status: appwire.ThreadStatusActive, Profile: "codex-jesse-fsck.com"},
		// A crashed session's process is gone, so it runs on nothing.
		hubcore.LiveEntry{PID: 3, SessionID: "s3", Status: appwire.ThreadStatusActive, Profile: "codex-jesse-fsck.com", Crashed: true},
		hubcore.LiveEntry{PID: 4, SessionID: "s4", Status: appwire.ThreadStatusIdle, Profile: "lunaroute"},
	)
	sources := appsource.NewRegistry()
	sources.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "paradise-park"}, online: false})
	sources.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "m4"}, online: true})
	remote := &hubcore.RemoteThreadCache{}
	remote.Store([]appwire.Thread{
		remoteNoticeThread("paradise-park", "p1", appwire.ThreadStatusIdle, ""),
		remoteNoticeThread("paradise-park", "p2", appwire.ThreadStatusActive, ""),
		// A subagent is summarized on its coordinator; an ended session is not
		// blocked by anything.
		remoteNoticeThread("paradise-park", "p3", appwire.ThreadStatusActive, "subagent"),
		remoteNoticeThread("paradise-park", "p4", appwire.ThreadStatusClosed, ""),
		remoteNoticeThread("m4", "m1", appwire.ThreadStatusActive, ""),
	})
	notices := &hubNotices{
		auth: func() (appwire.AuthListResponse, error) {
			return appwire.AuthListResponse{Providers: []appwire.AuthStatusResponse{
				{Provider: "lunaroute", SignedIn: true},
				{Provider: "codex-jesse-fsck.com", NeedsLogin: true},
			}}, nil
		},
		plugins: func(context.Context) (appwire.PluginListResponse, error) {
			return appwire.PluginListResponse{Plugins: []appwire.PluginEntry{
				{Plugin: "go", Marketplace: "acme"},
				{Plugin: "superpowers", Marketplace: "obra", Broken: true},
			}}, nil
		},
		sources: sources,
		roster:  roster,
		remote:  remote,
	}

	got, failed := notices.derive(context.Background())
	want := []appwire.HubNotice{
		{ID: "signInRequired:codex-jesse-fsck.com", Kind: appwire.NoticeKindSignInRequired, Subject: "codex-jesse-fsck.com", AffectedSessions: 2},
		{ID: "hostOffline:paradise-park", Kind: appwire.NoticeKindHostOffline, Subject: "paradise-park", AffectedSessions: 2},
		{ID: "pluginBroken:superpowers@obra", Kind: appwire.NoticeKindPluginBroken, Subject: "superpowers", Marketplace: "obra"},
	}
	if !reflect.DeepEqual(got, want) || len(failed) != 0 {
		t.Fatalf("notices = %+v (failed %v), want %+v", got, failed, want)
	}
}

func remoteNoticeThread(source, id, status, kind string) appwire.Thread {
	return appwire.Thread{
		ID: id, SessionID: id, Source: source,
		Status: appwire.ThreadStatus{Type: status},
		Evener: appwire.EvenerThread{Ref: source + ":" + id, Kind: kind},
	}
}

// With nothing wrong there are no notices, and the list is empty rather than
// null, so a client can render it as it comes.
func TestHubNoticesAreEmptyWhenNothingIsWrong(t *testing.T) {
	got, _ := (&hubNotices{}).derive(context.Background())
	if got == nil || len(got) != 0 {
		t.Fatalf("notices = %#v, want an empty list", got)
	}
}

// The sign-in and plugin notices come from the controllers that answer
// evener/auth/list and evener/plugin/list, so a notice and the screen its
// action opens always agree: an OAuth record whose access token expired with
// no usable refresh token needs signing in again (#2483), and a plugin whose
// install directory is gone is broken.
func TestHubNoticesReadTheAuthAndPluginControllers(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	auth := newHubAuthController(map[string]string{"OPENAI_API_KEY": ""})
	auth.stateDir = t.TempDir()
	attachTestRegistry(t, auth)
	auth.now = func() time.Time { return now }
	if err := authopenai.SaveAuth(auth.stateDir, "openai-codex", authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: now.Add(-2 * time.Hour), TokenType: "Bearer", AccessToken: "stored-access-token",
		RefreshToken: "   ", Expiry: now.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	pluginRoot := t.TempDir()
	if err := plugins.SaveRegistry(filepath.Join(pluginRoot, "installed_plugins.json"), plugins.Registry{
		Plugins: map[string][]plugins.InstallEntry{
			"superpowers@obra": {{
				InstallPath: filepath.Join(pluginRoot, "gone"), Version: "1.0.0", Enabled: true,
				Source: plugins.Source{Kind: plugins.SourceDirectory, Path: filepath.Join(pluginRoot, "gone")},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	pluginsController := &hubPluginsController{mgr: plugins.NewManager(pluginRoot)}
	notices := &hubNotices{
		auth:    func() (appwire.AuthListResponse, error) { return auth.List(appwire.EmptyParams{}) },
		plugins: pluginsController.ListPlugins,
	}

	got, failed := notices.derive(context.Background())
	want := []appwire.HubNotice{
		{ID: "signInRequired:openai-codex", Kind: appwire.NoticeKindSignInRequired, Subject: "openai-codex"},
		{ID: "pluginBroken:superpowers@obra", Kind: appwire.NoticeKindPluginBroken, Subject: "superpowers", Marketplace: "obra"},
	}
	if !reflect.DeepEqual(got, want) || len(failed) != 0 {
		t.Fatalf("notices = %+v (failed %v), want %+v", got, failed, want)
	}
}

// The watcher announces each change once, with the whole new list: a notice
// appearing, its count moving, and it clearing. A tick that finds the same
// notices announces nothing. Only a change in which providers need signing in
// also broadcasts the no-data evener/auth/updated, so a providers pane
// refreshes; a count moving does not.
func TestNoticeWatcherAnnouncesEachChangeOnce(t *testing.T) {
	signIn := func(count int) appwire.HubNotice {
		return appwire.HubNotice{ID: "signInRequired:codex", Kind: appwire.NoticeKindSignInRequired, Subject: "codex", AffectedSessions: count}
	}
	steps := [][]appwire.HubNotice{
		{},          // the baseline, taken before the first tick
		{},          // unchanged: nothing announced
		{signIn(1)}, // a sign-in needed: auth updated, then notices
		{signIn(1)}, // unchanged
		{signIn(3)}, // the count moved: notices only
		{},          // resolved: auth updated, then notices
	}
	derived := make(chan []appwire.HubNotice, len(steps))
	for _, step := range steps {
		derived <- step
	}
	read := func(context.Context) []appwire.HubNotice { return <-derived }
	broadcaster := newRecordingBroadcaster()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runNoticeWatcher(ctx, ticks, read, broadcaster)
		close(done)
	}()
	for range steps[1:] {
		ticks <- time.Time{}
	}
	// The watcher handles a tick before it can see the cancel, and done
	// closes only once it has returned, so every broadcast is in by then.
	cancel()
	<-done

	want := []recordedBroadcast{
		{method: appwire.NotifyEvenerAuthUpdated, params: appwire.EvenerAuthUpdatedParams{}},
		{method: appwire.NotifyEvenerNoticesChanged, params: appwire.NoticesListResponse{Notices: []appwire.HubNotice{signIn(1)}}},
		{method: appwire.NotifyEvenerNoticesChanged, params: appwire.NoticesListResponse{Notices: []appwire.HubNotice{signIn(3)}}},
		{method: appwire.NotifyEvenerAuthUpdated, params: appwire.EvenerAuthUpdatedParams{}},
		{method: appwire.NotifyEvenerNoticesChanged, params: appwire.NoticesListResponse{Notices: []appwire.HubNotice{}}},
	}
	if got := broadcaster.broadcasts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("broadcasts = %+v, want %+v", got, want)
	}
}

// A read that fails is no news: the kind whose read failed keeps the notices
// the last read gave, for evener/notices/list and the watcher alike, so a
// transient failure never reports a problem resolved and then new again.
func TestHubNoticesKeepAFailedKindsLastNotices(t *testing.T) {
	sources := appsource.NewRegistry()
	host := &offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "paradise-park"}, online: true}
	sources.Add(host)
	var authErr error
	notices := &hubNotices{
		auth: func() (appwire.AuthListResponse, error) {
			return appwire.AuthListResponse{Providers: []appwire.AuthStatusResponse{{Provider: "codex", NeedsLogin: true}}}, authErr
		},
		sources: sources,
	}
	signIn := appwire.HubNotice{ID: "signInRequired:codex", Kind: appwire.NoticeKindSignInRequired, Subject: "codex"}
	offline := appwire.HubNotice{ID: "hostOffline:paradise-park", Kind: appwire.NoticeKindHostOffline, Subject: "paradise-park"}
	if got := notices.read(context.Background()); !reflect.DeepEqual(got, []appwire.HubNotice{signIn}) {
		t.Fatalf("first read = %+v, want the sign-in", got)
	}
	// The auth read fails and the host goes offline in the same read.
	authErr = errors.New("credentials unreadable")
	host.online = false
	if got := notices.read(context.Background()); !reflect.DeepEqual(got, []appwire.HubNotice{signIn, offline}) {
		t.Fatalf("read with a failed auth read = %+v, want the sign-in kept beside the new host notice", got)
	}
}

// A kind that fails to read is left out of evener/notices/list's answer rather
// than failing the whole read: the other kinds still show.
func TestHubNoticesLeaveOutAKindThatFailsToRead(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "paradise-park"}, online: false})
	notices := &hubNotices{
		auth: func() (appwire.AuthListResponse, error) {
			return appwire.AuthListResponse{}, errors.New("credentials unreadable")
		},
		plugins: func(context.Context) (appwire.PluginListResponse, error) {
			return appwire.PluginListResponse{}, errors.New("store unreadable")
		},
		sources: sources,
	}
	got, failed := notices.derive(context.Background())
	want := []appwire.HubNotice{{ID: "hostOffline:paradise-park", Kind: appwire.NoticeKindHostOffline, Subject: "paradise-park"}}
	wantFailed := map[string]bool{appwire.NoticeKindSignInRequired: true, appwire.NoticeKindPluginBroken: true}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(failed, wantFailed) {
		t.Fatalf("notices = %+v failed = %v, want %+v failed %v", got, failed, want, wantFailed)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestHubNotices|TestNoticeWatcher' -count=1`
Expected: FAIL to compile (`hubNotices`, `appwire.HubNotice` undefined).

- [ ] **Step 3: Implement**

`appwire/types.go`: add `MethodEvenerNoticesList              = "evener/notices/list"` after `MethodEvenerActivityRead`, and `NotifyEvenerNoticesChanged        = "evener/notices/changed"` after `NotifyEvenerPluginUpdated`. Then, after the `SessionActivity` type:

```go
// The kinds of hub notice (S11, spec 7.1).
const (
	// NoticeKindSignInRequired: a provider instance on this hub needs signing
	// in again before it can serve a request.
	NoticeKindSignInRequired = "signInRequired"
	// NoticeKindHostOffline: a host this hub manages is not attached, so its
	// sessions are out of reach.
	NoticeKindHostOffline = "hostOffline"
	// NoticeKindPluginBroken: an installed plugin fails validation.
	NoticeKindPluginBroken = "pluginBroken"
)

// HubNotice is one hub-level problem that blocks sessions (S11, spec 7.1).
type HubNotice struct {
	// ID is the notice's identity, stable while the problem lasts:
	// "<kind>:<subject>", and "<kind>:<plugin>@<marketplace>" for a plugin.
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Subject is what the notice names and what its action routes by: the
	// provider instance (an evener/auth/list row's provider), the host's source
	// ID (a manifest source's id), or the plugin's name.
	Subject string `json:"subject"`
	// Marketplace is a plugin notice's marketplace: two marketplaces can each
	// ship a plugin of the same name.
	Marketplace string `json:"marketplace,omitempty"`
	// AffectedSessions counts the live top-level sessions the problem blocks:
	// for a sign-in, this hub's live sessions whose current model runs on the
	// provider instance; for a host, the host's sessions that were live when it
	// was last reached. Absent when there are none, and on a plugin notice,
	// since no row says which plugins a session runs.
	AffectedSessions int `json:"affectedSessions,omitempty"`
}

// NoticesListResponse is evener/notices/list's answer and
// evener/notices/changed's payload: every notice the hub derives now, sign-ins
// first, then hosts, then plugins.
type NoticesListResponse struct {
	Notices []HubNotice `json:"notices"`
}
```

`cmd/evener-hub/web.go`: `sourceOnline` ends by calling a new helper, which follows it:

```go
	source, ok := s.sources.Source(id)
	if !ok || source == nil {
		return true
	}
	return sourceIsOnline(source)
}

// sourceIsOnline reports whether a registered source can serve requests now: a
// source that reports no online state (the controller's own) always can.
func sourceIsOnline(source appsource.Source) bool {
	if online, ok := source.(appsource.OnlineSource); ok {
		return online.Online()
	}
	return true
}
```

`cmd/evener-hub/app_notices.go`:

```go
package hub

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/appserver"
)

// noticeInterval is how often the hub re-derives its notices to announce a
// change it did not cause: an access token that expired with no refresh token,
// a refresh the issuer rejected, a plugin directory that went missing.
const noticeInterval = 5 * time.Second

// hubNotices derives the Board's notices (S11, spec 7.1): hub-level problems
// that block sessions. It reads the answers the phone's fallback reads, so a
// notice and the screen its action opens agree: the evener/auth/list rows, the
// navigation manifest's sources, and the evener/plugin/list rows.
type hubNotices struct {
	auth    func() (appwire.AuthListResponse, error)
	plugins func(context.Context) (appwire.PluginListResponse, error)
	sources *appsource.Registry
	roster  *hubcore.Roster
	remote  *hubcore.RemoteThreadCache

	// mu guards last, the notices read last answered with.
	mu   sync.Mutex
	last []appwire.HubNotice
}

// read is the hub's notices now, as both evener/notices/list and the watcher
// report them. A kind whose read failed keeps the notices the last read gave
// for it: a failed read is no news, so neither a client that asks during a
// transient failure nor the watcher is told a problem resolved when it was
// only unreadable.
func (n *hubNotices) read(ctx context.Context) []appwire.HubNotice {
	next, failed := n.derive(ctx)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.last = keepFailedNotices(next, n.last, failed)
	return n.last
}

// derive returns the notices the hub can read now, sign-ins first, then hosts,
// then plugins. A kind whose read failed is named in failed, so read can keep
// that kind's last notices instead of reporting them resolved.
func (n *hubNotices) derive(ctx context.Context) (notices []appwire.HubNotice, failed map[string]bool) {
	notices = []appwire.HubNotice{}
	failed = map[string]bool{}
	signIns, err := n.signInNotices()
	if err != nil {
		failed[appwire.NoticeKindSignInRequired] = true
	}
	notices = append(notices, signIns...)
	notices = append(notices, n.hostNotices()...)
	plugins, err := n.pluginNotices(ctx)
	if err != nil {
		failed[appwire.NoticeKindPluginBroken] = true
	}
	return append(notices, plugins...), failed
}

// signInNotices names every provider instance whose evener/auth/list row says
// it needs signing in again, with the live sessions whose current model runs
// on it.
func (n *hubNotices) signInNotices() ([]appwire.HubNotice, error) {
	if n.auth == nil {
		return nil, nil
	}
	list, err := n.auth()
	if err != nil {
		return nil, err
	}
	sessions := n.liveSessionsByProfile()
	var notices []appwire.HubNotice
	for _, provider := range list.Providers {
		if !provider.NeedsLogin {
			continue
		}
		notices = append(notices, appwire.HubNotice{
			ID:               appwire.NoticeKindSignInRequired + ":" + provider.Provider,
			Kind:             appwire.NoticeKindSignInRequired,
			Subject:          provider.Provider,
			AffectedSessions: sessions[provider.Provider],
		})
	}
	sort.Slice(notices, func(i, j int) bool { return notices[i].Subject < notices[j].Subject })
	return notices, nil
}

// liveSessionsByProfile counts this hub's live top-level sessions by the
// provider instance their current model runs on. A crashed session's process
// is gone, so it runs on nothing.
func (n *hubNotices) liveSessionsByProfile() map[string]int {
	counts := map[string]int{}
	if n.roster == nil {
		return counts
	}
	for _, entry := range n.roster.List() {
		if entry.Crashed || entry.SessionID == "" || entry.Profile == "" {
			continue
		}
		counts[entry.Profile]++
	}
	return counts
}

// hostNotices names every registered host that is offline, as the navigation
// manifest's sources report it, with its sessions that were live when the hub
// last reached it.
func (n *hubNotices) hostNotices() []appwire.HubNotice {
	if n.sources == nil {
		return nil
	}
	var sessions map[string]int
	var notices []appwire.HubNotice
	for _, source := range n.sources.All() {
		if source.ID() == "local" || sourceIsOnline(source) {
			continue
		}
		if sessions == nil {
			sessions = n.lastSeenLiveSessionsBySource()
		}
		notices = append(notices, appwire.HubNotice{
			ID:               appwire.NoticeKindHostOffline + ":" + source.ID(),
			Kind:             appwire.NoticeKindHostOffline,
			Subject:          source.ID(),
			AffectedSessions: sessions[source.ID()],
		})
	}
	return notices
}

// lastSeenLiveSessionsBySource counts each remote source's top-level rows that
// were live in the hub's last remote snapshot: an offline host keeps its
// last-known rows there.
func (n *hubNotices) lastSeenLiveSessionsBySource() map[string]int {
	counts := map[string]int{}
	if n.remote == nil {
		return counts
	}
	for _, thread := range n.remote.Snapshot().Threads {
		if thread.Evener.Kind == "subagent" || !appThreadTreeLive(thread) {
			continue
		}
		counts[thread.Source]++
	}
	return counts
}

// pluginNotices names every installed plugin evener/plugin/list reports
// broken, in that list's order (plugin, then marketplace).
func (n *hubNotices) pluginNotices(ctx context.Context) ([]appwire.HubNotice, error) {
	if n.plugins == nil {
		return nil, nil
	}
	list, err := n.plugins(ctx)
	if err != nil {
		return nil, err
	}
	var notices []appwire.HubNotice
	for _, plugin := range list.Plugins {
		if !plugin.Broken {
			continue
		}
		notices = append(notices, appwire.HubNotice{
			ID:          appwire.NoticeKindPluginBroken + ":" + plugin.Plugin + "@" + plugin.Marketplace,
			Kind:        appwire.NoticeKindPluginBroken,
			Subject:     plugin.Plugin,
			Marketplace: plugin.Marketplace,
		})
	}
	return notices, nil
}

func registerNoticesHandler(server *appserver.Server, notices *hubNotices) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerNoticesList, func(ctx context.Context, _ appwire.EmptyParams) (appwire.NoticesListResponse, error) {
		return appwire.NoticesListResponse{Notices: notices.read(ctx)}, nil
	})
}

// runNoticeWatcher announces every change in the hub's notices: on each tick it
// reads them again and, when they differ from the last announced set,
// broadcasts evener/notices/changed with the new list. When the sign-in
// notices change it first broadcasts the no-data evener/auth/updated, so a
// providers pane refreshes a sign-in whose state changed with no write through
// this hub.
func runNoticeWatcher(ctx context.Context, ticks <-chan time.Time, read func(context.Context) []appwire.HubNotice, broadcaster hostNotificationBroadcaster) {
	announced := read(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			next := read(ctx)
			if slices.Equal(next, announced) {
				continue
			}
			if !slices.Equal(signInSubjects(next), signInSubjects(announced)) {
				broadcaster.BroadcastAll(appwire.NotifyEvenerAuthUpdated, appwire.EvenerAuthUpdatedParams{})
			}
			announced = next
			broadcaster.BroadcastAll(appwire.NotifyEvenerNoticesChanged, appwire.NoticesListResponse{Notices: next})
		}
	}
}

// keepFailedNotices puts back, for each kind whose read failed, the notices of
// that kind the last announcement carried, in the derivation's kind order.
func keepFailedNotices(next, last []appwire.HubNotice, failed map[string]bool) []appwire.HubNotice {
	if len(failed) == 0 {
		return next
	}
	merged := []appwire.HubNotice{}
	for _, kind := range []string{appwire.NoticeKindSignInRequired, appwire.NoticeKindHostOffline, appwire.NoticeKindPluginBroken} {
		from := next
		if failed[kind] {
			from = last
		}
		for _, notice := range from {
			if notice.Kind == kind {
				merged = append(merged, notice)
			}
		}
	}
	return merged
}

// signInSubjects is the provider instances the sign-in notices name.
func signInSubjects(notices []appwire.HubNotice) []string {
	var subjects []string
	for _, notice := range notices {
		if notice.Kind == appwire.NoticeKindSignInRequired {
			subjects = append(subjects, notice.Subject)
		}
	}
	return subjects
}
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub -run 'TestHubNotices|TestNoticeWatcher|TestWebServer|SourceOnline' -count=1`
Expected: PASS, with no stderr noise.
- `TestHubNoticesReadTheAuthAndPluginControllers` uses the real auth controller and plugin manager.
- The watcher tests drive `runNoticeWatcher` with an unbuffered tick channel. The watcher handles a tick before it can see the cancel, so no sleep is needed.
- Prove `TestHubNoticesKeepAFailedKindsLastNotices` can fail: set `n.last = next` in `read` and watch the sign-in notice vanish on the failed read.

Gates: `go vet ./cmd/evener-hub/ ./appwire/`. golangci-lint waits for Task 22.3: until then `registerNoticesHandler` and `noticeInterval` have no caller, and its `unused` check would report them.

- [ ] **Step 5: Commit**

```bash
git add appwire/types.go cmd/evener-hub/web.go cmd/evener-hub/app_notices.go cmd/evener-hub/app_notices_test.go
git commit -m "feat(hub): the hub derives its notices"
```

### Task 22.3: evener/notices/list and evener/notices/changed

**Implementer:** Sonnet.

**Files:**
- Modify: `appwire/protocol.go` (a method row after `MethodEvenerActivityRead`'s; a notification row after `NotifyEvenerPluginUpdated`'s)
- Modify: `appwire/protocol_test.go` (`TestThreadNotificationsRequireAuthoritativeRoutingIdentity`'s `global` set)
- Modify: `cmd/evener-hub/app_rpc.go` (`newHubAppServerWithNavigation`, `newHubAppServerWithNavigationAndTrace`: build and register the notices, return them)
- Modify: `cmd/evener-hub/web.go` (`WebServer.notices`, set in `newWebServer`)
- Modify: `cmd/evener-hub/main_background.go` (`watchHubNotices` before `seedHubMarketplaces`)
- Modify: `cmd/evener-hub/main.go` (start it after the attention watcher)
- Modify: `cmd/evener-hub/app_notices_test.go` (the RPC round trip), `cmd/evener-hub/app_host_admin_test.go` (policy row), `cmd/evener-hub/app_rpc_test.go` (expected handler), `cmd/evener-tui/hub_notification_coverage_test.go` (ignored notification)
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Consumes: everything Task 22.2 produces.
- Produces: `newHubAppServerWithNavigationAndTrace` returning `(*appserver.Server, *hubHostAdminController, *hubHostManager, *hubNotices)`; `WebServer.notices`; `watchHubNotices(ctx, web)`; TypeScript `HubNotice`, `NoticesListResponse`, and the method and notification in `MethodTypes`, `NotificationTypes`, `METHOD_NAMES` and `NOTIFICATION_NAMES`.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/evener-hub/app_notices_test.go`:

```go
// evener/notices/list answers over the wire with every notice the hub derives
// now.
func TestHubRPCNoticesListRoundTrip(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&offlineStubSource{scriptedAppSource: &scriptedAppSource{id: "paradise-park"}, online: false})
	server := newHubAppServer(hubcore.WebConfig{}, sources)
	response, err := server.Router().Dispatch(context.Background(), appwire.Request{Method: appwire.MethodEvenerNoticesList})
	if err != nil {
		t.Fatalf("evener/notices/list: %v", err)
	}
	got, ok := response.(appwire.NoticesListResponse)
	want := []appwire.HubNotice{{ID: "hostOffline:paradise-park", Kind: appwire.NoticeKindHostOffline, Subject: "paradise-park"}}
	if !ok || !reflect.DeepEqual(got.Notices, want) {
		t.Fatalf("evener/notices/list = %#v, want %+v", response, want)
	}
}
```

In `app_rpc_test.go`'s `TestHubRPCRegistersExpectedHandlerSet`, add `appwire.MethodEvenerNoticesList,` after `appwire.MethodEvenerActivityRead,`.

In `app_host_admin_test.go`'s policy, after the `"evener/navigation/read"` row, add `"evener/notices/list":                     false,`. Aligned with its neighbours, and with no trailing comment, so gofmt moves no other row.

In `cmd/evener-tui/hub_notification_coverage_test.go`, at the end of `notifyMethodsDeliberatelyIgnored`:

```go
	// Hub notices (S11) are the phone's and web's Board rows. The TUI shows no
	// notices; its provider and host screens read their own status.
	appwire.NotifyEvenerNoticesChanged,
```

In `appwire/protocol_test.go`'s `global` set, after `NotifyEvenerHostNotification: true,`:

```go
		// evener/notices/changed is hub-wide: the hub's own notices (S11),
		// about providers, hosts and plugins rather than any one thread.
		NotifyEvenerNoticesChanged: true,
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestHubRPCNoticesListRoundTrip|TestHubRPCRegistersExpectedHandlerSet|TestHubRouterMatchesCatalog|TestHostAdminAllowListMatchesCatalog' -count=1`
Expected: FAIL. The round trip is refused with method not found, and the catalog and handler-set checks name `evener/notices/list`.

- [ ] **Step 3: Implement**

`appwire/protocol.go`, after the `MethodEvenerActivityRead` row:

```go
	{MethodEvenerNoticesList, EmptyParams{}, NoticesListResponse{}, ScopeHub, "Lists the hub's notices (S11): provider instances on this hub that need signing in again, hosts that are offline, and installed plugins that are broken, each with the live sessions it blocks when the hub can count them. evener/notices/changed announces every change."},
```

and after the `NotifyEvenerPluginUpdated` row:

```go
	{NotifyEvenerNoticesChanged, NoticesListResponse{}, "Hub-derived: the hub's notices changed (a notice appeared, cleared, or its session count moved); carries the whole new list, as evener/notices/list returns it. Hub-originated; never sent by daemons."},
```

`cmd/evener-hub/app_rpc.go`:

```diff
--- a/cmd/evener-hub/app_rpc.go
+++ b/cmd/evener-hub/app_rpc.go
@@ -994,7 +994,7 @@ func newHubAppServer(cfg hubcore.WebConfig, sources *appsource.Registry) *appser
 }
 
 func newHubAppServerWithNavigation(cfg hubcore.WebConfig, sources *appsource.Registry, navigation *NavigationService, resolve topLevelSessionResolver) *appserver.Server {
-	server, _, _ := newHubAppServerWithNavigationAndTrace(cfg, sources, navigation, resolve, nil)
+	server, _, _, _ := newHubAppServerWithNavigationAndTrace(cfg, sources, navigation, resolve, nil)
 	return server
 }
 
@@ -1006,7 +1006,7 @@ func newHubAppServerWithNavigation(cfg hubcore.WebConfig, sources *appsource.Reg
 // server directly, for a caller that never builds through newWebServer
 // (most tests, and any embedder calling this constructor's exported
 // wrappers directly).
-func newHubAppServerWithNavigationAndTrace(cfg hubcore.WebConfig, sources *appsource.Registry, navigation *NavigationService, resolve topLevelSessionResolver, appwireTrace *appserver.WebSocketTrace) (*appserver.Server, *hubHostAdminController, *hubHostManager) {
+func newHubAppServerWithNavigationAndTrace(cfg hubcore.WebConfig, sources *appsource.Registry, navigation *NavigationService, resolve topLevelSessionResolver, appwireTrace *appserver.WebSocketTrace) (*appserver.Server, *hubHostAdminController, *hubHostManager, *hubNotices) {
 	// One fallback registry when cfg carries no live one, built once here so
 	// every host surface below — attach, management, and the admin proxy —
 	// validates against the same instance: a host added at runtime must be
@@ -1184,6 +1184,16 @@ func newHubAppServerWithNavigationAndTrace(cfg hubcore.WebConfig, sources *appso
 	registerNavigationReadHandler(server, navigation)
 	registerFavoriteHandler(server, cfg, navigation)
 	registerActivityReadHandler(server, cfg, sources)
+	// The notices read the same answers evener/auth/list and evener/plugin/list
+	// give, through the controllers those methods use (S11).
+	notices := &hubNotices{
+		auth:    func() (appwire.AuthListResponse, error) { return authController.List(appwire.EmptyParams{}) },
+		plugins: pluginsController.ListPlugins,
+		sources: sources,
+		roster:  cfg.Roster,
+		remote:  cfg.RemoteThreadCache,
+	}
+	registerNoticesHandler(server, notices)
 	registerArchiveHandler(server, cfg, sources, func() *NavigationService { return navigation })
 	registerDaemonHandlers(server, cfg, sources)
 	registerSessionDeleteHandler(server, nil)
@@ -1247,7 +1257,7 @@ func newHubAppServerWithNavigationAndTrace(cfg hubcore.WebConfig, sources *appso
 	// push) instead of a nil store one of them dereferences.
 	credsStore, credsErr := authController.credentialStore()
 	hostAdmin := registerHostAdminHandlers(server.Lifetime(), server, cfg.RemoteHostRegistry, sources, credsStore, credsErr)
-	return server, hostAdmin, hostManage
+	return server, hostAdmin, hostManage, notices
 }
 
 func normalizedAdmissionRef(params appwire.ThreadReadParams) string {
```

`cmd/evener-hub/web.go`: after the `hostManage *hubHostManager` field,

```go
	// notices derives the Board's notices (S11); main.go runs its watcher.
	notices *hubNotices
```

and in `newWebServer`:

```go
	server, hostAdmin, hostManage, notices := newHubAppServerWithNavigationAndTrace(web.cfg, sources, web.navigation, web.resolveTopLevelSessionRef, appwireTrace)
	web.appRPC = server
	web.hostAdmin = hostAdmin
	web.hostManage = hostManage
	web.notices = notices
```

`cmd/evener-hub/main_background.go`, before `seedHubMarketplaces`:

```go
// watchHubNotices announces the hub's notice changes (S11) until ctx ends.
func watchHubNotices(ctx context.Context, web *WebServer) {
	if ctx.Err() != nil {
		return
	}
	ticks, stop := hubTicker(noticeInterval)
	defer stop()
	runNoticeWatcher(ctx, ticks, web.notices.read, web.appRPC)
}
```

`cmd/evener-hub/main.go`, after `startBackground(func() { watchHubAttention(ctx, attentionPoke, archive, past, roster, web) })`:

```go
	// Notices watcher: re-derives the hub's notices every few seconds and
	// broadcasts evener/notices/changed when they change (S11).
	startBackground(func() { watchHubNotices(ctx, web) })
```

Run `$(go env GOROOT)/bin/gofmt -w` on every touched Go file, then `make generate`.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run:
- `go test ./cmd/evener-hub -run 'TestHubNotices|TestNoticeWatcher|TestHubRPCNoticesListRoundTrip|TestHubRPCRegistersExpectedHandlerSet|TestHubRouterMatchesCatalog|TestHostAdminAllowListMatchesCatalog' -count=1`
- `go test ./appwire -count=1`
- `go test ./cmd/evener-tui -run 'Notification' -count=1`
- `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`
- `cd cmd/evener-hub/frontend && npm run typecheck && npx vitest run ../../../appwire-client/typescript/reducer.test.ts ../../../appwire-client/typescript/testing/fakeClient.test.ts`

Expected: PASS. `types.gen.ts` gains `HubNotice`, `NoticesListResponse`, `"evener/notices/list"` and `"evener/notices/changed"`. The reducer suite feeds every notification name through the reducer, and the new one is ignored there.

Gates, root module:
- `go vet ./...`, `go vet -tags evenerfuzz ./...` and `GOOS=windows go vet -tags evenerfuzz ./...`;
- `golangci-lint run ./appwire/ ./cmd/evener-hub/ ./cmd/evener-hub/internal/hubcore/ ./cmd/evener-tui/`;
- `make lint-generated`.

- [ ] **Step 5: Commit and open PR 22**

```bash
git add appwire/protocol.go appwire/protocol_test.go cmd/evener-hub/app_rpc.go cmd/evener-hub/web.go cmd/evener-hub/main.go cmd/evener-hub/main_background.go cmd/evener-hub/app_notices_test.go cmd/evener-hub/app_host_admin_test.go cmd/evener-hub/app_rpc_test.go cmd/evener-tui/hub_notification_coverage_test.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): evener/notices/list and evener/notices/changed"
```

Title "feat(hub): the Board's notices from the hub (S11a, phase 7 PR 22)". The body names the three kinds and their counts (rulings 3 to 5), the watcher's five-second re-derivation and its no-data `evener/auth/updated`, and that the phone's fallback retires as the handoff says.

---

## PR 23: a refused refresh needs a sign-in, S11b (Tasks 23.1-23.2)

**Branch:** `git fetch origin && git switch -c claude/s11b-refused-refresh origin/main`, after PR 22 merges. The body says "Closes #2479".

### Task 23.1: The auth package notes a refused refresh token

**Implementer:** Sonnet.

**Files:**
- Create: `auth/openai/refresh_rejection.go`
- Create: `auth/openai/refresh_rejection_test.go`
- Modify: `auth/openai/storage.go` (`SaveAuth` clears the note after writing; `DeleteAuth` clears it)
- Modify: `auth/openai/service.go` (`ResolveRuntimeCredentials` notes a permanent refusal; `statusFromRecord` takes the state dir and instance and reads the note; `AuthRecord.NeedsLogin`'s comment)
- Modify: `auth/openai/cov_au_service_test.go` (`statusFromRecord`'s new arguments)

**Interfaces:**
- Produces: `RecordRefreshRejection(stateDir, instanceName, refreshToken string, at time.Time) error`, `RefreshRejected(stateDir, instanceName string, record AuthRecord) bool`, the unexported `clearRefreshRejection` and `refreshRejectionPath`, and `(*Service).statusFromRecord(stateDir, instanceName string, record AuthRecord) AuthStatus`.

- [ ] **Step 1: Write the failing tests**

`auth/openai/refresh_rejection_test.go`:

```go
package openai

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A refresh token the issuer refuses for good is noted where status reads it,
// so status says to sign in again (#2479). The auth record itself is left
// byte for byte as it was.
func TestRuntimeCredentialsNotesAPermanentRefreshRefusal(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	record := sampleAuthRecord()
	record.Expiry = now.Add(time.Minute)
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(AuthFilePath(stateDir, "openai"))
	if err != nil {
		t.Fatal(err)
	}
	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		return TokenSet{}, errors.New("token endpoint returned status 400: invalid_grant")
	}

	if _, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai"); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials error = %v, want ErrLoginRequired", err)
	}
	status, err := svc.Status(stateDir, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if !status.NeedsLogin {
		t.Fatalf("status = %+v, want NeedsLogin after the issuer refused the refresh token", status)
	}
	after, err := os.ReadFile(AuthFilePath(stateDir, "openai"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("noting the refusal rewrote the auth record")
	}
}

// A refresh that fails for a reason that may pass (a 503) notes nothing, so
// status keeps saying the sign-in is fine.
func TestRuntimeCredentialsNotesNoRefusalForATransientFailure(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	record := sampleAuthRecord()
	record.Expiry = now.Add(time.Minute)
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		return TokenSet{}, errors.New("token endpoint returned status 503")
	}

	if _, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai"); err == nil || errors.Is(err, ErrLoginRequired) {
		t.Fatalf("ResolveRuntimeCredentials error = %v, want a transient failure", err)
	}
	if RefreshRejected(stateDir, "openai", record) {
		t.Fatal("a transient refresh failure was noted as a refusal")
	}
}

// The note names the refused token. When another process refreshed the same
// record first and saved the token that replaced it, the note says nothing
// about the record now on disk, so status never cries sign-in for a healthy
// login.
func TestRefreshRefusalNamesOnlyTheRefusedToken(t *testing.T) {
	stateDir := t.TempDir()
	refused := sampleAuthRecord()
	if err := RecordRefreshRejection(stateDir, "openai", refused.RefreshToken, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !RefreshRejected(stateDir, "openai", refused) {
		t.Fatal("the refused token is not reported refused")
	}
	rotated := refused
	rotated.RefreshToken = "rotated-refresh-token"
	if RefreshRejected(stateDir, "openai", rotated) {
		t.Fatal("a record holding another token is reported refused")
	}
	if RefreshRejected(stateDir, "work", refused) {
		t.Fatal("another instance is reported refused")
	}
}

// Saving a record clears the note: a login, or a refresh that worked, even
// from an issuer that keeps the same refresh token.
func TestSaveAuthClearsARefreshRefusal(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleAuthRecord()
	if err := RecordRefreshRejection(stateDir, "openai", record.RefreshToken, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatal(err)
	}
	if RefreshRejected(stateDir, "openai", record) {
		t.Fatal("the note outlived a save of the same record")
	}
}

// A refresh that works after a refusal clears the note, so a refusal the
// issuer later takes back does not leave the instance asking to sign in.
func TestRuntimeCredentialsRefreshClearsAnEarlierRefusal(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	record := sampleAuthRecord()
	record.Expiry = now.Add(time.Minute)
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatal(err)
	}
	if err := RecordRefreshRejection(stateDir, "openai", record.RefreshToken, now); err != nil {
		t.Fatal(err)
	}
	svc := newTestService(now)
	svc.refreshToken = func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error) {
		// An issuer that does not rotate: no new refresh token.
		return TokenSet{AccessToken: "fresh-access-token", TokenType: "Bearer", Expiry: now.Add(time.Hour)}, nil
	}

	if _, err := svc.ResolveRuntimeCredentials(context.Background(), stateDir, "openai"); err != nil {
		t.Fatal(err)
	}
	status, err := svc.Status(stateDir, "openai")
	if err != nil {
		t.Fatal(err)
	}
	if status.NeedsLogin {
		t.Fatalf("status = %+v, want no NeedsLogin once a refresh worked", status)
	}
}

// Signing out clears the note with the record.
func TestDeleteAuthClearsARefreshRefusal(t *testing.T) {
	stateDir := t.TempDir()
	record := sampleAuthRecord()
	if err := SaveAuth(stateDir, "openai", record); err != nil {
		t.Fatal(err)
	}
	if err := RecordRefreshRejection(stateDir, "openai", record.RefreshToken, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := DeleteAuth(stateDir, "openai"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(refreshRejectionPath(stateDir, "openai")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the note survived signing out: %v", err)
	}
}

// The note is private like the record, never carries the token, and can never
// be read as an instance's auth record: its name does not end in ".json".
func TestRefreshRefusalNoteIsPrivateAndNotARecord(t *testing.T) {
	stateDir := t.TempDir()
	if err := RecordRefreshRejection(stateDir, "openai", "secret-refresh-token", time.Now()); err != nil {
		t.Fatal(err)
	}
	path := refreshRejectionPath(stateDir, "openai")
	if filepath.Ext(path) == ".json" {
		t.Fatalf("note %s ends in .json", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows records no permission bits (TestAuthStorageSaveUsesOwnerOnlyPermissions).
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("note permissions = %#o, want %#o", got, 0o600)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret-refresh-token") {
		t.Fatal("the note carries the refresh token itself")
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./auth/openai -run 'Refus' -count=1`
Expected: FAIL to compile (`RecordRefreshRejection`, `RefreshRejected`, `refreshRejectionPath` undefined).

- [ ] **Step 3: Implement**

`auth/openai/refresh_rejection.go`:

```go
package openai

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// refreshRejectionSuffix names the note beside an instance's auth record that
// says the issuer permanently refused the record's refresh token (#2479). It
// does not end in ".json", so it can never be another instance's record.
const refreshRejectionSuffix = ".json.refresh-rejected"

// refreshRejection is the note's content: which refresh token the issuer
// refused, by its SHA-256 (never the token itself), and when.
type refreshRejection struct {
	RefreshTokenSHA256 string    `json:"refresh_token_sha256"`
	RejectedAt         time.Time `json:"rejected_at"`
}

func refreshRejectionPath(stateDir, instanceName string) string {
	return filepath.Join(stateDir, authDirName, filepath.Base(instanceName)+refreshRejectionSuffix)
}

func refreshTokenDigest(refreshToken string) string {
	sum := sha256.Sum256([]byte(refreshToken))
	return hex.EncodeToString(sum[:])
}

// RecordRefreshRejection notes that the issuer permanently refused
// refreshToken for instanceName, so status can say signing in again is needed
// (#2479). It never rewrites the auth record: another process refreshing the
// same record may be saving the token that replaced this one, and rewriting
// the record could put the refused token back over it. The note names the
// refused token, so it stops mattering once the record holds another.
func RecordRefreshRejection(stateDir, instanceName, refreshToken string, at time.Time) error {
	data, err := json.Marshal(refreshRejection{RefreshTokenSHA256: refreshTokenDigest(refreshToken), RejectedAt: at.UTC()})
	if err != nil {
		return fmt.Errorf("marshal refresh rejection: %w", err)
	}
	path := refreshRejectionPath(stateDir, instanceName)
	if err := authMkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create auth directory: %w", err)
	}
	return WriteAuthFile(path, append(data, '\n'))
}

// RefreshRejected reports whether the issuer permanently refused record's
// refresh token, as RecordRefreshRejection noted it. No note, an unreadable
// one, or a note about any other token is false.
func RefreshRejected(stateDir, instanceName string, record AuthRecord) bool {
	data, err := os.ReadFile(refreshRejectionPath(stateDir, instanceName))
	if err != nil {
		return false
	}
	var rejection refreshRejection
	if err := json.Unmarshal(data, &rejection); err != nil {
		return false
	}
	return rejection.RefreshTokenSHA256 == refreshTokenDigest(record.RefreshToken)
}

// clearRefreshRejection removes instanceName's note, if there is one. SaveAuth
// and DeleteAuth call it best effort: a note left behind names a token that
// only an issuer that never rotates refresh tokens keeps on the record, and
// the next save clears it again.
func clearRefreshRejection(stateDir, instanceName string) error {
	if err := authRemove(refreshRejectionPath(stateDir, instanceName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear refresh rejection: %w", err)
	}
	return nil
}
```

`auth/openai/storage.go`:

```diff
--- a/auth/openai/storage.go
+++ b/auth/openai/storage.go
@@ -138,7 +138,13 @@ func SaveAuth(stateDir, instanceName string, record AuthRecord) error {
 	}
 	data = append(data, '\n')
 
-	return WriteAuthFile(path, data)
+	if err := WriteAuthFile(path, data); err != nil {
+		return err
+	}
+	// A saved record is a login or a refresh that worked, so a refusal noted
+	// for the record it replaced no longer applies (#2479).
+	_ = clearRefreshRejection(stateDir, instanceName)
+	return nil
 }
 
 // WriteAuthFile writes data to path as an auth file, replacing whatever was
@@ -185,6 +191,8 @@ func WriteAuthFile(path string, data []byte) error {
 // (false, nil).
 func DeleteAuth(stateDir, instanceName string) (bool, error) {
 	path := AuthFilePath(stateDir, instanceName)
+	// A signed-out instance has no refresh token for a refusal to be about.
+	_ = clearRefreshRejection(stateDir, instanceName)
 	if err := os.Remove(path); err != nil {
 		if errors.Is(err, os.ErrNotExist) {
 			return false, nil
```

`auth/openai/service.go`:

```diff
--- a/auth/openai/service.go
+++ b/auth/openai/service.go
@@ -270,7 +270,7 @@ func (s *Service) Login(ctx context.Context, stateDir, instanceName string) (Aut
 	if err := SaveAuth(stateDir, instanceName, record); err != nil {
 		return AuthStatus{}, err
 	}
-	return s.statusFromRecord(record), nil
+	return s.statusFromRecord(stateDir, instanceName, record), nil
 }
 
 // LoginWithDevice runs the OpenAI device-code flow. It is the headless
@@ -340,7 +340,7 @@ func (s *Service) LoginWithDevice(ctx context.Context, stateDir, instanceName st
 		if s.notifyConcurrentLogin != nil {
 			s.notifyConcurrentLogin()
 		}
-		return s.statusFromRecord(record), nil
+		return s.statusFromRecord(stateDir, instanceName, record), nil
 	}
 
 	if pollErr != nil {
@@ -360,7 +360,7 @@ func (s *Service) LoginWithDevice(ctx context.Context, stateDir, instanceName st
 	if err := SaveAuth(stateDir, instanceName, record); err != nil {
 		return AuthStatus{}, err
 	}
-	return s.statusFromRecord(record), nil
+	return s.statusFromRecord(stateDir, instanceName, record), nil
 }
 
 // watchForConcurrentLogin polls the auth state file every
@@ -416,7 +416,7 @@ func (s *Service) Status(stateDir, instanceName string) (AuthStatus, error) {
 	record, err := LoadAuth(stateDir, instanceName)
 	switch {
 	case err == nil:
-		return s.statusFromRecord(record), nil
+		return s.statusFromRecord(stateDir, instanceName, record), nil
 	case errors.Is(err, ErrAuthNotFound):
 		// fall through to env fallback below
 	default:
@@ -484,6 +484,10 @@ func (s *Service) ResolveRuntimeCredentials(ctx context.Context, stateDir, insta
 	})
 	if err != nil {
 		if isPermanentRefreshError(err) {
+			// Note the refusal where status can read it (#2479). Best effort:
+			// the turn fails with ErrLoginRequired either way, and the next
+			// attempt refreshes again rather than trusting the note.
+			_ = RecordRefreshRejection(stateDir, instanceName, record.RefreshToken, s.now())
 			return RuntimeCredentials{}, loginRequiredError(err)
 		}
 		return RuntimeCredentials{}, fmt.Errorf("refresh OpenAI auth: %w", err)
@@ -508,15 +512,14 @@ func (s *Service) ResolveRuntimeCredentials(ctx context.Context, stateDir, insta
 // access token has expired (a non-zero expiry at or before now) and it has no
 // refresh token. An expired access token backed by a refresh token is routine,
 // since ResolveRuntimeCredentials refreshes it on the next use (issue #2468).
-// A refresh token the issuer has permanently rejected still counts as usable
-// here, because nothing records that rejection on the stored record
-// (issue #2479).
+// A refresh token the issuer permanently refused is noted beside the record,
+// not on it, so status also asks RefreshRejected (issue #2479).
 func (r AuthRecord) NeedsLogin(now time.Time) bool {
 	expired := !r.Expiry.IsZero() && !r.Expiry.After(now)
 	return expired && strings.TrimSpace(r.RefreshToken) == ""
 }
 
-func (s *Service) statusFromRecord(record AuthRecord) AuthStatus {
+func (s *Service) statusFromRecord(stateDir, instanceName string, record AuthRecord) AuthStatus {
 	now := s.now()
 	return AuthStatus{
 		SignedIn:     true,
@@ -526,7 +529,7 @@ func (s *Service) statusFromRecord(record AuthRecord) AuthStatus {
 		WorkspaceID:  record.WorkspaceID,
 		Expiry:       record.Expiry,
 		NeedsRefresh: needsRefresh(now, record.Expiry),
-		NeedsLogin:   record.NeedsLogin(now),
+		NeedsLogin:   record.NeedsLogin(now) || RefreshRejected(stateDir, instanceName, record),
 	}
 }
 
```

`service.go`'s four `s.statusFromRecord(record)` calls, in `Login`, `LoginWithDevice` (twice) and `Status`, become `s.statusFromRecord(stateDir, instanceName, record)`. Each is inside a method with those two parameters. In `cov_au_service_test.go`, `svc.statusFromRecord(record)` becomes `svc.statusFromRecord(t.TempDir(), "openai", record)`.

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./auth/openai -count=1`
Expected: PASS, the existing refresh and status tests unchanged. Prove the tests can fail:
- delete the `RecordRefreshRejection` call in `ResolveRuntimeCredentials`: `TestRuntimeCredentialsNotesAPermanentRefreshRefusal` fails;
- delete the clear in `SaveAuth`: `TestSaveAuthClearsARefreshRefusal` and `TestRuntimeCredentialsRefreshClearsAnEarlierRefusal` fail.

Gates: `go vet ./auth/... && go vet -tags evenerfuzz ./auth/... && GOOS=windows go vet -tags evenerfuzz ./auth/...`; `golangci-lint run ./auth/openai/`.

- [ ] **Step 5: Commit**

```bash
git add auth/openai/refresh_rejection.go auth/openai/refresh_rejection_test.go auth/openai/storage.go auth/openai/service.go auth/openai/cov_au_service_test.go
git commit -m "fix(auth): a refresh token the issuer refused is noted, so status asks for a sign-in (#2479)"
```

### Task 23.2: The hub reports the refusal, and the notice follows

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/app_auth.go` (`openAIStatusFromRecord` takes `refreshRejected`; `openAIInstanceStatusKeyed` passes `authopenai.RefreshRejected(c.stateDir, name, record)`)
- Modify: `cmd/evener-hub/cov_auth_instances_fuzz_test.go` (its call passes `false`)
- Modify: `cmd/evener-hub/app_auth_test.go` (`TestHubRPCAuthStatusReportsOAuthRefreshAndLoginStates` gains a refused case)
- Modify: `cmd/evener-hub/app_notices_test.go` (`TestHubNoticesNameARefusedRefresh`)

**Interfaces:**
- Consumes: `authopenai.RecordRefreshRejection` and `RefreshRejected` (Task 23.1); `hubNotices` (PR 22).
- Produces: `openAIStatusFromRecord(now time.Time, record authopenai.AuthRecord, refreshRejected bool) authopenai.AuthStatus`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/app_auth_test.go`:

```diff
--- a/cmd/evener-hub/app_auth_test.go
+++ b/cmd/evener-hub/app_auth_test.go
@@ -170,6 +170,9 @@ func TestHubRPCAuthStatusReportsOAuthRefreshAndLoginStates(t *testing.T) {
 		wantSignedIn bool
 		wantRefresh  bool
 		wantLogin    bool
+		// refused notes the issuer's permanent refusal of the stored refresh
+		// token, as a daemon's failed refresh does (#2479).
+		refused bool
 	}{
 		{
 			name:         "refreshable",
@@ -201,6 +204,17 @@ func TestHubRPCAuthStatusReportsOAuthRefreshAndLoginStates(t *testing.T) {
 			refreshToken: "   ",
 			wantLogin:    true,
 		},
+		{
+			// The access token is still good, but the issuer refused the
+			// refresh token for good the last time a daemon tried it: the
+			// session fails at its next refresh, so signing in again is
+			// needed now (#2479).
+			name:         "refresh token refused by the issuer",
+			expiry:       now.Add(time.Hour),
+			refreshToken: "stored-refresh-token",
+			refused:      true,
+			wantLogin:    true,
+		},
 	}
 
 	for _, tc := range tests {
@@ -223,6 +237,11 @@ func TestHubRPCAuthStatusReportsOAuthRefreshAndLoginStates(t *testing.T) {
 			}); err != nil {
 				t.Fatal(err)
 			}
+			if tc.refused {
+				if err := authopenai.RecordRefreshRejection(ctrl.stateDir, "openai-codex", tc.refreshToken, now); err != nil {
+					t.Fatal(err)
+				}
+			}
 
 			status, err := ctrl.Status(appwire.AuthStatusParams{Provider: "openai-codex"})
 			if err != nil {
```

Append to `cmd/evener-hub/app_notices_test.go`:

```go
// A daemon's refresh the issuer refused for good raises the sign-in notice
// (#2479): the refusal is noted beside the record, the evener/auth/list row
// reads it, and the notice names the instance while its access token is still
// good. A later sign-in, which saves a new record, clears it.
func TestHubNoticesNameARefusedRefresh(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	auth := newHubAuthController(map[string]string{"OPENAI_API_KEY": ""})
	auth.stateDir = t.TempDir()
	attachTestRegistry(t, auth)
	auth.now = func() time.Time { return now }
	record := authopenai.AuthRecord{
		Version: 1, Provider: "openai", Source: authopenai.AuthSourceOAuth,
		ObtainedAt: now.Add(-time.Hour), TokenType: "Bearer", AccessToken: "stored-access-token",
		RefreshToken: "stored-refresh-token", Expiry: now.Add(time.Hour),
	}
	if err := authopenai.SaveAuth(auth.stateDir, "openai-codex", record); err != nil {
		t.Fatal(err)
	}
	notices := &hubNotices{auth: func() (appwire.AuthListResponse, error) { return auth.List(appwire.EmptyParams{}) }}
	if got, _ := notices.derive(context.Background()); len(got) != 0 {
		t.Fatalf("notices before any refusal = %+v, want none", got)
	}

	if err := authopenai.RecordRefreshRejection(auth.stateDir, "openai-codex", record.RefreshToken, now); err != nil {
		t.Fatal(err)
	}
	want := []appwire.HubNotice{{ID: "signInRequired:openai-codex", Kind: appwire.NoticeKindSignInRequired, Subject: "openai-codex"}}
	if got, _ := notices.derive(context.Background()); !reflect.DeepEqual(got, want) {
		t.Fatalf("notices after a refusal = %+v, want %+v", got, want)
	}

	record.RefreshToken = "fresh-refresh-token"
	if err := authopenai.SaveAuth(auth.stateDir, "openai-codex", record); err != nil {
		t.Fatal(err)
	}
	if got, _ := notices.derive(context.Background()); len(got) != 0 {
		t.Fatalf("notices after signing in again = %+v, want none", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestHubRPCAuthStatusReportsOAuthRefreshAndLoginStates|TestHubNoticesNameARefusedRefresh' -count=1`
Expected: FAIL. The refused case reports `signedIn` true and `needsLogin` false, and no notice appears.

- [ ] **Step 3: Implement**

`cmd/evener-hub/app_auth.go`:

```diff
--- a/cmd/evener-hub/app_auth.go
+++ b/cmd/evener-hub/app_auth.go
@@ -1017,8 +1017,12 @@ func openAIStateDirFromEnv(env map[string]string) string {
 	return openAIStateDirFromEnvMap(env)
 }
 
-func openAIStatusFromRecord(now time.Time, record authopenai.AuthRecord) authopenai.AuthStatus {
-	needsLogin := record.NeedsLogin(now)
+// openAIStatusFromRecord is a Codex instance's status from its OAuth record.
+// refreshRejected says the issuer permanently refused the record's refresh
+// token (authopenai.RefreshRejected, #2479), which needs a fresh sign-in as
+// surely as an expired record with no refresh token does.
+func openAIStatusFromRecord(now time.Time, record authopenai.AuthRecord, refreshRejected bool) authopenai.AuthStatus {
+	needsLogin := record.NeedsLogin(now) || refreshRejected
 	return authopenai.AuthStatus{
 		SignedIn:     !needsLogin,
 		Source:       record.Source,
@@ -1666,7 +1670,7 @@ func (c *hubAuthController) openAIInstanceStatusKeyed(key []byte, name string, r
 	source := "none"
 	var active authopenai.AuthStatus
 	if hasRecord {
-		active = openAIStatusFromRecord(c.now(), record)
+		active = openAIStatusFromRecord(c.now(), record, authopenai.RefreshRejected(c.stateDir, name, record))
 		source = authopenai.AuthSourceOAuth
 	}
 
```

In `cov_auth_instances_fuzz_test.go`, `openAIStatusFromRecord(now, r)` becomes `openAIStatusFromRecord(now, r, false)`.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./cmd/evener-hub -run 'Auth|TestHubNotices' -count=1`
Expected: PASS.

Gates:
- `go vet ./cmd/evener-hub/`, `go vet -tags evenerfuzz ./cmd/evener-hub/`, `GOOS=windows go vet -tags evenerfuzz ./cmd/evener-hub/`;
- `golangci-lint run ./cmd/evener-hub/ ./auth/openai/`.

- [ ] **Step 5: Commit and open PR 23**

```bash
git add cmd/evener-hub/app_auth.go cmd/evener-hub/app_auth_test.go cmd/evener-hub/cov_auth_instances_fuzz_test.go cmd/evener-hub/app_notices_test.go
git commit -m "feat(hub): a refused refresh reads as needing a sign-in, and raises the notice"
```

Title "fix(auth): a refused Codex refresh needs a sign-in (S11b, phase 7 PR 23)". The body says "Closes #2479", explains why the note sits beside the record (ruling 11), and says a refusal the issuer takes back heals on the next refresh that works (ruling 12).

---

## PR 24: the message index, S14a (Tasks 24.1-24.2)

**Branch:** `git fetch origin && git switch -c claude/s14a-message-index origin/main`

**What it adds.** `search.db`, an FTS5 index of every user and agent message in this hub's format-2 transcripts under the item keys a thread read shows, kept in step on the past index's interval, and forgetting a deleted session at once. Nothing reads it yet; PR 25 answers from it.

### Task 24.1: The message index

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/hubcore/past.go` (`SearchTokens`, split out of `ftsQuery`)
- Create: `cmd/evener-hub/internal/hubcore/message_search.go`
- Create: `cmd/evener-hub/internal/hubcore/message_search_test.go`

**Interfaces:**
- Consumes: `transcriptindex.Open`, `DirFor`, `(*Index).Latest`, `Before`, `Incarnation`, `ChangedSince`, `ErrUpdateLogTruncated`; `transcript.ReadLine`, `DecodeHeader`, `ErrUnsupportedFormat`; `sqliteDSN`, `chmodSQLiteIndexFiles`, `ftsQuery` (hubcore); `events.SteeringSourceUser`.
- Produces:
  - `hubcore.SearchTokens(q string) []string`;
  - `OpenMessageSearch(path string) (*MessageSearch, error)`;
  - `(*MessageSearch).Close() error`;
  - `Refresh(ctx, []MessageSearchSession) ([]MessageSearchFailure, error)`;
  - `Forget(ctx, sessionID string) error`;
  - `Match(ctx, query string, hitsPerSession int) (map[string]MessageMatch, error)`;
  - `Texts(ctx, []MessageHit) ([]string, error)`;
  - the types `MessageSearchSession{ID, TranscriptPath}`, `MessageSearchFailure{SessionID, Err}`, `MessageMatch{Count, Hits}` and `MessageHit{TranscriptKey, Position}` (plus an unexported row id).

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/internal/hubcore/message_search_test.go`:

```go
package hubcore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// countReads wraps index's transcript reads and records the file each read,
// with "+" after it when the read took only what changed since the snapshot
// the index held.
func countReads(index *MessageSearch) *[]string {
	var read []string
	next := index.read
	index.read = func(path string, held *appwire.SnapshotIdentity) (transcriptItems, error) {
		items, err := next(path, held)
		name := filepath.Base(path)
		if err == nil && !items.replace {
			name += "+"
		}
		read = append(read, name)
		return items, err
	}
	return &read
}

func openTestMessageSearch(t *testing.T) *MessageSearch {
	t.Helper()
	index, err := OpenMessageSearch(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	return index
}

// writeTestTranscript writes a format-2 transcript of turns for sessionID at
// path.
func writeTestTranscript(t *testing.T, path, sessionID string, turns ...schema.Turn) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(path, transcript.Header{
		SessionID: sessionID, CreatedAt: time.Date(2026, 9, 27, 9, 0, 0, 0, time.UTC),
		ProfileID: "openai", Model: "gpt-5", SystemPrompt: "You are a careful engineer.",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// appendTestTranscript appends turns to the transcript at path.
func appendTestTranscript(t *testing.T, path string, turns ...schema.Turn) {
	t.Helper()
	w, err := transcript.OpenWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

// settleTurns is a user message, then an assistant turn that reasons, speaks
// and runs a tool, then the tool's output and the agent's answer. Every one of
// them says "settle"; only the two messages are searchable.
func settleTurns() []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("Why does the settle pass race the drain?")),
		{Kind: schema.TurnAssistant, Timestamp: time.Now().UTC(), Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "reasoning about settle"}},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"command":"grep settle"}`)}},
		}}},
		{Kind: schema.TurnToolResults, Timestamp: time.Now().UTC(), Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call_1", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Name: "shell", Content: "settle.go:12: settle()"},
		}}}},
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("The settle pass takes the tree lock first.")),
	}
}

// messageItems is every message item a thread read of path shows, by key,
// with its position: the latest window of the transcript's index, which holds
// every item of these short fixtures.
func messageItems(t *testing.T, path string) map[string]appwire.ThreadItemPosition {
	t.Helper()
	index, err := transcriptindex.Open(path, transcriptindex.DirFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = index.Close() }()
	window, err := index.Latest(appwire.TranscriptItemPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	if window.HasOlder {
		t.Fatal("the fixture outgrew one window")
	}
	items := map[string]appwire.ThreadItemPosition{}
	for _, candidate := range window.Candidates {
		if searchableMessage(candidate.Item) {
			items[candidate.Item.TranscriptKey] = candidate.Position
		}
	}
	return items
}

// The index keeps what the user typed and what the agent said, each under the
// transcript key and position a thread read gives that message's item, so a
// hit opens the session at the message. Reasoning, a tool's call and its
// output are not messages, whatever they say.
func TestMessageSearchKeepsUserAndAgentMessagesUnderTheirReadKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	index := openTestMessageSearch(t)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}

	matches, err := index.Match(context.Background(), "settle", 10)
	if err != nil {
		t.Fatal(err)
	}
	match, ok := matches["s1"]
	if !ok || match.Count != 2 || len(match.Hits) != 2 {
		t.Fatalf("matches = %+v, want s1's two messages", matches)
	}
	want := messageItems(t, path)
	if len(want) != 2 {
		t.Fatalf("the fixture projects %d message items, want 2", len(want))
	}
	for _, hit := range match.Hits {
		if position, ok := want[hit.TranscriptKey]; !ok || position != hit.Position {
			t.Fatalf("hit %+v is not a message item the read shows (%v)", hit, want)
		}
	}
	if compareItemPositions(match.Hits[0].Position, match.Hits[1].Position) <= 0 {
		t.Fatalf("hits = %+v, want the newest first", match.Hits)
	}
	texts, err := index.Texts(context.Background(), match.Hits)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"The settle pass takes the tree lock first.", "Why does the settle pass race the drain?"}; !reflect.DeepEqual(texts, want) {
		t.Fatalf("texts = %q, want %q", texts, want)
	}
}

// Every word must match, each as a prefix, letter case aside. A search whose
// words are all one letter searches nothing, and FTS5's own syntax in a search
// is only words.
func TestMessageSearchMatchesEveryWordAsAPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	index := openTestMessageSearch(t)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int{
		"SETT":                       2,
		"settle drain":               1,
		"settle lunaroute":           0,
		"t":                          0,
		`"settle" OR (drain* NEAR x`: 0,
		`settle:drain`:               1,
	} {
		matches, err := index.Match(context.Background(), query, 10)
		if err != nil {
			t.Fatalf("Match(%q): %v", query, err)
		}
		if got := matches["s1"].Count; got != want {
			t.Errorf("Match(%q) found %d messages, want %d", query, got, want)
		}
	}
}

// A session keeps its newest hits and counts the rest.
func TestMessageSearchKeepsTheNewestHitsPerSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	var turns []schema.Turn
	for range 5 {
		turns = append(turns, schema.NewTurn(schema.TurnUserInput, llm.User("check the drain")), schema.NewTurn(schema.TurnAssistant, llm.Assistant("the drain is fine")))
	}
	writeTestTranscript(t, path, "s1", turns...)
	index := openTestMessageSearch(t)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	matches, err := index.Match(context.Background(), "drain", 3)
	if err != nil {
		t.Fatal(err)
	}
	match := matches["s1"]
	if match.Count != 10 || len(match.Hits) != 3 {
		t.Fatalf("match = %+v, want 10 counted and the newest 3 kept", match)
	}
	all := messageItems(t, path)
	positions := make([]appwire.ThreadItemPosition, 0, len(all))
	for _, position := range all {
		positions = append(positions, position)
	}
	sort.Slice(positions, func(i, j int) bool { return compareItemPositions(positions[i], positions[j]) > 0 })
	for i, hit := range match.Hits {
		if hit.Position != positions[i] {
			t.Fatalf("hit %d = %+v, want position %+v, the %d-th newest", i, hit, positions[i], i+1)
		}
	}
}

// A refresh reads again only the transcripts that changed: a session whose
// transcript grew is re-read and its new message is found, and a session that
// did not change is not read at all.
func TestMessageSearchRefreshReadsOnlyChangedTranscripts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	first, second := filepath.Join(dir, "s1.transcript.jsonl"), filepath.Join(dir, "s2.transcript.jsonl")
	writeTestTranscript(t, first, "s1", settleTurns()...)
	writeTestTranscript(t, second, "s2", schema.NewTurn(schema.TurnUserInput, llm.User("unrelated work")))
	index := openTestMessageSearch(t)
	read := countReads(index)
	sessions := []MessageSearchSession{{ID: "s1", TranscriptPath: first}, {ID: "s2", TranscriptPath: second}}
	if _, err := index.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if _, err := index.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if want := []string{"s1.transcript.jsonl", "s2.transcript.jsonl"}; !reflect.DeepEqual(*read, want) {
		t.Fatalf("read %v, want each transcript once across two refreshes", *read)
	}

	appendTestTranscript(t, first, schema.NewTurn(schema.TurnUserInput, llm.User("now fix the retirement drain")))
	if _, err := index.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if want := []string{"s1.transcript.jsonl", "s2.transcript.jsonl", "s1.transcript.jsonl+"}; !reflect.DeepEqual(*read, want) {
		t.Fatalf("read %v, want only the grown transcript read again, and only what it gained", *read)
	}
	matches, err := index.Match(context.Background(), "retirement", 3)
	if err != nil {
		t.Fatal(err)
	}
	if matches["s1"].Count != 1 {
		t.Fatalf("matches = %+v, want the appended message found", matches)
	}
	// The read took only what changed: the messages from before stay once.
	if matches, _ := index.Match(context.Background(), "settle", 3); matches["s1"].Count != 2 {
		t.Fatalf("settle matches = %+v, want the two earlier messages once each", matches)
	}
}

// A transcript that stops being its old self grown by appends (a rewrite) has
// its transcript index rebuilt under a new incarnation, and the index reads it
// whole again: the old messages go, the new ones come.
func TestMessageSearchRereadsARewrittenTranscriptWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	index := openTestMessageSearch(t)
	sessions := []MessageSearchSession{{ID: "s1", TranscriptPath: path}}
	if _, err := index.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeTestTranscript(t, path, "s1",
		schema.NewTurn(schema.TurnUserInput, llm.User("audit the tool descriptions")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("Fourteen descriptions mention flags.")),
		schema.NewTurn(schema.TurnUserInput, llm.User("drop them")),
	)
	if _, err := index.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if matches, _ := index.Match(context.Background(), "settle", 3); len(matches) != 0 {
		t.Fatalf("settle matches = %+v, want none after the rewrite", matches)
	}
	if matches, _ := index.Match(context.Background(), "descriptions", 3); matches["s1"].Count != 2 {
		t.Fatalf("descriptions matches = %+v, want the rewritten transcript's two messages", matches)
	}
}

// The index outlives the hub: reopened, it keeps what it read and reads
// nothing again until a transcript changes.
func TestMessageSearchKeepsWhatItReadAcrossAReopen(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	dbPath := filepath.Join(root, "search.db")
	index, err := OpenMessageSearch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sessions := []MessageSearchSession{{ID: "s1", TranscriptPath: path}}
	if _, err := index.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenMessageSearch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	read := countReads(reopened)
	if _, err := reopened.Refresh(context.Background(), sessions); err != nil {
		t.Fatal(err)
	}
	if len(*read) != 0 {
		t.Fatalf("the reopened index read %v again, want nothing", *read)
	}
	if matches, err := reopened.Match(context.Background(), "settle", 3); err != nil || matches["s1"].Count != 2 {
		t.Fatalf("matches = %+v (%v), want what the first index read", matches, err)
	}
}

// A transcript older than format 2, which no reader opens, is recorded with
// no messages and not read again until it changes; it is not a failure, and
// no transcript index is built beside it. A transcript that fails to read for
// another reason is recorded the same way, and reported, once.
func TestMessageSearchRecordsAnUnreadableTranscriptOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	legacy, failing := filepath.Join(dir, "old.transcript.jsonl"), filepath.Join(dir, "bad.transcript.jsonl")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"kind":"header","format_version":1}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestTranscript(t, failing, "bad", settleTurns()...)
	index := openTestMessageSearch(t)
	next := index.read
	var read []string
	index.read = func(path string, held *appwire.SnapshotIdentity) (transcriptItems, error) {
		read = append(read, filepath.Base(path))
		if path == failing {
			return transcriptItems{}, errors.New("the disk refused the read")
		}
		return next(path, held)
	}
	sessions := []MessageSearchSession{{ID: "old", TranscriptPath: legacy}, {ID: "bad", TranscriptPath: failing}}

	failures, err := index.Refresh(context.Background(), sessions)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != 1 || failures[0].SessionID != "bad" {
		t.Fatalf("failures = %+v, want one for the transcript that failed to read", failures)
	}
	failures, err = index.Refresh(context.Background(), sessions)
	if err != nil || len(failures) != 0 {
		t.Fatalf("second refresh failures = %+v (%v), want none", failures, err)
	}
	if want := []string{"old.transcript.jsonl", "bad.transcript.jsonl"}; !reflect.DeepEqual(read, want) {
		t.Fatalf("read %v, want each unreadable transcript read once", read)
	}
	if _, err := os.Stat(transcriptindex.DirFor(legacy)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a transcript index was built beside the legacy transcript: %v", err)
	}
}

// A session the past index no longer lists, and one Forget names, leave the
// index at once: their words are not found.
func TestMessageSearchForgetsSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	first, second := filepath.Join(dir, "s1.transcript.jsonl"), filepath.Join(dir, "s2.transcript.jsonl")
	writeTestTranscript(t, first, "s1", settleTurns()...)
	writeTestTranscript(t, second, "s2", settleTurns()...)
	index := openTestMessageSearch(t)
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: first}, {ID: "s2", TranscriptPath: second}}); err != nil {
		t.Fatal(err)
	}
	if err := index.Forget(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if matches, _ := index.Match(context.Background(), "settle", 3); len(matches) != 1 || matches["s2"].Count != 2 {
		t.Fatalf("after Forget(s1) matches = %+v, want s2 alone", matches)
	}
	if _, err := index.Refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if matches, _ := index.Match(context.Background(), "settle", 3); len(matches) != 0 {
		t.Fatalf("after the past index listed nothing, matches = %+v, want none", matches)
	}
}

// search.db holds message text, so it and the WAL file SQLite creates beside
// it are the owner's alone.
func TestMessageSearchFilesAreOwnerOnly(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	dbPath := filepath.Join(root, "search.db")
	index, err := OpenMessageSearch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{dbPath, dbPath + "-wal"} {
		info, err := os.Stat(file)
		if err != nil {
			t.Fatal(err)
		}
		hubtest.AssertFileMode0600(t, info, filepath.Base(file))
	}
}

// An index of another schema version is a cache of the transcripts, so
// opening it drops it and starts empty rather than failing.
func TestMessageSearchRebuildsAnotherSchemaVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sessions", "s1.transcript.jsonl")
	writeTestTranscript(t, path, "s1", settleTurns()...)
	dbPath := filepath.Join(root, "search.db")
	index, err := OpenMessageSearch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	if _, err := index.db.ExecContext(context.Background(), `PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenMessageSearch(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if matches, err := reopened.Match(context.Background(), "settle", 3); err != nil || len(matches) != 0 {
		t.Fatalf("matches = %+v (%v), want an empty index", matches, err)
	}
	if _, err := reopened.Refresh(context.Background(), []MessageSearchSession{{ID: "s1", TranscriptPath: path}}); err != nil {
		t.Fatal(err)
	}
	if matches, _ := reopened.Match(context.Background(), "settle", 3); matches["s1"].Count != 2 {
		t.Fatalf("matches = %+v, want the transcript read again", matches)
	}
}

// SearchTokens is the one word rule the title search, the message search and
// its highlighting share: lowercased runs of letters, digits and underscores.
func TestSearchTokensSplitsOnEverythingButWordCharacters(t *testing.T) {
	if got, want := SearchTokens(`Fix "the" settle_race (NEAR drain*)`), []string{"fix", "the", "settle_race", "near", "drain"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SearchTokens = %q, want %q", got, want)
	}
	if got := strings.Join(SearchTokens("!!!"), ","); got != "" {
		t.Fatalf("SearchTokens(punctuation) = %q, want none", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run 'TestMessageSearch|TestSearchTokens' -count=1`
Expected: FAIL to compile (`MessageSearch`, `SearchTokens` undefined).

- [ ] **Step 3: Implement**

`cmd/evener-hub/internal/hubcore/past.go`:

```diff
--- a/cmd/evener-hub/internal/hubcore/past.go
+++ b/cmd/evener-hub/internal/hubcore/past.go
@@ -1015,10 +1015,19 @@ func (i *PastIndex) searchFTS(q string) ([]PastEntry, bool) {
 	return out, true
 }
 
-func ftsQuery(q string) string {
-	tokens := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
+// SearchTokens splits a search into the lowercased words it looks for: the
+// runs of letters, digits and underscores. Everything else, FTS5's own query
+// syntax included, separates words, so no search can be read as an FTS5
+// operator.
+func SearchTokens(q string) []string {
+	return strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
 		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
 	})
+}
+
+// ftsQuery is q as an FTS5 match: every word of it as a prefix, all required.
+func ftsQuery(q string) string {
+	tokens := SearchTokens(q)
 	if len(tokens) == 0 {
 		return ""
 	}
```

`cmd/evener-hub/internal/hubcore/message_search.go`:

```go
package hubcore

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/transcriptindex"
)

// messageSearchSchemaVersion is the layout OpenMessageSearch creates, kept in
// PRAGMA user_version. search.db is its own file, so its header is free for
// it, and the index is a cache of the transcripts: a file of any other
// version is dropped and rebuilt rather than migrated. Bump it when what the
// index keeps changes too (searchableMessage), so every transcript is read
// again.
const messageSearchSchemaVersion = 1

// messageSearchSchema is the index's layout. messages holds each message's
// text once, under its session and the transcript key a thread read gives its
// item; messages_fts indexes the text through FTS5's external-content mode,
// kept in step by the two triggers. transcripts records, per session, the
// file state the index last read and the transcript-index snapshot its rows
// describe.
//
// The tokenizer leaves diacritics alone, so a word matches exactly what the
// search's highlighting finds (package hub's snippets), letter case aside.
var messageSearchSchema = []string{
	`CREATE TABLE transcripts(
	session_id TEXT PRIMARY KEY,
	size INTEGER NOT NULL,
	mod_time_ns INTEGER NOT NULL,
	incarnation TEXT NOT NULL,
	length INTEGER NOT NULL
)`,
	`CREATE TABLE messages(
	id INTEGER PRIMARY KEY,
	session_id TEXT NOT NULL,
	transcript_key TEXT NOT NULL,
	entry INTEGER NOT NULL,
	item INTEGER NOT NULL,
	sub INTEGER NOT NULL,
	text TEXT NOT NULL
)`,
	`CREATE UNIQUE INDEX messages_by_key ON messages(session_id, transcript_key)`,
	`CREATE VIRTUAL TABLE messages_fts USING fts5(text, content='messages', content_rowid='id', tokenize='unicode61 remove_diacritics 0')`,
	`CREATE TRIGGER messages_indexed AFTER INSERT ON messages BEGIN
	INSERT INTO messages_fts(rowid, text) VALUES (new.id, new.text);
END`,
	`CREATE TRIGGER messages_unindexed AFTER DELETE ON messages BEGIN
	INSERT INTO messages_fts(messages_fts, rowid, text) VALUES ('delete', old.id, old.text);
END`,
}

// messageSearchDrop removes every object messageSearchSchema creates, triggers
// first.
var messageSearchDrop = []string{
	`DROP TRIGGER IF EXISTS messages_indexed`,
	`DROP TRIGGER IF EXISTS messages_unindexed`,
	`DROP TABLE IF EXISTS messages_fts`,
	`DROP TABLE IF EXISTS messages`,
	`DROP TABLE IF EXISTS transcripts`,
}

// minMessageSearchTokenRunes is the shortest word a message search runs for.
// A single letter prefixes most words in every transcript, so it would list
// nearly every session and cost the most to answer; the title search still
// answers it.
const minMessageSearchTokenRunes = 2

// MessageSearch is the hub's message-text search index (S14): the text of
// every user and agent message in the hub's local transcripts, kept under the
// transcript key and position a thread read gives the message's item, so a
// hit opens its session at the message. It reads transcripts through their
// transcript indexes (internal/transcriptindex), the read model thread reads
// use, and lives in its own SQLite file beside index.db.
type MessageSearch struct {
	db *sql.DB
	// read reads a transcript's items for the index: readTranscriptItems,
	// which a test wraps to count the reads.
	read func(transcriptPath string, held *appwire.SnapshotIdentity) (transcriptItems, error)
	// writeMu keeps a Refresh and a Forget from interleaving their writes to
	// one session.
	writeMu sync.Mutex
}

// MessageSearchSession names one session the index covers: its ID and where
// its transcript is.
type MessageSearchSession struct {
	ID             string
	TranscriptPath string
}

// MessageSearchFailure is a transcript the index could not read, for a reason
// other than its format. The index records it as it records an unreadable
// format, so it is not read again until it changes, and the caller reports it
// once.
type MessageSearchFailure struct {
	SessionID string
	Err       error
}

// MessageMatch is one session's answer to a message search: how many of its
// messages match, and the newest of them, newest first.
type MessageMatch struct {
	Count int
	Hits  []MessageHit
}

// MessageHit is one matching message: the transcript item it is.
type MessageHit struct {
	id            int64
	TranscriptKey string
	Position      appwire.ThreadItemPosition
}

// transcriptStamp is the transcript file's state when the index last read it:
// a transcript only grows, so an append moves its size, and a rewrite moves
// its modification time.
type transcriptStamp struct {
	size      int64
	modTimeNS int64
}

// indexedTranscript is what the index holds for one session: the file state it
// last read and the transcript-index snapshot its rows describe (no
// incarnation when the transcript could not be read).
type indexedTranscript struct {
	stamp    transcriptStamp
	snapshot appwire.SnapshotIdentity
}

// transcriptItems is one read of a transcript: the items it returned and the
// snapshot they describe. replace says they are every item; otherwise they
// are the ones an entry at or past the held snapshot's length created or
// changed.
type transcriptItems struct {
	items    []appwire.ThreadItem
	snapshot appwire.SnapshotIdentity
	replace  bool
}

// OpenMessageSearch opens the message-text search index at path, creating it
// when it is missing and rebuilding it empty when it holds another schema
// version.
func OpenMessageSearch(path string) (*MessageSearch, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create message search directory: %w", err)
	}
	// Create the file owner-only before SQLite opens it: SQLite gives the WAL
	// and shared-memory files it creates later the database file's own mode,
	// and the index holds message text.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create message search index: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("create message search index: %w", err)
	}
	if err := chmodSQLiteIndexFiles(path); err != nil {
		return nil, fmt.Errorf("restrict message search index: %w", err)
	}
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open message search index: %w", err)
	}
	if err := migrateMessageSearch(context.Background(), db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &MessageSearch{db: db, read: readTranscriptItems}, nil
}

// migrateMessageSearch leaves a current index alone and replaces anything
// else, a new file included, with an empty current one.
func migrateMessageSearch(ctx context.Context, db *sql.DB) error {
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read message search version: %w", err)
	}
	if version == messageSearchSchemaVersion {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate message search index: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range slices.Concat(messageSearchDrop, messageSearchSchema) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate message search index: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, messageSearchSchemaVersion)); err != nil {
		return fmt.Errorf("migrate message search index: %w", err)
	}
	return tx.Commit()
}

// Close closes the index's database.
func (x *MessageSearch) Close() error {
	return x.db.Close()
}

// Refresh brings the index in step with sessions' transcripts. It reads only a
// transcript whose size or modification time moved since the index last read
// it, and then only what changed since the snapshot the index holds (all of it
// when the transcript's index was rebuilt since). It forgets every indexed
// session sessions no longer names. A transcript it cannot read is recorded
// with no messages, so it is not read again until it changes; a failure other
// than an unsupported format (a transcript older than format 2, which no
// reader opens) is returned, once. The error is the index's own: its database
// failed, or ctx ended.
func (x *MessageSearch) Refresh(ctx context.Context, sessions []MessageSearchSession) ([]MessageSearchFailure, error) {
	indexed, err := x.indexedTranscripts(ctx)
	if err != nil {
		return nil, err
	}
	var failures []MessageSearchFailure
	listed := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		if err := ctx.Err(); err != nil {
			return failures, err
		}
		listed[session.ID] = true
		info, err := os.Stat(session.TranscriptPath)
		if err != nil {
			// No transcript yet, or not any more: nothing to search.
			if _, ok := indexed[session.ID]; ok {
				if err := x.Forget(ctx, session.ID); err != nil {
					return failures, err
				}
			}
			continue
		}
		stamp := transcriptStamp{size: info.Size(), modTimeNS: info.ModTime().UnixNano()}
		held, known := indexed[session.ID]
		if known && held.stamp == stamp {
			continue
		}
		var since *appwire.SnapshotIdentity
		if known && held.snapshot.Incarnation != "" {
			since = &held.snapshot
		}
		read, err := x.read(session.TranscriptPath, since)
		if err != nil {
			if !errors.Is(err, transcript.ErrUnsupportedFormat) {
				failures = append(failures, MessageSearchFailure{SessionID: session.ID, Err: err})
			}
			read = transcriptItems{replace: true}
		}
		if err := x.apply(ctx, session.ID, stamp, read); err != nil {
			return failures, err
		}
	}
	for sessionID := range indexed {
		if !listed[sessionID] {
			if err := x.Forget(ctx, sessionID); err != nil {
				return failures, err
			}
		}
	}
	return failures, nil
}

// indexedTranscripts is what the index holds for every indexed session.
func (x *MessageSearch) indexedTranscripts(ctx context.Context) (map[string]indexedTranscript, error) {
	rows, err := x.db.QueryContext(ctx, `SELECT session_id, size, mod_time_ns, incarnation, length FROM transcripts`)
	if err != nil {
		return nil, fmt.Errorf("read message search transcripts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	indexed := map[string]indexedTranscript{}
	for rows.Next() {
		var sessionID string
		var held indexedTranscript
		if err := rows.Scan(&sessionID, &held.stamp.size, &held.stamp.modTimeNS, &held.snapshot.Incarnation, &held.snapshot.Length); err != nil {
			return nil, fmt.Errorf("read message search transcripts: %w", err)
		}
		indexed[sessionID] = held
	}
	return indexed, rows.Err()
}

// readTranscriptItems reads a transcript's items through its transcript index,
// the read model a thread read uses, so every item carries the key and
// position a reader shows. It opens its own handle and closes it, so indexing
// every transcript never evicts the handles readers hold. With held naming the
// index's current incarnation it reads only what changed since held;
// otherwise, or when the index's update log no longer reaches back to held, it
// reads every item.
func readTranscriptItems(transcriptPath string, held *appwire.SnapshotIdentity) (transcriptItems, error) {
	// A legacy transcript has no transcript index: checking its header first
	// keeps Open from leaving an empty sidecar directory beside it.
	if err := checkTranscriptHeader(transcriptPath); err != nil {
		return transcriptItems{}, err
	}
	index, err := transcriptindex.Open(transcriptPath, transcriptindex.DirFor(transcriptPath))
	if err != nil {
		return transcriptItems{}, err
	}
	defer func() { _ = index.Close() }()
	if held != nil {
		incarnation, err := index.Incarnation()
		if err != nil {
			return transcriptItems{}, err
		}
		if incarnation == held.Incarnation {
			changes, err := index.ChangedSince(held.Length)
			if err == nil {
				return transcriptItems{items: candidateItems(changes.Items), snapshot: appwire.SnapshotIdentity{Incarnation: changes.Incarnation, Length: changes.Length}}, nil
			}
			if !errors.Is(err, transcriptindex.ErrUpdateLogTruncated) {
				return transcriptItems{}, err
			}
		}
	}
	return readEveryItem(index)
}

// readEveryItem pages through every item of the index, newest window first.
func readEveryItem(index *transcriptindex.Index) (transcriptItems, error) {
	window, err := index.Latest(appwire.TranscriptItemPageLimit)
	if err != nil {
		return transcriptItems{}, err
	}
	read := transcriptItems{snapshot: appwire.SnapshotIdentity{Incarnation: window.Incarnation, Length: window.Length}, replace: true}
	for {
		read.items = append(read.items, candidateItems(window.Candidates)...)
		if !window.HasOlder || len(window.Candidates) == 0 {
			return read, nil
		}
		window, err = index.Before(window.Candidates[0].Position, appwire.TranscriptItemPageLimit)
		if err != nil {
			return transcriptItems{}, err
		}
		// A rebuild between two pages renames every item; the next refresh
		// reads the new incarnation whole.
		if window.Incarnation != read.snapshot.Incarnation {
			return transcriptItems{}, appwire.TranscriptItemCursorStale()
		}
	}
}

func candidateItems(candidates []appitempaging.TranscriptItemCandidate) []appwire.ThreadItem {
	items := make([]appwire.ThreadItem, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, candidate.Item)
	}
	return items
}

// checkTranscriptHeader reads only a transcript's first line and returns
// transcript.ErrUnsupportedFormat (wrapped) unless it is a format-2 header.
func checkTranscriptHeader(transcriptPath string) error {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	line, complete, _, err := transcript.ReadLine(bufio.NewReader(f), 128<<20)
	if err != nil {
		return err
	}
	if !complete {
		return fmt.Errorf("%w: missing transcript header", transcript.ErrUnsupportedFormat)
	}
	_, err = transcript.DecodeHeader(bytes.TrimSpace(line))
	return err
}

// searchableMessage reports whether the index keeps item: what the user typed
// (a message, or a steer the user sent) and what the agent said. Reasoning,
// tool calls and their output, and system notices are not messages. An item
// without a key cannot be opened at, and one with no text has nothing to find.
func searchableMessage(item appwire.ThreadItem) bool {
	message := item.Type == "userMessage" || item.Type == "agentMessage" ||
		(item.Type == "steering" && item.Source == events.SteeringSourceUser)
	return message && item.TranscriptKey != "" && item.Position != nil && strings.TrimSpace(item.Text) != ""
}

// apply writes one read of a session in one transaction, so a search sees the
// session's messages from before the read or after it and never a mix: a
// replace drops every row first; otherwise each changed item's row is dropped
// and written again while the item is a message. Then it records stamp and the
// read's snapshot.
func (x *MessageSearch) apply(ctx context.Context, sessionID string, stamp transcriptStamp, read transcriptItems) error {
	x.writeMu.Lock()
	defer x.writeMu.Unlock()
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("index session %s: %w", sessionID, err)
	}
	defer func() { _ = tx.Rollback() }()
	if read.replace {
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE session_id = ?`, sessionID); err != nil {
			return fmt.Errorf("index session %s: %w", sessionID, err)
		}
	}
	for _, item := range read.items {
		if !read.replace {
			if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE session_id = ? AND transcript_key = ?`, sessionID, item.TranscriptKey); err != nil {
				return fmt.Errorf("index session %s: %w", sessionID, err)
			}
		}
		if !searchableMessage(item) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO messages(session_id, transcript_key, entry, item, sub, text) VALUES (?, ?, ?, ?, ?, ?)`,
			sessionID, item.TranscriptKey, int64(item.Position.Entry), int64(item.Position.Item), int64(item.Position.Sub), item.Text); err != nil {
			return fmt.Errorf("index session %s: %w", sessionID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO transcripts(session_id, size, mod_time_ns, incarnation, length) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(session_id) DO UPDATE SET size = excluded.size, mod_time_ns = excluded.mod_time_ns, incarnation = excluded.incarnation, length = excluded.length`,
		sessionID, stamp.size, stamp.modTimeNS, read.snapshot.Incarnation, read.snapshot.Length); err != nil {
		return fmt.Errorf("index session %s: %w", sessionID, err)
	}
	return tx.Commit()
}

// Forget removes one session from the index: its messages and its transcript
// record. Deleting a session calls it, so the session's words leave search at
// once rather than at the next Refresh.
func (x *MessageSearch) Forget(ctx context.Context, sessionID string) error {
	x.writeMu.Lock()
	defer x.writeMu.Unlock()
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("forget session %s: %w", sessionID, err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{`DELETE FROM messages WHERE session_id = ?`, `DELETE FROM transcripts WHERE session_id = ?`} {
		if _, err := tx.ExecContext(ctx, stmt, sessionID); err != nil {
			return fmt.Errorf("forget session %s: %w", sessionID, err)
		}
	}
	return tx.Commit()
}

// Match finds the messages that hold every word of query, each word matching
// as a prefix, letter case aside. It answers for every matching session: how
// many of its messages match, and its newest hitsPerSession hits. A query with
// no word of at least two letters or digits searches nothing.
func (x *MessageSearch) Match(ctx context.Context, query string, hitsPerSession int) (map[string]MessageMatch, error) {
	if !hasMessageSearchWord(SearchTokens(query)) {
		return map[string]MessageMatch{}, nil
	}
	rows, err := x.db.QueryContext(ctx, `SELECT m.id, m.session_id, m.transcript_key, m.entry, m.item, m.sub
FROM messages_fts JOIN messages m ON m.id = messages_fts.rowid
WHERE messages_fts MATCH ?`, ftsQuery(query))
	if err != nil {
		return nil, fmt.Errorf("search messages: %w", err)
	}
	defer func() { _ = rows.Close() }()
	matches := map[string]MessageMatch{}
	for rows.Next() {
		var sessionID string
		var hit MessageHit
		var entry, item, sub int64
		if err := rows.Scan(&hit.id, &sessionID, &hit.TranscriptKey, &entry, &item, &sub); err != nil {
			return nil, fmt.Errorf("search messages: %w", err)
		}
		hit.Position = appwire.ThreadItemPosition{Entry: uint64(entry), Item: uint32(item), Sub: uint32(sub)}
		match := matches[sessionID]
		match.Count++
		match.Hits = keepNewestHits(match.Hits, hit, hitsPerSession)
		matches[sessionID] = match
	}
	return matches, rows.Err()
}

func hasMessageSearchWord(tokens []string) bool {
	for _, token := range tokens {
		if utf8.RuneCountInString(token) >= minMessageSearchTokenRunes {
			return true
		}
	}
	return false
}

// keepNewestHits adds hit to hits, which is newest first, and keeps at most
// limit of them.
func keepNewestHits(hits []MessageHit, hit MessageHit, limit int) []MessageHit {
	at, _ := slices.BinarySearchFunc(hits, hit, func(kept, hit MessageHit) int {
		return compareItemPositions(hit.Position, kept.Position)
	})
	if at >= limit {
		return hits
	}
	hits = slices.Insert(hits, at, hit)
	return hits[:min(len(hits), limit)]
}

// compareItemPositions orders two items of one transcript: negative when a
// comes first.
func compareItemPositions(a, b appwire.ThreadItemPosition) int {
	return cmp.Or(cmp.Compare(a.Entry, b.Entry), cmp.Compare(a.Item, b.Item), cmp.Compare(a.Sub, b.Sub))
}

// Texts returns each hit's message text, in hits' order. A hit whose message
// left the index since the match comes back empty.
func (x *MessageSearch) Texts(ctx context.Context, hits []MessageHit) ([]string, error) {
	texts := make([]string, len(hits))
	if len(hits) == 0 {
		return texts, nil
	}
	args := make([]any, len(hits))
	for i, hit := range hits {
		args[i] = hit.id
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(hits)), ",")
	rows, err := x.db.QueryContext(ctx, `SELECT id, text FROM messages WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("read message texts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	byID := make(map[int64]string, len(hits))
	for rows.Next() {
		var id int64
		var text string
		if err := rows.Scan(&id, &text); err != nil {
			return nil, fmt.Errorf("read message texts: %w", err)
		}
		byID[id] = text
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read message texts: %w", err)
	}
	for i, hit := range hits {
		texts[i] = byID[hit.id]
	}
	return texts, nil
}
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test ./cmd/evener-hub/internal/hubcore -run 'TestMessageSearch|TestSearchTokens|TestPast|FuzzHubcoreScenarios' -count=1`
Expected: PASS. The past index's own tests are unchanged: `ftsQuery` answers as before. Prove the tests can fail:
- make `Refresh` skip its stamp check: `TestMessageSearchRefreshReadsOnlyChangedTranscripts` and `TestMessageSearchKeepsWhatItReadAcrossAReopen` fail;
- report an unsupported format as a failure: `TestMessageSearchRecordsAnUnreadableTranscriptOnce` fails;
- let `searchableMessage` accept `commandExecution` and `reasoning`: `TestMessageSearchKeepsUserAndAgentMessagesUnderTheirReadKeys` fails.

Gates: `golangci-lint run ./cmd/evener-hub/internal/hubcore/`.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/internal/hubcore/past.go cmd/evener-hub/internal/hubcore/message_search.go cmd/evener-hub/internal/hubcore/message_search_test.go
git commit -m "feat(hub): the message index, read through the transcript index"
```

### Task 24.2: The hub keeps the index and forgets deleted sessions

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/hubcore/config.go` (`WebConfig.MessageSearch` after `SessionSeen`)
- Modify: `cmd/evener-hub/main.go` (open `search.db` after the auth token loads; the config field; the refresher after the past-index rebuild loop)
- Modify: `cmd/evener-hub/main_background.go` (`refreshHubMessageSearch` and `messageSearchSessions`, after `watchHubAttention`)
- Modify: `cmd/evener-hub/project_delete.go` (`scrubSessionDecisions` forgets the session's messages)
- Create: `cmd/evener-hub/message_search_test.go`

**Interfaces:**
- Consumes: Task 24.1; `pastTranscriptPath`, `pastThreadReadResponse` (package hub, on main).
- Produces: `hubcore.WebConfig.MessageSearch *hubcore.MessageSearch`; `refreshHubMessageSearch(ctx, index, past, interval)`; `messageSearchSessions([]hubcore.PastEntry) []hubcore.MessageSearchSession`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/message_search_test.go`:

```go
package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/llm"
)

// seedSearchSession saves a past session whose transcript holds turns, with a
// system prompt, so the read's prelude takes the first entry ordinal and
// every later item's position moves past it.
func seedSearchSession(t *testing.T, projectsRoot, readable string, updated time.Time, turns ...schema.Turn) (hubcore.PastEntry, string) {
	t.Helper()
	stateDir := hubtest.ProjectDir(t, projectsRoot, readable)
	sessionID := hubtest.SessionID(t)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated,
		Name: "Settle " + readable, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/" + readable},
	}); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", sessionID+".transcript.jsonl"), transcript.Header{
		SessionID: sessionID, CreatedAt: updated.Add(-time.Hour), ProfileID: "openai", Model: "gpt-5",
		SystemPrompt: "You are a careful engineer.",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return hubcore.PastEntry{ID: sessionID, StateDir: stateDir}, sessionID
}

// A message hit opens its session at the item a thread read shows for that
// message: the index reads transcripts through the thread read's own
// projection, so its key and position are the reader's, prelude, tool calls
// and continuation turns included.
func TestMessageSearchHitsAreTheItemsAThreadReadShows(t *testing.T) {
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	_, sessionID := seedSearchSession(t, projectsRoot, "alpha", time.Now().Add(-time.Hour),
		schema.NewTurn(schema.TurnUserInput, llm.User("Why does the settle pass race the drain?")),
		schema.Turn{Kind: schema.TurnAssistant, Timestamp: time.Now().UTC(), Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "Checking the settle pass."},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_1", Name: "shell", Arguments: json.RawMessage(`{"command":"grep settle"}`)}},
		}}},
		schema.Turn{Kind: schema.TurnToolResults, Timestamp: time.Now().UTC(), Message: llm.Message{Role: llm.RoleTool, ToolCallID: "call_1", Content: []llm.ContentPart{{
			Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_1", Name: "shell", Content: "settle.go:12"},
		}}}},
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("The settle pass takes the tree lock first.")),
		schema.NewTurn(schema.TurnUserInput, llm.User("Then settle it.")),
	)
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	index, err := hubcore.OpenMessageSearch(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	if failures, err := index.Refresh(context.Background(), messageSearchSessions(past.All())); err != nil || len(failures) != 0 {
		t.Fatalf("Refresh failures %+v, err %v", failures, err)
	}
	matches, err := index.Match(context.Background(), "settle", 10)
	if err != nil {
		t.Fatal(err)
	}
	hits := matches[sessionID].Hits
	if len(hits) != 4 {
		t.Fatalf("hits = %+v, want the four messages that say settle", hits)
	}

	read, found := requirePastThreadReadResponse(t, hubcore.WebConfig{Past: past}, appwire.ThreadReadParams{Ref: "local:" + sessionID, IncludeTurns: true})
	if !found {
		t.Fatal("the past session was not found for reading")
	}
	shown := map[string]appwire.ThreadItem{}
	for _, turn := range read.Thread.Turns {
		for _, item := range turn.Items {
			shown[item.TranscriptKey] = item
		}
	}
	for _, hit := range hits {
		item, ok := shown[hit.TranscriptKey]
		if !ok || item.Position == nil || *item.Position != hit.Position || !strings.Contains(strings.ToLower(item.Text), "settle") {
			t.Fatalf("hit %+v is not an item the thread read shows saying settle (read: %+v)", hit, shown)
		}
	}
}

// Deleting a session takes its words out of search at once, not at the next
// refresh: the deletion's scrub forgets it in the message index.
func TestDeletingASessionForgetsItsMessages(t *testing.T) {
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	_, sessionID := seedSearchSession(t, projectsRoot, "alpha", time.Now(), schema.NewTurn(schema.TurnUserInput, llm.User("settle the drain")))
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	index, err := hubcore.OpenMessageSearch(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	if _, err := index.Refresh(context.Background(), messageSearchSessions(past.All())); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{HubStateRoot: t.TempDir(), MessageSearch: index})
	if errs := web.scrubSessionDecisions(sessionID); len(errs) != 0 {
		t.Fatalf("scrub errors: %v", errs)
	}
	if matches, err := index.Match(context.Background(), "settle", 3); err != nil || len(matches) != 0 {
		t.Fatalf("matches after the scrub = %+v (%v), want none", matches, err)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestMessageSearchHitsAreTheItemsAThreadReadShows|TestDeletingASessionForgetsItsMessages' -count=1`
Expected: FAIL to compile (`messageSearchSessions`, `WebConfig.MessageSearch` undefined).

- [ ] **Step 3: Implement**

`cmd/evener-hub/internal/hubcore/config.go`:

```diff
--- a/cmd/evener-hub/internal/hubcore/config.go
+++ b/cmd/evener-hub/internal/hubcore/config.go
@@ -85,6 +85,10 @@ type WebConfig struct {
 	Favorite    *FavoriteStore    // favorite decision store; nil when not configured
 	PinSections *PinSectionStore  // named pin-section store; nil when not configured
 	SessionSeen *SessionSeenStore // per-session seen-through markers (S4); nil when not configured
+	// MessageSearch is the message-text search index (S14); nil when it is not
+	// configured or search.db could not be opened, and search then finds
+	// sessions by title and prompt only.
+	MessageSearch *MessageSearch
 
 	Inputs *InputsVersion // shared inputs-version counter; nil in tests (memo treats as version 0)
 
```

`cmd/evener-hub/main.go`:

```diff
--- a/cmd/evener-hub/main.go
+++ b/cmd/evener-hub/main.go
@@ -344,6 +344,16 @@ func runMain(args []string, stderr io.Writer, deps mainDeps) error {
 		_, _ = fmt.Fprintf(stderr, "[hub] auth token: %v\n", err)
 		return err
 	}
+	// The message search index (S14) lives in its own file beside index.db.
+	// A hub that cannot open it still serves; search then finds sessions by
+	// title and prompt only.
+	messageSearch, err := hubcore.OpenMessageSearch(filepath.Join(hubStateRoot, "search.db"))
+	if err != nil {
+		_, _ = fmt.Fprintf(stderr, "[hub] message search: %v\n", err)
+		messageSearch = nil
+	} else {
+		defer func() { _ = messageSearch.Close() }()
+	}
 	providersConfigPath, noUserLayer := cmdutil.ProvidersConfigPath()
 	credentialsPath := cmdutil.CredentialsPath()
 	credsStore, err := deps.loadCredentials(credentialsPath)
@@ -586,6 +596,7 @@ func runMain(args []string, stderr io.Writer, deps mainDeps) error {
 		Favorite:                  favorite,
 		PinSections:               pinSections,
 		SessionSeen:               sessionSeen,
+		MessageSearch:             messageSearch,
 		Spawner:                   spawner,
 		APILogDefault:             cfg.APILog,
 		DeletionStore:             deletionStore,
@@ -772,6 +783,9 @@ func runMain(args []string, stderr io.Writer, deps mainDeps) error {
 			}
 		}
 	})
+	// Message search refresher: re-reads the transcripts that changed on the
+	// past index's rebuild interval (S14).
+	startBackground(func() { refreshHubMessageSearch(ctx, messageSearch, past, cfg.PastIndexRebuild) })
 
 	// Attention watcher: derives each live session's attention level from the
 	// same roster/past-index/archive inputs the sidebar tree uses, and
```

`cmd/evener-hub/project_delete.go`:

```diff
--- a/cmd/evener-hub/project_delete.go
+++ b/cmd/evener-hub/project_delete.go
@@ -512,6 +512,12 @@ func (s *WebServer) scrubSessionDecisions(threadID string) (decisionErrors []str
 			decisionErrors = append(decisionErrors, fmt.Sprintf("seen marker store error: %v", err))
 		}
 	}
+	// A deleted session's words leave search now, not at the next refresh.
+	if s.cfg.MessageSearch != nil {
+		if err := s.cfg.MessageSearch.Forget(context.Background(), threadID); err != nil {
+			decisionErrors = append(decisionErrors, fmt.Sprintf("message search index error: %v", err))
+		}
+	}
 	return decisionErrors
 }
 
```

`cmd/evener-hub/main_background.go`, after `watchHubAttention`:

```go
// refreshHubMessageSearch keeps the message search index (S14) in step with
// the transcripts the past index lists: once now, then every interval (the
// past index's own rebuild interval). A refresh reads again only the
// transcripts that changed.
func refreshHubMessageSearch(ctx context.Context, index *hubcore.MessageSearch, past *hubcore.PastIndex, interval time.Duration) {
	if index == nil || ctx.Err() != nil {
		return
	}
	ticks, stop := hubTicker(interval)
	defer stop()
	refresh := func() {
		failures, err := index.Refresh(ctx, messageSearchSessions(past.All()))
		for _, failure := range failures {
			fmt.Fprintf(os.Stderr, "[hub] message search: session %s: %v\n", failure.SessionID, failure.Err)
		}
		if err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "[hub] message search: %v\n", err)
		}
	}
	refresh()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			refresh()
		}
	}
}

// messageSearchSessions is every session the past index lists, with its
// transcript.
func messageSearchSessions(entries []hubcore.PastEntry) []hubcore.MessageSearchSession {
	sessions := make([]hubcore.MessageSearchSession, 0, len(entries))
	for _, entry := range entries {
		sessions = append(sessions, hubcore.MessageSearchSession{ID: entry.Meta.ID, TranscriptPath: pastTranscriptPath(entry)})
	}
	return sessions
}
```

Run `$(go env GOROOT)/bin/gofmt -w` on the touched files.

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run: `go test ./cmd/evener-hub -run 'TestMessageSearch|TestDeletingASession|Delete|TestRunMain|MainBackground' -count=1`
Expected: PASS. `TestMessageSearchHitsAreTheItemsAThreadReadShows` compares every hit against the hub's own `pastThreadReadResponse`. Prove `TestDeletingASessionForgetsItsMessages` can fail: skip the forget in `scrubSessionDecisions`.

Gates, root module:
- `go vet ./...`, `go vet -tags evenerfuzz ./...` and `GOOS=windows go vet -tags evenerfuzz ./...`;
- `golangci-lint run ./cmd/evener-hub/ ./cmd/evener-hub/internal/hubcore/`.

- [ ] **Step 5: Commit and open PR 24**

```bash
git add cmd/evener-hub/internal/hubcore/config.go cmd/evener-hub/main.go cmd/evener-hub/main_background.go cmd/evener-hub/project_delete.go cmd/evener-hub/message_search_test.go
git commit -m "feat(hub): keep the message index in step, and forget deleted sessions"
```

Title "feat(hub): the message search index (S14a, phase 7 PR 24)". The body gives the measured costs ("What was measured"), the 60-second freshness, `search.db`'s 0600 mode, and that the refresher builds the same transcript-index sidecar a first thread read builds.

---

## PR 25: search scopes, the archived flag and message hits, S14b (Tasks 25.1-25.2)

**Branch:** `git fetch origin && git switch -c claude/s14b-search-results origin/main`, after PR 24 merges.

### Task 25.1: One archive rule, and search's scopes and archived flag

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/internal/hubcore/tree.go` (`SessionArchived` before `classifySession`; the Live band's filter calls it)
- Create: `cmd/evener-hub/internal/hubcore/session_archived_test.go`
- Modify: `appwire/types.go` (`SearchParams.Scope`, the scope constants, `SearchResult.Archived`, `Hits`, `HitCount`, `SearchHit`, `SearchSnippetPart`, `SearchResponse.InSessions`, `Scope`)
- Modify: `appwire/protocol.go` (`evener/search`'s description)
- Modify: `cmd/evener-hub/app_search.go` (rewritten below; Task 25.2 adds the In sessions group to it)
- Modify: `cmd/evener-hub/app_rpc.go` (the handler passes its context and `time.Now()`; import `time`)
- Modify: `cmd/evener-hub/app_search_test.go` (the four `hubSearch` calls)
- Create: `cmd/evener-hub/app_search_scope_test.go`
- Regenerate: `appwire-client/typescript/types.gen.ts`, `docs/appwire-protocol.md`

**Interfaces:**
- Produces:
  - `hubcore.SessionArchived(decisions map[ArchiveKey]bool, sessionID, projectID, source string, lastActivity, now time.Time) bool`;
  - `appwire.SearchScopeAll`, `SearchScopeLive`, `SearchScopeArchived`;
  - `hubSearch(ctx, cfg, params, now) (appwire.SearchResponse, error)`;
  - `searchScopeAdmits`, `liveSearchResult`, `pastSearchResult`, `searchDecisions`.

This task writes `app_search.go` whole, In sessions group included, because the group is a dozen lines of the same function. Task 25.2 adds the snippet it calls, so this task's step 3 uses a placeholder `searchSnippet` that Task 25.2 replaces:

```go
// cmd/evener-hub/app_search_snippet.go (Task 25.2 replaces this file)
package hub

import "primeradiant.com/evener/appwire"

func searchSnippet(text string, _ []string) []appwire.SearchSnippetPart {
	return []appwire.SearchSnippetPart{{Text: text}}
}
```

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/internal/hubcore/session_archived_test.go`:

```go
package hubcore

import (
	"testing"
	"time"
)

// SessionArchived is the rail's rule (S14 reports it as a search result's
// archived flag): an explicit decision wins over age; with none, a session two
// weeks without activity is archived; and a project its source archived
// archives its sessions whatever their own decision.
func TestSessionArchivedFollowsTheRail(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	old := now.Add(-20 * 24 * time.Hour)
	decisions := map[ArchiveKey]bool{
		{Kind: "session", ID: "archived"}:              true,
		{Kind: "session", ID: "unarchived"}:            false,
		{Kind: "project", ID: "p-archived"}:            true,
		{Kind: "project", ID: "p-remote", Source: "h"}: true,
	}
	for _, tc := range []struct {
		name, id, project, source string
		lastActivity              time.Time
		want                      bool
	}{
		{"recent", "s", "p", "", now, false},
		{"idle two weeks", "s", "p", "", old, true},
		{"explicitly archived", "archived", "p", "", now, true},
		{"explicitly unarchived though idle", "unarchived", "p", "", old, false},
		{"in an archived project", "unarchived", "p-archived", "", now, true},
		{"another source's project decision", "s", "p-remote", "", now, false},
		{"its own source's project decision", "s", "p-remote", "h", now, true},
	} {
		if got := SessionArchived(decisions, tc.id, tc.project, tc.source, tc.lastActivity, now); got != tc.want {
			t.Errorf("%s: SessionArchived = %t, want %t", tc.name, got, tc.want)
		}
	}
}
```

`cmd/evener-hub/app_search_scope_test.go` (the two In sessions tests pass once Task 25.2's snippet lands; run them there):

```go
package hub

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/llm"
)

func searchIDs(results []appwire.SearchResult) []string {
	ids := []string{}
	for _, result := range results {
		ids = append(ids, result.ID)
	}
	return ids
}

// A live session's meta sits in the past index too. Its prompt matching lists
// it once, live, with its live state; before, it came back a second time, as
// an ended past result.
func TestHubSearchListsALiveSessionOnceAsLive(t *testing.T) {
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	liveID, endedID := hubtest.SessionID(t), hubtest.SessionID(t)
	for _, meta := range []schema.SessionMeta{
		{ID: liveID, UpdatedAt: time.Now(), Name: "Refactor the queue", OriginalPrompt: "fix the frobnitz drain", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: endedID, UpdatedAt: time.Now().Add(-time.Hour), Name: "Frobnitz audit", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	} {
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{PID: 1, WorkingDir: "/projects/alpha", SessionID: liveID, Status: appwire.ThreadStatusActive})

	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Roster: roster}, appwire.SearchParams{Query: "frobnitz"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(resp.Live); !reflect.DeepEqual(got, []string{liveID}) || resp.Live[0].State != "active" {
		t.Fatalf("live = %+v, want the live session once, working", resp.Live)
	}
	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{endedID}) {
		t.Fatalf("past = %v, want only the ended session", got)
	}
	if resp.Scope != appwire.SearchScopeAll {
		t.Fatalf("scope = %q, want the default all echoed", resp.Scope)
	}
}

// The scopes and the archived flag follow the rail (S14, spec 7.1 and 7.4):
// an explicit decision wins; with none, a session whose project is archived,
// or one two weeks without activity, is archived. Live keeps the Live
// section's sessions (live and unarchived); Archived keeps every archived
// session, live or ended.
func TestHubSearchScopesByTheRailsArchiveRules(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	alpha := hubtest.ProjectDir(t, projectsRoot, "alpha")
	beta := hubtest.ProjectDir(t, projectsRoot, "beta")
	ids := map[string]string{}
	for _, s := range []struct {
		name     string
		stateDir string
		updated  time.Time
	}{
		{"live", alpha, now},
		{"liveArchived", alpha, now},
		{"recent", alpha, now.Add(-2 * 24 * time.Hour)},
		{"old", alpha, now.Add(-20 * 24 * time.Hour)},
		{"oldUnarchived", alpha, now.Add(-20 * 24 * time.Hour)},
		{"inArchivedProject", beta, now.Add(-time.Hour)},
	} {
		ids[s.name] = hubtest.SessionID(t)
		if err := schema.SaveSessionMeta(s.stateDir, schema.SessionMeta{ID: ids[s.name], UpdatedAt: s.updated, Name: "Settle " + s.name}); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(filepath.Join(root, "index.db"))
	for _, d := range []struct {
		kind, id string
		archived bool
	}{
		{"session", ids["liveArchived"], true},
		{"session", ids["oldUnarchived"], false},
		{"project", filepath.Base(beta), true},
	} {
		if err := archive.Set("", d.kind, d.id, d.archived, now); err != nil {
			t.Fatal(err)
		}
	}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 1, SessionID: ids["live"], Status: appwire.ThreadStatusIdle},
		hubcore.LiveEntry{PID: 2, SessionID: ids["liveArchived"], Status: appwire.ThreadStatusIdle},
	)
	cfg := hubcore.WebConfig{Past: past, Roster: roster, Archive: archive}
	search := func(scope string) (live, ended []string, archived map[string]bool) {
		t.Helper()
		resp, err := hubSearch(context.Background(), cfg, appwire.SearchParams{Query: "settle", Scope: scope}, now)
		if err != nil {
			t.Fatal(err)
		}
		archived = map[string]bool{}
		for _, result := range append(append([]appwire.SearchResult{}, resp.Live...), resp.Past...) {
			archived[result.ID] = result.Archived
		}
		return searchIDs(resp.Live), searchIDs(resp.Past), archived
	}
	name := map[string]string{}
	for n, id := range ids {
		name[id] = n
	}
	names := func(got []string) []string {
		out := []string{}
		for _, id := range got {
			out = append(out, name[id])
		}
		return out
	}

	live, ended, archived := search(appwire.SearchScopeAll)
	if len(live) != 2 || len(ended) != 4 {
		t.Fatalf("all: live %v ended %v, want every session", names(live), names(ended))
	}
	want := map[string]bool{"live": false, "liveArchived": true, "recent": false, "old": true, "oldUnarchived": false, "inArchivedProject": true}
	for n, id := range ids {
		if archived[id] != want[n] {
			t.Errorf("archived[%s] = %t, want %t", n, archived[id], want[n])
		}
	}
	if live, ended, _ := search(appwire.SearchScopeLive); !reflect.DeepEqual(names(live), []string{"live"}) || len(ended) != 0 {
		t.Fatalf("live scope: live %v ended %v, want the one unarchived live session", names(live), names(ended))
	}
	live, ended, _ = search(appwire.SearchScopeArchived)
	if got := append(names(live), names(ended)...); len(got) != 3 || strings.Contains(strings.Join(got, ","), "recent") {
		t.Fatalf("archived scope: %v, want liveArchived, old and inArchivedProject", got)
	}
}

// A scope the hub does not know is refused rather than read as all.
func TestHubSearchRefusesAnUnknownScope(t *testing.T) {
	_, err := hubSearch(context.Background(), hubcore.WebConfig{}, appwire.SearchParams{Query: "x", Scope: "pinned"}, time.Now())
	if wire, ok := errors.AsType[appwire.WireError](err); !ok || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("err = %v, want InvalidParams", err)
	}
}

// The In sessions group (S14, spec 7.4): the sessions whose messages match,
// each with how many match and its newest three hits, each hit naming the
// item to open at and a one-line snippet with the matched words marked. A hub
// without a message index answers without the group, and still echoes the
// scope.
func TestHubSearchInSessionsCarriesHitsWithSnippets(t *testing.T) {
	now := time.Now()
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	var turns []schema.Turn
	for i := range 4 {
		turns = append(turns, schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("background ", 10)+"why does the settle pass race? #"+string(rune('a'+i)))))
	}
	_, sessionID := seedSearchSession(t, projectsRoot, "alpha", now.Add(-time.Hour), turns...)
	seedSearchSession(t, projectsRoot, "beta", now.Add(-time.Hour), schema.NewTurn(schema.TurnUserInput, llm.User("unrelated work")))
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	index, err := hubcore.OpenMessageSearch(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	if _, err := index.Refresh(context.Background(), messageSearchSessions(past.All())); err != nil {
		t.Fatal(err)
	}

	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, MessageSearch: index}, appwire.SearchParams{Query: "Settle race"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.InSessions) != 1 || resp.InSessions[0].ID != sessionID || resp.InSessions[0].HitCount != 4 || len(resp.InSessions[0].Hits) != 3 {
		t.Fatalf("inSessions = %+v, want the alpha session with 4 matches and its newest 3 hits", resp.InSessions)
	}
	hits := resp.InSessions[0].Hits
	for i, hit := range hits {
		if hit.TranscriptKey == "" || (i > 0 && hit.Position.Entry >= hits[i-1].Position.Entry) {
			t.Fatalf("hits = %+v, want keyed hits, newest first", hits)
		}
		var text strings.Builder
		var matched []string
		for _, part := range hit.Snippet {
			text.WriteString(part.Text)
			if part.Match {
				matched = append(matched, part.Text)
			}
		}
		if !reflect.DeepEqual(matched, []string{"settle", "race"}) || !strings.HasPrefix(text.String(), "…") || strings.ContainsAny(text.String(), "\n") {
			t.Fatalf("snippet %q marks %q, want one line opening with an ellipsis and settle and race marked", text.String(), matched)
		}
	}

	resp, err = hubSearch(context.Background(), hubcore.WebConfig{Past: past}, appwire.SearchParams{Query: "settle"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if resp.InSessions != nil || resp.Scope != appwire.SearchScopeAll {
		t.Fatalf("without an index: inSessions %+v scope %q, want no group and the scope echoed", resp.InSessions, resp.Scope)
	}
}

// The In sessions group keeps only what the scope admits, before its limit.
func TestHubSearchInSessionsFollowsTheScope(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	recent, recentID := seedSearchSession(t, projectsRoot, "alpha", now.Add(-time.Hour), schema.NewTurn(schema.TurnUserInput, llm.User("settle it")))
	_, oldID := seedSearchSession(t, projectsRoot, "beta", now.Add(-20*24*time.Hour), schema.NewTurn(schema.TurnUserInput, llm.User("settle it")))
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	index, err := hubcore.OpenMessageSearch(filepath.Join(root, "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	if _, err := index.Refresh(context.Background(), messageSearchSessions(past.All())); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Past: past, MessageSearch: index}
	for scope, want := range map[string][]string{
		appwire.SearchScopeAll:      {recentID, oldID},
		appwire.SearchScopeArchived: {oldID},
		appwire.SearchScopeLive:     {},
	} {
		resp, err := hubSearch(context.Background(), cfg, appwire.SearchParams{Query: "settle", Scope: scope}, now)
		if err != nil {
			t.Fatal(err)
		}
		if got := searchIDs(resp.InSessions); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: inSessions %v, want %v (recent %s)", scope, got, want, recent.ID)
		}
	}
}
```

In `app_search_test.go`, each `resp := hubSearch(<cfg>, <params>)` becomes:

```go
	resp, err := hubSearch(context.Background(), <cfg>, <params>, time.Now())
	if err != nil {
		t.Fatal(err)
	}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub/internal/hubcore -run TestSessionArchived -count=1` and `go test ./cmd/evener-hub -run 'TestHubSearch' -count=1`
Expected: FAIL to compile (`SessionArchived`, `SearchScopeAll` undefined; `hubSearch` takes two arguments).

- [ ] **Step 3: Implement**

`cmd/evener-hub/internal/hubcore/tree.go`:

```diff
--- a/cmd/evener-hub/internal/hubcore/tree.go
+++ b/cmd/evener-hub/internal/hubcore/tree.go
@@ -398,6 +398,18 @@ func (p TreeProject) TierRows(tier string) ([]TreeNode, bool) {
 	}
 }
 
+// SessionArchived reports whether the rail files a session as archived: the
+// source that owns it archived its project, or its own decision archives it,
+// or, with no decision, it has gone archiveWindow without activity. The Live
+// band leaves such a session out, and search's archived flag reports it
+// (S14). source is the project decision's owning source ("" for this hub's
+// own); lastActivity is the session's last-activity time, now for a session
+// with no meta, which age never archives.
+func SessionArchived(decisions map[ArchiveKey]bool, sessionID, projectID, source string, lastActivity, now time.Time) bool {
+	return projectArchivedDecision(decisions, projectID, []string{source}) ||
+		classifySession(decisionFor(decisions, sessionID), lastActivity, now) == "archived"
+}
+
 // classifySession returns a session's sidebar tier from its last activity and
 // archive decision. A user decision (archive/unarchive) overrides the auto rule;
 // otherwise inactivity older than archiveWindow auto-archives.
@@ -1597,19 +1609,16 @@ func buildTreeAtWithProjects(metas []schema.SessionMeta, live []LiveEntry, decis
 	unarchivedLive := make([]TreeNode, 0, len(liveNodes))
 	for _, node := range liveNodes {
 		entry := liveMap[node.ID]
-		if entry.Project.ID != "" {
-			// A project can merge the same ID/path across hosts, but an archive
-			// decision is source-qualified: consult only the source that owns
-			// this entry, so a different host archiving the shared project ID
-			// does not hide this host's still-live session.
-			if projectArchivedDecision(decisions, entry.Project.ID, []string{liveEntrySource(entry)}) {
-				continue
-			}
+		// A live session with no meta has no last activity to age: now.
+		lastActivity := now
+		if _, hasMeta := metaMap[node.ID]; hasMeta {
+			lastActivity = node.UpdatedAt
 		}
-		if decision := decisionFor(decisions, node.ID); decision != nil && *decision {
-			continue
-		}
-		if _, hasMeta := metaMap[node.ID]; hasMeta && classifySession(decisionFor(decisions, node.ID), node.UpdatedAt, now) == "archived" {
+		// A project can merge the same ID/path across hosts, but an archive
+		// decision is source-qualified: consult only the source that owns
+		// this entry, so a different host archiving the shared project ID
+		// does not hide this host's still-live session.
+		if SessionArchived(decisions, node.ID, entry.Project.ID, liveEntrySource(entry), lastActivity, now) {
 			continue
 		}
 		unarchivedLive = append(unarchivedLive, node)
```

`appwire/types.go`, replacing `SearchParams`, `SearchResult` and `SearchResponse`:

```go
// SearchParams selects matching live and past sessions for the hub command
// palette. An empty query returns the most recent past sessions and all live
// sessions, matching the palette's initial result set.
type SearchParams struct {
	Query string `json:"query,omitempty"`
	// Scope narrows every group of the answer (S14, spec 7.4): SearchScopeAll
	// (the default when absent), SearchScopeLive or SearchScopeArchived. An
	// older hub ignores it and answers as for all; SearchResponse.Scope says
	// whether it was applied.
	Scope string `json:"scope,omitempty"`
}

// The search scopes (S14, spec 7.4).
const (
	// SearchScopeAll is every session.
	SearchScopeAll = "all"
	// SearchScopeLive is the sessions the Board's Live section holds: live and
	// not archived.
	SearchScopeLive = "live"
	// SearchScopeArchived is the sessions the rail files as archived, live or
	// ended.
	SearchScopeArchived = "archived"
)

// SearchResult is one session hit from the hub's live or past search index.
// Ref is the qualified session reference that clients use to open the hit.
type SearchResult struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Project string `json:"project"`
	State   string `json:"state"`
	Age     string `json:"age"`
	Ref     string `json:"ref"`
	// AskPending and ApprovalPending carry the flags a navigation row does: a
	// live session is waiting on an answer to an ask_user question, or on a
	// person to allow or deny a sandbox escalation (M7). State keeps its real
	// value ("active" while an escalation blocks mid-turn), so a pending
	// approval shows only in ApprovalPending. A past (ended) result carries
	// neither. Additive: an older hub omits both, decoding as false.
	AskPending      bool `json:"askPending,omitempty"`
	ApprovalPending bool `json:"approvalPending,omitempty"`
	// Archived says the rail files the session as archived: its own archive
	// decision, its project's, or two weeks without activity (S14). Absent
	// when it is not, and from an older hub.
	Archived bool `json:"archived,omitempty"`
	// Hits are the session's newest messages that match the search, newest
	// first, and HitCount how many match in all. Only an InSessions result
	// carries them (S14).
	Hits     []SearchHit `json:"hits,omitempty"`
	HitCount int         `json:"hitCount,omitempty"`
}

// SearchHit is one message that matches a search (S14).
type SearchHit struct {
	// TranscriptKey and Position name the transcript item the message is, as
	// a thread read's items carry them, so a client opens the session at it.
	TranscriptKey string             `json:"transcriptKey"`
	Position      ThreadItemPosition `json:"position"`
	// Snippet is the message around its first match, one line, in parts: a
	// part with match set is text the search matched.
	Snippet []SearchSnippetPart `json:"snippet"`
}

// SearchSnippetPart is one run of a snippet's text.
type SearchSnippetPart struct {
	Text  string `json:"text"`
	Match bool   `json:"match,omitempty"`
}

// SearchResponse groups matching live sessions separately from persisted
// sessions so the command palette can render its two result sections.
type SearchResponse struct {
	Live []SearchResult `json:"live"`
	Past []SearchResult `json:"past"`
	// InSessions lists the sessions whose messages match, each with its hits
	// (S14), newest session first: live sessions, then ended ones. Absent when
	// none match, and from an older hub.
	InSessions []SearchResult `json:"inSessions,omitempty"`
	// Scope is the scope the answer applied. An older hub leaves it out, so a
	// client knows it offers no Archived scope and no message hits.
	Scope string `json:"scope,omitempty"`
}
```

`appwire/protocol.go`, `evener/search`'s description becomes:

```go
"Searches the hub's sessions: live and ended ones whose ID, title or prompt match, each once, and (S14) the sessions whose messages match, with each one's newest hits and snippets. A scope narrows every group; every result says whether it is archived."
```

`cmd/evener-hub/app_search.go`:

```go
package hub

import (
	"context"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

const (
	searchPastLimit = 20
	// searchInSessionsLimit bounds the sessions the In sessions group lists
	// (S14), as searchPastLimit bounds the ended sessions.
	searchInSessionsLimit = 20
	// searchHitsPerSession bounds each listed session's message hits.
	searchHitsPerSession = 3
)

// hubSearch answers evener/search: the live sessions and the ended ones whose
// ID, title or prompt match (each session once, live when it is live), and,
// with a message index, the sessions whose messages match (S14). Every group
// keeps only the sessions params.Scope admits, and every result says whether
// the rail files it as archived.
func hubSearch(ctx context.Context, cfg hubcore.WebConfig, params appwire.SearchParams, now time.Time) (appwire.SearchResponse, error) {
	scope := params.Scope
	if scope == "" {
		scope = appwire.SearchScopeAll
	}
	if scope != appwire.SearchScopeAll && scope != appwire.SearchScopeLive && scope != appwire.SearchScopeArchived {
		return appwire.SearchResponse{}, appwire.InvalidParams(`scope must be "all", "live" or "archived"`)
	}
	decisions, err := searchDecisions(cfg)
	if err != nil {
		return appwire.SearchResponse{}, err
	}
	resp := appwire.SearchResponse{Live: []appwire.SearchResult{}, Past: []appwire.SearchResult{}, Scope: scope}
	q := strings.ToLower(strings.TrimSpace(params.Query))
	// Every past match, so the scope filters before the limit cuts: the
	// newest matches are rarely the archived ones.
	var pastMatches []hubcore.PastEntry
	pastMatched := map[string]bool{}
	if cfg.Past != nil {
		pastMatches = cfg.Past.Search(q, math.MaxInt32, 0)
		for _, e := range pastMatches {
			pastMatched[e.Meta.ID] = true
		}
	}
	var live []hubcore.LiveEntry
	isLive := map[string]bool{}
	if cfg.Roster != nil {
		live = cfg.Roster.List()
		sortLiveForSearch(live, cfg.Past)
		for _, le := range live {
			if le.SessionID == "" {
				continue
			}
			isLive[le.SessionID] = true
			title := liveTitle(le.SessionID, le, cfg.Past)
			// A live session's meta is in the past index too, so a prompt or
			// working-directory match there lists it here, live.
			if q != "" && !strings.Contains(strings.ToLower(le.SessionID), q) && !strings.Contains(strings.ToLower(title), q) && !pastMatched[le.SessionID] {
				continue
			}
			if result := liveSearchResult(cfg, le, title, decisions, now); searchScopeAdmits(scope, result, true) {
				resp.Live = append(resp.Live, result)
			}
		}
	}
	for _, e := range pastMatches {
		if len(resp.Past) == searchPastLimit {
			break
		}
		if isLive[e.Meta.ID] {
			continue
		}
		if result := pastSearchResult(e, decisions, now); searchScopeAdmits(scope, result, false) {
			resp.Past = append(resp.Past, result)
		}
	}
	resp.InSessions, err = searchInSessions(ctx, cfg, params.Query, scope, live, decisions, now)
	return resp, err
}

// searchDecisions is the hub's archive decisions, or none without a store.
func searchDecisions(cfg hubcore.WebConfig) (map[hubcore.ArchiveKey]bool, error) {
	if cfg.Archive == nil {
		return map[hubcore.ArchiveKey]bool{}, nil
	}
	return cfg.Archive.Decisions()
}

// searchScopeAdmits reports whether scope keeps result: Live keeps what the
// Board's Live section holds, live and not archived; Archived keeps every
// archived session.
func searchScopeAdmits(scope string, result appwire.SearchResult, live bool) bool {
	switch scope {
	case appwire.SearchScopeLive:
		return live && !result.Archived
	case appwire.SearchScopeArchived:
		return result.Archived
	default:
		return true
	}
}

func liveSearchResult(cfg hubcore.WebConfig, le hubcore.LiveEntry, title string, decisions map[hubcore.ArchiveKey]bool, now time.Time) appwire.SearchResult {
	// A live session with no meta yet has no last activity to age.
	lastActivity := now
	if cfg.Past != nil {
		if pe, ok := cfg.Past.Find(le.SessionID); ok {
			lastActivity = hubcore.OrderUpdatedAt(pe.Meta.UpdatedAt, pe.Meta.CreatedAt)
		}
	}
	return appwire.SearchResult{
		ID:              le.SessionID,
		Title:           title,
		State:           hubcore.NormalizeState(le.Status),
		Project:         filepath.Base(le.WorkingDir),
		Age:             "now",
		Ref:             hubRefFromTreeNodeID(le.SessionID).String(),
		AskPending:      le.PendingAsk,
		ApprovalPending: le.PendingEscalation,
		Archived:        hubcore.SessionArchived(decisions, le.SessionID, le.Project.ID, "", lastActivity, now),
	}
}

func pastSearchResult(e hubcore.PastEntry, decisions map[hubcore.ArchiveKey]bool, now time.Time) appwire.SearchResult {
	return appwire.SearchResult{
		ID:      e.Meta.ID,
		Title:   searchPastTitle(e),
		State:   "ended",
		Project: filepath.Base(e.Meta.EnvInfo.WorkingDir),
		Age:     hubcore.AgeString(e.Meta.UpdatedAt),
		Ref:     hubRefFromTreeNodeID(e.Meta.ID).String(),
		// A past entry's state directory is named by its project's ID.
		Archived: hubcore.SessionArchived(decisions, e.Meta.ID, filepath.Base(e.StateDir), "", hubcore.OrderUpdatedAt(e.Meta.UpdatedAt, e.Meta.CreatedAt), now),
	}
}

// searchInSessions is the In sessions group (S14): the sessions whose messages
// match query and scope admits, live ones first in the Live order, then ended
// ones newest first, at most searchInSessionsLimit, each with its newest hits
// and their snippets. Without a message index there is no group.
func searchInSessions(ctx context.Context, cfg hubcore.WebConfig, query, scope string, live []hubcore.LiveEntry, decisions map[hubcore.ArchiveKey]bool, now time.Time) ([]appwire.SearchResult, error) {
	if cfg.MessageSearch == nil {
		return nil, nil
	}
	matches, err := cfg.MessageSearch.Match(ctx, query, searchHitsPerSession)
	if err != nil || len(matches) == 0 {
		return nil, err
	}
	var chosen []appwire.SearchResult
	seen := map[string]bool{}
	take := func(result appwire.SearchResult, isLive bool) {
		if len(chosen) < searchInSessionsLimit && searchScopeAdmits(scope, result, isLive) {
			result.HitCount = matches[result.ID].Count
			chosen = append(chosen, result)
		}
	}
	for _, le := range live {
		if _, ok := matches[le.SessionID]; !ok || le.SessionID == "" {
			continue
		}
		seen[le.SessionID] = true
		take(liveSearchResult(cfg, le, liveTitle(le.SessionID, le, cfg.Past), decisions, now), true)
	}
	if cfg.Past != nil {
		for _, e := range cfg.Past.All() {
			if _, ok := matches[e.Meta.ID]; !ok || seen[e.Meta.ID] {
				continue
			}
			take(pastSearchResult(e, decisions, now), false)
		}
	}
	var hits []hubcore.MessageHit
	for _, result := range chosen {
		hits = append(hits, matches[result.ID].Hits...)
	}
	texts, err := cfg.MessageSearch.Texts(ctx, hits)
	if err != nil {
		return nil, err
	}
	tokens := hubcore.SearchTokens(query)
	next := 0
	for i := range chosen {
		for _, hit := range matches[chosen[i].ID].Hits {
			chosen[i].Hits = append(chosen[i].Hits, appwire.SearchHit{
				TranscriptKey: hit.TranscriptKey,
				Position:      hit.Position,
				Snippet:       searchSnippet(texts[next], tokens),
			})
			next++
		}
	}
	return chosen, nil
}

func sortLiveForSearch(live []hubcore.LiveEntry, past *hubcore.PastIndex) {
	sort.SliceStable(live, func(i, j int) bool {
		return hubcore.LiveEntryWithPastLess(live[i], live[j], past)
	})
}

func searchPastTitle(pe hubcore.PastEntry) string {
	if title := strings.TrimSpace(pe.Meta.Name); title != "" {
		return title
	}
	return hubcore.ShortID(pe.Meta.ID)
}
```

`cmd/evener-hub/app_rpc.go`:

```diff
--- a/cmd/evener-hub/app_rpc.go
+++ b/cmd/evener-hub/app_rpc.go
@@ -10,6 +10,7 @@ import (
 	"os"
 	"sort"
 	"strings"
+	"time"
 
 	"primeradiant.com/evener/agent/plugin"
 	"primeradiant.com/evener/appwire"
@@ -2241,8 +2242,8 @@ func registerMiscHandlers(server *appserver.Server, cfg hubcore.WebConfig, sourc
 	appserver.HandleTyped(server.Router(), appwire.MethodEvenerUpgrade, hubUpgrade)
 	appserver.HandleTyped(server.Router(), appwire.MethodEvenerUpdateCheck, hubUpdateCheck)
 	appserver.HandleTyped(server.Router(), appwire.MethodEvenerUpdateApply, hubUpdateApply)
-	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSearch, func(_ context.Context, params appwire.SearchParams) (appwire.SearchResponse, error) {
-		return hubSearch(cfg, params), nil
+	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSearch, func(ctx context.Context, params appwire.SearchParams) (appwire.SearchResponse, error) {
+		return hubSearch(ctx, cfg, params, time.Now())
 	})
 	appserver.HandleTyped(server.Router(), appwire.MethodModelList, func(ctx context.Context, params appwire.ModelListParams) (appwire.ModelListResponse, error) {
 		return hubModelList(ctx, cfg, sources, params)
```

Create the placeholder `app_search_snippet.go` shown above. Run `$(go env GOROOT)/bin/gofmt -w` on the touched files, then `make generate`.

- [ ] **Step 4: Run them to verify they pass**

Run:
- `go test ./cmd/evener-hub/internal/hubcore -count=1`: the whole hubcore suite, since the Live band's filter now calls `SessionArchived`;
- `go test ./cmd/evener-hub -run 'TestHubSearchListsALiveSessionOnceAsLive|TestHubSearchScopesByTheRailsArchiveRules|TestHubSearchRefusesAnUnknownScope|TestHubSearchIncludesMatchingPastSession|TestHubSearchLive|TestHubSearchOrders|TestHubRPCSearch|Navigation|Archive' -count=1`;
- `go test ./internal/appwirets -run '^TestGeneratedFileCurrent$' -count=1`.

Expected: PASS. Prove the tests can fail:
- keep a live session in `past`: `TestHubSearchListsALiveSessionOnceAsLive` fails;
- drop `!result.Archived` from the Live scope: `TestHubSearchScopesByTheRailsArchiveRules` fails.

- [ ] **Step 5: Commit**

```bash
git add cmd/evener-hub/internal/hubcore/tree.go cmd/evener-hub/internal/hubcore/session_archived_test.go appwire/types.go appwire/protocol.go cmd/evener-hub/app_search.go cmd/evener-hub/app_search_snippet.go cmd/evener-hub/app_rpc.go cmd/evener-hub/app_search_test.go cmd/evener-hub/app_search_scope_test.go appwire-client/typescript/types.gen.ts docs/appwire-protocol.md
git commit -m "feat(hub): search scopes and the archived flag follow the rail"
```

### Task 25.2: Message hits with snippets

**Implementer:** Sonnet.

**Files:**
- Modify: `cmd/evener-hub/app_search_snippet.go` (replace the placeholder)
- Create: `cmd/evener-hub/app_search_snippet_test.go`

**Interfaces:**
- Consumes: `appwire.Excerpt` (on main); `searchInSessions` (Task 25.1).
- Produces: `searchSnippet(text string, tokens []string) []appwire.SearchSnippetPart`, `searchSnippetLead = 40`, `searchSnippetRunes = 160`.

- [ ] **Step 1: Write the failing tests**

`cmd/evener-hub/app_search_snippet_test.go`:

```go
package hub

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
)

func snippetText(parts []appwire.SearchSnippetPart) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(part.Text)
	}
	return b.String()
}

// A short message is the whole snippet, on one line, with every word a search
// word prefixes marked, letter case aside.
func TestSearchSnippetMarksEveryMatchingWord(t *testing.T) {
	got := searchSnippet("The Settle pass\nraces the settled drain.", []string{"settl", "drain"})
	want := []appwire.SearchSnippetPart{
		{Text: "The "}, {Text: "Settle", Match: true}, {Text: " pass races the "},
		{Text: "settled", Match: true}, {Text: " "}, {Text: "drain", Match: true}, {Text: "."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snippet = %+v, want %+v", got, want)
	}
}

// A long message is cut around its first match: some words before it, the
// rest up to the bound, both cuts at word breaks and marked with an ellipsis.
func TestSearchSnippetCutsAroundTheFirstMatch(t *testing.T) {
	text := strings.Repeat("lead ", 40) + "the settle pass " + strings.Repeat("tail ", 60)
	got := searchSnippet(text, []string{"settle"})
	line := snippetText(got)
	if !strings.HasPrefix(line, "…lead") || !strings.HasSuffix(line, "tail…") {
		t.Fatalf("snippet %q, want word-aligned cuts on both sides", line)
	}
	if n := utf8.RuneCountInString(line); n > searchSnippetRunes+2 {
		t.Fatalf("snippet is %d runes, want at most %d and two ellipses", n, searchSnippetRunes)
	}
	if lead := strings.Index(line, "settle"); lead > searchSnippetLead+2 {
		t.Fatalf("the match sits %d bytes in, want at most %d runes of lead", lead, searchSnippetLead)
	}
}

// With no word to mark (the index matched in a way the words do not show) the
// snippet is the message's opening.
func TestSearchSnippetWithoutAMatchIsTheOpening(t *testing.T) {
	got := searchSnippet(strings.Repeat("word ", 100), []string{"settle"})
	line := snippetText(got)
	if len(got) != 1 || got[0].Match || !strings.HasPrefix(line, "word") || !strings.HasSuffix(line, "…") {
		t.Fatalf("snippet = %+v, want the unmarked opening, cut", got)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./cmd/evener-hub -run 'TestSearchSnippet|TestHubSearchInSessions' -count=1`
Expected: FAIL. The placeholder marks nothing and cuts nothing.

- [ ] **Step 3: Implement**

`cmd/evener-hub/app_search_snippet.go`:

```go
package hub

import (
	"slices"
	"strings"
	"unicode"

	"primeradiant.com/evener/appwire"
)

const (
	// searchSnippetLead is how much of a message a snippet keeps before its
	// first match, so the match reads in context.
	searchSnippetLead = 40
	// searchSnippetRunes bounds a snippet's text, ellipses aside: about two
	// lines on a phone.
	searchSnippetRunes = 160
)

// searchWord is one word of a snippet's line: runes [start, end).
type searchWord struct {
	start, end int
	match      bool
}

// searchSnippet is text as one line around its first word that a search word
// prefixes, letter case aside, cut to searchSnippetRunes at word breaks, with
// every such word marked (S14). Words split as hubcore.SearchTokens splits a
// search, so the marks fall where the index matched. With no such word the
// snippet is the message's opening.
func searchSnippet(text string, tokens []string) []appwire.SearchSnippetPart {
	line := []rune(appwire.Excerpt(text, len(text)))
	words := searchWords(line, tokens)
	first := -1
	for i, word := range words {
		if word.match {
			first = i
			break
		}
	}
	start := 0
	if first >= 0 && words[first].start > searchSnippetLead {
		// Back up searchSnippetLead runes, then forward to a word's start.
		start = words[first].start - searchSnippetLead
		for _, word := range words {
			if word.start >= start {
				start = word.start
				break
			}
		}
	}
	end := min(len(line), start+searchSnippetRunes)
	if end < len(line) {
		// End at the last word break before the cut, but never before the
		// first match's end.
		for _, word := range slices.Backward(words) {
			if word.end <= end && (first < 0 || word.end >= words[first].end) {
				end = word.end
				break
			}
		}
	}
	var parts []appwire.SearchSnippetPart
	add := func(text string, match bool) {
		if text == "" {
			return
		}
		if n := len(parts); n > 0 && parts[n-1].Match == match {
			parts[n-1].Text += text
			return
		}
		parts = append(parts, appwire.SearchSnippetPart{Text: text, Match: match})
	}
	if start > 0 {
		add("…", false)
	}
	at := start
	for _, word := range words {
		if !word.match || word.start < start || word.end > end {
			continue
		}
		add(string(line[at:word.start]), false)
		add(string(line[word.start:word.end]), true)
		at = word.end
	}
	add(string(line[at:end]), false)
	if end < len(line) {
		add("…", false)
	}
	return parts
}

// searchWords splits line into its words, marking each one a token prefixes.
func searchWords(line []rune, tokens []string) []searchWord {
	var words []searchWord
	isWord := func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }
	for i := 0; i < len(line); {
		if !isWord(line[i]) {
			i++
			continue
		}
		start := i
		for i < len(line) && isWord(line[i]) {
			i++
		}
		lower := strings.ToLower(string(line[start:i]))
		match := false
		for _, token := range tokens {
			if strings.HasPrefix(lower, token) {
				match = true
				break
			}
		}
		words = append(words, searchWord{start: start, end: i, match: match})
	}
	return words
}
```

- [ ] **Step 4: Run them to verify they pass, then the gates**

Run:
- `go test ./cmd/evener-hub -run 'TestSearchSnippet|TestHubSearch|TestMessageSearch' -count=1`;
- `cd cmd/evener-hub/frontend && npm run typecheck && npx vitest run ../../../appwire-client/typescript/reducer.test.ts ../../../appwire-client/typescript/testing/fakeClient.test.ts`.

Expected: PASS. Prove `TestHubSearchInSessionsCarriesHitsWithSnippets` can fail: pass `tokens[:0]` to `searchSnippet` in `searchInSessions`.

Gates, root module:
- `go vet ./...`, `go vet -tags evenerfuzz ./...` and `GOOS=windows go vet -tags evenerfuzz ./...`;
- `golangci-lint run ./appwire/ ./cmd/evener-hub/ ./cmd/evener-hub/internal/hubcore/`;
- `make lint-generated`.

- [ ] **Step 5: Commit and open PR 25**

```bash
git add cmd/evener-hub/app_search_snippet.go cmd/evener-hub/app_search_snippet_test.go
git commit -m "feat(hub): message hits carry a one-line snippet with the matches marked"
```

Title "feat(hub): search scopes, archived flag and message hits (S14b, phase 7 PR 25)". The body names the wire additions and the capability signal (ruling 24), the dedupe (ruling 23), and that search stays on this hub (question 3).

---

## Self-review

- **Spec coverage.**
  - 7.1's notices:
    - a sign-in, as expired or refused (PRs 22 and 23);
    - a host offline, with Details (PR 22);
    - a broken plugin (PR 22);
    - one sentence naming the affected count, where the hub can count it (rulings 3 to 5);
    - dismissing themselves when resolved (the watcher, ruling 8);
    - no Reconnect (the notice has no action of its own; the phone's Details leads to Connect).
  - 13.3's alert when a notice appears: `evener/notices/changed` and stable ids.
  - 7.4's scopes (PR 25), Sessions as title and prompt matches (ruling 23), In sessions with a highlighted snippet and the hit's position (PRs 24 and 25), and Projects (the phone's, from its catalog).
  - 18's S11 and S14 rows, with their fallbacks in the handoff section. Jesse's answer 2: no "expiring within a day" and no expiry on the Providers row.
  - S12 is out of this plan, as Jesse ordered.
- **Checked by running.**
  - Every task was implemented on a scratch branch from main at `d18386ade` (`scratch/s11-s14-dryrun`, local only), and the code blocks above are rendered from that branch's commits.
  - These passed:
    - `go build ./...`;
    - `go vet` plain, with `evenerfuzz` and for Windows, on the touched packages;
    - the full `cmd/evener-hub/...`, `cmd/evener-tui`, `appwire`, `auth/...` and `internal/appwirets` suites;
    - `TestGeneratedFileCurrent`;
    - golangci-lint 2.13.1 on every touched package;
    - the frontend `npm run typecheck`;
    - the package's `reducer.test.ts` and `fakeClient.test.ts`.
  - Each red check named in the steps was run and failed as described.
- **Not run.** `make test-web`, `make test-native`, `make test-web-browser` and `make lint` (targeted runs only; nothing here changes web or phone code). Linux: every run is macOS.
- **Placeholders.** None: each task carries its code or the exact change. The one temporary stub, `searchSnippet` in Task 25.1, is shown in full and replaced by Task 25.2.
- **Names.** One spelling across tasks:
  - S11: `LiveEntry.Profile`; `HubNotice`, `NoticesListResponse`, `NoticeKind*`; `hubNotices.derive`, `runNoticeWatcher`, `keepFailedNotices`, `watchHubNotices`, `sourceIsOnline`; `RecordRefreshRejection`, `RefreshRejected`, `clearRefreshRejection`.
  - S14: `SearchTokens`; `MessageSearch`, `MessageSearchSession`, `MessageMatch`, `MessageHit`; `refreshHubMessageSearch`, `messageSearchSessions`; `SessionArchived`; `SearchScope*`, `SearchHit`, `SearchSnippetPart`, `InSessions`, `HitCount`; `searchSnippet`.
- **Review Focus.** Each line names the tests that pin it, in the task that owns the code.
- **Departures from the server plan's sketches.**
  - S11: sign-in notices come from the credential state, not failed rows, so there is no dependency on PR 9 (ruling 2). There is no `label` (ruling 1). The notification carries the list (ruling 7). S11b is the #2479 fix, as a note rather than a record field (ruling 11).
  - S14: the index reads through the transcript read model that landed as #2475, so hits carry v2 keys and there is no separate projection or byte high-water mark (ruling 16). `archived` comes from one exported rule the Live band now shares (ruling 22). There is no remote fan-out (question 3).
