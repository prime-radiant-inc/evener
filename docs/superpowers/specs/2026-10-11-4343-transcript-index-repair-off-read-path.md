# Spec: Move transcript-index full rebuilds off the daemonless read path (#4343)

Issue: [#4343](https://github.com/prime-radiant-inc/evener/issues/4343)
Branch: `fix-4343-stale-sidecar-repair` (base `a681b28565`)
Revision 7 (2026-10-12): folds every adjudicated finding from review
round 5 (the legacy-transcript residue stated honestly; the stale-mark
clearing scoped to foreign-CURRENT adoption only; the exhausted-path
refresher re-arm interval; the re-arming read's own answer; the perf-lane
claim returned to parity; the wire error's JSON-RPC code named) on top
of revision 6's round-4 fold.

Agreed direction (Jesse, 2026-10-11): the first read that finds a
stale-identity sidecar triggers one owned background repair off the read
path and answers immediately with a structured retryable "rebuilding"
state; concurrent readers dedupe onto that single repair instead of
parking on the exclusive lock.

## Why (incident summary)

A hub read of a daemonless session takes the sidecar's lock at several
depths, and today every one of them can run a full inline rebuild or
block behind one:

- The read path `withPastTranscriptIndex` (`cmd/evener-hub/app_threadread.go:304-317`)
  acquires the session's index through the hub's handle cache. A cache
  miss opens the sidecar with `Open`, whose contract is "a missing, stale
  or corrupt index is rebuilt from the transcript"
  (`internal/transcriptindex/index.go:243-260`) — so on a cold cache
  (after a hub restart, or eviction from the 64-handle LRU) the first read
  rebuilds inside `Acquire`, and concurrent acquires of the same path park
  on the cache's ready channel (`internal/transcriptindex/cache.go:93-99`).
- The explicit `CatchUp()` the read then runs takes the sidecar's
  exclusive lock and extends; `extend` rebuilds inline at five call sites
  (`internal/transcriptindex/index.go:457-458, 489-497, 510-513, 515-524,
  550`): the covered-prefix branch (no live build / stale build / the
  transcript no longer the covered file grown by appends), the mid-scan
  `errRebuild` fallback, leftover update rows from an interrupted
  extension, the defensive pending-communicate rebuild, and a
  `restoreBuilder` failure inside `repairBuilder`.
- The read's window step re-takes the lock (`readWindow`,
  `internal/transcriptindex/window.go:131-154`, and the shared-lock reads
  at `window.go:79, 96, 135`): blocking, and — when the builder is stale
  — exclusive, running `repairBuilder`, which can reach the same fifth
  rebuild site (`window.go:150`). A corrupt record found mid-read marks
  the index stale and returns the raw error (`fail`, `window.go:397-403`).
- The 60s message-search refresher reads changed transcripts through the
  same rebuild-capable `Open`
  (`cmd/evener-hub/internal/hubcore/message_search.go:374`), so it too
  can hold the exclusive lock for a full rebuild, unattended, every cycle.

In the incident, a 510MB / 15,213-turn transcript's stale-identity
sidecar (a schema-fingerprint change landed while the session's
week-old daemon kept extending it) rebuilt inline for ~12 minutes under
the exclusive lock: every reader on every connection parked,
per-connection read slots (16) and inbound queues (64) filled, keepalives
died, and the torn-down connections discarded queued frames (companion
issue #4342). Measured on a state copy with current code: a
healthy-sidecar cold past read costs 8.8-10.2s, warm 17ms; after a repair
the path is entirely healthy. The defect is the multi-minute inline
rebuild landing on reads. (The unbuilt open also removes today's
redundant double catch-up on every cold read: one inside `Open`, one at
`app_threadread.go:313`.)

## Scope

1. `internal/transcriptindex` — refusing handles (new exported API;
   existing behavior unchanged):
   - New sentinel `ErrRebuildRequired`: an operation needed a full
     rebuild and the handle refuses to perform one. The refusal carries
     the ordinal of the last entry the refused build had recorded — its
     committed entry count minus one, matching `TranscriptHistoryFailed`'s
     documented semantic ("naming ordinal, the last entry it had
     recorded", `appwire/errors.go:676-686`) — and the refused build's
     own name, both read at refusal time from
     the live build's meta.json and CURRENT pointer, published by atomic
     rename, so the read is tear-safe and still holds the committed
     count even when a discarded mid-scan had advanced the in-memory
     one; an empty name is the no-live-build refusal, 0 entries
     likewise. (The identity mismatch itself discards the parsed
     meta the refusing path never adopts, `index.go:362-364`, so the
     disk is the only source.)
   - New sentinel `ErrLockBusy`: the sidecar's lock is held by another
     writer and the handle refuses to block on it. Contention alone
     translates to it: the platform's contention errno (`EWOULDBLOCK`,
     `ERROR_LOCK_VIOLATION`) and nothing else, so a fault on the lock
     itself — a dead fd, an exhausted lock table — wraps and surfaces
     as an ordinary error; a masquerading fault would ride the
     refresher's silent lock-busy skip and the hub's transient
     classification forever, undiagnosable.
   - `OpenRefusing(path, dir)`: opens a handle that performs no full
     rebuild and never blocks on the sidecar's lock, in any operation —
     catch-up, extension, window reads, repair-builder paths alike. It
     opens without catching up; the caller runs `CatchUp()` explicitly.
     The policy is a property of the handle, armed at open and consulted
     at four points: the lock acquisition in `locked` (a new
     non-blocking `lockFileTry`, in both `lock_unix.go` and
     `lock_windows.go`; contention returns `ErrLockBusy`); the top of
     the internal rebuild (`x.rebuild`, `index.go:720`, the choke point
     every one of the five call sites — and `Index.Rebuild` — funnels
     through; on a refusing handle it returns `ErrRebuildRequired`);
     for the leftover-update-rows trigger only, the trigger site
     (`index.go:478`), where `extend` has already collected the unsafe
     offsets but has not yet truncated the tables or scanned — a refusal
     there precedes every mutation that trigger would perform (this can
     refuse where a completed scan would have healed, which converges
     through the repair), which marks the build stale before its
     refusal: the rows prove a writer died mid-update and the loaded
     build's committed slots already carry what it flushed, so the
     same discipline must hold; and after a successful refresh in
     `locked`, where a handle whose refused fallback rebuild or
     leftover-rows refusal marked its build stale refuses every
     operation — a half-applied scan's items sit past the committed
     counts in that handle's memory, or its committed slots carry a
     crashed writer's flushed update past the committed snapshot — and
     no window or change read may serve them under the old snapshot
     length —
     until the refresh adopts the repair's committed build, the load
     that clears the mark. The sentinel is the refusal-mode translation
     of the internal `errRebuild` the scan already uses
     (`build.go:23`); the internal sentinel keeps its mid-scan meaning.
   - On a refusing handle, a read that fails because the sidecar is
     corrupt or no longer matches its transcript — `fail`'s errCorrupt
     path (`window.go:397-403`) and reader-mode refresh mismatches
     (`index.go:314-318`) — returns `ErrRebuildRequired` (so the repair
     registers immediately) rather than the raw error. Failures a
     rebuild cannot fix (I/O errors) stay ordinary errors.
   - Stale marks are a property of the build they were set on, not of
     the handle: refresh's foreign-CURRENT adoption path (`index.go:331-337`)
     — a different build was committed, by a repair or any foreign
     writer — clears them. Today the only clear site is a successful
     rebuild on the same handle (`index.go:756`), which a refusing
     handle never performs; without this rule, a cached refusing handle
     keeps refusing forever after its repair commits (each read
     re-refuses, re-registers, and never converges). The same-build
     changed-meta adoption (`index.go:344-348`) keeps the marks:
     extend's checks are blind to record corruption, so a stale-marked
     ordinary handle must still rebuild at its next extension exactly
     as today's covered-prefix comment promises (`index.go:236-238`) —
     and a committed repair always mints a new build directory
     (`index.go:733-745`) and repoints CURRENT, so the post-repair
     adoption that must converge is always the foreign-CURRENT path.
   - Write behavior: the covered-prefix, `restoreBuilder`, and
     leftover-rows refusals write nothing. The mid-scan `errRebuild`
     fallback is the one trigger whose refusal can follow a discarded
     prefix-scan (knowable only at the entry), and the
     pending-communicate trigger refuses after the scan wrote records
     but before meta commits: uncommitted records past meta's counts,
     which the sidecar already tolerates by design (`index.go:95-98`).
     No partial CURRENT swap can happen: a rebuild publishes CURRENT
     only after meta commits (`index.go:772-778`).
   - `Open`, `CatchUp`, `CatchUpTo`, and `Rebuild` keep their current
     behavior for daemon and existing callers, with one stated
     exception: adopting a foreign build through refresh now clears the
     stale marks (the rule above); today they survive into a redundant
     rebuild (`index.go:457`'s x.stale branch, cleared only at
     `index.go:756`).

2. Hub index cache: `transcriptindex.Cache` gains one injection point for
   its open flavor — a constructor variant taking the open function,
   with `NewCache` unchanged, defaulting to `Open`, for the server-side
   registry that depends on today's catch-up-and-rebuild contract
   (`server/thread_histories.go:47`). The hub's `pastTranscriptIndexes`
   is constructed with `OpenRefusing`, so no cache miss can rebuild and
   no in-flight open can park an acquirer behind a rebuild (an unbuilt
   open is milliseconds). The hub cache's callers are the read path
   (which always catches up explicitly) and `Forget` (project deletion,
	   which does not read). A handle parked in the cache releases the
	   in-memory record buffers its open loaded: the buffering serves
	   active builds and extensions, and idle retention would change the
	   cache's memory bound from handles and headers to complete sidecars
	   (whole record files, up to the per-table buffer limit, three tables
	   per handle, times the cache's capacity). A re-acquired handle reads
	   and extends through the files.

3. Hub past read path (`app_threadread.go`). `withPastTranscriptIndex`
   keeps today's shape — acquire, `CatchUp()`, run — and adds one
   error mapping at its end:
   - `ErrRebuildRequired`: consult the transcript-header gate first —
     hubcore exports the check the message-search refresher already
     uses (`message_search.go:369-373`, where it exists to keep `Open`
     from leaving an empty sidecar directory beside a legacy
     transcript). A transcript whose header cannot be this format can
     never be indexed: answer the existing `transcriptHistoryFailed`
     history-read error directly (ordinal 0), register nothing, and
     show no repairing status — no false "Repairing" for a session that
     can never be repaired, no doomed repair attempts. The check runs
     only on this refusal path; the healthy path never pays it. What
     the gate cannot prevent: the refusal surfaces after the open, and
     OpenRefusing creates the sidecar's directory and lock file to
     refuse through them (`index.go:247-250`'s shape; a legacy
     transcript and a never-indexed session are indistinguishable at
     refusal time). The gate's promise is no build directory, no
     CURRENT write, no repair, and no false status — the dir-and-lock
     residue matches today's read path (which leaves it too, plus a
     failed rebuild's partial build directory).
     Otherwise: register or join the single-flight background repair
     for this transcript path and answer the typed wire error
     immediately. One repair per path; concurrent registrants join,
     none start a second.
   - `ErrLockBusy`: consult the registry (an in-memory map lookup; the
     healthy path still never touches it). A repair in flight for this
     path answers the same typed rebuilding error, status included —
     during our own repair the lock is held by the repair, so this is
     the path concurrent retries take, and the answer must keep telling
     the truth. Contention with no in-flight repair — a writer
     mid-extension, or a foreign rebuild — answers the typed
     lock-contention answer (retryable, nothing registered), and each
     client's retry owner converges it: the web's tracked
     ordinary-failure retry, and the TUI's paced entry retry, which has
     no ordinary-error retry and would otherwise strand a first entry
     on the bare error.
   - Repair registry: keyed by transcript path, in package hub, armed
     with the server's lifetime at construction — a directly constructed
     server (`NewWebServer` plus `Handler()`, no runMain) recovers a
     staled or missing sidecar under the default lifetime on its own;
     `runMain` re-wires with the hub's background context, so shutdown
     still gates repairs. Its
     single-flight start must never block a registrant: a mutex plus an
     in-flight map (or `singleflight`'s `DoChan` with the result
     channel discarded — the hub's `Do` usage, `app_models.go:225`,
     hands joiners the leader's result, which would park a reader for
     the repair's duration). Project deletion drops the path's entry
     alongside `Forget` (`project_delete.go:676-680`), and a repair
     completion landing on a dropped entry is a no-op — the completion
     records only into a known entry, so a deleted session leaves no
     registry residue, not even from an in-flight repair finishing
     after the deletion.
   - Failure budget: the registry counts consecutive failed repairs per
     path, keyed by the transcript's (size, mtime) stamp (hubcore's
     `transcriptStamp`, `message_search.go:150-162`, exported for this),
     computed only when the registry holds an entry for the path so
     healthy reads pay no stat. After 3 consecutive failures for the
     same stamp — the count mirroring the daemon's
     `threadHistoryMaxRebuilds` (`server/thread_history.go:53-57`) —
     reads stop returning the rebuilding answer and return the existing
     `transcriptHistoryFailed` history-read error instead (stamped with
     the daemonless identity as today), passing the refusal's carried
     ordinal (`appwire/errors.go:665-675`). Precedence: a repair in
     flight answers the rebuilding code even on an exhausted path — the
     status must stay truthful while a repair actually runs. The read
     that re-arms answers `transcriptHistoryFailed` itself; the
     rebuilding answer is what subsequent reads see while the re-armed
     repair runs (the re-arm does not flip its own trigger's answer).
     Refresher-fed re-arms of an exhausted path are paced: the registry
     records each exhausted path's last refresher-fed re-arm, and the
     lane skips the path until ten minutes have passed (a named
     constant), without recording a failure — a deterministically
     failing path can no longer monopolize the single lane cycle after
     cycle, while a transiently failing one still heals through the
     lane within the interval.
     The budget's job is to stop the already-open latest-window owner's
     unbounded retry loop: the history-failed answer is the client's
     existing no-auto-retry diagnostic, but that branch requires a base
     model (`threads.ts:2326`), so it stops the loop only for
     already-open surfaces — and it leans on the client's current
     suppress-retry branch; when friction.md C03's recorded decision
     (retrying with backoff even after structured history failures) is
     implemented, single-flight and repair-duration pacing remain the
     bounds, still strictly better than today's inline attempts. A budget-exhausted first open has no base
     model and keeps its pre-existing silent retry shape, and
     older-page demand classifies `transcriptHistoryFailed` as
     unresolved (`historyPaging.ts:111`: permanent is only
     `upgradeRequired` or `targetDeleted`) and keeps demanding — both
     keep re-arming repairs, bounded by single-flight (one repair per
     path at a time, and while one runs its answers are the rebuilding
     code) and by repair duration, which is strictly better than
     today's shape, where the same loops drive full inline rebuild
     attempts on the request path under the exclusive lock. A later
     read of an exhausted path re-arms exactly one repair (off-path,
     single-flight), mirroring the daemon's `recoverForRead`
     (`server/thread_history.go:364-428`); a successful repair clears
     the budget. No new wire code for this state.

4. The repair: a deliberately detached goroutine — the `startBackground`
   group is awaited at shutdown (`main.go:818-826`), and a multi-minute
   rebuild must not hold hub exit — started only while the hub's
   background context is live. Its recipe, in order:
   - Stat the transcript first; a vanished transcript (a deleted
     project's session) aborts the repair without creating anything —
     `Open`'s `MkdirAll` and lock-file creation (`index.go:247-260`)
     would otherwise resurrect a deleted path's sidecar directory
     before `CatchUp`'s stat (`index.go:276-280`) fails.
   - Open an ordinary handle WITHOUT catching up (the same
     open-without-catch-up shape `OpenRefusing` uses, minus the
     refusing policy; `Open`'s built-in catch-up would either duplicate
     the rebuild for identity triggers or no-op for corruption). It
     must not use the cached handles: they carry the refusing policy,
     which would refuse the repair's own rebuild.
   - Call the revalidating rebuild exactly once —
     `RebuildIfStillRefused(length, refusedBuild)`, with the stat's size
     and the refused build the registration's refusal named — the one
     place a full rebuild legitimately runs, off the request path, under
     the blocking exclusive lock, the check and the rebuild one step —
     and close the handle. A registration can land long after its
     refusing read answered (the refusal path's own header gate reads
     the transcript in between), and in that window another repair can
     already have committed: rebuilding the healthy sidecar then would
     re-pay the full cost and rotate an incarnation readers hold, so
     the recipe rebuilds only while the named build is still the live
     one and adopts the committed outcome otherwise. That skip is not
     the catch-up-only no-op the livelock argument above rules out: it
     fires when the sidecar's live build is no longer the named one —
     another repair committed a fresh build, or no build is live at all
     (a deletion that won the race mid-recipe; the next read re-arms
     with the empty name and the repair after that rebuilds) — and
     never while the still-named corrupt or leftover-rows build stands:
     `extend`'s incremental checks cannot see record-level corruption
     (the covered-prefix branch at `index.go:457` passes a corrupt
     build's identity, size, and growth shape), so the livelock
     argument stands — no revalidation path ever answers a corrupt
     build with success. The same uniform full-rebuild contract serves
     every trigger, once per staleness; the leftover-rows trigger's
     cheaper truncate-and-rescan is traded away for it.
   Hub exit abandons an in-flight repair exactly as a crash would,
   which Scope 1's commit rule makes harmless. Both of the recipe's
   outcome paths owe the deletion contract: a rebuild that fails while
   the transcript is gone removes the sidecar directory, finishing the
   deletion the open's resurrection interrupted — and so does a
   revalidation skip, whose resurrecting open left the same artifacts
   behind. With no transcript at the path no live session can own the
   directory, and a transcript that exists again is a new session's,
   left entirely to it. On completion the
   registry records success (clearing the budget) or failure
   (incrementing it) — into the path's entry only if it still exists.

5. Typed wire error (`appwire/errors.go`): a new `ErrorInfo` code
   (`transcriptIndexRebuilding`) on `CodeInternalError` — matching
   `TranscriptHistoryFailed`, the closer analogue, a server-side
   condition, rather than `TranscriptItemCursorStale`'s
   `CodeInvalidParams`; clients match the `evenerErrorInfo` string,
   never the code — with a message naming what is happening, and
   `ErrorData.RetryDisposition = RetryDispositionAutomatic`, carried
   through the existing `daemonlessReadError` stamping (daemonless boot
   generation, epoch 0) on `thread/read` and `thread/turns/list`.

6. Clients:
   - Shared TypeScript client (`appwire-client/typescript/errors.ts`):
     export the new code constant in the established pattern, with the
     binding test reading the Go constant.
   - Browser store (`cmd/evener-hub/frontend/src/stores/threads.ts`):
     the new code is deliberately outside
     `isTranscriptHistoryFailedError` (which matches only
     `transcriptHistoryFailed`), so hydrations land in the store's
     `scheduleOwnedHydrationRetry` paced retry-forever owner on both
     first open and already-open sessions (base 500ms, cap 15s,
     `threads.ts:916-923`). The status lives in the store as a per-ref
     flag (the `deletedRefs` pattern): set when the latest read of the
     ref failed with the rebuilding code, cleared by the next read of
     the ref that does not fail with the rebuilding code (a successful
     read or any other failure — including the budget-exhausted
     `transcriptHistoryFailed`, which rides the store's existing
     history-failed diagnostic and never a false repair status). The
     rule is per read kind: a watched read that requests history
     publishes its verdict like a tracked one, while a metadata-only
     (lean) watched read — which the hub can answer without touching
     the transcript index — neither advances the flag's ordering
     sequence nor publishes any verdict except an explicit rebuilding
     answer it received itself, so a lean watch can never clear or
     silence a repair status another read owns. The
     session's loading surface reads the flag and shows the concise
     status "Repairing session history" (agreed with Jesse, 2026-10-11).
     A budget-exhausted already-open session with a model shows the
     existing history-failed diagnostic and stops auto-retrying; a
     budget-exhausted first open has no model for the diagnostic and
     keeps its pre-existing silent retry shape (the pre-existing
     first-open shape for terminal history failures, which this PR
     records but does not change; friction.md C03 tracks the
     already-open gap it does address, and Scope 3 states what those
     retained loops do to repair pacing).
   - The older-page demand owner (`HistoryPaging`) classifies the new
     code as unresolved (retryable), never permanent — permanent is
     only `upgradeRequired` or `targetDeleted` (`historyPaging.ts:111`),
     so this holds by construction; the test plan pins it anyway.
   - TUI (`cmd/evener-tui`): session entry reads that fail with the
     rebuilding or lock-contention code retry under the same paced owner
     the browser store uses (base 500ms, cap 15s, retry-forever), scoped
     to the current navigation intent. An entry failure leaves the model
     on the surface it was on — the dashboard, entering from the session
     list — so the status shows on the model's error line, the surface
     that would otherwise carry the entry failure: "Repairing session
     history" instead of the error, standing while the retry stands. Any
     other outcome replaces it: a successful read enters the session; a
     history-failed answer — or one of the web owner's permanent
     answers, upgradeRequired or a target deletion — ends the paced
     retry and shows the error on the same line (the TUI's own rule —
     its error line is the diagnostic; the budget's terminal answer
     deserves it); an ordinary transient failure keeps the paced retry
     armed with the status standing, mirroring the browser owner —
     mid-repair lock contention with no repair in flight answers the
     typed lock-contention code, which arms the same paced retry, so the
     entry must not strand on it. The
     automatic completion holds for the displayed session's unsent work
     and open overlays exactly as the cycling switch and focus trap
     do — and for the standing spawn form's prompt, edited since the
     submission that armed the retry: a successful re-read that would
     switch away from a composer
     draft, pending attachments, or a panel or picker the user is
     interacting with, or replace an edited spawn form, or close an open
     transcript viewer, retires the retry
     instead, restores the displayed session's subscription when one is
     displayed, and leaves the entry for the
     user to re-pick. Navigating away — entering
     another session, opening another pane, selecting a transcript to
     open (at the selection itself, before its read lands), leaving the
     hub surface, or
     submitting the spawn form again — retires the pending retry and its
     status, and a late-landing retry
     for a departed intent enters nothing. Picks are fenced the same way
     from the issuing side: an entry read a navigation pick issues
     carries the generation it was issued under, and a response that lands
     after a newer navigation — a second pick, a cycling press, a
     reconnect — is discarded whole, entering nothing and arming nothing,
     with the displayed session's subscription re-established when one is
     displayed: deferred while a newer navigation read is still in
     flight, so the reconciliation never races the newer read's own
     replacement, and settling with that navigation's landing — or, when
     that landing is a retryable failure that arms or keeps the paced
     retry, settling alongside the retry it schedules, so the displayed
     session is not left unsubscribed through the retries the repair
     paces. The same restore covers the flight that needs no superseded
     landing at all: a replacing read whose thread/read leg succeeded
     re-points the server-side subscription at its ref before its older
     pages fail, so the failure's own handling — the arming, the armed
     failure, or the terminal settle — restores the displayed session's
     subscriptions beside it, whatever the error class. A hub reconnect is not a departure: the pending retry
     survives it, rebound to the replacement connection's generation, so
     its paced read resumes on the new connection while the dead
     connection's late responses land as superseded. The TUI's
     already-open surfaces keep their existing re-entry points (the
     C03-class already-open gap the native section records): the
     displayed session's /details and clear re-reads and the raw
     transcript viewer's past-session reads surface their one-shot errors
     as today, arming no retry and showing no status.

7. Message-search refresher (`hubcore/message_search.go`):
   `readTranscriptItems` opens its own refusing handle (not the hub's
   shared cache, keeping its no-eviction discipline) and runs
   `CatchUp()`. On `ErrRebuildRequired` or `ErrLockBusy` the refresh
   cycle skips the session — the same silent skip the cursor-stale race
   already uses (`message_search.go:298-305`): no failure record, no
   inline rebuild, no lock-hold — and, on `ErrRebuildRequired`, asks
   the hub's registry to start the shared repair through one background
   lane: at most one refresher-triggered repair runs hub-wide at a
   time, so a fleet-wide staleness (a schema-fingerprint change) trickles
   in rather than bursting; a lane already busy retains the ask — one
   per path, the newest refusal's identity — and serves it as the
   running repair retires, so an early session that goes stale again
   every cycle cannot take the lane each time and leave the sessions
   behind it unindexed (its successful repairs delete its entry, so
   the exhaustion pacing never applies to it); a path already in
   flight just skips, retrying next cycle, and a path whose budget is
   exhausted paces at the take exactly as at the ask. Client reads
   trigger their
   own path's repair immediately, independent of the lane, so no user
   waits behind the fleet. The lane and its single slot live in
   package hub; the refresher's registry ask crosses hubcore→hub
   through an injected callback on `MessageSearch` (the package's
   established seam — its `read` field, `message_search.go:96-98`),
   wired at hub startup. This keeps today's coverage — every session
   the past index lists, read or not, eventually searchable, including
   after budget exhaustion (the refresher's next cycles re-arm through
   the lane, paced by the exhausted-path interval in Scope 3). A live old-identity daemon that re-stales a repaired
   sidecar makes the next read or refresh cycle trigger the next
   repair for it. That fight is real and this mechanism only paces
   it: refresher-fed repairs are bounded in concurrency by the single
   lane, demand-fed repairs by the readers actively reading the
   session, and frequency by real demand — never by a global count.
   Nothing today resolves it: the session fence is protocol-based
   (`restartRequiredDaemon`, `app_restart_required.go:34-36`) and the
   projection schema fingerprint is a separate construct folded into
   the sidecar's identity (`schema_identity.go:40-42`), so a
   protocol-current daemon carrying a stale schema fingerprint is never
   fenced — the incident's own daemon. Issue #4368 tracks that gap.

8. Native clients: unchanged in this PR. An already-open native session
   that receives the rebuilding code waits for its existing re-entry
   points (reconnect, reopen, or another user action) — the already-open
   gap friction.md C03 records for history failures generally. No native
   code matches the new code and none is added.

9. Hub-side observability, in the established `[hub]` log style, naming
   the session: one advisory line when a background repair starts, one
   when it completes (carrying the outcome: finished or failed), and one
   when a path exhausts its repair budget — written through the sink
   resolved when the registry is armed, `hubLogfFor`'s rule, so a detached
   repair never follows a late `os.Stderr` reassign into an unintended
   or closed sink. The incident was only
   diagnosable from mtimes and CPU burn.

10. Evergreen docs: update the S10 row in `docs/product/subsystems.md`
    to the true contract — a daemonless read never runs or waits behind
    a full rebuild; a read that needs one answers a typed retryable error
    while one owned background repair runs, and a live writer sharing
    the sidecar still serializes behind that repair as behind any
    rebuild — and add the new code to the errors paragraph in
    `docs/appwire-protocol.md`. C03 stays open: it is the client-side
    already-open retry gap for history failures generally, and this
    change does not resolve it.

## Behavior

- Daemonless `thread/read` and `thread/turns/list` whose sidecar extends
  incrementally behave exactly as today, at every lock depth: the
  catch-up and the window reads take non-blocking locks and proceed when
  free. A read colliding with a writer for the milliseconds of an
  extension answers the typed lock-contention answer (retryable, nothing
  registered) rather than blocking, and each client's retry owner
  converges it.
- No daemonless read ever performs a full rebuild inline, on any
  trigger, at any lock depth, warm or cold cache; and no daemonless read
  ever blocks on the sidecar lock: an in-flight repair, a daemon's own
  inline rebuild, or a concurrent refresher read is answered immediately
  instead of waited on.
- Exactly one repair per transcript path runs at a time, off the
  request path under the exclusive lock, and always rebuilds from the
  transcript — the authority — through the revalidating rebuild:
  full and unconditional while the sidecar's live build is still the
  one the registration's refusal named, and a skip that adopts the
  committed outcome only when another repair already replaced it. Reads
  that arrive during the repair answer the typed rebuilding error, from
  the refusal or from the busy branch's registry lookup alike, so the
  status stays truthful for the repair's whole duration. After the
  repair commits, the cached handle adopts the new build through
  refresh — its stale marks clear with the adoption — and subsequent
  reads succeed with correct data, paging cursors, and snapshot
  identity as today, until an old-identity writer re-stales the
  sidecar, which re-arms the mechanism for the next read.
- Repairs are triggered by client reads (immediately) and by the
  refresher's cycle (through the single background lane).
  Demand-triggered repairs are bounded only by concurrent reader demand
  — each is a session a user is actively reading — and hub-wide CPU
  saturation under that load is issue #4344's concern, not this
  mechanism's. The failure budget (Scope 3) stops the already-open
  owner's unbounded retry loop at three consecutive failed repairs per
  unchanged transcript by switching the answer (when no repair is in
  flight) to the no-auto-retry history-failed diagnostic; exhausted
  paths still re-arm from later reads and refresh cycles, and a repair
  in flight still answers the rebuilding code.
- A live daemon extending the same sidecar serializes behind the
  repair's lock hold, as it does behind any rebuild today: its
  projection goroutine waits (its bounded queue absorbs the pause, and a
  long enough stall can overflow it toward the daemon's own failed
  state — the incident's daemon lived this against the inline rebuild).
  The repair does not add a stall today's path does not already have —
  today's inline rebuild imposed the same hold on the same writers —
  and reads never wait behind it. (A separate rebuild-performance
  branch, merged into this lane before implementation, reduces the
  hold itself; the stall argument stands on parity alone.) Fencing or
  forcing a restart of a
  protocol-compatible daemon is out of scope — issue #4368 tracks that
  the protocol-based fence cannot see a schema-only mismatch at all.
- The "Repairing session history" status follows the operable rule in
  Scope 6 on the browser: shown while the latest read of the ref failed
  with the rebuilding code, cleared by the next read that does not. The
  TUI's status follows its own owner's lifetime instead — shown while
  the navigation-scoped entry retry stands, replaced by any other
  outcome — because the TUI has no per-ref store flag; the retry's
  lifetime is the flag.
- A first read of a never-indexed session (no sidecar at all) answers
  the rebuilding state for one retry interval while its repair builds —
  the same uniform path, at the cost of one extra retry round trip
  (≥500ms) against today's inline build; the status text is accurate:
  its history is being built. A transcript whose header cannot be this
  format (a legacy transcript) never shows the status: its first read
  answers the history-failed error directly (Scope 3's header gate).

## Non-goals

- Daemon-side catch-up behavior: daemons still extend and rebuild their
  own live sidecars inline.
- Hub CPU saturation and rendezvous starvation (#4344), and
  teardown-frame discard visibility (#4342): separate issues, worked
  separately.
- Resolving the re-stale fight — fencing, winding down, or forcing a
  restart of a protocol-compatible daemon with a stale schema
  fingerprint: a product decision tracked as #4368, not silently
  owned by this mechanism.
- Making the rebuild itself fast: a separate lane is profiling and
  fixing the rebuild's per-entry cost on its own branch; this spec
  makes who waits for it honest regardless of its speed.
- Yielding the sidecar lock between rebuild stages to shorten the
  writer stall: the build-then-rename commit is designed for one
  continuous hold; if the residual stall matters after the perf lane's
  fixes, it is its own issue.
- New UI beyond the one agreed status line: no banners, toasts, or
  retry controls. The loading surface plus "Repairing session history"
  is the whole client-visible addition; the paced retry is invisible.
- Native client changes: none (Scope 8 records the accepted
  consequence).

## Recovery ownership

Authoritative state: the transcript file; the sidecar is derived
storage, rebuilt from it. The recovery flow — retry ownership, the
failure budget's fallback, and the refresher's pickup — is as specified
in Scopes 3, 6, and 7. Smallest affected scope: the one transcript
being repaired.

## Test plan (whole journey)

1. `internal/transcriptindex` unit tests (red first):
   - a refusing handle returns `ErrRebuildRequired` on each rebuild
     trigger reachable from the catch-up path: covered-prefix variants
     (identity-stale sidecar, replaced transcript file, no live build),
     the mid-scan `errRebuild` fallback, the leftover-update-rows
     trigger, the pending-communicate trigger, and a `restoreBuilder`
     failure — and on the window-path `restoreBuilder` failure
     (builder stale, `window.go:150`'s exclusive step);
   - the refusal carries the ordinal of the last entry the refused
     build had recorded (committed count minus one; 0 for no live
     build; the value survives even when the refusing path never
     adopts the meta);
   - the covered-prefix, `restoreBuilder`, and leftover-rows refusals
     leave the sidecar byte-identical (table files included, not just
     no new build directory and no CURRENT change); the mid-scan
     refusals may leave uncommitted records past meta's counts and
     never swap CURRENT;
   - a corrupt record found by a window read returns `ErrRebuildRequired`
     on a refusing handle (not the raw error), and still marks the
     index stale;
   - corrupt-record convergence: after the refusal (stale mark set), a
     foreign handle's `Rebuild` commits a fresh build, and the refusing
     handle's next operation adopts the new build through refresh with
     its stale marks cleared — the read succeeds where the pre-change
     code would have refused forever (the only other clear site being
     a rebuild on the same handle, `index.go:756`);
   - with the sidecar's lock held, a refusing handle's catch-up and
     window reads return `ErrLockBusy` without blocking;
   - an ordinary handle's `CatchUp` still rebuilds in all the same
     cases, and `Open` still catches up and rebuilds (existing behavior
     pinned by test); an ordinary handle marked stale that adopts a
     foreign build proceeds without rebuilding (the marks cleared with
     the abandoned build), while same-build marks survive a changed-meta
     adoption and still rebuild at the next extension;
   - stale-identity fixtures are hand-authored (a CURRENT pointer plus a
     `meta.json` whose Projection string is a foreign
     `projectionID:schemaID`, `schema_identity.go`): seeding through any
     open path repairs the sidecar and would make the test vacuous.
2. Cache tests: a cache constructed with `OpenRefusing` opens on a miss
   without catch-up (no rebuild counter movement, no build directory);
   `NewCache` keeps `Open`'s behavior; concurrent acquires of one path
   share one open; eviction, `Forget`, and `Close` unchanged.
3. Hub unit tests with the past-read harness
   (`cmd/evener-hub/app_threadread_index_test.go`), hand-authored stale
   sidecar:
   - cold cache: the first `thread/read` answers the typed error
     promptly, no rebuild ran inline, and exactly one repair registered;
   - warm cache, stale again: the same typed answer;
   - with a controllable repair seam held open, N concurrent reads all
     receive the typed error (the busy branch's registry lookup answers
     the typed error while the seam holds the lock) and exactly one
     repair runs; releasing the seam lets the next read return correct
     data with today's response shape, cursors, and boot generation;
   - lock contention with no repair in flight (a held foreign lock)
     answers an ordinary transient error, registers nothing, and sets
     no repairing status;
   - the repair runs the exported `Rebuild`, not a catch-up (a seam
     asserts the rebuild itself ran) — including for the corrupt-record
     trigger, where a catch-up-only repair would no-op and record
     success;
   - a legacy-format transcript (header rejected by hubcore's check)
     first-reads with the history-failed error directly: no repair
     registered, no repairing status, no build directory and no
     CURRENT written — the open's dir-and-lock-file residue matches
     today's read path, which also leaves a failed rebuild's partial
     build directory;
   - a repair triggered on a deleted path stats the transcript first
     and aborts without creating the sidecar directory;
   - repair failure: a seam failing once gives the completion log line,
     no re-arm, and the next read starts exactly one fresh repair;
   - failure budget: three consecutive failures on an unchanged
     transcript make the read answer `transcriptHistoryFailed` with the
     refusal's carried ordinal — and the later read that re-arms
     answers history-failed itself, with subsequent reads while the
     re-armed repair runs answering the rebuilding code; a read with no
     repair in flight still re-arms exactly one repair, and its success
     clears the budget; the refresher's next cycle also re-arms through
     the lane, but an exhausted path's refresher re-arm is skipped
     within the ten-minute interval after its last refresher re-arm and
     taken once the interval has passed;
   - a live writer (an ordinary handle extending between seam
     positions) observes the repair's hold as a stall — records stay
     consistent, and the writer's extension succeeds after the repair
     releases;
   - the registry starts no repair once the background context is done;
     project deletion drops the path's registry entry, and an in-flight
     repair finishing after the deletion records nothing (no recreated
     entry);
   - a never-indexed session (no sidecar) first-reads with the typed
     rebuilding answer and completes on the retry after the repair;
   - the repair log lines (start, completion, budget-exhausted) pinned.
4. Wire tests: the new error round-trips with
   `RetryDispositionAutomatic`, is distinct from
   `transcriptHistoryFailed`, and carries the daemonless history
   identity; the TypeScript binding test reads the Go constant.
5. Browser store tests (`cmd/evener-hub/frontend`): a fake client
   rejecting `thread/read` with the new code arms the owned hydration
   retry on both a first open and an already-open session, applies no
   history-failed diagnostic, and a later successful read hydrates
   normally; the per-ref status flag is set by the failing read, read by
   the loading surface ("Repairing session history"), cleared by the
   next read that does not fail with the code — including the
   budget-exhausted `transcriptHistoryFailed` transition; the older-page
   demand owner keeps the code unresolved (retryable), and a later read
   completes the page.
6. TUI tests (`cmd/evener-tui`): a session entry read rejected with the
   rebuilding or lock-contention code arms the paced retry and shows
   "Repairing session history" on the model's error line — the surface
   the entry failure itself occupies — in place of the error; a later
   successful read enters the session. The pacing shape is asserted against an
   injected delay schedule, not lived waits: the owner takes a
   test-injectable delay function (the suite has no fake clock for
   tea.Tick), asserted as 500ms, 1s, 2s, 4s, 8s, 15s, 15s. The
   history-failed answer ends the retry and shows the error on the
   same line; an ordinary transient failure keeps the retry armed with
   the status standing; a navigation away retires the pending retry —
   selecting a raw transcript to open included, at the selection itself:
   the read's window holds the retirement, so a success landing inside
   it enters nothing and a failed read leaves the retry retired — and a
   late-landing
   retry for a departed intent enters nothing; the automatic
   completion holds for an open transcript viewer, leaving the viewer
   standing; the
   already-open deferral is pinned — an errored /details or clear
   already-open deferral is pinned — an errored /details or clear
   re-read of the displayed session, and the raw transcript viewer's
   past-session read, arm no retry and show no status.
7. Message-search tests: a stale-sidecar session is skipped for a cycle
   (no failure recorded, no rebuild, no lock-hold) and indexes on the
   next cycle after the repair commits; the refresher's refusal
   registers a repair through the lane (via the injected callback), and
   the lane runs at most one refresher-triggered repair at a time
   across two stale paths while a client read's own repair starts
   immediately — and a busy lane retains a later session's ask and
   serves it as the running repair retires, so an early session that
   re-stales every cycle cannot starve the sessions behind it.
8. Whole-journey check across the real hub stack: open a daemonless
   session with a stale sidecar, confirm the first paint waits through
   the typed-error/retry cycle showing "Repairing session history",
   completes after the repair with correct content and paging, and a
   message composed during the window is unaffected by the read path
   (its delivery is the outbox's existing contract).
9. Existing suites stay green: `internal/transcriptindex`, hubcore
   past-path and message-search tests, `app_threadread*`, and the
   frontend gates (`make test-web`; typecheck included).
