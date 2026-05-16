# To-Ask-Jesse

Open design questions where the right call isn't obvious. Add new items at the
bottom with date + context.

## 2026-05-16

### Credentials display when both env and file are set
The credentials UI shows either "Configured via stored API key" or "Configured via environment variable" — mutually exclusive in the display. But env takes precedence even when a file key exists. Should the row show a layered state like "Stored key shadowed by env var"? Or hide the file key entirely when env is set?

### `/settings/project` without `?cwd=`
Visiting `/settings/project` with no `cwd` query param shows "No project selected." Should it instead:
- Redirect to the most-recently-active project
- Show a picker listing known projects
- Stay as-is (sidebar always passes cwd, so this state should be rare)

### Codex launch tab — read-only forever?
`/settings/launch-codex` currently only displays `cfg.CodexLaunches` from hub.toml; no editing. Should we make this UI-writable to match `/settings/launch-serf` parity? Codex launches are infrequent setup, but consistency argues yes.

### TOFU re-prompt cadence for in-repo `.serf/launch.toml`
A user with branches that have different `.serf/launch.toml` content (per-feature configs) would see constant trust prompts as they switch branches. Should we remember a *set* of trusted hashes per project rather than just the latest?

### Model name validation on spawn
User reported spawning a session with `model = gpt-5.4-mini` (which doesn't exist). The spawn form doesn't enforce that the typed value comes from the suggested list. Should we validate against the provider list at submit time, or trust the user?

### Branch chip default resolving to HEAD (qa-c finding y5bt)
Leaving "(default)" branch on /new ends up running on the current git HEAD. Some users will want that; others might expect a sandbox/clean checkout. Should `(default)` be defined explicitly somewhere visible, or should we add a "clean checkout" option as a separate harness?

### Persistence of /new form state
Model chip persists across visits, working dir does not (qa-c finding ahtm). Pick a policy: either persist both via localStorage, or reset both on each /new. Need to know intended behavior.

### Stream-ended error retry UX
When the LLM stream dies (kata 910x / mggf), the session is "ended · 0 turns" with no retry affordance. Should there be a "retry this turn" button that re-issues the user message? Or a "resume" button that re-creates the daemon? Today the user has to spawn a fresh session.
