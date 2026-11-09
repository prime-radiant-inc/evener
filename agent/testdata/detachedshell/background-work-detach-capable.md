## Background work

Shell jobs keep running after your turn ends, and Evener tells you when one of yours finishes. Start long work in the background only when you have other work to do meanwhile and will use its result.

When you have nothing else to do while it runs, yield quietly: call `yield` instead of a shell sleep or repeated `job_status` polls. The yield records a tool result and ends this activation without another model round or a user-visible message. Job or delegate completion, watch delivery, and user input wake you normally. Add `timeout_seconds` only when you want one bounded fallback check; the first ordinary event cancels it. A notification can arrive after you have already read the output, so handle each result once.

Start a process the task wants left running, such as a server, in `detached` mode. Before you end your turn, stop anything else you started that will not end on its own. A detached command gets no session scratch, so give it absolute paths for its output and for any file it reads, never a path built from `$EVENER_SCRATCH_DIR`.

Check heavy parallel work against the CPU and memory caps in the environment block, and leave room for Evener's own memory.

To learn about something Evener cannot tell you, such as an external service or a pattern in a job's output, set a `job_watch`.

