# Frontend CPU-efficiency audit

## Verdict

The largest avoidable work is that every `overlay/delta` rebuilds the entire transcript projection, including a full sort of all item positions, even though the reducer rebuilds only one display turn. The next cost is that each delta copies the turn array and grows the streamed field, then notifies the mounted store subscribers; visible streaming markdown must also be reparsed as its source changes. I checked virtualization, settled Markdown memoization, the routing index, visibility handling in rail polling, and terminal activity rows; those already limit work or have intentional gates.

## Findings, ranked

### 1. High: Every streamed delta reprojects and sorts the full transcript

**Path:** AppWire websocket `client.ts:647,905-950` (`handleMessage` parses once and dispatches the notification) → `src/stores/threads.ts:6728-6859` (`handleNotification` routes and calls `applyToMap` / `applyNotification`) → `appwire-client/typescript/reducer.ts:78-99` (`applyOverlayDelta`) → `src/panes/session/ConversationBody.tsx:332` (`projectThread` on each new model) → `appwire-client/typescript/transcriptProjector.ts:496-582`.

**Mechanism:** A streamed `overlay/delta` changes the model identity. `ConversationBody` memoizes `projectThread` by `[model, displayConfig]`, so each new model recomputes it. `projectThread` calls `sourceIndexRanks`, which collects every item and sorts them, then scans every turn and item to rebuild projected entries and anchors. `TranscriptBody` also rebuilds rows and exchange opener/closer maps when these identities change. Virtualization limits mounted DOM rows, not this full-list projection work.

**Evidence, read:** `transcriptProjector.ts:496-501` collects all items, sorts them, and builds a map; `:505-583` iterates all turns/items. `ConversationBody.tsx:332-335` keys the projection and row memos on the changing model/projection. The route is reachable from the websocket notification handler above. No timing measurement ran because the frontend dependencies were unavailable.

**Scale:** Per streamed delta, O(I log I + I) for I items across the complete loaded transcript, plus O(T) row/list mapping for T turns/rows. With hundreds of deltas, this repeats hundreds of times. A later-loaded history page increases every subsequent delta's cost.

**Fix design:** In the owning `appwire-client/typescript/transcriptProjector.ts`, preserve projected turn/anchor data for unchanged recorded turns and reproject only the affected display turn when the model exposes a per-turn change identity. Maintain the source-position rank index when history changes, not on overlay text deltas. Use `displayTurnSources` and the reducer's existing changed-turn knowledge rather than adding a new projection subsystem. Preserve global ordering/anchor ranks when recorded history changes. This could break interleaved-position ordering, disclosure IDs, or scroll anchors if rank updates are stale; add a flood test that counts projector visits and verifies ranks/anchors after history insertion and out-of-order turn items.

**How to verify the win:** Add a focused `vitest` benchmark/test for 500 `overlay/delta` notifications against a seeded transcript of increasing item counts; report item visits, sort calls, and elapsed time before/after. Run `npx vitest run --reporter=dot appwire-client/typescript/transcriptProjector.test.ts` and the targeted frontend transcript test(s).

### 2. High: The reducer copies all turns for every delta

**Path:** websocket `client.ts:647,905-950` → `src/stores/threads.ts:6728-6860,7050-7062` → `appwire-client/typescript/reducer.ts:78-99`.

**Mechanism:** The delta reducer correctly rebuilds only the matching display turn, but returns `turns: model.turns.map(...)` on every accepted delta. The map allocates a fresh array and visits every turn; `handleNotification` then publishes the changed model in a new `threads` map and creates a new `frameTimes` map for the accepted ref. The `threadsStore.setState(patch)` publication is one store write for the normal tracked-ref delta path. Zustand notifies subscribers on that publication; selector callback count depends on how many components are mounted and was not instrumented.

**Evidence, read:** `reducer.ts:88-99` identifies the matching display turn, rebuilds it, then maps the complete turns array. `threads.ts:6521-6543` copies the thread map for a changed routed ref; `:7050-7062` copies frame times and calls `setState` once for the normal notification. `ConversationBody.tsx:332` subscribes to the model identity through its caller's store selector.

**Scale:** O(T) turn references copied per delta, in addition to the full projection in finding 1. The full map copy adds O(S) when the thread map is copied, where S is the number of tracked threads, though the notification route and copy are only on changed models. One array copy per delta compounds linearly with turn count and number of deltas.

**Fix design:** Have the reducer preserve an indexed/persistent turn collection or use a targeted immutable replacement structure so changing one turn does not walk all turns. A minimal first step is a changed-turn helper that copies only at the necessary public array boundary, but a plain JavaScript array cannot avoid O(T) copying while preserving immutable semantics. Consider a per-turn map plus ordered IDs inside the internal model only if downstream APIs can remain unchanged. Risks include array identity contracts and consumers assuming `turns` is an array; add reducer and native shared-client compatibility tests before adopting a structural change.

**How to verify the win:** Benchmark `applyNotification` for 500 deltas with 10, 1,000, and 10,000 turns; record allocations and latency. Confirm unchanged turns retain identity and that web/native transcript ordering remains identical.

### 3. Medium: Markdown for the live message is reparsed as deltas arrive

**Path:** `client.ts:647,905-950` → `threads.ts:6728-7062` → reducer `applyOverlayDelta` (`reducer.ts:78-99`) → `ConversationBody.tsx:332` → `TranscriptBody.tsx:446-479` renders visible `TurnBlock`/item → `src/widgets/markdown/index.tsx:232-293`.

**Mechanism:** A streaming item receives a new `text` or `output` string with each delta, so the live Markdown source changes. `Markdown` memoizes its parse by source, not by a stable append-only parse state. It reparses the whole source when it is short (`:253-256`); above `LIVE_WINDOWED_MIN_LENGTH`, it can reuse a frozen head and parse a bounded tail (`:257-271`), but unsafe split cases deliberately fall back to full parse (`:258-261`). There is no syntax highlighter in this widget; fenced code is escaped and emitted as static code-block HTML (`:68-80`).

**Evidence, read:** The new string enters `applyOverlayDelta` at `reducer.ts:83-85`; the visible rendered path reaches `Markdown` through `TranscriptBody.tsx:446-479`. The Markdown implementation's cache is keyed to exact source/head/segment identity. It explicitly has a bounded-tail optimization and exact-safety fallbacks.

**Scale:** Per visible streaming message, parse and sanitize work runs on each UI render/delta. For short content and unsafe split shapes, work grows with accumulated source length, so the sequence can approach quadratic total parsing over the turn. Long safe prose past the window threshold parses a cached head plus the tail instead. Off-screen virtual rows do not mount/render.

**Fix design:** Keep the current Markdown soundness gates. Instrument `Markdown` parse/sanitize invocation counts and optimize only the measured remaining path: ensure append-only prose stays in the bounded live window, or batch consecutive deltas to one render per animation frame. Batching at the client/store boundary can change perceived latency and accessibility announcement timing; any design must preserve final byte-identical settled HTML, live construct formatting, sanitizer behavior, and reduced-motion/scroll timing. Add tests for a streaming paragraph, open fence, reference link, table, and unsafe split fallback.

**How to verify the win:** Benchmark parse/sanitize calls and wall time for a 500-delta live message at 1 KB, 20 KB, and an unsafe-boundary input; compare per-delta counts and ensure the settled HTML matches. Use the existing markdown widget tests plus a focused render counter test.

### 4. Medium: Live clocks tick in hidden tabs and at a faster cadence than the recorded decision

**Path:** `ConversationBody.tsx:238` → `liveness.ts:28-34` (`useNowTick`) and `ActivityTree.tsx:211-218` (`TreeTickProvider`). `Spawn.tsx:251-257` owns another local timer.

**Mechanism:** Each mounted `ConversationBody` starts a `setInterval` and updates a `SessionNowContext` value. It runs regardless of `document.visibilityState`; the Activity tree and Starting loader intervals also lack visibility listeners. Context subscribers and live Activity row detail/metadata recompute at each tick. The Activity tree does gate the interval on `live`, and terminal rows do not consume the time context. A collapsed-but-mounted pane is not explicitly paused.

**Evidence, read:** `liveness.ts:11,28-34` sets `NOW_TICK_MS = 3_000` and always installs/clears an interval. `ConversationBody.tsx:238` requests that tick. `ActivityTree.tsx:211-218` uses 1,000 ms when `live` but no tab visibility check. `Spawn.tsx:254-256` uses 1,000 ms while mounted. The project memory page `design/session-clock-cadence.md` says relative timestamps/Cadence must not tick more often than 5 s, while work-time displays keep 1 s; this commit's clock constant is 3 s, so code and memory decision disagree.

**Scale:** One interval per mounted session ConversationBody at 3 s (20 updates/minute), plus one per live Activity tree and one per mounted Starting loader at 1 s (60 updates/minute each). Work per tick is the React subscribers to the relevant context/state, not every session in the sidebar. Hidden tabs are not explicitly paused; browsers may throttle them.

**Fix design:** First reconcile the `NOW_TICK_MS` 3 s source against the existing 5 s decision in `liveness.ts` and `liveness.test.tsx`. Separately pause relative-age/Cadence clocks on `visibilitychange` and resume with an immediate current-time update; keep the independent second-granular work clock at 1 s as documented. Scope ActivityTree and StartingLoader similarly if they remain mounted while hidden. Risks: stale timestamps until visibility resumes and incorrect elapsed-time display if the work clock is mistakenly slowed. Test 4,999 ms/no tick and 5,000 ms/tick for the relative clock, retain 1 s work-time tests, and test hidden-tab pause/resume.

**How to verify the win:** Use fake-timer component tests to count provider updates with one and several mounted sessions and assert zero updates while hidden, then compare context render counts at 3 s vs 5 s. Run `npx vitest run --reporter=dot src/panes/session/liveness.test.tsx` and the ActivityTree/Spawn targeted tests.

### 5. Low: Several visible settings surfaces poll durable state when it has not changed

**Path:** `HostPicker.tsx:33-39` and `sections/hosts.tsx:199-245`; `sections/hubResidents.tsx:134-141`; `sections/hosts.tsx:218-245`; `appwire-client/state/mutation/outbox.ts:239-241,308-324,414-449`; OAuth dialogs `sections/credentials/oauthDialogs.tsx:219-302`; device pairing `sections/devicePairing/DeviceSetup.tsx:40-57`.

**Mechanism:** Mounted host pickers and the Hosts section each poll `hostsStore.refresh()` every 2 s; when both surfaces are mounted, both issue the same host-list read. Hub residents also refresh every 2 s. The Hosts section checks tracked operations once per second and reads each operation that still needs a read. The mutation outbox scans IndexedDB target refs every 2 s while its runtime is started and the client is ready; its scan does not check visibility, although it rechecks readiness before using results. OAuth and pairing use recursive timeouts while an active flow is open.

**Evidence, read:** `HOST_POLL_MS` is 2,000 (`appwire-client/typescript/hostMutations.ts:22`) and both callers install it. `hubResidents.tsx:28,134-141` uses 2,000 ms. `hosts.tsx:65,218-245` polls operations at 1,000 ms, skipping terminal/pending reads but iterating tracked operation keys. The outbox's `SCAN_INTERVAL_MS` is 2,000 (`state/mutation/outbox.ts:239-241`), and each interval calls `listTargetRefs` through `:414-449`. OAuth cadence is `max(1, intervalSeconds || 5) * 1000` at `oauthDialogs.tsx:219`; pairing retries every 1,200 ms at `DeviceSetup.tsx:57`. These polls stop on component unmount or flow termination, not on tab hidden.

**Scale:** Host/resident polls cost one read per mounted surface per 2 s, even with no changes. The operation poll scans O(O) tracked operation keys per second and reads only eligible operations. Outbox performs an IndexedDB target-ref list per 2 s per live runtime, with follow-on discovery work over retained targets. OAuth/pairing are bounded by active modal/flow lifetime.

**Fix design:** Avoid duplicate host-list polling by sharing one mounted poll owner or deduplicating in `hostsStore.refresh`; retain the out-of-band lifecycle freshness contract. Pause purely freshness-oriented polls in hidden documents and trigger an immediate read on resume, but keep request/lifetime guards. Do not remove the mutation-outbox scan without Jesse's call: it is a recovery path for durable mutation discovery across tabs. Test out-of-band attach/removal, operation terminal state, cross-tab mutation recovery, and visibility resume before changing cadences.

**How to verify the win:** Instrument FakeClient call counts with HostPicker and Hosts mounted together; establish the baseline list RPC count per 2 s, then verify the shared owner cuts duplicate calls while out-of-band updates still appear. For the outbox, count `listTargetRefs()` calls over a fake 10 s idle window and hidden/visible transitions.

## Timer inventory

These are recurring production schedules found in owned frontend/client source; one-shot debounce, gesture, request-deadline, retry-backoff, audio-stop, and cleanup timeouts are excluded.

| Timer | Cadence | Idle work | Hidden/collapsed behavior |
|---|---:|---|---|
| `useNowTick` (`liveness.ts:28-34`, used by `ConversationBody.tsx:238`) | 3 s | `Date.now()`, then updates session clock context | No explicit hidden/collapsed pause; stops on unmount. |
| `TreeTickProvider` (`ActivityTree.tsx:211-218`) | 1 s | `Date.now()` and provider state update while `live` | No explicit hidden pause; stops when `live` is false or unmounted. |
| `StartingLoader` (`Spawn.tsx:251-257`) | 1 s | `Date.now()` for elapsed loader | No explicit hidden pause; stops on unmount. |
| Host list refresh (`HostPicker.tsx:37`; `sections/hosts.tsx:214`; `HOST_POLL_MS` is 2 s) | 2 s | Host registry refresh/read for out-of-band lifecycle changes | Stops when each component unmounts; no explicit hidden pause. Duplicate when both mount. |
| Hub residents (`hubResidents.tsx:28,134-141`) | 2 s | Refresh resident roster | Stops on unmount; no explicit hidden pause. |
| Host operation reads (`hosts.tsx:65,218-245`) | 1 s | Iterates tracked operations and reads those still needing state | Stops on unmount; no explicit hidden pause. |
| Mutation outbox scan (`state/mutation/outbox.ts:239-241,308-324`) | 2 s | Lists durable target refs and discovers waiting mutations when client is ready | Runtime lifetime; no visibility pause for interval (visibility event also triggers scan on becoming visible). |
| OAuth device flow (`oauthDialogs.tsx:219,227-255,299-302`) | `max(1, intervalSeconds || 5)` s, default 5 s | Polls provider/device authorization status | Exists only during active flow; no explicit hidden pause. |
| Device pairing (`DeviceSetup.tsx:40-57`) | 1.2 s after each pending read | Reads invitation state until approved/terminal/cancelled | Flow-scoped and canceled on cleanup; no explicit hidden pause. |
| AppWire heartbeat (`appwire-client/typescript/client.ts:93-99`; timer arm around `:276+`) | 20 s quiet interval, followed by 10 s timeout | Sends ping after no inbound frame, then closes if unanswered | Connection-scoped, not a UI rendering clock; timer resets on each inbound frame. |

## Clean

- **Transcript DOM is virtualized.** `TranscriptBody.tsx:499-523` gives `VirtualList` a count and row callback; `VirtualList` uses TanStack `useVirtualizer` (`src/widgets/virtuallist/index.tsx:296`). A delta can still rederive full-list data (finding 1), but it does not mount every transcript row.
- **Markdown results have existing memoization/throttling.** `widgets/markdown/index.tsx:206-220` memoizes sanitized settled token slices; `:247-292` caches a frozen live head and resegments only the tail where safe. Unsafe fallbacks preserve correctness. There is no syntax-highlighting engine in this renderer (`:68-80`).
- **Thread notification routing is indexed.** `threads.ts:1376-1465` routes by ref or threadId index rather than scanning all tracked/watched models for every frame; the code comments identify the index as the token-hot-path optimization. `applyToMap` copies maps only after a routed model actually changes (`:6516-6543`).
- **Activity rows separate live and terminal work.** `ActivityTree.tsx:234-263,276-278,439-506` puts time subscriptions only on live metadata/detail and memoizes terminal rows; the tree clock only starts while live.
- **Rail activity polling already pauses in hidden tabs.** `railActivity.ts:71-123` checks connection and visibility, stops the timer on hidden, and performs an immediate read on resume. It polls only while the activity card is active/open.
- **Shared client decodes a websocket frame once.** `client.ts:912-924` performs one `JSON.parse` per text frame, then passes the parsed notification to handlers (`:937-949`). Reducers consume this parsed object; I found no repeated JSON parse in the per-delta reducer path.

## Open questions

- The project memory ruling says `NOW_TICK_MS` is at least 5 s, but source at this audited commit is 3 s. Should the audit treat the memory decision as authoritative, or did the cadence change intentionally after it was recorded?
- Should hidden-tab pausing apply to all active settings/background polls, including mutation discovery and in-progress OAuth/device flows? Some polls preserve cross-tab recovery or user-visible freshness and need a behavior decision before pausing.
- How much store/projection CPU is acceptable in exchange for faster streaming updates? A frame-batched publication reduces redundant renders but adds up to one frame of latency and can affect announcements.

## Measurement and limitations

- Checked `cmd/evener-hub/frontend/package.json`: `test` runs package-boundary checks, Vitest, and Node script tests; `typecheck` runs `tsc --noEmit --incremental false`. Did not run either full command.
- Attempted the existing focused benchmark: `cd cmd/evener-hub/frontend && npx vitest bench ../../../appwire-client/typescript/tokenFlood.bench.ts -t 'fold-and-read n=1000' --run`. It exited 1 before collecting benchmark data. Vite reported `Could not resolve '@vitejs/plugin-react'` and `Could not resolve 'vite'`; `ERR_MODULE_NOT_FOUND` prevented loading `vite.config.ts`. Per brief, I did not run `npm ci` or reinstall dependencies.
- No store selector callback counts, exact component render counts, parse call counts, layout-read frequency, or browser CPU profile were measured. Those counts depend on mounted subscribers/browser scheduling; the report labels code-traced counts and scales as **read**, not executed.
