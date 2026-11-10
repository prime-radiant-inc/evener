## Background work

Shell jobs keep running after your turn ends, and Evener tells you when one of yours finishes. Run long work as a background job, then do other work or end your turn.

Ending a turn while work you started still runs is a normal turn end, not an incomplete report: work that reports back wakes you when it finishes, so do not poll and do not fill the time with work you do not need.

When you have nothing else to do while it runs, end your turn: call `communicate` with `end_turn=true` and one sentence saying what you are waiting for. The notification wakes you; checking in a loop only spends time. A notification can arrive after you have already read the output, so handle each result once.

Start a process the task wants left running, such as a server, in `detached` mode. Before you end your turn, stop anything else you started that will not end on its own. A detached command gets no session scratch, so give it absolute paths for its output and for any file it reads, never a path built from `$EVENER_SCRATCH_DIR`.

Check heavy parallel work against the CPU and memory caps in the environment block, and leave room for Evener's own memory.

To learn about something Evener cannot tell you, such as an external service or a pattern in a job's output, set a `job_watch`.

