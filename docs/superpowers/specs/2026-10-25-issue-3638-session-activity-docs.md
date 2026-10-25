# Document the pulse-meter read and its catalog entry

Issue: <https://github.com/prime-radiant-inc/evener/issues/3638>

## What the issue asked for

`evener/activity/read` is the hub's pulse-meter read. The issue records two
documentation gaps: the generated AppWire catalog gives `ActivityReadResponse` a
section but not `SessionActivity`, and no page under `docs/product` describes the
read. It asks for a catalog section and one product-doc description linked from
`docs/product/session-activity.md`.

## Catalog finding: already present, not deliberately omitted

The catalog is not missing `SessionActivity` on current main.

- `ActivityReadResponse.Sessions` is `[]SessionActivity`
  (`appwire/types.go:901-903`).
- `internal/appwiredoc/main.go` registers every JSON-visible nested type of a
  registered root (`registerNestedTypes`, `internal/appwiredoc/main.go:194-215`).
  `SessionActivity` is in package `appwire`, so it is registered, sorted and
  rendered like any other type.
- The committed catalog already carries `### SessionActivity` with `ref`,
  `minutes`, `runningSubagents`, `quietForMs`, `latestIntent` and `lastMovedAt`
  (`docs/appwire-protocol.md:3328-3337`), and `ActivityReadResponse.sessions`
  links to it (`docs/appwire-protocol.md:408-412`).
- The nested-type walk and the catalog section arrived together in commit
  `5883bc782b` (#4729). The generator has no omission list; its only skip rule is
  unexported fields and `json:"-"` (`fieldsOf`,
  `internal/appwiredoc/main.go:236-260`); nested types outside package `appwire`
  remain leaves rather than being walked.
- Re-running the generator from the `appwire` package directory leaves the file
  byte-identical (`cd appwire && go run
  primeradiant.com/evener/internal/appwiredoc -out ../docs/appwire-protocol.md`).
- `internal/appwiredoc/main_test.go:336-367` already pins the nested
  `SessionActivity*` resource types, `SessionDelegate` and `SessionWatch`, but
  not `SessionActivity` itself.

The issue was filed on 2026-10-01; the nested-type walk and catalog section
landed on 2026-10-17 in #4729, so its first premise was true when written and
has since been fixed incidentally.

Decision: make no generator or catalog change. Add a dedicated
`TestBuildIncludesActivityReadSessionActivity` pinning `SessionActivity` and its
six wire fields, keeping the pulse-meter element type out of the test named for
thread activity resources. The rendered catalog is guarded separately by
`make lint-generated`, which reruns the generator and diffs its outputs.

## Product documentation

Add `docs/product/pulse-meter.md`, one evergreen page describing the read and
its facts:

- Method, scope and params: `evener/activity/read` is hub-scoped, takes optional
  `refs` (at most 500), reads every live top-level session of the hub and its
  attached hosts when empty, and answers `ActivityReadResponse.sessions`.
- `SessionActivity` fields and their absence rules: `ref`, `minutes`,
  `runningSubagents` (subagents at every depth whose run is open),
  `quietForMs`, `latestIntent`, `lastMovedAt`.
- Producer: `server/activity_meter.go` keeps ten-second slots, seven one-minute
  bars oldest first, counts finished transcript items and tool output events,
  and takes root plus in-process descendants as the tree. `latestIntent` is only
  the root turn's newest tool intent, cleared when a turn begins.
- Quiet time: withheld unless the session is working and no subagent runs;
  measured from the later of when the daemon began serving the session and the
  tree's newest transcript motion; waiting on subagents is never quiet or
  stuck.
- Hub aggregation: local roster plus attached hosts, three-second per-host
  bound, relayed answers re-cut to the intent bound and invalid moved times
  dropped; an absent session keeps the client's fallback until the next read.
- Client facts and links: the phone's 10-second poll and 20-second freshness
  rule, fallback single newest-minute bar, Quiet/May be stuck labels, and the
  web's read for `lastMovedAt` when a visible pane's row carries a seen mark
  (the seen-mark rule itself stays in `docs/product/session-activity.md`).
- The ownership boundary: this read is not the `evener/thread/*` activity
  domain. Delegates, jobs and watches stay in
  `docs/product/session-activity.md`.

Link the new page from the opening section of
`docs/product/session-activity.md`, beside the existing web seen-mark
paragraph. Also add it to the S05 owning references in
`docs/product/subsystems.md` and list it in `docs/product/README.md`.

## Acceptance checks

- `go test ./internal/appwiredoc` passes, including the new
  `TestBuildIncludesActivityReadSessionActivity`.
- After `make generate`, `git diff --exit-code` on `docs/appwire-protocol.md`
  and `appwire-client/typescript/types.gen.ts` is clean.
- `make docs-check` passes, including local link and fragment validation.
- The changed Markdown keeps one H1 per page and publishes no new page (the
  `docs/product` tree is repository-only and not in `docs-site/publication.json`).

## Non-goals

- No change to `evener/activity/read`, `SessionActivity`, the hub, the daemon or
  any client behavior.
- No change to the `evener/thread/*` activity reads or their contract.
- No label changes and no merge; the calling agent owns CI and merge.
