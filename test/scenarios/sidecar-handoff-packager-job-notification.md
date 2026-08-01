# sidecar-handoff-packager-job-notification: handoff sidecar packages a completed delegate result

**What this covers**: workflow handoff packaging. The parent starts a
worker delegate and an observer sidecar; the worker's completion
reaches the observer as a `job.notification` watch frame, and the
observer packages the handoff from that frame AND from the worker's
own output. This card is also the pin on the OBSERVER READ GRANT: the
delivery mints the observer a durable read on the concrete job the
payload names, the frame names the one call that spends it, and
nothing else about that job opens up. Driving mechanism:
`delegate(watch_parent:true)` + observer-installed
`job_watch(source:"parent", events:["job.notification"])` + the
observer's terminal `communicate(end_turn:true)` callback — see
`docs/job-control.md` "Observer and sidecar composition".

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
   >    the job_id, job_type, status and output_bytes. If the frame ends
   >    with a 'read with:' line, make exactly that call and read the
   >    worker's report out of the returned content. Then call job_status
   >    once with that same job_id — it is EXPECTED to fail; do not retry
   >    it. Finish with communicate end_turn true and message exactly
   >    HANDOFF_PACKAGE delivery=<delivery_id> job=<job_id>
   >    status=<status> bytes=<output_bytes> owner=release-team
   >    artifact=<the artifact=... value from the worker's report>. Use
   >    no other tools."
   > 2. After the delegate result reports `watching: true`, capture the
   >    watch_id from its `watches` entry, then start a worker delegate
   >    with `max_wait_ms` 90000 and task: "Communicate exactly
   >    HANDOFF_WORKER_RESULT artifact=summary status=ready." Capture
   >    the worker's job_id.
   > 3. When the HANDOFF_PACKAGE observer callback arrives, call
   >    `job_watch` with operation "clear" and the observer's watch_id,
   >    then communicate exactly SCENARIO_DONE handoff-packager.

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
  read with: read_transcript(transcript_ref="job:job_...")
  ```

  The top-level `job_id:` is the literal `caller` — the watch's source
  is a session, not a job. The concrete job id lives in the `event:`
  block (`watchEventWatchedIdentity`, `agent/job_watch.go:2428`
  returns the session alias for a session-source watch) and again in
  the trailing `read with:` line.
- **The observer CAN read the worker's job output, through exactly one
  call.** The delivery minted the grant from the terminal payload it
  carried, so `read_transcript(transcript_ref="job:<the worker's
  job_id>")` from the observer returns an envelope whose `content` is a
  `# Delegate Job job_...` heading, `- status: completed`,
  `- total_bytes:`, and the worker's `HANDOFF_WORKER_RESULT
  artifact=summary status=ready` report in a fenced block. That is what
  the packager's `artifact=` field is read from.
- The observer packages the handoff from the frame's event fields plus
  that read, and reports it through one terminal
  `communicate(end_turn=true)`, which reaches the parent as an
  `Observer callback:` block.
- **Nothing else about that job opens up.** The observer's own
  `job_status` call on the same job_id fails — visible in its transcript
  — and the failure now names the sanctioned read:
  `job "job_..." belongs to another session; read its output with
  read_transcript(transcript_ref="job:job_...")`. Status stays denied
  because a delegate job's status projects its SESSION `transcript_ref`,
  and session refs are not access-controlled — answering would turn a
  one-job output grant into full read access to the child's
  conversation. The grant also carries no `job_list` visibility and no
  `job_stop`.

  Falsification: `job_status` SUCCEEDS on the worker's job, or its
  failure text is still the plain
  `job "job_..." not found — use job_list to see this session's jobs`
  (the grant never minted, so the read above cannot have worked either).
- **The observer's OWN callback jobs mint nothing.** A `source:"parent"`
  watch on `job.notification` also fires when the observer's own resumed
  callback job completes. That delivery's frame carries NO `read with:`
  line: a finished job whose delegate is the receiver is skipped at the
  mint, so the grant table never gains a row an observer holds on
  itself. Falsification: a `read with:` line naming the observer's own
  job id.
- The tree shows both a worker delegate and an observer delegate.
- The watch condition is `events: [job.notification]`.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:40
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count communicate
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count read_transcript
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count job_status
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
- The frame reports `output_bytes`, not the output. The observer needs
  the trailing `read with:` call to get the worker's actual text, and
  that call is the only one the grant answers.
- The grant is terminal-only by construction: it is derived from the
  finished-job payload the runtime built, never from a job id the model
  supplied. A frame with no `read with:` line grants nothing, and
  reading a job id out of the frame's prose does not conjure access.
- The parent can clear a watch the observer created: a parent-source
  watch is installed into the parent's own job manager
  (`parentInstallWatch`, `agent/session_tools_jobs.go:218`).
