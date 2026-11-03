# Effort level for a session that isn't running

## Intent

A session that has exited (its daemon is gone, the web shows it from saved state) lets you pick its next model, because `pastThreadCapabilities` advertises `ChangeModel` and the hub resumes the daemon behind `thread/model/set`. It offers no way to pick the next reasoning-effort level. The effort fields on the wire `EvenerThread` exist and the web already renders a control from them, so a cold session just needs the hub to tell the truth about them.

## Why

`thread/reasoning-effort/set` already persists into the session's saved `meta.json` (`Config.ReasoningEffort`, written by `agent/session.go`'s `Session.SetReasoningEffort`), and the live daemon's envelope already publishes `ReasoningEffort`, `ReasoningEffortLevels`, `SupportsReasoning` from the profile. The cold path (`pastEntryThreadForList`) stamps none of them, so the web's StatusRow control has nothing to render and the RPC fails on a cold session because no daemon is attached to answer it. Same capability asymmetry as the model switch before `setThreadModelWithResume` existed.

## Goals

- A cold session's web footer shows the effort control with the session's current level and the model's ladder, next to the model picker.
- Picking a level resumes the session's daemon behind the call, exactly like a model switch does, and the choice persists to `meta.json` so resume keeps it.
- A model the registry can't resolve still gets a fallback ladder (the same one the spawn pane uses), so the control renders rather than hiding.

## Non-goals

- No new wire method or protocol change; `thread/reasoning-effort/set` already exists.
- No client-side capability gate for effort: the daemon answers for itself (same as ModelSwitch's reasoning about `changeModel`).
- No change to the live-session path; it already works.

## Approaches considered

1. **Hub stamps cold reads from the registry (chosen).** `pastEntryThreadForList` resolves the saved `ProfileID`/`Model` at facts depth through `cfg.Registry.ResolveInstanceModelFacts` — the same route `costFor` already uses to price a past session — and stamps the three fields. Current level comes from `entry.Meta.Config.ReasoningEffort`. The hub's effort-set handler gets the resume-behind-the-call treatment that `setThreadModelWithResume` gives the model switch.
2. Frontend-only: the web resolves the ladder itself from `model/list` like the spawn pane does. Rejected: it duplicates a resolution the hub already performs and renders from catalog truth instead of the session's own recorded state.
3. New persisted snapshot field outside `Config`. Rejected: `Config.ReasoningEffort` already persists and resume already reads it.

## Decisions left to the builder

- Exact placement of the registry stamp within `pastEntryThreadForList` (single-thread read path only, same place the cost stamp runs, so the list sweep pays nothing extra).
- Whether the cold effort-set handler reuses `setThreadModelWithResume`'s helper directly or gets a sibling; prefer reuse.
