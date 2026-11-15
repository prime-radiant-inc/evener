# Idle/background CPU audit

## Verdict

The clearest idle cost is hub polling: the roster refreshes all discovered daemons every five seconds, and attention/notices independently rebuild projections at the same cadence. The 250 ms relay retirement ticker is the highest-frequency per-session polling I found, although each wake only checks whether a session has lost subscribers and a later wake retires it. An empty `evener serve` used 0.07 CPU-seconds over a settled 60-second interval, and the no-session hub used 1.07 CPU-seconds over 120 seconds; I could not run the requested startup benchmarks because another auditor's untracked `agent/zz_audit_round_test.go` does not compile.

## Findings, ranked

### 1. High: Hub idle projections repeat full snapshots every five seconds

**Path/call path:** `cmd/evener-hub/hub_startup.go:849-878` starts `watchHubRoster`, `watchHubAttention`, and `watchHubNotices`; `cmd/evener-hub/internal/hubcore/roster.go:1234-1275` calls `Roster.Refresh` on 5-second ticks; `cmd/evener-hub/main_background.go:54-87` calls `watchHubAttention.run`; `cmd/evener-hub/app_notices.go:16-19,259-270` derives notices on the same cadence.

**Mechanism:** With no fsnotify changes, roster polling still lists rendezvous files, snapshots maps, probes each daemon, and compares/publishes the result. Attention calls `archive.Decisions`, `web.navigationSnapshot`, and derives/updates roster attention every five seconds. Notices perform a fresh read and compare the resulting slice. All are fixed cadence; attention/notices do not wait for an event-only signal. A no-op `Roster.Refresh` suppresses change callbacks, but does not skip its scan/probe pass.

**Evidence:** **read**: traced the `hub_startup` goroutine launch to the ticker branches and refresh/read functions. `Roster.Watch` comments explicitly state “every fsnotify event and at a 5-second tick” (`roster.go:1234-1235`). **executed (coordinator measurement):** `evener hub --addr 127.0.0.1:19744 --no-deploy` with fresh `HOME`, `XDG_CONFIG_HOME`, and `XDG_STATE_HOME` under `$EVENER_SCRATCH_DIR/idlehub2`, no sessions; CPU time rose from 0.78s at settle to 1.85s after 120s (1.07 CPU-s/120s, ≈0.54 CPU-s/min, 0.9% of one core). This is aggregate hub CPU, not attribution to this group of loops.

**Scale:** Per hub, three scheduled 5-second wakes (0.6/s total) independent of activity; roster cost grows with rendezvous entries and probes, attention with live/remote sessions and decisions, notices with the state their `watchRead` inspects. The operations take separate paths, so improvements need not sacrifice one source's correctness to fix another.

**Fix design:** Keep fsnotify as the roster's primary path and add a longer, backoff-based reconciliation timer after successful quiet refreshes, resetting to five seconds on an event, error, or detected change. In `watchHubAttention`, prefer the existing `attentionPoke` and navigation/roster change publication; retain a slow reconciliation tick only as a lost-event safety net. For notices, trigger on the existing relevant state/store change hooks where feasible and retain a slower safety refresh. Tests should assert prompt event-driven updates, no duplicate/no-op broadcasts, and recovery after a deliberately dropped event. This preserves correctness while making quiet time cheaper; the risk is stale UI if a change source is missed, so do not remove the safety net without proving coverage.

**How to verify the win:** Run isolated hub idle sampling with zero sessions and with N resident daemons; instrument counts around `Roster.Refresh`, `web.navigationSnapshot`, and `notices.watchRead`; compare per-minute CPU and pass counts before/after. Existing hub roster, attention, and notices tests must remain green.

### 2. High: Each relay polls every 250 ms to discover that subscribers are gone

**Path/call path:** A browser subscription reaches `hubRelay.startRelayForThread` (`cmd/evener-hub/app_relay.go`), which starts a `relaySupervisor.run` (`app_relay_start.go:344-429`) and, for acknowledged sources, `startAcknowledgedFanout` (`app_relay_fanout.go:12-20,435-461`). Both select on `hubRelayIdleInterval = 250ms` (`app_relay_types.go:120`) and call idle-retirement checks.

**Mechanism:** A quiet but still-subscribed relay wakes four times per second. `relaySupervisor.retireIfIdle` checks `SubscriberCount`, and if zero it repeats the count under the relay mutex before deleting bookkeeping. Fanout `retireIdle` takes `r.mu`, scans `handle.relayKeys`, then calls `SubscriberCount` for candidate keys and rechecks before deletion. Timers are fixed, not activity-adaptive. Active notification streams otherwise block on channels.

**Evidence:** **read**: traced loop select and retirement bodies at cited lines. The interval is an explicit package variable set to 250 ms. No subscriber-count or CPU profile was run.

**Scale:** For R simultaneously relayed sessions, approximately 4R supervisor ticks/s plus 4A fanout ticks/s for A acknowledged handles. Supervisor checks are O(1) each; fanout checks can scan O(routes per handle) each tick, so many quiet subscribed sessions multiply wakes and route scans.

**Fix design:** Make subscriber removal signal an idle channel from `appserver.Server`/`Connection` subscription bookkeeping; have the relay wait on that signal plus its context and incoming notifications instead of polling. Preserve the current double-check-under-lock before deletion to close subscribe/retire races. If changing the server API is too broad, use a slower adaptive timer (e.g. increase delay while subscribers remain, reset on route/subscriber changes) as an interim fix. Test unsubscribe followed by immediate resubscribe, concurrent subscription admission, and relay cleanup exactly once. Main risk: a missed signal could retain a relay; retain a low-frequency safety check until signal delivery is proven.

**How to verify the win:** Run a hub relay test with N subscribed quiet sessions for 60 seconds and count idle callbacks/locks; compare to the current 240N/s supervisor tick rate, while asserting subscriber disappearance still retires within the documented bound.

### 3. Medium: Past index and message-search refresh revisit all sessions each minute

**Path/call path:** Hub startup starts `past.Rebuild()` on `cfg.PastIndexRebuild` (`cmd/evener-hub/hub_startup.go:851-862`, default 60s at `config.go:191`), and `refreshHubMessageSearch` at that same cadence (`hub_startup.go:864-866`, `main_background.go:120-145`). That calls `MessageSearch.Refresh(ctx, messageSearchSessions(past.All()))`.

**Mechanism:** A no-change minute still rebuilds the past index and produces a session list for search refresh. Search refresh is documented as reading only changed transcripts (`main_background.go:116-120`), so it can avoid reparsing unchanged transcript bytes, but still builds/iterates the session list and checks index metadata. The past-index implementation's complete no-change cost was not profiled.

**Evidence:** **read**: timer setup, default cadence, and refresh call path. The comment in `MessageSearch` wrapper confirms changed-only transcript reads; no runtime count was measured.

**Scale:** One full past-index rebuild and one search refresh per hub per minute, plus startup rebuild/refresh. Session-list work grows O(S), where S is indexed sessions; changed transcript parsing grows with changed transcript bytes. Fixed cadence independent of file changes.

**Fix design:** Have the transcript indexer share the filesystem change signal or persist a dirty generation; skip `PastIndex.Rebuild` when the indexed inputs' generation is unchanged. Let `MessageSearch.Refresh` consume changed session IDs instead of reconstructing and passing all `PastEntry` rows. Keep a slower periodic repair pass. Test new/deleted/modified transcript detection, missed watcher recovery, and no-op generations. Main risk is stale search/index state when notifications are lost or files change outside the watched tree.

**How to verify the win:** Benchmark empty and unchanged populated roots through 10 rebuild intervals; count filesystem opens/stats/parses and CPU with `go test ./cmd/evener-hub -run '^$' -bench ...` or an instrumented index test. The expected delta is zero transcript opens on quiet intervals and reduced O(S) per-minute work.

### 4. Medium: Browser WebSocket keepalives send a ping every 15 seconds per connection

**Path/call path:** `Server.ServeWebSocket` (`internal/appserver/websocket.go:123-203`) starts `runWebSocketKeepaliveWithTicker` at line 192; ticker cadence is `keepalivePingInterval = 15s` and timeout is 10s (lines 20-28).

**Mechanism:** For every live WebSocket connection, the keepalive goroutine wakes, locks the read gate (`beginPing`/`finishPing`), writes a ping control frame, and waits for a pong. The read gate defers pings when the receive loop cannot receive a pong. This is necessary for silent disconnect detection; cost scales per connected browser even when it is idle.

**Evidence:** **read**: traced actual connection setup and keepalive loop constants. The number of concurrent hub connections and cost per ping were not measured.

**Scale:** 1/15 ping per second per connected WebSocket (4/minute each); 100 idle browser connections imply roughly 6.7 ping attempts/s. Each attempt incurs a lock pair, context/cancel bookkeeping, a control-frame write, and peer pong processing.

**Fix design:** Keep the 15-second cadence unless measured scale warrants a change. A safer optimization is to avoid keepalive goroutines for transports where the protocol stack already provides dead-peer detection, but this server's WebSocket peer currently relies on this mechanism. Add a connection-count and ping-count metric before adjusting cadence. Longer intervals trade dead-peer detection latency for CPU/network and need Jesse's ruling.

**How to verify the win:** Instrument `runWebSocketKeepaliveWithTicker` ping attempts and `Server` active-connection count; measure idle hub CPU at 0/10/100 connections and verify silent-drop detection remains within the chosen timeout.

### 5. Medium: Agent keepalive pings every 15 seconds on each ping-capable AppWire connection

**Path/call path:** `Client.Start` (`appwire/client.go:114-121`) starts `runClientKeepalive` only when transport implements `Pinger`; the loop (`client.go:212-245`) ticks every 15s (`client.go:105-112`) and does `Ping` with a 10s timeout.

**Mechanism:** Every idle ping-capable connection writes a ping, waits for its response, and allocates/cancels a timeout context. A failed ping logs and closes the connection. Non-pinger transports such as the stream transport do not take this path.

**Evidence:** **read**: traced capability gate, ticker, ping call, and timeout constants. Per-connection ping CPU and live connection counts were not measured.

**Scale:** 4 pings/minute per ping-capable client; for C such connections, 4C pings/minute. This scales with live remote/local relay connections, not sessions that have no transport.

**Fix design:** Retain the current behavior for failure detection. First measure ping and connection counts; if material, centralize keepalive scheduling only if it preserves per-transport timeout/cancellation semantics. Tests must cover failure close, context cancellation, and transports that do not implement `Pinger`. Increasing cadence risks delaying detection and is a product tradeoff.

**How to verify the win:** Count pings by transport type and sample idle CPU with a representative number of ping-capable sessions; any scheduler change must preserve the existing keepalive tests and bound disconnect detection.

### 6. Low: Process-exit polling is 50 times per second while a caller waits

**Path/call path:** `daemonprocess.process.Wait(ctx)` (`cmd/evener-hub/internal/daemonprocess/process.go:250-275`) checks `handle.exited()` then waits on a 20ms ticker.

**Mechanism:** While an explicit caller is waiting for daemon exit, each tick takes `p.mu`, calls platform process-exit inspection, then selects again. It stops as soon as the process exits or context ends. This is not unconditional idle-daemon polling; it is paid only while an API/cleanup operation is already waiting for process termination.

**Evidence:** **read**: code trace and fixed 20ms period. Per-platform syscall cost was not measured.

**Scale:** 50 checks/s per concurrent wait, constant in process count; only concurrent shutdown/reclamation waits multiply it. Maximum delay to notice exit is approximately one tick.

**Fix design:** Prefer an OS readiness primitive where available (the comment identifies pidfd on Linux; macOS currently uses unique process identity). Keep the portable poll fallback and make its interval configurable/backed off after the initial responsiveness window. Test prompt exit detection and cancellation on both platforms. Risk is delayed cleanup or PID-reuse mistakes if identity binding changes.

**How to verify the win:** Benchmark concurrent `Wait` calls with children that exit at known times; count `handle.exited` calls, CPU, and exit-detection latency by OS.

## Timer inventory

Periods below are production values at this commit. One-shot deadlines and timers are excluded from the table; `time.After` retries recur only while their respective operation is recovering. “Idle” means no user input or state change, not simply a process that remains alive.

| Path / timer | Period | Work per fire; idle behavior |
|---|---:|---|
| `cmd/evener-hub/internal/hubcore/roster.go:1256` `Roster.Watch` | 5s | `Roster.Refresh`: lists rendezvous claims, probes known daemons, replaces snapshot; runs despite no fsnotify event. |
| `cmd/evener-hub/main_background.go:61-87` attention | 5s | Reads archive decisions, navigation snapshot, derives/publishes roster and attention; wakes and recomputes while idle. |
| `cmd/evener-hub/app_notices.go:19,259-270` notices | 5s | `watchRead` and equality comparison; broadcasts only on difference. |
| `cmd/evener-hub/hub_startup.go:852-860` past index | default 60s | `PastIndex.Rebuild` over indexed past sessions/files. |
| `cmd/evener-hub/main_background.go:120-145` message search | default 60s | Builds full session input and calls refresh; unchanged transcripts avoid full reads per code comments. |
| `cmd/evener-hub/main_background.go:208-239` remote threads | 30s | Calls `refreshRemoteThreadSnapshot` for configured remote sources, stores snapshot; additionally wakes on poke. |
| `cmd/evener-hub/app_relay_types.go:120`, `app_relay_start.go:345-429` relay supervisor | 250ms | Checks one key's subscriber count; when zero rechecks under mutex, deletes and cancels. One per relay. |
| `cmd/evener-hub/app_relay_fanout.go:17,435-461` acknowledged fanout | 250ms | `retireIdle` locks and scans route map, checks subscriber counts, retires when empty. One per acknowledged handle. |
| `cmd/evener-hub/app_plugin_autoupgrade.go:93-107`, config default `config.go:195` | 12h (when enabled) | Refreshes every marketplace then checks/upgrades eligible plugins; also runs once immediately at start. Not a cheap no-op tick. |
| `cmd/evener-hub/internal/daemonprocess/process.go:253-274` process wait | 20ms | Checks OS process exit while an explicit waiter is active; not an always-on daemon sweep. |
| `cmd/evener-hub/spawn.go:1235` spawn wait | 50ms | Polls for spawn/rendezvous completion during an in-progress spawn; request-scoped. |
| `cmd/evener-hub/restart.go:219` restart wait | 250ms | Polls during a restart/recovery operation; request-scoped. |
| `cmd/evener-hub/app_host_deploy.go:1233` operation wait | `operationPollInterval` | Rechecks a running host operation while a caller waits; not a background idle loop. |
| `internal/selfupdate/install_lock_unix.go:84` lock wait | `installLockPollInterval` | Retries installation-lock acquisition only while another installer holds the lock. |
| `agent/internal/mcp/manager.go:302` sandbox recheck | `serverSandboxRecheckPause` | Rechecks MCP server sandbox state during a start/recovery operation; no timer while stable. |
| `agent/job_watch_hub.go:1335,1514` watch retry/rebind | dynamic bounded backoff | Retries failed Manager watch reads/rebinds; wakes only during a failing observation/recovery episode. |
| `appwire/client.go:225` client keepalive | 15s | Per `Pinger` connection sends ping and waits up to 10s for pong; 4/minute/connection. |
| `internal/appserver/websocket.go:69,192` server keepalive | 15s | Per live WebSocket sends ping when read gate allows; 4/minute/connection, 10s pong timeout. |
| `auth/openai/service.go:396` auth service | supplied `interval` | Refreshes/checks auth state while the owning service is active; period is caller-configured, not fixed here. |
| `agent/internal/clock/clock.go:63` ticker adapter | caller-supplied | Generic clock adapter; cadence/work are determined by each injected production caller, not an independent timer. |
| `cmd/evener-hub/app_plugin_autoupgrade.go:27` ticker factory | caller-supplied, default 12h | Adapter/factory for the auto-upgrade timer above, not a second loop. |
| `cmd/evener-hub/internal/hubcore/roster.go:414` ticker factory | caller-supplied, Watch uses 5s | Adapter/factory for the roster timer above, not a second loop. |

Recurring `time.After` delays also appear in `agent/job_watch_hub.go:1335` and `:1514` (retryable Manager observation/rebind failures; dynamic bounded backoff), `internal/selfupdate/install_lock_unix.go:84` (waits at `installLockPollInterval` only while contending), and `agent/internal/mcp/manager.go:302` (sandbox restart recheck, operation-scoped). Hub deploy's `app_host_deploy.go:1233` likewise polls only while an operation is awaiting completion. These are recovery/wait paths, not steady idle wakeups. `time.After` calls in `cmd/evener/serve.go:303,309`, `app_host_deploy.go:685`, process-group stop, and exec shutdown are one-shot deadlines, not periodic work.

Production `time.Sleep` sites are not steady idle loops: `cmd/evener-hub/app_update.go:582` delays a restart once; `cmd/evener-hub/app_host_commit.go:297` backs off while retrying a concurrent-edit operation; `cmd/evener-hub/hub_startup.go:947` performs at most three failed-store-open retries; `cmd/evener-dev/webgate.go:573` polls during an active web-gate operation; `agent/job_watch_hub.go:835` waits during retry/recovery; `agent/internal/installid/installation_id.go:24` waits on an installation-ID lock; and `agent/execenv/local.go:530` waits for a child termination grace period. These waits do not spin and are tied to an active operation or one-time startup/teardown.

## Clean

- `watchHubRoster` is event-driven through `Roster.Watch` and fsnotify, with only one directory watch (`roster.go:1241-1250`); the periodic tick is a fallback, not one watcher per session. **Evidence: read.**
- `Roster.Refresh` explicitly suppresses change callbacks unless the membership/status fingerprint changes; no-op scans do not necessarily broadcast (`roster.go:377-382`). **Evidence: read.**
- `refreshHubMessageSearch` is specified to reread only changed transcripts (`main_background.go:116-120`); the likely residual cost is list/generation checking, not unconditional parse of every transcript. **Evidence: read, no syscall count.**
- Auto-upgrade is gated by config and has a 12-hour default interval; the timer path is not high-frequency (`config.go:191,480-483`). **Evidence: read.**
- A settled isolated `evener serve` with no client consumed 0.45 CPU seconds at the first sample and 0.52 seconds 60 seconds later: 0.07 CPU-seconds/minute. The process was launched with `--model openai/gpt-4o --state-dir <scratch>/idle-serve/state --run-dir <scratch>/idle-serve/run --addr 127.0.0.1:0 --disable-memory --non-interactive`, after 20 seconds settling; environment had isolated `HOME` and `XDG_STATE_HOME`. **Evidence: executed.** The first attempt without a model exited immediately, so it was not counted.
- The no-session hub's 0.54 CPU-s/min aggregate accounts for all hub work, including runtime/network/GC and startup residue. Code inspection identifies the recurring roster/attention/notices, relay, past-index/search, and remote-thread work above, but I did not isolate their individual shares; therefore no defensible fraction of that 0.54 can be attributed to specific timers.

## Open questions

- How stale may the hub's roster, attention, notice, and transcript/search views become if periodic safety refreshes slow down or rely more on event signals?
- Is 15-second dead-peer detection worth four WebSocket/AppWire pings per minute per connection at expected hub connection counts? No connection counts or per-ping CPU were available.
- Is a delayed relay cleanup acceptable if subscriber-removal events replace 250ms polling? A low-frequency fallback can preserve a bound at residual CPU cost.
- Startup benchmark was requested but could not execute: `go test ./agent -run '^$' -bench 'BenchmarkNewSession|BenchmarkProfileToolRegistry' -benchmem` failed compiling the pre-existing/untracked `agent/zz_audit_round_test.go:30:18` (`cannot use ... as "encoding/json".RawMessage`). I did not edit another auditor's file.
- The hub binary build succeeded with `cd cmd/evener-hub && go build -o "$EVENER_SCRATCH_DIR/evener-hub-bin" .`; I did not launch it. No-session hub idle CPU was measured separately by the coordinator as stated above.
- Startup wall/CPU for one-shot `evener` and hub was not measured. The requested `go test ./agent -run '^$' -bench 'BenchmarkNewSession|BenchmarkProfileToolRegistry' -benchmem` failed during compilation before either benchmark ran: `agent/zz_audit_round_test.go:30:18: cannot use ... as "encoding/json".RawMessage`. This file was untracked and belongs to another auditor, so I left it unchanged.
