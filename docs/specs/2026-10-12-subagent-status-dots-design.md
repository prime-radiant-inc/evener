# Subagent status dots — a short design

Date: 2026-10-12. Follow-up to 2026-10-11-delegate-latest-intent-design.md.
Jesse's rulings: the dot is quiet-encoding (alive while fresh, dim past the
quiet threshold, so the word "Quiet" retires and the intent always owns the
why-line), and the swap covers all three surfaces — the native list, the web
sidebar, and the web tree's delegate rows. Shell-job rows keep the $ glyph.

## What changes

A subagent row's leading visual becomes a status dot in the main sessions'
vocabulary, on every surface, with one state the main sessions' set does not
have: quiet.

- Fresh running (an open run whose latest activity is within the quiet
  threshold): alive dot.
- Quiet running (an open run, no activity past the threshold): the same dot,
  dimmed to quiet ink. This is the state that retires the "Quiet" word.
- Needs-you (awaiting approval or input): attention dot, regardless of
  recency.
- Failed: danger dot. Terminal-done: no dot at all, matching the main
  sessions' convention that idle and ended rows show nothing.

The why-line no longer spends itself on the quiet/working distinction. A
running row's line carries the stated intent, else "Running <command>", else
"Waiting on N subagents"; with none of those the line is empty and the dot
alone speaks. The quiet threshold stays the one the why-line used today.

## Non-goals

Shell-job rows (the $ glyph stays, so subagent-versus-job remains readable at
a glance), watch rows, the cascade spine indicators, Cadence traces, and any
wire change: every input already reaches the clients.

## The one shared rule

The dot state is computed by one shared helper in appwire-client, beside
delegateWorkText: terminal rows read their outcome (failed danger, done
none), attention phases read attention, and a running row reads its
quietForMs against the quiet threshold — fresh under it, quiet past it. The
three surfaces consume the helper and cannot drift, exactly as the intent
line cannot. The threshold itself moves to the shared client so all three
surfaces flip at the same instant.

## Surface behavior

- Web sidebar: the subagent row's ⌘ glyph becomes the dot. The dot's
  aria-label carries the state word; the row's meta keeps showing the intent
  (or status word) exactly as it does today. A failed subagent gains the red
  dot it never had: today terminal rows all read quiet ink, failed included.
- Web tree: delegate rows swap the ⌘ kind glyph for the dot. The row's
  aria-description keeps the status word, the meta keeps its segments
  (intent, tokens, quiet age), and shell rows keep $ and their status word.
- Native list: the running row's circle.fill tints quiet past the
  threshold; the failed xmark becomes the danger dot and the done checkmark
  retires (the FAILED and DONE band headers already carry those states).
  The why-line drops "Quiet <age>" and "Working"; the row's corner keeps
  its time-in-state duration.

The dot must flip at the threshold, not only on the next unrelated render.
Native already owns this wake for the quiet line (useNowPastQuiet) and keeps
it for the dot; the web tree's live tick already covers it; the sidebar,
which never timed anything before, gains the same cheapest wake: one
threshold timeout per running row, cancelled when its activity refreshes.

Consumers of the quiet words outside these rows — the tray, the board strip,
the transcript's subagent outcome line — keep their own contracts. The
change is scoped to what a subagent row itself renders.

## Recovery and preservation

The dot reads only wire-carried fields (terminal, outcome, phase,
latestActivityAt), so a reconnected or cold-restored row renders the same
dot state the live one had. No journal, wire or daemon change; restarts and
reconnects change nothing about this feature.

## Test plan

RED-first, per surface, over the shared helper's boundary (the threshold to
the second, plus each state):

- Shared helper: fresh under the threshold, quiet past it, attention phases,
  failed and done terminal states.
- Web sidebar: alive dot fresh, dim dot past the threshold (the new wake
  fires), danger dot failed, no dot done; the intent still owns the meta.
- Web tree: delegate rows carry the dot and keep their aria and meta
  contracts; shell rows keep $.
- Native: the dot tints at the threshold with the wake firing; failed shows
  the danger dot; done shows nothing; the why-line shows the intent or the
  fallbacks and no longer the quiet words. The existing tests that pin the
  "Quiet <age>" and "Working" lines are the behavior change's RED: they
  fail on the new rule and are rewritten to the dot's contract, never
  deleted.

## Documentation

session-activity.md's row-glyph vocabulary and the native quiet-row contract
gain the dot states (quiet stays truthful — it moves from the word to the
dot); the subsystem map's S02 and S03 rows gain the matching clause. Same
change as the code, evergreen wording.
