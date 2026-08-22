# Compact round — glm52-a (lunaroute/glm-5.2-vision, dense pattern surfaces)

> **Provenance note:** this slot's delegate exhausted its tool-round budget twice
> (a model-level retry loop on tool-call formatting) after completing the three
> mockups. This README was written by the orchestrating session from the mockups
> themselves; the mockups are the delegate's own work.

All three are compact realtime companions rendered beside a dimmed mock
transcript, each embedding a synthetic event stream (turns, tool calls,
delegates, jobs, watches, compaction folds) with transport controls in page
chrome. Every user_input/steering prompt is a permanent marker, clickable to
jump the mock transcript to that exact step (never aggregated away).

## idea-1 — Compact Event Tape Rail (vertical rail, ≤160px wide × full height)

- **Question:** what pattern is forming right now — burst, stall, retry loop,
  runaway — and is that normal for this session?
- **Encoding:** the full session compressed into a vertical glyph column; each
  turn is a ~12px-tall cell whose glyph/color encodes kind, intensity, and
  anomaly state; delegates and jobs read as bracketed sub-runs; compaction
  folds as divider bands. Live cell glows at the head.
- **Update model:** cells append at the head as turns stream in; older cells
  compress so the whole session-so-far stays in the rail.
- **Scaling:** cell height shrinks with turn count; prompt markers and anomaly
  glyphs stay distinct at 250+ turns.
- **Prompt jumps:** permanent prompt markers in the rail; click → mock
  transcript scrolls to that exact user/steering prompt.

## idea-2 — Compact Tool-Mix Timeline (horizontal strip, ≤100px tall × full width)

- **Question:** how is the tool mix shifting right now, which tools dominate
  this phase, and where is the error episode?
- **Encoding:** the round-1 heatmap compressed to a barcode strip: rows = tool
  types, columns = time buckets, cell intensity = call count; the active bucket
  pulses; compaction boundaries render as vertical bands.
- **Update model:** new time-bucket columns stream in from the right; the strip
  renormalizes as the session grows.
- **Scaling:** fixed bucket×tool-type grid — cell size changes, layout doesn't,
  at any session length.
- **Prompt jumps:** prompt events marked on the strip; click → transcript jump
  to that prompt.

## idea-3 — Compact Delegate Cost Matrix (corner badge, icicle tree)

- **Question:** which branch of the delegation tree owns the session's tokens,
  wall-clock, and tool calls — right now?
- **Encoding:** the 3-level delegation icicle grows live as turns stream in —
  node width = turn span, depth = delegation level; stacked micro-ribbons per
  node show tool-call density (green), token cost (blue), wall-clock duration
  (violet); a red ribbon marks the error/retry node; active nodes pulse.
- **Update model:** root bar grows rightward; child nodes spawn in live; error
  ribbons appear at the instant a failure fires.
- **Scaling:** one node per actor — bounded tree regardless of turn volume.
- **Prompt jumps:** prompt markers on the owning nodes; click → transcript
  jump.
