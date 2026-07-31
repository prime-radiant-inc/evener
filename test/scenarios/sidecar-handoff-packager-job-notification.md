# sidecar-handoff-packager-job-notification: handoff sidecar packages a completed delegate result

**What this covers**: workflow handoff packaging. The parent starts a
worker delegate and an observer sidecar; the worker's completion
reaches the observer as a `job.notification` watch frame, and the
observer packages the handoff from what that frame carries. This card
is also the pin on the OBSERVER READ BOUNDARY: a session-source watch
delivers an event payload and grants nothing — no cross-session read,
no job visibility. Driving mechanism: `delegate(watch_parent:true)` +
observer-installed `job_watch(source:"parent",
events:["job.notification"])` + the observer's terminal
`communicate(end_turn:true)` callback — see `docs/job-control.md`
"Observer and sidecar composition".

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-handoff-packager-XXXXX)`.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`; use
   `kimi/kimi-for-coding` for the Kimi pass.
2. Prompt:

   > Run the handoff packager sidecar scenario.
   > 1. Call `delegate` with `watch_parent` true, `max_wait_ms` 120000,
   >    and this exact task: "You are HANDOFF_PACKAGER. First: call
   >    job_watch with operation 'create', source 'parent', events
   >    ['job.notification']. Then communicate exactly HANDOFF_READY and
   >    finish. When later resumed with a message containing 'Watch
   >    frame', read the delivery_id and, from the frame's event block,
   >    the job_id, job_type, status and output_bytes. Finish with
   >    communicate end_turn true and message exactly HANDOFF_PACKAGE
   >    delivery=<delivery_id> job=<job_id> status=<status>
   >    bytes=<output_bytes> owner=release-team. Do not call job_status
   >    or any other tool."
   > 2. After the delegate result reports `watching: true`, capture the
   >    watch_id from its `watches` entry, then start a worker delegate
   >    with `max_wait_ms` 90000 and task: "Communicate exactly
   >    HANDOFF_WORKER_RESULT artifact=summary status=ready." Capture
   >    the worker's job_id and transcript_ref.
   > 3. When the HANDOFF_PACKAGE observer callback arrives, call
   >    `job_watch` with operation "clear" and the observer's watch_id.
   > 4. Read the worker's own result yourself — you own that job — with
   >    `read_transcript` on its transcript_ref, then communicate
   >    exactly SCENARIO_DONE handoff-packager.

## Expected

- The readiness delegate result reports `watching: true` and lists the
  observer's watch as `{"id":"watch_...","source":"parent",
  "condition":"events: [job.notification]","deliveries":0}`.
- The observer is woken by a `job.notification` frame naming the WORKER
  delegate's job. The frame's shape is fixed:

  ```text
  Watch frame
  watch_id: watch_...
  delivery_id: wd_...
  job_id: caller
  trigger: event: JOB_FINISHED
  provenance: ...
  event:
    kind: job.notification
    job_id: job_...           <- the worker's job
    job_type: delegate
    status: completed
    output_bytes: 52
  ```

  The top-level `job_id:` is the literal `caller` — the watch's source
  is a session, not a job. The concrete job id lives ONLY in the
  `event:` block (`watchEventWatchedIdentity`, `agent/job_watch.go:2421`
  returns the session alias for a session-source watch).
- The observer packages the handoff from those event fields and reports
  it through one terminal `communicate(end_turn=true)`, which reaches
  the parent as an `Observer callback:` block.
- **The observer cannot read or inspect the worker's job.** This is the
  boundary this card pins, and both halves of it are load-bearing:
  - `job_read_output` is not in ANY session's tool set — it was retired
    from the model-facing registry by commit `cf84923c6` (2026-06-23)
    in favour of `job_status` / `read_transcript`. A task that asks for
    it gets `unknown tool: "job_read_output"` plus the available-tools
    list.
  - `job_status` IS in the observer's tool set, and on the worker's
    job_id it fails
    `job "job_..." not found — use job_list to see this session's jobs`.
    A session-source watch mints no read grant: both
    `mintWatchCreateReadGrant` and `mintWatchSendReadGrant` return early
    for a session target (`agent/job_watch.go:3270,3316`), so the
    observer's session never enters the durable grant table
    `lookupGrantedJobRead` consults.

  Falsification: if either call SUCCEEDS, cross-session observer reads
  have been added — re-read `docs/job-control.md`'s access-control rule
  and this card's premise before trusting the run.
- Reading the worker's result is the PARENT's job, because the parent
  owns it. Step 4 is the honest half of the handoff.
- The tree shows both a worker delegate and an observer delegate.
- The watch condition is `events: [job.notification]`.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:40
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count communicate
```

## Sharp edges

- If the worker finishes before the watch exists, the event is never
  observed. The observer installs its own watch inside its first turn,
  so `watching: true` on the readiness result is the proof the watch
  predates the worker — start the worker only after that result.
- `events: ["job.notification"]` on `source: "parent"` fires for EVERY
  job the parent completes, including the observer's own callback jobs.
  Each callback therefore re-fires the watch, and the next frame
  carries the self-influence `<system-reminder>` line
  (`↳ this turn responded to your last message.`). That loop is bounded
  by the breaker and the 50-delivery watch budget, not by silence —
  clear the watch as soon as the package arrives, or the run
  ping-pongs until the budget auto-clears it.
- The frame reports `output_bytes`, not the output. A packager that
  needs the worker's actual text has to get it from the parent, which
  is why step 4 exists.
- The parent can clear a watch the observer created: a parent-source
  watch is installed into the parent's own job manager
  (`parentInstallWatch`, `agent/session_tools_jobs.go:218`).
