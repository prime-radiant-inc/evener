# Real-stack agent cascade guard

Run from the repository root:

```sh
make build-web
go test -tags browserguard ./cmd/evener-hub -run '^TestAgentCascadeBrowser$' -count=1 -v
```

`make test-web-browser` also runs this guard. Chrome, Node and the production
frontend build are required. Missing prerequisites fail; this test never
substitutes dev markup or a fake AppWire handler.

## Boundary and journey

Go scripts only the external LLM provider. Actual delegate tools create 51
root siblings and a six-edge chain through the daemon. The public hub returns
52 direct root delegates and the real ordered ancestry. Chrome opens the
production SPA and activates those actual delegate rows.

Go passes the authenticated fixture JSON through Node's stdin pipe, keeping
its auth URL out of process arguments and environment variables.

The guard checks:

- Two readable columns and five 52px ancestor spines at depth six, a 400px
  parent and a leaf with a 440px minimum.
- Compact ancestor status geometry with exact hover and screen-reader text,
  including a real running-to-idle transition and a still spinner under reduced
  motion, without changing the selected branch, focus or column geometry.
- Narrow overflow, selected-leaf visibility, independent transcript scrolling,
  parent selection, ancestor Tasks, one Escape and native keyboard branching.
- Real cursor paging past the default 50 rows and two actual socket reconnects,
  additive membership, retained page extent and closed-peek demand release.
- Original panel identity, draft, a catalog skill and exact processed PNG bytes.
- Native canvas completion held across promotion, both successful settlement
  and explicit failure, while preserving a newer source draft.
- Same-spelling project command, skill and inert prose with exact UTF-16 atom
  offsets and persisted metadata after detached image failure and Return.
  Two retained PNGs surround the atoms, then a held native IndexedDB receipt
  strips each submitted marker independently from a newer detached draft.
  Actual command expansion uses empty arguments, and the provider and public
  transcript independently verify the original mixed input arrives once.
- An actual committed IndexedDB transaction with its application acknowledgement
  held, and an actual held provider call with queued input. Promotion and Return
  retain the original recipient and mutation IDs; the provider and public
  transcript independently verify each submitted input arrives once.
- A real page reload restoring exact selected ref, six saved edges and Return
  descriptor, while an independent pane keeps its identity, grid and draft.
- Actual leaf ancestry replies held at native WebSocket delivery, then released
  unchanged, with exact focus retained and no column style mutations.
- A phone-width saved cascade showing only its selected read-only transcript,
  a visible Return restoring source work, and ordinary Agents transcript entry.

Provider holds use filesystem-backed control acknowledgements. Browser holds
wrap only native completion delivery and return the actual event or encoded
blob. No test seeds a frontend store, manufactures a delegate row, replaces an
encoder or adds a production scheduling hook. Context and runtime changes must
preserve focus and geometry. Unexpected browser errors or warnings fail.

The independent Go activity tests cover real archive decisions, nested socket
membership, runtime release and an initially empty incomplete retained jobs page:

```sh
go test ./agent -run '^TestSessionActivityRealDelegateTree$' -count=1
go test ./cmd/evener-hub -run '^TestSessionActivity' -count=1
```

## Evidence and retention

Go owns the real provider, hub and daemon lifecycles. The driver emits
condition-backed milestones in `milestones.jsonl` and receives controls through
`control.jsonl`. DOM, PNG, measured geometry, console events and actual RPC frames
are kept on failure. Set `CASCADEGUARD_KEEP_ARTIFACTS=1` to retain a passing run;
the Go output names its private evidence directory.

The authenticated fixture URL and raw RPC artifacts are private test inputs.
Keep token values out of reports. Copy retained evidence before inspecting it.
The shared hub fixture redacts auth URLs in failure diagnostics while retaining
raw private logs for daemon cleanup.

PNG preservation belongs to the original panel lifetime. Layout reload tests
must not expect memory-only image bytes to survive a page reload or put them in
layout JSON/localStorage. See the [product contract](../../../../../docs/product/session-activity.md#automatic-agent-cascade)
and [approved spec](../../../../../docs/superpowers/specs/2026-10-01-automatic-agent-cascade-design.md).
