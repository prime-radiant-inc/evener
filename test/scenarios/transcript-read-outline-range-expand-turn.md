# transcript-read-outline-range-expand-turn: outline maps the shape, range reads a span, expand_turn opens one result

**What this covers**: the three-rung `read_session_transcript` ladder and
the single-turn-numbering invariant (`docs/tools/transcripts.md`,
"The one rule worth memorizing"). A session does enough work to produce
several turns and at least one large tool result. A later run maps it
with `format:"outline"`, reads a Turn **range** taken straight from the
outline, then `expand_turn`s a Turn whose result was truncated in the
condensed view. The load-bearing assertion: **the Turn numbers the
outline prints are exactly the numbers `range` and `expand_turn` accept**
— no second index, no translation.

## Pre-state

- Built serf binary (`go build -o /tmp/serf ./cmd/serf` if absent).
- Creds exported into the child env:
  ```bash
  set -a; . "$PWD/.env"; set +a
  ```
- A live model (`oai-work/gpt-5.5`).
- State persistence on (default for the CLI).

## Steps

1. Shared project dir (same `--dir` across both runs ⇒ same bucket):
   ```bash
   proj=$(mktemp -d -t serf-e2e-outline-XXXXX)
   ```

2. **Session A — produce a multi-turn session with one big result.** Ask
   for several distinct steps and a deliberately large output so at least
   one tool result exceeds the per-result line clamp (300 runes) and gets
   marked `[truncated]` in the condensed view:
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Do these steps in order, one tool call at a time: (1) create a file count.py that prints the numbers 1 through 200 each on its own line; (2) run it with python3 and let the full output through; (3) create a file hello.txt containing the word hello; (4) cat hello.txt; (5) report what you did. Do not combine steps."
   ```
   Wait for exit 0. This yields multiple ASSISTANT/tool turns, one of
   which (the 1..200 run) has a wide/long result.

3. **Session B — outline, then range, then expand.** Drive the full loop
   in one run:
   ```bash
   /tmp/serf --model oai-work/gpt-5.5 --dir "$proj" \
     "Find the earlier session in this project that ran a script printing the numbers 1 to 200 (use find_session_transcripts). Then: (1) call read_session_transcript on it with format outline and report the Turn number of the line that ran count.py and whether that line is marked [truncated]; (2) call read_session_transcript again with a range that spans a few turns around that Turn (e.g. if the outline shows it at Turn 6, use range '4-8') and confirm those turns render; (3) call read_session_transcript once more with expand_turn set to that same Turn number and report whether the count.py result now shows the full 1..200 output instead of a truncated card. Quote the Turn numbers you used at each step."
   ```

## Expected

- After step 3, session B reports:
  - An outline (`format:"outline"`) of one line per turn, each line
    **starting with its Turn number** (dot-separated segments, e.g.
    `6 · Assistant · exec_command · "run count.py" · ok · NN lines [truncated]`).
    It identifies the count.py-run turn by its Turn number N and notes
    the `[truncated]` marker (the wide result was width-clamped).
  - A range read whose window is the turns it asked for, taken from the
    outline's numbering — the same N falls inside it.
  - An `expand_turn:N` read in which the count.py result renders **in
    full** (the 1..200 lines), un-truncated — `expand_turn` is exempt
    from the per-result clamp and the conversation budget.
  - The Turn number used for `range` and `expand_turn` is the SAME number
    the outline printed. No remapping step is described.
- Falsification:
  - The Turn number from the outline does not address the same turn when
    passed to `range`/`expand_turn` (B has to translate or guess) → the
    single-turn-numbering invariant regressed (a second index leaked).
  - The count.py line is NOT marked `[truncated]` in the outline yet its
    result is too wide to fit the clamp → the outline/clamp markers are
    out of sync.
  - `expand_turn:N` still shows a truncated card (the full 1..200 output
    never appears) → `expand_turn` isn't exempting the result from the
    clamp/budget.
  - A range read returns turns outside the requested window, or the
    window is silently shifted with no marker → range honesty regressed.

## Cleanup

```bash
rm -rf "$proj"
```

## Sharp edges

- The outline is **always bounded**: over the conversation budget it
  keeps a head and tail of lines and drops the middle under an honest
  `… N turns elided …` marker. For a short session A this won't trigger;
  don't assert elision here. To exercise elision deliberately, drive a
  much longer session and read its outline with a tight `range`.
- `range` applies to **every** format including outline — a windowed
  outline (`range:"last:200"`) is how you map a huge session without
  dumping all of it. This scenario uses a small session so a default
  outline shows everything; the range step is exercised on the markdown
  read.
- `expand_turn` is **markdown-only** and names exactly one Turn. If the
  named Turn falls outside the rendered range, it is appended as a
  labeled supplemental section naming its real Turn number — still
  addressed by the same number. Don't treat an out-of-range expand as a
  failure; it's a documented affordance.
- The model decides how many tool calls to make; the exact Turn number
  for count.py will vary run to run. That's WHY B reads the outline first
  and uses the number it sees, rather than a hard-coded Turn — which is
  precisely the behavior under test. Do not hard-code a Turn number in
  the assertion.
