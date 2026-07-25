# spawn-empty-prompt-starts-dormant: a blank prompt starts a dormant session

**What this covers**: kata `ytpa`. The spawn form's prompt placeholder
reads `What should the agent work on? Leave blank to start it dormant.`
Clicking Start with the prompt empty must honour that promise: create
the session, start no turn, and open it.

This scenario **replaces** `spawn-empty-prompt-blocked.md`, which
asserted the opposite. That guard (kata `xj9j`, commit `7743e7f`) was
defensive cover for an *accidental* empty submit — pressing Enter in
the model-picker search bubbled into the form's implicit submit — and
that root cause was fixed separately as kata `t13x`. The React spawn
pane has no `<form>` and no implicit submit, so the accident cannot
recur, while the guard went on contradicting the placeholder.

The daemon has always supported the dormant case: `hubThreadStart`
calls `StartTurn` only when `len(params.Input) > 0`
(`cmd/serf-hub/app_threadlifecycle.go:183`), and the client's
`buildInput` drops a blank text item (`src/panes/spawn/startThread.ts`),
so the wire carries `input: []`.

## Pre-state

- Hub running with auth set up, on an isolated `HOME` and a free port
  (never Jesse's port 9180). Assert `location.port` in the browser
  before trusting anything you see.
- The spawn pane open (the rail's `New session` button, or `/new`).
- A working directory that exists, so the cwd preflight passes without
  diverting into the "create it?" dialog.

## Steps

1. Leave the prompt textarea completely empty. Attach nothing.
2. Set the working directory to a path that exists.
3. Click `Start` (or press `⌘↵` / `Ctrl+Enter` in the textarea).
4. Watch the appwire traffic (DevTools network/WS, or the hub log) for
   the `thread/start` call and for any turn start that follows.
5. Read the session pane you land on.
6. Repeat steps 1-5 with a whitespace-only prompt (`"   \n  "`); the
   outcome must be identical.

## Expected

- No error toast. Specifically, nothing reading `Prompt is empty.`
- The page navigates to the new session, URL `/s/<ref>`.
- `thread/start` is sent with `input: []` — an empty array, not an
  array holding an empty text item.
- **No turn is started.** No turn start follows, and the daemon logs
  no turn for this session.
- The session pane shows the zero-turn empty state: `No turns yet` /
  `This session hasn't sent or received anything yet.`
- The composer is present and focusable below it, placeholder
  `Message the agent…`. Typing there and sending starts the first turn
  normally.
- Falsification: an error toast appears, the page stays put, `input`
  carries a `{type: "text", text: ""}` item, or a turn starts anyway.

## Cleanup

- Shut down the dormant session (session menu → shutdown), or leave it
  — it holds a daemon process until it is closed.

## Sharp edges

- The prompt is trimmed only for the emptiness decision. A real prompt
  with surrounding whitespace (`"   write me a haiku   "`) is still
  sent **untrimmed**; only an all-whitespace prompt takes the dormant
  path.
- The cwd guard is unaffected and still fires first: an empty or
  relative working directory aborts the submit before `thread/start`,
  with the validator's own message (`path is required`,
  `absolute path required`, `path is not a directory`).
- An attachment with no text was always allowed through and still is —
  that submit carries `input: [{type: "image", ...}]`, which is
  non-empty, so it starts a turn rather than going dormant.
- In the rail, a dormant session is currently **indistinguishable**
  from a session that ran and went idle: both are quiet rows with no
  status dot and no second line. Its title falls back to
  `session <last6>` only while it is unnamed. Tracked separately —
  do not treat that as a failure of this scenario.
