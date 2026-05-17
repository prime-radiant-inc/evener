# To-Ask-Jesse

Open design questions where the right call isn't obvious. Add new items at the
bottom with date + context.

## 2026-05-16 — Answered 2026-05-17

### w7j9 BLOCKER — model filtering / Responses API compatibility
**Answer**: Web-search for current correct handling. Then fall back to /v1/chat/completions for OpenAI models when /v1/responses fails. Never silent fail.

### ahtm — /new form state persistence
**Answer**: Persist both model and working dir (current model behavior, extend to working dir).

### y5bt — branch chip `(default)` meaning
**Answer**: Show the resolved branch name in the chip before spawn.

### mggf — stream-ended recovery UX
**Answer**: Retry first, Resume as fallback. Also: serf should have exponential backoff retries internally — copy what Codex does in `inspo/codex`.

### Credentials display when both env and file are set
**Answer**: Show both with priority indicator (effective env, file shadowed).

### `/settings/project` without `?cwd=`
**Answer**: Keep the 'No project selected' state but add a project picker inline so the user can choose without leaving the page.

### Codex launch tab parity
**Answer**: Make it writable, full parity with `/settings/launch-serf`.

### TOFU re-prompt cadence
**Answer**: Remember a SET of trusted hashes per project so branch-switching with different `.serf/launch.toml` content doesn't constantly re-prompt.

### Model name validation on spawn
**Answer**: Don't pre-validate or filter the picker. Trust the user's typed value. BUT failures must NEVER be silent — every failure surfaces with a clear error.

## 2026-05-17 — Open

### Turn count semantics for failed turns (kata k5t4)
For a turn that failed before any assistant response (e.g. stream-ended): should the turn count show 1 (user messages sent) or 0 (completed exchanges)?
Today live shows 1, ended shows 0. Pick one definition and apply consistently across live + persisted/past-index paths.
