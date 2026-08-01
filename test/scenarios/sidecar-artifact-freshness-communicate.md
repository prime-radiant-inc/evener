# sidecar-artifact-freshness-communicate: artifact freshness sidecar catches a missing final draft

**What this covers**: the quality/artifact observer use case where a
sidecar checks whether a referenced final artifact exists or is stale.
The trigger is a caller `communicate` frame that carries the artifact
path. Driving mechanism: `delegate(watch_parent:true)` +
observer-installed `job_watch(source:"parent")` + the observer's
terminal `communicate(end_turn:true)` callback — see
`docs/job-control.md` "Observer and sidecar composition" and the
reference card `job-watch-observer-snide-thread.md`.

## Pre-state

- Fresh hub and daemon binaries.
- `tmpdir=$(mktemp -d -t serf-e2e-artifact-freshness-XXXXX)`.
- Do not create `$tmpdir/missing-report.md`.
- Use `kimi/kimi-for-coding` for Kimi fluency validation.

## Steps

1. Spawn a parent session with `working_dir=$tmpdir`.
2. Prompt:

   > Run the artifact freshness sidecar scenario.
   > 1. Call `delegate` with `watch_parent` true, `max_wait_ms` 120000,
   >    and this exact task: "You are ARTIFACT_FRESHNESS. First: call
   >    job_watch with operation 'create', source 'parent', events
   >    ['communicate']. Then communicate exactly ARTIFACT_READY and
   >    finish. When later resumed with a message containing 'Watch
   >    frame' whose event message contains FINAL_DRAFT, read the
   >    delivery_id and the artifact path from the frame. If the path
   >    contains missing-report.md, finish with communicate end_turn
   >    true, message exactly ARTIFACT_ALERT delivery=<delivery_id>
   >    path=missing-report.md status=missing. For nonmatching frames,
   >    finish with communicate end_turn true and message exactly
   >    ARTIFACT_IGNORED. Use no other tools."
   > 2. After the delegate result reports `watching: true`, capture the
   >    watch_id from its `watches` entry, then communicate exactly
   >    FINAL_DRAFT artifact=missing-report.md.
   > 3. When the ARTIFACT_ALERT observer callback arrives, call
   >    `job_watch` with operation "clear" and that watch_id, then
   >    communicate exactly SCENARIO_DONE artifact-freshness.

## Expected

- The readiness delegate result reports `watching: true` and lists the
  observer's watch under `watches` — the OBSERVER owns the watch.
- The watch uses `events: [communicate]` on source `parent`;
  `assistant.message` is not a public watch event.
- The observer emits `ARTIFACT_ALERT` as its terminal
  `communicate(end_turn=true)` — the parent sees it as an `Observer
  callback:` block — without trying to edit or create the missing
  artifact. The artifact path it reports comes from the frame's
  `event: message:` text.
- Nonmatching frames, if any, are answered with a bare
  `ARTIFACT_IGNORED` callback and no other tools.
- The watch is cleared and has no dropped deliveries.

## Doctor audit

```bash
go run ./cmd/serf-doctor watches "$SID"
go run ./cmd/serf-doctor tree "$SID" --observers
go run ./cmd/serf-doctor transcript "$SID" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --format outline --range last:30
go run ./cmd/serf-doctor transcript "$OBSERVER_REF" --count job_list
```

## Sharp edges

- This intentionally checks observation/reporting, not artifact repair.
  A sidecar that starts fixing the file is out of scope. A delegate
  runs in the parent's working directory by default, so it CAN reach
  `missing-report.md` — the assertion is that it reports instead.
- The observer must install its watch BEFORE communicating readiness,
  or the readiness result cannot report `watching: true`.
- The alert and the record collapse into one call: the observer's
  terminal `communicate(end_turn=true)` IS the callback
  (`docs/job-control.md` "`job_watch`" "That terminal communicate is
  the callback to the parent").
