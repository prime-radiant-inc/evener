Hold for a confirmed delayed timer-arm race. The current relative-clock interface cannot safely preserve the intended deadline across all measured clock-advance interleavings. An absolute-deadline constructor is proposed and awaits Jesse's architectural decision; this head is not merge-ready.

A redundant retirement evaluation can read the old clock time, then reset an already armed deadline after virtual time advances. The reset schedules the old remaining duration from the new time and postpones retirement; this caused the daemon regression in #1936 to miss its expected expiry.

The controller now remembers the absolute deadline it armed and skips resets for the same deadline. Disarm and consumed ticks clear that value, so genuine eligibility changes and retry paths still rearm. Clock and timer interfaces are unchanged.

A gated regression forces the stale-time interleaving, waits for the timer decision, and verifies the original expiry. Reverting only the production fix produces the duplicate-arm failure. Plain, race, and tagged timer tests; the exact daemon regression; normal/tagged/Windows vet; formatting; and scoped lint pass. Independent review and local RoboRev branch review2550 found no blocking findings.

Fixes #1936. The fix covers redundant resets; it does not change the relative timing semantics of initial or genuinely changed timer arms.
