# worktree-create-and-orient: an agent reaches for manage_worktree and knows where it landed

**What this covers**: the `manage_worktree` tool (branch
`worktree-native-worktree-tools`) — that a model, asked to do isolated work,
(a) discovers the tool, (b) picks `operation: create`, (c) understands the
name doubles as the branch, and (d) correctly reports the worktree path +
branch from the tool result. This is the first-contact ergonomics test: does
the tool sell itself and does its result orient the agent?

This is a live end-to-end test against a real provider API (billed).

## Pre-state

- A `serf` binary from this branch: `go build -o /tmp/serf-wt ./cmd/serf`.
- A hermetic git repo with at least one commit (the working dir).
- An isolated `SERF_STATE_DIR` with `providers.toml`/`credentials.toml`/
  `auth-token` symlinked from `~/.serf` (read-only config, isolated mutable
  state). Managed worktrees then land under `$SERF_STATE_DIR/worktrees/`.
- A model string for the tier under test (e.g. `kimi/kimi-for-coding`,
  `openai/gpt-5.4-mini`, `lunaroute/glm-5.2-nvfp4`).

## Steps

1. Run serf non-interactively with a prompt that asks for isolated work
   **without naming the tool**:
   `"You need to make a risky experimental change to main.go in isolation,
   without touching the current working copy. Set up an isolated worktree for
   this, then tell me the exact path you're now working in and what branch
   you're on."`
2. Inspect the transcript for the `manage_worktree` call and its result.

## Expected

- The agent calls `manage_worktree` with `operation: "create"` and a `name`.
  **Falsify**: if it instead runs `git worktree add` via the shell, the tool
  failed to present itself — record it as an ergonomics miss (not a hard fail,
  but the signal we care about).
- The `create` result string names the path, branch, base SHA, and the
  behavioral consequence ("Subsequent tools operate inside it").
- The agent's final message reports a path under
  `$SERF_STATE_DIR/worktrees/<projectid>/<name>` and the branch it chose.
  **Falsify**: if it reports the main repo path, or the wrong branch, it did
  not understand that it entered the worktree.
- On disk: the worktree dir exists with a `.git` pointer file and a
  `.meta/<name>.json` sidecar; `git -C <mainrepo> worktree list --porcelain`
  shows it `locked` with a `serf:` reason.

## Cleanup

Remove the scratch `SERF_STATE_DIR` tree and the demo repo (unique temp paths
so reruns don't collide). No shared state touched.

## Sharp edges

- Pointing `SERF_STATE_DIR` at a bare dir loses provider config — symlink
  `providers.toml`/`credentials.toml`/`auth-token` in first, or serf reports
  "unknown instance".
- Weak models may emit extra args (e.g. a `purpose` field); the tool ignores
  unknown args, so that's not a failure — note it as a prompt-shape signal.
- Default base is `HEAD`; a model passing `base_ref: "main"` is fine as long
  as `main` exists in the fixture.
