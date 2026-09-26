# Usability test harness

Lets an AI "participant" drive an HTML phone-app prototype the way a person
would: by looking at screenshots and tapping, swiping, long-pressing,
scrolling and typing — not by reading the DOM or calling app internals. Use
it to run a moderated-style usability test against a prototype before real
users see it: hand the participant a task prompt and the CLI below, and see
where they get stuck.

This harness only drives a prototype; it does not contain one. Point it at
whatever directory holds the prototype's `index.html`.

## How a moderator runs a session

1. Start the file server once, pointed at the prototype directory:

   ```
   node serve.mjs --dir /path/to/prototype --port 8750
   ```

2. For each participant, start one driver on its own port with its own
   output directory (drivers are independent; run as many as you like):

   ```
   node driver.mjs --port 8761 --url http://127.0.0.1:8750/ --out ./p1 \
       [--scheme light|dark] [--tasks ./tasks.json]
   ```

   `--tasks` is a JSON file (only needed if the session uses the `task`
   command):

   ```json
   [
     {
       "id": "T1",
       "prompt": "Find last month's total and tell me what it was.",
       "preset": "with-history",
       "events": [{ "name": "sync-finished", "afterActions": 3 }]
     }
   ]
   ```

   `preset` (optional) is a name the prototype's `window.__proto.reset(name)`
   understands, used to put the app into a known state before the task
   starts. `events` (optional) are moderator-scripted interrupts: after the
   participant's Nth action *within that task*, the driver calls
   `window.__proto.trigger(name)` once. Both hooks are the prototype's
   responsibility to implement; the harness just calls them.

3. Give the participant nothing but the port number, the command list below
   (or point them at this file), and which task numbers to run. They drive
   entirely through `phone.mjs`:

   ```
   node phone.mjs --port 8761 task 1
   node phone.mjs --port 8761 see
   node phone.mjs --port 8761 tap "Continue"
   ...
   node phone.mjs --port 8761 done "found the total on the summary screen"
   node phone.mjs --port 8761 finish
   ```

4. When every participant has run `finish`, collect each `--out` directory.

## Commands (`node phone.mjs --port <port> <command> ...`)

| Command | Does |
|---|---|
| `shot [label]` | Take a screenshot now; prints its path. |
| `tap "<text>" [--nth N]` | Tap the on-screen element labeled `<text>`. If several match, lists candidates and asks for `--nth`. |
| `tapxy X Y` | Touch-tap at CSS coordinates. |
| `longpress "<text>"` / `longpress --xy X Y` | Touch down, hold 650ms, release. |
| `swipe X1 Y1 X2 Y2 [--ms 300]` | Touch-drag from one point to another. |
| `swiperow "<text>" left\|right` | Swipe the row containing `<text>` left or right. |
| `scroll down\|up [pixels] [--at X Y]` | Scroll like a finger. "down" reveals content further down. |
| `type "<text>"` | Type into the focused field; fails if nothing is focused. |
| `key <Name>` | Press a key (`Enter`, `Backspace`, `Escape`, ...). |
| `back` | The iOS edge-swipe back gesture. |
| `see` | List what's on screen (role, name, position) for someone who can't judge from the screenshot alone. |
| `task <k>` | Start task `k` (1-based index or `id` from `--tasks`); prints only the prompt. |
| `done "<note>"` | End the current task, recording the participant's note. |
| `finish` | End the session: write `summary.json`, shut the driver down. |
| `health` | Check the driver is alive. |

Every action command (`tap`, `tapxy`, `longpress`, `swipe`, `swiperow`,
`scroll`, `type`, `key`, `back`) waits ~450ms for animations and saves a
screenshot automatically, printing its path — that's how the participant
"sees" the result. `tap`/`longpress`/`swiperow` only consider elements
whose bounding box is inside the current viewport, matching what a person
could actually reach without scrolling first.

`phone.mjs` always prints a short, plain-text line or two — never raw JSON —
and exits non-zero with a plain-English reason on failure (bad arguments, no
match on screen, nothing focused, or the driver not answering within 20s).

## Reading the outputs

Each participant's `--out` directory contains:

- `NNN-<label>.png` — one 393×852 screenshot per explicit `shot` and per
  successful action, in order.
- `actions.jsonl` — one JSON line per command the participant ran
  (timestamp, current task, command, args, ok/error, short result text).
  This is the full record of what they did; `summary.json` is the digest.
- `task-<id>-log.json` — for each completed task, whatever the prototype's
  own `window.__proto.log` recorded during that session (its own account of
  what happened — clicks, state changes, whatever it chooses to log).
- `summary.json` — written once, on `finish`: every task with its start/end
  time, the participant's `done` note, its action count, plus every browser
  console error and uncaught page error seen during the whole session. Check
  `consoleErrors`/`pageErrors` here first if a task went sideways — they
  often explain a "nothing happened" report.

## Self-check

`node selftest.mjs` exercises the whole pipeline (serve, driver, every
`phone.mjs` command, task scheduling with a preset and a scheduled event)
against a small fixture it writes itself, with no real prototype required.
Run it after changing anything in this directory.
