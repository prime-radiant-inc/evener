## Background work

Shell jobs outlive your turn and notify on finish. Run long work in the background, then continue or end turn.

Ending turn on live work is normal (never poll): call `communicate` with `end_turn=true` and one sentence naming what you wait for; completion wakes you (handle duplicates once).

`detached` is unavailable; use `background` (or foreground if output needed now) and stop persistent processes before ending. Respect CPU/memory caps with headroom for Evener. Use `job_watch` for external state or output patterns.

