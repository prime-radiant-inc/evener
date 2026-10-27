# Session pulse meter

`evener/activity/read` is the hub's read of recent activity across its live
top-level sessions. It answers what each session's whole tree finished in the
last seven minutes, how many subagents it is running, how long it has been
quiet, what its root turn last set out to do, and when the tree last moved. The
phone Board draws its
[pulse meter](../../mobile-native/DESIGN.md#state-marks-and-the-pulse-meter)
and its `Quiet` and `May be stuck` lines from it. On a hub that tracks seen
marks, the web reads it for an open, visible session pane whose row carries a
`seen_through` mark, supplying the session's `lastMovedAt` to that mark.

This is not [session activity](session-activity.md), the scoped
`evener/thread/*` delegate, job and watch reads. It carries no resource rows
and never becomes part of navigation.

## Read and payload

The read is hub-scoped. `ActivityReadParams.refs` optionally names sessions by
the refs their Live rows carry, at most 500; an empty list reads every live
top-level session of this hub and of its attached hosts. The response is
`ActivityReadResponse.sessions`, ordered by ref.

Only a live, uncrashed top-level session whose daemon reported a sample appears.
A session whose daemon predates the meter, or whose host did not answer in time,
is absent from the response; a client keeps its fallback for that session until
the next read.

| Field | Meaning | Absent when |
| --- | --- | --- |
| `ref` | The session's navigation ref, the one its Live row carries. | Never |
| `minutes` | Seven one-minute counts over the session's whole tree, oldest first. The last minute ends when the hub last probed the daemon. | Never |
| `runningSubagents` | Subagents at every depth whose run is open, the same running count the session's Live row shows. | Never |
| `quietForMs` | How long the tree has gone without transcript motion, as of this read. | The session is not working, or any of its subagents runs |
| `latestIntent` | What the session's own root turn last set out to do, one line of at most 200 code points. | This turn stated none, or the daemon predates the field |
| `lastMovedAt` | Unix milliseconds of the tree's newest transcript motion. A client compares it with the session's `seen_through` to tell whether anything moved since the person last looked, mid-turn included. | The tree has not moved since its daemon began serving it, or an older hub or daemon answers |

## What the meter counts

The daemon's [`activityMeter`](../../server/activity_meter.go) keeps ten-second
slots, six to a bar. That makes seven trailing one-minute bars, not clock-aligned
minutes: the newest bar ends when the hub last probed the daemon.
The root session and its in-process descendants all feed the meter, so a root's
bars cover its whole tree.

A finished user input, finished assistant text, finished tool call, delivered
`communicate`, and every tool output event each count toward the current bar. A
turn or item starting and a message or reasoning summary streaming prove the
tree is moving without finishing anything, so they only advance the quiet
clock. Session lifecycle, environment, retry, task, goal, note, job, delegate
and notice events move neither: a retry loop that makes no progress reads as
quiet rather than busy.

`latestIntent` is the one fact that is not tree-wide. It is the newest tool-call
intent the meter's own root session stated, because the row names the root; a
subagent's tool calls never put their words there. A turn beginning clears it.
A tool call that states no intent leaves the previous line standing rather than
blanking the row mid-turn.

## Quiet time

`quietForMs` measures from the tree's newest transcript motion, or from the
moment the daemon began serving the session when nothing has moved yet. It is
present only while the session is working and none of its subagents runs. An
agent waiting on subagents is never quiet or stuck: a subagent inside one long
model call can emit nothing for minutes without the tree stalling.

`lastMovedAt` counts only real motion, not the start of serving. A daemon
restart is therefore not news to a client comparing the value with what the
person last saw.

## Hub aggregation

The hub combines its own live roster with the attached hosts' answers. Each
attached host gets its own three-second budget, queried in parallel; an
unattached host is skipped without a call, and a host that fails or times out
contributes nothing to this read. A host's answer is relayed rather than
trusted: `latestIntent` is re-cut to the wire's 200-code-point bound, and a
`lastMovedAt` that is negative or too far ahead of this hub's clock is dropped
while the row is kept.

## Clients

The shared client decodes the read in
[`sessionActivity.ts`](../../appwire-client/typescript/sessionActivity.ts). It
validates each entry key by key and drops only a malformed entry, so one bad
row never blanks every meter; a response that is not a session list throws, and
a caller keeps the meters it already drew. The same module owns the shared
`Quiet` and `May be stuck` thresholds.

On the phone, [`ActivityPoll`](../../mobile-native/src/board/activityPoll.ts)
polls every ten seconds while the Board or a session screen is in front. The
Board asks with no refs; a session screen asks for its one ref. A read older
than two poll intervals is treated as no read at all, so a hub that reports
ready while silently dropping reads cannot make a session look quiet or stuck
from staleness alone. A working row with no read draws a single bar in its
newest minute instead of ticking toward a false `stuck`. A method-not-found
answer means the hub predates the read: polling stops for good and the existing
fallback stays.

The Board draws seven bars on one fixed log scale, full at 64 events per
minute, with a baseline that always shows, older bars fading, and gray while the
connection is down. A working session reads `Quiet` after three minutes without
motion and `May be stuck` after ten, when the meter goes flat and amber and the
row rises to the top of Working, unless the session waits on subagents. The
board row's why line puts a subagent wait first, then `Quiet` or `May be
stuck`, then `latestIntent`, then the running job; the intent outranks the job
because it says what the job is for.

On the web,
[`useMarkSessionSeenOnOpen`](../../cmd/evener-hub/frontend/src/panes/session/markSeen.ts)
reads the session's `lastMovedAt` for an open, visible pane whose row carries a
`seen_through` mark. The marking rule, its visibility re-reads and its
fallbacks belong to [session activity](session-activity.md).

## Contracts

- [AppWire types](../../appwire/types_session.go): `ActivityReadParams`,
  `ActivityReadResponse`, `SessionActivity`. The
  [generated catalog](../appwire-protocol.md#activityreadresponse) carries the
  fields, with [`SessionActivity`](../appwire-protocol.md#sessionactivity) as
  its own section.
- [Daemon meter](../../server/activity_meter.go): slots, bars, counting,
  quiet clock and root intent.
- [Hub read](../../cmd/evener-hub/app_activity.go): local roster, quiet time and
  host fan-out.
- [Remote relay](../../cmd/evener-hub/internal/appsource/remote_hub_source.go):
  attached-host answer forwarding.
- [Shared decoder](../../appwire-client/typescript/sessionActivity.ts): row
  validation and the shared `Quiet` / `May be stuck` thresholds.
- [Phone poll](../../mobile-native/src/board/activityPoll.ts) and
  [meter geometry](../../mobile-native/src/board/pulse.ts).
- [Native design](../../mobile-native/DESIGN.md#state-marks-and-the-pulse-meter):
  the Board's presentation.
- [Web seen mark](../../cmd/evener-hub/frontend/src/panes/session/markSeen.ts).
