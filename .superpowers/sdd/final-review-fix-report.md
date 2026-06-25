# Final Review Fix Report: Job Notification Renderer

Status: DONE

Commit hash(es):
- 5ba29c1e52168ef12e841cd550751434053adeb9

## What changed

- `cmd/serf-hub/assets/renderer-format.js`
  - Fixed `notificationTone()` so outer job notification failure/error signals take precedence over parsed `communicate.status` success values.
  - Failure is now detected from outer `attrs.status`, outer `attrs.event`, or nonzero `exit_code` before considering `communicate.status` for success.
  - Updated the stale `classifySteering` comment from a temporary minimal notification card to a structured notification card.

- `cmd/serf-hub/assets/renderer.js`
  - Updated notification metadata rendering for numeric `output_bytes` to use the existing `formatBytes()` helper.
  - Preserved the fallback for unexpected nonnumeric `output_bytes` values as `<value> B`.

- `cmd/serf-hub/jstest/test-renderer-notifications.js`
  - Added a regression scenario for `<job-notification status="failed">` with excerpt JSON `{"data":{"status":"DONE"}}`, asserting it renders `.notification-card-error` while still displaying the communicate status.
  - Added an assertion that `.notification-card-raw pre` contains the exact original raw notification block.
  - Updated the existing shell failure byte assertion to match the existing `formatBytes()` output (`128 bytes`).

## Exact commands and output

### Focused notification renderer test

```bash
node cmd/serf-hub/jstest/test-renderer-notifications.js
```

Output:

```text
PASS — delegate completion notification parses
PASS — raw notification remains inspectable
PASS — watch notification parses as warning with non-json excerpt
PASS — watch notification renders minimal warning card
PASS — watch-send notification parses concerns and warning tone
PASS — watch-send notification renders minimal warning card
PASS — observer callback coerces success tone to warning
PASS — observer callback renders minimal warning card
PASS — malformed excerpt remains raw inspectable text
PASS — malformed excerpt is not injected as HTML
PASS — nonzero exit code gives error tone
PASS — nonzero exit code renders minimal error card
PASS — outer failure overrides successful communicate status
PASS — shell failure notification shows failure metadata
PASS — watch event notification renders as watch card
PASS — watch-send notification renders delivery metadata
PASS — observer callback renders in notification family
PASS — malformed notification falls back with raw evidence
PASS — notification text is escaped
PASS: notification renderer assertions
[exit 0]
```

### Advanced renderer regression test

```bash
node cmd/serf-hub/jstest/test-renderer-advanced.js
```

Output:

```text
PASS — system transcript blocks
PASS — slim system line
PASS — system status preferences reveal saved transcript statuses
PASS — append 3 tasks
PASS — multi-update in one call (descriptions seeded via full-list)
PASS — full-list steering renders as pointer
PASS — loop steering still renders
PASS — view action suppressed
PASS — same-verb runs collapse
PASS — cancelled tasks get distinct verb
PASS — full-list parses descriptions ending in [TBD]
PASS — full-list strips [high] reasoning-effort suffix
PASS — full-list strips [minimal]/[max] reasoning-effort suffixes
PASS — task-nudge steering suppressed
PASS — auto-advance steering becomes 'now on X'
PASS — update with no seeded description falls back to #N
PASS — active status avoids stale idle send capability
[exit 0]
```

### Hub package test

```bash
go test ./cmd/serf-hub -count=1
```

Output:

```text
ok  	primeradiant.com/serf/cmd/serf-hub	4.583s
[exit 0]
```

### Required diff whitespace check

```bash
git diff --check -- cmd/serf-hub/assets/renderer-format.js cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-notifications.js
```

Output:

```text
[exit 0]
```

### Commit

```bash
git add cmd/serf-hub/assets/renderer-format.js cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-notifications.js && git commit -m "fix(web): respect job notification failure tone" && git rev-parse HEAD
```

Output:

```text
[main 5ba29c1e] fix(web): respect job notification failure tone
 3 files changed, 30 insertions(+), 5 deletions(-)
5ba29c1e52168ef12e841cd550751434053adeb9
[exit 0]
```

## Concerns

None.
